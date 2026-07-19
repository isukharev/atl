package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const privatePlanTestSecret = "SYNTHETIC_PRIVATE_PLAN_SECRET"

type privatePlanTestFixture struct {
	root, repository, liveConfig, pluginRoot string
	agent, atl, wrapper                      string
	mutationControl                          string
	runSetAlias                              string
	now                                      time.Time
}

func TestCreatePrivatePlanRequiresBoundedConsentAndReturnsSafePreview(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, false, false)
	options := fixture.createOptions()

	for _, test := range []struct {
		name   string
		mutate func(*PrivatePlanCreateOptions)
		code   string
	}{
		{name: "confirmation", code: "consent", mutate: func(options *PrivatePlanCreateOptions) { options.Confirm = "YES" }},
		{name: "provider data", code: "consent", mutate: func(options *PrivatePlanCreateOptions) { options.Consent.ProviderDataApproved = false }},
		{name: "malformed expiry", code: "consent_expiry", mutate: func(options *PrivatePlanCreateOptions) { options.Consent.ExpiresAt = "tomorrow" }},
		{name: "expired", code: "consent_expiry", mutate: func(options *PrivatePlanCreateOptions) {
			options.Consent.ExpiresAt = fixture.now.Add(-time.Second).Format(time.RFC3339)
		}},
		{name: "too far", code: "consent_expiry", mutate: func(options *PrivatePlanCreateOptions) {
			options.Consent.ExpiresAt = fixture.now.Add(7*24*time.Hour + time.Second).Format(time.RFC3339)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := options
			test.mutate(&candidate)
			_, err := CreatePrivatePlan(context.Background(), candidate)
			assertPrivatePlanError(t, err, test.code)
		})
	}
	if entries, err := os.ReadDir(filepath.Join(fixture.root, "plans")); err != nil || len(entries) != 0 {
		t.Fatalf("rejected consent created a plan: entries=%d err=%v", len(entries), err)
	}
	assertPrivatePlanNoRuntimeInvocation(t, fixture)

	preview, err := CreatePrivatePlan(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if preview.SchemaVersion != PrivatePlanSchemaVersion || !privatePlanIDRE.MatchString(preview.PlanID) || !validSHA256(preview.PlanSHA256) ||
		preview.Provider != "codex" || preview.Model != "test-model" || len(preview.Surfaces) != 1 || preview.Surfaces[0] != SurfaceATLMCP {
		t.Fatalf("preview=%+v", preview)
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivatePlanTextSafe(t, string(encoded), fixture)
	planData, err := os.ReadFile(filepath.Join(fixture.root, "plans", preview.PlanID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(preview.PlanSHA256), []byte(sha256HexBytes(planData))) {
		t.Fatal("preview hash does not bind the exact stored plan")
	}
	for _, forbidden := range []string{privatePlanTestSecret, fixture.liveConfig, fixture.agent, "fake-agent-1"} {
		if bytes.Contains(planData, []byte(forbidden)) {
			t.Fatalf("stored plan retained private input, executable path, or version output %q", forbidden)
		}
	}
}

func TestCreatePrivatePlanRejectsRunSetAboveWorkspaceCostBudget(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, false, false)
	manifestPath := filepath.Join(fixture.root, PrivateWorkspaceManifestName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Execution.MaxEstimatedCostMicroUSD = maxRunCostMicroUSD - 1
	data, err = EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(manifestPath, data); err != nil {
		t.Fatal(err)
	}
	_, err = CreatePrivatePlan(context.Background(), fixture.createOptions())
	assertPrivatePlanError(t, err, "cost_budget")
	assertPrivatePlanNoRuntimeInvocation(t, fixture)
}

func TestCreatePrivatePlanSerializesWorkspaceMutation(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, false, false)
	lock, err := acquirePrivateWorkspaceLock(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Unlock() }()

	_, err = CreatePrivatePlan(context.Background(), fixture.createOptions())
	assertPrivatePlanError(t, err, "workspace_busy")
	assertPrivatePlanNoRuntimeInvocation(t, fixture)
	entries, readErr := os.ReadDir(filepath.Join(fixture.root, "plans"))
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("blocked create wrote plan state: entries=%d err=%v", len(entries), readErr)
	}
}

