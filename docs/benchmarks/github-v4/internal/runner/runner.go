// Package runner holds shared types + helpers both bench runners
// (baseline + scry) emit. Keeping the results schema centralized
// here lets the aggregation step in cmd/aggregate consume both
// JSONL files through a single decoder.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// TrialResult is one line of results.jsonl per trial. Fields chosen
// so the summary table can render:
//   - InputTokens / OutputTokens → cost + compression ratio
//   - LatencyMs → p50/p95 wall-clock
//   - Success → success rate
//   - Error → failure-mode breakdown (context_overflow, rate_limited,
//     unparseable_response, max_iter_exceeded, api_error: …)
//   - ScoreReasons → per-trial diagnostics
//   - RawResponse → archived for forensics on borderline trials
type TrialResult struct {
	Runner       string   `json:"runner"`  // "baseline" | "scry"
	Trial        int      `json:"trial"`   // 0-indexed within runner
	Success      bool     `json:"success"` // task.Score(...) == ok
	Error        string   `json:"error,omitempty"`
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	LatencyMs    int64    `json:"latency_ms"`
	ScoreReasons []string `json:"score_reasons,omitempty"`
	RawResponse  string   `json:"raw_response,omitempty"`
}

// ExecuteGraphQL POSTs a query to api.github.com/graphql with the
// given PAT. Returns the raw response body (the agent sees the
// upstream JSON verbatim; no scry-style envelope rewriting).
func ExecuteGraphQL(ctx context.Context, token, query string, variables map[string]any) ([]byte, error) {
	if variables == nil {
		variables = map[string]any{}
	}
	payload, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.github.com/graphql", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "scry-bench/0.6")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("http %d", resp.StatusCode)
	}
	return body, nil
}
