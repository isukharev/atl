package mirror

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const (
	completePullPublicationSchema       = 2
	maxCompletePullPublicationArtifacts = 2048
	maxCompletePullPublicationBytes     = 256 << 20
	maxCompletePullPublicationIntent    = 16 << 20
)

type completePullPublicationPreState struct {
	Present bool   `json:"present"`
	SHA256  string `json:"sha256,omitempty"`
	Mode    uint32 `json:"mode,omitempty"`
}

type completePullPublicationArtifact struct {
	Path       string                          `json:"path"`
	Pre        completePullPublicationPreState `json:"pre"`
	Remove     bool                            `json:"remove,omitempty"`
	BestEffort bool                            `json:"best_effort,omitempty"`
	Payload    string                          `json:"payload,omitempty"`
	SHA256     string                          `json:"sha256,omitempty"`
	Size       int64                           `json:"size,omitempty"`
	Mode       uint32                          `json:"mode,omitempty"`
	Temp       string                          `json:"temp,omitempty"`
}

type completePullPublicationRelocation struct {
	Artifacts []completePullPublicationArtifact `json:"artifacts"`
	Next      int                               `json:"next"`
}

type completePullPublicationIntent struct {
	SchemaVersion   int                                `json:"schema_version"`
	Service         string                             `json:"service"`
	SelectorSHA256  string                             `json:"selector_sha256"`
	OptionsSHA256   string                             `json:"options_sha256"`
	SelectionSHA256 string                             `json:"selection_sha256"`
	Index           int                                `json:"index"`
	Entry           CompletePullJournalEntry           `json:"entry"`
	Eligible        bool                               `json:"checkpoint_eligible"`
	Artifacts       []completePullPublicationArtifact  `json:"artifacts"`
	Next            int                                `json:"next"`
	StatePublished  bool                               `json:"state_published,omitempty"`
	Relocation      *completePullPublicationRelocation `json:"relocation,omitempty"`
	Committed       bool                               `json:"committed,omitempty"`
	WriteToken      string                             `json:"write_token"`
}

type completePullPublicationOps struct {
	write      func(root, target string, data []byte, mode os.FileMode) error
	writeOwned func(m *Mirror, target, temp string, data []byte, mode os.FileMode) error
	remove     func(root, target string) error
	sync       func(root, target string) error
	after      func(step string) error
}

func defaultCompletePullPublicationOps() completePullPublicationOps {
	return completePullPublicationOps{
		write: safepath.WriteFileWithin, remove: safepath.RemoveWithin,
		writeOwned: func(m *Mirror, target, temp string, data []byte, mode os.FileMode) error {
			return m.writeCompletePullOwned(target, temp, data, mode)
		},
		sync: safepath.SyncDirectoryWithin,
	}
}

func publicationAfter(ops completePullPublicationOps, step string) error {
	if ops.after != nil {
		return ops.after(step)
	}
	return nil
}

func syncPublicationPath(root, target string, ops completePullPublicationOps) error {
	dirs, err := registrationTargetDirectories(root, []preparedRegistrationArtifact{{path: target}})
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		if err := ops.sync(root, dir); err != nil {
			return err
		}
	}
	return nil
}

func (m *Mirror) completePullPublicationDir(selectorSHA256 string) (string, error) {
	checkpoint, err := m.completePullCheckpointPath(selectorSHA256)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(checkpoint, ".json") + ".publish", nil
}

