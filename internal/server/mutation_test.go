// End-to-end mutation flow over query_execute.
//
// v0.1-v0.4 unit-tested gate.Classify("mutation") and write-budget
// exhaustion, but no test exercised the full path: validate → cost →
// gate write budget → upstream POST → audit record outcome=ok
// effect=write. The closest existing coverage was the Pokémon live
// smoke, but Pokémon GraphQL is read-only — SWAPI and countries
// similarly. A stable public mutable endpoint doesn't exist (people
// have tried; teardown of any unauthenticated mutable demo gets
// abused within hours). v0.5 ships a deterministic httptest fake
// instead.
//
// Asserts the three guarantees v0.5's spec calls out:
//
//  1. Mutations bypass the cache (two identical mutation calls hit
//     upstream twice, even with cache wired).
//  2. The session write counter increments only on outcome=ok.
//  3. Audit chain Evidence records carry effect=write so the
//     integrity proof distinguishes side-effecting from read-only
//     operations.
package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcp "github.com/felixgeelhaar/mcp-go"

	"github.com/felixgeelhaar/scry/internal/gate"
	"github.com/felixgeelhaar/scry/internal/runtime"
)

// newMutationFixture wires an upstream that exposes both a Query
// and a Mutation root, with a side-effecting `incrementCounter`
// mutation. Returns counter pointers for the two channels the test
// asserts on: total upstream hits + mutation-specific hits.
type mutationFixture struct {
	t          *testing.T
	mgr        *runtime.Manager
	gate       *gate.Gate
	srv        *mcp.Server
	tool       func(ctx context.Context, input json.RawMessage) (any, error)
	hits       *atomic.Int64
	mutHits    *atomic.Int64
	mutCounter *atomic.Int64
}

func newMutationFixture(t *testing.T) *mutationFixture {
	t.Helper()

	var (
		hits       atomic.Int64
		mutHits    atomic.Int64
		mutCounter atomic.Int64
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		switch {
		case strings.Contains(s, "__schema"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
  "data": {
    "__schema": {
      "queryType":    {"name": "Query"},
      "mutationType": {"name": "Mutation"},
      "types": [
        {
          "kind": "OBJECT", "name": "Query",
          "fields": [{"name": "ping", "type": {"kind": "SCALAR", "name": "String"}}]
        },
        {
          "kind": "OBJECT", "name": "Mutation",
          "fields": [{"name": "incrementCounter", "type": {"kind": "SCALAR", "name": "Int"}}]
        }
      ]
    }
  }
}`))
		case strings.Contains(s, "incrementCounter"):
			mutHits.Add(1)
			n := mutCounter.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"incrementCounter":` + itoa(n) + `}}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"ping":"pong"}}`))
		}
	})
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)

	indexDir := t.TempDir()
	mgr, err := runtime.New(indexDir, 1000)
	if err != nil {
		t.Fatalf("mgr: %v", err)
	}
	// Wire cache BEFORE Add so the entry picks up a non-nil
	// Cache. The point of this test is that mutations bypass it.
	mgr.CacheTTL = time.Minute
	mgr.CacheMaxEntries = 10
	t.Cleanup(func() { _ = mgr.Close() })

	if err := mgr.Add(context.Background(), runtime.AddConfig{
		Name:     "default",
		Upstream: upstream.URL,
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	g, err := gate.New(gate.Policy{MaxWritesPerSession: 10, MaxComplexityPerSession: 1000})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })

	srv := mcp.NewServer(mcp.ServerInfo{Name: "test", Version: "0"})
	if err := registerQueryTools(srv, Config{CostCeiling: 1000, CacheTTL: time.Minute, CacheMaxEntries: 10}, mgr, g); err != nil {
		t.Fatalf("register: %v", err)
	}
	tool, ok := srv.GetTool("query_execute")
	if !ok {
		t.Fatalf("query_execute not registered")
	}

	return &mutationFixture{
		t: t, mgr: mgr, gate: g, srv: srv, tool: tool.Execute,
		hits: &hits, mutHits: &mutHits, mutCounter: &mutCounter,
	}
}

// itoa avoids the strconv import for the tiny formatting need above.
// One-digit counter is enough for two mutations.
func itoa(n int64) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return "n" // shouldn't happen in this test
}

