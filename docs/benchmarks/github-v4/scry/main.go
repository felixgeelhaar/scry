// scry runner: search-first agent.
//
// Spawns scry as an MCP stdio subprocess pointed at GitHub's v4
// GraphQL endpoint. The agent receives the canonical task with NO
// SDL in the system prompt — only scry's tool list. Forces the
// path schema_search → schema_get (optional) → query_validate →
// query_execute.
//
// Same N=10 trials as the baseline runner, same scoring against
// fixtures/expected.json, same TrialResult shape in
// results/scry.jsonl. The delta between baseline.jsonl and
// scry.jsonl IS the v0.6 finding.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/felixgeelhaar/scry/docs/benchmarks/github-v4/internal/mcpclient"
	"github.com/felixgeelhaar/scry/docs/benchmarks/github-v4/internal/runner"
	"github.com/felixgeelhaar/scry/docs/benchmarks/github-v4/internal/task"
)

func main() {
	var (
		model    = flag.String("model", "claude-sonnet-4-6", "Claude model id")
		trials   = flag.Int("trials", 10, "number of trials to run")
		outPath  = flag.String("out", "results/scry.jsonl", "results output path (JSONL)")
		expected = flag.String("expected", "fixtures/expected.json", "ground-truth fixture")
		scryBin  = flag.String("scry-bin", "scry", "path to the scry binary (built from ./cmd/scry)")
		maxIter  = flag.Int("max-iter", 12, "max tool-use iterations per trial (scry's search-first path takes more rounds than baseline's direct-execute)")
	)
	flag.Parse()

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fail("ANTHROPIC_API_KEY required")
	}
	if os.Getenv("GITHUB_TOKEN") == "" {
		fail("GITHUB_TOKEN required (scry forwards it to GitHub via env://GITHUB_TOKEN)")
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

	for i := 0; i < *trials; i++ {
		res := runTrial(context.Background(), &client, *model, exp, *scryBin, *maxIter)
		res.Trial = i
		res.Runner = "scry"
		if err := encoder.Encode(res); err != nil {
			fail("encode trial %d: %v", i, err)
		}
		fmt.Printf("trial %2d: success=%v err=%q tokens(in=%d out=%d) %dms\n",
			i, res.Success, res.Error, res.InputTokens, res.OutputTokens, res.LatencyMs)
	}
}

// runTrial owns one scry subprocess + one Claude conversation. The
// subprocess is spawned fresh per trial so cached state (in-memory
// audit chain, gate budget) doesn't leak across trials.
func runTrial(ctx context.Context, client *anthropic.Client, model string, exp *task.Expected, scryBin string, maxIter int) runner.TrialResult {
	start := time.Now()
	res := runner.TrialResult{}
	defer func() { res.LatencyMs = time.Since(start).Milliseconds() }()

	mcpClient, kill, err := spawnScry(scryBin)
	if err != nil {
		res.Error = fmt.Sprintf("spawn_scry: %v", err)
		return res
	}
	defer kill()

	initCtx, cancelInit := context.WithTimeout(ctx, 30*time.Second)
	defer cancelInit()
	if err := mcpClient.Initialize(initCtx, "scry-bench", "0.6"); err != nil {
		res.Error = fmt.Sprintf("mcp_initialize: %v", err)
		return res
	}
	scryTools, err := mcpClient.ToolsList(initCtx)
	if err != nil {
		res.Error = fmt.Sprintf("mcp_tools_list: %v", err)
		return res
	}
	anthTools := translateTools(scryTools)

	messages := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(exp.Params.Prompt())),
	}
	systemPrompt := "You are an AI agent. Use the provided tools to answer the user. Tools give you read-only search + validate + execute access to a GraphQL upstream — call schema_search first to find the right types, then query_execute to run a query. Return the final answer in the exact JSON format requested."

	for iter := 0; iter < maxIter; iter++ {
		resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     anthropic.Model(model),
			MaxTokens: 4096,
			System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
			Messages:  messages,
			Tools:     anthTools,
		})
		if err != nil {
			res.Error = classifyError(err)
			return res
		}
		res.InputTokens += int(resp.Usage.InputTokens)
		res.OutputTokens += int(resp.Usage.OutputTokens)
		messages = append(messages, resp.ToParam())

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

		toolResults, err := dispatchScryToolCalls(ctx, mcpClient, resp)
		if err != nil {
			res.Error = fmt.Sprintf("tool_dispatch: %v", err)
			return res
		}
		messages = append(messages, anthropic.NewUserMessage(toolResults...))
	}
	res.Error = "max_iter_exceeded"
	return res
}

