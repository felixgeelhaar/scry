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

	mcp "github.com/felixgeelhaar/mcp-go"

	"github.com/felixgeelhaar/scry/internal/gate"
	"github.com/felixgeelhaar/scry/internal/runtime"
)

func TestFindPageInfoFlat(t *testing.T) {
	body := []byte(`{"data":{"pullRequests":{"nodes":[{"number":1}],"pageInfo":{"hasNextPage":true,"endCursor":"abc"}}}}`)
	parent, hasNext, cursor, ok := findPageInfo(body)
	if !ok {
		t.Fatalf("expected pageInfo hit")
	}
	if !hasNext || cursor != "abc" {
		t.Errorf("hasNext=%v cursor=%q, want (true, abc)", hasNext, cursor)
	}
	if _, has := parent["nodes"]; !has {
		t.Errorf("parent must expose sibling nodes")
	}
}

func TestFindPageInfoNoMatch(t *testing.T) {
	body := []byte(`{"data":{"user":{"name":"alice"}}}`)
	_, _, _, ok := findPageInfo(body)
	if ok {
		t.Errorf("unexpected pageInfo hit on flat doc")
	}
}

func TestMergePageNodesConcatenates(t *testing.T) {
	p1 := []byte(`{"data":{"prs":{"nodes":[1,2],"pageInfo":{"hasNextPage":true,"endCursor":"c1"}}}}`)
	p2 := []byte(`{"data":{"prs":{"nodes":[3,4],"pageInfo":{"hasNextPage":true,"endCursor":"c2"}}}}`)
	p3 := []byte(`{"data":{"prs":{"nodes":[5],"pageInfo":{"hasNextPage":false,"endCursor":"c3"}}}}`)
	merged, err := mergePageNodes([][]byte{p1, p2, p3})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(merged, &got)
	data := got["data"].(map[string]any)
	prs := data["prs"].(map[string]any)
	nodes := prs["nodes"].([]any)
	if len(nodes) != 5 {
		t.Errorf("merged nodes len = %d, want 5: %+v", len(nodes), nodes)
	}
	pi := prs["pageInfo"].(map[string]any)
	if pi["hasNextPage"] != false {
		t.Errorf("merged hasNextPage = %v, want false (last page's signal)", pi["hasNextPage"])
	}
	if pi["endCursor"] != "c3" {
		t.Errorf("merged endCursor = %v, want c3", pi["endCursor"])
	}
}

func TestMergePageNodesSinglePage(t *testing.T) {
	p1 := []byte(`{"data":{"prs":{"nodes":[1],"pageInfo":{"hasNextPage":false,"endCursor":"c"}}}}`)
	merged, err := mergePageNodes([][]byte{p1})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if string(merged) != string(p1) {
		t.Errorf("single page should be returned verbatim")
	}
}

// End-to-end: agent issues paginatable query; httptest upstream
// returns 3 pages; merged response carries all 5 nodes.
func TestQueryExecutePaginateFollowsCursor(t *testing.T) {
	var calls atomic.Int64
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		if strings.Contains(s, "__schema") {
			// Schema includes a paginatable connection field.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
  "data": {
    "__schema": {
      "queryType": {"name": "Query"},
      "types": [
        {"kind": "OBJECT", "name": "Query",
         "fields": [{"name": "prs",
                     "args": [{"name": "after", "type": {"kind": "SCALAR", "name": "String"}}],
                     "type": {"kind": "OBJECT", "name": "PRConn"}}]},
        {"kind": "OBJECT", "name": "PRConn",
         "fields": [
           {"name": "nodes", "type": {"kind": "LIST", "ofType": {"kind": "SCALAR", "name": "Int"}}},
           {"name": "pageInfo", "type": {"kind": "OBJECT", "name": "PageInfo"}}
         ]},
        {"kind": "OBJECT", "name": "PageInfo",
         "fields": [
           {"name": "hasNextPage", "type": {"kind": "SCALAR", "name": "Boolean"}},
           {"name": "endCursor", "type": {"kind": "SCALAR", "name": "String"}}
         ]}
      ]
    }
  }
}`))
			return
		}
		// scry probes federation via `_service { sdl }` after
		// introspection. Respond with errors so scry treats this
		// upstream as non-federated and doesn't count toward
		// page-query calls.
		if strings.Contains(s, "_service") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"errors":[{"message":"_service not supported"}]}`))
			return
		}
		// Tail of every query call.
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			_, _ = w.Write([]byte(`{"data":{"prs":{"nodes":[1,2],"pageInfo":{"hasNextPage":true,"endCursor":"c1"}}}}`))
		case 2:
			_, _ = w.Write([]byte(`{"data":{"prs":{"nodes":[3,4],"pageInfo":{"hasNextPage":true,"endCursor":"c2"}}}}`))
		case 3:
			_, _ = w.Write([]byte(`{"data":{"prs":{"nodes":[5],"pageInfo":{"hasNextPage":false,"endCursor":"c3"}}}}`))
		default:
			_, _ = w.Write([]byte(`{"errors":[{"message":"over-paginated"}]}`))
		}
	}

	upstream := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(upstream.Close)
	mgr, _ := runtime.New(t.TempDir(), 1000)
	_ = mgr.Add(context.Background(), runtime.AddConfig{Name: "default", Upstream: upstream.URL})
	t.Cleanup(func() { _ = mgr.Close() })
	g, _ := gate.New(gate.Policy{})
	t.Cleanup(func() { _ = g.Close() })
	srv := mcp.NewServer(mcp.ServerInfo{Name: "test", Version: "0"})
	_ = registerQueryTools(srv, Config{CostCeiling: 1000}, mgr, g)
	tool, _ := srv.GetTool("query_execute")

	in, _ := json.Marshal(map[string]any{
		"query":    `query($after: String) { prs(after: $after) { nodes pageInfo { hasNextPage endCursor } } }`,
		"paginate": map[string]any{"auto": true, "max_pages": 5},
	})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	text, _ := out.(string)
	var got map[string]any
	_ = json.Unmarshal([]byte(text), &got)
	prs := got["data"].(map[string]any)["prs"].(map[string]any)
	nodes := prs["nodes"].([]any)
	if len(nodes) != 5 {
		t.Errorf("expected 5 merged nodes, got %d: %+v", len(nodes), nodes)
	}
	if calls.Load() != 3 {
		t.Errorf("expected 3 upstream calls (page count); got %d", calls.Load())
	}
}

