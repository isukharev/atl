package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fixtureGoVersion = "1.26.5"

const graphifyConstraintsFixture = `# Resolved with uv 0.12.2 for graphifyy 0.9.34 on Python 3.11.
networkx==3.6.1
numpy==2.4.6
rapidfuzz==3.14.5
tree-sitter==0.25.2
tree-sitter-bash==0.25.1
tree-sitter-c==0.24.2
tree-sitter-c-sharp==0.23.5
tree-sitter-cpp==0.23.4
tree-sitter-elixir==0.3.5
tree-sitter-fortran==0.6.0
tree-sitter-go==0.25.0
tree-sitter-groovy==0.1.2
tree-sitter-java==0.23.5
tree-sitter-javascript==0.25.0
tree-sitter-json==0.24.8
tree-sitter-julia==0.23.1
tree-sitter-kotlin==1.1.0
tree-sitter-lua==0.5.0
tree-sitter-objc==3.0.2
tree-sitter-php==0.24.1
tree-sitter-powershell==0.26.4
tree-sitter-python==0.25.0
tree-sitter-ruby==0.23.1
tree-sitter-rust==0.24.2
tree-sitter-scala==0.26.0
tree-sitter-swift==0.7.3
tree-sitter-typescript==0.23.2
tree-sitter-verilog==1.0.3
tree-sitter-zig==1.1.2
`

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

