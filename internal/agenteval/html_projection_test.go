package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go/parser"
	"go/token"
	"html"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestProjectHTMLIsDeterministicAndContentMinimized(t *testing.T) {
	input := validHTMLProjectionInput()
	original := input
	report, err := ProjectHTML(input)
	if err != nil {
		t.Fatalf("ProjectHTML() error = %v", err)
	}
	encoded, err := EncodeHTML(report)
	if err != nil {
		t.Fatalf("EncodeHTML() error = %v", err)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatal("ProjectHTML mutated its input")
	}

	shuffled := input
	shuffled.Coverage = reverseHTML(input.Coverage)
	shuffled.Lift = reverseHTML(input.Lift)
	shuffled.Activation = reverseHTML(input.Activation)
	shuffled.Funnels = reverseHTML(input.Funnels)
	shuffled.Failures = reverseHTML(input.Failures)
	shuffled.Resources = reverseHTML(input.Resources)
	for index := range shuffled.Failures {
		shuffled.Failures[index].Failures = reverseHTML(shuffled.Failures[index].Failures)
	}
	shuffledReport, err := ProjectHTML(shuffled)
	if err != nil {
		t.Fatalf("ProjectHTML(shuffled) error = %v", err)
	}
	shuffledEncoded, err := EncodeHTML(shuffledReport)
	if err != nil {
		t.Fatalf("EncodeHTML(shuffled) error = %v", err)
	}
	if !bytes.Equal(encoded, shuffledEncoded) {
		t.Fatal("equivalent inputs produced different HTML")
	}
	if !bytes.HasSuffix(encoded, []byte("\n")) || len(encoded) > HTMLMaxBytes {
		t.Fatalf("invalid output bounds or terminator: length=%d", len(encoded))
	}
	goldenDigest := sha256.Sum256(encoded)
	if got := hex.EncodeToString(goldenDigest[:]); got != "31cd55ae131e80dac4beab3615438a316aa9913a6d773d5d97e940d5973415a2" {
		t.Fatalf("golden HTML changed: got %s", got)
	}
	if !strings.Contains(string(encoded), report.ProjectionSHA256) {
		t.Fatal("projection digest is not visible in the provenance table")
	}
	if !strings.Contains(string(encoded), htmlStyles) {
		t.Fatal("fixed stylesheet is missing")
	}
	for _, label := range []string{"False activation", "Unnecessary load", "Security bundle", "Security policy", "Rule pack"} {
		if !strings.Contains(string(encoded), label) {
			t.Errorf("rendered report is missing %q", label)
		}
	}
	if !strings.Contains(html.UnescapeString(string(encoded)), "default-src 'none'") {
		t.Fatal("CSP is missing default-src none")
	}
}

func TestEncodeHTMLHasNoActiveOrExternalContent(t *testing.T) {
	report, err := ProjectHTML(validHTMLProjectionInput())
	if err != nil {
		t.Fatalf("ProjectHTML() error = %v", err)
	}
	encoded, err := EncodeHTML(report)
	if err != nil {
		t.Fatalf("EncodeHTML() error = %v", err)
	}
	document := strings.ToLower(string(encoded))
	for _, forbidden := range []string{
		"<script", "<link", "<img", "<iframe", "<object", "<embed", "href=", "src=",
		"javascript:", "http://", "https://", "onclick=", "onerror=", "url(",
	} {
		if strings.Contains(document, forbidden) {
			t.Errorf("output contains forbidden active-content marker %q", forbidden)
		}
	}
	for _, directive := range []string{"script-src &#39;none&#39;", "connect-src &#39;none&#39;", "object-src &#39;none&#39;"} {
		if !strings.Contains(document, directive) && !strings.Contains(html.UnescapeString(document), strings.ReplaceAll(directive, "&#39;", "'")) {
			t.Errorf("CSP is missing %q", directive)
		}
	}
}

