package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcp "go.klarlabs.de/mcp"

	"github.com/felixgeelhaar/scry/internal/runtime"
	"github.com/felixgeelhaar/scry/internal/schema"
)

// schemaToolsFixture wires a one-server scry environment and exposes
// the four schema-tool handlers via name → Execute thunks. Mirrors
// queryExecuteFixture but skips gate.Gate (the read-only schema
// tools don't touch the budget).
type schemaToolsFixture struct {
	t      *testing.T
	mgr    *runtime.Manager
	srv    *mcp.Server
	upstrm *httptest.Server
}

func newSchemaToolsFixture(t *testing.T, handler http.HandlerFunc) *schemaToolsFixture {
	t.Helper()

	if handler == nil {
		handler = defaultSchemaUpstream
	}
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)

	indexDir := t.TempDir()
	mgr, err := runtime.New(indexDir, 1000)
	if err != nil {
		t.Fatalf("mgr: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	if err := mgr.Add(context.Background(), runtime.AddConfig{
		Name:     "default",
		Upstream: upstream.URL,
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	srv := mcp.NewServer(mcp.ServerInfo{Name: "test", Version: "0"})
	if err := registerSchemaTools(srv, Config{CostCeiling: 1000}, mgr); err != nil {
		t.Fatalf("register: %v", err)
	}

	return &schemaToolsFixture{t: t, mgr: mgr, srv: srv, upstrm: upstream}
}

// call dispatches to a named schema tool and decodes the response.
// Tools that return non-JSON (raw SDL, formatted markdown) decode
// to a nil map — callers should also receive raw text for those.
func (f *schemaToolsFixture) call(name string, args map[string]any) (map[string]any, string) {
	f.t.Helper()
	tool, ok := f.srv.GetTool(name)
	if !ok {
		f.t.Fatalf("tool %q not registered", name)
	}
	in, _ := json.Marshal(args)
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		f.t.Fatalf("%s: %v", name, err)
	}
	text := toolResultJSON(out)
	if text == "" {
		f.t.Fatalf("%s returned empty result: %+v", name, out)
	}
	var decoded map[string]any
	_ = json.Unmarshal([]byte(text), &decoded)
	return decoded, text
}

