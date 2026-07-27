package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
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

func TestPrivateFindingScorecardReconcilesFailureAgainstRegressionHoldout(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	failureResult := privateSamplingResult(t, "jira.holdout-evidence", false)
	failureResult.Runtime.Provider = "codex"
	regressionResult := privateSamplingResult(t, "jira.primary-evidence", true)
	regressionResult.Runtime.Provider = "codex"

	failureRoot := addSyntheticFindingRoot(
		t, fixture, "failure-holdout-synthetic-runs", failureResult,
		"jira.holdout-evidence", 1, strings.Repeat("1", 64),
		strings.Repeat("2", 64), strings.Repeat("3", 64),
	)
	failureAssessment := fixture.storeSyntheticAssessment(t, PrivateSyntheticSamplingSpec{
		SchemaVersion: PrivateSyntheticSamplingSchemaVersion,
		Tier:          PrivateSamplingTierCalibration,
		Primary:       failureRoot,
	})
	primary := addSyntheticFindingRoot(
		t, fixture, "primary-fixed-synthetic-runs", regressionResult,
		"jira.primary-evidence", 3, strings.Repeat("4", 64),
		strings.Repeat("5", 64), strings.Repeat("6", 64),
	)
	holdout := addSyntheticFindingRoot(
		t, fixture, "holdout-fixed-synthetic-runs", regressionResult,
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
			FindingID: "finding-holdout-001",
			Failure: PrivateFindingEvidenceRef{
				Source: PrivateFindingAcceptanceSourceSyntheticRoot, AssessmentSHA256: failureAssessment,
			},
			FailureClass:  PrivateFailureHarness,
			ProductIssues: []int{101},
			PullRequests:  []int{201},
			ChangedContracts: []PrivateFindingContractTransition{
				{Kind: PrivateFindingContractExecution, BeforeSHA256: strings.Repeat("2", 64), AfterSHA256: strings.Repeat("8", 64)},
				{Kind: PrivateFindingContractPrompt, BeforeSHA256: strings.Repeat("3", 64), AfterSHA256: strings.Repeat("9", 64)},
				{Kind: PrivateFindingContractTask, BeforeSHA256: strings.Repeat("1", 64), AfterSHA256: strings.Repeat("7", 64)},
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
	writePrivateFindingLedgerV2(t, fixture.root, ledger)
	writePrivateFindingAcceptanceV2(t, fixture.root, acceptance)

	report, err := buildPrivateFindingScorecard(
		PrivateFindingScorecardOptions{Root: fixture.root, RepositoryRoot: fixture.repository},
		fixture.dependencies().load,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Findings != 1 || report.Regressions != 1 || len(report.Groups) != 1 ||
		report.Groups[0].Regression.Observed != 1 || report.Groups[0].Regression.Statuses.Pass != 1 ||
		report.Groups[0].Sampling.Primary.Observed != 3 ||
		report.Groups[0].Sampling.Holdout.Observed != 1 {
		t.Fatalf("report=%+v", report)
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

func TestPrivateFindingSyntheticRegressionMatchFailsClosed(t *testing.T) {
	failure := privateSyntheticSamplingCohort{
		ScenarioID: "jira.holdout-evidence", TaskClass: "jira/evidence",
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
	fixed := failure
	assessment := privateSyntheticSamplingAssessment{
		Primary: privateSyntheticSamplingBinding{Cohort: fixed, Observations: 3},
		Holdout: []privateSyntheticSamplingBinding{{Cohort: fixed, Observations: 1}},
	}
	primary := []Result{{Status: "pass"}, {Status: "pass"}, {Status: "pass"}}
	holdout := []Result{{Status: "pass"}}
	if _, ok := matchPrivateSyntheticFindingRegression(failure, assessment, primary, holdout, nil); ok {
		t.Fatal("ambiguous primary and holdout match accepted")
	}
	assessment.Primary.Cohort.ScenarioID = "jira.primary-evidence"
	assessment.Holdout[0].Cohort.ScenarioID = "jira.other-holdout"
	if _, ok := matchPrivateSyntheticFindingRegression(failure, assessment, primary, holdout, nil); ok {
		t.Fatal("missing compatible cohort accepted")
	}
	assessment.Holdout[0].Cohort = fixed
	assessment.Holdout[0].Observations = 2
	if _, ok := matchPrivateSyntheticFindingRegression(failure, assessment, primary, holdout, nil); ok {
		t.Fatal("inconsistent holdout observation count accepted")
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
	_, err := loadPrivateFindingLedger(fixture.root)
	assertPrivateFindingCode(t, err, "ledger_ambiguous")
	// Both candidates probed cleanly; the pair itself is the rejection.
	if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
		t.Fatalf("causes=%v, want an ambiguity-only rejection", causes)
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
	assertPrivateFindingCode(t, err, "ledger_file")
	// The window closes on an inventory comparison, not on a failed probe.
	if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
		t.Fatalf("causes=%v, want an inventory-comparison rejection", causes)
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

func TestPrivateFindingErrorKeepsCausesInspectableAndOutOfTheMessage(t *testing.T) {
	privatePath := filepath.Join("private", "reports", "finding-ledger.v2.json")
	statCause := &fs.PathError{Op: "statat", Path: privatePath, Err: fs.ErrPermission}
	secondCause := errors.New("synthetic close failure")

	err := privateFindingError("ledger_file", statCause, nil, secondCause)
	assertPrivateFindingCode(t, err, "ledger_file")
	if strings.Contains(err.Error(), privatePath) || strings.Contains(err.Error(), secondCause.Error()) {
		t.Fatalf("message leaked a cause: %q", err.Error())
	}
	if !errors.Is(err, fs.ErrPermission) || !errors.Is(err, secondCause) {
		t.Fatalf("error %v lost a cause", err)
	}
	var typed *fs.PathError
	if !errors.As(err, &typed) || typed.Path != statCause.Path {
		t.Fatalf("error %v does not expose the concrete stat failure", err)
	}
	// The file recheck supplies final-stat then close, and the directory
	// recheck supplies final-handle then ambient. That fixed order is pinned
	// here because neither branch can be driven into a both-failed state
	// without racing the reader.
	causes := privateFindingErrorCauses(t, err)
	if len(causes) != 2 || causes[0] != error(statCause) || causes[1] != secondCause {
		t.Fatalf("causes=%v, want both non-nil causes retained in order", causes)
	}

	// A rejection with nothing in hand classifies exactly as it did before.
	assertPrivateFindingCode(t, privateFindingError("ledger_ambiguous"), "ledger_ambiguous")
	if causes := privateFindingErrorCauses(t, privateFindingError("ledger_file", nil, nil)); len(causes) != 0 {
		t.Fatalf("causes=%v, want nil causes dropped", causes)
	}

	// The candidate probe classifies under this same shared constructor and is
	// returned unchanged by the reader, so a nested classification has to stay
	// reachable below the outer code.
	nested := privateFindingError("ledger_file")
	outer := privateFindingError("ledger_contract", nested)
	assertPrivateFindingCode(t, outer, "ledger_contract")
	var classified interface{ Code() string }
	if !errors.As(outer, &classified) || classified.Code() != "ledger_contract" {
		t.Fatalf("error %v does not report the outer ledger code", outer)
	}
	if inner := privateFindingErrorCauses(t, outer); len(inner) != 1 || inner[0] != nested {
		t.Fatalf("causes=%v, want the nested classification retained", inner)
	}
}

func TestPrivateFindingLedgerAttachesDirectoryProbeCauses(t *testing.T) {
	t.Run("absent reports directory", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		if err := os.RemoveAll(filepath.Join(fixture.root, "reports")); err != nil {
			t.Fatal(err)
		}
		_, _, err := readPrivateFindingLedger(fixture.root)
		assertPrivateFindingCode(t, err, "ledger_directory")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the directory probe failure retained", causes)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error %v does not expose the concrete probe failure", err)
		}
		var typed *fs.PathError
		if !errors.As(err, &typed) {
			t.Fatalf("error %v does not expose a path failure", err)
		}
		if strings.Contains(err.Error(), fixture.root) {
			t.Fatalf("message leaked the workspace root: %q", err.Error())
		}
	})

	t.Run("loose reports permission", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		if err := os.Chmod(filepath.Join(fixture.root, "reports"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, _, err := readPrivateFindingLedger(fixture.root)
		assertPrivateFindingCode(t, err, "ledger_directory")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a permission-observation rejection", causes)
		}
	})

	t.Run("reports path is not a directory", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		reports := filepath.Join(fixture.root, "reports")
		if err := os.RemoveAll(reports); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reports, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := readPrivateFindingLedger(fixture.root)
		assertPrivateFindingCode(t, err, "ledger_directory")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a file-type-observation rejection", causes)
		}
	})
}

func TestPrivateFindingLedgerCandidateProbeRejectsWithoutCause(t *testing.T) {
	t.Run("world-readable candidate", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		if err := os.Chmod(fixture.currentPath(), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := readPrivateFindingLedger(fixture.root)
		assertPrivateFindingCode(t, err, "ledger_file")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a mode-observation rejection", causes)
		}
	})

	t.Run("candidate is a directory", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		if err := os.Remove(fixture.currentPath()); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(fixture.currentPath(), 0o700); err != nil {
			t.Fatal(err)
		}
		_, _, err := readPrivateFindingLedger(fixture.root)
		assertPrivateFindingCode(t, err, "ledger_file")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a file-type-observation rejection", causes)
		}
	})

	t.Run("candidate is a symlink", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		legacy := filepath.Join(fixture.root, PrivateFindingLedgerRelativePath)
		if err := os.Symlink(fixture.currentPath(), legacy); err != nil {
			t.Fatal(err)
		}
		_, _, err := readPrivateFindingLedger(fixture.root)
		assertPrivateFindingCode(t, err, "ledger_file")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a file-type-observation rejection", causes)
		}
	})

	t.Run("no candidate at all", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		if err := os.Remove(fixture.currentPath()); err != nil {
			t.Fatal(err)
		}
		_, _, err := readPrivateFindingLedger(fixture.root)
		assertPrivateFindingCode(t, err, "ledger_file")
		// Both probes reported ordinary absence, which is not a failure.
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want an absence-only rejection", causes)
		}
	})
}

