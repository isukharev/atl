//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package corpus

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStoreSealPublishSelectLifecycle(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()

	stage, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	inputs := []struct {
		spec MemberSpec
		body string
	}{
		{MemberSpec{Service: ServiceJira, StableID: "issue-2", Role: RoleDocument, Path: "jira/second.json"}, "second"},
		{MemberSpec{Service: ServiceConfluence, StableID: "page-1", Role: RoleNative, Path: "confluence/first.csf"}, "first"},
	}
	for _, input := range inputs {
		if err := stage.Add(context.Background(), input.spec, strings.NewReader(input.body)); err != nil {
			t.Fatal(err)
		}
	}
	generation, err := stage.Seal(context.Background(), sealOptions("", ServiceJira, ServiceConfluence))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = generation.Close() }()
	manifest := generation.Manifest()
	if len(manifest.Members) != 2 || manifest.Members[0].Service != ServiceConfluence || manifest.Members[1].Service != ServiceJira {
		t.Fatalf("members are not in canonical tuple order: %+v", manifest.Members)
	}
	if manifest.Totals != (Totals{Members: 2, Bytes: 11}) {
		t.Fatalf("totals = %+v", manifest.Totals)
	}
	if info, err := os.Stat(filepath.Join(root, generationsDir, stage.ID(), manifestFile)); err != nil || info.Mode().Perm() != privateFileMode {
		t.Fatalf("manifest mode: info=%v err=%v", info, err)
	}
	if info, err := os.Stat(filepath.Join(root, generationsDir, stage.ID(), receiptFile)); err != nil || info.Mode().Perm() != privateFileMode {
		t.Fatalf("receipt mode: info=%v err=%v", info, err)
	}

	summary, err := store.Publish(context.Background(), stage.ID())
	if err != nil {
		t.Fatal(err)
	}
	if summary.GenerationDigest != generation.Receipt().GenerationDigest || summary.Totals != manifest.Totals {
		t.Fatalf("summary = %+v", summary)
	}
	selected, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = selected.Close() }()
	var copied bytes.Buffer
	count, err := selected.CopyMember(context.Background(), ServiceConfluence, "page-1", RoleNative, &copied)
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 || copied.String() != "first" {
		t.Fatalf("copy = %d %q", count, copied.String())
	}
	if err := stage.Add(context.Background(), inputs[0].spec, strings.NewReader("again")); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("add after seal error = %v", err)
	}
	if _, err := stage.Seal(context.Background(), sealOptions("", ServiceJira, ServiceConfluence)); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("reseal error = %v", err)
	}
}

func TestInitializeRequiresAnEmptyOwnerOnlyTrustRoot(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{name: "loose mode", setup: func(t *testing.T, root string) {
			t.Helper()
			if err := os.Chmod(root, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "nonempty", setup: func(t *testing.T, root string) {
			t.Helper()
			writePrivate(t, filepath.Join(root, "evidence"), []byte("preserve"))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "store")
			if err := os.Mkdir(root, privateDirMode); err != nil {
				t.Fatal(err)
			}
			test.setup(t, root)
			if _, err := Initialize(root, Options{}); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("initialize error = %v", err)
			}
		})
	}
}

func TestAddEnforcesAggregateBoundsAndPoisonsFailedStage(t *testing.T) {
	root, store := newTestStore(t, Options{Limits: Limits{
		MaxMembers: 2, MaxMemberBytes: 4, MaxTotalBytes: 5,
	}})
	defer func() { _ = store.Close() }()
	stage, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.Add(context.Background(), MemberSpec{Service: ServiceJira, StableID: "one", Role: RoleNative, Path: "one"}, strings.NewReader("123")); err != nil {
		t.Fatal(err)
	}
	if err := stage.Add(context.Background(), MemberSpec{Service: ServiceJira, StableID: "two", Role: RoleNative, Path: "two"}, strings.NewReader("456")); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("aggregate overflow error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, generationsDir, stage.ID(), artifactsDir, "two")); !os.IsNotExist(err) {
		t.Fatalf("overflow member survived: %v", err)
	}
	if _, err := stage.Seal(context.Background(), sealOptions("", ServiceJira)); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("failed stage sealed: %v", err)
	}
}

