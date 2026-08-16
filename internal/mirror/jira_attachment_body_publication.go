package mirror

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const (
	jiraAttachmentBodyPublicationSchema = 1
	maxJiraAttachmentBodyIntentBytes    = 64 << 10
	// MaxJiraAttachmentBodyMaterializationTransactions bounds one explicit
	// materializer invocation. Each unit is one body and one sidecar update.
	MaxJiraAttachmentBodyMaterializationTransactions = 4096
)

type jiraAttachmentBodyPublicationIntent struct {
	SchemaVersion int       `json:"schema_version"`
	Identity      string    `json:"identity"`
	State         SyncState `json:"state"`
	AttachmentID  string    `json:"attachment_id"`
	SidecarSHA256 string    `json:"sidecar_sha256"`
	SidecarSize   int64     `json:"sidecar_size"`
	NextSHA256    string    `json:"next_sha256"`
	NextSize      int64     `json:"next_size"`
	BodySHA256    string    `json:"body_sha256"`
	BodySize      int64     `json:"body_size"`
	Next          int       `json:"next"`
	Committed     bool      `json:"committed,omitempty"`
	WriteToken    string    `json:"write_token"`
}

func (m *Mirror) jiraAttachmentBodyPublicationDir() string {
	return filepath.Join(m.Root, ".atl", "jira-attachment-bodies")
}

func jiraAttachmentBodyIntentPath(dir string) string { return filepath.Join(dir, "intent.json") }
func jiraAttachmentBodyPayloadPath(dir, name string) string {
	return filepath.Join(dir, name)
}

// RecoverJiraAttachmentBodyMaterialization completes one durably staged local
// body transaction. It never repeats a backend read: all accepted bytes and
// their successor sidecar are already private payloads in the staged intent.
func (m *Mirror) RecoverJiraAttachmentBodyMaterialization() error {
	if m == nil {
		return fmt.Errorf("%w: mirror is unavailable", domain.ErrCheckFailed)
	}
	dir := m.jiraAttachmentBodyPublicationDir()
	info, err := safepath.StatWithin(m.Root, dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: Jira attachment body publication state is unsafe; preserving it", domain.ErrCheckFailed)
	}
	entries, err := safepath.ReadDirWithinLimit(m.Root, dir, 3)
	if err != nil {
		return fmt.Errorf("%w: Jira attachment body publication state is unsafe; preserving it", domain.ErrCheckFailed)
	}
	// A crash is allowed after the committed intent has been retired and before
	// the now-empty private stage directory is removed. The same empty directory
	// can arise before the first intent is written. It proves no unpublished
	// payload exists, so retire only that exact private directory before any next
	// backend read.
	if len(entries) == 0 {
		if err := safepath.RemoveWithin(m.Root, dir); err != nil {
			return err
		}
		return safepath.SyncDirectoryWithin(m.Root, filepath.Dir(dir))
	}
	data, found, err := readJiraAttachmentBodyStageFile(m.Root, jiraAttachmentBodyIntentPath(dir), maxJiraAttachmentBodyIntentBytes)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: Jira attachment body publication has no durable intent; preserving it", domain.ErrCheckFailed)
	}
	var intent jiraAttachmentBodyPublicationIntent
	if err := decodeCompletePullJSON(jiraAttachmentBodyIntentPath(dir), data, &intent); err != nil {
		return err
	}
	if err := m.validateJiraAttachmentBodyIntent(dir, intent); err != nil {
		return err
	}
	return m.publishJiraAttachmentBodyIntent(dir, intent)
}

