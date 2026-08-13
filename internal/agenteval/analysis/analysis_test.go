package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/agenteval/experiment"
)

func TestAnalyzeHandCalculatedPairedStatisticsAndMultiAxisViews(t *testing.T) {
	manifest := testManifest(t, 6, 4, 100, []uint32{1, 2, 3})
	records := testRecords(t, manifest)
	report, err := Analyze(manifest, records)
	if err != nil {
		t.Fatal(err)
	}
	if report.Coverage.CompletePairs != 6 || report.Coverage.ExcludedPairs != 0 || len(report.Comparisons) != 1 {
		t.Fatalf("coverage=%+v comparisons=%d", report.Coverage, len(report.Comparisons))
	}
	comparison := report.Comparisons[0]
	load := findBinary(t, comparison, DimensionStage, string(experiment.StageLoad))
	if load.BothFalse != 0 || load.ReferenceOnly != 0 || load.CandidateOnly != 6 || load.BothTrue != 0 ||
		load.RiskDifference != (Rational{Numerator: "1", Denominator: "1"}) ||
		load.ExactTest == nil || load.ExactTest.RawProbability != (Rational{Numerator: "1", Denominator: "32"}) ||
		load.ExactTest.AdjustedProbability == nil || *load.ExactTest.AdjustedProbability != (Rational{Numerator: "1", Denominator: "16"}) ||
		load.ExactTest.RejectNull == nil || *load.ExactTest.RejectNull {
		t.Fatalf("load=%+v", load)
	}
	if len(load.Pairs) != 6 {
		t.Fatalf("load pairs=%+v", load.Pairs)
	}
	for _, pair := range load.Pairs {
		if pair.Reference || !pair.Candidate {
			t.Fatalf("load pair=%+v", pair)
		}
	}
	outcome := findBinary(t, comparison, DimensionMetric, string(experiment.MetricOutcome))
	if outcome.BothFalse != 1 || outcome.ReferenceOnly != 1 || outcome.CandidateOnly != 3 || outcome.BothTrue != 1 ||
		outcome.RiskDifference != (Rational{Numerator: "1", Denominator: "3"}) ||
		outcome.ExactTest == nil || outcome.ExactTest.RawProbability != (Rational{Numerator: "5", Denominator: "8"}) ||
		outcome.ExactTest.AdjustedProbability == nil || *outcome.ExactTest.AdjustedProbability != (Rational{Numerator: "5", Denominator: "8"}) {
		t.Fatalf("outcome=%+v", outcome)
	}
	duration := findContinuous(t, comparison, experiment.MetricDurationMillis)
	if load.Interval == nil || load.Interval.Lower != (Rational{Numerator: "1", Denominator: "1"}) ||
		load.Interval.Upper != (Rational{Numerator: "1", Denominator: "1"}) || outcome.Interval == nil ||
		outcome.Interval.Lower != (Rational{Numerator: "-1", Denominator: "3"}) ||
		outcome.Interval.Upper != (Rational{Numerator: "1", Denominator: "1"}) || duration.Interval == nil ||
		duration.Interval.Lower != (Rational{Numerator: "-25", Denominator: "6"}) ||
		duration.Interval.Upper != (Rational{Numerator: "10", Denominator: "1"}) {
		t.Fatalf("intervals load=%+v outcome=%+v duration=%+v", load.Interval, outcome.Interval, duration.Interval)
	}
	if duration.MeanDelta != (Rational{Numerator: "10", Denominator: "3"}) ||
		duration.MedianDelta != (Rational{Numerator: "5", Denominator: "2"}) ||
		duration.PairedSignEffect != (Rational{Numerator: "1", Denominator: "6"}) ||
		duration.CandidateHigher != 3 || duration.ReferenceHigher != 2 || duration.Equal != 1 ||
		duration.DirectionAdjusted != (Rational{Numerator: "-10", Denominator: "3"}) || !duration.Regression {
		t.Fatalf("duration=%+v", duration)
	}
	deltaValues := make([]string, len(duration.Deltas))
	for index, delta := range duration.Deltas {
		if index > 0 && duration.Deltas[index-1].PairID >= delta.PairID || delta.Delta.Denominator != "1" {
			t.Fatalf("noncanonical pair deltas=%+v", duration.Deltas)
		}
		deltaValues[index] = delta.Delta.Numerator
	}
	sort.Strings(deltaValues)
	if !reflect.DeepEqual(deltaValues, []string{"-10", "-5", "0", "10", "20", "5"}) {
		t.Fatalf("pair deltas=%v", deltaValues)
	}
	if comparison.Pareto != ParetoTradeoff {
		t.Fatalf("pareto=%s", comparison.Pareto)
	}
	if len(report.Activation) != 1 {
		t.Fatalf("activation strata=%d", len(report.Activation))
	}
	activation := report.Activation[0]
	if activation.TruePositive != 6 || activation.TrueNegative != 6 ||
		activation.FalsePositive != 0 || activation.FalseNegative != 0 ||
		activation.Precision == nil || *activation.Precision != (Rational{Numerator: "1", Denominator: "1"}) ||
		activation.Recall == nil || *activation.Recall != (Rational{Numerator: "1", Denominator: "1"}) {
		t.Fatalf("activation=%+v", activation)
	}
	candidate := treatmentByRole(t, manifest, experiment.RoleCandidate)
	pass := findPass(t, report, candidate.ID, 2)
	if pass.PassAtK == nil || *pass.PassAtK != (Rational{Numerator: "14", Denominator: "15"}) ||
		pass.PassPowerK == nil || *pass.PassPowerK != (Rational{Numerator: "2", Denominator: "5"}) {
		t.Fatalf("pass=%+v", pass)
	}
	if err := ValidateReportForManifest(manifest, report); err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeReport(report, manifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeReport(bytes.NewReader(encoded), manifest)
	if err != nil || !reflect.DeepEqual(decoded, report) {
		t.Fatalf("decode err=%v equal=%v", err, reflect.DeepEqual(decoded, report))
	}
	otherCapability := manifest.CapabilityContract
	otherCapability.Runtime.ModelSHA256 = testDigest("other-model")
	otherCapability, err = experiment.SealCapabilityContract(otherCapability)
	if err != nil {
		t.Fatal(err)
	}
	otherDesign := manifest.Design
	otherDesign.CapabilityContractSHA256 = otherCapability.CapabilityContractSHA256
	otherDesign, err = experiment.SealDesign(otherDesign)
	if err != nil {
		t.Fatal(err)
	}
	otherManifest, err := experiment.Compile(otherDesign, otherCapability, manifest.AnalysisPlan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(bytes.NewReader(encoded), otherManifest); err == nil {
		t.Fatal("report decoded against a different valid manifest")
	}
}

func TestAnalyzeIsInputOrderInvariantAndBindsMultiplicity(t *testing.T) {
	manifest := testManifest(t, 6, 4, 100, []uint32{1, 2, 3})
	records := testRecords(t, manifest)
	first, err := Analyze(manifest, records)
	if err != nil {
		t.Fatal(err)
	}
	shuffled := append([]experiment.TrialRecord{}, records...)
	rand.New(rand.NewSource(7)).Shuffle(len(shuffled), func(left, right int) {
		shuffled[left], shuffled[right] = shuffled[right], shuffled[left]
	})
	second, err := Analyze(manifest, shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("input order changed report bytes")
	}
	permutedDesign := manifest.Design
	permutedDesign.Treatments = append([]experiment.TreatmentRequest{}, manifest.Design.Treatments...)
	for left, right := 0, len(permutedDesign.Treatments)-1; left < right; left, right = left+1, right-1 {
		permutedDesign.Treatments[left], permutedDesign.Treatments[right] = permutedDesign.Treatments[right], permutedDesign.Treatments[left]
	}
	sealedDesign, err := experiment.SealDesign(permutedDesign)
	if err != nil {
		t.Fatal(err)
	}
	permutedManifest, err := experiment.Compile(sealedDesign, manifest.CapabilityContract, manifest.AnalysisPlan)
	if err != nil || !reflect.DeepEqual(permutedManifest, manifest) {
		t.Fatalf("treatment order changed manifest err=%v equal=%t", err, reflect.DeepEqual(permutedManifest, manifest))
	}
	permutedReport, err := Analyze(permutedManifest, records)
	if err != nil || !reflect.DeepEqual(permutedReport, first) {
		t.Fatalf("treatment order changed report err=%v equal=%t", err, reflect.DeepEqual(permutedReport, first))
	}
	duplicate := append([]experiment.TrialRecord{}, records[1:]...)
	duplicate = append(duplicate, records[1])
	third, err := Analyze(manifest, duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if third.InputSetSHA256 == first.InputSetSHA256 || third.Coverage.MissingRecords != 1 || third.Coverage.DuplicateRecords != 1 {
		t.Fatalf("input=%s coverage=%+v", third.InputSetSHA256, third.Coverage)
	}
	statuses := map[PairStatus]uint32{}
	for _, pair := range third.Coverage.Pairs {
		statuses[pair.Status]++
	}
	if statuses[PairMissing] != 0 || statuses[PairDuplicate] != 1 || statuses[PairComplete] != 5 {
		t.Fatalf("statuses=%v", statuses)
	}
	for _, pair := range third.Coverage.Pairs {
		if pair.Status == PairDuplicate && (!containsReason(pair.Reasons, experiment.ExclusionDuplicateMember) || !containsReason(pair.Reasons, experiment.ExclusionMissingMember)) {
			t.Fatalf("duplicate pair reasons=%v", pair.Reasons)
		}
	}
}

func TestAnalyzeCoveragePartitionProperties(t *testing.T) {
	manifest := testManifest(t, 6, 4, 100, []uint32{1, 2, 3})
	canonical := testRecords(t, manifest)
	random := rand.New(rand.NewSource(29))
	for iteration := 0; iteration < 32; iteration++ {
		records := make([]experiment.TrialRecord, 0, len(canonical)*2)
		for _, record := range canonical {
			for copies := random.Intn(3); copies > 0; copies-- {
				records = append(records, record)
			}
		}
		random.Shuffle(len(records), func(left, right int) { records[left], records[right] = records[right], records[left] })
		report, err := Analyze(manifest, records)
		if err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		coverage := report.Coverage
		if coverage.UniqueRecords+coverage.MissingRecords != coverage.ExpectedRecords ||
			coverage.UniqueRecords+coverage.DuplicateRecords != coverage.ReceivedRecords ||
			coverage.CompletePairs+coverage.ExcludedPairs != uint32(len(coverage.Pairs)) {
			t.Fatalf("iteration %d partition=%+v", iteration, coverage)
		}
		reshuffled := append([]experiment.TrialRecord{}, records...)
		random.Shuffle(len(reshuffled), func(left, right int) { reshuffled[left], reshuffled[right] = reshuffled[right], reshuffled[left] })
		second, err := Analyze(manifest, reshuffled)
		if err != nil || !reflect.DeepEqual(second, report) {
			t.Fatalf("iteration %d reorder err=%v equal=%t", iteration, err, reflect.DeepEqual(second, report))
		}
	}
}

func TestAnalyzeNeverPoolsDeclaredRandomizationStrata(t *testing.T) {
	manifest := testManifestWithStrata(t, 2, 100, []uint32{1, 2}, []experiment.StratumRequest{
		{BindingSHA256: testDigest("stratum-a"), Blocks: 4},
		{BindingSHA256: testDigest("stratum-b"), Blocks: 4},
	})
	records := testRecords(t, manifest)
	report, err := Analyze(manifest, records)
	if err != nil {
		t.Fatal(err)
	}
	if report.Coverage.CompletePairs != 8 || len(report.Comparisons) != 2 || len(report.Activation) != 2 ||
		len(report.Funnels) != 4 || len(report.PassAtK) != 8 {
		t.Fatalf("coverage=%+v comparisons=%d activation=%d funnels=%d pass=%d", report.Coverage,
			len(report.Comparisons), len(report.Activation), len(report.Funnels), len(report.PassAtK))
	}
	seenStrata := map[string]bool{}
	for _, comparison := range report.Comparisons {
		if comparison.CompletePairs != 4 || seenStrata[comparison.StratumID] {
			t.Fatalf("comparison=%+v", comparison)
		}
		seenStrata[comparison.StratumID] = true
	}
	firstOutcome := findBinary(t, report.Comparisons[0], DimensionMetric, string(experiment.MetricOutcome))
	secondOutcome := findBinary(t, report.Comparisons[1], DimensionMetric, string(experiment.MetricOutcome))
	if len(seenStrata) != 2 || firstOutcome.RiskDifference == secondOutcome.RiskDifference {
		t.Fatalf("stratified comparisons=%+v", report.Comparisons)
	}
	shuffled := append([]experiment.TrialRecord{}, records...)
	rand.New(rand.NewSource(17)).Shuffle(len(shuffled), func(left, right int) {
		shuffled[left], shuffled[right] = shuffled[right], shuffled[left]
	})
	second, err := Analyze(manifest, shuffled)
	if err != nil || !reflect.DeepEqual(second, report) {
		t.Fatalf("stratified reorder err=%v equal=%t", err, reflect.DeepEqual(second, report))
	}
}

func TestAnalyzeRejectsAggregateOnlyThresholdsWithoutNarrowingManifestV1(t *testing.T) {
	tests := []struct {
		name    string
		minimum uint32
		k       []uint32
	}{
		{name: "minimum inference blocks", minimum: 3, k: []uint32{1}},
		{name: "pass at k", minimum: 2, k: []uint32{1, 3}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := testManifestWithStrata(t, test.minimum, 100, test.k, []experiment.StratumRequest{
				{BindingSHA256: testDigest("stratum-a"), Blocks: 2},
				{BindingSHA256: testDigest("stratum-b"), Blocks: 2},
			})
			// Manifest v1 predates the stratum-preserving analysis consumer and
			// remains valid/readable under its aggregate-roster contract.
			if err := experiment.ValidateManifest(manifest); err != nil {
				t.Fatalf("manifest compatibility: %v", err)
			}
			_, err := Analyze(manifest, nil)
			if code, ok := CodeOf(err); !ok || code != ErrorInvalidInput {
				t.Fatalf("analysis err=%v code=%s", err, code)
			}
		})
	}
}

func TestAnalyzePreservesDescriptiveAndCoverageSemantics(t *testing.T) {
	manifest := testManifest(t, 2, 2, 100, []uint32{1})
	records := testRecords(t, manifest)
	records = records[:2]
	report, err := Analyze(manifest, records)
	if err != nil {
		t.Fatal(err)
	}
	if report.Coverage.CompletePairs != 1 || report.Coverage.ExcludedPairs != 1 {
		t.Fatalf("coverage=%+v", report.Coverage)
	}
	for _, result := range report.Comparisons[0].Binary {
		if result.Status != InferenceDescriptive || result.Interval != nil || result.ExactTest != nil {
			t.Fatalf("binary=%+v", result)
		}
	}
	for _, result := range report.Comparisons[0].Continuous {
		if result.Status != InferenceDescriptive || result.Interval != nil || result.ExactTest != nil {
			t.Fatalf("continuous=%+v", result)
		}
	}
	for _, pass := range report.PassAtK {
		if pass.Status != InferenceInsufficient || pass.PassAtK != nil || pass.PassPowerK != nil {
			t.Fatalf("pass=%+v", pass)
		}
	}
}

func TestAnalyzeSeparatesFalseActivationAndUnnecessaryLoadRates(t *testing.T) {
	manifest := testManifest(t, 2, 2, 100, []uint32{1})
	records := testRecords(t, manifest)
	expectedActivation := map[string]bool{}
	for _, treatment := range manifest.Treatments {
		expectedActivation[treatment.ID] = treatment.ExpectedActivation
	}
	mutated := false
	for index := range records {
		if expectedActivation[records[index].TreatmentID] {
			continue
		}
		for stageIndex := range records[index].Stages {
			if records[index].Stages[stageIndex].Stage == experiment.StageLoad {
				records[index].Stages[stageIndex].Value = boolPointer(true)
				sealed, err := experiment.SealTrialRecord(manifest, records[index])
				if err != nil {
					t.Fatal(err)
				}
				records[index] = sealed
				mutated = true
				break
			}
		}
		if mutated {
			break
		}
	}
	if !mutated {
		t.Fatal("missing non-activating treatment record")
	}
	report, err := Analyze(manifest, records)
	if err != nil {
		t.Fatal(err)
	}
	activation := report.Activation[0]
	if activation.FalsePositive != 1 || activation.TrueNegative != 1 || activation.TruePositive != 2 ||
		activation.FalseActivationRate == nil || *activation.FalseActivationRate != (Rational{Numerator: "1", Denominator: "2"}) ||
		activation.UnnecessaryLoadRate == nil || *activation.UnnecessaryLoadRate != (Rational{Numerator: "1", Denominator: "3"}) {
		t.Fatalf("activation=%+v", activation)
	}
}

func TestAnalyzePreservesMaximumMetricPrecisionAndRefusesPrimitiveOverflow(t *testing.T) {
	manifest := testManifest(t, 2, 2, 100, []uint32{1})
	records := testRecords(t, manifest)
	roles := map[string]experiment.TreatmentRole{}
	for _, treatment := range manifest.Treatments {
		roles[treatment.ID] = treatment.Role
	}
	for index := range records {
		for metricIndex := range records[index].Metrics {
			if records[index].Metrics[metricIndex].Metric != experiment.MetricDurationMillis {
				continue
			}
			value := uint64(0)
			if roles[records[index].TreatmentID] == experiment.RoleCandidate {
				value = experiment.MaxMetricValue
			}
			records[index].Metrics[metricIndex].Value = uint64Pointer(value)
		}
		sealed, err := experiment.SealTrialRecord(manifest, records[index])
		if err != nil {
			t.Fatal(err)
		}
		records[index] = sealed
	}
	report, err := Analyze(manifest, records)
	if err != nil {
		t.Fatal(err)
	}
	duration := findContinuous(t, report.Comparisons[0], experiment.MetricDurationMillis)
	want := Rational{Numerator: "9007199254740991", Denominator: "1"}
	if duration.MeanDelta != want || duration.MedianDelta != want || duration.DirectionAdjusted != (Rational{Numerator: "-9007199254740991", Denominator: "1"}) {
		t.Fatalf("duration=%+v", duration)
	}

	oversized := testManifest(t, 512, 2, experiment.MaxBootstrapSamples, []uint32{1})
	_, err = Analyze(oversized, testRecords(t, oversized))
	if code, ok := CodeOf(err); !ok || code != ErrorLimitExceeded {
		t.Fatalf("overflow err=%v code=%s", err, code)
	}
}

func TestAnalyzeAdmitsMaximumBoundedPassAtKRoster(t *testing.T) {
	strata := make([]experiment.StratumRequest, experiment.MaxStrata/2)
	for index := range strata {
		strata[index] = experiment.StratumRequest{BindingSHA256: testDigest("stratum-limit-" + strconv.Itoa(index)), Blocks: experiment.MaxPassK}
	}
	k := make([]uint32, experiment.MaxPassK)
	for index := range k {
		k[index] = uint32(index + 1)
	}
	manifest := testManifestWithStrata(t, 2, 100, k, strata)
	report, err := Analyze(manifest, nil)
	if err != nil || len(report.PassAtK) != MaxPassAtKResults {
		t.Fatalf("bounded roster err=%v pass=%d", err, len(report.PassAtK))
	}
}

func TestAnalysisCodecRejectsSemanticAndEncodingDrift(t *testing.T) {
	manifest := testManifest(t, 2, 2, 100, []uint32{1})
	report, err := Analyze(manifest, testRecords(t, manifest))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeReport(report, manifest)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string][]byte{
		"future":         bytes.Replace(encoded, []byte("\"schema_version\":1"), []byte("\"schema_version\":2"), 1),
		"unknown":        bytes.Replace(encoded, []byte("\"schema_version\":1"), []byte("\"schema_version\":1,\"extra\":true"), 1),
		"duplicate":      bytes.Replace(encoded, []byte("\"schema_version\":1"), []byte("\"schema_version\":1,\"schema_version\":1"), 1),
		"case_alias":     bytes.Replace(encoded, []byte("\"comparisons\":"), []byte("\"Comparisons\":"), 1),
		"case_duplicate": bytes.Replace(encoded, []byte("\"comparisons\":"), []byte("\"Comparisons\":[],\"comparisons\":"), 1),
		"crlf":           bytes.ReplaceAll(encoded, []byte("\n"), []byte("\r\n")),
		"trailing":       append(append([]byte{}, encoded...), []byte("{}\n")...),
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeReport(bytes.NewReader(mutation), manifest); err == nil {
				t.Fatal("accepted mutation")
			}
		})
	}
	drift := cloneReport(report)
	drift.Comparisons[0].Binary[0].CandidateOnly--
	drift.ReportSHA256 = reportDigest(drift)
	if err := ValidateReportForManifest(manifest, drift); err == nil {
		t.Fatal("accepted semantic drift")
	}
	stratumDrift := cloneReport(report)
	stratumDrift.Comparisons[0].StratumID = "stratum-" + strings.Repeat("f", 64)
	stratumDrift.ReportSHA256 = reportDigest(stratumDrift)
	if err := ValidateReportForManifest(manifest, stratumDrift); err == nil {
		t.Fatal("accepted comparison outside declared pair coverage")
	}
	deltaDrift := cloneReport(report)
	deltaDrift.Comparisons[0].Continuous[0].Deltas[0].Delta = Rational{Numerator: "99", Denominator: "1"}
	deltaDrift.ReportSHA256 = reportDigest(deltaDrift)
	if err := ValidateReportForManifest(manifest, deltaDrift); err == nil {
		t.Fatal("accepted continuous summary detached from pair deltas")
	}
	pairOrderDrift := cloneReport(report)
	pairOrderDrift.Coverage.Pairs[0], pairOrderDrift.Coverage.Pairs[1] = pairOrderDrift.Coverage.Pairs[1], pairOrderDrift.Coverage.Pairs[0]
	pairOrderDrift.ReportSHA256 = reportDigest(pairOrderDrift)
	if err := ValidateReportForManifest(manifest, pairOrderDrift); err == nil {
		t.Fatal("accepted noncanonical pair coverage order")
	}
	binaryIntervalDrift := cloneReport(report)
	binaryIntervalDrift.Comparisons[0].Binary[0].Interval.Lower = Rational{Numerator: "99", Denominator: "1"}
	binaryIntervalDrift.Comparisons[0].Binary[0].Interval.Upper = Rational{Numerator: "99", Denominator: "1"}
	binaryIntervalDrift.ReportSHA256 = reportDigest(binaryIntervalDrift)
	if err := ValidateReportForManifest(manifest, binaryIntervalDrift); err == nil {
		t.Fatal("accepted binary interval detached from retained pair deltas")
	}
	binaryCellDrift := cloneReport(report)
	outcomeIndex := -1
	for index := range binaryCellDrift.Comparisons[0].Binary {
		if binaryCellDrift.Comparisons[0].Binary[index].ID == string(experiment.MetricOutcome) {
			outcomeIndex = index
			break
		}
	}
	if outcomeIndex < 0 {
		t.Fatal("missing outcome binary")
	}
	mutatedCell := false
	for index := range binaryCellDrift.Comparisons[0].Binary[outcomeIndex].Pairs {
		pair := &binaryCellDrift.Comparisons[0].Binary[outcomeIndex].Pairs[index]
		if pair.Reference == pair.Candidate {
			pair.Reference = !pair.Reference
			pair.Candidate = !pair.Candidate
			mutatedCell = true
			break
		}
	}
	if !mutatedCell {
		t.Fatal("test fixture has no equal binary pair")
	}
	binaryCellDrift.ReportSHA256 = reportDigest(binaryCellDrift)
	if err := ValidateReportForManifest(manifest, binaryCellDrift); err == nil {
		t.Fatal("accepted both-false/both-true drift with the same signed delta")
	}
	continuousIntervalDrift := cloneReport(report)
	continuousIntervalDrift.Comparisons[0].Continuous[0].Interval.Lower = Rational{Numerator: "99", Denominator: "1"}
	continuousIntervalDrift.Comparisons[0].Continuous[0].Interval.Upper = Rational{Numerator: "99", Denominator: "1"}
	continuousIntervalDrift.ReportSHA256 = reportDigest(continuousIntervalDrift)
	if err := ValidateReportForManifest(manifest, continuousIntervalDrift); err == nil {
		t.Fatal("accepted continuous interval detached from retained pair deltas")
	}
	malformedContinuous := cloneReport(report)
	malformedContinuous.Comparisons[0].Continuous[0].MeanDelta = Rational{Numerator: "not-an-integer", Denominator: "1"}
	malformedContinuous.ReportSHA256 = reportDigest(malformedContinuous)
	malformedData, err := json.Marshal(malformedContinuous)
	if err != nil {
		t.Fatal(err)
	}
	malformedData = append(malformedData, '\n')
	if _, err := DecodeReport(bytes.NewReader(malformedData), manifest); err == nil {
		t.Fatal("decoded a malformed continuous rational")
	} else if code, ok := CodeOf(err); !ok || code != ErrorInvalidReport {
		t.Fatalf("malformed continuous rational err=%v code=%s", err, code)
	}
	invalidK := cloneReport(report)
	invalidK.PassAtK[0].K = 1000
	invalidK.ReportSHA256 = reportDigest(invalidK)
	invalidKData, err := json.Marshal(invalidK)
	if err != nil {
		t.Fatal(err)
	}
	invalidKData = append(invalidKData, '\n')
	if _, err := DecodeReport(bytes.NewReader(invalidKData), manifest); err == nil {
		t.Fatal("decoded pass-at-k outside the closed domain")
	} else if code, ok := CodeOf(err); !ok || code != ErrorInvalidReport {
		t.Fatalf("invalid k err=%v code=%s", err, code)
	}
	truncatedPass := cloneReport(report)
	truncatedPass.PassAtK[0].Attempts = 1
	truncatedPass.PassAtK[0].Passed = 1
	pass, power, ok := passEstimators(1, 1, truncatedPass.PassAtK[0].K)
	if !ok {
		t.Fatal("test pass estimator")
	}
	truncatedPass.PassAtK[0].PassAtK = &pass
	truncatedPass.PassAtK[0].PassPowerK = &power
	truncatedPass.PassAtK[0].Status = InferenceDescriptive
	truncatedPass.ReportSHA256 = reportDigest(truncatedPass)
	if err := ValidateReportForManifest(manifest, truncatedPass); err == nil {
		t.Fatal("accepted fixed-roster estimate from a truncated attempt roster")
	}
	missingCompletePass := cloneReport(report)
	missingCompletePass.PassAtK[0].PassAtK = nil
	missingCompletePass.PassAtK[0].PassPowerK = nil
	missingCompletePass.PassAtK[0].Status = InferenceInsufficient
	missingCompletePass.ReportSHA256 = reportDigest(missingCompletePass)
	if err := ValidateReportForManifest(manifest, missingCompletePass); err == nil {
		t.Fatal("accepted an omitted estimator for a complete fixed roster")
	}
	rosterManifest := testManifest(t, 2, 2, 100, []uint32{1, 2})
	inconsistentRoster, err := Analyze(rosterManifest, testRecords(t, rosterManifest))
	if err != nil {
		t.Fatal(err)
	}
	rosterMutated := false
	for first := range inconsistentRoster.PassAtK {
		for second := first + 1; second < len(inconsistentRoster.PassAtK); second++ {
			left, right := inconsistentRoster.PassAtK[first], &inconsistentRoster.PassAtK[second]
			if left.StratumID != right.StratumID || left.TreatmentID != right.TreatmentID || right.Passed == 0 {
				continue
			}
			right.Passed--
			pass, power, ok := passEstimators(right.Attempts, right.Passed, right.K)
			if !ok {
				t.Fatal("test inconsistent-roster estimator")
			}
			right.PassAtK, right.PassPowerK = &pass, &power
			rosterMutated = true
			break
		}
		if rosterMutated {
			break
		}
	}
	if !rosterMutated {
		t.Fatal("test fixture lacks repeated k rows with a passed attempt")
	}
	inconsistentRoster.ReportSHA256 = reportDigest(inconsistentRoster)
	if err := ValidateReportForManifest(rosterManifest, inconsistentRoster); err == nil {
		t.Fatal("accepted inconsistent fixed-roster counts across preregistered k rows")
	}
	coverageContradiction := cloneReport(report)
	coverageContradiction.Coverage.UniqueRecords--
	coverageContradiction.Coverage.ReceivedRecords--
	coverageContradiction.Coverage.MissingRecords++
	coverageContradiction.ReportSHA256 = reportDigest(coverageContradiction)
	if err := ValidateReportForManifest(manifest, coverageContradiction); err == nil {
		t.Fatal("accepted complete-pair and summary observations beyond unique record coverage")
	}
	memberContradiction := cloneReport(report)
	memberContradiction.Coverage.Members[0].Records = 0
	memberContradiction.Coverage.Members[1].Records = 2
	memberContradiction.Coverage.UniqueRecords--
	memberContradiction.Coverage.MissingRecords++
	memberContradiction.Coverage.DuplicateRecords++
	memberContradiction.ReportSHA256 = reportDigest(memberContradiction)
	if err := ValidateReportForManifest(manifest, memberContradiction); err == nil {
		t.Fatal("accepted pair and summary claims detached from exact trial multiplicities")
	}
	retainedStageDrift := cloneReport(report)
	stageMutated := false
	for funnelIndex := range retainedStageDrift.Funnels {
		funnel := &retainedStageDrift.Funnels[funnelIndex]
		if treatmentByRole(t, manifest, experiment.RoleReference).ID != funnel.TreatmentID {
			continue
		}
		for stageIndex := range retainedStageDrift.Funnels[funnelIndex].Stages {
			stage := &funnel.Stages[stageIndex]
			if stage.Stage != experiment.StageLoad || stage.Observed == 0 || stage.Reached != 0 {
				continue
			}
			removed := stage.Observed
			stage.Observed, stage.Reached, stage.EligibleTransitions, stage.Converted = 0, 0, 0, 0
			stage.Rate, stage.Conversion = nil, nil
			for summaryIndex := range retainedStageDrift.Activation {
				summary := &retainedStageDrift.Activation[summaryIndex]
				if summary.StratumID != funnel.StratumID {
					continue
				}
				summary.Observed -= removed
				summary.Missing += removed
				summary.TrueNegative -= removed
				recomputeActivationRatios(summary)
			}
			stageMutated = true
			break
		}
		if stageMutated {
			break
		}
	}
	if !stageMutated {
		t.Fatal("test fixture lacks a retained reference load-stage observation")
	}
	retainedStageDrift.ReportSHA256 = reportDigest(retainedStageDrift)
	if err := ValidateReportForManifest(manifest, retainedStageDrift); err == nil {
		t.Fatal("accepted a funnel summary below retained complete-pair stage observations")
	}
	retainedOutcomeDrift := cloneReport(rosterManifestReport(t, rosterManifest))
	treatmentID := retainedOutcomeDrift.PassAtK[0].TreatmentID
	for index := range retainedOutcomeDrift.PassAtK {
		row := &retainedOutcomeDrift.PassAtK[index]
		if row.TreatmentID != treatmentID {
			continue
		}
		row.Attempts, row.Passed = 0, 0
		row.PassAtK, row.PassPowerK = nil, nil
		row.Status = InferenceInsufficient
	}
	retainedOutcomeDrift.ReportSHA256 = reportDigest(retainedOutcomeDrift)
	if err := ValidateReportForManifest(rosterManifest, retainedOutcomeDrift); err == nil {
		t.Fatal("accepted pass-at-k attempts below retained complete-pair outcome observations")
	}
	funnelDrift := cloneReport(report)
	funnelDrift.Funnels[0].Stages[0].EligibleTransitions = 0
	funnelDrift.Funnels[0].Stages[0].Converted = 0
	funnelDrift.Funnels[0].Stages[0].Conversion = nil
	funnelDrift.ReportSHA256 = reportDigest(funnelDrift)
	if err := ValidateReportForManifest(manifest, funnelDrift); err == nil {
		t.Fatal("accepted impossible first-stage funnel transitions")
	}
	transitionDrift := cloneReport(report)
	transitionMutated := false
	for funnelIndex := range transitionDrift.Funnels {
		if transitionDrift.Funnels[funnelIndex].Stages[1].EligibleTransitions == 0 ||
			transitionDrift.Funnels[funnelIndex].Stages[0].Observed == 0 {
			continue
		}
		first := &transitionDrift.Funnels[funnelIndex].Stages[0]
		first.Reached = 0
		first.Converted = 0
		zero := Rational{Numerator: "0", Denominator: "1"}
		first.Rate = &zero
		first.Conversion = &zero
		transitionMutated = true
		break
	}
	if !transitionMutated {
		t.Fatal("test fixture lacks an observed funnel transition")
	}
	transitionDrift.ReportSHA256 = reportDigest(transitionDrift)
	if err := ValidateReportForManifest(manifest, transitionDrift); err == nil {
		t.Fatal("accepted a transition population outside the previous reached subset")
	}
	lowerBoundDrift := cloneReport(report)
	lowerBoundMutated := false
	for funnelIndex := range lowerBoundDrift.Funnels {
		funnel := &lowerBoundDrift.Funnels[funnelIndex]
		for stageIndex := 1; stageIndex < len(funnel.Stages); stageIndex++ {
			stage := &funnel.Stages[stageIndex]
			lower := intersectionLowerBound(funnel.Stages[stageIndex-1].Reached, stage.Observed, funnel.Trials)
			if lower == 0 || stage.EligibleTransitions < lower {
				continue
			}
			stage.EligibleTransitions = lower - 1
			if stage.Converted > stage.EligibleTransitions {
				stage.Converted = stage.EligibleTransitions
			}
			if stage.EligibleTransitions == 0 {
				stage.Conversion = nil
			} else {
				conversion := rationalFromUint64(uint64(stage.Converted), uint64(stage.EligibleTransitions))
				stage.Conversion = &conversion
			}
			lowerBoundMutated = true
			break
		}
		if lowerBoundMutated {
			break
		}
	}
	if !lowerBoundMutated {
		t.Fatal("test fixture lacks a positive transition intersection lower bound")
	}
	lowerBoundDrift.ReportSHA256 = reportDigest(lowerBoundDrift)
	if err := ValidateReportForManifest(manifest, lowerBoundDrift); err == nil {
		t.Fatal("accepted an impossible transition below the necessary intersection bound")
	}
	convertedLowerBoundDrift := cloneReport(report)
	convertedLowerBoundMutated := false
	for funnelIndex := range convertedLowerBoundDrift.Funnels {
		funnel := &convertedLowerBoundDrift.Funnels[funnelIndex]
		for stageIndex := 1; stageIndex < len(funnel.Stages); stageIndex++ {
			stage := &funnel.Stages[stageIndex]
			lower := intersectionLowerBound(stage.EligibleTransitions, stage.Reached, stage.Observed)
			if lower == 0 || stage.Converted < lower {
				continue
			}
			stage.Converted = lower - 1
			conversion := rationalFromUint64(uint64(stage.Converted), uint64(stage.EligibleTransitions))
			stage.Conversion = &conversion
			convertedLowerBoundMutated = true
			break
		}
		if convertedLowerBoundMutated {
			break
		}
	}
	if !convertedLowerBoundMutated {
		t.Fatal("test fixture lacks a positive converted intersection lower bound")
	}
	convertedLowerBoundDrift.ReportSHA256 = reportDigest(convertedLowerBoundDrift)
	if err := ValidateReportForManifest(manifest, convertedLowerBoundDrift); err == nil {
		t.Fatal("accepted an impossible conversion below the necessary intersection bound")
	}
	activationDrift := cloneReport(report)
	activationDrift.Activation[0].TruePositive--
	activationDrift.Activation[0].FalseNegative++
	precision := rationalFromUint64(uint64(activationDrift.Activation[0].TruePositive),
		uint64(activationDrift.Activation[0].TruePositive+activationDrift.Activation[0].FalsePositive))
	recall := rationalFromUint64(uint64(activationDrift.Activation[0].TruePositive),
		uint64(activationDrift.Activation[0].TruePositive+activationDrift.Activation[0].FalseNegative))
	activationDrift.Activation[0].Precision = &precision
	activationDrift.Activation[0].Recall = &recall
	activationDrift.ReportSHA256 = reportDigest(activationDrift)
	if err := ValidateReportForManifest(manifest, activationDrift); err == nil {
		t.Fatal("accepted activation counts detached from load-stage funnels")
	}
	oversize := bytes.Repeat([]byte{'x'}, MaxReportBytes+1)
	if _, err := DecodeReport(bytes.NewReader(oversize), manifest); err == nil {
		t.Fatal("accepted oversize report")
	}
}

