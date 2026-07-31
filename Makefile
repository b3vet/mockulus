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

# The JavaScript side of the tree: the admin UI in ui/, which vite builds into
# internal/adminui/dist for go:embed to pick up.
#
# `build` above deliberately does not depend on `ui-build`, and neither does
# `test`. Node is not a build dependency of mockulus: the repo commits
# internal/adminui/dist/.gitkeep as a placeholder, and when index.html is
# absent the handler serves a "built without UI" notice instead. That is what
# lets someone clone this repo, run `make build`, and get a working server
# without first installing a toolchain they never asked for. Wiring ui-build in
# as a prerequisite would take that away from every Go-only contributor in
# exchange for convenience the people who need the UI can get by typing
# `make ui-build build`.
#
# The guarantee that shipped artifacts do carry the UI is therefore enforced
# where artifacts are actually produced, not here: the Dockerfile builds it in
# its own Node stage, .goreleaser.yaml runs ui-build as a before-hook, and the
# CI lanes that gate a binary against the corpus build it first. A missing UI
# in a release is a broken release; a missing UI in a local `go build` is a
# Tuesday.
PNPM ?= pnpm

# --frozen-lockfile is left off on purpose. pnpm applies it by default when CI
# is set, so the pipelines get the reproducible install for free, while someone
# who is halfway through editing ui/package.json is not stopped by a lockfile
# they have not finished updating yet.
.PHONY: ui-build
ui-build: ## Build the admin UI into internal/adminui/dist (needs Node and pnpm)
	$(PNPM) install
	$(PNPM) run build

.PHONY: ui-dev
ui-dev: ## Serve the admin UI from vite, proxying the API to a local `make run`
	$(PNPM) install
	$(PNPM) run dev

.PHONY: ui-check
ui-check: ## Type-check, lint, format-check and unit-test the admin UI
	$(PNPM) install
	$(PNPM) run check
	$(PNPM) run test

# The other package in the workspace: @mockulus/admin-sdk, the published client.
# It is kept separate from the ui-* targets rather than folded into a single
# js-check, because the two fail for unrelated reasons and a contributor who
# broke one should not have to read past the other's output to find out.
.PHONY: sdk-build
sdk-build: ## Compile the TypeScript admin SDK into sdk/typescript/dist
	$(PNPM) install
	$(PNPM) run sdk:build

.PHONY: sdk-check
sdk-check: ## Type-check, lint, format-check and unit-test the admin SDK
	$(PNPM) install
	$(PNPM) run sdk:gen:check
	$(PNPM) run sdk:check
	$(PNPM) run sdk:test

# The generated types are committed and diffed rather than produced at install
# time, the same way the §13 config table and docs/compatibility.md are: a
# reader of this repository, and the admin UI that imports the SDK from the
# workspace, should never need a generation step to see what the types are.
.PHONY: sdk-gen
sdk-gen: ## Regenerate the SDK's types from api/openapi.yaml
	$(PNPM) install
	$(PNPM) run sdk:gen

# Drives a real mockulus the suite starts itself, so the binary has to exist.
# This is the SDK's regression gate: a client that type-checks against the
# contract can still send something the server refuses, and only a live round
# trip finds that.
.PHONY: sdk-integration
sdk-integration: build ## Run the SDK integration suite against a freshly built binary
	$(PNPM) install
	$(PNPM) run sdk:test:integration

# The contract half of the coupling rule in AGENTS.md. The SDK's types are
# generated from api/openapi.yaml, so an admin route that reaches the server
# without reaching the contract is a call the SDK cannot make — and one that
# reaches the contract without reaching the server is a call that compiles and
# then 404s. Neither shows up by reading either file on its own.
.PHONY: contract-lint
contract-lint: ## Cross-check api/openapi.yaml against the behavior catalog, both ways
	$(GO) run ./scripts/contractlint

.PHONY: test
test: ## Run unit tests with the race detector
	$(GO) test -race -count=1 ./...

# The allocation ceilings of SPEC §16.3 rule 1 and D-OPEN-14. They are built out
# of the race suite above, because the race detector allocates on its own
# account and not the same number of times twice, so this is the run that
# actually asserts them.
.PHONY: test-alloc
test-alloc: ## Assert the hot-path allocation budgets (no race detector)
	$(GO) test -count=1 -run 'AllocBudget' ./internal/match

.PHONY: test-cover
test-cover: ## Run unit tests and report coverage of the correctness core
	$(GO) test -race -count=1 -coverprofile=coverage.txt -covermode=atomic ./...
	@$(GO) tool cover -func=coverage.txt | tail -1

