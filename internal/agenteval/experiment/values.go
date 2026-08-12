package experiment

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

var closedCapabilities = []CapabilityID{
	CapabilityChannelAdapterNative,
	CapabilityChannelCombined,
	CapabilityChannelDeveloper,
	CapabilityChannelExplicitUser,
	CapabilityChannelImplicit,
	CapabilityConditionAutonomousOracle,
	CapabilityConditionCurrent,
	CapabilityConditionForcedOracle,
	CapabilityConditionNone,
	CapabilityConditionOracleDistractors,
	CapabilityConditionPrevious,
	CapabilityConditionRetrievedAbsent,
	CapabilityConditionRetrievedPresent,
	CapabilityControlAdversarialDistractor,
	CapabilityControlIrrelevant,
	CapabilityControlNearMissNegative,
	CapabilityControlPositive,
	CapabilityControlStaleVersionMismatch,
	CapabilityControlUnsupportedDomain,
	CapabilityObserveCandidateRecall,
	CapabilityObserveDuration,
	CapabilityObserveCost,
	CapabilityObserveInputTokens,
	CapabilityObserveInstructionAccess,
	CapabilityObserveLoad,
	CapabilityObserveOutcome,
	CapabilityObserveOutputTokens,
	CapabilityObserveReferenceAccess,
	CapabilityObserveScriptAccess,
	CapabilityObserveSelection,
	CapabilityObserveUsefulAdherence,
	CapabilityObserveVerifierOutcome,
}

var closedStages = []FunnelStage{
	StageCandidateRecall,
	StageSelection,
	StageLoad,
	StageInstructionAccess,
	StageReferenceAccess,
	StageScriptAccess,
	StageUsefulAdherence,
	StageVerifierOutcome,
}

