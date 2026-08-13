package corpus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetentionV1RejectsInvalidCallerAndReviewedPlanState(t *testing.T) {
	_, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	publishRetentionChain(t, store, 4)
	plan, canonical := retentionPlanBytes(t, store, 1)
	if err := VerifyRetentionPlanV1(plan, Limits{}); err != nil {
		t.Fatal(err)
	}

	var nilStore *Store
	if _, err := nilStore.RetentionInventoryStatusV1(context.Background()); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("nil status error=%v", err)
	}
	if _, err := BuildRetentionPlanV1(context.Background(), nilStore, plan.Policy); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("nil preview error=%v", err)
	}
	if _, err := nilStore.ApplyRetentionPlanV1(context.Background(), canonical, plan.PlanDigest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("nil apply error=%v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.RetentionInventoryStatusV1(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled status error=%v", err)
	}
	if _, err := BuildRetentionPlanV1(canceled, store, plan.Policy); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preview error=%v", err)
	}
	if _, err := store.ApplyRetentionPlanV1(canceled, canonical, plan.PlanDigest); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled apply error=%v", err)
	}

	if _, err := ParseRetentionPlanV1(nil, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("empty plan error=%v", err)
	}
	noncanonical := append(append([]byte(nil), canonical...), ' ')
	if _, err := ParseRetentionPlanV1(noncanonical, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("noncanonical plan error=%v", err)
	}
	if _, err := CanonicalRetentionPlanV1(plan, Limits{MaxManifestBytes: 1}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("bounded canonical error=%v", err)
	}
	if _, err := CanonicalRetentionPlanV1(plan, Limits{MaxMembers: -1}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("invalid canonical limits error=%v", err)
	}
	invalidPlan := cloneRetentionPlanForTest(plan)
	invalidPlan.SchemaVersion++
	if _, err := CanonicalRetentionPlanV1(invalidPlan, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("invalid canonical plan error=%v", err)
	}
	if _, err := ParseRetentionPlanV1(canonical, Limits{MaxManifestBytes: 1}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("bounded parse error=%v", err)
	}
	if _, err := ParseRetentionPlanV1(canonical, Limits{MaxMembers: -1}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("invalid parse limits error=%v", err)
	}
	invalidPlanBytes, err := marshalCanonical(invalidPlan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseRetentionPlanV1(invalidPlanBytes, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("invalid semantic plan error=%v", err)
	}
	if _, err := store.ApplyRetentionPlanV1(context.Background(), canonical, "not-a-digest"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("expected-digest error=%v", err)
	}
	_, closed := newTestStore(t, Options{})
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.RetentionInventoryStatusV1(context.Background()); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("closed status error=%v", err)
	}
	if _, err := BuildRetentionPlanV1(context.Background(), closed, plan.Policy); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("closed preview error=%v", err)
	}
	if _, err := closed.ApplyRetentionPlanV1(context.Background(), canonical, plan.PlanDigest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("closed apply error=%v", err)
	}

	_, empty := newTestStore(t, Options{})
	defer func() { _ = empty.Close() }()
	if _, err := BuildRetentionPlanV1(context.Background(), empty, plan.Policy); !errors.Is(err, ErrNoCurrent) {
		t.Fatalf("empty preview error=%v", err)
	}
	for name, policy := range map[string]RetentionPolicyV1{
		"schema": {SchemaVersion: 2, RetainPredecessors: 1},
		"depth":  {SchemaVersion: RetentionPolicySchemaV1, RetainPredecessors: 0},
	} {
		t.Run("policy "+name, func(t *testing.T) {
			if _, err := BuildRetentionPlanV1(context.Background(), store, policy); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	limits, err := normalizeLimits(Limits{})
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name          string
		requireDigest bool
		limits        Limits
		mutate        func(*RetentionPlanV1)
	}{
		{name: "schema", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.SchemaVersion++ }},
		{name: "root digest", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.RootDigest = "short" }},
		{name: "policy schema", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Policy.SchemaVersion++ }},
		{name: "policy depth", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Policy.RetainPredecessors = 0 }},
		{name: "pointer", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Current.GenerationID = "short" }},
		{name: "empty inventory", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Inventory = nil }},
		{name: "empty protected", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Protected = nil }},
		{name: "protected above policy", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Protected = append(value.Protected, value.Protected[0]) }},
		{name: "negative unsealed", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.UnsealedStages = -1 }},
		{name: "unsealed digest", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.UnsealedInventoryDigest = "short" }},
		{name: "inventory bound", requireDigest: true, limits: limitsWithMembers(limits, 3)},
		{name: "record id", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Inventory[0].GenerationID = "short" }},
		{name: "record digest", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Inventory[0].GenerationDigest = "short" }},
		{name: "predecessor digest", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Inventory[0].PredecessorGenerationDigest = "short" }},
		{name: "predecessor id", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Inventory[0].PredecessorGenerationID = "short" }},
		{name: "self predecessor", requireDigest: true, mutate: func(value *RetentionPlanV1) {
			index := retentionRecordIndex(value, value.Current.GenerationID)
			value.Inventory[index].PredecessorGenerationID = value.Current.GenerationID
		}},
		{name: "record totals", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Inventory[0].Totals.Bytes = -1 }},
		{name: "inventory order", requireDigest: true, mutate: func(value *RetentionPlanV1) {
			value.Inventory[0], value.Inventory[1] = value.Inventory[1], value.Inventory[0]
		}},
		{name: "duplicate digest", requireDigest: true, mutate: func(value *RetentionPlanV1) {
			value.Inventory[1].GenerationDigest = value.Inventory[0].GenerationDigest
		}},
		{name: "current absent", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Current.GenerationID = strings.Repeat("f", 32) }},
		{name: "protected absent", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Protected[1].GenerationID = strings.Repeat("f", 32) }},
		{name: "duplicate protected", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Protected[1] = value.Protected[0] }},
		{name: "first protected", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Protected[0] = value.Protected[1] }},
		{name: "broken protected lineage", requireDigest: true, mutate: func(value *RetentionPlanV1) {
			index := retentionRecordIndex(value, value.Protected[0].GenerationID)
			value.Inventory[index].PredecessorGenerationDigest = digestByte('f')
		}},
		{name: "candidate absent", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Candidates[0].GenerationID = strings.Repeat("f", 32) }},
		{name: "candidate order", requireDigest: true, mutate: func(value *RetentionPlanV1) {
			value.Candidates[0], value.Candidates[1] = value.Candidates[1], value.Candidates[0]
		}},
		{name: "duplicate partition", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Candidates[0] = value.Protected[0] }},
		{name: "incomplete partition", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.Candidates = value.Candidates[:1] }},
		{name: "missing plan digest", requireDigest: true, mutate: func(value *RetentionPlanV1) { value.PlanDigest = "" }},
		{name: "premature plan digest", requireDigest: false},
	}
	for _, test := range mutations {
		t.Run("plan "+test.name, func(t *testing.T) {
			value := cloneRetentionPlanForTest(plan)
			if test.mutate != nil {
				test.mutate(&value)
			}
			testLimits := test.limits
			if testLimits == (Limits{}) {
				testLimits = limits
			}
			if err := validateRetentionPlan(value, testLimits, test.requireDigest); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCacheBindingV1RejectsEveryUnboundIdentityClass(t *testing.T) {
	receipt := confluenceCaptureReceipt(t)
	generator, err := GeneratorIdentityDigest("test-1.0", strings.Repeat("a", 40), BuildStateClean)
	if err != nil {
		t.Fatal(err)
	}
	base := mustCacheBinding(t, CacheBindingInput{
		Service: ServiceConfluence, ScopeDigest: receipt.ScopeDigest,
		SelectorDigest: receipt.SelectorDigest, OptionsDigest: receipt.OptionsDigest,
		TrustDigest: digestByte('5'), GeneratorDigest: generator, BuildState: BuildStateClean,
		ManifestSchema: ManifestSchemaV1, ReceiptSchema: ReceiptSchemaV1,
		ProjectionSchema: IndexerSchemaV2, CaptureSchema: CaptureReceiptSchemaV1,
		SelectionDigest: receipt.SelectionDigest, MetadataDigest: digestByte('6'), Total: receipt.Total,
		UserReferencesDeterministic: true, Reusable: true,
	})
	limits, err := normalizeLimits(Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*CacheBindingV1){
		"schema":          func(value *CacheBindingV1) { value.SchemaVersion++ },
		"service":         func(value *CacheBindingV1) { value.Service = ServiceJira },
		"build state":     func(value *CacheBindingV1) { value.BuildState = "other" },
		"scope":           func(value *CacheBindingV1) { value.ScopeDigest = "short" },
		"trust":           func(value *CacheBindingV1) { value.TrustDigest = "short" },
		"negative total":  func(value *CacheBindingV1) { value.Total = -1 },
		"reusable reason": func(value *CacheBindingV1) { value.IneligibleReason = CacheIneligibleEvidenceIncomplete },
		"binding digest":  func(value *CacheBindingV1) { value.BindingDigest = "short" },
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			mutate(&value)
			if err := validateCacheBindingFields(value, limits, true); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	for reason, value := range map[CacheBindingIneligibleReason]CacheBindingV1{
		CacheIneligibleBuildNotClean:                  {IneligibleReason: CacheIneligibleBuildNotClean, BuildState: BuildStateModified},
		CacheIneligibleGeneratorUnbound:               {IneligibleReason: CacheIneligibleGeneratorUnbound},
		CacheIneligibleTrustUnbound:                   {IneligibleReason: CacheIneligibleTrustUnbound},
		CacheIneligibleMetadataUnbound:                {IneligibleReason: CacheIneligibleMetadataUnbound},
		CacheIneligibleUserReferencesNondeterministic: {IneligibleReason: CacheIneligibleUserReferencesNondeterministic},
		CacheIneligibleEvidenceIncomplete:             {IneligibleReason: CacheIneligibleEvidenceIncomplete},
	} {
		t.Run("reason "+string(reason), func(t *testing.T) {
			if !validCacheIneligibleReason(reason) || !cacheIneligibleReasonMatches(value) {
				t.Fatalf("reason %q did not match", reason)
			}
		})
	}
	if validCacheIneligibleReason("other") || cacheIneligibleReasonMatches(CacheBindingV1{IneligibleReason: "other"}) {
		t.Fatal("unknown ineligible reason was accepted")
	}
	if validExactCommit(strings.Repeat("A", 40)) || validExactCommit(strings.Repeat("g", 40)) || validExactCommit(strings.Repeat("a", 39)) {
		t.Fatal("invalid exact commit was accepted")
	}
	if _, err := GeneratorIdentityDigest("", strings.Repeat("a", 40), BuildStateClean); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("empty generator version error=%v", err)
	}
	if digest, err := GeneratorIdentityDigest("test-1.0", strings.Repeat("b", 64), BuildStateClean); err != nil || digest == "" {
		t.Fatalf("64-byte commit digest=%q error=%v", digest, err)
	}
	input := CacheBindingInput{
		Service: ServiceConfluence, ScopeDigest: receipt.ScopeDigest,
		SelectorDigest: receipt.SelectorDigest, OptionsDigest: receipt.OptionsDigest,
		TrustDigest: digestByte('5'), GeneratorDigest: generator, BuildState: BuildStateClean,
		ManifestSchema: ManifestSchemaV1, ReceiptSchema: ReceiptSchemaV1,
		ProjectionSchema: IndexerSchemaV2, CaptureSchema: CaptureReceiptSchemaV1,
		SelectionDigest: receipt.SelectionDigest, MetadataDigest: digestByte('6'), Total: receipt.Total,
		UserReferencesDeterministic: true, Reusable: true,
	}
	if _, err := BuildCacheBindingV1(input, Limits{MaxMembers: -1}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("invalid build limits error=%v", err)
	}
	canonical, err := CanonicalCacheBindingV1(base, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	invalidField := base
	invalidField.ScopeDigest = "short"
	if _, err := CanonicalCacheBindingV1(invalidField, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("invalid canonical field error=%v", err)
	}
	wrongDigest := base
	wrongDigest.BindingDigest = digestByte('f')
	if _, err := CanonicalCacheBindingV1(wrongDigest, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("self-digest mismatch error=%v", err)
	}
	if _, err := CanonicalCacheBindingV1(base, Limits{MaxMembers: -1}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("invalid canonical limits error=%v", err)
	}
	if _, err := CanonicalCacheBindingV1(base, Limits{MaxMemberBytes: 1}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("bounded canonical error=%v", err)
	}
	if _, err := ParseCacheBindingV1(nil, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("empty parse error=%v", err)
	}
	if _, err := ParseCacheBindingV1(canonical, Limits{MaxMembers: -1}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("invalid parse limits error=%v", err)
	}
	if _, err := ParseCacheBindingV1(canonical, Limits{MaxMemberBytes: 1}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("bounded parse error=%v", err)
	}
	noncanonical := append(append([]byte(nil), canonical...), ' ')
	if _, err := ParseCacheBindingV1(noncanonical, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("noncanonical parse error=%v", err)
	}
	invalidBytes, err := marshalCanonical(invalidField)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCacheBindingV1(invalidBytes, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("invalid semantic parse error=%v", err)
	}
	var generation *Generation
	if _, err := LoadCacheBindingV1(context.Background(), generation); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("nil load error=%v", err)
	}
	if err := VerifyCacheBindingV1(base, generation); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("nil verify error=%v", err)
	}

	_, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	sealed := sealCacheBindingGeneration(t, store, receipt, base, CacheBindingMemberSpec())
	defer func() { _ = sealed.Close() }()
	if err := VerifyCacheBindingV1(invalidField, sealed); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("invalid supplied binding error=%v", err)
	}
	invalidReason := base
	invalidReason.Reusable = false
	invalidReason.IneligibleReason = "other"
	if err := validateCacheBindingFields(invalidReason, limits, true); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("invalid ineligible reason error=%v", err)
	}
	if err := validateCacheBindingFields(base, limits, false); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("premature binding digest error=%v", err)
	}
	differentInput := input
	differentInput.MetadataDigest = digestByte('7')
	different := mustCacheBinding(t, differentInput)
	if err := VerifyCacheBindingV1(different, sealed); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("different binding error=%v", err)
	}
	driftInput := input
	driftInput.ScopeDigest = digestByte('8')
	drift := mustCacheBinding(t, driftInput)
	driftGeneration := sealCacheBindingGeneration(t, store, receipt, drift, CacheBindingMemberSpec())
	defer func() { _ = driftGeneration.Close() }()
	if err := VerifyCacheBindingV1(drift, driftGeneration); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("qualification drift error=%v", err)
	}
	optionsInput := input
	optionsInput.OptionsDigest = digestByte('9')
	optionsDrift := mustCacheBinding(t, optionsInput)
	optionsGeneration := sealCacheBindingGeneration(t, store, receipt, optionsDrift, CacheBindingMemberSpec())
	defer func() { _ = optionsGeneration.Close() }()
	if err := VerifyCacheBindingV1(optionsDrift, optionsGeneration); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("capture drift error=%v", err)
	}
	missingCapture := sealCacheBindingOnlyGeneration(t, store, receipt, base)
	defer func() { _ = missingCapture.Close() }()
	if err := VerifyCacheBindingV1(base, missingCapture); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("missing capture error=%v", err)
	}

	partialInput := validCaptureReceiptInput()
	partialScope, err := PrincipalScopeDigest(ServiceConfluence, "sha256:"+digestByte('a'), "synthetic-user")
	if err != nil {
		t.Fatal(err)
	}
	partialInput.Service = ServiceConfluence
	partialInput.ScopeDigest = partialScope
	partialInput.Dimensions[0].State = CapturePartial
	partialReceipt := mustCaptureReceipt(t, partialInput)
	partialBindingInput := input
	partialBindingInput.ScopeDigest = partialReceipt.ScopeDigest
	partialBindingInput.SelectorDigest = partialReceipt.SelectorDigest
	partialBindingInput.OptionsDigest = partialReceipt.OptionsDigest
	partialBindingInput.SelectionDigest = partialReceipt.SelectionDigest
	partialBindingInput.Total = partialReceipt.Total
	partialBinding := mustCacheBinding(t, partialBindingInput)
	partialGeneration := sealCacheBindingGeneration(t, store, partialReceipt, partialBinding, CacheBindingMemberSpec())
	defer func() { _ = partialGeneration.Close() }()
	if err := VerifyCacheBindingV1(partialBinding, partialGeneration); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("partial capture error=%v", err)
	}

	if err := driftGeneration.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCacheBindingV1(context.Background(), driftGeneration); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("closed load error=%v", err)
	}
	if err := VerifyCacheBindingV1(drift, driftGeneration); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("closed verify error=%v", err)
	}
}

