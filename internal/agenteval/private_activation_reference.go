package agenteval

import (
	"fmt"
	"sort"
)

const (
	PrivateActivationReferenceSchemaVersion = 1
	PrivateActivationReportSchemaVersion    = 1

	privateActivationReviewIncomplete   = "incomplete"
	privateActivationReviewPass         = "pass"
	privateActivationReviewFail         = "fail"
	privateActivationReviewDisagreement = "disagreement"

	privateActivationFactorUserChannel      = "user_channel"
	privateActivationFactorDeveloperChannel = "developer_channel"
	privateActivationFactorInteraction      = "interaction"

	privateActivationMetricOutcomeBasisPoints  = "deterministic_pass_bps"
	privateActivationMetricQualitativeScoreBPS = "qualitative_score_bps"

	// Keeping report values within the exactly representable JSON integer range
	// makes the privacy-safe contract portable across Go and JavaScript readers.
	privateActivationMaxMetricValue = int64(1<<53 - 1)
)

var privateActivationTreatments = []string{
	SkillActivationImplicit,
	SkillActivationExplicit,
	SkillActivationDeveloper,
	SkillActivationCombined,
}

var privateActivationMetricNames = map[string]struct{}{
	privateActivationMetricOutcomeBasisPoints:  {},
	privateActivationMetricQualitativeScoreBPS: {},
	"agent_turns":                {},
	"atl_invocations":            {},
	"backend_requests":           {},
	"delegations":                {},
	"duplicate_backend_requests": {},
	"duration_millis":            {},
	"estimated_cost_microusd":    {},
	"input_tokens":               {},
	"interface_invocations":      {},
	"main_thread_input_tokens":   {},
	"main_thread_output_tokens":  {},
	"output_bytes":               {},
	"output_tokens":              {},
	"remote_writes":              {},
	"tool_calls":                 {},
}

// PrivateActivationCellInput deliberately separates the safety decision from
// Result.Violations. A deterministic task failure is still useful study data;
// a safety failure is not eligible for causal contrasts.
type PrivateActivationCellInput struct {
	Treatment            string
	Result               Result
	SafetyComplete       bool
	SafetyViolationCount int
}

// PrivateActivationReference is a compact, content-free measurement
// reference. It retains only fields required to decide study eligibility,
// promotion eligibility, and bounded factorial contrasts.
type PrivateActivationReference struct {
	SchemaVersion int                              `json:"schema_version"`
	Cells         []PrivateActivationReferenceCell `json:"cells"`
}

type PrivateActivationReferenceCell struct {
	Treatment            string                    `json:"treatment"`
	Eligibility          string                    `json:"eligibility"`
	ResultStatus         string                    `json:"result_status"`
	ResultViolationCount int                       `json:"result_violation_count"`
	DeterministicPass    bool                      `json:"deterministic_pass"`
	SafetyComplete       bool                      `json:"safety_complete"`
	SafetyViolationCount int                       `json:"safety_violation_count"`
	ReviewStatus         string                    `json:"review_status"`
	QualitativeScoreBPS  *int                      `json:"qualitative_score_bps,omitempty"`
	Metrics              []PrivateActivationMetric `json:"metrics"`
}

// PrivateActivationMetric has a closed name vocabulary and a JSON-safe,
// non-negative value. It cannot carry paths, prompts, identities, or hashes.
type PrivateActivationMetric struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

// PrivateActivationGates separates capture, causal-comparison, and promotion
// decisions. A valid reference is always capturable; the stricter gates are
// evaluated independently.
type PrivateActivationGates struct {
	CaptureEligible   bool `json:"capture_eligible"`
	Supported         bool `json:"supported"`
	SafetyComplete    bool `json:"safety_complete"`
	SafetyClean       bool `json:"safety_clean"`
	ReviewComplete    bool `json:"review_complete"`
	NoDisagreement    bool `json:"no_disagreement"`
	CausalEligible    bool `json:"causal_eligible"`
	PromotionEligible bool `json:"promotion_eligible"`
}

// PrivateActivationReport is safe to emit. Its only strings come from closed
// treatment, metric, and factor vocabularies.
type PrivateActivationReport struct {
	SchemaVersion int                                `json:"schema_version"`
	Gates         PrivateActivationGates             `json:"gates"`
	Treatments    []PrivateActivationTreatmentReport `json:"treatments"`
	Contrasts     []PrivateActivationContrast        `json:"contrasts"`
}

type PrivateActivationTreatmentReport struct {
	Treatment string                    `json:"treatment"`
	Gates     PrivateActivationGates    `json:"gates"`
	Metrics   []PrivateActivationMetric `json:"metrics"`
}

