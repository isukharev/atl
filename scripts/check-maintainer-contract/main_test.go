package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fixtureGoVersion = "1.26.5"

func TestRepositoryMaintainerContract(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	if _, err := validateRepository(root, runtime.Version()); err != nil {
		t.Fatal(err)
	}
}

func TestValidMaintainerContract(t *testing.T) {
	root := writeFixture(t)
	var output bytes.Buffer
	if err := run(root, "go"+fixtureGoVersion, &output); err != nil {
		t.Fatal(err)
	}
	var got report
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, output.String())
	}
	want := report{Status: "ok", GoVersion: fixtureGoVersion, RuntimeVersion: "go" + fixtureGoVersion}
	if got != want {
		t.Fatalf("report=%+v want=%+v", got, want)
	}
}

func TestMaintainerContractRejectsDrift(t *testing.T) {
	tests := []struct {
		name, path, old, replacement, runtime, want string
	}{
		{name: "runtime", runtime: "go1.26.4", want: "runtime.Version()"},
		{name: "evaluator patch drift", path: "internal/agenteval/go.mod", old: "go " + fixtureGoVersion, replacement: "go 1.26.4", want: "evaluator module go directive"},
		{name: "root evaluator environment", path: "Makefile", old: "GOWORK=off", replacement: "GOWORK=on", want: "workspace-independent root and evaluator environments"},
		{name: "nested evaluator environment", path: "internal/agenteval/Makefile", old: "GOWORK=off", replacement: "GOWORK=on", want: "workspace-independent environment"},
		{name: "nested full gate", path: "internal/agenteval/Makefile", old: "full: tidy-check build race lint vet vuln contract windows product-boundary", replacement: "full: tidy-check build race lint vet vuln contract windows", want: "exact \"full\" gate"},
		{name: "nested contract unit dependency", path: "internal/agenteval/Makefile", old: "contract: compat unit\n", replacement: "contract: compat\n", want: "exact \"contract\" gate"},
		{name: "nested lint pin", path: "internal/agenteval/Makefile", old: "golangci-lint@v2.12.2 run", replacement: "golangci-lint@latest run", want: "exact \"lint\" gate"},
		{name: "root facade bypass", path: "Makefile", old: "\t$(AGENT_EVAL_MAKE) race", replacement: "\tgo test -race ./internal/agenteval", want: "nested-module facades"},
		{name: "root module boundary", path: "Makefile", old: "go run ./scripts/check-module-boundary -root .", replacement: "echo skipped", want: "exact two-module boundary gate"},
		{name: "nested ignored failures", path: "internal/agenteval/Makefile", old: ".PHONY: build", replacement: ".IGNORE: build\n.PHONY: build", want: "failure propagation"},
		{name: "ci evaluator job condition", path: ".github/workflows/ci.yml", old: "  agent-eval:\n    runs-on", replacement: "  agent-eval:\n    if: false\n    runs-on", want: "ci agent-eval job must be unconditional"},
		{name: "ci evaluator fail-open fallback", path: ".github/workflows/ci.yml", old: "          mode=full", replacement: "          mode=compat", want: "exact required workflow block"},
		{name: "ci evaluator internal tree coverage", path: ".github/workflows/ci.yml", old: ".claude-plugin .mcp.json cmd internal scripts", replacement: ".claude-plugin .mcp.json cmd scripts", want: "exact required workflow block"},
		{name: "ci evaluator allowed failure", path: ".github/workflows/ci.yml", old: "        run: make agent-eval-full", replacement: "        run: make agent-eval-full\n        continue-on-error: true", want: "exact required workflow block"},
		{name: "release evaluator full", path: ".github/workflows/release.yml", old: "run: make agent-eval-full", replacement: "run: make agent-eval-compat", want: "exact required workflow block"},
		{name: "CodeQL evaluator build", path: ".github/workflows/codeql.yml", old: "run: make agent-eval-build", replacement: "run: echo skipped", want: "exact workflow block"},
		{name: "nested Dependabot module", path: ".github/dependabot.yml", old: "directory: \"/internal/agenteval\"", replacement: "directory: \"/internal/evaluator\"", want: "exactly one reviewed gomod entry"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeFixture(t)
			if test.path != "" {
				replaceFixture(t, filepath.Join(root, filepath.FromSlash(test.path)), test.old, test.replacement)
			}
			runtimeVersion := test.runtime
			if runtimeVersion == "" {
				runtimeVersion = "go" + fixtureGoVersion
			}
			_, err := validateRepository(root, runtimeVersion)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func TestMakeExecutionControlsRejectBypasses(t *testing.T) {
	for name, makefile := range map[string]string{
		"global ignored failure": "MAKEFLAGS += -i\n",
		"computed shell":         "name = SHELL\noverride $(name) := /bin/true\n",
		"conditional":            "ifeq (1,0)\nendif\n",
		"hidden include":         "-include bypass.mk\n",
		"one shell":              ".ONESHELL:\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateMakeExecutionControls([]byte(makefile)); err == nil {
				t.Fatal("make control bypass passed")
			}
		})
	}
}