func TestHTMLProjectionHasNoLeakMarkersOrEffectfulImports(t *testing.T) {
	report, err := ProjectHTML(validHTMLProjectionInput())
	if err != nil {
		t.Fatalf("ProjectHTML() error = %v", err)
	}
	encoded, err := EncodeHTML(report)
	if err != nil {
		t.Fatalf("EncodeHTML() error = %v", err)
	}
	document := strings.ToLower(string(encoded))
	for _, marker := range []string{
		"/home/", "/workspaces/", "file://", "ssh://", "bearer ",
		"authorization", "api_key", "secret", "password", "private key",
		"set-cookie", "x-api-key", "configured backend", "provider host",
	} {
		if strings.Contains(document, marker) {
			t.Errorf("output contains leak marker %q", marker)
		}
	}

	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	productionPath := filepath.Join(filepath.Dir(sourcePath), "html_projection.go")
	file, err := parser.ParseFile(token.NewFileSet(), productionPath, nil, 0)
	if err != nil {
		t.Fatalf("parse production source: %v", err)
	}
	allowed := map[string]bool{
		"bytes": true, "crypto/sha256": true, "encoding/base64": true,
		"encoding/hex": true, "encoding/json": true, "errors": true,
		"fmt": true, "html/template": true, "math/big": true,
		"sort": true, "strconv": true, "strings": true,
	}
	for _, importSpec := range file.Imports {
		path := strings.Trim(importSpec.Path.Value, `"`)
		if !allowed[path] {
			t.Errorf("production imports effectful or unreviewed package %q", path)
		}
	}
	if _, err := os.Stat(productionPath); err != nil {
		t.Fatalf("production source disappeared during import audit: %v", err)
	}
}

func TestHTMLProjectionRejectsLegacyAndFutureGeneration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*HTMLReport)
	}{
		{name: "legacy schema", mutate: func(report *HTMLReport) {
			report.SchemaVersion--
		}},
		{name: "future schema", mutate: func(report *HTMLReport) {
			report.SchemaVersion++
		}},
		{name: "legacy template", mutate: func(report *HTMLReport) {
			report.TemplateVersion--
		}},
		{name: "future template", mutate: func(report *HTMLReport) {
			report.TemplateVersion++
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := ProjectHTML(validHTMLProjectionInput())
			if err != nil {
				t.Fatalf("ProjectHTML() error = %v", err)
			}
			test.mutate(&report)
			if _, err := EncodeHTML(report); !errors.Is(err, ErrInvalidHTMLProjection) {
				t.Fatalf("EncodeHTML() error = %v, want generation rejection", err)
			}
		})
	}
}

func TestProjectHTMLRejectsPrivateUnknownAndUnsafeValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*HTMLProjectionInput)
	}{
		{name: "private provenance", mutate: func(input *HTMLProjectionInput) {
			input.Provenance.Privacy = HTMLPrivacyTier("owner_private")
		}},
		{name: "invalid digest", mutate: func(input *HTMLProjectionInput) {
			input.Provenance.ManifestSHA256 = "not-a-digest"
		}},
		{name: "unknown dimension", mutate: func(input *HTMLProjectionInput) {
			input.Lift[0].Dimension = HTMLDimension("<script>alert(1)</script>")
		}},
		{name: "unknown failure code", mutate: func(input *HTMLProjectionInput) {
			input.Failures[0].Failures[0].Code = HTMLFailureCode("unclassified")
		}},
		{name: "runtime safety assertion", mutate: func(input *HTMLProjectionInput) {
			input.Safety.RuntimeSafetyProven = true
		}},
		{name: "metric-kind mismatch", mutate: func(input *HTMLProjectionInput) {
			input.Lift[0].Kind = HTMLDimensionMetric
		}},
		{name: "negative rate", mutate: func(input *HTMLProjectionInput) {
			input.Activation[0].Precision = &HTMLFraction{Numerator: -1, Denominator: 2}
		}},
		{name: "derived activation rate", mutate: func(input *HTMLProjectionInput) {
			input.Activation[0].Precision = htmlTestFraction(1, 2)
		}},
		{name: "derived funnel rate", mutate: func(input *HTMLProjectionInput) {
			input.Funnels[0].Stages[0].Rate = htmlTestFraction(1, 2)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validHTMLProjectionInput()
			test.mutate(&input)
			if _, err := ProjectHTML(input); !errors.Is(err, ErrInvalidHTMLProjection) {
				t.Fatalf("ProjectHTML() error = %v, want ErrInvalidHTMLProjection", err)
			}
		})
	}
}

