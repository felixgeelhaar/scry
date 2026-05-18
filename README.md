# scry

[![CI](https://github.com/felixgeelhaar/scry/actions/workflows/ci.yml/badge.svg)](https://github.com/felixgeelhaar/scry/actions/workflows/ci.yml)
[![Security](https://github.com/felixgeelhaar/scry/actions/workflows/security.yml/badge.svg)](https://github.com/felixgeelhaar/scry/actions/workflows/security.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/felixgeelhaar/scry.svg)](https://pkg.go.dev/github.com/felixgeelhaar/scry)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

**Searchable GraphQL ↔ MCP bridge for AI agents.**

> Scry the schema. The agent sees only what it needs.

Connect scry to one or many GraphQL endpoints. It introspects each
schema, builds a per-upstream SQLite + FTS5 index, and exposes ten
MCP tools:

| Tool | Purpose |
|---|---|
| `list_servers()` | enumerate configured upstreams |
| `schema_search(server?, query, limit)` | natural-language → ranked type/field snippets |
| `schema_get(server?, name)` | full SDL for a single named type |
| `query_validate(server?, query)` | static validation against the cached schema |
| `query_cost(server?, query)` | complexity estimate before execute |
| `query_execute(server?, query, vars?)` | run upstream; gated by validate + cost ceiling + session budget |
| `auth_status(server?)` | credential traffic light (no secrets returned) |
| `auth_login(server)` | recovery hint when a token expires mid-task |
| `gate_status()` | session budget + audit-chain stats |
| `gate_chain(verify?, limit?)` | full SHA-256 evidence chain; optional integrity verify |

`tools/list` is **identity-filtered**: read-only clients (or
clients.yml-scoped clients) never see tools they can't call.

`server` is optional when only one upstream is configured (the
`--upstream` flag path). With multiple upstreams loaded from
`servers.yml`, the agent calls `list_servers` first then targets
each request explicitly.

Why search-first: a 1,500-type schema (Shopify, Linear, GitHub) is 100k+
tokens of overhead per session. scry keeps the agent's context small —
~2k tokens per query, ~50× reduction.

## Quickstart

```bash
# install via Homebrew (macOS + Linux)
brew install felixgeelhaar/tap/scry

# OR via go install
go install github.com/felixgeelhaar/scry/cmd/scry@latest

# single upstream — desktop MCP client launches scry as subprocess
scry serve --upstream https://swapi-graphql.netlify.app/graphql

# multi-upstream — register servers, then run without --upstream
scry servers add shopify --upstream https://api.shopify.com/admin/api/2024-01/graphql.json
scry servers add linear  --upstream https://api.linear.app/graphql
scry auth login shopify --token "env://SHOPIFY_TOKEN"
scry auth login linear  --token "env://LINEAR_TOKEN"
scry serve  # picks up every server from servers.yml
```

Multi-upstream mode is the right shape for company-hosted scry: one
process fronts every internal GraphQL service. The agent calls
`list_servers` to discover routes, then targets each tool call:

```json
{"name":"schema_search","arguments":{"server":"shopify","query":"customer email"}}
```

**Hot reload:** scry watches `servers.yml` via fsnotify. Add/remove
servers, rotate tokens, change upstream URLs — running agents pick
up the new state inside one debounce window (~500ms) without a
restart. Token rotation skips re-introspection (cheap path); URL
change triggers a fresh introspection (cached index would be stale).

## Transports

scry speaks four MCP transports. Choose one with `--transport`:

| Transport | Use when | Auth |
|---|---|---|
| `stdio` (default) | Desktop client (Claude Desktop, Cursor, Claude Code) launches scry as a subprocess. | Implicit — only the launching process talks to it. |
| `http` | Company-hosted MCP for multiple agents. Run behind a reverse proxy that terminates TLS. | `--serve-auth` Bearer token. |
| `grpc` | High-throughput internal services. Same hosting model as HTTP. | `--serve-auth` Bearer token. |
| `ws` | Browser-side agents or anything that prefers WebSocket framing. | `--serve-auth` Bearer token. |

```bash
# stdio (default) — launched by an MCP client
scry serve --upstream https://api.example.com/graphql --auth "env://TOKEN"

# HTTP for company-hosted multi-agent access
scry serve --transport http --listen :7777 \
           --upstream https://api.example.com/graphql \
           --auth        "env://UPSTREAM_TOKEN" \
           --serve-auth  "env://SCRY_SHARED_SECRET"

# gRPC for internal service-to-service
scry serve --transport grpc --listen :7778 \
           --upstream https://api.example.com/graphql \
           --auth        "env://UPSTREAM_TOKEN" \
           --serve-auth  "file:///run/secrets/scry-clients"
```

**Two distinct credentials:** `--auth` is what scry uses to talk to
the *upstream*. `--serve-auth` is what clients must present to talk
to *scry*. Both accept token-ref schemes (`env://`, `file://`,
`op://`) and a literal as a quick-start fallback.

**Per-tool authz:** `--serve-auth` is the *admin* token — holders
may call every tool. Add `--serve-auth-readonly` to issue a second
bearer scoped to non-destructive tools (list_servers, schema_search,
schema_get, query_validate, query_cost, auth_status). Read-only
clients calling `query_execute` or `auth_login` get a
`permission_denied` envelope:

```bash
scry serve --transport http --listen :7777 \
           --upstream    https://api.example.com/graphql \
           --auth        env://UPSTREAM_TOKEN \
           --serve-auth          env://SCRY_ADMIN_SECRET    \
           --serve-auth-readonly env://SCRY_DASHBOARD_SECRET
```

stdio + no-auth deployments don't have remote identities so all
tools are callable. Use the read-only token any time agents differ
in trust level (e.g. an internal dashboard that should never mutate
production data).

**TLS:** scry supports both edge-terminated *and* embedded TLS. Run a
reverse proxy when cert rotation + SNI live in your existing pipeline,
or pass `--tls-cert` + `--tls-key` to scry directly:

```bash
scry serve --transport http --listen :7777 \
           --tls-cert /etc/scry/cert.pem \
           --tls-key  /etc/scry/key.pem \
           --upstream https://api.example.com/graphql \
           --serve-auth env://SCRY_SHARED_SECRET
```

For **mTLS** (client-cert identity, common in service meshes), add
`--mtls-ca` pointing at a PEM bundle of CAs:

```bash
scry serve --transport grpc --listen :7778 \
           --tls-cert /etc/scry/server.pem \
           --tls-key  /etc/scry/server-key.pem \
           --mtls-ca  /etc/scry/clients-ca.pem \
           --upstream https://api.example.com/graphql
```

mTLS plus a shared-secret Bearer is overkill for most setups —
pick one. Cert-based identity scales better when many agents
connect; shared secret is simpler when there's one trusted client.

## Credential storage

scry stores credentials at `$XDG_CONFIG_HOME/scry/servers.yml` with
mode `0600`. Same pattern as `kubectl config`, `gh`, `gcloud`,
`~/.aws/credentials`, `~/.npmrc`.

**Security caveats — read these:**

- The file contains **plaintext bearer tokens** by default.
- Backup tools (Time Machine, restic, borg) will copy it. Treat backup
  storage as sensitive.
- `~/.config` is **not** in default `.gitignore`. If you cd into a
  dotfiles repo: add `scry/servers.yml` before committing.
- Any process running as your user can read the file. No sandbox.

For higher-trust setups, use a **token reference** instead of a literal
in servers.yml or on the `--auth` flag:

```yaml
# servers.yml
servers:
  shopify:
    upstream: https://api.shopify.com/admin/api/2024-01/graphql.json
    auth:
      type: bearer
      token: env://SHOPIFY_TOKEN              # or
      # token: file:///run/secrets/shopify    # or
      # token: op://Personal/shopify/token    # requires 1Password CLI
```

Supported reference schemes:

| Scheme | Resolves to | When to use |
|---|---|---|
| `env://VAR` | environment variable | CI, ephemeral shells |
| `file://path` | file contents (must be 0600) | systemd secrets, Docker secrets, ramdisk |
| `op://Vault/Item/field` | 1Password CLI (`op read`) | desktop with 1Password installed |
| (literal) | the string verbatim | dev / quick start |

OS keychain integration (`keychain://`) is planned for v0.2.

## Observability

scry emits structured JSON logs (bolt) by default and **distributed
traces** when OTel is configured. Logs auto-correlate to traces — a
`query_execute` log line carries the same `trace_id` / `span_id` as
the span in your collector.

Enable tracing via standard OTel env vars (zero scry-specific
config):

```bash
# Off (default): zero overhead, no exporter contacted
scry serve --upstream ...

# Send spans to a local OTel collector
OTEL_TRACES_EXPORTER=otlp \
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318 \
scry serve --upstream ...

# Pretty-print spans to stderr (dev)
OTEL_TRACES_EXPORTER=stdout scry serve --upstream ...
```

W3C trace-context propagation is on — incoming `traceparent` headers
continue the agent's trace through scry to the upstream. End-to-end
view: **agent → scry MCP request → schema validate → upstream POST
→ response**.

Spans emitted:
- `mcp.<method>` (auto, from mcp-go OTel middleware)
- `runtime.refresh` (introspect + index rebuild)
- `upstream.execute` (the actual GraphQL POST)

Metrics emitted (`OTEL_METRICS_EXPORTER=otlp|stdout`):
- `scry.query_execute.count` — by server + outcome
- `scry.query_execute.duration_seconds`
- `scry.query_execute.complexity` — histogram
- `scry.introspect.count` / `scry.introspect.errors`
- `scry.upstream.latency_seconds`

Audit chain (persistent via `--audit-dir`):
- Every `query_execute` appends one SHA-256-linked record to
  `<dir>/<session>.jsonl` (mode 0600). Tamper-evident: flipping
  any past record breaks every later chain hash.
- Replayed into memory on restart so `gate_chain(verify=true)`
  spans process lifetimes.
- Records carry hashes only — never the raw query or response.

Each span carries scry-specific attributes (`scry.server`,
`graphql.query_len`, `http.status_code`, `introspection.mode`, etc.).

## Status

Pre-MVP. Working: introspection (full + Graph CDN shallow fallback),
schema index (FTS5 + BM25), validate, cost, execute (fortify-wrapped),
credential store with token refs, multi-upstream + hot reload,
per-tool authz, structured logs + OTel tracing.

## Design references

- [`docs/dep-eval.md`](docs/dep-eval.md) — why mcp-go, axi-go, bolt, fortify
- [`docs/auth-design.md`](docs/auth-design.md) — credential model + recovery loop
- [`docs/schema-index.md`](docs/schema-index.md) — pipeline + Graph CDN fallback
