package task

import (
	"strings"
	"testing"
)

func TestPromptIncludesParams(t *testing.T) {
	p := Default.Prompt()
	for _, want := range []string{
		Default.Repo, Default.Author, Default.File, Default.Since, Default.Until,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	// Must specify the JSON shape so the scorer can parse the
	// answer deterministically.
	if !strings.Contains(p, `"pull_requests"`) {
		t.Errorf("prompt must specify pull_requests JSON shape")
	}
}

func TestScoreExactMatch(t *testing.T) {
	expected := []PR{
		{Number: 50, Merged: true, Reviewers: []string{"copilot-pull-request-reviewer"}},
		{Number: 58, Merged: true, Reviewers: []string{"copilot-pull-request-reviewer"}},
	}
	got := Response{PullRequests: []PR{
		{Number: 58, Merged: true, Reviewers: []string{"copilot-pull-request-reviewer"}},
		{Number: 50, Merged: true, Reviewers: []string{"copilot-pull-request-reviewer"}},
	}}
	ok, reasons := Score(got, expected)
	if !ok {
		t.Errorf("expected match, got reasons: %v", reasons)
	}
}

func TestScoreMissingPR(t *testing.T) {
	expected := []PR{
		{Number: 50, Merged: true},
		{Number: 58, Merged: true},
	}
	got := Response{PullRequests: []PR{
		{Number: 50, Merged: true},
	}}
	ok, _ := Score(got, expected)
	if ok {
		t.Errorf("missing PR must score as miss")
	}
}

func TestScoreMergeStatusMismatch(t *testing.T) {
	expected := []PR{{Number: 50, Merged: true}}
	got := Response{PullRequests: []PR{{Number: 50, Merged: false}}}
	ok, reasons := Score(got, expected)
	if ok {
		t.Errorf("merge-status mismatch must score as miss")
	}
	if len(reasons) == 0 {
		t.Errorf("expected reasons populated")
	}
}

func TestScoreReviewersOrderInsensitive(t *testing.T) {
	expected := []PR{
		{Number: 50, Merged: true, Reviewers: []string{"alice", "bob"}},
	}
	got := Response{PullRequests: []PR{
		{Number: 50, Merged: true, Reviewers: []string{"bob", "alice"}},
	}}
	ok, _ := Score(got, expected)
	if !ok {
		t.Errorf("reviewer order must not affect score")
	}
}

func TestParseResponsePlain(t *testing.T) {
	r, err := ParseResponse(`{"pull_requests": [{"number": 50, "title": "x", "merged": true, "reviewers": []}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(r.PullRequests) != 1 || r.PullRequests[0].Number != 50 {
		t.Errorf("decode wrong: %+v", r)
	}
}

func TestParseResponseFenced(t *testing.T) {
	body := "Here you go:\n\n```json\n{\"pull_requests\": [{\"number\": 64, \"title\": \"x\", \"merged\": true, \"reviewers\": []}]}\n```\nLet me know if that helps."
	r, err := ParseResponse(body)
	if err != nil {
		t.Fatalf("parse fenced: %v", err)
	}
	if len(r.PullRequests) != 1 || r.PullRequests[0].Number != 64 {
		t.Errorf("decode fenced wrong: %+v", r)
	}
}

func TestParseResponseNoJSON(t *testing.T) {
	_, err := ParseResponse("I couldn't figure out the schema.")
	if err == nil {
		t.Errorf("expected error on JSON-less response")
	}
}