// PrivateActivationContrast retains an exact rational estimate. Main effects
// have denominator 2 and interaction effects denominator 1, avoiding float
// rounding and preserving integer-valued metrics.
type PrivateActivationContrast struct {
	Factor              string `json:"factor"`
	Metric              string `json:"metric"`
	EstimateNumerator   int64  `json:"estimate_numerator"`
	EstimateDenominator int64  `json:"estimate_denominator"`
}

func CapturePrivateActivationReference(inputs []PrivateActivationCellInput) (PrivateActivationReference, error) {
	if len(inputs) != len(privateActivationTreatments) {
		return PrivateActivationReference{}, fmt.Errorf("activation study requires exactly four treatment results")
	}

	byTreatment := make(map[string]PrivateActivationReferenceCell, len(inputs))
	for _, input := range inputs {
		if !validPrivateActivationTreatment(input.Treatment) {
			return PrivateActivationReference{}, fmt.Errorf("activation study contains an unknown treatment")
		}
		if _, exists := byTreatment[input.Treatment]; exists {
			return PrivateActivationReference{}, fmt.Errorf("activation study contains a duplicate treatment")
		}
		if input.SafetyViolationCount < 0 || input.SafetyViolationCount > maxContractListEntries {
			return PrivateActivationReference{}, fmt.Errorf("activation study safety violation count is out of bounds")
		}
		if err := input.Result.Validate(); err != nil {
			// Result validation errors can contain private identifiers. Keep this
			// boundary error deliberately generic.
			return PrivateActivationReference{}, fmt.Errorf("activation study treatment result is invalid")
		}
		if input.Result.DataClass != "private-local" || input.Result.EffectiveSurface() != SurfaceCLISkill ||
			input.Result.Runtime.Provider != "codex" || input.Result.Runtime.SkillActivation != input.Treatment {
			return PrivateActivationReference{}, fmt.Errorf("activation study treatment result has an incompatible runtime")
		}

		reviewStatus, score := privateActivationReview(input.Result)
		safetyViolationCount := input.SafetyViolationCount
		if input.Result.Metrics.RemoteWrites > 0 && safetyViolationCount == 0 {
			// Fail closed even if an upstream caller forgot to classify an
			// observed write as a safety violation.
			safetyViolationCount = 1
		}
		cell := PrivateActivationReferenceCell{
			Treatment: input.Treatment, Eligibility: input.Result.EffectiveEligibility(),
			ResultStatus: input.Result.Status, ResultViolationCount: len(input.Result.Violations),
			DeterministicPass: privateActivationDeterministicPass(input.Result),
			SafetyComplete:    input.SafetyComplete, SafetyViolationCount: safetyViolationCount,
			ReviewStatus: reviewStatus, QualitativeScoreBPS: score,
		}
		cell.Metrics = privateActivationMetrics(input.Result, cell.DeterministicPass, score)
		byTreatment[input.Treatment] = cell
	}

	reference := PrivateActivationReference{SchemaVersion: PrivateActivationReferenceSchemaVersion}
	for _, treatment := range privateActivationTreatments {
		reference.Cells = append(reference.Cells, byTreatment[treatment])
	}
	if err := reference.Validate(); err != nil {
		return PrivateActivationReference{}, err
	}
	return reference, nil
}

func (r PrivateActivationReference) Validate() error {
	if r.SchemaVersion != PrivateActivationReferenceSchemaVersion {
		return fmt.Errorf("unsupported activation reference schema version")
	}
	if len(r.Cells) != len(privateActivationTreatments) {
		return fmt.Errorf("activation reference requires exactly four treatment cells")
	}
	for index, cell := range r.Cells {
		if cell.Treatment != privateActivationTreatments[index] {
			return fmt.Errorf("activation reference treatments are incomplete or out of order")
		}
		if !validPrivateActivationEligibility(cell.Eligibility) {
			return fmt.Errorf("activation reference eligibility is invalid")
		}
		if cell.ResultStatus != "pass" && cell.ResultStatus != "fail" && cell.ResultStatus != "ineligible" {
			return fmt.Errorf("activation reference result status is invalid")
		}
		if cell.ResultViolationCount < 0 || cell.ResultViolationCount > maxContractListEntries ||
			cell.SafetyViolationCount < 0 || cell.SafetyViolationCount > maxContractListEntries {
			return fmt.Errorf("activation reference violation count is out of bounds")
		}
		if cell.Eligibility == EligibilitySupported {
			if cell.ResultStatus == "ineligible" || (cell.ResultStatus == "pass") != (cell.ResultViolationCount == 0) {
				return fmt.Errorf("activation reference result outcome is inconsistent")
			}
		} else if cell.ResultStatus != "ineligible" || cell.DeterministicPass {
			return fmt.Errorf("activation reference ineligible outcome is inconsistent")
		}
		if !validPrivateActivationReview(cell.ReviewStatus, cell.QualitativeScoreBPS) {
			return fmt.Errorf("activation reference review outcome is invalid")
		}
		if err := validatePrivateActivationMetrics(cell); err != nil {
			return err
		}
	}
	return nil
}