func TestCreatePrivatePlanRequiresExternalUpstreamConsent(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, false, false)
	caseRoot := filepath.Join(fixture.root, "cases", "portfolio")
	var scenario Scenario
	readPrivatePlanTestJSON(t, filepath.Join(caseRoot, "scenario.json"), &scenario)
	scenario.Category = BenchmarkCategoryNeutralCommon
	scenario.Budgets.MaxInterfaceInvocations = scenario.Budgets.MaxATLInvocations
	scenario.Budgets.MaxATLInvocations = 0
	scenario.RequiredMetrics = replacePrivatePlanTestString(scenario.RequiredMetrics, "atl_invocations", "interface_invocations")
	scenario.RequiredChecks = removePrivatePlanTestString(scenario.RequiredChecks, "http_observed")
	writeJSONTestFile(t, filepath.Join(caseRoot, "scenario.json"), scenario)
	var spec RunSpec
	readPrivatePlanTestJSON(t, filepath.Join(caseRoot, "run.mcp.json"), &spec)
	spec.Category = BenchmarkCategoryNeutralCommon
	spec.Surface = SurfaceExternalMCP
	spec.Variant = "external-mcp"
	spec.AllowedMCPTools = []string{"safe_lookup"}
	spec.DataCapabilities = []string{"jira.issue.field"}
	filteredChecks := make([]RunCheck, 0, len(spec.Checks))
	for _, check := range spec.Checks {
		switch check.Kind {
		case "atl_all_succeeded":
			check.Kind = "interface_all_succeeded"
		case "atl_invocations_min":
			check.Kind = "interface_invocations_min"
		case "http_methods_observed":
			continue
		}
		filteredChecks = append(filteredChecks, check)
	}
	spec.Checks = filteredChecks
	writeJSONTestFile(t, filepath.Join(caseRoot, "run.mcp.json"), spec)
	if _, _, err := ValidateRunSpecFile(filepath.Join(caseRoot, "run.mcp.json")); err != nil {
		t.Fatalf("external test spec: %v", err)
	}

	profileRoot := filepath.Join(t.TempDir(), "external-profile")
	if err := os.Mkdir(profileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(profileRoot, "profile.json")
	writeJSONTestFile(t, profilePath, validExternalTestProfile())
	t.Setenv(DefaultPrivateWorkspaceManifest().ExternalMCPProfileEnv, profilePath)

	options := fixture.createOptions()
	_, err := CreatePrivatePlan(context.Background(), options)
	assertPrivatePlanError(t, err, "external_consent")
	assertPrivatePlanNoRuntimeInvocation(t, fixture)
	options.Consent.ExternalUpstreamApproved = true
	preview, err := CreatePrivatePlan(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Surfaces) != 1 || preview.Surfaces[0] != SurfaceExternalMCP {
		t.Fatalf("preview=%+v", preview)
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("example.invalid")) || bytes.Contains(encoded, []byte("safe_lookup")) {
		t.Fatalf("external preview leaked upstream policy: %s", encoded)
	}
}

