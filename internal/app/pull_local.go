package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

const (
	pullLocalBlocked        = "blocked"
	pullLocalWouldOverwrite = "would_overwrite"
	pullLocalWouldStash     = "would_stash"
	pullLocalOverwritten    = "overwritten"
	pullLocalStashed        = "stashed"
)

// PullLocalAction is content-free evidence about an explicitly qualified
// local native substrate. Hashes prove the exact bytes without copying Jira or
// Confluence content into command output.
type PullLocalAction struct {
	ID             string `json:"id"`
	Path           string `json:"path"`
	Status         string `json:"status"`
	Reason         string `json:"reason"`
	CurrentSHA256  string `json:"current_sha256,omitempty"`
	BaselineSHA256 string `json:"baseline_sha256,omitempty"`
	StashPath      string `json:"stash_path,omitempty"`
}

// PullLocalSafety qualifies every non-default local overwrite decision. It is
// omitted from ordinary clean pulls so their established JSON remains stable.
type PullLocalSafety struct {
	DryRun      bool              `json:"dry_run"`
	Complete    bool              `json:"complete"`
	Blocked     int               `json:"blocked"`
	ActionCount int               `json:"action_count"`
	Actions     []PullLocalAction `json:"actions"`
}

func newPullLocalSafety(dryRun bool, actions []PullLocalAction) *PullLocalSafety {
	if !dryRun && len(actions) == 0 {
		return nil
	}
	result := &PullLocalSafety{DryRun: dryRun, Complete: true, ActionCount: len(actions), Actions: append([]PullLocalAction{}, actions...)}
	for _, action := range actions {
		if action.Status == pullLocalBlocked {
			result.Blocked++
		}
	}
	result.Complete = result.Blocked == 0
	return result
}

func appendPullLocalBlocked(safety **PullLocalSafety, dryRun bool, action PullLocalAction) {
	action.Status = pullLocalBlocked
	if *safety == nil {
		*safety = newPullLocalSafety(dryRun, []PullLocalAction{action})
		return
	}
	for i := range (*safety).Actions {
		current := &(*safety).Actions[i]
		if current.ID == action.ID && current.Path == action.Path {
			if current.Status != pullLocalBlocked {
				(*safety).Blocked++
			}
			*current = action
			(*safety).Complete = false
			return
		}
	}
	(*safety).Actions = append((*safety).Actions, action)
	(*safety).ActionCount++
	(*safety).Blocked++
	(*safety).Complete = false
}

func pullLocalSafetyError(service string, safety *PullLocalSafety) error {
	if safety == nil || safety.Blocked == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s pull preserved %d locally edited or unqualified native substrate(s); inspect local_safety and choose apply/push, --stash-local, or --overwrite-local explicitly", domain.ErrCheckFailed, service, safety.Blocked)
}

// qualifyPullNative determines whether pull may replace one native substrate.
// It requires the sidecar, pristine base, and on-disk bytes to agree before an
// overwrite is implicit. Explicit recovery flags only qualify a known local
// edit; they never bypass missing/corrupt baseline evidence or path drift.
func qualifyPullNative(m *mirror.Mirror, id, nativePath, ext string, overwrite, stash bool, knownState *mirror.SyncState) (*PullLocalAction, []byte, error) {
	rel, err := filepath.Rel(m.Root, nativePath)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || !safepath.Within(m.Root, nativePath) {
		return nil, nil, fmt.Errorf("%w: native path is outside the mirror root", domain.ErrCheckFailed)
	}
	rel = filepath.Clean(rel)
	state := mirror.SyncState{}
	tracked := knownState != nil
	if tracked {
		state = *knownState
	}
	local, readErr := safepath.ReadFileWithin(m.Root, nativePath)
	localExists := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return &PullLocalAction{ID: id, Path: filepath.ToSlash(rel), Status: pullLocalBlocked, Reason: "local_native_unreadable"}, nil, nil
	}
	if tracked && filepath.Clean(filepath.FromSlash(state.Path)) != rel {
		return &PullLocalAction{ID: id, Path: filepath.ToSlash(rel), Status: pullLocalBlocked, Reason: "tracked_path_changed", BaselineSHA256: state.Hash}, local, nil
	}
	if tracked {
		base, present, baseErr := m.ReadBaseBodyExt(id, ext)
		if baseErr != nil {
			return &PullLocalAction{ID: id, Path: filepath.ToSlash(rel), Status: pullLocalBlocked, Reason: "baseline_unreadable", BaselineSHA256: state.Hash}, local, nil
		}
		if !present || mirror.Hash(base) != state.Hash {
			return &PullLocalAction{ID: id, Path: filepath.ToSlash(rel), Status: pullLocalBlocked, Reason: "baseline_unqualified", BaselineSHA256: state.Hash}, local, nil
		}
		if !localExists {
			return &PullLocalAction{ID: id, Path: filepath.ToSlash(rel), Status: pullLocalBlocked, Reason: "local_native_missing", BaselineSHA256: state.Hash}, nil, nil
		}
		if mirror.Hash(local) == state.Hash {
			return nil, local, nil
		}
	}
	if !localExists {
		return nil, nil, nil
	}

	action := &PullLocalAction{
		ID: id, Path: filepath.ToSlash(rel), Reason: "local_native_modified",
		CurrentSHA256: mirror.Hash(local),
	}
	if tracked {
		action.BaselineSHA256 = state.Hash
	} else {
		action.Reason = "untracked_native_present"
	}
	switch {
	case stash:
		action.Status = pullLocalWouldStash
	case overwrite:
		action.Status = pullLocalWouldOverwrite
	default:
		action.Status = pullLocalBlocked
	}
	return action, local, nil
}

func revalidatePullFile(root, path string, qualified []byte, existed bool, id, kind string) error {
	current, err := safepath.ReadFileWithin(root, path)
	if !existed && os.IsNotExist(err) {
		return nil
	}
	if err != nil || !existed || !bytes.Equal(current, qualified) {
		return fmt.Errorf("%w: %s %s changed after pull qualification; preserving it", domain.ErrCheckFailed, id, kind)
	}
	return nil
}

func pullRelativePath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
