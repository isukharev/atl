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

const (
	// MaxJiraAttachmentBodyMaterializationBytes is the hard per-body bound for
	// the resumable Jira materializer. It is deliberately independent from the
	// lower complete-pull page envelope: each materialization transaction stages
	// one body and one sidecar, never a whole issue replacement.
	MaxJiraAttachmentBodyMaterializationBytes      int64 = 128 << 20
	maxJiraAttachmentBodyMaterializationStateBytes       = 256 << 20
)

// JiraAttachmentBodyInventory is the content-free local descriptor consumed
// by the app layer before one bounded body request. The attachment metadata is
// already bound to the native Jira snapshot and its backend population; body
// bytes are never retained here.
type JiraAttachmentBodyInventory struct {
	Identity       string
	ParentRevision string
	BodiesState    AttachmentBodiesState
	Attachments    []AttachmentSidecarRecord
}

type jiraAttachmentBodyMaterializationInspection struct {
	identity       string
	state          SyncState
	stem           string
	sidecarPath    string
	sidecarData    []byte
	sidecar        AttachmentSidecarV1
	metadata       []byte
	capturedBodies map[string]AttachmentSidecarBody
}

// RefuseActiveCompletePullState prevents a body materializer from racing an
// unfinished primary complete pull. Completed pulls leave this directory empty;
// a nonempty directory is intentionally preserved for its owning recovery
// command rather than interpreted or retired here.
func (m *Mirror) RefuseActiveCompletePullState() error {
	if m == nil {
		return fmt.Errorf("%w: mirror is unavailable", domain.ErrCheckFailed)
	}
	dir := filepath.Join(m.Root, ".atl", "complete-pulls")
	entries, err := safepath.ReadDirWithinLimit(m.Root, dir, 1)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect complete-pull control state", domain.ErrCheckFailed)
	}
	if len(entries) != 0 {
		return fmt.Errorf("%w: an incomplete complete pull owns local publication state; resume or retire it before materializing Jira attachment bodies", domain.ErrCheckFailed)
	}
	return nil
}

// JiraAttachmentBodyInventories fully revalidates every qualified Jira
// attachment receipt currently tracked by the mirror. It is the expensive
// admission boundary for a new materialization invocation: captured payloads
// are hash-checked once before a command may retain more bodies. Subsequent
// transactions use the narrower candidate inspection and are finally checked
// again before reporting completion.
func (m *Mirror) JiraAttachmentBodyInventories() ([]JiraAttachmentBodyInventory, error) {
	if m == nil {
		return nil, fmt.Errorf("%w: mirror is unavailable", domain.ErrCheckFailed)
	}
	states, err := m.SyncStates()
	if err != nil {
		return nil, err
	}
	if len(states) > maxCompletePullCheckpointIDs {
		return nil, fmt.Errorf("%w: Jira attachment inventory roster exceeds its supported bound", domain.ErrCheckFailed)
	}
	seen := make(map[string]struct{})
	out := make([]JiraAttachmentBodyInventory, 0)
	for _, state := range states {
		if state.Version != 0 || !strings.HasSuffix(state.Path, ".wiki") {
			continue
		}
		inspection, found, inspectErr := m.inspectJiraAttachmentBodyInventoryForState(state)
		if inspectErr != nil {
			return nil, inspectErr
		}
		if !found {
			continue
		}
		if _, duplicate := seen[inspection.identity]; duplicate {
			return nil, fmt.Errorf("%w: more than one Jira attachment sidecar claims one stable identity", domain.ErrCheckFailed)
		}
		seen[inspection.identity] = struct{}{}
		if _, progressErr := jiraAttachmentBodyProgress(inspection.sidecar); progressErr != nil {
			return nil, progressErr
		}
		if verifyErr := m.verifyJiraAttachmentBodyInspection(inspection); verifyErr != nil {
			return nil, verifyErr
		}
		out = append(out, jiraAttachmentBodyInventoryFromInspection(inspection))
	}
	sort.Slice(out, func(i, j int) bool { return jiraAttachmentMaterializationLess(out[i].Identity, out[j].Identity) })
	return out, nil
}

