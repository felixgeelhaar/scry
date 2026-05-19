package server

import (
	"context"
	"strings"
	"testing"

	"github.com/felixgeelhaar/scry/internal/gate"
)

func TestApplyJMESPathProjection(t *testing.T) {
	body := []byte(`{"data":{"user":{"name":"alice","email":"a@x"}}}`)
	got, err := applyJMESPath("data.user.name", body)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if string(got) != `"alice"` {
		t.Errorf("got %s, want \"alice\"", got)
	}
}

func TestApplyJMESPathRestructure(t *testing.T) {
	body := []byte(`{"data":{"user":{"name":"alice","email":"a@x"}}}`)
	got, err := applyJMESPath("{n: data.user.name, e: data.user.email}", body)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(string(got), `"n":"alice"`) || !strings.Contains(string(got), `"e":"a@x"`) {
		t.Errorf("restructure wrong: %s", got)
	}
}

func TestApplyJMESPathInvalidSyntax(t *testing.T) {
	_, err := applyJMESPath("data.user.[bad", []byte(`{}`))
	if err == nil {
		t.Errorf("expected syntax error")
	}
	if !strings.Contains(err.Error(), "syntax") {
		t.Errorf("error should say syntax; got %q", err)
	}
}

func TestApplyJMESPathNoMatch(t *testing.T) {
	_, err := applyJMESPath("data.nonexistent", []byte(`{"data":{}}`))
	if err == nil {
		t.Errorf("typo'd path should error so agents notice")
	}
}

func TestApplyJMESPathInvalidJSON(t *testing.T) {
	_, err := applyJMESPath("data", []byte(`not json`))
	if err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Errorf("expected JSON-shape error, got %v", err)
	}
}

func TestQueryExecuteSelectTrims(t *testing.T) {
	f := newFixture(t, nil, gate.Policy{}, Config{CostCeiling: 1000})
	// Default fixture upstream returns {"data":{"ping":"pong"}}.
	// Project to just the string value.
	_, raw := f.call(context.Background(), map[string]any{
		"query":  "{ ping }",
		"select": "data.ping",
	})
	if raw != `"pong"` {
		t.Errorf("projected response = %q, want \"pong\"", raw)
	}
}

func TestQueryExecuteSelectInvalidSyntax(t *testing.T) {
	f := newFixture(t, nil, gate.Policy{}, Config{CostCeiling: 1000})
	got, _ := f.call(context.Background(), map[string]any{
		"query":  "{ ping }",
		"select": "data.[bad",
	})
	if got["error"] != "invalid_select" {
		t.Errorf("envelope = %v, want invalid_select (got %+v)", got["error"], got)
	}
}

func TestQueryExecuteSelectNoOp(t *testing.T) {
	// Empty select must passthrough — existing callers (no
	// select arg) keep working.
	f := newFixture(t, nil, gate.Policy{}, Config{CostCeiling: 1000})
	_, raw := f.call(context.Background(), map[string]any{"query": "{ ping }"})
	if !strings.Contains(raw, `"pong"`) {
		t.Errorf("no-select call should passthrough; got %q", raw)
	}
}