func TestAddRejectsCaseAliasedPaths(t *testing.T) {
	_, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	stage, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.Add(context.Background(), MemberSpec{Service: ServiceJira, StableID: "one", Role: RoleNative, Path: "Data/Item"}, strings.NewReader("one")); err != nil {
		t.Fatal(err)
	}
	if err := stage.Add(context.Background(), MemberSpec{Service: ServiceJira, StableID: "two", Role: RoleNative, Path: "data/item"}, strings.NewReader("two")); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("case alias error = %v", err)
	}
}

func TestGenerationBytesAndDigestsAreDeterministic(t *testing.T) {
	makeGeneration := func(t *testing.T) (Manifest, Receipt, []byte, []byte) {
		t.Helper()
		root, store := newTestStore(t, Options{})
		defer func() { _ = store.Close() }()
		stage, err := store.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := stage.Add(context.Background(), MemberSpec{Service: ServiceJira, StableID: "42", Role: RoleMetadata, Path: "records/42.json"}, strings.NewReader("{}\n")); err != nil {
			t.Fatal(err)
		}
		generation, err := stage.Seal(context.Background(), sealOptions("", ServiceJira))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = generation.Close() }()
		manifestBytes, err := os.ReadFile(filepath.Join(root, generationsDir, stage.ID(), manifestFile))
		if err != nil {
			t.Fatal(err)
		}
		receiptBytes, err := os.ReadFile(filepath.Join(root, generationsDir, stage.ID(), receiptFile))
		if err != nil {
			t.Fatal(err)
		}
		return generation.Manifest(), generation.Receipt(), manifestBytes, receiptBytes
	}
	manifestA, receiptA, manifestBytesA, receiptBytesA := makeGeneration(t)
	manifestB, receiptB, manifestBytesB, receiptBytesB := makeGeneration(t)
	if !bytes.Equal(manifestBytesA, manifestBytesB) || !bytes.Equal(receiptBytesA, receiptBytesB) {
		t.Fatalf("canonical bytes differ:\n%s\n%s", manifestBytesA, manifestBytesB)
	}
	if manifestA.Totals != manifestB.Totals || receiptA.GenerationDigest != receiptB.GenerationDigest {
		t.Fatalf("deterministic digests differ: %s / %s", receiptA.GenerationDigest, receiptB.GenerationDigest)
	}
}

