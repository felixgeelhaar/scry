package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	mcp "go.klarlabs.de/mcp"

	"github.com/felixgeelhaar/scry/internal/runtime"
	"github.com/felixgeelhaar/scry/internal/schema"
)

// registerSchemaTools wires the four read-only schema tools:
//
//	schema_search    — NL → ranked snippets
//	schema_get       — full SDL for a named type
//	query_validate   — static validation against the live schema
//	query_cost       — complexity estimate before execute
//
// All four take an optional `server` argument. When omitted and
// exactly one upstream is configured, the request routes to that
// one. With multiple upstreams configured, omitting `server`
// returns an unknown_server envelope listing the valid options so
// the agent can pick.
//
// NeighborsResult is the structured output of schema_neighbors: the
// incoming + outgoing type-reference edges for a named type.
type NeighborsResult struct {
	Type     string        `json:"type"`
	Server   string        `json:"server"`
	Incoming []schema.Edge `json:"incoming"`
	Outgoing []schema.Edge `json:"outgoing"`
}

//nolint:unparam // symmetry with other register*Tools — future wiring may fail
func registerSchemaTools(srv *mcp.Server, cfg Config, mgr *runtime.Manager) error {
	_ = cfg
	type SearchInput struct {
		Server string `json:"server,omitempty" jsonschema:"description=upstream server name (omit when only one is configured)"`
		Query  string `json:"query" jsonschema:"required,description=natural-language description of the data the agent needs"`
		Limit  int    `json:"limit,omitempty" jsonschema:"description=max snippets to return; default 10,maximum=50"`
	}
	srv.Tool("schema_search").
		Description(descSchemaSearch).
		Handler(func(ctx context.Context, in SearchInput) (string, error) {
			entry, errResp := resolveServer(in.Server, mgr)
			if errResp != "" {
				return errResp, nil
			}
			results, err := entry.Store.Search(ctx, in.Query, in.Limit)
			if err != nil {
				return "", fmt.Errorf("schema_search: %w", err)
			}
			return renderSearchResults(entry.Name, in.Query, results), nil
		})

	type GetInput struct {
		Server string `json:"server,omitempty" jsonschema:"description=upstream server name (omit when only one is configured)"`
		Name   string `json:"name" jsonschema:"required,description=type or field name (e.g. 'Customer' or 'Query.customer')"`
	}
	srv.Tool("schema_get").
		Description(descSchemaGet).
		Handler(func(ctx context.Context, in GetInput) (string, error) {
			entry, errResp := resolveServer(in.Server, mgr)
			if errResp != "" {
				return errResp, nil
			}
			sdl, err := entry.Store.GetSDL(ctx, in.Name)
			if errors.Is(err, schema.ErrNotFound) {
				return renderError("not_found",
					fmt.Sprintf("no schema unit named %q on server %q — try schema_search to find the right name", in.Name, entry.Name)), nil
			}
			if err != nil {
				return "", fmt.Errorf("schema_get: %w", err)
			}
			return sdl, nil
		})

	type ValidateInput struct {
		Server string `json:"server,omitempty"`
		Query  string `json:"query" jsonschema:"required,description=GraphQL query string to validate"`
	}
	srv.Tool("query_validate").
		Description(descQueryValidate).
		Handler(func(ctx context.Context, in ValidateInput) (string, error) {
			entry, errResp := resolveServer(in.Server, mgr)
			if errResp != "" {
				return errResp, nil
			}
			sdl, err := entry.Store.GetMeta(ctx, "full_sdl")
			if err != nil {
				return renderError("schema_unavailable",
					"schema index has no SDL — wait for the next refresh or restart with --refresh"), nil
			}
			errs := schema.ValidateQuery(sdl, in.Query)
			if len(errs) > 0 {
				return renderValidation(errs), nil
			}
			// Field-level authz: surface deny hits at validate
			// time so agents discover policy violations before
			// they spend execute budget.
			if denied := checkDeniedFields(ctx, sdl, in.Query); denied != "" {
				return denied, nil
			}
			return renderValidation(nil), nil
		})

	type NeighborsInput struct {
		Server string `json:"server,omitempty" jsonschema:"description=upstream server name (omit when only one is configured)"`
		Name   string `json:"name" jsonschema:"required,description=type name to look up neighbors for"`
		Limit  int    `json:"limit,omitempty" jsonschema:"description=cap on incoming + outgoing edges; clamped to [1,50]"`
	}
	srv.Tool("schema_neighbors").
		Description(descSchemaNeighbors).
		OutputSchema(NeighborsResult{}).
		Handler(func(ctx context.Context, in NeighborsInput) (any, error) {
			entry, errResp := resolveServer(in.Server, mgr)
			if errResp != "" {
				return errResp, nil
			}
			set, err := entry.Store.Neighbors(ctx, in.Name, in.Limit)
			if err != nil {
				return "", fmt.Errorf("schema_neighbors: %w", err)
			}
			if len(set.Incoming) == 0 && len(set.Outgoing) == 0 {
				return renderError("not_found",
					fmt.Sprintf("no edges recorded for %q on server %q — confirm the name via schema_search or schema_get", in.Name, entry.Name)), nil
			}
			return NeighborsResult{
				Type:     in.Name,
				Server:   entry.Name,
				Incoming: set.Incoming,
				Outgoing: set.Outgoing,
			}, nil
		})

	type DiffInput struct {
		Server string `json:"server,omitempty"`
	}
	srv.Tool("schema_diff").
		Description(descSchemaDiff).
		Handler(func(ctx context.Context, in DiffInput) (string, error) {
			entry, errResp := resolveServer(in.Server, mgr)
			if errResp != "" {
				return errResp, nil
			}
			raw, err := entry.Store.GetMeta(ctx, "last_diff")
			if err != nil {
				return renderError("no_diff",
					"no schema diff recorded yet — diffs are computed on each background refresh after the first one"), nil
			}
			return raw, nil
		})

	type CostInput struct {
		Server string `json:"server,omitempty"`
		Query  string `json:"query" jsonschema:"required,description=GraphQL query string to estimate"`
	}
	srv.Tool("query_cost").
		Description(descQueryCost).
		OutputSchema(schema.CostReport{}).
		Handler(func(ctx context.Context, in CostInput) (any, error) {
			entry, errResp := resolveServer(in.Server, mgr)
			if errResp != "" {
				return errResp, nil
			}
			sdl, err := entry.Store.GetMeta(ctx, "full_sdl")
			if err != nil {
				return renderError("schema_unavailable",
					"schema index has no SDL — wait for the next refresh or restart with --refresh"), nil
			}
			rpt, vErrs := schema.EstimateCost(sdl, in.Query)
			if len(vErrs) > 0 {
				// Validation failed: surface the errors as an
				// {error, hint, errors} envelope (text only) so the
				// caller doesn't need a separate query_validate call.
				return renderCostErrors(vErrs), nil
			}
			// Happy path: the typed CostReport is promoted to
			// structuredContent via the declared output schema.
			return rpt, nil
		})

	return nil
}