func TestAnalysisJSONPreflightRejectsExpansionBeforeTypedDecode(t *testing.T) {
	array := func(count int) string {
		if count == 0 {
			return "[]"
		}
		return "[" + strings.Repeat("{},", count-1) + "{}]"
	}
	comparisons := []byte(`{"comparisons":` + array(MaxStratifiedResults+1) + `}`)
	if err := validateJSONMembers(comparisons); err == nil {
		t.Fatal("preflight accepted an oversized comparison array")
	}
	pairs := []byte(`{"comparisons":[{"binary":[{"pairs":` + array(experiment.MaxBlocks+1) + `}]}]}`)
	if err := validateJSONMembers(pairs); err == nil {
		t.Fatal("preflight accepted a single dimension above the manifest block bound")
	}
	metrics := []byte(`{"coverage":{"members":[{"metrics":` + array(experiment.MaxMetrics+1) + `}]}}`)
	if err := validateJSONMembers(metrics); err == nil {
		t.Fatal("preflight accepted an oversized per-trial metric projection")
	}
}

func TestManifestBoundCoverageRejectsImpossibleSharedEndpointPresence(t *testing.T) {
	manifest := testFourTreatmentManifest(t, func(firstReference, secondReference, current, previous experiment.ArmSelector) []experiment.Comparison {
		return []experiment.Comparison{
			{Reference: firstReference, Candidate: current, Metrics: []experiment.MetricID{experiment.MetricOutcome}, Stages: []experiment.FunnelStage{}},
			{Reference: firstReference, Candidate: previous, Metrics: []experiment.MetricID{experiment.MetricOutcome}, Stages: []experiment.FunnelStage{}},
			{Reference: secondReference, Candidate: current, Metrics: []experiment.MetricID{experiment.MetricOutcome}, Stages: []experiment.FunnelStage{}},
			{Reference: secondReference, Candidate: previous, Metrics: []experiment.MetricID{experiment.MetricOutcome}, Stages: []experiment.FunnelStage{}},
		}
	})
	records := testRecords(t, manifest)
	report, err := Analyze(manifest, records)
	if err != nil {
		t.Fatal(err)
	}
	firstBlock := manifest.Blocks[0].ID
	firstReference := ""
	for _, treatment := range manifest.Treatments {
		if treatment.Role == experiment.RoleReference && treatment.Arm.ActivationChannel == experiment.ChannelImplicit {
			firstReference = treatment.ID
		}
	}
	wantReasons := []experiment.ExclusionReason{experiment.ExclusionCoverageMismatch, experiment.ExclusionUnsupportedCapability}
	mutated := 0
	for index := range report.Coverage.Pairs {
		pair := &report.Coverage.Pairs[index]
		binding := manifestPairByID(t, manifest, pair.PairID)
		if pair.BlockID != firstBlock || binding.ReferenceTreatmentID != firstReference || mutated == len(wantReasons) {
			continue
		}
		pair.Status = PairExcluded
		pair.Reasons = []experiment.ExclusionReason{wantReasons[mutated]}
		mutated++
	}
	if mutated != len(wantReasons) {
		t.Fatalf("mutated pairs=%d", mutated)
	}
	rebuildForgedReport(t, manifest, records, &report)
	if err := ValidateReportForManifest(manifest, report); err == nil {
		t.Fatal("accepted mutually impossible presence classes for one shared endpoint")
	}
}