func (m *Mirror) hasCompletePullPublicationStage(selectorSHA256 string) (bool, error) {
	dir, err := m.completePullPublicationDir(selectorSHA256)
	if err != nil {
		return false, err
	}
	_, err = safepath.StatWithin(m.Root, dir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func validateCompletePullPublication(intent completePullPublicationIntent, checkpoint CompletePullCheckpoint, stageDir string) error {
	if intent.SchemaVersion != completePullPublicationSchema || intent.Service != checkpoint.Service || intent.SelectorSHA256 != checkpoint.SelectorSHA256 || intent.OptionsSHA256 != checkpoint.OptionsSHA256 || intent.SelectionSHA256 != checkpoint.SelectionSHA256 {
		return fmt.Errorf("%w: complete-pull publication binding is invalid", domain.ErrCheckFailed)
	}
	if intent.Index < checkpoint.NextIndex || intent.Index >= len(checkpoint.IDs) || checkpoint.IDs[intent.Index] != intent.Entry.State.ID {
		return fmt.Errorf("%w: complete-pull publication index is outside the pending selection", domain.ErrCheckFailed)
	}
	if !validCompletePullWriteToken(intent.WriteToken) {
		return fmt.Errorf("%w: complete-pull publication write token is invalid", domain.ErrCheckFailed)
	}
	if err := validateCompletePullJournalEntry(intent.Entry); err != nil {
		return err
	}
	count := len(intent.Artifacts)
	if intent.Relocation != nil {
		count += len(intent.Relocation.Artifacts)
		if intent.Relocation.Next < 0 || intent.Relocation.Next > len(intent.Relocation.Artifacts) {
			return fmt.Errorf("%w: complete-pull relocation progress is invalid", domain.ErrCheckFailed)
		}
	}
	if count == 0 || count > maxCompletePullPublicationArtifacts || intent.Next < 0 || intent.Next > len(intent.Artifacts) {
		return fmt.Errorf("%w: complete-pull publication artifact bounds are invalid", domain.ErrCheckFailed)
	}
	seen := make(map[string]struct{}, count)
	var total int64
	privateArtifacts := 0
	tempNameBytes := len(completePullJournalTemp(intent.WriteToken)) + len(completePullSidecarTemp(intent.WriteToken)) + len(completePullProgressTemp(intent.WriteToken))
	writeIndex := 0
	validate := func(artifact completePullPublicationArtifact, allowPrivate bool) error {
		path, err := validatePublicationArtifact(artifact, stageDir, allowPrivate, intent.WriteToken, writeIndex)
		if err != nil {
			return err
		}
		if artifactPathIsPrivateBase(path) {
			privateArtifacts++
			expected := filepath.ToSlash(filepath.Join(".atl", "base", safepath.Segment(intent.Entry.State.ID)+filepath.Ext(filepath.FromSlash(intent.Entry.State.Path))))
			if artifact.Path != expected || artifact.Remove || artifact.BestEffort || artifact.Mode != 0o600 {
				return fmt.Errorf("private publication artifact is not the exact pristine base")
			}
		}
		if !artifact.Remove {
			tempNameBytes += len(artifact.Temp)
			writeIndex++
		}
		key, err := artifactPathCollisionKey(path)
		if err != nil {
			return err
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate publication destination")
		}
		seen[key] = struct{}{}
		total += artifact.Size
		return nil
	}
	for _, artifact := range intent.Artifacts {
		if err := validate(artifact, true); err != nil {
			return fmt.Errorf("%w: invalid complete-pull publication artifact: %v", domain.ErrCheckFailed, err)
		}
	}
	if intent.Relocation != nil {
		for _, artifact := range intent.Relocation.Artifacts {
			if err := validate(artifact, false); err != nil {
				return fmt.Errorf("%w: invalid complete-pull relocation artifact: %v", domain.ErrCheckFailed, err)
			}
		}
	}
	if privateArtifacts > 1 {
		return fmt.Errorf("%w: complete-pull publication contains duplicate private base artifacts", domain.ErrCheckFailed)
	}
	if total > maxCompletePullPublicationBytes {
		return fmt.Errorf("%w: complete-pull publication exceeds %d staged bytes", domain.ErrCheckFailed, maxCompletePullPublicationBytes)
	}
	tempNameCount := writeIndex + 3 // journal, sidecar, and progress plus write artifacts
	if tempNameCount > maxCompletePullPublicationArtifacts+3 || tempNameBytes > (maxCompletePullPublicationArtifacts+3)*maxCompletePullTempName {
		return fmt.Errorf("%w: complete-pull publication temporary-file declarations exceed their bound", domain.ErrCheckFailed)
	}
	if intent.Committed && (intent.Next != len(intent.Artifacts) || (intent.Relocation != nil && intent.Relocation.Next != len(intent.Relocation.Artifacts))) {
		return fmt.Errorf("%w: committed complete-pull publication has unfinished artifacts", domain.ErrCheckFailed)
	}
	return nil
}

func completePullPublicationResidueName(name string) bool {
	if strings.HasPrefix(name, "payload-") && len(name) == len("payload-")+4 {
		value := 0
		for _, r := range name[len("payload-"):] {
			if r < '0' || r > '9' {
				return false
			}
			value = value*10 + int(r-'0')
		}
		return value < maxCompletePullPublicationArtifacts
	}
	if strings.HasPrefix(name, ".tmp-") && len(name) == len(".tmp-")+16 {
		_, err := hex.DecodeString(name[len(".tmp-"):])
		return err == nil
	}
	return false
}

func completePullPublicationResidue(entry os.DirEntry) (os.FileInfo, bool) {
	if !completePullPublicationResidueName(entry.Name()) {
		return nil, false
	}
	info, err := entry.Info()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, false
	}
	return info, true
}

func (m *Mirror) cleanupAbandonedCompletePullPublication(dir string, entries []os.DirEntry) error {
	if len(entries) > maxCompletePullPublicationArtifacts+1 {
		return fmt.Errorf("%w: complete-pull publication stage without an intent exceeds its residue bound; preserving it", domain.ErrCheckFailed)
	}
	var total int64
	for _, entry := range entries {
		info, owned := completePullPublicationResidue(entry)
		if !owned || info.Size() < 0 || info.Size() > maxCompletePullPublicationBytes+maxCompletePullPublicationIntent-total {
			return fmt.Errorf("%w: complete-pull publication stage has no intent and contains unexpected evidence; preserving it", domain.ErrCheckFailed)
		}
		total += info.Size()
	}
	ops := defaultCompletePullPublicationOps()
	for _, entry := range entries {
		if err := ops.remove(m.Root, filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := ops.sync(m.Root, dir); err != nil {
		return err
	}
	if err := ops.remove(m.Root, dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncPublicationPath(m.Root, dir, ops)
}

func (m *Mirror) readPublicationIntent(selectorSHA256 string, checkpoint CompletePullCheckpoint, checkpointFound bool) (completePullPublicationIntent, string, bool, error) {
	if checkpointFound && checkpoint.SchemaVersion == 0 {
		checkpoint.SchemaVersion = completePullCheckpointSchema
	}
	dir, err := m.completePullPublicationDir(selectorSHA256)
	if err != nil {
		return completePullPublicationIntent{}, "", false, err
	}
	intentPath := filepath.Join(dir, "intent.json")
	info, dirErr := safepath.StatWithin(m.Root, dir)
	if os.IsNotExist(dirErr) {
		return completePullPublicationIntent{}, dir, false, nil
	}
	if dirErr != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return completePullPublicationIntent{}, dir, false, fmt.Errorf("%w: complete-pull publication stage is unsafe; preserve it for inspection", domain.ErrCheckFailed)
	}
	b, found, err := readCompletePullFile(m.Root, intentPath, maxCompletePullPublicationIntent)
	if err != nil {
		return completePullPublicationIntent{}, dir, false, err
	}
	if !found {
		entries, readErr := safepath.ReadDirWithin(m.Root, dir)
		if readErr != nil {
			return completePullPublicationIntent{}, dir, false, readErr
		}
		if !checkpointFound {
			return completePullPublicationIntent{}, dir, false, fmt.Errorf("%w: complete-pull publication stage has no immutable selection; preserving it", domain.ErrCheckFailed)
		}
		if err := validateCompletePullCheckpoint(checkpoint, selectorSHA256); err != nil {
			return completePullPublicationIntent{}, dir, false, err
		}
		if err := m.cleanupAbandonedCompletePullPublication(dir, entries); err != nil {
			return completePullPublicationIntent{}, dir, false, err
		}
		return completePullPublicationIntent{}, dir, false, nil
	}
	if !checkpointFound {
		return completePullPublicationIntent{}, dir, false, fmt.Errorf("%w: complete-pull publication intent has no immutable selection; preserving it", domain.ErrCheckFailed)
	}
	var intent completePullPublicationIntent
	if err := decodeCompletePullJSON(intentPath, b, &intent); err != nil {
		return completePullPublicationIntent{}, dir, false, err
	}
	validateStage := dir
	if intent.Committed {
		// Cleanup may already have removed any prefix of payloads. A committed
		// intent no longer needs them to establish state or acceptance.
		validateStage = ""
	}
	if err := validateCompletePullPublication(intent, checkpoint, validateStage); err != nil {
		return completePullPublicationIntent{}, dir, false, err
	}
	return intent, dir, true, nil
}

func (m *Mirror) savePublicationIntent(dir string, intent completePullPublicationIntent, ops completePullPublicationOps) error {
	b, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return err
	}
	if len(b)+1 > maxCompletePullPublicationIntent {
		return fmt.Errorf("%w: complete-pull publication intent exceeds %d bytes", domain.ErrCheckFailed, maxCompletePullPublicationIntent)
	}
	path := filepath.Join(dir, "intent.json")
	if err := ops.write(m.Root, path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return ops.sync(m.Root, dir)
}

func relocationPublicationArtifacts(m *Mirror, plan *PageRelocation) ([]CompletePullArtifact, error) {
	if plan == nil {
		return nil, nil
	}
	tombstone, err := json.MarshalIndent(relocationTombstone{ID: plan.id, CanonicalPath: plan.newRel}, "", "  ")
	if err != nil {
		return nil, err
	}
	rel, err := mirrorRelativePath(m.Root, plan.tombstonePath)
	if err != nil {
		return nil, err
	}
	tombstoneArtifactPath, err := NewPublicArtifactPath(rel)
	if err != nil {
		return nil, err
	}
	out := []CompletePullArtifact{{Path: tombstoneArtifactPath, Data: append(tombstone, '\n'), Mode: 0o600}}
	currentTombstone, err := publicationCurrent(m.Root, tombstoneArtifactPath)
	if err != nil {
		return nil, err
	}
	if plan.tombstoneExists {
		if !currentTombstone.Present || currentTombstone.SHA256 != plan.tombstoneHash {
			return nil, fmt.Errorf("%w: relocation ownership marker changed after qualification; preserving it", domain.ErrCheckFailed)
		}
	} else if currentTombstone.Present {
		return nil, fmt.Errorf("%w: relocation ownership marker appeared after qualification; preserving it", domain.ErrCheckFailed)
	}
	if plan.sourcePresent {
		for i, path := range []string{plan.oldCSF, plan.oldMD, plan.oldMeta} {
			rel, err := mirrorRelativePath(m.Root, path)
			if err != nil {
				return nil, err
			}
			artifactPath, pathErr := NewPublicArtifactPath(rel)
			if pathErr != nil {
				return nil, pathErr
			}
			current, currentErr := publicationCurrent(m.Root, artifactPath)
			expected := []string{plan.csfHash, plan.mdHash, plan.metaHash}[i]
			if currentErr != nil || !current.Present || current.SHA256 != expected {
				return nil, fmt.Errorf("%w: relocation source %s changed after qualification; preserving it", domain.ErrCheckFailed, rel)
			}
			out = append(out, CompletePullArtifact{Path: artifactPath, Remove: true})
		}
	}
	return out, nil
}

// PrepareCompletePullPublication durably stages one page's exact artifact set
// and writes its selector-bound intent before any canonical destination is
// mutated.
func (m *Mirror) PrepareCompletePullPublication(checkpoint CompletePullCheckpoint, index int, entry CompletePullJournalEntry, eligible bool, artifacts []CompletePullArtifact, relocation *PageRelocation) error {
	return m.prepareCompletePullPublicationWith(checkpoint, index, entry, eligible, artifacts, relocation, defaultCompletePullPublicationOps())
}

func (m *Mirror) prepareCompletePullPublicationWith(checkpoint CompletePullCheckpoint, index int, entry CompletePullJournalEntry, eligible bool, artifacts []CompletePullArtifact, relocation *PageRelocation, ops completePullPublicationOps) error {
	if checkpoint.SchemaVersion == 0 {
		checkpoint.SchemaVersion = completePullCheckpointSchema
	}
	if err := validateCompletePullCheckpoint(checkpoint, checkpoint.SelectorSHA256); err != nil {
		return err
	}
	if index < checkpoint.NextIndex || index >= len(checkpoint.IDs) || checkpoint.IDs[index] != entry.State.ID {
		return fmt.Errorf("%w: complete-pull publication is not the next selected identity", domain.ErrCheckFailed)
	}
	retirement, err := relocationPublicationArtifacts(m, relocation)
	if err != nil {
		return err
	}
	if len(artifacts)+len(retirement) == 0 || len(artifacts)+len(retirement) > maxCompletePullPublicationArtifacts {
		return fmt.Errorf("%w: complete-pull publication exceeds its artifact bound", domain.ErrCheckFailed)
	}
	var inputBytes int64
	for _, artifact := range append(append([]CompletePullArtifact(nil), artifacts...), retirement...) {
		if int64(len(artifact.Data)) > maxCompletePullPublicationBytes-inputBytes {
			return fmt.Errorf("%w: complete-pull publication exceeds %d staged bytes", domain.ErrCheckFailed, maxCompletePullPublicationBytes)
		}
		inputBytes += int64(len(artifact.Data))
	}
	dir, err := m.completePullPublicationDir(checkpoint.SelectorSHA256)
	if err != nil {
		return err
	}
	if _, _, found, err := m.readPublicationIntent(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
		return err
	} else if found {
		return fmt.Errorf("%w: recover the existing complete-pull publication before staging another page", domain.ErrCheckFailed)
	}
	if err := safepath.MkdirAllWithin(m.Root, dir, 0o700); err != nil {
		return err
	}
	info, err := safepath.StatWithin(m.Root, dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: complete-pull publication stage does not have mode 0700", domain.ErrCheckFailed)
	}
	intent := completePullPublicationIntent{
		SchemaVersion: completePullPublicationSchema, Service: checkpoint.Service,
		SelectorSHA256: checkpoint.SelectorSHA256, OptionsSHA256: checkpoint.OptionsSHA256,
		SelectionSHA256: checkpoint.SelectionSHA256, Index: index, Entry: entry,
		Eligible: eligible, Artifacts: make([]completePullPublicationArtifact, 0, len(artifacts)),
	}
	intent.WriteToken, err = newCompletePullWriteToken()
	if err != nil {
		return err
	}
	sequence := 0
	var total int64
	for _, artifact := range artifacts {
		prepared, prepErr := m.stagePublicationArtifact(dir, artifact, sequence, intent.WriteToken, ops)
		if prepErr != nil {
			return prepErr
		}
		intent.Artifacts = append(intent.Artifacts, prepared)
		total += prepared.Size
		if !prepared.Remove {
			sequence++
		}
	}
	if len(retirement) > 0 {
		intent.Relocation = &completePullPublicationRelocation{Artifacts: make([]completePullPublicationArtifact, 0, len(retirement))}
		for _, artifact := range retirement {
			prepared, prepErr := m.stagePublicationArtifact(dir, artifact, sequence, intent.WriteToken, ops)
			if prepErr != nil {
				return prepErr
			}
			intent.Relocation.Artifacts = append(intent.Relocation.Artifacts, prepared)
			total += prepared.Size
			if !prepared.Remove {
				sequence++
			}
		}
	}
	if len(intent.Artifacts)+len(retirement) > maxCompletePullPublicationArtifacts || total > maxCompletePullPublicationBytes {
		return fmt.Errorf("%w: complete-pull publication exceeds its artifact or byte bound", domain.ErrCheckFailed)
	}
	if err := ops.sync(m.Root, dir); err != nil {
		return err
	}
	if err := publicationAfter(ops, "staged_payloads"); err != nil {
		return err
	}
	if err := validateCompletePullPublication(intent, checkpoint, dir); err != nil {
		return err
	}
	if err := m.savePublicationIntent(dir, intent, ops); err != nil {
		return err
	}
	if err := syncPublicationPath(m.Root, filepath.Join(dir, "intent.json"), ops); err != nil {
		return err
	}
	return publicationAfter(ops, "intent")
}

func (m *Mirror) publishArtifact(dir string, artifact completePullPublicationArtifact, ops completePullPublicationOps) error {
	path, err := artifactPathFromDurable(artifact.Path)
	if err != nil {
		return fmt.Errorf("%w: invalid durable complete-pull destination", domain.ErrCheckFailed)
	}
	target, err := artifactPathTarget(m.Root, path)
	if err != nil {
		return err
	}
	current, err := publicationCurrent(m.Root, path)
	if err != nil {
		return fmt.Errorf("%w: inspect complete-pull destination %s: %v", domain.ErrCheckFailed, artifact.Path, err)
	}
	if publicationMatchesPost(current, artifact) && !publicationMatchesPre(current, artifact.Pre) {
		if !artifact.Remove {
			if err := m.removeCompletePullOwnedResidue(target, artifact.Temp, artifact.Size, os.FileMode(artifact.Mode)); err != nil {
				return err
			}
		}
		// The previous process may have completed the atomic mutation but died
		// before its containing directories were synced or intent progress was
		// advanced. Repeat the durability barrier before accepting the post-image.
		return syncPublicationPath(m.Root, target, ops)
	}
	if !publicationMatchesPre(current, artifact.Pre) {
		return fmt.Errorf("%w: complete-pull destination %s matches neither its reviewed pre-image nor staged post-image; preserving it", domain.ErrCheckFailed, artifact.Path)
	}
	if artifact.Remove {
		if current.Present {
			if err := ops.remove(m.Root, target); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	} else {
		payload, err := safepath.ReadFileWithin(m.Root, filepath.Join(dir, artifact.Payload))
		if err != nil || int64(len(payload)) != artifact.Size || Hash(payload) != artifact.SHA256 {
			return fmt.Errorf("%w: complete-pull staged payload %s is missing or changed", domain.ErrCheckFailed, artifact.Payload)
		}
		if err := safepath.MkdirAllWithin(m.Root, filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := ops.writeOwned(m, target, artifact.Temp, payload, os.FileMode(artifact.Mode)); err != nil {
			if !artifact.BestEffort {
				return err
			}
			if removeErr := ops.remove(m.Root, target); removeErr != nil && !os.IsNotExist(removeErr) {
				return fmt.Errorf("best-effort publication failed and stale view could not be removed: %w", removeErr)
			}
		}
	}
	if err := syncPublicationPath(m.Root, target, ops); err != nil {
		return err
	}
	current, err = publicationCurrent(m.Root, path)
	if err != nil || !publicationMatchesPost(current, artifact) {
		return fmt.Errorf("%w: complete-pull destination %s did not reach its exact postcondition", domain.ErrCheckFailed, artifact.Path)
	}
	return nil
}

func (m *Mirror) ensurePublicationJournalEntry(checkpoint CompletePullCheckpoint, intent completePullPublicationIntent) error {
	journal, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256)
	if err != nil {
		return err
	}
	if found {
		if err := validateCompletePullJournal(journal, checkpoint); err != nil {
			return err
		}
		position := intent.Index - journal.StartIndex
		if position >= 0 && position < len(journal.Entries) {
			if !reflect.DeepEqual(journal.Entries[position], intent.Entry) {
				return fmt.Errorf("%w: accepted journal entry differs from surviving publication intent", domain.ErrCheckFailed)
			}
			return nil
		}
	}
	return m.appendCompletePullJournalOwned(checkpoint, intent.Index, intent.Entry, intent.WriteToken)
}

func (m *Mirror) cleanupCompletePullPublication(dir string, intent completePullPublicationIntent, ops completePullPublicationOps) error {
	seen := map[string]bool{}
	allowed := map[string]bool{"intent.json": true}
	residue := make([]string, 0, 1)
	for _, artifact := range intent.Artifacts {
		if artifact.Payload != "" {
			allowed[artifact.Payload] = true
		}
	}
	if intent.Relocation != nil {
		for _, artifact := range intent.Relocation.Artifacts {
			if artifact.Payload != "" {
				allowed[artifact.Payload] = true
			}
		}
	}
	entries, err := safepath.ReadDirWithin(m.Root, dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if allowed[entry.Name()] {
			continue
		}
		if _, owned := completePullPublicationResidue(entry); owned && strings.HasPrefix(entry.Name(), ".tmp-") {
			residue = append(residue, entry.Name())
			continue
		}
		return fmt.Errorf("%w: complete-pull publication stage contains unexpected evidence %s; preserving it", domain.ErrCheckFailed, entry.Name())
	}
	removePayload := func(artifact completePullPublicationArtifact) error {
		if artifact.Payload == "" || seen[artifact.Payload] {
			return nil
		}
		seen[artifact.Payload] = true
		if err := ops.remove(m.Root, filepath.Join(dir, artifact.Payload)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	for _, artifact := range intent.Artifacts {
		if err := removePayload(artifact); err != nil {
			return err
		}
	}
	if intent.Relocation != nil {
		for _, artifact := range intent.Relocation.Artifacts {
			if err := removePayload(artifact); err != nil {
				return err
			}
		}
	}
	for _, name := range residue {
		if err := ops.remove(m.Root, filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := ops.sync(m.Root, dir); err != nil {
		return err
	}
	if err := ops.remove(m.Root, filepath.Join(dir, "intent.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := syncPublicationPath(m.Root, filepath.Join(dir, "intent.json"), ops); err != nil {
		return err
	}
	if err := ops.remove(m.Root, dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w: complete-pull publication stage was not empty at retirement; preserving it: %v", domain.ErrCheckFailed, err)
	}
	return syncPublicationPath(m.Root, dir, ops)
}

func (m *Mirror) verifyCommittedPublication(checkpoint CompletePullCheckpoint, intent completePullPublicationIntent) error {
	for _, artifact := range intent.Artifacts {
		path, pathErr := artifactPathFromDurable(artifact.Path)
		current, err := publicationCurrent(m.Root, path)
		if pathErr != nil || err != nil || !publicationMatchesPost(current, artifact) {
			return fmt.Errorf("%w: committed complete-pull artifact %s does not match its exact postcondition", domain.ErrCheckFailed, artifact.Path)
		}
	}
	if intent.Relocation != nil {
		state, ok, err := m.SyncStateOf(intent.Entry.State.ID)
		if err != nil || !ok || state != intent.Entry.State {
			return fmt.Errorf("%w: committed complete-pull relocation has no exact canonical state", domain.ErrCheckFailed)
		}
		view, ok, err := m.ViewStateOf(intent.Entry.State.ID)
		if err != nil || !ok || !reflect.DeepEqual(view, intent.Entry.View) {
			return fmt.Errorf("%w: committed complete-pull relocation has no exact view state", domain.ErrCheckFailed)
		}
		for _, artifact := range intent.Relocation.Artifacts {
			path, pathErr := artifactPathFromDurable(artifact.Path)
			current, err := publicationCurrent(m.Root, path)
			if pathErr != nil || err != nil || !publicationMatchesPost(current, artifact) {
				return fmt.Errorf("%w: committed complete-pull relocation artifact %s does not match its exact postcondition", domain.ErrCheckFailed, artifact.Path)
			}
		}
	}
	if intent.Eligible {
		journal, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256)
		if err != nil || !found {
			return fmt.Errorf("%w: committed complete-pull publication has no accepted journal evidence", domain.ErrCheckFailed)
		}
		if err := validateCompletePullJournal(journal, checkpoint); err != nil {
			return err
		}
		position := intent.Index - journal.StartIndex
		if position < 0 || position >= len(journal.Entries) || !reflect.DeepEqual(journal.Entries[position], intent.Entry) {
			return fmt.Errorf("%w: committed complete-pull publication differs from accepted journal evidence", domain.ErrCheckFailed)
		}
	} else {
		state, ok, err := m.SyncStateOf(intent.Entry.State.ID)
		if err != nil || !ok || state != intent.Entry.State {
			return fmt.Errorf("%w: committed ineligible complete-pull publication has no exact canonical state", domain.ErrCheckFailed)
		}
	}
	return nil
}

// RecoverCompletePullPublication finishes a staged page using no backend data.
// It is safe at every write/progress/acceptance boundary and runs before local
// mirror qualification on every complete-pull invocation.
func (m *Mirror) RecoverCompletePullPublication(selectorSHA256 string, checkpoint CompletePullCheckpoint, checkpointFound bool) error {
	return m.recoverCompletePullPublicationWith(selectorSHA256, checkpoint, checkpointFound, defaultCompletePullPublicationOps())
}

func (m *Mirror) recoverCompletePullPublicationWith(selectorSHA256 string, checkpoint CompletePullCheckpoint, checkpointFound bool, ops completePullPublicationOps) error {
	intent, dir, found, err := m.readPublicationIntent(selectorSHA256, checkpoint, checkpointFound)
	if err != nil || !found {
		return err
	}
	if intent.Committed {
		if err := m.verifyCommittedPublication(checkpoint, intent); err != nil {
			return err
		}
		return m.cleanupCompletePullPublication(dir, intent, ops)
	}
	for intent.Next < len(intent.Artifacts) {
		index := intent.Next
		if err := m.publishArtifact(dir, intent.Artifacts[index], ops); err != nil {
			return err
		}
		if err := publicationAfter(ops, fmt.Sprintf("artifact:%d", index)); err != nil {
			return err
		}
		intent.Next++
		if err := m.savePublicationIntent(dir, intent, ops); err != nil {
			return err
		}
	}
	if err := publicationAfter(ops, "fully_published"); err != nil {
		return err
	}
	if intent.Relocation != nil || !intent.Eligible {
		if !intent.StatePublished {
			if err := m.mergeSidecarPatchOwned(
				map[string]SyncState{intent.Entry.State.ID: intent.Entry.State},
				map[string]ViewState{intent.Entry.State.ID: intent.Entry.View},
				map[string]*StagedState{intent.Entry.State.ID: nil},
				completePullSidecarTemp(intent.WriteToken),
			); err != nil {
				return err
			}
			if err := ops.sync(m.Root, filepath.Dir(m.sidecarPath())); err != nil {
				return err
			}
			intent.StatePublished = true
			if err := m.savePublicationIntent(dir, intent, ops); err != nil {
				return err
			}
			if err := publicationAfter(ops, "state"); err != nil {
				return err
			}
		}
	}
	if intent.Relocation != nil {
		for intent.Relocation.Next < len(intent.Relocation.Artifacts) {
			index := intent.Relocation.Next
			if err := m.publishArtifact(dir, intent.Relocation.Artifacts[index], ops); err != nil {
				return err
			}
			if err := publicationAfter(ops, fmt.Sprintf("relocation:%d", index)); err != nil {
				return err
			}
			intent.Relocation.Next++
			if err := m.savePublicationIntent(dir, intent, ops); err != nil {
				return err
			}
		}
	}
	if intent.Eligible {
		if err := m.ensurePublicationJournalEntry(checkpoint, intent); err != nil {
			return err
		}
		if err := ops.sync(m.Root, filepath.Dir(dir)); err != nil {
			return err
		}
		if err := publicationAfter(ops, "accepted"); err != nil {
			return err
		}
	}
	intent.Committed = true
	if err := m.savePublicationIntent(dir, intent, ops); err != nil {
		return err
	}
	if err := publicationAfter(ops, "committed"); err != nil {
		return err
	}
	if err := m.cleanupCompletePullPublication(dir, intent, ops); err != nil {
		return err
	}
	return publicationAfter(ops, "retired")
}
