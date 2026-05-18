package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	mcp "github.com/felixgeelhaar/mcp-go"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/felixgeelhaar/scry/internal/cache"
	"github.com/felixgeelhaar/scry/internal/gate"
	"github.com/felixgeelhaar/scry/internal/obs"
	"github.com/felixgeelhaar/scry/internal/runtime"
	"github.com/felixgeelhaar/scry/internal/schema"
	"github.com/felixgeelhaar/scry/internal/upstream"
)

// registerQueryTools wires query_execute. Resolves the target
// server via the Manager (legacy single-upstream callers can omit
// the `server` argument); validates + cost-gates against the
// cached schema; runs the upstream POST through fortify.
//
//nolint:unparam // symmetry with other register*Tools — future wiring may fail
func registerQueryTools(srv *mcp.Server, cfg Config, mgr *runtime.Manager, g *gate.Gate) error {
	type ExecuteInput struct {
		Server        string         `json:"server,omitempty" jsonschema:"description=upstream server name (omit when only one is configured)"`
		Query         string         `json:"query" jsonschema:"required,description=GraphQL query string"`
		Variables     map[string]any `json:"variables,omitempty" jsonschema:"description=optional variables map"`
		OperationName string         `json:"operation_name,omitempty"`
	}
	srv.Tool("query_execute").
		Description("Run a GraphQL query against the named upstream and return the result. ALWAYS run query_validate + query_cost first — query_execute counts against the agent's execution budget. With multiple upstreams, set `server`; otherwise the single configured upstream is used.").
		Handler(func(ctx context.Context, in ExecuteInput) (string, error) {
			start := time.Now()
			m := obs.Metrics()
			recordOutcome := func(outcome, server string, complexity int) {
				attrs := []attribute.KeyValue{
					attribute.String("outcome", outcome),
					attribute.String("server", server),
				}
				m.ExecuteCount.Add(ctx, 1, otelmetric.WithAttributes(attrs...))
				m.ExecuteDuration.Record(ctx, time.Since(start).Seconds(), otelmetric.WithAttributes(attrs...))
				if complexity > 0 {
					m.ExecuteComplexity.Record(ctx, int64(complexity), otelmetric.WithAttributes(
						attribute.String("server", server),
					))
				}
			}
			// Ctx auto-attaches trace_id + span_id when an OTel
			// span is active (mcp.OTel middleware creates one per
			// request).
			ev := obs.L.Ctx(ctx).Info().
				Str("event", "execute").
				Int("query_len", len(in.Query))
			if id := mcp.IdentityFromContext(ctx); id != nil {
				ev = ev.Str("client", id.Name)
			}
			// Attach tool-level attributes to the active span so
			// they appear in traces alongside the auto-injected
			// MCP method name.
			if sp := mcp.SpanFromContext(ctx); sp != nil {
				mcp.SetSpanAttribute(ctx, "graphql.query_len", int64(len(in.Query)))
				if in.Server != "" {
					mcp.SetSpanAttribute(ctx, "scry.server", in.Server)
				}
			}

			if denied := requireAdmin(ctx, "query_execute"); denied != "" {
				ev.Str("outcome", "permission_denied").Dur("dur", time.Since(start)).Send()
				recordOutcome("permission_denied", in.Server, 0)
				return denied, nil
			}

			entry, errResp := resolveServer(in.Server, mgr)
			if errResp != "" {
				ev.Str("outcome", "unknown_server").Dur("dur", time.Since(start)).Send()
				recordOutcome("unknown_server", in.Server, 0)
				return errResp, nil
			}
			if denied := requireServerScope(ctx, entry.Name); denied != "" {
				ev.Str("outcome", "permission_denied_server").Dur("dur", time.Since(start)).Send()
				recordOutcome("permission_denied_server", entry.Name, 0)
				return denied, nil
			}
			ev = ev.Str("server", entry.Name)

			sdl, err := entry.Store.GetMeta(ctx, "full_sdl")
			if err != nil {
				ev.Str("outcome", "schema_unavailable").Dur("dur", time.Since(start)).Send()
				recordOutcome("schema_unavailable", entry.Name, 0)
				return renderError("schema_unavailable",
					"schema index has no SDL — wait for the next refresh or restart with --refresh"), nil
			}

			if errs := schema.ValidateQuery(sdl, in.Query); len(errs) > 0 {
				ev.Str("outcome", "invalid_query").Int("errors", len(errs)).Dur("dur", time.Since(start)).Send()
				recordOutcome("invalid_query", entry.Name, 0)
				return renderExecuteError("invalid_query",
					"fix the validation errors then re-run; use query_validate to inspect", map[string]any{
						"errors": errs,
					}), nil
			}

			var complexity int
			rpt, _ := schema.EstimateCost(sdl, in.Query)
			complexity = rpt.Complexity

			// Gate: classify effect + check session budget BEFORE
			// hitting upstream. Failing a write budget never
			// reaches the upstream; failing a read budget skips
			// the call too.
			effect := gate.Classify(in.Query)
			session := sessionFromContext(ctx)

			// Cache check for reads. Mutations bypass — the cache
			// stays correctness-safe even when an agent mixes
			// reads + writes against the same data.
			cacheKey := ""
			if effect == gate.EffectRead && entry.Cache != nil {
				cacheKey = cache.Key(in.Query, in.Variables, in.OperationName)
				if body, hit := entry.Cache.Get(cacheKey); hit {
					m.CacheHits.Add(ctx, 1, otelmetric.WithAttributes(
						attribute.String("server", entry.Name),
					))
					ev.Str("outcome", "ok_cached").
						Int("response_bytes", len(body)).
						Dur("dur", time.Since(start)).Send()
					recordOutcome("ok_cached", entry.Name, complexity)
					g.Record(session, entry.Name, effect, complexity, in.Query, body, "ok_cached")
					return string(body), nil
				}
				m.CacheMisses.Add(ctx, 1, otelmetric.WithAttributes(
					attribute.String("server", entry.Name),
				))
			}
			if decision := g.CheckBudget(session, effect, complexity); !decision.Allowed {
				ev.Str("outcome", "budget_exceeded").
					Str("effect", string(effect)).
					Str("session", string(session)).
					Dur("dur", time.Since(start)).Send()
				recordOutcome("budget_exceeded", in.Server, complexity)
				return renderExecuteError("budget_exceeded", decision.Reason, map[string]any{
					"effect":    string(effect),
					"remaining": decision.Remaining,
				}), nil
			}

			if cfg.CostCeiling > 0 {
				if rpt.Complexity > cfg.CostCeiling {
					ev.Str("outcome", "cost_exceeded").
						Int("complexity", rpt.Complexity).
						Int("ceiling", cfg.CostCeiling).
						Dur("dur", time.Since(start)).Send()
					recordOutcome("cost_exceeded", entry.Name, rpt.Complexity)
					return renderExecuteError("cost_exceeded",
						fmt.Sprintf("estimated complexity %d exceeds ceiling %d; narrow the selection set or raise --cost-ceiling", rpt.Complexity, cfg.CostCeiling),
						map[string]any{"cost": rpt, "ceiling": cfg.CostCeiling}), nil
				}
				ev = ev.Int("complexity", rpt.Complexity)
			}

			res, err := entry.Client.Execute(ctx, in.Query, in.Variables, in.OperationName)
			if errors.Is(err, upstream.ErrRateLimited) {
				ev.Str("outcome", "rate_limited").Dur("dur", time.Since(start)).Send()
				recordOutcome("rate_limited", entry.Name, complexity)
				return renderExecuteError("rate_limited",
					fmt.Sprintf("scry's per-server rate limit for %q rejected this request — back off and retry", entry.Name),
					map[string]any{
						"server":      entry.Name,
						"retry_after": "1s",
					}), nil
			}
			if errors.Is(err, upstream.ErrAuthExpired) {
				ev.Str("outcome", "auth_expired").Int("status", statusOf(res)).Dur("dur", time.Since(start)).Send()
				recordOutcome("auth_expired", entry.Name, complexity)
				return renderExecuteError("auth_expired",
					fmt.Sprintf("upstream %q returned 401 — call auth_login(%q) to refresh, then retry query_execute", entry.Name, entry.Name), map[string]any{
						"server": entry.Name,
					}), nil
			}
			if err != nil {
				ev.Str("outcome", "upstream_error").Int("status", statusOf(res)).Err(err).Dur("dur", time.Since(start)).Send()
				recordOutcome("upstream_error", entry.Name, complexity)
				return renderExecuteError("upstream_error", err.Error(), map[string]any{
					"status": statusOf(res),
					"server": entry.Name,
				}), nil
			}

			ev.Str("outcome", "ok").Int("status", res.Status).Int("response_bytes", len(res.Raw)).Dur("dur", time.Since(start)).Send()
			recordOutcome("ok", entry.Name, complexity)
			g.Record(session, entry.Name, effect, complexity, in.Query, res.Raw, "ok")
			if cacheKey != "" {
				entry.Cache.Set(cacheKey, res.Raw)
			}
			return string(res.Raw), nil
		})
	return nil
}

// sessionFromContext derives the session key for the gate from the
// MCP identity context. Falls back to "local" when there's no
// remote identity (stdio + no-auth deployments), so audit + budget
// still apply per-process.
func sessionFromContext(ctx context.Context) gate.SessionID {
	if id := mcp.IdentityFromContext(ctx); id != nil && id.ID != "" {
		return gate.SessionID(id.ID)
	}
	return "local"
}

func statusOf(r *upstream.Result) int {
	if r == nil {
		return 0
	}
	return r.Status
}

func renderExecuteError(code, hint string, extras map[string]any) string {
	out := map[string]any{"error": code, "hint": hint}
	for k, v := range extras {
		out[k] = v
	}
	enc, _ := json.Marshal(out)
	return string(enc)
}