func TestMaintainerContractAcceptsCompatibilityTestFromSubpackage(t *testing.T) {
	root := writeFixture(t)
	subpackageTest := filepath.Join(root, "internal", "agenteval", "cmd", "agent-eval", "fixture_test.go")
	if err := os.MkdirAll(filepath.Dir(subpackageTest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subpackageTest, []byte("package main\n\nimport \"testing\"\n\nfunc TestSubpackageFixture(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	makefile := filepath.Join(root, "internal", "agenteval", "Makefile")
	replaceFixture(t, makefile, "TestFixtureWires", "TestSubpackageFixture")
	if _, err := validateRepository(root, "go"+fixtureGoVersion); err != nil {
		t.Fatalf("subpackage-only compatibility test was not discovered: %v", err)
	}
}

func TestMaintainerContractRejectsDuplicateRecursiveCompatibilityTest(t *testing.T) {
	root := writeFixture(t)
	duplicate := filepath.Join(root, "internal", "agenteval", "core", "duplicate_test.go")
	if err := os.MkdirAll(filepath.Dir(duplicate), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(duplicate, []byte("package core\n\nimport \"testing\"\n\nfunc TestFixtureWires(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateRepository(root, "go"+fixtureGoVersion); err == nil || !strings.Contains(err.Error(), "2 test definitions, want 1") {
		t.Fatalf("duplicate recursive compatibility test error=%v", err)
	}
}

func TestMaintainerContractRejectsSymlinkedRecursiveCompatibilityTest(t *testing.T) {
	root := writeFixture(t)
	directory := filepath.Join(root, "internal", "agenteval", "core")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../fixture_test.go", filepath.Join(directory, "linked_test.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := validateRepository(root, "go"+fixtureGoVersion); err == nil || !strings.Contains(err.Error(), "symbolic links are not allowed") {
		t.Fatalf("symlinked recursive compatibility test error=%v", err)
	}
}

func TestMaintainerContractRejectsCompatibilitySelectorThatIsNotRunnable(t *testing.T) {
	tests := []struct {
		name, source string
	}{
		{name: "lowercase suffix", source: "package agenteval\n\nimport \"testing\"\n\nfunc Testfixture(t *testing.T) {}\n"},
		{name: "wrong signature", source: "package agenteval\n\nfunc TestFixture() {}\n"},
		{name: "inactive build constraint", source: "//go:build ignore\n\npackage agenteval\n\nimport \"testing\"\n\nfunc TestFixture(t *testing.T) {}\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeFixture(t)
			if err := os.WriteFile(filepath.Join(root, "internal", "agenteval", "fixture_test.go"), []byte(test.source), 0o600); err != nil {
				t.Fatal(err)
			}
			if test.name == "lowercase suffix" {
				replaceFixture(t, filepath.Join(root, "internal", "agenteval", "Makefile"), "TestFixture", "Testfixture")
			}
			if _, err := validateRepository(root, "go"+fixtureGoVersion); err == nil || !strings.Contains(err.Error(), "0 test definitions") {
				t.Fatalf("non-runnable compatibility test error=%v", err)
			}
		})
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
		{name: "nested contract repeats compatibility tests", path: "internal/agenteval/Makefile", old: "contract: compat-oracles unit\n", replacement: "contract: compat unit\n", want: "exact \"contract\" gate"},
		{name: "nested compatibility test omission", path: "internal/agenteval/Makefile", old: "COMPAT_TEST_COUNT := 4", replacement: "COMPAT_TEST_COUNT := 5", want: "selects 4 compatibility tests, want 5"},
		{name: "nested wires package recursion", path: "internal/agenteval/Makefile", old: "go test ./... -run '$(COMPAT_TESTS_WIRES)'", replacement: "go test . -run '$(COMPAT_TESTS_WIRES)'", want: "compatibility and deterministic contract commands"},
		{name: "nested mirror package recursion", path: "internal/agenteval/Makefile", old: "go test ./... -run '$(COMPAT_TESTS_MIRROR)'", replacement: "go test . -run '$(COMPAT_TESTS_MIRROR)'", want: "compatibility and deterministic contract commands"},
		{name: "nested writes package recursion", path: "internal/agenteval/Makefile", old: "go test ./... -run '$(COMPAT_TESTS_WRITES)'", replacement: "go test . -run '$(COMPAT_TESTS_WRITES)'", want: "compatibility and deterministic contract commands"},
		{name: "nested MCP package recursion", path: "internal/agenteval/Makefile", old: "go test ./... -run '$(COMPAT_TESTS_MCP)'", replacement: "go test . -run '$(COMPAT_TESTS_MCP)'", want: "compatibility and deterministic contract commands"},
		{name: "nested lint pin", path: "internal/agenteval/Makefile", old: "golangci-lint@v2.12.2 run", replacement: "golangci-lint@latest run", want: "exact \"lint\" gate"},
		{name: "root facade bypass", path: "Makefile", old: "\t$(AGENT_EVAL_MAKE) race", replacement: "\tgo test -race ./internal/agenteval", want: "nested-module facades"},
		{name: "root module boundary", path: "Makefile", old: "go run ./scripts/check-module-boundary -root .", replacement: "echo skipped", want: "exact two-module boundary gate"},
		{name: "nested ignored failures", path: "internal/agenteval/Makefile", old: ".PHONY: build", replacement: ".IGNORE: build\n.PHONY: build", want: "failure propagation"},
		{name: "capability catalog generation fail open", path: "internal/agenteval/Makefile", old: "@set -eu;", replacement: "@set +e;", want: "compatibility and deterministic contract commands"},
		{name: "ci evaluator job condition", path: ".github/workflows/ci.yml", old: "  agent-eval:\n    runs-on", replacement: "  agent-eval:\n    if: false\n    runs-on", want: "ci agent-eval job must be unconditional"},
		{name: "ci evaluator fail-open fallback", path: ".github/workflows/ci.yml", old: "          mode=full", replacement: "          mode=compat", want: "exact required workflow block"},
		{name: "ci evaluator internal tree coverage", path: ".github/workflows/ci.yml", old: ".claude-plugin .mcp.json cmd internal scripts", replacement: ".claude-plugin .mcp.json cmd scripts", want: "exact required workflow block"},
		{name: "ci evaluator allowed failure", path: ".github/workflows/ci.yml", old: "        run: make agent-eval-full", replacement: "        run: make agent-eval-full\n        continue-on-error: true", want: "exact required workflow block"},
		{name: "ci extension runtime selector", path: ".github/workflows/ci.yml", old: "TestExtensionProtocolV1StateMachineIsClosed", replacement: "TestExtensionProtocolV1StateMachine", want: "exact required workflow block"},
		{name: "ci Windows extension runner", path: ".github/workflows/ci.yml", old: "  agent-eval-extension-windows:\n    if: github.event_name == 'pull_request' || github.event_name == 'workflow_dispatch'\n    runs-on: windows-latest", replacement: "  agent-eval-extension-windows:\n    if: github.event_name == 'pull_request' || github.event_name == 'workflow_dispatch'\n    runs-on: ubuntu-latest", want: "ci agent-eval-extension-windows job must retain runs-on: windows-latest"},
		{name: "ci Windows extension allowed failure", path: ".github/workflows/ci.yml", old: "        shell: pwsh\n        run: |", replacement: "        shell: pwsh\n        continue-on-error: true\n        run: |", want: "exact required workflow block"},
		{name: "release evaluator full", path: ".github/workflows/release.yml", old: "run: make agent-eval-full", replacement: "run: make agent-eval-compat", want: "exact required workflow block"},
		{name: "CodeQL evaluator build", path: ".github/workflows/codeql.yml", old: "run: make agent-eval-build", replacement: "run: echo skipped", want: "exact workflow block"},
		{name: "nested Dependabot module", path: ".github/dependabot.yml", old: "directory: \"/internal/agenteval\"", replacement: "directory: \"/internal/evaluator\"", want: "exactly one reviewed gomod entry"},
		{name: "Graphify post-create hook", path: ".devcontainer/post-create.sh", old: `bash "${here}/install-graphify.sh"`, replacement: "echo skipped", want: "install Graphify exactly once"},
		{name: "Claude post-create hook", path: ".devcontainer/post-create.sh", old: `bash "${here}/install-claude-code.sh"`, replacement: "echo skipped", want: "install Claude Code exactly once"},
		{name: "Graphify Python runtime", path: ".devcontainer/post-create.sh", old: "gnupg python3 ripgrep", replacement: "gnupg ripgrep", want: "system packages required by Claude Code and Graphify"},
		{name: "Claude key verification dependency", path: ".devcontainer/post-create.sh", old: "gnupg python3 ripgrep", replacement: "python3 ripgrep", want: "system packages required by Claude Code and Graphify"},
		{name: "Graphify version pin", path: ".devcontainer/install-graphify.sh", old: `readonly GRAPHIFY_VERSION="` + graphifyVersion + `"`, replacement: `readonly GRAPHIFY_VERSION="latest"`, want: "Graphify installer"},
		{name: "Graphify dependency pin", path: ".devcontainer/graphify-constraints.txt", old: "networkx==3.6.1", replacement: "networkx>=3.6.1", want: "reviewed dependency set"},
		{name: "Graphify extraction remains explicit", path: ".devcontainer/install-graphify.sh", old: "set -euo pipefail", replacement: "set -euo pipefail\ngraphify extract . --code-only", want: "must not run"},
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

func TestClaudeInstallerUsesSignedAPTRepository(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Claude Code devcontainer installer is Linux-only")
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	installer := filepath.Join(filepath.Dir(current), "..", "..", ".devcontainer", "install-claude-code.sh")

	for _, test := range []struct {
		name        string
		channel     string
		fingerprint string
		extraKey    string
		wantRepo    string
		wantError   bool
	}{
		{
			name:        "latest channel",
			channel:     "latest",
			fingerprint: "31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE",
			wantRepo:    "deb [signed-by=/etc/apt/keyrings/claude-code.asc] https://downloads.claude.ai/claude-code/apt/latest latest main\n",
		},
		{
			name:        "stable channel",
			channel:     "stable",
			fingerprint: "31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE",
			wantRepo:    "deb [signed-by=/etc/apt/keyrings/claude-code.asc] https://downloads.claude.ai/claude-code/apt/stable stable main\n",
		},
		{name: "untrusted key", channel: "latest", fingerprint: "0000000000000000000000000000000000000000", wantError: true},
		{
			name:        "additional primary key",
			channel:     "latest",
			fingerprint: "31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE",
			extraKey:    "0000000000000000000000000000000000000000",
			wantError:   true,
		},
		{name: "invalid channel", channel: "2.3.4", fingerprint: "31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			binDir := filepath.Join(home, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			writeExecutable(t, filepath.Join(binDir, "curl"), `#!/bin/sh
output=
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o) shift; output="$1" ;;
    esac
    shift
done
printf '%s\n' 'synthetic key' > "$output"
`)
			writeExecutable(t, filepath.Join(binDir, "gpg"), `#!/bin/sh
printf 'pub::::::::::\n'
printf 'fpr:::::::::%s:\n' "$FAKE_FINGERPRINT"
printf 'sub::::::::::\n'
printf 'fpr:::::::::%s:\n' '1111111111111111111111111111111111111111'
if [ -n "$FAKE_EXTRA_KEY" ]; then
    printf 'pub::::::::::\n'
    printf 'fpr:::::::::%s:\n' "$FAKE_EXTRA_KEY"
fi
`)
			writeExecutable(t, filepath.Join(binDir, "sudo"), `#!/bin/sh
printf '%s\n' "$*" >> "$HOME/sudo-calls"
if [ "$1" = tee ]; then
    cat > "$HOME/repository-line"
fi
`)
			writeExecutable(t, filepath.Join(binDir, "claude"), "#!/bin/sh\nprintf '%s\\n' '2.3.4 (Claude Code)'\n")

			runs := 2
			if test.wantError {
				runs = 1
			}
			var output []byte
			var err error
			for range runs {
				cmd := exec.Command("bash", installer, test.channel)
				cmd.Env = append(os.Environ(),
					"FAKE_FINGERPRINT="+test.fingerprint,
					"FAKE_EXTRA_KEY="+test.extraKey,
					"HOME="+home,
					"PATH="+binDir+":"+os.Getenv("PATH"),
				)
				output, err = cmd.CombinedOutput()
				if err != nil {
					break
				}
			}
			if test.wantError && err == nil {
				t.Fatalf("installer succeeded unexpectedly\n%s", output)
			}
			if !test.wantError && err != nil {
				t.Fatalf("installer failed: %v\n%s", err, output)
			}
			repo, readErr := os.ReadFile(filepath.Join(home, "repository-line"))
			if test.wantError {
				if !os.IsNotExist(readErr) {
					t.Fatalf("untrusted key configured a repository: %v", readErr)
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(repo) != test.wantRepo {
				t.Fatalf("repository=%q want=%q", repo, test.wantRepo)
			}
			sudoCalls, readErr := os.ReadFile(filepath.Join(home, "sudo-calls"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, want := range []string{
				"install -d -m 0755 /etc/apt/keyrings",
				"install -m 0644",
				"apt-get -o Acquire::Retries=3 -o APT::Update::Error-Mode=any update -qq",
				"apt-get -o Acquire::Retries=3 install -y --no-install-recommends claude-code",
			} {
				if count := bytes.Count(sudoCalls, []byte(want)); count != runs {
					t.Fatalf("sudo call count for %q = %d, want %d:\n%s", want, count, runs, sudoCalls)
				}
			}
		})
	}
}

func TestGraphifyInstallerSkipsMatchingVersions(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Graphify devcontainer installer is Linux-only")
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	installer := filepath.Join(filepath.Dir(current), "..", "..", ".devcontainer", "install-graphify.sh")
	for _, uvOutput := range []string{
		"uv " + graphifyUVVersion,
		"uv " + graphifyUVVersion + " (x86_64-unknown-linux-gnu)",
	} {
		t.Run(uvOutput, func(t *testing.T) {
			home := t.TempDir()
			binDir := filepath.Join(home, ".local", "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			writeExecutable(t, filepath.Join(binDir, "uv"), "#!/bin/sh\nprintf '%s\\n' '"+uvOutput+"'\n")
			writeExecutable(t, filepath.Join(binDir, "graphify"), "#!/bin/sh\nprintf '%s\\n' 'graphify "+graphifyVersion+"'\n")
			writeExecutable(t, filepath.Join(binDir, "curl"), "#!/bin/sh\n: > \"$HOME/curl-called\"\nexit 99\n")

			cmd := exec.Command("bash", installer)
			cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+binDir+":"+os.Getenv("PATH"))
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("installer failed: %v\n%s", err, output)
			}
			if _, err := os.Stat(filepath.Join(home, "curl-called")); !os.IsNotExist(err) {
				t.Fatalf("matching uv version unexpectedly downloaded an archive: %v", err)
			}
		})
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
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
		".devcontainer/post-create.sh": "#!/usr/bin/env bash\ngo run ./scripts/check-maintainer-contract\n" + devcontainerSystemPackagesContract + "bash \"${here}/install-claude-code.sh\"\nbash \"${here}/install-graphify.sh\"\n",
		".devcontainer/install-graphify.sh": `#!/usr/bin/env bash
set -euo pipefail
readonly UV_VERSION="` + graphifyUVVersion + `"
readonly GRAPHIFY_VERSION="` + graphifyVersion + `"
uv_sha256="d66e96b5f1ca3b99806eee283a8125d33a0bd669e6e6d9bc4ab7ffda63c41bf4"
uv_sha256="19b7f1f66895261fbaa07f8ea91da0f86337ad4e47efa594e87641c1718ffc52"
sha256sum --check --status
readonly GRAPHIFY_WHEEL_URL="https://files.pythonhosted.org/packages/c3/fe/eb0afeb410f29e2e534f2e46a2d3191a0e08c02a36176080548542371f83/graphifyy-0.9.34-py3-none-any.whl#sha256=2bb5fdc6aa96abbeb105f177040815f68253a56610af64771b5dcfa0464eb35b"
--constraints "${here}/graphify-constraints.txt"
`,
		".devcontainer/graphify-constraints.txt": graphifyConstraintsFixture,
		"Makefile": rootGoEnvironmentMakeContract +
			"check-maintainer-contract:\n\t$(GO_LOCAL_ENV) go run ./scripts/check-maintainer-contract\n" +
			windowsCompileMakeContract + coreCoverageMakeContract + moduleBoundaryMakeContract + packageBoundaryMakeContract +
			maintainabilityMakeContract + pluginsMakeContract + docsCatalogMakeContract + docsFreshnessMakeContract +
			repositorySkillsMakeContract + referenceSplitMakeContract + context7MakeContract + onboardingMakeContract +
			agentEvalFacadeMakeContract,
		"internal/agenteval/Makefile": `GO_ENV := env -u GOROOT GOTOOLCHAIN=auto GOWORK=off
REPOSITORY_ROOT ?= $(abspath ../..)
ATL_BINARY ?= $(REPOSITORY_ROOT)/atl

CAPABILITY_CATALOG_FIXTURE := $(CURDIR)/testdata/capability-catalog.v1.json
COMPAT_TEST_COUNT := 4
COMPAT_TESTS_WIRES := ^(TestFixtureWires)$$
COMPAT_TESTS_MIRROR := ^(TestFixtureMirror)$$
COMPAT_TESTS_WRITES := ^(TestFixtureWrites)$$
COMPAT_TESTS_MCP := ^TestFixtureMCP$$

.PHONY: build
build:
	$(GO_ENV) go build ./...

.PHONY: unit
unit: product-atl
	$(GO_ENV) go test ./... -count=1 -timeout=10m

.PHONY: race
race: product-atl
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

.PHONY: gen-capability-catalog
gen-capability-catalog: product-atl
	@set -eu; \
		tmp="$$(mktemp "$(CURDIR)/testdata/.capability-catalog.XXXXXX")"; \
		trap 'rm -f "$$tmp"' EXIT; \
		env -i ATL_NO_UPDATE=1 ATL_READ_ONLY=1 "$(ATL_BINARY)" capabilities -o json >"$$tmp"; \
		chmod 0644 "$$tmp"; \
		mv "$$tmp" "$(CAPABILITY_CATALOG_FIXTURE)"

.PHONY: compat-tests
compat-tests: product-atl
	@test -x "$(ATL_BINARY)"
	$(GO_ENV) go test ./... -run '$(COMPAT_TESTS_WIRES)' -count=1
	$(GO_ENV) go test ./... -run '$(COMPAT_TESTS_MIRROR)' -count=1
	$(GO_ENV) go test ./... -run '$(COMPAT_TESTS_WRITES)' -count=1
	$(GO_ENV) go test ./... -run '$(COMPAT_TESTS_MCP)' -count=1

.PHONY: compat-oracles
compat-oracles: product-atl
	@test -x "$(ATL_BINARY)"
	$(GO_ENV) go run ./cmd/agent-eval validate fixture >/dev/null
	$(GO_ENV) go run ./cmd/agent-eval validate-run fixture >/dev/null
	@set -eu; \
		ledger_parent="$$(mktemp -d)"; \
		trap 'rm -rf "$$ledger_parent"' EXIT; \
		chmod 0700 "$$ledger_parent"; \
		$(GO_ENV) go run ./cmd/agent-eval verify-atl-capabilities --ledger "$$ledger_parent/attempt-ledger" $(ATL_BINARY) >/dev/null
	$(GO_ENV) go run ./cmd/agent-eval verify-codex-skill-package $(REPOSITORY_ROOT)/plugins/atl >/dev/null

.PHONY: compat
compat: compat-tests compat-oracles

.PHONY: contract
contract: compat-oracles unit

.PHONY: product-boundary
product-boundary:
	$(MAKE) -C $(REPOSITORY_ROOT) check-package-boundary

.PHONY: full
full: tidy-check build race lint vet vuln contract windows product-boundary
`,
		"internal/agenteval/fixture_test.go": "package agenteval\n\nimport \"testing\"\n\nfunc TestFixtureWires(t *testing.T) {}\nfunc TestFixtureMirror(t *testing.T) {}\nfunc TestFixtureWrites(t *testing.T) {}\nfunc TestFixtureMCP(t *testing.T) {}\n",
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
` + checkoutStepContract + "\n" + setupGoStepContract + "\n" + buildStepContract + "\n" + ciProvenanceStepContract + "\n" + vetStepContract + "\n" + extensionProtocolRuntimeStepContract + "\n" + coreGateStepContract + "\n" + windowsCompileStepContract + `
  agent-eval:
    runs-on: ubuntu-latest
    steps:
` + agentEvalCheckoutStepContract + "\n" + setupGoStepContract + "\n" + agentEvalImpactStepContract + "\n" + agentEvalCompatStepContract + "\n" + agentEvalFullStepContract + `
  agent-eval-extension-windows:
    if: github.event_name == 'pull_request' || github.event_name == 'workflow_dispatch'
    runs-on: windows-latest
    steps:
` + checkoutStepContract + "\n" + setupGoStepContract + "\n" + extensionProtocolWindowsRuntimeStepContract + `
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
