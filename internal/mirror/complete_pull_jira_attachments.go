package mirror

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

type jiraAttachmentCaptureOwnership struct {
	sidecarPath  string
	sidecarHash  string
	metadataHash string
	bodies       map[string]string
}

// JiraAttachmentCaptureBodyReplacementBound proves a same-path prior capture
// and reports the largest number of old body removals a replacement capture
// may need. The caller separately reserves its replacement sidecar, which
// overwrites the old one in place; counting that sidecar as both a replacement
// and a retirement would falsely reject an otherwise admissible transaction.
// It is called before opening optional bodies so a valid GET cannot be the
// first point at which atomic publication is known to be impossible.
func (m *Mirror) JiraAttachmentCaptureBodyReplacementBound(identity string, next SyncState) (int, error) {
	if m == nil || !positiveDecimalIdentity(identity) || next.Identity != identity || !strings.HasSuffix(next.Path, ".wiki") {
		return 0, fmt.Errorf("%w: replacement Jira attachment identity is invalid", domain.ErrCheckFailed)
	}
	previous, found, err := m.JiraCompletePullStateByIdentity(identity)
	if err != nil || !found || filepath.Clean(previous.State.Path) != filepath.Clean(next.Path) {
		return 0, err
	}
	ownership, owned, err := m.qualifiedJiraAttachmentCapture(identity, previous.State)
	if err != nil || !owned {
		return 0, err
	}
	return len(ownership.bodies), nil
}

// readQualifiedJiraPrivateEvidence admits only the bounded private artifact
// format produced by a qualified Jira complete pull. It applies equally to
// comment sidecars, attachment sidecars, and attachment bodies. Its exact
// stat/read snapshot keeps a local replacement from turning recovery or
// retirement accounting into an unbounded allocation or special-file read.
func (m *Mirror) readQualifiedJiraPrivateEvidence(path string, maximum int64) ([]byte, bool, error) {
	if m == nil || maximum < 0 || maximum > maxCompletePullPublicationBytes {
		return nil, false, fmt.Errorf("%w: Jira private evidence bound is invalid", domain.ErrCheckFailed)
	}
	target := filepath.Join(m.Root, filepath.FromSlash(path))
	info, err := safepath.StatWithin(m.Root, target)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() < 0 || info.Size() > maximum {
		return nil, false, fmt.Errorf("%w: Jira private evidence is not a bounded private regular file", domain.ErrCheckFailed)
	}
	data, err := safepath.ReadFileWithinLimit(m.Root, target, info.Size())
	if err != nil {
		return nil, false, fmt.Errorf("%w: inspect Jira private evidence", domain.ErrCheckFailed)
	}
	after, err := safepath.StatWithin(m.Root, target)
	if err != nil || !after.Mode().IsRegular() || after.Mode().Perm() != 0o600 || after.Size() != info.Size() || int64(len(data)) != after.Size() {
		return nil, false, fmt.Errorf("%w: Jira private evidence changed after qualification", domain.ErrCheckFailed)
	}
	return data, true, nil
}

