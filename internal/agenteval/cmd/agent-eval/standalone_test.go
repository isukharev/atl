package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval"
)

func TestStandaloneDescriptorTreeIsImmutableAndHelpCoversEveryNode(t *testing.T) {
	first := standaloneCommandTree()
	if len(first.Children) == 0 {
		t.Fatal("descriptor tree has no commands")
	}
	first.Children[0].Name = "mutated"
	if got := standaloneCommandTree().Children[0].Name; got == "mutated" {
		t.Fatal("descriptor tree retained caller mutation")
	}

	root := standaloneCommandTree()
	var rootHelp bytes.Buffer
	if !writeStandaloneHelp(&rootHelp, nil) {
		t.Fatal("root help unavailable")
	}
	for _, required := range []string{"Usage:", "Commands:", "Examples:", "JSON is the default"} {
		if !strings.Contains(rootHelp.String(), required) {
			t.Fatalf("root help missing %q:\n%s", required, rootHelp.String())
		}
	}
	if strings.Contains(rootHelp.String(), "\n  private ") || strings.Contains(rootHelp.String(), "\n  aggregate ") {
		t.Fatalf("root help exposed maintainer-only routes:\n%s", rootHelp.String())
	}

	standaloneWalkDescriptors(root, nil, func(path []string, descriptor standaloneCommandDescriptor) {
		var output bytes.Buffer
		if !writeStandaloneHelp(&output, path) {
			t.Fatalf("help rejected descriptor path %q", strings.Join(path, " "))
		}
		if !strings.Contains(output.String(), descriptor.Summary) || !strings.Contains(output.String(), "Usage:") {
			t.Fatalf("help for %q is incomplete:\n%s", strings.Join(path, " "), output.String())
		}
	})

	for _, args := range [][]string{
		{"validate", "--help"},
		{"schema", "--help"},
		{"schema", "inspect", "--help"},
		{"completion", "bash", "--help"},
		{"help", "migrate", "preview"},
	} {
		code, stdout, stderr := runStandaloneForTest(t, args, "")
		if code != 0 || stdout == "" || stderr != "" || !strings.Contains(stdout, "Usage:") {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
}

func TestStandaloneVersionAndCapabilitiesAreStableAndConfigurationFree(t *testing.T) {
	t.Setenv("AGENT_EVAL_UNRECOGNIZED_PRIVATE_MARKER", "must-not-be-read")
	firstCode, first, firstErr := runStandaloneForTest(t, []string{"version"}, "")
	secondCode, second, secondErr := runStandaloneForTest(t, []string{"version"}, "")
	if firstCode != 0 || secondCode != 0 || firstErr != "" || secondErr != "" || first != second {
		t.Fatalf("version instability: codes=%d/%d stderr=%q/%q\nfirst=%s\nsecond=%s", firstCode, secondCode, firstErr, secondErr, first, second)
	}
	var version struct {
		Schema          string                  `json:"schema"`
		SchemaVersion   int                     `json:"schema_version"`
		ContractVersion string                  `json:"contract_version"`
		Command         string                  `json:"command"`
		Status          string                  `json:"status"`
		Result          standaloneVersionResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(first), &version); err != nil {
		t.Fatal(err)
	}
	if version.Schema != standaloneResultSchema || version.SchemaVersion != 1 || version.ContractVersion != standaloneContractVersion ||
		version.Command != "version" || version.Status != "completed" || len(version.Result.Schemas) != 10 || len(version.Result.Protocols) != 2 {
		t.Fatalf("version envelope=%+v", version)
	}

	code, output, stderr := runStandaloneForTest(t, []string{"capabilities"}, "")
	if code != 0 || stderr != "" {
		t.Fatalf("capabilities code=%d stderr=%q", code, stderr)
	}
	var capabilities struct {
		Result standaloneCapabilitiesResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(output), &capabilities); err != nil {
		t.Fatal(err)
	}
	if len(capabilities.Result.ExitClasses) != 12 {
		t.Fatalf("exit classes=%+v", capabilities.Result.ExitClasses)
	}
	for code, class := range capabilities.Result.ExitClasses {
		if class.Code != code || class.ID == "" {
			t.Fatalf("exit class[%d]=%+v", code, class)
		}
	}
	if len(capabilities.Result.Capabilities) == 0 {
		t.Fatal("capability registry is empty")
	}

	code, text, stderr := runStandaloneForTest(t, []string{"version", "--output", "text"}, "")
	if code != 0 || text != standaloneBuildVersion+"\n" || stderr != "" {
		t.Fatalf("text version code=%d stdout=%q stderr=%q", code, text, stderr)
	}
}

func TestStandaloneMaintainerRoutingUsesExplicitPublicSelectors(t *testing.T) {
	legacy := []struct {
		name string
		args []string
	}{
		{"aggregate", []string{"aggregate", "result.json"}},
		{"aggregate-root", []string{"aggregate-root", "root"}},
		{"assess", []string{"assess"}},
		{"attempt-ledger", []string{"attempt-ledger", "inspect", "--root", "ledger"}},
		{"evaluate", []string{"evaluate", "scenario", "observation"}},
		{"inventory", []string{"inventory", "corpus"}},
		{"private", []string{"private", "status"}},
		{"review-template", []string{"review-template"}},
		{"run", []string{"run", "--spec", "spec.json"}},
		{"run single-dash", []string{"run", "-spec", "spec.json"}},
		{"run bool assignment", []string{"run", "--spec=spec.json", "--dry-run=false"}},
		{"validate", []string{"validate", "scenario.json"}},
		{"validate-comparison-set", []string{"validate-comparison-set", "a", "b"}},
		{"validate-pair", []string{"validate-pair", "a", "b"}},
		{"validate-run", []string{"validate-run", "spec.json"}},
		{"verify-atl-capabilities", []string{"verify-atl-capabilities", "atl"}},
		{"verify-codex-skill-package", []string{"verify-codex-skill-package", "root"}},
		{"verify-extension-protocol", []string{"verify-extension-protocol", "--manifest", "m", "--adapter", "a", "--bundle", "b", "--ledger", "l"}},
	}
	for _, invocation := range legacy {
		if standaloneInvocation(invocation.args) {
			t.Fatalf("legacy %s was captured by standalone routing: %v", invocation.name, invocation.args)
		}
	}
	for _, args := range [][]string{
		{"validate"},
		{"validate", "--kind", "scenario", "--input", "scenario.json"},
		{"validate", "--unknown"},
		{"run"},
		{"run", "--plan", "plan.json"},
		{"run", "--spec"},
		{"run", "--unknown"},
		{"unknown-top-level"},
	} {
		if !standaloneInvocation(args) {
			t.Fatalf("public selector was not captured: %v", args)
		}
	}
	if standaloneInvocation([]string{"run", "--spec=spec.json", "--dry-run"}) {
		t.Fatal("legacy run --spec=value route was captured")
	}

	err := run([]string{"private"})
	var exit standaloneExitStatus
	if err == nil || errors.As(err, &exit) {
		t.Fatalf("private command semantics changed: err=%v", err)
	}
}

func TestStandaloneValidateSelectorEmitsClosedEnvelope(t *testing.T) {
	scenario := agenteval.Scenario{
		SchemaVersion:        agenteval.ScenarioSchemaVersion,
		ID:                   "jira.standalone-synthetic",
		TaskClass:            "jira/read",
		Description:          "Validate a bounded synthetic scenario.",
		DataClass:            "synthetic",
		RequiredCapabilities: []string{"jira.issue.get"},
		RequiredChecks:       []string{"answer_correct"},
		RequiredMetrics:      []string{"backend_requests"},
		Budgets: agenteval.Budgets{
			MaxBackendRequests: 1,
			MaxOutputBytes:     1024,
			AllowedHTTPMethods: []string{"GET"},
		},
	}
	data, err := json.Marshal(scenario)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "scenario.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runStandaloneForTest(t, []string{"validate", "--kind", "scenario", "--input", path}, "")
	if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "\n") || strings.Count(stdout, "\n") != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var envelope struct {
		Schema          string `json:"schema"`
		SchemaVersion   int    `json:"schema_version"`
		ContractVersion string `json:"contract_version"`
		Command         string `json:"command"`
		Status          string `json:"status"`
		Result          struct {
			Kind  string `json:"kind"`
			Valid bool   `json:"valid"`
			Count int    `json:"count"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Schema != standaloneResultSchema || envelope.SchemaVersion != 1 || envelope.ContractVersion != standaloneContractVersion ||
		envelope.Command != "validate" || envelope.Status != "completed" || envelope.Result.Kind != "scenario" || !envelope.Result.Valid || envelope.Result.Count != 1 {
		t.Fatalf("envelope=%+v", envelope)
	}

	code, stdout, stderr = runStandaloneForTest(t, []string{"validate", "--kind", "scenario", "--input", path, "--dry-run"}, "")
	if code != 0 || stderr != "" {
		t.Fatalf("dry-run code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var preview struct {
		Result standalonePreviewResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Result.Operation != "validate" || preview.Result.Mode != "default" ||
		preview.Result.AuthorityCeiling.Authority != "local_read" ||
		!preview.Result.AuthorityCeiling.LocalRead || preview.Result.AuthorityCeiling.LocalWrite ||
		!preview.Result.DryRunEffects.LocalRead || preview.Result.DryRunEffects.ProcessSpawn ||
		preview.Result.Resolution.Inputs.Count != 1 || len(preview.Result.Resolution.Inputs.IdentitySHA256) != 64 ||
		preview.Result.Resolution.Capabilities.RequiredCount != 1 || len(preview.Result.Resolution.Capabilities.RequiredSHA256) != 64 ||
		preview.Result.Resolution.Capabilities.ProviderExecution || preview.Result.Resolution.Capabilities.BackendExecution ||
		preview.Result.Resolution.Capabilities.GraderExecution {
		t.Fatalf("preview=%+v", preview.Result)
	}
	for _, private := range []string{path, scenario.ID, scenario.RequiredCapabilities[0]} {
		if strings.Contains(stdout, private) {
			t.Fatalf("dry-run leaked resolved identity %q: %s", private, stdout)
		}
	}
	originalIdentity := preview.Result.Resolution.Inputs.IdentitySHA256
	scenario.Description = "A semantically changed bounded synthetic scenario."
	mutatedData, err := json.Marshal(scenario)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, mutatedData, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runStandaloneForTest(t, []string{"validate", "--kind", "scenario", "--input", path, "--dry-run"}, "")
	if code != 0 || stderr != "" {
		t.Fatalf("mutated dry-run code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Result.Resolution.Inputs.IdentitySHA256 == originalIdentity || strings.Contains(stdout, scenario.Description) {
		t.Fatalf("semantic mutation was not content-minimized into resolution identity: %s", stdout)
	}

	missing := filepath.Join(t.TempDir(), "missing-private-path")
	code, stdout, stderr = runStandaloneForTest(t, []string{"validate", "--kind", "scenario", "--input", missing, "--dry-run"}, "")
	if code != standaloneInputError.code || stdout != "" || strings.Contains(stderr, missing) {
		t.Fatalf("missing dry-run input: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertStandaloneError(t, stderr, standaloneInputError.id, "invalid_scenario", false)

	oversized := filepath.Join(t.TempDir(), "oversized-scenario.json")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte("x"), (1<<20)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runStandaloneForTest(t, []string{"validate", "--kind", "scenario", "--input", oversized, "--explain"}, "")
	if code != standaloneInputError.code || stdout != "" || strings.Contains(stderr, oversized) {
		t.Fatalf("oversized dry-run input: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestStandaloneConfigPrecedenceAndPrivateProvenance(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	config := fmt.Sprintf(`{
  "schema":"agent-eval/project-config",
  "schema_version":1,
  "contract_version":%q,
  "model":"file-model-private",
	"repetitions":3
}`, standaloneContractVersion)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_EVAL_PROFILE", "environment-profile-private")
	t.Setenv("AGENT_EVAL_MODEL", "environment-model-private")
	t.Setenv("AGENT_EVAL_REPETITIONS", "9")
	flagModel := "flag-model-private"
	code, stdout, stderr := runStandaloneForTest(t, []string{
		"inspect", "--kind", "configuration",
		"--config", configPath,
		"--environment", "portable-v1",
		"--model", flagModel,
		"--explain",
	}, "")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, secret := range []string{configPath, "file-model-private", "environment-profile-private", "environment-model-private", flagModel} {
		if strings.Contains(stdout, secret) {
			t.Fatalf("configuration projection leaked %q: %s", secret, stdout)
		}
	}
	var envelope struct {
		Result standalonePreviewResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	wantSources := map[string]string{
		"profile": "opt_in_environment", "model": "flags", "repetitions": "project_file",
	}
	if envelope.Result.Configuration.ConfigSource != "explicit_file" ||
		envelope.Result.Configuration.EnvironmentProjection != "portable-v1" ||
		strings.Join(envelope.Result.Configuration.Precedence, ",") != "flags,project_file,opt_in_environment" {
		t.Fatalf("configuration summary=%+v", envelope.Result.Configuration)
	}
	for _, entry := range envelope.Result.Configuration.Provenance {
		if wantSources[entry.Key] != entry.Source || entry.ValueClass == "" {
			t.Fatalf("provenance entry=%+v want=%q", entry, wantSources[entry.Key])
		}
		delete(wantSources, entry.Key)
	}
	if len(wantSources) != 0 {
		t.Fatalf("missing provenance=%v", wantSources)
	}
}

func TestStandaloneConfigRejectsInvalidShadowedLayers(t *testing.T) {
	t.Run("environment hidden by flag", func(t *testing.T) {
		t.Setenv("AGENT_EVAL_REPETITIONS", "invalid-private-repetition")
		code, stdout, stderr := runStandaloneForTest(t, []string{
			"inspect", "--kind", "configuration",
			"--environment", "portable-v1",
			"--repetitions", "2",
		}, "")
		if code != standaloneConfigurationError.code || stdout != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		assertStandaloneError(t, stderr, standaloneConfigurationError.id, "invalid_repetitions", false)
		if strings.Contains(stderr, "invalid-private-repetition") {
			t.Fatalf("environment error leaked value: %s", stderr)
		}
	})

	t.Run("project hidden by flag", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		data := fmt.Sprintf(
			`{"schema":"agent-eval/project-config","schema_version":1,"contract_version":%q,"repetitions":0,"model":"project-private-model"}`,
			standaloneContractVersion,
		)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := runStandaloneForTest(t, []string{
			"inspect", "--kind", "configuration",
			"--config", path,
			"--repetitions", "2",
		}, "")
		if code != standaloneConfigurationError.code || stdout != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		assertStandaloneError(t, stderr, standaloneConfigurationError.id, "invalid_config_json", false)
		if strings.Contains(stderr, path) || strings.Contains(stderr, "project-private-model") {
			t.Fatalf("project error leaked value: %s", stderr)
		}
	})

	t.Run("project identity hidden by flag", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		data := fmt.Sprintf(
			`{"schema":"agent-eval/project-config","schema_version":1,"contract_version":%q,"profile":"   "}`,
			standaloneContractVersion,
		)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := runStandaloneForTest(t, []string{
			"inspect", "--kind", "configuration",
			"--config", path,
			"--profile", "valid-profile",
		}, "")
		if code != standaloneConfigurationError.code || stdout != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		assertStandaloneError(t, stderr, standaloneConfigurationError.id, "invalid_config_json", false)
	})
}

func TestStandaloneConfigRejectsUnknownDuplicateTrailingOversizeAndWalking(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"unknown", fmt.Sprintf(`{"schema":"agent-eval/project-config","schema_version":1,"contract_version":%q,"private_unknown":"private-marker"}`, standaloneContractVersion)},
		{"duplicate", fmt.Sprintf(`{"schema":"agent-eval/project-config","schema":"agent-eval/project-config","schema_version":1,"contract_version":%q}`, standaloneContractVersion)},
		{"trailing", fmt.Sprintf(`{"schema":"agent-eval/project-config","schema_version":1,"contract_version":%q} {}`, standaloneContractVersion)},
		{"future", `{"schema":"agent-eval/project-config","schema_version":2,"contract_version":"future-private"}`},
		{"null profile", fmt.Sprintf(`{"schema":"agent-eval/project-config","schema_version":1,"contract_version":%q,"profile":null}`, standaloneContractVersion)},
		{"null model", fmt.Sprintf(`{"schema":"agent-eval/project-config","schema_version":1,"contract_version":%q,"model":null}`, standaloneContractVersion)},
		{"null repetitions", fmt.Sprintf(`{"schema":"agent-eval/project-config","schema_version":1,"contract_version":%q,"repetitions":null}`, standaloneContractVersion)},
		{"long profile", fmt.Sprintf(`{"schema":"agent-eval/project-config","schema_version":1,"contract_version":%q,"profile":%q}`, standaloneContractVersion, strings.Repeat("p", agenteval.StandaloneProjectConfigIdentifierMaxBytes+1))},
		{"long model", fmt.Sprintf(`{"schema":"agent-eval/project-config","schema_version":1,"contract_version":%q,"model":%q}`, standaloneContractVersion, strings.Repeat("m", agenteval.StandaloneProjectConfigIdentifierMaxBytes+1))},
		{"oversize", strings.Repeat("x", agenteval.StandaloneProjectConfigMaxBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := runStandaloneForTest(t, []string{"inspect", "--kind", "configuration", "--config", path}, "")
			if code != standaloneConfigurationError.code || stdout != "" {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			assertStandaloneError(t, stderr, standaloneConfigurationError.id, "", false)
			if strings.Contains(stderr, path) || strings.Contains(stderr, "private-marker") || strings.Contains(stderr, "future-private") {
				t.Fatalf("configuration error leaked input: %s", stderr)
			}
		})
	}

	parent := t.TempDir()
	configDirectory := filepath.Join(parent, ".agent-eval")
	if err := os.MkdirAll(configDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	valid := fmt.Sprintf(`{"schema":"agent-eval/project-config","schema_version":1,"contract_version":%q}`, standaloneContractVersion)
	if err := os.WriteFile(filepath.Join(configDirectory, "config.json"), []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runStandaloneForTest(t, []string{"inspect", "--kind", "configuration", "--project", child}, "")
	if code != standaloneConfigurationError.code || stdout != "" {
		t.Fatalf("parent config was walked: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertStandaloneError(t, stderr, standaloneConfigurationError.id, "invalid_config_file", false)

	containedProject := t.TempDir()
	containedConfigDirectory := filepath.Join(containedProject, ".agent-eval")
	if err := os.Mkdir(containedConfigDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(containedConfigDirectory, "config.json"), []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runStandaloneForTest(t, []string{"inspect", "--kind", "configuration", "--project", containedProject}, "")
	if code != 0 || stderr != "" {
		t.Fatalf("contained project config failed: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	escapingProject := t.TempDir()
	escapingTarget := t.TempDir()
	if err := os.WriteFile(filepath.Join(escapingTarget, "config.json"), []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escapingTarget, filepath.Join(escapingProject, ".agent-eval")); err == nil {
		code, stdout, stderr = runStandaloneForTest(t, []string{"inspect", "--kind", "configuration", "--project", escapingProject}, "")
		if code != standaloneConfigurationError.code || stdout != "" {
			t.Fatalf("escaping project symlink accepted: code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		assertStandaloneError(t, stderr, standaloneConfigurationError.id, "invalid_config_file", false)
	} else {
		t.Logf("symlink containment regression unavailable: %v", err)
	}
}

func TestStandaloneEnvironmentIsClosedAndOptIn(t *testing.T) {
	t.Setenv("AGENT_EVAL_PRIVATE_UNKNOWN", "private-environment-value")
	code, stdout, stderr := runStandaloneForTest(t, []string{"inspect", "--kind", "configuration"}, "")
	if code != 0 || stderr != "" || strings.Contains(stdout, "private-environment-value") {
		t.Fatalf("ambient environment affected command: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = runStandaloneForTest(t, []string{"inspect", "--kind", "configuration", "--environment", "portable-v1"}, "")
	if code != standaloneConfigurationError.code || stdout != "" {
		t.Fatalf("unknown opt-in environment key accepted: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertStandaloneError(t, stderr, standaloneConfigurationError.id, "unknown_environment_key", false)
	if strings.Contains(stderr, "private-environment-value") {
		t.Fatalf("environment error leaked value: %s", stderr)
	}
}

func TestStandaloneProcessIsOneRequestStrictBoundedAndVersioned(t *testing.T) {
	valid := standaloneProcessRequest{
		Schema:               "agent-eval/process-request",
		SchemaVersion:        1,
		ContractVersion:      standaloneContractVersion,
		Command:              "version",
		Mode:                 "execute",
		DeadlineMilliseconds: 1000,
		Configuration:        standaloneProcessConfiguration{Source: "none", Environment: "none"},
		Arguments:            standaloneProcessArguments{},
	}
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runStandaloneForTest(t, []string{"process"}, string(data))
	if code != 0 || stderr != "" {
		t.Fatalf("process version code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var result standaloneResultEnvelope
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Schema != standaloneResultSchema || result.Command != "version" || result.Status != "completed" {
		t.Fatalf("process result=%+v", result)
	}

	malformed := []string{
		"",
		string(append([]byte{0xef, 0xbb, 0xbf}, data...)),
		string(data) + "{}",
		strings.Replace(string(data), `"mode":"execute"`, `"mode":"execute","mode":"execute"`, 1),
		strings.Replace(string(data), `"command":"version"`, `"command":"version","unknown_private":"private-value"`, 1),
		strings.Replace(string(data), `"arguments":[]`, `"arguments":null`, 1),
		strings.Replace(string(data), `,"arguments":[]`, "", 1),
		strings.Repeat("x", standaloneProcessMaxRequestBytes+1),
	}
	for index, input := range malformed {
		code, stdout, stderr = runStandaloneForTest(t, []string{"process"}, input)
		if code != standaloneInputError.code || stderr != "" {
			t.Fatalf("malformed[%d] code=%d stdout=%q stderr=%q", index, code, stdout, stderr)
		}
		assertStandaloneError(t, stdout, standaloneInputError.id, "", false)
		if strings.Contains(stdout, "private-value") {
			t.Fatalf("malformed request leaked input: %s", stdout)
		}
	}

	valid.SchemaVersion = 2
	data, err = json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runStandaloneForTest(t, []string{"process"}, string(data))
	if code != standaloneCompatibilityError.code || stderr != "" {
		t.Fatalf("future request code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertStandaloneError(t, stdout, standaloneCompatibilityError.id, "unsupported_process_version", false)

	valid.SchemaVersion = 1
	valid.DeadlineMilliseconds = 1
	data, err = json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	acknowledged := make(chan struct{})
	var deadlineStdout, deadlineStderr bytes.Buffer
	failure := runStandaloneProcessWithExecutor(
		strings.NewReader(string(data)),
		&deadlineStdout,
		&deadlineStderr,
		nil,
		func(ctx context.Context, _ []string) (standaloneOutcome, *standaloneFailure) {
			<-ctx.Done()
			close(acknowledged)
			return standaloneOutcome{}, nil
		},
	)
	<-acknowledged
	if failure == nil || failure.class.code != standaloneInterruptedError.code || deadlineStderr.Len() != 0 {
		t.Fatalf("deadline failure=%+v stdout=%q stderr=%q", failure, deadlineStdout.String(), deadlineStderr.String())
	}
	assertStandaloneError(t, deadlineStdout.String(), standaloneInterruptedError.id, "deadline_exceeded", true)

	release := make(chan struct{})
	finished := make(chan struct{})
	deadlineStdout.Reset()
	deadlineStderr.Reset()
	failure = runStandaloneProcessWithExecutor(
		strings.NewReader(string(data)),
		&deadlineStdout,
		&deadlineStderr,
		nil,
		func(_ context.Context, _ []string) (standaloneOutcome, *standaloneFailure) {
			defer close(finished)
			<-release
			return standaloneOutcome{}, nil
		},
	)
	if failure == nil || failure.class.code != standaloneOutcomeUnknownError.code || deadlineStderr.Len() != 0 {
		t.Fatalf("unacknowledged deadline failure=%+v stdout=%q stderr=%q", failure, deadlineStdout.String(), deadlineStderr.String())
	}
	assertStandaloneError(t, deadlineStdout.String(), standaloneOutcomeUnknownError.id, "deadline_outcome_unknown", false)
	close(release)
	<-finished

	buffer := &standaloneBoundedBuffer{maximum: 4}
	if written, err := buffer.Write([]byte("12345")); err == nil || written != 4 || !buffer.overflow || buffer.Len() != 4 {
		t.Fatalf("bounded write: written=%d err=%v overflow=%t len=%d", written, err, buffer.overflow, buffer.Len())
	}
}

func TestStandaloneProcessRejectsLegacyAndUnavailableBeforeConfig(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "private-config-must-not-be-read")
	requests := []standaloneProcessRequest{
		{
			Schema: "agent-eval/process-request", SchemaVersion: 1, ContractVersion: standaloneContractVersion,
			Command: "private", Mode: "execute", DeadlineMilliseconds: 1000,
			Configuration: standaloneProcessConfiguration{Source: "config", Path: marker, Environment: "portable-v1"},
			Arguments:     standaloneProcessArguments{},
		},
		{
			Schema: "agent-eval/process-request", SchemaVersion: 1, ContractVersion: standaloneContractVersion,
			Command: "init", Mode: "execute", DeadlineMilliseconds: 1000,
			Configuration: standaloneProcessConfiguration{Source: "config", Path: marker, Environment: "portable-v1"},
			Arguments:     standaloneProcessArguments{},
		},
		{
			Schema: "agent-eval/process-request", SchemaVersion: 1, ContractVersion: standaloneContractVersion,
			Command: "grade", Mode: "execute", DeadlineMilliseconds: 1000,
			Configuration: standaloneProcessConfiguration{Source: "config", Path: marker, Environment: "portable-v1"},
			Arguments:     standaloneProcessArguments{"--mode", "judge", "--scenario", marker, "--observation", marker},
		},
		{
			Schema: "agent-eval/process-request", SchemaVersion: 1, ContractVersion: standaloneContractVersion,
			Command: "grade", Mode: "execute", DeadlineMilliseconds: 1000,
			Configuration: standaloneProcessConfiguration{Source: "config", Path: marker, Environment: "portable-v1"},
			Arguments:     standaloneProcessArguments{"--mode", "deterministic", "--scenario", marker, "--observation", marker},
		},
	}
	for _, request := range requests {
		data, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := runStandaloneForTest(t, []string{"process"}, string(data))
		if code != standaloneCompatibilityError.code || stderr != "" {
			t.Fatalf("%s: code=%d stdout=%q stderr=%q", request.Command, code, stdout, stderr)
		}
		assertStandaloneError(t, stdout, standaloneCompatibilityError.id, "operation_unavailable", false)
		if strings.Contains(stdout, marker) {
			t.Fatalf("%s leaked authority-bearing path: %s", request.Command, stdout)
		}
	}
}

func TestStandaloneCompletionIsGeneratedFromPublicDescriptors(t *testing.T) {
	rootNames := standaloneChildNames(standaloneCommandTree())
	for _, shell := range []string{"bash", "fish", "powershell", "zsh"} {
		var first, second bytes.Buffer
		if !writeStandaloneCompletion(&first, shell) || !writeStandaloneCompletion(&second, shell) || first.String() != second.String() {
			t.Fatalf("%s completion was unavailable or unstable", shell)
		}
		for _, name := range rootNames {
			if !strings.Contains(first.String(), name) {
				t.Fatalf("%s completion omitted %q", shell, name)
			}
		}
		for _, hidden := range []string{"aggregate-root", "review-template", "validate-run", "verify-atl-capabilities", "atl-eval-guard", " private"} {
			if strings.Contains(first.String(), hidden) {
				t.Fatalf("%s completion exposed hidden route %q", shell, hidden)
			}
		}
	}
}

func TestStandaloneErrorsAreClosedAndDoNotUseStdout(t *testing.T) {
	code, stdout, stderr := runStandaloneForTest(t, []string{"version", "--unknown-private-flag", "private-value"}, "")
	if code != standaloneUsageError.code || stdout != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertStandaloneError(t, stderr, standaloneUsageError.id, "unknown_flag", false)
	if strings.Contains(stderr, "unknown-private-flag") || strings.Contains(stderr, "private-value") {
		t.Fatalf("error leaked arguments: %s", stderr)
	}
}

func runStandaloneForTest(t *testing.T, args []string, input string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	handled, err := runStandaloneCommand(args, strings.NewReader(input), &stdout, &stderr)
	if !handled {
		t.Fatalf("standalone command was not handled: %v", args)
	}
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	var status standaloneExitStatus
	if !errors.As(err, &status) {
		t.Fatalf("standalone command returned unclassified error: %v", err)
	}
	return status.code, stdout.String(), stderr.String()
}

func assertStandaloneError(t *testing.T, data, exitClass, kind string, retrySafe bool) {
	t.Helper()
	if !strings.HasSuffix(data, "\n") || strings.Count(data, "\n") != 1 {
		t.Fatalf("error is not exactly one JSON line: %q", data)
	}
	var envelope standaloneErrorEnvelope
	if err := json.Unmarshal([]byte(data), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v: %s", err, data)
	}
	if envelope.Schema != standaloneErrorSchema || envelope.SchemaVersion != 1 || envelope.ContractVersion != standaloneContractVersion ||
		envelope.ExitClass != exitClass || envelope.Error == "" || envelope.RetrySafe != retrySafe ||
		envelope.Recovery.SchemaVersion != 1 || envelope.Recovery.Action == "" {
		t.Fatalf("error envelope=%+v", envelope)
	}
	if kind != "" && envelope.Kind != kind {
		t.Fatalf("error kind=%q want=%q", envelope.Kind, kind)
	}
}
