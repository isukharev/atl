package lineage

import "sort"

type roleProjection struct {
	Role           DatasetRole `json:"role"`
	ContentSHA256  string      `json:"content_sha256"`
	Coverage       Coverage    `json:"coverage"`
	LegacyIDSHA256 string      `json:"legacy_id_sha256"`
	LegacyReadOnly bool        `json:"legacy_read_only"`
}

type differenceProjection struct {
	Axis          DifferenceAxis `json:"axis"`
	PrimarySHA256 string         `json:"primary_sha256"`
	HoldoutSHA256 string         `json:"holdout_sha256"`
}

type bindingProjection struct {
	HoldoutRole           DatasetRole      `json:"holdout_role"`
	HoldoutRoleSHA256     string           `json:"holdout_role_sha256"`
	HoldoutContractSHA256 string           `json:"holdout_contract_sha256"`
	HoldoutIdentity       RuntimeIdentity  `json:"holdout_identity"`
	Differences           []AxisDifference `json:"differences"`
	ReviewedMaterialAxes  []DifferenceAxis `json:"reviewed_material_axes"`
}

type lineageProjection struct {
	Schema                string           `json:"schema"`
	SchemaVersion         int              `json:"schema_version"`
	ContractVersion       string           `json:"contract_version"`
	Roles                 []RoleDescriptor `json:"roles"`
	PrimaryRole           DatasetRole      `json:"primary_role"`
	PrimaryRoleSHA256     string           `json:"primary_role_sha256"`
	PrimaryContractSHA256 string           `json:"primary_contract_sha256"`
	PrimaryIdentity       RuntimeIdentity  `json:"primary_identity"`
	Holdouts              []HoldoutBinding `json:"holdouts"`
}

