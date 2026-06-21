package mirror

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/safepath"
)

// SyncState is the last-synced snapshot of one resource.
type SyncState struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Hash    string `json:"hash"`
	Path    string `json:"path"` // rel to mirror root
}

type sidecarFile struct {
	Pages map[string]SyncState `json:"pages"`
}

func (m *Mirror) sidecarPath() string { return filepath.Join(m.Root, ".atl", "state.json") }

func (m *Mirror) loadSidecar() (sidecarFile, error) {
	sc := sidecarFile{Pages: map[string]SyncState{}}
	b, err := os.ReadFile(m.sidecarPath())
	if os.IsNotExist(err) {
		return sc, nil
	}
	if err != nil {
		return sc, err
	}
	_ = json.Unmarshal(b, &sc)
	if sc.Pages == nil {
		sc.Pages = map[string]SyncState{}
	}
	return sc, nil
}

func (m *Mirror) saveSidecar(sc sidecarFile) error {
	if err := os.MkdirAll(filepath.Dir(m.sidecarPath()), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(sc, "", "  ")
	return safepath.WriteFile(m.sidecarPath(), append(b, '\n'), 0o600)
}

// recordSync updates the last-synced state for one resource.
func (m *Mirror) recordSync(id string, version int, hash, relPath string) error {
	sc, err := m.loadSidecar()
	if err != nil {
		return err
	}
	sc.Pages[id] = SyncState{ID: id, Version: version, Hash: hash, Path: relPath}
	return m.saveSidecar(sc)
}

// SyncedVersion returns the last-synced version for an id (0 if untracked).
func (m *Mirror) SyncedVersion(id string) int {
	sc, _ := m.loadSidecar()
	return sc.Pages[id].Version
}

// saveBase stores a pristine copy of the last-synced body so push can diff the
// agent's edits against it (consequence report) without a network round-trip.
func (m *Mirror) saveBase(id string, body []byte) error {
	dir := filepath.Join(m.Root, ".atl", "base")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// id is a backend-supplied content id: sanitize it to a single safe segment
	// so a hostile server cannot use it to traverse out of the base store, and
	// assert containment as defense in depth.
	target := filepath.Join(dir, safepath.Segment(id)+".csf")
	if !safepath.Within(dir, target) {
		return fmt.Errorf("refusing unsafe base path for id %q", id)
	}
	return safepath.WriteFile(target, body, 0o600)
}

// BaseBody returns the pristine last-synced body for an id, if present.
func (m *Mirror) BaseBody(id string) ([]byte, bool) {
	dir := filepath.Join(m.Root, ".atl", "base")
	target := filepath.Join(dir, safepath.Segment(id)+".csf")
	if !safepath.Within(dir, target) {
		return nil, false
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return nil, false
	}
	return b, true
}