// toolResultJSON normalizes a tool.Execute return value to its JSON
// text form. Handlers that advertise an output schema now return typed
// structs on the happy path (the framework promotes them to
// structuredContent); handlers still return string envelopes for error
// and prose responses. Tests decode both shapes uniformly.
func toolResultJSON(out any) string {
	if s, ok := out.(string); ok {
		return s
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func defaultSchemaUpstream(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if strings.Contains(string(body), "__schema") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "data": {
    "__schema": {
      "queryType": {"name": "Query"},
      "types": [{
        "kind": "OBJECT", "name": "Query",
        "fields": [
          {"name": "ping", "type": {"kind": "SCALAR", "name": "String"}},
          {"name": "count", "type": {"kind": "SCALAR", "name": "Int"}}
        ]
      }]
    }
  }
}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"data":{}}`))
}

// --- schema_search ---

func TestSchemaSearchEmptyResults(t *testing.T) {
	f := newSchemaToolsFixture(t, nil)
	_, raw := f.call("schema_search", map[string]any{"query": "zzznonexistentterm"})
	if !strings.Contains(raw, "No schema units") {
		t.Errorf("expected empty-result rendering, got %q", raw)
	}
}

func TestSchemaSearchPopulated(t *testing.T) {
	f := newSchemaToolsFixture(t, nil)
	_, raw := f.call("schema_search", map[string]any{"query": "ping"})
	if !strings.Contains(raw, "Query.ping") {
		t.Errorf("expected Query.ping hit in results, got %q", raw)
	}
	if strings.Contains(raw, "| Subgraph |") {
		t.Errorf("non-federated upstream should hide Subgraph column, got %q", raw)
	}
}

func TestSchemaSearchSubgraphColumn(t *testing.T) {
	f := newSchemaToolsFixture(t, nil)
	// Backfill the index with a unit that carries a Subgraph
	// value so the renderer adds the column. Replace() wipes the
	// store first — keep both fields the test expects.
	entry, err := f.mgr.Get("default")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := entry.Store.Replace(context.Background(), []schema.SearchUnit{
		{Name: "Query.ping", Kind: "field", Composed: "ping String", Signature: "ping: String", Subgraph: "users"},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	_, raw := f.call("schema_search", map[string]any{"query": "ping"})
	if !strings.Contains(raw, "| Subgraph |") {
		t.Errorf("federated hit should add Subgraph column, got %q", raw)
	}
	if !strings.Contains(raw, "users") {
		t.Errorf("expected subgraph value in table, got %q", raw)
	}
}

// --- schema_get ---

func TestSchemaGetHit(t *testing.T) {
	f := newSchemaToolsFixture(t, nil)
	_, raw := f.call("schema_get", map[string]any{"name": "Query"})
	if !strings.Contains(raw, "Query") {
		t.Errorf("expected Query SDL, got %q", raw)
	}
}

func TestSchemaGetNotFound(t *testing.T) {
	f := newSchemaToolsFixture(t, nil)
	got, _ := f.call("schema_get", map[string]any{"name": "DoesNotExist"})
	if got["error"] != "not_found" {
		t.Errorf("envelope error = %v, want not_found", got["error"])
	}
}

// --- query_validate ---

func TestQueryValidateOk(t *testing.T) {
	f := newSchemaToolsFixture(t, nil)
	got, _ := f.call("query_validate", map[string]any{"query": "{ ping }"})
	if got["ok"] != true {
		t.Errorf("envelope ok = %v, want true (got %+v)", got["ok"], got)
	}
}

func TestQueryValidateErrors(t *testing.T) {
	f := newSchemaToolsFixture(t, nil)
	got, _ := f.call("query_validate", map[string]any{"query": "{ unknownField }"})
	if got["ok"] != false {
		t.Errorf("envelope ok = %v, want false", got["ok"])
	}
	if _, has := got["errors"]; !has {
		t.Errorf("expected errors[] in envelope, got %+v", got)
	}
}

// --- query_cost ---

func TestQueryCostOk(t *testing.T) {
	f := newSchemaToolsFixture(t, nil)
	got, _ := f.call("query_cost", map[string]any{"query": "{ ping count }"})
	// Cost report shape varies; assert there's no error envelope.
	if got["error"] != nil {
		t.Errorf("unexpected error envelope: %+v", got)
	}
}

func TestQueryCostErrors(t *testing.T) {
	f := newSchemaToolsFixture(t, nil)
	got, _ := f.call("query_cost", map[string]any{"query": "{ unknownField }"})
	if got["error"] != "invalid_query" {
		t.Errorf("envelope error = %v, want invalid_query", got["error"])
	}
}

// --- schema_diff ---

func TestSchemaDiffNoDiff(t *testing.T) {
	f := newSchemaToolsFixture(t, nil)
	// Fresh fixture: first introspection writes no diff (no prior
	// schema to compare against), so the meta key is absent.
	got, _ := f.call("schema_diff", map[string]any{})
	if got["error"] != "no_diff" {
		t.Errorf("envelope error = %v, want no_diff", got["error"])
	}
}

func TestSchemaDiffPopulated(t *testing.T) {
	f := newSchemaToolsFixture(t, nil)
	entry, err := f.mgr.Get("default")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	payload := `{"added":["NewType"],"removed":[],"breaking":[]}`
	if err := entry.Store.SetMeta(context.Background(), "last_diff", payload); err != nil {
		t.Fatalf("set last_diff: %v", err)
	}
	_, raw := f.call("schema_diff", map[string]any{})
	if raw != payload {
		t.Errorf("schema_diff returned %q, want stored payload verbatim", raw)
	}
}

// --- resolveServer envelope behaviour ---

func TestResolveServerSingleDefault(t *testing.T) {
	f := newSchemaToolsFixture(t, nil)
	// Single configured server, no `server` arg → routes to default.
	_, raw := f.call("schema_search", map[string]any{"query": "ping"})
	if !strings.Contains(raw, "default") {
		t.Errorf("expected server name in rendered output, got %q", raw)
	}
}

func TestResolveServerMultiNoName(t *testing.T) {
	f := newSchemaToolsFixture(t, nil)
	if err := f.mgr.Add(context.Background(), runtime.AddConfig{
		Name:     "second",
		Upstream: f.upstrm.URL,
	}); err != nil {
		t.Fatalf("add second: %v", err)
	}
	got, _ := f.call("schema_search", map[string]any{"query": "ping"})
	if got["error"] != "unknown_server" {
		t.Errorf("envelope error = %v, want unknown_server", got["error"])
	}
}

func TestResolveServerMultiKnownName(t *testing.T) {
	f := newSchemaToolsFixture(t, nil)
	if err := f.mgr.Add(context.Background(), runtime.AddConfig{
		Name:     "second",
		Upstream: f.upstrm.URL,
	}); err != nil {
		t.Fatalf("add second: %v", err)
	}
	_, raw := f.call("schema_search", map[string]any{
		"server": "second",
		"query":  "ping",
	})
	if !strings.Contains(raw, "second") {
		t.Errorf("expected server name in output, got %q", raw)
	}
}

func TestResolveServerMultiUnknownName(t *testing.T) {
	f := newSchemaToolsFixture(t, nil)
	if err := f.mgr.Add(context.Background(), runtime.AddConfig{
		Name:     "second",
		Upstream: f.upstrm.URL,
	}); err != nil {
		t.Fatalf("add second: %v", err)
	}
	got, _ := f.call("schema_search", map[string]any{
		"server": "nope",
		"query":  "ping",
	})
	if got["error"] != "unknown_server" {
		t.Errorf("envelope error = %v, want unknown_server", got["error"])
	}
}
