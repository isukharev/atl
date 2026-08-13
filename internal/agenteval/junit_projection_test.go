package agenteval

import (
	"bytes"
	"strings"
	"testing"
)

func TestProjectJUnitMapsClosedResultAndDecisionStates(t *testing.T) {
	input := JUnitProjectionInput{
		Results: []JUnitResultInput{
			{Identity: "result/zeta", SchemaVersion: LegacyResultSchemaVersion, Status: "pass", EvidenceCovered: true, EvidenceState: string(EvidenceAttemptStateSucceeded)},
			{Identity: "result/alpha", SchemaVersion: ResultSchemaVersion, Status: "fail", Violations: []JUnitViolationInput{{Code: "required_check_failed", Subject: "answer"}}},
			{Identity: "result/beta", SchemaVersion: ResultSchemaVersion, Status: "fail", Violations: []JUnitViolationInput{{Code: "metric_not_observed", Subject: "answer"}}},
			{Identity: "result/gamma", SchemaVersion: ResultSchemaVersion, Status: "ineligible", Eligibility: EligibilityUnsupportedCapability},
			{Identity: "result/delta", SchemaVersion: ResultSchemaVersion, Status: "ineligible", Eligibility: EligibilityInvalidatedDrift},
			{Identity: "result/epsilon", SchemaVersion: ResultSchemaVersion, Status: "pass", EvidenceCovered: true, EvidenceState: string(EvidenceAttemptStateFailed)},
		},
		Decisions: []JUnitPairedDecisionInput{
			{Identity: "decision/zeta", InferenceStatus: "inferential", CompletePairs: 2},
			{Identity: "decision/alpha", InferenceStatus: "inferential", CompletePairs: 2, Regression: true},
			{Identity: "decision/beta", InferenceStatus: "insufficient", ExcludedPairs: 2, UnsupportedPairs: 2},
			{Identity: "decision/gamma", InferenceStatus: "descriptive", CompletePairs: 1},
			{Identity: "decision/delta", InferenceStatus: "insufficient", ExcludedPairs: 1},
		},
	}
	first, err := ProjectJUnit(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Tests != 11 || first.Failures != 2 || first.Errors != 5 || first.Skipped != 2 {
		t.Fatalf("summary=%+v", first)
	}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := EncodeJUnit(first)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeJUnit(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Tests != first.Tests || !bytes.Equal(data, mustEncodeJUnit(t, decoded)) {
		t.Fatal("decoded JUnit is not an exact canonical round trip")
	}

	// Reordering canonical input cannot reorder or otherwise change the XML.
	reordered := input
	reordered.Results = append([]JUnitResultInput(nil), input.Results...)
	reordered.Decisions = append([]JUnitPairedDecisionInput(nil), input.Decisions...)
	for left, right := 0, len(reordered.Results)-1; left < right; left, right = left+1, right-1 {
		reordered.Results[left], reordered.Results[right] = reordered.Results[right], reordered.Results[left]
	}
	for left, right := 0, len(reordered.Decisions)-1; left < right; left, right = left+1, right-1 {
		reordered.Decisions[left], reordered.Decisions[right] = reordered.Decisions[right], reordered.Decisions[left]
	}
	second, err := ProjectJUnit(reordered)
	if err != nil {
		t.Fatal(err)
	}
	secondData, err := EncodeJUnit(second)
	if err != nil || !bytes.Equal(data, secondData) {
		t.Fatalf("projection is not deterministic: err=%v", err)
	}
}

func TestProjectJUnitRejectsUnknownAndIncompleteStates(t *testing.T) {
	tests := []struct {
		name   string
		input  JUnitProjectionInput
		reject bool
	}{
		{name: "unknown result status", reject: true, input: JUnitProjectionInput{Results: []JUnitResultInput{{Identity: "result/a", SchemaVersion: ResultSchemaVersion, Status: "unknown"}}}},
		{name: "missing evidence cannot pass", input: JUnitProjectionInput{Results: []JUnitResultInput{{Identity: "result/a", SchemaVersion: ResultSchemaVersion, Status: "pass"}}}},
		{name: "unknown evidence", input: JUnitProjectionInput{Results: []JUnitResultInput{{Identity: "result/a", SchemaVersion: ResultSchemaVersion, Status: "pass", EvidenceCovered: true, EvidenceState: "unknown"}}}},
		{name: "incomplete pair cannot pass", input: JUnitProjectionInput{Decisions: []JUnitPairedDecisionInput{{Identity: "decision/a", InferenceStatus: "inferential", CompletePairs: 2, ExcludedPairs: 1}}}},
		{name: "unknown violation", reject: true, input: JUnitProjectionInput{Results: []JUnitResultInput{{Identity: "result/a", SchemaVersion: ResultSchemaVersion, Status: "fail", Violations: []JUnitViolationInput{{Code: "provider_message", Subject: "answer"}}}}}},
		{name: "unsafe identity", reject: true, input: JUnitProjectionInput{Results: []JUnitResultInput{{Identity: "result/<script>", SchemaVersion: ResultSchemaVersion, Status: "pass"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := ProjectJUnit(test.input)
			if test.reject {
				if err == nil {
					t.Fatal("unsafe or unknown input was accepted")
				}
				return
			}
			if err != nil || report.Tests != 1 || report.Errors != 1 {
				t.Fatalf("uncertain input was not mapped to one error: report=%+v err=%v", report, err)
			}
		})
	}
}

func TestProjectJUnitDoesNotEmitInputIdentityOrDiagnostics(t *testing.T) {
	input := JUnitProjectionInput{Results: []JUnitResultInput{
		{Identity: "result/synthetic-case", SchemaVersion: ResultSchemaVersion, Status: "fail", Violations: []JUnitViolationInput{{Code: "required_check_failed", Subject: "answer"}}},
	}}
	report, err := ProjectJUnit(input)
	if err != nil {
		t.Fatal(err)
	}
	data, err := EncodeJUnit(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"synthetic-case", "answer", "/tmp", "prompt", "provider", "<script>", "&lt;"} {
		if bytes.Contains(data, []byte(marker)) {
			t.Fatalf("projection leaked marker %q: %s", marker, data)
		}
	}
	if !bytes.Contains(data, []byte("test-000001")) || !bytes.Contains(data, []byte("task regression")) {
		t.Fatalf("projection lost generic bounded fields: %s", data)
	}
}

func TestProjectJUnitFacadeValidatesCanonicalResult(t *testing.T) {
	result := privateFindingTestResult(t, true)
	result.ScenarioID = "junit-facade"
	result.EvidenceAttempt = EvidenceAttemptTelemetry{Coverage: true, State: EvidenceAttemptStateSucceeded, Attempts: 1, Admitted: 1, Succeeded: 1}
	report, err := ProjectJUnitResult(result)
	if err != nil {
		t.Fatal(err)
	}
	if report.Tests != 1 || report.Failures != 0 || report.Errors != 0 || report.Skipped != 0 {
		t.Fatalf("report=%+v", report)
	}
	result.Status = "pass"
	result.Violations = []Violation{{Code: "provider_message", Subject: "answer"}}
	if _, err := ProjectJUnitResult(result); err == nil {
		t.Fatal("noncanonical result was accepted")
	}
}

func TestJUnitGoldenIsByteStable(t *testing.T) {
	report, err := ProjectJUnit(JUnitProjectionInput{Results: []JUnitResultInput{
		{Identity: "result/b", SchemaVersion: LegacyResultSchemaVersion, Status: "pass", EvidenceCovered: true, EvidenceState: string(EvidenceAttemptStateSucceeded)},
		{Identity: "result/a", SchemaVersion: LegacyResultSchemaVersion, Status: "fail", Violations: []JUnitViolationInput{{Code: "required_check_failed", Subject: "answer"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := EncodeJUnit(report)
	if err != nil {
		t.Fatal(err)
	}
	want := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<testsuites name=\"agent-eval\" tests=\"2\" failures=\"1\" errors=\"0\" skipped=\"0\"><testsuite name=\"agent-eval\" tests=\"2\" failures=\"1\" errors=\"0\" skipped=\"0\"><properties><property name=\"agent-eval.schema\" value=\"agent-eval/junit\"></property><property name=\"agent-eval.schema_version\" value=\"1\"></property><property name=\"agent-eval.contract_version\" value=\"0.1.0-pre-release\"></property><property name=\"agent-eval.producer\" value=\"atl-agent-eval\"></property></properties><testcase classname=\"agent-eval\" name=\"test-000001\"><failure message=\"task regression\"></failure></testcase><testcase classname=\"agent-eval\" name=\"test-000002\"></testcase></testsuite></testsuites>\n"
	if string(data) != want {
		t.Fatalf("JUnit golden drifted:\n%s", data)
	}
}

func mustEncodeJUnit(t *testing.T, report JUnitReport) []byte {
	t.Helper()
	data, err := EncodeJUnit(report)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestJUnitStateDiagnosticsRemainClosed(t *testing.T) {
	report := JUnitReport{
		Name: "agent-eval", Tests: 1,
		Suites: []JUnitSuite{{Name: "agent-eval", Tests: 1, Properties: JUnitProperties{Properties: []JUnitProperty{
			{Name: "agent-eval.schema", Value: JUnitSchema}, {Name: "agent-eval.schema_version", Value: "1"},
			{Name: "agent-eval.contract_version", Value: JUnitContractVersion}, {Name: "agent-eval.producer", Value: JUnitProducer},
		}}, Testcases: []JUnitTestcase{{Classname: "agent-eval", Name: "test-000001", Failure: &JUnitDiagnostic{Message: "<injected>"}}}}},
	}
	if err := report.Validate(); err == nil || !strings.Contains(err.Error(), "diagnostic") {
		t.Fatalf("unclosed diagnostic accepted: %v", err)
	}
}