# Benchmark knobs. BENCHCOUNT is the one worth reaching for: §16.2 compares runs
# with benchstat, which needs several samples per benchmark to say whether a
# difference is a regression or the machine having a bad minute.
BENCH      ?= .
BENCHTIME  ?= 1s
BENCHCOUNT ?= 1

.PHONY: bench
bench: ## Run microbenchmarks with allocation counts (BENCH=<re> BENCHCOUNT=<n>)
	$(GO) test -run '^$$' -bench '$(BENCH)' -benchmem \
		-benchtime $(BENCHTIME) -count $(BENCHCOUNT) ./...

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

# The compatibility matrix is derived from the same three files the E2E gate is
# derived from — the spec's rows, the behavior catalog, the corpus — so the
# document cannot claim support the gate does not enforce (SPEC §20, M6). The
# hand-written preamble of docs/compatibility.md is preserved; only the region
# between the GENERATED MATRIX markers is rewritten.
.PHONY: compat-docs
compat-docs: ## Regenerate docs/compatibility.md from the behavior catalog and corpus
	$(GO) run ./scripts/compatmatrix

.PHONY: compat-docs-check
compat-docs-check: ## Verify docs/compatibility.md matches the catalog and corpus
	$(GO) run ./scripts/compatmatrix -check

.PHONY: spdx
spdx: ## Verify every mockulus-authored source file carries an SPDX header
	@./scripts/check-spdx.sh

# The two halves of the §22.1 license gate. `check` is the one that can fail a
# build; `report` regenerates the attribution file CI diffs against, which is
# what stops THIRD_PARTY_LICENSES from becoming a record that was true once.
LICENSE_ALLOWLIST := Apache-2.0,MIT,BSD-2-Clause,BSD-3-Clause,ISC

# Both halves resolve the graph for the platform that actually ships. A build
# graph is per-GOOS, and `prometheus/client_golang` reaches `prometheus/procfs`
# only on Linux — so a report generated on a developer's macOS omits an
# Apache-2.0 dependency that is inside every image we publish, and the
# attribution file is then wrong about the artifact rather than about the
# working directory it was generated in.
LICENSE_GOOS   := linux
LICENSE_GOARCH := amd64

.PHONY: license-check
license-check: ## Verify every module in the shipped binary's graph is on the allowlist
	GOOS=$(LICENSE_GOOS) GOARCH=$(LICENSE_GOARCH) \
		go-licenses check ./cmd/... ./internal/... --allowed_licenses=$(LICENSE_ALLOWLIST)

.PHONY: license-report
license-report: ## Regenerate THIRD_PARTY_LICENSES from the module graph
	# Generated aside and moved into place only on success. Redirecting straight
	# at the file truncates it before the command runs, so a missing go-licenses
	# empties the attribution file and the failure reads as a tool that is not
	# installed rather than as the working tree it just damaged.
	GOOS=$(LICENSE_GOOS) GOARCH=$(LICENSE_GOARCH) \
		go-licenses report ./cmd/mockulus --template=.github/licenses.tpl \
		--ignore $(MODULE) > THIRD_PARTY_LICENSES.tmp
	mv THIRD_PARTY_LICENSES.tmp THIRD_PARTY_LICENSES

# The other half of §22.1, for the half of the tree go-licenses cannot see.
# It resolves the Go module graph and stops there, so every npm package the
# admin UI is built from — including the ones whose code vite inlines into the
# bundle the binary embeds — would otherwise pass through the license gate
# unexamined. The allowlist and its exceptions are argued in the script.
.PHONY: npm-license-check
npm-license-check: ## Verify every npm package in the workspace is permissively licensed
	@./scripts/check-npm-licenses.sh

# No ui-build prerequisite here either, and for a happier reason than above:
# the Dockerfile builds the UI in its own node:22-alpine stage, so `make image`
# produces an image with the UI in it on a host that has never seen Node. That
# also keeps `docker build .` self-contained for anyone building from a clean
# checkout, which is what the release pipeline does.
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
	# The admin UI's build output goes too, but never the .gitkeep beside it:
	# that file is committed, and go:embed fails outright on a directory with
	# nothing in it, so deleting it would leave `make build` broken rather than
	# clean. Emptying the rest is the point — a stale bundle embedded into a
	# binary built an hour later is a confusing way to find out that ui-build
	# is a separate step.
	@if [ -d internal/adminui/dist ]; then \
		find internal/adminui/dist -mindepth 1 ! -name .gitkeep -delete; \
	fi
