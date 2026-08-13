package agenteval

// This file owns the deliberately small offline HTML projection. Canonical
// JSON remains authoritative; this package accepts only a closed,
// content-minimized view of that JSON and cannot discover or read its source.
// The resulting document is a static, non-authoritative view: it has no
// scripts, links, remote resources, or active export capability.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

const (
	HTMLSchema          = "agent-eval/html-report"
	HTMLSchemaVersion   = 1
	HTMLContractVersion = StandaloneContractVersion
	HTMLProducer        = "atl-agent-eval"
	HTMLMaxBytes        = 4 << 20
	HTMLMaxStrata       = 256
	HTMLMaxComparisons  = 4096
	HTMLMaxCount        = 1 << 20
	HTMLMaxMetric       = int64(1 << 50)
	HTMLMaxDigest       = 64
)

// ErrInvalidHTMLProjection is intentionally content-free. Callers should not
// expose source paths, private labels, or validation details in diagnostics.
var ErrInvalidHTMLProjection = errors.New("invalid_html_projection")

// ErrInvalidHTMLInput is an input-oriented alias for callers that classify
// projection failures alongside the other evaluator formats.
var ErrInvalidHTMLInput = ErrInvalidHTMLProjection

type HTMLSourceClass string

const (
	HTMLSourcePublicSynthetic  HTMLSourceClass = "public_synthetic"
	HTMLSourceContentMinimized HTMLSourceClass = "content_minimized"
)

type HTMLPrivacyTier string

const HTMLPrivacyPublicSafe HTMLPrivacyTier = "public_safe"

// HTMLProvenance contains only identities of already admitted artifacts. It
// deliberately has no source paths, scenario names, provider values, or raw
// evidence. The four analysis identities are required for every report.
type HTMLProvenance struct {
	SourceClass          HTMLSourceClass `json:"source_class"`
	Privacy              HTMLPrivacyTier `json:"privacy"`
	ManifestSHA256       string          `json:"manifest_sha256"`
	AnalysisPlanSHA256   string          `json:"analysis_plan_sha256"`
	InputSetSHA256       string          `json:"input_set_sha256"`
	AnalysisReportSHA256 string          `json:"analysis_report_sha256"`
	AggregateSHA256      string          `json:"aggregate_sha256"`
	StructureTreeSHA256  string          `json:"structure_tree_sha256,omitempty"`
	SecurityBundleSHA256 string          `json:"security_bundle_sha256,omitempty"`
	SecurityPolicySHA256 string          `json:"security_policy_sha256,omitempty"`
	RulePackSHA256       string          `json:"rule_pack_sha256,omitempty"`
}

type HTMLDimensionKind string

const (
	HTMLDimensionStage  HTMLDimensionKind = "stage"
	HTMLDimensionMetric HTMLDimensionKind = "metric"
)

type HTMLDimension string

const (
	HTMLDimensionCandidateRecall   HTMLDimension = "candidate_recall"
	HTMLDimensionSelection         HTMLDimension = "selection"
	HTMLDimensionLoad              HTMLDimension = "load"
	HTMLDimensionInstructionAccess HTMLDimension = "instruction_access"
	HTMLDimensionReferenceAccess   HTMLDimension = "reference_access"
	HTMLDimensionScriptAccess      HTMLDimension = "script_access"
	HTMLDimensionUsefulAdherence   HTMLDimension = "useful_adherence"
	HTMLDimensionVerifierOutcome   HTMLDimension = "verifier_outcome"
	HTMLDimensionOutcome           HTMLDimension = "outcome"
	HTMLDimensionInputTokens       HTMLDimension = "input_tokens"
	HTMLDimensionOutputTokens      HTMLDimension = "output_tokens"
	HTMLDimensionEstimatedCost     HTMLDimension = "estimated_cost_microusd"
	HTMLDimensionDuration          HTMLDimension = "duration_millis"
)

type HTMLInferenceStatus string

const (
	HTMLInferenceInsufficient HTMLInferenceStatus = "insufficient"
	HTMLInferenceDescriptive  HTMLInferenceStatus = "descriptive"
	HTMLInferenceInferential  HTMLInferenceStatus = "inferential"
)

type HTMLParetoRelation string

const (
	HTMLParetoUnavailable        HTMLParetoRelation = "unavailable"
	HTMLParetoEqual              HTMLParetoRelation = "equal"
	HTMLParetoCandidateDominates HTMLParetoRelation = "candidate_dominates"
	HTMLParetoReferenceDominates HTMLParetoRelation = "reference_dominates"
	HTMLParetoTradeoff           HTMLParetoRelation = "tradeoff"
)

type HTMLTreatmentRole string

const (
	HTMLRoleReference HTMLTreatmentRole = "reference"
	HTMLRoleCandidate HTMLTreatmentRole = "candidate"
)

type HTMLResourceAxis string

const (
	HTMLResourceDuration      HTMLResourceAxis = "duration_millis"
	HTMLResourceInputTokens   HTMLResourceAxis = "input_tokens"
	HTMLResourceOutputTokens  HTMLResourceAxis = "output_tokens"
	HTMLResourceEstimatedCost HTMLResourceAxis = "estimated_cost_microusd"
)

type HTMLFailureCode string

const (
	HTMLFailureMissingMember         HTMLFailureCode = "missing_member"
	HTMLFailureDuplicateMember       HTMLFailureCode = "duplicate_member"
	HTMLFailureLifecycleIncomplete   HTMLFailureCode = "lifecycle_incomplete"
	HTMLFailureLifecycleUnknown      HTMLFailureCode = "lifecycle_unknown"
	HTMLFailureUnsupportedCapability HTMLFailureCode = "unsupported_capability"
	HTMLFailureIneligible            HTMLFailureCode = "ineligible"
	HTMLFailureDrift                 HTMLFailureCode = "drift"
	HTMLFailureGradeIncomplete       HTMLFailureCode = "grade_incomplete"
	HTMLFailureCoverageMismatch      HTMLFailureCode = "coverage_mismatch"
	HTMLFailureTaskRegression        HTMLFailureCode = "task_regression"
	HTMLFailureInfrastructure        HTMLFailureCode = "infrastructure"
)

type HTMLSafetyStatus string

const (
	HTMLSafetyAdmitted           HTMLSafetyStatus = "admitted"
	HTMLSafetyClean              HTMLSafetyStatus = "clean"
	HTMLSafetyCompleteSuppressed HTMLSafetyStatus = "complete_suppressed"
	HTMLSafetyBlocked            HTMLSafetyStatus = "blocked"
	HTMLSafetyIncomplete         HTMLSafetyStatus = "incomplete"
	HTMLSafetyUnavailable        HTMLSafetyStatus = "unavailable"
)

// HTMLFraction retains exact bounded values without floating-point drift.
type HTMLFraction struct {
	Numerator   int64  `json:"numerator"`
	Denominator uint64 `json:"denominator"`
}

type HTMLInterval struct {
	ConfidenceBasisPoints uint16       `json:"confidence_basis_points"`
	Lower                 HTMLFraction `json:"lower"`
	Upper                 HTMLFraction `json:"upper"`
}

