package mirror

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const maxJiraCompletePullSnapshotBytes int64 = 64 << 20

// CompletePullPreviousState binds a Jira key relocation to the exact sidecar
// entry that must be replaced. View is nil only when the previous sidecar had
// no render-state entry.
type CompletePullPreviousState struct {
	State SyncState  `json:"state"`
	View  *ViewState `json:"view,omitempty"`
}

// JiraCompletePullTrackedState is the content-free local evidence needed by
// the app layer to reconstruct an old derived view before a key relocation.
type JiraCompletePullTrackedState struct {
	State     SyncState
	View      ViewState
	ViewFound bool
}

type jiraIssueRelocationArtifact struct {
	artifact CompletePullArtifact
	hash     string
}

// JiraIssueRelocation is an opaque, hash-bound plan. It is accepted only by
// PrepareJiraCompletePullPublication and cannot be used for Confluence.
type JiraIssueRelocation struct {
	identity  string
	previous  CompletePullPreviousState
	retire    []jiraIssueRelocationArtifact
	newAbsent []ArtifactPath
}

type jiraIdentitySnapshot struct {
	Key    string          `json:"key"`
	ID     string          `json:"id"`
	Fields json.RawMessage `json:"fields"`
}

func (m *Mirror) jiraSnapshotIdentity(state SyncState) (string, bool, error) {
	path := strings.TrimSuffix(state.Path, ".wiki") + ".json"
	b, err := safepath.ReadFileWithinLimit(m.Root, filepath.Join(m.Root, filepath.FromSlash(path)), maxJiraCompletePullSnapshotBytes)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var snapshot jiraIdentitySnapshot
	if json.Unmarshal(b, &snapshot) != nil || snapshot.Key != state.ID || !positiveDecimalIdentity(snapshot.ID) || len(snapshot.Fields) == 0 || bytes.Equal(bytes.TrimSpace(snapshot.Fields), []byte("null")) {
		return "", false, fmt.Errorf("%w: tracked Jira snapshot does not prove its key and stable identity", domain.ErrCheckFailed)
	}
	return snapshot.ID, true, nil
}

// JiraCompletePullStateByIdentity resolves one stable numeric Jira identity.
// Legacy sidecars are qualified through their exact local snapshot; ambiguous
// or malformed evidence fails closed.
func (m *Mirror) JiraCompletePullStateByIdentity(identity string) (JiraCompletePullTrackedState, bool, error) {
	if !positiveDecimalIdentity(identity) {
		return JiraCompletePullTrackedState{}, false, fmt.Errorf("%w: Jira identity is not canonical positive decimal", domain.ErrCheckFailed)
	}
	sc, err := m.loadSidecar()
	if err != nil {
		return JiraCompletePullTrackedState{}, false, err
	}
	var result JiraCompletePullTrackedState
	found := false
	for _, state := range sc.Pages {
		if state.Version != 0 || filepath.Ext(filepath.FromSlash(state.Path)) != ".wiki" {
			continue
		}
		candidate := state.Identity
		if candidate == "" {
			var proved bool
			candidate, proved, err = m.jiraSnapshotIdentity(state)
			if err != nil {
				return JiraCompletePullTrackedState{}, false, err
			}
			if !proved {
				continue
			}
		}
		if candidate != identity {
			continue
		}
		if found {
			return JiraCompletePullTrackedState{}, false, fmt.Errorf("%w: more than one tracked Jira key claims the same stable identity", domain.ErrCheckFailed)
		}
		result.State = state
		result.View, result.ViewFound = sc.Views[state.ID]
		found = true
	}
	return result, found, nil
}

