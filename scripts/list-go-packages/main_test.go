package main

import (
	"fmt"
	"os"
	"os/exec"
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
		module + "/scripts/agent-eval/subcommand",
		module + "/cmd/product",
		module + "/internal/agenteval",
		module + "/scripts/agent-eval",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCore := []string{
		module + "/cmd/product",
		module + "/internal/product",
		module + "/scripts/tool",
	}
	wantHeavy := []string{
		module + "/internal/agenteval",
		module + "/scripts/agent-eval",
		module + "/scripts/agent-eval/subcommand",
	}
	if !reflect.DeepEqual(sets.Core, wantCore) || !reflect.DeepEqual(sets.Heavy, wantHeavy) {
		t.Fatalf("sets=%+v want core=%v heavy=%v", sets, wantCore, wantHeavy)
	}

	for name, packages := range map[string][]string{
		"unclassified": {
			module + "/internal/agenteval", module + "/scripts/agent-eval", module + "/pkg/new",
		},
		"missing heavy root": {
			module + "/internal/agenteval", module + "/scripts/agent-eval/subcommand", module + "/cmd/product",
		},
		"duplicate": {
			module + "/internal/agenteval", module + "/scripts/agent-eval", module + "/cmd/product", module + "/cmd/product",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := classifyPackages(module, packages); err == nil {
				t.Fatal("invalid package inventory passed")
			}
		})
	}
}

func TestPackageClassHelpers(t *testing.T) {
	const module = "example.test/project"
	for _, path := range []string{
		module + "/internal/agenteval",
		module + "/internal/agenteval/sub",
		module + "/scripts/agent-eval",
		module + "/scripts/agent-eval/sub",
	} {
		if _, ok := heavyPackageRoot(module, path); !ok {
			t.Fatalf("%s is not heavy", path)
		}
	}
	if _, ok := heavyPackageRoot(module, module+"/internal/product"); ok {
		t.Fatal("product package classified as heavy")
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

func TestRepositoryPackageBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("repository go-list contract")
	}
	var output strings.Builder
	if err := run("../..", []string{"--class", "core"}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "/internal/agenteval") ||
		strings.Contains(output.String(), "/scripts/agent-eval") {
		t.Fatalf("core output contains heavy package:\n%s", output.String())
	}
}
