# GitHub v4 benchmark — full-SDL vs. scry

Reproducible head-to-head: same task, same model, same upstream.
Two paths into GitHub's GraphQL schema. Measures whether scry's
search-first wedge translates into real token + success deltas on
a non-toy schema.

GitHub v4 published SDL is **1.48MB, ~370k tokens** (1756 types,
7115 indexed search units when scry introspects it). That's ~2×
Claude Sonnet 4.6's 200k context window — so the baseline runner
exists primarily to prove that "paste the SDL" doesn't even fit,
not to compete for success rate.

## What this proves (or refutes)

scry's top-of-funnel claim: dramatic context reduction by
replacing "paste the SDL" with FTS5-ranked search. The bench
records what actually happens on a real-world schema and renders
one of two decisions:

- **`SHIP`** — scry success rate ≥ 85% AND compression ≥ 10× (or
  baseline DNF). Distribute: HN, Apollo Slack, r/LocalLLaMA.
- **`ITERATE`** — below either threshold. v0.7 plan grounded in
  observed failure modes (embedding-backed search, response-shape
  changes, docstring examples), not speculation.

## The task

> List every pull request that `@felixgeelhaar` opened in the
> repository `felixgeelhaar/tokenops` between 2026-04-15 and
> 2026-05-15 that modified the file `internal/proxy/dashboard.html`,
> with reviewer + merge status.

Multi-field, joins across `User → PullRequest → Files → Reviews`.
Picking ~5 fields out of GitHub's 1756 types is the schema-
navigation cost the wedge is supposed to compress.

Ground truth recorded once via a hand-written GraphQL query against
the pinned repo + date window, persisted to `fixtures/expected.json`
(5 PRs: #50, #58, #59, #60, #64, all merged, all reviewed by
`copilot-pull-request-reviewer`). Re-recording invalidates prior
comparisons — gated behind `make bench-fixtures`.

## Reproduce

```bash
export GITHUB_TOKEN=ghp_…       # public_repo scope is enough
export ANTHROPIC_API_KEY=sk-ant-…

cd docs/benchmarks/github-v4
make bench
```

Outputs:

- `results/baseline.jsonl` — per-trial metrics for the full-SDL runner.
- `results/scry.jsonl` — per-trial metrics for the scry-routed runner.
- `results/summary.md` — comparison table (compression, success rate,
  p50/p95 latency, $$/task, failure-mode breakdown, go/no-go).
- `results/summary.json` — same numbers in machine-readable form.

`make bench-baseline` / `make bench-scry` run one side at a time.
`make bench-summary` aggregates without re-running trials.
`make bench-clean` wipes results + the built scry binary.
`make bench-fixtures` re-records ground truth (rare — see above).

## Architecture

```
docs/benchmarks/github-v4/
├── Makefile              (bench / bench-baseline / bench-scry / bench-summary / bench-fixtures / bench-clean / build-scry)
├── internal/task         (shared: canonical Prompt(), PR struct, Score(), ParseResponse())
├── internal/runner       (shared: TrialResult shape, ExecuteGraphQL helper)
├── internal/mcpclient    (minimal stdio JSON-RPC MCP client: initialize, tools/list, tools/call)
├── baseline/             (paste full SDL into system prompt, expose execute_graphql tool)
├── scry/                 (spawn `scry serve` as MCP subprocess, no SDL in prompt, expose scry's tools)
├── cmd/aggregate         (read baseline.jsonl + scry.jsonl, render summary.md + summary.json + decision)
├── cmd/record-fixtures   (one-shot recorder: hand-written GraphQL → fixtures/expected.json)
├── cmd/scry-smoke        (standalone subprocess + MCP plumbing smoke — no Anthropic API budget)
└── fixtures/
    ├── github-v4.sdl     (pinned SDL — 1.48MB, ~370k tokens)
    └── expected.json     (ground truth — 5 PRs)
```

Both runners emit `internal/runner.TrialResult`. Both runners use
`internal/task.Prompt()` so the agent receives an identical prompt;
the only difference is the schema-access path.

## Results

_Populated by `make bench-summary` after a full run._

```
(awaiting first live trial — gated on valid ANTHROPIC_API_KEY)
```

## Pricing assumptions

Sonnet 4.6 published rates: **$3/MTok input, $15/MTok output**.
Hardcoded in `cmd/aggregate/main.go` — update if Anthropic shifts
pricing.

## Why GitHub v4

- **Public.** Anyone with a GitHub PAT can repro.
- **Large.** 1756 types via introspection — well past Sonnet 4.6's
  200k context window in raw SDL form. The naive baseline is
  provably infeasible without preprocessing.
- **Well-known.** Reduces the "but the task isn't realistic"
  objection.

Shopify Admin (~3000+ types) is the next stress level but requires
a dev-store + per-tenant auth dance that breaks the "anyone can
repro" property. Future work.

## Why this benchmark exists

scry v0.5 shipped 30+ tests, ~64% server coverage, four transports,
audit chain, persisted queries — technically credible, zero external
users. The product needs a credible artifact before any distribution
motion. The bench's outputs ARE that artifact: a reproducible
side-by-side a stranger on HN can run themselves in 5 minutes.
