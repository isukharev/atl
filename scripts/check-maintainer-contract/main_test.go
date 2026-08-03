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
		{name: "make automatic repair", path: "Makefile", old: "GOTOOLCHAIN=local", replacement: "GOTOOLCHAIN=auto", want: "must start with GOTOOLCHAIN=local"},
		{name: "windows make target", path: "Makefile", old: "GOOS=windows", replacement: "GOOS=linux", want: "exact Windows source cross-compile target"},
		{name: "ci literal", path: ".github/workflows/ci.yml", old: "go-version-file: go.mod", replacement: "go-version: '1.26.5'", want: "must not use a literal"},
		{name: "windows ci step", path: ".github/workflows/ci.yml", old: "run: make check-windows-compile", replacement: "run: echo skipped", want: "Ubuntu test job must run"},
		{name: "windows ci condition", path: ".github/workflows/ci.yml", old: "if: matrix.os == 'ubuntu-latest'", replacement: "if: matrix.os == 'macos-latest'", want: "Ubuntu test job must run"},
		{name: "windows ci allowed failure", path: ".github/workflows/ci.yml", old: "run: make check-windows-compile", replacement: "run: make check-windows-compile\n        continue-on-error: true", want: "cross-compile step must not allow failure"},
		{name: "windows ci expression allowed failure", path: ".github/workflows/ci.yml", old: "run: make check-windows-compile", replacement: "run: make check-windows-compile\n        continue-on-error: ${{ true }}", want: "cross-compile step must not allow failure"},
		{name: "windows ci duplicate condition", path: ".github/workflows/ci.yml", old: "run: make check-windows-compile", replacement: "run: make check-windows-compile\n        if: false", want: "one exact condition and command"},
		{name: "windows ci job allowed failure", path: ".github/workflows/ci.yml", old: "    runs-on: ${{ matrix.os }}", replacement: "    runs-on: ${{ matrix.os }}\n    continue-on-error: true", want: "job-level failure"},
		{name: "windows ci excluded Ubuntu", path: ".github/workflows/ci.yml", old: "        os: [ubuntu-latest, macos-latest]", replacement: "        os: [ubuntu-latest, macos-latest]\n        exclude:\n          - os: ubuntu-latest", want: "exact required Ubuntu/macOS matrix"},
		{name: "windows ci skipped dependency", path: ".github/workflows/ci.yml", old: "    runs-on: ${{ matrix.os }}", replacement: "    runs-on: ${{ matrix.os }}\n    needs: optional", want: "potentially skipped job"},
		{name: "windows ci missing pull request trigger", path: ".github/workflows/ci.yml", old: "  pull_request:\n    branches: [main]", replacement: "  issues:\n    types: [opened]", want: "pull-request trigger contract"},
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
		"go.mod": "module example.test/project\n\ngo " + fixtureGoVersion + "\n",
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
` + windowsCompileMakeContract,
		".github/workflows/ci.yml": `on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
  workflow_dispatch:

steps:
  - uses: actions/setup-go@fixture
    with:
      go-version-file: go.mod
  - name: Maintainer contract
    run: make check-maintainer-contract
  - name: Build
    run: go build ./...
jobs:
  test:
    if: github.event_name == 'pull_request' || github.event_name == 'workflow_dispatch'
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - name: Windows source cross-compile
        if: matrix.os == 'ubuntu-latest'
        run: make check-windows-compile
      - name: Optional fixture step
        continue-on-error: true
        run: echo optional
`,
		".github/workflows/codeql.yml": `steps:
  - uses: actions/setup-go@fixture
    with:
      go-version-file: go.mod
  - name: Analyze
    run: codeql
`,
		".github/workflows/release.yml": `steps:
  - uses: actions/setup-go@fixture
    with:
      go-version-file: go.mod
  - name: Release
    run: go build ./...
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