// resolveServer dispatches a server-name argument to a Manager
// Entry. Empty name routes to the only configured server when
// there is exactly one; otherwise it returns an unknown_server
// envelope listing valid names so the agent can pick.
func resolveServer(name string, mgr *runtime.Manager) (*runtime.Entry, string) {
	if name == "" {
		if def, ok := mgr.DefaultServer(); ok {
			e, _ := mgr.Get(def)
			return e, ""
		}
		return nil, renderUnknownServerError("", mgr)
	}
	e, err := mgr.Get(name)
	if err != nil {
		return nil, renderUnknownServerError(name, mgr)
	}
	return e, ""
}

// renderUnknownServerError returns a structured envelope describing
// the failure plus the list of valid server names so the agent
// has everything it needs to retry without an extra call.
func renderUnknownServerError(name string, mgr *runtime.Manager) string {
	list := mgr.List()
	hint := fmt.Sprintf("specify one of: %s (call list_servers for details)", strings.Join(list, ", "))
	if name == "" {
		hint = "multiple upstreams configured — " + hint
	}
	enc, _ := json.Marshal(map[string]any{
		"error":   "unknown_server",
		"hint":    hint,
		"server":  name,
		"servers": list,
	})
	return string(enc)
}

// renderSearchResults formats hits as a markdown table followed by
// a JSON appendix. Matches TokenOps' Tier-1 rendering pattern so
// most MCP clients (Claude Desktop, Cursor) render styled tables
// while agents that re-parse can read the JSON tail.
func renderSearchResults(server, query string, results []schema.SearchResult) string {
	var b strings.Builder
	if len(results) == 0 {
		fmt.Fprintf(&b, "No schema units on %q match %q.\n\nTip: try broader terms or singular forms (e.g. \"customer\" not \"customers's address\").", server, query)
		return b.String()
	}
	// Federated upstreams populate the Subgraph field; render an
	// extra column only when at least one hit carries one so
	// non-federated callers don't see a useless empty column.
	hasSubgraph := false
	for _, r := range results {
		if r.Subgraph != "" {
			hasSubgraph = true
			break
		}
	}
	fmt.Fprintf(&b, "Top %d results for %q on server %q:\n\n", len(results), query, server)
	if hasSubgraph {
		b.WriteString("| Name | Kind | Subgraph | Signature |\n")
		b.WriteString("|------|------|----------|-----------|\n")
	} else {
		b.WriteString("| Name | Kind | Signature |\n")
		b.WriteString("|------|------|-----------|\n")
	}
	for _, r := range results {
		sig := r.Signature
		if len(sig) > 80 {
			sig = sig[:77] + "..."
		}
		if hasSubgraph {
			sg := r.Subgraph
			if sg == "" {
				sg = "—"
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | `%s` |\n", r.Name, r.Kind, sg, sig)
		} else {
			fmt.Fprintf(&b, "| `%s` | %s | `%s` |\n", r.Name, r.Kind, sig)
		}
	}
	b.WriteString("\nCall `schema_get(name)` for full SDL.\n\n")
	b.WriteString("```json\n")
	enc, _ := json.MarshalIndent(results, "", "  ")
	b.Write(enc)
	b.WriteString("\n```")
	return b.String()
}

// renderError returns a JSON error envelope matching the
// {error, hint} contract used throughout scry tool responses.
func renderError(code, hint string) string {
	enc, _ := json.Marshal(map[string]string{"error": code, "hint": hint})
	return string(enc)
}

// renderValidation formats the validation result. Empty errors → ok
// envelope; otherwise a JSON list of {message, line, column}.
func renderValidation(errs []schema.ValidationError) string {
	if len(errs) == 0 {
		enc, _ := json.Marshal(map[string]any{"ok": true})
		return string(enc)
	}
	enc, _ := json.Marshal(map[string]any{
		"ok":     false,
		"errors": errs,
	})
	return string(enc)
}

// renderCostErrors returns the {error, hint, errors} envelope for a
// query that failed static validation, so the caller doesn't need to
// call query_validate separately. The happy-path CostReport is
// returned as a typed struct (not through this helper) so it can be
// promoted to structuredContent.
func renderCostErrors(errs []schema.ValidationError) string {
	enc, _ := json.Marshal(map[string]any{
		"error":  "invalid_query",
		"hint":   "fix the validation errors then re-run query_cost",
		"errors": errs,
	})
	return string(enc)
}
