package server

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"go.klarlabs.de/mcp/protocol"
)

// TestIdentityContextFnResolvesBearerToken confirms the HTTP request
// hook maps a valid Authorization: Bearer header to the configured
// identity, and stashes nothing for missing/unknown tokens.
func TestIdentityContextFnResolvesBearerToken(t *testing.T) {
	want := &Identity{ID: identityAdmin, Name: identityAdmin}
	fn := identityContextFn(map[string]*Identity{"admin-tok": want})

	cases := []struct {
		name   string
		header string
		want   *Identity
	}{
		{"valid bearer", "Bearer admin-tok", want},
		{"case-insensitive scheme", "bearer admin-tok", want},
		{"unknown token", "Bearer nope", nil},
		{"no header", "", nil},
		{"malformed", "admin-tok", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := http.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			ctx := fn(context.Background(), r)
			got := identityFromContext(ctx)
			if got != tc.want {
				t.Fatalf("identity = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBearerGateRejectsUnauthenticated confirms bearerGate reproduces
// the removed mcp.BearerAuth authentication contract: handshake methods
// pass without an identity, every other method without an identity is
// rejected with CodeUnauthorized, and an authenticated call proceeds.
func TestBearerGateRejectsUnauthenticated(t *testing.T) {
	reached := false
	next := func(_ context.Context, _ *protocol.Request) (*protocol.Response, error) {
		reached = true
		return &protocol.Response{}, nil
	}
	gate := bearerGate()(next)

	// Handshake method: allowed without identity.
	reached = false
	if _, err := gate(context.Background(), &protocol.Request{Method: protocol.MethodInitialize}); err != nil {
		t.Fatalf("handshake should pass, got %v", err)
	}
	if !reached {
		t.Fatal("handshake should reach the next handler")
	}

	// Protected method, no identity: rejected with CodeUnauthorized.
	reached = false
	_, err := gate(context.Background(), &protocol.Request{Method: protocol.MethodToolsCall})
	if err == nil {
		t.Fatal("protected method without identity should be rejected")
	}
	var mcpErr *protocol.Error
	if !errors.As(err, &mcpErr) || mcpErr.Code != protocol.CodeUnauthorized {
		t.Fatalf("want CodeUnauthorized, got %v", err)
	}
	if reached {
		t.Fatal("rejected call must not reach the next handler")
	}

	// Protected method with identity: allowed.
	reached = false
	ctx := contextWithIdentity(context.Background(), &Identity{ID: identityAdmin, Name: identityAdmin})
	if _, err := gate(ctx, &protocol.Request{Method: protocol.MethodToolsCall}); err != nil {
		t.Fatalf("authenticated call should pass, got %v", err)
	}
	if !reached {
		t.Fatal("authenticated call should reach the next handler")
	}
}
