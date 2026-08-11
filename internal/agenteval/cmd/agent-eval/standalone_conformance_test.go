package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval"
)

type standaloneWholeProcessResult struct {
	Schema          string          `json:"schema"`
	SchemaVersion   int             `json:"schema_version"`
	ContractVersion string          `json:"contract_version"`
	Command         string          `json:"command"`
	Status          string          `json:"status"`
	Result          json.RawMessage `json:"result"`
}

type standaloneConformanceArtifacts struct {
	scenarioPath    string
	observationPath string
	resultPath      string
}

type standaloneProductContractFixture struct {
	StandaloneOperations []standaloneProductOperationFixture `json:"standalone_operations"`
}

type standaloneProductOperationFixture struct {
	ID                     string `json:"id"`
	Mode                   string `json:"mode"`
	CurrentStatus          string `json:"current_status"`
	StandaloneStatus       string `json:"standalone_status"`
	Authority              string `json:"authority"`
	LocalRead              bool   `json:"local_read"`
	LocalWrite             bool   `json:"local_write"`
	ProcessSpawn           bool   `json:"process_spawn"`
	ProviderContact        bool   `json:"provider_contact"`
	BackendContact         bool   `json:"backend_contact"`
	Network                bool   `json:"network"`
	CredentialAccess       bool   `json:"credential_access"`
	PrivateWorkspaceAccess bool   `json:"private_workspace_access"`
}

