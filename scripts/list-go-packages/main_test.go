package main

import (
	"reflect"
	"strings"
	"testing"
)

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