func TestManifestBoundFunnelsRejectCrossTrialConversionForgery(t *testing.T) {
	manifest := testThreeTreatmentManifest(t, func(reference, current, previous experiment.ArmSelector) []experiment.Comparison {
		return []experiment.Comparison{
			{Reference: reference, Candidate: current, Stages: []experiment.FunnelStage{experiment.StageCandidateRecall}, Metrics: []experiment.MetricID{experiment.MetricOutcome}},
			{Reference: reference, Candidate: previous, Stages: []experiment.FunnelStage{experiment.StageSelection}, Metrics: []experiment.MetricID{}},
		}
	})
	records := testRecords(t, manifest)
	conditions := map[string]experiment.Condition{}
	for _, treatment := range manifest.Treatments {
		conditions[treatment.ID] = treatment.Arm.Condition
	}
	blockOrdinals := map[string]int{}
	for index, block := range manifest.Blocks {
		blockOrdinals[block.ID] = index
	}
	for index := range records {
		record := records[index]
		blockIndex := blockOrdinals[record.BlockID]
		condition := conditions[record.TreatmentID]
		for stageIndex := range record.Stages {
			stage := &record.Stages[stageIndex]
			switch stage.Stage {
			case experiment.StageCandidateRecall:
				switch condition {
				case experiment.ConditionNone:
					setTestStageProjection(stage, blockIndex < 2, blockIndex == 0)
				case experiment.ConditionCurrent:
					setTestStageProjection(stage, blockIndex == 0, false)
				}
			case experiment.StageSelection:
				switch condition {
				case experiment.ConditionNone:
					setTestStageProjection(stage, blockIndex < 2, blockIndex == 1)
				case experiment.ConditionPrevious:
					setTestStageProjection(stage, blockIndex == 1, false)
				}
			}
		}
		record.RecordSHA256 = ""
		sealed, err := experiment.SealTrialRecord(manifest, record)
		if err != nil {
			t.Fatal(err)
		}
		records[index] = sealed
	}
	report, err := Analyze(manifest, records)
	if err != nil {
		t.Fatal(err)
	}
	currentTreatment := ""
	for _, treatment := range manifest.Treatments {
		if treatment.Arm.Condition == experiment.ConditionNone {
			currentTreatment = treatment.ID
		}
	}
	mutated := false
	for funnelIndex := range report.Funnels {
		funnel := &report.Funnels[funnelIndex]
		if funnel.TreatmentID != currentTreatment {
			continue
		}
		for stageIndex := range funnel.Stages {
			stage := &funnel.Stages[stageIndex]
			if stage.Stage != experiment.StageSelection || stage.EligibleTransitions != 1 || stage.Converted != 0 || stage.Reached != 1 {
				continue
			}
			stage.Converted = 1
			one := Rational{Numerator: "1", Denominator: "1"}
			stage.Conversion = &one
			mutated = true
		}
	}
	if !mutated {
		t.Fatal("fixture lacks the cross-trial conversion boundary")
	}
	report.ReportSHA256 = reportDigest(report)
	if retained, err := reportRetainedObservationsMatchSummaries(context.Background(), report, manifest); err != nil || !retained {
		t.Fatalf("retained-only check unexpectedly rejected the cross-trial construction: retained=%t err=%v", retained, err)
	}
	if err := ValidateReportForManifest(manifest, report); err == nil {
		t.Fatal("accepted a funnel conversion assembled from different labeled trials")
	}
}

