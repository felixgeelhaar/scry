package server

import (
	"context"
	"strings"
	"testing"

	"github.com/felixgeelhaar/scry/internal/gate"
)

func TestQueryExecuteByPQName(t *testing.T) {
	f := newFixture(t, nil, gate.Policy{}, Config{CostCeiling: 1000})
	// Register a persisted query under a friendly name.
	entry, err := f.mgr.Get("default")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := entry.PQ.Put(context.Background(), "getPing", "{ ping }"); err != nil {
		t.Fatalf("put: %v", err)
	}
	_, raw := f.call(context.Background(), map[string]any{"name": "getPing"})
	if !strings.Contains(raw, `"pong"`) {
		t.Errorf("PQ name resolution should reach upstream; got %q", raw)
	}
}

func TestQueryExecuteByPQNameNotFound(t *testing.T) {
	f := newFixture(t, nil, gate.Policy{}, Config{CostCeiling: 1000})
	got, _ := f.call(context.Background(), map[string]any{"name": "nope"})
	if got["error"] != "pq_not_found" {
		t.Errorf("envelope error = %v, want pq_not_found", got["error"])
	}
}

func TestQueryExecuteConflictAllThree(t *testing.T) {
	f := newFixture(t, nil, gate.Policy{}, Config{CostCeiling: 1000})
	got, _ := f.call(context.Background(), map[string]any{
		"query": "{ ping }",
		"hash":  "abc",
		"name":  "n",
	})
	if got["error"] != "pq_conflict" {
		t.Errorf("envelope error = %v, want pq_conflict", got["error"])
	}
}

func TestQueryExecuteConflictHashAndName(t *testing.T) {
	f := newFixture(t, nil, gate.Policy{}, Config{CostCeiling: 1000})
	got, _ := f.call(context.Background(), map[string]any{
		"hash": "abc",
		"name": "getPing",
	})
	if got["error"] != "pq_conflict" {
		t.Errorf("hash + name must conflict; got %+v", got)
	}
}