func (m *Mirror) verifyJiraCompletePullAttachmentArtifacts(state SyncState, identity string, maximum int64) (int64, error) {
	if m == nil || !positiveDecimalIdentity(identity) || state.Identity != identity || state.Version != 0 || !strings.HasSuffix(state.Path, ".wiki") || state.Hash == "" ||
		maximum < 0 || maximum > maxCompletePullPublicationBytes {
		return 0, fmt.Errorf("%w: complete-pull Jira attachment state is invalid", domain.ErrCheckFailed)
	}
	stem := strings.TrimSuffix(state.Path, ".wiki")
	sidecarPath := stem + ".attachments.json"
	sidecarData, found, err := m.readQualifiedJiraPrivateEvidence(sidecarPath, MaxAttachmentSidecarPublicationBytes)
	if err != nil || !found {
		return 0, fmt.Errorf("%w: complete-pull Jira attachment sidecar is missing or unsafe", domain.ErrCheckFailed)
	}
	metadataPath := stem + ".json"
	metadata, err := safepath.ReadFileWithinLimit(m.Root, filepath.Join(m.Root, filepath.FromSlash(metadataPath)), maxJiraCompletePullSnapshotBytes)
	if err != nil {
		return 0, fmt.Errorf("%w: complete-pull Jira attachment metadata is unavailable", domain.ErrCheckFailed)
	}
	binding, bound, bindingErr := m.BackendBinding(CorpusSnapshotJira)
	sidecar, decodeErr := DecodeAttachmentSidecarV1(sidecarData)
	if decodeErr != nil || bindingErr != nil || !bound || sidecar.Service != CorpusSnapshotJira || sidecar.OriginSHA256 != binding.OriginSHA256 ||
		sidecar.ParentID != identity || sidecar.ParentVersion != 0 || sidecar.ParentRevision != jiraRelocationRevisionFromMetadata(metadata) ||
		sidecar.NativeSHA256 != state.Hash || sidecar.MetadataSHA256 != Hash(metadata) {
		return 0, fmt.Errorf("%w: complete-pull Jira attachment sidecar is misbound", domain.ErrCheckFailed)
	}
	bodyPrefix := stem + ".attachments/"
	bodies := make(map[string]string)
	bytes, bytesErr := attachmentSidecarCapturedBytes(sidecar, bodyPrefix, maximum, func(path string, size int64, digest string) error {
		body, bodyFound, bodyErr := m.readQualifiedJiraPrivateEvidence(path, size)
		if bodyErr != nil || !bodyFound || int64(len(body)) != size || Hash(body) != digest {
			return fmt.Errorf("jira attachment body is missing or changed")
		}
		bodies[path] = digest
		return nil
	})
	if bytesErr != nil || m.validateJiraAttachmentDirectory(stem, bodies) != nil {
		return 0, fmt.Errorf("%w: complete-pull Jira attachment bodies do not match their sidecar", domain.ErrCheckFailed)
	}
	return bytes, nil
}

// VerifyJiraCompletePullAttachmentBodyBytes reconstructs the durable captured
// body total for the accepted checkpoint prefix. Options bind body policy; this
// independent receipt scan prevents a resumed pull from treating one complete
// clone as multiple fresh aggregate budgets.
func (m *Mirror) VerifyJiraCompletePullAttachmentBodyBytes(checkpoint CompletePullCheckpoint, maximum int64) (int64, error) {
	if m == nil || checkpoint.Service != CompletePullServiceJira || checkpoint.NextIndex < 0 || checkpoint.NextIndex > len(checkpoint.IDs) ||
		maximum < 0 || maximum > maxCompletePullPublicationBytes {
		return 0, fmt.Errorf("%w: complete-pull Jira attachment prefix is invalid", domain.ErrCheckFailed)
	}
	var total int64
	for _, identity := range checkpoint.IDs[:checkpoint.NextIndex] {
		tracked, found, err := m.JiraCompletePullStateByIdentity(identity)
		if err != nil || !found {
			return 0, fmt.Errorf("%w: complete-pull Jira attachment state is missing for its accepted prefix", domain.ErrCheckFailed)
		}
		bytes, err := m.verifyJiraCompletePullAttachmentArtifacts(tracked.State, identity, maximum-total)
		if err != nil || bytes < 0 || bytes > maximum-total {
			return 0, fmt.Errorf("%w: complete-pull Jira attachment body accounting is not bound to its prefix", domain.ErrCheckFailed)
		}
		total += bytes
	}
	return total, nil
}

