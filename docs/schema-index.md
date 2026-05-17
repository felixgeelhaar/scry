# Schema index

scry's read-side: how an upstream GraphQL schema becomes searchable
MCP tool output.

## Pipeline

```
upstream GraphQL endpoint
        │  POST { query: introspectionQuery }
        ▼
   Client.Introspect
   ├── full query  (depth ~9)
   └── fallback: shallow query (depth ~5) on depth-limit
        │  *Schema (typed Go structs)
        ▼
   BuildUnits + BuildSDL
   ├── BuildUnits  → []SearchUnit  (one per type, one per field)
   └── BuildSDL    → full reconstructed SDL document
        │
        ▼
   Store.Replace + Store.SetMeta
   ├── units table     (canonical rows)
   ├── units_fts5      (BM25-ranked virtual table)
   └── meta            (full_sdl, refreshed_at, introspection_mode)
        │
        ▼
   MCP tools
   ├── schema_search → Store.Search(query, limit)   → ranked snippets
   ├── schema_get    → Store.GetSDL(name)           → SDL fragment
   ├── query_validate → gqlparser.LoadQuery(sdl, q) → []ValidationError
   └── query_cost    → walk AST → CostReport
```

## SQLite layout

```sql
CREATE TABLE units (
  name        TEXT PRIMARY KEY,   -- "Customer" or "Query.customer"
  kind        TEXT NOT NULL,      -- type | field | input | enum | scalar | union
  parent_type TEXT,               -- "" for types, "Query" for fields
  description TEXT,
  signature   TEXT,
  sdl         TEXT,               -- full SDL fragment for schema_get
  composed    TEXT                -- BM25-indexed search text
);

CREATE VIRTUAL TABLE units_fts USING fts5(
  name, kind, parent_type, description, signature, composed,
  content='units', content_rowid='rowid',
  tokenize='porter unicode61'
);

CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);
```

Triggers keep `units_fts` in sync with `units` on INSERT/UPDATE/DELETE,
so callers only touch the base table. `Replace` is atomic — the whole
index is swapped inside one transaction.

## Graph CDN depth-limit fallback

### Problem

The standard GraphQL introspection query nests `ofType` four levels
deep to unwrap type wrappers like `NonNull(List(NonNull(NonNull(X))))`:

```graphql
type { kind name ofType { kind name ofType { kind name ofType { kind name } } } }
```

Combined with the `fields { args { type { … } } }` chain, total
selection depth is ~9. Many commercial GraphQL APIs route through
**Graph CDN** (now Stellate), which enforces a default query-depth
limit of 7-8 and rejects deeper queries with:

```
HTTP 413
{"errors":[{"message":"Query depth limit exceeded.",
            "extensions":{"code":"GCDN_QUERY_DEPTH_LIMIT"}}]}
```

Observed on:
- `https://countries.trevorblades.com/`
- `https://rickandmortyapi.com/graphql`

Both reject the standard depth-9 introspection.

### Fix: two-mode fallback

`Client.Introspect` returns `(*Schema, IntrospectionMode, error)`:

1. Try `introspectionQuery` (full, depth ~9).
2. On `isDepthLimitError(err)`, retry with `introspectionQueryShallow`
   (depth ~5, two `ofType` levels instead of four).
3. Other errors (auth, network, malformed JSON) propagate immediately
   — no point retrying a 401 with a shorter query.

`isDepthLimitError` matches three signals conservatively:
- HTTP 413 (Graph CDN signature).
- HTTP 400 with body containing "depth" or "complexity" (some Apollo
  Router / Hasura deployments).
- GraphQL errors containing "depth" or "complexity".

The mode that succeeded is persisted to `meta.introspection_mode`
(`full` | `shallow`) so operators can inspect what was indexed.

### Trade-off: SDL fidelity

The shallow query only unwraps two type-wrapper levels. A type like
`NonNull(List(NonNull(X)))` resolves through two `ofType` hops to
`NonNull(List(X))` instead of the full `[X!]!`. The inner `!` is
dropped on doubly-wrapped types.

Impact:
- `schema_search` — unaffected. Named types are still indexed.
- `schema_get` — slightly less precise SDL on doubly-wrapped fields.
- `query_validate` — still catches field-name / arg-name errors.
- `query_cost` — still detects list fields and counts complexity.

The full mode is preferred when the upstream allows it. The shallow
fallback is a graceful degradation, not a v0 default.

### Operator escape hatches (not yet implemented)

When a real user hits an upstream that rejects both queries:
- `--sdl path/to/schema.graphql` — skip introspection entirely; load
  SDL from a checked-in file.
- Chunked per-type introspection — issue `__type(name: $name)`
  per type to keep single-query depth low.

Both deferred until a customer hits the limit.

## Background refresh

`server.runRefresher` re-introspects on a ticker (default 24h, set
via `--refresh-interval`, `0` disables). Errors are logged but never
propagated — a transient upstream outage must not kill scry; the
cached index keeps serving until the next tick succeeds.

The mode that succeeded is logged to stderr on first introspection so
operators see when the fallback fires.

## Test layers

| Layer | File | Coverage |
|---|---|---|
| Unit — error detection | `introspect_test.go` | 9 cases of `isDepthLimitError` |
| Unit — fallback flow | `introspect_test.go` | httptest mock Graph CDN |
| Unit — index pipeline | `store_test.go` | BuildUnits, FTS5 search, GetSDL |
| Unit — validate + cost | `validate_test.go` | gqlparser integration |
| Live (build-tagged) | `live_test.go` | SWAPI (full mode), trevorblades (shallow mode) |

Run live tests with `go test -tags=live -run TestLive ./internal/schema/...`.