// TestPrivateFindingLedgerAttachesReadPathCauses drives the read window
// through the reader's existing post-inventory seam. Four failure paths stay
// uncovered here because they are reachable only by racing the reader and the
// production code deliberately grows no hook for them: a directory open or
// opened-handle stat that fails after the ambient probe already succeeded, a
// stat or close failure on the already-open ledger descriptor, a final size
// that no longer matches the bytes just read, and a candidate probe that fails
// for a reason other than ordinary absence. They attach on the same terms as
// the branches covered below; the fixed multi-cause order they rely on is
// pinned directly on the constructor instead.
func TestPrivateFindingLedgerAttachesReadPathCauses(t *testing.T) {
	t.Run("candidate removed after inventory", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		_, _, err := readPrivateFindingLedgerWithHook(fixture.root, func() {
			if removeErr := os.Remove(fixture.currentPath()); removeErr != nil {
				t.Fatal(removeErr)
			}
		})
		assertPrivateFindingCode(t, err, "ledger_read")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the open failure retained", causes)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error %v does not expose the concrete open failure", err)
		}
	})

	t.Run("oversized candidate", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		oversized := bytes.Repeat([]byte("x"), privateFindingLedgerMaxBytes+1)
		if err := os.WriteFile(fixture.currentPath(), oversized, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := readPrivateFindingLedger(fixture.root)
		assertPrivateFindingCode(t, err, "ledger_read")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the bounded-read failure retained", causes)
		}
		if strings.Contains(err.Error(), fixture.root) {
			t.Fatalf("message leaked the workspace root: %q", err.Error())
		}
	})

	t.Run("candidate replaced after inventory", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		_, _, err := readPrivateFindingLedgerWithHook(fixture.root, func() {
			replacement := filepath.Join(fixture.root, "reports", "replacement.tmp")
			if writeErr := os.WriteFile(replacement, []byte("{}\n"), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			if renameErr := os.Rename(replacement, fixture.currentPath()); renameErr != nil {
				t.Fatal(renameErr)
			}
		})
		assertPrivateFindingCode(t, err, "ledger_file")
		// The opened handle stats cleanly; only its identity differs.
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want an identity-comparison rejection", causes)
		}
	})

	t.Run("reports directory moved after inventory", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		_, _, err := readPrivateFindingLedgerWithHook(fixture.root, func() {
			if renameErr := os.Rename(
				filepath.Join(fixture.root, "reports"), filepath.Join(fixture.root, "reports-moved"),
			); renameErr != nil {
				t.Fatal(renameErr)
			}
		})
		assertPrivateFindingCode(t, err, "ledger_file")
		// The retained handle still stats, so only the ambient probe fails.
		if causes := privateFindingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the ambient directory probe failure retained", causes)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error %v does not expose the concrete ambient failure", err)
		}
	})

	t.Run("reports directory replaced after inventory", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		_, _, err := readPrivateFindingLedgerWithHook(fixture.root, func() {
			reports := filepath.Join(fixture.root, "reports")
			if renameErr := os.Rename(reports, filepath.Join(fixture.root, "reports-moved")); renameErr != nil {
				t.Fatal(renameErr)
			}
			if mkdirErr := os.Mkdir(reports, 0o700); mkdirErr != nil {
				t.Fatal(mkdirErr)
			}
		})
		assertPrivateFindingCode(t, err, "ledger_file")
		// Both directory probes succeed; the rejection is the identity change.
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a directory-identity rejection", causes)
		}
	})
}

