package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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
	descriptors := make(map[string]standaloneCommandDescriptor)
	standaloneWalkDescriptors(root, nil, func(path []string, descriptor standaloneCommandDescriptor) {
		if len(descriptor.Children) == 0 {
			descriptors[strings.Join(path, " ")] = descriptor
		}
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
	for _, command := range []string{"grade", "import agent-skills", "export agent-skills", "run"} {
		if processCommands[command] {
			t.Fatalf("%q retained ProcessAPI despite disallowed authority", command)
		}
	}
	for _, profile := range standaloneAuthorityProfiles() {
		if profile.Command == "" {
			if available, processAPI, registered := standaloneCommandRegistryState(profile.Command); available || processAPI || registered {
				t.Fatalf("commandless registry row acquired command state: %+v", profile)
			}
			continue
		}
		descriptor, found := descriptors[profile.Command]
		available, processAPI, registered := standaloneCommandRegistryState(profile.Command)
		if !found || !registered || descriptor.Available != available || descriptor.ProcessAPI != processAPI {
			t.Fatalf("registry/descriptor drift for %q: descriptor=%+v state=%t/%t registered=%t",
				profile.Command, descriptor, available, processAPI, registered)
		}
	}

	var gradeHelp bytes.Buffer
	if !writeStandaloneHelp(&gradeHelp, []string{"grade"}) {
		t.Fatal("grade help unavailable")
	}
	if !strings.Contains(gradeHelp.String(), "--mode deterministic") ||
		!strings.Contains(gradeHelp.String(), "Status:\n  pre-release (supported)") ||
		!strings.Contains(gradeHelp.String(), "Reserved modes (unavailable):\n  judge") ||
		strings.Contains(gradeHelp.String(), "deterministic|judge") {
		t.Fatalf("grade help did not distinguish supported and reserved modes:\n%s", gradeHelp.String())
	}
	var reservedHelp bytes.Buffer
	if !writeStandaloneHelp(&reservedHelp, []string{"compat", "verify"}) ||
		!strings.Contains(reservedHelp.String(), "Status:\n  reserved (unavailable)") {
		t.Fatalf("reserved command help did not label its status:\n%s", reservedHelp.String())
	}
	var rootHelp bytes.Buffer
	if !writeStandaloneHelp(&rootHelp, nil) || strings.Contains(rootHelp.String(), "Status:") ||
		!strings.Contains(rootHelp.String(), "compat         verify provider-free component compatibility (reserved)") {
		t.Fatalf("root help operation status/classification drift:\n%s", rootHelp.String())
	}
	var compareHelp bytes.Buffer
	if !writeStandaloneHelp(&compareHelp, []string{"compare"}) ||
		!strings.Contains(compareHelp.String(), "--kind results|root") ||
		strings.Contains(compareHelp.String(), "results|root|pair|set") {
		t.Fatalf("compare help advertised an unavailable kind:\n%s", compareHelp.String())
	}

	rootChildren := standaloneCompletionNodes()[0].children
	wantRootChildren := []string{
		"capabilities", "version", "import", "export", "validate", "run", "grade", "compare", "inspect",
		"schema", "migrate", "completion", "process", "help",
	}
	if strings.Join(rootChildren, "\x00") != strings.Join(wantRootChildren, "\x00") {
		t.Fatalf("root completion children=%v, want %v", rootChildren, wantRootChildren)
	}
	for _, node := range standaloneCompletionNodes() {
		if strings.Join(node.path, " ") == "compat" && len(node.children) != 0 {
			t.Fatalf("reserved compat completion exposed children: %+v", node)
		}
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate standalone Process API reference")
	}
	reference, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "..",
		"docs", "reference", "agent-eval", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	const processSentence = "The exact admitted operations are"
	start := strings.Index(string(reference), processSentence)
	if start < 0 {
		t.Fatal("Process API reference omitted the canonical admitted-operation sentence")
	}
	paragraph := string(reference)[start+len(processSentence):]
	if end := strings.IndexByte(paragraph, ';'); end >= 0 {
		paragraph = paragraph[:end]
	} else {
		t.Fatal("Process API admitted-operation sentence is not closed")
	}
	gotReference := standaloneBacktickValues(paragraph)
	wantReference := make([]string, 0)
	for _, profile := range standaloneAuthorityProfiles() {
		if profile.Supported && profile.ProcessAPI {
			wantReference = append(wantReference, profile.Command)
		}
	}
	gotReference = standaloneSortedUnique(gotReference)
	wantReference = standaloneSortedUnique(wantReference)
	if strings.Join(gotReference, "\x00") != strings.Join(wantReference, "\x00") {
		t.Fatalf("Process API reference=%v registry=%v", gotReference, wantReference)
	}
}

func standaloneBacktickValues(value string) []string {
	parts := strings.Split(value, "`")
	result := make([]string, 0, len(parts)/2)
	for index := 1; index < len(parts); index += 2 {
		result = append(result, parts[index])
	}
	sort.Strings(result)
	return result
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