func TestBuildWorkspaceRejectsInvalidAndClosedOperations(t *testing.T) {
	root := privateBuildWorkspaceRoot(t)
	//nolint:staticcheck // Exercise the public nil-context rejection contract.
	if workspace, err := InitializeBuildWorkspace(nil, root, Options{}); workspace != nil || !errors.Is(err, ErrIntegrity) {
		t.Fatalf("nil-context workspace=%#v error=%v", workspace, err)
	}

	workspaceRoot := privateBuildWorkspaceRoot(t)
	workspace, err := InitializeBuildWorkspace(context.Background(), workspaceRoot, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.SelectCurrent(context.Background()); !errors.Is(err, ErrNoCurrent) {
		t.Fatalf("fresh current error=%v", err)
	}
	for name, services := range map[string][]Service{
		"empty":     nil,
		"duplicate": {ServiceJira, ServiceJira},
		"unsorted":  {ServiceJira, ServiceConfluence},
	} {
		t.Run("services "+name, func(t *testing.T) {
			if _, _, err := workspace.BeginAttempt(services); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if _, err := workspace.AttemptRoot("short", ServiceJira); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("invalid attempt error=%v", err)
	}
	attemptID, _, err := workspace.BeginAttempt([]Service{ServiceJira})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := workspace.LoadCaptureReceipt(attemptID, ServiceJira); err != nil || found {
		t.Fatalf("missing receipt found=%t error=%v", found, err)
	}
	if _, _, err := workspace.LoadCaptureReceipt("short", ServiceJira); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("invalid receipt path error=%v", err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	active := buildWorkspaceActive(attemptID, mustCaptureReceipt(t, validCaptureReceiptInput()))
	if _, _, err := workspace.BeginAttempt([]Service{ServiceJira}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("closed begin error=%v", err)
	}
	if _, err := workspace.AttemptRoot(attemptID, ServiceJira); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("closed attempt error=%v", err)
	}
	if err := workspace.SaveActive(active); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("closed active error=%v", err)
	}
	if _, _, err := workspace.LoadActive(); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("closed load error=%v", err)
	}
	if err := workspace.SaveCaptureReceipt(attemptID, mustCaptureReceipt(t, validCaptureReceiptInput())); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("closed receipt error=%v", err)
	}
	if _, _, err := workspace.LoadCaptureReceipt(attemptID, ServiceJira); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("closed receipt load error=%v", err)
	}

	for name, prepare := range map[string]func(t *testing.T, root string){
		"missing attempts": func(_ *testing.T, _ string) {},
		"missing lock": func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, buildAttemptsDir), privateDirMode); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			partialRoot := privateBuildWorkspaceRoot(t)
			store, err := Initialize(partialRoot, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			prepare(t, partialRoot)
			if opened, err := OpenBuildWorkspace(context.Background(), partialRoot, Options{}); opened != nil || !errors.Is(err, ErrIntegrity) {
				t.Fatalf("workspace=%#v error=%v", opened, err)
			}
		})
	}
}

