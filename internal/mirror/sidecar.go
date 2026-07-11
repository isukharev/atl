package mirror

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

// SyncState is the last-synced snapshot of one resource.
type SyncState struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Hash    string `json:"hash"`
	Path    string `json:"path"` // rel to mirror root
}

// ViewState records the render settings a resource's .md view was last written
// with, so apply can reproduce the exact pristine view regardless of the
// ambient config. Sections is the computed enabled-section list (sorted), not
// the profile name, so it stays valid if profile definitions evolve.
type ViewState struct {
	Sections     []string         `json:"sections"`
	CustomFields []string         `json:"custom_fields,omitempty"`
	FieldViews   []FieldViewState `json:"field_views,omitempty"`
	PageFields   []FieldViewState `json:"page_fields,omitempty"`
	EpicField    string           `json:"epic_field,omitempty"`
}

// FieldViewState is the serialized, backend-neutral shape of a configured
// Jira field view or Confluence page field. Mirror deliberately does not import
// config/app; the app layer converts it to resolved render settings.
type FieldViewState struct {
	ID        string `json:"id"`
	Label     string `json:"label,omitempty"`
	Placement string `json:"placement,omitempty"`
	Format    string `json:"format,omitempty"`
	ShowEmpty bool   `json:"show_empty,omitempty"`
	Editable  bool   `json:"editable,omitempty"`
}

type sidecarFile struct {
	Pages map[string]SyncState `json:"pages"`
	// Views records the render settings each resource's .md view was last
	// written with (keyed by the same page id / issue key as Pages). It lets
	// apply reproduce the exact pristine view regardless of the ambient config.
	Views map[string]ViewState `json:"views,omitempty"`
}

func (m *Mirror) sidecarPath() string     { return filepath.Join(m.Root, ".atl", "state.json") }
func (m *Mirror) sidecarLockPath() string { return filepath.Join(m.Root, ".atl", "state.lock") }

