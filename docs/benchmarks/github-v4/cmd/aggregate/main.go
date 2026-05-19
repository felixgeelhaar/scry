// aggregate reads baseline.jsonl + scry.jsonl, produces a comparison
// summary in both markdown (for README embedding + screenshotting)
// and JSON (for downstream charting or CI gates).
//
// The headline number is the input-token compression ratio — that's
// what determines whether scry's wedge holds on a real schema.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/felixgeelhaar/scry/docs/benchmarks/github-v4/internal/runner"
)

// Sonnet 4.6 published pricing (USD per million tokens). Updates
// here if Anthropic shifts rates — the bench's $$ column is purely
// derived from these constants.
const (
	InputPricePerMTok  = 3.0
	OutputPricePerMTok = 15.0
)

// goNoGoThresholds encodes the v0.6 spec's decision rule. Below
// either bar → v0.7 plan grounded in observed failure modes. At or
// above both → distribute.
const (
	GoSuccessRate      = 0.85 // scry success_rate >= 85%
	GoCompressionRatio = 10.0 // baseline_input / scry_input >= 10x
)

type runnerStats struct {
	Name            string  `json:"name"`
	Trials          int     `json:"trials"`
	Successes       int     `json:"successes"`
	SuccessRate     float64 `json:"success_rate"`
	AvgInputTokens  float64 `json:"avg_input_tokens"`
	AvgOutputTokens float64 `json:"avg_output_tokens"`
	P50LatencyMs    int64   `json:"p50_latency_ms"`
	P95LatencyMs    int64   `json:"p95_latency_ms"`
	AvgCostUSD      float64 `json:"avg_cost_usd"`
	// Errors counts the unique error labels and their occurrence;
	// surfaces failure-mode breakdown (context_overflow vs.
	// rate_limited vs. unparseable_response) cleanly in the
	// summary.
	Errors map[string]int `json:"errors,omitempty"`
}

type summary struct {
	Baseline       runnerStats `json:"baseline"`
	Scry           runnerStats `json:"scry"`
	CompressionX   float64     `json:"compression_ratio"` // baseline.AvgInputTokens / scry.AvgInputTokens
	CostReductionX float64     `json:"cost_reduction_x"`  // baseline.AvgCostUSD / scry.AvgCostUSD
	Decision       string      `json:"decision"`          // "ship" | "iterate"
	DecisionReason string      `json:"decision_reason"`
}

func main() {
	var (
		baselinePath = flag.String("baseline", "results/baseline.jsonl", "baseline runner JSONL output")
		scryPath     = flag.String("scry", "results/scry.jsonl", "scry runner JSONL output")
		outMd        = flag.String("out", "results/summary.md", "markdown summary path")
		outJSON      = flag.String("json", "results/summary.json", "machine-readable summary path")
	)
	flag.Parse()

	base, err := loadRunner(*baselinePath, "baseline")
	if err != nil {
		fail("load baseline: %v", err)
	}
	scry, err := loadRunner(*scryPath, "scry")
	if err != nil {
		fail("load scry: %v", err)
	}

	sum := summary{Baseline: base, Scry: scry}
	if scry.AvgInputTokens > 0 {
		sum.CompressionX = base.AvgInputTokens / scry.AvgInputTokens
	}
	if scry.AvgCostUSD > 0 {
		sum.CostReductionX = base.AvgCostUSD / scry.AvgCostUSD
	}
	// Baseline DNF (zero tokens consumed = API rejected every
	// request) means compression is effectively ∞. Surface that
	// to the decider so the ship/iterate call doesn't penalise
	// scry for the wedge being absolutely necessary.
	compressionForDecision := sum.CompressionX
	if base.AvgInputTokens == 0 && scry.AvgInputTokens > 0 {
		compressionForDecision = math.Inf(1)
	}
	sum.Decision, sum.DecisionReason = decide(scry.SuccessRate, compressionForDecision)

	mdBytes := []byte(renderMarkdown(sum))
	if err := os.WriteFile(*outMd, mdBytes, 0o644); err != nil {
		fail("write md: %v", err)
	}
	jsonBytes, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		fail("marshal json: %v", err)
	}
	if err := os.WriteFile(*outJSON, append(jsonBytes, '\n'), 0o644); err != nil {
		fail("write json: %v", err)
	}
	fmt.Println(renderMarkdown(sum))
}

