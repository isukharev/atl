package agenteval

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

func TestLegacyPrivateActivationReferenceIsReadableAndCompareOnly(t *testing.T) {
	reference, err := CapturePrivateActivationReference(privateActivationPassingInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	reference.SchemaVersion = LegacyPrivateActivationReferenceSchemaVersion
	for index := range reference.Cells {
		reference.Cells[index].Metrics = withoutPrivateActivationAttemptMetrics(reference.Cells[index].Metrics)
	}
	if err := reference.Validate(); err != nil {
		t.Fatalf("legacy reference was not readable: %v", err)
	}
	report, err := ComparePrivateActivationReference(reference)
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != LegacyPrivateActivationReportSchemaVersion || !report.Gates.CausalEligible || report.Gates.PromotionEligible || len(report.Contrasts) == 0 {
		t.Fatalf("legacy report=%+v", report)
	}
	for _, treatment := range report.Treatments {
		if treatment.Gates.PromotionEligible {
			t.Fatalf("legacy treatment was marked promotable: %+v", treatment)
		}
		for _, metric := range treatment.Metrics {
			if strings.HasPrefix(metric.Name, "evidence_") {
				t.Fatalf("legacy comparison fabricated attempt metric: %+v", metric)
			}
		}
	}
	if err := ValidatePrivateActivationReferencePromotion(reference); err == nil || !strings.Contains(err.Error(), "compare-only") {
		t.Fatalf("legacy promotion err=%v", err)
	}
}

func TestCurrentPrivateActivationReferenceRequiresSuccessfulAttemptForPromotion(t *testing.T) {
	reference, err := CapturePrivateActivationReference(privateActivationPassingInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	if reference.SchemaVersion != PrivateActivationReferenceSchemaVersion || reference.SchemaVersion == LegacyPrivateActivationReferenceSchemaVersion {
		t.Fatalf("schema=%d", reference.SchemaVersion)
	}

	missing := reference
	missing.Cells = append([]PrivateActivationReferenceCell(nil), reference.Cells...)
	missing.Cells[0].Metrics = withoutPrivateActivationAttemptMetrics(missing.Cells[0].Metrics)
	if err := missing.Validate(); err == nil {
		t.Fatal("current reference without attempt telemetry was accepted")
	}

	blocked := reference
	blocked.Cells = append([]PrivateActivationReferenceCell(nil), reference.Cells...)
	blocked.Cells[0].Metrics = append([]PrivateActivationMetric(nil), reference.Cells[0].Metrics...)
	setPrivateActivationMetric(t, blocked.Cells[0].Metrics, privateActivationMetricEvidenceSucceededBPS, 0)
	setPrivateActivationMetric(t, blocked.Cells[0].Metrics, privateActivationMetricEvidenceBlockedBPS, 10000)
	if err := blocked.Validate(); err != nil {
		t.Fatalf("valid observed block was rejected: %v", err)
	}
	gates, err := PrivateActivationReferenceGates(blocked)
	if err != nil {
		t.Fatal(err)
	}
	if !gates.CausalEligible || gates.PromotionEligible {
		t.Fatalf("blocked gates=%+v", gates)
	}
	if err := ValidatePrivateActivationReferencePromotion(blocked); err == nil {
		t.Fatal("reference without successful evidence was promoted")
	}

	inconsistent := reference
	inconsistent.Cells = append([]PrivateActivationReferenceCell(nil), reference.Cells...)
	inconsistent.Cells[0].Metrics = append([]PrivateActivationMetric(nil), reference.Cells[0].Metrics...)
	setPrivateActivationMetric(t, inconsistent.Cells[0].Metrics, privateActivationMetricEvidenceAttemptedBPS, 0)
	if err := inconsistent.Validate(); err == nil {
		t.Fatal("inconsistent attempt metrics were accepted")
	}
}

func TestPrivateActivationReferenceKeepsZeroCallModelReportSeparateFromAudit(t *testing.T) {
	inputs := privateActivationPassingInputs(t)
	inputs[0].Result.EvidenceAttempt, _ = NewEvidenceAttemptTelemetry(true, EvidenceAttemptCounts{})
	inputs[0].Result.EvidenceReport = EvidenceOutcomeReport{Coverage: true, State: EvidenceAttemptStateUnavailable}
	reference, err := CapturePrivateActivationReference(inputs)
	if err != nil {
		t.Fatal(err)
	}
	metrics := reference.Cells[0].Metrics
	assertPrivateActivationMetric(t, metrics, privateActivationMetricEvidenceAttemptedBPS, 0)
	assertPrivateActivationMetric(t, metrics, privateActivationMetricEvidenceUnavailableBPS, 0)
	assertPrivateActivationMetric(t, metrics, privateActivationMetricReportedNoneBPS, 0)
	assertPrivateActivationMetric(t, metrics, privateActivationMetricReportedUnavailableBPS, 10000)
	if err := ValidatePrivateActivationReferencePromotion(reference); err == nil {
		t.Fatal("self-reported unavailable route was promoted as audited evidence")
	}
}

func TestPrivateActivationReferenceRejectsAuditReportContradictions(t *testing.T) {
	inputs := privateActivationPassingInputs(t)
	contradictory := inputs[0].Result
	contradictory.EvidenceReport = EvidenceOutcomeReport{Coverage: true, State: EvidenceAttemptStateNone}
	if err := contradictory.Validate(); err == nil {
		t.Fatal("result accepted a self-report that contradicted successful audit evidence")
	}
	inputs[0].Result = contradictory
	if _, err := CapturePrivateActivationReference(inputs); err == nil {
		t.Fatal("reference capture accepted contradictory result evidence")
	}

	reference, err := CapturePrivateActivationReference(privateActivationPassingInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	reference.Cells[0].Metrics = append([]PrivateActivationMetric(nil), reference.Cells[0].Metrics...)
	setPrivateActivationMetric(t, reference.Cells[0].Metrics, privateActivationMetricReportedNoneBPS, 10000)
	if err := reference.Validate(); err == nil {
		t.Fatal("durable reference accepted contradictory compressed evidence")
	}
	if err := ValidatePrivateActivationReferencePromotion(reference); err == nil {
		t.Fatal("contradictory durable reference was promotable")
	}
}

func TestLegacyPrivateActivationReferenceRejectsCurrentEvidenceMetrics(t *testing.T) {
	reference, err := CapturePrivateActivationReference(privateActivationPassingInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	reference.SchemaVersion = LegacyPrivateActivationReferenceSchemaVersion
	for index := range reference.Cells {
		reference.Cells[index].Metrics = withoutPrivateActivationAttemptMetrics(reference.Cells[index].Metrics)
	}
	reference.Cells[0].Metrics = append(reference.Cells[0].Metrics,
		PrivateActivationMetric{Name: privateActivationMetricEvidenceAttemptedBPS, Value: 10000})
	sort.Slice(reference.Cells[0].Metrics, func(i, j int) bool {
		return reference.Cells[0].Metrics[i].Name < reference.Cells[0].Metrics[j].Name
	})
	if err := reference.Validate(); err == nil {
		t.Fatal("legacy reference accepted current evidence metrics")
	}
}

func TestPrivateActivationReportRequiresCurrentEvidenceReportMetrics(t *testing.T) {
	reference, err := CapturePrivateActivationReference(privateActivationPassingInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	report, err := ComparePrivateActivationReference(reference)
	if err != nil {
		t.Fatal(err)
	}
	report.Treatments[0].Metrics = removePrivateActivationMetric(report.Treatments[0].Metrics, privateActivationMetricReportedNoneBPS)
	if err := report.Validate(); err == nil {
		t.Fatal("current report accepted missing model-report metric")
	}

	legacy := report
	legacy.SchemaVersion = LegacyPrivateActivationReportSchemaVersion
	legacy.Treatments = append([]PrivateActivationTreatmentReport(nil), report.Treatments...)
	legacy.Treatments[0].Metrics = []PrivateActivationMetric{
		{Name: privateActivationMetricEvidenceAttemptedBPS, Value: 10000},
	}
	if err := legacy.Validate(); err == nil {
		t.Fatal("legacy report accepted current evidence metric")
	}
}

func TestStoredPrivateActivationReferenceSchemasCannotCross(t *testing.T) {
	current := privateStoredActivationReference{SchemaVersion: privateActivationStoredSchemaVersion,
		Reference: PrivateActivationReference{SchemaVersion: PrivateActivationReferenceSchemaVersion}}
	legacy := privateStoredActivationReference{SchemaVersion: legacyPrivateActivationStoredSchemaVersion,
		Reference: PrivateActivationReference{SchemaVersion: LegacyPrivateActivationReferenceSchemaVersion}}
	if !validPrivateStoredActivationReferenceSchema(current) || !validPrivateStoredActivationReferenceSchema(legacy) {
		t.Fatal("matching stored/reference schemas were rejected")
	}
	current.Reference.SchemaVersion = LegacyPrivateActivationReferenceSchemaVersion
	legacy.Reference.SchemaVersion = PrivateActivationReferenceSchemaVersion
	if validPrivateStoredActivationReferenceSchema(current) || validPrivateStoredActivationReferenceSchema(legacy) {
		t.Fatal("crossed stored/reference schemas were accepted")
	}
}

func TestPrivateActivationReferenceKeepsTaskFailureComparableButNotPromotable(t *testing.T) {
	inputs := privateActivationPassingInputs(t)
	failed := inputs[0].Result
	failed.Status = "fail"
	failed.Violations = append(failed.Violations, Violation{Code: "oracle_failed", Subject: "answer_correct"})
	inputs[0].Result = failed

	reference, err := CapturePrivateActivationReference(inputs)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ComparePrivateActivationReference(reference)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Gates.CaptureEligible || !report.Gates.CausalEligible || report.Gates.PromotionEligible {
		t.Fatalf("gates=%+v", report.Gates)
	}
	if len(report.Contrasts) == 0 {
		t.Fatal("safe task failure suppressed factorial contrasts")
	}
	if report.Treatments[0].Gates.CausalEligible != true || report.Treatments[0].Gates.PromotionEligible {
		t.Fatalf("failed treatment gates=%+v", report.Treatments[0].Gates)
	}
	if err := ValidatePrivateActivationReferencePromotion(reference); err == nil {
		t.Fatal("task-failing reference was promotable")
	}
}

func TestPrivateActivationReferenceSuppressesContrastsForStudyGateFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, []PrivateActivationCellInput)
		check  func(PrivateActivationGates) bool
	}{
		{
			name: "unsupported",
			mutate: func(t *testing.T, inputs []PrivateActivationCellInput) {
				result := privateActivationResult(t, SkillActivationImplicit)
				result.Eligibility = EligibilityUnsupportedCapability
				result.UnavailableCapabilities = []string{"jira.unsupported"}
				result.Status = "ineligible"
				inputs[0].Result = result
			},
			check: func(gates PrivateActivationGates) bool { return !gates.Supported },
		},
		{
			name: "safety-incomplete",
			mutate: func(_ *testing.T, inputs []PrivateActivationCellInput) {
				inputs[0].SafetyComplete = false
			},
			check: func(gates PrivateActivationGates) bool { return !gates.SafetyComplete && !gates.SafetyClean },
		},
		{
			name: "safety-violation",
			mutate: func(_ *testing.T, inputs []PrivateActivationCellInput) {
				inputs[0].SafetyViolationCount = 1
			},
			check: func(gates PrivateActivationGates) bool { return gates.SafetyComplete && !gates.SafetyClean },
		},
		{
			name: "review-incomplete",
			mutate: func(t *testing.T, inputs []PrivateActivationCellInput) {
				inputs[0].Result = privateActivationResult(t, SkillActivationImplicit)
			},
			check: func(gates PrivateActivationGates) bool { return !gates.ReviewComplete && gates.NoDisagreement },
		},
		{
			name: "review-disagreement",
			mutate: func(t *testing.T, inputs []PrivateActivationCellInput) {
				inputs[0].Result = privateActivationDisagreementResult(t, SkillActivationImplicit)
			},
			check: func(gates PrivateActivationGates) bool { return !gates.ReviewComplete && !gates.NoDisagreement },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inputs := privateActivationPassingInputs(t)
			test.mutate(t, inputs)
			reference, err := CapturePrivateActivationReference(inputs)
			if err != nil {
				t.Fatal(err)
			}
			report, err := ComparePrivateActivationReference(reference)
			if err != nil {
				t.Fatal(err)
			}
			if !report.Gates.CaptureEligible || report.Gates.CausalEligible || report.Gates.PromotionEligible || !test.check(report.Gates) {
				t.Fatalf("gates=%+v", report.Gates)
			}
			if report.Contrasts == nil || len(report.Contrasts) != 0 {
				t.Fatalf("ineligible report contrasts=%+v", report.Contrasts)
			}
		})
	}
}

func TestPrivateActivationReferencePromotionRequiresCompletePassingOutcomes(t *testing.T) {
	reference, err := CapturePrivateActivationReference(privateActivationPassingInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateActivationReferencePromotion(reference); err != nil {
		t.Fatalf("passing reference was not promotable: %v", err)
	}

	inputs := privateActivationPassingInputs(t)
	inputs[0].Result = privateActivationReviewedResult(t, privateActivationResult(t, SkillActivationImplicit), false)
	failedReview, err := CapturePrivateActivationReference(inputs)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ComparePrivateActivationReference(failedReview)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Gates.CausalEligible || report.Gates.PromotionEligible {
		t.Fatalf("qualitative failure gates=%+v", report.Gates)
	}
	if err := ValidatePrivateActivationReferencePromotion(failedReview); err == nil {
		t.Fatal("qualitative-failing reference was promotable")
	}
}

func TestPrivateActivationReferenceComputesExactFactorialContrasts(t *testing.T) {
	inputs := privateActivationPassingInputs(t)
	for index, value := range []int{1, 3, 5, 9} {
		result := privateActivationResult(t, inputs[index].Treatment)
		result.Coverage["tool_calls"] = true
		result.Metrics.ToolCalls = value
		inputs[index].Result = privateActivationReviewedResult(t, result, true)
	}
	reference, err := CapturePrivateActivationReference(inputs)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ComparePrivateActivationReference(reference)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string][2]int64{
		privateActivationFactorUserChannel:      {6, 2},
		privateActivationFactorDeveloperChannel: {10, 2},
		privateActivationFactorInteraction:      {2, 1},
	}
	seen := 0
	for _, contrast := range report.Contrasts {
		if contrast.Metric != "tool_calls" {
			continue
		}
		expected, ok := want[contrast.Factor]
		if !ok || contrast.EstimateNumerator != expected[0] || contrast.EstimateDenominator != expected[1] {
			t.Fatalf("contrast=%+v", contrast)
		}
		seen++
	}
	if seen != 3 {
		t.Fatalf("tool-call contrasts seen=%d report=%+v", seen, report.Contrasts)
	}
}

func TestPrivateActivationReportOmitsPrivateIdentityAndContentFields(t *testing.T) {
	inputs := privateActivationPassingInputs(t)
	for index := range inputs {
		result := inputs[index].Result
		result.ScenarioID = "private-scenario-canary"
		result.TaskClass = "private-task-canary"
		result.Variant = "private-variant-canary"
		result.Runtime.AgentVersion = "private-agent-canary"
		result.Runtime.Model = "private-model-canary"
		result.Runtime.PromptContractSHA256 = strings.Repeat("a", 64)
		result.Qualitative.Reviewer.ID = "private-reviewer-canary"
		result.Qualitative.Reviewer.Model = "private-review-model-canary"
		inputs[index].Result = result
	}
	reference, err := CapturePrivateActivationReference(inputs)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ComparePrivateActivationReference(reference)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"private-scenario-canary", "private-task-canary", "private-variant-canary",
		"private-agent-canary", "private-model-canary", "private-reviewer-canary",
		"private-review-model-canary", strings.Repeat("a", 64),
		"scenario_id", "prompt", "reviewer", "model", "path", "sha256",
	} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("privacy-safe report contains %q: %s", forbidden, data)
		}
	}
}