func TestRetainedBinaryPairsMustMatchLabeledTrialProjection(t *testing.T) {
	manifest := testManifest(t, 2, 2, 100, []uint32{1})
	report, err := Analyze(manifest, testRecords(t, manifest))
	if err != nil {
		t.Fatal(err)
	}
	drift := cloneReport(report)
	mutated := false
	for comparisonIndex := range drift.Comparisons {
		for binaryIndex := range drift.Comparisons[comparisonIndex].Binary {
			pairs := drift.Comparisons[comparisonIndex].Binary[binaryIndex].Pairs
			if len(pairs) == 0 {
				continue
			}
			drift.Comparisons[comparisonIndex].Binary[binaryIndex].Pairs[0].Reference = !pairs[0].Reference
			mutated = true
			break
		}
		if mutated {
			break
		}
	}
	if !mutated {
		t.Fatal("fixture has no retained binary pair")
	}
	matched, err := reportRetainedObservationsMatchSummaries(context.Background(), drift, manifest)
	if err != nil || matched {
		t.Fatalf("retained/projection drift matched=%t err=%v", matched, err)
	}
}

func TestValidateReportRejectsOversizedInMemoryShapeBeforeDigestProjection(t *testing.T) {
	manifest := testManifest(t, 2, 2, 100, []uint32{1})
	report, err := Analyze(manifest, testRecords(t, manifest))
	if err != nil {
		t.Fatal(err)
	}
	report.ReportSHA256 = strings.Repeat("a", 64)
	report.Comparisons = make([]ComparisonResult, MaxStratifiedResults+1)
	if err := ValidateReportForManifest(manifest, report); err == nil {
		t.Fatal("accepted an oversized in-memory report")
	}
}

