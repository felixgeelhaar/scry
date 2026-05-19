// Package task is the shared contract between the baseline and
// scry-routed runners. Both runners hand the agent the same natural-
// language prompt and score the response against the same ground-
// truth fixture — so any delta in input tokens, output tokens, or
// success rate is attributable to the schema-access path (full SDL
// in prompt vs. scry-routed search), not to task variation.
package task

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Params pins every parameter of the canonical task. Fixed values
// here keep trial-to-trial and runner-to-runner comparisons honest.
// Re-recording fixtures with different params invalidates prior
// results — only do it via `make bench-fixtures`.
type Params struct {
	// Repo is "<owner>/<name>". Public, has many PRs by Author
	// touching File in the window.
	Repo string
	// Author is the PR author the task asks for. The agent never
	// sees this struct — it sees the rendered Prompt() — but the
	// scorer needs it to discard noise from other authors.
	Author string
	// File path, repo-relative.
	File string
	// Since, Until: fixed ISO-8601 date window (inclusive on
	// both ends). Stays fixed across runs so ground truth doesn't
	// drift.
	Since string
	Until string
}

// Default is the pinned task parameters used by `make bench`. Picked
// because:
//   - tokenops is a public felixgeelhaar repo with 97+ PRs (rich
//     enough to exercise schema navigation).
//   - internal/proxy/dashboard.html is touched by 5 PRs in window;
//     the agent has to navigate User -> PullRequest -> File links
//     plus the review system to answer correctly.
//   - The window is fully past, so ground truth doesn't shift.
var Default = Params{
	Repo:   "felixgeelhaar/tokenops",
	Author: "felixgeelhaar",
	File:   "internal/proxy/dashboard.html",
	Since:  "2026-04-15",
	Until:  "2026-05-15",
}

// Prompt is the natural-language task the agent receives. Identical
// across both runners. No SDL hints, no field names, no GraphQL
// syntax — the whole point is to measure whether the agent can find
// the right fields from a plain English description.
//
// JSON shape is specified so the scorer can parse machine-readable
// output; if the agent emits prose, the scorer counts it as a miss.
func (p Params) Prompt() string {
	return fmt.Sprintf(`List every pull request that the user %q opened in the GitHub repository %q between %s and %s (inclusive) that modified the file %q.

For each pull request, return:
  - number (integer)
  - title (string)
  - merged (boolean — true if the PR was merged, false otherwise)
  - reviewers (array of GitHub login strings — every user that left a review)

Return the final answer as a JSON array under the key "pull_requests", with one object per PR. No commentary, no markdown — just the JSON object:

{"pull_requests": [{"number": ..., "title": "...", "merged": ..., "reviewers": [...]}, ...]}`,
		p.Author, p.Repo, p.Since, p.Until, p.File)
}

// PR is one expected entry. JSON tags match the shape Prompt()
// specifies, so the ground-truth file and the agent's response
// decode through the same struct.
type PR struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	Merged    bool     `json:"merged"`
	Reviewers []string `json:"reviewers"`
}

// Expected is the deserialized fixtures/expected.json. Tracks Params
// alongside the PRs so a runner can sanity-check it's scoring
// against the right task.
type Expected struct {
	Params       Params `json:"params"`
	PullRequests []PR   `json:"pull_requests"`
}

// LoadExpected reads + decodes fixtures/expected.json.
func LoadExpected(path string) (*Expected, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var e Expected
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return &e, nil
}

// Response is what the scorer attempts to parse out of the agent's
// final assistant message. Same shape as Expected.PullRequests.
type Response struct {
	PullRequests []PR `json:"pull_requests"`
}

// Score grades a parsed agent response against ground truth.
//
// Scoring is deliberately strict:
//
//   - The set of PR numbers must match exactly (no extras, no missing).
//     This is the load-bearing dimension — if the agent finds the
//     wrong PRs, nothing else matters.
//   - Merge status for each matched PR must match.
//   - Reviewer sets for each matched PR must match (order-insensitive).
//
// Title comparison is intentionally skipped: agents that find the
// right PRs sometimes paraphrase titles, and the failure mode we
// care about is "did the agent navigate the schema correctly," not
// "did the agent verbatim-copy a string field."
//
// Returns ok=true only when every check passes. The reasons slice
// surfaces the specific mismatches so trial-level diagnostics in
// results.jsonl are actionable.
func Score(got Response, expected []PR) (ok bool, reasons []string) {
	gotNums := numbersOf(got.PullRequests)
	wantNums := numbersOf(expected)

	if !sameInts(gotNums, wantNums) {
		reasons = append(reasons,
			fmt.Sprintf("PR set mismatch: got %v want %v", gotNums, wantNums))
		// Set mismatch is fatal — skip per-PR checks, the data's
		// not aligned to even attempt them.
		return false, reasons
	}

	wantByNum := map[int]PR{}
	for _, p := range expected {
		wantByNum[p.Number] = p
	}
	for _, g := range got.PullRequests {
		w := wantByNum[g.Number]
		if g.Merged != w.Merged {
			reasons = append(reasons,
				fmt.Sprintf("PR #%d merged: got %v want %v", g.Number, g.Merged, w.Merged))
		}
		if !sameStrings(g.Reviewers, w.Reviewers) {
			reasons = append(reasons,
				fmt.Sprintf("PR #%d reviewers: got %v want %v", g.Number, sorted(g.Reviewers), sorted(w.Reviewers)))
		}
	}
	return len(reasons) == 0, reasons
}

// ParseResponse pulls the JSON object out of an agent's free-form
// assistant message. Tolerates leading/trailing markdown fences and
// chatty prefaces — agents reliably emit one of the two patterns:
//
//	{"pull_requests": [...]}
//	```json
//	{"pull_requests": [...]}
//	```
//
// Returns a zero-value Response + error if no parseable JSON object
// is present.
func ParseResponse(s string) (Response, error) {
	// Strip code fences if present.
	s = strings.ReplaceAll(s, "```json", "```")
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	// Find the outermost JSON object.
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < 0 || end <= start {
		return Response{}, fmt.Errorf("no JSON object in response")
	}
	var r Response
	if err := json.Unmarshal([]byte(s[start:end+1]), &r); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}
	return r, nil
}

func numbersOf(prs []PR) []int {
	out := make([]int, len(prs))
	for i, p := range prs {
		out[i] = p.Number
	}
	sort.Ints(out)
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := sorted(a)
	bb := sorted(b)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}
