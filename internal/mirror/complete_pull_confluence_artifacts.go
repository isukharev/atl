package mirror

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

func confluenceCompletePullBasePath(entry CompletePullJournalEntry) string {
	return filepath.ToSlash(filepath.Join(".atl", "base", entry.State.ID+".csf"))
}

func validateConfluenceCompletePullPayloads(entry CompletePullJournalEntry, artifacts []CompletePullArtifact) error {
	nativeCount, baseCount := 0, 0
	for _, artifact := range artifacts {
		switch artifact.Path.String() {
		case entry.State.Path:
			nativeCount++
			if artifact.Remove || artifact.BestEffort || artifact.Mode != 0o644 || Hash(artifact.Data) != entry.State.Hash {
				return fmt.Errorf("confluence native artifact does not match the accepted page state")
			}
		case confluenceCompletePullBasePath(entry):
			baseCount++
			if artifact.Remove || artifact.BestEffort || artifact.Mode != 0o600 || Hash(artifact.Data) != entry.State.Hash {
				return fmt.Errorf("confluence pristine-base artifact does not match the accepted page state")
			}
		}
	}
	if nativeCount != 1 || baseCount != 1 {
		return fmt.Errorf("confluence publication requires exactly one native and pristine-base artifact")
	}
	return validateConfluenceCompletePullAttachmentPayloads(entry, artifacts)
}

func confluenceCompletePullAttachmentEvidence(entry CompletePullJournalEntry) (domain.ConfluencePullIncludeEvidence, bool, error) {
	if entry.Includes == nil {
		return domain.ConfluencePullIncludeEvidence{}, false, nil
	}
	var evidence domain.ConfluencePullIncludeEvidence
	found := false
	for _, value := range *entry.Includes {
		if value.Dimension != domain.ConfluencePullIncludeAttachments {
			continue
		}
		if found {
			return domain.ConfluencePullIncludeEvidence{}, false, fmt.Errorf("complete-pull attachment evidence is duplicated")
		}
		evidence, found = value, true
	}
	return evidence, found, nil
}

func confluenceCompletePullAttachmentStem(entry CompletePullJournalEntry) (string, error) {
	if !strings.HasSuffix(entry.State.Path, ".csf") {
		return "", fmt.Errorf("complete-pull attachment evidence has an invalid parent path")
	}
	return strings.TrimSuffix(entry.State.Path, ".csf"), nil
}

func validateConfluenceCompletePullAttachmentPayloads(entry CompletePullJournalEntry, artifacts []CompletePullArtifact) error {
	evidence, requested, evidenceErr := confluenceCompletePullAttachmentEvidence(entry)
	if evidenceErr != nil {
		return evidenceErr
	}
	stem, stemErr := confluenceCompletePullAttachmentStem(entry)
	if stemErr != nil {
		return stemErr
	}
	sidecarPath := stem + ".attachments.json"
	bodyPrefix := stem + ".attachments/"
	var sidecarData []byte
	var metadata []byte
	bodies := make(map[string]CompletePullArtifact)
	for _, artifact := range artifacts {
		path := artifact.Path.String()
		switch path {
		case sidecarPath:
			if !artifact.Remove {
				if sidecarData != nil || artifact.BestEffort || artifact.Mode != 0o600 {
					return fmt.Errorf("complete-pull attachment sidecar is invalid")
				}
				sidecarData = artifact.Data
			}
		case stem + ".meta.json":
			if !artifact.Remove && !artifact.BestEffort && artifact.Mode == 0o644 {
				metadata = artifact.Data
			}
		default:
			if !strings.HasPrefix(path, bodyPrefix) || artifact.Remove {
				continue
			}
			if artifact.BestEffort || artifact.Mode != 0o600 {
				return fmt.Errorf("complete-pull attachment body artifact is invalid")
			}
			if _, duplicate := bodies[path]; duplicate {
				return fmt.Errorf("complete-pull attachment body artifact is duplicated")
			}
			bodies[path] = artifact
		}
	}
	if !requested {
		if sidecarData != nil || len(bodies) != 0 {
			return fmt.Errorf("complete-pull attachment artifacts have no include evidence")
		}
		return nil
	}
	if sidecarData == nil || metadata == nil || len(sidecarData) > maxCompletePullPublicationIntent {
		return fmt.Errorf("complete-pull attachment evidence is unavailable")
	}
	sidecar, err := DecodeAttachmentSidecarV1(sidecarData)
	if err != nil || sidecar.Service != CorpusSnapshotConfluence || sidecar.ParentID != entry.State.ID ||
		sidecar.ParentVersion != entry.State.Version || sidecar.ParentRevision != "" ||
		sidecar.NativeSHA256 != entry.State.Hash || sidecar.MetadataSHA256 != Hash(metadata) {
		return fmt.Errorf("complete-pull attachment sidecar is misbound")
	}
	bytes, bytesErr := attachmentSidecarCapturedBytes(sidecar, bodyPrefix, maxCompletePullPublicationBytes, func(path string, size int64, digest string) error {
		artifact, found := bodies[path]
		if !found || int64(len(artifact.Data)) != size || Hash(artifact.Data) != digest {
			return fmt.Errorf("complete-pull attachment body does not match its sidecar")
		}
		delete(bodies, path)
		return nil
	})
	if bytesErr != nil || len(bodies) != 0 || bytes != evidence.BodyBytes {
		return fmt.Errorf("complete-pull attachment body accounting is not bound to its artifacts")
	}
	return nil
}