func TestSealRejectsExactTreeAdversaries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string, stage *Stage, member string)
	}{
		{name: "extra file", mutate: func(t *testing.T, root string, stage *Stage, _ string) {
			t.Helper()
			writePrivate(t, filepath.Join(root, generationsDir, stage.ID(), artifactsDir, "extra"), []byte("x"))
		}},
		{name: "extra empty directory", mutate: func(t *testing.T, root string, stage *Stage, _ string) {
			t.Helper()
			if err := os.Mkdir(filepath.Join(root, generationsDir, stage.ID(), artifactsDir, "extra"), privateDirMode); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", mutate: func(t *testing.T, root string, stage *Stage, _ string) {
			t.Helper()
			if err := os.Symlink("item.bin", filepath.Join(root, generationsDir, stage.ID(), artifactsDir, "link")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", mutate: func(t *testing.T, root string, stage *Stage, member string) {
			t.Helper()
			if err := os.Link(member, filepath.Join(root, generationsDir, stage.ID(), artifactsDir, "linked")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unsafe file mode", mutate: func(t *testing.T, _ string, _ *Stage, member string) {
			t.Helper()
			if err := os.Chmod(member, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unsafe directory mode", mutate: func(t *testing.T, root string, stage *Stage, _ string) {
			t.Helper()
			if err := os.Chmod(filepath.Join(root, generationsDir, stage.ID(), artifactsDir), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, store := newTestStore(t, Options{})
			defer func() { _ = store.Close() }()
			stage, err := store.Begin()
			if err != nil {
				t.Fatal(err)
			}
			spec := MemberSpec{Service: ServiceJira, StableID: "one", Role: RoleNative, Path: "item.bin"}
			if err := stage.Add(context.Background(), spec, strings.NewReader("payload")); err != nil {
				t.Fatal(err)
			}
			member := filepath.Join(root, generationsDir, stage.ID(), artifactsDir, "item.bin")
			test.mutate(t, root, stage, member)
			if _, err := stage.Seal(context.Background(), sealOptions("", ServiceJira)); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("seal error = %v", err)
			}
			if _, err := store.SelectCurrent(context.Background()); !errors.Is(err, ErrNoCurrent) {
				t.Fatalf("failed stage became current: %v", err)
			}
		})
	}
}

func TestVerifyRejectsMissingSealMetadata(t *testing.T) {
	for _, name := range []string{manifestFile, receiptFile} {
		t.Run(name, func(t *testing.T) {
			root, store := newTestStore(t, Options{})
			defer func() { _ = store.Close() }()
			stage, generation := sealTestGeneration(t, store, "payload", "")
			_ = generation.Close()
			if err := os.Remove(filepath.Join(root, generationsDir, stage.ID(), name)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Verify(context.Background(), stage.ID()); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("verify error = %v", err)
			}
		})
	}
}

func TestSelectionRejectsPointerLinksAndLooseModes(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, root string)
	}{
		{name: "symlink", mutate: func(t *testing.T, root string) {
			t.Helper()
			pointer := filepath.Join(root, pointerFile)
			if err := os.Rename(pointer, pointer+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(pointerFile+".saved", pointer); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "loose mode", mutate: func(t *testing.T, root string) {
			t.Helper()
			if err := os.Chmod(filepath.Join(root, pointerFile), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, store := newTestStore(t, Options{})
			defer func() { _ = store.Close() }()
			stage, generation := sealTestGeneration(t, store, "payload", "")
			defer func() { _ = generation.Close() }()
			if _, err := store.Publish(context.Background(), stage.ID()); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, root)
			if _, err := store.SelectCurrent(context.Background()); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("select error = %v", err)
			}
		})
	}
}

func TestSealDetectsDriftAcrossBothInventoryBoundaries(t *testing.T) {
	for _, step := range []string{"before_second_inventory", "after_second_inventory"} {
		t.Run(step, func(t *testing.T) {
			root, store := newTestStore(t, Options{})
			defer func() { _ = store.Close() }()
			stage, err := store.Begin()
			if err != nil {
				t.Fatal(err)
			}
			spec := MemberSpec{Service: ServiceJira, StableID: "one", Role: RoleNative, Path: "item.bin"}
			if err := stage.Add(context.Background(), spec, strings.NewReader("original")); err != nil {
				t.Fatal(err)
			}
			member := filepath.Join(root, generationsDir, stage.ID(), artifactsDir, "item.bin")
			store.testHook = func(current string) error {
				if current == step {
					return os.WriteFile(member, []byte("modified"), privateFileMode)
				}
				return nil
			}
			if _, err := stage.Seal(context.Background(), sealOptions("", ServiceJira)); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("seal error = %v", err)
			}
			store.testHook = nil
			if _, err := store.SelectCurrent(context.Background()); !errors.Is(err, ErrNoCurrent) {
				t.Fatalf("drifted stage became current: %v", err)
			}
		})
	}
}

func TestReceiptAndPointerFaultsRemainRecoverable(t *testing.T) {
	t.Run("receipt link", func(t *testing.T) {
		root, store := newTestStore(t, Options{})
		defer func() { _ = store.Close() }()
		stage, err := store.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := stage.Add(context.Background(), MemberSpec{Service: ServiceJira, StableID: "one", Role: RoleNative, Path: "item"}, strings.NewReader("data")); err != nil {
			t.Fatal(err)
		}
		store.testHook = func(step string) error {
			if step == "after_receipt_link" {
				return errors.New("injected")
			}
			return nil
		}
		if _, err := stage.Seal(context.Background(), sealOptions("", ServiceJira)); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("seal error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, generationsDir, stage.ID(), receiptFile)); err != nil {
			t.Fatalf("receipt evidence was removed: %v", err)
		}
		if _, err := store.SelectCurrent(context.Background()); !errors.Is(err, ErrNoCurrent) {
			t.Fatalf("ambiguous seal became current: %v", err)
		}
	})

	t.Run("pointer rename", func(t *testing.T) {
		_, store := newTestStore(t, Options{})
		defer func() { _ = store.Close() }()
		stage, generation := sealTestGeneration(t, store, "data", "")
		defer func() { _ = generation.Close() }()
		fired := false
		store.testHook = func(step string) error {
			if step == "after_pointer_rename" && !fired {
				fired = true
				return errors.New("injected")
			}
			return nil
		}
		if _, err := store.Publish(context.Background(), stage.ID()); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("publish error = %v", err)
		}
		store.testHook = nil
		if _, err := store.Publish(context.Background(), stage.ID()); err != nil {
			t.Fatalf("idempotent recovery failed: %v", err)
		}
		selected, err := store.SelectCurrent(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_ = selected.Close()
	})
}