// PublishJiraAttachmentBody advances one inventory row from the deterministic
// pending state to a captured private body. The caller must have revalidated
// the remote parent and exact attachment selector immediately around the
// bounded stream; this method proves only local state and publishes the local
// two-file transition atomically recoverably.
func (m *Mirror) PublishJiraAttachmentBody(identity, attachmentID string, body []byte) error {
	if m == nil || !positiveDecimalIdentity(identity) || !positiveDecimalIdentity(attachmentID) ||
		int64(len(body)) > MaxJiraAttachmentBodyMaterializationBytes {
		return fmt.Errorf("%w: Jira attachment body publication is invalid", domain.ErrCheckFailed)
	}
	if err := m.RecoverJiraAttachmentBodyMaterialization(); err != nil {
		return err
	}
	tracked, found, err := m.JiraCompletePullStateByIdentity(identity)
	if err != nil || !found {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: Jira attachment body has no tracked primary state", domain.ErrCheckFailed)
	}
	inspection, present, err := m.inspectJiraAttachmentBodyInventoryForState(tracked.State)
	if err != nil || !present {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: Jira attachment body inventory is missing", domain.ErrCheckFailed)
	}
	if _, err := jiraAttachmentBodyProgress(inspection.sidecar); err != nil {
		return err
	}
	var selected AttachmentSidecarRecord
	found = false
	for _, attachment := range inspection.sidecar.Attachments {
		if attachment.ID == attachmentID {
			selected, found = attachment, true
			break
		}
	}
	if !found || !jiraAttachmentBodyPending(inspection.sidecar.BodiesState, selected.Body) || int64(len(body)) != selected.DeclaredSize {
		return fmt.Errorf("%w: Jira attachment body no longer matches its pending inventory row", domain.ErrCheckFailed)
	}
	next, data, err := nextJiraAttachmentBodySidecar(inspection, attachmentID, body)
	if err != nil {
		return err
	}
	return m.prepareJiraAttachmentBodyPublication(inspection, next, data, attachmentID, body)
}

func nextJiraAttachmentBodySidecar(inspection jiraAttachmentBodyMaterializationInspection, attachmentID string, body []byte) (AttachmentSidecarV1, []byte, error) {
	next := inspection.sidecar
	next.Attachments = append([]AttachmentSidecarRecord(nil), inspection.sidecar.Attachments...)
	stem := inspection.stem
	bodyPath, err := NewPublicArtifactPath(stem + ".attachments/" + attachmentID + ".body")
	if err != nil {
		return AttachmentSidecarV1{}, nil, err
	}
	pending := 0
	selected := false
	for index := range next.Attachments {
		attachment := &next.Attachments[index]
		if attachment.ID == attachmentID {
			if !jiraAttachmentBodyPending(inspection.sidecar.BodiesState, attachment.Body) || int64(len(body)) != attachment.DeclaredSize {
				return AttachmentSidecarV1{}, nil, fmt.Errorf("%w: Jira attachment body successor is not pending", domain.ErrCheckFailed)
			}
			attachment.Body = AttachmentSidecarBody{State: AttachmentBodyCaptured, Path: bodyPath.String(), Size: int64(len(body)), SHA256: Hash(body)}
			selected = true
			continue
		}
		if jiraAttachmentBodyPending(inspection.sidecar.BodiesState, attachment.Body) {
			attachment.Body = AttachmentSidecarBody{State: AttachmentBodyExcluded, Reason: AttachmentBodyReasonAggregateLimit}
			pending++
		}
	}
	if !selected {
		return AttachmentSidecarV1{}, nil, fmt.Errorf("%w: Jira attachment body successor is absent", domain.ErrCheckFailed)
	}
	if pending == 0 {
		next.BodiesState = AttachmentBodiesComplete
		next.PartialReasons = []AttachmentPartialReason{}
	} else {
		next.BodiesState = AttachmentBodiesPartial
		next.PartialReasons = []AttachmentPartialReason{AttachmentReasonBodyAggregateLimit}
	}
	next.Complete = next.InventoryComplete && next.BodiesState == AttachmentBodiesComplete
	encoded, err := EncodeAttachmentSidecarV1(next)
	if err != nil {
		return AttachmentSidecarV1{}, nil, err
	}
	return next, encoded, nil
}