type HTMLLiftRow struct {
	ComparisonOrdinal uint32              `json:"comparison_ordinal"`
	StratumOrdinal    uint32              `json:"stratum_ordinal"`
	Kind              HTMLDimensionKind   `json:"kind"`
	Dimension         HTMLDimension       `json:"dimension"`
	Status            HTMLInferenceStatus `json:"status"`
	CompletePairs     uint32              `json:"complete_pairs"`
	ExcludedPairs     uint32              `json:"excluded_pairs"`
	Effect            HTMLFraction        `json:"effect"`
	Interval          *HTMLInterval       `json:"interval,omitempty"`
	Regression        bool                `json:"regression"`
	Pareto            HTMLParetoRelation  `json:"pareto"`
}

type HTMLActivationRow struct {
	StratumOrdinal  uint32        `json:"stratum_ordinal"`
	Observed        uint32        `json:"observed"`
	Missing         uint32        `json:"missing"`
	TruePositive    uint32        `json:"true_positive"`
	FalsePositive   uint32        `json:"false_positive"`
	TrueNegative    uint32        `json:"true_negative"`
	FalseNegative   uint32        `json:"false_negative"`
	Precision       *HTMLFraction `json:"precision,omitempty"`
	Recall          *HTMLFraction `json:"recall,omitempty"`
	FalseActivation *HTMLFraction `json:"false_activation_rate,omitempty"`
	UnnecessaryLoad *HTMLFraction `json:"unnecessary_load_rate,omitempty"`
}

type HTMLFunnelStage struct {
	Stage               HTMLDimension `json:"stage"`
	Observed            uint32        `json:"observed"`
	Reached             uint32        `json:"reached"`
	EligibleTransitions uint32        `json:"eligible_transitions"`
	Converted           uint32        `json:"converted"`
	Rate                *HTMLFraction `json:"rate,omitempty"`
	Conversion          *HTMLFraction `json:"conversion,omitempty"`
}

type HTMLFunnelRow struct {
	StratumOrdinal uint32            `json:"stratum_ordinal"`
	Role           HTMLTreatmentRole `json:"role"`
	Trials         uint32            `json:"trials"`
	Stages         []HTMLFunnelStage `json:"stages"`
}

type HTMLCoverageRow struct {
	StratumOrdinal   uint32 `json:"stratum_ordinal"`
	ExpectedRecords  uint32 `json:"expected_records"`
	ReceivedRecords  uint32 `json:"received_records"`
	UniqueRecords    uint32 `json:"unique_records"`
	MissingRecords   uint32 `json:"missing_records"`
	DuplicateRecords uint32 `json:"duplicate_records"`
	CompletePairs    uint32 `json:"complete_pairs"`
	ExcludedPairs    uint32 `json:"excluded_pairs"`
	Complete         bool   `json:"complete"`
}

type HTMLFailureCount struct {
	Code  HTMLFailureCode `json:"code"`
	Count uint32          `json:"count"`
}

type HTMLFailureRow struct {
	StratumOrdinal uint32             `json:"stratum_ordinal"`
	Failures       []HTMLFailureCount `json:"failures"`
}

type HTMLResourceValue struct {
	Available    bool   `json:"available"`
	ObservedRuns uint32 `json:"observed_runs"`
	P50          int64  `json:"p50"`
	P90          int64  `json:"p90"`
}

type HTMLResourceRow struct {
	StratumOrdinal uint32             `json:"stratum_ordinal"`
	Axis           HTMLResourceAxis   `json:"axis"`
	Reference      HTMLResourceValue  `json:"reference"`
	Candidate      HTMLResourceValue  `json:"candidate"`
	Pareto         HTMLParetoRelation `json:"pareto"`
}

type HTMLSafetySummary struct {
	StructureStatus       HTMLSafetyStatus `json:"structure_status"`
	SecurityStatus        HTMLSafetyStatus `json:"security_status"`
	StructureFindingCount uint32           `json:"structure_finding_count"`
	SecurityFindingCount  uint32           `json:"security_finding_count"`
	SuppressedFindings    uint32           `json:"suppressed_findings"`
	// SecurityCoverageComplete describes lifecycle-security rule coverage;
	// analysis strata coverage is represented independently by HTMLCoverageRow.
	SecurityCoverageComplete bool `json:"security_coverage_complete"`
	BlocksExecution          bool `json:"blocks_execution"`
	RuntimeSafetyProven      bool `json:"runtime_safety_proven"`
}

// HTMLProjectionInput is the only input accepted by ProjectHTML. It is a
// content-minimized, already-admitted view; it contains no arbitrary labels,
// paths, URLs, source text, or private evidence. Callers must construct it
// from validated canonical artifacts and preserve the source identities in
// Provenance.
type HTMLProjectionInput struct {
	Provenance HTMLProvenance
	Coverage   []HTMLCoverageRow
	Lift       []HTMLLiftRow
	Activation []HTMLActivationRow
	Funnels    []HTMLFunnelRow
	Failures   []HTMLFailureRow
	Resources  []HTMLResourceRow
	Safety     HTMLSafetySummary
}

type HTMLReport struct {
	Schema           string              `json:"schema"`
	SchemaVersion    int                 `json:"schema_version"`
	ContractVersion  string              `json:"contract_version"`
	Producer         string              `json:"producer"`
	TemplateVersion  int                 `json:"template_version"`
	Provenance       HTMLProvenance      `json:"provenance"`
	Coverage         []HTMLCoverageRow   `json:"coverage"`
	Lift             []HTMLLiftRow       `json:"lift"`
	Activation       []HTMLActivationRow `json:"activation"`
	Funnels          []HTMLFunnelRow     `json:"funnels"`
	Failures         []HTMLFailureRow    `json:"failures"`
	Resources        []HTMLResourceRow   `json:"resources"`
	Safety           HTMLSafetySummary   `json:"safety"`
	ProjectionSHA256 string              `json:"projection_sha256"`
}

// ProjectHTML normalizes an input copy, validates all closed fields and
// cross-section strata, then binds the resulting projection to a digest.
func ProjectHTML(input HTMLProjectionInput) (HTMLReport, error) {
	if err := validateHTMLProjectionInputBounds(input); err != nil {
		return HTMLReport{}, err
	}
	report := HTMLReport{
		Schema: HTMLSchema, SchemaVersion: HTMLSchemaVersion,
		ContractVersion: HTMLContractVersion, Producer: HTMLProducer,
		TemplateVersion: 1, Provenance: input.Provenance, Safety: input.Safety,
		Coverage: cloneHTMLCoverage(input.Coverage), Lift: cloneHTMLLift(input.Lift),
		Activation: cloneHTMLActivation(input.Activation), Funnels: cloneHTMLFunnels(input.Funnels),
		Failures: cloneHTMLFailures(input.Failures), Resources: cloneHTMLResources(input.Resources),
	}
	normalizeHTMLFractions(&report)
	normalizeHTMLReport(&report)
	if err := report.validateBody(); err != nil {
		return HTMLReport{}, err
	}
	report.ProjectionSHA256 = htmlReportDigest(report)
	if err := report.Validate(); err != nil {
		return HTMLReport{}, err
	}
	return report, nil
}

func validateHTMLProjectionInputBounds(input HTMLProjectionInput) error {
	if len(input.Coverage) == 0 || len(input.Coverage) > HTMLMaxStrata ||
		len(input.Lift) == 0 || len(input.Lift) > HTMLMaxComparisons ||
		len(input.Activation) == 0 || len(input.Activation) > HTMLMaxStrata ||
		len(input.Funnels) == 0 || len(input.Funnels) > 2*HTMLMaxStrata ||
		len(input.Failures) == 0 || len(input.Failures) > HTMLMaxStrata ||
		len(input.Resources) == 0 || len(input.Resources) > HTMLMaxStrata*len(htmlResourceAxes) {
		return fmt.Errorf("%w: input bounds", ErrInvalidHTMLProjection)
	}
	for _, row := range input.Funnels {
		if len(row.Stages) > len(htmlStages) {
			return fmt.Errorf("%w: funnel bounds", ErrInvalidHTMLProjection)
		}
	}
	for _, row := range input.Failures {
		if len(row.Failures) > len(htmlFailureCodes) {
			return fmt.Errorf("%w: failure bounds", ErrInvalidHTMLProjection)
		}
	}
	return nil
}

