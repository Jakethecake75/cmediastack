VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.Version=$(VERSION)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binary (cgo-free, per ADR-0002)
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/cmediastack ./cmd/cmediastack

.PHONY: test
test: ## Run the test suite
	go test -count=1 ./...

.PHONY: test-race
test-race: ## Run the test suite with the race detector
	go test -race -count=1 ./...

.PHONY: cover
cover: ## Report coverage per package
	go test -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out

# The scanners CI pins, pinned here too: "clean locally" means "clean in CI",
# whatever happens to be installed. Move them with ci.yml, deliberately.
GOLANGCI_LINT_VERSION := v2.14.0
GOSEC_VERSION         := v2.29.0

.PHONY: lint
lint: ## Run golangci-lint at the version CI pins
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --timeout=5m ./...

.PHONY: security
security: ## Run the security gates locally (same as CI)
	go vet ./...
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	go run github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION) -severity medium -confidence medium -exclude-dir=web ./...

.PHONY: acceptance
acceptance: ## Run only the Phase 1 acceptance tests
	@echo "== (a) route enumeration: anonymous rejection =="
	go test ./internal/api/ -run TestEveryNonAllowlistedRouteRejectsAnonymous -v
	@echo "== (b) pending and un-enrolled accounts reach nothing =="
	go test ./internal/api/ -run 'TestPendingAccountIsIndistinguishable|TestAwaitingMFAReachesOnlyEnrollment|TestSuspendedAndDisabled' -v
	@echo "== (c) low-privilege user cannot reach admin =="
	go test ./internal/api/ -run 'TestUserCannotReachAnyAdminRoute|TestManagerCannotReachAdminOnly' -v
	go test ./internal/authz/ -run 'TestUserCannotReach' -v
	@echo "== (d) manager cannot escalate =="
	go test ./internal/authz/ -run 'TestManagerCannot|TestNobodyCanModifyTheirOwnRole' -v

.PHONY: genkey
genkey: ## Generate a master key for CMS_MASTER_KEY
	@head -c32 /dev/urandom | base64

.PHONY: docker
docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) -t cmediastack:$(VERSION) .

.PHONY: check-config
check-config: build ## Validate a config file without starting the server
	./bin/cmediastack --check --config $(or $(CONFIG),config/config.yaml)

.PHONY: clean
clean:
	rm -rf bin coverage.out
