package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval"
)

func TestStandaloneUnavailableCommandsFailBeforeConfigurationOrAuthority(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "private-config-marker-must-not-be-opened")
	t.Setenv("AGENT_EVAL_UNKNOWN_PRIVATE_MARKER", "must-not-be-read")
	for _, args := range [][]string{
		{"init", "--config", marker, "--environment", "portable-v1"},
		{"plan", "--config", marker},
		{"run", "--plan", marker, "--config", marker},
		{"resume", "--config", marker},
		{"reconcile", "--config", marker},
		{"report", "--config", marker},
		{"compat", "verify", "--target", "atl", "--config", marker},
		{"grade", "--mode", "judge", "--scenario", marker, "--observation", marker, "--config", marker},
	} {
		code, stdout, stderr := runStandaloneForTest(t, args, "")
		if code != standaloneCompatibilityError.code || stdout != "" {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
		assertStandaloneError(t, stderr, standaloneCompatibilityError.id, "operation_unavailable", false)
		if strings.Contains(stderr, marker) || strings.Contains(stderr, "must-not-be-read") {
			t.Fatalf("%v leaked or consumed authority-bearing input: %s", args, stderr)
		}
	}
	for _, test := range []struct {
		args []string
		kind string
	}{
		{args: []string{"import", "--format", "agent-skills", "--config", marker}, kind: "subcommand_required"},
		{args: []string{"import", "agent-skills", "--config", marker}, kind: "unknown_flag"},
		{args: []string{"export", "agent-skills", "--config", marker}, kind: "unknown_flag"},
		{args: []string{"validate", "--kind", "agent-skills", "--input", marker, "--config", marker}, kind: "invalid_validate_options"},
	} {
		code, stdout, stderr := runStandaloneForTest(t, test.args, "")
		if code != standaloneUsageError.code || stdout != "" {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", test.args, code, stdout, stderr)
		}
		assertStandaloneError(t, stderr, standaloneUsageError.id, test.kind, false)
		if strings.Contains(stderr, marker) || strings.Contains(stderr, "must-not-be-read") {
			t.Fatalf("%v leaked or consumed authority-bearing input: %s", test.args, stderr)
		}
	}
}

func TestStandaloneFlagAndEnvironmentIdentifiersMatchProjectConfigBound(t *testing.T) {
	tooLong := strings.Repeat("x", agenteval.StandaloneProjectConfigIdentifierMaxBytes+1)
	for _, flag := range []string{"--profile", "--model"} {
		code, stdout, stderr := runStandaloneForTest(t, []string{"inspect", "--kind", "configuration", flag, tooLong}, "")
		if code != standaloneConfigurationError.code || stdout != "" {
			t.Fatalf("%s oversized value: code=%d stdout=%q stderr=%q", flag, code, stdout, stderr)
		}
		assertStandaloneError(t, stderr, standaloneConfigurationError.id, "invalid_config_value", false)
	}
	for _, variable := range []string{"AGENT_EVAL_PROFILE", "AGENT_EVAL_MODEL"} {
		t.Run(variable, func(t *testing.T) {
			t.Setenv(variable, tooLong)
			code, stdout, stderr := runStandaloneForTest(t, []string{"inspect", "--kind", "configuration", "--environment", "portable-v1"}, "")
			if code != standaloneConfigurationError.code || stdout != "" {
				t.Fatalf("oversized environment value: code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			assertStandaloneError(t, stderr, standaloneConfigurationError.id, "invalid_config_value", false)
		})
	}
}

func TestStandaloneProcessAPIAuthorityRatchet(t *testing.T) {
	root := standaloneCommandTree()
	processCommands := make(map[string]bool)
	standaloneWalkDescriptors(root, nil, func(path []string, descriptor standaloneCommandDescriptor) {
		if !descriptor.ProcessAPI {
			return
		}
		command := strings.Join(path, " ")
		processCommands[command] = true
		authority, found := standaloneAuthorityProfileFor(command, "default")
		if !found || !standaloneProcessAuthorityAllowed(command, authority.standaloneAuthorityDimensions) {
			t.Fatalf("unsafe ProcessAPI descriptor %q: found=%t authority=%+v", command, found, authority)
		}
	})
	for _, command := range []string{"version", "capabilities", "validate", "compare", "inspect", "schema inspect", "migrate preview", "migrate apply"} {
		if !processCommands[command] {
			t.Fatalf("safe ProcessAPI command %q missing from ratchet: %v", command, processCommands)
		}
	}
	for _, command := range []string{"grade", "import agent-skills", "export agent-skills"} {
		if processCommands[command] {
			t.Fatalf("%q retained ProcessAPI despite disallowed authority", command)
		}
	}
}

func TestStandalonePowerShellAndZshCompletionInsertOnlyStateChildren(t *testing.T) {
	outputs := make(map[string]string)
	for _, shell := range []string{"powershell", "zsh"} {
		var output bytes.Buffer
		if !writeStandaloneCompletion(&output, shell) {
			t.Fatalf("%s completion unavailable", shell)
		}
		outputs[shell] = output.String()
		for _, joined := range []string{"schema inspect", "migrate preview", "migrate apply", "compat verify", "completion bash"} {
			if strings.Contains(output.String(), joined) {
				t.Fatalf("%s completion inserts multi-token candidate %q:\n%s", shell, joined, output.String())
			}
		}
	}
	if !strings.Contains(outputs["powershell"], `switch ($path)`) ||
		!strings.Contains(outputs["powershell"], `"schema" { $children = @("inspect"); break }`) {
		t.Fatalf("PowerShell completion lacks schema child state:\n%s", outputs["powershell"])
	}
	if !strings.Contains(outputs["zsh"], `case "$path" in`) ||
		!strings.Contains(outputs["zsh"], `"inspect:inspect a versioned artifact schema"`) {
		t.Fatalf("zsh completion lacks schema child state:\n%s", outputs["zsh"])
	}
}

func TestStandaloneFishCompletionRequiresExactOrderedPath(t *testing.T) {
	var output bytes.Buffer
	if !writeStandaloneCompletion(&output, "fish") {
		t.Fatal("fish completion unavailable")
	}
	text := output.String()
	for _, condition := range []string{
		`test (string join ' ' (commandline -opc)) = 'agent-eval import'`,
		`test (string join ' ' (commandline -opc)) = 'agent-eval import agent-skills --variant'`,
	} {
		if !strings.Contains(text, condition) {
			t.Fatalf("Fish completion omitted exact path condition %q:\n%s", condition, text)
		}
	}
	if strings.Contains(text, "__fish_seen_subcommand_from") {
		t.Fatalf("Fish completion retained any-subcommand matching:\n%s", text)
	}
}
