// Package lineage owns the provider-free, content-addressed dataset lineage
// contract. It stores only opaque digests, bounded counts, and coverage; raw
// prompts, expected outputs, verifier bundles, treatment identities, paths,
// providers, credentials, and network configuration are deliberately absent.
package lineage

import "errors"

const (
	Schema          = "agent-eval/dataset-lineage"
	SchemaVersion   = 1
	ContractVersion = "0.1.0-pre-release"

	MaxLineageBytes = 1 << 20
	MaxRoles        = 16
	MaxHoldouts     = 64
	MaxDependencies = 64
	MaxMembers      = 1 << 20
	MaxCoverage     = 1 << 20
	MaxMaterialAxes = 16
	MaxJSONDepth    = 64
	SHA256HexLength = 64
)

var ErrInvalid = errors.New("dataset_lineage_invalid")

// ErrorCode identifies a stable, content-free validation class.
type ErrorCode string

const (
	ErrorInvalidLineage  ErrorCode = "invalid_dataset_lineage"
	ErrorInvalidRole     ErrorCode = "invalid_dataset_role"
	ErrorInvalidIdentity ErrorCode = "invalid_dataset_identity"
	ErrorInvalidHoldout  ErrorCode = "invalid_dataset_holdout"
	ErrorLimitExceeded   ErrorCode = "dataset_lineage_limit_exceeded"
)

type Error struct{ code ErrorCode }

func (e *Error) Error() string   { return string(e.code) }
func (e *Error) Unwrap() error   { return ErrInvalid }
func (e *Error) Code() ErrorCode { return e.code }

func fail(code ErrorCode) error { return &Error{code: code} }

// CodeOf returns the stable validation class carried by err.
func CodeOf(err error) (ErrorCode, bool) {
	var target *Error
	if !errors.As(err, &target) {
		return "", false
	}
	return target.Code(), true
}

// DatasetRole is a closed cohort-purpose vocabulary. The legacy role is
// explicit and read-only; it exists only to keep historical ID-derived
// holdouts readable without reclassifying them.
type DatasetRole string

const (
	RoleAuthoring              DatasetRole = "authoring"
	RoleTrain                  DatasetRole = "train"
	RoleValidation             DatasetRole = "validation"
	RoleGeneralization         DatasetRole = "generalization"
	RoleTrigger                DatasetRole = "trigger"
	RoleSecurity               DatasetRole = "security"
	RoleEvolutionCompatibility DatasetRole = "evolution_compatibility"
	RoleFinalPromotion         DatasetRole = "final_promotion"
	RoleLegacyIDDerived        DatasetRole = "legacy_id_derived"
)

var closedRoles = [...]DatasetRole{
	RoleAuthoring,
	RoleTrain,
	RoleValidation,
	RoleGeneralization,
	RoleTrigger,
	RoleSecurity,
	RoleEvolutionCompatibility,
	RoleFinalPromotion,
	RoleLegacyIDDerived,
}

// Roles returns the complete role vocabulary in canonical order.
func Roles() []DatasetRole {
	roles := make([]DatasetRole, len(closedRoles))
	copy(roles, closedRoles[:])
	return roles
}

// DifferenceAxis is a closed material-difference vocabulary. Every holdout
// binding records every axis, while ReviewedMaterialAxes names the reviewed
// subset that is allowed to differ.
type DifferenceAxis string

const (
	AxisDataset     DifferenceAxis = "dataset"
	AxisContract    DifferenceAxis = "contract"
	AxisSkill       DifferenceAxis = "skill"
	AxisEvaluation  DifferenceAxis = "evaluation"
	AxisGrader      DifferenceAxis = "grader"
	AxisAgent       DifferenceAxis = "agent"
	AxisModel       DifferenceAxis = "model"
	AxisHarness     DifferenceAxis = "harness"
	AxisEnvironment DifferenceAxis = "environment"
	AxisToolAPI     DifferenceAxis = "tool_api"
	AxisDependency  DifferenceAxis = "dependency"
)

var closedAxes = [...]DifferenceAxis{
	AxisDataset,
	AxisContract,
	AxisSkill,
	AxisEvaluation,
	AxisGrader,
	AxisAgent,
	AxisModel,
	AxisHarness,
	AxisEnvironment,
	AxisToolAPI,
	AxisDependency,
}

