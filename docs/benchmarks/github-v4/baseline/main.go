// baseline runner: "the naive approach."
//
// Loads the pinned github-v4.sdl into Claude's system prompt and
// exposes a single execute_graphql tool. The agent receives the
// canonical task and is expected to navigate the schema itself,
// emit a query via tool-use, see the result, and produce a final
// JSON answer.
//
// Expected outcome on a real-world schema (~1.48MB SDL, ~370k
// tokens): every trial fails with an Anthropic API context-overflow
// error before reaching tool-use. That IS the data point — the
// naive baseline isn't viable on schemas this size. results.jsonl
// records the error per trial so summary aggregation can render
// it cleanly.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/felixgeelhaar/scry/docs/benchmarks/github-v4/internal/runner"
	"github.com/felixgeelhaar/scry/docs/benchmarks/github-v4/internal/task"
)

func main() {
	var (
		model    = flag.String("model", "claude-sonnet-4-6", "Claude model id")
		trials   = flag.Int("trials", 10, "number of trials to run")
		outPath  = flag.String("out", "results/baseline.jsonl", "results output path (JSONL)")
		sdlPath  = flag.String("sdl", "fixtures/github-v4.sdl", "pinned GitHub v4 SDL")
		expected = flag.String("expected", "fixtures/expected.json", "ground-truth fixture")
		maxIter  = flag.Int("max-iter", 8, "max tool-use iterations per trial")
	)
	flag.Parse()

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fail("ANTHROPIC_API_KEY required")
	}
	ghToken := os.Getenv("GITHUB_TOKEN")
	if ghToken == "" {
		fail("GITHUB_TOKEN required (execute_graphql tool calls api.github.com)")
	}

	sdl, err := os.ReadFile(*sdlPath)
	if err != nil {
		fail("read SDL: %v", err)
	}
	exp, err := task.LoadExpected(*expected)
	if err != nil {
		fail("load expected: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fail("mkdir results: %v", err)
	}
	outFile, err := os.Create(*outPath)
	if err != nil {
		fail("open output: %v", err)
	}
	defer func() { _ = outFile.Close() }()
	encoder := json.NewEncoder(outFile)

	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	systemPrompt := buildSystemPrompt(string(sdl))

	for i := 0; i < *trials; i++ {
		res := runTrial(context.Background(), &client, *model, systemPrompt, exp, ghToken, *maxIter)
		res.Trial = i
		res.Runner = "baseline"
		if err := encoder.Encode(res); err != nil {
			fail("encode trial %d: %v", i, err)
		}
		fmt.Printf("trial %2d: success=%v err=%q tokens(in=%d out=%d) %dms\n",
			i, res.Success, res.Error, res.InputTokens, res.OutputTokens, res.LatencyMs)
	}
}

// buildSystemPrompt embeds the full SDL. Production deployments do
// exactly this — paste the schema, hope it fits. The bench reveals
// whether that hope is justified.
func buildSystemPrompt(sdl string) string {
	return fmt.Sprintf(`You are an AI agent with access to GitHub's GraphQL API v4.

The complete schema for the API is provided below as SDL. Use the execute_graphql tool to run queries. Return your final answer in the exact JSON format requested by the user.

--- GITHUB v4 GRAPHQL SCHEMA (SDL) ---

%s

--- END SCHEMA ---`, sdl)
}

func runTrial(ctx context.Context, client *anthropic.Client, model, systemPrompt string, exp *task.Expected, ghToken string, maxIter int) runner.TrialResult {
	start := time.Now()
	res := runner.TrialResult{}
	defer func() { res.LatencyMs = time.Since(start).Milliseconds() }()

	messages := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(exp.Params.Prompt())),
	}
	tools := []anthropic.ToolUnionParam{executeGraphQLTool()}

	for iter := 0; iter < maxIter; iter++ {
		resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     anthropic.Model(model),
			MaxTokens: 4096,
			System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
			Messages:  messages,
			Tools:     tools,
		})
		if err != nil {
			res.Error = classifyError(err)
			return res
		}
		res.InputTokens += int(resp.Usage.InputTokens)
		res.OutputTokens += int(resp.Usage.OutputTokens)

		// Append the assistant's turn so the conversation stays
		// coherent across iterations.
		messages = append(messages, resp.ToParam())

		// If the assistant requested no tool, this is the final
		// answer — parse it.
		if resp.StopReason != anthropic.StopReasonToolUse {
			finalText := extractText(resp)
			parsed, parseErr := task.ParseResponse(finalText)
			if parseErr != nil {
				res.Error = "unparseable_response"
				res.RawResponse = finalText
				return res
			}
			ok, reasons := task.Score(parsed, exp.PullRequests)
			res.Success = ok
			res.ScoreReasons = reasons
			res.RawResponse = finalText
			return res
		}

		// Execute every tool_use block and append the results.
		toolResults, err := dispatchToolCalls(ctx, resp, ghToken)
		if err != nil {
			res.Error = fmt.Sprintf("tool_dispatch: %v", err)
			return res
		}
		messages = append(messages, anthropic.NewUserMessage(toolResults...))
	}

	res.Error = "max_iter_exceeded"
	return res
}

func executeGraphQLTool() anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{
		OfTool: &anthropic.ToolParam{
			Name:        "execute_graphql",
			Description: anthropic.String("Execute a GraphQL query against GitHub's v4 API. Returns the raw JSON response."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "The GraphQL query string.",
					},
					"variables": map[string]any{
						"type":        "object",
						"description": "Optional variables for the query.",
					},
				},
				Required: []string{"query"},
			},
		},
	}
}

func dispatchToolCalls(ctx context.Context, resp *anthropic.Message, ghToken string) ([]anthropic.ContentBlockParamUnion, error) {
	var out []anthropic.ContentBlockParamUnion
	for _, block := range resp.Content {
		tu := block.AsToolUse()
		if tu.ID == "" {
			continue
		}
		if tu.Name != "execute_graphql" {
			out = append(out, anthropic.NewToolResultBlock(tu.ID, fmt.Sprintf("unknown tool %q", tu.Name), true))
			continue
		}
		var args struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(tu.Input, &args); err != nil {
			out = append(out, anthropic.NewToolResultBlock(tu.ID, fmt.Sprintf("bad input: %v", err), true))
			continue
		}
		body, err := runner.ExecuteGraphQL(ctx, ghToken, args.Query, args.Variables)
		if err != nil {
			out = append(out, anthropic.NewToolResultBlock(tu.ID, fmt.Sprintf("graphql: %v", err), true))
			continue
		}
		out = append(out, anthropic.NewToolResultBlock(tu.ID, string(body), false))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no tool_use blocks found in assistant turn")
	}
	return out, nil
}

func extractText(resp *anthropic.Message) string {
	var b strings.Builder
	for _, block := range resp.Content {
		if t := block.AsText(); t.Text != "" {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// classifyError maps SDK errors to short, comparable labels for the
// summary table. Anthropic's API rejects requests over the model's
// context limit with a 400 carrying "prompt is too long" or similar
// — that's the headline failure mode this baseline is built to
// demonstrate.
func classifyError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "prompt is too long"),
		strings.Contains(msg, "context_length_exceeded"),
		strings.Contains(strings.ToLower(msg), "max_tokens"),
		strings.Contains(strings.ToLower(msg), "context length"):
		return "context_overflow"
	case strings.Contains(msg, "rate_limit"):
		return "rate_limited"
	default:
		return "api_error: " + msg
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