func (m *Mirror) lockSidecar() (*safepath.FileLock, error) {
	if err := safepath.MkdirAllWithin(m.Root, filepath.Dir(m.sidecarPath()), 0o755); err != nil {
		return nil, err
	}
	// Service-level mutations use distinct Jira/Confluence locks, so two short
	// sidecar commits may overlap even though neither operation is unsafe. Give
	// the other atomic patch a bounded window to finish; prolonged contention
	// still fails closed instead of waiting indefinitely or losing entries.
	for attempt := 0; attempt < 20; attempt++ {
		lock, acquired, err := safepath.TryLockFileWithin(m.Root, m.sidecarLockPath(), 0o600)
		if err != nil {
			return nil, err
		}
		if acquired {
			return lock, nil
		}
		if attempt < 19 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	return nil, fmt.Errorf("%w: another mirror state update is active for %s after a brief retry window", domain.ErrCheckFailed, m.Root)
}

// loadSidecar reads .atl/state.json. A missing file is an empty state (fresh
// mirror); an unparseable one is a loud error — silently treating it as empty
// would reset every page to never-synced and quietly disable drift detection.
func (m *Mirror) loadSidecar() (sidecarFile, error) {
	sc := sidecarFile{Pages: map[string]SyncState{}, Views: map[string]ViewState{}}
	b, err := safepath.ReadFileWithin(m.Root, m.sidecarPath())
	if os.IsNotExist(err) {
		return sc, nil
	}
	if err != nil {
		return sc, err
	}
	if err := json.Unmarshal(b, &sc); err != nil {
		// ErrCheckFailed (exit 8) gives agents a branchable signal, consistent
		// with the other local pre-write integrity refusals.
		return sc, fmt.Errorf("%w: corrupt mirror sidecar %s: %v — fix the JSON or delete the file to reset sync state (pages will read as never-synced until re-pulled)", domain.ErrCheckFailed, m.sidecarPath(), err)
	}
	if sc.Pages == nil {
		sc.Pages = map[string]SyncState{}
	}
	if sc.Views == nil {
		sc.Views = map[string]ViewState{}
	}
	return sc, nil
}

// saveSidecar replaces state.json atomically (temp + fsync + rename), so a
// crash mid-save can never leave a half-written file. Callers that perform a
// read-modify-write must hold lockSidecar or use mergeSidecarPatch.
func (m *Mirror) saveSidecar(sc sidecarFile) error {
	if err := safepath.MkdirAllWithin(m.Root, filepath.Dir(m.sidecarPath()), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(sc, "", "  ")
	return safepath.WriteFileWithin(m.Root, m.sidecarPath(), append(b, '\n'), 0o600)
}

// mergeSidecarPatch applies only the entries changed by one operation to the
// latest state under a backend-neutral lock. Re-reading after lock acquisition
// is essential: Jira and Confluence may share one mirror root and batches can
// have been opened from the same old snapshot.
func (m *Mirror) mergeSidecarPatch(pages map[string]SyncState, views map[string]ViewState) error {
	if len(pages) == 0 && len(views) == 0 {
		return nil
	}
	lock, err := m.lockSidecar()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	sc, err := m.loadSidecar()
	if err != nil {
		return err
	}
	for id, state := range pages {
		sc.Pages[id] = state
	}
	for id, state := range views {
		sc.Views[id] = state
	}
	return m.saveSidecar(sc)
}

// SyncedVersion returns the last-synced version for an id (0 if untracked).
// The error is the loud corrupt-sidecar signal — swallowing it here would
// reintroduce the silent state reset this API exists to prevent.
func (m *Mirror) SyncedVersion(id string) (int, error) {
	sc, err := m.loadSidecar()
	if err != nil {
		return 0, err
	}
	return sc.Pages[id].Version, nil
}

// SyncStateOf returns the complete tracked state for one resource. It is used
// by relocation preflight to find the old canonical path by stable page id.
func (m *Mirror) SyncStateOf(id string) (SyncState, bool, error) {
	sc, err := m.loadSidecar()
	if err != nil {
		return SyncState{}, false, err
	}
	st, ok := sc.Pages[id]
	return st, ok, nil
}

// ViewStateOf returns the render settings a resource's .md view was last written
// with. ok is false when no view state was ever recorded (a pre-upgrade mirror
// or a never-rendered resource). The error is the loud corrupt-sidecar signal,
// same as SyncedVersion — swallowing it would let apply silently fall back to
// ambient settings against a broken sidecar.
func (m *Mirror) ViewStateOf(id string) (ViewState, bool, error) {
	sc, err := m.loadSidecar()
	if err != nil {
		return ViewState{}, false, err
	}
	vs, ok := sc.Views[id]
	return vs, ok, nil
}

// SaveViewStates merges a batch of view states into the sidecar in one
// load-modify-save (for the render commands, which rewrite many .md views but
// touch no sync state). Existing entries for other ids are preserved.
func (m *Mirror) SaveViewStates(views map[string]ViewState) error {
	return m.mergeSidecarPatch(nil, views)
}

// saveBaseExt stores a pristine copy of the last-synced body under a
// caller-chosen extension (".csf" for Confluence, ".wiki" for the Jira
// substrate) so push can diff the agent's edits against it (consequence report)
// without a network round-trip. ext must include the leading dot.
func (m *Mirror) saveBaseExt(id string, body []byte, ext string) error {
	dir := filepath.Join(m.Root, ".atl", "base")
	if err := safepath.MkdirAllWithin(m.Root, dir, 0o755); err != nil {
		return err
	}
	// id is a backend-supplied content id / issue key: sanitize it to a single
	// safe segment so a hostile server cannot use it to traverse out of the base
	// store, and assert containment as defense in depth.
	target := filepath.Join(dir, safepath.Segment(id)+ext)
	if !safepath.Within(dir, target) {
		return fmt.Errorf("refusing unsafe base path for id %q", id)
	}
	return safepath.WriteFileWithin(m.Root, target, body, 0o600)
}

// saveBase stores the pristine Confluence `.csf` base copy. See saveBaseExt.
func (m *Mirror) saveBase(id string, body []byte) error {
	return m.saveBaseExt(id, body, ".csf")
}

// SaveBaseExt is the exported ext-aware base writer for a backend (e.g. Jira)
// that writes its own substrate files outside writePageFiles but still needs a
// pristine base recorded for drift detection. ext must include the leading dot.
func (m *Mirror) SaveBaseExt(id string, body []byte, ext string) error {
	return m.saveBaseExt(id, body, ext)
}

// baseBodyExt returns the pristine last-synced body for an id under ext.
func (m *Mirror) baseBodyExt(id, ext string) ([]byte, bool) {
	dir := filepath.Join(m.Root, ".atl", "base")
	target := filepath.Join(dir, safepath.Segment(id)+ext)
	if !safepath.Within(dir, target) {
		return nil, false
	}
	b, err := safepath.ReadFileWithin(m.Root, target)
	if err != nil {
		return nil, false
	}
	return b, true
}

// BaseBody returns the pristine last-synced Confluence `.csf` body for an id.
func (m *Mirror) BaseBody(id string) ([]byte, bool) {
	return m.baseBodyExt(id, ".csf")
}

// BaseBodyExt returns the pristine last-synced body for an id under a
// caller-chosen extension (e.g. ".wiki" for the Jira substrate). See SaveBaseExt.
func (m *Mirror) BaseBodyExt(id, ext string) ([]byte, bool) {
	return m.baseBodyExt(id, ext)
}
