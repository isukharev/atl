package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
)

const PrivateActivationStudyContractSchemaVersion = 1

// PrivateActivationCellIdentity identifies one same-surface treatment cell.
// The pair, rather than the surface alone, is the durable identity because all
// four activation-study cells intentionally use cli-skill.
type PrivateActivationCellIdentity struct {
	Surface         string `json:"surface"`
	SkillActivation string `json:"skill_activation"`
}

func (c PrivateActivationCellIdentity) Validate() error {
	if c.Surface != SurfaceCLISkill || privateActivationTreatmentIndex(c.SkillActivation) < 0 {
		return fmt.Errorf("invalid private activation cell identity")
	}
	return nil
}

// PrivateActivationTreatmentContract binds one exact run spec to its composite
// cell identity. Variant is retained as the collision-free runner output label;
// it is deliberately excluded from the common contract shared by all cells.
type PrivateActivationTreatmentContract struct {
	Cell          PrivateActivationCellIdentity `json:"cell"`
	Variant       string                        `json:"variant"`
	RunSpecSHA256 string                        `json:"run_spec_sha256"`
}

// PrivateActivationStudyContract proves that an exact four-cell set differs
// only in skill activation and its per-cell variant label. Treatments are
// always stored in canonical treatment order, independent of input ordering.
type PrivateActivationStudyContract struct {
	SchemaVersion        int                                  `json:"schema_version"`
	Provider             string                               `json:"provider"`
	Surface              string                               `json:"surface"`
	CommonContractSHA256 string                               `json:"common_contract_sha256"`
	Cells                []PrivateActivationTreatmentContract `json:"cells"`
}

func (c PrivateActivationStudyContract) Validate() error {
	if c.SchemaVersion != PrivateActivationStudyContractSchemaVersion || c.Provider != "codex" ||
		c.Surface != SurfaceCLISkill || !validSHA256(c.CommonContractSHA256) || len(c.Cells) != 4 {
		return fmt.Errorf("invalid private activation study contract")
	}
	seenVariants := make(map[string]struct{}, len(c.Cells))
	seenDigests := make(map[string]struct{}, len(c.Cells))
	for index, treatment := range c.Cells {
		if treatment.Cell.Surface != c.Surface || treatment.Cell.Validate() != nil ||
			privateActivationTreatmentIndex(treatment.Cell.SkillActivation) != index ||
			validatePathComponentID("run variant", treatment.Variant) != nil || !validSHA256(treatment.RunSpecSHA256) {
			return fmt.Errorf("invalid private activation study contract")
		}
		if _, exists := seenVariants[treatment.Variant]; exists {
			return fmt.Errorf("invalid private activation study contract")
		}
		seenVariants[treatment.Variant] = struct{}{}
		if _, exists := seenDigests[treatment.RunSpecSHA256]; exists {
			return fmt.Errorf("invalid private activation study contract")
		}
		seenDigests[treatment.RunSpecSHA256] = struct{}{}
	}
	return nil
}

// Treatment returns the canonical contract for one closed treatment name.
func (c PrivateActivationStudyContract) Treatment(skillActivation string) (PrivateActivationTreatmentContract, bool) {
	index := privateActivationTreatmentIndex(skillActivation)
	if index < 0 || index >= len(c.Cells) || c.Cells[index].Cell.SkillActivation != skillActivation {
		return PrivateActivationTreatmentContract{}, false
	}
	return c.Cells[index], true
}

// ValidatePrivateActivationStudy loads four contained run specifications and
// returns their privacy-safe common contract. All paths must belong to the same
// case directory so identical relative RunSpec references cannot resolve to
// different task material.
func ValidatePrivateActivationStudy(paths ...string) (PrivateActivationStudyContract, error) {
	if len(paths) != 4 {
		return PrivateActivationStudyContract{}, fmt.Errorf("private activation study requires exactly four run specs")
	}
	specs := make([]RunSpec, 0, len(paths))
	commonDirectory := ""
	for index, path := range paths {
		loaded, err := loadRunInputs(RunOptions{SpecPath: path})
		if err != nil {
			return PrivateActivationStudyContract{}, fmt.Errorf("private activation study run %d is invalid", index+1)
		}
		directory, err := filepath.Abs(loaded.specDir)
		if err != nil {
			return PrivateActivationStudyContract{}, fmt.Errorf("private activation study run %d is invalid", index+1)
		}
		if commonDirectory == "" {
			commonDirectory = directory
		} else if directory != commonDirectory {
			return PrivateActivationStudyContract{}, fmt.Errorf("private activation study runs must use one case directory")
		}
		if err := validateEvidenceOutcomeResponseSchema(loaded.responseSchema); err != nil {
			return PrivateActivationStudyContract{}, fmt.Errorf("private activation study run %d has no bounded evidence outcome: %w", index+1, err)
		}
		if loaded.spec.SchemaVersion != RunSpecSchemaVersion {
			return PrivateActivationStudyContract{}, fmt.Errorf("private activation study run %d uses a legacy run spec", index+1)
		}
		specs = append(specs, loaded.spec)
	}
	return BuildPrivateActivationStudyContract(specs)
}