// JiraAttachmentBodyCandidate requalifies one intended body without rereading
// every earlier captured payload. It still rechecks the primary/native/base,
// private sidecar binding, current directory membership, and exact pending
// record before the app opens a backend stream.
func (m *Mirror) JiraAttachmentBodyCandidate(identity, attachmentID string) (JiraAttachmentBodyInventory, AttachmentSidecarRecord, error) {
	if m == nil || !positiveDecimalIdentity(identity) || !positiveDecimalIdentity(attachmentID) {
		return JiraAttachmentBodyInventory{}, AttachmentSidecarRecord{}, fmt.Errorf("%w: Jira attachment body candidate identity is invalid", domain.ErrCheckFailed)
	}
	tracked, found, err := m.JiraCompletePullStateByIdentity(identity)
	if err != nil || !found {
		if err != nil {
			return JiraAttachmentBodyInventory{}, AttachmentSidecarRecord{}, err
		}
		return JiraAttachmentBodyInventory{}, AttachmentSidecarRecord{}, fmt.Errorf("%w: tracked Jira attachment inventory is missing", domain.ErrCheckFailed)
	}
	inspection, present, err := m.inspectJiraAttachmentBodyInventoryForState(tracked.State)
	if err != nil || !present || inspection.identity != identity {
		if err != nil {
			return JiraAttachmentBodyInventory{}, AttachmentSidecarRecord{}, err
		}
		return JiraAttachmentBodyInventory{}, AttachmentSidecarRecord{}, fmt.Errorf("%w: tracked Jira attachment inventory is missing or changed", domain.ErrCheckFailed)
	}
	if _, err := jiraAttachmentBodyProgress(inspection.sidecar); err != nil {
		return JiraAttachmentBodyInventory{}, AttachmentSidecarRecord{}, err
	}
	for _, attachment := range inspection.sidecar.Attachments {
		if attachment.ID != attachmentID {
			continue
		}
		if !jiraAttachmentBodyPending(inspection.sidecar.BodiesState, attachment.Body) {
			return JiraAttachmentBodyInventory{}, AttachmentSidecarRecord{}, fmt.Errorf("%w: Jira attachment body is not pending materialization", domain.ErrCheckFailed)
		}
		return jiraAttachmentBodyInventoryFromInspection(inspection), cloneAttachmentSidecarRecord(attachment), nil
	}
	return JiraAttachmentBodyInventory{}, AttachmentSidecarRecord{}, fmt.Errorf("%w: Jira attachment body is absent from its qualified inventory", domain.ErrCheckFailed)
}

func jiraAttachmentBodyInventoryFromInspection(inspection jiraAttachmentBodyMaterializationInspection) JiraAttachmentBodyInventory {
	attachments := make([]AttachmentSidecarRecord, len(inspection.sidecar.Attachments))
	for i, attachment := range inspection.sidecar.Attachments {
		attachments[i] = cloneAttachmentSidecarRecord(attachment)
	}
	return JiraAttachmentBodyInventory{
		Identity: inspection.identity, ParentRevision: inspection.sidecar.ParentRevision,
		BodiesState: inspection.sidecar.BodiesState, Attachments: attachments,
	}
}

func cloneAttachmentSidecarRecord(value AttachmentSidecarRecord) AttachmentSidecarRecord {
	return value
}

func jiraAttachmentMaterializationLess(left, right string) bool {
	if len(left) != len(right) {
		return len(left) < len(right)
	}
	return left < right
}

func jiraAttachmentBodyPending(state AttachmentBodiesState, body AttachmentSidecarBody) bool {
	return state == AttachmentBodiesNotRequested && body.State == AttachmentBodyNotRequested ||
		state == AttachmentBodiesPartial && body.State == AttachmentBodyExcluded && body.Reason == AttachmentBodyReasonAggregateLimit
}

func jiraAttachmentBodyProgress(sidecar AttachmentSidecarV1) (int, error) {
	if !sidecar.InventoryComplete {
		return 0, fmt.Errorf("%w: Jira attachment inventory is incomplete and cannot be materialized", domain.ErrCheckFailed)
	}
	pending := 0
	for _, attachment := range sidecar.Attachments {
		switch {
		case attachment.Body.State == AttachmentBodyCaptured:
			continue
		case jiraAttachmentBodyPending(sidecar.BodiesState, attachment.Body):
			pending++
		default:
			return 0, fmt.Errorf("%w: Jira attachment inventory has a non-resumable body state", domain.ErrCheckFailed)
		}
	}
	if pending == 0 {
		if len(sidecar.Attachments) == 0 && sidecar.BodiesState == AttachmentBodiesNotRequested {
			return 0, nil
		}
		if sidecar.BodiesState != AttachmentBodiesComplete {
			return 0, fmt.Errorf("%w: Jira attachment inventory is not complete after all bodies were captured", domain.ErrCheckFailed)
		}
		return 0, nil
	}
	if sidecar.BodiesState != AttachmentBodiesNotRequested && sidecar.BodiesState != AttachmentBodiesPartial {
		return 0, fmt.Errorf("%w: Jira attachment inventory has an invalid materialization state", domain.ErrCheckFailed)
	}
	return pending, nil
}

