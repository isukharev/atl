//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package corpus

import (
	"context"
	"errors"
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
	if err := unix.Mkfifo(filepath.Join(root, generationsDir, stage.ID(), artifactsDir, "fifo"), uint32(privateFileMode)); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Seal(context.Background(), sealOptions("", ServiceJira)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("seal error = %v", err)
	}
}
