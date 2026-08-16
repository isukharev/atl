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
	completePullPublicationSchema            = 2 // legacy Confluence schema; bytes are immutable
	completePullJiraPublicationSchema        = 3 // legacy Jira schema without stable sidecar identity/relocation
	completePullJiraPublicationSchema4       = 4
	completePullJiraPublicationSchema5       = 5 // bounded durable pre-images for qualified auxiliary retirement
	completePullJiraPublicationSchema6       = 6 // exact Jira optional-evidence receipts
	completePullConfluencePublicationSchema  = 5 // legacy current schema without attachment evidence
	completePullConfluencePublicationSchema6 = 6 // attachment evidence without bounded retirement preimages
	completePullConfluencePublicationSchema7 = 7 // current include evidence with bounded durable preimages
	maxCompletePullPublicationArtifacts      = 2048
	maxCompletePullPublicationBytes          = 256 << 20
	maxCompletePullPublicationIntent         = 16 << 20
)

type completePullPublicationPreState struct {
	Present bool   `json:"present"`
	SHA256  string `json:"sha256,omitempty"`
	Mode    uint32 `json:"mode,omitempty"`
}

type completePullPublicationArtifact struct {
	Path       string                          `json:"path"`
	Role       CompletePullArtifactRole        `json:"role,omitempty"`
	Pre        completePullPublicationPreState `json:"pre"`
	PreSize    *int64                          `json:"pre_size,omitempty"`
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
	Service         CompletePullService                `json:"service"`
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

func validatePublicationArtifact(service CompletePullService, entry CompletePullJournalEntry, artifact completePullPublicationArtifact, stageDir string, allowPrivate bool, token string, writeIndex int) error {
	qualified, err := parseDurableArtifactPath(artifact.Path)
	if err != nil {
		return err
	}
	if !allowPrivate && qualified.class == artifactPathClassPrivateBase {
		return fmt.Errorf("publication path targets reserved private state")
	}
	if err := validateCompletePullArtifactRole(service, entry, qualified, artifact.Role, artifact.Mode, artifact.Remove, artifact.BestEffort); err != nil {
		return err
	}
	if artifact.Pre.Present {
		mode := os.FileMode(artifact.Pre.Mode)
		if !validSHA256(artifact.Pre.SHA256) || mode != mode.Perm() || mode.Perm() == 0 {
			return fmt.Errorf("invalid publication pre-image")
		}
	} else if artifact.Pre.SHA256 != "" || artifact.Pre.Mode != 0 {
		return fmt.Errorf("absent publication pre-image has a hash")
	}
	if artifact.PreSize != nil && (!artifact.Pre.Present || *artifact.PreSize < 0 || *artifact.PreSize > maxCompletePullPublicationBytes) {
		return fmt.Errorf("invalid bounded publication pre-image")
	}
	if artifact.Remove {
		if artifact.BestEffort || artifact.Payload != "" || artifact.SHA256 != "" || artifact.Size != 0 || artifact.Mode != 0 || artifact.Temp != "" {
			return fmt.Errorf("invalid publication removal")
		}
		return nil
	}
	if artifact.Payload == "" || filepath.Base(artifact.Payload) != artifact.Payload || strings.ContainsAny(artifact.Payload, "/\\:\x00") {
		return fmt.Errorf("invalid publication payload name")
	}
	if !validSHA256(artifact.SHA256) || artifact.Size < 0 || artifact.Size > maxCompletePullPublicationBytes {
		return fmt.Errorf("invalid publication payload identity")
	}
	mode := os.FileMode(artifact.Mode)
	if mode != mode.Perm() || mode.Perm() == 0 {
		return fmt.Errorf("invalid publication artifact mode")
	}
	if artifact.Temp != completePullArtifactTemp(token, writeIndex) || !validCompletePullTempName(artifact.Temp) {
		return fmt.Errorf("invalid publication temporary-file ownership")
	}
	if stageDir != "" {
		payloadPath := filepath.Join(stageDir, artifact.Payload)
		info, err := safepath.StatWithin(filepath.Dir(stageDir), payloadPath)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != artifact.Size {
			return fmt.Errorf("publication payload is missing or has unsafe metadata")
		}
		payload, err := safepath.ReadFileWithinLimit(filepath.Dir(stageDir), payloadPath, artifact.Size)
		if err != nil || int64(len(payload)) != artifact.Size || Hash(payload) != artifact.SHA256 {
			return fmt.Errorf("publication payload is missing or changed")
		}
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
		entries, readErr := safepath.ReadDirWithinLimit(m.Root, dir, maxCompletePullPublicationArtifacts+1)
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

func publicationCurrent(root, rel string) (completePullPublicationPreState, error) {
	current, _, err := publicationCurrentWithinLimit(root, rel, maxCompletePullPublicationBytes)
	return current, err
}

// publicationCurrentWithinLimit reads an existing publication path through a
// held contained root, using the caller's immutable size bound. Recovery uses
// this for journaled artifacts: a file that grew after an interrupted publish
// is rejected after at most its recorded size plus one byte, rather than being
// materialized before its postcondition can fail.
func publicationCurrentWithinLimit(root, rel string, maximum int64) (completePullPublicationPreState, int64, error) {
	if maximum < 0 || maximum > maxCompletePullPublicationBytes {
		return completePullPublicationPreState{}, 0, fmt.Errorf("inspect publication destination %s: invalid bounded size", rel)
	}
	target := filepath.Join(root, filepath.FromSlash(rel))
	info, err := safepath.StatWithin(root, target)
	if os.IsNotExist(err) {
		return completePullPublicationPreState{}, 0, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return completePullPublicationPreState{}, 0, fmt.Errorf("inspect publication destination %s: exceeds bounded preimage", rel)
	}
	b, err := safepath.ReadFileWithinLimit(root, target, info.Size())
	if err != nil {
		return completePullPublicationPreState{}, 0, err
	}
	after, afterErr := safepath.StatWithin(root, target)
	if afterErr != nil || !after.Mode().IsRegular() || after.Mode().Perm() != info.Mode().Perm() || after.Size() != info.Size() || int64(len(b)) != after.Size() {
		return completePullPublicationPreState{}, 0, fmt.Errorf("inspect publication destination %s: changed during bounded read", rel)
	}
	return completePullPublicationPreState{Present: true, SHA256: Hash(b), Mode: uint32(after.Mode().Perm())}, after.Size(), nil
}

// publicationCurrentForIntentTransition reads an artifact while recovery may
// still observe either its immutable preimage or its immutable postimage. A
// current schema-7 intent persists both relevant sizes; older intents retain
// the transaction-wide bound for compatibility. The maximum is only used for
// this short pre/post discrimination, not as permission to publish a larger
// payload.
func publicationCurrentForIntentTransition(root string, artifact completePullPublicationArtifact) (completePullPublicationPreState, error) {
	maximum := artifact.Size
	if artifact.Pre.Present {
		preMaximum := int64(maxCompletePullPublicationBytes)
		if artifact.PreSize != nil {
			preMaximum = *artifact.PreSize
		}
		if preMaximum > maximum {
			maximum = preMaximum
		}
	}
	if artifact.Remove && artifact.Pre.Present && artifact.PreSize != nil {
		maximum = *artifact.PreSize
	}
	current, _, err := publicationCurrentWithinLimit(root, artifact.Path, maximum)
	return current, err
}

// publicationCurrentForIntentPostcondition inspects a completed intent
// artifact through its immutable postcondition size. A removal has no payload,
// so even an unexpected replacement is rejected without materializing data.
func publicationCurrentForIntentPostcondition(root string, artifact completePullPublicationArtifact) (completePullPublicationPreState, error) {
	maximum := artifact.Size
	if artifact.Remove {
		maximum = 0
	}
	current, _, err := publicationCurrentWithinLimit(root, artifact.Path, maximum)
	return current, err
}

func publicationMatchesPre(current, pre completePullPublicationPreState) bool {
	return current == pre
}

func publicationMatchesPost(current completePullPublicationPreState, artifact completePullPublicationArtifact) bool {
	if artifact.Remove || (artifact.BestEffort && !current.Present) {
		return !current.Present
	}
	return current.Present && current.SHA256 == artifact.SHA256 && current.Mode == artifact.Mode
}

func completePullBoundRemoval(path ArtifactPath, pre completePullPublicationPreState) CompletePullArtifact {
	snapshot := pre
	return CompletePullArtifact{Path: path, Remove: true, expectedPre: &snapshot}
}

func completePullBoundRemovalWithSize(path ArtifactPath, pre completePullPublicationPreState, size int64) CompletePullArtifact {
	snapshot, boundedSize := pre, size
	return CompletePullArtifact{Path: path, Remove: true, expectedPre: &snapshot, expectedPreSize: &boundedSize}
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

func (m *Mirror) stagePublicationArtifact(service CompletePullService, dir string, input CompletePullArtifact, sequence int, token string, persistPreSize bool, ops completePullPublicationOps) (completePullPublicationArtifact, error) {
	rel, err := input.Path.relativeAny()
	if err != nil {
		return completePullPublicationArtifact{}, fmt.Errorf("%w: invalid complete-pull destination: %v", domain.ErrCheckFailed, err)
	}
	var (
		pre     completePullPublicationPreState
		preSize int64
	)
	if input.Remove && input.expectedPreSize != nil {
		pre, preSize, err = publicationCurrentWithinLimit(m.Root, rel, *input.expectedPreSize)
	} else {
		pre, preSize, err = publicationCurrentWithinLimit(m.Root, rel, maxCompletePullPublicationBytes)
	}
	if err != nil {
		return completePullPublicationArtifact{}, fmt.Errorf("%w: %v", domain.ErrCheckFailed, err)
	}
	out := completePullPublicationArtifact{Path: rel, Pre: pre, Remove: input.Remove, BestEffort: input.BestEffort}
	if pre.Present && persistPreSize {
		if input.expectedPreSize != nil && preSize != *input.expectedPreSize {
			return completePullPublicationArtifact{}, fmt.Errorf("%w: complete-pull removal source changed after qualification: %s", domain.ErrCheckFailed, rel)
		}
		boundedSize := preSize
		out.PreSize = &boundedSize
	}
	if service == CompletePullServiceJira {
		out.Role = input.Role
	}
	if input.Remove {
		if input.BestEffort || len(input.Data) != 0 || input.Mode != 0 {
			return completePullPublicationArtifact{}, fmt.Errorf("%w: invalid complete-pull removal for %s", domain.ErrCheckFailed, rel)
		}
		if input.expectedPreSize != nil && (*input.expectedPreSize < 0 || *input.expectedPreSize > maxCompletePullPublicationBytes) {
			return completePullPublicationArtifact{}, fmt.Errorf("%w: invalid complete-pull removal size for %s", domain.ErrCheckFailed, rel)
		}
		if input.expectedPre != nil {
			expected := *input.expectedPre
			if !expected.Present || !validSHA256(expected.SHA256) || expected.Mode == 0 ||
				!pre.Present || pre.SHA256 != expected.SHA256 || pre.Mode != expected.Mode {
				return completePullPublicationArtifact{}, fmt.Errorf("%w: complete-pull removal source changed after qualification: %s", domain.ErrCheckFailed, rel)
			}
		}
		return out, nil
	}
	if input.expectedPre != nil {
		return completePullPublicationArtifact{}, fmt.Errorf("%w: complete-pull write cannot bind a removal preimage: %s", domain.ErrCheckFailed, rel)
	}
	if input.Mode != input.Mode.Perm() || input.Mode.Perm() == 0 {
		return completePullPublicationArtifact{}, fmt.Errorf("%w: invalid complete-pull mode for %s", domain.ErrCheckFailed, rel)
	}
	out.Payload = fmt.Sprintf("payload-%04d", sequence)
	out.Temp = completePullArtifactTemp(token, sequence)
	out.SHA256 = Hash(input.Data)
	out.Size = int64(len(input.Data))
	out.Mode = uint32(input.Mode.Perm())
	if err := ops.write(m.Root, filepath.Join(dir, out.Payload), input.Data, 0o600); err != nil {
		return completePullPublicationArtifact{}, err
	}
	return out, nil
}

func relocationPublicationArtifacts(m *Mirror, plan *PageRelocation) ([]CompletePullArtifact, error) {
	if plan == nil {
		return nil, nil
	}
	tombstone, err := json.MarshalIndent(relocationTombstone{ID: plan.id, CanonicalPath: plan.newRel}, "", "  ")
	if err != nil {
		return nil, err
	}
	rel, err := PublicArtifactPathWithin(m.Root, plan.tombstonePath)
	if err != nil {
		return nil, err
	}
	out := []CompletePullArtifact{{Path: rel, Data: append(tombstone, '\n'), Mode: 0o600}}
	currentTombstone, err := publicationCurrent(m.Root, rel.String())
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
			rel, err := PublicArtifactPathWithin(m.Root, path)
			if err != nil {
				return nil, err
			}
			current, currentErr := publicationCurrent(m.Root, rel.String())
			expected := []string{plan.csfHash, plan.mdHash, plan.metaHash}[i]
			if currentErr != nil || !current.Present || current.SHA256 != expected {
				return nil, fmt.Errorf("%w: relocation source %s changed after qualification; preserving it", domain.ErrCheckFailed, rel.String())
			}
			out = append(out, completePullBoundRemoval(rel, current))
		}
		for _, owned := range plan.owned {
			rel, err := PublicArtifactPathWithin(m.Root, owned.path)
			if err != nil {
				return nil, err
			}
			current, data, found, currentErr := m.readQualifiedConfluenceAttachmentPrivate(rel.String(), confluenceAttachmentArtifactLimit(rel.String()))
			if currentErr != nil || !found || current.SHA256 != owned.hash || int64(len(data)) != owned.size ||
				owned.mode != 0 && current.Mode != uint32(owned.mode.Perm()) {
				return nil, fmt.Errorf("%w: relocation source %s changed after qualification; preserving it", domain.ErrCheckFailed, rel.String())
			}
			out = append(out, completePullBoundRemovalWithSize(rel, current, int64(len(data))))
		}
	}
	return out, nil
}

// PrepareCompletePullPublication durably stages one page's exact artifact set
// and its selector-bound intent before any canonical destination is mutated.
func (m *Mirror) PrepareCompletePullPublication(checkpoint CompletePullCheckpoint, index int, entry CompletePullJournalEntry, eligible bool, artifacts []CompletePullArtifact, relocation *PageRelocation) error {
	return m.prepareCompletePullPublicationWith(checkpoint, index, entry, eligible, artifacts, relocation, defaultCompletePullPublicationOps())
}

// CompletePullPublicationArtifactSlots returns the remaining artifact slots
// after callers have reserved their known replacement and retirement entries.
// It is deliberately checked before optional binary reads: complete-pull
// publication has one finite transaction, so a later staging failure must not
// be the first indication that a requested body cannot be retained.
func (m *Mirror) CompletePullPublicationArtifactSlots(replacementArtifacts, retirementArtifacts int) (int, error) {
	if m == nil || replacementArtifacts < 0 || retirementArtifacts < 0 ||
		replacementArtifacts > maxCompletePullPublicationArtifacts ||
		retirementArtifacts > maxCompletePullPublicationArtifacts-replacementArtifacts {
		return 0, fmt.Errorf("%w: complete-pull publication exceeds its artifact bound", domain.ErrCheckFailed)
	}
	return maxCompletePullPublicationArtifacts - replacementArtifacts - retirementArtifacts, nil
}

// CompletePullPublicationPayloadBytes returns the payload bytes still
// available after exact known replacement bytes and a caller-provided bounded
// reservation. Optional artifact planners use it before reading a binary body;
// a later staging failure must not be the first indication that the atomic
// page transaction cannot retain that body.
func (m *Mirror) CompletePullPublicationPayloadBytes(replacementBytes, reserveBytes int64) (int64, error) {
	if m == nil || replacementBytes < 0 || reserveBytes < 0 ||
		replacementBytes > maxCompletePullPublicationBytes ||
		reserveBytes > maxCompletePullPublicationBytes-replacementBytes {
		return 0, fmt.Errorf("%w: complete-pull publication exceeds its staged-byte bound", domain.ErrCheckFailed)
	}
	return maxCompletePullPublicationBytes - replacementBytes - reserveBytes, nil
}

// PageRelocationPublicationArtifactFootprint revalidates a Confluence
// relocation plan and reports its exact atomic-publication artifact and
// payload-byte footprint, including the nonempty ownership tombstone and any
// qualified attachment retirements. Optional-body planners reserve the bytes
// before their first GET, so a later publication failure cannot reveal an
// unaccounted relocation payload.
func (m *Mirror) PageRelocationPublicationArtifactFootprint(plan *PageRelocation) (int, int64, error) {
	artifacts, err := relocationPublicationArtifacts(m, plan)
	if err != nil {
		return 0, 0, err
	}
	var bytes int64
	for _, artifact := range artifacts {
		next := int64(len(artifact.Data))
		if next < 0 || bytes+next < bytes || bytes+next > maxCompletePullPublicationBytes {
			return 0, 0, fmt.Errorf("%w: complete-pull relocation exceeds its staged-byte bound", domain.ErrCheckFailed)
		}
		bytes += next
	}
	return len(artifacts), bytes, nil
}

// PageRelocationPublicationArtifactCount retains the count-only projection for
// callers that do not schedule optional payloads. New body-capacity callers
// must use PageRelocationPublicationArtifactFootprint.
func (m *Mirror) PageRelocationPublicationArtifactCount(plan *PageRelocation) (int, error) {
	count, _, err := m.PageRelocationPublicationArtifactFootprint(plan)
	return count, err
}

func (m *Mirror) prepareCompletePullPublicationWith(checkpoint CompletePullCheckpoint, index int, entry CompletePullJournalEntry, eligible bool, artifacts []CompletePullArtifact, relocation *PageRelocation, ops completePullPublicationOps) error {
	return m.prepareCompletePullPublicationWithJira(checkpoint, index, entry, eligible, artifacts, relocation, nil, ops)
}

// PrepareJiraCompletePullPublication is the Jira-only key-relocation variant.
// relocation is nil for an unchanged key and otherwise carries a mirror-owned,
// hash-bound retirement plan returned by PlanJiraIssueRelocation.
func (m *Mirror) PrepareJiraCompletePullPublication(checkpoint CompletePullCheckpoint, index int, entry CompletePullJournalEntry, eligible bool, artifacts []CompletePullArtifact, relocation *JiraIssueRelocation) error {
	return m.prepareCompletePullPublicationWithJira(checkpoint, index, entry, eligible, artifacts, nil, relocation, defaultCompletePullPublicationOps())
}

func (m *Mirror) prepareCompletePullPublicationWithJira(checkpoint CompletePullCheckpoint, index int, entry CompletePullJournalEntry, eligible bool, artifacts []CompletePullArtifact, relocation *PageRelocation, jiraRelocation *JiraIssueRelocation, ops completePullPublicationOps) error {
	if checkpoint.SchemaVersion == 0 {
		checkpoint.SchemaVersion = completePullCheckpointSchema
	}
	if jiraRelocation != nil {
		if checkpoint.Service != CompletePullServiceJira || relocation != nil || jiraRelocation.identity != entry.Identity {
			return fmt.Errorf("%w: Jira relocation does not match the complete-pull entry", domain.ErrCheckFailed)
		}
		entry.Previous = &jiraRelocation.previous
	}
	if err := validateCompletePullCheckpoint(checkpoint, checkpoint.SelectorSHA256); err != nil {
		return err
	}
	if err := validateCompletePullJournalEntry(checkpoint.Service, entry); err != nil {
		return err
	}
	if index < checkpoint.NextIndex || index >= len(checkpoint.IDs) {
		return fmt.Errorf("%w: complete-pull publication is not the next selected identity", domain.ErrCheckFailed)
	}
	selectedIdentity := entry.State.ID
	if checkpoint.Service == CompletePullServiceJira {
		selectedIdentity = entry.Identity
	}
	if checkpoint.IDs[index] != selectedIdentity {
		return fmt.Errorf("%w: complete-pull publication is not the next selected identity", domain.ErrCheckFailed)
	}
	if (checkpoint.Service == CompletePullServiceJira && relocation != nil) ||
		(checkpoint.Service == CompletePullServiceConfluence && jiraRelocation != nil) {
		return fmt.Errorf("%w: Jira complete-pull publication cannot relocate a Confluence page", domain.ErrCheckFailed)
	}
	retirement, err := relocationPublicationArtifacts(m, relocation)
	if err != nil {
		return err
	}
	if jiraRelocation != nil {
		retirement, err = m.jiraRelocationArtifacts(jiraRelocation)
		if err != nil {
			return err
		}
	}
	if len(artifacts)+len(retirement) == 0 || len(artifacts)+len(retirement) > maxCompletePullPublicationArtifacts {
		return fmt.Errorf("%w: complete-pull publication exceeds its artifact bound", domain.ErrCheckFailed)
	}
	var inputBytes int64
	roles := make([]CompletePullArtifactRole, 0, len(artifacts))
	for _, artifact := range append(append([]CompletePullArtifact(nil), artifacts...), retirement...) {
		qualified, pathErr := artifact.Path.relativeAny()
		if pathErr != nil {
			return fmt.Errorf("%w: complete-pull publication contains an unqualified destination", domain.ErrCheckFailed)
		}
		parsed, pathErr := parseDurableArtifactPath(qualified)
		if pathErr != nil {
			return pathErr
		}
		if pathErr := validateCompletePullArtifactRole(checkpoint.Service, entry, parsed, artifact.Role, uint32(artifact.Mode), artifact.Remove, artifact.BestEffort); pathErr != nil {
			return fmt.Errorf("%w: invalid complete-pull artifact role: %v", domain.ErrCheckFailed, pathErr)
		}
		if int64(len(artifact.Data)) > maxCompletePullPublicationBytes-inputBytes {
			return fmt.Errorf("%w: complete-pull publication exceeds %d staged bytes", domain.ErrCheckFailed, maxCompletePullPublicationBytes)
		}
		inputBytes += int64(len(artifact.Data))
	}
	for _, artifact := range artifacts {
		roles = append(roles, artifact.Role)
	}
	switch checkpoint.Service {
	case CompletePullServiceConfluence:
		if err := validateConfluenceCompletePullPayloads(entry, artifacts); err != nil {
			return fmt.Errorf("%w: %v", domain.ErrCheckFailed, err)
		}
	case CompletePullServiceJira:
		if err := validateJiraArtifactRoleCounts(roles); err != nil {
			return fmt.Errorf("%w: %v", domain.ErrCheckFailed, err)
		}
		if err := validateJiraCompletePullPayloads(entry, artifacts); err != nil {
			return fmt.Errorf("%w: %v", domain.ErrCheckFailed, err)
		}
		if entry.State.Identity != "" || entry.Previous != nil {
			evidence, evidenceErr := completePullJiraOptionalEvidenceFromArtifacts(entry, artifacts)
			if evidenceErr != nil {
				return fmt.Errorf("%w: %v", domain.ErrCheckFailed, evidenceErr)
			}
			if entry.JiraOptionalEvidence != nil && *entry.JiraOptionalEvidence != evidence {
				return fmt.Errorf("%w: Jira complete-pull optional evidence does not match staged artifacts", domain.ErrCheckFailed)
			}
			entry.JiraOptionalEvidence = &evidence
		}
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
		SchemaVersion: completePullPublicationSchemaForEntry(checkpoint.Service, entry), Service: checkpoint.Service,
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
		persistPreSize := intent.SchemaVersion == completePullConfluencePublicationSchema7 ||
			(checkpoint.Service == CompletePullServiceJira && (intent.SchemaVersion == completePullJiraPublicationSchema5 || intent.SchemaVersion == completePullJiraPublicationSchema6))
		prepared, prepErr := m.stagePublicationArtifact(checkpoint.Service, dir, artifact, sequence, intent.WriteToken, persistPreSize, ops)
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
			persistPreSize := intent.SchemaVersion == completePullConfluencePublicationSchema7 ||
				(checkpoint.Service == CompletePullServiceJira && (intent.SchemaVersion == completePullJiraPublicationSchema5 || intent.SchemaVersion == completePullJiraPublicationSchema6))
			prepared, prepErr := m.stagePublicationArtifact(checkpoint.Service, dir, artifact, sequence, intent.WriteToken, persistPreSize, ops)
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
	current, err := publicationCurrentForIntentTransition(m.Root, artifact)
	if err != nil {
		return fmt.Errorf("%w: inspect complete-pull destination %s: %v", domain.ErrCheckFailed, artifact.Path, err)
	}
	if publicationMatchesPost(current, artifact) && !publicationMatchesPre(current, artifact.Pre) {
		if !artifact.Remove {
			target := filepath.Join(m.Root, filepath.FromSlash(artifact.Path))
			if err := m.removeCompletePullOwnedResidue(target, artifact.Temp, artifact.Size, os.FileMode(artifact.Mode)); err != nil {
				return err
			}
		}
		// The previous process may have completed the atomic mutation but died
		// before its containing directories were synced or intent progress was
		// advanced. Repeat the durability barrier before accepting the post-image.
		target := filepath.Join(m.Root, filepath.FromSlash(artifact.Path))
		return syncPublicationPath(m.Root, target, ops)
	}
	if !publicationMatchesPre(current, artifact.Pre) {
		return fmt.Errorf("%w: complete-pull destination %s matches neither its reviewed pre-image nor staged post-image; preserving it", domain.ErrCheckFailed, artifact.Path)
	}
	target := filepath.Join(m.Root, filepath.FromSlash(artifact.Path))
	if artifact.Remove {
		if current.Present {
			if err := ops.remove(m.Root, target); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	} else {
		payload, err := safepath.ReadFileWithinLimit(m.Root, filepath.Join(dir, artifact.Payload), artifact.Size)
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
	current, err = publicationCurrentForIntentPostcondition(m.Root, artifact)
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
	entries, err := safepath.ReadDirWithinLimit(m.Root, dir, maxCompletePullPublicationArtifacts+1)
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
			var stateErr error
			if intent.Service == CompletePullServiceJira {
				stateErr = m.mergeCompletePullEntriesOwned([]CompletePullJournalEntry{intent.Entry}, completePullSidecarTemp(intent.WriteToken))
			} else {
				stateErr = m.mergeSidecarPatchOwned(
					map[string]SyncState{intent.Entry.State.ID: intent.Entry.State},
					map[string]ViewState{intent.Entry.State.ID: intent.Entry.View},
					map[string]*StagedState{intent.Entry.State.ID: nil},
					completePullSidecarTemp(intent.WriteToken),
				)
			}
			if stateErr != nil {
				return stateErr
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
