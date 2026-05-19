# GitHub v4 benchmark — full-SDL baseline vs. scry-routed agent.
#
# Reproduces the v0.6 wedge proof on a real-world schema (~700
# types). Captures input/output tokens, latency, success rate, and
# $$ per task across 10 trials per runner.
#
# Requires:
#   GITHUB_TOKEN       — PAT with `public_repo` scope (for the
#                        canonical task; no write access needed).
#   ANTHROPIC_API_KEY  — for Claude Sonnet 4.5 (the agent under test).
#
# Reproduces from a clean checkout:
#   make bench

SHELL := /bin/bash
GO    ?= go

# Pin Claude model so trial-to-trial cost + tokens are comparable
# across users running the bench at different dates.
MODEL ?= claude-sonnet-4-5
TRIALS ?= 10

.PHONY: bench bench-baseline bench-scry bench-summary bench-clean bench-fixtures

# Full pipeline. Refresh fixtures only when explicitly requested
# (re-recording ground truth shifts every comparison). bench-clean
# wipes results between runs but keeps fixtures.
bench: bench-baseline bench-scry bench-summary

bench-baseline:
	@echo ">> baseline: full-SDL in system prompt × $(TRIALS) trials"
	$(GO) run ./baseline -model $(MODEL) -trials $(TRIALS) -out results/baseline.jsonl

bench-scry:
	@echo ">> scry: schema_search → query_validate → query_execute × $(TRIALS) trials"
	$(GO) run ./scry -model $(MODEL) -trials $(TRIALS) -out results/scry.jsonl

bench-summary:
	@echo ">> aggregating results"
	$(GO) run ./results -baseline results/baseline.jsonl -scry results/scry.jsonl -out results/summary.md

bench-fixtures:
	@echo ">> WARNING: refreshing fixtures invalidates prior comparisons"
	$(GO) run ./fixtures -out fixtures/

bench-clean:
	rm -f results/*.jsonl results/summary.md results/summary.json