func TestInjectedDurabilityBoundariesNeverSelectPartialBytes(t *testing.T) {
	t.Run("member", func(t *testing.T) {
		for _, step := range []string{"after_member_link", "after_member_sync"} {
			t.Run(step, func(t *testing.T) {
				_, store := newTestStore(t, Options{})
				defer func() { _ = store.Close() }()
				stage, err := store.Begin()
				if err != nil {
					t.Fatal(err)
				}
				store.testHook = failAt(step)
				err = stage.Add(context.Background(), MemberSpec{Service: ServiceJira, StableID: "one", Role: RoleNative, Path: "item"}, strings.NewReader("data"))
				if !errors.Is(err, ErrIntegrity) {
					t.Fatalf("add error = %v", err)
				}
				if _, err := store.SelectCurrent(context.Background()); !errors.Is(err, ErrNoCurrent) {
					t.Fatalf("member fault became current: %v", err)
				}
			})
		}
	})

	t.Run("seal", func(t *testing.T) {
		for _, step := range []string{
			"after_manifest_link", "after_manifest_sync", "before_second_inventory", "after_second_inventory",
			"before_receipt_link", "after_receipt_link", "after_receipt_sync",
		} {
			t.Run(step, func(t *testing.T) {
				_, store := newTestStore(t, Options{})
				defer func() { _ = store.Close() }()
				stage, err := store.Begin()
				if err != nil {
					t.Fatal(err)
				}
				if err := stage.Add(context.Background(), MemberSpec{Service: ServiceJira, StableID: "one", Role: RoleNative, Path: "item"}, strings.NewReader("data")); err != nil {
					t.Fatal(err)
				}
				store.testHook = failAt(step)
				_, err = stage.Seal(context.Background(), sealOptions("", ServiceJira))
				if step == "after_receipt_link" || step == "after_receipt_sync" {
					if !errors.Is(err, ErrOutcomeUnknown) {
						t.Fatalf("seal error = %v", err)
					}
				} else if !errors.Is(err, ErrIntegrity) {
					t.Fatalf("seal error = %v", err)
				}
				if _, err := store.SelectCurrent(context.Background()); !errors.Is(err, ErrNoCurrent) {
					t.Fatalf("seal fault became current: %v", err)
				}
			})
		}
	})

	t.Run("pointer", func(t *testing.T) {
		for _, step := range []string{"before_pointer_write", "after_pointer_temp_sync", "after_pointer_rename", "after_pointer_sync"} {
			t.Run(step, func(t *testing.T) {
				_, store := newTestStore(t, Options{})
				defer func() { _ = store.Close() }()
				stage, generation := sealTestGeneration(t, store, "data", "")
				defer func() { _ = generation.Close() }()
				store.testHook = failAt(step)
				_, err := store.Publish(context.Background(), stage.ID())
				renamed := step == "after_pointer_rename" || step == "after_pointer_sync"
				if renamed {
					if !errors.Is(err, ErrOutcomeUnknown) {
						t.Fatalf("publish error = %v", err)
					}
					store.testHook = nil
					selected, selectErr := store.SelectCurrent(context.Background())
					if selectErr != nil {
						t.Fatalf("renamed pointer is not fully valid: %v", selectErr)
					}
					_ = selected.Close()
				} else {
					if !errors.Is(err, ErrIntegrity) {
						t.Fatalf("publish error = %v", err)
					}
					if _, selectErr := store.SelectCurrent(context.Background()); !errors.Is(selectErr, ErrNoCurrent) {
						t.Fatalf("pre-rename fault became current: %v", selectErr)
					}
				}
			})
		}
	})
}

