package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSeparatedCommandOutputIgnoresSuccessfulStderr(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestSeparatedCommandOutputHelperProcess")
	command.Env = append(os.Environ(), "ATL_LIST_PACKAGES_HELPER=success")
	output, err := separatedCommandOutput(command, "go list")
	if err != nil || output != "example.test/project/internal/example\n" {
		t.Fatalf("output=%q err=%v", output, err)
	}
}

func TestSeparatedCommandOutputPreservesFailureStderr(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestSeparatedCommandOutputHelperProcess")
	command.Env = append(os.Environ(), "ATL_LIST_PACKAGES_HELPER=failure")
	if _, err := separatedCommandOutput(command, "go list"); err == nil || !strings.Contains(err.Error(), "bounded failure detail") {
		t.Fatalf("error=%v", err)
	}
}

func TestSeparatedCommandOutputKeepsBoundedFailureTail(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestSeparatedCommandOutputHelperProcess")
	command.Env = append(os.Environ(), "ATL_LIST_PACKAGES_HELPER=large-failure")
	_, err := separatedCommandOutput(command, "go list")
	if err == nil || !strings.Contains(err.Error(), "stderr truncated to final 64 KiB") ||
		!strings.Contains(err.Error(), "distinctive final failure") || len(err.Error()) > commandStderrMaxBytes+256 {
		t.Fatalf("bounded tail error length=%d error=%v", len(errorString(err)), err)
	}
}

func TestBoundedStderrDoesNotExpandInvalidUTF8(t *testing.T) {
	input := make([]byte, commandStderrMaxBytes+257)
	for index := range input {
		if index%2 == 0 {
			input[index] = 0xff
		} else {
			input[index] = 'x'
		}
	}
	var stderr boundedStderr
	if written, err := stderr.Write(input); err != nil || written != len(input) {
		t.Fatalf("written=%d err=%v", written, err)
	}
	output := stderr.String()
	if !utf8.ValidString(output) || !strings.Contains(output, "stderr truncated to final 64 KiB") ||
		len(output) > commandStderrMaxBytes+64 {
		t.Fatalf("invalid bounded output: bytes=%d valid_utf8=%t", len(output), utf8.ValidString(output))
	}
}

func TestSeparatedCommandOutputHelperProcess(_ *testing.T) {
	switch os.Getenv("ATL_LIST_PACKAGES_HELPER") {
	case "success":
		fmt.Fprintln(os.Stdout, "example.test/project/internal/example")
		fmt.Fprintln(os.Stderr, "go: downloading example.test/module v1.0.0")
		os.Exit(0)
	case "failure":
		fmt.Fprintln(os.Stdout, "partial stdout")
		fmt.Fprintln(os.Stderr, "bounded failure detail")
		os.Exit(2)
	case "large-failure":
		fmt.Fprint(os.Stderr, strings.Repeat("download progress line\n", commandStderrMaxBytes/8))
		fmt.Fprintln(os.Stderr, "distinctive final failure")
		os.Exit(2)
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestClassifyPackagesIsExactAndFailClosed(t *testing.T) {
	const module = "example.test/project"
	sets, err := classifyPackages(module, []string{
		module + "/scripts/tool",
		module + "/internal/product",
		module + "/cmd/product",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		module + "/cmd/product",
		module + "/internal/product",
		module + "/scripts/tool",
	}
	if !reflect.DeepEqual(sets.RootCore, want) {
		t.Fatalf("sets=%+v want root-core=%v", sets, want)
	}

	for name, packages := range map[string][]string{
		"unclassified": {module + "/pkg/new"},
		"duplicate":    {module + "/cmd/product", module + "/cmd/product"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := classifyPackages(module, packages); err == nil {
				t.Fatal("invalid package inventory passed")
			}
		})
	}
}

func TestRootCorePackageHelpers(t *testing.T) {
	const module = "example.test/project"
	for _, path := range []string{
		module + "/cmd/product",
		module + "/internal/product",
		module + "/scripts/tool",
	} {
		if !rootCorePackagePath(module, path) {
			t.Fatalf("%s is not root-core", path)
		}
	}
	if rootCorePackagePath(module, module+"/pkg/product") {
		t.Fatal("unowned package classified as root-core")
	}
	filtered := filterPackagePrefix([]string{
		module + "/cmd/product",
		module + "/internal/one",
		module + "/internal/two",
	}, module+"/internal/")
	if got := strings.Join(filtered, ","); got != module+"/internal/one,"+module+"/internal/two" {
		t.Fatalf("filtered=%q", got)
	}
}

func TestRepositoryRootCoreBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("repository go-list contract")
	}
	var output strings.Builder
	if err := run("../..", []string{"--class", classRootCore}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "/internal/agenteval") ||
		strings.Contains(output.String(), "/cmd/agent-eval") {
		t.Fatalf("root-core output contains evaluator package:\n%s", output.String())
	}
}