// Seal creates an immutable, canonical lineage record. It clones all input
// slices, fills omitted derived digests and complete axis projections, and
// refuses any supplied stale digest rather than silently moving a reference.
func Seal(input Lineage) (Lineage, error) {
	if len(input.Roles) > MaxRoles || len(input.Holdouts) > MaxHoldouts ||
		len(input.PrimaryIdentity.DependencySHA256) > MaxDependencies {
		return Lineage{}, fail(ErrorLimitExceeded)
	}
	for _, holdout := range input.Holdouts {
		if len(holdout.Differences) > len(closedAxes) ||
			len(holdout.ReviewedMaterialAxes) > MaxMaterialAxes ||
			len(holdout.HoldoutIdentity.DependencySHA256) > MaxDependencies {
			return Lineage{}, fail(ErrorLimitExceeded)
		}
	}
	lineage := cloneLineage(input)
	if lineage.Schema != "" && lineage.Schema != Schema ||
		lineage.SchemaVersion != 0 && lineage.SchemaVersion != SchemaVersion ||
		lineage.ContractVersion != "" && lineage.ContractVersion != ContractVersion {
		return Lineage{}, fail(ErrorInvalidLineage)
	}
	lineage.Schema = Schema
	lineage.SchemaVersion = SchemaVersion
	lineage.ContractVersion = ContractVersion
	if lineage.Roles == nil {
		lineage.Roles = []RoleDescriptor{}
	}
	if lineage.Holdouts == nil {
		lineage.Holdouts = []HoldoutBinding{}
	}
	if lineage.PrimaryIdentity.DependencySHA256 == nil {
		lineage.PrimaryIdentity.DependencySHA256 = []string{}
	}
	var err error
	lineage.PrimaryIdentity, err = sealIdentity(lineage.PrimaryIdentity)
	if err != nil {
		return Lineage{}, err
	}
	sort.SliceStable(lineage.Roles, func(left, right int) bool {
		return roleOrdinal(lineage.Roles[left].Role) < roleOrdinal(lineage.Roles[right].Role)
	})
	for index := range lineage.Roles {
		role := &lineage.Roles[index]
		providedDigest := role.RoleSHA256
		shape := *role
		shape.RoleSHA256 = ""
		if err := validateRoleShape(shape, false); err != nil {
			return Lineage{}, err
		}
		digest, digestErr := roleDigest(*role)
		if digestErr != nil {
			return Lineage{}, fail(ErrorInvalidRole)
		}
		if providedDigest != "" && providedDigest != digest {
			return Lineage{}, fail(ErrorInvalidRole)
		}
		role.RoleSHA256 = digest
	}
	primaryRole, ok := findRole(lineage.Roles, lineage.PrimaryRole)
	if !ok || lineage.PrimaryRole == "" {
		return Lineage{}, fail(ErrorInvalidLineage)
	}
	if lineage.PrimaryRoleSHA256 != "" && lineage.PrimaryRoleSHA256 != primaryRole.RoleSHA256 {
		return Lineage{}, fail(ErrorInvalidLineage)
	}
	lineage.PrimaryRoleSHA256 = primaryRole.RoleSHA256
	if !validDigest(lineage.PrimaryContractSHA256) {
		return Lineage{}, fail(ErrorInvalidLineage)
	}
	for index := range lineage.Holdouts {
		holdout := &lineage.Holdouts[index]
		if holdout.HoldoutRole == lineage.PrimaryRole {
			return Lineage{}, fail(ErrorInvalidHoldout)
		}
		role, found := findRole(lineage.Roles, holdout.HoldoutRole)
		if !found {
			return Lineage{}, fail(ErrorInvalidHoldout)
		}
		if holdout.HoldoutRoleSHA256 != "" && holdout.HoldoutRoleSHA256 != role.RoleSHA256 {
			return Lineage{}, fail(ErrorInvalidHoldout)
		}
		holdout.HoldoutRoleSHA256 = role.RoleSHA256
		if holdout.HoldoutIdentity.DependencySHA256 == nil {
			holdout.HoldoutIdentity.DependencySHA256 = []string{}
		}
		holdout.HoldoutIdentity, err = sealIdentity(holdout.HoldoutIdentity)
		if err != nil {
			return Lineage{}, err
		}
		if !validDigest(holdout.HoldoutContractSHA256) {
			return Lineage{}, fail(ErrorInvalidHoldout)
		}
		if holdout.Differences == nil {
			holdout.Differences = deriveDifferences(lineage, *holdout, *primaryRole, *role)
		}
		if err := sealDifferences(lineage, holdout, *primaryRole, *role); err != nil {
			return Lineage{}, err
		}
		bindingDigest, digestErr := holdoutDigest(*holdout)
		if digestErr != nil {
			return Lineage{}, fail(ErrorInvalidHoldout)
		}
		if holdout.BindingSHA256 != "" && holdout.BindingSHA256 != bindingDigest {
			return Lineage{}, fail(ErrorInvalidHoldout)
		}
		holdout.BindingSHA256 = bindingDigest
	}
	sort.SliceStable(lineage.Holdouts, func(left, right int) bool {
		leftOrdinal, rightOrdinal := roleOrdinal(lineage.Holdouts[left].HoldoutRole), roleOrdinal(lineage.Holdouts[right].HoldoutRole)
		if leftOrdinal != rightOrdinal {
			return leftOrdinal < rightOrdinal
		}
		return lineage.Holdouts[left].HoldoutRoleSHA256 < lineage.Holdouts[right].HoldoutRoleSHA256
	})
	lineageDigest, digestErr := digestLineage(lineage)
	if digestErr != nil {
		return Lineage{}, fail(ErrorInvalidLineage)
	}
	if lineage.LineageSHA256 != "" && lineage.LineageSHA256 != lineageDigest {
		return Lineage{}, fail(ErrorInvalidLineage)
	}
	lineage.LineageSHA256 = lineageDigest
	if err := Validate(lineage); err != nil {
		return Lineage{}, err
	}
	return lineage, nil
}

// Validate checks one already-sealed immutable record without mutating it.
func Validate(lineage Lineage) error {
	if err := validateLineageShape(lineage, true); err != nil {
		return err
	}
	digest, err := digestLineage(lineage)
	if err != nil || digest != lineage.LineageSHA256 {
		return fail(ErrorInvalidLineage)
	}
	return nil
}

