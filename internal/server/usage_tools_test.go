package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcp "go.klarlabs.de/mcp"

	"github.com/felixgeelhaar/scry/internal/usage"
)

func TestUsageStatsReturnsSnapshot(t *testing.T) {
	tr := usage.NewTracker()
	tr.RecordToolCall("acme", "session-1", "query_execute", "ok")
	tr.RecordToolCall("acme", "session-1", "schema_search", "ok")

	srv := mcp.NewServer(mcp.ServerInfo{Name: "test", Version: "0"})
	if err := registerUsageTools(srv, tr); err != nil {
		t.Fatalf("register: %v", err)
	}
	tool, ok := srv.GetTool("usage_stats")
	if !ok {
		t.Fatalf("usage_stats not registered")
	}
	ctx := contextWithIdentity(context.Background(), &Identity{
		ID: identityAdmin, Name: identityAdmin,
	})
	out, err := tool.Execute(ctx, []byte(`{}`))
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	text := toolResultJSON(out)
	if !strings.Contains(text, "acme") || !strings.Contains(text, "query_execute") {
		t.Errorf("snapshot missing tenant or tool name: %q", text)
	}
}

func TestUsageStatsTenantFilter(t *testing.T) {
	tr := usage.NewTracker()
	tr.RecordToolCall("acme", "s1", "query_execute", "ok")
	tr.RecordToolCall("globex", "s2", "query_execute", "ok")
	srv := mcp.NewServer(mcp.ServerInfo{Name: "test", Version: "0"})
	_ = registerUsageTools(srv, tr)
	tool, _ := srv.GetTool("usage_stats")
	ctx := contextWithIdentity(context.Background(), &Identity{
		ID: identityAdmin, Name: identityAdmin,
	})
	out, _ := tool.Execute(ctx, []byte(`{"tenant":"acme"}`))
	text := toolResultJSON(out)
	if !strings.Contains(text, "acme") {
		t.Errorf("acme cell should appear, got %q", text)
	}
	if strings.Contains(text, "globex") {
		t.Errorf("globex must not appear when filtering for acme; got %q", text)
	}
}

func TestUsageStatsAdminOnly(t *testing.T) {
	tr := usage.NewTracker()
	tr.RecordToolCall("acme", "s1", "query_execute", "ok")
	srv := mcp.NewServer(mcp.ServerInfo{Name: "test", Version: "0"})
	_ = registerUsageTools(srv, tr)
	tool, _ := srv.GetTool("usage_stats")
	ctx := contextWithIdentity(context.Background(), &Identity{
		ID: identityReadOnly, Name: identityReadOnly,
	})
	out, _ := tool.Execute(ctx, []byte(`{}`))
	text := toolResultJSON(out)
	var got map[string]any
	_ = json.Unmarshal([]byte(text), &got)
	if got["error"] != "permission_denied" {
		t.Errorf("read-only must be denied; got %+v", got)
	}
}

func TestUsageStatsNilTrackerSkipsRegistration(t *testing.T) {
	srv := mcp.NewServer(mcp.ServerInfo{Name: "test", Version: "0"})
	if err := registerUsageTools(srv, nil); err != nil {
		t.Fatalf("nil tracker should be a no-op, got err: %v", err)
	}
	if _, ok := srv.GetTool("usage_stats"); ok {
		t.Errorf("nil tracker should NOT register usage_stats")
	}
}