func TestProjectHTMLRejectsPooledOrIncompleteStrata(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*HTMLProjectionInput)
	}{
		{name: "duplicate coverage stratum", mutate: func(input *HTMLProjectionInput) {
			input.Coverage = append(input.Coverage, input.Coverage[0])
		}},
		{name: "missing activation stratum", mutate: func(input *HTMLProjectionInput) {
			input.Activation = input.Activation[:1]
		}},
		{name: "activation coverage mismatch", mutate: func(input *HTMLProjectionInput) {
			input.Activation[0].Missing++
		}},
		{name: "incomplete coverage marked complete", mutate: func(input *HTMLProjectionInput) {
			input.Coverage[1].Complete = true
		}},
		{name: "zero pair coverage", mutate: func(input *HTMLProjectionInput) {
			input.Coverage[0].CompletePairs = 0
			input.Coverage[0].ExcludedPairs = 0
		}},
		{name: "missing failure taxonomy", mutate: func(input *HTMLProjectionInput) {
			input.Failures[1].Failures = input.Failures[1].Failures[:len(input.Failures[1].Failures)-1]
		}},
		{name: "missing resource axis", mutate: func(input *HTMLProjectionInput) {
			input.Resources = input.Resources[:len(input.Resources)-1]
		}},
		{name: "lift input bound", mutate: func(input *HTMLProjectionInput) {
			input.Lift = make([]HTMLLiftRow, HTMLMaxComparisons+1)
		}},
		{name: "nested funnel bound", mutate: func(input *HTMLProjectionInput) {
			input.Funnels[0].Stages = make([]HTMLFunnelStage, len(htmlStages)+1)
		}},
		{name: "resource reference dominates but candidate is declared", mutate: func(input *HTMLProjectionInput) {
			input.Resources[0].Candidate.P50 = input.Resources[0].Reference.P50 + 1
			input.Resources[0].Candidate.P90 = input.Resources[0].Reference.P90 + 1
		}},
		{name: "resource tradeoff but candidate is declared", mutate: func(input *HTMLProjectionInput) {
			input.Resources[0].Candidate.P50 = input.Resources[0].Reference.P50 - 1
			input.Resources[0].Candidate.P90 = input.Resources[0].Reference.P90 + 1
		}},
		{name: "resource equal but candidate is declared", mutate: func(input *HTMLProjectionInput) {
			input.Resources[0].Candidate.P50 = input.Resources[0].Reference.P50
			input.Resources[0].Candidate.P90 = input.Resources[0].Reference.P90
		}},
		{name: "resource unavailable but candidate is declared", mutate: func(input *HTMLProjectionInput) {
			input.Resources[0].Candidate = HTMLResourceValue{}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validHTMLProjectionInput()
			test.mutate(&input)
			if _, err := ProjectHTML(input); !errors.Is(err, ErrInvalidHTMLProjection) {
				t.Fatalf("ProjectHTML() error = %v, want ErrInvalidHTMLProjection", err)
			}
		})
	}
}

func TestHTMLReportValidationBindsDigest(t *testing.T) {
	report, err := ProjectHTML(validHTMLProjectionInput())
	if err != nil {
		t.Fatalf("ProjectHTML() error = %v", err)
	}
	report.ProjectionSHA256 = strings.Repeat("f", HTMLMaxDigest)
	if err := report.Validate(); !errors.Is(err, ErrInvalidHTMLProjection) {
		t.Fatalf("Validate() error = %v, want ErrInvalidHTMLProjection", err)
	}

	report, err = ProjectHTML(validHTMLProjectionInput())
	if err != nil {
		t.Fatalf("ProjectHTML() error = %v", err)
	}
	report.Lift[0].Effect.Numerator++
	if _, err := EncodeHTML(report); !errors.Is(err, ErrInvalidHTMLProjection) {
		t.Fatalf("EncodeHTML() error = %v, want ErrInvalidHTMLProjection", err)
	}
}

