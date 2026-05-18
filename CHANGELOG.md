# Changelog

All notable changes to scry are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the
project uses [Semantic Versioning](https://semver.org/).

## Unreleased

## 0.5.0 - 2026-05-18

### Added

- **Server-package test coverage uplift (33% → 63.8%)** — internal/server
  was the lowest-coverage package. v0.5 adds direct-handler-invocation
  tests driving the registered MCP tools through their internal
  closures, with httptest upstreams and in-memory runtime fixtures.
  - `query_execute_test.go` — 11 cases covering envelope outcomes:
    invalid_query, cost_exceeded, budget_exceeded, auth_expired,
    upstream_error, pq_conflict, pq_not_found, permission_denied
    (read-only identity), unknown_server (multi-server manager), the
    happy path, and the cached path.
  - `schema_tools_test.go` — 15 cases covering schema_search (empty,
    populated, subgraph column rendering), schema_get (hit + not_found),
    query_validate (ok + errors), query_cost (ok + errors), schema_diff
    (no_diff envelope + populated payload), and resolveServer routing
    across single-default / multi-no-name / multi-known / multi-unknown.

- **End-to-end mutation test** — `mutation_test.go` stands up an
  httptest upstream exposing a Mutation root with a side-effecting
  `incrementCounter`, then drives query_execute through the full
  path: validate → cost → gate write budget → upstream POST → audit
  record. Asserts the three spec guarantees: mutations bypass the
  cache, write counter increments only on outcome=ok, and chain
  Evidence carries `Effect=write`. Documents the lack of a stable
  public mutable GraphQL endpoint so future contributors don't
  reinvent the search.

### Quality

- `internal/server` coverage: 33% → 63.8% (clears the 60% target).
- 30 new tests, all in-process + deterministic (no network).

### Deferred (still)

- **mcp-go ToolFilter merge** to a future cycle — upstream PR
  remains closed-unmerged; deferred from v0.3 → v0.4 → v0.5.
- **mTLS identity propagation** — still blocked on mcp-go
  [#93](https://github.com/felixgeelhaar/mcp-go/issues/93).

## 0.4.0 - 2026-05-18

### Added

- **Persisted queries** — operator pre-registers expensive queries
  via `scry pq add/list/remove`; agent calls
  `query_execute(server, hash="…")` instead of pushing full query
  text. Per-server SQLite store at `<IndexDir>/<safe>.pq.db`.
  Hot-add: changes via CLI take effect without restarting the
  daemon. Cuts agent context budget + upstream payload for known
  workloads.
- **Apollo Federation awareness** — `_service { sdl }` probe on
  every refresh extracts `@join__type(graph: NAME)` subgraph
  ownership; `schema_search` surfaces a Subgraph column when
  results carry one. Non-federated upstreams keep the original
  3-column shape (no regression).
- **Cache hit/miss metrics** — `scry.cache.hits_total{server}` +
  `scry.cache.misses_total{server}` counters. Operators chart
  per-upstream dedupe rate; agents get feedback on cache hygiene.
- **Fuzz harness** — three FuzzXxx targets covering `ParseSDL`,
  `gate.Classify`, `cache.Key`. Nightly CI runs each for 5
  minutes; crashers upload as artifacts.
  `.github/workflows/fuzz.yml`.

### Tools

- New: `scry pq add/list/remove` CLI.
- `query_execute` accepts `hash` arg (mutually exclusive with
  `query`). Returns `pq_not_found` / `pq_conflict` envelopes
  with operator-actionable hints.

### Deferred

- **mTLS identity propagation** to v0.5 — blocked on mcp-go
  [#93](https://github.com/felixgeelhaar/mcp-go/issues/93) (HTTP
  transport doesn't expose a request-context augmentation hook,
  so `req.TLS.PeerCertificates` can't reach the middleware
  chain). Internal scry change reduces to a one-liner once
  upstream lands the hook.

## 0.3.0 - 2026-05-18

### Added

- **Audit chain anchor sidecar** — closes the v0.2 truncation gap.
  When `--audit-keep > 0` rotation drops an archive, the last
  record's ChainHash is persisted to `<session>.anchor` (0600).
  `Gate.VerifyChainForSession` + `VerifyChainFromAnchor` read it
  as the genesis prev-hash so chains survive arbitrarily many
  rotations end-to-end. Existing `VerifyChain` stays as the
  one-argument shortcut for callers without rotation history.
- **OTel audit-log bridge** — every evidence record now ships
  through OTel logs in addition to the local JSONL. `OTEL_LOGS_EXPORTER`
  honours `none|otlp|stdout`; falls back to `OTEL_TRACES_EXPORTER`
  for the single-pipeline case. Strict redaction: hashes only,
  never raw query/response bodies. JSONL stays as the durable
  local copy; OTel is the streaming sink for Splunk / Datadog /
  Loki / any OTLP-receiving SIEM.
- **Schema diff alerting** — refresh now diffs the new SDL against
  the cached prior and emits a structured `schema.changed` log +
  the `scry.schema.changes_total{kind=added|removed|breaking}`
  metric. New `schema_diff(server)` MCP tool surfaces the last
  diff so agents can plan around upstream schema evolution. First
  refresh is suppressed (no "everything is added" noise on a
  fresh upstream).
- **Query result cache** — per-upstream TTL + LRU cache for read
  queries, keyed by SHA-256(query | sorted variables | operationName).
  Operator knobs: `--cache-ttl` (default 30s, 0 disables),
  `--cache-max-entries` (default 1000, 0 unbounded). Mutations
  always bypass. Cache hits record evidence as `outcome=ok_cached`
  so audit + metrics distinguish cache vs upstream provenance.

### Tools

- `schema_diff` brings the MCP catalog to 12 tools.

### Deferred

- mcp-go `ToolFilter` upstream adoption. PR
  [felixgeelhaar/mcp-go#92](https://github.com/felixgeelhaar/mcp-go/pull/92)
  was closed unmerged by the maintainer. scry's internal
  `internal/server/tool_filter.go` wrapper stays; the v0.3 acceptance
  criterion ("scry doesn't ship its own tool-list filter") slips to
  v0.4. The wrapper is functional + tested.

## 0.2.0 - 2026-05-18

### Added

- **Custom auth headers** — `auth.header_name` + `auth.scheme` per
  server. Empty scheme = raw credential, no prefix. Unblocks
  upstreams that don't speak OAuth 2.0 Bearer (`X-API-Key`,
  `Token <T>`, custom shapes). Hot reload swaps headers + scheme
  atomically alongside token rotation.
- **Per-server rate limiting** — `rate_limit.{rps, burst}` per
  server, layered onto the fortify chain as a token-bucket gate.
  Rejected calls fail closed with `ErrRateLimited` before
  reaching the upstream; `query_execute` maps that to a
  `rate_limited` envelope with `retry_after` hint.
- **Audit log rotation** — `--audit-max-size` (50 MiB default) +
  `--audit-keep` (5 default). Per-session JSONL files shift
  archives on rollover (`<session>.jsonl.1` … `.N`); chain hashes
  link forward across files. `keep=0` retains all archives for
  compliance workloads.
- **`scry doctor`** diagnostic CLI. Probes servers.yml /
  clients.yml / audit dir / OTel exporter / per-upstream
  reachability. One-line verdict per check; exit code != 0 lists
  the failing checks. Replaces the v0.1 "figure out why scry
  isn't working" guessing.
- **`keychain://service/key`** token-ref scheme via
  99designs/keyring. macOS Keychain, libsecret on Linux, KWallet,
  Windows credential manager. Headless systems get an actionable
  fallback message pointing at `file://` / `env://`.
- **Race-stress harness** — build-tagged `stress` test that
  hammers hot-reload + Gate.Record + concurrent `Get` under
  `-race`. Nightly CI workflow (`.github/workflows/stress.yml`)
  runs it on `main` so locking regressions surface within 24h.
- **Deployment manifests** — `Dockerfile` (multi-stage,
  distroless, nonroot), `deploy/systemd/scry.service` (hardened
  with PrivateTmp / ProtectSystem=strict / dropped capabilities),
  `deploy/k8s/*` (namespace + ConfigMap + Secret + PVC +
  Deployment + Service + HPA, `scry doctor` as startup probe).
  Docs: `docs/deployment/{docker,systemd,kubernetes}.md`.

### Changed

- `upstream.Client.SetAuth` now takes an `AuthSpec` carrying
  header + scheme + token-fn together so all three rotate
  atomically.
- `runtime.AddConfig` + `runtime.Entry` carry the resolved auth
  header / scheme + per-server rate-limit config; `Replace`'s
  diff covers header / scheme / rate-limit changes in addition
  to token rotation.
- `gate.Policy` gains `AuditMaxSize` + `AuditKeep` for rotation
  control.
- `upstream.AuthError` renamed to `upstream.ErrAuthExpired` to
  match Go's `Err*` convention.
- `obs.Metrics()` now returns the exported `ScryMeters` type;
  reset helper renamed to `ResetMetersForTest`.
- `scry version` reports ldflags-stamped Version / Commit / Date.

### Fixed

- Lint sweep across the codebase against `golangci-lint v2.11`:
  gofmt drift, error-naming conventions, deprecated
  `gqlparser.LoadQuery` → `LoadQueryWithRules`, fortify
  HTTPClient roundtripper false-positive bodyclose suppression
  with documented rationale.
- `cmd/scry` exit paths now delegate via `run() int` so deferred
  tracer + meter shutdown always runs before process exit. Last
  span / metric batch flushes on flag-validation errors.

### Filed upstream

- mcp-go [#92](https://github.com/felixgeelhaar/mcp-go/pull/92):
  `ToolFilter` middleware for identity-aware tools/list
  filtering. Closes mcp-go #90. Internal wrapper in scry
  (`internal/server/tool_filter.go`) stays until the PR merges +
  releases — swap in a v0.2.x patch.

## 0.1.0 - 2026-05-18

### Added

- Initial scry implementation: searchable GraphQL ↔ MCP bridge for
  AI agents.
- Schema introspection with Graph CDN shallow-fallback path and
  operator-provided SDL escape hatch (`--sdl-file`).
- Per-upstream SQLite + FTS5 + BM25 search index with hot reload
  via fsnotify on `servers.yml`.
- Four MCP transports (stdio, HTTP, gRPC, WebSocket) with embedded
  TLS + mTLS.
- Ten MCP tools: `list_servers`, `schema_search`, `schema_get`,
  `query_validate`, `query_cost`, `query_execute`, `auth_status`,
  `auth_login`, `gate_status`, `gate_chain`. Identity-filtered at
  `tools/list` time.
- Three-tier transport authz: `--serve-auth` admin token,
  `--serve-auth-readonly` token, and per-client allowlists in
  `clients.yml`.
- Gate layer: GraphQL effect classification (read/write/subscribe),
  per-session budgets (write count, cumulative complexity), and
  SHA-256 tamper-evident evidence chain with optional JSONL
  persistence (`--audit-dir`) replayed on restart.
- Structured logging via bolt with automatic OTel trace_id
  correlation; OTel traces + metrics exporters (OTLP/stdout)
  driven by standard `OTEL_*` env vars.
- Token references for credential indirection: `env://VAR`,
  `file://path`, `op://Vault/Item/field`.