// spawnScry launches `scry serve --upstream <github-graphql>` as a
// stdio subprocess. The caller drives it via the returned client;
// kill cleans up. Stderr is captured and forwarded to our stderr so
// any scry-side panics + structured logs surface alongside the
// bench output.
func spawnScry(scryBin string) (*mcpclient.Client, func(), error) {
	cmd := exec.Command(scryBin, "serve",
		"--upstream", "https://api.github.com/graphql",
		"--auth", "env://GITHUB_TOKEN",
		// Skip persistent index dir — every trial gets a fresh
		// tempdir from os.TempDir() under scry's default.
	)
	// Pass env through so GITHUB_TOKEN reaches scry.
	cmd.Env = os.Environ()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start scry: %w", err)
	}

	// Forward scry's stderr to bench stderr so introspection
	// failures + auth errors are visible.
	go func() {
		s := bufio.NewScanner(stderr)
		s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for s.Scan() {
			fmt.Fprintf(os.Stderr, "[scry] %s\n", s.Text())
		}
	}()

	cli := mcpclient.New(stdout, stdin)
	kill := func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	return cli, kill, nil
}

// translateTools converts scry's MCP tool definitions into
// Anthropic ToolUnionParam values Claude can call. Both formats
// carry name + description + JSON-schema input, so this is a
// straight rename.
func translateTools(scryTools []mcpclient.Tool) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(scryTools))
	for _, t := range scryTools {
		var schema struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
			// Skip tools with malformed schemas rather than fail
			// the trial — Claude will simply not have access to
			// that tool, which is recorded honestly in the trial
			// outcome.
			fmt.Fprintf(os.Stderr, "[bench] skipping tool %q: bad schema: %v\n", t.Name, err)
			continue
		}
		out = append(out, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: anthropic.ToolInputSchemaParam{
					Properties: schema.Properties,
					Required:   schema.Required,
				},
			},
		})
	}
	return out
}

// dispatchScryToolCalls forwards Claude's tool_use blocks to scry
// via MCP tools/call and packages the results as tool_result blocks
// for the next message turn.
func dispatchScryToolCalls(ctx context.Context, cli *mcpclient.Client, resp *anthropic.Message) ([]anthropic.ContentBlockParamUnion, error) {
	var out []anthropic.ContentBlockParamUnion
	for _, block := range resp.Content {
		tu := block.AsToolUse()
		if tu.ID == "" {
			continue
		}
		var args map[string]any
		if err := json.Unmarshal(tu.Input, &args); err != nil {
			out = append(out, anthropic.NewToolResultBlock(tu.ID, fmt.Sprintf("bad input: %v", err), true))
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		body, err := cli.ToolsCall(callCtx, tu.Name, args)
		cancel()
		if err != nil {
			out = append(out, anthropic.NewToolResultBlock(tu.ID, fmt.Sprintf("scry error: %v", err), true))
			continue
		}
		out = append(out, anthropic.NewToolResultBlock(tu.ID, body, false))
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

func classifyError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "prompt is too long"),
		strings.Contains(msg, "context_length_exceeded"),
		strings.Contains(strings.ToLower(msg), "context length"):
		return "context_overflow"
	case strings.Contains(msg, "rate_limit"):
		return "rate_limited"
	default:
		return "api_error: " + msg
	}
}

// Suppress unused-import false-positive when io is only used inline
// indirectly. (kept for symmetry with baseline; remove if linter
// complains.)
var _ io.Reader

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
