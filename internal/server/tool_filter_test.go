package server

import (
	"context"
	"testing"

	mcp "go.klarlabs.de/mcp"
	mcpmw "go.klarlabs.de/mcp/middleware"
	"go.klarlabs.de/mcp/protocol"

	"github.com/felixgeelhaar/scry/internal/auth"
)

// stubHandler returns a fixed tools/list response. Used to feed the
// toolListFilter middleware without booting the whole server.
func stubHandler(toolNames []string) mcpmw.HandlerFunc {
	return func(_ context.Context, req *protocol.Request) (*protocol.Response, error) {
		tools := make([]map[string]any, 0, len(toolNames))
		for _, n := range toolNames {
			tools = append(tools, map[string]any{
				"name":        n,
				"description": "stub for " + n,
			})
		}
		return &protocol.Response{Result: map[string]any{"tools": tools}}, nil
	}
}

func TestToolListFilterStripsToolsForReadOnlyIdentity(t *testing.T) {
	prev := scopeRegistry
	scopeRegistry = nil
	t.Cleanup(func() { scopeRegistry = prev })

	mw := toolListFilter()
	h := mw(stubHandler([]string{
		"schema_search", "schema_get", "query_validate", "query_cost",
		"query_execute", "auth_status", "auth_login", "list_servers",
		"gate_status", "gate_chain",
	}))

	ctx := mcp.ContextWithIdentity(context.Background(), &mcpmw.Identity{
		ID: identityReadOnly, Name: identityReadOnly,
	})
	req := &protocol.Request{Method: protocol.MethodToolsList}
	resp, err := h(ctx, req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	names := toolNamesFromResponse(t, resp)
	for _, banned := range []string{"query_execute", "auth_login"} {
		for _, n := range names {
			if n == banned {
				t.Errorf("read-only identity should not see %q in tools/list", banned)
			}
		}
	}
	// Read-only tools must still be visible.
	for _, allowed := range []string{"schema_search", "list_servers", "gate_status"} {
		found := false
		for _, n := range names {
			if n == allowed {
				found = true
			}
		}
		if !found {
			t.Errorf("read-only identity should see %q in tools/list (got %v)", allowed, names)
		}
	}
}

func TestToolListFilterPassesThroughForAdmin(t *testing.T) {
	prev := scopeRegistry
	scopeRegistry = nil
	t.Cleanup(func() { scopeRegistry = prev })

	mw := toolListFilter()
	full := []string{"schema_search", "query_execute", "auth_login"}
	h := mw(stubHandler(full))
	ctx := mcp.ContextWithIdentity(context.Background(), &mcpmw.Identity{
		ID: identityAdmin, Name: identityAdmin,
	})
	resp, err := h(ctx, &protocol.Request{Method: protocol.MethodToolsList})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	names := toolNamesFromResponse(t, resp)
	if len(names) != len(full) {
		t.Errorf("admin should see all tools, got %v", names)
	}
}

func TestToolListFilterAppliesClientsYAMLScope(t *testing.T) {
	prev := scopeRegistry
	t.Cleanup(func() { scopeRegistry = prev })
	scope, err := auth.Client{
		Name: "dashboard", Token: "t",
		Tools:   []string{"schema_search", "list_servers"},
		Servers: []string{"*"},
	}.BuildScope(nil)
	if err != nil {
		t.Fatalf("BuildScope: %v", err)
	}
	scopeRegistry = map[string]*auth.Scope{"dash-token": &scope}

	mw := toolListFilter()
	h := mw(stubHandler([]string{
		"schema_search", "schema_get", "query_execute", "list_servers", "gate_status",
	}))
	ctx := mcp.ContextWithIdentity(context.Background(), &mcpmw.Identity{
		ID: "dash-token", Name: "dashboard",
	})
	resp, err := h(ctx, &protocol.Request{Method: protocol.MethodToolsList})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	names := toolNamesFromResponse(t, resp)
	want := map[string]bool{"schema_search": true, "list_servers": true}
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("expected %q in scoped tools/list, got %v", k, names)
		}
	}
	for n := range got {
		if !want[n] {
			t.Errorf("unexpected tool %q in scoped tools/list", n)
		}
	}
}

func TestToolListFilterIgnoresNonListMethods(t *testing.T) {
	prev := scopeRegistry
	scopeRegistry = nil
	t.Cleanup(func() { scopeRegistry = prev })

	mw := toolListFilter()
	called := false
	inner := mcpmw.HandlerFunc(func(_ context.Context, req *protocol.Request) (*protocol.Response, error) {
		called = true
		return &protocol.Response{Result: map[string]any{"hello": "world"}}, nil
	})
	h := mw(inner)
	resp, err := h(context.Background(), &protocol.Request{Method: protocol.MethodToolsCall})
	if err != nil || !called {
		t.Fatalf("non-list method should pass through unchanged")
	}
	if resp.Result.(map[string]any)["hello"] != "world" {
		t.Errorf("response body mutated for non-list method")
	}
}

func toolNamesFromResponse(t *testing.T, resp *protocol.Response) []string {
	t.Helper()
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("response.Result not a map: %+v", resp.Result)
	}
	tools, ok := m["tools"].([]map[string]any)
	if !ok {
		t.Fatalf("tools not []map[string]any: %+v", m["tools"])
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		n, _ := tool["name"].(string)
		names = append(names, n)
	}
	return names
}
