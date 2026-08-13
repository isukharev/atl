package experiment

import (
	"encoding/binary"
	"sort"
)

func Compile(design Design, capability CapabilityContract, analysis AnalysisPlan) (Manifest, error) {
	if err := ValidateDesign(design); err != nil {
		return Manifest{}, err
	}
	if err := ValidateCapabilityContract(capability); err != nil {
		return Manifest{}, err
	}
	if err := ValidateAnalysisPlan(analysis); err != nil {
		return Manifest{}, err
	}
	if err := validateCompilationAdmission(design, capability, analysis); err != nil {
		return Manifest{}, err
	}
	manifest, err := deriveManifest(design, capability, analysis)
	if err != nil {
		return Manifest{}, err
	}
	digest, err := digestManifest(manifest)
	if err != nil {
		return Manifest{}, contractError(ErrorInvalidManifest, err)
	}
	manifest.ManifestSHA256 = digest
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// validateCompilationAdmission owns the cross-artifact constraints that must
// hold before deriving a potentially large manifest. Decode and validation
// replay the same admission so canonical bytes cannot bypass Compile.
func validateCompilationAdmission(design Design, capability CapabilityContract, analysis AnalysisPlan) error {
	blocks := totalDesignBlocks(design)
	if design.CapabilityContractSHA256 != capability.CapabilityContractSHA256 ||
		design.AnalysisPlanSHA256 != analysis.AnalysisPlanSHA256 ||
		(design.CompatibilityProfile == CompatibilityNone && blocks < analysis.MinimumInferenceBlocks) ||
		analysis.RepeatedAttempts.K[len(analysis.RepeatedAttempts.K)-1] > blocks {
		return contractError(ErrorUnsupportedDesign, errInvalidValue)
	}
	if uint64(blocks)*uint64(len(analysis.Comparisons)) > MaxPairBindings {
		return contractError(ErrorLimitExceeded, errInvalidValue)
	}
	return nil
}

func deriveManifest(design Design, capability CapabilityContract, analysis AnalysisPlan) (Manifest, error) {
	required, err := requiredCapabilities(design, analysis)
	if err != nil {
		return Manifest{}, err
	}
	if err := requireSupportedCapabilities(capability, required); err != nil {
		return Manifest{}, err
	}
	treatments := make([]Treatment, len(design.Treatments))
	for index, request := range design.Treatments {
		identifier, err := treatmentID(request)
		if err != nil {
			return Manifest{}, contractError(ErrorInvalidManifest, err)
		}
		treatments[index] = Treatment{
			ID:                        identifier,
			Arm:                       request.Arm,
			Role:                      request.Role,
			SkillSHA256:               request.SkillSHA256,
			SkillVersionSHA256:        request.SkillVersionSHA256,
			DistractorSHA256:          append([]string{}, request.DistractorSHA256...),
			RetrieverSHA256:           request.RetrieverSHA256,
			ControlSHA256:             request.ControlSHA256,
			ControlProvenance:         request.ControlProvenance,
			ExecutionBindingSHA256:    request.ExecutionBindingSHA256,
			ExpectedActivation:        request.ExpectedActivation,
			AutonomousRoutingEligible: autonomousRoutingEligible(request.Arm),
		}
	}
	sort.Slice(treatments, func(left, right int) bool { return treatments[left].ID < treatments[right].ID })
	ordered := append([]Treatment{}, treatments...)
	sort.Slice(ordered, func(left, right int) bool {
		leftScore := hashParts("ordering", []byte(design.Ordering.SeedSHA256), []byte(ordered[left].ID))
		rightScore := hashParts("ordering", []byte(design.Ordering.SeedSHA256), []byte(ordered[right].ID))
		if leftScore == rightScore {
			return ordered[left].ID < ordered[right].ID
		}
		return leftScore < rightScore
	})
	sequences := treatmentSequences(ordered)
	if len(sequences) == 0 {
		return Manifest{}, contractError(ErrorInvalidManifest, errInvalidValue)
	}
	blocks := make([]Block, 0, totalDesignBlocks(design))
	pairs := make([]PairBinding, 0, int(totalDesignBlocks(design))*len(analysis.Comparisons))
	ordinal := uint32(0)
	for _, stratum := range design.Strata {
		stratumID := "stratum-" + hashParts("stratum", []byte(design.Case.CaseSHA256), []byte(stratum.BindingSHA256))
		for local := uint32(0); local < stratum.Blocks; local++ {
			ordinal++
			blockID := "block-" + hashParts(
				"block",
				[]byte(design.Case.CaseSHA256),
				[]byte(stratum.BindingSHA256),
				uint32Bytes(local+1),
			)
			sequence := sequences[int(local)%len(sequences)]
			if design.Ordering.Kind == OrderingLegacyFixed {
				sequence, err = legacyTreatmentSequence(design, treatments, int(ordinal-1))
				if err != nil {
					return Manifest{}, err
				}
			}
			assignments := make([]Assignment, len(sequence))
			for position, treatment := range sequence {
				assignments[position] = Assignment{
					TrialID:     "trial-" + hashParts("trial", []byte(blockID), []byte(treatment.ID)),
					TreatmentID: treatment.ID,
					Position:    uint32(position + 1),
				}
			}
			block := Block{ID: blockID, Ordinal: ordinal, StratumID: stratumID, Assignments: assignments}
			blocks = append(blocks, block)
			blockPairs, err := pairBindings(block, treatments, analysis.Comparisons)
			if err != nil {
				return Manifest{}, err
			}
			pairs = append(pairs, blockPairs...)
		}
	}
	return Manifest{
		Schema:                  ManifestSchema,
		SchemaVersion:           SchemaVersion,
		ContractVersion:         ContractVersion,
		Design:                  cloneDesign(design),
		CapabilityContract:      cloneCapabilityContract(capability),
		AnalysisPlan:            cloneAnalysisPlan(analysis),
		RequiredCapabilities:    required,
		Treatments:              treatments,
		Blocks:                  blocks,
		Pairs:                   pairs,
		PositionBalanceComplete: positionBalanced(blocks, len(treatments)),
	}, nil
}

func requiredCapabilities(design Design, analysis AnalysisPlan) ([]CapabilityID, error) {
	seen := map[CapabilityID]bool{}
	for _, treatment := range design.Treatments {
		condition, ok := capabilityForCondition(treatment.Arm.Condition)
		if !ok {
			return nil, contractError(ErrorInvalidDesign, errInvalidValue)
		}
		channel, ok := capabilityForChannel(treatment.Arm.ActivationChannel)
		if !ok {
			return nil, contractError(ErrorInvalidDesign, errInvalidValue)
		}
		seen[condition] = true
		seen[channel] = true
		control, ok := capabilityForControl(treatment.Arm.Control)
		if !ok {
			return nil, contractError(ErrorInvalidDesign, errInvalidValue)
		}
		seen[control] = true
	}
	for _, stage := range closedStages {
		capability, _ := capabilityForStage(stage)
		seen[capability] = true
	}
	for _, metric := range analysis.Metrics {
		seen[metric.Capability] = true
	}
	result := make([]CapabilityID, 0, len(seen))
	for capability := range seen {
		result = append(result, capability)
	}
	sortCapabilityIDs(result)
	return result, nil
}

func requireSupportedCapabilities(contract CapabilityContract, required []CapabilityID) error {
	claims := make(map[CapabilityID]Support, len(contract.Capabilities))
	for _, capability := range contract.Capabilities {
		claims[capability.ID] = capability.Support
	}
	for _, capability := range required {
		if claims[capability] != SupportSupported {
			return contractError(ErrorUnsupportedDesign, errInvalidValue)
		}
	}
	return nil
}

func treatmentSequences(treatments []Treatment) [][]Treatment {
	if len(treatments) == 0 {
		return nil
	}
	count := len(treatments)
	base := make([]int, 0, count)
	base = append(base, 0)
	for lower, upper := 1, count-1; len(base) < count; lower, upper = lower+1, upper-1 {
		base = append(base, lower)
		if len(base) < count {
			base = append(base, upper)
		}
	}
	sequences := make([][]Treatment, 0, count*2)
	for rotation := 0; rotation < count; rotation++ {
		row := make([]Treatment, count)
		for position, index := range base {
			row[position] = treatments[(index+rotation)%count]
		}
		sequences = append(sequences, row)
	}
	if count%2 == 1 {
		for rotation := 0; rotation < count; rotation++ {
			row := make([]Treatment, count)
			for position, index := range base {
				row[count-position-1] = treatments[(index+rotation)%count]
			}
			sequences = append(sequences, row)
		}
	}
	return sequences
}

func legacyTreatmentSequence(design Design, treatments []Treatment, blockIndex int) ([]Treatment, error) {
	width := len(treatments)
	offset := blockIndex * width
	if offset < 0 || offset+width > len(design.Ordering.LegacySequence) {
		return nil, contractError(ErrorInvalidManifest, errInvalidValue)
	}
	sequence := make([]Treatment, width)
	for index, arm := range design.Ordering.LegacySequence[offset : offset+width] {
		treatment, count := findTreatment(treatments, arm)
		if count != 1 {
			return nil, contractError(ErrorInvalidManifest, errInvalidValue)
		}
		sequence[index] = treatment
	}
	return sequence, nil
}

func positionBalanced(blocks []Block, treatmentCount int) bool {
	if treatmentCount == 0 || len(blocks)%treatmentCount != 0 {
		return false
	}
	counts := make(map[string][]int, treatmentCount)
	for _, block := range blocks {
		if len(block.Assignments) != treatmentCount {
			return false
		}
		for index, assignment := range block.Assignments {
			if counts[assignment.TreatmentID] == nil {
				counts[assignment.TreatmentID] = make([]int, treatmentCount)
			}
			counts[assignment.TreatmentID][index]++
		}
	}
	if len(counts) != treatmentCount {
		return false
	}
	for _, positions := range counts {
		for index := 1; index < len(positions); index++ {
			if positions[index] != positions[0] {
				return false
			}
		}
	}
	return true
}

func pairBindings(block Block, treatments []Treatment, comparisons []Comparison) ([]PairBinding, error) {
	result := make([]PairBinding, 0, len(comparisons))
	for _, comparison := range comparisons {
		reference, referenceCount := findTreatment(treatments, comparison.Reference)
		candidate, candidateCount := findTreatment(treatments, comparison.Candidate)
		if referenceCount != 1 || candidateCount != 1 || reference.ID == candidate.ID || reference.Role != RoleReference ||
			(candidate.Role != RoleCandidate && candidate.Role != RoleControl) {
			return nil, contractError(ErrorUnsupportedDesign, errInvalidValue)
		}
		left, right := reference.ID, candidate.ID
		if left > right {
			left, right = right, left
		}
		result = append(result, PairBinding{
			ID: "pair-" + hashParts(
				"pair",
				[]byte(block.ID),
				[]byte(comparison.ID),
				[]byte(left),
				[]byte(right),
			),
			BlockID:              block.ID,
			ComparisonID:         comparison.ID,
			ReferenceTreatmentID: reference.ID,
			CandidateTreatmentID: candidate.ID,
		})
	}
	return result, nil
}

func findTreatment(treatments []Treatment, selector ArmSelector) (Treatment, int) {
	var result Treatment
	count := 0
	for _, treatment := range treatments {
		if armKey(treatment.Arm) == armKey(selector) {
			result = treatment
			count++
		}
	}
	return result, count
}

func totalDesignBlocks(design Design) uint32 {
	var total uint32
	for _, stratum := range design.Strata {
		total += stratum.Blocks
	}
	return total
}

func uint32Bytes(value uint32) []byte {
	var data [4]byte
	binary.BigEndian.PutUint32(data[:], value)
	return data[:]
}

func TrialExperimentSHA256(manifest Manifest, trialID string) (string, error) {
	if !validDerivedID("trial", trialID) {
		return "", contractError(ErrorInvalidTrial, errInvalidValue)
	}
	digests, err := TrialExperimentSHA256s(manifest)
	if err != nil {
		return "", contractError(ErrorInvalidTrial, err)
	}
	digest, ok := digests[trialID]
	if !ok {
		return "", contractError(ErrorInvalidTrial, errInvalidValue)
	}
	return digest, nil
}

// TrialExperimentSHA256s validates one manifest once and returns the distinct
// lifecycle experiment identity for every trial in its immutable roster.
func TrialExperimentSHA256s(manifest Manifest) (map[string]string, error) {
	if err := ValidateManifest(manifest); err != nil {
		return nil, contractError(ErrorInvalidTrial, err)
	}
	result := make(map[string]string, len(manifest.Blocks)*len(manifest.Treatments))
	for _, block := range manifest.Blocks {
		for _, assignment := range block.Assignments {
			result[assignment.TrialID] = hashParts("trial-experiment", []byte(manifest.ManifestSHA256), []byte(assignment.TrialID))
		}
	}
	return result, nil
}
