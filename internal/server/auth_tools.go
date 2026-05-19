package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	mcp "github.com/felixgeelhaar/mcp-go"

	"github.com/felixgeelhaar/scry/internal/auth"
)

// registerAuthTools exposes two read-only credential tools to the
// agent:
//
//	auth_status         — list servers + traffic light (no secrets)
//	auth_login(server)  — recovery hint when a token has expired
//
// auth_login is intentionally non-interactive in v0: bearer tokens
// don't have a programmatic refresh flow, so the tool returns the
// exact CLI command the operator needs to run rather than trying to
// drive the login over MCP. Device-code and PKCE flows that the
// agent CAN drive are listed in docs/auth-design.md as Phase 2.
// Returns an error for symmetry with the other register*Tools
// funcs; today no tool wiring can fail, but keeping the signature
// stable means a future tool that *does* validate at registration
// time slots in without churning every call site.
//
//nolint:unparam // see comment above
func registerAuthTools(srv *mcp.Server) error {
	type StatusInput struct {
		Server string `json:"server,omitempty" jsonschema:"description=optional server name; omit to list all"`
	}
	srv.Tool("auth_status").
		Description(descAuthStatus).
		Handler(func(_ context.Context, in StatusInput) (string, error) {
			s, err := loadServers()
			if err != nil {
				return renderAuthError("auth_store_unavailable", err.Error()), nil
			}
			entries := s.StatusAll(time.Now())
			if in.Server != "" {
				filtered := entries[:0]
				for _, e := range entries {
					if e.Name == in.Server {
						filtered = append(filtered, e)
					}
				}
				entries = filtered
				if len(entries) == 0 {
					return renderAuthError("not_found",
						fmt.Sprintf("server %q is not registered — list candidates with auth_status", in.Server)), nil
				}
			}
			enc, _ := json.MarshalIndent(map[string]any{"servers": entries}, "", "  ")
			return string(enc), nil
		})

	type LoginInput struct {
		Server string `json:"server" jsonschema:"required,description=server name to authenticate"`
	}
	srv.Tool("auth_login").
		Description(descAuthLogin).
		Handler(func(ctx context.Context, in LoginInput) (string, error) {
			if denied := requireAdmin(ctx, "auth_login"); denied != "" {
				return denied, nil
			}
			s, err := loadServers()
			if err != nil {
				return renderAuthError("auth_store_unavailable", err.Error()), nil
			}
			srv, ok := s.Servers[in.Server]
			if !ok {
				return renderAuthError("not_found",
					fmt.Sprintf("server %q is not registered — operator must run `scry servers add %s --upstream <url>` first", in.Server, in.Server)), nil
			}
			// v0 supports only bearer. Surface the operator
			// instruction inline so the agent can relay it to the
			// human via the MCP host UI.
			enc, _ := json.MarshalIndent(map[string]any{
				"status":    string(s.Status(in.Server, time.Now())),
				"server":    in.Server,
				"upstream":  srv.Upstream,
				"auth_type": "bearer",
				"action":    "operator_run_cli",
				"cli":       fmt.Sprintf("scry auth login %s --token <T>", in.Server),
				"reason":    "v0 supports non-interactive bearer login only; OAuth device-code is Phase 2 (docs/auth-design.md).",
			}, "", "  ")
			return string(enc), nil
		})

	return nil
}

// loadServers reads servers.yml from the default location. Wrapped
// here so the MCP handlers stay one-liners.
func loadServers() (*auth.Servers, error) {
	p, err := auth.DefaultPath()
	if err != nil {
		return nil, err
	}
	return auth.Load(p)
}

// renderAuthError matches the {error, hint} contract used elsewhere.
func renderAuthError(code, hint string) string {
	enc, _ := json.Marshal(map[string]string{"error": code, "hint": hint})
	return string(enc)
}
