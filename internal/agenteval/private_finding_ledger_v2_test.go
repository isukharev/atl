package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateFindingScorecardReconcilesSyntheticOnlyFixedChain(t *testing.T) {
	fixture, ledger, acceptance := newSyntheticOnlyFindingFixture(t)
	writePrivateFindingLedgerV2(t, fixture.root, ledger)
	writePrivateFindingAcceptanceV2(t, fixture.root, acceptance)

	before := privateCheckpointTree(t, fixture.root)
	report, err := buildPrivateFindingScorecard(
		PrivateFindingScorecardOptions{Root: fixture.root, RepositoryRoot: fixture.repository},
		fixture.dependencies().load,
	)
	if err != nil {
		t.Fatal(err)
	}
	after := privateCheckpointTree(t, fixture.root)
	if !bytes.Equal(before, after) {
		t.Fatal("read-only scorecard changed the workspace")
	}
	if report.SchemaVersion != PrivateFindingScorecardSchemaVersion ||
		report.LedgerSchemaVersion != PrivateFindingLedgerV2SchemaVersion ||
		!report.Reconciled || report.Findings != 1 || report.LinkedIssues != 2 ||
		report.LinkedPullRequests != 2 || report.Regressions != 1 ||
		report.SamplingAssessments != 1 || report.Decisions.Fixed != 1 ||
		len(report.Groups) != 1 {
		t.Fatalf("report=%+v", report)
	}
	group := report.Groups[0]
	if group.Failure.Statuses.Fail != 1 || group.Regression.Observed != 3 ||
		group.Regression.Statuses.Pass != 3 || group.Sampling.Primary.Observed != 3 ||
		group.Sampling.Primary.Statuses.Pass != 3 || group.Sampling.Holdout.Observed != 1 ||
		group.Sampling.Holdout.Statuses.Pass != 1 {
		t.Fatalf("group=%+v", group)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		ledger.Entries[0].FindingID,
		ledger.Entries[0].Failure.AssessmentSHA256,
		ledger.Entries[0].Regression.AssessmentSHA256,
		acceptance.Entries[0].PromptContractSHA256,
		"finding-synthetic-001",
	} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("private value %q leaked in %s", private, encoded)
		}
	}
}

func TestPrivateFindingScorecardAcceptsPrivateLiveLedgerV2(t *testing.T) {
	fixture := newPrivateFindingFixture(t)
	planID := "pln-11111111111111111111111111111111"
	failure := privateFindingTestResult(t, false)
	fixture.addSource(t, planID, "", failure)
	writePrivateFindingLedgerV2(t, fixture.root, PrivateFindingLedgerV2{
		SchemaVersion: PrivateFindingLedgerV2SchemaVersion,
		Entries: []PrivateFindingEntryV2{{
			FindingID: "finding-private-live-001",
			Failure: PrivateFindingEvidenceRef{
				Source: PrivateFindingAcceptanceSourcePrivateLive,
				PlanID: planID, Surface: SurfaceCLISkill, Baseline: "captured",
			},
			FailureClass: PrivateFailureModel, ProductIssues: []int{101},
			Decision: PrivateFindingDecisionInvestigate,
		}},
	})
	report, err := buildPrivateFindingScorecard(fixture.options(), fixture.load)
	if err != nil {
		t.Fatal(err)
	}
	if report.LedgerSchemaVersion != PrivateFindingLedgerV2SchemaVersion ||
		report.Findings != 1 || report.Decisions.Investigate != 1 ||
		len(report.Groups) != 1 || report.Groups[0].Failure.Statuses.Fail != 1 {
		t.Fatalf("report=%+v", report)
	}
}