func (m *Mirror) inspectJiraAttachmentBodyInventoryForState(state SyncState) (jiraAttachmentBodyMaterializationInspection, bool, error) {
	if m == nil || state.Version != 0 || state.ID == "" || state.Hash == "" || !strings.HasSuffix(state.Path, ".wiki") {
		return jiraAttachmentBodyMaterializationInspection{}, false, fmt.Errorf("%w: tracked Jira attachment state is invalid", domain.ErrCheckFailed)
	}
	stem := strings.TrimSuffix(state.Path, ".wiki")
	sidecarPath := stem + ".attachments.json"
	sidecarData, found, err := m.readQualifiedJiraPrivateEvidence(sidecarPath, MaxAttachmentSidecarPublicationBytes)
	if err != nil {
		return jiraAttachmentBodyMaterializationInspection{}, false, err
	}
	if !found {
		if err := m.validateAbsentJiraAttachmentSidecar(stem); err != nil {
			return jiraAttachmentBodyMaterializationInspection{}, false, err
		}
		return jiraAttachmentBodyMaterializationInspection{}, false, nil
	}
	sidecar, err := DecodeAttachmentSidecarV1(sidecarData)
	if err != nil || sidecar.Service != CorpusSnapshotJira || !positiveDecimalIdentity(sidecar.ParentID) {
		return jiraAttachmentBodyMaterializationInspection{}, false, fmt.Errorf("%w: Jira attachment sidecar is invalid", domain.ErrCheckFailed)
	}
	if state.Identity != "" && state.Identity != sidecar.ParentID {
		return jiraAttachmentBodyMaterializationInspection{}, false, fmt.Errorf("%w: Jira attachment sidecar identity does not match tracked state", domain.ErrCheckFailed)
	}
	return m.inspectJiraAttachmentBodyInventory(sidecar.ParentID, state, stem, sidecarPath, sidecarData, sidecar)
}

func (m *Mirror) inspectJiraAttachmentBodyInventory(
	identity string,
	state SyncState,
	stem, sidecarPath string,
	sidecarData []byte,
	sidecar AttachmentSidecarV1,
) (jiraAttachmentBodyMaterializationInspection, bool, error) {
	if !positiveDecimalIdentity(identity) || state.Version != 0 || state.ID == "" || state.Hash == "" ||
		!strings.HasSuffix(state.Path, ".wiki") || strings.TrimSuffix(state.Path, ".wiki") != stem {
		return jiraAttachmentBodyMaterializationInspection{}, false, fmt.Errorf("%w: Jira attachment materialization state is invalid", domain.ErrCheckFailed)
	}
	tracked, found, err := m.JiraCompletePullStateByIdentity(identity)
	if err != nil || !found || tracked.State != state {
		if err != nil {
			return jiraAttachmentBodyMaterializationInspection{}, false, err
		}
		return jiraAttachmentBodyMaterializationInspection{}, false, fmt.Errorf("%w: Jira attachment sidecar lost its tracked primary state", domain.ErrCheckFailed)
	}
	if err := m.verifyJiraAttachmentMaterializationPrimary(identity, state, stem); err != nil {
		return jiraAttachmentBodyMaterializationInspection{}, false, err
	}
	metadataPath := filepath.Join(m.Root, filepath.FromSlash(stem+".json"))
	metadata, err := safepath.ReadFileWithinLimit(m.Root, metadataPath, maxJiraCompletePullSnapshotBytes)
	if err != nil {
		return jiraAttachmentBodyMaterializationInspection{}, false, fmt.Errorf("%w: Jira attachment metadata is unavailable", domain.ErrCheckFailed)
	}
	binding, bound, bindingErr := m.BackendBinding(CorpusSnapshotJira)
	if bindingErr != nil || !bound || sidecar.Service != CorpusSnapshotJira || sidecar.OriginSHA256 != binding.OriginSHA256 ||
		sidecar.ParentID != identity || sidecar.ParentVersion != 0 || sidecar.ParentRevision != jiraRelocationRevisionFromMetadata(metadata) ||
		sidecar.NativeSHA256 != state.Hash || sidecar.MetadataSHA256 != Hash(metadata) {
		return jiraAttachmentBodyMaterializationInspection{}, false, fmt.Errorf("%w: Jira attachment sidecar is misbound", domain.ErrCheckFailed)
	}
	bodies := make(map[string]AttachmentSidecarBody)
	for _, attachment := range sidecar.Attachments {
		if attachment.Body.State != AttachmentBodyCaptured {
			continue
		}
		if attachment.Body.Size < 0 || attachment.Body.Size > MaxJiraAttachmentBodyMaterializationBytes {
			return jiraAttachmentBodyMaterializationInspection{}, false, fmt.Errorf("%w: captured Jira attachment body exceeds the materialization bound", domain.ErrCheckFailed)
		}
		bodies[attachment.Body.Path] = attachment.Body
	}
	expected := make(map[string]string, len(bodies))
	for path, body := range bodies {
		expected[path] = body.SHA256
	}
	if err := m.validateJiraAttachmentMaterializationDirectory(stem, expected); err != nil {
		return jiraAttachmentBodyMaterializationInspection{}, false, err
	}
	return jiraAttachmentBodyMaterializationInspection{
		identity: identity, state: state, stem: stem, sidecarPath: sidecarPath,
		sidecarData: append([]byte(nil), sidecarData...), sidecar: sidecar,
		metadata: metadata, capturedBodies: bodies,
	}, true, nil
}