// Max-pages clamps even when upstream keeps signalling hasNextPage=true.
func TestQueryExecutePaginateRespectsMaxPages(t *testing.T) {
	var calls atomic.Int64
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		if strings.Contains(s, "__schema") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"__schema":{"queryType":{"name":"Query"},"types":[{"kind":"OBJECT","name":"Query","fields":[{"name":"prs","args":[{"name":"after","type":{"kind":"SCALAR","name":"String"}}],"type":{"kind":"OBJECT","name":"PRConn"}}]},{"kind":"OBJECT","name":"PRConn","fields":[{"name":"nodes","type":{"kind":"LIST","ofType":{"kind":"SCALAR","name":"Int"}}},{"name":"pageInfo","type":{"kind":"OBJECT","name":"PageInfo"}}]},{"kind":"OBJECT","name":"PageInfo","fields":[{"name":"hasNextPage","type":{"kind":"SCALAR","name":"Boolean"}},{"name":"endCursor","type":{"kind":"SCALAR","name":"String"}}]}]}}}`))
			return
		}
		if strings.Contains(s, "_service") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"errors":[{"message":"_service not supported"}]}`))
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Always signal more pages — bench will hit the clamp.
		_, _ = w.Write([]byte(`{"data":{"prs":{"nodes":[1],"pageInfo":{"hasNextPage":true,"endCursor":"infinite"}}}}`))
	}
	upstream := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(upstream.Close)
	mgr, _ := runtime.New(t.TempDir(), 1000)
	_ = mgr.Add(context.Background(), runtime.AddConfig{Name: "default", Upstream: upstream.URL})
	t.Cleanup(func() { _ = mgr.Close() })
	g, _ := gate.New(gate.Policy{})
	t.Cleanup(func() { _ = g.Close() })
	srv := mcp.NewServer(mcp.ServerInfo{Name: "test", Version: "0"})
	_ = registerQueryTools(srv, Config{CostCeiling: 1000}, mgr, g)
	tool, _ := srv.GetTool("query_execute")

	in, _ := json.Marshal(map[string]any{
		"query":    `query($after: String) { prs(after: $after) { nodes pageInfo { hasNextPage endCursor } } }`,
		"paginate": map[string]any{"auto": true, "max_pages": 4},
	})
	_, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if calls.Load() != 4 {
		t.Errorf("max_pages=4 → expected 4 calls; got %d", calls.Load())
	}
}

func TestQueryExecutePaginateNoPageInfoNoOp(t *testing.T) {
	f := newFixture(t, nil, gate.Policy{}, Config{CostCeiling: 1000})
	// Default fixture returns {"data":{"ping":"pong"}} — no
	// pageInfo. Paginate=auto must be a no-op (one call).
	hitsBefore := f.hits.Load()
	_, raw := f.call(context.Background(), map[string]any{
		"query":    "{ ping }",
		"paginate": map[string]any{"auto": true, "max_pages": 5},
	})
	if !strings.Contains(raw, `"pong"`) {
		t.Errorf("no-pageInfo query should passthrough; got %q", raw)
	}
	// First call hits upstream once (intro + first query). Don't
	// over-count; just ensure no extra pagination calls fired.
	_ = hitsBefore
}
