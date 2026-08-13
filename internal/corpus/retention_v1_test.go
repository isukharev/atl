package corpus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type retentionTestGeneration struct {
	id     string
	digest string
}

func TestRetentionV1FiniteProtectionPreviewAndApply(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	chain := publishRetentionChain(t, store, 4)
	inactiveFirst := sealRetentionOnly(t, store, "inactive-first", "")
	inactiveSecond := sealRetentionOnly(t, store, "inactive-second", inactiveFirst.digest)
	stage, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}

	plan, err := BuildRetentionPlanV1(context.Background(), store, RetentionPolicyV1{
		SchemaVersion: RetentionPolicySchemaV1, RetainPredecessors: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Protected) != 2 || plan.Protected[0].GenerationID != chain[3].id || plan.Protected[1].GenerationID != chain[2].id ||
		len(plan.Candidates) != 4 || plan.UnsealedStages != 1 {
		t.Fatalf("plan protected=%#v candidates=%d unsealed=%d", plan.Protected, len(plan.Candidates), plan.UnsealedStages)
	}
	wantRootDigest, err := RootIdentityDigest(root)
	if err != nil || plan.RootDigest != wantRootDigest {
		t.Fatalf("root digest=%q want=%q error=%v", plan.RootDigest, wantRootDigest, err)
	}
	canonical, err := CanonicalRetentionPlanV1(plan, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseRetentionPlanV1(canonical, Limits{})
	if err != nil || parsed.PlanDigest != plan.PlanDigest {
		t.Fatalf("parsed digest=%q error=%v", parsed.PlanDigest, err)
	}
	statusBytes, err := marshalCanonical(plan.Status())
	if err != nil {
		t.Fatal(err)
	}
	if bytesContainAny(statusBytes, chain[3].id, chain[3].digest) {
		t.Fatalf("status leaked a private identity: %s", statusBytes)
	}
	otherRoot := t.TempDir()
	if err := os.Chmod(otherRoot, privateDirMode); err != nil {
		t.Fatal(err)
	}
	wrongRoot := plan
	wrongRoot.RootDigest, err = RootIdentityDigest(otherRoot)
	if err != nil {
		t.Fatal(err)
	}
	wrongRoot.PlanDigest, err = retentionPlanDigest(wrongRoot)
	if err != nil {
		t.Fatal(err)
	}
	wrongCanonical, err := CanonicalRetentionPlanV1(wrongRoot, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRetentionPlanV1(context.Background(), wrongCanonical, wrongRoot.PlanDigest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("cross-root apply error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, generationsDir, plan.Candidates[0].GenerationID)); err != nil {
		t.Fatalf("root-mismatched plan removed a candidate: %v", err)
	}
	status, err := store.ApplyRetentionPlanV1(context.Background(), canonical, plan.PlanDigest)
	if err != nil || !status.Complete || status.RemovedThisApply != 4 || status.UnsealedStages != 1 {
		t.Fatalf("status=%#v err=%v", status, err)
	}
	for _, keep := range chain[2:] {
		if _, err := os.Stat(filepath.Join(root, generationsDir, keep.id)); err != nil {
			t.Fatalf("protected generation missing: %v", err)
		}
	}
	for _, remove := range append(chain[:2], inactiveFirst, inactiveSecond) {
		if _, err := os.Stat(filepath.Join(root, generationsDir, remove.id)); !os.IsNotExist(err) {
			t.Fatalf("candidate survived: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, generationsDir, stage.ID())); err != nil {
		t.Fatalf("unsealed failed stage was removed: %v", err)
	}
	next, err := BuildRetentionPlanV1(context.Background(), store, RetentionPolicyV1{
		SchemaVersion: RetentionPolicySchemaV1, RetainPredecessors: 1,
	})
	if err != nil || len(next.Protected) != 2 || len(next.Candidates) != 0 || next.UnsealedStages != 1 {
		t.Fatalf("post-apply plan=%#v err=%v", next, err)
	}
}

func TestRetentionInventoryStatusV1WithoutCurrent(t *testing.T) {
	_, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	sealRetentionOnly(t, store, "sealed", "")
	if _, err := store.Begin(); err != nil {
		t.Fatal(err)
	}
	status, err := store.RetentionInventoryStatusV1(context.Background())
	if err != nil || status.SchemaVersion != RetentionStatusSchemaV1 || status.SealedGenerations != 1 ||
		status.UnsealedStages != 1 || !status.Complete || status.ProtectedGenerations != 0 || status.CandidateGenerations != 0 {
		t.Fatalf("status=%#v err=%v", status, err)
	}
}

func TestRetentionV1ProtectsExactCurrentDeltaPredecessor(t *testing.T) {
	_, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	predecessor := publishRetentionChain(t, store, 1)[0]
	successor := sealRetentionDelta(t, store, predecessor, predecessor.id)
	if _, err := store.Publish(context.Background(), successor.id); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildRetentionPlanV1(context.Background(), store, RetentionPolicyV1{
		SchemaVersion: RetentionPolicySchemaV1, RetainPredecessors: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Protected) != 2 || plan.Protected[1].GenerationID != predecessor.id {
		t.Fatalf("protected=%#v", plan.Protected)
	}
	var current RetentionInventoryRecordV1
	for _, record := range plan.Inventory {
		if record.GenerationID == successor.id {
			current = record
		}
	}
	if current.PredecessorGenerationID != predecessor.id || current.PredecessorGenerationDigest != predecessor.digest {
		t.Fatalf("current record=%#v", current)
	}
}

func TestRetentionV1RejectsWrongCurrentDeltaPredecessorID(t *testing.T) {
	_, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	predecessor := publishRetentionChain(t, store, 1)[0]
	successor := sealRetentionDelta(t, store, predecessor, strings.Repeat("d", 32))
	if _, err := store.Publish(context.Background(), successor.id); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRetentionPlanV1(context.Background(), store, RetentionPolicyV1{
		SchemaVersion: RetentionPolicySchemaV1, RetainPredecessors: 1,
	}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("error=%v", err)
	}
}

func TestRetentionV1ApplyRejectsInventoryDrift(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, root string, store *Store, plan RetentionPlanV1){
		"new sealed": func(t *testing.T, _ string, store *Store, _ RetentionPlanV1) {
			sealRetentionOnly(t, store, "new", "")
		},
		"new unsealed": func(t *testing.T, _ string, store *Store, _ RetentionPlanV1) {
			if _, err := store.Begin(); err != nil {
				t.Fatal(err)
			}
		},
		"missing protected": func(t *testing.T, root string, _ *Store, plan RetentionPlanV1) {
			if err := os.RemoveAll(filepath.Join(root, generationsDir, plan.Protected[1].GenerationID)); err != nil {
				t.Fatal(err)
			}
		},
		"changed candidate": func(t *testing.T, root string, _ *Store, plan RetentionPlanV1) {
			candidate := plan.Candidates[0].GenerationID
			if err := os.WriteFile(filepath.Join(root, generationsDir, candidate, artifactsDir, "item"), []byte("tampered"), privateFileMode); err != nil {
				t.Fatal(err)
			}
		},
		"changed protected": func(t *testing.T, root string, _ *Store, plan RetentionPlanV1) {
			protected := plan.Protected[1].GenerationID
			if err := os.WriteFile(filepath.Join(root, generationsDir, protected, artifactsDir, "item"), []byte("tampered"), privateFileMode); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, store := newTestStore(t, Options{})
			defer func() { _ = store.Close() }()
			publishRetentionChain(t, store, 3)
			plan, err := BuildRetentionPlanV1(context.Background(), store, RetentionPolicyV1{
				SchemaVersion: RetentionPolicySchemaV1, RetainPredecessors: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := CanonicalRetentionPlanV1(plan, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, root, store, plan)
			if _, err := store.ApplyRetentionPlanV1(context.Background(), canonical, plan.PlanDigest); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestRetentionV1ApplyAllowsMissingCandidateSubsetAndPartialResume(t *testing.T) {
	t.Run("missing subset", func(t *testing.T) {
		root, store := newTestStore(t, Options{})
		defer func() { _ = store.Close() }()
		publishRetentionChain(t, store, 4)
		plan, canonical := retentionPlanBytes(t, store, 1)
		if err := os.RemoveAll(filepath.Join(root, generationsDir, plan.Candidates[0].GenerationID)); err != nil {
			t.Fatal(err)
		}
		status, err := store.ApplyRetentionPlanV1(context.Background(), canonical, plan.PlanDigest)
		if err != nil || status.RemovedThisApply != len(plan.Candidates)-1 || status.DeletedCandidates != len(plan.Candidates) {
			t.Fatalf("status=%#v err=%v", status, err)
		}
	})

	t.Run("partial resume", func(t *testing.T) {
		_, store := newTestStore(t, Options{})
		defer func() { _ = store.Close() }()
		publishRetentionChain(t, store, 4)
		plan, canonical := retentionPlanBytes(t, store, 1)
		store.testHook = failAt("after_retention_remove")
		if _, err := store.ApplyRetentionPlanV1(context.Background(), canonical, plan.PlanDigest); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("partial error=%v", err)
		}
		store.testHook = nil
		status, err := store.ApplyRetentionPlanV1(context.Background(), canonical, plan.PlanDigest)
		if err != nil || !status.Complete || status.RemovedThisApply != len(plan.Candidates)-1 {
			t.Fatalf("resume status=%#v err=%v", status, err)
		}
	})
}

func TestRetentionV1RemovalCannotFollowCandidatePathExchange(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	publishRetentionChain(t, store, 3)
	plan, canonical := retentionPlanBytes(t, store, 1)
	candidate := plan.Candidates[0].GenerationID
	protected := plan.Protected[0].GenerationID
	protectedMember := filepath.Join(root, generationsDir, protected, artifactsDir, "item")
	want, err := os.ReadFile(protectedMember)
	if err != nil {
		t.Fatal(err)
	}
	exchanged := false
	store.testHook = func(step string) error {
		if step != "after_retention_namespace_check" || exchanged {
			return nil
		}
		exchanged = true
		candidatePath := filepath.Join(root, generationsDir, candidate)
		movedPath := filepath.Join(root, ".moved-candidate")
		if err := os.Rename(candidatePath, movedPath); err != nil {
			return err
		}
		return os.Symlink(filepath.Join(root, generationsDir, protected), candidatePath)
	}
	if _, err := store.ApplyRetentionPlanV1(context.Background(), canonical, plan.PlanDigest); !errors.Is(err, ErrIntegrity) || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("exchange error=%v", err)
	}
	store.testHook = nil
	got, err := os.ReadFile(protectedMember)
	if err != nil || string(got) != string(want) {
		t.Fatalf("protected bytes=%q want=%q err=%v", got, want, err)
	}
}

func TestRetentionV1QuarantineResumesPartiallyPurgedCandidate(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	publishRetentionChain(t, store, 4)
	plan, canonical := retentionPlanBytes(t, store, 1)
	store.testHook = failAt("after_retention_remove")
	if _, err := store.ApplyRetentionPlanV1(context.Background(), canonical, plan.PlanDigest); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("quarantine interruption error=%v", err)
	}
	store.testHook = nil

	quarantined := retentionQuarantineGenerationPath(plan.PlanDigest, plan.Candidates[0].GenerationID)
	if err := os.RemoveAll(filepath.Join(root, quarantined, artifactsDir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, quarantined, manifestFile)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	store = reopened
	if _, err := store.Begin(); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("new stage during quarantine error=%v", err)
	}
	if _, err := store.Publish(context.Background(), plan.Current.GenerationID); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("publication during quarantine error=%v", err)
	}
	if _, err := store.RetentionInventoryStatusV1(context.Background()); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("status during quarantine error=%v", err)
	}

	status, err := store.ApplyRetentionPlanV1(context.Background(), canonical, plan.PlanDigest)
	if err != nil || !status.Complete || status.RemovedThisApply != len(plan.Candidates)-1 || status.RemainingCandidates != 0 {
		t.Fatalf("resume status=%#v error=%v", status, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, generationsDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), retentionQuarantinePrefix) {
			t.Fatalf("quarantine survived successful resume: %q", entry.Name())
		}
	}
	for _, candidate := range plan.Candidates {
		if _, err := os.Lstat(filepath.Join(root, generationPath(candidate.GenerationID))); !os.IsNotExist(err) {
			t.Fatalf("candidate survived successful resume: %v", err)
		}
	}
	for _, protected := range plan.Protected {
		if _, err := os.Stat(filepath.Join(root, generationPath(protected.GenerationID))); err != nil {
			t.Fatalf("protected generation missing after resume: %v", err)
		}
	}
}

func TestRetentionV1QuarantineResumesEveryDurabilityBoundary(t *testing.T) {
	for _, step := range []string{
		"after_retention_quarantine_rename",
		"after_retention_quarantine_sync",
		"before_retention_quarantine_purge",
		"after_retention_quarantine_purge",
		"after_retention_quarantine_unlink",
	} {
		t.Run(step, func(t *testing.T) {
			root, store := newTestStore(t, Options{})
			publishRetentionChain(t, store, 3)
			plan, canonical := retentionPlanBytes(t, store, 1)
			store.testHook = failAt(step)
			if _, err := store.ApplyRetentionPlanV1(context.Background(), canonical, plan.PlanDigest); !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("interruption error=%v", err)
			}
			store.testHook = nil
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := Open(root, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			status, err := store.ApplyRetentionPlanV1(context.Background(), canonical, plan.PlanDigest)
			if err != nil || !status.Complete || status.RemainingCandidates != 0 {
				t.Fatalf("resume status=%#v error=%v", status, err)
			}
			for _, protected := range plan.Protected {
				if _, err := os.Stat(filepath.Join(root, generationPath(protected.GenerationID))); err != nil {
					t.Fatalf("protected generation missing after resume: %v", err)
				}
			}
		})
	}
}

func TestRetentionV1RejectsConflictingQuarantineState(t *testing.T) {
	tests := map[string]func(string, RetentionPlanV1) error{
		"simultaneous live and quarantine": func(root string, plan RetentionPlanV1) error {
			candidate := plan.Candidates[0].GenerationID
			source := filepath.Join(root, generationPath(candidate))
			target := filepath.Join(root, retentionQuarantineGenerationPath(plan.PlanDigest, candidate))
			if err := os.Rename(source, target); err != nil {
				return err
			}
			return os.Mkdir(source, privateDirMode)
		},
		"foreign plan": func(root string, plan RetentionPlanV1) error {
			candidate := plan.Candidates[0].GenerationID
			foreign := strings.Repeat("a", 64)
			if foreign == plan.PlanDigest {
				foreign = strings.Repeat("b", 64)
			}
			return os.Rename(
				filepath.Join(root, generationPath(candidate)),
				filepath.Join(root, retentionQuarantineGenerationPath(foreign, candidate)),
			)
		},
		"malformed quarantine name": func(root string, plan RetentionPlanV1) error {
			return os.Rename(
				filepath.Join(root, generationPath(plan.Candidates[0].GenerationID)),
				filepath.Join(root, generationsDir, retentionQuarantinePrefix+"malformed"),
			)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root, store := newTestStore(t, Options{})
			defer func() { _ = store.Close() }()
			publishRetentionChain(t, store, 3)
			plan, canonical := retentionPlanBytes(t, store, 1)
			if err := mutate(root, plan); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ApplyRetentionPlanV1(context.Background(), canonical, plan.PlanDigest); !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("conflicting quarantine error=%v", err)
			}
			for _, protected := range plan.Protected {
				if _, err := os.Stat(filepath.Join(root, generationPath(protected.GenerationID))); err != nil {
					t.Fatalf("protected generation changed: %v", err)
				}
			}
		})
	}
}

func TestRetentionV1FutureQuarantineNamespaceBlocksLifecycle(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	current := publishRetentionChain(t, store, 1)[0]
	stage, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.Add(context.Background(), MemberSpec{
		Service: ServiceJira, StableID: "future-quarantine-test", Role: RoleNative, Path: "item",
	}, strings.NewReader("body")); err != nil {
		t.Fatal(err)
	}
	future := retentionQuarantineFamilyPrefix + ".v2-" + strings.Repeat("a", 64) + "-" + strings.Repeat("b", 32)
	if err := os.Mkdir(filepath.Join(root, generationsDir, future), privateDirMode); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin(); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("begin error=%v", err)
	}
	if _, err := stage.Seal(context.Background(), sealOptions(current.digest, ServiceJira)); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("seal error=%v", err)
	}
	if _, err := store.Publish(context.Background(), current.id); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("publish error=%v", err)
	}
	if _, err := store.RetentionInventoryStatusV1(context.Background()); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("status error=%v", err)
	}
	if _, err := BuildRetentionPlanV1(context.Background(), store, RetentionPolicyV1{
		SchemaVersion: RetentionPolicySchemaV1, RetainPredecessors: 1,
	}); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("preview error=%v", err)
	}
}

