//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package corpus

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSealRejectsSpecialFiles(t *testing.T) {
	root, store := newTestStore(t, Options{})
	defer func() { _ = store.Close() }()
	stage, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.Add(context.Background(), MemberSpec{Service: ServiceJira, StableID: "one", Role: RoleNative, Path: "item"}, strings.NewReader("payload")); err != nil {
		t.Fatal(err)
	}
	memberPath := filepath.Join(root, generationsDir, stage.ID(), artifactsDir, "item")
	if err := os.Remove(memberPath); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(memberPath, uint32(privateFileMode)); err != nil {
		t.Fatal(err)
	}
	_, err = stage.Seal(context.Background(), sealOptions("", ServiceJira))
	assertReason(t, err, ReasonMode)
}
