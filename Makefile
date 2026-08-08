# atl — build, test, and release helpers.
#
# Common targets:
#   make build            build ./cmd/atl into ./atl (version-stamped)
#   make test             run unit tests
#   make lint             run golangci-lint (if installed)
#   make vet              go vet
#   make check-core-race-coverage run the shared release-grade root-core test gate
#   make gen-plugins      regenerate skills/ and plugins/atl/skills/ from skills-src/
#   make check-plugins    verify the generated plugin trees are current
#   make check-skill-safety validate designated read-only skill shell blocks
#   make check-repository-skills validate repository-only maintainer skills
#   make check-docs-catalog validate the maintained public Markdown inventory
#   make check-docs-freshness bind CLI leaves, safety docs, and change-impact gates
#   make check-reference-split validate generated legacy reference indexes
#   make check-context7-docs validate the public Context7 parsing/snippet boundary
#   make update-reference-navigation regenerate navigation in large references
#   make check-onboarding-docs validate first-use links and offline command paths
#   make check-maintainer-contract verify the exact Go maintainer toolchain
#   make check-maintainability enforce reviewed production growth ratchets
#   make check-windows-compile verify Windows source cross-compilation
#   make check-module-boundary verify the reviewed two-module layout
#   make check-package-boundary verify root-core and bilateral module boundaries
#   make agent-eval-compat run the small product/evaluation compatibility gate
#   make agent-eval-full   run every independent evaluator-module gate
#   make live-smoke       run opt-in live CLI smoke checks
#   make dist             cross-compile release binaries into ./dist
#   make manifest         generate dist/manifest.json from ./dist binaries
#   make homebrew         generate dist/atl.rb (Homebrew formula) from ./dist
#   make genkey           generate an ed25519 release signing key (off-CI)

MODULE   := github.com/isukharev/atl
REPO     := isukharev/atl
VERSION  := $(shell cat VERSION 2>/dev/null || echo dev)
BUILD_COMMIT ?= $(shell git rev-parse --verify HEAD 2>/dev/null || echo unknown)
BUILD_STATE  ?= $(shell if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then test -z "$$(git status --porcelain --untracked-files=normal)" && echo clean || echo dirty; else echo unknown; fi)
LDFLAGS  := -s -w -X $(MODULE)/internal/version.Version=$(VERSION) -X $(MODULE)/internal/version.Commit=$(BUILD_COMMIT) -X $(MODULE)/internal/version.BuildState=$(BUILD_STATE)
GOFLAGS  := -trimpath
GO_ENV   := env -u GOROOT GOTOOLCHAIN=auto GOWORK=off
GO_LOCAL_ENV := env -u GOROOT GOTOOLCHAIN=local GOWORK=off
AGENT_EVAL_DIR := internal/agenteval
AGENT_EVAL_MAKE := $(MAKE) -C $(AGENT_EVAL_DIR) REPOSITORY_ROOT="$(CURDIR)" ATL_BINARY="$(CURDIR)/atl"

# Platforms published to GitHub Releases. Keep in sync with the release workflow.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build
build:
	$(GO_ENV) CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o atl ./cmd/atl

.PHONY: install
install:
	$(GO_ENV) CGO_ENABLED=0 go install $(GOFLAGS) -ldflags "$(LDFLAGS)" ./cmd/atl

.PHONY: test
test:
	@packages="$$( $(GO_ENV) go run ./scripts/list-go-packages --class root-core)" && \
		test -n "$$packages" && $(GO_ENV) go test $$packages

.PHONY: race
race:
	@packages="$$( $(GO_ENV) go run ./scripts/list-go-packages --class root-core)" && \
		test -n "$$packages" && $(GO_ENV) go test -race $$packages

# Shared by pull-request CI and tag releases. Keep package selection routed
# through list-go-packages so the race and cross-package coverage scopes cannot
# silently drift apart as the root module changes.
.PHONY: check-core-race-coverage
check-core-race-coverage:
	@root_core_packages="$$( $(GO_ENV) go run ./scripts/list-go-packages --class root-core)" && \
		root_core_cover="$$( $(GO_ENV) go run ./scripts/list-go-packages --class root-core --scope internal --format csv)" && \
		test -n "$$root_core_packages" && test -n "$$root_core_cover" && \
		$(GO_ENV) go test -race -covermode=atomic -coverprofile=cover.out -coverpkg="$$root_core_cover" -count=1 -timeout=10m $$root_core_packages
	@$(GO_ENV) go run ./scripts/check-coverage --profile cover.out --minimum "84.0"

# Live integration tests against a REAL Confluence/Jira Data Center. Opt-in only —
# never part of `make test` and never run in CI. Reads local-only ./.env.integration
# (copy .env.integration.example and fill in your DC URL, PATs, and throwaway test
# objects); that file is gitignored so the real URL/tokens never reach the repo.
.PHONY: integration
integration:
	@test -f .env.integration || { echo "missing .env.integration — run: cp .env.integration.example .env.integration && edit it"; exit 1; }
	@set -e; packages="$$( $(GO_ENV) go run ./scripts/list-go-packages --class root-core)"; \
		test -n "$$packages"; set -a; . ./.env.integration; set +a; \
		ATL_INTEGRATION=1 $(GO_ENV) go test $$packages -run Integration -count=1 -v

# CLI-level live smoke against locally configured fixtures. This complements
# `make integration`: it exercises the built binary and optional fixture-specific
# Jira Structure / Confluence table paths. Real fixture IDs stay in
# .env.integration, which is gitignored.
.PHONY: live-smoke
live-smoke: build
	./scripts/live-smoke.sh

.PHONY: vet
vet:
	$(GO_ENV) go vet ./...

.PHONY: lint
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed: https://golangci-lint.run/usage/install/"; exit 1; }
	$(GO_ENV) golangci-lint run