func TestPrivateFindingScorecardPreservesLedgerV1RegressionReuse(t *testing.T) {
	fixture := newPrivateFindingFixture(t)
	regressionID := "pln-11111111111111111111111111111111"
	firstFailureID := "pln-22222222222222222222222222222222"
	secondFailureID := "pln-33333333333333333333333333333333"
	fixture.addSource(t, regressionID, "", privateFindingTestResult(t, true))
	fixture.addSource(t, firstFailureID, "", privateFindingTestResult(t, false))
	fixture.addSource(t, secondFailureID, "", privateFindingTestResult(t, false))
	regression := PrivateFindingRunRef{
		PlanID: regressionID, Surface: SurfaceCLISkill, Baseline: "captured",
	}
	fixture.writeLedger(t, PrivateFindingLedger{
		SchemaVersion: PrivateFindingLedgerSchemaVersion,
		Entries: []PrivateFindingEntry{
			{
				FindingID: "finding-001",
				Failure: PrivateFindingRunRef{
					PlanID: firstFailureID, Surface: SurfaceCLISkill, Baseline: "captured",
				},
				FailureClass: PrivateFailureModel, ProductIssue: 101,
				Regression: &regression, Decision: PrivateFindingDecisionAccepted,
			},
			{
				FindingID: "finding-002",
				Failure: PrivateFindingRunRef{
					PlanID: secondFailureID, Surface: SurfaceCLISkill, Baseline: "captured",
				},
				FailureClass: PrivateFailureModel, ProductIssue: 102,
				Regression: &regression, Decision: PrivateFindingDecisionAccepted,
			},
		},
	})
	report, err := buildPrivateFindingScorecard(fixture.options(), fixture.load)
	if err != nil {
		t.Fatal(err)
	}
	if report.LedgerSchemaVersion != PrivateFindingLedgerSchemaVersion ||
		report.Findings != 2 || report.Regressions != 2 ||
		report.Decisions.Accepted != 2 || report.Groups[0].Regression.Observed != 2 {
		t.Fatalf("report=%+v", report)
	}
}