func validateLineageShape(lineage Lineage, requireDigest bool) error {
	if lineage.Schema != Schema || lineage.SchemaVersion != SchemaVersion || lineage.ContractVersion != ContractVersion ||
		lineage.Roles == nil || len(lineage.Roles) == 0 || len(lineage.Roles) > MaxRoles ||
		lineage.Holdouts == nil || len(lineage.Holdouts) > MaxHoldouts || !validDigest(lineage.PrimaryContractSHA256) ||
		(requireDigest && !validDigest(lineage.LineageSHA256)) || (!requireDigest && lineage.LineageSHA256 != "") {
		return fail(ErrorInvalidLineage)
	}
	if err := validateIdentity(lineage.PrimaryIdentity); err != nil {
		return err
	}
	roleByName := make(map[DatasetRole]RoleDescriptor, len(lineage.Roles))
	roleByDigest := make(map[string]bool, len(lineage.Roles))
	previousRole := DatasetRole("")
	for index, role := range lineage.Roles {
		if err := validateRole(role); err != nil ||
			(index > 0 && roleOrdinal(previousRole) >= roleOrdinal(role.Role)) ||
			roleByName[role.Role].Role != "" || roleByDigest[role.RoleSHA256] {
			return fail(ErrorInvalidRole)
		}
		roleByName[role.Role] = role
		roleByDigest[role.RoleSHA256] = true
		previousRole = role.Role
	}
	primary, ok := roleByName[lineage.PrimaryRole]
	if !ok || lineage.PrimaryRole == RoleLegacyIDDerived || lineage.PrimaryRoleSHA256 != primary.RoleSHA256 {
		return fail(ErrorInvalidLineage)
	}
	holdoutRoles := make(map[DatasetRole]bool, len(lineage.Holdouts))
	previousHoldout := DatasetRole("")
	for index, holdout := range lineage.Holdouts {
		if err := validateHoldoutShape(lineage, holdout, primary, roleByName); err != nil ||
			holdout.HoldoutRole == lineage.PrimaryRole || holdoutRoles[holdout.HoldoutRole] ||
			(index > 0 && roleOrdinal(previousHoldout) >= roleOrdinal(holdout.HoldoutRole)) {
			return fail(ErrorInvalidHoldout)
		}
		holdoutRoles[holdout.HoldoutRole] = true
		previousHoldout = holdout.HoldoutRole
	}
	if len(holdoutRoles) != len(lineage.Roles)-1 {
		return fail(ErrorInvalidLineage)
	}
	for role := range roleByName {
		if role != lineage.PrimaryRole && !holdoutRoles[role] {
			return fail(ErrorInvalidLineage)
		}
	}
	return nil
}

func validateRole(role RoleDescriptor) error {
	if err := validateRoleShape(role, true); err != nil {
		return err
	}
	digest, err := roleDigest(role)
	if err != nil || digest != role.RoleSHA256 {
		return fail(ErrorInvalidRole)
	}
	return nil
}

func validateRoleShape(role RoleDescriptor, requireDigest bool) error {
	if roleOrdinal(role.Role) < 0 || !validDigest(role.ContentSHA256) || role.Coverage.Total == 0 ||
		role.Coverage.Total > MaxMembers || role.Coverage.Covered > role.Coverage.Total ||
		role.Coverage.Covered > MaxCoverage || (requireDigest && !validDigest(role.RoleSHA256)) ||
		(!requireDigest && role.RoleSHA256 != "") {
		return fail(ErrorInvalidRole)
	}
	if role.Role == RoleLegacyIDDerived {
		if !role.LegacyReadOnly || !validDigest(role.LegacyIDSHA256) {
			return fail(ErrorInvalidRole)
		}
	} else if role.LegacyReadOnly || role.LegacyIDSHA256 != "" {
		return fail(ErrorInvalidRole)
	}
	return nil
}

func validateHoldoutShape(lineage Lineage, holdout HoldoutBinding, primary RoleDescriptor,
	roles map[DatasetRole]RoleDescriptor) error {
	role, ok := roles[holdout.HoldoutRole]
	if !ok || holdout.HoldoutRoleSHA256 != role.RoleSHA256 || holdout.HoldoutRole == lineage.PrimaryRole ||
		!validDigest(holdout.HoldoutContractSHA256) || holdout.Differences == nil || len(holdout.Differences) != len(closedAxes) ||
		holdout.ReviewedMaterialAxes == nil || len(holdout.ReviewedMaterialAxes) == 0 || len(holdout.ReviewedMaterialAxes) > MaxMaterialAxes {
		return fail(ErrorInvalidHoldout)
	}
	if err := validateIdentity(holdout.HoldoutIdentity); err != nil {
		return fail(ErrorInvalidHoldout)
	}
	for index, difference := range holdout.Differences {
		if difference.Axis != closedAxes[index] || !validDigest(difference.PrimarySHA256) || !validDigest(difference.HoldoutSHA256) ||
			!validDigest(difference.DifferenceSHA256) {
			return fail(ErrorInvalidHoldout)
		}
		primaryValue, holdoutValue := axisValues(lineage, primary, role, holdout)
		if difference.PrimarySHA256 != primaryValue[index] || difference.HoldoutSHA256 != holdoutValue[index] {
			return fail(ErrorInvalidHoldout)
		}
		digest, err := differenceDigest(difference)
		if err != nil || digest != difference.DifferenceSHA256 {
			return fail(ErrorInvalidHoldout)
		}
	}
	previousAxis := DifferenceAxis("")
	material := make(map[DifferenceAxis]bool, len(holdout.ReviewedMaterialAxes))
	for index, axis := range holdout.ReviewedMaterialAxes {
		if axisOrdinal(axis) < 0 || material[axis] || index > 0 && axisOrdinal(previousAxis) >= axisOrdinal(axis) {
			return fail(ErrorInvalidHoldout)
		}
		material[axis] = true
		previousAxis = axis
	}
	for _, difference := range holdout.Differences {
		changed := difference.PrimarySHA256 != difference.HoldoutSHA256
		if material[difference.Axis] != changed {
			return fail(ErrorInvalidHoldout)
		}
	}
	bindingDigest, err := holdoutDigest(holdout)
	if err != nil || !validDigest(holdout.BindingSHA256) || bindingDigest != holdout.BindingSHA256 {
		return fail(ErrorInvalidHoldout)
	}
	return nil
}

