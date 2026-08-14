package lineage

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSealIsCanonicalAndInputOwned(t *testing.T) {
	input := validLineageInput()
	sealed, err := Seal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(sealed); err != nil {
		t.Fatal(err)
	}
	encoded, err := Encode(sealed)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, sealed) {
		t.Fatal("decode changed the sealed record")
	}
	if !bytes.Equal(encoded, mustEncode(t, decoded)) {
		t.Fatal("encoded record is not byte stable")
	}
	if strings.Contains(string(encoded), "prompt") || strings.Contains(string(encoded), "secret") {
		t.Fatal("sealed content leaked into the public lineage projection")
	}

	input.Roles[0].ContentSHA256 = SHA256Hex([]byte("mutated-input"))
	input.PrimaryIdentity.SkillSHA256 = SHA256Hex([]byte("mutated-input-skill"))
	if err := Validate(sealed); err != nil {
		t.Fatal("mutating the input changed the sealed snapshot: ", err)
	}
}

func TestHoldoutClaimsAndIdentityDriftFailClosed(t *testing.T) {
	sealed := mustSeal(t, validLineageInput())

	noOpClaim := cloneLineage(sealed)
	noOpClaim.Holdouts[0].ReviewedMaterialAxes = []DifferenceAxis{AxisModel}
	if err := Validate(noOpClaim); err == nil {
		t.Fatal("unchanged claimed axis was accepted")
	}

	driftedIdentity := validLineageInput()
	driftedIdentity.Holdouts[0].HoldoutIdentity.ModelSHA256 = SHA256Hex([]byte("different-model"))
	if _, err := Seal(driftedIdentity); err == nil {
		t.Fatal("unclaimed identity drift was accepted")
	}

	sealedIdentityDrift := cloneLineage(sealed)
	sealedIdentityDrift.Holdouts[0].HoldoutIdentity.ModelSHA256 = SHA256Hex([]byte("different-model"))
	if err := Validate(sealedIdentityDrift); err == nil {
		t.Fatal("stale identity digest was accepted")
	}
}

func TestHoldoutMustUseADistinctDatasetIdentity(t *testing.T) {
	input := validLineageInput()
	input.Roles[1].ContentSHA256 = input.Roles[0].ContentSHA256
	input.Holdouts[0].ReviewedMaterialAxes = []DifferenceAxis{AxisModel}
	input.Holdouts[0].HoldoutIdentity.ModelSHA256 = digest("different-model")
	if _, err := Seal(input); lineageCode(err) != ErrorInvalidHoldout {
		t.Fatalf("same-dataset holdout error=%v", err)
	}
}

func TestLegacyIDDerivedRoleIsExplicitReadOnlyAndContentAddressed(t *testing.T) {
	input := validLineageInput()
	input.Roles[1].Role = RoleLegacyIDDerived
	input.Roles[1].LegacyIDSHA256 = SHA256Hex([]byte("legacy-id-derived-selector"))
	input.Roles[1].LegacyReadOnly = true
	input.Holdouts[0].HoldoutRole = RoleLegacyIDDerived
	sealed := mustSeal(t, input)
	if err := Validate(sealed); err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bytes.NewReader(mustEncode(t, sealed)))
	if err != nil {
		t.Fatal("historical legacy role was not readable: ", err)
	}
	legacy, ok := findRole(decoded.Roles, RoleLegacyIDDerived)
	if !ok || !legacy.LegacyReadOnly || !validDigest(legacy.LegacyIDSHA256) {
		t.Fatalf("legacy role lost its explicit read-only marker: %+v", legacy)
	}

	moved := cloneLineage(sealed)
	moved.Roles[1].LegacyIDSHA256 = SHA256Hex([]byte("different-selector"))
	if err := Validate(moved); err == nil {
		t.Fatal("moving a legacy alias to different content was accepted")
	}

	reclassified := cloneLineage(sealed)
	reclassified.Roles[1].Role = RoleGeneralization
	if err := Validate(reclassified); err == nil {
		t.Fatal("legacy role reclassification was accepted")
	}

	primaryLegacy := validLineageInput()
	primaryLegacy.Roles[0].Role = RoleLegacyIDDerived
	primaryLegacy.Roles[0].LegacyIDSHA256 = SHA256Hex([]byte("legacy-primary-selector"))
	primaryLegacy.Roles[0].LegacyReadOnly = true
	primaryLegacy.PrimaryRole = RoleLegacyIDDerived
	if _, err := Seal(primaryLegacy); lineageCode(err) != ErrorInvalidLineage {
		t.Fatalf("legacy primary error=%v", err)
	}
}

