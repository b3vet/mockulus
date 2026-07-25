# SPDX-License-Identifier: Apache-2.0

BINARY      := mockulus
MODULE      := github.com/b3vet/mockulus
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE       ?= ghcr.io/b3vet/mockulus
GO          ?= go
GOFLAGS     ?=
LDFLAGS     := -s -w -X main.version=$(VERSION)

# Packages whose unit-test coverage is held to the higher correctness-core bar
# (SPEC §19.2).
CORE_PKGS := ./internal/match/... ./internal/matchers/... ./internal/stub/... \
             ./internal/template/... ./internal/scenario/...

.DEFAULT_GOAL := help

.PHONY: help
help: ## List available targets
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the mockulus binary into bin/
	$(GO) build $(GOFLAGS) -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/mockulus

.PHONY: run
run: ## Run mockulus locally with text logs
	MOCKULUS_LOG_FORMAT=text $(GO) run ./cmd/mockulus

.PHONY: test
test: ## Run unit tests with the race detector
	$(GO) test -race -count=1 ./...

.PHONY: test-cover
test-cover: ## Run unit tests and report coverage of the correctness core
	$(GO) test -race -count=1 -coverprofile=coverage.txt -covermode=atomic ./...
	@$(GO) tool cover -func=coverage.txt | tail -1

.PHONY: bench
bench: ## Run microbenchmarks with allocation counts
	$(GO) test -run '^$$' -bench . -benchmem ./...

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run

.PHONY: fmt
fmt: ## Format the tree
	$(GO) fmt ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: config-docs
config-docs: ## Regenerate the SPEC §13 configuration table from the config struct
	$(GO) test ./internal/config -run TestSpecConfigTable -update

.PHONY: spdx
spdx: ## Verify every mockulus-authored source file carries an SPDX header
	@./scripts/check-spdx.sh

.PHONY: image
image: ## Build the shippable container image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

.PHONY: image-cover
image-cover: ## Build the coverage-instrumented image the E2E gate runs against
	docker build --build-arg VERSION=$(VERSION) --build-arg COVER=1 \
		-t $(IMAGE):$(VERSION)-cover .

.PHONY: e2e
e2e: ## Run the E2E regression gate (SPEC §19)
	$(GO) run ./test/e2e/runner --corpus test/e2e/corpus --catalog test/e2e/catalog

.PHONY: e2e-catalog
e2e-catalog: ## Check the behavior catalog against the spec and the corpus
	$(GO) run ./test/e2e/runner --catalog test/e2e/catalog --check-only

.PHONY: clean
clean: ## Remove build and test artifacts
	rm -rf bin dist coverage.txt test/e2e/.artifacts