func TestExecutePrivatePlanBindsApprovalAndReviewedInputs(t *testing.T) {
	t.Run("approval", func(t *testing.T) {
		fixture := newPrivatePlanTestFixture(t, false, false)
		preview := fixture.createPlan(t)
		options := fixture.executeOptions(preview)
		options.Confirm = "YES"
		_, err := ExecutePrivatePlan(context.Background(), options)
		assertPrivatePlanError(t, err, "approval")
		assertPrivatePlanNoRuntimeInvocation(t, fixture)
		options = fixture.executeOptions(preview)
		options.ExpectedPlanSHA256 = strings.Repeat("0", 64)
		_, err = ExecutePrivatePlan(context.Background(), options)
		assertPrivatePlanError(t, err, "plan_hash")
		assertPrivatePlanTextSafe(t, err.Error(), fixture)
		assertPrivatePlanNoRuntimeInvocation(t, fixture)
		options = fixture.executeOptions(preview)
		options.Now = fixture.now.Add(2 * time.Hour)
		_, err = ExecutePrivatePlan(context.Background(), options)
		assertPrivatePlanError(t, err, "expired")
		assertPrivatePlanNoRuntimeInvocation(t, fixture)
		if _, err := os.Lstat(filepath.Join(fixture.root, "plans", preview.PlanID+".state.json")); !os.IsNotExist(err) {
			t.Fatalf("failed approval consumed the plan: %v", err)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, privatePlanTestFixture)
	}{
		{name: "case", mutate: func(t *testing.T, fixture privatePlanTestFixture) {
			appendPrivatePlanTestFile(t, filepath.Join(fixture.root, "cases", "portfolio", "prompt.md"), "\nchanged\n")
		}},
		{name: "config", mutate: func(t *testing.T, fixture privatePlanTestFixture) {
			appendPrivatePlanTestFile(t, filepath.Join(fixture.liveConfig, "config.json"), "\n")
		}},
		{name: "binary", mutate: func(t *testing.T, fixture privatePlanTestFixture) {
			appendPrivatePlanTestFile(t, fixture.atl, "\n# changed\n")
		}},
		{name: "plugin-skill", mutate: func(t *testing.T, fixture privatePlanTestFixture) {
			appendPrivatePlanTestFile(t, filepath.Join(fixture.pluginRoot, "plugins", "atl", "skills", "atl", "SKILL.md"), "\nChanged.\n")
		}},
		{name: "plugin-routing", mutate: func(t *testing.T, fixture privatePlanTestFixture) {
			appendPrivatePlanTestFile(t, filepath.Join(fixture.pluginRoot, "plugins", "atl", "skills", "atl", "agents", "openai.yaml"), "\nChanged.\n")
		}},
		{name: "plugin-manifest", mutate: func(t *testing.T, fixture privatePlanTestFixture) {
			appendPrivatePlanTestFile(t, filepath.Join(fixture.pluginRoot, "plugins", "atl", ".codex-plugin", "plugin.json"), "\n")
		}},
		{name: "plugin-marketplace", mutate: func(t *testing.T, fixture privatePlanTestFixture) {
			appendPrivatePlanTestFile(t, filepath.Join(fixture.pluginRoot, ".agents", "plugins", "marketplace.json"), "\n")
		}},
		{name: "plugin-package", mutate: func(t *testing.T, fixture privatePlanTestFixture) {
			appendPrivatePlanTestFile(t, filepath.Join(fixture.pluginRoot, "plugins", "atl", ".mcp.json"), "\n")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			includeCLI := test.name == "plugin-marketplace" || test.name == "plugin-package"
			fixture := newPrivatePlanTestFixture(t, includeCLI, false)
			preview := fixture.createPlan(t)
			test.mutate(t, fixture)
			_, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
			assertPrivatePlanError(t, err, "input_drift")
			assertPrivatePlanTextSafe(t, err.Error(), fixture)
			assertPrivatePlanNoRuntimeInvocation(t, fixture)
			if _, err := os.Lstat(filepath.Join(fixture.root, "plans", preview.PlanID+".state.json")); !os.IsNotExist(err) {
				t.Fatalf("input drift consumed the plan: %v", err)
			}
		})
	}
}

func TestPrivatePlanAndStateRejectSymlinksAndNonPrivateModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission and symlink boundary")
	}
	t.Run("plan symlink", func(t *testing.T) {
		fixture := newPrivatePlanTestFixture(t, false, false)
		preview := fixture.createPlan(t)
		planPath := filepath.Join(fixture.root, "plans", preview.PlanID+".json")
		outside := filepath.Join(t.TempDir(), "reviewed-plan.json")
		data, err := os.ReadFile(planPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outside, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(planPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, planPath); err != nil {
			t.Fatal(err)
		}
		_, err = ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
		assertPrivatePlanError(t, err, "plan_hash")
		assertPrivatePlanNoRuntimeInvocation(t, fixture)
	})

	t.Run("plan mode", func(t *testing.T) {
		fixture := newPrivatePlanTestFixture(t, false, false)
		preview := fixture.createPlan(t)
		planPath := filepath.Join(fixture.root, "plans", preview.PlanID+".json")
		if err := os.Chmod(planPath, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
		assertPrivatePlanError(t, err, "plan_hash")
		assertPrivatePlanNoRuntimeInvocation(t, fixture)
	})

	t.Run("state symlink", func(t *testing.T) {
		fixture := newPrivatePlanTestFixture(t, false, false)
		preview := fixture.createPlan(t)
		outside := filepath.Join(t.TempDir(), "completed.state.json")
		state := privatePlanState{SchemaVersion: 1, PlanSHA256: preview.PlanSHA256, RunID: "run-00000000000000000000000000000000", Status: "completed", CompletedSurfaces: []string{SurfaceATLMCP}}
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outside, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(fixture.root, "plans", preview.PlanID+".state.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID); err == nil {
			t.Fatal("symlinked completed state passed")
		}
		_, err = ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
		assertPrivatePlanError(t, err, "consumed")
		assertPrivatePlanNoRuntimeInvocation(t, fixture)
	})

	t.Run("state mode", func(t *testing.T) {
		fixture := newPrivatePlanTestFixture(t, false, false)
		preview := fixture.createPlan(t)
		statePath := filepath.Join(fixture.root, "plans", preview.PlanID+".state.json")
		state := privatePlanState{SchemaVersion: 1, PlanSHA256: preview.PlanSHA256, RunID: "run-00000000000000000000000000000000", Status: "completed", CompletedSurfaces: []string{SurfaceATLMCP}}
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(statePath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID); err == nil {
			t.Fatal("non-private completed state passed")
		}
	})
}

func TestCreatePrivatePlanRotatesSurfaceOrderAfterCompletedPlan(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, true, false)
	first := fixture.createPlan(t)
	wantFirst := []string{SurfaceCLISkill, SurfaceATLMCP}
	if !equalStrings(first.Surfaces, wantFirst) {
		t.Fatalf("first surfaces=%v want=%v", first.Surfaces, wantFirst)
	}
	completed := privatePlanState{SchemaVersion: 1, PlanSHA256: first.PlanSHA256, RunID: "run-00000000000000000000000000000000", Status: "completed", CompletedSurfaces: append([]string(nil), first.Surfaces...)}
	if err := os.Mkdir(filepath.Join(fixture.root, "runs", completed.RunID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writePrivatePlanState(filepath.Join(fixture.root, "plans", first.PlanID+".state.json"), completed); err != nil {
		t.Fatal(err)
	}
	second := fixture.createPlan(t)
	wantSecond := []string{SurfaceATLMCP, SurfaceCLISkill}
	if !equalStrings(second.Surfaces, wantSecond) {
		t.Fatalf("second surfaces=%v want=%v", second.Surfaces, wantSecond)
	}
}

func TestPrivatePlanBindsSkillActivationAndPromptContractBeforeExecution(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, true, false)
	preview := fixture.createPlan(t)
	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	for index := range plan.Items {
		if plan.Items[index].Surface != SurfaceCLISkill {
			continue
		}
		if plan.Items[index].SkillActivation != SkillActivationImplicit || !validSHA256(plan.Items[index].PromptContractSHA256) {
			t.Fatalf("cli item=%+v", plan.Items[index])
		}
		plan.Items[index].SkillActivation = SkillActivationExplicit
		changed = true
	}
	if !changed {
		t.Fatal("plan has no cli-skill item")
	}
	data, err := encodePrivatePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "plans", preview.PlanID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	tampered := preview
	tampered.PlanSHA256 = sha256HexBytes(data)
	_, err = ExecutePrivatePlan(context.Background(), fixture.executeOptions(tampered))
	assertPrivatePlanError(t, err, "input_drift")
	assertPrivatePlanNoRuntimeInvocation(t, fixture)
}

func TestPrivatePlanPromptIdentityPreservesExplicitSchemaGenerations(t *testing.T) {
	item := privatePlanItem{
		Provider:             "codex",
		Surface:              SurfaceCLISkill,
		PromptContractSHA256: strings.Repeat("a", 64),
	}
	for _, activation := range []string{SkillActivationImplicit, SkillActivationExplicit, SkillActivationDeveloper, SkillActivationCombined} {
		item.SkillActivation = activation
		if !validPrivatePlanPromptIdentity(PrivatePlanSchemaVersion, item) {
			t.Fatalf("current plan rejected activation %q", activation)
		}
	}
	for _, activation := range []string{SkillActivationImplicit, SkillActivationExplicit} {
		item.SkillActivation = activation
		if !validPrivatePlanPromptIdentity(LegacyPromptBoundPrivatePlanSchemaVersion, item) {
			t.Fatalf("legacy prompt-bound plan rejected activation %q", activation)
		}
	}
	for _, activation := range []string{SkillActivationDeveloper, SkillActivationCombined} {
		item.SkillActivation = activation
		if validPrivatePlanPromptIdentity(LegacyPromptBoundPrivatePlanSchemaVersion, item) {
			t.Fatalf("legacy prompt-bound plan accepted new activation %q", activation)
		}
	}
	item.SkillActivation = ""
	item.PromptContractSHA256 = ""
	if !validPrivatePlanPromptIdentity(LegacyPrivatePlanSchemaVersion, item) {
		t.Fatal("pre-prompt plan identity became unreadable")
	}
	if validPrivatePlanPromptIdentity(PrivatePlanSchemaVersion+1, item) {
		t.Fatal("future plan schema accepted prompt identity")
	}
}

func TestLegacyPromptBoundPrivatePlanV2RemainsReadable(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, true, false)
	preview := fixture.createPlan(t)
	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	plan.SchemaVersion = LegacyPromptBoundPrivatePlanSchemaVersion
	plan.Kind = ""
	plan.ReviewerReserveMicroUSD = 0
	plan.CostAssurance = ""
	plan.StudyContract = nil
	plan.ActivationContract = nil
	for index := range plan.Items {
		plan.Items[index].CellID = ""
		plan.Items[index].MaxEstimatedCostMicroUSD = 0
	}
	data, err := encodePrivatePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "plans", preview.PlanID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil || loaded.SchemaVersion != LegacyPromptBoundPrivatePlanSchemaVersion {
		t.Fatalf("legacy prompt-bound plan=%+v err=%v", loaded, err)
	}
}

