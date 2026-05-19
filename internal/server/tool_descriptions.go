// Package server holds tool description constants with few-shot examples.
//
// Descriptions live here rather than inline at every register* call
// site so the few-shot examples — which are load-bearing for
// agent-pickup quality — stay scannable + edit-without-merging-prose.
//
// Format: short prose paragraph, blank line, "Examples:" header,
// then 1-2 bullet examples. Each example covers either the happy
// path (showing inputs + the response shape the agent should
// expect) or a known gotcha (mutually exclusive args, empty result
// hints).
//
// Anthropic + OpenAI tool-use guides both call out: models pick the
// right tool faster when descriptions carry concrete examples vs.
// when they carry only prose. The v0.6 audit flagged the absence
// of examples as a Tier-2 friction tax; v0.7 fixes it.
package server

const (
	descListServers = `List every upstream scry can route to. Call this first to discover which ` + "`server`" + ` value to pass to schema_search / query_execute / etc. Returns name + upstream URL per entry; never returns secrets.

Examples:
- Input: {} → Output: {"servers":[{"name":"shopify","url":"https://api.shopify.com/admin/api/2024-01/graphql.json"},{"name":"linear","url":"https://api.linear.app/graphql"}]}
- Use the returned ` + "`name`" + ` values verbatim in the ` + "`server`" + ` arg of every subsequent tool call.`

	descSchemaSearch = `Searchable view of an upstream GraphQL schema. Returns ranked type/field snippets matching the natural-language query. Call this FIRST before composing a query so the full SDL doesn't blow your context budget. With multiple upstreams, set ` + "`server`" + ` — call list_servers to enumerate.

Examples:
- Input: {"query":"customer email","limit":5} → Output: a markdown table of the top 5 hits with their SDL signatures.
- Empty result: the response tells you to try broader terms / singular forms (e.g. "customer" not "customer's address"). It is NOT an error — the schema just doesn't match.`

	descSchemaGet = `Return the full SDL for a single named type or field. Use after schema_search to expand a specific result.

Examples:
- Input: {"name":"Customer"} → Output: the raw SDL block for the Customer type.
- Input: {"name":"DoesNotExist"} → Output: {"error":"not_found","hint":"...try schema_search to find the right name"}.`

	descQueryValidate = `Static validation against the cached schema. Returns ok or a list of validation errors. Does NOT call upstream.

Examples:
- Valid query → {"ok":true}
- Invalid query → {"ok":false,"errors":[{"message":"Cannot query field 'foo' on type 'User'","line":1,"column":12}]}
- When clients.yml has deny_fields rules, this tool also returns permission_denied envelopes BEFORE you spend execute budget.`

	descSchemaDiff = `Return the most recent schema diff for an upstream, computed at refresh time. Reports added / removed / breaking changes. Use to plan around upstream schema evolution; agents that cached query strings can spot when a referenced type / field has been removed before their next call fails validation.

Examples:
- Diff present → JSON {"added":[...],"removed":[...],"breaking":[...]}
- No diff recorded yet → {"error":"no_diff","hint":"...diffs are computed on each background refresh after the first one"}.`

	descQueryCost = `Estimate query complexity (depth × breadth × list-multipliers) before execution. Use to gate expensive queries against the agent's headroom budget.

Examples:
- Cheap query → {"complexity":3,...}
- Invalid query → {"error":"invalid_query","errors":[...]} — same shape as query_validate so you can call this first to get cost AND validation in one round-trip.`

	descQueryExecute = `Run a GraphQL query against the named upstream and return the result. ALWAYS run query_validate + query_cost first — query_execute counts against the agent's execution budget. With multiple upstreams, set ` + "`server`" + `; otherwise the single configured upstream is used.

Optional args (exactly one of query / hash / name; the other two stay empty):
- ` + "`hash`" + `: SHA-256 of a persisted query registered via ` + "`scry pq add`" + `
- ` + "`name`" + `: friendly name of a persisted query (more memorable than the hash)
- ` + "`select`" + `: JMESPath expression projected against the response before return — cuts tokens dramatically on field-heavy queries
- ` + "`paginate`" + `: {auto: true, max_pages: N} walks Relay-style {hasNextPage, endCursor} cursors automatically and concatenates ` + "`nodes[]`" + `

Examples:
- Input: {"query":"{ viewer { login } }"} → Output: raw upstream JSON response
- Input: {"query":"{ viewer { login email } }","select":"data.viewer.login"} → Output: "alice" (just the field, no envelope overhead)
- Input: {"query":"...{ nodes pageInfo { hasNextPage endCursor } }","paginate":{"auto":true,"max_pages":5}} → all pages concatenated up to the cap
- Conflict: passing BOTH ` + "`query`" + ` and ` + "`hash`" + ` returns {"error":"pq_conflict",...}.`

	descAuthStatus = `Return credential status (valid/expiring/expired/missing) for one or all configured servers. Never returns the token itself. Call before kicking off a long agent task to confirm credentials are healthy.

Examples:
- Input: {} → Output: list of every server with its status enum.
- Input: {"server":"shopify"} → Output: just that server's status.`

	descAuthLogin = `Recover from auth_expired errors. v0 bearer-token flow is operator-driven; this tool returns the exact CLI command to run. Phase 2 will support agent-driven device-code flows for OAuth servers.

Examples:
- After receiving an auth_expired envelope from query_execute, call this with the offending server name to get the operator-facing remediation command.`

	descGateStatus = `Return the caller's session budget + audit-chain stats. Use BEFORE kicking off a long agent workflow to confirm headroom (writes_remaining, complexity_remaining). Returned identity name reflects the transport credential presented; stdio + no-auth deployments share a single 'local' session.

Examples:
- Input: {} → Output: {"writes_remaining":4,"complexity_remaining":850,"chain_len":7}.
- writes_remaining = -1 means unlimited (no MaxWritesPerSession policy set).`

	descGateChain = `Return the caller's full evidence chain (SHA-256 tamper-evident audit log of every query_execute call). Each record carries query/response hashes — never the raw payloads. Optional ` + "`verify=true`" + ` re-derives every chain hash and reports the first mismatch. Use for compliance audits, incident response, or to export the chain to an external audit pipeline.

Examples:
- Input: {"limit":10} → Output: last 10 evidence records.
- Input: {"verify":true} → Output: chain plus a verification result. badIndex=-1 means the whole chain checks out.`

	descCacheStats = `Return per-upstream cache hit-rate + size + eviction counters. Admin-only. Use to monitor cache health: low hit_rate ⇒ TTL too low / query mix too varied / CacheTTL not configured.

Examples:
- Input: {} → Output: {"caches":[{"server":"shopify","stats":{"entries":12,"hits":34,"misses":4,"evictions":0,"oldest_entry_age_seconds":18.5},"hit_rate":0.894}, ...]}
- Disabled cache: {"server":"x","disabled":true} — operator didn't configure CacheTTL.`

	descCachePurge = `Invalidate the result cache (one server or all). Admin-only. Use after a known upstream-side data change that would otherwise serve stale results until the TTL expires. Counters survive — historic hit-rate stays visible across purges.

Examples:
- Input: {"server":"shopify"} → Output: {"purged":[{"server":"shopify","entries_purged":12}]}
- Input: {} → Output: list of every server with its purged count.`

	descSchemaNeighbors = `Return the type's directed edges in the schema graph: what other types reference it (incoming) and what types it references (outgoing). Use to answer "what else depends on Customer?" without scanning the SDL. Depth=1; caps list lengths at 50 each for context-budget friendliness.

Examples:
- Input: {"name":"User"} → Output: {"type":"User","incoming":[{"src":"Query","dst":"User","field":"viewer","kind":"field"},...],"outgoing":[{"src":"User","dst":"Address","field":"primaryAddress","kind":"field"},...]}
- Input: {"name":"DoesNotExist"} → Output: {"error":"not_found","hint":"...confirm the name via schema_search or schema_get"}.`

	descSchemaDiffSubscribe = `Register an outbound webhook URL that scry POSTs to on every refresh that produces a non-empty schema diff. Admin-only. Returns a one-time secret used to HMAC-sign the body (header X-Scry-Signature, algo SHA-256). 4xx receivers skip retry; 5xx retries with exp backoff up to 3 times.

Examples:
- Input: {"url":"https://hooks.example.com/scry-diffs"} → Output: {"id":1,"url":"...","secret":"<64-hex-chars>","hint":"store this secret now — scry only returns it once."}
- Input: {"url":"not a url"} → Output: {"error":"invalid_url","hint":"webhook url must be a fully-qualified https:// (or http:// for testing) URL"}.`

	descSchemaWebhooksList = `List the schema-diff webhook registrations for a server. Admin-only. Secret is NOT returned — only Register exposes it. Use schema_webhooks_remove with the id to deregister.

Examples:
- Input: {} → Output: {"server":"shopify","webhooks":[{"id":1,"url":"...","created_at":"..."}]}`

	descSchemaWebhooksRemove = `Remove a schema-diff webhook by id. Admin-only.

Examples:
- Input: {"id":1} → Output: {"removed_id":1,"server":"shopify"}
- Input: {"id":999} → Output: {"error":"not_found","hint":"no webhook with that id — call schema_webhooks_list to enumerate"}`
)