func TestHTMLReportValidationRejectsOversizedSections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*HTMLReport)
	}{
		{name: "activation", mutate: func(report *HTMLReport) {
			report.Activation = make([]HTMLActivationRow, HTMLMaxStrata+1)
		}},
		{name: "funnels", mutate: func(report *HTMLReport) {
			report.Funnels = make([]HTMLFunnelRow, 2*HTMLMaxStrata+1)
		}},
		{name: "failures", mutate: func(report *HTMLReport) {
			report.Failures = make([]HTMLFailureRow, HTMLMaxStrata+1)
		}},
		{name: "resources", mutate: func(report *HTMLReport) {
			report.Resources = make([]HTMLResourceRow, HTMLMaxStrata*len(htmlResourceAxes)+1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := ProjectHTML(validHTMLProjectionInput())
			if err != nil {
				t.Fatalf("ProjectHTML() error = %v", err)
			}
			test.mutate(&report)
			if _, err := EncodeHTML(report); !errors.Is(err, ErrInvalidHTMLProjection) {
				t.Fatalf("EncodeHTML() error = %v, want ErrInvalidHTMLProjection", err)
			}
		})
	}
}

func TestHTMLSafetyStateMappingIsFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*HTMLProjectionInput)
		blocks bool
	}{
		{name: "blocked finding", setup: func(input *HTMLProjectionInput) {
			input.Safety.SecurityStatus = HTMLSafetyBlocked
			input.Safety.SecurityFindingCount = 1
			input.Safety.BlocksExecution = true
		}, blocks: true},
		{name: "incomplete coverage", setup: func(input *HTMLProjectionInput) {
			input.Safety.SecurityStatus = HTMLSafetyIncomplete
			input.Safety.SecurityCoverageComplete = false
			input.Safety.BlocksExecution = true
		}, blocks: true},
		{name: "unavailable security", setup: func(input *HTMLProjectionInput) {
			input.Safety.SecurityStatus = HTMLSafetyUnavailable
			input.Safety.SecurityCoverageComplete = false
			input.Safety.BlocksExecution = true
			input.Provenance.SecurityBundleSHA256 = ""
			input.Provenance.SecurityPolicySHA256 = ""
			input.Provenance.RulePackSHA256 = ""
		}, blocks: true},
		{name: "complete suppressed findings", setup: func(input *HTMLProjectionInput) {
			input.Safety.SecurityStatus = HTMLSafetyCompleteSuppressed
			input.Safety.SecurityFindingCount = 2
			input.Safety.SuppressedFindings = 2
			input.Safety.SecurityCoverageComplete = true
			input.Safety.BlocksExecution = false
		}, blocks: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validHTMLProjectionInput()
			test.setup(&input)
			report, err := ProjectHTML(input)
			if err != nil {
				t.Fatalf("ProjectHTML() error = %v", err)
			}
			if report.Safety.BlocksExecution != test.blocks {
				t.Fatalf("BlocksExecution = %v, want %v", report.Safety.BlocksExecution, test.blocks)
			}
		})
	}
}