func (m *Mirror) validateJiraAttachmentDirectory(stem string, expected map[string]string) error {
	dir := filepath.Join(m.Root, filepath.FromSlash(stem+".attachments"))
	entries, err := safepath.ReadDirWithinLimit(m.Root, dir, maxCompletePullPublicationArtifacts)
	if os.IsNotExist(err) {
		if len(expected) == 0 {
			return nil
		}
		return fmt.Errorf("%w: Jira attachment directory is missing", domain.ErrCheckFailed)
	}
	if err != nil || len(entries) != len(expected) {
		return fmt.Errorf("%w: Jira attachment directory differs from its ownership inventory", domain.ErrCheckFailed)
	}
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return fmt.Errorf("%w: Jira attachment directory contains an unsafe entry", domain.ErrCheckFailed)
		}
		if _, ok := expected[stem+".attachments/"+entry.Name()]; !ok {
			return fmt.Errorf("%w: Jira attachment directory contains an unowned entry", domain.ErrCheckFailed)
		}
	}
	return nil
}

// PlanJiraAttachmentCaptureRetirements binds every same-path replacement to
// the prior private attachment receipt. A new sidecar replaces the old sidecar
// in place and retires only bodies that it no longer retains; an ordinary
// complete refresh with no new sidecar retires the whole owned capture. An
// unexplained .attachments directory never grants deletion authority.
func (m *Mirror) PlanJiraAttachmentCaptureRetirements(
	identity string,
	next SyncState,
	nextMetadata []byte,
	nextArtifacts []CompletePullArtifact,
	retireWhenUnselected bool,
) ([]CompletePullArtifact, error) {
	if m == nil || !positiveDecimalIdentity(identity) || next.Identity != identity || next.Version != 0 || !strings.HasSuffix(next.Path, ".wiki") || next.Hash == "" {
		return nil, fmt.Errorf("%w: replacement Jira attachment identity is invalid", domain.ErrCheckFailed)
	}
	previous, found, err := m.JiraCompletePullStateByIdentity(identity)
	if err != nil || !found {
		return nil, err
	}
	ownership, owned, err := m.qualifiedJiraAttachmentCapture(identity, previous.State)
	if err != nil || !owned {
		return nil, err
	}
	if filepath.Clean(previous.State.Path) != filepath.Clean(next.Path) {
		// Only a complete pull has the hash-bound relocation plan that can
		// retire the old receipt and move the primary issue atomically. The
		// ordinary path must refuse before it writes the new key; otherwise an
		// interrupted or failed post-write relocation strands private bodies at
		// the old path with no current primary state that can prove ownership.
		return nil, fmt.Errorf("%w: Jira attachment capture key relocation requires a complete pull", domain.ErrCheckFailed)
	}
	nextBodies, nextHasSidecar, err := m.nextJiraAttachmentBodies(identity, next, nextMetadata, nextArtifacts)
	if err != nil {
		return nil, err
	}
	removals := make([]CompletePullArtifact, 0, len(ownership.bodies)+1)
	if !nextHasSidecar && !retireWhenUnselected && previous.State == next && ownership.metadataHash == Hash(nextMetadata) {
		// An ordinary pull may reproduce the exact primary receipt without
		// rewriting private evidence. It remains valid and must not be retired.
		return removals, nil
	}
	if !nextHasSidecar {
		removal, removeErr := m.ownedJiraAttachmentRemoval(ownership.sidecarPath, ownership.sidecarHash, MaxAttachmentSidecarPublicationBytes)
		if removeErr != nil {
			return nil, removeErr
		}
		removals = append(removals, removal)
	}
	for path, hash := range ownership.bodies {
		if nextHasSidecar {
			if _, retained := nextBodies[path]; retained {
				continue
			}
		}
		removal, removeErr := m.ownedJiraAttachmentRemoval(path, hash, maxCompletePullPublicationBytes)
		if removeErr != nil {
			return nil, removeErr
		}
		removals = append(removals, removal)
	}
	sort.Slice(removals, func(i, j int) bool { return removals[i].Path.String() < removals[j].Path.String() })
	return removals, nil
}

