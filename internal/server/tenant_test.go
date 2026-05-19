package server

import (
	"context"
	"testing"

	mcp "github.com/felixgeelhaar/mcp-go"
	mcpmw "github.com/felixgeelhaar/mcp-go/middleware"

	"github.com/felixgeelhaar/scry/internal/auth"
)

func TestTenantFromContextDefaultsForStdio(t *testing.T) {
	// No identity, no clients.yml scope → default tenant.
	got := TenantFromContext(context.Background())
	if got != auth.DefaultTenant {
		t.Errorf("stdio caller should get %q, got %q", auth.DefaultTenant, got)
	}
}

func TestTenantFromContextScopedToClient(t *testing.T) {
	prev := scopeRegistry
	t.Cleanup(func() { scopeRegistry = prev })
	scope, err := auth.Client{
		Name: "acme-dashboard", Token: "tok",
		Tools: []string{"*"}, Servers: []string{"*"},
		Tenant: "acme",
	}.BuildScope(nil)
	if err != nil {
		t.Fatalf("BuildScope: %v", err)
	}
	scopeRegistry = map[string]*auth.Scope{"tok": &scope}
	ctx := mcp.ContextWithIdentity(context.Background(), &mcpmw.Identity{
		ID: "tok", Name: "acme-dashboard",
	})
	if got := TenantFromContext(ctx); got != "acme" {
		t.Errorf("client-scoped tenant = %q, want acme", got)
	}
}

func TestTenantFromContextFallsBackForUnmappedIdentity(t *testing.T) {
	prev := scopeRegistry
	t.Cleanup(func() { scopeRegistry = prev })
	scopeRegistry = map[string]*auth.Scope{}
	ctx := mcp.ContextWithIdentity(context.Background(), &mcpmw.Identity{
		ID: "unknown-token", Name: "unknown",
	})
	// Unknown identity → scopeFor returns nil → DefaultTenant.
	if got := TenantFromContext(ctx); got != auth.DefaultTenant {
		t.Errorf("unmapped identity should default to %q, got %q", auth.DefaultTenant, got)
	}
}