func TestPrivateActivationReferenceRejectsIncompleteTreatmentMatrix(t *testing.T) {
	inputs := privateActivationPassingInputs(t)
	if _, err := CapturePrivateActivationReference(inputs[:3]); err == nil {
		t.Fatal("three-cell matrix was accepted")
	}
	inputs[3].Treatment = SkillActivationImplicit
	if _, err := CapturePrivateActivationReference(inputs); err == nil {
		t.Fatal("duplicate treatment matrix was accepted")
	}
}

func TestPrivateActivationResultIdentityIsBoundToPlanCell(t *testing.T) {
	result := privateActivationResult(t, SkillActivationImplicit)
	item := privatePlanItem{ScenarioID: result.ScenarioID, Variant: result.Variant, Surface: result.EffectiveSurface(),
		Provider: result.Runtime.Provider, SkillActivation: result.Runtime.SkillActivation,
		PromptContractSHA256: result.Runtime.PromptContractSHA256}
	plan := privatePlan{Provider: result.Runtime.Provider, Model: result.Runtime.Model}
	if !privateActivationResultMatchesPlan(result, plan, item) {
		t.Fatal("matching result identity was rejected")
	}
	tests := []struct {
		name   string
		mutate func(*Result)
	}{
		{name: "scenario", mutate: func(value *Result) { value.ScenarioID = "different-scenario" }},
		{name: "variant", mutate: func(value *Result) { value.Variant = "different-variant" }},
		{name: "provider", mutate: func(value *Result) { value.Runtime.Provider = "claude-code" }},
		{name: "model", mutate: func(value *Result) { value.Runtime.Model = "different-model" }},
		{name: "prompt", mutate: func(value *Result) { value.Runtime.PromptContractSHA256 = strings.Repeat("c", 64) }},
		{name: "treatment", mutate: func(value *Result) { value.Runtime.SkillActivation = SkillActivationExplicit }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := result
			test.mutate(&candidate)
			if privateActivationResultMatchesPlan(candidate, plan, item) {
				t.Fatal("drifted result identity was accepted")
			}
		})
	}
}