var closedExclusions = map[ExclusionReason]bool{
	ExclusionNone:                  true,
	ExclusionMissingMember:         true,
	ExclusionDuplicateMember:       true,
	ExclusionLifecycleIncomplete:   true,
	ExclusionLifecycleUnknown:      true,
	ExclusionUnsupportedCapability: true,
	ExclusionIneligible:            true,
	ExclusionDrift:                 true,
	ExclusionGradeIncomplete:       true,
	ExclusionCoverageMismatch:      true,
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validOptionalDigest(value string) bool { return value == "" || validDigest(value) }

func validDerivedID(prefix, value string) bool {
	return len(value) == len(prefix)+1+sha256.Size*2 && strings.HasPrefix(value, prefix+"-") &&
		validDigest(value[len(prefix)+1:])
}

func hashParts(domain string, parts ...[]byte) string {
	hash := sha256.New()
	writeHashPart(hash, []byte("agent-eval/experiment/v1"))
	writeHashPart(hash, []byte(domain))
	for _, part := range parts {
		writeHashPart(hash, part)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func writeHashPart(writer hashWriter, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

func digestProjection(domain string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return hashParts(domain, data), nil
}

func armKey(arm ArmSelector) string {
	return strings.Join([]string{
		string(arm.Condition),
		string(arm.ActivationChannel),
		string(arm.SelectionAuthority),
		string(arm.Control),
	}, "\x00")
}

func treatmentRequestKey(request TreatmentRequest) string {
	parts := []string{
		armKey(request.Arm),
		request.SkillSHA256,
		request.SkillVersionSHA256,
		strings.Join(request.DistractorSHA256, ","),
		request.RetrieverSHA256,
		request.ControlSHA256,
		string(request.ControlProvenance),
		request.ExecutionBindingSHA256,
	}
	if request.ExpectedActivation {
		parts = append(parts, "1")
	} else {
		parts = append(parts, "0")
	}
	return strings.Join(parts, "\x00")
}

func treatmentID(request TreatmentRequest) (string, error) {
	projection := struct {
		Arm                    ArmSelector       `json:"arm"`
		SkillSHA256            string            `json:"skill_sha256,omitempty"`
		SkillVersionSHA256     string            `json:"skill_version_sha256,omitempty"`
		DistractorSHA256       []string          `json:"distractor_sha256"`
		RetrieverSHA256        string            `json:"retriever_sha256,omitempty"`
		ControlSHA256          string            `json:"control_sha256"`
		ControlProvenance      ControlProvenance `json:"control_provenance"`
		ExecutionBindingSHA256 string            `json:"execution_binding_sha256"`
		ExpectedActivation     bool              `json:"expected_activation"`
	}{
		request.Arm,
		request.SkillSHA256,
		request.SkillVersionSHA256,
		append([]string{}, request.DistractorSHA256...),
		request.RetrieverSHA256,
		request.ControlSHA256,
		request.ControlProvenance,
		request.ExecutionBindingSHA256,
		request.ExpectedActivation,
	}
	digest, err := digestProjection("treatment", projection)
	return "treatment-" + digest, err
}

func comparisonID(comparison Comparison) (string, error) {
	projection := struct {
		Reference ArmSelector   `json:"reference"`
		Candidate ArmSelector   `json:"candidate"`
		Stages    []FunnelStage `json:"stages"`
		Metrics   []MetricID    `json:"metrics"`
	}{comparison.Reference, comparison.Candidate, append([]FunnelStage{}, comparison.Stages...), append([]MetricID{}, comparison.Metrics...)}
	digest, err := digestProjection("comparison", projection)
	return "comparison-" + digest, err
}

func capabilityForCondition(condition Condition) (CapabilityID, bool) {
	values := map[Condition]CapabilityID{
		ConditionNone:              CapabilityConditionNone,
		ConditionCurrent:           CapabilityConditionCurrent,
		ConditionPrevious:          CapabilityConditionPrevious,
		ConditionForcedOracle:      CapabilityConditionForcedOracle,
		ConditionAutonomousOracle:  CapabilityConditionAutonomousOracle,
		ConditionOracleDistractors: CapabilityConditionOracleDistractors,
		ConditionRetrievedPresent:  CapabilityConditionRetrievedPresent,
		ConditionRetrievedAbsent:   CapabilityConditionRetrievedAbsent,
	}
	value, ok := values[condition]
	return value, ok
}

func capabilityForChannel(channel ActivationChannel) (CapabilityID, bool) {
	values := map[ActivationChannel]CapabilityID{
		ChannelImplicit:      CapabilityChannelImplicit,
		ChannelExplicitUser:  CapabilityChannelExplicitUser,
		ChannelDeveloper:     CapabilityChannelDeveloper,
		ChannelCombined:      CapabilityChannelCombined,
		ChannelAdapterNative: CapabilityChannelAdapterNative,
	}
	value, ok := values[channel]
	return value, ok
}

func capabilityForControl(control ControlClass) (CapabilityID, bool) {
	values := map[ControlClass]CapabilityID{
		ControlPositive:              CapabilityControlPositive,
		ControlNearMissNegative:      CapabilityControlNearMissNegative,
		ControlIrrelevant:            CapabilityControlIrrelevant,
		ControlUnsupportedDomain:     CapabilityControlUnsupportedDomain,
		ControlStaleVersionMismatch:  CapabilityControlStaleVersionMismatch,
		ControlAdversarialDistractor: CapabilityControlAdversarialDistractor,
	}
	value, ok := values[control]
	return value, ok
}

func capabilityForStage(stage FunnelStage) (CapabilityID, bool) {
	values := map[FunnelStage]CapabilityID{
		StageCandidateRecall:   CapabilityObserveCandidateRecall,
		StageSelection:         CapabilityObserveSelection,
		StageLoad:              CapabilityObserveLoad,
		StageInstructionAccess: CapabilityObserveInstructionAccess,
		StageReferenceAccess:   CapabilityObserveReferenceAccess,
		StageScriptAccess:      CapabilityObserveScriptAccess,
		StageUsefulAdherence:   CapabilityObserveUsefulAdherence,
		StageVerifierOutcome:   CapabilityObserveVerifierOutcome,
	}
	value, ok := values[stage]
	return value, ok
}

func expectedMetricCapability(metric MetricID) (CapabilityID, MetricKind, bool) {
	values := map[MetricID]struct {
		capability CapabilityID
		kind       MetricKind
	}{
		MetricOutcome:               {CapabilityObserveOutcome, MetricBinary},
		MetricInputTokens:           {CapabilityObserveInputTokens, MetricCount},
		MetricOutputTokens:          {CapabilityObserveOutputTokens, MetricCount},
		MetricEstimatedCostMicroUSD: {CapabilityObserveCost, MetricCount},
		MetricDurationMillis:        {CapabilityObserveDuration, MetricCount},
	}
	value, ok := values[metric]
	return value.capability, value.kind, ok
}

func supportValid(value Support) bool {
	return value == SupportUnknown || value == SupportSupported || value == SupportUnsupported || value == SupportNotApplicable
}

func presenceValid(value Presence) bool {
	return value == PresenceUnknown || value == PresenceObserved || value == PresenceUnsupported || value == PresenceNotApplicable
}

func selectorValid(selector ArmSelector) bool {
	_, condition := capabilityForCondition(selector.Condition)
	_, channel := capabilityForChannel(selector.ActivationChannel)
	selection := selector.SelectionAuthority == SelectionNone || selector.SelectionAuthority == SelectionHarness ||
		selector.SelectionAuthority == SelectionAgent || selector.SelectionAuthority == SelectionRetriever
	_, control := capabilityForControl(selector.Control)
	return condition && channel && selection && control
}

func autonomousRoutingEligible(selector ArmSelector) bool {
	selectedWithoutHarness := selector.SelectionAuthority == SelectionAgent || selector.SelectionAuthority == SelectionRetriever
	undirectedChannel := selector.ActivationChannel == ChannelImplicit || selector.ActivationChannel == ChannelAdapterNative
	return selectedWithoutHarness && undirectedChannel
}

func sortedUniqueStrings(values []string) bool {
	for index := range values {
		if index > 0 && values[index-1] >= values[index] {
			return false
		}
	}
	return true
}

func sortCapabilityIDs(values []CapabilityID) {
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
}
