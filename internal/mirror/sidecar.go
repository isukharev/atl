package mirror

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
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

// StagedState binds ATL-produced local substrate bytes to their resource
// identity, mirror-relative path, and exact remote base digest. It is
// deliberately separate from SyncState: recording a staged result does not
// claim that the bytes exist on the remote, advance a remote version, or
// replace the pristine base.
type StagedState struct {
	ID       string `json:"id"`
	Hash     string `json:"hash"`
	BaseHash string `json:"base_hash"`
	Path     string `json:"path"` // canonical slash-separated path rel to mirror root
}

// StagedContent is one ATL-produced native body and its exact remote base
// digest to bind as staged lineage. RecordStaged hashes Body inside the mirror
// package so callers cannot accidentally bind the lineage to a transformed or
// separately hashed view.
type StagedContent struct {
	ID       string
	Path     string
	Body     []byte
	BaseHash string
}

// ViewState records the render settings a resource's .md view was last written
// with, so apply can reproduce the exact pristine view regardless of the
// ambient config. Sections is the computed enabled-section list (sorted), not
// the profile name, so it stays valid if profile definitions evolve.
type ViewState struct {
	Sections        []string         `json:"sections"`
	DisplayTimeZone string           `json:"display_time_zone,omitempty"`
	CustomFields    []string         `json:"custom_fields,omitempty"`
	FieldViews      []FieldViewState `json:"field_views,omitempty"`
	PageFields      []FieldViewState `json:"page_fields,omitempty"`
	EpicField       string           `json:"epic_field,omitempty"`
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
	// Staged records local ATL-produced native bytes that have not been
	// established as the remote baseline. Old sidecars omit this map and decode
	// conservatively as having no staged lineage.
	Staged map[string]StagedState `json:"staged,omitempty"`
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
	sc := sidecarFile{Pages: map[string]SyncState{}, Views: map[string]ViewState{}, Staged: map[string]StagedState{}}
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
	if sc.Staged == nil {
		sc.Staged = map[string]StagedState{}
	}
	for id, state := range sc.Staged {
		if err := validateStagedState(id, state); err != nil {
			return sc, fmt.Errorf("%w: corrupt mirror sidecar %s: invalid staged lineage: %v — fix the JSON or delete the staged entry", domain.ErrCheckFailed, m.sidecarPath(), err)
		}
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
func (m *Mirror) mergeSidecarPatch(pages map[string]SyncState, views map[string]ViewState, staged map[string]*StagedState) error {
	if len(pages) == 0 && len(views) == 0 && len(staged) == 0 {
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
	for id, state := range staged {
		if state == nil {
			delete(sc.Staged, id)
			continue
		}
		if err := validateStagedState(id, *state); err != nil {
			return fmt.Errorf("%w: invalid staged lineage: %v", domain.ErrCheckFailed, err)
		}
		sc.Staged[id] = *state
	}
	return m.saveSidecar(sc)
}

func validateStagedState(key string, state StagedState) error {
	if key == "" || state.ID == "" || key != state.ID {
		return fmt.Errorf("map identity %q does not match resource id %q", key, state.ID)
	}
	if err := validateStagedPath(state.Path); err != nil {
		return fmt.Errorf("resource %q path %q: %w", key, state.Path, err)
	}
	if len(state.Hash) != 64 {
		return fmt.Errorf("resource %q has invalid SHA-256 digest", key)
	}
	if _, err := hex.DecodeString(state.Hash); err != nil {
		return fmt.Errorf("resource %q has invalid SHA-256 digest", key)
	}
	if len(state.BaseHash) != 64 {
		return fmt.Errorf("resource %q has invalid base SHA-256 digest", key)
	}
	if _, err := hex.DecodeString(state.BaseHash); err != nil {
		return fmt.Errorf("resource %q has invalid base SHA-256 digest", key)
	}
	return nil
}

func validateStagedPath(relative string) error {
	if relative == "" || relative == "." || strings.ContainsAny(relative, "\\:") || strings.ContainsRune(relative, 0) {
		return fmt.Errorf("must be a non-empty canonical slash-separated relative path")
	}
	clean := path.Clean(relative)
	if clean != relative || path.IsAbs(relative) || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("must be a canonical path contained by the mirror root")
	}
	return nil
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

// ViewStatesOf reads one sidecar snapshot and returns the requested recorded
// view states. Missing ids are omitted from the result.
func (m *Mirror) ViewStatesOf(ids []string) (map[string]ViewState, error) {
	sc, err := m.loadSidecar()
	if err != nil {
		return nil, err
	}
	out := make(map[string]ViewState, len(ids))
	for _, id := range ids {
		if state, ok := sc.Views[id]; ok {
			out[id] = state
		}
	}
	return out, nil
}

// StagedStateOf returns the staged-local lineage for one resource. ok is false
// for an old sidecar, a never-staged resource, or lineage cleared by a later
// successful sync. The remote SyncState and pristine base are not consulted or
// changed by this API.
func (m *Mirror) StagedStateOf(id string) (StagedState, bool, error) {
	sc, err := m.loadSidecar()
	if err != nil {
		return StagedState{}, false, err
	}
	state, ok := sc.Staged[id]
	return state, ok, nil
}

// StagedStatesOf reads one sidecar snapshot and returns the requested staged
// lineage entries. Missing ids are omitted from the result.
func (m *Mirror) StagedStatesOf(ids []string) (map[string]StagedState, error) {
	sc, err := m.loadSidecar()
	if err != nil {
		return nil, err
	}
	out := make(map[string]StagedState, len(ids))
	for _, id := range ids {
		if state, ok := sc.Staged[id]; ok {
			out[id] = state
		}
	}
	return out, nil
}

// RecordStaged atomically records or updates staged-local lineage for a
// batch. Staged hashes are computed directly from the supplied native bytes;
// callers must supply the exact remote base hash. Entries for other resources
// and services are merge-preserved under the sidecar lock.
func (m *Mirror) RecordStaged(contents []StagedContent) error {
	patch := make(map[string]*StagedState, len(contents))
	for _, content := range contents {
		state := StagedState{ID: content.ID, Hash: Hash(content.Body), BaseHash: content.BaseHash, Path: content.Path}
		if err := validateStagedState(content.ID, state); err != nil {
			return fmt.Errorf("%w: invalid staged lineage: %v", domain.ErrCheckFailed, err)
		}
		if _, duplicate := patch[content.ID]; duplicate {
			return fmt.Errorf("%w: duplicate staged lineage for resource %q", domain.ErrCheckFailed, content.ID)
		}
		copy := state
		patch[content.ID] = &copy
	}
	return m.mergeSidecarPatch(nil, nil, patch)
}

// ClearStaged atomically clears staged-local lineage for the supplied resource
// identities. Unknown ids are harmless. Other resources and sidecar maps are
// merge-preserved.
func (m *Mirror) ClearStaged(ids ...string) error {
	patch := make(map[string]*StagedState, len(ids))
	for _, id := range ids {
		if id == "" {
			return fmt.Errorf("%w: staged lineage resource id is empty", domain.ErrCheckFailed)
		}
		patch[id] = nil
	}
	return m.mergeSidecarPatch(nil, nil, patch)
}

// SaveViewStates merges a batch of view states into the sidecar in one
// load-modify-save (for the render commands, which rewrite many .md views but
// touch no sync state). Existing entries for other ids are preserved.
func (m *Mirror) SaveViewStates(views map[string]ViewState) error {
	return m.mergeSidecarPatch(nil, views, nil)
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

// ReadBaseBody reads the pristine last-synced Confluence body while preserving
// the distinction between a missing pre-upgrade baseline and an unreadable
// baseline. BaseBody intentionally keeps its historical best-effort contract;
// integrity-sensitive offline analysis should use this method instead.
func (m *Mirror) ReadBaseBody(id string) ([]byte, bool, error) {
	return m.ReadBaseBodyExt(id, ".csf")
}

// ReadBaseBodyExt reads a pristine last-synced body under a caller-selected
// extension while preserving missing versus unreadable evidence. It is the
// integrity-sensitive counterpart to BaseBodyExt.
func (m *Mirror) ReadBaseBodyExt(id, ext string) ([]byte, bool, error) {
	dir := filepath.Join(m.Root, ".atl", "base")
	target := filepath.Join(dir, safepath.Segment(id)+ext)
	if !safepath.Within(dir, target) {
		return nil, false, fmt.Errorf("refusing unsafe base path for id %q", id)
	}
	b, err := safepath.ReadFileWithin(m.Root, target)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// SyncStates returns a copy of every recorded resource state. Consumers must
// filter by substrate extension because Jira and Confluence may share a mirror.
func (m *Mirror) SyncStates() ([]SyncState, error) {
	sc, err := m.loadSidecar()
	if err != nil {
		return nil, err
	}
	out := make([]SyncState, 0, len(sc.Pages))
	for _, state := range sc.Pages {
		out = append(out, state)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// BaseBodyExt returns the pristine last-synced body for an id under a
// caller-chosen extension (e.g. ".wiki" for the Jira substrate). See SaveBaseExt.
func (m *Mirror) BaseBodyExt(id, ext string) ([]byte, bool) {
	return m.baseBodyExt(id, ext)
}
