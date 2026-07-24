package app

import (
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/safepath"
)

func TestMirrorSnapshotLockDetectsLegacyWriterBootstrap(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "mirror.lock")
	guard, err := beginMirrorSnapshotLock(root, path)
	if err != nil {
		t.Fatal(err)
	}
	writer, acquired, err := safepath.TryLockFileWithin(root, path, 0o600)
	if err != nil || !acquired || writer == nil {
		t.Fatalf("writer bootstrap: acquired=%t err=%v", acquired, err)
	}
	retry, err := guard.finish()
	if err != nil || !retry {
		t.Fatalf("finish: retry=%t err=%v", retry, err)
	}
	if err := writer.Unlock(); err != nil {
		t.Fatal(err)
	}

	guard, err = beginMirrorSnapshotLock(root, path)
	if err != nil {
		t.Fatal(err)
	}
	if guard.missing || guard.lock == nil {
		t.Fatalf("bootstrapped guard=%+v", guard)
	}
	if retry, err := guard.finish(); err != nil || retry {
		t.Fatalf("held finish: retry=%t err=%v", retry, err)
	}
}
