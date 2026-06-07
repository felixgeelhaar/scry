package server

import (
	"context"
	"encoding/json"

	mcp "go.klarlabs.de/mcp"
)

// requireAdmin returns "" when the caller may invoke a destructive
// tool, or a JSON permission_denied envelope (which the handler
// returns verbatim to the agent) otherwise.
//
// Decision rules:
//
//   - No identity in context: caller is local (stdio, or HTTP without
//     --serve-auth set). Allow — there's no remote caller to
//     distinguish from "the operator".
//   - Identity ID is identityAdmin: allow.
//   - Anything else (identityReadOnly, third-party identities): deny.
//
// Distinct from the upstream's 401 contract: that's a credential
// problem the agent can recover from via auth_login. This is a
// *policy* problem — the agent's transport token doesn't carry the
// scope. No amount of retrying will fix it; the operator must hand
// the agent a higher-scoped token.
func requireAdmin(ctx context.Context, tool string) string {
	id := mcp.IdentityFromContext(ctx)
	if id == nil {
		return ""
	}
	// clients.yml caller? Defer to its tool scope rather than
	// the admin/read-only flag.
	if scope := scopeFor(ctx); scope != nil {
		if scope.MayCallTool(tool) {
			return ""
		}
		enc, _ := json.Marshal(map[string]any{
			"error":            "permission_denied",
			"hint":             "this client is not authorised to call this tool in clients.yml",
			"tool":             tool,
			"required_scope":   "clients.yml tool grant for " + tool,
			"presented_client": scope.Name,
		})
		return string(enc)
	}
	if id.ID == identityAdmin {
		return ""
	}
	enc, _ := json.Marshal(map[string]any{
		"error":            "permission_denied",
		"hint":             "this client's transport token grants read-only access; ask the operator to issue an admin token (see --serve-auth)",
		"tool":             tool,
		"required_scope":   "admin",
		"presented_client": id.Name,
	})
	return string(enc)
}

// requireToolScope guards read-only tools when clients.yml is in
// play — a clients.yml caller with a narrower tools list (e.g.
// schema_search-only) must be denied other tools. Returns "" when
// allowed.
//
// For the legacy --serve-auth path this is a no-op (every read-only
// tool is allowed to both admin + read-only tokens).
func requireToolScope(ctx context.Context, tool string) string {
	scope := scopeFor(ctx)
	if scope == nil {
		return ""
	}
	if scope.MayCallTool(tool) {
		return ""
	}
	enc, _ := json.Marshal(map[string]any{
		"error":            "permission_denied",
		"hint":             "this client is not authorised to call this tool in clients.yml",
		"tool":             tool,
		"required_scope":   "clients.yml tool grant for " + tool,
		"presented_client": scope.Name,
	})
	return string(enc)
}

// requireServerScope guards the per-call `server` argument when
// clients.yml is in play. Empty server = single-upstream default
// routing, always allowed.
func requireServerScope(ctx context.Context, server string) string {
	scope := scopeFor(ctx)
	if scope == nil {
		return ""
	}
	if scope.MayCallServer(server) {
		return ""
	}
	enc, _ := json.Marshal(map[string]any{
		"error":            "permission_denied",
		"hint":             "this client is not authorised to target the requested server in clients.yml",
		"server":           server,
		"presented_client": scope.Name,
	})
	return string(enc)
}