// Validate rejects a mutated, reordered, future, private, or internally
// inconsistent projection. EncodeHTML calls it before producing any bytes.
func (report HTMLReport) Validate() error {
	normalized := cloneHTMLReport(report)
	normalizeHTMLFractions(&normalized)
	if err := normalized.validateBody(); err != nil {
		return err
	}
	if !validHTMLDigest(report.ProjectionSHA256) || report.ProjectionSHA256 != htmlReportDigest(normalized) {
		return fmt.Errorf("%w: projection digest", ErrInvalidHTMLProjection)
	}
	return nil
}

// EncodeHTML produces one canonical UTF-8 document with a final LF. It never
// writes a file and has no network, process, provider, or credential effects.
func EncodeHTML(report HTMLReport) ([]byte, error) {
	normalized := cloneHTMLReport(report)
	normalizeHTMLFractions(&normalized)
	if err := normalized.Validate(); err != nil {
		return nil, err
	}
	view := htmlTemplateView{Report: normalized, CSP: htmlCSP(), Styles: template.CSS(htmlStyles)} // #nosec G203 -- htmlStyles is a package-owned constant with no caller-controlled bytes.
	var body bytes.Buffer
	if err := htmlReportTemplate.Execute(&body, view); err != nil {
		return nil, fmt.Errorf("%w: render", ErrInvalidHTMLProjection)
	}
	data := append(body.Bytes(), '\n')
	if len(data) > HTMLMaxBytes {
		return nil, fmt.Errorf("%w: html bounds", ErrInvalidHTMLProjection)
	}
	return append([]byte(nil), data...), nil
}

func EncodeHTMLProjection(report HTMLReport) ([]byte, error) { return EncodeHTML(report) }

func (report HTMLReport) validateBody() error {
	if report.Schema != HTMLSchema || report.SchemaVersion != HTMLSchemaVersion ||
		report.ContractVersion != HTMLContractVersion || report.Producer != HTMLProducer ||
		report.TemplateVersion != 1 || len(report.Coverage) == 0 || len(report.Lift) == 0 ||
		len(report.Activation) == 0 || len(report.Funnels) == 0 || len(report.Failures) == 0 ||
		len(report.Resources) == 0 || len(report.Coverage) > HTMLMaxStrata ||
		len(report.Activation) > HTMLMaxStrata || len(report.Funnels) > 2*HTMLMaxStrata ||
		len(report.Failures) > HTMLMaxStrata || len(report.Resources) > HTMLMaxStrata*len(htmlResourceAxes) ||
		len(report.Lift) > HTMLMaxComparisons {
		return fmt.Errorf("%w: report shape", ErrInvalidHTMLProjection)
	}
	if err := report.Provenance.validate(report.Safety); err != nil {
		return err
	}
	if err := validateHTMLSafety(report.Safety); err != nil {
		return err
	}
	strata := make(map[uint32]struct{}, len(report.Coverage))
	for index, row := range report.Coverage {
		if row.StratumOrdinal == 0 || row.StratumOrdinal > HTMLMaxStrata || (index > 0 && report.Coverage[index-1].StratumOrdinal >= row.StratumOrdinal) {
			return fmt.Errorf("%w: coverage strata", ErrInvalidHTMLProjection)
		}
		if err := validateHTMLCoverage(row); err != nil {
			return err
		}
		strata[row.StratumOrdinal] = struct{}{}
	}
	expectedByStratum := make(map[uint32]uint32, len(report.Coverage))
	for _, row := range report.Coverage {
		expectedByStratum[row.StratumOrdinal] = row.ExpectedRecords
	}
	if err := validateHTMLActivation(report.Activation, strata, expectedByStratum); err != nil {
		return err
	}
	if err := validateHTMLFunnels(report.Funnels, strata); err != nil {
		return err
	}
	if err := validateHTMLFailures(report.Failures, strata); err != nil {
		return err
	}
	if err := validateHTMLResources(report.Resources, strata); err != nil {
		return err
	}
	return validateHTMLLift(report.Lift, strata)
}

func (provenance HTMLProvenance) validate(safety HTMLSafetySummary) error {
	if (provenance.SourceClass != HTMLSourcePublicSynthetic && provenance.SourceClass != HTMLSourceContentMinimized) || provenance.Privacy != HTMLPrivacyPublicSafe {
		return fmt.Errorf("%w: provenance privacy", ErrInvalidHTMLProjection)
	}
	for _, digest := range []string{provenance.ManifestSHA256, provenance.AnalysisPlanSHA256, provenance.InputSetSHA256, provenance.AnalysisReportSHA256, provenance.AggregateSHA256} {
		if !validHTMLDigest(digest) {
			return fmt.Errorf("%w: provenance digest", ErrInvalidHTMLProjection)
		}
	}
	if safety.StructureStatus != HTMLSafetyUnavailable {
		if !validHTMLDigest(provenance.StructureTreeSHA256) {
			return fmt.Errorf("%w: structure provenance", ErrInvalidHTMLProjection)
		}
	} else if provenance.StructureTreeSHA256 != "" {
		return fmt.Errorf("%w: unexpected structure provenance", ErrInvalidHTMLProjection)
	}
	if safety.SecurityStatus != HTMLSafetyUnavailable {
		for _, digest := range []string{provenance.SecurityBundleSHA256, provenance.SecurityPolicySHA256, provenance.RulePackSHA256} {
			if !validHTMLDigest(digest) {
				return fmt.Errorf("%w: security provenance", ErrInvalidHTMLProjection)
			}
		}
	} else if provenance.SecurityBundleSHA256 != "" || provenance.SecurityPolicySHA256 != "" || provenance.RulePackSHA256 != "" {
		return fmt.Errorf("%w: unexpected security provenance", ErrInvalidHTMLProjection)
	}
	return nil
}