// validateJiraAttachmentMaterializationDirectory has a wider entry cap than
// the primary complete-pull publisher. A materializer transaction writes one
// body, so it remains bounded even when an already-qualified inventory has
// more bodies than can participate in one 2,048-artifact page replacement.
func (m *Mirror) validateJiraAttachmentMaterializationDirectory(stem string, expected map[string]string) error {
	dir := filepath.Join(m.Root, filepath.FromSlash(stem+".attachments"))
	entries, err := safepath.ReadDirWithinLimit(m.Root, dir, maxAttachmentSidecarRecords)
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

func (m *Mirror) verifyJiraAttachmentMaterializationPrimary(identity string, state SyncState, stem string) error {
	if !positiveDecimalIdentity(identity) || state.Identity != "" && state.Identity != identity {
		return fmt.Errorf("%w: Jira attachment primary identity is invalid", domain.ErrCheckFailed)
	}
	wikiPath := filepath.Join(m.Root, filepath.FromSlash(state.Path))
	wiki, err := readJiraAttachmentMaterializationPublicFile(m.Root, wikiPath, maxJiraAttachmentBodyMaterializationStateBytes)
	if err != nil || Hash(wiki) != state.Hash {
		return fmt.Errorf("%w: Jira attachment primary native substrate is missing or changed", domain.ErrCheckFailed)
	}
	base, found, err := m.readJiraAttachmentMaterializationBase(state.ID)
	if err != nil || !found || Hash(base) != state.Hash {
		return fmt.Errorf("%w: Jira attachment primary baseline is missing or changed", domain.ErrCheckFailed)
	}
	if strings.TrimSuffix(state.Path, ".wiki") != stem {
		return fmt.Errorf("%w: Jira attachment primary path is invalid", domain.ErrCheckFailed)
	}
	return nil
}

func readJiraAttachmentMaterializationPublicFile(root, path string, maximum int64) ([]byte, error) {
	if maximum < 0 {
		return nil, fmt.Errorf("invalid bounded public evidence size")
	}
	info, err := safepath.StatWithin(root, path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 || info.Size() < 0 || info.Size() > maximum {
		return nil, fmt.Errorf("public evidence is unsafe")
	}
	data, err := safepath.ReadFileWithinLimit(root, path, info.Size())
	if err != nil {
		return nil, err
	}
	after, err := safepath.StatWithin(root, path)
	if err != nil || !after.Mode().IsRegular() || after.Mode().Perm() != 0o644 || after.Size() != info.Size() || int64(len(data)) != after.Size() {
		return nil, fmt.Errorf("public evidence changed during bounded read")
	}
	return data, nil
}

func (m *Mirror) readJiraAttachmentMaterializationBase(id string) ([]byte, bool, error) {
	path := filepath.Join(m.Root, ".atl", "base", safepath.Segment(id)+".wiki")
	info, err := safepath.StatWithin(m.Root, path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() < 0 || info.Size() > maxJiraAttachmentBodyMaterializationStateBytes {
		return nil, false, fmt.Errorf("private base is unsafe")
	}
	data, err := safepath.ReadFileWithinLimit(m.Root, path, info.Size())
	if err != nil {
		return nil, false, err
	}
	after, err := safepath.StatWithin(m.Root, path)
	if err != nil || !after.Mode().IsRegular() || after.Mode().Perm() != 0o600 || after.Size() != info.Size() || int64(len(data)) != after.Size() {
		return nil, false, fmt.Errorf("private base changed during bounded read")
	}
	return data, true, nil
}

func (m *Mirror) verifyJiraAttachmentBodyInspection(inspection jiraAttachmentBodyMaterializationInspection) error {
	for path, body := range inspection.capturedBodies {
		data, found, err := m.readQualifiedJiraPrivateEvidence(path, body.Size)
		if err != nil || !found || int64(len(data)) != body.Size || Hash(data) != body.SHA256 {
			return fmt.Errorf("%w: captured Jira attachment body is missing or changed", domain.ErrCheckFailed)
		}
	}
	return nil
}