// loadRunner streams a JSONL file into TrialResults and folds them
// into a runnerStats summary.
func loadRunner(path, name string) (runnerStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return runnerStats{Name: name}, err
	}
	defer func() { _ = f.Close() }()

	stats := runnerStats{Name: name, Errors: map[string]int{}}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var inputTokens, outputTokens int
	var latencies []int64
	for scanner.Scan() {
		var t runner.TrialResult
		if err := json.Unmarshal(scanner.Bytes(), &t); err != nil {
			return stats, fmt.Errorf("decode trial: %w", err)
		}
		stats.Trials++
		if t.Success {
			stats.Successes++
		}
		inputTokens += t.InputTokens
		outputTokens += t.OutputTokens
		latencies = append(latencies, t.LatencyMs)
		if t.Error != "" {
			stats.Errors[t.Error]++
		}
	}
	if err := scanner.Err(); err != nil {
		return stats, err
	}
	if stats.Trials == 0 {
		return stats, fmt.Errorf("%s: no trials in %s", name, path)
	}
	stats.SuccessRate = float64(stats.Successes) / float64(stats.Trials)
	stats.AvgInputTokens = float64(inputTokens) / float64(stats.Trials)
	stats.AvgOutputTokens = float64(outputTokens) / float64(stats.Trials)
	stats.P50LatencyMs = percentile(latencies, 0.5)
	stats.P95LatencyMs = percentile(latencies, 0.95)
	stats.AvgCostUSD = (stats.AvgInputTokens*InputPricePerMTok + stats.AvgOutputTokens*OutputPricePerMTok) / 1_000_000.0
	return stats, nil
}

func percentile(xs []int64, p float64) int64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]int64(nil), xs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

// decide encodes the v0.6 go/no-go rule. Below either threshold:
// the wedge needs more work before distribution.
//
// math.Inf(1) for compression signals "baseline never reached the
// API" — the wedge is necessary by definition; renderer prints "∞"
// instead of a number.
func decide(scrySuccess, compression float64) (string, string) {
	infCompression := math.IsInf(compression, 1)
	compressionStr := fmt.Sprintf("%.1f×", compression)
	if infCompression {
		compressionStr = "∞"
	}
	switch {
	case scrySuccess >= GoSuccessRate && compression >= GoCompressionRatio:
		return "ship", fmt.Sprintf("scry success_rate=%.0f%% ≥ %.0f%%, compression=%s ≥ %.0f×",
			scrySuccess*100, GoSuccessRate*100, compressionStr, GoCompressionRatio)
	case scrySuccess < GoSuccessRate && compression < GoCompressionRatio:
		return "iterate", fmt.Sprintf("both metrics below threshold: success_rate=%.0f%% < %.0f%%, compression=%s < %.0f×",
			scrySuccess*100, GoSuccessRate*100, compressionStr, GoCompressionRatio)
	case scrySuccess < GoSuccessRate:
		return "iterate", fmt.Sprintf("success_rate=%.0f%% below %.0f%% threshold — agent picks wrong fields too often",
			scrySuccess*100, GoSuccessRate*100)
	default:
		return "iterate", fmt.Sprintf("compression=%s below %.0f× threshold — token wedge weaker than claimed",
			compressionStr, GoCompressionRatio)
	}
}