func validateHTMLSafety(s HTMLSafetySummary) error {
	if !htmlStructureStatus(s.StructureStatus) || !htmlSecurityStatus(s.SecurityStatus) || s.RuntimeSafetyProven {
		return fmt.Errorf("%w: safety state", ErrInvalidHTMLProjection)
	}
	for _, count := range []uint32{s.StructureFindingCount, s.SecurityFindingCount, s.SuppressedFindings} {
		if count > HTMLMaxCount {
			return fmt.Errorf("%w: safety bounds", ErrInvalidHTMLProjection)
		}
	}
	if s.SuppressedFindings > s.SecurityFindingCount {
		return fmt.Errorf("%w: suppressed security findings", ErrInvalidHTMLProjection)
	}
	switch s.SecurityStatus {
	case HTMLSafetyClean:
		if s.SecurityFindingCount != 0 || s.SuppressedFindings != 0 || !s.SecurityCoverageComplete {
			return fmt.Errorf("%w: clean security state", ErrInvalidHTMLProjection)
		}
	case HTMLSafetyCompleteSuppressed:
		if s.SecurityFindingCount == 0 || s.SuppressedFindings != s.SecurityFindingCount || !s.SecurityCoverageComplete {
			return fmt.Errorf("%w: suppressed security state", ErrInvalidHTMLProjection)
		}
	case HTMLSafetyBlocked:
		if s.SecurityCoverageComplete && s.SecurityFindingCount == s.SuppressedFindings {
			return fmt.Errorf("%w: blocked security state", ErrInvalidHTMLProjection)
		}
	case HTMLSafetyIncomplete:
		if s.SecurityCoverageComplete {
			return fmt.Errorf("%w: incomplete security state", ErrInvalidHTMLProjection)
		}
	case HTMLSafetyUnavailable:
		if s.SecurityCoverageComplete || s.SecurityFindingCount != 0 || s.SuppressedFindings != 0 {
			return fmt.Errorf("%w: unavailable security state", ErrInvalidHTMLProjection)
		}
	}
	// A blocked, incomplete, or unavailable static safety layer is fail-closed.
	// Only an admitted structure and a complete clean/suppressed security scan
	// can make the report execution-eligible; this remains a static finding,
	// never a runtime-safety proof.
	wantBlocks := s.StructureStatus != HTMLSafetyAdmitted ||
		(s.SecurityStatus != HTMLSafetyClean && s.SecurityStatus != HTMLSafetyCompleteSuppressed)
	if s.BlocksExecution != wantBlocks {
		return fmt.Errorf("%w: safety block state", ErrInvalidHTMLProjection)
	}
	return nil
}

func validateHTMLCoverage(row HTMLCoverageRow) error {
	if row.ExpectedRecords == 0 || row.ExpectedRecords > HTMLMaxCount || row.ReceivedRecords > HTMLMaxCount || row.UniqueRecords > row.ExpectedRecords ||
		uint64(row.UniqueRecords)+uint64(row.MissingRecords) != uint64(row.ExpectedRecords) ||
		uint64(row.UniqueRecords)+uint64(row.DuplicateRecords) != uint64(row.ReceivedRecords) ||
		row.CompletePairs > HTMLMaxCount || row.ExcludedPairs > HTMLMaxCount ||
		row.Complete != (row.MissingRecords == 0 && row.DuplicateRecords == 0) {
		return fmt.Errorf("%w: coverage values", ErrInvalidHTMLProjection)
	}
	if row.CompletePairs == 0 && row.ExcludedPairs == 0 {
		return fmt.Errorf("%w: empty coverage", ErrInvalidHTMLProjection)
	}
	return nil
}

func validateHTMLActivation(rows []HTMLActivationRow, strata map[uint32]struct{}, expectedByStratum map[uint32]uint32) error {
	if len(rows) != len(strata) {
		return fmt.Errorf("%w: activation strata", ErrInvalidHTMLProjection)
	}
	seen := map[uint32]bool{}
	for index, row := range rows {
		if _, ok := strata[row.StratumOrdinal]; !ok || seen[row.StratumOrdinal] || (index > 0 && rows[index-1].StratumOrdinal >= row.StratumOrdinal) ||
			row.Observed > HTMLMaxCount || row.Missing > HTMLMaxCount || row.TruePositive > HTMLMaxCount || row.FalsePositive > HTMLMaxCount || row.TrueNegative > HTMLMaxCount || row.FalseNegative > HTMLMaxCount {
			return fmt.Errorf("%w: activation values", ErrInvalidHTMLProjection)
		}
		if uint64(row.Observed)+uint64(row.Missing) != uint64(expectedByStratum[row.StratumOrdinal]) ||
			uint64(row.TruePositive)+uint64(row.FalsePositive)+uint64(row.TrueNegative)+uint64(row.FalseNegative) != uint64(row.Observed) {
			return fmt.Errorf("%w: activation partition", ErrInvalidHTMLProjection)
		}
		seen[row.StratumOrdinal] = true
		for rateIndex, derived := range []struct {
			value                  *HTMLFraction
			numerator, denominator uint32
		}{
			{row.Precision, row.TruePositive, row.TruePositive + row.FalsePositive},
			{row.Recall, row.TruePositive, row.TruePositive + row.FalseNegative},
			{row.FalseActivation, row.FalsePositive, row.FalsePositive + row.TrueNegative},
			{row.UnnecessaryLoad, row.FalsePositive, row.TruePositive + row.FalsePositive},
		} {
			if err := validateHTMLDerivedRate(derived.value, derived.numerator, derived.denominator); err != nil {
				return fmt.Errorf("%w: activation rate index %d value=%v expected=%d/%d", err, rateIndex, derived.value, derived.numerator, derived.denominator)
			}
		}
	}
	return nil
}

func validateHTMLFunnels(rows []HTMLFunnelRow, strata map[uint32]struct{}) error {
	if len(rows) != len(strata)*2 {
		return fmt.Errorf("%w: funnel rows", ErrInvalidHTMLProjection)
	}
	seen := map[uint32]map[HTMLTreatmentRole]bool{}
	for _, row := range rows {
		if _, ok := strata[row.StratumOrdinal]; !ok || !htmlRole(row.Role) || row.Trials == 0 || row.Trials > HTMLMaxCount || len(row.Stages) > len(htmlStages) || len(row.Stages) != len(htmlStages) {
			return fmt.Errorf("%w: funnel shape", ErrInvalidHTMLProjection)
		}
		if seen[row.StratumOrdinal] == nil {
			seen[row.StratumOrdinal] = map[HTMLTreatmentRole]bool{}
		}
		if seen[row.StratumOrdinal][row.Role] {
			return fmt.Errorf("%w: duplicate funnel role", ErrInvalidHTMLProjection)
		}
		seen[row.StratumOrdinal][row.Role] = true
		for index, stage := range row.Stages {
			if stage.Stage != htmlStages[index] || stage.Observed > HTMLMaxCount || stage.Observed > row.Trials || stage.Reached > stage.Observed || stage.EligibleTransitions > row.Trials || stage.EligibleTransitions > stage.Observed || stage.Converted > stage.EligibleTransitions {
				return fmt.Errorf("%w: funnel stage", ErrInvalidHTMLProjection)
			}
			if index == 0 {
				if stage.EligibleTransitions != stage.Observed || stage.Converted != stage.Reached {
					return fmt.Errorf("%w: first funnel transition", ErrInvalidHTMLProjection)
				}
			} else {
				previous := row.Stages[index-1]
				if stage.EligibleTransitions > previous.Reached ||
					stage.EligibleTransitions < htmlIntersectionLowerBound(previous.Reached, stage.Observed, row.Trials) ||
					stage.Converted > stage.Reached ||
					stage.Converted < htmlIntersectionLowerBound(stage.EligibleTransitions, stage.Reached, stage.Observed) {
					return fmt.Errorf("%w: funnel transition", ErrInvalidHTMLProjection)
				}
			}
			if err := validateHTMLDerivedRate(stage.Rate, stage.Reached, stage.Observed); err != nil {
				return fmt.Errorf("%w: funnel rate stage %d", err, index)
			}
			if err := validateHTMLDerivedRate(stage.Conversion, stage.Converted, stage.EligibleTransitions); err != nil {
				return fmt.Errorf("%w: funnel conversion stage %d", err, index)
			}
		}
	}
	for stratum := range strata {
		if !seen[stratum][HTMLRoleReference] || !seen[stratum][HTMLRoleCandidate] {
			return fmt.Errorf("%w: incomplete funnel roles", ErrInvalidHTMLProjection)
		}
	}
	for index := 1; index < len(rows); index++ {
		if htmlFunnelKey(rows[index-1]) >= htmlFunnelKey(rows[index]) {
			return fmt.Errorf("%w: funnel order", ErrInvalidHTMLProjection)
		}
	}
	return nil
}

