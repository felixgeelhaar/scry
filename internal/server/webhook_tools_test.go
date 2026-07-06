package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcp "go.klarlabs.de/mcp"

	"github.com/felixgeelhaar/scry/internal/runtime"
)

// newWebhookFixture stands up a single-server scry instance with
// the webhook tools registered. Returns the manager + the server.
func newWebhookFixture(t *testing.T) (*runtime.Manager, *mcp.Server) {
	t.Helper()
	indexDir := t.TempDir()
	mgr, err := runtime.New(indexDir, 1000)
	if err != nil {
		t.Fatalf("mgr: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	upstreamURL := startFakeUpstream(t)
	if err := mgr.Add(context.Background(), runtime.AddConfig{
		Name:     "default",
		Upstream: upstreamURL,
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	srv := mcp.NewServer(mcp.ServerInfo{Name: "test", Version: "0"})
	if err := registerWebhookTools(srv, mgr); err != nil {
		t.Fatalf("register: %v", err)
	}
	return mgr, srv
}

// adminCtx is a context carrying the admin identity so requireAdmin
// returns "" for tool calls in the tests below.
func adminCtx() context.Context {
	return contextWithIdentity(context.Background(), &Identity{
		ID: identityAdmin, Name: identityAdmin,
	})
}

func TestSchemaDiffSubscribeRegistersWebhook(t *testing.T) {
	_, srv := newWebhookFixture(t)
	tool, _ := srv.GetTool("schema_diff_subscribe")
	in, _ := json.Marshal(map[string]any{"url": "https://hooks.example.com/scry"})
	out, err := tool.Execute(adminCtx(), in)
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	text, _ := out.(string)
	var got map[string]any
	_ = json.Unmarshal([]byte(text), &got)
	if got["id"] == nil {
		t.Errorf("expected id in response, got %+v", got)
	}
	if got["secret"] == nil || got["secret"] == "" {
		t.Errorf("expected non-empty secret on registration")
	}
}

func TestSchemaDiffSubscribeRejectsInvalidURL(t *testing.T) {
	_, srv := newWebhookFixture(t)
	tool, _ := srv.GetTool("schema_diff_subscribe")
	in, _ := json.Marshal(map[string]any{"url": "not a url"})
	out, _ := tool.Execute(adminCtx(), in)
	text, _ := out.(string)
	var got map[string]any
	_ = json.Unmarshal([]byte(text), &got)
	if got["error"] != "invalid_url" {
		t.Errorf("envelope = %v, want invalid_url", got["error"])
	}
}

func TestSchemaDiffSubscribeRejectsNonAdmin(t *testing.T) {
	_, srv := newWebhookFixture(t)
	tool, _ := srv.GetTool("schema_diff_subscribe")
	in, _ := json.Marshal(map[string]any{"url": "https://hooks.example.com/scry"})
	// Read-only identity → requireAdmin returns permission_denied.
	ctx := contextWithIdentity(context.Background(), &Identity{
		ID: identityReadOnly, Name: identityReadOnly,
	})
	out, _ := tool.Execute(ctx, in)
	text, _ := out.(string)
	var got map[string]any
	_ = json.Unmarshal([]byte(text), &got)
	if got["error"] != "permission_denied" {
		t.Errorf("read-only identity must be denied; got %+v", got)
	}
}

func TestSchemaWebhooksListOmitsSecret(t *testing.T) {
	_, srv := newWebhookFixture(t)
	subscribe, _ := srv.GetTool("schema_diff_subscribe")
	_, _ = subscribe.Execute(adminCtx(), mustJSON(map[string]any{"url": "https://hooks.example.com/scry"}))

	list, _ := srv.GetTool("schema_webhooks_list")
	out, _ := list.Execute(adminCtx(), mustJSON(map[string]any{}))
	text, _ := out.(string)
	if strings.Contains(text, `"secret"`) {
		t.Errorf("List response must not expose secret; got %q", text)
	}
}

func TestSchemaWebhooksRemoveRoundTrip(t *testing.T) {
	_, srv := newWebhookFixture(t)
	subscribe, _ := srv.GetTool("schema_diff_subscribe")
	out, _ := subscribe.Execute(adminCtx(), mustJSON(map[string]any{"url": "https://hooks.example.com/scry"}))
	var registered map[string]any
	_ = json.Unmarshal([]byte(out.(string)), &registered)
	id := registered["id"]

	remove, _ := srv.GetTool("schema_webhooks_remove")
	out, _ = remove.Execute(adminCtx(), mustJSON(map[string]any{"id": id}))
	text, _ := out.(string)
	var got map[string]any
	_ = json.Unmarshal([]byte(text), &got)
	if got["removed_id"] == nil {
		t.Errorf("expected removed_id in response, got %+v", got)
	}

	// Re-remove → not_found
	out, _ = remove.Execute(adminCtx(), mustJSON(map[string]any{"id": id}))
	text, _ = out.(string)
	_ = json.Unmarshal([]byte(text), &got)
	if got["error"] != "not_found" {
		t.Errorf("re-remove must return not_found; got %+v", got)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
