package mirror

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

// SaveNativeStash preserves an immutable, content-addressed copy of a native
// backend body for explicit pull recovery. The returned path is relative to
// the mirror root and uses slash separators on every platform.
func (m *Mirror) SaveNativeStash(service, id, ext string, body []byte) (string, error) {
	if ext != ".csf" && ext != ".wiki" {
		return "", fmt.Errorf("%w: native stash extension must be .csf or .wiki", domain.ErrCheckFailed)
	}

	stashRoot := filepath.Join(m.Root, ".atl", "stash")
	dir := filepath.Join(stashRoot, safepath.Segment(service), safepath.Segment(id))
	target := filepath.Join(dir, Hash(body)+ext)
	if !safepath.Within(stashRoot, dir) || !safepath.Within(dir, target) {
		return "", fmt.Errorf("%w: refusing unsafe native stash path", domain.ErrCheckFailed)
	}
	if err := safepath.MkdirAllWithin(m.Root, dir, 0o700); err != nil {
		return "", fmt.Errorf("%w: create native stash directory: %v", domain.ErrCheckFailed, err)
	}

	if err := safepath.WriteFileExclusiveWithin(m.Root, target, body, 0o600); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%w: save native stash: %v", domain.ErrCheckFailed, err)
		}
		existing, readErr := safepath.ReadFileWithin(m.Root, target)
		if readErr != nil {
			return "", fmt.Errorf("%w: inspect existing native stash: %v", domain.ErrCheckFailed, readErr)
		}
		if !bytes.Equal(existing, body) {
			return "", fmt.Errorf("%w: existing native stash does not match its content hash", domain.ErrCheckFailed)
		}
	}

	relativePath, err := filepath.Rel(m.Root, target)
	if err != nil || !safepath.Within(m.Root, target) {
		return "", fmt.Errorf("%w: resolve native stash path", domain.ErrCheckFailed)
	}
	return filepath.ToSlash(relativePath), nil
}
