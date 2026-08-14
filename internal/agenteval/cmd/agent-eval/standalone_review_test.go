package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval"
)

func TestStandaloneFrozenAuthorityProfiles(t *testing.T) {
	expected := map[string]string{
		"capabilities/default":        "none/00000000",
		"compare/default":             "local_read/10000000",
		"compat verify/provider-free": "verifier_execution/10100000",
		"export/agent-skills":         "local_write/11000000",
		"grade/deterministic":         "verifier_execution/10100000",
		"grade/judge":                 "provider_execution/10110110",
		"import/agent-skills":         "local_read/10000000",
		"import/default":              "local_write/11000000",
		"init/default":                "local_write/01000001",
		"inspect/default":             "local_read/10000000",
		"migrate apply/default":       "local_write/11000001",
		"migrate preview/default":     "local_read/10000001",
		"plan/default":                "local_write/11000001",
		"promote/default":             "local_write/11000000",
		"reconcile/evidence-only":     "local_write/11000001",
		"report/default":              "local_read/10000000",
		"resume/default":              "agent_execution/11111111",
		"resume/reference":            "local_write/11000000",
		"rollback/default":            "local_write/11000000",
		"run/default":                 "agent_execution/11111110",
		"run/reference":               "local_write/11000000",
		"schema inspect/default":      "local_read/10000000",
		"validate/default":            "local_read/10000000",
		"version/default":             "none/00000000",
	}
	profiles := standaloneAuthorityProfiles()
	if len(profiles) != len(expected) {
		t.Fatalf("authority profiles=%d want=%d", len(profiles), len(expected))
	}
	seen := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		key := profile.Operation + "/" + profile.Mode
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate authority profile %q", key)
		}
		seen[key] = struct{}{}
		got := profile.Authority + "/" + standaloneAuthorityBits(profile.standaloneAuthorityDimensions)
		if got != expected[key] {
			t.Fatalf("authority %q=%q want=%q", key, got, expected[key])
		}
		delete(expected, key)
	}
	if len(expected) != 0 {
		t.Fatalf("missing authority profiles=%v", expected)
	}
}

func TestStandaloneResolutionIdentityBindsSemanticsAndInputMultiplicity(t *testing.T) {
	base := struct {
		ID      string `json:"id"`
		Outcome string `json:"outcome"`
	}{ID: "synthetic-input", Outcome: "accepted"}
	mutated := base
	mutated.Outcome = "rejected"
	baseIdentity, err := standaloneSemanticIdentity("result", base)
	if err != nil {
		t.Fatal(err)
	}
	mutatedIdentity, err := standaloneSemanticIdentity("result", mutated)
	if err != nil {
		t.Fatal(err)
	}
	if baseIdentity == mutatedIdentity {
		t.Fatal("semantic mutation retained the same resolution identity")
	}
	otherIdentity, err := standaloneSemanticIdentity("result", struct {
		ID string `json:"id"`
	}{ID: "second-synthetic-input"})
	if err != nil {
		t.Fatal(err)
	}
	one := standaloneEvidenceDigest("input", "compare", "default", "results", []string{baseIdentity})
	duplicate := standaloneEvidenceDigest("input", "compare", "default", "results", []string{baseIdentity, baseIdentity})
	ordered := standaloneEvidenceDigest("input", "compare", "default", "results", []string{baseIdentity, otherIdentity})
	reversed := standaloneEvidenceDigest("input", "compare", "default", "results", []string{otherIdentity, baseIdentity})
	if one == duplicate {
		t.Fatal("input multiset digest deduplicated repeated semantic identities")
	}
	if ordered != reversed {
		t.Fatal("input multiset digest depends on caller ordering")
	}
	for _, digest := range []string{baseIdentity, mutatedIdentity, one, duplicate} {
		if strings.Contains(digest, base.ID) || strings.Contains(digest, base.Outcome) {
			t.Fatalf("resolution digest exposed semantic content: %q", digest)
		}
	}
}

