package corpus

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCacheBindingV1SealedMemberRoundTripAndVerification(t *testing.T) {
	root, store := newTestStore(t, Options{})
	_ = root
	defer func() { _ = store.Close() }()
	receipt := confluenceCaptureReceipt(t)
	generator, err := GeneratorIdentityDigest("test-1.0", strings.Repeat("a", 40), BuildStateClean)
	if err != nil {
		t.Fatal(err)
	}
	binding := mustCacheBinding(t, CacheBindingInput{
		Service: ServiceConfluence, ScopeDigest: receipt.ScopeDigest,
		SelectorDigest: receipt.SelectorDigest, OptionsDigest: receipt.OptionsDigest,
		TrustDigest: digestByte('5'), GeneratorDigest: generator, BuildState: BuildStateClean,
		ManifestSchema: ManifestSchemaV1, ReceiptSchema: ReceiptSchemaV1,
		ProjectionSchema: IndexerSchemaV2, CaptureSchema: CaptureReceiptSchemaV1,
		SelectionDigest: receipt.SelectionDigest, MetadataDigest: digestByte('6'), Total: receipt.Total,
		UserReferencesDeterministic: true, Reusable: true,
	})
	generation := sealCacheBindingGeneration(t, store, receipt, binding, CacheBindingMemberSpec())
	defer func() { _ = generation.Close() }()

	loaded, err := LoadCacheBindingV1(context.Background(), generation)
	if err != nil || loaded != binding {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	if err := VerifyCacheBindingV1(binding, generation); err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalCacheBindingV1(binding, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseCacheBindingV1(canonical, Limits{})
	if err != nil || parsed != binding {
		t.Fatalf("parsed=%#v err=%v", parsed, err)
	}

	tampered := append([]byte(nil), canonical...)
	tampered[len(tampered)-3] ^= 1
	if _, err := ParseCacheBindingV1(tampered, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tamper error=%v", err)
	}
}

func TestCacheBindingV1EligibilityAndGeneratorIdentityFailClosed(t *testing.T) {
	receipt := confluenceCaptureReceipt(t)
	base := CacheBindingInput{
		Service: ServiceConfluence, ScopeDigest: receipt.ScopeDigest,
		SelectorDigest: receipt.SelectorDigest, OptionsDigest: receipt.OptionsDigest,
		BuildState: BuildStateModified, ManifestSchema: ManifestSchemaV1, ReceiptSchema: ReceiptSchemaV1,
		ProjectionSchema: IndexerSchemaV2, CaptureSchema: CaptureReceiptSchemaV1,
		SelectionDigest: receipt.SelectionDigest, Total: receipt.Total,
		Reusable: false, IneligibleReason: CacheIneligibleBuildNotClean,
	}
	binding := mustCacheBinding(t, base)
	if binding.TrustDigest != "" || binding.GeneratorDigest != "" || binding.MetadataDigest != "" {
		t.Fatalf("optional ineligible digests=%#v", binding)
	}

	for name, mutate := range map[string]func(*CacheBindingInput){
		"reusable missing exact digests": func(input *CacheBindingInput) {
			input.BuildState = BuildStateClean
			input.Reusable = true
			input.IneligibleReason = ""
			input.UserReferencesDeterministic = true
		},
		"reusable nondeterministic users": func(input *CacheBindingInput) {
			input.BuildState = BuildStateClean
			input.Reusable = true
			input.IneligibleReason = ""
			input.TrustDigest, input.GeneratorDigest, input.MetadataDigest = digestByte('1'), digestByte('2'), digestByte('3')
		},
		"reason does not match": func(input *CacheBindingInput) {
			input.BuildState = BuildStateClean
		},
		"jira": func(input *CacheBindingInput) { input.Service = ServiceJira },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			mutate(&input)
			if _, err := BuildCacheBindingV1(input, Limits{}); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	for name, values := range map[string]struct {
		commit string
		state  BuildState
	}{
		"unknown": {commit: "unknown", state: BuildStateClean},
		"short":   {commit: strings.Repeat("a", 12), state: BuildStateClean},
		"dirty":   {commit: strings.Repeat("a", 40), state: BuildStateModified},
	} {
		t.Run("generator "+name, func(t *testing.T) {
			if digest, err := GeneratorIdentityDigest("test-1.0", values.commit, values.state); digest != "" || !errors.Is(err, ErrIntegrity) {
				t.Fatalf("digest=%q err=%v", digest, err)
			}
		})
	}
}

func TestLoadCacheBindingV1RequiresExactMemberPath(t *testing.T) {
	_, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	receipt := confluenceCaptureReceipt(t)
	binding := mustCacheBinding(t, CacheBindingInput{
		Service: ServiceConfluence, ScopeDigest: receipt.ScopeDigest,
		SelectorDigest: receipt.SelectorDigest, OptionsDigest: receipt.OptionsDigest,
		BuildState: BuildStateUnknown, ManifestSchema: ManifestSchemaV1, ReceiptSchema: ReceiptSchemaV1,
		ProjectionSchema: IndexerSchemaV2, CaptureSchema: CaptureReceiptSchemaV1,
		SelectionDigest: receipt.SelectionDigest, Total: receipt.Total,
		IneligibleReason: CacheIneligibleBuildNotClean,
	})
	wrong := CacheBindingMemberSpec()
	wrong.Path = "confluence/other-cache-binding.v1.json"
	generation := sealCacheBindingGeneration(t, store, receipt, binding, wrong)
	defer func() { _ = generation.Close() }()
	if _, err := LoadCacheBindingV1(context.Background(), generation); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("error=%v", err)
	}
}

func confluenceCaptureReceipt(t testing.TB) CaptureReceipt {
	t.Helper()
	input := validCaptureReceiptInput()
	input.Service = ServiceConfluence
	scope, err := PrincipalScopeDigest(ServiceConfluence, "sha256:"+digestByte('a'), "synthetic-user")
	if err != nil {
		t.Fatal(err)
	}
	input.ScopeDigest = scope
	return mustCaptureReceipt(t, input)
}

func mustCacheBinding(t testing.TB, input CacheBindingInput) CacheBindingV1 {
	t.Helper()
	binding, err := BuildCacheBindingV1(input, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func sealCacheBindingGeneration(t testing.TB, store *Store, capture CaptureReceipt, binding CacheBindingV1, bindingSpec MemberSpec) *Generation {
	t.Helper()
	stage, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	captureBytes, err := CanonicalCaptureReceipt(capture, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	bindingBytes, err := CanonicalCacheBindingV1(binding, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []struct {
		spec MemberSpec
		data []byte
	}{
		{MemberSpec{Service: ServiceConfluence, StableID: cacheCaptureReceiptStableID, Role: RoleMetadata, Path: cacheCaptureReceiptPath}, captureBytes},
		{bindingSpec, bindingBytes},
	} {
		if err := stage.Add(context.Background(), member.spec, bytes.NewReader(member.data)); err != nil {
			t.Fatal(err)
		}
	}
	generation, err := stage.Seal(context.Background(), SealOptions{
		ProjectionSchema: IndexerSchemaV2, GeneratorVersion: "test-1.0", BuildState: binding.BuildState,
		Qualifications: []Qualification{{
			Service: ServiceConfluence, ReceiptSchema: capture.SchemaVersion,
			ScopeDigest: capture.ScopeDigest, SelectorDigest: capture.SelectorDigest,
			ProjectionDigest: digestByte('7'), ReceiptDigest: capture.ReceiptDigest,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return generation
}