func validateHTMLFailures(rows []HTMLFailureRow, strata map[uint32]struct{}) error {
	if len(rows) != len(strata) {
		return fmt.Errorf("%w: failure strata", ErrInvalidHTMLProjection)
	}
	seen := map[uint32]bool{}
	for index, row := range rows {
		if _, ok := strata[row.StratumOrdinal]; !ok || seen[row.StratumOrdinal] || (index > 0 && rows[index-1].StratumOrdinal >= row.StratumOrdinal) || len(row.Failures) != len(htmlFailureCodes) {
			return fmt.Errorf("%w: failure shape", ErrInvalidHTMLProjection)
		}
		seen[row.StratumOrdinal] = true
		for index, failure := range row.Failures {
			if failure.Code != htmlFailureCodes[index] || failure.Count > HTMLMaxCount {
				return fmt.Errorf("%w: failure code", ErrInvalidHTMLProjection)
			}
		}
	}
	for stratum := range strata {
		if !seen[stratum] {
			return fmt.Errorf("%w: missing failure stratum", ErrInvalidHTMLProjection)
		}
	}
	return nil
}

func validateHTMLResources(rows []HTMLResourceRow, strata map[uint32]struct{}) error {
	if len(rows) != len(strata)*len(htmlResourceAxes) {
		return fmt.Errorf("%w: resource rows", ErrInvalidHTMLProjection)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if _, ok := strata[row.StratumOrdinal]; !ok || !htmlResourceAxis(row.Axis) {
			return fmt.Errorf("%w: resource axis", ErrInvalidHTMLProjection)
		}
		key := fmt.Sprintf("%d/%s", row.StratumOrdinal, row.Axis)
		if seen[key] {
			return fmt.Errorf("%w: duplicate resource axis", ErrInvalidHTMLProjection)
		}
		seen[key] = true
		if err := validateHTMLResourceValue(row.Reference); err != nil {
			return err
		}
		if err := validateHTMLResourceValue(row.Candidate); err != nil {
			return err
		}
		if !htmlPareto(row.Pareto) || row.Pareto != htmlResourcePareto(row.Reference, row.Candidate) {
			return fmt.Errorf("%w: resource pareto", ErrInvalidHTMLProjection)
		}
	}
	if len(seen) != len(strata)*len(htmlResourceAxes) {
		return fmt.Errorf("%w: incomplete resource axes", ErrInvalidHTMLProjection)
	}
	for index := 1; index < len(rows); index++ {
		if htmlResourceKey(rows[index-1]) >= htmlResourceKey(rows[index]) {
			return fmt.Errorf("%w: resource order", ErrInvalidHTMLProjection)
		}
	}
	return nil
}

func validateHTMLResourceValue(value HTMLResourceValue) error {
	if value.ObservedRuns > HTMLMaxCount || value.P50 < 0 || value.P90 < 0 || value.P50 > HTMLMaxMetric || value.P90 > HTMLMaxMetric || value.P90 < value.P50 || (!value.Available && (value.ObservedRuns != 0 || value.P50 != 0 || value.P90 != 0)) {
		return fmt.Errorf("%w: resource values", ErrInvalidHTMLProjection)
	}
	if value.Available && value.ObservedRuns == 0 {
		return fmt.Errorf("%w: resource coverage", ErrInvalidHTMLProjection)
	}
	return nil
}

func htmlResourcePareto(reference, candidate HTMLResourceValue) HTMLParetoRelation {
	if !reference.Available || !candidate.Available {
		return HTMLParetoUnavailable
	}
	candidateNoWorse := candidate.P50 <= reference.P50 && candidate.P90 <= reference.P90
	referenceNoWorse := reference.P50 <= candidate.P50 && reference.P90 <= candidate.P90
	candidateBetter := candidate.P50 < reference.P50 || candidate.P90 < reference.P90
	referenceBetter := reference.P50 < candidate.P50 || reference.P90 < candidate.P90
	switch {
	case candidateNoWorse && candidateBetter:
		return HTMLParetoCandidateDominates
	case referenceNoWorse && referenceBetter:
		return HTMLParetoReferenceDominates
	case !candidateBetter && !referenceBetter:
		return HTMLParetoEqual
	default:
		return HTMLParetoTradeoff
	}
}

func validateHTMLLift(rows []HTMLLiftRow, strata map[uint32]struct{}) error {
	seen := map[string]bool{}
	for _, row := range rows {
		if _, ok := strata[row.StratumOrdinal]; !ok || row.ComparisonOrdinal == 0 || row.ComparisonOrdinal > HTMLMaxComparisons || !htmlDimensionKind(row.Kind) || !htmlDimension(row.Dimension) || !htmlDimensionMatchesKind(row.Kind, row.Dimension) || !htmlStatus(row.Status) || row.CompletePairs > HTMLMaxCount || row.ExcludedPairs > HTMLMaxCount || !htmlPareto(row.Pareto) {
			return fmt.Errorf("%w: lift shape", ErrInvalidHTMLProjection)
		}
		if row.CompletePairs == 0 && row.ExcludedPairs == 0 {
			return fmt.Errorf("%w: empty lift coverage", ErrInvalidHTMLProjection)
		}
		if err := validateHTMLFraction(row.Effect); err != nil {
			return err
		}
		if row.Status == HTMLInferenceInferential {
			if row.Interval == nil {
				return fmt.Errorf("%w: missing lift interval", ErrInvalidHTMLProjection)
			}
		} else if row.Interval != nil {
			return fmt.Errorf("%w: noninferential interval", ErrInvalidHTMLProjection)
		}
		if row.Interval != nil {
			if row.Interval.ConfidenceBasisPoints < 5000 || row.Interval.ConfidenceBasisPoints >= 10000 || validateHTMLFraction(row.Interval.Lower) != nil || validateHTMLFraction(row.Interval.Upper) != nil || compareHTMLFractions(row.Interval.Lower, row.Interval.Upper) > 0 {
				return fmt.Errorf("%w: lift interval", ErrInvalidHTMLProjection)
			}
		}
		key := fmt.Sprintf("%d/%d/%s", row.ComparisonOrdinal, row.StratumOrdinal, row.Dimension)
		if seen[key] {
			return fmt.Errorf("%w: duplicate lift dimension", ErrInvalidHTMLProjection)
		}
		seen[key] = true
	}
	for index := 1; index < len(rows); index++ {
		if htmlLiftKey(rows[index-1]) >= htmlLiftKey(rows[index]) {
			return fmt.Errorf("%w: lift order", ErrInvalidHTMLProjection)
		}
	}
	return nil
}

func validateHTMLFraction(value HTMLFraction) error {
	if value.Denominator == 0 || value.Denominator > HTMLMaxCount || value.Numerator < -HTMLMaxMetric || value.Numerator > HTMLMaxMetric {
		return fmt.Errorf("%w: fraction", ErrInvalidHTMLProjection)
	}
	return nil
}

func validateHTMLOptionalFraction(value *HTMLFraction) error {
	if value != nil {
		return validateHTMLFraction(*value)
	}
	return nil
}

func validateHTMLRate(value *HTMLFraction) error {
	if err := validateHTMLOptionalFraction(value); err != nil {
		return err
	}
	if value != nil && (value.Numerator < 0 || uint64(value.Numerator) > value.Denominator) {
		return fmt.Errorf("%w: rate", ErrInvalidHTMLProjection)
	}
	return nil
}