func TestLegacyPrivatePlanV1RemainsReadableByWorkspaceLifecycle(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, true, false)
	preview := fixture.createPlan(t)
	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	plan.SchemaVersion = LegacyPrivatePlanSchemaVersion
	plan.Kind = ""
	plan.ReviewerReserveMicroUSD = 0
	plan.CostAssurance = ""
	plan.StudyContract = nil
	plan.ActivationContract = nil
	for index := range plan.Items {
		plan.Items[index].CellID = ""
		plan.Items[index].MaxEstimatedCostMicroUSD = 0
		plan.Items[index].SkillActivation = ""
		plan.Items[index].PromptContractSHA256 = ""
	}
	data, err := encodePrivatePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "plans", preview.PlanID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil || loaded.SchemaVersion != LegacyPrivatePlanSchemaVersion {
		t.Fatalf("legacy plan=%+v err=%v", loaded, err)
	}
	if report, err := DoctorPrivateWorkspace(fixture.root, fixture.repository); err != nil || !report.Healthy {
		t.Fatalf("legacy workspace report=%+v err=%v", report, err)
	}
	legacyPreview := preview
	legacyPreview.PlanSHA256 = sha256HexBytes(data)
	_, err = ExecutePrivatePlan(context.Background(), fixture.executeOptions(legacyPreview))
	assertPrivatePlanError(t, err, "legacy_plan_read_only")
	assertPrivatePlanNoRuntimeInvocation(t, fixture)
}

func TestExecutePrivatePlanCompletesExactlyOnceAndLoadsBaselineSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, false)
	preview := fixture.createPlan(t)

	if _, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID); err == nil {
		t.Fatal("unexecuted plan loaded as completed")
	}
	summary, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	if err != nil {
		t.Fatal(err)
	}
	if summary.PlanID != preview.PlanID || !privateRunIDRE.MatchString(summary.RunID) || summary.Status != "completed" || summary.Completed != 1 || len(summary.Surfaces) != 1 || summary.Surfaces[0] != SurfaceATLMCP || summary.EstimatedCostMicroUSD != 140 {
		t.Fatalf("summary=%+v", summary)
	}
	assertPrivatePlanInvocationCount(t, fixture.agent, 1)
	assertPrivatePlanInvocationCount(t, fixture.atl, 1)

	source, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if !source.Completed || !source.Immutable || source.PlanID != preview.PlanID || source.PlanSHA256 != preview.PlanSHA256 ||
		!privateRunIDRE.MatchString(source.RunID) || len(source.Surfaces) != 1 || source.Surfaces[0].Surface != SurfaceATLMCP {
		t.Fatalf("source=%+v", source)
	}
	wantRunDirectory := filepath.Join(source.RunRoot, "raw", "private.pair", "codex", "typed-mcp", "run-01")
	if source.Surfaces[0].RunDirectory != wantRunDirectory {
		t.Fatalf("run directory=%q want=%q", source.Surfaces[0].RunDirectory, wantRunDirectory)
	}
	if _, err := os.Stat(filepath.Join(wantRunDirectory, "result.json")); err != nil {
		t.Fatal(err)
	}
	scratchEntries, err := os.ReadDir(filepath.Join(fixture.root, ".ephemeral"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range scratchEntries {
		if entry.Name() != filepath.Base(privateWorkspaceLockPath) {
			t.Fatalf("private execution left scratch entry %q", entry.Name())
		}
	}

	_, err = ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	assertPrivatePlanError(t, err, "consumed")
	assertPrivatePlanInvocationCount(t, fixture.agent, 1)
	assertPrivatePlanInvocationCount(t, fixture.atl, 1)

	statePath := filepath.Join(fixture.root, "plans", preview.PlanID+".state.json")
	var state privatePlanState
	stateData, err := os.ReadFile(statePath)
	if err != nil || json.Unmarshal(stateData, &state) != nil {
		t.Fatal(err)
	}
	state.PlanSHA256 = strings.Repeat("f", 64)
	if err := writePrivatePlanState(statePath, state); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID); err == nil {
		t.Fatal("state detached from the plan hash loaded as completed")
	}
}