func (m *Mirror) prepareJiraAttachmentBodyPublication(
	inspection jiraAttachmentBodyMaterializationInspection,
	next AttachmentSidecarV1,
	nextData []byte,
	attachmentID string,
	body []byte,
) error {
	if err := ValidateAttachmentSidecarPublicationData(nextData, 0); err != nil || len(nextData) > maxJiraAttachmentBodyIntentBytes*256 {
		return fmt.Errorf("%w: Jira attachment successor sidecar is invalid", domain.ErrCheckFailed)
	}
	if next.ParentID != inspection.identity || next.ParentRevision != inspection.sidecar.ParentRevision ||
		next.NativeSHA256 != inspection.sidecar.NativeSHA256 || next.MetadataSHA256 != inspection.sidecar.MetadataSHA256 {
		return fmt.Errorf("%w: Jira attachment successor sidecar is misbound", domain.ErrCheckFailed)
	}
	dir := m.jiraAttachmentBodyPublicationDir()
	if _, err := safepath.StatWithin(m.Root, dir); !os.IsNotExist(err) {
		if err == nil {
			return fmt.Errorf("%w: Jira attachment body publication is already staged", domain.ErrCheckFailed)
		}
		return err
	}
	if err := safepath.MkdirAllWithin(m.Root, dir, 0o700); err != nil {
		return err
	}
	if err := safepath.SyncDirectoryWithin(m.Root, filepath.Dir(dir)); err != nil {
		return err
	}
	if err := safepath.SyncDirectoryWithin(m.Root, dir); err != nil {
		return err
	}
	token, err := newCompletePullWriteToken()
	if err != nil {
		return err
	}
	intent := jiraAttachmentBodyPublicationIntent{
		SchemaVersion: jiraAttachmentBodyPublicationSchema,
		Identity:      inspection.identity,
		State:         inspection.state,
		AttachmentID:  attachmentID,
		SidecarSHA256: Hash(inspection.sidecarData),
		SidecarSize:   int64(len(inspection.sidecarData)),
		NextSHA256:    Hash(nextData),
		NextSize:      int64(len(nextData)),
		BodySHA256:    Hash(body),
		BodySize:      int64(len(body)),
		Next:          -1,
		WriteToken:    token,
	}
	if err := m.saveJiraAttachmentBodyIntent(dir, intent); err != nil {
		return err
	}
	if err := m.writeCompletePullOwned(jiraAttachmentBodyPayloadPath(dir, "body.bin"), completePullArtifactTemp(token, 0), body, 0o600); err != nil {
		return err
	}
	if err := m.writeCompletePullOwned(jiraAttachmentBodyPayloadPath(dir, "sidecar.json"), completePullArtifactTemp(token, 1), nextData, 0o600); err != nil {
		return err
	}
	if err := safepath.SyncDirectoryWithin(m.Root, dir); err != nil {
		return err
	}
	intent.Next = 0
	if err := m.saveJiraAttachmentBodyIntent(dir, intent); err != nil {
		return err
	}
	return m.publishJiraAttachmentBodyIntent(dir, intent)
}

func (m *Mirror) saveJiraAttachmentBodyIntent(dir string, intent jiraAttachmentBodyPublicationIntent) error {
	data, err := json.MarshalIndent(intent, "", "  ")
	if err != nil || len(data)+1 > maxJiraAttachmentBodyIntentBytes {
		return fmt.Errorf("%w: Jira attachment body intent exceeds its bounded encoding", domain.ErrCheckFailed)
	}
	if err := m.writeCompletePullOwned(jiraAttachmentBodyIntentPath(dir), completePullJournalTemp(intent.WriteToken), append(data, '\n'), 0o600); err != nil {
		return err
	}
	return safepath.SyncDirectoryWithin(m.Root, dir)
}

