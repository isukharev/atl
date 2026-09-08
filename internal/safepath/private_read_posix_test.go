//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package safepath

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReadFilePrivateDoesNotBlockWhenFileBecomesFIFO(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "broker.json")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readFilePrivateWithHook("", target, 16, true, func() error {
			if err := os.Remove(target); err != nil {
				return err
			}
			return syscall.Mkfifo(target, 0o600)
		})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnsafePrivatePath) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("private read blocked after FIFO replacement")
	}
}
