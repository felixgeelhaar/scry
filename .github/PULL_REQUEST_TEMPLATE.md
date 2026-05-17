## Summary

<!-- 1–3 bullets: what changed + why. -->

## Type

<!-- Pick one. Match the commit prefix per CONTRIBUTING.md. -->
- [ ] `feat` — new functionality
- [ ] `fix` — bug fix
- [ ] `refactor` — code restructure, no behaviour change
- [ ] `docs` — documentation only
- [ ] `test` — tests added or changed
- [ ] `chore` / `ci` — tooling / pipeline
- [ ] `security` — vuln fix or hardening
- [ ] `perf` — performance-relevant

## Checks

- [ ] `make verify` passes locally (fmt + lint + race-tests)
- [ ] New behaviour has tests (unit + integration where applicable)
- [ ] Security-sensitive paths use `obs.RedactTokenRef` for any
      token-shaped data in logs / traces / metrics / responses
- [ ] CHANGELOG.md entry added under **Unreleased**
- [ ] Public Go APIs carry GoDoc

## Test plan

<!-- Steps a reviewer can run to confirm the change works.
     Example: `go test ./internal/foo/...`, run `scry serve --upstream X
     --transport http`, etc. -->
