package mirror

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const (
	completePullCheckpointSchema          = 1
	completePullProgressSchema            = 1 // legacy Confluence schema; bytes are immutable
	completePullJiraProgressSchema        = 2
	completePullConfluenceProgressSchema  = 3 // legacy current schema without attachment evidence
	completePullConfluenceProgressSchema4 = 4
	maxCompletePullCheckpointBytes        = 64 << 20
	maxCompletePullProgressBytes          = 4 << 10
	maxCompletePullCheckpointIDs          = 1_000_000
	maxCompletePullIDBytes                = 256
	completePullJournalSchema             = 2 // legacy Confluence schema; bytes are immutable
	completePullJiraJournalSchema         = 3 // legacy Jira schema without stable sidecar identity/relocation
	completePullJiraJournalSchema4        = 4
	completePullJiraJournalSchema5        = 5 // bounded durable pre-images for qualified auxiliary retirement
	completePullJiraJournalSchema6        = 6 // exact Jira optional-evidence receipts
	completePullConfluenceJournalSchema   = 5 // legacy current schema without attachment evidence
	completePullConfluenceJournalSchema6  = 6
	maxCompletePullJournalEntries         = 25
	maxCompletePullJournalBytes           = 256 << 10
)

// CompletePullCheckpoint is a private, backend-neutral snapshot of one exact
// selector plus the prefix whose mirror commits are known durable. IDs contain
// identities only: credentials, backend URLs, page bodies, and titles never
// enter this resume artifact.
type CompletePullCheckpoint struct {
	SchemaVersion   int                         `json:"schema_version"`
	Service         CompletePullService         `json:"service"`
	SelectorSHA256  string                      `json:"selector_sha256"`
	OptionsSHA256   string                      `json:"options_sha256"`
	SelectionSHA256 string                      `json:"selection_sha256"`
	IDs             []string                    `json:"ids"`
	NextIndex       int                         `json:"next_index"`
	Includes        CompletePullIncludeProgress `json:"-"`
}

type completePullProgress struct {
	SchemaVersion   int                          `json:"schema_version"`
	Service         CompletePullService          `json:"service,omitempty"`
	SelectorSHA256  string                       `json:"selector_sha256"`
	OptionsSHA256   string                       `json:"options_sha256"`
	SelectionSHA256 string                       `json:"selection_sha256"`
	NextIndex       int                          `json:"next_index"`
	Includes        *CompletePullIncludeProgress `json:"includes,omitempty"`
}

// completePullProgressConfluenceV3 is retained solely to re-emit the exact
// pre-attachment wire shape for a prefix that has no attachment evidence. A
// plain CompletePullIncludeProgress would add the new field to v3 JSON even
// when its aggregate is zero, which older readers correctly reject.
type completePullProgressConfluenceV3 struct {
	SchemaVersion   int                 `json:"schema_version"`
	Service         CompletePullService `json:"service"`
	SelectorSHA256  string              `json:"selector_sha256"`
	OptionsSHA256   string              `json:"options_sha256"`
	SelectionSHA256 string              `json:"selection_sha256"`
	NextIndex       int                 `json:"next_index"`
	Includes        *struct {
		EvidenceComplete bool                         `json:"evidence_complete"`
		Assets           CompletePullIncludeAggregate `json:"assets"`
		Comments         CompletePullIncludeAggregate `json:"comments"`
	} `json:"includes,omitempty"`
}

// CompletePullJournalEntry is the content-minimized durable commit evidence
// for one accepted page. The native body, metadata, title, backend identity,
// and credentials remain in their existing private artifacts; recovery needs
// only the exact sidecar state and view policy that were pending in memory.
type CompletePullJournalEntry struct {
	Identity string                                  `json:"identity,omitempty"`
	State    SyncState                               `json:"state"`
	View     ViewState                               `json:"view"`
	Previous *CompletePullPreviousState              `json:"previous,omitempty"`
	Includes *[]domain.ConfluencePullIncludeEvidence `json:"includes,omitempty"`
	// JiraOptionalEvidence binds exact optional Jira receipt post-images. A
	// non-nil empty value explicitly asserts absence; nil remains legacy-wire
	// compatibility for entries created before this receipt was introduced.
	JiraOptionalEvidence *CompletePullJiraOptionalEvidence `json:"jira_optional_evidence,omitempty"`
}