func sealDifferences(lineage Lineage, holdout *HoldoutBinding, primaryRole, holdoutRole RoleDescriptor) error {
	if len(holdout.Differences) != len(closedAxes) {
		return fail(ErrorInvalidHoldout)
	}
	if len(holdout.Differences) != 0 {
		sort.SliceStable(holdout.Differences, func(left, right int) bool {
			return axisOrdinal(holdout.Differences[left].Axis) < axisOrdinal(holdout.Differences[right].Axis)
		})
	}
	primaryValues, holdoutValues := axisValues(lineage, primaryRole, holdoutRole, *holdout)
	if len(holdout.Differences) == 0 {
		return fail(ErrorInvalidHoldout)
	}
	for index := range holdout.Differences {
		difference := &holdout.Differences[index]
		if difference.Axis != closedAxes[index] || difference.PrimarySHA256 != "" && difference.PrimarySHA256 != primaryValues[index] ||
			difference.HoldoutSHA256 != "" && difference.HoldoutSHA256 != holdoutValues[index] {
			return fail(ErrorInvalidHoldout)
		}
		difference.PrimarySHA256 = primaryValues[index]
		difference.HoldoutSHA256 = holdoutValues[index]
		digest, err := differenceDigest(*difference)
		if err != nil {
			return fail(ErrorInvalidHoldout)
		}
		if difference.DifferenceSHA256 != "" && difference.DifferenceSHA256 != digest {
			return fail(ErrorInvalidHoldout)
		}
		difference.DifferenceSHA256 = digest
	}
	sort.SliceStable(holdout.ReviewedMaterialAxes, func(left, right int) bool {
		return axisOrdinal(holdout.ReviewedMaterialAxes[left]) < axisOrdinal(holdout.ReviewedMaterialAxes[right])
	})
	return nil
}

func deriveDifferences(lineage Lineage, holdout HoldoutBinding, primaryRole, holdoutRole RoleDescriptor) []AxisDifference {
	primaryValues, holdoutValues := axisValues(lineage, primaryRole, holdoutRole, holdout)
	differences := make([]AxisDifference, len(closedAxes))
	for index, axis := range closedAxes {
		differences[index] = AxisDifference{Axis: axis, PrimarySHA256: primaryValues[index], HoldoutSHA256: holdoutValues[index]}
	}
	return differences
}

func axisValues(lineage Lineage, primaryRole, holdoutRole RoleDescriptor, holdout HoldoutBinding) ([]string, []string) {
	primary := make([]string, len(closedAxes))
	holdoutValues := make([]string, len(closedAxes))
	for index, axis := range closedAxes {
		switch axis {
		case AxisDataset:
			primary[index], holdoutValues[index] = primaryRole.ContentSHA256, holdoutRole.ContentSHA256
		case AxisContract:
			primary[index], holdoutValues[index] = lineage.PrimaryContractSHA256, holdout.HoldoutContractSHA256
		case AxisSkill:
			primary[index], holdoutValues[index] = lineage.PrimaryIdentity.SkillSHA256, holdout.HoldoutIdentity.SkillSHA256
		case AxisEvaluation:
			primary[index], holdoutValues[index] = lineage.PrimaryIdentity.EvalSHA256, holdout.HoldoutIdentity.EvalSHA256
		case AxisGrader:
			primary[index], holdoutValues[index] = lineage.PrimaryIdentity.GraderSHA256, holdout.HoldoutIdentity.GraderSHA256
		case AxisAgent:
			primary[index], holdoutValues[index] = lineage.PrimaryIdentity.AgentSHA256, holdout.HoldoutIdentity.AgentSHA256
		case AxisModel:
			primary[index], holdoutValues[index] = lineage.PrimaryIdentity.ModelSHA256, holdout.HoldoutIdentity.ModelSHA256
		case AxisHarness:
			primary[index], holdoutValues[index] = lineage.PrimaryIdentity.HarnessSHA256, holdout.HoldoutIdentity.HarnessSHA256
		case AxisEnvironment:
			primary[index], holdoutValues[index] = lineage.PrimaryIdentity.EnvironmentSHA256, holdout.HoldoutIdentity.EnvironmentSHA256
		case AxisToolAPI:
			primary[index], holdoutValues[index] = lineage.PrimaryIdentity.ToolAPISHA256, holdout.HoldoutIdentity.ToolAPISHA256
		case AxisDependency:
			primary[index], holdoutValues[index] = dependencySetDigest(lineage.PrimaryIdentity), dependencySetDigest(holdout.HoldoutIdentity)
		}
	}
	return primary, holdoutValues
}

