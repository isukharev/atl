//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package corpus

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPublishSyncsTargetBeforePointerReplacement(t *testing.T) {
	_, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	stage, generation := sealTestGeneration(t, store, "payload", "")
	defer func() { _ = generation.Close() }()

	var boundaries []string
	store.testHook = func(step string) error {
		switch step {
		case "before_pointer_write", "before_publish_target_sync", "after_publish_target_sync", "after_pointer_rename":
			boundaries = append(boundaries, step)
		}
		return nil
	}
	if _, err := store.Publish(context.Background(), stage.ID()); err != nil {
		t.Fatal(err)
	}
	want := []string{"before_pointer_write", "before_publish_target_sync", "after_publish_target_sync", "after_pointer_rename"}
	if !reflect.DeepEqual(boundaries, want) {
		t.Fatalf("publication boundaries = %v, want %v", boundaries, want)
	}
}

func TestPublishTargetSyncFailurePreservesPriorPointer(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	firstStage, first := sealTestGeneration(t, store, "first", "")
	defer func() { _ = first.Close() }()
	if _, err := store.Publish(context.Background(), firstStage.ID()); err != nil {
		t.Fatal(err)
	}
	priorPointer, err := os.ReadFile(filepath.Join(root, pointerFile))
	if err != nil {
		t.Fatal(err)
	}

	secondStage, second := sealTestGeneration(t, store, "second", first.Receipt().GenerationDigest)
	defer func() { _ = second.Close() }()
	targetPath := filepath.Join(root, generationsDir, secondStage.ID())
	fired := false
	var chmodErr error
	store.testHook = func(step string) error {
		if step == "before_publish_target_sync" && !fired {
			fired = true
			chmodErr = os.Chmod(targetPath, privateFileMode)
			return chmodErr
		}
		return nil
	}
	_, publishErr := store.Publish(context.Background(), secondStage.ID())
	store.testHook = nil
	if chmodErr != nil {
		t.Fatalf("inject target sync failure: %v", chmodErr)
	}
	if err := os.Chmod(targetPath, privateDirMode); err != nil {
		t.Fatalf("restore target mode: %v", err)
	}
	if !fired {
		t.Fatal("target sync boundary was not reached")
	}
	if !errors.Is(publishErr, ErrIntegrity) || errors.Is(publishErr, ErrOutcomeUnknown) {
		t.Fatalf("publish error = %v, want definite integrity failure", publishErr)
	}
	afterPointer, err := os.ReadFile(filepath.Join(root, pointerFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterPointer, priorPointer) {
		t.Fatal("target sync failure replaced the prior pointer")
	}
	selected, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = selected.Close() }()
	if selected.ID() != firstStage.ID() {
		t.Fatalf("selected generation = %s, want prior generation", selected.ID())
	}
}

func TestPublishRevalidatesTargetChangedAtPointerBoundary(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	stage, generation := sealTestGeneration(t, store, "payload", "")
	defer func() { _ = generation.Close() }()
	receiptPath := filepath.Join(root, generationsDir, stage.ID(), receiptFile)
	receiptBytes, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	fired := false
	var writeErr error
	store.testHook = func(step string) error {
		if step == "before_pointer_write" && !fired {
			fired = true
			writeErr = os.WriteFile(receiptPath, []byte("changed\n"), privateFileMode)
			return writeErr
		}
		return nil
	}
	_, publishErr := store.Publish(context.Background(), stage.ID())
	store.testHook = nil
	if writeErr != nil {
		t.Fatalf("mutate target at pointer boundary: %v", writeErr)
	}
	if err := os.WriteFile(receiptPath, receiptBytes, privateFileMode); err != nil {
		t.Fatalf("restore receipt: %v", err)
	}
	if !fired {
		t.Fatal("pointer boundary was not reached")
	}
	if !errors.Is(publishErr, ErrIntegrity) || errors.Is(publishErr, ErrOutcomeUnknown) {
		t.Fatalf("publish error = %v, want definite integrity failure", publishErr)
	}
	if _, err := store.SelectCurrent(context.Background()); !errors.Is(err, ErrNoCurrent) {
		t.Fatalf("target changed after sync became current: %v", err)
	}
}

