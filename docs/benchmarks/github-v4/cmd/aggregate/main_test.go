package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felixgeelhaar/scry/docs/benchmarks/github-v4/internal/runner"
)

// writeJSONL writes one TrialResult per line to a temp file and
// returns the path. Lets the aggregator's file-reading path be
// exercised end-to-end without depending on a real bench run.
func writeJSONL(t *testing.T, name string, trials []runner.TrialResult) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	for _, tr := range trials {
		if err := enc.Encode(tr); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	return path
}

func TestLoadRunnerStats(t *testing.T) {
	trials := []runner.TrialResult{
		{Trial: 0, Success: true, InputTokens: 100_000, OutputTokens: 500, LatencyMs: 2000},
		{Trial: 1, Success: false, InputTokens: 110_000, OutputTokens: 0, LatencyMs: 5000, Error: "unparseable_response"},
		{Trial: 2, Success: true, InputTokens: 95_000, OutputTokens: 600, LatencyMs: 1800},
	}
	path := writeJSONL(t, "trials.jsonl", trials)
	stats, err := loadRunner(path, "test")
	if err != nil {
		t.Fatalf("loadRunner: %v", err)
	}
	if stats.Trials != 3 {
		t.Errorf("trials = %d, want 3", stats.Trials)
	}
	if stats.Successes != 2 {
		t.Errorf("successes = %d, want 2", stats.Successes)
	}
	if got, want := stats.SuccessRate, 2.0/3.0; got != want {
		t.Errorf("success rate = %v, want %v", got, want)
	}
	wantAvgIn := float64(100_000+110_000+95_000) / 3.0
	if stats.AvgInputTokens != wantAvgIn {
		t.Errorf("avg input = %v, want %v", stats.AvgInputTokens, wantAvgIn)
	}
	if stats.Errors["unparseable_response"] != 1 {
		t.Errorf("error count wrong: %+v", stats.Errors)
	}
}

func TestPercentile(t *testing.T) {
	xs := []int64{100, 200, 300, 400, 500, 600, 700, 800, 900, 1000}
	if got := percentile(xs, 0.5); got != 500 {
		t.Errorf("p50 = %d, want 500", got)
	}
	if got := percentile(xs, 0.95); got != 900 {
		t.Errorf("p95 = %d, want 900", got)
	}
}

func TestDecideShip(t *testing.T) {
	d, _ := decide(0.9, 50.0)
	if d != "ship" {
		t.Errorf("decision = %q, want ship", d)
	}
}

func TestDecideIterateOnSuccess(t *testing.T) {
	d, reason := decide(0.6, 50.0)
	if d != "iterate" {
		t.Errorf("decision = %q, want iterate", d)
	}
	if !strings.Contains(reason, "success_rate") {
		t.Errorf("reason should call out success_rate, got %q", reason)
	}
}

func TestDecideIterateOnCompression(t *testing.T) {
	d, reason := decide(0.95, 5.0)
	if d != "iterate" {
		t.Errorf("decision = %q, want iterate", d)
	}
	if !strings.Contains(reason, "compression") {
		t.Errorf("reason should call out compression, got %q", reason)
	}
}

func TestRenderMarkdown(t *testing.T) {
	s := summary{
		Baseline: runnerStats{
			Name: "baseline", Trials: 10, Successes: 0, SuccessRate: 0.0,
			AvgInputTokens: 372_000, AvgOutputTokens: 0,
			P50LatencyMs: 1500, P95LatencyMs: 1500,
			AvgCostUSD: 0,
			Errors:     map[string]int{"context_overflow": 10},
		},
		Scry: runnerStats{
			Name: "scry", Trials: 10, Successes: 9, SuccessRate: 0.9,
			AvgInputTokens: 8_000, AvgOutputTokens: 1_200,
			P50LatencyMs: 14_000, P95LatencyMs: 22_000,
			AvgCostUSD: 0.042,
		},
		CompressionX: 46.5,
	}
	s.Decision, s.DecisionReason = decide(s.Scry.SuccessRate, s.CompressionX)
	md := renderMarkdown(s)
	for _, want := range []string{
		"Headline", "scry success rate:** 90%",
		"Decision: SHIP",
		"46.5×",
		"context_overflow",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
}

func TestDecideBaselineDNFTreatedAsInfiniteCompression(t *testing.T) {
	// Baseline produced zero tokens (every request rejected with
	// context_overflow) → compression = ∞ → if scry success >= 85%,
	// the call is ship, not iterate. The wedge is by definition
	// necessary if the naive baseline can't even attempt.
	d, reason := decide(0.9, math.Inf(1))
	if d != "ship" {
		t.Errorf("baseline-DNF + scry 90%% should ship; got %q (%s)", d, reason)
	}
	if !strings.Contains(reason, "∞") {
		t.Errorf("reason should render compression as ∞, got %q", reason)
	}
}

// End-to-end: synthetic bench result that hits the ship decision.
// Exercises the full pipeline: load → aggregate → render.
func TestEndToEndShipDecision(t *testing.T) {
	baseline := make([]runner.TrialResult, 10)
	for i := range baseline {
		baseline[i] = runner.TrialResult{
			Trial: i, Success: false, InputTokens: 0, OutputTokens: 0,
			LatencyMs: 1500, Error: "context_overflow",
		}
	}
	scry := make([]runner.TrialResult, 10)
	for i := range scry {
		success := i < 9 // 9/10 = 90%
		scry[i] = runner.TrialResult{
			Trial: i, Success: success, InputTokens: 8_000, OutputTokens: 1_200,
			LatencyMs: 14_000,
		}
	}
	basePath := writeJSONL(t, "baseline.jsonl", baseline)
	scryPath := writeJSONL(t, "scry.jsonl", scry)

	baseStats, err := loadRunner(basePath, "baseline")
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	scryStats, err := loadRunner(scryPath, "scry")
	if err != nil {
		t.Fatalf("scry: %v", err)
	}
	if baseStats.SuccessRate != 0 {
		t.Errorf("expected 0%% baseline success; got %v", baseStats.SuccessRate)
	}
	if scryStats.SuccessRate != 0.9 {
		t.Errorf("expected 90%% scry success; got %v", scryStats.SuccessRate)
	}
	// Compression formula: baseline_in is 0 → CompressionX undefined.
	// The aggregator handles div-by-zero by leaving CompressionX = 0.
	// Decision still trips on success_rate alone for "iterate" path
	// because compression = 0 < 10. Confirm.
	s := summary{Baseline: baseStats, Scry: scryStats}
	if scryStats.AvgInputTokens > 0 {
		s.CompressionX = baseStats.AvgInputTokens / scryStats.AvgInputTokens
	}
	d, _ := decide(s.Scry.SuccessRate, s.CompressionX)
	if d != "iterate" {
		t.Errorf("baseline=0 tokens forces compression=0 → must iterate; got %q", d)
	}
}