func roleDigest(role RoleDescriptor) (string, error) {
	return digestProjection("role", roleProjection{Role: role.Role, ContentSHA256: role.ContentSHA256,
		Coverage: role.Coverage, LegacyIDSHA256: role.LegacyIDSHA256, LegacyReadOnly: role.LegacyReadOnly})
}

func differenceDigest(difference AxisDifference) (string, error) {
	return digestProjection("difference", differenceProjection{Axis: difference.Axis,
		PrimarySHA256: difference.PrimarySHA256, HoldoutSHA256: difference.HoldoutSHA256})
}

func holdoutDigest(holdout HoldoutBinding) (string, error) {
	return digestProjection("holdout", bindingProjection{HoldoutRole: holdout.HoldoutRole,
		HoldoutRoleSHA256: holdout.HoldoutRoleSHA256, HoldoutContractSHA256: holdout.HoldoutContractSHA256,
		HoldoutIdentity: cloneIdentity(holdout.HoldoutIdentity), Differences: cloneDifferences(holdout.Differences),
		ReviewedMaterialAxes: append([]DifferenceAxis{}, holdout.ReviewedMaterialAxes...)})
}

func digestLineage(lineage Lineage) (string, error) {
	return digestProjection("lineage", lineageProjection{Schema: lineage.Schema, SchemaVersion: lineage.SchemaVersion,
		ContractVersion: lineage.ContractVersion, Roles: cloneRoles(lineage.Roles), PrimaryRole: lineage.PrimaryRole,
		PrimaryRoleSHA256: lineage.PrimaryRoleSHA256, PrimaryContractSHA256: lineage.PrimaryContractSHA256,
		PrimaryIdentity: cloneIdentity(lineage.PrimaryIdentity), Holdouts: cloneHoldouts(lineage.Holdouts)})
}

func findRole(roles []RoleDescriptor, wanted DatasetRole) (*RoleDescriptor, bool) {
	for index := range roles {
		if roles[index].Role == wanted {
			return &roles[index], true
		}
	}
	return nil, false
}

func roleMap(roles []RoleDescriptor) map[DatasetRole]RoleDescriptor {
	result := make(map[DatasetRole]RoleDescriptor, len(roles))
	for _, role := range roles {
		result[role.Role] = role
	}
	return result
}

func roleOrdinal(role DatasetRole) int {
	for index, value := range closedRoles {
		if value == role {
			return index
		}
	}
	return -1
}

func axisOrdinal(axis DifferenceAxis) int {
	for index, value := range closedAxes {
		if value == axis {
			return index
		}
	}
	return -1
}

func cloneLineage(input Lineage) Lineage {
	output := input
	output.Roles = cloneRoles(input.Roles)
	output.PrimaryIdentity = cloneIdentity(input.PrimaryIdentity)
	output.Holdouts = cloneHoldouts(input.Holdouts)
	return output
}

func cloneRoles(input []RoleDescriptor) []RoleDescriptor {
	if input == nil {
		return nil
	}
	return append([]RoleDescriptor{}, input...)
}

func cloneDifferences(input []AxisDifference) []AxisDifference {
	if input == nil {
		return nil
	}
	return append([]AxisDifference{}, input...)
}

func cloneHoldouts(input []HoldoutBinding) []HoldoutBinding {
	if input == nil {
		return nil
	}
	output := make([]HoldoutBinding, len(input))
	for index, holdout := range input {
		output[index] = holdout
		output[index].HoldoutIdentity = cloneIdentity(holdout.HoldoutIdentity)
		output[index].Differences = cloneDifferences(holdout.Differences)
		output[index].ReviewedMaterialAxes = append([]DifferenceAxis{}, holdout.ReviewedMaterialAxes...)
	}
	return output
}