func TestStandaloneGradePreviewSeparatesCeilingFromEffects(t *testing.T) {
	scenario := agenteval.Scenario{
		SchemaVersion:        agenteval.ScenarioSchemaVersion,
		ID:                   "jira.preview-synthetic",
		TaskClass:            "jira/read",
		Description:          "Exercise a bounded deterministic preview.",
		DataClass:            "synthetic",
		RequiredCapabilities: []string{"jira.issue.get"},
		RequiredChecks:       []string{"answer_correct"},
		RequiredMetrics:      []string{"backend_requests", "output_bytes"},
		Budgets: agenteval.Budgets{
			MaxBackendRequests: 1,
			MaxOutputBytes:     1024,
			AllowedHTTPMethods: []string{"GET"},
		},
	}
	observation := agenteval.Observation{
		SchemaVersion: agenteval.ObservationSchemaVersion,
		ScenarioID:    scenario.ID,
		Variant:       "baseline",
		Runtime: agenteval.Runtime{
			Provider: "codex", Model: "private-model-marker", Reasoning: "high", ATLVersion: "test-atl",
			PromptContractSHA256: strings.Repeat("a", 64),
		},
		Metrics:     agenteval.InputMetrics{OutputBytes: 128},
		Coverage:    map[string]bool{"backend_requests": true, "output_bytes": true},
		HTTPMethods: map[string]int{"GET": 1},
		Checks:      map[string]bool{"answer_correct": true},
	}
	directory := t.TempDir()
	scenarioPath := filepath.Join(directory, "private-scenario.json")
	observationPath := filepath.Join(directory, "private-observation.json")
	for path, value := range map[string]any{scenarioPath: scenario, observationPath: observation} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, stdout, stderr := runStandaloneForTest(t, []string{
		"grade", "--mode", "deterministic",
		"--scenario", scenarioPath,
		"--observation", observationPath,
		"--dry-run",
	}, "")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var envelope struct {
		Result standalonePreviewResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	result := envelope.Result
	if result.Operation != "grade" || result.Mode != "deterministic" ||
		result.AuthorityCeiling.Authority != "verifier_execution" ||
		!result.AuthorityCeiling.LocalRead || !result.AuthorityCeiling.ProcessSpawn ||
		result.AuthorityCeiling.ProviderContact || result.AuthorityCeiling.BackendContact ||
		!result.DryRunEffects.LocalRead || standaloneAuthorityBits(result.DryRunEffects) != "10000000" ||
		result.Resolution.Inputs.Count != 2 || result.Resolution.Capabilities.RequiredCount != 1 ||
		result.Resolution.Capabilities.ProviderExecution ||
		result.Resolution.Capabilities.BackendExecution ||
		result.Resolution.Capabilities.GraderExecution {
		t.Fatalf("preview=%+v", result)
	}
	for _, private := range []string{scenarioPath, observationPath, scenario.ID, scenario.RequiredCapabilities[0], observation.Runtime.Model} {
		if strings.Contains(stdout, private) {
			t.Fatalf("preview leaked %q: %s", private, stdout)
		}
	}
}

func TestStandaloneProjectConfigContainsIdentityDefaultsOnly(t *testing.T) {
	for _, flagName := range []string{"--result-root", "--atl-binary", "--plugin-root", "--agent-binary"} {
		code, stdout, stderr := runStandaloneForTest(t, []string{
			"inspect", "--kind", "configuration", flagName, "private-path-marker",
		}, "")
		if code != standaloneUsageError.code || stdout != "" {
			t.Fatalf("%s: code=%d stdout=%q stderr=%q", flagName, code, stdout, stderr)
		}
		assertStandaloneError(t, stderr, standaloneUsageError.id, "unknown_flag", false)
		if strings.Contains(stderr, "private-path-marker") {
			t.Fatalf("%s leaked value: %s", flagName, stderr)
		}
	}

	for _, key := range []string{"result_root", "atl_binary", "plugin_root", "agent_binary"} {
		path := filepath.Join(t.TempDir(), "config.json")
		data := fmt.Sprintf(
			`{"schema":"agent-eval/project-config","schema_version":1,"contract_version":%q,%q:"private-path-marker"}`,
			standaloneContractVersion,
			key,
		)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := runStandaloneForTest(t, []string{"inspect", "--kind", "configuration", "--config", path}, "")
		if code != standaloneConfigurationError.code || stdout != "" {
			t.Fatalf("%s: code=%d stdout=%q stderr=%q", key, code, stdout, stderr)
		}
		assertStandaloneError(t, stderr, standaloneConfigurationError.id, "invalid_config_json", false)
		if strings.Contains(stderr, path) || strings.Contains(stderr, "private-path-marker") {
			t.Fatalf("%s leaked config input: %s", key, stderr)
		}
	}
}

func TestStandaloneCoordinatorWholeProcessStructuredFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		args      []string
		exitClass standaloneExitClass
		kind      string
	}{
		{name: "incomplete validate", args: []string{"validate"}, exitClass: standaloneUsageError, kind: "invalid_validate_options"},
		{name: "reserved run", args: []string{"run"}, exitClass: standaloneCompatibilityError, kind: "operation_unavailable"},
		{name: "unknown top level", args: []string{"unknown-private-command"}, exitClass: standaloneUsageError, kind: "unknown_command"},
		{name: "malformed validate selector", args: []string{"validate", "--kind"}, exitClass: standaloneUsageError, kind: "invalid_flag"},
		{name: "malformed run selector", args: []string{"run", "--plan"}, exitClass: standaloneCompatibilityError, kind: "operation_unavailable"},
		{name: "unknown help topic", args: []string{"unknown-private-command", "--help"}, exitClass: standaloneUsageError, kind: "unknown_help_topic"},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := runStandaloneCoordinatorProcess(t, test.args)
			if code != test.exitClass.code || stdout != "" {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			assertStandaloneError(t, stderr, test.exitClass.id, test.kind, false)
			if strings.Contains(stderr, "private-command") {
				t.Fatalf("structured failure leaked invocation: %s", stderr)
			}
		})
	}
}