func TestEveryNonPrimaryRoleRequiresAHoldoutBinding(t *testing.T) {
	input := validLineageInput()
	input.Roles = append(input.Roles, RoleDescriptor{
		Role: RoleAuthoring, ContentSHA256: digest("unbound-role"), Coverage: Coverage{Total: 1, Covered: 1},
	})
	if _, err := Seal(input); lineageCode(err) != ErrorInvalidLineage {
		t.Fatalf("orphan role error=%v", err)
	}
}

func TestStrictDecoderRejectsAliasesFutureFieldsAndNonCanonicalBytes(t *testing.T) {
	encoded := mustEncode(t, mustSeal(t, validLineageInput()))
	tests := map[string][]byte{
		"future schema":      bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"unknown alias":      bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":1,"alias":"moved"`), 1),
		"duplicate member":   bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1),
		"trailing object":    append(append([]byte{}, encoded...), []byte(`{}`)...),
		"leading whitespace": append([]byte(" "), encoded...),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(bytes.NewReader(data)); err == nil {
				t.Fatal("non-canonical lineage was accepted")
			} else if strings.Contains(err.Error(), "moved") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("validation error leaked input content: %v", err)
			}
		})
	}
}

func TestDecodeRejectsCollectionExpansionBeforeTypedDecode(t *testing.T) {
	encoded := bytes.TrimSuffix(mustEncode(t, mustSeal(t, validLineageInput())), []byte{'\n'})
	tests := []struct {
		name       string
		key        string
		occurrence int
		limit      int
		existing   int
		alias      string
	}{
		{name: "roles", key: "roles", occurrence: 1, limit: MaxRoles, existing: 2, alias: "Roles"},
		{name: "holdouts", key: "holdouts", occurrence: 1, limit: MaxHoldouts, existing: 1, alias: "Holdouts"},
		{name: "primary dependencies", key: "dependency_sha256", occurrence: 1, limit: MaxDependencies, existing: 2, alias: "Dependency_sha256"},
		{name: "holdout dependencies", key: "dependency_sha256", occurrence: 2, limit: MaxDependencies, existing: 2, alias: "Dependency_SHA256"},
		{name: "differences", key: "differences", occurrence: 1, limit: len(closedAxes), existing: len(closedAxes), alias: "Differences"},
		{name: "reviewed axes", key: "reviewed_material_axes", occurrence: 1, limit: MaxMaterialAxes, existing: 1, alias: "Reviewed_Material_Axes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			atLimit := insertArrayItems(encoded, test.key, test.occurrence, test.limit-test.existing)
			if err := validateJSONShape(atLimit); err != nil {
				t.Fatalf("exact %s bound rejected: %v", test.name, err)
			}
			overLimit := insertArrayItems(encoded, test.key, test.occurrence, test.limit-test.existing+1)
			if err := validateJSONShape(overLimit); err == nil {
				t.Fatalf("over-limit %s array reached typed decoding", test.name)
			}
			if _, err := Decode(bytes.NewReader(append(append([]byte{}, overLimit...), '\n'))); lineageCode(err) != ErrorInvalidLineage {
				t.Fatalf("oversized %s decode error=%v", test.name, err)
			}
			alias := replaceArrayKey(encoded, test.key, test.occurrence, test.alias)
			if err := validateJSONShape(alias); err == nil {
				t.Fatalf("case-folded %s alias reached typed decoding", test.name)
			}
		})
	}
}

func TestIdentityRequiresCompleteStableDigests(t *testing.T) {
	missing := validLineageInput()
	missing.PrimaryIdentity.EvalSHA256 = ""
	if _, err := Seal(missing); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing identity error=%v", err)
	}

	sealed := mustSeal(t, validLineageInput())
	unordered := cloneLineage(sealed)
	unordered.PrimaryIdentity.DependencySHA256[0], unordered.PrimaryIdentity.DependencySHA256[1] =
		unordered.PrimaryIdentity.DependencySHA256[1], unordered.PrimaryIdentity.DependencySHA256[0]
	if err := Validate(unordered); err == nil {
		t.Fatal("dependency identity order drift was accepted")
	}
}

func TestSealRejectsOversizedAndMalformedCollectionsWithoutPanic(t *testing.T) {
	input := validLineageInput()
	input.Holdouts[0].Differences = make([]AxisDifference, len(DifferenceAxes())+1)
	assertLineageError(t, func() error {
		_, err := Seal(input)
		return err
	}, ErrorLimitExceeded)

	input = validLineageInput()
	input.Roles = make([]RoleDescriptor, MaxRoles+1)
	assertLineageError(t, func() error {
		_, err := Seal(input)
		return err
	}, ErrorLimitExceeded)

	input = validLineageInput()
	input.PrimaryIdentity.DependencySHA256 = make([]string, MaxDependencies+1)
	assertLineageError(t, func() error {
		_, err := Seal(input)
		return err
	}, ErrorLimitExceeded)
}

