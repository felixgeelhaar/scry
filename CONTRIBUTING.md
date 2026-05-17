# Contributing to scry

Thanks for your interest. scry is a searchable GraphQL ↔ MCP bridge;
contributions stick to atomic commits, TDD where practical, and
conventional commit messages.

## Quick start

```bash
git clone https://github.com/felixgeelhaar/scry.git
cd scry
go build ./...
go test ./...                              # unit tests
go test -tags=live ./internal/schema/...   # live tests (hit public GraphQL endpoints)
go test -tags=stdio_smoke ./internal/server/...  # MCP stdio JSON-RPC smoke
```

## Development workflow

1. Branch from `main`: `git checkout -b feat/<short-name>`.
2. Follow TDD when changing behaviour: failing test → make it pass →
   refactor.
3. Keep commits atomic. Use [Conventional Commits](https://www.conventionalcommits.org/):
   - `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`,
     `perf:`, `security:`.
4. Run `go test ./... && go vet ./... && golangci-lint run` before
   pushing.
5. Open a pull request describing what changed and why. Link related
   issues.

## Code standards

- **Go**: `gofmt`, `goimports` (`local-prefixes=github.com/felixgeelhaar/scry`),
  `go vet`, `golangci-lint run`. Exported APIs carry GoDoc.
- **Tests**: unit + integration (build-tagged `live` / `stdio_smoke`).
  New behaviour without tests is rejected unless documented why.
- **Security**: never log bearer tokens. Use `obs.RedactTokenRef` when
  surfacing references. Audit dir + servers.yml + clients.yml must
  be 0600 (POSIX); load paths refuse looser perms.
- **Determinism**: schema introspection must produce identical units
  across runs for a stable upstream. Tests should not depend on
  network unless build-tagged.

## Security disclosures

Don't file public issues for security-sensitive reports. Email
[security@felixgeelhaar.dev](mailto:security@felixgeelhaar.dev) with
a description and reproduction steps.

## License

By contributing you agree your contributions are licensed under the
Apache License 2.0 (see `LICENSE`).
