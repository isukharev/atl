package mirror

import (
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/safepath"
)

// EnsureScaffold creates the mirror root and its required privacy guard.
func (m *Mirror) EnsureScaffold() error {
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		return err
	}
	path := filepath.Join(m.Root, ".gitignore")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return safepath.WriteFileWithin(m.Root, path, []byte("# atl mirror — never commit secrets\n.atl/\ncredentials.json\n*.pat\n"), 0o644)
	} else {
		return err
	}
}
