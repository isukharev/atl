# atl — build, test, and release helpers.
#
# Common targets:
#   make build            build ./cmd/atl into ./atl (version-stamped)
#   make test             run unit tests
#   make lint             run golangci-lint (if installed)
#   make vet              go vet
#   make live-smoke       run opt-in live CLI smoke checks
#   make dist             cross-compile release binaries into ./dist
#   make manifest         generate dist/manifest.json from ./dist binaries
#   make homebrew         generate dist/atl.rb (Homebrew formula) from ./dist
#   make genkey           generate an ed25519 release signing key (off-CI)

MODULE   := github.com/isukharev/atl
REPO     := isukharev/atl
VERSION  := $(shell cat VERSION 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X $(MODULE)/internal/version.Version=$(VERSION)
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
	go test ./...

.PHONY: race
race:
	go test -race ./...

# Live integration tests against a REAL Confluence/Jira Data Center. Opt-in only —
# never part of `make test` and never run in CI. Reads local-only ./.env.integration
# (copy .env.integration.example and fill in your DC URL, PATs, and throwaway test
# objects); that file is gitignored so the real URL/tokens never reach the repo.
.PHONY: integration
integration:
	@test -f .env.integration || { echo "missing .env.integration — run: cp .env.integration.example .env.integration && edit it"; exit 1; }
	set -a; . ./.env.integration; set +a; \
	ATL_INTEGRATION=1 go test ./... -run Integration -count=1 -v

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
