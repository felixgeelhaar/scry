package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	mcp "github.com/felixgeelhaar/mcp-go"
	mcpmw "github.com/felixgeelhaar/mcp-go/middleware"

	"github.com/felixgeelhaar/scry/internal/auth"
	"github.com/felixgeelhaar/scry/internal/gate"
)

// withDenyScope swaps the package-level scopeRegistry to install a
// client identity with the provided deny patterns and returns a
// context carrying the matching MCP identity. Test cleanup restores
// the previous registry.
func withDenyScope(t *testing.T, denyPatterns []string) context.Context {
	t.Helper()
	scope, err := auth.Client{
		Name: "limited", Token: "limited-token",
		Tools: []string{"*"}, Servers: []string{"*"},
		DenyFields: denyPatterns,
	}.BuildScope(nil)
	if err != nil {
		t.Fatalf("BuildScope: %v", err)
	}
	prev := scopeRegistry
	t.Cleanup(func() { scopeRegistry = prev })
	scopeRegistry = map[string]*auth.Scope{"limited-token": &scope}
	return mcp.ContextWithIdentity(context.Background(), &mcpmw.Identity{
		ID: "limited-token", Name: "limited",
	})
}

func TestQueryExecuteDenyFieldsTrips(t *testing.T) {
	// Default fixture's introspection exposes Query{ping,count}.
	// Add a deny rule that blocks Query.ping; any selection of
	// ping must return permission_denied.
	f := newFixture(t, nil, gate.Policy{}, Config{CostCeiling: 1000})
	ctx := withDenyScope(t, []string{"Query.ping"})

	got, _ := f.call(ctx, map[string]any{"query": "{ ping }"})
	if got["error"] != "permission_denied" {
		t.Fatalf("envelope error = %v, want permission_denied (got %+v)", got["error"], got)
	}
	// denied_fields array must name the violating field + pattern.
	denied, ok := got["denied_fields"].([]any)
	if !ok || len(denied) == 0 {
		t.Fatalf("expected denied_fields list, got %+v", got)
	}
	first, _ := denied[0].(map[string]any)
	if first["field"] != "ping" || first["pattern"] != "Query.ping" {
		t.Errorf("denied_fields[0] = %+v, want field=ping pattern=Query.ping", first)
	}
}

func TestQueryExecuteDenyFieldsPassthrough(t *testing.T) {
	// Deny rule that doesn't match anything in the query → must
	// allow through to the upstream.
	f := newFixture(t, nil, gate.Policy{}, Config{CostCeiling: 1000})
	ctx := withDenyScope(t, []string{"User.email"})

	_, raw := f.call(ctx, map[string]any{"query": "{ ping }"})
	if !strings.Contains(raw, `"pong"`) {
		t.Errorf("non-matching deny rule must passthrough; got %q", raw)
	}
}

func TestQueryExecuteDenyFieldsAliasDoesNotBypass(t *testing.T) {
	f := newFixture(t, nil, gate.Policy{}, Config{CostCeiling: 1000})
	ctx := withDenyScope(t, []string{"Query.ping"})
	// Alias renames JSON key — must NOT slip past the deny rule.
	got, _ := f.call(ctx, map[string]any{"query": "{ p: ping }"})
	if got["error"] != "permission_denied" {
		t.Errorf("alias must not bypass deny; got %+v", got)
	}
}

func TestQueryExecuteDenyFieldsAdminBypassesViaNoScope(t *testing.T) {
	// No identity = no clients.yml scope in play. The legacy
	// admin path should NOT be subject to deny rules — those
	// only apply to clients.yml callers. Fixture sends no
	// identity so this is the "stdio local" case.
	f := newFixture(t, nil, gate.Policy{}, Config{CostCeiling: 1000})
	// scopeRegistry stays empty — no scope wired for this ctx.
	_, raw := f.call(context.Background(), map[string]any{"query": "{ ping }"})
	if !strings.Contains(raw, `"pong"`) {
		t.Errorf("no-scope caller must passthrough; got %q", raw)
	}
}

func TestQueryValidateDenyFields(t *testing.T) {
	// query_validate should also enforce deny rules so agents
	// discover violations BEFORE spending execute budget.
	f := newSchemaToolsFixture(t, nil)
	ctx := withDenyScope(t, []string{"Query.ping"})

	tool, _ := f.srv.GetTool("query_validate")
	in, _ := json.Marshal(map[string]any{"query": "{ ping }"})
	out, err := tool.Execute(ctx, in)
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	text, _ := out.(string)
	var got map[string]any
	_ = json.Unmarshal([]byte(text), &got)
	if got["error"] != "permission_denied" {
		t.Errorf("query_validate must surface deny at validate time; got %+v", got)
	}
}

func TestQueryExecuteDenyFieldsAuditChainRecordsPermissionDenied(t *testing.T) {
	f := newFixture(t, nil, gate.Policy{}, Config{CostCeiling: 1000})
	ctx := withDenyScope(t, []string{"Query.ping"})
	_, _ = f.call(ctx, map[string]any{"query": "{ ping }"})

	// gate.Record uses session id from context identity.
	chain := f.gate.Chain(gate.SessionID("default:limited-token"))
	if len(chain) == 0 {
		t.Fatalf("expected one evidence record, got 0")
	}
	last := chain[len(chain)-1]
	if last.Outcome != "permission_denied" {
		t.Errorf("audit outcome = %q, want permission_denied", last.Outcome)
	}
	if last.Effect != gate.EffectRead {
		t.Errorf("audit effect = %q, want read (upstream never reached)", last.Effect)
	}
}

// TestQueryExecuteDenyFieldsWildcardField confirms `*.password`
// fires on any type that exposes a `password` field. The default
// fixture schema doesn't have one, so we use a star-against-actual
// ping deny that should hit.
func TestQueryExecuteDenyFieldsWildcardField(t *testing.T) {
	f := newFixture(t, nil, gate.Policy{}, Config{CostCeiling: 1000})
	ctx := withDenyScope(t, []string{"*.ping"})
	got, _ := f.call(ctx, map[string]any{"query": "{ ping }"})
	if got["error"] != "permission_denied" {
		t.Errorf("wildcard *.ping should match; got %+v", got)
	}
}

// Confirms the suppression of duplicate (type, field) selections —
// repeating ping shouldn't render multiple denied_fields entries.
func TestQueryExecuteDenyFieldsDedupes(t *testing.T) {
	f := newFixture(t, nil, gate.Policy{}, Config{CostCeiling: 1000})
	ctx := withDenyScope(t, []string{"Query.ping"})
	// Same field selected twice via aliases.
	got, _ := f.call(ctx, map[string]any{"query": "{ a: ping b: ping }"})
	denied, _ := got["denied_fields"].([]any)
	if len(denied) != 1 {
		t.Errorf("expected 1 deduped entry, got %d (%+v)", len(denied), denied)
	}
}

// Helper to satisfy the unused-import check when this file's tests
// reference http types only transitively via shared fixture code.
var _ http.Handler
