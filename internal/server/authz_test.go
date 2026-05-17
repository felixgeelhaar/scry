package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcp "github.com/felixgeelhaar/mcp-go"
	mcpmw "github.com/felixgeelhaar/mcp-go/middleware"
)

func TestRequireAdminAllowsLocalCaller(t *testing.T) {
	// stdio / no-auth: identity is nil. Must allow.
	if got := requireAdmin(context.Background(), "query_execute"); got != "" {
		t.Errorf("local caller should be allowed, got envelope %q", got)
	}
}

func TestRequireAdminAllowsAdminIdentity(t *testing.T) {
	ctx := mcp.ContextWithIdentity(context.Background(), &mcpmw.Identity{
		ID: identityAdmin, Name: identityAdmin,
	})
	if got := requireAdmin(ctx, "query_execute"); got != "" {
		t.Errorf("admin identity should be allowed, got %q", got)
	}
}

func TestRequireAdminDeniesReadOnlyIdentity(t *testing.T) {
	ctx := mcp.ContextWithIdentity(context.Background(), &mcpmw.Identity{
		ID: identityReadOnly, Name: identityReadOnly,
	})
	got := requireAdmin(ctx, "query_execute")
	if got == "" {
		t.Fatalf("read-only identity should be denied")
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(got), &env); err != nil {
		t.Fatalf("denial envelope not JSON: %v", err)
	}
	if env["error"] != "permission_denied" {
		t.Errorf("error code = %v, want permission_denied", env["error"])
	}
	if env["tool"] != "query_execute" {
		t.Errorf("tool field = %v, want query_execute", env["tool"])
	}
	if env["required_scope"] != "admin" {
		t.Errorf("required_scope = %v, want admin", env["required_scope"])
	}
}

func TestRequireAdminDeniesUnknownIdentity(t *testing.T) {
	ctx := mcp.ContextWithIdentity(context.Background(), &mcpmw.Identity{
		ID: "random-third-party", Name: "stranger",
	})
	if got := requireAdmin(ctx, "auth_login"); got == "" {
		t.Errorf("unknown identity should be denied (defense in depth)")
	}
}

func TestBuildServeOptsRejectsDuplicateTokens(t *testing.T) {
	// Pasting the same token into both flags would silently let
	// read-only callers bypass authz (the map overwrite would
	// keep whichever Go iterated last). Surface as a boot error.
	cfg := Config{
		ServeAuthToken:         "literal-shared-secret",
		ServeAuthTokenReadOnly: "literal-shared-secret",
	}
	if _, err := buildServeOpts(cfg); err == nil || !strings.Contains(err.Error(), "same token") {
		t.Errorf("expected duplicate-token error, got %v", err)
	}
}

func TestBuildServeOptsAcceptsDistinctTokens(t *testing.T) {
	cfg := Config{
		ServeAuthToken:         "admin-secret",
		ServeAuthTokenReadOnly: "readonly-secret",
	}
	opts, err := buildServeOpts(cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// 3 middleware entries: OTel + BearerAuth + tool-list filter.
	if len(opts) != 3 {
		t.Errorf("expected 3 middleware ServeOptions (OTel + BearerAuth + tool filter), got %d", len(opts))
	}
}

func TestBuildServeOptsNoTokensReturnsOTelOnly(t *testing.T) {
	opts, err := buildServeOpts(Config{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// 2 middleware entries: OTel + tool-list filter. Both always
	// install — they're no-ops when their inputs are empty.
	if len(opts) != 2 {
		t.Errorf("expected 2 middleware (OTel + tool filter) when no auth tokens, got %d", len(opts))
	}
}
