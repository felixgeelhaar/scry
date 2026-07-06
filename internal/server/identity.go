package server

import (
	"context"
	"net/http"
	"strings"

	mcpmw "go.klarlabs.de/mcp/middleware"
	"go.klarlabs.de/mcp/protocol"
)

// Identity is scry's own request-scoped caller identity, derived from
// the transport bearer token.
//
// mcp-go v1.19 removed all in-library authentication (the framework
// ships no Identity type and never handles credentials); auth is now
// caller-owned. scry therefore owns identity end to end: it is
// populated from the HTTP transport by identityContextFn (via
// transport.WithRequestContextFn) and read back with
// identityFromContext — replacing the removed mcp.Identity /
// mcp.IdentityFromContext / mcp.ContextWithIdentity.
//
// Field contract (matched by the per-tool guards):
//
//   - --serve-auth callers: ID == Name == identityAdmin/identityReadOnly.
//     requireAdmin keys off ID == identityAdmin.
//   - clients.yml callers: ID == the resolved token, Name == the
//     friendly client name. scopeFor keys scopeRegistry off ID (the
//     token), and logs / session IDs surface Name.
type Identity struct {
	// ID is the scopeFor lookup key: the resolved token for
	// clients.yml callers, or the admin/read-only label for
	// --serve-auth callers.
	ID string
	// Name is the friendly label surfaced in logs + session IDs.
	Name string
}

type identityKey struct{}

// contextWithIdentity attaches the caller identity derived from the
// transport credential.
func contextWithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// identityFromContext returns the caller identity the transport hook
// attached, or nil for local (stdio / unauthenticated) callers.
func identityFromContext(ctx context.Context) *Identity {
	id, _ := ctx.Value(identityKey{}).(*Identity)
	return id
}

// identityContextFn returns a transport.WithRequestContextFn hook that
// reads the HTTP `Authorization: Bearer <token>` header, resolves it
// against the configured token→identity map, and stashes the matching
// *Identity in the request context. Unknown or missing tokens stash
// nothing; bearerGate turns that into a rejection for protected
// methods.
//
// This is the v1.21 replacement for the removed mcp.BearerAuth
// middleware, whose token→identity derivation could read the
// Authorization header from request metadata — plumbing the transport
// no longer performs. Header extraction must now happen at the HTTP
// transport layer, which is exactly what this hook does.
func identityContextFn(identities map[string]*Identity) func(context.Context, *http.Request) context.Context {
	return func(ctx context.Context, r *http.Request) context.Context {
		tok := bearerToken(r.Header.Get("Authorization"))
		if tok == "" {
			return ctx
		}
		if id, ok := identities[tok]; ok {
			return contextWithIdentity(ctx, id)
		}
		return ctx
	}
}

// bearerToken extracts the token from a "Bearer <token>" Authorization
// header value, or "" when the header is absent or malformed.
func bearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// handshakeMethods are exempt from bearer authentication — the MCP
// handshake must complete before a client can present richer context.
// Mirrors the removed mcp.BearerAuth default skip set
// (initialize + ping) plus notifications/initialized.
var handshakeMethods = map[string]bool{
	protocol.MethodInitialize:  true,
	protocol.MethodInitialized: true,
	protocol.MethodPing:        true,
}

// bearerGate reproduces the *authentication* half of the removed
// mcp.BearerAuth: when a serve credential is configured, every method
// outside the handshake set requires a caller identity (populated by
// identityContextFn from the transport bearer token). A missing or
// unknown token yields protocol.CodeUnauthorized, preserving the old
// 401 contract that an agent can recover from by presenting a valid
// credential.
//
// Authorization (admin vs read-only vs clients.yml scope) stays in the
// per-tool guards (requireAdmin / requireToolScope / requireServerScope),
// which continue to key off identityFromContext.
//
// Only added to the middleware stack when at least one serve token is
// configured; without it a nil identity means "local caller" and the
// guards allow by default.
func bearerGate() mcpmw.Middleware {
	return func(next mcpmw.HandlerFunc) mcpmw.HandlerFunc {
		return func(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
			if handshakeMethods[req.Method] {
				return next(ctx, req)
			}
			if identityFromContext(ctx) == nil {
				return nil, &protocol.Error{
					Code:    protocol.CodeUnauthorized,
					Message: "authentication required",
				}
			}
			return next(ctx, req)
		}
	}
}