func TestAnalyzeBindsInputAndPresenceExclusionReasons(t *testing.T) {
	manifest := testManifest(t, 2, 2, 100, []uint32{1})
	records := testRecords(t, manifest)
	mutations := []struct {
		index       int
		eligibility experiment.Eligibility
		reason      experiment.ExclusionReason
	}{
		{0, experiment.EligibilityIneligible, experiment.ExclusionIneligible},
		{2, experiment.EligibilityDrifted, experiment.ExclusionDrift},
	}
	for _, mutation := range mutations {
		record := records[mutation.index]
		record.RecordSHA256 = ""
		record.Eligibility = mutation.eligibility
		record.Exclusion = mutation.reason
		record.GradeReceiptSHA256 = ""
		sealed, err := experiment.SealTrialRecord(manifest, record)
		if err != nil {
			t.Fatal(err)
		}
		records[mutation.index] = sealed
	}
	report, err := Analyze(manifest, records)
	if err != nil {
		t.Fatal(err)
	}
	if report.Coverage.CompletePairs != 0 || report.Coverage.ExcludedPairs != 2 ||
		!coverageReasonCount(report.Coverage, experiment.ExclusionIneligible, 1) ||
		!coverageReasonCount(report.Coverage, experiment.ExclusionDrift, 1) {
		t.Fatalf("coverage=%+v", report.Coverage)
	}
	drift := cloneReport(report)
	drift.Coverage.Pairs[0].Reasons = []experiment.ExclusionReason{experiment.ExclusionGradeIncomplete}
	drift.Coverage.Reasons = []ReasonCount{{Reason: experiment.ExclusionGradeIncomplete, Count: 1}, {Reason: drift.Coverage.Pairs[1].Reasons[0], Count: 1}}
	sort.Slice(drift.Coverage.Reasons, func(left, right int) bool {
		return drift.Coverage.Reasons[left].Reason < drift.Coverage.Reasons[right].Reason
	})
	restrictedPlan := manifest.AnalysisPlan
	restrictedPlan.AnalysisPlanSHA256 = ""
	restrictedPlan.AllowedExclusions = []experiment.ExclusionReason{experiment.ExclusionDrift, experiment.ExclusionIneligible}
	restrictedPlan, err = experiment.SealAnalysisPlan(restrictedPlan)
	if err != nil {
		t.Fatal(err)
	}
	restrictedDesign := manifest.Design
	restrictedDesign.AnalysisPlanSHA256 = restrictedPlan.AnalysisPlanSHA256
	restrictedDesign.DesignSHA256 = ""
	restrictedDesign, err = experiment.SealDesign(restrictedDesign)
	if err != nil {
		t.Fatal(err)
	}
	restrictedManifest, err := experiment.Compile(restrictedDesign, manifest.CapabilityContract, restrictedPlan)
	if err != nil {
		t.Fatal(err)
	}
	drift.ManifestSHA256 = restrictedManifest.ManifestSHA256
	drift.AnalysisPlanSHA256 = restrictedPlan.AnalysisPlanSHA256
	drift.ReportSHA256 = reportDigest(drift)
	if err := ValidateReportForManifest(restrictedManifest, drift); err == nil {
		t.Fatal("accepted a report exclusion outside the manifest allowlist")
	}
}

