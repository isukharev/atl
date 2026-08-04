# atl — build, test, and release helpers.
#
# Common targets:
#   make build            build ./cmd/atl into ./atl (version-stamped)
#   make test             run unit tests
#   make lint             run golangci-lint (if installed)
#   make vet              go vet
#   make check-core-race-coverage run the shared release-grade core test gate
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
#   make check-package-boundary verify the core/heavy dependency split
#   make agent-eval-compat run the small product/evaluation compatibility gate
#   make agent-eval-contract run the complete deterministic evaluation gate
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

# Platforms published to GitHub Releases. Keep in sync with the release workflow.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build
build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o atl ./cmd/atl

.PHONY: install
install:
	CGO_ENABLED=0 go install $(GOFLAGS) -ldflags "$(LDFLAGS)" ./cmd/atl

.PHONY: test
test:
	@packages="$$(go run ./scripts/list-go-packages --class core)" && \
		test -n "$$packages" && go test $$packages

.PHONY: race
race:
	@packages="$$(go run ./scripts/list-go-packages --class core)" && \
		test -n "$$packages" && go test -race $$packages

# Shared by pull-request CI and tag releases. Keep package selection routed
# through list-go-packages so the race and cross-package coverage scopes cannot
# silently drift apart as heavy evaluator packages are added.
.PHONY: check-core-race-coverage
check-core-race-coverage:
	@core_packages="$$(go run ./scripts/list-go-packages --class core)" && \
		core_cover="$$(go run ./scripts/list-go-packages --class core --scope internal --format csv)" && \
		test -n "$$core_packages" && test -n "$$core_cover" && \
		go test -race -covermode=atomic -coverprofile=cover.out -coverpkg="$$core_cover" -count=1 -timeout=10m $$core_packages
	@go run ./scripts/check-coverage --profile cover.out --minimum "84.0"

# Live integration tests against a REAL Confluence/Jira Data Center. Opt-in only —
# never part of `make test` and never run in CI. Reads local-only ./.env.integration
# (copy .env.integration.example and fill in your DC URL, PATs, and throwaway test
# objects); that file is gitignored so the real URL/tokens never reach the repo.
.PHONY: integration
integration:
	@test -f .env.integration || { echo "missing .env.integration — run: cp .env.integration.example .env.integration && edit it"; exit 1; }
	@set -e; packages="$$(go run ./scripts/list-go-packages --class core)"; \
		test -n "$$packages"; set -a; . ./.env.integration; set +a; \
		ATL_INTEGRATION=1 go test $$packages -run Integration -count=1 -v

# CLI-level live smoke against locally configured fixtures. This complements
# `make integration`: it exercises the built binary and optional fixture-specific
# Jira Structure / Confluence table paths. Real fixture IDs stay in
# .env.integration, which is gitignored.
.PHONY: live-smoke
live-smoke: build
	./scripts/live-smoke.sh

.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed: https://golangci-lint.run/usage/install/"; exit 1; }
	golangci-lint run

.PHONY: gen-plugins
gen-plugins:
	go run ./scripts/gen-plugins
	cp .mcp.json plugins/atl/.mcp.json

.PHONY: check-plugins
check-plugins: gen-plugins check-skill-safety check-skill-routing
	@test -z "$$(git status --porcelain -- skills plugins/atl/skills plugins/atl/.mcp.json plugins/atl/skill-catalog.v1.json)" || { \
		git status --porcelain -- skills plugins/atl/skills plugins/atl/.mcp.json plugins/atl/skill-catalog.v1.json; \
		echo "generated plugin outputs are stale or hand-edited: edit skills-src/, run 'make gen-plugins', commit every generated output"; exit 1; }

.PHONY: check-skill-safety
check-skill-safety:
	go run ./scripts/check-skill-safety

.PHONY: check-skill-routing
check-skill-routing:
	go run ./scripts/check-skill-routing --root .

.PHONY: check-repository-skills
check-repository-skills:
	go run ./scripts/check-repository-skills -root .

.PHONY: check-context7-docs
check-context7-docs:
	go run ./scripts/check-context7-docs

.PHONY: update-reference-navigation
update-reference-navigation:
	go run ./scripts/check-context7-docs -write-navigation

.PHONY: check-docs-catalog
check-docs-catalog:
	go run ./scripts/check-docs-catalog -root .

.PHONY: check-docs-freshness
check-docs-freshness:
	go run ./scripts/check-docs-freshness -root .

.PHONY: check-reference-split
check-reference-split:
	go run ./scripts/check-reference-split -root .

.PHONY: check-onboarding-docs
check-onboarding-docs: build
	ATL_NO_UPDATE=1 go run ./scripts/check-onboarding-docs -root . -atl ./atl

.PHONY: check-maintainer-contract
check-maintainer-contract:
	GOTOOLCHAIN=local go run ./scripts/check-maintainer-contract

.PHONY: check-maintainability
check-maintainability:
	go run ./scripts/check-maintainability

.PHONY: check-windows-compile
check-windows-compile:
	GOROOT= GOTOOLCHAIN=auto GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...