func TestPointerCASDetectsAnUncoordinatedSelectionChange(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	targetStage, target := sealTestGeneration(t, store, "target", "")
	defer func() { _ = target.Close() }()
	otherStage, other := sealTestGeneration(t, store, "other", "")
	defer func() { _ = other.Close() }()
	otherPointer, err := canonicalPointer(Pointer{
		SchemaVersion: PointerSchemaV1, GenerationID: otherStage.ID(), GenerationDigest: other.Receipt().GenerationDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	fired := false
	store.testHook = func(step string) error {
		if step == "before_pointer_write" && !fired {
			fired = true
			return os.WriteFile(filepath.Join(root, pointerFile), otherPointer, privateFileMode)
		}
		return nil
	}
	if _, err := store.Publish(context.Background(), targetStage.ID()); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("CAS error = %v", err)
	}
	store.testHook = nil
	selected, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = selected.Close() }()
	if selected.ID() != otherStage.ID() {
		t.Fatal("publisher overwrote the intervening pointer")
	}
}

func TestPublicationUsesPredecessorCASAndPreservesOldSelection(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	firstStage, first := sealTestGeneration(t, store, "first", "")
	defer func() { _ = first.Close() }()
	if _, err := store.Publish(context.Background(), firstStage.ID()); err != nil {
		t.Fatal(err)
	}
	oldSelection, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = oldSelection.Close() }()
	predecessor := first.Receipt().GenerationDigest
	secondStage, second := sealTestGeneration(t, store, "second", predecessor)
	defer func() { _ = second.Close() }()
	thirdStage, third := sealTestGeneration(t, store, "third", predecessor)
	defer func() { _ = third.Close() }()
	secondProcess, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = secondProcess.Close() }()

	type result struct {
		id  string
		err error
	}
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for _, publisher := range []struct {
		store *Store
		id    string
	}{{store: store, id: secondStage.ID()}, {store: secondProcess, id: thirdStage.ID()}} {
		wait.Add(1)
		go func(publisher *Store, id string) {
			defer wait.Done()
			_, err := publisher.Publish(context.Background(), id)
			results <- result{id: id, err: err}
		}(publisher.store, publisher.id)
	}
	wait.Wait()
	close(results)
	successes, stale := 0, 0
	for result := range results {
		switch {
		case result.err == nil:
			successes++
		case errors.Is(result.err, ErrStalePredecessor):
			stale++
		default:
			t.Fatalf("publisher %s error = %v", result.id, result.err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("publish results: success=%d stale=%d", successes, stale)
	}
	var old bytes.Buffer
	if _, err := oldSelection.CopyMember(context.Background(), ServiceJira, "one", RoleNative, &old); err != nil || old.String() != "first" {
		t.Fatalf("old selection was invalidated: %q %v", old.String(), err)
	}
}

func TestPinnedRootsAndCopiesDetectReplacementAndTampering(t *testing.T) {
	t.Run("store root replacement", func(t *testing.T) {
		root, store := newTestStore(t, Options{})
		defer func() { _ = store.Close() }()
		stage, err := store.Begin()
		if err != nil {
			t.Fatal(err)
		}
		moved := root + "-moved"
		if err := os.Rename(root, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(root, privateDirMode); err != nil {
			t.Fatal(err)
		}
		err = stage.Add(context.Background(), MemberSpec{Service: ServiceJira, StableID: "one", Role: RoleNative, Path: "item"}, strings.NewReader("data"))
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("replacement error = %v", err)
		}
	})

	t.Run("sealed member tampering", func(t *testing.T) {
		root, store := newTestStore(t, Options{})
		defer func() { _ = store.Close() }()
		stage, generation := sealTestGeneration(t, store, "original", "")
		defer func() { _ = generation.Close() }()
		if _, err := store.Publish(context.Background(), stage.ID()); err != nil {
			t.Fatal(err)
		}
		selected, err := store.SelectCurrent(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = selected.Close() }()
		member := filepath.Join(root, generationsDir, stage.ID(), artifactsDir, "item")
		if err := os.WriteFile(member, []byte("tampered"), privateFileMode); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if _, err := selected.CopyMember(context.Background(), ServiceJira, "one", RoleNative, &output); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("copy error = %v", err)
		}
		if _, err := store.SelectCurrent(context.Background()); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("select error = %v", err)
		}
	})
}