func renderMarkdown(s summary) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# Benchmark results — full SDL vs. scry")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Generated by `make bench-summary`. Reproducible from a")
	fmt.Fprintln(&b, "clean checkout via `make bench` given valid `GITHUB_TOKEN`")
	fmt.Fprintln(&b, "and `ANTHROPIC_API_KEY`.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Headline")
	fmt.Fprintln(&b)
	// When baseline can't even submit a request (all trials fail
	// with context_overflow before consuming tokens), compression
	// ratio is undefined. Surface that explicitly rather than
	// rendering "0×" which reads as "scry is worse."
	if s.Baseline.AvgInputTokens == 0 {
		fmt.Fprintln(&b, "- **Input-token compression:** ∞ (baseline trials never reached the API — context overflow)")
		fmt.Fprintln(&b, "- **Cost reduction:** ∞ (baseline trials never billed)")
	} else {
		fmt.Fprintf(&b, "- **Input-token compression:** %.1f×\n", s.CompressionX)
		fmt.Fprintf(&b, "- **Cost reduction:** %.1f×\n", s.CostReductionX)
	}
	fmt.Fprintf(&b, "- **scry success rate:** %.0f%% (n=%d)\n", s.Scry.SuccessRate*100, s.Scry.Trials)
	fmt.Fprintf(&b, "- **Baseline success rate:** %.0f%% (n=%d)\n", s.Baseline.SuccessRate*100, s.Baseline.Trials)
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "**Decision: %s** — %s\n", strings.ToUpper(s.Decision), s.DecisionReason)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Side-by-side")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Metric | Baseline (full SDL) | scry (search-first) | Delta |")
	fmt.Fprintln(&b, "|---|---:|---:|---:|")
	fmt.Fprintf(&b, "| Trials | %d | %d | — |\n", s.Baseline.Trials, s.Scry.Trials)
	fmt.Fprintf(&b, "| Success rate | %.0f%% | %.0f%% | %+.0fpp |\n",
		s.Baseline.SuccessRate*100, s.Scry.SuccessRate*100, (s.Scry.SuccessRate-s.Baseline.SuccessRate)*100)
	compressionCell := fmt.Sprintf("%.1f× compression", s.CompressionX)
	if s.Baseline.AvgInputTokens == 0 {
		compressionCell = "∞ (baseline DNF)"
	}
	fmt.Fprintf(&b, "| Avg input tokens | %s | %s | %s |\n",
		fmtTokens(s.Baseline.AvgInputTokens), fmtTokens(s.Scry.AvgInputTokens), compressionCell)
	fmt.Fprintf(&b, "| Avg output tokens | %s | %s | — |\n",
		fmtTokens(s.Baseline.AvgOutputTokens), fmtTokens(s.Scry.AvgOutputTokens))
	fmt.Fprintf(&b, "| p50 latency | %s | %s | — |\n",
		fmtLatency(s.Baseline.P50LatencyMs), fmtLatency(s.Scry.P50LatencyMs))
	fmt.Fprintf(&b, "| p95 latency | %s | %s | — |\n",
		fmtLatency(s.Baseline.P95LatencyMs), fmtLatency(s.Scry.P95LatencyMs))
	costCell := fmt.Sprintf("%.1f× cheaper", s.CostReductionX)
	if s.Baseline.AvgCostUSD == 0 {
		costCell = "∞ (baseline DNF)"
	}
	fmt.Fprintf(&b, "| $$ per task | $%.4f | $%.4f | %s |\n",
		s.Baseline.AvgCostUSD, s.Scry.AvgCostUSD, costCell)
	fmt.Fprintln(&b)
	if len(s.Baseline.Errors)+len(s.Scry.Errors) > 0 {
		fmt.Fprintln(&b, "## Failure modes")
		fmt.Fprintln(&b)
		renderErrorBreakdown(&b, "Baseline", s.Baseline.Errors)
		renderErrorBreakdown(&b, "scry", s.Scry.Errors)
	}
	return b.String()
}

func renderErrorBreakdown(b *strings.Builder, label string, errors map[string]int) {
	if len(errors) == 0 {
		return
	}
	keys := make([]string, 0, len(errors))
	for k := range errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(b, "### %s\n\n", label)
	for _, k := range keys {
		fmt.Fprintf(b, "- `%s`: %d trial(s)\n", k, errors[k])
	}
	fmt.Fprintln(b)
}

func fmtTokens(n float64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", n/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", n/1_000)
	default:
		return fmt.Sprintf("%.0f", n)
	}
}

func fmtLatency(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