func TestAnalyzePreservesLegacyStructuralTrialExclusionsAsExplicitExcludedPairs(t *testing.T) {
	manifest := testManifest(t, 2, 2, 100, []uint32{1})
	for _, reason := range []experiment.ExclusionReason{experiment.ExclusionMissingMember, experiment.ExclusionDuplicateMember} {
		t.Run(string(reason), func(t *testing.T) {
			records := testRecords(t, manifest)
			record := records[0]
			record.RecordSHA256 = ""
			record.Eligibility = experiment.EligibilityIneligible
			record.Exclusion = reason
			record.GradeReceiptSHA256 = ""
			sealed, err := experiment.SealTrialRecord(manifest, record)
			if err != nil {
				t.Fatal(err)
			}
			records[0] = sealed
			report, err := Analyze(manifest, records)
			if err != nil {
				t.Fatal(err)
			}
			if report.Coverage.CompletePairs != 1 || report.Coverage.ExcludedPairs != 1 ||
				!coverageReasonCount(report.Coverage, reason, 1) {
				t.Fatalf("coverage=%+v", report.Coverage)
			}
			found := false
			for _, pair := range report.Coverage.Pairs {
				if pair.Status == PairExcluded && containsReason(pair.Reasons, reason) {
					found = true
				}
			}
			if !found {
				t.Fatalf("legacy structural reason was not retained as PairExcluded: %+v", report.Coverage.Pairs)
			}
		})
	}
}

func TestReportMemberExclusionsCannotBorrowStructuralPairAuthority(t *testing.T) {
	plan := experiment.AnalysisPlan{AllowedExclusions: []experiment.ExclusionReason{experiment.ExclusionDrift}}
	report := Report{Coverage: Coverage{
		Members: []TrialCoverage{{TrialID: "trial-" + strings.Repeat("a", 64), Records: 1, Exclusion: experiment.ExclusionMissingMember}},
		Pairs:   []PairCoverage{{Status: PairDuplicate, Reasons: []experiment.ExclusionReason{experiment.ExclusionDuplicateMember, experiment.ExclusionMissingMember}}},
	}}
	if reportReasonsAllowedByManifest(report, plan) {
		t.Fatal("a singleton record exclusion borrowed a pair-level structural exemption")
	}
}

func TestAnalyzeHandlesEveryNonObservedOutcomePresenceWithoutPanic(t *testing.T) {
	manifest := testManifest(t, 2, 2, 100, []uint32{1})
	for _, presence := range []experiment.Presence{experiment.PresenceUnknown, experiment.PresenceUnsupported, experiment.PresenceNotApplicable} {
		t.Run(string(presence), func(t *testing.T) {
			records := testRecords(t, manifest)
			record := records[0]
			for index := range record.Metrics {
				if record.Metrics[index].Metric == experiment.MetricOutcome {
					record.Metrics[index].Presence = presence
					record.Metrics[index].Value = nil
				}
			}
			record.RecordSHA256 = ""
			sealed, err := experiment.SealTrialRecord(manifest, record)
			if err != nil {
				t.Fatal(err)
			}
			records[0] = sealed
			report, err := Analyze(manifest, records)
			if err != nil {
				t.Fatal(err)
			}
			if report.Coverage.ExcludedPairs != 1 {
				t.Fatalf("coverage=%+v", report.Coverage)
			}
		})
	}
}

func TestAnalysisRepeatedAttemptPolicyIsNotSilentlyReinterpreted(t *testing.T) {
	base := testManifest(t, 2, 2, 100, []uint32{1})
	none := testManifestWithPlan(t, base, func(plan *experiment.AnalysisPlan) {
		plan.RepeatedAttempts = experiment.RepeatedAttemptPolicy{Kind: experiment.RepeatedAttemptsNone, K: []uint32{1}}
	})
	report, err := Analyze(none, testRecords(t, none))
	if err != nil || report.PassAtK == nil || len(report.PassAtK) != 0 {
		t.Fatalf("none policy report=%+v err=%v", report.PassAtK, err)
	}
	missingOutcome := testManifestWithPlan(t, base, func(plan *experiment.AnalysisPlan) {
		for index := range plan.Stages {
			if plan.Stages[index].Stage == experiment.StageLoad {
				plan.Stages[index].Role = experiment.MetricPrimary
			}
		}
		plan.Metrics = plan.Metrics[:1]
		plan.Comparisons[0].Metrics = []experiment.MetricID{experiment.MetricDurationMillis}
	})
	if _, err := Analyze(missingOutcome, testRecords(t, missingOutcome)); err == nil {
		t.Fatal("accepted repeated-attempt estimation without an outcome declaration")
	} else if code, ok := CodeOf(err); !ok || code != ErrorInvalidInput {
		t.Fatalf("missing outcome err=%v code=%s", err, code)
	}
}