func TestErrorsDoNotExposePrivateCanaries(t *testing.T) {
	const canary = "private-host-selector-object-title"
	parent := t.TempDir()
	root := filepath.Join(parent, canary)
	if err := os.Mkdir(root, privateDirMode); err != nil {
		t.Fatal(err)
	}
	store, err := Initialize(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	stage, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	spec := MemberSpec{Service: ServiceJira, StableID: canary, Role: RoleNative, Path: canary + "/item"}
	if err := stage.Add(context.Background(), spec, strings.NewReader(canary)); err != nil {
		t.Fatal(err)
	}
	generation, err := stage.Seal(context.Background(), sealOptions("", ServiceJira))
	if err != nil {
		t.Fatal(err)
	}
	_ = generation.Close()
	manifestPath := filepath.Join(root, generationsDir, stage.ID(), manifestFile)
	if err := os.WriteFile(manifestPath, []byte(`{"schema_version":1,"private":"`+canary+`"}`), privateFileMode); err != nil {
		t.Fatal(err)
	}
	_, err = store.Verify(context.Background(), stage.ID())
	if err == nil || strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), root) {
		t.Fatalf("private value leaked in error %q", err)
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("error lost integrity class: %v", err)
	}
}

func TestPublicationSurvivesAbruptProcessExitAfterRename(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	stage, generation := sealTestGeneration(t, store, "payload", "")
	defer func() { _ = generation.Close() }()
	command := exec.Command(os.Args[0], "-test.run=^TestCorpusCrashHelper$")
	command.Env = append(os.Environ(),
		"ATL_CORPUS_CRASH_HELPER=1",
		"ATL_CORPUS_CRASH_ROOT="+root,
		"ATL_CORPUS_CRASH_GENERATION="+stage.ID(),
	)
	err := command.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("helper exit = %v", err)
	}
	selected, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatalf("renamed pointer did not select a verified generation: %v", err)
	}
	defer func() { _ = selected.Close() }()
	if selected.Receipt().GenerationDigest != generation.Receipt().GenerationDigest {
		t.Fatal("crash selected a mixed generation")
	}
}

func TestCorpusCrashHelper(_ *testing.T) {
	if os.Getenv("ATL_CORPUS_CRASH_HELPER") != "1" {
		return
	}
	store, err := Open(os.Getenv("ATL_CORPUS_CRASH_ROOT"), Options{})
	if err != nil {
		os.Exit(21)
	}
	store.testHook = func(step string) error {
		if step == "after_pointer_rename" {
			os.Exit(23)
		}
		return nil
	}
	_, _ = store.Publish(context.Background(), os.Getenv("ATL_CORPUS_CRASH_GENERATION"))
	os.Exit(22)
}

func newTestStore(t *testing.T, opts Options) (string, *Store) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "store")
	if err := os.Mkdir(root, privateDirMode); err != nil {
		t.Fatal(err)
	}
	store, err := Initialize(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	return root, store
}

func sealTestGeneration(t *testing.T, store *Store, body, predecessor string) (*Stage, *Generation) {
	t.Helper()
	stage, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.Add(context.Background(), MemberSpec{Service: ServiceJira, StableID: "one", Role: RoleNative, Path: "item"}, strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	generation, err := stage.Seal(context.Background(), sealOptions(predecessor, ServiceJira))
	if err != nil {
		t.Fatal(err)
	}
	return stage, generation
}

func sealOptions(predecessor string, services ...Service) SealOptions {
	qualifications := make([]Qualification, 0, len(services))
	const hexadecimal = "0123456789abcdef"
	for index, service := range services {
		base := index * 4
		qualifications = append(qualifications, Qualification{
			Service: service, ReceiptSchema: 1,
			ScopeDigest: repeatedDigestByte(hexadecimal[base]), SelectorDigest: repeatedDigestByte(hexadecimal[base+1]),
			ProjectionDigest: repeatedDigestByte(hexadecimal[base+2]), ReceiptDigest: repeatedDigestByte(hexadecimal[base+3]),
		})
	}
	return SealOptions{
		ProjectionSchema: 1, GeneratorVersion: "test-1.0", BuildState: BuildStateClean,
		PredecessorDigest: predecessor, Qualifications: qualifications,
	}
}

func repeatedDigestByte(value byte) string { return strings.Repeat(string(value), 64) }

func writePrivate(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, privateFileMode); err != nil {
		t.Fatal(err)
	}
}

func failAt(want string) func(string) error {
	return func(step string) error {
		if step == want {
			return errors.New("injected")
		}
		return nil
	}
}