// DifferenceAxes returns the complete axis vocabulary in canonical order.
func DifferenceAxes() []DifferenceAxis {
	axes := make([]DifferenceAxis, len(closedAxes))
	copy(axes, closedAxes[:])
	return axes
}

// Coverage contains aggregate counts only. It cannot carry case names,
// content, paths, or evidence.
type Coverage struct {
	Total   uint64 `json:"total"`
	Covered uint64 `json:"covered"`
}

// RoleDescriptor content-addresses one immutable role without retaining its
// members. LegacyIDSHA256 is a digest of the historical ID-derived selector,
// never the selector itself.
type RoleDescriptor struct {
	Role           DatasetRole `json:"role"`
	ContentSHA256  string      `json:"content_sha256"`
	Coverage       Coverage    `json:"coverage"`
	LegacyIDSHA256 string      `json:"legacy_id_sha256"`
	LegacyReadOnly bool        `json:"legacy_read_only"`
	RoleSHA256     string      `json:"role_sha256"`
}

// RuntimeIdentity is the complete opaque identity set required for a cohort.
// DependencySHA256 is sorted and contains one digest per dependency; no
// dependency name, version, path, URL, or credential is represented.
type RuntimeIdentity struct {
	SkillSHA256       string   `json:"skill_sha256"`
	EvalSHA256        string   `json:"eval_sha256"`
	GraderSHA256      string   `json:"grader_sha256"`
	AgentSHA256       string   `json:"agent_sha256"`
	ModelSHA256       string   `json:"model_sha256"`
	HarnessSHA256     string   `json:"harness_sha256"`
	EnvironmentSHA256 string   `json:"environment_sha256"`
	ToolAPISHA256     string   `json:"tool_api_sha256"`
	DependencySHA256  []string `json:"dependency_sha256"`
	IdentitySHA256    string   `json:"identity_sha256"`
}

// AxisDifference binds one closed axis to opaque primary and holdout values.
// DifferenceSHA256 prevents an axis record from being moved or rewritten
// without invalidating its enclosing binding.
type AxisDifference struct {
	Axis             DifferenceAxis `json:"axis"`
	PrimarySHA256    string         `json:"primary_sha256"`
	HoldoutSHA256    string         `json:"holdout_sha256"`
	DifferenceSHA256 string         `json:"difference_sha256"`
}

// HoldoutBinding binds one holdout role to the primary role's reviewed
// contract and complete runtime identity. It contains no sealed material.
type HoldoutBinding struct {
	HoldoutRole           DatasetRole      `json:"holdout_role"`
	HoldoutRoleSHA256     string           `json:"holdout_role_sha256"`
	HoldoutContractSHA256 string           `json:"holdout_contract_sha256"`
	HoldoutIdentity       RuntimeIdentity  `json:"holdout_identity"`
	Differences           []AxisDifference `json:"differences"`
	ReviewedMaterialAxes  []DifferenceAxis `json:"reviewed_material_axes"`
	BindingSHA256         string           `json:"binding_sha256"`
}

// Lineage is an immutable, versioned dataset lineage record. Roles are closed
// labels whose non-primary members each have exactly one HoldoutBinding;
// PrimaryRole is the sole unbound role. LegacyIDDerived is therefore readable
// only as a historical holdout, never as the primary. Accompanying content
// digests are immutable references; there is intentionally no mutable alias or
// path.
type Lineage struct {
	Schema                string           `json:"schema"`
	SchemaVersion         int              `json:"schema_version"`
	ContractVersion       string           `json:"contract_version"`
	Roles                 []RoleDescriptor `json:"roles"`
	PrimaryRole           DatasetRole      `json:"primary_role"`
	PrimaryRoleSHA256     string           `json:"primary_role_sha256"`
	PrimaryContractSHA256 string           `json:"primary_contract_sha256"`
	PrimaryIdentity       RuntimeIdentity  `json:"primary_identity"`
	Holdouts              []HoldoutBinding `json:"holdouts"`
	LineageSHA256         string           `json:"lineage_sha256"`
}