type completePullJournal struct {
	SchemaVersion   int                        `json:"schema_version"`
	Service         CompletePullService        `json:"service"`
	SelectorSHA256  string                     `json:"selector_sha256"`
	OptionsSHA256   string                     `json:"options_sha256"`
	SelectionSHA256 string                     `json:"selection_sha256"`
	StartIndex      int                        `json:"start_index"`
	Entries         []CompletePullJournalEntry `json:"entries"`
	WriteToken      string                     `json:"write_token"`
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (m *Mirror) completePullCheckpointPath(selectorSHA256 string) (string, error) {
	if !validSHA256(selectorSHA256) {
		return "", fmt.Errorf("%w: complete-pull selector hash is not canonical SHA-256", domain.ErrCheckFailed)
	}
	return filepath.Join(m.Root, ".atl", "complete-pulls", selectorSHA256+".json"), nil
}

func (m *Mirror) completePullProgressPath(selectorSHA256 string) (string, error) {
	checkpoint, err := m.completePullCheckpointPath(selectorSHA256)
	if err != nil {
		return "", err
	}
	return checkpoint[:len(checkpoint)-len(".json")] + ".progress.json", nil
}

func (m *Mirror) completePullJournalPath(selectorSHA256 string) (string, error) {
	checkpoint, err := m.completePullCheckpointPath(selectorSHA256)
	if err != nil {
		return "", err
	}
	return checkpoint[:len(checkpoint)-len(".json")] + ".journal.json", nil
}

func validateCompletePullCheckpoint(value CompletePullCheckpoint, expectedSelectorSHA256 string) error {
	if value.SchemaVersion != completePullCheckpointSchema {
		return fmt.Errorf("%w: unsupported complete-pull checkpoint schema %d", domain.ErrCheckFailed, value.SchemaVersion)
	}
	if !validCompletePullService(value.Service) || value.SelectorSHA256 != expectedSelectorSHA256 || !validSHA256(value.OptionsSHA256) || !validSHA256(value.SelectionSHA256) {
		return fmt.Errorf("%w: complete-pull checkpoint identity is invalid", domain.ErrCheckFailed)
	}
	if len(value.IDs) > maxCompletePullCheckpointIDs {
		return fmt.Errorf("%w: complete-pull checkpoint exceeds %d identities", domain.ErrCheckFailed, maxCompletePullCheckpointIDs)
	}
	if value.NextIndex < 0 || value.NextIndex > len(value.IDs) {
		return fmt.Errorf("%w: complete-pull checkpoint progress is outside its selection", domain.ErrCheckFailed)
	}
	seen := make(map[string]struct{}, len(value.IDs))
	for _, id := range value.IDs {
		if len(id) > maxCompletePullIDBytes || !positiveDecimalIdentity(id) {
			return fmt.Errorf("%w: complete-pull checkpoint contains an invalid identity", domain.ErrCheckFailed)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("%w: complete-pull checkpoint contains duplicate identity %q", domain.ErrCheckFailed, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateCompletePullJournal(journal completePullJournal, checkpoint CompletePullCheckpoint) error {
	if !validCompletePullService(journal.Service) || !validCompletePullJournalSchema(journal.Service, journal.SchemaVersion) {
		return fmt.Errorf("%w: unsupported complete-pull journal schema %d", domain.ErrCheckFailed, journal.SchemaVersion)
	}
	if journal.Service != checkpoint.Service || journal.SelectorSHA256 != checkpoint.SelectorSHA256 || journal.OptionsSHA256 != checkpoint.OptionsSHA256 || journal.SelectionSHA256 != checkpoint.SelectionSHA256 {
		return fmt.Errorf("%w: complete-pull journal binding does not match its immutable selection", domain.ErrCheckFailed)
	}
	if !validCompletePullWriteToken(journal.WriteToken) || !validCompletePullTempName(completePullJournalTemp(journal.WriteToken)) || !validCompletePullTempName(completePullSidecarTemp(journal.WriteToken)) || !validCompletePullTempName(completePullProgressTemp(journal.WriteToken)) {
		return fmt.Errorf("%w: complete-pull journal write ownership is invalid", domain.ErrCheckFailed)
	}
	if len(journal.Entries) == 0 || len(journal.Entries) > maxCompletePullJournalEntries || journal.StartIndex < 0 || journal.StartIndex+len(journal.Entries) > len(checkpoint.IDs) {
		return fmt.Errorf("%w: complete-pull journal range is outside its bounded selection", domain.ErrCheckFailed)
	}
	seen := make(map[string]struct{}, len(journal.Entries))
	for i, entry := range journal.Entries {
		if err := validateCompletePullJournalEntry(checkpoint.Service, entry); err != nil {
			return err
		}
		switch journal.Service {
		case CompletePullServiceJira:
			if err := validateCompletePullJiraEntrySchema(journal.SchemaVersion, entry); err != nil {
				return err
			}
		case CompletePullServiceConfluence:
			if err := validateCompletePullConfluenceEntrySchema(journal.SchemaVersion, entry); err != nil {
				return err
			}
		}
		expected := checkpoint.IDs[journal.StartIndex+i]
		actual := entry.State.ID
		if checkpoint.Service == CompletePullServiceJira {
			actual = entry.Identity
		}
		if actual != expected {
			return fmt.Errorf("%w: complete-pull journal contains a gap or unrequested identity at index %d", domain.ErrCheckFailed, journal.StartIndex+i)
		}
		if _, duplicate := seen[entry.State.ID]; duplicate {
			return fmt.Errorf("%w: complete-pull journal contains duplicate identity %q", domain.ErrCheckFailed, entry.State.ID)
		}
		seen[entry.State.ID] = struct{}{}
	}
	return nil
}

func decodeCompletePullJSON(path string, b []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: corrupt complete-pull state %s: %v", domain.ErrCheckFailed, path, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("%w: corrupt complete-pull state %s: trailing JSON value", domain.ErrCheckFailed, path)
	}
	return nil
}

func encodeCompletePullProgress(value completePullProgress) ([]byte, error) {
	if value.SchemaVersion == completePullConfluenceProgressSchema && value.Service == CompletePullServiceConfluence {
		legacy := completePullProgressConfluenceV3{
			SchemaVersion: value.SchemaVersion, Service: value.Service, SelectorSHA256: value.SelectorSHA256,
			OptionsSHA256: value.OptionsSHA256, SelectionSHA256: value.SelectionSHA256, NextIndex: value.NextIndex,
		}
		if value.Includes != nil {
			legacy.Includes = &struct {
				EvidenceComplete bool                         `json:"evidence_complete"`
				Assets           CompletePullIncludeAggregate `json:"assets"`
				Comments         CompletePullIncludeAggregate `json:"comments"`
			}{
				EvidenceComplete: value.Includes.EvidenceComplete,
				Assets:           value.Includes.Assets,
				Comments:         value.Includes.Comments,
			}
		}
		return json.MarshalIndent(legacy, "", "  ")
	}
	return json.MarshalIndent(value, "", "  ")
}

func readCompletePullFile(root, path string, maxBytes int) ([]byte, bool, error) {
	if maxBytes < 0 {
		return nil, false, fmt.Errorf("%w: complete-pull state %s has an invalid read bound", domain.ErrCheckFailed, path)
	}
	info, err := safepath.StatWithin(root, path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() < 0 || info.Size() > int64(maxBytes) {
		return nil, false, fmt.Errorf("%w: complete-pull state %s exceeds %d bytes", domain.ErrCheckFailed, path, maxBytes)
	}
	b, err := safepath.ReadFileWithinLimit(root, path, int64(maxBytes))
	if err != nil {
		return nil, false, fmt.Errorf("%w: read bounded complete-pull state %s: %v", domain.ErrCheckFailed, path, err)
	}
	after, afterErr := safepath.StatWithin(root, path)
	if afterErr != nil || !after.Mode().IsRegular() || after.Mode().Perm() != 0o600 || after.Size() != info.Size() || int64(len(b)) != after.Size() {
		return nil, false, fmt.Errorf("%w: complete-pull state %s changed beyond %d bytes while being read", domain.ErrCheckFailed, path, maxBytes)
	}
	return b, true, nil
}

func (m *Mirror) loadCompletePullSelection(selectorSHA256 string) (CompletePullCheckpoint, bool, error) {
	path, err := m.completePullCheckpointPath(selectorSHA256)
	if err != nil {
		return CompletePullCheckpoint{}, false, err
	}
	b, found, err := readCompletePullFile(m.Root, path, maxCompletePullCheckpointBytes)
	if err != nil || !found {
		return CompletePullCheckpoint{}, found, err
	}
	var value CompletePullCheckpoint
	if err := decodeCompletePullJSON(path, b, &value); err != nil {
		return CompletePullCheckpoint{}, false, err
	}
	if err := validateCompletePullCheckpoint(value, selectorSHA256); err != nil {
		return CompletePullCheckpoint{}, false, fmt.Errorf("%w in %s", err, path)
	}
	if value.NextIndex != 0 {
		return CompletePullCheckpoint{}, false, fmt.Errorf("%w: complete-pull selection manifest %s contains mutable progress", domain.ErrCheckFailed, path)
	}
	return value, true, nil
}

func (m *Mirror) loadCompletePullJournal(selectorSHA256 string) (completePullJournal, bool, error) {
	path, err := m.completePullJournalPath(selectorSHA256)
	if err != nil {
		return completePullJournal{}, false, err
	}
	b, found, err := readCompletePullFile(m.Root, path, maxCompletePullJournalBytes)
	if err != nil || !found {
		return completePullJournal{}, found, err
	}
	var journal completePullJournal
	if err := decodeCompletePullJSON(path, b, &journal); err != nil {
		return completePullJournal{}, false, err
	}
	return journal, true, nil
}

// appendCompletePullJournalOwned durably records one consecutive accepted page.
// ownerToken must already be present in the surviving publication intent when
// the journal is first created; later appends inherit the durable journal owner.
func (m *Mirror) appendCompletePullJournalOwned(checkpoint CompletePullCheckpoint, index int, entry CompletePullJournalEntry, ownerToken string) error {
	if checkpoint.SchemaVersion == 0 {
		checkpoint.SchemaVersion = completePullCheckpointSchema
	}
	if err := validateCompletePullCheckpoint(checkpoint, checkpoint.SelectorSHA256); err != nil {
		return err
	}
	if err := validateCompletePullJournalEntry(checkpoint.Service, entry); err != nil {
		return err
	}
	if index < checkpoint.NextIndex || index >= len(checkpoint.IDs) {
		return fmt.Errorf("%w: complete-pull journal append is not the next requested identity", domain.ErrCheckFailed)
	}
	selectedIdentity := entry.State.ID
	if checkpoint.Service == CompletePullServiceJira {
		selectedIdentity = entry.Identity
	}
	if checkpoint.IDs[index] != selectedIdentity {
		return fmt.Errorf("%w: complete-pull journal append is not the next requested identity", domain.ErrCheckFailed)
	}
	journal, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256)
	if err != nil {
		return err
	}
	if !found {
		intent, _, intentFound, err := m.readPublicationIntent(checkpoint.SelectorSHA256, checkpoint, true)
		if err != nil {
			return err
		}
		if !intentFound || intent.Index != index || intent.WriteToken != ownerToken || !reflect.DeepEqual(intent.Entry, entry) {
			return fmt.Errorf("%w: first complete-pull journal write has no matching durable publication intent", domain.ErrCheckFailed)
		}
		if index != checkpoint.NextIndex {
			return fmt.Errorf("%w: complete-pull journal must begin at durable checkpoint progress", domain.ErrCheckFailed)
		}
		journal = completePullJournal{
			SchemaVersion: completePullJournalSchemaForPublication(checkpoint.Service, intent.SchemaVersion, entry), Service: checkpoint.Service,
			SelectorSHA256: checkpoint.SelectorSHA256, OptionsSHA256: checkpoint.OptionsSHA256,
			SelectionSHA256: checkpoint.SelectionSHA256, StartIndex: index,
			Entries: []CompletePullJournalEntry{}, WriteToken: ownerToken,
		}
	} else if err := validateCompletePullJournal(journal, checkpoint); err != nil {
		return err
	}
	if journal.StartIndex+len(journal.Entries) != index {
		return fmt.Errorf("%w: complete-pull journal append would create a gap or duplicate", domain.ErrCheckFailed)
	}
	journal.Entries = append(journal.Entries, entry)
	if err := validateCompletePullJournal(journal, checkpoint); err != nil {
		return err
	}
	b, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	if len(b)+1 > maxCompletePullJournalBytes {
		return fmt.Errorf("%w: complete-pull journal would exceed %d bytes", domain.ErrCheckFailed, maxCompletePullJournalBytes)
	}
	path, err := m.completePullJournalPath(checkpoint.SelectorSHA256)
	if err != nil {
		return err
	}
	if err := safepath.MkdirAllWithin(m.Root, filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := m.writeCompletePullOwned(path, completePullJournalTemp(journal.WriteToken), append(b, '\n'), 0o600); err != nil {
		return err
	}
	return syncPublicationPath(m.Root, path, defaultCompletePullPublicationOps())
}

func (m *Mirror) verifyCompletePullJournalArtifacts(journal completePullJournal) error {
	for _, entry := range journal.Entries {
		state := entry.State
		nativePath := filepath.Join(m.Root, filepath.FromSlash(state.Path))
		body, err := safepath.ReadFileWithinLimit(m.Root, nativePath, maxCompletePullPublicationBytes)
		if err != nil || Hash(body) != state.Hash {
			return fmt.Errorf("%w: complete-pull journal native artifact for %q is missing or changed", domain.ErrCheckFailed, state.ID)
		}
		switch journal.Service {
		case CompletePullServiceConfluence:
			metaPath := strings.TrimSuffix(nativePath, ".csf") + ".meta.json"
			metaBytes, err := safepath.ReadFileWithinLimit(m.Root, metaPath, maxCompletePullPublicationBytes)
			var meta Meta
			if err != nil || json.Unmarshal(metaBytes, &meta) != nil || meta.ID != state.ID || meta.Version != state.Version || meta.Hash != state.Hash {
				return fmt.Errorf("%w: complete-pull journal metadata for %q does not prove the landed state", domain.ErrCheckFailed, state.ID)
			}
			base, present, err := m.ReadBaseBodyWithinLimit(state.ID, maxCompletePullPublicationBytes)
			if err != nil || !present || Hash(base) != state.Hash {
				return fmt.Errorf("%w: complete-pull journal baseline for %q is missing or changed", domain.ErrCheckFailed, state.ID)
			}
			if _, err := m.verifyConfluenceCompletePullAttachmentArtifacts(entry, true); err != nil {
				return fmt.Errorf("%w: complete-pull journal attachment artifacts for %q are missing or changed", domain.ErrCheckFailed, state.ID)
			}
		case CompletePullServiceJira:
			snapshotPath := strings.TrimSuffix(nativePath, ".wiki") + ".json"
			snapshotBytes, err := safepath.ReadFileWithinLimit(m.Root, snapshotPath, maxJiraCompletePullSnapshotBytes)
			var snapshot struct {
				Key    string          `json:"key"`
				ID     string          `json:"id"`
				Fields json.RawMessage `json:"fields"`
			}
			var fields map[string]json.RawMessage
			if err != nil || json.Unmarshal(snapshotBytes, &snapshot) != nil || snapshot.Key != state.ID || snapshot.ID != entry.Identity || json.Unmarshal(snapshot.Fields, &fields) != nil || fields == nil {
				return fmt.Errorf("%w: complete-pull Jira snapshot for %q does not prove the landed identity", domain.ErrCheckFailed, state.ID)
			}
			base, present, err := m.ReadBaseBodyExtWithinLimit(state.ID, ".wiki", maxCompletePullPublicationBytes)
			if err != nil || !present || Hash(base) != state.Hash {
				return fmt.Errorf("%w: complete-pull Jira baseline for %q is missing or changed", domain.ErrCheckFailed, state.ID)
			}
			if err := m.verifyJiraCompletePullOptionalArtifacts(entry); err != nil {
				return fmt.Errorf("%w: complete-pull Jira optional artifacts for %q are missing or changed", domain.ErrCheckFailed, state.ID)
			}
		default:
			return fmt.Errorf("%w: complete-pull journal service is invalid", domain.ErrCheckFailed)
		}
	}
	return nil
}

func (m *Mirror) mergeCompletePullJournal(journal completePullJournal) error {
	if journal.Service == CompletePullServiceJira {
		if err := m.mergeCompletePullEntriesOwned(journal.Entries, completePullSidecarTemp(journal.WriteToken)); err != nil {
			return err
		}
		return syncPublicationPath(m.Root, m.sidecarPath(), defaultCompletePullPublicationOps())
	}
	pages := make(map[string]SyncState, len(journal.Entries))
	views := make(map[string]ViewState, len(journal.Entries))
	staged := make(map[string]*StagedState, len(journal.Entries))
	for _, entry := range journal.Entries {
		pages[entry.State.ID] = entry.State
		views[entry.State.ID] = entry.View
		staged[entry.State.ID] = nil
	}
	if err := m.mergeSidecarPatchOwned(pages, views, staged, completePullSidecarTemp(journal.WriteToken)); err != nil {
		return err
	}
	return syncPublicationPath(m.Root, m.sidecarPath(), defaultCompletePullPublicationOps())
}

func (m *Mirror) removeCompletePullJournal(selectorSHA256 string) error {
	path, err := m.completePullJournalPath(selectorSHA256)
	if err != nil {
		return err
	}
	if err := safepath.RemoveWithin(m.Root, path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncPublicationPath(m.Root, path, defaultCompletePullPublicationOps())
}

// RecoverCompletePullJournal reconciles the only accepted-but-not-checkpointed
// prefix before local qualification. Each cross-file step is idempotent:
// artifact proof, shared sidecar merge, progress advance, then journal retire.
func (m *Mirror) RecoverCompletePullJournal(selectorSHA256 string, checkpoint CompletePullCheckpoint, checkpointFound bool) (CompletePullCheckpoint, error) {
	journal, found, err := m.loadCompletePullJournal(selectorSHA256)
	if err != nil || !found {
		return checkpoint, err
	}
	if !checkpointFound {
		return checkpoint, fmt.Errorf("%w: orphan complete-pull journal has no immutable selection", domain.ErrCheckFailed)
	}
	if checkpoint.SchemaVersion == 0 {
		checkpoint.SchemaVersion = completePullCheckpointSchema
	}
	if err := validateCompletePullCheckpoint(checkpoint, selectorSHA256); err != nil {
		return checkpoint, err
	}
	if err := validateCompletePullJournal(journal, checkpoint); err != nil {
		return checkpoint, err
	}
	end := journal.StartIndex + len(journal.Entries)
	if checkpoint.NextIndex != journal.StartIndex && checkpoint.NextIndex != end {
		return checkpoint, fmt.Errorf("%w: complete-pull journal does not begin at or end on durable progress", domain.ErrCheckFailed)
	}
	if err := m.verifyCompletePullJournalArtifacts(journal); err != nil {
		return checkpoint, err
	}
	if err := m.mergeCompletePullJournal(journal); err != nil {
		return checkpoint, err
	}
	if checkpoint.NextIndex == journal.StartIndex {
		if journal.Service == CompletePullServiceConfluence {
			if journal.SchemaVersion == completePullJournalSchema {
				checkpoint.Includes.EvidenceComplete = false
			} else {
				for _, entry := range journal.Entries {
					if err := applyCompletePullIncludeEvidence(&checkpoint.Includes, confluencePullEntryEvidence(entry)); err != nil {
						return checkpoint, err
					}
				}
			}
		}
		checkpoint.NextIndex = end
		if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
			return checkpoint, err
		}
	}
	if err := m.removeCompletePullJournal(selectorSHA256); err != nil {
		return checkpoint, err
	}
	return checkpoint, nil
}

// RetireCompletePullJournal removes only a valid journal fully covered by the
// supplied durable checkpoint. Corrupt, mismatched, or ahead-of-progress state
// is preserved and fails closed.
func (m *Mirror) RetireCompletePullJournal(checkpoint CompletePullCheckpoint) error {
	journal, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256)
	if err != nil || !found {
		return err
	}
	if err := validateCompletePullJournal(journal, checkpoint); err != nil {
		return err
	}
	if journal.StartIndex+len(journal.Entries) != checkpoint.NextIndex {
		return fmt.Errorf("%w: complete-pull journal is not covered by durable progress", domain.ErrCheckFailed)
	}
	if err := m.verifyCompletePullJournalArtifacts(journal); err != nil {
		return err
	}
	return m.removeCompletePullJournal(checkpoint.SelectorSHA256)
}

// CompletePullCheckpoint loads the active snapshot for one selector. Missing
// state means no resumable run exists; malformed state fails closed.
func (m *Mirror) CompletePullCheckpoint(selectorSHA256 string) (CompletePullCheckpoint, bool, error) {
	value, found, err := m.loadCompletePullSelection(selectorSHA256)
	if err != nil || !found {
		return CompletePullCheckpoint{}, found, err
	}
	progressPath, err := m.completePullProgressPath(selectorSHA256)
	if err != nil {
		return CompletePullCheckpoint{}, false, err
	}
	b, progressFound, err := readCompletePullFile(m.Root, progressPath, maxCompletePullProgressBytes)
	if err != nil {
		return CompletePullCheckpoint{}, false, err
	}
	if !progressFound {
		if value.Service == CompletePullServiceConfluence {
			value.Includes.EvidenceComplete = true
		}
		return value, true, nil
	}
	var progress completePullProgress
	if err := decodeCompletePullJSON(progressPath, b, &progress); err != nil {
		return CompletePullCheckpoint{}, false, err
	}
	if staleCompletePullProgressService(value.Service, progress) {
		if value.Service == CompletePullServiceConfluence {
			value.Includes.EvidenceComplete = true
		}
		return value, true, nil
	}
	legacyConfluence := value.Service == CompletePullServiceConfluence && progress.SchemaVersion == completePullProgressSchema && progress.Service == ""
	currentConfluenceProgress := value.Service == CompletePullServiceConfluence &&
		(progress.SchemaVersion == completePullConfluenceProgressSchema || progress.SchemaVersion == completePullConfluenceProgressSchema4) &&
		validCompletePullProgressService(value.Service, progress.Service)
	currentProgress := currentConfluenceProgress ||
		value.Service == CompletePullServiceJira && progress.SchemaVersion == completePullProgressSchemaFor(value.Service) && validCompletePullProgressService(value.Service, progress.Service)
	if !legacyConfluence && !currentProgress {
		return CompletePullCheckpoint{}, false, fmt.Errorf("%w: unsupported complete-pull progress schema %d in %s", domain.ErrCheckFailed, progress.SchemaVersion, progressPath)
	}
	if progress.SelectorSHA256 != value.SelectorSHA256 || progress.OptionsSHA256 != value.OptionsSHA256 || progress.SelectionSHA256 != value.SelectionSHA256 {
		// A crash after atomically replacing a restarted selection can leave the
		// previous tiny progress sidecar. Replaying from zero is conservative;
		// trusting or rejecting that stale prefix would make recovery worse.
		if value.Service == CompletePullServiceConfluence {
			value.Includes.EvidenceComplete = true
		}
		return value, true, nil
	}
	if progress.NextIndex < 0 || progress.NextIndex > len(value.IDs) {
		return CompletePullCheckpoint{}, false, fmt.Errorf("%w: complete-pull progress is outside its selection in %s", domain.ErrCheckFailed, progressPath)
	}
	value.NextIndex = progress.NextIndex
	if value.Service == CompletePullServiceConfluence {
		if legacyConfluence {
			value.Includes.EvidenceComplete = progress.NextIndex == 0
		} else if progress.Includes == nil {
			return CompletePullCheckpoint{}, false, fmt.Errorf("%w: current Confluence complete-pull progress omits include evidence in %s", domain.ErrCheckFailed, progressPath)
		} else {
			value.Includes = *progress.Includes
			if err := validateCompletePullIncludeProgressSchema(progress.SchemaVersion, value.Includes, value.NextIndex); err != nil {
				return CompletePullCheckpoint{}, false, fmt.Errorf("%w in %s", err, progressPath)
			}
			if value.NextIndex == 0 && value.Includes == (CompletePullIncludeProgress{}) {
				// There is no accepted prefix whose evidence could be unknown.
				// Normalize an explicit empty current object the same way as an
				// absent legacy progress file, without improving any durable page.
				value.Includes.EvidenceComplete = true
			}
		}
	} else if progress.Includes != nil {
		return CompletePullCheckpoint{}, false, fmt.Errorf("%w: Jira complete-pull progress contains Confluence include evidence in %s", domain.ErrCheckFailed, progressPath)
	}
	return value, true, nil
}

// SaveCompletePullCheckpoint atomically replaces one selector checkpoint.
// The long Confluence mutation lock serializes callers; the selector-specific
// file keeps independent unfinished snapshots from overwriting one another.
func (m *Mirror) SaveCompletePullCheckpoint(value CompletePullCheckpoint) error {
	if value.SchemaVersion == 0 {
		value.SchemaVersion = completePullCheckpointSchema
	}
	if err := validateCompletePullCheckpoint(value, value.SelectorSHA256); err != nil {
		return err
	}
	if value.Service == CompletePullServiceConfluence {
		if value.NextIndex == 0 && value.Includes == (CompletePullIncludeProgress{}) {
			value.Includes.EvidenceComplete = true
		}
		if err := validateCompletePullIncludeProgress(value.Includes, value.NextIndex); err != nil {
			return err
		}
	} else if value.Includes != (CompletePullIncludeProgress{}) {
		return fmt.Errorf("%w: Jira complete-pull checkpoint contains Confluence include progress", domain.ErrCheckFailed)
	}
	path, err := m.completePullCheckpointPath(value.SelectorSHA256)
	if err != nil {
		return err
	}
	if err := safepath.MkdirAllWithin(m.Root, filepath.Dir(path), 0o700); err != nil {
		return err
	}
	existing, found, err := m.loadCompletePullSelection(value.SelectorSHA256)
	if err != nil {
		return err
	}
	selectionChanged := !found || existing.Service != value.Service || existing.OptionsSHA256 != value.OptionsSHA256 || existing.SelectionSHA256 != value.SelectionSHA256 || !reflect.DeepEqual(existing.IDs, value.IDs)
	if !selectionChanged && value.Service == CompletePullServiceConfluence {
		current, currentFound, currentErr := m.CompletePullCheckpoint(value.SelectorSHA256)
		if currentErr != nil {
			return currentErr
		}
		if !currentFound {
			return fmt.Errorf("%w: complete-pull immutable selection disappeared while saving progress", domain.ErrCheckFailed)
		}
		if err := validateCompletePullIncludeAdvance(current.Includes, current.NextIndex, value.Includes, value.NextIndex); err != nil {
			return err
		}
	}
	publicationFound, publicationErr := m.hasCompletePullPublicationStage(value.SelectorSHA256)
	if publicationErr != nil {
		return publicationErr
	}
	journal, journalFound, journalErr := m.loadCompletePullJournal(value.SelectorSHA256)
	if journalErr != nil {
		return journalErr
	}
	if selectionChanged {
		if publicationFound {
			return fmt.Errorf("%w: recover the existing complete-pull publication before replacing its immutable selection", domain.ErrCheckFailed)
		}
		if journalFound {
			return fmt.Errorf("%w: recover the existing complete-pull journal before replacing its immutable selection", domain.ErrCheckFailed)
		}
		manifest := value
		manifest.NextIndex = 0
		b, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return err
		}
		if len(b)+1 > maxCompletePullCheckpointBytes {
			return fmt.Errorf("%w: complete-pull checkpoint would exceed %d bytes; narrow the selector or set --max-pages", domain.ErrCheckFailed, maxCompletePullCheckpointBytes)
		}
		if err := safepath.WriteFileWithin(m.Root, path, append(b, '\n'), 0o600); err != nil {
			return err
		}
		if err := syncPublicationPath(m.Root, path, defaultCompletePullPublicationOps()); err != nil {
			return err
		}
	} else if journalFound {
		if err := validateCompletePullJournal(journal, value); err != nil {
			return err
		}
		journalEnd := journal.StartIndex + len(journal.Entries)
		if value.NextIndex != journal.StartIndex && value.NextIndex != journalEnd {
			return fmt.Errorf("%w: complete-pull progress must remain at the journal start or advance to its exact end", domain.ErrCheckFailed)
		}
	}
	progress := completePullProgress{
		SchemaVersion: completePullProgressSchemaForCheckpoint(value.Service, value.Includes), SelectorSHA256: value.SelectorSHA256,
		OptionsSHA256: value.OptionsSHA256, SelectionSHA256: value.SelectionSHA256, NextIndex: value.NextIndex,
	}
	if value.Service == CompletePullServiceJira {
		progress.Service = value.Service
	} else {
		progress.Service = value.Service
		progress.Includes = &value.Includes
	}
	b, err := encodeCompletePullProgress(progress)
	if err != nil {
		return err
	}
	progressPath, err := m.completePullProgressPath(value.SelectorSHA256)
	if err != nil {
		return err
	}
	if journalFound {
		if err := m.writeCompletePullOwned(progressPath, completePullProgressTemp(journal.WriteToken), append(b, '\n'), 0o600); err != nil {
			return err
		}
	} else if err := safepath.WriteFileWithin(m.Root, progressPath, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return syncPublicationPath(m.Root, progressPath, defaultCompletePullPublicationOps())
}

// RemoveCompletePullCheckpoint retires a fully consumed selector snapshot.
func (m *Mirror) RemoveCompletePullCheckpoint(selectorSHA256 string) error {
	if found, err := m.hasCompletePullPublicationStage(selectorSHA256); err != nil {
		return err
	} else if found {
		return fmt.Errorf("%w: retire the complete-pull publication before removing its checkpoint", domain.ErrCheckFailed)
	}
	if _, journalFound, journalErr := m.loadCompletePullJournal(selectorSHA256); journalErr != nil {
		return journalErr
	} else if journalFound {
		return fmt.Errorf("%w: retire the covered complete-pull journal before removing its checkpoint", domain.ErrCheckFailed)
	}
	path, err := m.completePullCheckpointPath(selectorSHA256)
	if err != nil {
		return err
	}
	if err := safepath.RemoveWithin(m.Root, path); err != nil && !os.IsNotExist(err) {
		return err
	}
	progressPath, err := m.completePullProgressPath(selectorSHA256)
	if err != nil {
		return err
	}
	if err := safepath.RemoveWithin(m.Root, progressPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncPublicationPath(m.Root, progressPath, defaultCompletePullPublicationOps())
}
