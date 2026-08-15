package mirror

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

type confluenceAttachmentCaptureOwnership struct {
	sidecarPath  string
	sidecarHash  string
	metadataHash string
	bodies       map[string]string
}

// ConfluenceAttachmentBodyRetirementBound proves the currently retained
// same-path capture and returns the maximum number of body removals a
// replacement sidecar could require. The final retirement may retain a subset,
// but reserving this bound before downloads prevents a valid body GET from
// reaching a complete-pull transaction that cannot publish it.
func (m *Mirror) ConfluenceAttachmentBodyRetirementBound(id, nextPath string) (int, error) {
	if m == nil || id == "" || !strings.HasSuffix(nextPath, ".csf") {
		return 0, fmt.Errorf("%w: replacement Confluence attachment identity is invalid", domain.ErrCheckFailed)
	}
	previous, found, err := m.SyncStateOf(id)
	if err != nil || !found || filepath.Clean(previous.Path) != filepath.Clean(nextPath) {
		return 0, err
	}
	ownership, owned, ownershipErr := m.qualifiedConfluenceAttachmentCapture(id, previous)
	if ownershipErr != nil || !owned {
		return 0, ownershipErr
	}
	return len(ownership.bodies), nil
}

// PlanConfluenceAttachmentBodyRetirements proves ownership of bodies published
// for the current canonical Confluence page before retiring only those that a
// replacement attachment sidecar no longer retains. The returned removals are
// ordinary complete-pull artifacts, so their pre-images and crash recovery are
// bound into the same publication as the replacement sidecar.
//
// It intentionally does nothing across a page relocation: PageRelocation owns
// retirement of the old path. A missing sidecar never grants ownership of an
// attachment directory; non-empty unexplained residue fails closed.
func (m *Mirror) PlanConfluenceAttachmentBodyRetirements(
	id string,
	next SyncState,
	nextMetadata []byte,
	nextArtifacts []CompletePullArtifact,
) ([]CompletePullArtifact, error) {
	if m == nil || id == "" || next.ID != id || !strings.HasSuffix(next.Path, ".csf") || next.Hash == "" {
		return nil, fmt.Errorf("%w: replacement Confluence attachment identity is invalid", domain.ErrCheckFailed)
	}
	nextStem := strings.TrimSuffix(next.Path, ".csf")
	nextBodies, err := m.validateNextConfluenceAttachmentBodies(id, next, nextMetadata, nextStem, nextArtifacts)
	if err != nil {
		return nil, err
	}

	previous, found, err := m.SyncStateOf(id)
	if err != nil || !found || filepath.Clean(previous.Path) != filepath.Clean(next.Path) {
		return nil, err
	}
	ownership, owned, ownershipErr := m.qualifiedConfluenceAttachmentCapture(id, previous)
	if ownershipErr != nil || !owned {
		return nil, ownershipErr
	}

	removals := make([]CompletePullArtifact, 0, len(ownership.bodies))
	for path, hash := range ownership.bodies {
		if _, retained := nextBodies[path]; retained {
			continue
		}
		removal, removalErr := m.ownedConfluenceAttachmentRemoval(path, hash)
		if removalErr != nil {
			return nil, removalErr
		}
		removals = append(removals, removal)
	}
	sort.Slice(removals, func(i, j int) bool { return removals[i].Path.String() < removals[j].Path.String() })
	return removals, nil
}

