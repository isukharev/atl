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

const incrementalStateSchema = 1

const (
	maxIncrementalStateBytes = 4 << 20
	maxIncrementalSelectors  = 1000
)

// IncrementalWatermark is a backend-neutral, selector-bound lower boundary.
// Protocol and Boundary let a backend bind the display-oriented Since value to
// an absolute instant. BoundaryVersions makes an inclusive, coarse timestamp
// cursor efficient while still admitting a new version or unseen identity.
type IncrementalWatermark struct {
	Service          string         `json:"service"`
	SelectorSHA256   string         `json:"selector_sha256"`
	Selector         string         `json:"selector"`
	Since            string         `json:"since"`
	TimeZone         string         `json:"time_zone,omitempty"`
	Protocol         string         `json:"protocol,omitempty"`
	Boundary         string         `json:"boundary,omitempty"`
	Observed         string         `json:"observed,omitempty"`
	BoundaryVersions map[string]int `json:"boundary_versions,omitempty"`
}

type incrementalState struct {
	SchemaVersion int                             `json:"schema_version"`
	Watermarks    map[string]IncrementalWatermark `json:"watermarks"`
}

func (m *Mirror) incrementalPath() string {
	return filepath.Join(m.Root, ".atl", "incremental.json")
}

func (m *Mirror) incrementalLockPath() string {
	return filepath.Join(m.Root, ".atl", "incremental.lock")
}

func incrementalKey(service, selectorSHA256 string) string {
	return service + ":" + selectorSHA256
}

func (m *Mirror) loadIncrementalState() (incrementalState, error) {
	state := incrementalState{SchemaVersion: incrementalStateSchema, Watermarks: map[string]IncrementalWatermark{}}
	b, err := safepath.ReadFileWithin(m.Root, m.incrementalPath())
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if len(b) > maxIncrementalStateBytes {
		return state, fmt.Errorf("%w: incremental state %s exceeds %d bytes", domain.ErrCheckFailed, m.incrementalPath(), maxIncrementalStateBytes)
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return state, fmt.Errorf("%w: corrupt incremental state %s: %v", domain.ErrCheckFailed, m.incrementalPath(), err)
	}
	if state.SchemaVersion != incrementalStateSchema || state.Watermarks == nil {
		return state, fmt.Errorf("%w: unsupported incremental state schema in %s", domain.ErrCheckFailed, m.incrementalPath())
	}
	if len(state.Watermarks) > maxIncrementalSelectors {
		return state, fmt.Errorf("%w: incremental state %s exceeds %d selectors", domain.ErrCheckFailed, m.incrementalPath(), maxIncrementalSelectors)
	}
	return state, nil
}

// IncrementalWatermark returns a recorded watermark for one service/selector.
func (m *Mirror) IncrementalWatermark(service, selectorSHA256 string) (IncrementalWatermark, bool, error) {
	state, err := m.loadIncrementalState()
	if err != nil {
		return IncrementalWatermark{}, false, err
	}
	value, ok := state.Watermarks[incrementalKey(service, selectorSHA256)]
	return value, ok, nil
}

// SaveIncrementalWatermark atomically merge-patches one selector watermark.
// A separate short lock prevents future backend-specific writers from losing
// each other's entries; the outer service mutation lock remains the long lock.
func (m *Mirror) SaveIncrementalWatermark(value IncrementalWatermark) error {
	if value.Service == "" || value.SelectorSHA256 == "" || value.Selector == "" || value.Since == "" {
		return fmt.Errorf("%w: incomplete incremental watermark", domain.ErrCheckFailed)
	}
	if err := safepath.MkdirAllWithin(m.Root, filepath.Dir(m.incrementalPath()), 0o755); err != nil {
		return err
	}
	var lock *safepath.FileLock
	for attempt := 0; attempt < 20; attempt++ {
		candidate, acquired, err := safepath.TryLockFileWithin(m.Root, m.incrementalLockPath(), 0o600)
		if err != nil {
			return err
		}
		if acquired {
			lock = candidate
			break
		}
		if attempt < 19 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if lock == nil {
		return fmt.Errorf("%w: another incremental state update is active for %s", domain.ErrCheckFailed, m.Root)
	}
	defer func() { _ = lock.Unlock() }()
	state, err := m.loadIncrementalState()
	if err != nil {
		return err
	}
	key := incrementalKey(value.Service, value.SelectorSHA256)
	if _, exists := state.Watermarks[key]; !exists && len(state.Watermarks) >= maxIncrementalSelectors {
		return fmt.Errorf("%w: incremental state reached its %d-selector limit", domain.ErrCheckFailed, maxIncrementalSelectors)
	}
	state.Watermarks[key] = value
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if len(b)+1 > maxIncrementalStateBytes {
		return fmt.Errorf("%w: incremental state would exceed %d bytes; narrow the selector boundary or remove obsolete private watermarks", domain.ErrCheckFailed, maxIncrementalStateBytes)
	}
	return safepath.WriteFileWithin(m.Root, m.incrementalPath(), append(b, '\n'), 0o600)
}