func TestRoleAndAxisVocabulariesAreClosed(t *testing.T) {
	if !reflect.DeepEqual(Roles(), []DatasetRole{
		"authoring", "train", "validation", "generalization", "trigger",
		"security", "evolution_compatibility", "final_promotion", "legacy_id_derived",
	}) || !reflect.DeepEqual(DifferenceAxes(), []DifferenceAxis{
		"dataset", "contract", "skill", "evaluation", "grader", "agent", "model",
		"harness", "environment", "tool_api", "dependency",
	}) {
		t.Fatal("closed vocabulary changed unexpectedly")
	}
	input := validLineageInput()
	input.Roles[0].Role = DatasetRole("moved-alias")
	if _, err := Seal(input); err == nil {
		t.Fatal("unknown role alias was accepted")
	}
	input = validLineageInput()
	input.Holdouts[0].ReviewedMaterialAxes = []DifferenceAxis{DifferenceAxis("prompt")}
	if _, err := Seal(input); err == nil {
		t.Fatal("unknown difference axis was accepted")
	}
}

func TestCanonicalV1KnownAnswerVector(t *testing.T) {
	sealed, err := Seal(knownVectorInput())
	if err != nil {
		t.Fatal(err)
	}
	if sealed.LineageSHA256 != "bc70638815e90df9be8e10ffd83f9581d66e35fa6290a2c68e1059bb3637a800" {
		t.Fatalf("lineage digest=%s", sealed.LineageSHA256)
	}
	encoded, err := Encode(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if got := SHA256Hex(encoded); got != "2207aaf9c3d0fab2727b1c5bbd6e475fd2937f82207bfdcf639efe6930971485" {
		t.Fatalf("canonical wire digest=%s", got)
	}
	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil || !reflect.DeepEqual(decoded, sealed) {
		t.Fatalf("known vector did not round-trip: %v", err)
	}
}

func FuzzDecodeRejectsUntrustedBytes(f *testing.F) {
	seed := mustEncode(f, mustSeal(f, validLineageInput()))
	f.Add(seed)
	f.Add([]byte(`{"schema":"agent-eval/dataset-lineage","schema_version":1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		decoded, err := Decode(bytes.NewReader(data))
		if err != nil {
			return
		}
		canonical, encodeErr := Encode(decoded)
		if encodeErr != nil || !bytes.Equal(canonical, data) {
			t.Fatal("decoder accepted a non-canonical record")
		}
	})
}

func validLineageInput() Lineage {
	return Lineage{
		Roles: []RoleDescriptor{
			{Role: RoleValidation, ContentSHA256: digest("primary-content"), Coverage: Coverage{Total: 3, Covered: 3}},
			{Role: RoleGeneralization, ContentSHA256: digest("holdout-content"), Coverage: Coverage{Total: 1, Covered: 1}},
		},
		PrimaryRole:           RoleValidation,
		PrimaryContractSHA256: digest("primary-contract"),
		PrimaryIdentity:       validIdentity("same-runtime"),
		Holdouts: []HoldoutBinding{{
			HoldoutRole:           RoleGeneralization,
			HoldoutContractSHA256: digest("primary-contract"),
			HoldoutIdentity:       validIdentity("same-runtime"),
			ReviewedMaterialAxes:  []DifferenceAxis{AxisDataset},
		}},
	}
}

func validIdentity(seed string) RuntimeIdentity {
	return RuntimeIdentity{
		SkillSHA256:       digest(seed + "-skill"),
		EvalSHA256:        digest(seed + "-eval"),
		GraderSHA256:      digest(seed + "-grader"),
		AgentSHA256:       digest(seed + "-agent"),
		ModelSHA256:       digest(seed + "-model"),
		HarnessSHA256:     digest(seed + "-harness"),
		EnvironmentSHA256: digest(seed + "-environment"),
		ToolAPISHA256:     digest(seed + "-tool-api"),
		DependencySHA256:  []string{digest(seed + "-dependency-b"), digest(seed + "-dependency-a")},
	}
}

func knownVectorInput() Lineage {
	return Lineage{
		Roles: []RoleDescriptor{
			{Role: RoleValidation, ContentSHA256: "1ef8f27e50b01f3ef4844d172b6db57024565edae5f9a94d5e22da4845a01ae9", Coverage: Coverage{Total: 3, Covered: 3}},
			{Role: RoleGeneralization, ContentSHA256: "81e0aea5f4ddd15b09e85266b820302b721b695b521ae9a46ce364e6c2351fd1", Coverage: Coverage{Total: 1, Covered: 1}},
		},
		PrimaryRole:           RoleValidation,
		PrimaryContractSHA256: "8565caeff2db7a7bc72fd845216f5c0963f081219a90fff23782e287abe9a304",
		PrimaryIdentity:       knownVectorIdentity(),
		Holdouts: []HoldoutBinding{{
			HoldoutRole:           RoleGeneralization,
			HoldoutContractSHA256: "8565caeff2db7a7bc72fd845216f5c0963f081219a90fff23782e287abe9a304",
			HoldoutIdentity:       knownVectorIdentity(),
			ReviewedMaterialAxes:  []DifferenceAxis{AxisDataset},
		}},
	}
}

func knownVectorIdentity() RuntimeIdentity {
	return RuntimeIdentity{
		SkillSHA256:       "9b645a81d403c24318cdedeb4089606d26b476265557d53f0494b1e2eef9fc9d",
		EvalSHA256:        "ebfec4f2c7fe55f1645c51270977079860493858d1c0ebe4b6ef357b2079d0a0",
		GraderSHA256:      "0f99332201edd3021d54c017945e9a0f56703e3dbad2b8f52618e796039fa27a",
		AgentSHA256:       "3717a1d609d196a0d52d730011de630ba030dcb8d6f62672d97f9ef3043e7fac",
		ModelSHA256:       "0f4733bd9e3b2d4b61e009fc766af966588268d4b9c7415ec0d8358153b6776c",
		HarnessSHA256:     "2919e9563e5ee0b37e76c5cb1c6d3bfaedfba98b3f7cb60e1045fca43d06332c",
		EnvironmentSHA256: "711b48f53834df02d489a53a96d494b9b79fd66d2ee38a7a601544e8dc1d57df",
		ToolAPISHA256:     "b5ce0dfcf7c061a56c6c274d01f039270379245404378101adfe478e2d28128f",
		DependencySHA256: []string{
			"4a1b95325c8a5ae31ae4343b6efa5e6224a22745bb34e173f6f703c6e63f2bdf",
			"4c0cb4b8c7d27999cc6e2183bec475d304c85a7cc9ddb5619ff50a313c72f951",
		},
	}
}

func digest(value string) string { return SHA256Hex([]byte(value)) }

func mustSeal(t testing.TB, input Lineage) Lineage {
	t.Helper()
	sealed, err := Seal(input)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func mustEncode(t testing.TB, input Lineage) []byte {
	t.Helper()
	encoded, err := Encode(input)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertLineageError(t testing.TB, operation func() error, code ErrorCode) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("lineage operation panicked: %v", recovered)
		}
	}()
	err := operation()
	if err == nil {
		t.Fatalf("expected lineage error %q", code)
	}
	if got, ok := CodeOf(err); !ok || got != code {
		t.Fatalf("error code=%q, ok=%v, want=%q", got, ok, code)
	}
}

func lineageCode(err error) ErrorCode {
	if err == nil {
		return ""
	}
	code, _ := CodeOf(err)
	return code
}

func insertArrayItems(encoded []byte, key string, occurrence, count int) []byte {
	if count <= 0 {
		return encoded
	}
	marker := []byte(`"` + key + `":[`)
	index := -1
	searchFrom := 0
	for current := 0; current < occurrence; current++ {
		found := bytes.Index(encoded[searchFrom:], marker)
		if found < 0 {
			return encoded
		}
		index = searchFrom + found
		searchFrom = index + len(marker)
	}
	if index < 0 {
		return encoded
	}
	insert := strings.TrimSuffix(strings.Repeat("null,", count), ",") + ","
	result := make([]byte, 0, len(encoded)+len(insert))
	result = append(result, encoded[:index+len(marker)]...)
	result = append(result, insert...)
	result = append(result, encoded[index+len(marker):]...)
	return result
}

func replaceArrayKey(encoded []byte, key string, occurrence int, replacement string) []byte {
	marker := []byte(`"` + key + `":[`)
	index := -1
	searchFrom := 0
	for current := 0; current < occurrence; current++ {
		found := bytes.Index(encoded[searchFrom:], marker)
		if found < 0 {
			return encoded
		}
		index = searchFrom + found
		searchFrom = index + len(marker)
	}
	if index < 0 {
		return encoded
	}
	result := make([]byte, 0, len(encoded)+len(replacement)-len(key))
	result = append(result, encoded[:index]...)
	result = append(result, []byte(`"`+replacement+`":[`)...)
	result = append(result, encoded[index+len(marker):]...)
	return result
}
