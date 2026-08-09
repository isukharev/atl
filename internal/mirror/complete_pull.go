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
	completePullCheckpointSchema   = 1
	completePullProgressSchema     = 1 // legacy Confluence schema; bytes are immutable
	completePullJiraProgressSchema = 2
	maxCompletePullCheckpointBytes = 64 << 20
	maxCompletePullProgressBytes   = 4 << 10
	maxCompletePullCheckpointIDs   = 1_000_000
	maxCompletePullIDBytes         = 256
	completePullJournalSchema      = 2 // legacy Confluence schema; bytes are immutable
	completePullJiraJournalSchema  = 3
	maxCompletePullJournalEntries  = 25
	maxCompletePullJournalBytes    = 256 << 10
)

// CompletePullCheckpoint is a private, backend-neutral snapshot of one exact
// selector plus the prefix whose mirror commits are known durable. IDs contain
// identities only: credentials, backend URLs, page bodies, and titles never
// enter this resume artifact.
type CompletePullCheckpoint struct {
	SchemaVersion   int                 `json:"schema_version"`
	Service         CompletePullService `json:"service"`
	SelectorSHA256  string              `json:"selector_sha256"`
	OptionsSHA256   string              `json:"options_sha256"`
	SelectionSHA256 string              `json:"selection_sha256"`
	IDs             []string            `json:"ids"`
	NextIndex       int                 `json:"next_index"`
}

type completePullProgress struct {
	SchemaVersion   int                 `json:"schema_version"`
	Service         CompletePullService `json:"service,omitempty"`
	SelectorSHA256  string              `json:"selector_sha256"`
	OptionsSHA256   string              `json:"options_sha256"`
	SelectionSHA256 string              `json:"selection_sha256"`
	NextIndex       int                 `json:"next_index"`
}

// CompletePullJournalEntry is the content-minimized durable commit evidence
// for one accepted page. The native body, metadata, title, backend identity,
// and credentials remain in their existing private artifacts; recovery needs
// only the exact sidecar state and view policy that were pending in memory.
type CompletePullJournalEntry struct {
	Identity string    `json:"identity,omitempty"`
	State    SyncState `json:"state"`
	View     ViewState `json:"view"`
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
	if !validCompletePullService(journal.Service) || journal.SchemaVersion != completePullJournalSchemaFor(journal.Service) {
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

func readCompletePullFile(root, path string, maxBytes int) ([]byte, bool, error) {
	info, err := safepath.StatWithin(root, path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Size() > int64(maxBytes) {
		return nil, false, fmt.Errorf("%w: complete-pull state %s exceeds %d bytes", domain.ErrCheckFailed, path, maxBytes)
	}
	b, err := safepath.ReadFileWithin(root, path)
	if err != nil {
		return nil, false, err
	}
	if len(b) > maxBytes {
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
			SchemaVersion: completePullJournalSchemaFor(checkpoint.Service), Service: checkpoint.Service,
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
		body, err := safepath.ReadFileWithin(m.Root, nativePath)
		if err != nil || Hash(body) != state.Hash {
			return fmt.Errorf("%w: complete-pull journal native artifact for %q is missing or changed", domain.ErrCheckFailed, state.ID)
		}
		switch journal.Service {
		case CompletePullServiceConfluence:
			metaPath := strings.TrimSuffix(nativePath, ".csf") + ".meta.json"
			metaBytes, err := safepath.ReadFileWithin(m.Root, metaPath)
			var meta Meta
			if err != nil || json.Unmarshal(metaBytes, &meta) != nil || meta.ID != state.ID || meta.Version != state.Version || meta.Hash != state.Hash {
				return fmt.Errorf("%w: complete-pull journal metadata for %q does not prove the landed state", domain.ErrCheckFailed, state.ID)
			}
			base, present, err := m.ReadBaseBody(state.ID)
			if err != nil || !present || Hash(base) != state.Hash {
				return fmt.Errorf("%w: complete-pull journal baseline for %q is missing or changed", domain.ErrCheckFailed, state.ID)
			}
		case CompletePullServiceJira:
			snapshotPath := strings.TrimSuffix(nativePath, ".wiki") + ".json"
			snapshotBytes, err := safepath.ReadFileWithin(m.Root, snapshotPath)
			var snapshot struct {
				Key    string          `json:"key"`
				ID     string          `json:"id"`
				Fields json.RawMessage `json:"fields"`
			}
			var fields map[string]json.RawMessage
			if err != nil || json.Unmarshal(snapshotBytes, &snapshot) != nil || snapshot.Key != state.ID || snapshot.ID != entry.Identity || json.Unmarshal(snapshot.Fields, &fields) != nil || fields == nil {
				return fmt.Errorf("%w: complete-pull Jira snapshot for %q does not prove the landed identity", domain.ErrCheckFailed, state.ID)
			}
			base, present, err := m.ReadBaseBodyExt(state.ID, ".wiki")
			if err != nil || !present || Hash(base) != state.Hash {
				return fmt.Errorf("%w: complete-pull Jira baseline for %q is missing or changed", domain.ErrCheckFailed, state.ID)
			}
		default:
			return fmt.Errorf("%w: complete-pull journal service is invalid", domain.ErrCheckFailed)
		}
	}
	return nil
}

func (m *Mirror) mergeCompletePullJournal(journal completePullJournal) error {
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
		return value, true, nil
	}
	var progress completePullProgress
	if err := decodeCompletePullJSON(progressPath, b, &progress); err != nil {
		return CompletePullCheckpoint{}, false, err
	}
	if staleCompletePullProgressService(value.Service, progress) {
		return value, true, nil
	}
	if progress.SchemaVersion != completePullProgressSchemaFor(value.Service) || !validCompletePullProgressService(value.Service, progress.Service) {
		return CompletePullCheckpoint{}, false, fmt.Errorf("%w: unsupported complete-pull progress schema %d in %s", domain.ErrCheckFailed, progress.SchemaVersion, progressPath)
	}
	if progress.SelectorSHA256 != value.SelectorSHA256 || progress.OptionsSHA256 != value.OptionsSHA256 || progress.SelectionSHA256 != value.SelectionSHA256 {
		// A crash after atomically replacing a restarted selection can leave the
		// previous tiny progress sidecar. Replaying from zero is conservative;
		// trusting or rejecting that stale prefix would make recovery worse.
		return value, true, nil
	}
	if progress.NextIndex < 0 || progress.NextIndex > len(value.IDs) {
		return CompletePullCheckpoint{}, false, fmt.Errorf("%w: complete-pull progress is outside its selection in %s", domain.ErrCheckFailed, progressPath)
	}
	value.NextIndex = progress.NextIndex
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
		SchemaVersion: completePullProgressSchemaFor(value.Service), SelectorSHA256: value.SelectorSHA256,
		OptionsSHA256: value.OptionsSHA256, SelectionSHA256: value.SelectionSHA256, NextIndex: value.NextIndex,
	}
	if value.Service == CompletePullServiceJira {
		progress.Service = value.Service
	}
	b, err := json.MarshalIndent(progress, "", "  ")
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