.PHONY: gen-plugins
gen-plugins:
	$(GO_ENV) go run ./scripts/gen-plugins
	cp .mcp.json plugins/atl/.mcp.json

.PHONY: check-plugins
check-plugins: check-skill-safety check-skill-routing
	$(GO_ENV) go run ./scripts/gen-plugins --check

.PHONY: check-skill-safety
check-skill-safety:
	$(GO_ENV) go run ./scripts/check-skill-safety

.PHONY: check-skill-routing
check-skill-routing:
	$(GO_ENV) go run ./scripts/check-skill-routing --root .

.PHONY: check-repository-skills
check-repository-skills:
	$(GO_ENV) go run ./scripts/check-repository-skills -root .

.PHONY: check-context7-docs
check-context7-docs:
	$(GO_ENV) go run ./scripts/check-context7-docs

.PHONY: update-reference-navigation
update-reference-navigation:
	$(GO_ENV) go run ./scripts/check-context7-docs -write-navigation

.PHONY: check-docs-catalog
check-docs-catalog:
	$(GO_ENV) go run ./scripts/check-docs-catalog -root .

.PHONY: check-docs-freshness
check-docs-freshness:
	$(GO_ENV) go run ./scripts/check-docs-freshness -root .

.PHONY: check-reference-split
check-reference-split:
	$(GO_ENV) go run ./scripts/check-reference-split -root .

.PHONY: check-onboarding-docs
check-onboarding-docs: build
	ATL_NO_UPDATE=1 $(GO_ENV) go run ./scripts/check-onboarding-docs -root . -atl ./atl

.PHONY: check-maintainer-contract
check-maintainer-contract:
	$(GO_LOCAL_ENV) go run ./scripts/check-maintainer-contract

.PHONY: check-maintainability
check-maintainability:
	$(GO_ENV) go run ./scripts/check-maintainability

.PHONY: check-windows-compile
check-windows-compile:
	$(GO_ENV) GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...

.PHONY: check-module-boundary
check-module-boundary:
	$(GO_ENV) go run ./scripts/check-module-boundary -root .

.PHONY: check-package-boundary
check-package-boundary: check-module-boundary
	@root_core="$$( $(GO_ENV) go run ./scripts/list-go-packages --class root-core)" && \
		test -n "$$root_core"

.PHONY: agent-eval-build agent-eval-unit agent-eval-race agent-eval-lint agent-eval-vet agent-eval-vuln agent-eval-tidy-check agent-eval-windows
agent-eval-build:
	$(AGENT_EVAL_MAKE) build

agent-eval-unit:
	$(AGENT_EVAL_MAKE) unit

agent-eval-race:
	$(AGENT_EVAL_MAKE) race

agent-eval-lint:
	$(AGENT_EVAL_MAKE) lint

agent-eval-vet:
	$(AGENT_EVAL_MAKE) vet

agent-eval-vuln:
	$(AGENT_EVAL_MAKE) vuln

agent-eval-tidy-check:
	$(AGENT_EVAL_MAKE) tidy-check

agent-eval-windows:
	$(AGENT_EVAL_MAKE) windows

.PHONY: agent-eval-compat
agent-eval-compat: check-skill-routing
	$(AGENT_EVAL_MAKE) compat

.PHONY: agent-eval-contract
agent-eval-contract: check-skill-routing
	$(AGENT_EVAL_MAKE) contract

.PHONY: agent-eval-product-boundary
agent-eval-product-boundary: check-package-boundary

.PHONY: agent-eval-full
agent-eval-full: check-skill-routing check-module-boundary
	$(AGENT_EVAL_MAKE) full

.PHONY: tidy
tidy:
	$(GO_ENV) go mod tidy

.PHONY: install-hooks
install-hooks:
	cp hooks/pre-commit .git/hooks/pre-commit
	chmod +x .git/hooks/pre-commit

.PHONY: clean
clean:
	rm -rf atl dist

# Cross-compile every published platform into ./dist as atl-<os>-<arch>,
# alongside a .sha256 for each. CGO disabled => fully static binaries.
.PHONY: dist
dist: clean
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; out=dist/atl-$$os-$$arch; \
		echo "build $$out"; \
		$(GO_ENV) CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $$out ./cmd/atl || exit 1; \
		( cd dist && sha256sum atl-$$os-$$arch > atl-$$os-$$arch.sha256 ); \
	done
	@echo "$(VERSION)" > dist/VERSION

# Generate dist/manifest.json (version + per-binary sha256) from ./dist.
# Signing happens in CI (scripts/sign-manifest.go) with the release secret.
.PHONY: manifest
manifest:
	$(GO_ENV) go run ./scripts/gen-manifest --dist dist --version "$(VERSION)" > dist/manifest.json
	@echo "wrote dist/manifest.json"

# Generate the Homebrew formula (dist/atl.rb) from ./dist: each platform's
# release-asset URL pinned to its sha256. Published as a release asset; the tap
# repository that serves it (`brew install <owner>/tap/atl`) is created and
# maintained by the project owner — copy dist/atl.rb into its Formula/ dir.
.PHONY: homebrew
homebrew:
	$(GO_ENV) go run ./scripts/gen-homebrew-formula --dist dist --version "$(VERSION)" --repo "$(REPO)" > dist/atl.rb
	@echo "wrote dist/atl.rb"

# Generate an ed25519 signing keypair OUTSIDE CI. Prints the public key to embed
# in internal/selfupdate/pubkey.go and writes the private key to a gitignored
# file. NEVER commit the private key — store it as the ATL_RELEASE_PRIVATE_KEY
# GitHub Actions secret, then delete the local copy.
.PHONY: genkey
genkey:
	$(GO_ENV) go run ./scripts/genkey
