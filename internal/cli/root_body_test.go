package cli

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestReadBodyMissingFileUsesNotFoundSentinel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.csf")
	_, err := readBody(path)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("readBody(%q) err=%v, want ErrNotFound", path, err)
	}
	if code := codeFor(err); code != exitNotFound {
		t.Fatalf("codeFor(err)=%d, want %d", code, exitNotFound)
	}
}
