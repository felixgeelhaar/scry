# Changelog

All notable changes to scry are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the
project uses [Semantic Versioning](https://semver.org/).

## Unreleased

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
