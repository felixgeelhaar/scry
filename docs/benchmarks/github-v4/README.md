# GitHub v4 benchmark — full-SDL vs. scry

Reproducible head-to-head: same task, same model, same upstream.
Two paths into GitHub's GraphQL schema (~700 types). Measures
whether scry's search-first wedge translates into real token +
success deltas on a non-toy schema.

## What this proves (or refutes)

scry's top-of-funnel claim: ~50× context reduction by replacing
"paste the SDL" with FTS5-ranked search. This benchmark records
the actual delta on one realistic agent task. Two outcomes:

- **scry success rate ≥ 85% AND token compression ≥ 10×** →
  the wedge holds on a real schema. Distribute (HN, Apollo Slack,
  r/LocalLLaMA).
- **Below either threshold** → ship v0.7 grounded in observed
  failure modes (embedding-backed search, response-shape
  changes, docstring examples), not speculation.

## The task

> Find every PR I opened in `<repo>` that touched file `<X>` in the
> last 30 days, with reviewer + merge status.

Multi-field, joins across User → PullRequest → File → Review.
Picking ~5 fields out of GitHub's ~700-type schema is the
schema-navigation cost the wedge is supposed to compress.

Ground truth recorded once via a hand-written GraphQL query
against the pinned repo + date window, persisted to
`fixtures/expected.json`. Re-recording invalidates prior
comparisons — gated behind `make bench-fixtures` for that reason.

## Reproduce

```bash
export GITHUB_TOKEN=ghp_…       # public_repo scope is enough
export ANTHROPIC_API_KEY=sk-ant-…

cd docs/benchmarks/github-v4
make bench
```

Outputs:

- `results/baseline.jsonl` — per-trial metrics for full-SDL runner.
- `results/scry.jsonl` — per-trial metrics for scry-routed runner.
- `results/summary.md` — comparison table (token compression, success
  rate, p50/p95 latency, $$/task).
- `results/summary.json` — same numbers in machine-readable form.

`make bench-baseline` / `make bench-scry` run one side at a time.
`make bench-clean` wipes results between trials; `make bench-fixtures`
re-records ground truth (rare).

## Results

_Populated by `make bench-summary` after a full run._

| Metric | Baseline (full SDL) | scry (search-first) | Delta |
|---|---|---|---|
| Avg input tokens | — | — | — |
| Avg output tokens | — | — | — |
| p50 latency | — | — | — |
| Success rate (n=10) | — | — | — |
| $$ per task | — | — | — |

**Go / no-go decision:** see `results/summary.md` after running.

## Why GitHub v4 (and not Shopify / Linear)

- **Public.** Anyone reading this can repro with a PAT.
- **Large.** ~700 types is mid-tier — enough to exercise the
  context budget, small enough to fit the full SDL in Claude's
  window for a fair baseline.
- **Well-known.** Reduces the "but the task isn't realistic" objection.

Shopify Admin (~1500 types) is a better stress test but requires a
dev-store + auth dance that breaks reproducibility for outsiders.
Future work.
