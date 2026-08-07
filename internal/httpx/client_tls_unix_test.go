//go:build !windows

package httpx

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

func TestValidateCABundleRejectsFIFOWithoutOpeningIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundle.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- ValidateCABundle(path) }()
	select {
	case err := <-done:
		if !errors.Is(err, domain.ErrConfig) {
			t.Fatalf("error=%v, want ErrConfig", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CA bundle validation blocked while opening a FIFO")
	}
}
