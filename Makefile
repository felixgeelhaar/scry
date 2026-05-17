# scry developer Makefile. Keeps the everyday commands a `make X` away
# so the CI workflows + local dev share the same recipes.

GO       ?= go
PKGS     ?= ./...
BIN      ?= scry
BUILD_DIR := dist

.PHONY: all
all: build

.PHONY: tools
tools:
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@v2.11.4
	$(GO) install golang.org/x/vuln/cmd/govulncheck@latest

.PHONY: build
build:
	mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -ldflags "-s -w" -o $(BUILD_DIR)/$(BIN) ./cmd/scry

.PHONY: install
install:
	$(GO) install ./cmd/scry

.PHONY: test
test:
	$(GO) test -race -count=1 -coverprofile=coverage.out $(PKGS)

.PHONY: test-live
test-live:
	$(GO) test -tags=live -count=1 -run TestLive $(PKGS)

.PHONY: test-stdio-smoke
test-stdio-smoke:
	$(GO) test -tags=stdio_smoke -count=1 -timeout 120s -run TestStdio ./internal/server/...

.PHONY: lint
lint:
	$(GO) vet $(PKGS)
	golangci-lint run --timeout=5m

.PHONY: fmt
fmt:
	gofmt -w .
	$(GO) run golang.org/x/tools/cmd/goimports@latest -local github.com/felixgeelhaar/scry -w .

.PHONY: verify
verify: fmt lint test

.PHONY: vuln
vuln:
	govulncheck $(PKGS)

.PHONY: nox
nox:
	@which nox >/dev/null || (echo "install nox: https://github.com/nox-hq/nox/releases" && exit 1)
	nox scan .

.PHONY: snapshot
snapshot:
	@which goreleaser >/dev/null || (echo "install: brew install goreleaser" && exit 1)
	goreleaser release --snapshot --clean

.PHONY: install-hooks
install-hooks:
	@mkdir -p .git/hooks
	@cp scripts/pre-push.sh .git/hooks/pre-push
	@chmod +x .git/hooks/pre-push
	@echo "pre-push hook installed"

.PHONY: clean
clean:
	rm -rf $(BUILD_DIR) coverage.out findings.json nox-findings.json ai.inventory.json
