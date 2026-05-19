package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/felixgeelhaar/scry/internal/schema"
)

// schemaNeighborsFixture wires a single-server scry instance and
// pre-populates the neighbors table directly via the schema.Store.
// Lets the test bypass full introspection — the unit under test is
// just the MCP tool wiring.
func setupNeighborsFixture(t *testing.T, edges []schema.Edge) *schemaToolsFixture {
	t.Helper()
	f := newSchemaToolsFixture(t, nil)
	entry, err := f.mgr.Get("default")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := entry.Store.ReplaceNeighbors(context.Background(), edges); err != nil {
		t.Fatalf("replace neighbors: %v", err)
	}
	return f
}

func TestSchemaNeighborsIncomingOutgoing(t *testing.T) {
	f := setupNeighborsFixture(t, []schema.Edge{
		{Src: "Query", Dst: "User", Field: "viewer", Kind: "field"},
		{Src: "User", Dst: "Address", Field: "primaryAddress", Kind: "field"},
		{Src: "User", Dst: "Node", Kind: "interface"},
	})
	tool, _ := f.srv.GetTool("schema_neighbors")
	in, _ := json.Marshal(map[string]any{"name": "User"})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	text, _ := out.(string)
	var got map[string]any
	_ = json.Unmarshal([]byte(text), &got)

	incoming, _ := got["incoming"].([]any)
	outgoing, _ := got["outgoing"].([]any)
	if len(incoming) != 1 {
		t.Errorf("expected 1 incoming, got %d (%+v)", len(incoming), incoming)
	}
	if len(outgoing) != 2 {
		t.Errorf("expected 2 outgoing, got %d (%+v)", len(outgoing), outgoing)
	}
}

func TestSchemaNeighborsNotFound(t *testing.T) {
	f := setupNeighborsFixture(t, nil)
	tool, _ := f.srv.GetTool("schema_neighbors")
	in, _ := json.Marshal(map[string]any{"name": "Nonexistent"})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	text, _ := out.(string)
	var got map[string]any
	_ = json.Unmarshal([]byte(text), &got)
	if got["error"] != "not_found" {
		t.Errorf("envelope = %v, want not_found", got["error"])
	}
	if hint, _ := got["hint"].(string); !strings.Contains(hint, "schema_search") {
		t.Errorf("hint should suggest schema_search; got %q", hint)
	}
}

func TestSchemaNeighborsRespectsLimit(t *testing.T) {
	// Build 60 outgoing edges from Hub; tool clamps to 50.
	edges := make([]schema.Edge, 60)
	for i := range edges {
		edges[i] = schema.Edge{Src: "Hub", Dst: "Target" + intkey(i), Field: "f", Kind: "field"}
	}
	f := setupNeighborsFixture(t, edges)
	tool, _ := f.srv.GetTool("schema_neighbors")
	in, _ := json.Marshal(map[string]any{"name": "Hub", "limit": 9999})
	out, _ := tool.Execute(context.Background(), in)
	text, _ := out.(string)
	var got map[string]any
	_ = json.Unmarshal([]byte(text), &got)
	outgoing := got["outgoing"].([]any)
	if len(outgoing) > 50 {
		t.Errorf("limit must clamp at 50; got %d", len(outgoing))
	}
}

func intkey(i int) string {
	digits := []byte{'0', '0', '0'}
	for n := 2; n >= 0 && i > 0; n-- {
		digits[n] = byte('0' + (i % 10))
		i /= 10
	}
	return string(digits)
}
