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
	if len(Roles()) != 9 || len(DifferenceAxes()) != 11 {
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