.PHONY: check-package-boundary
check-package-boundary:
	@core="$$(go run ./scripts/list-go-packages --class core)" && \
		heavy="$$(go run ./scripts/list-go-packages --class heavy)" && \
		test -n "$$core" && test -n "$$heavy"

.PHONY: agent-eval-compat
agent-eval-compat: check-skill-routing build
	go test ./internal/agenteval -run '^(TestRepositoryBenchmarkCorpusContract|TestRepositoryScenarioCapabilitiesMatchCatalog|TestEvaluatorProductDependencyLedger|TestEvaluatorProductDependencyLedgerDetectsAliasAndOwnershipDrift|TestParseCLIErrorContractAdmitsOnlyTypedFailedCLIErrors|TestCLIErrorContractVocabularyMatchesVersionedWireFixture|TestCLIErrorRecoveryV1AcceptsOnlyDocumentedShapes|TestPinnedCapabilityCatalogIsStrictAndImmutable|TestDecodeCapabilityCatalogFailsClosed|TestVerifyPinnedCapabilityCatalogChecksEveryWireField|TestCapabilityCatalogMinimalProjectionPreservesProfiles|TestVerifyATLCapabilityCatalogUsesExactBoundedOfflineCommand|TestVerifyATLCapabilityCatalogRejectsSemanticDriftAndTimeout|TestRunHeadlessChecksSelectedATLCatalogBeforeCreatingOutput|TestRepositoryCodexSkillPackageMatchesReleasedSemantics|TestVerifyReleasedCodexSkillSemanticsRejectsPolicyDrift|TestVerifyCodexSkillPackageReconcilesExactTree|TestDecodeCodexSkillCatalogRejectsMalformedContracts|TestRepositoryJiraReferenceSummaryFixturesDriveProviderOracles|TestJiraReferenceMCPFixturesDriveProviderOracles|TestJiraSnapshotReconciliationFixturesDriveSelectedATLBinary|TestJiraArtifactGraphMCPFixturesDriveSelectedATLBinary|TestJiraArtifactGraphDevelopmentMCPFixturesDriveSelectedATLBinary|TestRepositoryStructureMCPV1FixturesDriveSelectedATLBinary|TestRepositoryStructureQualificationFixturesMatchSafeOracles|TestRepositoryJiraPaginatedSearchFixturesDriveProviderOracles|TestRepositoryJiraSearchZeroProgressFixturesDriveProviderOracles|TestConfluencePageMetadataFixturesDriveProviderOracles|TestConfluenceSectionBoundRecoveryFixturesDriveProviderOracles|TestConfluenceSectionVersionBoundFixturesDriveProviderOracles|TestRepositoryConfluenceAttachmentEvidenceFixturesDriveProviderOracles|TestConfluenceCommentRoutingFixturesDriveSelectedATLBinary)$$' -count=1
	go test ./internal/cli -run '^TestCLIErrorWireProductContract$$' -count=1
	go run ./scripts/agent-eval validate internal/cli/testdata/agent-eval/*.json benchmarks/agent-eval/*/scenario.v*.json >/dev/null
	go run ./scripts/agent-eval validate-run benchmarks/agent-eval/*/run.*.json >/dev/null
	go run ./scripts/agent-eval verify-atl-capabilities ./atl >/dev/null
	go run ./scripts/agent-eval verify-codex-skill-package plugins/atl >/dev/null

.PHONY: agent-eval-contract
agent-eval-contract: agent-eval-compat
	go test ./internal/agenteval ./scripts/agent-eval -count=1 -timeout=10m

.PHONY: agent-eval-race
agent-eval-race: agent-eval-compat
	go test -race ./internal/agenteval ./scripts/agent-eval -count=1 -timeout=10m

.PHONY: tidy
tidy:
	go mod tidy

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
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $$out ./cmd/atl || exit 1; \
		( cd dist && sha256sum atl-$$os-$$arch > atl-$$os-$$arch.sha256 ); \
	done
	@echo "$(VERSION)" > dist/VERSION

# Generate dist/manifest.json (version + per-binary sha256) from ./dist.
# Signing happens in CI (scripts/sign-manifest.go) with the release secret.
.PHONY: manifest
manifest:
	go run ./scripts/gen-manifest --dist dist --version "$(VERSION)" > dist/manifest.json
	@echo "wrote dist/manifest.json"

# Generate the Homebrew formula (dist/atl.rb) from ./dist: each platform's
# release-asset URL pinned to its sha256. Published as a release asset; the tap
# repository that serves it (`brew install <owner>/tap/atl`) is created and
# maintained by the project owner — copy dist/atl.rb into its Formula/ dir.
.PHONY: homebrew
homebrew:
	go run ./scripts/gen-homebrew-formula --dist dist --version "$(VERSION)" --repo "$(REPO)" > dist/atl.rb
	@echo "wrote dist/atl.rb"

# Generate an ed25519 signing keypair OUTSIDE CI. Prints the public key to embed
# in internal/selfupdate/pubkey.go and writes the private key to a gitignored
# file. NEVER commit the private key — store it as the ATL_RELEASE_PRIVATE_KEY
# GitHub Actions secret, then delete the local copy.
.PHONY: genkey
genkey:
	go run ./scripts/genkey