func TestAnalysisRejectsOneMultiplicityFamilyAcrossExploratoryAndRegisteredRoles(t *testing.T) {
	base := testManifest(t, 2, 2, 100, []uint32{1})
	mixed := testManifestWithPlan(t, base, func(plan *experiment.AnalysisPlan) {
		family := ""
		for _, stage := range plan.Stages {
			if stage.Stage == experiment.StageLoad {
				family = stage.FamilySHA256
			}
		}
		for index := range plan.Metrics {
			if plan.Metrics[index].ID == experiment.MetricDurationMillis {
				plan.Metrics[index].FamilySHA256 = family
			}
		}
	})
	if _, err := Analyze(mixed, testRecords(t, mixed)); err == nil {
		t.Fatal("accepted one family identity across exploratory and registered hypotheses")
	} else if code, ok := CodeOf(err); !ok || code != ErrorInvalidInput {
		t.Fatalf("mixed family err=%v code=%s", err, code)
	}
}

func TestContinuousRetainedDeltasMustHaveOneFeasibleObservationGraph(t *testing.T) {
	manifest := experiment.Manifest{
		Blocks: []experiment.Block{{ID: "block-a", StratumID: "stratum-a", Assignments: []experiment.Assignment{
			{TreatmentID: "treatment-a", TrialID: "trial-a"}, {TreatmentID: "treatment-b", TrialID: "trial-b"}, {TreatmentID: "treatment-c", TrialID: "trial-c"},
		}}},
		Pairs: []experiment.PairBinding{
			{ID: "pair-ab", BlockID: "block-a", ComparisonID: "comparison-ab", ReferenceTreatmentID: "treatment-a", CandidateTreatmentID: "treatment-b"},
			{ID: "pair-ac", BlockID: "block-a", ComparisonID: "comparison-ac", ReferenceTreatmentID: "treatment-a", CandidateTreatmentID: "treatment-c"},
			{ID: "pair-bc", BlockID: "block-a", ComparisonID: "comparison-bc", ReferenceTreatmentID: "treatment-b", CandidateTreatmentID: "treatment-c"},
		},
	}
	report := Report{Comparisons: []ComparisonResult{
		{ComparisonID: "comparison-ab", StratumID: "stratum-a", Continuous: []ContinuousResult{{Metric: experiment.MetricDurationMillis, Deltas: []PairDelta{{PairID: "pair-ab", Delta: Rational{Numerator: "1", Denominator: "1"}}}}}},
		{ComparisonID: "comparison-ac", StratumID: "stratum-a", Continuous: []ContinuousResult{{Metric: experiment.MetricDurationMillis, Deltas: []PairDelta{{PairID: "pair-ac", Delta: Rational{Numerator: "3", Denominator: "1"}}}}}},
		{ComparisonID: "comparison-bc", StratumID: "stratum-a", Continuous: []ContinuousResult{{Metric: experiment.MetricDurationMillis, Deltas: []PairDelta{{PairID: "pair-bc", Delta: Rational{Numerator: "1", Denominator: "1"}}}}}},
	}}
	if feasible, err := reportContinuousDeltasFeasible(context.Background(), report, manifest); err != nil || feasible {
		t.Fatalf("cycle feasible=%t err=%v", feasible, err)
	}
	report.Comparisons = report.Comparisons[:2]
	report.Comparisons[0].Continuous[0].Deltas[0].Delta.Numerator = strconv.FormatInt(maxMetricDelta, 10)
	report.Comparisons[1].Continuous[0].Deltas[0].Delta.Numerator = strconv.FormatInt(-maxMetricDelta, 10)
	if feasible, err := reportContinuousDeltasFeasible(context.Background(), report, manifest); err != nil || feasible {
		t.Fatalf("range feasible=%t err=%v", feasible, err)
	}
}

func rebuildForgedReport(t *testing.T, manifest experiment.Manifest, records []experiment.TrialRecord, report *Report) {
	t.Helper()
	coverageByPair := make(map[string]PairCoverage, len(report.Coverage.Pairs))
	reasonCounts := map[experiment.ExclusionReason]uint32{}
	report.Coverage.CompletePairs, report.Coverage.ExcludedPairs = 0, 0
	for _, coverage := range report.Coverage.Pairs {
		coverageByPair[coverage.PairID] = coverage
		if coverage.Status == PairComplete {
			report.Coverage.CompletePairs++
		} else {
			report.Coverage.ExcludedPairs++
			for _, reason := range coverage.Reasons {
				reasonCounts[reason]++
			}
		}
	}
	report.Coverage.Reasons = report.Coverage.Reasons[:0]
	for reason, count := range reasonCounts {
		report.Coverage.Reasons = append(report.Coverage.Reasons, ReasonCount{Reason: reason, Count: count})
	}
	sort.Slice(report.Coverage.Reasons, func(left, right int) bool {
		return report.Coverage.Reasons[left].Reason < report.Coverage.Reasons[right].Reason
	})
	recordsByTrial := make(map[string]experiment.TrialRecord, len(records))
	for _, record := range records {
		recordsByTrial[record.TrialID] = record
	}
	blocks := make(map[string]experiment.Block, len(manifest.Blocks))
	stratumSet := map[string]bool{}
	for _, block := range manifest.Blocks {
		blocks[block.ID] = block
		stratumSet[block.StratumID] = true
	}
	strata := make([]string, 0, len(stratumSet))
	for stratum := range stratumSet {
		strata = append(strata, stratum)
	}
	sort.Strings(strata)
	report.Comparisons = report.Comparisons[:0]
	for _, comparison := range manifest.AnalysisPlan.Comparisons {
		for _, stratum := range strata {
			pairs := []pairRecords{}
			for _, binding := range manifest.Pairs {
				if binding.ComparisonID != comparison.ID || blocks[binding.BlockID].StratumID != stratum ||
					coverageByPair[binding.ID].Status != PairComplete {
					continue
				}
				assignments := map[string]string{}
				for _, assignment := range blocks[binding.BlockID].Assignments {
					assignments[assignment.TreatmentID] = assignment.TrialID
				}
				pairs = append(pairs, pairRecords{
					pairID: binding.ID, reference: recordsByTrial[assignments[binding.ReferenceTreatmentID]],
					candidate: recordsByTrial[assignments[binding.CandidateTreatmentID]],
				})
			}
			result, _, err := analyzeComparison(context.Background(), manifest, comparison, stratum, pairs)
			if err != nil {
				t.Fatal(err)
			}
			report.Comparisons = append(report.Comparisons, result)
		}
	}
	if err := applyHolmContext(context.Background(), report, report.ConfidenceBasisPoints); err != nil {
		t.Fatal(err)
	}
	report.ReportSHA256 = reportDigest(*report)
}

func setTestStageProjection(stage *experiment.StageObservation, observed, value bool) {
	if !observed {
		stage.Presence = experiment.PresenceUnknown
		stage.Value = nil
		return
	}
	stage.Presence = experiment.PresenceObserved
	stage.Value = boolPointer(value)
}

func manifestPairByID(t *testing.T, manifest experiment.Manifest, pairID string) experiment.PairBinding {
	t.Helper()
	for _, pair := range manifest.Pairs {
		if pair.ID == pairID {
			return pair
		}
	}
	t.Fatalf("manifest pair %s not found", pairID)
	return experiment.PairBinding{}
}

func coverageReasonCount(coverage Coverage, reason experiment.ExclusionReason, want uint32) bool {
	for _, count := range coverage.Reasons {
		if count.Reason == reason {
			return count.Count == want
		}
	}
	return false
}

func recomputeActivationRatios(summary *ActivationSummary) {
	summary.Precision, summary.Recall, summary.FalseActivationRate, summary.UnnecessaryLoadRate = nil, nil, nil, nil
	if denominator := summary.TruePositive + summary.FalsePositive; denominator > 0 {
		value := rationalFromUint64(uint64(summary.TruePositive), uint64(denominator))
		summary.Precision = &value
		unnecessary := rationalFromUint64(uint64(summary.FalsePositive), uint64(denominator))
		summary.UnnecessaryLoadRate = &unnecessary
	}
	if denominator := summary.TruePositive + summary.FalseNegative; denominator > 0 {
		value := rationalFromUint64(uint64(summary.TruePositive), uint64(denominator))
		summary.Recall = &value
	}
	if denominator := summary.FalsePositive + summary.TrueNegative; denominator > 0 {
		value := rationalFromUint64(uint64(summary.FalsePositive), uint64(denominator))
		summary.FalseActivationRate = &value
	}
}

