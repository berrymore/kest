GOLANGCI_LINT_VERSION := 2.15.0

PKG     := github.com/berrymore/kest
BIN     := bin/kest
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X $(PKG)/internal/buildinfo.version=$(VERSION) \
           -X $(PKG)/internal/buildinfo.commit=$(COMMIT) \
           -X $(PKG)/internal/buildinfo.date=$(DATE)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build bin/kest
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/kest

.PHONY: test
test: ## Run unit tests with the race detector
	go test -race ./...

.PHONY: test-integration
test-integration: ## Run unit and integration tests (needs php and composer in PATH)
	go test -race -tags integration ./...

.PHONY: vet
vet: ## Run go vet
	go vet -tags integration,e2e ./...

.PHONY: test-e2e
test-e2e: ## Run end-to-end tests against the real binary (built with -race)
	go test -count=1 -tags e2e ./test/e2e/...

.PHONY: lint
lint: lint-version ## Run golangci-lint
	golangci-lint run --build-tags integration,e2e ./...

.PHONY: fmt
fmt: lint-version ## Format code (gofmt + goimports)
	golangci-lint fmt ./...

.PHONY: check
check: build vet lint test test-e2e ## Everything that must pass before work counts as done

.PHONY: clean
clean: ## Remove build output
	rm -rf bin

.PHONY: lint-version
lint-version:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not found. Install v$(GOLANGCI_LINT_VERSION) into $$(go env GOPATH)/bin:"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLANGCI_LINT_VERSION)"; \
		exit 1; }
	@v=$$(golangci-lint version --short 2>/dev/null); \
	if [ "$$v" != "$(GOLANGCI_LINT_VERSION)" ]; then \
		echo "golangci-lint $$v found, $(GOLANGCI_LINT_VERSION) expected. Install it with:"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLANGCI_LINT_VERSION)"; \
		exit 1; \
	fi
