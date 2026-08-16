package mirror

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

type jiraCompletePullSnapshot struct {
	Key    string                     `json:"key"`
	ID     string                     `json:"id"`
	Fields map[string]json.RawMessage `json:"fields"`
}

// CompletePullJiraOptionalEvidence is the content-minimized receipt for the
// private optional artifacts selected alongside one current Jira complete-pull
// entry. An empty digest asserts absence of that artifact; a sidecar digest
// transitively binds all attachment-body identities and bytes it declares.
type CompletePullJiraOptionalEvidence struct {
	CommentsSHA256    string `json:"comments_sha256,omitempty"`
	AttachmentsSHA256 string `json:"attachments_sha256,omitempty"`
}

func validCompletePullJiraOptionalEvidence(value *CompletePullJiraOptionalEvidence) bool {
	return value != nil &&
		(value.CommentsSHA256 == "" || validSHA256(value.CommentsSHA256)) &&
		(value.AttachmentsSHA256 == "" || validSHA256(value.AttachmentsSHA256))
}

func completePullJiraOptionalEvidenceFromArtifacts(entry CompletePullJournalEntry, artifacts []CompletePullArtifact) (CompletePullJiraOptionalEvidence, error) {
	if entry.Identity == "" || !strings.HasSuffix(entry.State.Path, ".wiki") {
		return CompletePullJiraOptionalEvidence{}, fmt.Errorf("jira optional evidence has an invalid parent identity")
	}
	stem := strings.TrimSuffix(entry.State.Path, ".wiki")
	commentsPath := stem + ".comments.json"
	attachmentsPath := stem + ".attachments.json"
	var evidence CompletePullJiraOptionalEvidence
	commentsSeen := false
	attachmentsSeen := false
	for _, artifact := range artifacts {
		var target *string
		var seen *bool
		switch artifact.Path.String() {
		case commentsPath:
			target = &evidence.CommentsSHA256
			seen = &commentsSeen
		case attachmentsPath:
			target = &evidence.AttachmentsSHA256
			seen = &attachmentsSeen
		default:
			continue
		}
		if *seen {
			return CompletePullJiraOptionalEvidence{}, fmt.Errorf("jira optional evidence sidecar is invalid")
		}
		*seen = true
		// A qualified same-path retirement is the reviewed post-image assertion
		// that this optional receipt is absent. It has no sidecar hash by design;
		// the schema-6 empty digest below records that absence durably.
		if artifact.Remove {
			continue
		}
		if artifact.BestEffort || artifact.Mode != 0o600 {
			return CompletePullJiraOptionalEvidence{}, fmt.Errorf("jira optional evidence sidecar is invalid")
		}
		*target = Hash(artifact.Data)
	}
	return evidence, nil
}

func validateJiraCompletePullPayloads(entry CompletePullJournalEntry, artifacts []CompletePullArtifact) error {
	for _, artifact := range artifacts {
		switch artifact.Role {
		case CompletePullArtifactRoleNative, CompletePullArtifactRoleBase:
			if Hash(artifact.Data) != entry.State.Hash {
				return fmt.Errorf("jira %s payload does not match the accepted native hash", artifact.Role)
			}
		case CompletePullArtifactRoleMetadata:
			var snapshot jiraCompletePullSnapshot
			if err := json.Unmarshal(artifact.Data, &snapshot); err != nil || snapshot.Key != entry.State.ID || snapshot.ID != entry.Identity || snapshot.Fields == nil {
				return fmt.Errorf("jira metadata payload does not match the accepted issue identity")
			}
		}
	}
	if err := validateJiraCompletePullCommentsPayloads(entry, artifacts); err != nil {
		return err
	}
	return validateJiraCompletePullAttachmentPayloads(entry, artifacts)
}

// validateJiraCompletePullCommentsPayloads checks an optional private comments
// receipt before an intent can be staged. Its origin binding is rechecked
// against the mirror population during recovery; this pure artifact check
// keeps the journal/publisher path self-consistent without coupling it to an
// ambient backend URL.
func validateJiraCompletePullCommentsPayloads(entry CompletePullJournalEntry, artifacts []CompletePullArtifact) error {
	if entry.Identity == "" || !strings.HasSuffix(entry.State.Path, ".wiki") {
		return fmt.Errorf("jira comments artifacts have an invalid parent identity")
	}
	stem := strings.TrimSuffix(entry.State.Path, ".wiki")
	commentsPath := stem + ".comments.json"
	var sidecarData, metadata []byte
	for _, artifact := range artifacts {
		if artifact.Remove {
			continue
		}
		switch artifact.Path.String() {
		case commentsPath:
			if sidecarData != nil || artifact.BestEffort || artifact.Mode != 0o600 {
				return fmt.Errorf("jira comments sidecar is invalid")
			}
			sidecarData = artifact.Data
		case stem + ".json":
			if !artifact.BestEffort && artifact.Mode == 0o644 {
				metadata = artifact.Data
			}
		}
	}
	if sidecarData == nil {
		return nil
	}
	if len(sidecarData) > maxCompletePullPublicationBytes || metadata == nil {
		return fmt.Errorf("jira comments sidecar is unavailable")
	}
	sidecar, err := DecodeJiraCommentsSidecarV1(sidecarData)
	if err != nil || sidecar.ParentID != entry.Identity || sidecar.ParentRevision != jiraRelocationRevisionFromMetadata(metadata) ||
		sidecar.NativeSHA256 != entry.State.Hash || sidecar.MetadataSHA256 != Hash(metadata) {
		return fmt.Errorf("jira comments sidecar is misbound")
	}
	return nil
}