func (m *Mirror) publishJiraAttachmentBodyIntent(dir string, intent jiraAttachmentBodyPublicationIntent) error {
	if err := m.validateJiraAttachmentBodyIntent(dir, intent); err != nil {
		return err
	}
	if intent.Next == -1 {
		_, bodyFound, bodyErr := readJiraAttachmentBodyStageFile(m.Root, jiraAttachmentBodyPayloadPath(dir, "body.bin"), intent.BodySize)
		_, sidecarFound, sidecarErr := readJiraAttachmentBodyStageFile(m.Root, jiraAttachmentBodyPayloadPath(dir, "sidecar.json"), intent.NextSize)
		if bodyErr != nil || sidecarErr != nil {
			return fmt.Errorf("%w: Jira attachment body staging payload is unsafe", domain.ErrCheckFailed)
		}
		// Both staged payloads are independently bounded and hash-checked by the
		// intent validation above. Promote this fully staged local transaction
		// rather than repeating its accepted backend read after a crash before the
		// progress marker could be advanced.
		if bodyFound && sidecarFound {
			intent.Next = 0
			if err := m.saveJiraAttachmentBodyIntent(dir, intent); err != nil {
				return err
			}
			return m.publishJiraAttachmentBodyIntent(dir, intent)
		}
		return m.abandonUnpublishedJiraAttachmentBodyIntent(dir, intent)
	}
	// A committed transaction has already made its two live destinations
	// durable. Its stage payloads are deliberately disposable: a crash between
	// their individual removals must resume cleanup from the live postcondition,
	// not require a payload that was correctly retired before the crash.
	if intent.Committed {
		if err := m.validateJiraAttachmentBodyIntentPostcondition(intent); err != nil {
			return err
		}
		return m.cleanupJiraAttachmentBodyPublication(dir, intent)
	}
	bodyRel, sidecarRel := jiraAttachmentBodyIntentPaths(intent)
	bodyTarget := filepath.Join(m.Root, filepath.FromSlash(bodyRel))
	sidecarTarget := filepath.Join(m.Root, filepath.FromSlash(sidecarRel))
	bodyData, _, err := readJiraAttachmentBodyStageFile(m.Root, jiraAttachmentBodyPayloadPath(dir, "body.bin"), intent.BodySize)
	if err != nil || Hash(bodyData) != intent.BodySHA256 || int64(len(bodyData)) != intent.BodySize {
		return fmt.Errorf("%w: Jira attachment body staged payload is missing or changed", domain.ErrCheckFailed)
	}
	nextData, _, err := readJiraAttachmentBodyStageFile(m.Root, jiraAttachmentBodyPayloadPath(dir, "sidecar.json"), intent.NextSize)
	if err != nil || Hash(nextData) != intent.NextSHA256 || int64(len(nextData)) != intent.NextSize {
		return fmt.Errorf("%w: Jira attachment sidecar staged payload is missing or changed", domain.ErrCheckFailed)
	}
	if intent.Next == 0 {
		if err := ensureJiraAttachmentBodyDirectory(m.Root, filepath.Dir(bodyTarget)); err != nil {
			return err
		}
		current, _, currentErr := publicationCurrentWithinLimit(m.Root, bodyRel, intent.BodySize)
		if currentErr != nil {
			return fmt.Errorf("%w: inspect Jira attachment body destination", domain.ErrCheckFailed)
		}
		if !current.Present {
			if err := m.writeCompletePullOwned(bodyTarget, completePullArtifactTemp(intent.WriteToken, 2), bodyData, 0o600); err != nil {
				return err
			}
			if err := syncPublicationPath(m.Root, bodyTarget, defaultCompletePullPublicationOps()); err != nil {
				return err
			}
		} else if current.SHA256 != intent.BodySHA256 || current.Mode != 0o600 {
			return fmt.Errorf("%w: Jira attachment body destination is not its staged transition", domain.ErrCheckFailed)
		}
		intent.Next = 1
		if err := m.saveJiraAttachmentBodyIntent(dir, intent); err != nil {
			return err
		}
	}
	if intent.Next == 1 {
		current, _, currentErr := publicationCurrentWithinLimit(m.Root, sidecarRel, MaxAttachmentSidecarPublicationBytes)
		if currentErr != nil {
			return fmt.Errorf("%w: inspect Jira attachment sidecar destination", domain.ErrCheckFailed)
		}
		pre := completePullPublicationPreState{Present: true, SHA256: intent.SidecarSHA256, Mode: 0o600}
		if publicationMatchesPre(current, pre) {
			if err := m.writeCompletePullOwned(sidecarTarget, completePullArtifactTemp(intent.WriteToken, 3), nextData, 0o600); err != nil {
				return err
			}
			if err := syncPublicationPath(m.Root, sidecarTarget, defaultCompletePullPublicationOps()); err != nil {
				return err
			}
		} else if current.SHA256 != intent.NextSHA256 || current.Mode != 0o600 {
			return fmt.Errorf("%w: Jira attachment sidecar destination is not its staged transition", domain.ErrCheckFailed)
		}
		intent.Next = 2
		intent.Committed = true
		if err := m.saveJiraAttachmentBodyIntent(dir, intent); err != nil {
			return err
		}
	}
	if intent.Next != 2 || !intent.Committed {
		return fmt.Errorf("%w: Jira attachment body intent has invalid progress", domain.ErrCheckFailed)
	}
	if err := m.validateJiraAttachmentBodyIntentPostcondition(intent); err != nil {
		return err
	}
	return m.cleanupJiraAttachmentBodyPublication(dir, intent)
}