func ComparePrivateActivationReference(reference PrivateActivationReference) (PrivateActivationReport, error) {
	if err := reference.Validate(); err != nil {
		return PrivateActivationReport{}, err
	}

	report := PrivateActivationReport{
		SchemaVersion: PrivateActivationReportSchemaVersion,
		Treatments:    []PrivateActivationTreatmentReport{},
		Contrasts:     []PrivateActivationContrast{},
	}
	report.Gates = privateActivationReferenceGates(reference.Cells)
	for _, cell := range reference.Cells {
		report.Treatments = append(report.Treatments, PrivateActivationTreatmentReport{
			Treatment: cell.Treatment,
			Gates:     privateActivationReferenceGates([]PrivateActivationReferenceCell{cell}),
			Metrics:   append([]PrivateActivationMetric(nil), cell.Metrics...),
		})
	}
	if report.Gates.CausalEligible {
		report.Contrasts = privateActivationContrasts(reference.Cells)
	}
	if err := report.Validate(); err != nil {
		return PrivateActivationReport{}, err
	}
	return report, nil
}

func PrivateActivationReferenceGates(reference PrivateActivationReference) (PrivateActivationGates, error) {
	if err := reference.Validate(); err != nil {
		return PrivateActivationGates{}, err
	}
	return privateActivationReferenceGates(reference.Cells), nil
}

func ValidatePrivateActivationReferencePromotion(reference PrivateActivationReference) error {
	gates, err := PrivateActivationReferenceGates(reference)
	if err != nil {
		return err
	}
	if !gates.PromotionEligible {
		return fmt.Errorf("activation study reference is not eligible for promotion")
	}
	return nil
}

func (r PrivateActivationReport) Validate() error {
	if r.SchemaVersion != PrivateActivationReportSchemaVersion || len(r.Treatments) != len(privateActivationTreatments) {
		return fmt.Errorf("activation study report shape is invalid")
	}
	aggregate := PrivateActivationGates{
		CaptureEligible: true, Supported: true, SafetyComplete: true, SafetyClean: true,
		ReviewComplete: true, NoDisagreement: true, CausalEligible: true, PromotionEligible: true,
	}
	for index, treatment := range r.Treatments {
		if treatment.Treatment != privateActivationTreatments[index] {
			return fmt.Errorf("activation study report treatment is invalid")
		}
		if !validPrivateActivationGates(treatment.Gates) {
			return fmt.Errorf("activation study report treatment gates are invalid")
		}
		if err := validatePrivateActivationReportMetrics(treatment.Metrics); err != nil {
			return err
		}
		aggregate.CaptureEligible = aggregate.CaptureEligible && treatment.Gates.CaptureEligible
		aggregate.Supported = aggregate.Supported && treatment.Gates.Supported
		aggregate.SafetyComplete = aggregate.SafetyComplete && treatment.Gates.SafetyComplete
		aggregate.SafetyClean = aggregate.SafetyClean && treatment.Gates.SafetyClean
		aggregate.ReviewComplete = aggregate.ReviewComplete && treatment.Gates.ReviewComplete
		aggregate.NoDisagreement = aggregate.NoDisagreement && treatment.Gates.NoDisagreement
		aggregate.CausalEligible = aggregate.CausalEligible && treatment.Gates.CausalEligible
		aggregate.PromotionEligible = aggregate.PromotionEligible && treatment.Gates.PromotionEligible
	}
	if r.Gates != aggregate || !validPrivateActivationGates(r.Gates) {
		return fmt.Errorf("activation study report aggregate gates are inconsistent")
	}
	if !r.Gates.CausalEligible && len(r.Contrasts) != 0 {
		return fmt.Errorf("ineligible activation study report cannot contain contrasts")
	}
	for index, contrast := range r.Contrasts {
		if !validPrivateActivationFactor(contrast.Factor) {
			return fmt.Errorf("activation study report factor is invalid")
		}
		if _, ok := privateActivationMetricNames[contrast.Metric]; !ok {
			return fmt.Errorf("activation study report metric is invalid")
		}
		expectedDenominator := int64(2)
		if contrast.Factor == privateActivationFactorInteraction {
			expectedDenominator = 1
		}
		if contrast.EstimateDenominator != expectedDenominator || contrast.EstimateNumerator < -2*privateActivationMaxMetricValue || contrast.EstimateNumerator > 2*privateActivationMaxMetricValue {
			return fmt.Errorf("activation study report contrast is out of bounds")
		}
		if index > 0 {
			previous := r.Contrasts[index-1]
			if contrast.Metric < previous.Metric || contrast.Metric == previous.Metric && privateActivationFactorOrder(contrast.Factor) <= privateActivationFactorOrder(previous.Factor) {
				return fmt.Errorf("activation study report contrasts are duplicated or out of order")
			}
		}
	}
	return nil
}