func TestPrivateMCPOnlyPlanIgnoresUnusedCodexPackageControls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, false)
	preview := fixture.createPlan(t)
	appendPrivatePlanTestFile(t, filepath.Join(fixture.pluginRoot, "plugins", "atl", ".mcp.json"), "\nchanged but unused by typed MCP\n")
	appendPrivatePlanTestFile(t, filepath.Join(fixture.pluginRoot, ".agents", "plugins", "marketplace.json"), "\n")
	summary, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "completed" || len(summary.Surfaces) != 1 || summary.Surfaces[0] != SurfaceATLMCP {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestPrivatePlanBindsQualitativePanelBeforeExecution(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, false, false)
	assignmentPath := filepath.Join(fixture.root, "cases", "blind-assignment.txt")
	writeTestFile(t, assignmentPath, "opaque assignment a\n", 0o600)
	panel := testPrivateQualitativePanel()
	panel.BlindAssignment = "cases/blind-assignment.txt"
	setPrivatePlanTestPanel(t, fixture, panel)

	firstPreview := fixture.createPlan(t)
	first, _, err := loadPrivatePlan(fixture.root, firstPreview.PlanID)
	if err != nil || first.QualitativeReviewPanel == nil || !first.QualitativeRequired {
		t.Fatalf("first plan=%+v err=%v", first, err)
	}
	firstContract := first.ContractSHA256

	panel.Reviewers[0].ID = "judge-1b"
	setPrivatePlanTestPanel(t, fixture, panel)
	rosterPreview := fixture.createPlan(t)
	roster, _, err := loadPrivatePlan(fixture.root, rosterPreview.PlanID)
	if err != nil || roster.ContractSHA256 == firstContract {
		t.Fatalf("reviewer roster did not change contract: first=%s roster=%s err=%v", firstContract, roster.ContractSHA256, err)
	}

	panel.Reviewers[0].Model = "model-a-v2"
	setPrivatePlanTestPanel(t, fixture, panel)
	secondPreview := fixture.createPlan(t)
	second, _, err := loadPrivatePlan(fixture.root, secondPreview.PlanID)
	if err != nil || second.ContractSHA256 == roster.ContractSHA256 {
		t.Fatalf("reviewer model did not change contract: roster=%s second=%s err=%v", roster.ContractSHA256, second.ContractSHA256, err)
	}

	panel.MaxCriterionRangeBPS++
	setPrivatePlanTestPanel(t, fixture, panel)
	thirdPreview := fixture.createPlan(t)
	third, _, err := loadPrivatePlan(fixture.root, thirdPreview.PlanID)
	if err != nil || third.ContractSHA256 == second.ContractSHA256 {
		t.Fatalf("range policy did not change contract: second=%s third=%s err=%v", second.ContractSHA256, third.ContractSHA256, err)
	}

	writeTestFile(t, assignmentPath, "opaque assignment b\n", 0o600)
	fourthPreview := fixture.createPlan(t)
	fourth, _, err := loadPrivatePlan(fixture.root, fourthPreview.PlanID)
	if err != nil || fourth.ContractSHA256 == third.ContractSHA256 || fourth.QualitativeReviewPanel.BlindAssignmentSHA256 == third.QualitativeReviewPanel.BlindAssignmentSHA256 {
		t.Fatalf("assignment bytes did not change contract: third=%+v fourth=%+v err=%v", third.QualitativeReviewPanel, fourth.QualitativeReviewPanel, err)
	}

	panel.Method = "unsupported-method"
	setPrivatePlanTestPanelRaw(t, fixture, panel)
	_, err = CreatePrivatePlan(context.Background(), fixture.createOptions())
	assertPrivatePlanError(t, err, "doctor")
}

func TestPrivatePlanNeutralPanelRequiresBlindAssignment(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, false, false)
	runSet := PrivateWorkspaceRunSet{Alias: "portfolio", SpecPaths: []string{"cases/portfolio/run.mcp.json"}, QualitativeReviewPanel: testPrivateQualitativePanel()}
	if _, _, _, err := buildPrivateQualitativePanelMaterial(fixture.root, runSet, true); err == nil {
		t.Fatal("neutral panel without blind assignment passed")
	}
	writeTestFile(t, filepath.Join(fixture.root, "cases", "blind.txt"), "opaque assignment\n", 0o600)
	runSet.QualitativeReviewPanel.BlindAssignment = "cases/blind.txt"
	contract, contractJSON, assignment, err := buildPrivateQualitativePanelMaterial(fixture.root, runSet, true)
	if err != nil || contract == nil || len(contractJSON) == 0 || string(assignment) != "opaque assignment\n" {
		t.Fatalf("contract=%+v json=%d assignment=%q err=%v", contract, len(contractJSON), assignment, err)
	}
}