func (m *Mirror) removeJiraAttachmentBodyIntentAndStage(dir string, intent jiraAttachmentBodyPublicationIntent) error {
	path := jiraAttachmentBodyIntentPath(dir)
	data, found, err := readJiraAttachmentBodyStageFile(m.Root, path, maxJiraAttachmentBodyIntentBytes)
	if err != nil || !found || len(data) == 0 {
		return fmt.Errorf("%w: Jira attachment body publication intent is unsafe; preserving it", domain.ErrCheckFailed)
	}
	var decoded jiraAttachmentBodyPublicationIntent
	if err := decodeCompletePullJSON(path, data, &decoded); err != nil || decoded != intent {
		return fmt.Errorf("%w: Jira attachment body publication intent changed before retirement", domain.ErrCheckFailed)
	}
	if err := safepath.RemoveWithin(m.Root, path); err != nil {
		return err
	}
	if err := safepath.SyncDirectoryWithin(m.Root, dir); err != nil {
		return err
	}
	if err := safepath.RemoveWithin(m.Root, dir); err != nil {
		return err
	}
	return safepath.SyncDirectoryWithin(m.Root, filepath.Dir(dir))
}

func (m *Mirror) validateJiraAttachmentBodyIntent(dir string, intent jiraAttachmentBodyPublicationIntent) error {
	if intent.SchemaVersion != jiraAttachmentBodyPublicationSchema || !positiveDecimalIdentity(intent.Identity) ||
		!positiveDecimalIdentity(intent.AttachmentID) || intent.State.Version != 0 || intent.State.Identity != "" && intent.State.Identity != intent.Identity ||
		!strings.HasSuffix(intent.State.Path, ".wiki") || !validSHA256(intent.State.Hash) || !validSHA256(intent.SidecarSHA256) ||
		!validSHA256(intent.NextSHA256) || !validSHA256(intent.BodySHA256) || intent.SidecarSize < 0 ||
		intent.SidecarSize > MaxAttachmentSidecarPublicationBytes || intent.NextSize < 0 || intent.NextSize > MaxAttachmentSidecarPublicationBytes ||
		intent.BodySize < 0 || intent.BodySize > MaxJiraAttachmentBodyMaterializationBytes || intent.Next < -1 || intent.Next > 2 ||
		intent.Committed && intent.Next != 2 || !intent.Committed && intent.Next == 2 || !validCompletePullWriteToken(intent.WriteToken) {
		return fmt.Errorf("%w: Jira attachment body publication intent is invalid", domain.ErrCheckFailed)
	}
	info, err := safepath.StatWithin(m.Root, dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: Jira attachment body publication directory is unsafe", domain.ErrCheckFailed)
	}
	entries, err := safepath.ReadDirWithinLimit(m.Root, dir, 3)
	if err != nil || len(entries) < 1 || len(entries) > 3 {
		return fmt.Errorf("%w: Jira attachment body publication directory differs from its intent", domain.ErrCheckFailed)
	}
	want := map[string]struct{}{"intent.json": {}, "body.bin": {}, "sidecar.json": {}}
	seen := map[string]bool{}
	for _, entry := range entries {
		entryInfo, infoErr := entry.Info()
		if infoErr != nil || !entryInfo.Mode().IsRegular() || entryInfo.Mode().Perm() != 0o600 {
			return fmt.Errorf("%w: Jira attachment body publication contains unsafe residue", domain.ErrCheckFailed)
		}
		if _, ok := want[entry.Name()]; !ok {
			return fmt.Errorf("%w: Jira attachment body publication contains unowned residue", domain.ErrCheckFailed)
		}
		seen[entry.Name()] = true
	}
	if !seen["intent.json"] || !intent.Committed && intent.Next >= 0 && (!seen["body.bin"] || !seen["sidecar.json"]) {
		return fmt.Errorf("%w: Jira attachment body publication payload is incomplete", domain.ErrCheckFailed)
	}
	if intent.Next == -1 || intent.Committed {
		for _, input := range []struct {
			name string
			size int64
			hash string
		}{{"body.bin", intent.BodySize, intent.BodySHA256}, {"sidecar.json", intent.NextSize, intent.NextSHA256}} {
			if !seen[input.name] {
				continue
			}
			data, found, readErr := readJiraAttachmentBodyStageFile(m.Root, jiraAttachmentBodyPayloadPath(dir, input.name), input.size)
			if readErr != nil || !found || int64(len(data)) != input.size || Hash(data) != input.hash {
				return fmt.Errorf("%w: Jira attachment body staging payload is unsafe", domain.ErrCheckFailed)
			}
		}
	}
	tracked, found, err := m.JiraCompletePullStateByIdentity(intent.Identity)
	if err != nil || !found || tracked.State != intent.State {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: Jira attachment body primary state changed during publication", domain.ErrCheckFailed)
	}
	stem := strings.TrimSuffix(intent.State.Path, ".wiki")
	if err := m.verifyJiraAttachmentMaterializationPrimary(intent.Identity, intent.State, stem); err != nil {
		return err
	}
	metadata, err := readJiraAttachmentMaterializationPublicFile(m.Root, filepath.Join(m.Root, filepath.FromSlash(stem+".json")), maxJiraCompletePullSnapshotBytes)
	if err != nil {
		return fmt.Errorf("%w: Jira attachment publication metadata is unsafe", domain.ErrCheckFailed)
	}
	if err := m.validateJiraAttachmentBodyIntentSidecar(intent, metadata); err != nil {
		return err
	}
	if intent.Next >= 0 && !intent.Committed {
		nextData, found, readErr := readJiraAttachmentBodyStageFile(m.Root, jiraAttachmentBodyPayloadPath(dir, "sidecar.json"), intent.NextSize)
		next, decodeErr := DecodeAttachmentSidecarV1(nextData)
		if readErr != nil || !found || decodeErr != nil || !jiraAttachmentBodySidecarBound(next, intent, metadata) || !jiraAttachmentBodySidecarCaptures(intent, next) {
			return fmt.Errorf("%w: Jira attachment successor payload is misbound", domain.ErrCheckFailed)
		}
	}
	return nil
}