func rosterManifestReport(t *testing.T, manifest experiment.Manifest) Report {
	t.Helper()
	report, err := Analyze(manifest, testRecords(t, manifest))
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestAnalyzeContextRefusesRevokedAuthorityBeforeWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := AnalyzeContext(ctx, experiment.Manifest{}, nil)
	if code, ok := CodeOf(err); !ok || code != ErrorInterrupted {
		t.Fatalf("err=%v code=%s", err, code)
	}
	if _, err := AnalyzeContext(nil, experiment.Manifest{}, nil); err == nil { //nolint:staticcheck // nil is an explicit fail-closed contract case.
		t.Fatal("nil context was accepted")
	}
}

type cancelAfterContext struct {
	remaining int
	done      chan struct{}
	canceled  bool
}

func (ctx *cancelAfterContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *cancelAfterContext) Done() <-chan struct{}       { return ctx.done }
func (ctx *cancelAfterContext) Value(any) any               { return nil }
func (ctx *cancelAfterContext) Err() error {
	if ctx.canceled {
		return context.Canceled
	}
	ctx.remaining--
	if ctx.remaining <= 0 {
		ctx.canceled = true
		close(ctx.done)
		return context.Canceled
	}
	return nil
}

func TestAnalyzeContextInterruptsDeterministicBootstrapWork(t *testing.T) {
	manifest := testManifest(t, 6, 4, experiment.MaxBootstrapSamples, []uint32{1, 2, 3})
	ctx := &cancelAfterContext{remaining: 18, done: make(chan struct{})}
	_, err := AnalyzeContext(ctx, manifest, testRecords(t, manifest))
	if code, ok := CodeOf(err); !ok || code != ErrorInterrupted || !ctx.canceled {
		t.Fatalf("err=%v code=%s canceled=%t remaining=%d", err, code, ctx.canceled, ctx.remaining)
	}
}

func TestExactProbabilityAndPassEstimatorsMatchIndependentVectors(t *testing.T) {
	if got := exactTwoSidedBinomial(1, 3); got != (Rational{Numerator: "5", Denominator: "8"}) {
		t.Fatalf("probability=%+v", got)
	}
	pass, power, ok := passEstimators(6, 4, 2)
	if !ok || pass != (Rational{Numerator: "14", Denominator: "15"}) || power != (Rational{Numerator: "2", Denominator: "5"}) {
		t.Fatalf("pass=%+v power=%+v ok=%v", pass, power, ok)
	}
	if value := exactTwoSidedBinomial(0, 0); value != (Rational{Numerator: "1", Denominator: "1"}) {
		t.Fatalf("zero discordance=%+v", value)
	}
	ctx := &cancelAfterContext{remaining: 3, done: make(chan struct{})}
	if _, err := exactTwoSidedBinomialContext(ctx, 4096, 4096); err == nil {
		t.Fatal("exact binomial work ignored cancellation")
	} else if code, ok := CodeOf(err); !ok || code != ErrorInterrupted || !ctx.canceled {
		t.Fatalf("exact cancellation err=%v code=%s canceled=%t", err, code, ctx.canceled)
	}
	if _, _, ok := passEstimators(2, 1, 3); ok {
		t.Fatal("accepted k above attempts")
	}
}

func TestHolmStepDownUsesExactMonotoneAdjustedProbabilities(t *testing.T) {
	first := &ExactTest{RawProbability: Rational{Numerator: "1", Denominator: "100"}, Multiplicity: MultiplicityHolmAdjusted, FamilySHA256: strings.Repeat("a", 64)}
	second := &ExactTest{RawProbability: Rational{Numerator: "1", Denominator: "25"}, Multiplicity: MultiplicityHolmAdjusted, FamilySHA256: strings.Repeat("a", 64)}
	third := &ExactTest{RawProbability: Rational{Numerator: "1", Denominator: "2"}, Multiplicity: MultiplicityHolmAdjusted, FamilySHA256: strings.Repeat("a", 64)}
	report := Report{Comparisons: []ComparisonResult{{ComparisonID: "comparison-" + strings.Repeat("b", 64), Binary: []BinaryResult{
		{ID: "candidate_recall", FamilySHA256: strings.Repeat("a", 64), ExactTest: third},
		{ID: "load", FamilySHA256: strings.Repeat("a", 64), ExactTest: first},
		{ID: "selection", FamilySHA256: strings.Repeat("a", 64), ExactTest: second},
	}}}}
	applyHolm(&report, 9500)
	if first.AdjustedProbability == nil || *first.AdjustedProbability != (Rational{Numerator: "3", Denominator: "100"}) ||
		first.RejectNull == nil || !*first.RejectNull || second.AdjustedProbability == nil ||
		*second.AdjustedProbability != (Rational{Numerator: "2", Denominator: "25"}) || second.RejectNull == nil || *second.RejectNull ||
		third.AdjustedProbability == nil || *third.AdjustedProbability != (Rational{Numerator: "1", Denominator: "2"}) ||
		third.RejectNull == nil || *third.RejectNull {
		t.Fatalf("adjusted first=%+v second=%+v third=%+v", first, second, third)
	}
}

func TestHolmFamilySizeRetainsPreregisteredUnavailableSlots(t *testing.T) {
	family := strings.Repeat("a", 64)
	first := &ExactTest{RawProbability: Rational{Numerator: "1", Denominator: "100"}, Multiplicity: MultiplicityHolmAdjusted, FamilySHA256: family}
	second := &ExactTest{RawProbability: Rational{Numerator: "1", Denominator: "25"}, Multiplicity: MultiplicityHolmAdjusted, FamilySHA256: family}
	report := Report{Comparisons: []ComparisonResult{{ComparisonID: "comparison-" + strings.Repeat("b", 64), Binary: []BinaryResult{
		{ID: "candidate_recall", Role: experiment.MetricPrimary, FamilySHA256: family, ExactTest: first},
		{ID: "load", Role: experiment.MetricPrimary, FamilySHA256: family, ExactTest: second},
		{ID: "selection", Role: experiment.MetricPrimary, FamilySHA256: family, ExactTest: nil},
	}}}}
	applyHolm(&report, 9500)
	if first.AdjustedProbability == nil || *first.AdjustedProbability != (Rational{Numerator: "3", Denominator: "100"}) ||
		second.AdjustedProbability == nil || *second.AdjustedProbability != (Rational{Numerator: "2", Denominator: "25"}) {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestRationalParserRejectsNoncanonicalForms(t *testing.T) {
	for _, value := range []Rational{{"01", "2"}, {"1", "02"}, {"2", "4"}, {"-0", "1"}, {"1", "0"}, {"+1", "2"}} {
		if _, ok := parseRational(value); ok {
			t.Fatalf("accepted %+v", value)
		}
	}
	if _, ok := parseRational(Rational{Numerator: strings.Repeat("1", MaxProbabilityDigits+1), Denominator: "1"}); ok {
		t.Fatal("accepted oversized numerator")
	}
	pairID := "pair-" + strings.Repeat("a", 64)
	oversizedDelta := ContinuousResult{
		Metric: experiment.MetricDurationMillis, Role: experiment.MetricExploratory, Direction: experiment.DirectionHigher,
		Status: InferenceDescriptive, CompletePairs: 1,
		Deltas:          []PairDelta{{PairID: pairID, Delta: Rational{Numerator: strconv.FormatInt(maxMetricDelta+1, 10), Denominator: "1"}}},
		CandidateHigher: 1, MeanDelta: Rational{Numerator: strconv.FormatInt(maxMetricDelta+1, 10), Denominator: "1"},
		MedianDelta:      Rational{Numerator: strconv.FormatInt(maxMetricDelta+1, 10), Denominator: "1"},
		PairedSignEffect: Rational{Numerator: "1", Denominator: "1"}, DirectionAdjusted: Rational{Numerator: strconv.FormatInt(maxMetricDelta+1, 10), Denominator: "1"},
	}
	if err := validateContinuous(Report{MinimumInferenceBlocks: 2}, oversizedDelta, []string{pairID}); err == nil {
		t.Fatal("accepted a paired count delta outside the admitted metric domain")
	}
}

func findBinary(t *testing.T, comparison ComparisonResult, kind DimensionKind, id string) BinaryResult {
	t.Helper()
	for _, result := range comparison.Binary {
		if result.Kind == kind && result.ID == id {
			return result
		}
	}
	t.Fatalf("missing binary %s/%s", kind, id)
	return BinaryResult{}
}

func findContinuous(t *testing.T, comparison ComparisonResult, metric experiment.MetricID) ContinuousResult {
	t.Helper()
	for _, result := range comparison.Continuous {
		if result.Metric == metric {
			return result
		}
	}
	t.Fatalf("missing continuous %s", metric)
	return ContinuousResult{}
}

func findPass(t *testing.T, report Report, treatmentID string, k uint32) PassAtKResult {
	t.Helper()
	for _, result := range report.PassAtK {
		if result.TreatmentID == treatmentID && result.K == k {
			return result
		}
	}
	t.Fatalf("missing pass@%d for %s", k, treatmentID)
	return PassAtKResult{}
}
