package server

import (
	"context"

	mcpmw "github.com/felixgeelhaar/mcp-go/middleware"
	"github.com/felixgeelhaar/mcp-go/protocol"
)

// toolListFilter is the middleware that strips tools the caller's
// scope doesn't permit out of the tools/list response. Without it,
// a read-only client (whether from --serve-auth-readonly or
// clients.yml) would see the full tool catalog and discover that
// query_execute / auth_login exist before getting denied at call
// time. Hiding them at list time is the cleaner contract — the
// agent's planner picks from tools the agent can actually run.
//
// Implementation: pass requests through unchanged. On the tools/list
// response, walk result.tools, drop any entry whose Name fails the
// scope check. Other methods pass through untouched.
func toolListFilter() mcpmw.Middleware {
	return func(next mcpmw.HandlerFunc) mcpmw.HandlerFunc {
		return func(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
			resp, err := next(ctx, req)
			if err != nil || resp == nil || resp.Result == nil {
				return resp, err
			}
			if req.Method != protocol.MethodToolsList {
				return resp, err
			}
			// Result is map[string]any{"tools": []map[string]any{...}}
			// per mcp-go's handler. Defensive: tolerate other
			// shapes (forward-compat with mcp-go schema changes)
			// by leaving the response untouched when our type
			// assertion fails.
			m, ok := resp.Result.(map[string]any)
			if !ok {
				return resp, err
			}
			rawTools, ok := m["tools"].([]map[string]any)
			if !ok {
				return resp, err
			}
			filtered := make([]map[string]any, 0, len(rawTools))
			for _, t := range rawTools {
				name, _ := t["name"].(string)
				if !mayList(ctx, name) {
					continue
				}
				filtered = append(filtered, t)
			}
			m["tools"] = filtered
			return resp, err
		}
	}
}

// mayList runs the same scope check used by handler-time guards.
// Returns true when the caller may both *call* the tool AND target
// at least one upstream the tool needs.
//
// Tools that don't take a `server` parameter (list_servers,
// auth_status, auth_login, gate_status, gate_chain) are gated only
// by the tool grant.
func mayList(ctx context.Context, name string) bool {
	if denied := requireToolScope(ctx, name); denied != "" {
		return false
	}
	// Destructive tools: require admin under the --serve-auth
	// legacy contract (clients.yml callers were already filtered
	// by requireToolScope above).
	switch name {
	case "query_execute", "auth_login":
		if denied := requireAdmin(ctx, name); denied != "" {
			return false
		}
	}
	return true
}