func TestPrivateFindingLedgerAttachesContractCauses(t *testing.T) {
	t.Run("undecodable schema-v2 bytes", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		writePrivateFindingLedgerBytes(t, fixture.currentPath(), []byte("{not json"))
		_, err := loadPrivateFindingLedger(fixture.root)
		assertPrivateFindingCode(t, err, "ledger_contract")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the decode failure retained", causes)
		}
		var syntax *json.SyntaxError
		if !errors.As(err, &syntax) {
			t.Fatalf("error %v does not expose the concrete decode failure", err)
		}
	})

	t.Run("rejected schema-v2 envelope", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		writePrivateFindingLedgerV2(t, fixture.root, PrivateFindingLedgerV2{
			SchemaVersion: PrivateFindingLedgerV2SchemaVersion,
		})
		_, err := loadPrivateFindingLedger(fixture.root)
		assertPrivateFindingCode(t, err, "ledger_contract")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the validation failure retained", causes)
		}
	})

	t.Run("decodable but non-canonical schema-v2 bytes", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		compact, err := json.Marshal(privateFindingLedgerReadFixtureLedger())
		if err != nil {
			t.Fatal(err)
		}
		writePrivateFindingLedgerBytes(t, fixture.currentPath(), append(compact, '\n'))
		_, err = loadPrivateFindingLedger(fixture.root)
		assertPrivateFindingCode(t, err, "ledger_contract")
		// The bytes decode and validate; only the canonical comparison rejects.
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a canonical-comparison rejection", causes)
		}
	})

	t.Run("undecodable legacy bytes", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		if err := os.Remove(fixture.currentPath()); err != nil {
			t.Fatal(err)
		}
		writePrivateFindingLedgerBytes(t, filepath.Join(fixture.root, PrivateFindingLedgerRelativePath), []byte("{not json"))
		_, err := loadPrivateFindingLedger(fixture.root)
		assertPrivateFindingCode(t, err, "ledger_contract")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the legacy decode failure retained", causes)
		}
		var syntax *json.SyntaxError
		if !errors.As(err, &syntax) {
			t.Fatalf("error %v does not expose the concrete legacy decode failure", err)
		}
	})

	t.Run("decodable but non-canonical legacy bytes", func(t *testing.T) {
		fixture := newPrivateFindingLedgerReadFixture(t)
		if err := os.Remove(fixture.currentPath()); err != nil {
			t.Fatal(err)
		}
		legacy := PrivateFindingLedger{
			SchemaVersion: PrivateFindingLedgerSchemaVersion,
			Entries: []PrivateFindingEntry{{
				FindingID: "finding-legacy-001",
				Failure: PrivateFindingRunRef{
					PlanID:  "pln-11111111111111111111111111111111",
					Surface: SurfaceCLISkill, Baseline: "captured",
				},
				FailureClass: PrivateFailureModel, ProductIssue: 101,
				Decision: PrivateFindingDecisionInvestigate,
			}},
		}
		compact, err := json.Marshal(legacy)
		if err != nil {
			t.Fatal(err)
		}
		writePrivateFindingLedgerBytes(t, filepath.Join(fixture.root, PrivateFindingLedgerRelativePath), append(compact, '\n'))
		_, err = loadPrivateFindingLedger(fixture.root)
		assertPrivateFindingCode(t, err, "ledger_contract")
		if causes := privateFindingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a canonical-comparison rejection", causes)
		}
	})
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