func TestProjectHTMLCanonicalizesEquivalentFractions(t *testing.T) {
	canonicalInput := validHTMLProjectionInput()
	canonical, err := ProjectHTML(canonicalInput)
	if err != nil {
		t.Fatalf("ProjectHTML(canonical) error = %v", err)
	}
	if canonical.Lift[0].Effect != (HTMLFraction{Numerator: 1, Denominator: 2}) ||
		canonical.Funnels[0].Stages[0].Rate == nil || *canonical.Funnels[0].Stages[0].Rate != (HTMLFraction{Numerator: 1, Denominator: 1}) {
		t.Fatalf("ProjectHTML did not reduce fractions: lift=%v funnel=%v", canonical.Lift[0].Effect, canonical.Funnels[0].Stages[0].Rate)
	}
	canonicalBytes, err := EncodeHTML(canonical)
	if err != nil {
		t.Fatalf("EncodeHTML(canonical) error = %v", err)
	}

	equivalentInput := validHTMLProjectionInput()
	equivalentInput.Lift[0].Effect = HTMLFraction{Numerator: 2, Denominator: 4}
	equivalentInput.Lift[0].Interval.Lower = HTMLFraction{Numerator: -2, Denominator: 4}
	equivalentInput.Lift[0].Interval.Upper = HTMLFraction{Numerator: 6, Denominator: 8}
	equivalentInput.Activation[0].Precision = htmlTestFraction(14, 16)
	equivalentInput.Funnels[0].Stages[0].Rate = htmlTestFraction(22, 22)
	equivalentInput.Funnels[0].Stages[0].Conversion = htmlTestFraction(22, 22)
	equivalent, err := ProjectHTML(equivalentInput)
	if err != nil {
		t.Fatalf("ProjectHTML(equivalent) error = %v", err)
	}
	equivalentBytes, err := EncodeHTML(equivalent)
	if err != nil {
		t.Fatalf("EncodeHTML(equivalent) error = %v", err)
	}
	if !bytes.Equal(canonicalBytes, equivalentBytes) || canonical.ProjectionSHA256 != equivalent.ProjectionSHA256 {
		t.Fatal("equivalent fractions changed canonical HTML or projection digest")
	}

	mutated := canonical
	mutated.Lift[0].Effect = HTMLFraction{Numerator: 2, Denominator: 4}
	if _, err := EncodeHTML(mutated); err != nil {
		t.Fatalf("EncodeHTML() rejected equivalent caller fraction: %v", err)
	}
}

func TestHTMLRejectsZeroDenominatorWithoutPanic(t *testing.T) {
	invalidReport := func() HTMLReport {
		report, err := ProjectHTML(validHTMLProjectionInput())
		if err != nil {
			t.Fatalf("ProjectHTML(valid) error = %v", err)
		}
		report.Activation[0].Precision = &HTMLFraction{Numerator: 0, Denominator: 0}
		return report
	}
	tests := []struct {
		name string
		call func() error
	}{
		{name: "project", call: func() error {
			input := validHTMLProjectionInput()
			input.Activation[0].Precision = &HTMLFraction{Numerator: 0, Denominator: 0}
			_, err := ProjectHTML(input)
			return err
		}},
		{name: "validate", call: func() error {
			return invalidReport().Validate()
		}},
		{name: "encode", call: func() error {
			_, err := EncodeHTML(invalidReport())
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var err error
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Fatalf("unexpected panic: %v", recovered)
					}
				}()
				err = test.call()
			}()
			if !errors.Is(err, ErrInvalidHTMLProjection) {
				t.Fatalf("error = %v, want ErrInvalidHTMLProjection", err)
			}
		})
	}
}