func attachmentSidecarCapturedBytes(
	sidecar AttachmentSidecarV1,
	bodyPrefix string,
	maximum int64,
	verify func(path string, size int64, digest string) error,
) (int64, error) {
	if maximum < 0 || maximum > maxCompletePullPublicationBytes {
		return 0, fmt.Errorf("complete-pull attachment body bound is invalid")
	}
	var total int64
	for _, attachment := range sidecar.Attachments {
		if attachment.Body.State != AttachmentBodyCaptured {
			continue
		}
		body := attachment.Body
		if body.Path != bodyPrefix+attachment.ID+".body" || body.Size < 0 || body.Size > maximum-total {
			return 0, fmt.Errorf("complete-pull attachment body is outside its parent or overflows accounting")
		}
		if err := verify(body.Path, body.Size, body.SHA256); err != nil {
			return 0, err
		}
		total += body.Size
	}
	return total, nil
}

// VerifyConfluenceCompletePullAttachmentBodyBytes reconstructs the exact
// captured-body total for a durable Confluence prefix. It is intentionally run
// only at resume admission, not on every progress write: one load of a large
// completed prefix remains bounded by the immutable selection, while repeated
// checkpoint commits retain their existing constant work.
func (m *Mirror) VerifyConfluenceCompletePullAttachmentBodyBytes(checkpoint CompletePullCheckpoint, maximum int64) (int64, error) {
	if m == nil || checkpoint.Service != CompletePullServiceConfluence || checkpoint.NextIndex < 0 || checkpoint.NextIndex > len(checkpoint.IDs) ||
		maximum < 0 || maximum > maxCompletePullPublicationBytes {
		return 0, fmt.Errorf("%w: complete-pull attachment prefix is invalid", domain.ErrCheckFailed)
	}
	if checkpoint.NextIndex == 0 {
		return 0, nil
	}
	if !checkpoint.Includes.EvidenceComplete || checkpoint.Includes.Attachments.Published != checkpoint.NextIndex {
		return 0, fmt.Errorf("%w: complete-pull attachment evidence does not cover its durable prefix", domain.ErrCheckFailed)
	}
	states, err := m.SyncStates()
	if err != nil {
		return 0, err
	}
	byID := make(map[string]SyncState, len(states))
	for _, state := range states {
		byID[state.ID] = state
	}
	var total int64
	for _, id := range checkpoint.IDs[:checkpoint.NextIndex] {
		state, found := byID[id]
		if !found || state.ID != id || state.Version <= 0 || !strings.HasSuffix(state.Path, ".csf") {
			return 0, fmt.Errorf("%w: complete-pull attachment state is missing for its accepted prefix", domain.ErrCheckFailed)
		}
		bytes, verifyErr := m.verifyConfluenceCompletePullAttachmentArtifactsBounded(CompletePullJournalEntry{State: state, Includes: &[]domain.ConfluencePullIncludeEvidence{{
			Dimension: domain.ConfluencePullIncludeAttachments, Qualification: domain.ConfluencePullIncludeQualified,
		}}}, false, maximum-total)
		if verifyErr != nil {
			return 0, verifyErr
		}
		if total > (1<<63-1)-bytes {
			return 0, fmt.Errorf("%w: complete-pull attachment body accounting overflows", domain.ErrCheckFailed)
		}
		total += bytes
	}
	return total, nil
}