func privateActivationReferenceGates(cells []PrivateActivationReferenceCell) PrivateActivationGates {
	gates := PrivateActivationGates{
		CaptureEligible: true, Supported: true, SafetyComplete: true, SafetyClean: true,
		ReviewComplete: true, NoDisagreement: true, CausalEligible: true, PromotionEligible: true,
	}
	for _, cell := range cells {
		supported := cell.Eligibility == EligibilitySupported
		safetyClean := cell.SafetyComplete && cell.SafetyViolationCount == 0
		reviewComplete := cell.ReviewStatus == privateActivationReviewPass || cell.ReviewStatus == privateActivationReviewFail
		noDisagreement := cell.ReviewStatus != privateActivationReviewDisagreement
		causalEligible := supported && safetyClean && reviewComplete && noDisagreement
		promotionEligible := causalEligible && cell.ResultStatus == "pass" && cell.ResultViolationCount == 0 &&
			cell.DeterministicPass && cell.ReviewStatus == privateActivationReviewPass

		gates.Supported = gates.Supported && supported
		gates.SafetyComplete = gates.SafetyComplete && cell.SafetyComplete
		gates.SafetyClean = gates.SafetyClean && safetyClean
		gates.ReviewComplete = gates.ReviewComplete && reviewComplete
		gates.NoDisagreement = gates.NoDisagreement && noDisagreement
		gates.CausalEligible = gates.CausalEligible && causalEligible
		gates.PromotionEligible = gates.PromotionEligible && promotionEligible
	}
	return gates
}

func privateActivationContrasts(cells []PrivateActivationReferenceCell) []PrivateActivationContrast {
	metrics := make([]map[string]int64, len(cells))
	for index, cell := range cells {
		metrics[index] = make(map[string]int64, len(cell.Metrics))
		for _, metric := range cell.Metrics {
			metrics[index][metric.Name] = metric.Value
		}
	}
	names := make([]string, 0, len(privateActivationMetricNames))
	for name := range privateActivationMetricNames {
		present := true
		for _, cellMetrics := range metrics {
			if _, ok := cellMetrics[name]; !ok {
				present = false
				break
			}
		}
		if present {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	contrasts := make([]PrivateActivationContrast, 0, len(names)*3)
	for _, name := range names {
		implicit := metrics[0][name]
		explicit := metrics[1][name]
		developer := metrics[2][name]
		combined := metrics[3][name]
		contrasts = append(contrasts,
			PrivateActivationContrast{Factor: privateActivationFactorUserChannel, Metric: name, EstimateNumerator: explicit + combined - implicit - developer, EstimateDenominator: 2},
			PrivateActivationContrast{Factor: privateActivationFactorDeveloperChannel, Metric: name, EstimateNumerator: developer + combined - implicit - explicit, EstimateDenominator: 2},
			PrivateActivationContrast{Factor: privateActivationFactorInteraction, Metric: name, EstimateNumerator: combined - developer - explicit + implicit, EstimateDenominator: 1},
		)
	}
	return contrasts
}

func privateActivationMetrics(result Result, deterministicPass bool, qualitativeScore *int) []PrivateActivationMetric {
	passBPS := int64(0)
	if deterministicPass {
		passBPS = 10000
	}
	metrics := []PrivateActivationMetric{{Name: privateActivationMetricOutcomeBasisPoints, Value: passBPS}}
	if qualitativeScore != nil {
		metrics = append(metrics, PrivateActivationMetric{Name: privateActivationMetricQualitativeScoreBPS, Value: int64(*qualitativeScore)})
	}
	values := map[string]int64{
		"agent_turns":                int64(result.Metrics.AgentTurns),
		"atl_invocations":            int64(result.Metrics.ATLInvocations),
		"backend_requests":           int64(result.Metrics.BackendRequests),
		"delegations":                int64(result.Metrics.Delegations),
		"duplicate_backend_requests": int64(result.Metrics.DuplicateBackendRequests),
		"duration_millis":            result.Metrics.DurationMillis,
		"estimated_cost_microusd":    result.Metrics.EstimatedCostMicroUSD,
		"input_tokens":               result.Metrics.InputTokens,
		"interface_invocations":      int64(result.Metrics.InterfaceInvocations),
		"main_thread_input_tokens":   result.Metrics.MainThreadInputTokens,
		"main_thread_output_tokens":  result.Metrics.MainThreadOutputTokens,
		"output_bytes":               result.Metrics.OutputBytes,
		"output_tokens":              result.Metrics.OutputTokens,
		"remote_writes":              int64(result.Metrics.RemoteWrites),
		"tool_calls":                 int64(result.Metrics.ToolCalls),
	}
	for name, value := range values {
		if result.Coverage[name] {
			metrics = append(metrics, PrivateActivationMetric{Name: name, Value: value})
		}
	}
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].Name < metrics[j].Name })
	return metrics
}

