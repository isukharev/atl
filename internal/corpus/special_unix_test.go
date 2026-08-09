//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package corpus

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestReadOpenDoesNotBlockAfterFIFOReplacement(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	memberPath := filepath.Join(rootPath, "item")
	if err := os.WriteFile(memberPath, []byte("payload"), privateFileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Lstat("item"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(memberPath); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(memberPath, uint32(privateFileMode)); err != nil {
		t.Fatal(err)
	}

	type openResult struct {
		file *os.File
		err  error
	}
	opened := make(chan openResult, 1)
	go func() {
		file, openErr := openReadOnlyNonblocking(root, "item")
		opened <- openResult{file: file, err: openErr}
	}()
	select {
	case result := <-opened:
		if result.err != nil {
			return
		}
		defer func() { _ = result.file.Close() }()
		info, statErr := result.file.Stat()
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().IsRegular() {
			t.Fatalf("replacement mode = %v, want non-regular", info.Mode())
		}
	case <-time.After(2 * time.Second):
		fd, openErr := unix.Open(memberPath, unix.O_WRONLY|unix.O_NONBLOCK, 0)
		if openErr == nil {
			_ = unix.Close(fd)
		}
		t.Fatal("read-only open blocked on FIFO replacement")
	}
}
