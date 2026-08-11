package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval"
)

func TestStandaloneSchemaInspectionAndMigrationCLIProcessContract(t *testing.T) {
	code, stdout, stderr := runStandaloneForTest(t, []string{
		"schema", "inspect", "--namespace", "atl-profile", "--kind", "private-workspace",
	}, "")
	if code != 0 || stderr != "" {
		t.Fatalf("schema inspect code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var envelope standaloneWholeProcessResult
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	var inspection agenteval.StandaloneSchemaInspection
	if err := json.Unmarshal(envelope.Result, &inspection); err != nil || inspection.Current != agenteval.PrivateWorkspaceSchemaVersion || inspection.SupportedMigrations != 1 {
		t.Fatalf("inspection=%+v err=%v", inspection, err)
	}

	originalPreview, originalApply := standalonePreviewMigration, standaloneApplyMigration
	t.Cleanup(func() { standalonePreviewMigration, standaloneApplyMigration = originalPreview, originalApply })
	previewCalls, applyCalls := 0, 0
	preview := standaloneMigrationCLIFixturePreview()
	standalonePreviewMigration = func(options agenteval.StandaloneMigrationPreviewOptions) (agenteval.StandaloneMigrationPreview, error) {
		previewCalls++
		if options.Namespace != "atl-profile" || options.Kind != "private-workspace" || options.From != 3 || options.To != 4 ||
			options.Root != "/private/workspace" || options.RepositoryRoot != "/repository" {
			t.Fatalf("preview options=%+v", options)
		}
		return preview, nil
	}
	result := agenteval.StandaloneMigrationResult{
		Schema: agenteval.StandaloneMigrationResultSchema, SchemaVersion: agenteval.StandaloneMigrationArtifactVersion,
		ContractVersion: agenteval.StandaloneContractVersion, Status: "migrated", Namespace: preview.Namespace, Kind: preview.Kind,
		From: preview.From, To: preview.To, SourceSHA256: preview.SourceSHA256, CandidateSHA256: preview.CandidateSHA256,
		ImplementationSHA256: preview.ImplementationSHA256, RegistrySHA256: preview.RegistrySHA256, PreviewSHA256: preview.PreviewSHA256,
	}
	standaloneApplyMigration = func(options agenteval.StandaloneMigrationApplyOptions) (agenteval.StandaloneMigrationResult, error) {
		applyCalls++
		if options.ExpectedPreviewSHA256 != preview.PreviewSHA256 || options.Confirm != agenteval.StandaloneMigrationConfirmation || options.Root != "/private/workspace" {
			t.Fatalf("apply options=%+v", options)
		}
		return result, nil
	}

	base := []string{"--namespace", "atl-profile", "--kind", "private-workspace", "--from", "3", "--to", "4", "--root", "/private/workspace", "--repository-root", "/repository"}
	code, stdout, stderr = runStandaloneForTest(t, append([]string{"migrate", "preview"}, base...), "")
	if code != 0 || stderr != "" || previewCalls != 1 || strings.Contains(stdout, "/private/workspace") || strings.Contains(stdout, "/repository") {
		t.Fatalf("preview code=%d calls=%d stdout=%q stderr=%q", code, previewCalls, stdout, stderr)
	}
	applyArgs := append(append([]string{"migrate", "apply"}, base...), "--expected-preview-sha256", preview.PreviewSHA256, "--confirm", agenteval.StandaloneMigrationConfirmation)
	code, stdout, stderr = runStandaloneForTest(t, applyArgs, "")
	if code != 0 || stderr != "" || applyCalls != 1 || strings.Contains(stdout, "/private/workspace") || strings.Contains(stdout, "/repository") {
		t.Fatalf("apply code=%d calls=%d stdout=%q stderr=%q", code, applyCalls, stdout, stderr)
	}

	denied := append(append([]string{"migrate", "apply"}, base...), "--expected-preview-sha256", preview.PreviewSHA256, "--confirm", "NO")
	code, stdout, stderr = runStandaloneForTest(t, denied, "")
	if code != standalonePolicyDeniedError.code || stdout != "" || applyCalls != 1 {
		t.Fatalf("denied code=%d calls=%d stdout=%q stderr=%q", code, applyCalls, stdout, stderr)
	}
	assertStandaloneError(t, stderr, standalonePolicyDeniedError.id, "migration_confirmation_required", false)

	request := standaloneProcessRequest{
		Schema: "agent-eval/process-request", SchemaVersion: 1, ContractVersion: standaloneContractVersion,
		Command: "schema", Mode: "execute", DeadlineMilliseconds: 1000,
		Configuration: standaloneProcessConfiguration{Source: "none", Environment: "none"},
		Arguments:     standaloneProcessArguments{"inspect", "--namespace", "standalone", "--kind", "schema-registry"},
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runStandaloneForTest(t, []string{"process"}, string(data))
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"command":"schema inspect"`) {
		t.Fatalf("process schema code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	request.Command = "migrate"
	request.Arguments = append(standaloneProcessArguments{"apply"}, applyArgs[2:]...)
	data, err = json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runStandaloneForTest(t, []string{"process"}, string(data))
	if code != 0 || stderr != "" || applyCalls != 2 || !strings.Contains(stdout, `"command":"migrate apply"`) {
		t.Fatalf("process apply code=%d calls=%d stdout=%q stderr=%q", code, applyCalls, stdout, stderr)
	}
}

func standaloneMigrationCLIFixturePreview() agenteval.StandaloneMigrationPreview {
	return agenteval.StandaloneMigrationPreview{
		Schema: agenteval.StandaloneMigrationPreviewSchema, SchemaVersion: agenteval.StandaloneMigrationArtifactVersion,
		ContractVersion: agenteval.StandaloneContractVersion, Status: "ready", Namespace: "atl-profile", Kind: "private-workspace",
		From: 3, To: 4, Privacy: "owner_private", SourceSHA256: strings.Repeat("a", 64), CandidateSHA256: strings.Repeat("b", 64),
		AdapterPreviewSHA256: strings.Repeat("c", 64), ImplementationSHA256: strings.Repeat("d", 64),
		MigrationGraphSHA256: strings.Repeat("e", 64), RegistrySHA256: strings.Repeat("f", 64),
		Counts:        []agenteval.StandaloneMigrationCount{{ID: "preserved_run_records", Value: 2}, {ID: "preserved_run_sets", Value: 1}, {ID: "preserved_spec_references", Value: 3}},
		PreviewSHA256: strings.Repeat("1", 64),
	}
}