// BuildPrivateActivationStudyContract validates and canonically binds one
// complete 2x2 activation study. It accepts exactly one v6 Codex private-live
// CLI-skill run for each treatment and rejects every non-treatment difference.
func BuildPrivateActivationStudyContract(specs []RunSpec) (PrivateActivationStudyContract, error) {
	if len(specs) != 4 {
		return PrivateActivationStudyContract{}, fmt.Errorf("private activation study requires exactly four run specs")
	}
	treatments := make([]PrivateActivationTreatmentContract, 4)
	seenTreatments := make(map[string]struct{}, 4)
	seenVariants := make(map[string]struct{}, 4)
	var commonJSON []byte
	for index, spec := range specs {
		if (spec.SchemaVersion != RunSpecSchemaVersion && spec.SchemaVersion != LegacyRunSpecSchemaVersion) || spec.Validate() != nil || spec.Provider != "codex" ||
			spec.EffectiveBackendMode() != BackendModePrivateLive || spec.EffectiveSurface() != SurfaceCLISkill ||
			spec.EffectiveToolTransport() != "cli" || spec.Repetitions != 1 {
			return PrivateActivationStudyContract{}, fmt.Errorf("private activation study run %d is invalid", index+1)
		}
		treatmentIndex := privateActivationTreatmentIndex(spec.SkillActivation)
		if treatmentIndex < 0 {
			return PrivateActivationStudyContract{}, fmt.Errorf("private activation study run %d has an invalid treatment", index+1)
		}
		if _, exists := seenTreatments[spec.SkillActivation]; exists {
			return PrivateActivationStudyContract{}, fmt.Errorf("private activation study treatments must be unique")
		}
		seenTreatments[spec.SkillActivation] = struct{}{}
		if _, exists := seenVariants[spec.Variant]; exists {
			return PrivateActivationStudyContract{}, fmt.Errorf("private activation study variants must be unique")
		}
		seenVariants[spec.Variant] = struct{}{}

		exactJSON, err := json.Marshal(spec)
		if err != nil {
			return PrivateActivationStudyContract{}, fmt.Errorf("private activation study run %d cannot be canonicalized", index+1)
		}
		normalized := spec
		normalized.SkillActivation = ""
		normalized.Variant = ""
		normalizedJSON, err := json.Marshal(normalized)
		if err != nil {
			return PrivateActivationStudyContract{}, fmt.Errorf("private activation study run %d cannot be canonicalized", index+1)
		}
		if commonJSON == nil {
			commonJSON = normalizedJSON
		} else if !bytes.Equal(commonJSON, normalizedJSON) {
			return PrivateActivationStudyContract{}, fmt.Errorf("private activation study runs differ outside treatment and variant")
		}
		treatments[treatmentIndex] = PrivateActivationTreatmentContract{
			Cell:    PrivateActivationCellIdentity{Surface: SurfaceCLISkill, SkillActivation: spec.SkillActivation},
			Variant: spec.Variant, RunSpecSHA256: sha256HexBytes(exactJSON),
		}
	}
	contract := PrivateActivationStudyContract{
		SchemaVersion: PrivateActivationStudyContractSchemaVersion,
		Provider:      "codex", Surface: SurfaceCLISkill, CommonContractSHA256: sha256HexBytes(commonJSON), Cells: treatments,
	}
	if err := contract.Validate(); err != nil {
		return PrivateActivationStudyContract{}, err
	}
	return contract, nil
}

// PrivateActivationStudyTreatments returns the closed canonical treatment
// vocabulary. The returned slice is independent and may be safely mutated.
func PrivateActivationStudyTreatments() []string {
	return []string{
		SkillActivationImplicit,
		SkillActivationExplicit,
		SkillActivationDeveloper,
		SkillActivationCombined,
	}
}

// CanonicalPrivateActivationStudyOrders returns a balanced four-block Latin
// square. Every treatment occupies every position once, and every directed
// treatment transition occurs exactly once across the four blocks.
func CanonicalPrivateActivationStudyOrders() [][]PrivateActivationCellIdentity {
	treatments := PrivateActivationStudyTreatments()
	indices := [4][4]int{
		{0, 1, 3, 2},
		{1, 2, 0, 3},
		{2, 3, 1, 0},
		{3, 0, 2, 1},
	}
	orders := make([][]PrivateActivationCellIdentity, len(indices))
	for row := range indices {
		orders[row] = make([]PrivateActivationCellIdentity, len(indices[row]))
		for column, treatmentIndex := range indices[row] {
			orders[row][column] = PrivateActivationCellIdentity{
				Surface: SurfaceCLISkill, SkillActivation: treatments[treatmentIndex],
			}
		}
	}
	return orders
}

// PrivateActivationStudyOrder selects the canonical order for a zero-based
// study attempt. The lifecycle decides which durable attempts advance it.
func PrivateActivationStudyOrder(attempt int) ([]PrivateActivationCellIdentity, error) {
	if attempt < 0 {
		return nil, fmt.Errorf("activation study attempt cannot be negative")
	}
	orders := CanonicalPrivateActivationStudyOrders()
	return append([]PrivateActivationCellIdentity(nil), orders[attempt%len(orders)]...), nil
}

// ActivationStudyOrder returns the treatment names for the zero-based block
// attempt. A negative attempt is invalid and returns nil.
func ActivationStudyOrder(attempt int) []string {
	order, err := PrivateActivationStudyOrder(attempt)
	if err != nil {
		return nil
	}
	treatments := make([]string, len(order))
	for index, cell := range order {
		treatments[index] = cell.SkillActivation
	}
	return treatments
}

func privateActivationTreatmentIndex(value string) int {
	for index, treatment := range PrivateActivationStudyTreatments() {
		if value == treatment {
			return index
		}
	}
	return -1
}