// PlanConfluenceAttachmentCaptureRetirements returns a complete-pull-bound
// retirement for every owned part of a prior attachment capture when the next
// native or metadata identity no longer matches it. It deliberately makes no
// cross-path decision: PageRelocation has the stronger old-path ownership plan
// needed for a title or hierarchy move.
func (m *Mirror) PlanConfluenceAttachmentCaptureRetirements(id string, next SyncState, nextMetadata []byte) ([]CompletePullArtifact, error) {
	if m == nil || id == "" || next.ID != id || !strings.HasSuffix(next.Path, ".csf") || next.Version <= 0 || next.Hash == "" {
		return nil, fmt.Errorf("%w: replacement Confluence attachment identity is invalid", domain.ErrCheckFailed)
	}
	previous, found, err := m.SyncStateOf(id)
	if err != nil || !found || filepath.Clean(previous.Path) != filepath.Clean(next.Path) {
		return nil, err
	}
	ownership, owned, ownershipErr := m.qualifiedConfluenceAttachmentCapture(id, previous)
	if ownershipErr != nil || !owned {
		return nil, ownershipErr
	}
	if previous == next && ownership.metadataHash == Hash(nextMetadata) {
		return nil, nil
	}
	removals := make([]CompletePullArtifact, 0, len(ownership.bodies)+1)
	sidecar, sidecarErr := m.ownedConfluenceAttachmentRemoval(ownership.sidecarPath, ownership.sidecarHash)
	if sidecarErr != nil {
		return nil, sidecarErr
	}
	removals = append(removals, sidecar)
	for path, hash := range ownership.bodies {
		removal, removalErr := m.ownedConfluenceAttachmentRemoval(path, hash)
		if removalErr != nil {
			return nil, removalErr
		}
		removals = append(removals, removal)
	}
	sort.Slice(removals, func(i, j int) bool { return removals[i].Path.String() < removals[j].Path.String() })
	return removals, nil
}

func (m *Mirror) validateNextConfluenceAttachmentBodies(
	id string,
	next SyncState,
	nextMetadata []byte,
	stem string,
	artifacts []CompletePullArtifact,
) (map[string]string, error) {
	sidecarPath := stem + ".attachments.json"
	var sidecarData []byte
	for _, artifact := range artifacts {
		if artifact.Path.String() != sidecarPath {
			continue
		}
		if artifact.Remove || artifact.BestEffort || artifact.Mode != 0o600 || sidecarData != nil {
			return nil, fmt.Errorf("%w: replacement Confluence attachment sidecar is invalid", domain.ErrCheckFailed)
		}
		sidecarData = artifact.Data
	}
	if sidecarData == nil || len(sidecarData) > maxCompletePullPublicationIntent {
		return nil, fmt.Errorf("%w: replacement Confluence attachment sidecar is unavailable", domain.ErrCheckFailed)
	}
	sidecar, err := DecodeAttachmentSidecarV1(sidecarData)
	if err != nil {
		return nil, fmt.Errorf("%w: replacement Confluence attachment sidecar is invalid", domain.ErrCheckFailed)
	}
	binding, found, bindingErr := m.BackendBinding(CorpusSnapshotConfluence)
	if bindingErr != nil || !found || sidecar.Service != CorpusSnapshotConfluence || sidecar.OriginSHA256 != binding.OriginSHA256 ||
		sidecar.ParentID != id || sidecar.ParentVersion != next.Version || sidecar.ParentRevision != "" ||
		sidecar.NativeSHA256 != next.Hash || sidecar.MetadataSHA256 != Hash(nextMetadata) {
		return nil, fmt.Errorf("%w: replacement Confluence attachment sidecar is misbound", domain.ErrCheckFailed)
	}
	bodyArtifacts := make(map[string]CompletePullArtifact, len(artifacts))
	bodyPrefix := stem + ".attachments/"
	for _, artifact := range artifacts {
		path := artifact.Path.String()
		if !strings.HasPrefix(path, bodyPrefix) {
			continue
		}
		if artifact.Remove || artifact.BestEffort || artifact.Mode != 0o600 {
			return nil, fmt.Errorf("%w: replacement Confluence attachment body artifact is invalid", domain.ErrCheckFailed)
		}
		if _, duplicate := bodyArtifacts[path]; duplicate {
			return nil, fmt.Errorf("%w: replacement Confluence attachment body artifact is duplicated", domain.ErrCheckFailed)
		}
		bodyArtifacts[path] = artifact
	}
	bodies := make(map[string]string, len(sidecar.Attachments))
	for _, attachment := range sidecar.Attachments {
		if attachment.Body.State != AttachmentBodyCaptured {
			continue
		}
		path := attachment.Body.Path
		if path != bodyPrefix+attachment.ID+".body" {
			return nil, fmt.Errorf("%w: replacement Confluence attachment body is outside its page", domain.ErrCheckFailed)
		}
		artifact, found := bodyArtifacts[path]
		if !found || int64(len(artifact.Data)) != attachment.Body.Size || Hash(artifact.Data) != attachment.Body.SHA256 {
			return nil, fmt.Errorf("%w: replacement Confluence attachment body does not match its sidecar", domain.ErrCheckFailed)
		}
		delete(bodyArtifacts, path)
		bodies[path] = attachment.Body.SHA256
	}
	if len(bodyArtifacts) != 0 {
		return nil, fmt.Errorf("%w: replacement Confluence attachment body is not declared by its sidecar", domain.ErrCheckFailed)
	}
	return bodies, nil
}