func (m *Mirror) nextJiraAttachmentBodies(identity string, next SyncState, metadata []byte, artifacts []CompletePullArtifact) (map[string]string, bool, error) {
	stem := strings.TrimSuffix(next.Path, ".wiki")
	sidecarPath := stem + ".attachments.json"
	var sidecarData []byte
	for _, artifact := range artifacts {
		if artifact.Path.String() != sidecarPath || artifact.Remove {
			continue
		}
		if sidecarData != nil || artifact.BestEffort || artifact.Mode != 0o600 {
			return nil, false, fmt.Errorf("%w: replacement Jira attachment sidecar is invalid", domain.ErrCheckFailed)
		}
		sidecarData = artifact.Data
	}
	if sidecarData == nil {
		for _, artifact := range artifacts {
			if strings.HasPrefix(artifact.Path.String(), stem+".attachments/") && !artifact.Remove {
				return nil, false, fmt.Errorf("%w: replacement Jira attachment body has no sidecar", domain.ErrCheckFailed)
			}
		}
		return nil, false, nil
	}
	if len(sidecarData) > MaxAttachmentSidecarPublicationBytes {
		return nil, false, fmt.Errorf("%w: replacement Jira attachment sidecar exceeds its bound", domain.ErrCheckFailed)
	}
	sidecar, err := DecodeAttachmentSidecarV1(sidecarData)
	binding, bound, bindingErr := m.BackendBinding(CorpusSnapshotJira)
	if err != nil || bindingErr != nil || !bound || sidecar.Service != CorpusSnapshotJira || sidecar.OriginSHA256 != binding.OriginSHA256 ||
		sidecar.ParentID != identity || sidecar.ParentVersion != 0 || sidecar.ParentRevision != jiraRelocationRevisionFromMetadata(metadata) ||
		sidecar.NativeSHA256 != next.Hash || sidecar.MetadataSHA256 != Hash(metadata) {
		return nil, false, fmt.Errorf("%w: replacement Jira attachment sidecar is misbound", domain.ErrCheckFailed)
	}
	bodyArtifacts := make(map[string]CompletePullArtifact)
	bodyPrefix := stem + ".attachments/"
	for _, artifact := range artifacts {
		if !strings.HasPrefix(artifact.Path.String(), bodyPrefix) || artifact.Remove {
			continue
		}
		if artifact.BestEffort || artifact.Mode != 0o600 {
			return nil, false, fmt.Errorf("%w: replacement Jira attachment body is invalid", domain.ErrCheckFailed)
		}
		if _, duplicate := bodyArtifacts[artifact.Path.String()]; duplicate {
			return nil, false, fmt.Errorf("%w: replacement Jira attachment body is duplicated", domain.ErrCheckFailed)
		}
		bodyArtifacts[artifact.Path.String()] = artifact
	}
	bodies := make(map[string]string)
	_, bytesErr := attachmentSidecarCapturedBytes(sidecar, bodyPrefix, maxCompletePullPublicationBytes, func(path string, size int64, digest string) error {
		artifact, found := bodyArtifacts[path]
		if !found || int64(len(artifact.Data)) != size || Hash(artifact.Data) != digest {
			return fmt.Errorf("replacement Jira attachment body does not match its sidecar")
		}
		delete(bodyArtifacts, path)
		bodies[path] = digest
		return nil
	})
	if bytesErr != nil || len(bodyArtifacts) != 0 {
		return nil, false, fmt.Errorf("%w: replacement Jira attachment bodies are not declared by their sidecar", domain.ErrCheckFailed)
	}
	return bodies, true, nil
}