func validateHTMLDerivedRate(value *HTMLFraction, numerator, denominator uint32) error {
	if denominator == 0 {
		if value != nil {
			return fmt.Errorf("%w: zero-denominator rate", ErrInvalidHTMLProjection)
		}
		return nil
	}
	if value == nil {
		return fmt.Errorf("%w: derived rate", ErrInvalidHTMLProjection)
	}
	if err := validateHTMLRate(value); err != nil {
		return err
	}
	want := new(big.Rat).SetFrac(new(big.Int).SetUint64(uint64(numerator)), new(big.Int).SetUint64(uint64(denominator)))
	got := new(big.Rat).SetFrac(big.NewInt(value.Numerator), new(big.Int).SetUint64(value.Denominator))
	if got.Cmp(want) != 0 {
		return fmt.Errorf("%w: derived rate", ErrInvalidHTMLProjection)
	}
	return nil
}

func htmlIntersectionLowerBound(left, right, universe uint32) uint32 {
	sum := uint64(left) + uint64(right)
	if sum <= uint64(universe) {
		return 0
	}
	difference := sum - uint64(universe)
	if difference > uint64(^uint32(0)) {
		// This is reachable only for callers that bypass the validated count
		// bounds. Preserve a safe bounded result rather than wrapping.
		return ^uint32(0)
	}
	return uint32(difference) // #nosec G115 -- explicit max-uint32 guard above.
}

func compareHTMLFractions(left, right HTMLFraction) int {
	leftNumerator := big.NewInt(left.Numerator)
	rightNumerator := big.NewInt(right.Numerator)
	leftNumerator.Mul(leftNumerator, new(big.Int).SetUint64(right.Denominator))
	rightNumerator.Mul(rightNumerator, new(big.Int).SetUint64(left.Denominator))
	return leftNumerator.Cmp(rightNumerator)
}

func normalizeHTMLReport(report *HTMLReport) {
	sort.Slice(report.Coverage, func(i, j int) bool { return report.Coverage[i].StratumOrdinal < report.Coverage[j].StratumOrdinal })
	sort.Slice(report.Activation, func(i, j int) bool { return report.Activation[i].StratumOrdinal < report.Activation[j].StratumOrdinal })
	sort.Slice(report.Funnels, func(i, j int) bool { return htmlFunnelKey(report.Funnels[i]) < htmlFunnelKey(report.Funnels[j]) })
	sort.Slice(report.Failures, func(i, j int) bool { return report.Failures[i].StratumOrdinal < report.Failures[j].StratumOrdinal })
	sort.Slice(report.Resources, func(i, j int) bool {
		return htmlResourceKey(report.Resources[i]) < htmlResourceKey(report.Resources[j])
	})
	sort.Slice(report.Lift, func(i, j int) bool { return htmlLiftKey(report.Lift[i]) < htmlLiftKey(report.Lift[j]) })
	for index := range report.Failures {
		sort.Slice(report.Failures[index].Failures, func(i, j int) bool {
			return htmlFailureRank(report.Failures[index].Failures[i].Code) < htmlFailureRank(report.Failures[index].Failures[j].Code)
		})
	}
}

func htmlLiftKey(row HTMLLiftRow) string {
	return fmt.Sprintf("%010d/%010d/%02d/%s", row.ComparisonOrdinal, row.StratumOrdinal, htmlDimensionRank(row.Dimension), row.Dimension)
}
func htmlFunnelKey(row HTMLFunnelRow) string {
	return fmt.Sprintf("%010d/%s", row.StratumOrdinal, row.Role)
}
func htmlResourceKey(row HTMLResourceRow) string {
	return fmt.Sprintf("%010d/%02d/%s", row.StratumOrdinal, htmlResourceRank(row.Axis), row.Axis)
}

var htmlStages = []HTMLDimension{HTMLDimensionCandidateRecall, HTMLDimensionSelection, HTMLDimensionLoad, HTMLDimensionInstructionAccess, HTMLDimensionReferenceAccess, HTMLDimensionScriptAccess, HTMLDimensionUsefulAdherence, HTMLDimensionVerifierOutcome}
var htmlFailureCodes = []HTMLFailureCode{HTMLFailureMissingMember, HTMLFailureDuplicateMember, HTMLFailureLifecycleIncomplete, HTMLFailureLifecycleUnknown, HTMLFailureUnsupportedCapability, HTMLFailureIneligible, HTMLFailureDrift, HTMLFailureGradeIncomplete, HTMLFailureCoverageMismatch, HTMLFailureTaskRegression, HTMLFailureInfrastructure}
var htmlResourceAxes = []HTMLResourceAxis{HTMLResourceDuration, HTMLResourceInputTokens, HTMLResourceOutputTokens, HTMLResourceEstimatedCost}

func htmlDimensionRank(value HTMLDimension) int {
	for index, candidate := range append(append([]HTMLDimension{}, htmlStages...), HTMLDimensionOutcome, HTMLDimensionInputTokens, HTMLDimensionOutputTokens, HTMLDimensionEstimatedCost, HTMLDimensionDuration) {
		if value == candidate {
			return index
		}
	}
	return len(htmlStages) + 5
}
func htmlResourceRank(value HTMLResourceAxis) int {
	for index, candidate := range htmlResourceAxes {
		if value == candidate {
			return index
		}
	}
	return len(htmlResourceAxes)
}
func htmlFailureRank(value HTMLFailureCode) int {
	for index, candidate := range htmlFailureCodes {
		if value == candidate {
			return index
		}
	}
	return len(htmlFailureCodes)
}
func htmlDimension(value HTMLDimension) bool { return htmlDimensionRank(value) < len(htmlStages)+5 }
func htmlDimensionMatchesKind(kind HTMLDimensionKind, value HTMLDimension) bool {
	if kind == HTMLDimensionStage {
		for _, stage := range htmlStages {
			if stage == value {
				return true
			}
		}
		return false
	}
	return kind == HTMLDimensionMetric && !htmlDimensionMatchesKind(HTMLDimensionStage, value)
}
func htmlDimensionKind(value HTMLDimensionKind) bool {
	return value == HTMLDimensionStage || value == HTMLDimensionMetric
}
func htmlStatus(value HTMLInferenceStatus) bool {
	return value == HTMLInferenceInsufficient || value == HTMLInferenceDescriptive || value == HTMLInferenceInferential
}
func htmlRole(value HTMLTreatmentRole) bool {
	return value == HTMLRoleReference || value == HTMLRoleCandidate
}
func htmlResourceAxis(value HTMLResourceAxis) bool {
	return htmlResourceRank(value) < len(htmlResourceAxes)
}
func htmlPareto(value HTMLParetoRelation) bool {
	return value == HTMLParetoUnavailable || value == HTMLParetoEqual || value == HTMLParetoCandidateDominates || value == HTMLParetoReferenceDominates || value == HTMLParetoTradeoff
}
func htmlStructureStatus(value HTMLSafetyStatus) bool {
	return value == HTMLSafetyAdmitted || value == HTMLSafetyBlocked || value == HTMLSafetyIncomplete || value == HTMLSafetyUnavailable
}
func htmlSecurityStatus(value HTMLSafetyStatus) bool {
	return value == HTMLSafetyClean || value == HTMLSafetyCompleteSuppressed || value == HTMLSafetyBlocked || value == HTMLSafetyIncomplete || value == HTMLSafetyUnavailable
}