// PlanJiraIssueRelocation proves the old key's complete primary representation
// and every owned auxiliary file before the transaction can replace the key.
func (m *Mirror) PlanJiraIssueRelocation(identity string, next SyncState, pristineOldMD []byte) (*JiraIssueRelocation, error) {
	tracked, found, err := m.JiraCompletePullStateByIdentity(identity)
	if err != nil || !found || tracked.State.ID == next.ID {
		return nil, err
	}
	if next.Identity != identity || !positiveDecimalIdentity(identity) {
		return nil, fmt.Errorf("%w: Jira relocation identity does not match the replacement state", domain.ErrCheckFailed)
	}
	if !tracked.ViewFound {
		return nil, fmt.Errorf("%w: Jira relocation source has no recorded render policy", domain.ErrCheckFailed)
	}
	staged, stagedFound, err := m.StagedStateOf(tracked.State.ID)
	if err != nil {
		return nil, err
	}
	if stagedFound {
		return nil, fmt.Errorf("%w: Jira relocation source %s has staged local lineage; apply, push, or reconcile it first", domain.ErrCheckFailed, staged.ID)
	}

	oldStem := strings.TrimSuffix(tracked.State.Path, ".wiki")
	oldPublic := []struct {
		path string
		role CompletePullArtifactRole
	}{
		{tracked.State.Path, CompletePullArtifactRoleNative},
		{oldStem + ".json", CompletePullArtifactRoleMetadata},
		{oldStem + ".md", CompletePullArtifactRoleView},
	}
	plan := &JiraIssueRelocation{identity: identity}
	previous := tracked.State
	view := tracked.View
	plan.previous = CompletePullPreviousState{State: previous, View: &view}
	var snapshot jiraIdentitySnapshot
	for _, owned := range oldPublic {
		path, pathErr := NewPublicArtifactPath(owned.path)
		if pathErr != nil {
			return nil, pathErr
		}
		b, readErr := safepath.ReadFileWithin(m.Root, filepath.Join(m.Root, filepath.FromSlash(owned.path)))
		if readErr != nil {
			return nil, fmt.Errorf("%w: Jira relocation source artifact %s is missing or unreadable", domain.ErrCheckFailed, owned.path)
		}
		switch owned.role {
		case CompletePullArtifactRoleNative:
			if Hash(b) != previous.Hash {
				return nil, fmt.Errorf("%w: Jira relocation source has local native edits", domain.ErrCheckFailed)
			}
		case CompletePullArtifactRoleMetadata:
			if json.Unmarshal(b, &snapshot) != nil || snapshot.Key != previous.ID || snapshot.ID != identity || len(snapshot.Fields) == 0 || bytes.Equal(bytes.TrimSpace(snapshot.Fields), []byte("null")) {
				return nil, fmt.Errorf("%w: Jira relocation snapshot does not prove the old key and stable identity", domain.ErrCheckFailed)
			}
		case CompletePullArtifactRoleView:
			if !bytes.Equal(b, pristineOldMD) {
				return nil, fmt.Errorf("%w: Jira relocation source has unapplied or unqualified Markdown edits", domain.ErrCheckFailed)
			}
		}
		plan.retire = append(plan.retire, jiraIssueRelocationArtifact{
			artifact: CompletePullArtifact{Path: path, Role: owned.role, Remove: true}, hash: Hash(b),
		})
	}
	basePath := filepath.ToSlash(filepath.Join(".atl", "base", previous.ID+".wiki"))
	base, basePathErr := NewPrivateBaseArtifactPath(basePath)
	if basePathErr != nil {
		return nil, basePathErr
	}
	baseBytes, present, err := m.ReadBaseBodyExt(previous.ID, ".wiki")
	if err != nil || !present || Hash(baseBytes) != previous.Hash {
		return nil, fmt.Errorf("%w: Jira relocation source pristine base is missing or changed", domain.ErrCheckFailed)
	}
	plan.retire = append(plan.retire, jiraIssueRelocationArtifact{
		artifact: CompletePullArtifact{Path: base, Role: CompletePullArtifactRoleBase, Remove: true}, hash: Hash(baseBytes),
	})

	epicPath := oldStem + ".epic-children.json"
	if epicBytes, readErr := safepath.ReadFileWithin(m.Root, filepath.Join(m.Root, filepath.FromSlash(epicPath))); readErr == nil {
		var epic struct {
			Epic string `json:"epic"`
		}
		if json.Unmarshal(epicBytes, &epic) != nil || epic.Epic != previous.ID {
			return nil, fmt.Errorf("%w: Jira relocation auxiliary does not prove ownership by the old key", domain.ErrCheckFailed)
		}
		qualified, pathErr := NewPublicArtifactPath(epicPath)
		if pathErr != nil {
			return nil, pathErr
		}
		plan.retire = append(plan.retire, jiraIssueRelocationArtifact{
			artifact: CompletePullArtifact{Path: qualified, Role: CompletePullArtifactRoleAuxiliary, Remove: true}, hash: Hash(epicBytes),
		})
	} else if !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("%w: inspect Jira relocation auxiliary", domain.ErrCheckFailed)
	}
	assetsRel := oldStem + ".assets"
	assetsDir := filepath.Join(m.Root, filepath.FromSlash(assetsRel))
	entries, readDirErr := safepath.ReadDirWithin(m.Root, assetsDir)
	if readDirErr == nil {
		if len(entries) != 0 {
			return nil, fmt.Errorf("%w: Jira relocation asset directory has no ownership inventory", domain.ErrCheckFailed)
		}
	} else if !os.IsNotExist(readDirErr) {
		return nil, fmt.Errorf("%w: inspect Jira relocation asset directory", domain.ErrCheckFailed)
	}
	if len(plan.retire) > maxCompletePullPublicationArtifacts-4 {
		return nil, fmt.Errorf("%w: Jira relocation exceeds the bounded artifact count", domain.ErrCheckFailed)
	}

	newStem := strings.TrimSuffix(next.Path, ".wiki")
	for _, rel := range []string{
		next.Path, newStem + ".json", newStem + ".md", newStem + ".epic-children.json", newStem + ".assets",
		filepath.ToSlash(filepath.Join(".atl", "base", next.ID+".wiki")),
	} {
		qualified, pathErr := parseDurableArtifactPath(rel)
		if pathErr != nil {
			return nil, pathErr
		}
		if _, statErr := safepath.StatWithin(m.Root, filepath.Join(m.Root, filepath.FromSlash(rel))); statErr == nil {
			return nil, fmt.Errorf("%w: Jira relocation target already contains local artifacts", domain.ErrCheckFailed)
		} else if !os.IsNotExist(statErr) {
			return nil, fmt.Errorf("%w: inspect Jira relocation target", domain.ErrCheckFailed)
		}
		plan.newAbsent = append(plan.newAbsent, qualified)
	}
	return plan, nil
}

func (m *Mirror) jiraRelocationArtifacts(plan *JiraIssueRelocation) ([]CompletePullArtifact, error) {
	if plan == nil {
		return nil, nil
	}
	for _, target := range plan.newAbsent {
		if _, err := safepath.StatWithin(m.Root, filepath.Join(m.Root, filepath.FromSlash(target.String()))); err == nil {
			return nil, fmt.Errorf("%w: Jira relocation target appeared after qualification", domain.ErrCheckFailed)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: inspect Jira relocation target", domain.ErrCheckFailed)
		}
	}
	out := make([]CompletePullArtifact, 0, len(plan.retire))
	for _, planned := range plan.retire {
		b, err := safepath.ReadFileWithin(m.Root, filepath.Join(m.Root, filepath.FromSlash(planned.artifact.Path.String())))
		if err != nil || Hash(b) != planned.hash {
			return nil, fmt.Errorf("%w: Jira relocation source changed after qualification", domain.ErrCheckFailed)
		}
		out = append(out, planned.artifact)
	}
	return out, nil
}
