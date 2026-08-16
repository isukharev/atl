package mirror

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

type jiraCommentsCaptureOwnership struct {
	path         string
	hash         string
	metadataHash string
}

// JiraCommentCaptureRetirementBound proves the current same-path comments
// receipt and returns the number of separate removals it needs. Callers use
// this while reserving an attachment-body transaction so disabling comments
// cannot make a body GET the first point at which publication is impossible.
func (m *Mirror) JiraCommentCaptureRetirementBound(
	identity string,
	next SyncState,
	nextMetadata []byte,
	nextArtifacts []CompletePullArtifact,
	retireWhenUnselected bool,
) (int, error) {
	retirements, err := m.PlanJiraCommentCaptureRetirements(identity, next, nextMetadata, nextArtifacts, retireWhenUnselected)
	if err != nil {
		return 0, err
	}
	return len(retirements), nil
}

// PlanJiraCommentCaptureRetirements binds a complete refresh to an existing
// private comments receipt. A replacement sidecar overwrites the current one
// in the same atomic publication; disabling qualified comments removes an
// owned sidecar only when the primary receipt changed. Key relocation is
// intentionally rejected here: only the complete relocation transaction can
// prove and retire the old path atomically.
func (m *Mirror) PlanJiraCommentCaptureRetirements(
	identity string,
	next SyncState,
	nextMetadata []byte,
	nextArtifacts []CompletePullArtifact,
	retireWhenUnselected bool,
) ([]CompletePullArtifact, error) {
	if m == nil || !positiveDecimalIdentity(identity) || next.Identity != identity || next.Version != 0 ||
		!strings.HasSuffix(next.Path, ".wiki") || next.Hash == "" {
		return nil, fmt.Errorf("%w: replacement Jira comments identity is invalid", domain.ErrCheckFailed)
	}
	previous, found, err := m.JiraCompletePullStateByIdentity(identity)
	if err != nil || !found {
		return nil, err
	}
	ownership, owned, err := m.qualifiedJiraCommentsCapture(identity, previous.State)
	if err != nil || !owned {
		return nil, err
	}
	if filepath.Clean(previous.State.Path) != filepath.Clean(next.Path) {
		return nil, fmt.Errorf("%w: Jira comments capture key relocation requires a complete pull", domain.ErrCheckFailed)
	}
	nextHasSidecar, err := m.nextJiraCommentsSidecar(identity, next, nextMetadata, nextArtifacts)
	if err != nil {
		return nil, err
	}
	if nextHasSidecar {
		return nil, nil
	}
	if !retireWhenUnselected && previous.State == next && ownership.metadataHash == Hash(nextMetadata) {
		// A byte-identical ordinary refresh does not invalidate the existing
		// receipt and therefore needs neither a removal nor a complete pull.
		return nil, nil
	}
	path, err := NewPublicArtifactPath(ownership.path)
	if err != nil {
		return nil, err
	}
	data, present, err := m.readQualifiedJiraPrivateEvidence(path.String(), maxCompletePullPublicationBytes)
	if err != nil || !present || Hash(data) != ownership.hash {
		return nil, fmt.Errorf("%w: prior Jira comments sidecar changed after qualification", domain.ErrCheckFailed)
	}
	pre := completePullPublicationPreState{Present: true, SHA256: Hash(data), Mode: 0o600}
	removal := completePullBoundRemovalWithSize(path, pre, int64(len(data)))
	removal.Role = CompletePullArtifactRoleAuxiliary
	return []CompletePullArtifact{removal}, nil
}

func (m *Mirror) nextJiraCommentsSidecar(
	identity string,
	next SyncState,
	metadata []byte,
	artifacts []CompletePullArtifact,
) (bool, error) {
	stem := strings.TrimSuffix(next.Path, ".wiki")
	commentsPath := stem + ".comments.json"
	var sidecarData []byte
	for _, artifact := range artifacts {
		if artifact.Path.String() != commentsPath {
			continue
		}
		if artifact.Remove || sidecarData != nil || artifact.BestEffort || artifact.Mode != 0o600 {
			return false, fmt.Errorf("%w: replacement Jira comments sidecar is invalid", domain.ErrCheckFailed)
		}
		sidecarData = artifact.Data
	}
	if sidecarData == nil {
		return false, nil
	}
	if len(sidecarData) > maxCompletePullPublicationBytes {
		return false, fmt.Errorf("%w: replacement Jira comments sidecar exceeds its bound", domain.ErrCheckFailed)
	}
	sidecar, err := DecodeJiraCommentsSidecarV1(sidecarData)
	binding, bound, bindingErr := m.BackendBinding(CorpusSnapshotJira)
	if err != nil || bindingErr != nil || !bound || sidecar.OriginSHA256 != binding.OriginSHA256 ||
		sidecar.ParentID != identity || sidecar.ParentRevision != jiraRelocationRevisionFromMetadata(metadata) ||
		sidecar.NativeSHA256 != next.Hash || sidecar.MetadataSHA256 != Hash(metadata) {
		return false, fmt.Errorf("%w: replacement Jira comments sidecar is misbound", domain.ErrCheckFailed)
	}
	return true, nil
}

func (m *Mirror) qualifiedJiraCommentsCapture(identity string, previous SyncState) (jiraCommentsCaptureOwnership, bool, error) {
	if !strings.HasSuffix(previous.Path, ".wiki") || previous.Version != 0 || previous.Hash == "" ||
		(previous.Identity != "" && previous.Identity != identity) {
		return jiraCommentsCaptureOwnership{}, false, fmt.Errorf("%w: existing Jira comments identity is invalid", domain.ErrCheckFailed)
	}
	stem := strings.TrimSuffix(previous.Path, ".wiki")
	path := stem + ".comments.json"
	data, found, err := m.readQualifiedJiraPrivateEvidence(path, maxCompletePullPublicationBytes)
	if err != nil || !found {
		return jiraCommentsCaptureOwnership{}, found, err
	}
	metadata, err := safepath.ReadFileWithinLimit(m.Root, filepath.Join(m.Root, filepath.FromSlash(stem+".json")), maxJiraCompletePullSnapshotBytes)
	if err != nil {
		return jiraCommentsCaptureOwnership{}, false, fmt.Errorf("%w: inspect prior Jira comments metadata", domain.ErrCheckFailed)
	}
	binding, bound, bindingErr := m.BackendBinding(CorpusSnapshotJira)
	sidecar, decodeErr := DecodeJiraCommentsSidecarV1(data)
	if decodeErr != nil || bindingErr != nil || !bound || sidecar.OriginSHA256 != binding.OriginSHA256 ||
		sidecar.ParentID != identity || sidecar.ParentRevision != jiraRelocationRevisionFromMetadata(metadata) ||
		sidecar.NativeSHA256 != previous.Hash || sidecar.MetadataSHA256 != Hash(metadata) {
		return jiraCommentsCaptureOwnership{}, false, fmt.Errorf("%w: prior Jira comments sidecar does not prove ownership", domain.ErrCheckFailed)
	}
	return jiraCommentsCaptureOwnership{path: path, hash: Hash(data), metadataHash: Hash(metadata)}, true, nil
}