// abandonUnpublishedJiraAttachmentBodyIntent retires only a stage that never
// reached a destination write. Its intent is immutable enough to prove both
// live destinations are still their preimages, so the caller can repeat the
// remote read rather than being stranded by a crash while staging payloads.
func (m *Mirror) abandonUnpublishedJiraAttachmentBodyIntent(dir string, intent jiraAttachmentBodyPublicationIntent) error {
	bodyRel, sidecarRel := jiraAttachmentBodyIntentPaths(intent)
	body, _, bodyErr := publicationCurrentWithinLimit(m.Root, bodyRel, intent.BodySize)
	sidecar, _, sidecarErr := publicationCurrentWithinLimit(m.Root, sidecarRel, MaxAttachmentSidecarPublicationBytes)
	if bodyErr != nil || sidecarErr != nil || body.Present || !sidecar.Present || sidecar.SHA256 != intent.SidecarSHA256 || sidecar.Mode != 0o600 {
		return fmt.Errorf("%w: Jira attachment body staging cannot be safely abandoned", domain.ErrCheckFailed)
	}
	entries, err := safepath.ReadDirWithinLimit(m.Root, dir, 3)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "intent.json" {
			continue
		}
		var size int64
		var hash string
		switch entry.Name() {
		case "body.bin":
			size, hash = intent.BodySize, intent.BodySHA256
		case "sidecar.json":
			size, hash = intent.NextSize, intent.NextSHA256
		default:
			return fmt.Errorf("%w: Jira attachment body staging contains unowned residue", domain.ErrCheckFailed)
		}
		data, found, readErr := readJiraAttachmentBodyStageFile(m.Root, jiraAttachmentBodyPayloadPath(dir, entry.Name()), size)
		if readErr != nil || !found || Hash(data) != hash {
			return fmt.Errorf("%w: Jira attachment body staging payload is unsafe", domain.ErrCheckFailed)
		}
		if err := safepath.RemoveWithin(m.Root, jiraAttachmentBodyPayloadPath(dir, entry.Name())); err != nil {
			return err
		}
	}
	return m.removeJiraAttachmentBodyIntentAndStage(dir, intent)
}

