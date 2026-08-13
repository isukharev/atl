package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	evaluatorCoreFixtureDir      = evaluatorModuleDir + "/core"
	evaluatorExtensionFixtureDir = evaluatorModuleDir + "/extension"
	evaluatorATLFixtureDir       = evaluatorModuleDir + "/profile/atl"
)

func TestRepositoryModuleBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("repository module boundary")
	}
	root := repositoryRoot(t)
	var output bytes.Buffer
	if err := run(root, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"status": "ok"`) ||
		!strings.Contains(output.String(), `"evaluator_module": "`+evaluatorModulePath+`"`) {
		t.Fatalf("unexpected report: %s", output.String())
	}
}

func TestModuleBoundaryRejectsLayoutAndDependencyMutations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string)
		want   string
	}{
		{
			name: "extra tracked module",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, "tools/extra/go.mod", "module example.test/extra\n\ngo 1.26.6\n")
				stageAll(t, root)
			},
			want: "tracked go.mod layout",
		},
		{
			name: "required evaluator module untracked",
			mutate: func(t *testing.T, root string) {
				runFixtureGit(t, root, "rm", "--cached", evaluatorModuleDir+"/go.mod")
			},
			want: "required module file \"internal/agenteval/go.mod\" is not tracked",
		},
		{
			name: "tracked workspace",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, "go.work", "go 1.26.6\n")
				stageAll(t, root)
			},
			want: "tracked workspace file",
		},
		{
			name: "Go patch drift",
			mutate: func(t *testing.T, root string) {
				replaceFixtureFile(t, root, evaluatorModuleDir+"/go.mod", "go 1.26.6", "go 1.26.4")
			},
			want: "go patch drift",
		},
		{
			name: "root requires evaluator",
			mutate: func(t *testing.T, root string) {
				appendFixtureFile(t, root, "go.mod", "\nrequire "+evaluatorModulePath+" v0.0.0\n")
			},
			want: "root module must not require the evaluator module",
		},
		{
			name: "root replaces evaluator",
			mutate: func(t *testing.T, root string) {
				appendFixtureFile(t, root, "go.mod", "\nreplace "+evaluatorModulePath+" => ./internal/agenteval\n")
			},
			want: "root module must not replace the evaluator module",
		},
		{
			name: "evaluator requires product",
			mutate: func(t *testing.T, root string) {
				appendFixtureFile(t, root, evaluatorModuleDir+"/go.mod", "\nrequire "+rootModulePath+" v0.0.0\n")
			},
			want: "evaluator module must not require the root module",
		},
		{
			name: "evaluator replaces product",
			mutate: func(t *testing.T, root string) {
				appendFixtureFile(t, root, evaluatorModuleDir+"/go.mod", "\nreplace "+rootModulePath+" => ../..\n")
			},
			want: "evaluator module must not replace the root module",
		},
		{
			name: "product imports evaluator",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, "internal/product/product.go", "package product\n\nimport _ \""+evaluatorModulePath+"\"\n")
				stageAll(t, root)
			},
			want: "product source internal/product/product.go imports evaluator module",
		},
		{
			name: "evaluator extension imports product private package",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, evaluatorExtensionFixtureDir+"/leak.go", "package extension\n\nimport _ \""+rootModulePath+"/internal/cli\"\n")
				stageAll(t, root)
			},
			want: "evaluator source internal/agenteval/extension/leak.go imports product-private package",
		},
		{
			name: "command lacks module self boundary",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, evaluatorCommandDir+"/main.go", "package main\n\nimport _ \"fmt\"\n")
				stageAll(t, root)
			},
			want: "missing its reviewed module self-boundary import",
		},
		{
			name: "command imports another product package",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, evaluatorCommandDir+"/main.go", "package main\n\nimport _ \""+rootModulePath+"/internal/cli\"\n")
				stageAll(t, root)
			},
			want: "evaluator command internal/agenteval/cmd/agent-eval/main.go imports product-private package",
		},
		{
			name: "command imports extension directly",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, evaluatorCommandDir+"/subpackage.go", "package main\n\nimport \""+evaluatorModulePath+"/extension\"\n")
				stageAll(t, root)
			},
			want: "outside its exact root boundary",
		},
		{
			name: "command imports product-private lookalike",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, evaluatorCommandDir+"/main.go", "package main\n\nimport \""+evaluatorModulePath+"ish\"\n")
				stageAll(t, root)
			},
			want: "imports product-private package",
		},
		{
			name: "malformed evaluator source",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, evaluatorCoreFixtureDir+"/malformed.go", "package core\n\nfunc malformed( {\n")
				stageAll(t, root)
			},
			want: "parse Go source",
		},
		{
			name: "symlinked evaluator source",
			mutate: func(t *testing.T, root string) {
				if err := os.Symlink("core.go", filepath.Join(root, filepath.FromSlash(evaluatorCoreFixtureDir), "link.go")); err != nil {
					t.Fatal(err)
				}
				stageAll(t, root)
			},
			want: "tracked Go source internal/agenteval/core/link.go must be a regular file",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeModuleFixture(t)
			test.mutate(t, root)
			var output bytes.Buffer
			err := run(root, &output)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want substring %q", err, test.want)
			}
			if output.Len() != 0 {
				t.Fatalf("failure emitted report: %q", output.String())
			}
		})
	}
}

func TestModuleBoundaryAllowsEvaluatorLibrarySelfSubpackages(t *testing.T) {
	root := writeModuleFixture(t)
	writeFixtureFile(t, root, evaluatorCoreFixtureDir+"/compat.go", "package core\n\nimport \""+evaluatorModulePath+"\"\n")
	writeFixtureFile(t, root, evaluatorATLFixtureDir+"/sibling.go", "package atl\n\nimport \""+evaluatorModulePath+"/profile/other\"\n")
	stageAll(t, root)
	if err := run(root, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
}

func TestModuleBoundaryRejectsUnexpectedModuleIdentities(t *testing.T) {
	for name, test := range map[string]struct {
		path, old, replacement, want string
	}{
		"root module identity": {
			path: "go.mod", old: rootModulePath, replacement: "example.test/root", want: "root module path",
		},
		"evaluator module identity": {
			path: evaluatorModuleDir + "/go.mod", old: evaluatorModulePath, replacement: "example.test/evaluator", want: "evaluator module path",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := writeModuleFixture(t)
			replaceFixtureFile(t, root, test.path, test.old, test.replacement)
			if err := run(root, ioDiscard{}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func writeModuleFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                                       "module " + rootModulePath + "\n\ngo 1.26.6\n",
		evaluatorModuleDir + "/go.mod":                 "module " + evaluatorModulePath + "\n\ngo 1.26.6\n",
		"cmd/atl/main.go":                              "package main\n",
		"internal/product/product.go":                  "package product\n",
		evaluatorModuleDir + "/runner.go":              "package agenteval\n\nimport (\n\t\"" + evaluatorModulePath + "/core\"\n\t\"" + evaluatorModulePath + "/extension\"\n\t\"" + evaluatorModulePath + "/profile/atl\"\n)\n",
		evaluatorCoreFixtureDir + "/core.go":           "package core\n",
		evaluatorCoreFixtureDir + "/core_test.go":      "package core_test\n\nimport \"" + evaluatorModulePath + "/core\"\n",
		evaluatorExtensionFixtureDir + "/extension.go": "package extension\n",
		evaluatorATLFixtureDir + "/profile.go":         "package atl\n\nimport \"" + evaluatorModulePath + "/core\"\n",
		evaluatorCommandDir + "/main.go":               "package main\n\nimport \"" + evaluatorModulePath + "\"\n",
	}
	for path, contents := range files {
		writeFixtureFile(t, root, path, contents)
	}
	runFixtureGit(t, root, "init", "-q")
	stageAll(t, root)
	return root
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}

func writeFixtureFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendFixtureFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(contents); err != nil {
		t.Fatal(err)
	}
	stageAll(t, root)
}

func replaceFixtureFile(t *testing.T, root, name, old, replacement string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(contents), old, replacement, 1)
	if updated == string(contents) {
		t.Fatalf("fixture %s does not contain %q", name, old)
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	stageAll(t, root)
}

func stageAll(t *testing.T, root string) {
	t.Helper()
	runFixtureGit(t, root, "add", "-A")
}

func runFixtureGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(data []byte) (int, error) { return len(data), nil }