func privateActivationPassingInputs(t *testing.T) []PrivateActivationCellInput {
	t.Helper()
	inputs := make([]PrivateActivationCellInput, 0, len(privateActivationTreatments))
	for _, treatment := range privateActivationTreatments {
		result := privateActivationReviewedResult(t, privateActivationResult(t, treatment), true)
		inputs = append(inputs, PrivateActivationCellInput{Treatment: treatment, Result: result, SafetyComplete: true})
	}
	return inputs
}

func privateActivationResult(t *testing.T, treatment string) Result {
	t.Helper()
	scenario := validScenario()
	scenario.DataClass = "private-local"
	observation := validObservation()
	observation.Surface = SurfaceCLISkill
	observation.Runtime.Provider = "codex"
	observation.Runtime.SkillActivation = treatment
	observation.Runtime.PromptContractSHA256 = strings.Repeat("b", 64)
	observation.EvidenceAttempt, _ = NewEvidenceAttemptTelemetry(true, EvidenceAttemptCounts{
		Attempts: 1, Admitted: 1, Succeeded: 1,
	})
	observation.EvidenceReport = EvidenceOutcomeReport{Coverage: true, State: EvidenceAttemptStateSucceeded}
	result, err := Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func withoutPrivateActivationAttemptMetrics(metrics []PrivateActivationMetric) []PrivateActivationMetric {
	out := make([]PrivateActivationMetric, 0, len(metrics))
	for _, metric := range metrics {
		switch metric.Name {
		case privateActivationMetricEvidenceAttemptedBPS, privateActivationMetricEvidenceSucceededBPS,
			privateActivationMetricEvidenceBlockedBPS, privateActivationMetricEvidenceUnavailableBPS,
			privateActivationMetricReportedNoneBPS, privateActivationMetricReportedUnavailableBPS:
			continue
		default:
			out = append(out, metric)
		}
	}
	return out
}

func setPrivateActivationMetric(t *testing.T, metrics []PrivateActivationMetric, name string, value int64) {
	t.Helper()
	for index := range metrics {
		if metrics[index].Name == name {
			metrics[index].Value = value
			return
		}
	}
	t.Fatalf("metric %q not found", name)
}

func assertPrivateActivationMetric(t *testing.T, metrics []PrivateActivationMetric, name string, want int64) {
	t.Helper()
	for _, metric := range metrics {
		if metric.Name == name {
			if metric.Value != want {
				t.Fatalf("metric %q=%d want=%d", name, metric.Value, want)
			}
			return
		}
	}
	t.Fatalf("metric %q not found", name)
}

func removePrivateActivationMetric(metrics []PrivateActivationMetric, name string) []PrivateActivationMetric {
	out := make([]PrivateActivationMetric, 0, len(metrics))
	for _, metric := range metrics {
		if metric.Name != name {
			out = append(out, metric)
		}
	}
	return out
}

func privateActivationReviewedResult(t *testing.T, result Result, pass bool) Result {
	t.Helper()
	resultData, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	final := []byte(`{"answer":"synthetic"}`)
	rubric := testRubric(result.ScenarioID)
	review, err := NewReviewTemplate(result, resultData, final, rubric, Reviewer{Kind: "human"})
	if err != nil {
		t.Fatal(err)
	}
	for index := range review.Criteria {
		if pass {
			review.Criteria[index].Score = rubric.Criteria[index].Maximum
		} else {
			review.Criteria[index].Score = 0
		}
	}
	assessed, err := AssessQualitative(result, resultData, final, rubric, review)
	if err != nil {
		t.Fatal(err)
	}
	return assessed
}

func privateActivationDisagreementResult(t *testing.T, treatment string) Result {
	t.Helper()
	result := privateActivationResult(t, treatment)
	resultData, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	final := []byte(`{"answer":"synthetic"}`)
	rubric := testRubric(result.ScenarioID)
	reviews := panelReviews(t, result, resultData, final, rubric, [][2]int{{4, 4}, {0, 0}, {4, 4}}, nil)
	assessed, err := AssessQualitativeReviewSet(result, resultData, final, rubric, panelPolicy(9999), reviews)
	if err != nil {
		t.Fatal(err)
	}
	if assessed.QualitativeReviewSet == nil || assessed.QualitativeReviewSet.Status != privateActivationReviewDisagreement {
		t.Fatalf("assessment=%+v", assessed.QualitativeReviewSet)
	}
	return assessed
}