// validateJiraCompletePullAttachmentPayloads binds optional private attachment
// artifacts to the same accepted native and metadata bytes as the primary Jira
// snapshot. Jira journal schemas deliberately remain compatible: no sidecar
// means no requested public attachment evidence, while any sidecar/body set is
// fully self-consistent before staging begins.
func validateJiraCompletePullAttachmentPayloads(entry CompletePullJournalEntry, artifacts []CompletePullArtifact) error {
	if entry.Identity == "" || !strings.HasSuffix(entry.State.Path, ".wiki") {
		return fmt.Errorf("jira attachment artifacts have an invalid parent identity")
	}
	stem := strings.TrimSuffix(entry.State.Path, ".wiki")
	sidecarPath := stem + ".attachments.json"
	bodyPrefix := stem + ".attachments/"
	var sidecarData, metadata []byte
	bodies := make(map[string]CompletePullArtifact)
	for _, artifact := range artifacts {
		if artifact.Remove {
			continue
		}
		switch artifact.Path.String() {
		case sidecarPath:
			if sidecarData != nil || artifact.BestEffort || artifact.Mode != 0o600 {
				return fmt.Errorf("jira attachment sidecar is invalid")
			}
			sidecarData = artifact.Data
		case stem + ".json":
			if !artifact.BestEffort && artifact.Mode == 0o644 {
				metadata = artifact.Data
			}
		default:
			if !strings.HasPrefix(artifact.Path.String(), bodyPrefix) {
				continue
			}
			if artifact.BestEffort || artifact.Mode != 0o600 {
				return fmt.Errorf("jira attachment body artifact is invalid")
			}
			if _, duplicate := bodies[artifact.Path.String()]; duplicate {
				return fmt.Errorf("jira attachment body artifact is duplicated")
			}
			bodies[artifact.Path.String()] = artifact
		}
	}
	if sidecarData == nil {
		if len(bodies) != 0 {
			return fmt.Errorf("jira attachment bodies have no sidecar")
		}
		return nil
	}
	if len(sidecarData) > MaxAttachmentSidecarPublicationBytes || metadata == nil {
		return fmt.Errorf("jira attachment sidecar is unavailable")
	}
	sidecar, err := DecodeAttachmentSidecarV1(sidecarData)
	if err != nil || sidecar.Service != CorpusSnapshotJira || sidecar.ParentID != entry.Identity || sidecar.ParentVersion != 0 ||
		sidecar.ParentRevision != jiraRelocationRevisionFromMetadata(metadata) || sidecar.NativeSHA256 != entry.State.Hash || sidecar.MetadataSHA256 != Hash(metadata) {
		return fmt.Errorf("jira attachment sidecar is misbound")
	}
	_, bytesErr := attachmentSidecarCapturedBytes(sidecar, bodyPrefix, maxCompletePullPublicationBytes, func(path string, size int64, digest string) error {
		artifact, found := bodies[path]
		if !found || int64(len(artifact.Data)) != size || Hash(artifact.Data) != digest {
			return fmt.Errorf("jira attachment body does not match its sidecar")
		}
		delete(bodies, path)
		return nil
	})
	if bytesErr != nil || len(bodies) != 0 {
		return fmt.Errorf("jira attachment bodies do not match their sidecar")
	}
	return nil
}

// verifyJiraCompletePullOptionalArtifacts revalidates any qualified private
// comments or attachment receipt that accompanies an accepted Jira entry. A
// legacy entry has no stable sidecar identity and therefore cannot claim the
// newer receipts; it remains readable without inventing evidence. For current
// entries, absence is valid only when the corresponding capture was never
// requested (and an attachment directory without its ownership sidecar is
// rejected by qualifiedJiraAttachmentCapture).
func (m *Mirror) verifyJiraCompletePullOptionalArtifacts(entry CompletePullJournalEntry) error {
	if !positiveDecimalIdentity(entry.Identity) {
		return nil
	}
	comments, commentsFound, err := m.qualifiedJiraCommentsCapture(entry.Identity, entry.State)
	if err != nil {
		return err
	}
	attachments, attachmentsFound, err := m.qualifiedJiraAttachmentCapture(entry.Identity, entry.State)
	if err != nil {
		return err
	}
	if entry.JiraOptionalEvidence == nil {
		return nil
	}
	evidence := entry.JiraOptionalEvidence
	if evidence.CommentsSHA256 == "" {
		if commentsFound {
			return fmt.Errorf("%w: complete-pull Jira comments receipt was not expected", domain.ErrCheckFailed)
		}
	} else if !commentsFound || comments.hash != evidence.CommentsSHA256 {
		return fmt.Errorf("%w: complete-pull Jira comments receipt is missing or changed", domain.ErrCheckFailed)
	}
	if evidence.AttachmentsSHA256 == "" {
		if attachmentsFound {
			return fmt.Errorf("%w: complete-pull Jira attachment receipt was not expected", domain.ErrCheckFailed)
		}
	} else if !attachmentsFound || attachments.sidecarHash != evidence.AttachmentsSHA256 {
		return fmt.Errorf("%w: complete-pull Jira attachment receipt is missing or changed", domain.ErrCheckFailed)
	}
	return nil
}