func (m *Mirror) validateExistingConfluenceAttachmentBodies(
	id string,
	previous SyncState,
	stem string,
	metadata, sidecarData []byte,
) (map[string]string, error) {
	sidecar, err := DecodeAttachmentSidecarV1(sidecarData)
	if err != nil {
		return nil, fmt.Errorf("%w: prior Confluence attachment sidecar is invalid", domain.ErrCheckFailed)
	}
	binding, found, bindingErr := m.BackendBinding(CorpusSnapshotConfluence)
	if bindingErr != nil || !found || sidecar.Service != CorpusSnapshotConfluence || sidecar.OriginSHA256 != binding.OriginSHA256 ||
		sidecar.ParentID != id || sidecar.ParentVersion != previous.Version || sidecar.ParentRevision != "" ||
		sidecar.NativeSHA256 != previous.Hash || sidecar.MetadataSHA256 != Hash(metadata) {
		return nil, fmt.Errorf("%w: prior Confluence attachment evidence does not prove ownership", domain.ErrCheckFailed)
	}
	bodies := make(map[string]string, len(sidecar.Attachments))
	bodyPrefix := stem + ".attachments/"
	var total int64
	for _, attachment := range sidecar.Attachments {
		if attachment.Body.State != AttachmentBodyCaptured {
			continue
		}
		if len(bodies) >= maxCompletePullPublicationArtifacts {
			return nil, fmt.Errorf("%w: prior Confluence attachment body inventory exceeds the publication bound", domain.ErrCheckFailed)
		}
		path := attachment.Body.Path
		if path != bodyPrefix+attachment.ID+".body" {
			return nil, fmt.Errorf("%w: prior Confluence attachment body is outside its page", domain.ErrCheckFailed)
		}
		if attachment.Body.Size < 0 || attachment.Body.Size > maxCompletePullPublicationBytes-total {
			return nil, fmt.Errorf("%w: prior Confluence attachment body exceeds the publication bound", domain.ErrCheckFailed)
		}
		body, readErr := m.readQualifiedConfluenceAttachmentBody(path, attachment.Body.Size)
		if readErr != nil || int64(len(body)) != attachment.Body.Size || Hash(body) != attachment.Body.SHA256 {
			return nil, fmt.Errorf("%w: prior Confluence attachment body is missing or changed", domain.ErrCheckFailed)
		}
		total += attachment.Body.Size
		bodies[path] = attachment.Body.SHA256
	}
	return bodies, nil
}

