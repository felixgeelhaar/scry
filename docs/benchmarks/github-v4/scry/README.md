# scry-routed runner

Spawns `scry serve --upstream https://api.github.com/graphql
--auth env://GITHUB_TOKEN` as MCP subprocess. Wires Claude's
MCP client to scry's stdio. Claude receives the canonical task
with NO SDL in system prompt — only the four read-only schema
tools. Forces the agent through `schema_search` →
`query_validate` → `query_execute`.

Run via `make bench-scry` from the parent directory. See parent
README.md for env vars.

Implementation lands in `v06-bench-scry-runner`.
