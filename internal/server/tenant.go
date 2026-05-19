package server

import (
	"context"

	"github.com/felixgeelhaar/scry/internal/auth"
)

// TenantFromContext returns the tenant the current request maps to.
// Resolution order:
//
//  1. clients.yml-driven scope's Tenant field (when the caller
//     presented a credential that resolved into a Scope).
//  2. auth.DefaultTenant — for stdio + legacy --serve-auth callers
//     that have no per-client identity.
//
// HTTP-header-driven tenant routing (X-Scry-Tenant) is intentionally
// NOT supported in v0.7: scry's threat model assumes the bearer
// token IS the tenant identity. Allowing an unauthenticated header
// to override the token would let any caller pivot to any tenant.
// Operators that need per-tenant routing pre-register one client
// identity per tenant in clients.yml.
func TenantFromContext(ctx context.Context) string {
	if scope := scopeFor(ctx); scope != nil {
		return scope.TenantOf()
	}
	return auth.DefaultTenant
}