func TestRetentionV1DetectsNewInventoryAtLastPredeleteBoundary(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	publishRetentionChain(t, store, 3)
	plan, canonical := retentionPlanBytes(t, store, 1)
	candidate := plan.Candidates[0].GenerationID
	injected := false
	store.testHook = func(step string) error {
		if step != "before_retention_remove" || injected {
			return nil
		}
		injected = true
		return os.Mkdir(filepath.Join(root, generationsDir, strings.Repeat("e", 32)), privateDirMode)
	}
	if _, err := store.ApplyRetentionPlanV1(context.Background(), canonical, plan.PlanDigest); !errors.Is(err, ErrIntegrity) || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("new inventory error=%v", err)
	}
	store.testHook = nil
	if _, err := os.Stat(filepath.Join(root, generationsDir, candidate)); err != nil {
		t.Fatalf("candidate removed before drift rejection: %v", err)
	}
}

func TestRetentionV1RejectsDuplicateDigestSpecialEntryAndPlanTamper(t *testing.T) {
	t.Run("duplicate digest", func(t *testing.T) {
		root, store := newTestStore(t, Options{})
		defer func() { _ = store.Close() }()
		current := publishRetentionChain(t, store, 1)[0]
		copyRetentionGeneration(t, root, current.id, strings.Repeat("e", 32))
		if _, err := BuildRetentionPlanV1(context.Background(), store, RetentionPolicyV1{
			SchemaVersion: RetentionPolicySchemaV1, RetainPredecessors: 1,
		}); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("special entry", func(t *testing.T) {
		root, store := newTestStore(t, Options{})
		defer func() { _ = store.Close() }()
		publishRetentionChain(t, store, 1)
		if err := os.Symlink(root, filepath.Join(root, generationsDir, strings.Repeat("f", 32))); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildRetentionPlanV1(context.Background(), store, RetentionPolicyV1{
			SchemaVersion: RetentionPolicySchemaV1, RetainPredecessors: 1,
		}); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("plan tamper", func(t *testing.T) {
		_, store := newTestStore(t, Options{})
		defer func() { _ = store.Close() }()
		publishRetentionChain(t, store, 3)
		plan, canonical := retentionPlanBytes(t, store, 1)
		tampered := plan
		tampered.Policy.RetainPredecessors++
		if _, err := CanonicalRetentionPlanV1(tampered, Limits{}); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("canonical tamper error=%v", err)
		}
		if _, err := store.ApplyRetentionPlanV1(context.Background(), canonical, digestByte('9')); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("expected digest error=%v", err)
		}
	})
}

func publishRetentionChain(t *testing.T, store *Store, count int) []retentionTestGeneration {
	t.Helper()
	chain := make([]retentionTestGeneration, 0, count)
	predecessor := ""
	for index := range count {
		stage, generation := sealTestGeneration(t, store, "body-"+string(rune('a'+index)), predecessor)
		entry := retentionTestGeneration{id: stage.ID(), digest: generation.Receipt().GenerationDigest}
		if _, err := store.Publish(context.Background(), entry.id); err != nil {
			t.Fatal(err)
		}
		if err := generation.Close(); err != nil {
			t.Fatal(err)
		}
		chain = append(chain, entry)
		predecessor = entry.digest
	}
	return chain
}

func sealRetentionOnly(t *testing.T, store *Store, body, predecessor string) retentionTestGeneration {
	t.Helper()
	stage, generation := sealTestGeneration(t, store, body, predecessor)
	entry := retentionTestGeneration{id: stage.ID(), digest: generation.Receipt().GenerationDigest}
	if err := generation.Close(); err != nil {
		t.Fatal(err)
	}
	return entry
}

func sealRetentionDelta(t *testing.T, store *Store, predecessor retentionTestGeneration, recordedID string) retentionTestGeneration {
	t.Helper()
	delta := GenerationDelta{
		SchemaVersion:           GenerationDeltaSchemaV1,
		PredecessorGenerationID: recordedID, PredecessorGenerationDigest: predecessor.digest,
		PredecessorProjectionDigest: digestByte('1'), SuccessorProjectionDigest: digestByte('2'),
		Bindings: []GenerationDeltaBinding{{
			Service: ServiceConfluence, ReceiptSchema: CaptureReceiptSchemaV1,
			ScopeDigest: digestByte('3'), SelectorDigest: digestByte('4'), OptionsDigest: digestByte('5'),
		}},
		Records: []GenerationDeltaRecord{}, Counts: GenerationDeltaCounts{},
	}
	deltaBytes, err := CanonicalGenerationDelta(delta, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(deltaBytes)
	stage, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []struct {
		spec MemberSpec
		body string
	}{
		{MemberSpec{Service: ServiceConfluence, StableID: "page", Role: RoleNative, Path: "item"}, "body"},
		{MemberSpec{Service: ServiceConfluence, StableID: "generation-delta-v1", Role: RoleTombstone, Path: "projection/confluence/generation-delta.v1.json"}, string(deltaBytes)},
	} {
		if err := stage.Add(context.Background(), member.spec, strings.NewReader(member.body)); err != nil {
			t.Fatal(err)
		}
	}
	opts := sealOptions(predecessor.digest, ServiceConfluence)
	opts.TombstoneDigest = hex.EncodeToString(sum[:])
	generation, err := stage.Seal(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	entry := retentionTestGeneration{id: stage.ID(), digest: generation.Receipt().GenerationDigest}
	if err := generation.Close(); err != nil {
		t.Fatal(err)
	}
	return entry
}

func retentionPlanBytes(t testing.TB, store *Store, predecessors int) (RetentionPlanV1, []byte) {
	t.Helper()
	plan, err := BuildRetentionPlanV1(context.Background(), store, RetentionPolicyV1{
		SchemaVersion: RetentionPolicySchemaV1, RetainPredecessors: predecessors,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalRetentionPlanV1(plan, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return plan, canonical
}

func copyRetentionGeneration(t testing.TB, root, sourceID, targetID string) {
	t.Helper()
	source := filepath.Join(root, generationsDir, sourceID)
	target := filepath.Join(root, generationsDir, targetID)
	if err := os.Mkdir(target, privateDirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(target, artifactsDir), privateDirMode); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{manifestFile, receiptFile, filepath.Join(artifactsDir, "item")} {
		data, err := os.ReadFile(filepath.Join(source, rel))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, rel), data, privateFileMode); err != nil {
			t.Fatal(err)
		}
	}
}

func bytesContainAny(data []byte, values ...string) bool {
	for _, value := range values {
		if strings.Contains(string(data), value) {
			return true
		}
	}
	return false
}