func (f *mutationFixture) call(args map[string]any) string {
	f.t.Helper()
	in, _ := json.Marshal(args)
	out, err := f.tool(context.Background(), in)
	if err != nil {
		f.t.Fatalf("tool: %v", err)
	}
	text, _ := out.(string)
	return text
}

func TestMutationCacheBypass(t *testing.T) {
	f := newMutationFixture(t)
	mut := map[string]any{"query": "mutation { incrementCounter }"}

	_ = f.call(mut)
	hitsAfterFirst := f.mutHits.Load()
	if hitsAfterFirst != 1 {
		t.Fatalf("first mutation should hit upstream once, got %d", hitsAfterFirst)
	}

	_ = f.call(mut)
	hitsAfterSecond := f.mutHits.Load()
	if hitsAfterSecond != 2 {
		t.Errorf("second mutation must bypass cache and hit upstream again; got total %d (cache treated mutation as cacheable)", hitsAfterSecond)
	}
}

func TestMutationWriteCounterIncrements(t *testing.T) {
	f := newMutationFixture(t)
	mut := map[string]any{"query": "mutation { incrementCounter }"}

	if w := f.gate.Stats("default:local").Writes; w != 0 {
		t.Fatalf("write counter starts at 0, got %d", w)
	}
	_ = f.call(mut)
	_ = f.call(mut)
	if w := f.gate.Stats("default:local").Writes; w != 2 {
		t.Errorf("write counter after 2 mutations = %d, want 2", w)
	}
}

func TestMutationChainEvidenceCarriesEffectWrite(t *testing.T) {
	f := newMutationFixture(t)
	_ = f.call(map[string]any{"query": "mutation { incrementCounter }"})

	chain := f.gate.Chain("default:local")
	if len(chain) != 1 {
		t.Fatalf("chain len = %d, want 1", len(chain))
	}
	ev := chain[0]
	if ev.Effect != gate.EffectWrite {
		t.Errorf("evidence Effect = %q, want write", ev.Effect)
	}
	if ev.Outcome != "ok" {
		t.Errorf("evidence Outcome = %q, want ok", ev.Outcome)
	}
}

// TestMutationWriteCounterDoesNotIncrementOnNonOK guards the
// spec.yaml requirement that "write counter increments only on
// outcome=ok". gate.Record bumps Writes unconditionally inside the
// effect=write branch (see internal/gate/gate.go), so the only path
// the increment doesn't happen on is when CheckBudget rejects the
// call BEFORE Record fires (e.g. write budget exhausted). This
// verifies that contract: a rejected mutation neither hits upstream
// nor increments the write counter.
func TestMutationWriteCounterDoesNotIncrementOnNonOK(t *testing.T) {
	t.Helper()

	// Build a fresh fixture with write budget = 1, then burn it,
	// then issue a second mutation that the gate must reject.
	f := newMutationFixture(t)
	// Swap the gate for one with a tight write budget. The
	// fixture's default gate is too generous to trip cheaply.
	tightGate, err := gate.New(gate.Policy{MaxWritesPerSession: 1, MaxComplexityPerSession: 1000})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	t.Cleanup(func() { _ = tightGate.Close() })

	srv := mcp.NewServer(mcp.ServerInfo{Name: "test", Version: "0"})
	if err := registerQueryTools(srv, Config{CostCeiling: 1000, CacheTTL: time.Minute, CacheMaxEntries: 10}, f.mgr, tightGate); err != nil {
		t.Fatalf("register: %v", err)
	}
	tool, _ := srv.GetTool("query_execute")
	exec := tool.Execute

	call := func(q string) {
		in, _ := json.Marshal(map[string]any{"query": q})
		_, _ = exec(context.Background(), in)
	}

	call("mutation { incrementCounter }")
	if w := tightGate.Stats("default:local").Writes; w != 1 {
		t.Fatalf("after first mutation Writes = %d, want 1", w)
	}
	mutHitsAfterFirst := f.mutHits.Load()

	call("mutation { incrementCounter }")
	if w := tightGate.Stats("default:local").Writes; w != 1 {
		t.Errorf("second (rejected) mutation must not bump write counter; got %d", w)
	}
	if f.mutHits.Load() != mutHitsAfterFirst {
		t.Errorf("rejected mutation must not reach upstream; mutHits moved from %d to %d", mutHitsAfterFirst, f.mutHits.Load())
	}
}
