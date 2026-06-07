# Dependency evaluation

scry's bet: build on top of four felixgeelhaar libraries instead of
re-implementing their surfaces. Each one carries a real concern that
scry would otherwise hand-roll.

| Library | Carries | Used for |
|---|---|---|
| `go.klarlabs.de/mcp` | MCP server framework (Gin-style) | `schema_search` + 4 sibling tools |
| `felixgeelhaar/axi-go` | Domain-driven tool kernel (safety + audit + budgets) | `query_execute` gating |
| `go.klarlabs.de/bolt`   | Zero-alloc `slog.Handler` with OTEL | All logging |
| `go.klarlabs.de/fortify`| Resilience patterns (CB, retry, timeout, rate limit, hedge) | Upstream GraphQL HTTP client |

## mcp-go

**Verdict: adopt.** Direct fit. scry is fundamentally an MCP server
exposing five tools — mcp-go gives typed handlers + auto-generated
input schemas + middleware + multiple transports (stdio, HTTP) out
of the box.

What it replaces: hand-rolled `Server` + `Tool().Handler()` plumbing.
TokenOps' MCP surface uses this library already; scry will reuse
the same pattern.

API touchpoints:
- `mcp.NewServer()` for the server bootstrap
- `s.Tool("schema_search").Description(...).Handler(func(ctx, in) (string, error) {...})`
- Stdio middleware for the session-pinging story (if scry tracks per-agent state)

## axi-go

**Verdict: adopt for `query_execute` only.** The other four tools
are read-only schema queries — they don't need kernel-level gating.
`query_execute` is the one tool with real side-effects: it talks to
an upstream GraphQL endpoint that may carry write mutations,
expensive queries, or auth-scoped data. axi-go gives us:

- **Effect profile** declaration per query (`read`/`write`/`external`).
  Mutations declare `write`; queries declare `read`. Operator can
  configure scry to require human approval for `write`.
- **Execution budget** per session: complexity score gates queries
  that would burn beyond the agent's headroom (TokenOps integration
  point — pull current headroom from the operator's scorecard).
- **Tamper-evident evidence chain**: every executed query is appended
  to an SHA-256-chained audit log. `session.VerifyEvidenceChain()`
  detects post-emission mutation. Free reproducibility for forensic
  agent debugging.
- **Capability resolution**: query references "Customer.email" → axi-go
  checks whether the calling agent has the `pii.read` capability.
  Out of scope for v0, but the framework leaves room.

What it replaces: a hand-rolled safety + audit + budget layer that
scry would otherwise build from scratch and get subtly wrong.

API touchpoints:
- `axi.NewKernel()` at server boot
- One `Action` per upstream endpoint (`shopify_query`, `github_query`)
- `kernel.ExecuteAction(ctx, action, input)` from the MCP handler

## bolt

**Verdict: adopt.** scry will emit structured logs for every query
(request, response size, latency, complexity score, cost). bolt's
zero-alloc handler means logging the hot path is free. First-class
OTEL means operator can ship traces to the same observability stack
they already run.

What it replaces: vanilla `log/slog` + manual OTEL bridging.

Notes:
- Use bolt's structured fields for the five canonical scry events:
  `schema.search`, `schema.get`, `query.validate`, `query.cost`,
  `query.execute`. Each carries `agent_id`, `session_id`, `latency_ms`,
  `complexity`, `tokens_in`, `tokens_out`.

## fortify

**Verdict: adopt for upstream calls only.** scry's HTTP client to
the upstream GraphQL endpoint is the single failure-mode surface
that matters. fortify gives us:

- **Circuit breaker**: upstream returns 5xx for >N requests → open
  the circuit, fail fast, log the state. Prevents an agent from
  burning turns on a dead upstream.
- **Adaptive concurrency**: bounds in-flight requests to what the
  upstream tolerates. Avoids thundering-herd when an agent loop
  fires many parallel queries.
- **Timeout + retry with jitter**: standard hygiene; fortify makes
  it composable so we get them right.
- **Hedge**: optional — fire a second request after the p99 latency
  threshold, take whichever returns first. Useful for read-only
  queries against high-tail-latency endpoints.

What it replaces: hand-rolled `http.Client` wrapping that scry
would otherwise build with `net/http` + ad-hoc retry loops.

API touchpoints:
- `fortify.Compose(cb, retry, timeout, concurrency).Wrap(http.DefaultTransport)`
- One composed transport per upstream endpoint
- Per-upstream config (Shopify tolerates ~50 RPS; GitHub ~30; tune)

## Composition diagram (sketch)

```
        ┌──────────────────────┐
agent  →│   mcp-go server      │  schema_search, schema_get, query_validate
        │   (stdio / http)     │  query_cost — all read-only, no kernel
        └─────────┬────────────┘
                  │
                  ▼ query_execute only
        ┌──────────────────────┐
        │   axi-go kernel      │  effect profile + budget + evidence chain
        │                      │
        └─────────┬────────────┘
                  │
                  ▼
        ┌──────────────────────┐
        │   fortify-wrapped    │  CB + retry + timeout + concurrency
        │   http.Client        │
        └─────────┬────────────┘
                  │
                  ▼ HTTP POST /graphql
        ┌──────────────────────┐
        │   upstream endpoint  │
        │   (Shopify / GitHub) │
        └──────────────────────┘

bolt handler attached at every layer for structured logging.
```

## What we are NOT pulling in

- **A GraphQL parser** — `vektah/gqlparser/v2` (the parser gqlgen uses).
  Required for `query_validate` and `query_cost`. External dep, but the
  only one in the standard Go GraphQL universe worth depending on.
- **An embedding model** — TBD. Either embed bge-small via onnxruntime,
  or call out to ollama. Decision deferred to `docs/schema-search.md`.
- **A vector store** — sqlite-vec for v0 (single binary, no service).
</content>