func TestStandalonePublicPreReleaseWholeProcessConformance(t *testing.T) {
	artifacts := writeStandaloneConformanceArtifacts(t)
	guideSkill := standaloneAgentSkillsFixture(t, "guide-v1", "skill")
	guideWorkspace := standaloneAgentSkillsFixture(t, "workspace-guide-v1")
	anthropicSkill := standaloneAgentSkillsFixture(t, "anthropic-v1", "skill")
	anthropicPrevious := standaloneAgentSkillsFixture(t, "anthropic-v1", "previous")
	anthropicWorkspace := standaloneAgentSkillsFixture(t, "workspace-anthropic-v1")

	t.Run("version and capabilities", func(t *testing.T) {
		version := requireStandaloneWholeProcessJSONSuccess(t, []string{"version"}, "", "version")
		var result standaloneVersionResult
		decodeStandaloneWholeProcessResult(t, version.Result, &result)
		wantSchemas := map[string]bool{"agent-skills-import-report": false, "agent-skills-export-report": false}
		for _, schema := range result.Schemas {
			if _, ok := wantSchemas[schema.ID]; ok && schema.Version == 1 {
				wantSchemas[schema.ID] = true
			}
		}
		for schema, present := range wantSchemas {
			if !present {
				t.Fatalf("version omitted %q: %+v", schema, result.Schemas)
			}
		}

		capabilities := requireStandaloneWholeProcessJSONSuccess(t, []string{"capabilities"}, "", "capabilities")
		var capabilityResult standaloneCapabilitiesResult
		decodeStandaloneWholeProcessResult(t, capabilities.Result, &capabilityResult)
		assertStandaloneCapabilitiesMatchProductContract(t, capabilityResult.Capabilities)
	})

	t.Run("validate grade compare and inspect", func(t *testing.T) {
		requireStandaloneWholeProcessJSONSuccess(t, []string{
			"validate", "--kind", "scenario", "--input", artifacts.scenarioPath,
		}, "", "validate")
		requireStandaloneWholeProcessJSONSuccess(t, []string{
			"grade", "--mode", "deterministic", "--scenario", artifacts.scenarioPath,
			"--observation", artifacts.observationPath,
		}, "", "grade")
		requireStandaloneWholeProcessJSONSuccess(t, []string{
			"compare", "--kind", "results", "--input", artifacts.resultPath,
		}, "", "compare")
		requireStandaloneWholeProcessJSONSuccess(t, []string{
			"inspect", "--kind", "configuration",
		}, "", "inspect")
	})

	t.Run("Guide and Anthropic imports", func(t *testing.T) {
		guide := requireStandaloneWholeProcessJSONSuccess(t, []string{
			"import", "agent-skills", "--format", "agent-skills", "--variant", "agentskills-guide-v1",
			"--skill-root", guideSkill, "--baseline", "no-skill",
		}, "", "import agent-skills")
		var guideReport agenteval.AgentSkillsImportReport
		decodeStandaloneWholeProcessResult(t, guide.Result, &guideReport)
		if guideReport.Schema != agenteval.AgentSkillsImportReportSchema || guideReport.Format != standaloneAgentSkillsVariantGuide ||
			guideReport.Baseline != standaloneAgentSkillsBaselineNone || guideReport.CaseCount != 2 || len(guideReport.ContentSHA256) != 64 {
			t.Fatalf("Guide import report=%+v", guideReport)
		}
		assertStandaloneContentMinimized(t, string(guide.Result), guideSkill, "csv-helper", "summarize the rows")
		code, textOutput, textError := runStandaloneCoordinatorProcess(t, []string{
			"import", "agent-skills", "--format", "agent-skills", "--variant", "agentskills-guide-v1",
			"--skill-root", guideSkill, "--baseline", "no-skill", "--output", "text",
		})
		if code != 0 || textOutput != "Agent Skills import inspected\n" || textError != "" {
			t.Fatalf("Guide text import: code=%d stdout=%q stderr=%q", code, textOutput, textError)
		}

		anthropic := requireStandaloneWholeProcessJSONSuccess(t, []string{
			"import", "agent-skills", "--format", "agent-skills", "--variant", "auto",
			"--skill-root", anthropicSkill, "--baseline", "previous-skill",
			"--previous-skill-root", anthropicPrevious,
		}, "", "import agent-skills")
		var anthropicReport agenteval.AgentSkillsImportReport
		decodeStandaloneWholeProcessResult(t, anthropic.Result, &anthropicReport)
		if anthropicReport.Format != standaloneAgentSkillsVariantAnthropic ||
			anthropicReport.Baseline != standaloneAgentSkillsBaselinePrevious || anthropicReport.PreviousSkillSHA256 == "" {
			t.Fatalf("Anthropic import report=%+v", anthropicReport)
		}
		assertStandaloneContentMinimized(t, string(anthropic.Result), anthropicSkill, anthropicPrevious, "status-checker")
	})

	t.Run("Guide export selects only exactly graded pairs", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "guide-publication")
		exported := requireStandaloneWholeProcessJSONSuccess(t, []string{
			"export", "agent-skills", "--format", "agent-skills", "--variant", "agentskills-guide-v1",
			"--skill-root", guideSkill, "--baseline", "no-skill", "--workspace-root", guideWorkspace,
			"--destination", destination,
			"--case-directory", "1=iteration-1/eval-summarize-rows",
			"--case-directory", "2=iteration-1/eval-describe-columns",
		}, "", "export agent-skills")
		var report agenteval.AgentSkillsExportReport
		decodeStandaloneWholeProcessResult(t, exported.Result, &report)
		if report.Format != standaloneAgentSkillsVariantGuide || report.Authoritative ||
			report.CaseCount != 2 || report.RunCount != 4 || report.FileCount == 0 {
			t.Fatalf("Guide export report=%+v", report)
		}
		for _, name := range []string{
			"benchmark.json",
			"iteration-1/eval-summarize-rows/with_skill/grading.json",
			"iteration-1/eval-summarize-rows/without_skill/grading.json",
			"iteration-1/eval-summarize-rows/with_skill/outputs/summary.txt",
		} {
			if info, err := os.Stat(filepath.Join(destination, filepath.FromSlash(name))); err != nil || !info.Mode().IsRegular() {
				t.Fatalf("Guide export omitted %q: %v", name, err)
			}
		}
		if _, err := os.Stat(filepath.Join(destination, "iteration-1", "eval-describe-columns")); !os.IsNotExist(err) {
			t.Fatalf("Guide export invented an ungraded case: %v", err)
		}
		assertStandaloneContentMinimized(t, string(exported.Result), guideSkill, guideWorkspace, destination, "csv-helper")
	})

	t.Run("Anthropic export roundtrip and refusal", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "anthropic-publication")
		roundtripDestination := filepath.Join(t.TempDir(), "anthropic-roundtrip")
		exportArgs := []string{
			"export", "agent-skills", "--format", "agent-skills", "--variant", "anthropic-skill-creator-v1",
			"--skill-root", anthropicSkill, "--baseline", "previous-skill", "--previous-skill-root", anthropicPrevious,
			"--workspace-root", anthropicWorkspace, "--destination", destination,
		}
		exported := requireStandaloneWholeProcessJSONSuccess(t, exportArgs, "", "export agent-skills")
		var exportReport agenteval.AgentSkillsExportReport
		decodeStandaloneWholeProcessResult(t, exported.Result, &exportReport)
		if exportReport.Schema != agenteval.AgentSkillsExportReportSchema || exportReport.Format != standaloneAgentSkillsVariantAnthropic ||
			exportReport.Authoritative || exportReport.CaseCount != 1 || exportReport.FileCount == 0 {
			t.Fatalf("Anthropic export report=%+v", exportReport)
		}
		if info, err := os.Stat(filepath.Join(destination, "benchmark.json")); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("Anthropic export did not create benchmark: %v", err)
		}
		assertStandaloneContentMinimized(t, string(exported.Result), anthropicSkill, anthropicPrevious, anthropicWorkspace, destination, "status-checker")

		roundtripArgs := []string{
			"export", "agent-skills", "--format", "agent-skills", "--variant", "anthropic-skill-creator-v1",
			"--skill-root", anthropicSkill, "--baseline", "previous-skill", "--previous-skill-root", anthropicPrevious,
			"--workspace-root", destination, "--destination", roundtripDestination,
		}
		roundtrip := requireStandaloneWholeProcessJSONSuccess(t, roundtripArgs, "", "export agent-skills")
		assertStandaloneContentMinimized(t, string(roundtrip.Result), destination, roundtripDestination)

		code, stdout, stderr := runStandaloneCoordinatorProcess(t, roundtripArgs)
		if code != standaloneInputError.code || stdout != "" {
			t.Fatalf("existing destination refusal: code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		assertStandaloneError(t, stderr, standaloneInputError.id, "destination_exists", false)
		assertStandaloneContentMinimized(t, stderr, roundtripDestination)
	})

	t.Run("process success and CLI-only refusals", func(t *testing.T) {
		request := standaloneProcessRequest{
			Schema: "agent-eval/process-request", SchemaVersion: 1, ContractVersion: standaloneContractVersion,
			Command: "version", Mode: "execute", DeadlineMilliseconds: 1000,
			Configuration: standaloneProcessConfiguration{Source: "none", Environment: "none"},
			Arguments:     standaloneProcessArguments{},
		}
		data, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		requireStandaloneWholeProcessJSONSuccess(t, []string{"process"}, string(data), "version")

		marker := filepath.Join(t.TempDir(), "must-not-be-read-or-written")
		refusals := []struct {
			command   string
			arguments standaloneProcessArguments
		}{
			{command: "grade", arguments: standaloneProcessArguments{"--mode", "deterministic", "--scenario", marker, "--observation", marker}},
			{command: "import", arguments: standaloneProcessArguments{"agent-skills", "--format", "agent-skills", "--variant", "auto", "--skill-root", marker, "--baseline", "no-skill"}},
			{command: "export", arguments: standaloneProcessArguments{"agent-skills", "--format", "agent-skills", "--variant", "agentskills-guide-v1", "--skill-root", marker, "--baseline", "no-skill", "--workspace-root", marker, "--destination", marker, "--case-directory", "1=iteration-1/eval-example"}},
		}
		for _, refusal := range refusals {
			request.Command, request.Arguments = refusal.command, refusal.arguments
			data, err = json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := runStandaloneCoordinatorProcessInput(t, []string{"process"}, string(data))
			if code != standaloneCompatibilityError.code || stderr != "" {
				t.Fatalf("process %s refusal: code=%d stdout=%q stderr=%q", refusal.command, code, stdout, stderr)
			}
			assertStandaloneError(t, stdout, standaloneCompatibilityError.id, "operation_unavailable", false)
			assertStandaloneContentMinimized(t, stdout, marker)
		}
	})

	t.Run("help and completion", func(t *testing.T) {
		for _, command := range []string{"import", "export"} {
			code, stdout, stderr := runStandaloneCoordinatorProcess(t, []string{"help", command, "agent-skills"})
			if code != 0 || stderr != "" || !strings.Contains(stdout, "Modes:") ||
				!strings.Contains(stdout, "--variant") || strings.Contains(stdout, "--config") || strings.Contains(stdout, "--dry-run") {
				t.Fatalf("%s help: code=%d stdout=%q stderr=%q", command, code, stdout, stderr)
			}
			if command == "export" && strings.Contains(stdout, "auto") {
				t.Fatalf("export help admitted auto mode: %s", stdout)
			}
		}
		code, stdout, stderr := runStandaloneCoordinatorProcess(t, []string{"completion", "powershell"})
		if code != 0 || stderr != "" || !strings.Contains(stdout, `"import" { $children = @("agent-skills"); break }`) ||
			!strings.Contains(stdout, `"export" { $children = @("agent-skills"); break }`) ||
			!strings.Contains(stdout, `"import agent-skills --variant" { $children = @("auto", "agentskills-guide-v1", "anthropic-skill-creator-v1"); break }`) ||
			!strings.Contains(stdout, `"export agent-skills --variant" { $children = @("agentskills-guide-v1", "anthropic-skill-creator-v1"); break }`) ||
			strings.Contains(stdout, `$children = @("import agent-skills")`) {
			t.Fatalf("PowerShell Agent Skills completion: code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
}

func TestStandaloneAgentSkillsCLIAdmissionIsClosed(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-be-read")
	absentDestination := filepath.Join(t.TempDir(), "new-destination")
	importBase := []string{
		"import", "agent-skills", "--format", "agent-skills", "--variant", "auto",
		"--skill-root", marker, "--baseline", "no-skill",
	}
	exportBase := []string{
		"export", "agent-skills", "--format", "agent-skills", "--variant", "agentskills-guide-v1",
		"--skill-root", marker, "--baseline", "no-skill", "--workspace-root", marker,
		"--destination", absentDestination, "--case-directory", "1=iteration-1/eval-example",
	}
	tests := []struct {
		name string
		args []string
		kind string
	}{
		{name: "ambient config rejected", args: append(append([]string(nil), importBase...), "--config", marker), kind: "unknown_flag"},
		{name: "dry-run rejected", args: append(append([]string(nil), importBase...), "--dry-run"), kind: "unknown_flag"},
		{name: "import case mapping rejected", args: append(append([]string(nil), importBase...), "--case-directory", "1=iteration-1/eval-example"), kind: "unknown_flag"},
		{name: "previous root requires baseline", args: []string{"import", "agent-skills", "--format", "agent-skills", "--variant", "auto", "--skill-root", marker, "--baseline", "no-skill", "--previous-skill-root", marker}, kind: "invalid_agent_skills_baseline"},
		{name: "previous baseline requires root", args: []string{"import", "agent-skills", "--format", "agent-skills", "--variant", "auto", "--skill-root", marker, "--baseline", "previous-skill"}, kind: "invalid_agent_skills_baseline"},
		{name: "export auto rejected", args: replaceStandaloneArgument(exportBase, "agentskills-guide-v1", "auto"), kind: "invalid_agent_skills_variant"},
		{name: "Guide mapping required", args: exportBase[:len(exportBase)-2], kind: "agent_skills_case_directories_required"},
		{name: "Anthropic mapping rejected", args: replaceStandaloneArgument(exportBase, "agentskills-guide-v1", "anthropic-skill-creator-v1"), kind: "agent_skills_case_directories_not_allowed"},
		{name: "relative destination rejected", args: replaceStandaloneArgument(exportBase, absentDestination, "relative-destination"), kind: "invalid_destination"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := runStandaloneForTest(t, test.args, "")
			if code != standaloneUsageError.code || stdout != "" {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			assertStandaloneError(t, stderr, standaloneUsageError.id, test.kind, false)
			assertStandaloneContentMinimized(t, stderr, marker, absentDestination)
		})
	}
}

func requireStandaloneWholeProcessJSONSuccess(t *testing.T, args []string, input, command string) standaloneWholeProcessResult {
	t.Helper()
	code, stdout, stderr := runStandaloneCoordinatorProcessInput(t, args, input)
	if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "\n") || strings.Count(stdout, "\n") != 1 {
		t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
	}
	var envelope standaloneWholeProcessResult
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("%v: decode result: %v: %s", args, err, stdout)
	}
	wantStatus := "completed"
	if command == "inspect" {
		wantStatus = "explained"
	}
	if envelope.Schema != standaloneResultSchema || envelope.SchemaVersion != 1 ||
		envelope.ContractVersion != standaloneContractVersion || envelope.Command != command || envelope.Status != wantStatus || len(envelope.Result) == 0 {
		t.Fatalf("%v: result envelope=%+v", args, envelope)
	}
	return envelope
}

func assertStandaloneCapabilitiesMatchProductContract(t *testing.T, capabilities []standaloneCapability) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate standalone conformance test")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "..", "testdata", "standalone-product-contract.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract standaloneProductContractFixture
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatalf("decode standalone product contract: %v", err)
	}
	if len(capabilities) != len(contract.StandaloneOperations) {
		t.Fatalf("capability rows=%d contract rows=%d", len(capabilities), len(contract.StandaloneOperations))
	}
	processAPI := map[string]bool{
		"capabilities/default": true,
		"compare/default":      true,
		"inspect/default":      true,
		"validate/default":     true,
		"version/default":      true,
	}
	formats := map[string][]string{
		"export/agent-skills": {standaloneAgentSkillsVariantGuide, standaloneAgentSkillsVariantAnthropic},
		"import/agent-skills": {standaloneAgentSkillsVariantAuto, standaloneAgentSkillsVariantGuide, standaloneAgentSkillsVariantAnthropic},
	}
	for index, operation := range contract.StandaloneOperations {
		capability := capabilities[index]
		key := operation.ID + "/" + operation.Mode
		wantStatus := "unsupported"
		if operation.CurrentStatus == "implemented_pre_release" && operation.StandaloneStatus == "pre_release" {
			wantStatus = "supported"
		}
		wantDimensions := standaloneAuthorityDimensions{
			LocalRead: operation.LocalRead, LocalWrite: operation.LocalWrite, ProcessSpawn: operation.ProcessSpawn,
			ProviderContact: operation.ProviderContact, BackendContact: operation.BackendContact, Network: operation.Network,
			CredentialAccess: operation.CredentialAccess, PrivateWorkspaceAccess: operation.PrivateWorkspaceAccess,
		}
		if capability.Command != operation.ID || capability.Mode != operation.Mode || capability.Status != wantStatus ||
			capability.Authority != operation.Authority || capability.Dimensions != wantDimensions ||
			capability.ProcessAPI != processAPI[key] || !slices.Equal(capability.Formats, formats[key]) {
			t.Fatalf("capability[%d]=%+v contract=%+v", index, capability, operation)
		}
	}
}