func validHTMLProjectionInput() HTMLProjectionInput {
	input := HTMLProjectionInput{
		Provenance: HTMLProvenance{
			SourceClass:          HTMLSourcePublicSynthetic,
			Privacy:              HTMLPrivacyPublicSafe,
			ManifestSHA256:       strings.Repeat("a", 64),
			AnalysisPlanSHA256:   strings.Repeat("b", 64),
			InputSetSHA256:       strings.Repeat("c", 64),
			AnalysisReportSHA256: strings.Repeat("d", 64),
			AggregateSHA256:      strings.Repeat("e", 64),
			StructureTreeSHA256:  strings.Repeat("f", 64),
			SecurityBundleSHA256: strings.Repeat("0", 64),
			SecurityPolicySHA256: strings.Repeat("1", 64),
			RulePackSHA256:       strings.Repeat("2", 64),
		},
		Coverage: []HTMLCoverageRow{
			{StratumOrdinal: 1, ExpectedRecords: 10, ReceivedRecords: 10, UniqueRecords: 10, CompletePairs: 8, ExcludedPairs: 2, Complete: true},
			{StratumOrdinal: 2, ExpectedRecords: 12, ReceivedRecords: 14, UniqueRecords: 10, MissingRecords: 2, DuplicateRecords: 4, CompletePairs: 6, ExcludedPairs: 3},
		},
		Activation: []HTMLActivationRow{
			{StratumOrdinal: 1, Observed: 10, TruePositive: 7, FalsePositive: 1, TrueNegative: 1, FalseNegative: 1, Precision: htmlTestFraction(7, 8), Recall: htmlTestFraction(7, 8), FalseActivation: htmlTestFraction(1, 2), UnnecessaryLoad: htmlTestFraction(1, 8)},
			{StratumOrdinal: 2, Observed: 8, Missing: 4, TruePositive: 4, FalsePositive: 1, TrueNegative: 2, FalseNegative: 1, Precision: htmlTestFraction(4, 5), Recall: htmlTestFraction(4, 5), FalseActivation: htmlTestFraction(1, 3), UnnecessaryLoad: htmlTestFraction(1, 5)},
		},
		Lift: []HTMLLiftRow{
			{ComparisonOrdinal: 1, StratumOrdinal: 1, Kind: HTMLDimensionStage, Dimension: HTMLDimensionCandidateRecall, Status: HTMLInferenceInferential, CompletePairs: 8, ExcludedPairs: 2, Effect: HTMLFraction{Numerator: 1, Denominator: 2}, Interval: &HTMLInterval{ConfidenceBasisPoints: 9500, Lower: HTMLFraction{Numerator: -1, Denominator: 2}, Upper: HTMLFraction{Numerator: 3, Denominator: 4}}, Pareto: HTMLParetoCandidateDominates},
			{ComparisonOrdinal: 1, StratumOrdinal: 2, Kind: HTMLDimensionMetric, Dimension: HTMLDimensionOutcome, Status: HTMLInferenceDescriptive, CompletePairs: 6, ExcludedPairs: 3, Effect: HTMLFraction{Numerator: -1, Denominator: 4}, Regression: true, Pareto: HTMLParetoTradeoff},
			{ComparisonOrdinal: 2, StratumOrdinal: 1, Kind: HTMLDimensionMetric, Dimension: HTMLDimensionDuration, Status: HTMLInferenceInsufficient, ExcludedPairs: 2, Effect: HTMLFraction{Denominator: 1}, Pareto: HTMLParetoUnavailable},
		},
		Safety: HTMLSafetySummary{StructureStatus: HTMLSafetyAdmitted, SecurityStatus: HTMLSafetyClean, SecurityCoverageComplete: true},
	}
	for _, stratum := range []uint32{1, 2} {
		input.Funnels = append(input.Funnels, htmlTestFunnel(stratum, HTMLRoleReference, 10+stratum))
		input.Funnels = append(input.Funnels, htmlTestFunnel(stratum, HTMLRoleCandidate, 10+stratum))
		failures := make([]HTMLFailureCount, 0, len(htmlFailureCodes))
		for index, code := range htmlFailureCodes {
			failures = append(failures, HTMLFailureCount{Code: code, Count: uint32(index + int(stratum))})
		}
		input.Failures = append(input.Failures, HTMLFailureRow{StratumOrdinal: stratum, Failures: failures})
		for index, axis := range htmlResourceAxes {
			input.Resources = append(input.Resources, HTMLResourceRow{
				StratumOrdinal: stratum,
				Axis:           axis,
				Reference:      HTMLResourceValue{Available: true, ObservedRuns: 10, P50: int64(index + 10), P90: int64(index + 20)},
				Candidate:      HTMLResourceValue{Available: true, ObservedRuns: 10, P50: int64(index + 8), P90: int64(index + 18)},
				Pareto:         HTMLParetoCandidateDominates,
			})
		}
	}
	return input
}

func htmlTestFunnel(stratum uint32, role HTMLTreatmentRole, trials uint32) HTMLFunnelRow {
	stages := make([]HTMLFunnelStage, len(htmlStages))
	for index, stage := range htmlStages {
		observed := trials - uint32(index)
		stages[index] = HTMLFunnelStage{
			Stage: stage, Observed: observed, Reached: observed,
			EligibleTransitions: observed, Converted: observed,
			Rate: htmlTestFraction(int64(observed), uint64(observed)), Conversion: htmlTestFraction(int64(observed), uint64(observed)),
		}
	}
	return HTMLFunnelRow{StratumOrdinal: stratum, Role: role, Trials: trials, Stages: stages}
}

func htmlTestFraction(numerator int64, denominator uint64) *HTMLFraction {
	return &HTMLFraction{Numerator: numerator, Denominator: denominator}
}

func reverseHTML[T any](values []T) []T {
	result := append([]T(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}