// privateFindingLedgerReadFixture is the smallest workspace the ledger reader
// accepts: an owner-only reports directory holding one canonical schema-v2
// ledger. It exercises the read path without building sampling evidence.
type privateFindingLedgerReadFixture struct{ root string }

func (f privateFindingLedgerReadFixture) currentPath() string {
	return filepath.Join(f.root, PrivateFindingLedgerV2RelativePath)
}

func newPrivateFindingLedgerReadFixture(t *testing.T) privateFindingLedgerReadFixture {
	t.Helper()
	fixture := privateFindingLedgerReadFixture{root: newPrivateFindingFixture(t).root}
	writePrivateFindingLedgerV2(t, fixture.root, privateFindingLedgerReadFixtureLedger())
	return fixture
}

func privateFindingLedgerReadFixtureLedger() PrivateFindingLedgerV2 {
	return PrivateFindingLedgerV2{
		SchemaVersion: PrivateFindingLedgerV2SchemaVersion,
		Entries: []PrivateFindingEntryV2{{
			FindingID: "finding-read-001",
			Failure: PrivateFindingEvidenceRef{
				Source: PrivateFindingAcceptanceSourceSyntheticRoot, AssessmentSHA256: strings.Repeat("1", 64),
			},
			FailureClass:  PrivateFailureHarness,
			ProductIssues: []int{101},
			Decision:      PrivateFindingDecisionInvestigate,
		}},
	}
}

func writePrivateFindingLedgerBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertPrivateFindingCode(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, ErrPrivateFindingLedgerRejected) {
		t.Fatalf("err=%v, want the finding-ledger sentinel", err)
	}
	if got, want := err.Error(), ErrPrivateFindingLedgerRejected.Error()+": "+code; got != want {
		t.Fatalf("message=%q, want %q", got, want)
	}
}

func privateFindingErrorCauses(t *testing.T, err error) []error {
	t.Helper()
	multi, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("%T does not unwrap to multiple errors", err)
	}
	tree := multi.Unwrap()
	if len(tree) == 0 || tree[0] != ErrPrivateFindingLedgerRejected {
		t.Fatalf("unwrap tree=%v, want the sentinel first", tree)
	}
	return tree[1:]
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