func decodeStandaloneWholeProcessResult(t *testing.T, data json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode whole-process result: %v: %s", err, data)
	}
}

func assertStandaloneContentMinimized(t *testing.T, output string, values ...string) {
	t.Helper()
	for _, value := range values {
		if value != "" && strings.Contains(output, value) {
			t.Fatalf("output exposed content-bearing value %q: %s", value, output)
		}
	}
}

func standaloneAgentSkillsFixture(t *testing.T, components ...string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Agent Skills fixture caller")
	}
	parts := append([]string{filepath.Dir(currentFile), "..", "..", "interchange", "agentskills", "testdata"}, components...)
	return filepath.Clean(filepath.Join(parts...))
}

func writeStandaloneConformanceArtifacts(t *testing.T) standaloneConformanceArtifacts {
	t.Helper()
	scenario := agenteval.Scenario{
		SchemaVersion: agenteval.ScenarioSchemaVersion, ID: "jira.public-pre-release-synthetic",
		TaskClass: "jira/read", Description: "Exercise the standalone public pre-release surface.", DataClass: "synthetic",
		RequiredCapabilities: []string{"jira.issue.get"}, RequiredChecks: []string{"answer_correct"},
		RequiredMetrics: []string{"backend_requests", "output_bytes"},
		Budgets:         agenteval.Budgets{MaxBackendRequests: 1, MaxOutputBytes: 1024, AllowedHTTPMethods: []string{"GET"}},
	}
	observation := agenteval.Observation{
		SchemaVersion: agenteval.ObservationSchemaVersion, ScenarioID: scenario.ID, Variant: "baseline",
		Runtime:     agenteval.Runtime{Provider: "synthetic", Model: "model-synthetic", Reasoning: "none", ATLVersion: "test-atl", PromptContractSHA256: strings.Repeat("a", 64)},
		Metrics:     agenteval.InputMetrics{OutputBytes: 128},
		Coverage:    map[string]bool{"backend_requests": true, "output_bytes": true},
		HTTPMethods: map[string]int{"GET": 1}, Checks: map[string]bool{"answer_correct": true},
	}
	result, err := agenteval.Evaluate(scenario, observation)
	if err != nil {
		t.Fatalf("build conformance result: %v", err)
	}
	directory := t.TempDir()
	artifacts := standaloneConformanceArtifacts{
		scenarioPath:    filepath.Join(directory, "scenario.json"),
		observationPath: filepath.Join(directory, "observation.json"),
		resultPath:      filepath.Join(directory, "result.json"),
	}
	for path, value := range map[string]any{
		artifacts.scenarioPath: scenario, artifacts.observationPath: observation, artifacts.resultPath: result,
	} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode conformance artifact: %v", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write conformance artifact: %v", err)
		}
	}
	return artifacts
}

func replaceStandaloneArgument(arguments []string, oldValue, newValue string) []string {
	result := append([]string(nil), arguments...)
	for index, argument := range result {
		if argument == oldValue {
			result[index] = newValue
			return result
		}
	}
	panic(fmt.Sprintf("standalone test argument %q missing", oldValue))
}