func TestPrivatePlanPersistsImmutableQualitativePanelMaterials(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, false, false)
	assignmentPath := filepath.Join(fixture.root, "cases", "blind.txt")
	writeTestFile(t, assignmentPath, "opaque assignment\n", 0o600)
	panel := testPrivateQualitativePanel()
	panel.BlindAssignment = "cases/blind.txt"
	setPrivatePlanTestPanel(t, fixture, panel)
	preview := fixture.createPlan(t)
	if _, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview)); err != nil {
		t.Fatal(err)
	}
	source, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID)
	if err != nil || len(source.Surfaces) != 1 {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	surface := source.Surfaces[0]
	if surface.QualitativePanelContractPath == "" || !validSHA256(surface.QualitativePanelContractSHA256) ||
		surface.BlindAssignmentPath == "" || !validSHA256(surface.BlindAssignmentSHA256) {
		t.Fatalf("panel handoff=%+v", surface)
	}
	for _, path := range []string{surface.QualitativePanelContractPath, surface.BlindAssignmentPath} {
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("persisted material %q info=%v err=%v", filepath.Base(path), info, statErr)
		}
	}

	// Completed-run review sources are bound to persisted execution-time bytes,
	// not later manifest or case edits.
	writeTestFile(t, assignmentPath, "later workspace assignment\n", 0o600)
	panel.Reviewers[0].Model = "later-model"
	setPrivatePlanTestPanel(t, fixture, panel)
	stable, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID)
	if err != nil || stable.Surfaces[0].BlindAssignmentSHA256 != surface.BlindAssignmentSHA256 || stable.Surfaces[0].QualitativePanelContractSHA256 != surface.QualitativePanelContractSHA256 {
		t.Fatalf("completed source drifted: stable=%+v err=%v", stable, err)
	}

	persistedAssignment, err := os.ReadFile(surface.BlindAssignmentPath)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, surface.BlindAssignmentPath, "drifted assignment\n", 0o600)
	if _, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID); err == nil {
		t.Fatal("drifted persisted blind assignment loaded")
	}
	writeTestFile(t, surface.BlindAssignmentPath, string(persistedAssignment), 0o600)
	appendPrivatePlanTestFile(t, surface.QualitativePanelContractPath, " ")
	if _, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID); err == nil {
		t.Fatal("drifted persisted panel contract loaded")
	}
}

func TestExecutePrivatePlanInterruptedStateCannotReplay(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, false, true)
	preview := fixture.createPlan(t)
	summary, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	assertPrivatePlanError(t, err, "execution")
	if summary.Status != "interrupted" || summary.Completed != 0 {
		t.Fatalf("summary=%+v", summary)
	}
	assertPrivatePlanInvocationCount(t, fixture.agent, 1)
	assertPrivatePlanInvocationCount(t, fixture.atl, 1)
	_, err = ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	assertPrivatePlanError(t, err, "consumed")
	assertPrivatePlanInvocationCount(t, fixture.agent, 1)
	assertPrivatePlanInvocationCount(t, fixture.atl, 1)
	if _, err := LoadCompletedPrivateRun(fixture.root, fixture.repository, preview.PlanID); err == nil {
		t.Fatal("interrupted plan loaded as completed")
	}
}

