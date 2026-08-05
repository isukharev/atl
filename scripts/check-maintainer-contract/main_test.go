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
		name        string
		path        string
		old         string
		replacement string
		runtime     string
		want        string
	}{
		{name: "runtime", runtime: "go1.26.4", want: "runtime.Version()"},
		{name: "base image", path: ".devcontainer/devcontainer.json", old: verifiedBaseImage, replacement: "mcr.microsoft.com/devcontainers/base:bookworm", want: "verified base image"},
		{name: "remote user", path: ".devcontainer/devcontainer.json", old: `"remoteUser": "vscode"`, replacement: `"remoteUser": "root"`, want: "remoteUser"},
		{name: "automatic toolchain", path: ".devcontainer/devcontainer.json", old: `"GOTOOLCHAIN": "local"`, replacement: `"GOTOOLCHAIN": "auto"`, want: "GOTOOLCHAIN"},
		{name: "feature patch", path: ".devcontainer/devcontainer.json", old: `"version": "1.26.5"`, replacement: `"version": "1.26.4"`, want: "Go feature version"},
		{name: "lock feature", path: ".devcontainer/devcontainer-lock.json", old: goFeatureID, replacement: "ghcr.io/devcontainers/features/missing:1", want: "lock is missing"},
		{name: "lock package version", path: ".devcontainer/devcontainer-lock.json", old: `"version": "` + goFeaturePackageVersion + `"`, replacement: `"version": "0.0.0"`, want: "reviewed"},
		{name: "lock integrity", path: ".devcontainer/devcontainer-lock.json", old: `"integrity": "` + goFeatureDigest + `"`, replacement: `"integrity": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"`, want: "reviewed package digest"},
		{name: "generated source hidden", path: ".gitattributes", old: generatedAttributesContract, replacement: generatedAttributesContract + "/skills-src/** linguist-generated=true\n", want: "exactly the two generated skill output trees"},
		{name: "make automatic repair", path: "Makefile", old: "GOTOOLCHAIN=local", replacement: "GOTOOLCHAIN=auto", want: "must start with GOTOOLCHAIN=local"},
		{name: "windows make target", path: "Makefile", old: "GOOS=windows", replacement: "GOOS=linux", want: "exact Windows source cross-compile target"},
		{name: "ci literal", path: ".github/workflows/ci.yml", old: "go-version-file: go.mod", replacement: "go-version: '1.26.5'", want: "exact required workflow block"},
		{name: "windows ci step", path: ".github/workflows/ci.yml", old: "run: make check-windows-compile", replacement: "run: echo skipped", want: "exact Windows source cross-compile workflow block"},
		{name: "windows ci condition", path: ".github/workflows/ci.yml", old: "if: matrix.os == 'ubuntu-latest'", replacement: "if: matrix.os == 'macos-latest'", want: "exact Windows source cross-compile workflow block"},
		{name: "windows ci allowed failure", path: ".github/workflows/ci.yml", old: "run: make check-windows-compile", replacement: "run: make check-windows-compile\n        continue-on-error: true", want: "exact Windows source cross-compile workflow block"},
		{name: "windows ci expression allowed failure", path: ".github/workflows/ci.yml", old: "run: make check-windows-compile", replacement: "run: make check-windows-compile\n        continue-on-error: ${{ true }}", want: "exact Windows source cross-compile workflow block"},
		{name: "windows ci duplicate condition", path: ".github/workflows/ci.yml", old: "run: make check-windows-compile", replacement: "run: make check-windows-compile\n        if: false", want: "exact Windows source cross-compile workflow block"},
		{name: "windows ci job allowed failure", path: ".github/workflows/ci.yml", old: "    runs-on: ${{ matrix.os }}", replacement: "    runs-on: ${{ matrix.os }}\n    continue-on-error: true", want: "job-level failure"},
		{name: "windows ci quoted job allowed failure", path: ".github/workflows/ci.yml", old: "    runs-on: ${{ matrix.os }}", replacement: "    runs-on: ${{ matrix.os }}\n    \"continue-on-error\": true", want: "job-level failure"},
		{name: "ci test make environment", path: ".github/workflows/ci.yml", old: "    runs-on: ${{ matrix.os }}", replacement: "    runs-on: ${{ matrix.os }}\n    env:\n      MAKEFLAGS: -i", want: "unexpected job-level key \"env\""},
		{name: "ci workflow make environment", path: ".github/workflows/ci.yml", old: "permissions:\n  contents: read", replacement: "env:\n  MAKEFLAGS: -i\npermissions:\n  contents: read", want: "unexpected top-level key \"env\""},
		{name: "windows ci excluded Ubuntu", path: ".github/workflows/ci.yml", old: "        os: [ubuntu-latest, macos-latest]", replacement: "        os: [ubuntu-latest, macos-latest]\n        exclude:\n          - os: ubuntu-latest", want: "exact required Ubuntu/macOS matrix"},
		{name: "windows ci skipped dependency", path: ".github/workflows/ci.yml", old: "    runs-on: ${{ matrix.os }}", replacement: "    runs-on: ${{ matrix.os }}\n    needs: optional", want: "potentially skipped job"},
		{name: "windows ci missing pull request trigger", path: ".github/workflows/ci.yml", old: "  pull_request:\n    branches: [main]", replacement: "  issues:\n    types: [opened]", want: "pull-request trigger contract"},
		{name: "coverage floor", path: "Makefile", old: `--minimum "84.0"`, replacement: `--minimum "0.0"`, want: "reviewed 84.0% floor"},
		{name: "coverage target-specific override", path: "Makefile", old: "check-core-race-coverage:\n", replacement: "check-core-race-coverage:\ncheck-core-race-coverage: COVERAGE_FLOOR=0.0\n", want: "reviewed 84.0% floor"},
		{name: "coverage dynamic target override", path: "Makefile", old: "check-core-race-coverage:\n", replacement: "check-core-race-coverage:\ncoverage_gate := check-core-race-coverage\n$(coverage_gate):\n\t@true\n", want: "hidden top-level directives"},
		{name: "make ignored failures", path: "Makefile", old: ".PHONY: check-core-race-coverage", replacement: ".IGNORE: check-core-race-coverage\n.PHONY: check-core-race-coverage", want: "failure propagation"},
		{name: "make global ignored failures", path: "Makefile", old: ".PHONY: check-core-race-coverage", replacement: "MAKEFLAGS += -i\n.PHONY: check-core-race-coverage", want: "must not override MAKEFLAGS"},
		{name: "make tabbed override", path: "Makefile", old: ".PHONY: check-core-race-coverage", replacement: "override\tMAKEFLAGS += -i\n.PHONY: check-core-race-coverage", want: "must not override MAKEFLAGS"},
		{name: "make computed ignored failures", path: "Makefile", old: ".PHONY: check-core-race-coverage", replacement: "control = MAKEFLAGS\noverride $(control) += -i\n.PHONY: check-core-race-coverage", want: "computed variable name"},
		{name: "make short computed ignored failures", path: "Makefile", old: ".PHONY: check-core-race-coverage", replacement: "A = MAKEFLAGS\noverride $A += -i\n.PHONY: check-core-race-coverage", want: "computed variable name"},
		{name: "make computed shell override", path: "Makefile", old: ".PHONY: check-core-race-coverage", replacement: "control = SHELL\noverride $(control) := /bin/true\n.PHONY: check-core-race-coverage", want: "computed variable name"},
		{name: "make target-specific computed override", path: "Makefile", old: ".PHONY: check-core-race-coverage", replacement: "control = MAKEFLAGS\ncheck-%: override $(control) += -i\n.PHONY: check-core-race-coverage", want: "computed variable name"},
		{name: "make conditional gate", path: "Makefile", old: ".PHONY: check-core-race-coverage", replacement: "ifeq (1,0)\n.PHONY: check-core-race-coverage", want: "conditionally disable reviewed build controls"},
		{name: "make conditional terminator", path: "Makefile", old: "\ncheck-core-race-coverage:\n", replacement: "\nendif\ncheck-core-race-coverage:\n", want: "conditionally disable reviewed build controls"},
		{name: "make continued override", path: "Makefile", old: ".PHONY: check-core-race-coverage", replacement: "MAKE\\\nFLAGS += -i\n.PHONY: check-core-race-coverage", want: "continue hidden top-level build controls"},
		{name: "make multiline ignored failures", path: "Makefile", old: ".PHONY: check-core-race-coverage", replacement: "define MAKEFLAGS\n-i\nendef\n.PHONY: check-core-race-coverage", want: "hidden multiline build controls"},
		{name: "make immediate ignored failures", path: "Makefile", old: ".PHONY: check-core-race-coverage", replacement: "MAKEFLAGS ::= -i\n.PHONY: check-core-race-coverage", want: "must not override MAKEFLAGS"},
		{name: "make pattern shell override", path: "Makefile", old: ".PHONY: check-core-race-coverage", replacement: "check-% agent-eval-%: SHELL=/bin/true\n.PHONY: check-core-race-coverage", want: "target-specific SHELL"},
		{name: "make one shell masking", path: "Makefile", old: ".PHONY: check-core-race-coverage", replacement: ".ONESHELL:\n.PHONY: check-core-race-coverage", want: "failure propagation"},
		{name: "make hidden include", path: "Makefile", old: ".PHONY: check-core-race-coverage", replacement: "-include bypass.mk\n.PHONY: check-core-race-coverage", want: "hidden build rules"},
		{name: "make tab-separated include", path: "Makefile", old: ".PHONY: check-core-race-coverage", replacement: "include\tbypass.mk\n.PHONY: check-core-race-coverage", want: "hidden build rules"},
		{name: "coverage command", path: "Makefile", old: "go test -race -covermode=atomic", replacement: "go test -covermode=count", want: "core race/coverage command"},
		{name: "coverage checker", path: "Makefile", old: "go run ./scripts/check-coverage --profile cover.out", replacement: "echo coverage", want: "core race/coverage command"},
		{name: "agent eval race timeout", path: "Makefile", old: "-timeout=20m", replacement: "-timeout=10m", want: "exact agent-evaluation race gate"},
		{name: "onboarding update opt out", path: "Makefile", old: "ATL_NO_UPDATE=1 go run ./scripts/check-onboarding-docs", replacement: "go run ./scripts/check-onboarding-docs", want: "onboarding binary assertion must set ATL_NO_UPDATE=1"},
		{name: "documentation catalog make gate", path: "Makefile", old: "go run ./scripts/check-docs-catalog -root .", replacement: "echo skipped", want: "exact documentation-catalog gate"},
		{name: "documentation freshness make gate", path: "Makefile", old: "go run ./scripts/check-docs-freshness -root .", replacement: "echo skipped", want: "exact documentation-freshness gate"},
		{name: "maintainability make gate", path: "Makefile", old: "go run ./scripts/check-maintainability", replacement: "echo skipped", want: "exact maintainability-ratchet gate"},
		{name: "repository skills make gate", path: "Makefile", old: "go run ./scripts/check-repository-skills -root .", replacement: "echo skipped", want: "exact repository-skills gate"},
		{name: "reference split make gate", path: "Makefile", old: "go run ./scripts/check-reference-split -root .", replacement: "echo skipped", want: "exact reference-split compatibility gate"},
		{name: "ci core gate", path: ".github/workflows/ci.yml", old: "run: make check-core-race-coverage", replacement: "run: make race", want: "exact required workflow block"},
		{name: "ci core gate quoted condition", path: ".github/workflows/ci.yml", old: "run: make check-core-race-coverage", replacement: "run: make check-core-race-coverage\n        'if': false", want: "exact required workflow block"},
		{name: "ci provenance update opt out", path: ".github/workflows/ci.yml", old: "ATL_NO_UPDATE=1 ./atl version", replacement: "./atl version", want: "exact required workflow block"},
		{name: "ci documentation catalog gate", path: ".github/workflows/ci.yml", old: "run: make check-docs-catalog", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "ci documentation freshness gate", path: ".github/workflows/ci.yml", old: "run: make check-docs-freshness", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "ci maintainability gate", path: ".github/workflows/ci.yml", old: "run: make check-maintainability", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "ci documentation freshness diff base", path: ".github/workflows/ci.yml", old: "ATL_DOCS_BASE: ${{ github.event.pull_request.base.sha }}", replacement: "ATL_DOCS_BASE: HEAD", want: "exact required workflow block"},
		{name: "ci documentation freshness diff head", path: ".github/workflows/ci.yml", old: "ATL_DOCS_HEAD: ${{ github.event.pull_request.head.sha }}", replacement: "ATL_DOCS_HEAD: HEAD", want: "exact required workflow block"},
		{name: "ci documentation freshness shallow checkout", path: ".github/workflows/ci.yml", old: "          fetch-depth: 0", replacement: "          fetch-depth: 1", want: "exact required workflow block"},
		{name: "ci repository skills gate", path: ".github/workflows/ci.yml", old: "run: make check-repository-skills", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "ci reference split gate", path: ".github/workflows/ci.yml", old: "run: make check-reference-split", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "ci provenance command only inert heredoc content", path: ".github/workflows/ci.yml", old: "          ATL_NO_UPDATE=1 ./atl version > \"$RUNNER_TEMP/atl-version.json\"", replacement: "          cat <<'EOF'\n          ATL_NO_UPDATE=1 ./atl version > \"$RUNNER_TEMP/atl-version.json\"\n          EOF", want: "exact required workflow block"},
		{name: "release tag trigger", path: ".github/workflows/release.yml", old: "tags: ['v*']", replacement: "branches: [main]", want: "exact v* tag trigger"},
		{name: "release workflow make environment", path: ".github/workflows/release.yml", old: "permissions:\n  contents: read", replacement: "env:\n  MAKEFLAGS: -i\npermissions:\n  contents: read", want: "unexpected top-level key \"env\""},
		{name: "release quoted duplicate publication job", path: ".github/workflows/release.yml", old: "jobs:\n", replacement: "jobs:\n  \"release\":\n    if: ${{ always() }}\n    runs-on: ubuntu-latest\n    steps: []\n", want: "defines \"release\" job more than once"},
		{name: "release unexpected publisher job", path: ".github/workflows/release.yml", old: "jobs:\n", replacement: "jobs:\n  publish-without-gates:\n    permissions:\n      contents: write\n    runs-on: ubuntu-latest\n    steps:\n      - run: gh release create v0.0.0\n", want: "unexpected job \"publish-without-gates\""},
		{name: "release runner", path: ".github/workflows/release.yml", old: "runs-on: ${{ matrix.os }}", replacement: "runs-on: ubuntu-latest", want: "exact Ubuntu/macOS matrix runners"},
		{name: "release matrix", path: ".github/workflows/release.yml", old: "os: [ubuntu-latest, macos-latest]", replacement: "os: [ubuntu-latest]", want: "exact Ubuntu/macOS matrix runners"},
		{name: "release matrix exclusion", path: ".github/workflows/release.yml", old: "os: [ubuntu-latest, macos-latest]", replacement: "os: [ubuntu-latest, macos-latest]\n        exclude:\n          - os: macos-latest", want: "exact Ubuntu/macOS matrix runners"},
		{name: "release core gate", path: ".github/workflows/release.yml", old: "run: make check-core-race-coverage", replacement: "run: make race", want: "exact required workflow block"},
		{name: "release maintainer gate", path: ".github/workflows/release.yml", old: "run: GOTOOLCHAIN=local go run ./scripts/check-maintainer-contract", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "release package gate", path: ".github/workflows/release.yml", old: "run: make check-package-boundary", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "release maintainability gate", path: ".github/workflows/release.yml", old: "run: make check-maintainability", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "release plugin gate", path: ".github/workflows/release.yml", old: "run: make check-plugins", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "release documentation catalog gate", path: ".github/workflows/release.yml", old: "run: make check-docs-catalog", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "release documentation freshness gate", path: ".github/workflows/release.yml", old: "run: make check-docs-freshness", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "release repository skills gate", path: ".github/workflows/release.yml", old: "run: make check-repository-skills", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "release reference split gate", path: ".github/workflows/release.yml", old: "run: make check-reference-split", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "release context7 gate", path: ".github/workflows/release.yml", old: "run: make check-context7-docs", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "release onboarding gate", path: ".github/workflows/release.yml", old: "run: make check-onboarding-docs", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "release vet gate", path: ".github/workflows/release.yml", old: "run: go vet ./...", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "release lint gate", path: ".github/workflows/release.yml", old: "uses: golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a", replacement: "run: echo skipped", want: "exact required workflow block"},
		{name: "release vulnerability gate", path: ".github/workflows/release.yml", old: "govulncheck ./...", replacement: "echo skipped", want: "exact required workflow block"},
		{name: "release agent eval race", path: ".github/workflows/release.yml", old: "run: make agent-eval-race", replacement: "run: make agent-eval-contract", want: "exact required workflow block"},
		{name: "release missing test prerequisite", path: ".github/workflows/release.yml", old: "needs: [test, quality, agent-eval]", replacement: "needs: [quality, agent-eval]", want: "must need \"test\""},
		{name: "release missing quality prerequisite", path: ".github/workflows/release.yml", old: "needs: [test, quality, agent-eval]", replacement: "needs: [test, agent-eval]", want: "must need \"quality\""},
		{name: "release missing agent eval prerequisite", path: ".github/workflows/release.yml", old: "needs: [test, quality, agent-eval]", replacement: "needs: [test, quality]", want: "must need \"agent-eval\""},
		{name: "release required step allowed failure", path: ".github/workflows/release.yml", old: "run: make check-package-boundary", replacement: "run: make check-package-boundary\n        continue-on-error: true", want: "exact required workflow block"},
		{name: "release quoted required step allowed failure", path: ".github/workflows/release.yml", old: "run: make check-package-boundary", replacement: "run: make check-package-boundary\n        \"continue-on-error\": true", want: "exact required workflow block"},
		{name: "release required command ignores failure", path: ".github/workflows/release.yml", old: "run: make check-package-boundary", replacement: "run: make check-package-boundary || true", want: "exact required workflow block"},
		{name: "release required command only inert heredoc content", path: ".github/workflows/release.yml", old: "run: make check-package-boundary", replacement: "run: |\n          cat <<'EOF'\n          run: make check-package-boundary\n          EOF", want: "exact required workflow block"},
		{name: "release quality job allowed failure", path: ".github/workflows/release.yml", old: "  quality:\n    runs-on: ubuntu-latest", replacement: "  quality:\n    continue-on-error: true\n    runs-on: ubuntu-latest", want: "quality job must be unconditional"},
		{name: "release quality quoted job allowed failure", path: ".github/workflows/release.yml", old: "  quality:\n    runs-on: ubuntu-latest", replacement: "  quality:\n    'continue-on-error': true\n    runs-on: ubuntu-latest", want: "quality job must be unconditional"},
		{name: "release quality runner drift", path: ".github/workflows/release.yml", old: "  quality:\n    runs-on: ubuntu-latest", replacement: "  quality:\n    runs-on: self-hosted", want: "must retain runs-on: ubuntu-latest"},
		{name: "release publication always runs", path: ".github/workflows/release.yml", old: "  release:\n    needs: [test, quality, agent-eval]", replacement: "  release:\n    if: ${{ always() }}\n    needs: [test, quality, agent-eval]", want: "publication job must be unconditional"},
		{name: "release publication quoted always runs", path: ".github/workflows/release.yml", old: "  release:\n    needs: [test, quality, agent-eval]", replacement: "  release:\n    \"if\": ${{ always() }}\n    needs: [test, quality, agent-eval]", want: "publication job must be unconditional"},
		{name: "release publication complex key", path: ".github/workflows/release.yml", old: "  release:\n    needs: [test, quality, agent-eval]", replacement: "  release:\n    ? if\n    : ${{ always() }}\n    needs: [test, quality, agent-eval]", want: "unrecognized job-level field"},
		{name: "release publication allowed failure", path: ".github/workflows/release.yml", old: "  release:\n    needs: [test, quality, agent-eval]", replacement: "  release:\n    continue-on-error: true\n    needs: [test, quality, agent-eval]", want: "publication job must be unconditional"},
		{name: "release publication quoted allowed failure", path: ".github/workflows/release.yml", old: "  release:\n    needs: [test, quality, agent-eval]", replacement: "  release:\n    'continue-on-error': true\n    needs: [test, quality, agent-eval]", want: "publication job must be unconditional"},
		{name: "release follow-up skips publication", path: ".github/workflows/release.yml", old: "    needs: [release]", replacement: "    needs: []", want: "must need \"release\""},
		{name: "codeql version file", path: ".github/workflows/codeql.yml", old: "go-version-file: go.mod", replacement: "version-file: go.mod", want: "must use go-version-file"},
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
		"go.mod":         "module example.test/project\n\ngo " + fixtureGoVersion + "\n",
		".gitattributes": generatedAttributesContract,
		".devcontainer/devcontainer.json": `{
  // OCI digest verified fixture.
  "image": "` + verifiedBaseImage + `",
  "features": {
    "` + goFeatureID + `": {"version": "` + fixtureGoVersion + `"},
    "ghcr.io/devcontainers/features/node:1": {"version": "lts"}
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
		".devcontainer/post-create.sh": `#!/usr/bin/env bash
go run ./scripts/check-maintainer-contract
`,
		"Makefile": `check-maintainer-contract:
	GOTOOLCHAIN=local go run ./scripts/check-maintainer-contract
` + windowsCompileMakeContract + coreCoverageMakeContract + packageBoundaryMakeContract + maintainabilityMakeContract + pluginsMakeContract + docsCatalogMakeContract + docsFreshnessMakeContract + repositorySkillsMakeContract + referenceSplitMakeContract + context7MakeContract + onboardingMakeContract + agentEvalRaceMakeContract,
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
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
      - uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16
        with:
          go-version-file: go.mod
          check-latest: true
      - name: Build
        run: make build
      - name: Verify stamped build provenance
        run: |
          ATL_NO_UPDATE=1 ./atl version > "$RUNNER_TEMP/atl-version.json"
          grep -F "\"commit\": \"$(git rev-parse HEAD)\"" "$RUNNER_TEMP/atl-version.json"
          grep -F '"build_state": "clean"' "$RUNNER_TEMP/atl-version.json"
      - name: Vet
        run: go vet ./...
      - name: Core race and coverage gate
        run: make check-core-race-coverage
      - name: Optional matrix-word output
        continue-on-error: true
        run: |
          echo 'include: optional diagnostic'
          echo 'exclude: optional diagnostic'
      - name: Windows source cross-compile
        if: matrix.os == 'ubuntu-latest'
        run: make check-windows-compile
      - name: Optional fixture step
        continue-on-error: true
        run: echo optional
  lint:
    if: github.event_name == 'pull_request' || github.event_name == 'workflow_dispatch'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with:
          fetch-depth: 0
      - uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16
        with:
          go-version-file: go.mod
          check-latest: true
      - name: Maintainer toolchain contract
        run: GOTOOLCHAIN=local go run ./scripts/check-maintainer-contract
      - name: Core/heavy package boundary
        run: make check-package-boundary
      - name: Maintainability ratchets
        run: make check-maintainability
      - name: Generated plugin trees are current
        run: make check-plugins
      - name: Documentation catalog
        run: make check-docs-catalog
      - name: Documentation freshness
        env:
          ATL_DOCS_BASE: ${{ github.event.pull_request.base.sha }}
          ATL_DOCS_HEAD: ${{ github.event.pull_request.head.sha }}
        run: make check-docs-freshness
      - name: Repository maintainer skills
        run: make check-repository-skills
      - name: Reference split compatibility
        run: make check-reference-split
      - name: Indexed documentation contract
        run: make check-context7-docs
      - name: Onboarding documentation rehearsal
        run: make check-onboarding-docs
      - name: golangci-lint
        uses: golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a
        with:
          version: v2.12.2
`,
		".github/workflows/codeql.yml": `steps:
  - uses: actions/setup-go@fixture
    with:
      go-version-file: go.mod
  - name: Analyze
    run: codeql
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
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
      - uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16
        with:
          go-version-file: go.mod
          check-latest: true
      - name: Core race and coverage gate
        run: make check-core-race-coverage
  quality:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
      - uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16
        with:
          go-version-file: go.mod
          check-latest: true
      - name: Maintainer toolchain contract
        run: GOTOOLCHAIN=local go run ./scripts/check-maintainer-contract
      - name: Core/heavy package boundary
        run: make check-package-boundary
      - name: Maintainability ratchets
        run: make check-maintainability
      - name: Generated plugin trees are current
        run: make check-plugins
      - name: Documentation catalog
        run: make check-docs-catalog
      - name: Documentation freshness
        run: make check-docs-freshness
      - name: Repository maintainer skills
        run: make check-repository-skills
      - name: Reference split compatibility
        run: make check-reference-split
      - name: Indexed documentation contract
        run: make check-context7-docs
      - name: Onboarding documentation rehearsal
        run: make check-onboarding-docs
      - name: Vet
        run: go vet ./...
      - name: golangci-lint
        uses: golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a
        with:
          version: v2.12.2
      - name: govulncheck
        run: |
          go install golang.org/x/vuln/cmd/govulncheck@v1.4.0
          govulncheck ./...
      -
        name: Optional notification
        continue-on-error: true
        run: echo optional
  agent-eval:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
      - uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16
        with:
          go-version-file: go.mod
          check-latest: true
      - name: Agent evaluation race gate
        run: make agent-eval-race
  release:
    needs: [test, quality, agent-eval]
    runs-on: ubuntu-latest
    environment: release
    permissions:
      contents: write
    env:
      FIXTURE: true
    steps:
      - name: Release
        run: go build ./...
  refresh-context7:
    name: Refresh Context7 stable docs (non-blocking)
    needs: [release]
    runs-on: ubuntu-latest
    continue-on-error: true
    environment: context7
    permissions:
      contents: write
    concurrency:
      group: context7-stable
      cancel-in-progress: false
    env:
      HAS_CONTEXT7_KEY: true
    steps:
      - name: Refresh
        run: echo refresh
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