func TestPublishIdempotentRecoveryRevalidatesCurrentTarget(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	stage, generation := sealTestGeneration(t, store, "payload", "")
	defer func() { _ = generation.Close() }()
	if _, err := store.Publish(context.Background(), stage.ID()); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(root, generationsDir, stage.ID(), receiptFile)
	receiptBytes, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	fired := false
	var writeErr error
	store.testHook = func(step string) error {
		if step == "after_publish_target_sync" && !fired {
			fired = true
			writeErr = os.WriteFile(receiptPath, []byte("changed\n"), privateFileMode)
			return writeErr
		}
		return nil
	}
	_, publishErr := store.Publish(context.Background(), stage.ID())
	store.testHook = nil
	if writeErr != nil {
		t.Fatalf("mutate current target after sync: %v", writeErr)
	}
	if err := os.WriteFile(receiptPath, receiptBytes, privateFileMode); err != nil {
		t.Fatalf("restore receipt: %v", err)
	}
	if !fired {
		t.Fatal("current-target sync boundary was not reached")
	}
	if !errors.Is(publishErr, ErrIntegrity) || errors.Is(publishErr, ErrOutcomeUnknown) {
		t.Fatalf("publish error = %v, want definite integrity failure", publishErr)
	}
	selected, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = selected.Close() }()
	if selected.ID() != stage.ID() {
		t.Fatalf("selected generation = %s, want %s", selected.ID(), stage.ID())
	}
}

func TestPublishIdempotentRecoveryDetectsPointerDrift(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	targetStage, target := sealTestGeneration(t, store, "target", "")
	defer func() { _ = target.Close() }()
	if _, err := store.Publish(context.Background(), targetStage.ID()); err != nil {
		t.Fatal(err)
	}
	otherStage, other := sealTestGeneration(t, store, "other", "")
	defer func() { _ = other.Close() }()
	otherPointer, err := canonicalPointer(Pointer{
		SchemaVersion: PointerSchemaV1, GenerationID: otherStage.ID(), GenerationDigest: other.Receipt().GenerationDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	fired := false
	var writeErr error
	store.testHook = func(step string) error {
		if step == "after_publish_target_sync" && !fired {
			fired = true
			writeErr = os.WriteFile(filepath.Join(root, pointerFile), otherPointer, privateFileMode)
			return writeErr
		}
		return nil
	}
	_, publishErr := store.Publish(context.Background(), targetStage.ID())
	store.testHook = nil
	if writeErr != nil {
		t.Fatalf("replace pointer during idempotent recovery: %v", writeErr)
	}
	if !fired {
		t.Fatal("current-target sync boundary was not reached")
	}
	assertReason(t, publishErr, ReasonConcurrent)
	selected, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = selected.Close() }()
	if selected.ID() != otherStage.ID() {
		t.Fatalf("selected generation = %s, want concurrent selection %s", selected.ID(), otherStage.ID())
	}
}

func TestPublishInsideLockReopenErrorDoesNotPanic(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	stage, generation := sealTestGeneration(t, store, "payload", "")
	defer func() { _ = generation.Close() }()
	targetPath := filepath.Join(root, generationsDir, stage.ID())
	movedPath := targetPath + ".moved"
	fired := false
	var moveErr error
	store.testHook = func(step string) error {
		if step == "before_locked_target_open" && !fired {
			fired = true
			moveErr = os.Rename(targetPath, movedPath)
			return moveErr
		}
		return nil
	}

	var publishErr error
	var panicValue any
	func() {
		defer func() { panicValue = recover() }()
		_, publishErr = store.Publish(context.Background(), stage.ID())
	}()
	store.testHook = nil
	if moveErr != nil {
		t.Fatalf("inject inside-lock reopen error: %v", moveErr)
	}
	if err := os.Rename(movedPath, targetPath); err != nil {
		t.Fatalf("restore generation directory: %v", err)
	}
	if !fired {
		t.Fatal("inside-lock target open boundary was not reached")
	}
	if panicValue != nil {
		t.Fatalf("publish panicked after inside-lock reopen error: %v", panicValue)
	}
	if !errors.Is(publishErr, ErrIntegrity) || errors.Is(publishErr, ErrOutcomeUnknown) {
		t.Fatalf("publish error = %v, want definite integrity failure", publishErr)
	}
	if _, err := store.SelectCurrent(context.Background()); !errors.Is(err, ErrNoCurrent) {
		t.Fatalf("failed publication became current: %v", err)
	}
}