// verifyConfluenceCompletePullAttachmentArtifacts validates the durable
// sidecar/body set for one accepted page. requireEvidenceBytes is true while
// replaying a journal entry, whose per-page aggregate is part of the signed
// journal shape; resume reconstruction deliberately derives that value from
// the files and compares only the final prefix aggregate.
func (m *Mirror) verifyConfluenceCompletePullAttachmentArtifacts(entry CompletePullJournalEntry, requireEvidenceBytes bool) (int64, error) {
	return m.verifyConfluenceCompletePullAttachmentArtifactsBounded(entry, requireEvidenceBytes, maxCompletePullPublicationBytes)
}

func (m *Mirror) verifyConfluenceCompletePullAttachmentArtifactsBounded(entry CompletePullJournalEntry, requireEvidenceBytes bool, maximum int64) (int64, error) {
	if maximum < 0 || maximum > maxCompletePullPublicationBytes {
		return 0, fmt.Errorf("%w: complete-pull attachment body bound is invalid", domain.ErrCheckFailed)
	}
	evidence, requested, evidenceErr := confluenceCompletePullAttachmentEvidence(entry)
	if evidenceErr != nil {
		return 0, fmt.Errorf("%w: %v", domain.ErrCheckFailed, evidenceErr)
	}
	if !requested {
		return 0, nil
	}
	if requireEvidenceBytes {
		if evidence.BodyBytes < 0 || evidence.BodyBytes > maximum {
			return 0, fmt.Errorf("%w: complete-pull attachment body evidence exceeds its bound", domain.ErrCheckFailed)
		}
		maximum = evidence.BodyBytes
	}
	stem, stemErr := confluenceCompletePullAttachmentStem(entry)
	if stemErr != nil {
		return 0, fmt.Errorf("%w: %v", domain.ErrCheckFailed, stemErr)
	}
	sidecarPath := stem + ".attachments.json"
	current, sidecarData, found, currentErr := m.readQualifiedConfluenceAttachmentSidecar(sidecarPath)
	if currentErr != nil || !found || current.Mode != 0o600 {
		return 0, fmt.Errorf("%w: complete-pull attachment sidecar is missing or unsafe", domain.ErrCheckFailed)
	}
	metadataPath := stem + ".meta.json"
	metadata, metadataErr := safepath.ReadFileWithinLimit(m.Root, filepath.Join(m.Root, filepath.FromSlash(metadataPath)), maxCompletePullPublicationIntent)
	if metadataErr != nil {
		return 0, fmt.Errorf("%w: complete-pull attachment metadata is unavailable", domain.ErrCheckFailed)
	}
	sidecar, decodeErr := DecodeAttachmentSidecarV1(sidecarData)
	binding, bound, bindingErr := m.BackendBinding(CorpusSnapshotConfluence)
	if decodeErr != nil || bindingErr != nil || !bound || sidecar.Service != CorpusSnapshotConfluence ||
		sidecar.OriginSHA256 != binding.OriginSHA256 || sidecar.ParentID != entry.State.ID ||
		sidecar.ParentVersion != entry.State.Version || sidecar.ParentRevision != "" ||
		sidecar.NativeSHA256 != entry.State.Hash || sidecar.MetadataSHA256 != Hash(metadata) {
		return 0, fmt.Errorf("%w: complete-pull attachment sidecar is misbound", domain.ErrCheckFailed)
	}
	bodyPrefix := stem + ".attachments/"
	bodyHashes := make(map[string]string)
	bytes, bytesErr := attachmentSidecarCapturedBytes(sidecar, bodyPrefix, maximum, func(path string, size int64, digest string) error {
		body, bodyErr := m.readQualifiedConfluenceAttachmentBody(path, size)
		if bodyErr != nil || int64(len(body)) != size || Hash(body) != digest {
			return fmt.Errorf("complete-pull attachment body is missing or changed")
		}
		bodyHashes[path] = digest
		return nil
	})
	if bytesErr != nil || m.validateConfluenceAttachmentDirectory(stem, bodyHashes) != nil || requireEvidenceBytes && bytes != evidence.BodyBytes {
		return 0, fmt.Errorf("%w: complete-pull attachment body accounting is not bound to its artifacts", domain.ErrCheckFailed)
	}
	return bytes, nil
}