func TestRunIgnoresBrokenGoTreeOutsideDeclaredRoots(t *testing.T) {
	root := writeGoListFixture(t)
	writeFixtureFile(t, root, "foreign/broken.go", "this is not a Go package\n")

	var output strings.Builder
	if err := run(root, []string{"--class", classRootCore}, &output); err != nil {
		t.Fatalf("run with broken foreign tree: %v", err)
	}
	if strings.Contains(output.String(), "/foreign") {
		t.Fatalf("foreign package was discovered:\n%s", output.String())
	}
	for _, required := range []string{
		"example.test/project/cmd/product",
		"example.test/project/internal/product",
		"example.test/project/scripts/tool",
	} {
		if !strings.Contains(output.String(), required) {
			t.Errorf("root-core output does not contain %q:\n%s", required, output.String())
		}
	}
}

func TestRunReportsBrokenPackageUnderDeclaredRoot(t *testing.T) {
	root := writeGoListFixture(t)
	writeFixtureFile(t, root, "internal/broken/broken.go", "package broken\nimport (\n")

	var output strings.Builder
	err := run(root, []string{"--class", classRootCore}, &output)
	if err == nil {
		t.Fatal("broken declared-root package passed discovery")
	}
	if !strings.Contains(err.Error(), "go list -f {{.ImportPath}} ./cmd/... ./internal/... ./scripts/...") ||
		!strings.Contains(err.Error(), "internal/broken") {
		t.Fatalf("error lacks useful go-list context: %v", err)
	}
}

func TestRunReportsMissingDeclaredRoot(t *testing.T) {
	root := writeGoListFixture(t)
	if err := os.RemoveAll(filepath.Join(root, "cmd")); err != nil {
		t.Fatal(err)
	}

	var output strings.Builder
	err := run(root, []string{"--class", classRootCore}, &output)
	if err == nil {
		t.Fatal("missing declared package root passed discovery")
	}
	if !strings.Contains(err.Error(), "go list -f {{.ImportPath}} ./cmd/... ./internal/... ./scripts/...") ||
		!strings.Contains(err.Error(), "pattern ./cmd/...") || len(err.Error()) > commandStderrMaxBytes+256 {
		t.Fatalf("error lacks useful missing-root context: %v", err)
	}
}

func TestRunRejectsImportedModulePackageOutsideDeclaredRoots(t *testing.T) {
	root := writeGoListFixture(t)
	writeFixtureFile(t, root, "pkg/escaped/escaped.go", "package escaped\n")
	writeFixtureFile(t, root, "internal/product/product.go", "package product\nimport _ \"example.test/project/pkg/escaped\"\n")

	var output strings.Builder
	err := run(root, []string{"--class", classRootCore}, &output)
	if err == nil {
		t.Fatal("imported module package outside declared roots passed classification")
	}
	if !strings.Contains(err.Error(), `test dependency "example.test/project/pkg/escaped" is outside declared package roots`) {
		t.Fatalf("error lacks escaped dependency identity: %v", err)
	}
}

func TestRootCoreEnvironmentIgnoresAmbientWorkspaceControls(t *testing.T) {
	t.Setenv("GOROOT", "/forbidden")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOWORK", "/forbidden/go.work")
	environment := strings.Join(goCommandEnvironment(), "\n")
	for _, forbidden := range []string{"GOROOT=", "GOTOOLCHAIN=local", "GOWORK=/forbidden/go.work"} {
		if strings.Contains(environment, forbidden) {
			t.Fatalf("environment retains %q: %s", forbidden, environment)
		}
	}
	for _, required := range []string{"GOTOOLCHAIN=auto", "GOWORK=off"} {
		if !strings.Contains(environment, required) {
			t.Fatalf("environment is missing %q: %s", required, environment)
		}
	}
}

func writeGoListFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, contents := range map[string]string{
		"go.mod":                      "module example.test/project\n\ngo 1.26.0\n",
		"cmd/product/product.go":      "package product\n",
		"internal/product/product.go": "package product\n",
		"scripts/tool/tool.go":        "package tool\n",
	} {
		writeFixtureFile(t, root, name, contents)
	}
	return root
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