func TestRetentionV1RevalidationAndFaultBoundaries(t *testing.T) {
	t.Run("pinned records", func(t *testing.T) {
		_, store := newTestStore(t, Options{})
		defer func() { _ = store.Close() }()
		publishRetentionChain(t, store, 3)
		plan, _ := retentionPlanBytes(t, store, 1)
		records := retentionProtectedRecords(plan)

		missing := records[0]
		missing.GenerationID = strings.Repeat("f", 32)
		if pinned, err := pinRetentionRecords(context.Background(), store, []RetentionInventoryRecordV1{missing}); pinned != nil || !errors.Is(err, ErrIntegrity) {
			t.Fatalf("missing pinned=%#v error=%v", pinned, err)
		}
		mismatched := records[0]
		mismatched.GenerationDigest = digestByte('f')
		if pinned, err := pinRetentionRecords(context.Background(), store, []RetentionInventoryRecordV1{mismatched}); pinned != nil || !errors.Is(err, ErrIntegrity) {
			t.Fatalf("mismatched pinned=%#v error=%v", pinned, err)
		}
		pinned, err := pinRetentionRecords(context.Background(), store, records[:1])
		if err != nil {
			t.Fatal(err)
		}
		if err := pinned[0].generation.Close(); err != nil {
			t.Fatal(err)
		}
		if err := revalidateRetentionPinned(context.Background(), store, pinned); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("closed revalidation error=%v", err)
		}

		snapshot, err := store.scanRetentionInventory(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		short := cloneRetentionEntries(snapshot.entries)
		for id := range short {
			delete(short, id)
			break
		}
		if err := store.confirmRetentionEntries(context.Background(), short, ""); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("short inventory error=%v", err)
		}
		wrong := cloneRetentionEntries(snapshot.entries)
		ids := make([]string, 0, len(wrong))
		for id := range wrong {
			ids = append(ids, id)
		}
		wrong[ids[0]] = wrong[ids[1]]
		if err := store.confirmRetentionEntries(context.Background(), wrong, ""); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("replaced inventory error=%v", err)
		}
	})

	for _, step := range []string{"before_retention_remove", "after_retention_namespace_check", "after_retention_sync"} {
		t.Run(step, func(t *testing.T) {
			_, store := newTestStore(t, Options{})
			defer func() { _ = store.Close() }()
			publishRetentionChain(t, store, 3)
			plan, canonical := retentionPlanBytes(t, store, 1)
			store.testHook = failAt(step)
			_, err := store.ApplyRetentionPlanV1(context.Background(), canonical, plan.PlanDigest)
			if step == "after_retention_sync" {
				if !errors.Is(err, ErrOutcomeUnknown) {
					t.Fatalf("error=%v", err)
				}
			} else if !errors.Is(err, ErrIntegrity) || errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	t.Run("cancellation after logical deletion", func(t *testing.T) {
		_, store := newTestStore(t, Options{})
		defer func() { _ = store.Close() }()
		publishRetentionChain(t, store, 4)
		plan, canonical := retentionPlanBytes(t, store, 1)
		ctx, cancel := context.WithCancel(context.Background())
		store.testHook = func(step string) error {
			if step == "after_retention_remove" {
				cancel()
			}
			return nil
		}
		if _, err := store.ApplyRetentionPlanV1(ctx, canonical, plan.PlanDigest); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestRetentionV1InventoryRejectsUntrustedNamespaceEntries(t *testing.T) {
	t.Run("canceled listing", func(t *testing.T) {
		_, store := newTestStore(t, Options{})
		defer func() { _ = store.Close() }()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := store.listRetentionGenerationEntries(ctx, ""); !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	})

	for name, create := range map[string]func(t *testing.T, root string){
		"invalid id": func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, generationsDir, "invalid"), privateDirMode); err != nil {
				t.Fatal(err)
			}
		},
		"special entry": func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, generationsDir, strings.Repeat("e", 32)), nil, privateFileMode); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, store := newTestStore(t, Options{})
			defer func() { _ = store.Close() }()
			create(t, root)
			if _, err := store.RetentionInventoryStatusV1(context.Background()); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	t.Run("inventory bound", func(t *testing.T) {
		root, store := newTestStore(t, Options{Limits: Limits{MaxMembers: 1}})
		defer func() { _ = store.Close() }()
		for _, id := range []string{strings.Repeat("a", 32), strings.Repeat("b", 32)} {
			if err := os.Mkdir(filepath.Join(root, generationsDir, id), privateDirMode); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.RetentionInventoryStatusV1(context.Background()); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("receipt mode", func(t *testing.T) {
		root, store := newTestStore(t, Options{})
		defer func() { _ = store.Close() }()
		stage, err := store.Begin()
		if err != nil {
			t.Fatal(err)
		}
		receiptPath := filepath.Join(root, generationPath(stage.ID()), receiptFile)
		if err := os.WriteFile(receiptPath, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(receiptPath, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RetentionInventoryStatusV1(context.Background()); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestBuildWorkspaceAdditionalFilesystemRefusals(t *testing.T) {
	t.Run("missing attempt", func(t *testing.T) {
		root := privateBuildWorkspaceRoot(t)
		workspace, err := InitializeBuildWorkspace(context.Background(), root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = workspace.Close() }()
		if _, err := workspace.AttemptRoot(strings.Repeat("f", 32), ServiceJira); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error=%v", err)
		}
		receipt := mustCaptureReceipt(t, validCaptureReceiptInput())
		missingActive := buildWorkspaceActive(strings.Repeat("f", 32), receipt)
		if err := workspace.SaveActive(missingActive); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("missing active attempt error=%v", err)
		}
		invalidActive := missingActive
		invalidActive.SchemaVersion = 99
		if err := workspace.SaveActive(invalidActive); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("invalid active error=%v", err)
		}
		if err := workspace.SaveCaptureReceipt(strings.Repeat("f", 32), receipt); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("missing receipt attempt error=%v", err)
		}
		attemptID, _, err := workspace.BeginAttempt([]Service{ServiceJira})
		if err != nil {
			t.Fatal(err)
		}
		invalidReceipt := receipt
		invalidReceipt.ReceiptDigest = "short"
		if err := workspace.SaveCaptureReceipt(attemptID, invalidReceipt); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("invalid receipt error=%v", err)
		}
		active := buildWorkspaceActive(attemptID, receipt)
		activeBytes, err := CanonicalBuildActive(active, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, buildActiveFileV3), activeBytes, privateFileMode); err != nil {
			t.Fatal(err)
		}
		if _, _, err := workspace.LoadActive(); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("mismatched active marker error=%v", err)
		}
		if err := os.Remove(filepath.Join(root, buildActiveFileV3)); err != nil {
			t.Fatal(err)
		}
		receiptPath := filepath.Join(root, buildReceiptPath(attemptID, ServiceJira))
		if err := os.Mkdir(receiptPath, privateDirMode); err != nil {
			t.Fatal(err)
		}
		if _, _, err := workspace.LoadCaptureReceipt(attemptID, ServiceJira); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("special receipt error=%v", err)
		}
	})

	t.Run("lock hook", func(t *testing.T) {
		root := privateBuildWorkspaceRoot(t)
		workspace, err := InitializeBuildWorkspace(context.Background(), root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if err := workspace.Close(); err != nil {
			t.Fatal(err)
		}
		store, err := Open(root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = store.Close() }()
		store.testHook = failAt("after_build_lock_acquired")
		if unlock, err := lockBuildFile(context.Background(), store); unlock != nil || !errors.Is(err, ErrIntegrity) {
			t.Fatalf("unlock_non_nil=%t error=%v", unlock != nil, err)
		}
	})

	t.Run("v3 downgrade", func(t *testing.T) {
		workspace, err := InitializeBuildWorkspace(context.Background(), privateBuildWorkspaceRoot(t), Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = workspace.Close() }()
		attemptID, _, err := workspace.BeginAttempt([]Service{ServiceJira})
		if err != nil {
			t.Fatal(err)
		}
		active := buildWorkspaceActive(attemptID, mustCaptureReceipt(t, validCaptureReceiptInput()))
		active.SchemaVersion = BuildActiveSchemaV3
		active.PublicationTarget = PublicationTargetWorkspace
		if err := workspace.SaveActive(active); err != nil {
			t.Fatal(err)
		}
		active.SchemaVersion = BuildActiveSchemaV2
		active.PublicationTarget = ""
		if err := workspace.SaveActive(active); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("special recovery temp", func(t *testing.T) {
		root := privateBuildWorkspaceRoot(t)
		workspace, err := InitializeBuildWorkspace(context.Background(), root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if err := workspace.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, buildActiveTemp), privateDirMode); err != nil {
			t.Fatal(err)
		}
		if opened, err := OpenBuildWorkspace(context.Background(), root, Options{}); opened != nil || !errors.Is(err, ErrIntegrity) {
			t.Fatalf("workspace=%#v error=%v", opened, err)
		}
	})

	t.Run("partial initialization", func(t *testing.T) {
		root := privateBuildWorkspaceRoot(t)
		store, err := Initialize(root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = store.Close() }()
		if err := store.root.Mkdir(buildAttemptsDir, privateDirMode); err != nil {
			t.Fatal(err)
		}
		if err := initializeBuildNamespace(store); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error=%v", err)
		}
	})

	if _, _, err := buildActiveTimes(BuildActive{StartedAt: "invalid", Deadline: "invalid"}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("invalid times error=%v", err)
	}
}

func cloneRetentionPlanForTest(plan RetentionPlanV1) RetentionPlanV1 {
	clone := plan
	clone.Inventory = append([]RetentionInventoryRecordV1(nil), plan.Inventory...)
	clone.Protected = append([]RetentionGenerationRefV1(nil), plan.Protected...)
	clone.Candidates = append([]RetentionGenerationRefV1(nil), plan.Candidates...)
	return clone
}

func retentionRecordIndex(plan *RetentionPlanV1, id string) int {
	for index := range plan.Inventory {
		if plan.Inventory[index].GenerationID == id {
			return index
		}
	}
	return -1
}

func limitsWithMembers(limits Limits, members int) Limits {
	limits.MaxMembers = members
	return limits
}

func sealCacheBindingOnlyGeneration(t testing.TB, store *Store, capture CaptureReceipt, binding CacheBindingV1) *Generation {
	t.Helper()
	stage, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	bindingBytes, err := CanonicalCacheBindingV1(binding, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.Add(context.Background(), CacheBindingMemberSpec(), strings.NewReader(string(bindingBytes))); err != nil {
		t.Fatal(err)
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