func TestPrivateFindingScorecardRejectsInvalidSyntheticOnlyChains(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PrivateFindingLedgerV2, *PrivateFindingAcceptanceV2Index)
	}{
		{
			name: "same assessment",
			mutate: func(ledger *PrivateFindingLedgerV2, _ *PrivateFindingAcceptanceV2Index) {
				ledger.Entries[0].Regression.AssessmentSHA256 = ledger.Entries[0].Failure.AssessmentSHA256
			},
		},
		{
			name: "missing transition",
			mutate: func(ledger *PrivateFindingLedgerV2, _ *PrivateFindingAcceptanceV2Index) {
				ledger.Entries[0].ChangedContracts = ledger.Entries[0].ChangedContracts[1:]
			},
		},
		{
			name: "extra transition",
			mutate: func(ledger *PrivateFindingLedgerV2, _ *PrivateFindingAcceptanceV2Index) {
				ledger.Entries[0].ChangedContracts = append(ledger.Entries[0].ChangedContracts,
					PrivateFindingContractTransition{
						Kind: PrivateFindingContractSkill, BeforeSHA256: strings.Repeat("7", 64),
						AfterSHA256: strings.Repeat("8", 64),
					})
			},
		},
		{
			name: "wrong regression acceptance",
			mutate: func(_ *PrivateFindingLedgerV2, acceptance *PrivateFindingAcceptanceV2Index) {
				acceptance.Entries[0].AssessmentSHA256 = strings.Repeat("a", 64)
			},
		},
		{
			name: "wrong prompt acceptance",
			mutate: func(_ *PrivateFindingLedgerV2, acceptance *PrivateFindingAcceptanceV2Index) {
				acceptance.Entries[0].PromptContractSHA256 = strings.Repeat("a", 64)
			},
		},
		{
			name: "wrong acceptance source",
			mutate: func(_ *PrivateFindingLedgerV2, acceptance *PrivateFindingAcceptanceV2Index) {
				acceptance.Entries[0].AssessmentSource = PrivateFindingAcceptanceSourcePrivateLive
				acceptance.Entries[0].PromptContractSHA256 = ""
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, ledger, acceptance := newSyntheticOnlyFindingFixture(t)
			test.mutate(&ledger, &acceptance)
			writePrivateFindingLedgerV2(t, fixture.root, ledger)
			writePrivateFindingAcceptanceV2(t, fixture.root, acceptance)
			if _, err := buildPrivateFindingScorecard(
				PrivateFindingScorecardOptions{Root: fixture.root, RepositoryRoot: fixture.repository},
				fixture.dependencies().load,
			); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestPrivateFindingSyntheticTransitionRejectsUnattestedIdentityDrift(t *testing.T) {
	base := privateSyntheticSamplingCohort{
		ScenarioID: "jira.primary-evidence", TaskClass: "jira/evidence",
		DataClass: "synthetic", Category: BenchmarkCategoryRouteFixed,
		Variant: "summary-v1", Surface: SurfaceCLISkill,
		Runtime: Runtime{
			Provider: "codex", AgentVersion: "agent-v1", Model: "model-v1",
			Reasoning: "high", ATLVersion: "atl-v1", PluginVersion: "plugin-v1",
			SkillDigest: strings.Repeat("1", 64), SkillActivation: "exact",
			PromptContractSHA256: strings.Repeat("2", 64),
		},
		TaskContractSHA256: strings.Repeat("3", 64), ExecutionContractSHA256: strings.Repeat("4", 64),
		AgentExecutableSHA256: strings.Repeat("5", 64), ATLExecutableSHA256: strings.Repeat("6", 64),
		WrapperExecutableSHA256: strings.Repeat("7", 64),
	}
	tests := []struct {
		name   string
		mutate func(*privateSyntheticSamplingCohort)
	}{
		{"scenario", func(value *privateSyntheticSamplingCohort) { value.ScenarioID = "jira.other-evidence" }},
		{"task class", func(value *privateSyntheticSamplingCohort) { value.TaskClass = "jira/history" }},
		{"data class", func(value *privateSyntheticSamplingCohort) { value.DataClass = "private-local" }},
		{"category", func(value *privateSyntheticSamplingCohort) { value.Category = BenchmarkCategoryNeutralCommon }},
		{"surface", func(value *privateSyntheticSamplingCohort) { value.Surface = SurfaceATLMCP }},
		{"provider", func(value *privateSyntheticSamplingCohort) { value.Runtime.Provider = "claude-code" }},
		{"agent version", func(value *privateSyntheticSamplingCohort) { value.Runtime.AgentVersion = "agent-v2" }},
		{"model", func(value *privateSyntheticSamplingCohort) { value.Runtime.Model = "model-v2" }},
		{"reasoning", func(value *privateSyntheticSamplingCohort) { value.Runtime.Reasoning = "medium" }},
		{"plugin", func(value *privateSyntheticSamplingCohort) { value.Runtime.PluginVersion = "plugin-v2" }},
		{"skill activation", func(value *privateSyntheticSamplingCohort) { value.Runtime.SkillActivation = "broad" }},
		{"agent executable", func(value *privateSyntheticSamplingCohort) { value.AgentExecutableSHA256 = strings.Repeat("8", 64) }},
		{"wrapper executable", func(value *privateSyntheticSamplingCohort) { value.WrapperExecutableSHA256 = strings.Repeat("8", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			regression := base
			test.mutate(&regression)
			if compatiblePrivateSyntheticFindingTransition(base, regression, nil) {
				t.Fatal("unattested identity drift accepted")
			}
		})
	}
}

func TestPrivateFindingSyntheticTransitionAllowsExactAttestedVariantAndBinaryChange(t *testing.T) {
	failure := privateSyntheticSamplingCohort{
		ScenarioID: "jira.primary-evidence", TaskClass: "jira/evidence",
		DataClass: "synthetic", Category: BenchmarkCategoryRouteFixed,
		Variant: "summary-old", Surface: SurfaceCLISkill,
		Runtime: Runtime{
			Provider: "codex", AgentVersion: "agent-v1", Model: "model-v1",
			Reasoning: "high", ATLVersion: "atl-old", PluginVersion: "plugin-v1",
			SkillDigest: "sha256:" + strings.Repeat("1", 64), SkillActivation: "exact",
			PromptContractSHA256: strings.Repeat("2", 64),
		},
		TaskContractSHA256: strings.Repeat("3", 64), ExecutionContractSHA256: strings.Repeat("4", 64),
		AgentExecutableSHA256: strings.Repeat("5", 64), ATLExecutableSHA256: strings.Repeat("6", 64),
		WrapperExecutableSHA256: strings.Repeat("7", 64),
	}
	regression := failure
	regression.Variant = "summary-v1"
	regression.Runtime.ATLVersion = "atl-v2"
	regression.Runtime.PromptContractSHA256 = strings.Repeat("8", 64)
	regression.Runtime.SkillDigest = "sha256:" + strings.Repeat("9", 64)
	regression.TaskContractSHA256 = strings.Repeat("a", 64)
	regression.ExecutionContractSHA256 = strings.Repeat("b", 64)
	regression.ATLExecutableSHA256 = strings.Repeat("c", 64)
	regression.WrapperExecutableSHA256 = strings.Repeat("d", 64)
	transitions := []PrivateFindingContractTransition{
		{Kind: PrivateFindingContractATLBinary, BeforeSHA256: failure.ATLExecutableSHA256, AfterSHA256: regression.ATLExecutableSHA256},
		{Kind: PrivateFindingContractExecution, BeforeSHA256: failure.ExecutionContractSHA256, AfterSHA256: regression.ExecutionContractSHA256},
		{Kind: PrivateFindingContractPrompt, BeforeSHA256: failure.Runtime.PromptContractSHA256, AfterSHA256: regression.Runtime.PromptContractSHA256},
		{Kind: PrivateFindingContractRunner, BeforeSHA256: failure.WrapperExecutableSHA256, AfterSHA256: regression.WrapperExecutableSHA256},
		{Kind: PrivateFindingContractSkill, BeforeSHA256: strings.Repeat("1", 64), AfterSHA256: strings.Repeat("9", 64)},
		{Kind: PrivateFindingContractTask, BeforeSHA256: failure.TaskContractSHA256, AfterSHA256: regression.TaskContractSHA256},
	}
	if !compatiblePrivateSyntheticFindingTransition(failure, regression, transitions) {
		t.Fatal("exact attested transition rejected")
	}
}

func TestPrivateFindingSyntheticSkillTransitionFailsClosed(t *testing.T) {
	failure := privateSyntheticSamplingCohort{
		ScenarioID: "jira.primary-evidence", TaskClass: "jira/evidence",
		DataClass: "synthetic", Category: BenchmarkCategoryRouteFixed,
		Variant: "summary-v1", Surface: SurfaceCLISkill,
		Runtime: Runtime{
			Provider: "codex", AgentVersion: "agent-v1", Model: "model-v1",
			Reasoning: "high", ATLVersion: "atl-v1", PluginVersion: "plugin-v1",
			SkillDigest: "sha256:" + strings.Repeat("1", 64), SkillActivation: "exact",
			PromptContractSHA256: strings.Repeat("2", 64),
		},
		TaskContractSHA256: strings.Repeat("3", 64), ExecutionContractSHA256: strings.Repeat("4", 64),
		AgentExecutableSHA256: strings.Repeat("5", 64), ATLExecutableSHA256: strings.Repeat("6", 64),
		WrapperExecutableSHA256: strings.Repeat("7", 64),
	}
	regression := failure
	regression.Runtime.SkillDigest = "sha256:" + strings.Repeat("8", 64)
	regression.ExecutionContractSHA256 = strings.Repeat("9", 64)
	execution := PrivateFindingContractTransition{
		Kind:         PrivateFindingContractExecution,
		BeforeSHA256: failure.ExecutionContractSHA256,
		AfterSHA256:  regression.ExecutionContractSHA256,
	}
	skill := PrivateFindingContractTransition{
		Kind:         PrivateFindingContractSkill,
		BeforeSHA256: strings.Repeat("1", 64),
		AfterSHA256:  strings.Repeat("8", 64),
	}
	if !compatiblePrivateSyntheticFindingTransition(
		failure, regression, []PrivateFindingContractTransition{execution, skill},
	) {
		t.Fatal("exact canonical skill transition rejected")
	}
	if compatiblePrivateSyntheticFindingTransition(
		failure, regression, []PrivateFindingContractTransition{execution},
	) {
		t.Fatal("undeclared skill transition accepted")
	}
	wrong := skill
	wrong.AfterSHA256 = strings.Repeat("a", 64)
	if compatiblePrivateSyntheticFindingTransition(
		failure, regression, []PrivateFindingContractTransition{execution, wrong},
	) {
		t.Fatal("incorrect skill transition accepted")
	}
	malformed := regression
	malformed.Runtime.SkillDigest = "sha256:not-a-digest"
	if compatiblePrivateSyntheticFindingTransition(
		failure, malformed, []PrivateFindingContractTransition{execution, skill},
	) {
		t.Fatal("malformed runtime skill digest accepted")
	}
}

func TestPrivateFindingSyntheticRunnerTransitionRequiresExecutionUmbrella(t *testing.T) {
	failure := privateSyntheticSamplingCohort{
		ScenarioID: "jira.primary-evidence", TaskClass: "jira/evidence",
		DataClass: "synthetic", Category: BenchmarkCategoryRouteFixed,
		Variant: "summary-v1", Surface: SurfaceCLISkill,
		Runtime: Runtime{
			Provider: "codex", AgentVersion: "agent-v1", Model: "model-v1",
			Reasoning: "high", ATLVersion: "atl-v1", PluginVersion: "plugin-v1",
			SkillDigest: strings.Repeat("1", 64), SkillActivation: "exact",
			PromptContractSHA256: strings.Repeat("2", 64),
		},
		TaskContractSHA256: strings.Repeat("3", 64), ExecutionContractSHA256: strings.Repeat("4", 64),
		AgentExecutableSHA256: strings.Repeat("5", 64), ATLExecutableSHA256: strings.Repeat("6", 64),
		WrapperExecutableSHA256: strings.Repeat("7", 64),
	}
	regression := failure
	regression.ExecutionContractSHA256 = strings.Repeat("8", 64)
	regression.WrapperExecutableSHA256 = strings.Repeat("9", 64)
	execution := PrivateFindingContractTransition{
		Kind:         PrivateFindingContractExecution,
		BeforeSHA256: failure.ExecutionContractSHA256,
		AfterSHA256:  regression.ExecutionContractSHA256,
	}
	runner := PrivateFindingContractTransition{
		Kind:         PrivateFindingContractRunner,
		BeforeSHA256: failure.WrapperExecutableSHA256,
		AfterSHA256:  regression.WrapperExecutableSHA256,
	}
	if !compatiblePrivateSyntheticFindingTransition(
		failure, regression, []PrivateFindingContractTransition{execution, runner},
	) {
		t.Fatal("exact runner and execution transitions rejected")
	}
	runnerOnlyRegression := failure
	runnerOnlyRegression.WrapperExecutableSHA256 = regression.WrapperExecutableSHA256
	if compatiblePrivateSyntheticFindingTransition(
		failure, runnerOnlyRegression, []PrivateFindingContractTransition{runner},
	) {
		t.Fatal("runner transition accepted without execution umbrella")
	}
}

func TestPrivateFindingLedgerVersionSelectionFailsClosed(t *testing.T) {
	fixture, ledger, _ := newSyntheticOnlyFindingFixture(t)
	writePrivateFindingLedgerV2(t, fixture.root, ledger)
	writePrivateFindingLedger(t, fixture.root, PrivateFindingLedger{
		SchemaVersion: PrivateFindingLedgerSchemaVersion,
		Entries: []PrivateFindingEntry{{
			FindingID: "legacy-finding", Failure: PrivateFindingRunRef{
				PlanID:  "pln-11111111111111111111111111111111",
				Surface: SurfaceCLISkill, Baseline: "captured",
			},
			FailureClass: PrivateFailureModel, ProductIssue: 1,
			Decision: PrivateFindingDecisionInvestigate,
		}},
	})
	if _, err := loadPrivateFindingLedger(fixture.root); !errors.Is(err, ErrPrivateFindingLedgerRejected) {
		t.Fatalf("err=%v", err)
	}
}

func TestPrivateFindingLedgerV2RejectsConcurrentCreation(t *testing.T) {
	fixture, ledger, _ := newSyntheticOnlyFindingFixture(t)
	writePrivateFindingLedgerV2(t, fixture.root, ledger)
	_, _, err := readPrivateFindingLedgerWithHook(fixture.root, func() {
		writePrivateFindingLedger(t, fixture.root, PrivateFindingLedger{
			SchemaVersion: PrivateFindingLedgerSchemaVersion,
			Entries: []PrivateFindingEntry{{
				FindingID: "legacy-finding", Failure: PrivateFindingRunRef{
					PlanID:  "pln-11111111111111111111111111111111",
					Surface: SurfaceCLISkill, Baseline: "captured",
				},
				FailureClass: PrivateFailureModel, ProductIssue: 1,
				Decision: PrivateFindingDecisionInvestigate,
			}},
		})
	})
	if !errors.Is(err, ErrPrivateFindingLedgerRejected) {
		t.Fatalf("err=%v", err)
	}
}

func TestPrivateFindingLedgerV2PublicContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(
		"..", "..", "benchmarks", "agent-eval", "private-finding-ledger-v2.example.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	ledger, canonical, err := decodePrivateFindingLedgerV2(data)
	if err != nil || ledger.SchemaVersion != PrivateFindingLedgerV2SchemaVersion ||
		len(ledger.Entries) != 1 || !bytes.Equal(data, canonical) {
		t.Fatalf("ledger=%+v canonical=%t err=%v", ledger, bytes.Equal(data, canonical), err)
	}
	var schema any
	schemaData, err := os.ReadFile(filepath.Join(
		"..", "..", "benchmarks", "agent-eval", "private-finding-ledger-v2.schema.json",
	))
	if err != nil || json.Unmarshal(schemaData, &schema) != nil {
		t.Fatalf("public schema is invalid JSON: %v", err)
	}
}

func newSyntheticOnlyFindingFixture(
	t *testing.T,
) (*privateSamplingFixture, PrivateFindingLedgerV2, PrivateFindingAcceptanceV2Index) {
	t.Helper()
	fixture := newPrivateSamplingFixture(t)
	failureResult := privateSamplingResult(t, "jira.primary-evidence", false)
	failureResult.Runtime.Provider = "codex"
	regressionResult := privateSamplingResult(t, "jira.primary-evidence", true)
	regressionResult.Runtime.Provider = "codex"

	failureRoot := addSyntheticFindingRoot(
		t, fixture, "failure-finding-synthetic-runs", failureResult,
		failureResult.ScenarioID, 1, strings.Repeat("1", 64),
		strings.Repeat("2", 64), strings.Repeat("3", 64),
	)
	failureAssessment := fixture.storeSyntheticAssessment(t, PrivateSyntheticSamplingSpec{
		SchemaVersion: PrivateSyntheticSamplingSchemaVersion,
		Tier:          PrivateSamplingTierCalibration,
		Primary:       failureRoot,
	})
	primary := addSyntheticFindingRoot(
		t, fixture, "primary-finding-synthetic-runs", regressionResult,
		regressionResult.ScenarioID, 3, strings.Repeat("4", 64),
		strings.Repeat("5", 64), strings.Repeat("6", 64),
	)
	holdout := addSyntheticFindingRoot(
		t, fixture, "holdout-finding-synthetic-runs", regressionResult,
		"jira.holdout-evidence", 1, strings.Repeat("7", 64),
		strings.Repeat("8", 64), strings.Repeat("9", 64),
	)
	regressionAssessment := fixture.storeSyntheticAssessment(t, PrivateSyntheticSamplingSpec{
		SchemaVersion: PrivateSyntheticSamplingSchemaVersion,
		Tier:          PrivateSamplingTierRegression,
		Primary:       primary,
		Holdout:       []PrivateSyntheticSamplingRootRef{holdout},
	})
	ledger := PrivateFindingLedgerV2{
		SchemaVersion: PrivateFindingLedgerV2SchemaVersion,
		Entries: []PrivateFindingEntryV2{{
			FindingID: "finding-synthetic-001",
			Failure: PrivateFindingEvidenceRef{
				Source: PrivateFindingAcceptanceSourceSyntheticRoot, AssessmentSHA256: failureAssessment,
			},
			FailureClass:  PrivateFailureHarness,
			ProductIssues: []int{101, 102},
			PullRequests:  []int{201, 202},
			ChangedContracts: []PrivateFindingContractTransition{
				{Kind: PrivateFindingContractExecution, BeforeSHA256: strings.Repeat("2", 64), AfterSHA256: strings.Repeat("5", 64)},
				{Kind: PrivateFindingContractPrompt, BeforeSHA256: strings.Repeat("3", 64), AfterSHA256: strings.Repeat("6", 64)},
				{Kind: PrivateFindingContractTask, BeforeSHA256: strings.Repeat("1", 64), AfterSHA256: strings.Repeat("4", 64)},
			},
			Regression: &PrivateFindingEvidenceRef{
				Source: PrivateFindingAcceptanceSourceSyntheticRoot, AssessmentSHA256: regressionAssessment,
			},
			Decision: PrivateFindingDecisionFixed,
		}},
	}
	acceptance := PrivateFindingAcceptanceV2Index{
		SchemaVersion: PrivateFindingAcceptanceV2SchemaVersion,
		Entries: []PrivateFindingAcceptanceV2Entry{{
			FindingID:            ledger.Entries[0].FindingID,
			AssessmentSHA256:     regressionAssessment,
			AssessmentSource:     PrivateFindingAcceptanceSourceSyntheticRoot,
			PromptContractSHA256: strings.Repeat("6", 64),
		}},
	}
	return fixture, ledger, acceptance
}

func writePrivateFindingLedgerV2(t *testing.T, root string, ledger PrivateFindingLedgerV2) {
	t.Helper()
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, PrivateFindingLedgerV2RelativePath)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}