func validHTMLDigest(value string) bool {
	if len(value) != HTMLMaxDigest || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func htmlReportDigest(report HTMLReport) string {
	projection := struct {
		Schema          string              `json:"schema"`
		SchemaVersion   int                 `json:"schema_version"`
		ContractVersion string              `json:"contract_version"`
		Producer        string              `json:"producer"`
		TemplateVersion int                 `json:"template_version"`
		Provenance      HTMLProvenance      `json:"provenance"`
		Coverage        []HTMLCoverageRow   `json:"coverage"`
		Lift            []HTMLLiftRow       `json:"lift"`
		Activation      []HTMLActivationRow `json:"activation"`
		Funnels         []HTMLFunnelRow     `json:"funnels"`
		Failures        []HTMLFailureRow    `json:"failures"`
		Resources       []HTMLResourceRow   `json:"resources"`
		Safety          HTMLSafetySummary   `json:"safety"`
	}{report.Schema, report.SchemaVersion, report.ContractVersion, report.Producer, report.TemplateVersion, report.Provenance, report.Coverage, report.Lift, report.Activation, report.Funnels, report.Failures, report.Resources, report.Safety}
	data, _ := json.Marshal(projection)
	hash := sha256.New()
	_, _ = hash.Write([]byte("agent-eval/html-report/v1\x00"))
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func cloneHTMLCoverage(rows []HTMLCoverageRow) []HTMLCoverageRow {
	return append([]HTMLCoverageRow(nil), rows...)
}
func cloneHTMLLift(rows []HTMLLiftRow) []HTMLLiftRow {
	result := append([]HTMLLiftRow(nil), rows...)
	for index := range result {
		if rows[index].Interval != nil {
			interval := *rows[index].Interval
			result[index].Interval = &interval
		}
	}
	return result
}
func cloneHTMLActivation(rows []HTMLActivationRow) []HTMLActivationRow {
	result := append([]HTMLActivationRow(nil), rows...)
	for index := range result {
		result[index] = cloneHTMLActivationRow(rows[index])
	}
	return result
}
func cloneHTMLActivationRow(row HTMLActivationRow) HTMLActivationRow {
	result := row
	result.Precision = cloneHTMLFraction(row.Precision)
	result.Recall = cloneHTMLFraction(row.Recall)
	result.FalseActivation = cloneHTMLFraction(row.FalseActivation)
	result.UnnecessaryLoad = cloneHTMLFraction(row.UnnecessaryLoad)
	return result
}
func cloneHTMLFraction(value *HTMLFraction) *HTMLFraction {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneHTMLFunnels(rows []HTMLFunnelRow) []HTMLFunnelRow {
	result := append([]HTMLFunnelRow(nil), rows...)
	for index := range result {
		result[index].Stages = append([]HTMLFunnelStage(nil), rows[index].Stages...)
		for stage := range result[index].Stages {
			result[index].Stages[stage].Rate = cloneHTMLFraction(rows[index].Stages[stage].Rate)
			result[index].Stages[stage].Conversion = cloneHTMLFraction(rows[index].Stages[stage].Conversion)
		}
	}
	return result
}
func cloneHTMLFailures(rows []HTMLFailureRow) []HTMLFailureRow {
	result := append([]HTMLFailureRow(nil), rows...)
	for index := range result {
		result[index].Failures = append([]HTMLFailureCount(nil), rows[index].Failures...)
	}
	return result
}
func cloneHTMLResources(rows []HTMLResourceRow) []HTMLResourceRow {
	return append([]HTMLResourceRow(nil), rows...)
}

func cloneHTMLReport(report HTMLReport) HTMLReport {
	result := report
	result.Coverage = cloneHTMLCoverage(report.Coverage)
	result.Lift = cloneHTMLLift(report.Lift)
	result.Activation = cloneHTMLActivation(report.Activation)
	result.Funnels = cloneHTMLFunnels(report.Funnels)
	result.Failures = cloneHTMLFailures(report.Failures)
	result.Resources = cloneHTMLResources(report.Resources)
	return result
}

// normalizeHTMLFractions reduces equivalent bounded fractions before validation,
// digesting, and rendering. Invalid bounds are intentionally left unchanged so
// validation still rejects them rather than turning an oversized value valid.
func normalizeHTMLFractions(report *HTMLReport) {
	for index := range report.Lift {
		normalizeHTMLFractionValue(&report.Lift[index].Effect)
		if report.Lift[index].Interval != nil {
			normalizeHTMLFractionValue(&report.Lift[index].Interval.Lower)
			normalizeHTMLFractionValue(&report.Lift[index].Interval.Upper)
		}
	}
	for index := range report.Activation {
		normalizeHTMLFraction(report.Activation[index].Precision)
		normalizeHTMLFraction(report.Activation[index].Recall)
		normalizeHTMLFraction(report.Activation[index].FalseActivation)
		normalizeHTMLFraction(report.Activation[index].UnnecessaryLoad)
	}
	for index := range report.Funnels {
		for stage := range report.Funnels[index].Stages {
			normalizeHTMLFraction(report.Funnels[index].Stages[stage].Rate)
			normalizeHTMLFraction(report.Funnels[index].Stages[stage].Conversion)
		}
	}
}

func normalizeHTMLFraction(value *HTMLFraction) {
	if value != nil {
		normalizeHTMLFractionValue(value)
	}
}

func normalizeHTMLFractionValue(value *HTMLFraction) {
	if value == nil || value.Denominator == 0 || value.Denominator > HTMLMaxCount || value.Numerator < -HTMLMaxMetric || value.Numerator > HTMLMaxMetric {
		return
	}
	rat := new(big.Rat).SetFrac(big.NewInt(value.Numerator), new(big.Int).SetUint64(value.Denominator))
	if !rat.Num().IsInt64() || !rat.Denom().IsUint64() {
		return
	}
	value.Numerator = rat.Num().Int64()
	value.Denominator = rat.Denom().Uint64()
}

func (fraction HTMLFraction) String() string {
	return strconv.FormatInt(fraction.Numerator, 10) + "/" + strconv.FormatUint(fraction.Denominator, 10)
}

const htmlStyles = `body{background:#fff;color:#202124;font:16px system-ui,sans-serif;line-height:1.45;margin:0 auto;max-width:1100px;padding:2rem}h1,h2{line-height:1.15}h1{font-size:2rem}h2{border-bottom:1px solid #dadce0;padding-bottom:.35rem}table{border-collapse:collapse;margin:1rem 0;width:100%}th,td{border:1px solid #dadce0;padding:.4rem;text-align:left;vertical-align:top}th{background:#f1f3f4}code{font-family:ui-monospace,monospace;overflow-wrap:anywhere}.notice{background:#fff8e1;border:1px solid #f9ab00;padding:.7rem}.state-blocked,.state-incomplete{font-weight:700}.muted{color:#5f6368}`

func htmlCSP() string {
	hash := sha256.Sum256([]byte(htmlStyles))
	return "default-src 'none'; script-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(hash[:]) + "'; img-src 'none'; font-src 'none'; connect-src 'none'; media-src 'none'; object-src 'none'; frame-src 'none'; worker-src 'none'; manifest-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}

type htmlTemplateView struct {
	Report HTMLReport
	CSP    string
	Styles template.CSS
}

var htmlReportTemplate = template.Must(template.New("agent-eval-html-report").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="referrer" content="no-referrer"><meta http-equiv="Content-Security-Policy" content="{{.CSP}}"><style>{{.Styles}}</style><title>Agent evaluation report</title></head><body>
<header><h1>Agent evaluation report</h1><p class="notice">Non-authoritative offline projection. Canonical JSON remains the source of truth.</p></header>
<section><h2>Artifact provenance</h2><table><tr><th>Source class</th><td>{{.Report.Provenance.SourceClass}}</td></tr><tr><th>Privacy</th><td>{{.Report.Provenance.Privacy}}</td></tr><tr><th>Manifest</th><td><code>{{.Report.Provenance.ManifestSHA256}}</code></td></tr><tr><th>Analysis plan</th><td><code>{{.Report.Provenance.AnalysisPlanSHA256}}</code></td></tr><tr><th>Input set</th><td><code>{{.Report.Provenance.InputSetSHA256}}</code></td></tr><tr><th>Analysis report</th><td><code>{{.Report.Provenance.AnalysisReportSHA256}}</code></td></tr><tr><th>Aggregate</th><td><code>{{.Report.Provenance.AggregateSHA256}}</code></td></tr><tr><th>Structure tree</th><td>{{if .Report.Provenance.StructureTreeSHA256}}<code>{{.Report.Provenance.StructureTreeSHA256}}</code>{{else}}not available{{end}}</td></tr><tr><th>Security bundle</th><td>{{if .Report.Provenance.SecurityBundleSHA256}}<code>{{.Report.Provenance.SecurityBundleSHA256}}</code>{{else}}not available{{end}}</td></tr><tr><th>Security policy</th><td>{{if .Report.Provenance.SecurityPolicySHA256}}<code>{{.Report.Provenance.SecurityPolicySHA256}}</code>{{else}}not available{{end}}</td></tr><tr><th>Rule pack</th><td>{{if .Report.Provenance.RulePackSHA256}}<code>{{.Report.Provenance.RulePackSHA256}}</code>{{else}}not available{{end}}</td></tr><tr><th>Projection</th><td><code>{{.Report.ProjectionSHA256}}</code></td></tr></table></section>
<section><h2>Treatment lift and uncertainty</h2><table><tr><th>Comparison</th><th>Stratum</th><th>Dimension</th><th>Status</th><th>Effect</th><th>Interval</th><th>Regression</th><th>Pareto</th></tr>{{range .Report.Lift}}<tr><td>{{.ComparisonOrdinal}}</td><td>{{.StratumOrdinal}}</td><td>{{.Kind}} / {{.Dimension}}</td><td>{{.Status}}</td><td>{{.Effect.Numerator}}/{{.Effect.Denominator}}</td><td>{{if .Interval}}{{.Interval.Lower.Numerator}}/{{.Interval.Lower.Denominator}} — {{.Interval.Upper.Numerator}}/{{.Interval.Upper.Denominator}} ({{.Interval.ConfidenceBasisPoints}} bps){{else}}not available{{end}}</td><td>{{.Regression}}</td><td>{{.Pareto}}</td></tr>{{end}}</table></section>
<section><h2>Activation funnel</h2>{{range .Report.Activation}}<h3>Stratum {{.StratumOrdinal}}</h3><table><tr><th>Observed</th><th>Missing</th><th>True positive</th><th>False positive</th><th>True negative</th><th>False negative</th><th>Precision</th><th>Recall</th><th>False activation</th><th>Unnecessary load</th></tr><tr><td>{{.Observed}}</td><td>{{.Missing}}</td><td>{{.TruePositive}}</td><td>{{.FalsePositive}}</td><td>{{.TrueNegative}}</td><td>{{.FalseNegative}}</td><td>{{if .Precision}}{{.Precision.Numerator}}/{{.Precision.Denominator}}{{else}}not available{{end}}</td><td>{{if .Recall}}{{.Recall.Numerator}}/{{.Recall.Denominator}}{{else}}not available{{end}}</td><td>{{if .FalseActivation}}{{.FalseActivation.Numerator}}/{{.FalseActivation.Denominator}}{{else}}not available{{end}}</td><td>{{if .UnnecessaryLoad}}{{.UnnecessaryLoad.Numerator}}/{{.UnnecessaryLoad.Denominator}}{{else}}not available{{end}}</td></tr></table>{{end}}{{range .Report.Funnels}}<h3>Stratum {{.StratumOrdinal}} — {{.Role}}</h3><table><tr><th>Stage</th><th>Observed</th><th>Reached</th><th>Eligible transitions</th><th>Converted</th><th>Rate</th><th>Conversion</th></tr>{{range .Stages}}<tr><td>{{.Stage}}</td><td>{{.Observed}}</td><td>{{.Reached}}</td><td>{{.EligibleTransitions}}</td><td>{{.Converted}}</td><td>{{if .Rate}}{{.Rate.Numerator}}/{{.Rate.Denominator}}{{else}}not available{{end}}</td><td>{{if .Conversion}}{{.Conversion.Numerator}}/{{.Conversion.Denominator}}{{else}}not available{{end}}</td></tr>{{end}}</table>{{end}}</section>
<section><h2>Coverage and failure taxonomy</h2>{{range .Report.Coverage}}<h3>Stratum {{.StratumOrdinal}}</h3><p>Expected {{.ExpectedRecords}}, received {{.ReceivedRecords}}, unique {{.UniqueRecords}}, missing {{.MissingRecords}}, duplicate {{.DuplicateRecords}}, complete pairs {{.CompletePairs}}, excluded pairs {{.ExcludedPairs}}; complete={{.Complete}}</p>{{end}}{{range .Report.Failures}}<table><caption>Stratum {{.StratumOrdinal}}</caption><tr><th>Failure class</th><th>Count</th></tr>{{range .Failures}}<tr><td>{{.Code}}</td><td>{{.Count}}</td></tr>{{end}}</table>{{end}}</section>
<section><h2>Resource Pareto views</h2>{{range .Report.Resources}}<table><caption>Stratum {{.StratumOrdinal}} — {{.Axis}} — {{.Pareto}}</caption><tr><th>Treatment</th><th>Available</th><th>Observed runs</th><th>P50</th><th>P90</th></tr><tr><td>reference</td><td>{{.Reference.Available}}</td><td>{{.Reference.ObservedRuns}}</td><td>{{.Reference.P50}}</td><td>{{.Reference.P90}}</td></tr><tr><td>candidate</td><td>{{.Candidate.Available}}</td><td>{{.Candidate.ObservedRuns}}</td><td>{{.Candidate.P50}}</td><td>{{.Candidate.P90}}</td></tr></table>{{end}}</section>
<section><h2>Safety states</h2><table><tr><th>Structural admission</th><td>{{.Report.Safety.StructureStatus}}</td></tr><tr><th>Lifecycle security</th><td>{{.Report.Safety.SecurityStatus}}</td></tr><tr><th>Structure findings</th><td>{{.Report.Safety.StructureFindingCount}}</td></tr><tr><th>Security findings</th><td>{{.Report.Safety.SecurityFindingCount}}</td></tr><tr><th>Suppressed findings</th><td>{{.Report.Safety.SuppressedFindings}}</td></tr><tr><th>Security coverage complete</th><td>{{.Report.Safety.SecurityCoverageComplete}}</td></tr><tr><th>Blocks execution</th><td>{{.Report.Safety.BlocksExecution}}</td></tr><tr><th>Runtime safety proven</th><td>{{.Report.Safety.RuntimeSafetyProven}}</td></tr></table></section>
</body></html>`))