func TestStandaloneReferenceFailureClassesPreserveThePublicationBoundary(t *testing.T) {
	for _, test := range []struct {
		name      string
		failure   *standaloneFailure
		exitClass standaloneExitClass
		kind      string
		retrySafe bool
	}{
		{name: "invalid input", failure: standaloneReferenceInputFailure("invalid_reference_bundle"), exitClass: standaloneInputError, kind: "invalid_reference_bundle", retrySafe: true},
		{name: "unsupported profile", failure: standaloneReferenceRunFailure(context.Background(), agenteval.ErrSequentialReferenceUnsupported), exitClass: standaloneCompatibilityError, kind: "reference_profile_unsupported", retrySafe: true},
		{name: "pre-authority interruption", failure: standaloneReferenceRunFailure(context.Background(), context.Canceled), exitClass: standaloneInterruptedError, kind: "execution_canceled", retrySafe: true},
		{name: "post-authority unknown", failure: standaloneReferenceRunFailure(context.Background(), agenteval.ErrSequentialReferenceOutcomeUnknown), exitClass: standaloneOutcomeUnknownError, kind: "reference_outcome_unknown", retrySafe: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.failure == nil || test.failure.class != test.exitClass || test.failure.kind != test.kind || test.failure.retrySafe != test.retrySafe {
				t.Fatalf("failure=%+v", test.failure)
			}
		})
	}
}

func TestStandaloneCoordinatorProcessHelper(_ *testing.T) {
	if os.Getenv("GO_WANT_AGENT_EVAL_COORDINATOR_HELPER") != "1" {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		os.Exit(90)
	}
	if err := run(os.Args[separator+1:]); err != nil {
		var status standaloneExitStatus
		if errors.As(err, &status) {
			os.Exit(status.code)
		}
		fmt.Fprintln(os.Stderr, "agent-eval:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runStandaloneCoordinatorProcess(t *testing.T, args []string) (int, string, string) {
	return runStandaloneCoordinatorProcessInput(t, args, "")
}

func runStandaloneCoordinatorProcessInput(t *testing.T, args []string, input string) (int, string, string) {
	t.Helper()
	commandArgs := append([]string{"-test.run=^TestStandaloneCoordinatorProcessHelper$", "--"}, args...)
	command := exec.Command(os.Args[0], commandArgs...)
	command.Env = standaloneCoordinatorHelperEnvironment()
	command.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("run coordinator helper: %v", err)
	}
	return exit.ExitCode(), stdout.String(), stderr.String()
}

func standaloneCoordinatorHelperEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GO_WANT_AGENT_EVAL_COORDINATOR_HELPER=") {
			environment = append(environment, entry)
		}
	}
	return append(environment, "GO_WANT_AGENT_EVAL_COORDINATOR_HELPER=1")
}

func standaloneAuthorityBits(dimensions standaloneAuthorityDimensions) string {
	bits := []bool{
		dimensions.LocalRead,
		dimensions.LocalWrite,
		dimensions.ProcessSpawn,
		dimensions.ProviderContact,
		dimensions.BackendContact,
		dimensions.Network,
		dimensions.CredentialAccess,
		dimensions.PrivateWorkspaceAccess,
	}
	var result strings.Builder
	for _, bit := range bits {
		if bit {
			result.WriteByte('1')
		} else {
			result.WriteByte('0')
		}
	}
	return result.String()
}
