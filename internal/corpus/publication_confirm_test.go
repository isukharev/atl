package corpus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConfirmCurrentRevalidatesPinnedGenerationAndExactPointer(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	firstStage, first := sealTestGeneration(t, store, "first", "")
	if _, err := store.Publish(context.Background(), firstStage.ID()); err != nil {
		t.Fatal(err)
	}
	selected, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = selected.Close() }()
	if err := store.ConfirmCurrent(context.Background(), selected); err != nil {
		t.Fatal(err)
	}
	secondStage, second := sealTestGeneration(t, store, "second", first.Receipt().GenerationDigest)
	defer func() { _ = first.Close(); _ = second.Close() }()
	pointerBytes, err := canonicalPointer(Pointer{
		SchemaVersion: PointerSchemaV1, GenerationID: secondStage.ID(),
		GenerationDigest: second.Receipt().GenerationDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	store.testHook = func(step string) error {
		if step != "after_confirm_current_revalidate" {
			return nil
		}
		return os.WriteFile(filepath.Join(root, pointerFile), pointerBytes, privateFileMode)
	}
	if err := store.ConfirmCurrent(context.Background(), selected); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("pointer race error=%v", err)
	}
	store.testHook = nil
}

func TestConfirmCurrentRejectsTamperedPinnedGeneration(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	stage, generation := sealTestGeneration(t, store, "payload", "")
	defer func() { _ = generation.Close() }()
	if _, err := store.Publish(context.Background(), stage.ID()); err != nil {
		t.Fatal(err)
	}
	member := generation.Manifest().Members[0]
	if err := os.WriteFile(filepath.Join(root, generationsDir, stage.ID(), artifactsDir, member.Path), []byte("tampered"), privateFileMode); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmCurrent(context.Background(), generation); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tamper error=%v", err)
	}
}