func validateConfluenceCompletePullIntent(entry CompletePullJournalEntry, artifacts []completePullPublicationArtifact) error {
	nativeCount, baseCount := 0, 0
	for _, artifact := range artifacts {
		switch artifact.Path {
		case entry.State.Path:
			nativeCount++
			if artifact.Remove || artifact.BestEffort || artifact.Mode != 0o644 || artifact.SHA256 != entry.State.Hash {
				return fmt.Errorf("confluence native artifact does not match the accepted page state")
			}
		case confluenceCompletePullBasePath(entry):
			baseCount++
			if artifact.Remove || artifact.BestEffort || artifact.Mode != 0o600 || artifact.SHA256 != entry.State.Hash {
				return fmt.Errorf("confluence pristine-base artifact does not match the accepted page state")
			}
		}
	}
	if nativeCount != 1 || baseCount != 1 {
		return fmt.Errorf("confluence publication requires exactly one native and pristine-base artifact")
	}
	return nil
}

func (m *Mirror) prepareCompletePullCommentArtifacts(dir, slug string, cs *commentSidecar, mdOpts MDViewOpts, meta *Meta) ([]CompletePullArtifact, error) {
	commentsJSONRel, err := PublicArtifactPathWithin(m.Root, filepath.Join(dir, slug+".comments.json"))
	if err != nil {
		return nil, err
	}
	displayComments := cs.display
	if mdOpts.CommentView != nil {
		displayComments = mdOpts.CommentView
	}
	commentsMDRel, err := PublicArtifactPathWithin(m.Root, filepath.Join(dir, slug+".comments.md"))
	if err != nil {
		return nil, err
	}
	meta.CommentsPulled = true
	meta.CommentCount = len(cs.display)
	meta.CommentsTruncated = cs.truncated
	if cs.v2 != nil {
		meta.CommentSidecarVersion = cs.v2.SchemaVersion
		meta.CommentCount = cs.v2.Count
		meta.CommentRootCount = cs.v2.RootCount
		meta.CommentsComplete = boolPointer(cs.v2.CommentsComplete)
		meta.CommentThreadsComplete = boolPointer(cs.v2.ThreadsComplete)
		meta.CommentAnchorsComplete = boolPointer(cs.v2.AnchorsComplete)
		meta.CommentPartialReasons = append([]string(nil), cs.v2.PartialReasons...)
		for _, comment := range cs.v2.Comments {
			if comment.Location == domain.ConfluenceCommentLocationInline && comment.Resolution == domain.ConfluenceCommentResolutionOpen {
				meta.OpenInlineCommentCount++
			}
		}
	}
	return []CompletePullArtifact{
		{Path: commentsJSONRel, Data: append([]byte(nil), cs.encoded...), Mode: 0o644},
		{Path: commentsMDRel, Data: RenderCommentsMarkdown(displayComments), Mode: 0o644, BestEffort: true},
	}, nil
}