func privateActivationReview(result Result) (string, *int) {
	if result.QualitativeReviewSet != nil {
		score := result.QualitativeReviewSet.ScoreBPS
		return result.QualitativeReviewSet.Status, &score
	}
	if result.Qualitative != nil {
		score := result.Qualitative.ScoreBPS
		return result.Qualitative.Status, &score
	}
	return privateActivationReviewIncomplete, nil
}

func privateActivationDeterministicPass(result Result) bool {
	if result.EffectiveEligibility() != EligibilitySupported {
		return false
	}
	for _, violation := range result.Violations {
		if violation.Code != "qualitative_review_failed" && violation.Code != "qualitative_review_disagreement" {
			return false
		}
	}
	return true
}

func validatePrivateActivationMetrics(cell PrivateActivationReferenceCell) error {
	if err := validatePrivateActivationReportMetrics(cell.Metrics); err != nil {
		return err
	}
	values := make(map[string]int64, len(cell.Metrics))
	for _, metric := range cell.Metrics {
		values[metric.Name] = metric.Value
	}
	wantPass := int64(0)
	if cell.DeterministicPass {
		wantPass = 10000
	}
	if value, ok := values[privateActivationMetricOutcomeBasisPoints]; !ok || value != wantPass {
		return fmt.Errorf("activation reference deterministic metric is inconsistent")
	}
	qualitative, hasQualitative := values[privateActivationMetricQualitativeScoreBPS]
	if hasQualitative != (cell.QualitativeScoreBPS != nil) || hasQualitative && qualitative != int64(*cell.QualitativeScoreBPS) {
		return fmt.Errorf("activation reference qualitative metric is inconsistent")
	}
	return nil
}

func validatePrivateActivationReportMetrics(metrics []PrivateActivationMetric) error {
	previous := ""
	for _, metric := range metrics {
		if _, ok := privateActivationMetricNames[metric.Name]; !ok || metric.Name <= previous || metric.Value < 0 || metric.Value > privateActivationMaxMetricValue {
			return fmt.Errorf("activation study report metrics are invalid")
		}
		previous = metric.Name
	}
	return nil
}

func validPrivateActivationTreatment(value string) bool {
	for _, treatment := range privateActivationTreatments {
		if value == treatment {
			return true
		}
	}
	return false
}

func validPrivateActivationEligibility(value string) bool {
	return value == EligibilitySupported || value == EligibilityUnsupportedCapability || value == EligibilityInvalidatedDrift
}

func validPrivateActivationReview(status string, score *int) bool {
	if status == privateActivationReviewIncomplete {
		return score == nil
	}
	if status != privateActivationReviewPass && status != privateActivationReviewFail && status != privateActivationReviewDisagreement {
		return false
	}
	return score != nil && *score >= 0 && *score <= 10000
}

func validPrivateActivationFactor(value string) bool {
	return value == privateActivationFactorUserChannel || value == privateActivationFactorDeveloperChannel || value == privateActivationFactorInteraction
}

func privateActivationFactorOrder(value string) int {
	switch value {
	case privateActivationFactorUserChannel:
		return 0
	case privateActivationFactorDeveloperChannel:
		return 1
	case privateActivationFactorInteraction:
		return 2
	default:
		return -1
	}
}

func validPrivateActivationGates(gates PrivateActivationGates) bool {
	if !gates.CaptureEligible || gates.SafetyClean && !gates.SafetyComplete || gates.PromotionEligible && !gates.CausalEligible {
		return false
	}
	wantCausal := gates.Supported && gates.SafetyClean && gates.ReviewComplete && gates.NoDisagreement
	return gates.CausalEligible == wantCausal
}