func (m *Mirror) validateJiraAttachmentBodyIntentSidecar(intent jiraAttachmentBodyPublicationIntent, metadata []byte) error {
	_, sidecarRel := jiraAttachmentBodyIntentPaths(intent)
	current, found, err := m.readQualifiedJiraPrivateEvidence(sidecarRel, MaxAttachmentSidecarPublicationBytes)
	if err != nil || !found {
		return fmt.Errorf("%w: Jira attachment sidecar is missing or unsafe", domain.ErrCheckFailed)
	}
	currentHash := Hash(current)
	currentIsPre := int64(len(current)) == intent.SidecarSize && currentHash == intent.SidecarSHA256
	currentIsPost := int64(len(current)) == intent.NextSize && currentHash == intent.NextSHA256
	if !currentIsPre && !currentIsPost {
		return fmt.Errorf("%w: Jira attachment sidecar differs from its durable transition", domain.ErrCheckFailed)
	}
	sidecar, decodeErr := DecodeAttachmentSidecarV1(current)
	if decodeErr != nil || !jiraAttachmentBodySidecarBound(sidecar, intent, metadata) {
		return fmt.Errorf("%w: Jira attachment sidecar is misbound during publication", domain.ErrCheckFailed)
	}
	if currentIsPost && !jiraAttachmentBodySidecarCaptures(intent, sidecar) {
		return fmt.Errorf("%w: Jira attachment successor sidecar lacks its staged body", domain.ErrCheckFailed)
	}
	if currentIsPre {
		for _, attachment := range sidecar.Attachments {
			if attachment.ID == intent.AttachmentID {
				if jiraAttachmentBodyPending(sidecar.BodiesState, attachment.Body) {
					return nil
				}
				break
			}
		}
		return fmt.Errorf("%w: Jira attachment sidecar no longer has its pending body", domain.ErrCheckFailed)
	}
	return nil
}

func jiraAttachmentBodyIntentPaths(intent jiraAttachmentBodyPublicationIntent) (string, string) {
	stem := strings.TrimSuffix(intent.State.Path, ".wiki")
	body := stem + ".attachments/" + intent.AttachmentID + ".body"
	sidecar := stem + ".attachments.json"
	return body, sidecar
}

func jiraAttachmentBodySidecarBound(sidecar AttachmentSidecarV1, intent jiraAttachmentBodyPublicationIntent, metadata []byte) bool {
	if sidecar.Service != CorpusSnapshotJira || sidecar.ParentID != intent.Identity || sidecar.NativeSHA256 != intent.State.Hash {
		return false
	}
	if metadata == nil {
		return true
	}
	return sidecar.ParentRevision == jiraRelocationRevisionFromMetadata(metadata) && sidecar.MetadataSHA256 == Hash(metadata)
}

func jiraAttachmentBodySidecarCaptures(intent jiraAttachmentBodyPublicationIntent, sidecar AttachmentSidecarV1) bool {
	bodyRel, _ := jiraAttachmentBodyIntentPaths(intent)
	for _, attachment := range sidecar.Attachments {
		if attachment.ID == intent.AttachmentID {
			return attachment.Body.State == AttachmentBodyCaptured && attachment.Body.Path == bodyRel &&
				attachment.Body.Size == intent.BodySize && attachment.Body.SHA256 == intent.BodySHA256
		}
	}
	return false
}