func TestSetupGoVersionFileMustBeInsideWith(t *testing.T) {
	for name, workflow := range map[string]string{
		"under env": `steps:
  - uses: actions/setup-go@fixture
    env:
      go-version-file: go.mod
`,
		"unrelated action substring": `steps:
  - uses: example.test/actions/setup-go@fixture
    with:
      go-version-file: go.mod
`,
		"after with sibling": `steps:
  - uses: actions/setup-go@fixture
    with:
      check-latest: true
    env:
      go-version-file: go.mod
`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateSetupGoWorkflow([]byte(workflow)); err == nil {
				t.Fatal("structurally invalid setup-go contract passed")
			}
		})
	}
}

func TestReleaseMatrixIgnoresOptionalStepBlockScalar(t *testing.T) {
	job := []byte(`  test:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - name: Optional diagnostics
        continue-on-error: true
        run: |
          echo 'include: harmless text'
          echo 'exclude: harmless text'
`)
	if err := validateReleaseMatrix(job); err != nil {
		t.Fatalf("optional step block-scalar content changed matrix validation: %v", err)
	}
}

func TestGoDirectiveRequiresExactPatch(t *testing.T) {
	for name, contents := range map[string]string{
		"minor only": "module example.test/project\n\ngo 1.26\n",
		"duplicate":  "module example.test/project\n\ngo 1.26.5\ngo 1.26.5\n",
		"missing":    "module example.test/project\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "go.mod")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readGoVersion(path); err == nil {
				t.Fatal("invalid go directive passed")
			}
		})
	}
}

func TestJSONCommentsPreserveStringContent(t *testing.T) {
	input := []byte("{\n// line\n\"url\": \"https://example.test/a//b\",\n/* block\ncomment */\n\"value\": \"/* literal */\"\n}")
	var got map[string]string
	if err := decodeJSONC(input, &got); err != nil {
		t.Fatal(err)
	}
	if got["url"] != "https://example.test/a//b" || got["value"] != "/* literal */" {
		t.Fatalf("decoded=%v", got)
	}
	if _, err := stripJSONComments([]byte("{/* unterminated")); err == nil {
		t.Fatal("unterminated block comment passed")
	}
}

func writeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                    "module example.test/project\n\ngo " + fixtureGoVersion + "\n",
		"internal/agenteval/go.mod": "module example.test/project/internal/agenteval\n\ngo " + fixtureGoVersion + "\n",
		".gitattributes":            generatedAttributesContract,
		".devcontainer/devcontainer.json": `{
  "image": "` + verifiedBaseImage + `",
  "features": {
    "` + goFeatureID + `": {"version": "` + fixtureGoVersion + `"}
  },
  "containerEnv": {"GOTOOLCHAIN": "local"},
  "remoteUser": "vscode"
}`,
		".devcontainer/devcontainer-lock.json": `{
  "features": {
    "` + goFeatureID + `": {
      "version": "` + goFeaturePackageVersion + `",
      "resolved": "ghcr.io/devcontainers/features/go@` + goFeatureDigest + `",
      "integrity": "` + goFeatureDigest + `"
    }
  }
}`,
		".devcontainer/post-create.sh": "#!/usr/bin/env bash\ngo run ./scripts/check-maintainer-contract\n",
		"Makefile": rootGoEnvironmentMakeContract +
			"check-maintainer-contract:\n\t$(GO_LOCAL_ENV) go run ./scripts/check-maintainer-contract\n" +
			windowsCompileMakeContract + coreCoverageMakeContract + moduleBoundaryMakeContract + packageBoundaryMakeContract +
			maintainabilityMakeContract + pluginsMakeContract + docsCatalogMakeContract + docsFreshnessMakeContract +
			repositorySkillsMakeContract + referenceSplitMakeContract + context7MakeContract + onboardingMakeContract +
			agentEvalFacadeMakeContract,
		"internal/agenteval/Makefile": `GO_ENV := env -u GOROOT GOTOOLCHAIN=auto GOWORK=off
REPOSITORY_ROOT ?= $(abspath ../..)
ATL_BINARY ?= $(REPOSITORY_ROOT)/atl
COMPAT_TESTS_WIRES := fixture
COMPAT_TESTS_MIRROR := fixture
COMPAT_TESTS_WRITES := fixture
COMPAT_TESTS_MCP := fixture

.PHONY: build
build:
	$(GO_ENV) go build ./...

.PHONY: unit
unit:
	$(GO_ENV) go test ./... -count=1 -timeout=10m

.PHONY: race
race:
	$(GO_ENV) go test -race ./... -count=1 -timeout=30m

.PHONY: lint
lint:
	$(GO_ENV) go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run

.PHONY: vet
vet:
	$(GO_ENV) go vet ./...

.PHONY: vuln
vuln:
	$(GO_ENV) go run golang.org/x/vuln/cmd/govulncheck@v1.4.0 ./...

.PHONY: tidy-check
tidy-check:
	$(GO_ENV) go mod tidy -diff

.PHONY: windows
windows:
	$(GO_ENV) GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...

.PHONY: product-atl
product-atl:
	$(MAKE) -C $(REPOSITORY_ROOT) build

.PHONY: compat
compat: product-atl
	@test -x "$(ATL_BINARY)"
	$(GO_ENV) go test . -run '$(COMPAT_TESTS_WIRES)' -count=1
	$(GO_ENV) go test . -run '$(COMPAT_TESTS_MIRROR)' -count=1
	$(GO_ENV) go test . -run '$(COMPAT_TESTS_WRITES)' -count=1
	$(GO_ENV) go test . -run '$(COMPAT_TESTS_MCP)' -count=1
	$(GO_ENV) go run ./cmd/agent-eval validate fixture >/dev/null
	$(GO_ENV) go run ./cmd/agent-eval validate-run fixture >/dev/null
	$(GO_ENV) go run ./cmd/agent-eval verify-atl-capabilities $(ATL_BINARY) >/dev/null
	$(GO_ENV) go run ./cmd/agent-eval verify-codex-skill-package $(REPOSITORY_ROOT)/plugins/atl >/dev/null

.PHONY: contract
contract: compat unit

.PHONY: product-boundary
product-boundary:
	$(MAKE) -C $(REPOSITORY_ROOT) check-package-boundary

.PHONY: full
full: tidy-check build race lint vet vuln contract windows product-boundary
`,
		".github/workflows/ci.yml": `name: ci
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
  workflow_dispatch:
permissions:
  contents: read
concurrency:
  group: fixture
jobs:
  test:
    if: github.event_name == 'pull_request' || github.event_name == 'workflow_dispatch'
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
` + checkoutStepContract + "\n" + setupGoStepContract + "\n" + buildStepContract + "\n" + ciProvenanceStepContract + "\n" + vetStepContract + "\n" + coreGateStepContract + "\n" + windowsCompileStepContract + `
  agent-eval:
    runs-on: ubuntu-latest
    steps:
` + agentEvalCheckoutStepContract + "\n" + setupGoStepContract + "\n" + agentEvalImpactStepContract + "\n" + agentEvalCompatStepContract + "\n" + agentEvalFullStepContract + `
  lint:
    if: github.event_name == 'pull_request' || github.event_name == 'workflow_dispatch'
    runs-on: ubuntu-latest
    steps:
` + lintCheckoutStepContract + "\n" + setupGoStepContract + "\n" + maintainerStepContract + "\n" + packageBoundaryStepContract + "\n" + maintainabilityStepContract + "\n" + pluginsStepContract + "\n" + docsCatalogStepContract + "\n" + docsFreshnessStepContract + "\n" + repositorySkillsStepContract + "\n" + referenceSplitStepContract + "\n" + context7StepContract + "\n" + onboardingStepContract + "\n" + lintStepContract + `
  govulncheck:
    runs-on: ubuntu-latest
    steps:
      - run: true
  smoke:
    runs-on: ubuntu-latest
    steps:
      - run: true
`,
		".github/workflows/release.yml": `name: release
on:
  push:
    tags: ['v*']
permissions:
  contents: read
concurrency:
  group: fixture
jobs:
  test:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
` + checkoutStepContract + "\n" + setupGoStepContract + "\n" + coreGateStepContract + `
  quality:
    runs-on: ubuntu-latest
    steps:
` + checkoutStepContract + "\n" + setupGoStepContract + "\n" + maintainerStepContract + "\n" + packageBoundaryStepContract + "\n" + maintainabilityStepContract + "\n" + pluginsStepContract + "\n" + docsCatalogStepContract + "\n" + releaseDocsFreshnessStepContract + "\n" + repositorySkillsStepContract + "\n" + referenceSplitStepContract + "\n" + context7StepContract + "\n" + onboardingStepContract + "\n" + vetStepContract + "\n" + lintStepContract + "\n" + govulncheckStepContract + `
  agent-eval:
    runs-on: ubuntu-latest
    steps:
` + checkoutStepContract + "\n" + setupGoStepContract + "\n" + agentEvalReleaseFullStepContract + `
  release:
    needs: [test, quality, agent-eval]
    runs-on: ubuntu-latest
    environment: release
    permissions:
      contents: write
    env:
      FIXTURE: true
    steps:
      - run: true
  refresh-context7:
    name: Refresh Context7 stable docs (non-blocking)
    needs: [release]
    runs-on: ubuntu-latest
    continue-on-error: true
    environment: context7
    permissions:
      contents: write
    concurrency:
      group: fixture
    env:
      FIXTURE: true
    steps:
      - run: true
`,
		".github/workflows/codeql.yml": `name: codeql
on:
  workflow_dispatch:
jobs:
  analyze:
    runs-on: ubuntu-latest
    permissions:
      contents: read
    steps:
      - uses: actions/setup-go@fixture
        with:
          go-version-file: go.mod
` + codeQLProductBuildStepContract + "\n" + codeQLEvaluatorBuildStepContract + "\n",
		".github/dependabot.yml": `version: 2
updates:
  - package-ecosystem: gomod
    directory: "/internal/agenteval"
    schedule:
      interval: weekly
      day: monday
    groups:
      minor-and-patch:
        update-types:
          - minor
          - patch
    open-pull-requests-limit: 5
    labels:
      - dependencies
      - go
  - package-ecosystem: gomod
    directory: "/"
    schedule:
      interval: weekly
      day: monday
    groups:
      minor-and-patch:
        update-types:
          - minor
          - patch
    open-pull-requests-limit: 5
    labels:
      - dependencies
      - go
`,
	}
	for name, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func replaceFixture(t *testing.T, path, old, replacement string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(contents), old, replacement, 1)
	if updated == string(contents) {
		t.Fatalf("fixture %s does not contain %q", path, old)
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
}