func TestExecutePrivatePlanRechecksInputsBetweenSurfaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, true, false)
	manifestData, err := os.ReadFile(filepath.Join(fixture.root, PrivateWorkspaceManifestName))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(manifestData))
	if err != nil {
		t.Fatal(err)
	}
	manifest.RunSets[0].SpecPaths[0], manifest.RunSets[0].SpecPaths[1] = manifest.RunSets[0].SpecPaths[1], manifest.RunSets[0].SpecPaths[0]
	manifestData, err = EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(filepath.Join(fixture.root, PrivateWorkspaceManifestName), manifestData); err != nil {
		t.Fatal(err)
	}
	preview := fixture.createPlan(t)
	if err := os.WriteFile(fixture.mutationControl, []byte(filepath.Join(fixture.pluginRoot, "plugins", "atl", "skills", "atl", "SKILL.md")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	assertPrivatePlanError(t, err, "input_drift")
	if summary.Status != "interrupted" || summary.Completed != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	assertPrivatePlanInvocationCount(t, fixture.agent, 1)
	_, err = ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	assertPrivatePlanError(t, err, "consumed")
	assertPrivatePlanInvocationCount(t, fixture.agent, 1)
}

func TestExecutePrivatePlanRechecksExecutionSnapshotBetweenSurfaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivatePlanTestFixture(t, true, false)
	manifestPath := filepath.Join(fixture.root, PrivateWorkspaceManifestName)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(manifestData))
	if err != nil {
		t.Fatal(err)
	}
	manifest.RunSets[0].SpecPaths[0], manifest.RunSets[0].SpecPaths[1] = manifest.RunSets[0].SpecPaths[1], manifest.RunSets[0].SpecPaths[0]
	manifestData, err = EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(manifestPath, manifestData); err != nil {
		t.Fatal(err)
	}
	preview := fixture.createPlan(t)
	if err := os.WriteFile(fixture.mutationControl, []byte("SNAPSHOT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	assertPrivatePlanError(t, err, "snapshot_drift")
	if summary.Status != "interrupted" || summary.Completed != 1 || summary.RunID == "" {
		t.Fatalf("summary=%+v", summary)
	}
	assertPrivatePlanInvocationCount(t, fixture.agent, 1)
	assertPrivatePlanInvocationCount(t, fixture.atl, 1)
}

func newPrivatePlanTestFixture(t *testing.T, includeCLI, failAgent bool) privatePlanTestFixture {
	t.Helper()
	useSyntheticCodexHome(t)
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", repository, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	writeTestFile(t, filepath.Join(repository, "README.md"), "synthetic repository\n", 0o600)
	if output, err := exec.Command("git", "-C", repository, "add", "README.md").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	commit := exec.Command("git", "-C", repository, "-c", "user.name=ATL Tests", "-c", "user.email=atl-tests@example.invalid", "commit", "-qm", "test fixture")
	if output, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}

	root := filepath.Join(t.TempDir(), "private-workspace")
	manifest := DefaultPrivateWorkspaceManifest()
	manifest.Execution.MaxEstimatedCostMicroUSD = 3 * maxRunCostMicroUSD
	if report, err := InitPrivateWorkspace(root, repository, manifest); err != nil || !report.Healthy {
		t.Fatalf("init report=%+v err=%v", report, err)
	}
	sourceCase, _, _, _, _ := writePrivatePairFixture(t)
	caseRoot := filepath.Join(root, "cases", "portfolio")
	if err := copyWorkspace(sourceCase, caseRoot); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(caseRoot, "prompt.md"), "Use the reviewed interface. "+privatePlanTestSecret+"\n", 0o600)
	specPaths := []string{"cases/portfolio/run.mcp.json"}
	if includeCLI {
		specPaths = []string{"cases/portfolio/run.cli.json", "cases/portfolio/run.mcp.json"}
	}
	manifest.RunSets = []PrivateWorkspaceRunSet{{Alias: "portfolio", SpecPaths: specPaths, QualitativeReviewRequired: true}}
	manifestData, err := EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(filepath.Join(root, PrivateWorkspaceManifestName), manifestData); err != nil {
		t.Fatal(err)
	}
	if report, err := DoctorPrivateWorkspace(root, repository); err != nil || !report.Healthy {
		t.Fatalf("doctor report=%+v err=%v", report, err)
	}

	liveConfig := filepath.Join(t.TempDir(), "live-config")
	if err := os.Mkdir(liveConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(liveConfig, "config.json"), `{"synthetic_marker":"`+privatePlanTestSecret+`"}`+"\n", 0o600)
	writeTestFile(t, filepath.Join(liveConfig, "credentials.json"), `{"jira":"synthetic-token"}`+"\n", 0o600)
	t.Setenv(manifest.LiveConfigEnv, liveConfig)

	pluginRoot := filepath.Join(t.TempDir(), "plugin")
	writeTestPluginTrees(t, pluginRoot, "test", "Synthetic skill.")

	binRoot := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(binRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(binRoot, "fake-agent")
	mutationControl := filepath.Join(binRoot, "mutation-control")
	agentSource := filepath.Join(binRoot, "fake-agent.go")
	agentProgram := fmt.Sprintf(`package main
import ("fmt"; "os"; "os/exec"; "path/filepath"; "strings")
const calls = %q
const mutationControl = %q
const fail = %t
func appendFile(path, value string) { f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); if err != nil { os.Exit(90) }; _, _ = f.WriteString(value); _ = f.Close() }
func main() {
  if len(os.Args) > 1 && os.Args[1] == "--version" { appendFile(calls+".version", "x\n"); fmt.Println("fake-agent-1"); return }
  if len(os.Args) > 2 && os.Args[1] == "sandbox" { command := exec.Command(os.Args[len(os.Args)-1]); command.Stdout = os.Stdout; command.Stderr = os.Stderr; command.Env = os.Environ(); if err := command.Run(); err != nil { os.Exit(43) }; return }
  appendFile(calls, "x\n")
  if fail { os.Exit(41) }
  guard := os.Getenv("ATL_EVAL_HTTP_GUARD_FILE"); if guard == "" { os.Exit(42) }
  _ = os.WriteFile(guard, []byte("{\"method\":\"GET\",\"request_hash\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}\n"), 0600)
  final := ""; for index := 1; index < len(os.Args); index++ { if os.Args[index] == "--output-last-message" && index+1 < len(os.Args) { final = os.Args[index+1]; index++ } }
  if os.Getenv("ATL_EVAL_CLI_POLICY_FILE") != "" { _ = exec.Command("atl", "jira", "fields").Run(); fmt.Println("{\"type\":\"item.completed\",\"item\":{\"type\":\"command_execution\"}}") } else { fmt.Println("{\"type\":\"item.completed\",\"item\":{\"type\":\"mcp_tool_call\",\"server\":\"atl\",\"tool\":\"jira_fields\",\"status\":\"completed\",\"result\":{\"fields\":[]}}}") }
  fmt.Println("{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":100,\"output_tokens\":20}}")
  _ = os.WriteFile(final, []byte("{\"complete\":true}\n"), 0600)
  if data, err := os.ReadFile(mutationControl); err == nil { target := strings.TrimSpace(string(data)); if target == "SNAPSHOT" { target = filepath.Join(filepath.Dir(os.Args[0]), "..", "plugin", "plugins", "atl", "skills", "atl", "SKILL.md") }; appendFile(target, "changed\n") }
}
`, agent+".calls", mutationControl, failAgent)
	writeTestFile(t, agentSource, agentProgram, 0o600)
	buildAgent := exec.Command("go", "build", "-buildvcs=false", "-o", agent, agentSource)
	if output, err := buildAgent.CombinedOutput(); err != nil {
		t.Fatalf("build native agent fixture: %v: %s", err, output)
	}
	atl := filepath.Join(binRoot, "fake-atl")
	writeTestFile(t, atl, "#!/bin/sh\n"+fmt.Sprintf("printf '%%s\\n' x >>%q\n", atl+".calls")+`if [ "$1" = "version" ]; then printf '%s\n' '{"version":"test","commit":"synthetic","build_state":"clean"}'; exit 0; fi
if [ "$1" = "jira" ] && [ "$2" = "fields" ]; then printf '%s\n' '{"fields":[]}'; exit 0; fi
exit 2
`, 0o700)
	wrapper := filepath.Join(binRoot, "fake-wrapper")
	writeTestFile(t, wrapper, "#!/bin/sh\nexit 0\n", 0o700)

	return privatePlanTestFixture{root: root, repository: repository, liveConfig: liveConfig, pluginRoot: pluginRoot,
		agent: agent, atl: atl, wrapper: wrapper, mutationControl: mutationControl,
		runSetAlias: "portfolio", now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
}

func setPrivatePlanTestPanel(t *testing.T, fixture privatePlanTestFixture, panel *PrivateQualitativeReviewPanel) {
	t.Helper()
	if err := panel.validate(); err != nil {
		t.Fatalf("invalid test panel: %v", err)
	}
	setPrivatePlanTestPanelRaw(t, fixture, panel)
}

func setPrivatePlanTestPanelRaw(t *testing.T, fixture privatePlanTestFixture, panel *PrivateQualitativeReviewPanel) {
	t.Helper()
	manifestPath := filepath.Join(fixture.root, PrivateWorkspaceManifestName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	manifest.RunSets[0].QualitativeReviewRequired = false
	manifest.RunSets[0].QualitativeReviewPanel = panel
	if err := panel.validate(); err != nil {
		data, marshalErr := json.MarshalIndent(manifest, "", "  ")
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := writePrivateFile(manifestPath, append(data, '\n')); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err = EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(manifestPath, data); err != nil {
		t.Fatal(err)
	}
}

func (fixture privatePlanTestFixture) createOptions() PrivatePlanCreateOptions {
	return PrivatePlanCreateOptions{Root: fixture.root, RepositoryRoot: fixture.repository, RunSetAlias: fixture.runSetAlias,
		ATLBinary: fixture.atl, PluginRoot: fixture.pluginRoot, AgentBinary: fixture.agent, WrapperExecutable: fixture.wrapper,
		Consent: PrivatePlanConsent{ExpiresAt: fixture.now.Add(time.Hour).Format(time.RFC3339), ProviderDataApproved: true},
		Confirm: PrivatePlanConsentConfirmation, Now: fixture.now}
}

func (fixture privatePlanTestFixture) createPlan(t *testing.T) PrivatePlanPreview {
	t.Helper()
	preview, err := CreatePrivatePlan(context.Background(), fixture.createOptions())
	if err != nil {
		t.Fatalf("create plan: %v; workspace=%+v", err, InspectPrivateWorkspace(fixture.root, fixture.repository))
	}
	return preview
}

func (fixture privatePlanTestFixture) executeOptions(preview PrivatePlanPreview) PrivatePlanExecuteOptions {
	return PrivatePlanExecuteOptions{Root: fixture.root, RepositoryRoot: fixture.repository, PlanID: preview.PlanID,
		ExpectedPlanSHA256: preview.PlanSHA256, Confirm: PrivatePlanConfirmation, ATLBinary: fixture.atl,
		PluginRoot: fixture.pluginRoot, AgentBinary: fixture.agent, WrapperExecutable: fixture.wrapper, Now: fixture.now}
}

func assertPrivatePlanError(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, ErrPrivatePlanRejected) || !strings.Contains(err.Error(), ": "+code) {
		t.Fatalf("error=%v, want private-plan code %q", err, code)
	}
}

func assertPrivatePlanTextSafe(t *testing.T, text string, fixture privatePlanTestFixture) {
	t.Helper()
	for _, forbidden := range []string{privatePlanTestSecret, fixture.root, fixture.liveConfig, fixture.repository, "synthetic-token"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("private-plan output leaked private material %q: %s", forbidden, text)
		}
	}
}

func appendPrivatePlanTestFile(t *testing.T, path, suffix string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(suffix); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertPrivatePlanInvocationCount(t *testing.T, agent string, want int) {
	t.Helper()
	data, err := os.ReadFile(agent + ".calls")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(bytes.Fields(data)); got != want {
		t.Fatalf("agent invocations=%d want=%d", got, want)
	}
}

func assertPrivatePlanNoRuntimeInvocation(t *testing.T, fixture privatePlanTestFixture) {
	t.Helper()
	for name, binary := range map[string]string{"provider": fixture.agent, "atl": fixture.atl} {
		if _, err := os.Stat(binary + ".calls"); !os.IsNotExist(err) {
			t.Fatalf("%s runtime invoked before plan authorization: %v", name, err)
		}
	}
}

func readPrivatePlanTestJSON(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func replacePrivatePlanTestString(values []string, old, replacement string) []string {
	out := append([]string(nil), values...)
	for index := range out {
		if out[index] == old {
			out[index] = replacement
		}
	}
	return out
}

func removePrivatePlanTestString(values []string, remove string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != remove {
			out = append(out, value)
		}
	}
	return out
}