func (m *Mirror) qualifiedJiraAttachmentCapture(identity string, previous SyncState) (jiraAttachmentCaptureOwnership, bool, error) {
	if !strings.HasSuffix(previous.Path, ".wiki") || previous.Version != 0 || previous.Hash == "" || (previous.Identity != "" && previous.Identity != identity) {
		return jiraAttachmentCaptureOwnership{}, false, fmt.Errorf("%w: existing Jira attachment identity is invalid", domain.ErrCheckFailed)
	}
	stem := strings.TrimSuffix(previous.Path, ".wiki")
	sidecarPath := stem + ".attachments.json"
	sidecarData, found, err := m.readQualifiedJiraPrivateEvidence(sidecarPath, MaxAttachmentSidecarPublicationBytes)
	if err != nil {
		return jiraAttachmentCaptureOwnership{}, false, err
	}
	if !found {
		if err := m.validateAbsentJiraAttachmentSidecar(stem); err != nil {
			return jiraAttachmentCaptureOwnership{}, false, err
		}
		return jiraAttachmentCaptureOwnership{}, false, nil
	}
	metadata, err := safepath.ReadFileWithinLimit(m.Root, filepath.Join(m.Root, filepath.FromSlash(stem+".json")), maxJiraCompletePullSnapshotBytes)
	if err != nil {
		return jiraAttachmentCaptureOwnership{}, false, fmt.Errorf("%w: inspect prior Jira attachment metadata", domain.ErrCheckFailed)
	}
	binding, bound, bindingErr := m.BackendBinding(CorpusSnapshotJira)
	sidecar, decodeErr := DecodeAttachmentSidecarV1(sidecarData)
	if decodeErr != nil || bindingErr != nil || !bound || sidecar.Service != CorpusSnapshotJira || sidecar.OriginSHA256 != binding.OriginSHA256 ||
		sidecar.ParentID != identity || sidecar.ParentVersion != 0 || sidecar.ParentRevision != jiraRelocationRevisionFromMetadata(metadata) ||
		sidecar.NativeSHA256 != previous.Hash || sidecar.MetadataSHA256 != Hash(metadata) {
		return jiraAttachmentCaptureOwnership{}, false, fmt.Errorf("%w: prior Jira attachment sidecar does not prove ownership", domain.ErrCheckFailed)
	}
	bodies := make(map[string]string)
	_, bytesErr := attachmentSidecarCapturedBytes(sidecar, stem+".attachments/", maxCompletePullPublicationBytes, func(path string, size int64, digest string) error {
		body, bodyFound, bodyErr := m.readQualifiedJiraPrivateEvidence(path, size)
		if bodyErr != nil || !bodyFound || int64(len(body)) != size || Hash(body) != digest {
			return fmt.Errorf("prior Jira attachment body is missing or changed")
		}
		bodies[path] = digest
		return nil
	})
	if bytesErr != nil || m.validateJiraAttachmentDirectory(stem, bodies) != nil {
		return jiraAttachmentCaptureOwnership{}, false, fmt.Errorf("%w: prior Jira attachment capture is incomplete", domain.ErrCheckFailed)
	}
	return jiraAttachmentCaptureOwnership{
		sidecarPath: sidecarPath, sidecarHash: Hash(sidecarData), metadataHash: Hash(metadata), bodies: bodies,
	}, true, nil
}

func (m *Mirror) ownedJiraAttachmentRemoval(path, expectedHash string, maximum int64) (CompletePullArtifact, error) {
	qualified, err := NewPublicArtifactPath(path)
	if err != nil {
		return CompletePullArtifact{}, err
	}
	data, found, err := m.readQualifiedJiraPrivateEvidence(qualified.String(), maximum)
	if err != nil || !found || Hash(data) != expectedHash {
		return CompletePullArtifact{}, fmt.Errorf("%w: prior Jira attachment artifact changed after qualification", domain.ErrCheckFailed)
	}
	pre := completePullPublicationPreState{Present: true, SHA256: Hash(data), Mode: 0o600}
	removal := completePullBoundRemovalWithSize(qualified, pre, int64(len(data)))
	removal.Role = CompletePullArtifactRoleAuxiliary
	return removal, nil
}

func (m *Mirror) validateAbsentJiraAttachmentSidecar(stem string) error {
	entries, err := safepath.ReadDirWithinLimit(m.Root, filepath.Join(m.Root, filepath.FromSlash(stem+".attachments")), maxCompletePullPublicationArtifacts)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || len(entries) != 0 {
		return fmt.Errorf("%w: Jira attachment directory has no ownership sidecar", domain.ErrCheckFailed)
	}
	return nil
}

func jiraRelocationRevisionFromMetadata(data []byte) string {
	var snapshot jiraIdentitySnapshot
	if json.Unmarshal(data, &snapshot) != nil {
		return ""
	}
	return jiraRelocationRevision(snapshot.Fields)
}