func (m *Mirror) qualifiedConfluenceAttachmentCapture(id string, previous SyncState) (confluenceAttachmentCaptureOwnership, bool, error) {
	if !strings.HasSuffix(previous.Path, ".csf") || previous.Version <= 0 || previous.Hash == "" {
		return confluenceAttachmentCaptureOwnership{}, false, fmt.Errorf("%w: existing Confluence attachment identity is invalid", domain.ErrCheckFailed)
	}
	previousStem := strings.TrimSuffix(previous.Path, ".csf")
	previousSidecarPath := previousStem + ".attachments.json"
	sidecarCurrent, previousSidecar, found, sidecarErr := m.readQualifiedConfluenceAttachmentSidecar(previousSidecarPath)
	if sidecarErr != nil {
		return confluenceAttachmentCaptureOwnership{}, false, sidecarErr
	}
	if !found {
		if err := m.validateAbsentConfluenceAttachmentSidecar(previousStem); err != nil {
			return confluenceAttachmentCaptureOwnership{}, false, err
		}
		return confluenceAttachmentCaptureOwnership{}, false, nil
	}
	previousMetadataPath := previousStem + ".meta.json"
	previousMetadata, metadataErr := safepath.ReadFileWithinLimit(
		m.Root, filepath.Join(m.Root, filepath.FromSlash(previousMetadataPath)), maxCompletePullPublicationIntent,
	)
	if metadataErr != nil {
		return confluenceAttachmentCaptureOwnership{}, false, fmt.Errorf("%w: inspect prior Confluence attachment metadata", domain.ErrCheckFailed)
	}
	previousBodies, err := m.validateExistingConfluenceAttachmentBodies(id, previous, previousStem, previousMetadata, previousSidecar)
	if err != nil {
		return confluenceAttachmentCaptureOwnership{}, false, err
	}
	if err := m.validateConfluenceAttachmentDirectory(previousStem, previousBodies); err != nil {
		return confluenceAttachmentCaptureOwnership{}, false, err
	}
	return confluenceAttachmentCaptureOwnership{
		sidecarPath:  previousSidecarPath,
		sidecarHash:  sidecarCurrent.SHA256,
		metadataHash: Hash(previousMetadata),
		bodies:       previousBodies,
	}, true, nil
}