func (m *Mirror) validateJiraAttachmentBodyIntentPostcondition(intent jiraAttachmentBodyPublicationIntent) error {
	bodyRel, sidecarRel := jiraAttachmentBodyIntentPaths(intent)
	body, found, err := m.readQualifiedJiraPrivateEvidence(bodyRel, intent.BodySize)
	if err != nil || !found || int64(len(body)) != intent.BodySize || Hash(body) != intent.BodySHA256 {
		return fmt.Errorf("%w: Jira attachment body postcondition is not durable", domain.ErrCheckFailed)
	}
	sidecar, found, err := m.readQualifiedJiraPrivateEvidence(sidecarRel, intent.NextSize)
	if err != nil || !found || int64(len(sidecar)) != intent.NextSize || Hash(sidecar) != intent.NextSHA256 {
		return fmt.Errorf("%w: Jira attachment sidecar postcondition is not durable", domain.ErrCheckFailed)
	}
	return nil
}

func ensureJiraAttachmentBodyDirectory(root, dir string) error {
	info, err := safepath.StatWithin(root, dir)
	if os.IsNotExist(err) {
		if err := safepath.MkdirAllWithin(root, dir, 0o700); err != nil {
			return err
		}
		if err := safepath.SyncDirectoryWithin(root, filepath.Dir(dir)); err != nil {
			return err
		}
		return safepath.SyncDirectoryWithin(root, dir)
	}
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: Jira attachment body directory is unsafe", domain.ErrCheckFailed)
	}
	return nil
}

func (m *Mirror) cleanupJiraAttachmentBodyPublication(dir string, intent jiraAttachmentBodyPublicationIntent) error {
	if !intent.Committed || intent.Next != 2 {
		return fmt.Errorf("%w: Jira attachment body cleanup is not committed", domain.ErrCheckFailed)
	}
	for _, file := range []struct {
		name string
		max  int64
		hash string
	}{
		{name: "body.bin", max: intent.BodySize, hash: intent.BodySHA256},
		{name: "sidecar.json", max: intent.NextSize, hash: intent.NextSHA256},
		{name: "intent.json", max: maxJiraAttachmentBodyIntentBytes},
	} {
		path := jiraAttachmentBodyPayloadPath(dir, file.name)
		data, found, err := readJiraAttachmentBodyStageFile(m.Root, path, file.max)
		if err != nil || !found && file.name == "intent.json" || found && file.hash != "" && Hash(data) != file.hash {
			return fmt.Errorf("%w: Jira attachment body publication residue is unsafe; preserving it", domain.ErrCheckFailed)
		}
		if !found {
			continue
		}
		if file.name == "intent.json" {
			var decoded jiraAttachmentBodyPublicationIntent
			if err := decodeCompletePullJSON(path, data, &decoded); err != nil || decoded != intent {
				return fmt.Errorf("%w: Jira attachment body publication intent changed before retirement", domain.ErrCheckFailed)
			}
		}
		if err := safepath.RemoveWithin(m.Root, path); err != nil {
			return err
		}
	}
	if err := safepath.SyncDirectoryWithin(m.Root, dir); err != nil {
		return err
	}
	if err := safepath.RemoveWithin(m.Root, dir); err != nil {
		return err
	}
	return safepath.SyncDirectoryWithin(m.Root, filepath.Dir(dir))
}

func readJiraAttachmentBodyStageFile(root, path string, maximum int64) ([]byte, bool, error) {
	if maximum < 0 {
		return nil, false, fmt.Errorf("%w: Jira attachment body stage has an invalid bound", domain.ErrCheckFailed)
	}
	info, err := safepath.StatWithin(root, path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() < 0 || info.Size() > maximum {
		return nil, false, fmt.Errorf("%w: Jira attachment body stage file is unsafe", domain.ErrCheckFailed)
	}
	data, err := safepath.ReadFileWithinLimit(root, path, info.Size())
	after, afterErr := safepath.StatWithin(root, path)
	if err != nil || afterErr != nil || !after.Mode().IsRegular() || after.Mode().Perm() != 0o600 || after.Size() != info.Size() || int64(len(data)) != after.Size() {
		return nil, false, fmt.Errorf("%w: Jira attachment body stage changed during bounded read", domain.ErrCheckFailed)
	}
	return data, true, nil
}