// readQualifiedConfluenceAttachmentPrivate reads one private attachment
// artifact through an exact pre-read size snapshot. It intentionally does not
// use the general publication preimage reader: this path runs before optional
// attachment downloads and must not let a late local replacement enlarge a
// sidecar/body allocation beyond its qualified bound.
func (m *Mirror) readQualifiedConfluenceAttachmentPrivate(path string, maximum int64) (completePullPublicationPreState, []byte, bool, error) {
	if maximum < 0 || maximum > maxCompletePullPublicationBytes {
		return completePullPublicationPreState{}, nil, false, fmt.Errorf("%w: prior Confluence attachment artifact bound is invalid", domain.ErrCheckFailed)
	}
	target := confluenceAttachmentCaptureTarget(m.Root, path)
	info, err := safepath.StatWithin(m.Root, target)
	if os.IsNotExist(err) {
		return completePullPublicationPreState{}, nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() < 0 || info.Size() > maximum {
		return completePullPublicationPreState{}, nil, false, fmt.Errorf("%w: prior Confluence attachment artifact is not a bounded private regular file", domain.ErrCheckFailed)
	}
	data, err := safepath.ReadFileWithinLimit(m.Root, target, info.Size())
	if err != nil {
		return completePullPublicationPreState{}, nil, false, fmt.Errorf("%w: inspect prior Confluence attachment artifact", domain.ErrCheckFailed)
	}
	// Recheck the file attributes after the bounded read. A later replacement is
	// also rejected by the hash-bound removal preimage, but this prevents a
	// changed path from being trusted as capture evidence during planning.
	after, afterErr := safepath.StatWithin(m.Root, target)
	if afterErr != nil || !after.Mode().IsRegular() || after.Mode().Perm() != 0o600 || after.Size() != info.Size() || int64(len(data)) != after.Size() {
		return completePullPublicationPreState{}, nil, false, fmt.Errorf("%w: prior Confluence attachment artifact changed after qualification", domain.ErrCheckFailed)
	}
	current := completePullPublicationPreState{Present: true, SHA256: Hash(data), Mode: uint32(after.Mode().Perm())}
	return current, data, true, nil
}

// readQualifiedConfluenceAttachmentSidecar grants retirement ownership only to
// a bounded, regular private sidecar.
func (m *Mirror) readQualifiedConfluenceAttachmentSidecar(path string) (completePullPublicationPreState, []byte, bool, error) {
	return m.readQualifiedConfluenceAttachmentPrivate(path, maxCompletePullPublicationIntent)
}

// readQualifiedConfluenceAttachmentBody reads one captured body only after
// checking that the contained file remains the regular private file described
// by its sidecar. Callers supply a previously aggregate-bounded expected size,
// so a hostile sidecar cannot turn recovery or retirement admission into an
// unbounded allocation or special-file read.
func (m *Mirror) readQualifiedConfluenceAttachmentBody(path string, expectedSize int64) ([]byte, error) {
	if expectedSize < 0 || expectedSize > maxCompletePullPublicationBytes {
		return nil, fmt.Errorf("%w: prior Confluence attachment body exceeds the publication bound", domain.ErrCheckFailed)
	}
	_, data, found, err := m.readQualifiedConfluenceAttachmentPrivate(path, expectedSize)
	if err != nil || !found || int64(len(data)) != expectedSize {
		return nil, fmt.Errorf("%w: prior Confluence attachment body changed after qualification", domain.ErrCheckFailed)
	}
	return data, nil
}

func confluenceAttachmentCaptureTarget(root, path string) string {
	target := filepath.FromSlash(path)
	if filepath.IsAbs(target) {
		return target
	}
	return filepath.Join(root, target)
}

func (m *Mirror) ownedConfluenceAttachmentRemoval(path, expectedHash string) (CompletePullArtifact, error) {
	artifactPath, pathErr := NewPublicArtifactPath(path)
	if pathErr != nil {
		return CompletePullArtifact{}, pathErr
	}
	current, data, found, currentErr := m.readQualifiedConfluenceAttachmentPrivate(artifactPath.String(), confluenceAttachmentArtifactLimit(artifactPath.String()))
	if currentErr != nil || !found || current.SHA256 != expectedHash || current.Mode != 0o600 {
		return CompletePullArtifact{}, fmt.Errorf("%w: prior Confluence attachment artifact changed after qualification", domain.ErrCheckFailed)
	}
	return completePullBoundRemovalWithSize(artifactPath, current, int64(len(data))), nil
}

func confluenceAttachmentArtifactLimit(path string) int64 {
	if strings.HasSuffix(filepath.ToSlash(path), ".attachments.json") {
		return maxCompletePullPublicationIntent
	}
	return maxCompletePullPublicationBytes
}

func (m *Mirror) validateAbsentConfluenceAttachmentSidecar(stem string) error {
	entries, err := safepath.ReadDirWithinLimit(
		m.Root, filepath.Join(m.Root, filepath.FromSlash(stem+".attachments")), maxCompletePullPublicationArtifacts,
	)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || len(entries) != 0 {
		return fmt.Errorf("%w: Confluence attachment directory has no ownership sidecar", domain.ErrCheckFailed)
	}
	return nil
}

func (m *Mirror) validateConfluenceAttachmentDirectory(stem string, expected map[string]string) error {
	dir := filepath.Join(m.Root, filepath.FromSlash(stem+".attachments"))
	entries, err := safepath.ReadDirWithinLimit(m.Root, dir, maxCompletePullPublicationArtifacts)
	if os.IsNotExist(err) {
		if len(expected) == 0 {
			return nil
		}
		return fmt.Errorf("%w: prior Confluence attachment directory is missing", domain.ErrCheckFailed)
	}
	if err != nil || len(entries) != len(expected) {
		return fmt.Errorf("%w: prior Confluence attachment directory differs from its ownership inventory", domain.ErrCheckFailed)
	}
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return fmt.Errorf("%w: prior Confluence attachment directory contains an unsafe entry", domain.ErrCheckFailed)
		}
		// The sidecar paths are root-relative, whereas dir is absolute. Build the
		// expected root-relative form from the reviewed stem rather than trusting
		// the directory entry's ambient spelling.
		path := stem + ".attachments/" + entry.Name()
		if _, found := expected[path]; !found {
			return fmt.Errorf("%w: prior Confluence attachment directory contains an unowned entry", domain.ErrCheckFailed)
		}
	}
	return nil
}
