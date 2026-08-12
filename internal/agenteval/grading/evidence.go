package grading

import (
	"context"
	"encoding/binary"
	"slices"
)

type FileEvidence struct {
	ID         string     `json:"id"`
	Visibility Visibility `json:"visibility"`
	Present    bool       `json:"present"`
	Mode       uint32     `json:"mode"`
	Data       []byte     `json:"data"`
}

type CommandEvidence struct {
	ID         string     `json:"id"`
	Visibility Visibility `json:"visibility"`
	ExitCode   int64      `json:"exit_code"`
	Stdout     []byte     `json:"stdout"`
	Stderr     []byte     `json:"stderr"`
}

type TreeEvidence struct {
	ID         string                  `json:"id"`
	Visibility Visibility              `json:"visibility"`
	Changes    []TreeChangeExpectation `json:"changes"`
}

type SequenceEvidence struct {
	ID         string     `json:"id"`
	Visibility Visibility `json:"visibility"`
	Values     []string   `json:"values"`
}

type CounterEvidence struct {
	ID         string     `json:"id"`
	Visibility Visibility `json:"visibility"`
	Value      uint64     `json:"value"`
}

// EvidenceSet is an in-memory input only. Its bytes are never embedded in a
// receipt; receipts retain content-addressed citations instead.
type EvidenceSet struct {
	InputProjectionSHA256 string             `json:"input_projection_sha256"`
	Files                 []FileEvidence     `json:"files"`
	Commands              []CommandEvidence  `json:"commands"`
	Trees                 []TreeEvidence     `json:"trees"`
	Sequences             []SequenceEvidence `json:"sequences"`
	Counters              []CounterEvidence  `json:"counters"`
}

type evidenceRef struct {
	kind       EvidenceKind
	visibility Visibility
	index      int
	digest     string
}

// PreparedEvidence is an owned immutable snapshot. It deliberately exposes no
// raw accessor, preventing hidden verifier input from flowing back to an agent.
type PreparedEvidence struct {
	set     EvidenceSet
	byID    map[string]evidenceRef
	catalog []Citation
	digest  string
}

func (p *PreparedEvidence) SHA256() string {
	if p == nil {
		return ""
	}
	return p.digest
}

// Citations returns the content-minimized catalog bound by SHA256. It exposes
// no evidence bytes and is safe to use when constructing a reviewed decision.
func (p *PreparedEvidence) Citations() []Citation {
	if p == nil {
		return nil
	}
	return slices.Clone(p.catalog)
}

// Destroy clears owned byte slices and invalidates the snapshot. Callers
// should defer it for hidden or owner-private evidence.
func (p *PreparedEvidence) Destroy() {
	if p == nil {
		return
	}
	for index := range p.set.Files {
		clear(p.set.Files[index].Data)
		p.set.Files[index].Data = nil
	}
	for index := range p.set.Commands {
		clear(p.set.Commands[index].Stdout)
		clear(p.set.Commands[index].Stderr)
		p.set.Commands[index].Stdout = nil
		p.set.Commands[index].Stderr = nil
	}
	p.set = EvidenceSet{}
	p.byID = nil
	p.catalog = nil
	p.digest = ""
}

func PrepareEvidence(ctx context.Context, admitted AdmittedPlan, input EvidenceSet) (*PreparedEvidence, error) {
	if ctx == nil || admitted.plan.Schema == "" || input.InputProjectionSHA256 != admitted.plan.InputProjectionSHA256 ||
		!validSHA256(input.InputProjectionSHA256) || input.Files == nil || input.Commands == nil || input.Trees == nil ||
		input.Sequences == nil || input.Counters == nil {
		return nil, evidenceError("shape")
	}
	if err := evidenceWithinAdmissionBounds(ctx, admitted, input); err != nil {
		return nil, err
	}
	owned := cloneEvidenceSet(input)
	prepared := &PreparedEvidence{set: owned, byID: make(map[string]evidenceRef)}
	failed := true
	defer func() {
		if failed {
			prepared.Destroy()
		}
	}()
	items := len(owned.Files) + len(owned.Commands) + len(owned.Trees) + len(owned.Sequences) + len(owned.Counters)
	limitItems := int(admitted.contract.Limits.MaxEvidenceItems)
	if items > limitItems || items > MaxEvidenceItems {
		return nil, policyError("evidence_items")
	}
	var total uint64
	addBytes := func(size int) bool {
		if size < 0 || uint64(size) > admitted.plan.Limits.MaxInputBytes-total {
			return false
		}
		total += uint64(size)
		return total <= admitted.contract.Limits.MaxEvidenceBytes && total <= MaxEvidenceBytes
	}
	register := func(id string, ref evidenceRef) error {
		if !validIdentifier(id) || !ref.visibility.valid() {
			return evidenceError("identity")
		}
		if _, duplicate := prepared.byID[id]; duplicate {
			return evidenceError("duplicate_identity")
		}
		prepared.byID[id] = ref
		return nil
	}
	for index, file := range owned.Files {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		if index > 0 && owned.Files[index-1].ID >= file.ID || file.Mode > 0o777 ||
			(!file.Present && (file.Mode != 0 || file.Data != nil)) || file.Present && file.Mode == 0 || !addBytes(len(file.Data)) {
			return nil, evidenceError("file")
		}
		ref := evidenceRef{kind: EvidenceFile, visibility: file.Visibility, index: index,
			digest: hashDomain("file-evidence", []byte(file.ID), []byte(file.Visibility), boolBytes(file.Present), uint64Bytes(uint64(file.Mode)), file.Data)}
		if err := register(file.ID, ref); err != nil {
			return nil, err
		}
	}
	for index, command := range owned.Commands {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		if index > 0 && owned.Commands[index-1].ID >= command.ID || !addBytes(len(command.Stdout)) || !addBytes(len(command.Stderr)) {
			return nil, evidenceError("command")
		}
		ref := evidenceRef{kind: EvidenceCommand, visibility: command.Visibility, index: index,
			digest: hashDomain("command-evidence", []byte(command.ID), []byte(command.Visibility), int64Bytes(command.ExitCode), command.Stdout, command.Stderr)}
		if err := register(command.ID, ref); err != nil {
			return nil, err
		}
	}
	for index, tree := range owned.Trees {
		if index > 0 && owned.Trees[index-1].ID >= tree.ID || tree.Changes == nil || len(tree.Changes) > MaxEvidenceItems {
			return nil, evidenceError("tree")
		}
		parts := [][]byte{[]byte(tree.ID), []byte(tree.Visibility)}
		for changeIndex, change := range tree.Changes {
			if !validRelativePath(change.Path) || !change.Kind.valid() || changeIndex > 0 && tree.Changes[changeIndex-1].Path >= change.Path ||
				change.Kind == TreeRemoved && change.SHA256 != "" || change.Kind != TreeRemoved && !validSHA256(change.SHA256) || !addBytes(len(change.Path)+len(change.SHA256)) {
				return nil, evidenceError("tree_change")
			}
			parts = append(parts, []byte(change.Path), []byte(change.Kind), []byte(change.SHA256))
		}
		ref := evidenceRef{kind: EvidenceTree, visibility: tree.Visibility, index: index, digest: hashDomain("tree-evidence", parts...)}
		if err := register(tree.ID, ref); err != nil {
			return nil, err
		}
	}
	for index, sequence := range owned.Sequences {
		if index > 0 && owned.Sequences[index-1].ID >= sequence.ID || sequence.Values == nil || len(sequence.Values) > MaxSequenceItems {
			return nil, evidenceError("sequence")
		}
		parts := [][]byte{[]byte(sequence.ID), []byte(sequence.Visibility)}
		for _, value := range sequence.Values {
			if !validText(value, MaxRelativePathBytes) || !addBytes(len(value)) {
				return nil, evidenceError("sequence_value")
			}
			parts = append(parts, []byte(value))
		}
		ref := evidenceRef{kind: EvidenceSequence, visibility: sequence.Visibility, index: index,
			digest: hashDomain("sequence-evidence", parts...)}
		if err := register(sequence.ID, ref); err != nil {
			return nil, err
		}
	}
	for index, counter := range owned.Counters {
		if index > 0 && owned.Counters[index-1].ID >= counter.ID || counter.Value > maxRuleValue {
			return nil, evidenceError("counter")
		}
		ref := evidenceRef{kind: EvidenceCounter, visibility: counter.Visibility, index: index,
			digest: hashDomain("counter-evidence", []byte(counter.ID), []byte(counter.Visibility), uint64Bytes(counter.Value))}
		if err := register(counter.ID, ref); err != nil {
			return nil, err
		}
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(prepared.byID))
	for id := range prepared.byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	prepared.catalog = make([]Citation, 0, len(ids))
	for _, id := range ids {
		ref := prepared.byID[id]
		prepared.catalog = append(prepared.catalog, Citation{EvidenceID: id, Kind: ref.kind, Visibility: ref.visibility, SHA256: ref.digest})
	}
	prepared.digest = evidenceCatalogSHA256(owned.InputProjectionSHA256, prepared.catalog)
	failed = false
	return prepared, nil
}

func evidenceWithinAdmissionBounds(ctx context.Context, admitted AdmittedPlan, input EvidenceSet) error {
	itemLimit := uint64(admitted.contract.Limits.MaxEvidenceItems)
	if itemLimit > MaxEvidenceItems {
		itemLimit = MaxEvidenceItems
	}
	items := uint64(0)
	addItems := func(count int) bool {
		if count < 0 || uint64(count) > itemLimit-items {
			return false
		}
		items += uint64(count)
		return true
	}
	if !addItems(len(input.Files)) || !addItems(len(input.Commands)) || !addItems(len(input.Trees)) ||
		!addItems(len(input.Sequences)) || !addItems(len(input.Counters)) {
		return policyError("evidence_items")
	}
	byteLimit := admitted.plan.Limits.MaxInputBytes
	if maximum := admitted.contract.Limits.MaxEvidenceBytes; byteLimit > maximum {
		byteLimit = maximum
	}
	if byteLimit > MaxEvidenceBytes {
		byteLimit = MaxEvidenceBytes
	}
	bytesUsed := uint64(0)
	addBytes := func(count int) bool {
		if count < 0 || uint64(count) > byteLimit-bytesUsed {
			return false
		}
		bytesUsed += uint64(count)
		return true
	}
	for _, file := range input.Files {
		if err := contextError(ctx); err != nil {
			return err
		}
		if !addBytes(len(file.Data)) {
			return policyError("evidence_bytes")
		}
	}
	for _, command := range input.Commands {
		if !addBytes(len(command.Stdout)) || !addBytes(len(command.Stderr)) {
			return policyError("evidence_bytes")
		}
	}
	for _, tree := range input.Trees {
		if !addItems(len(tree.Changes)) {
			return policyError("evidence_items")
		}
		for _, change := range tree.Changes {
			if !addBytes(len(change.Path)) || !addBytes(len(change.SHA256)) {
				return policyError("evidence_bytes")
			}
		}
	}
	for _, sequence := range input.Sequences {
		if !addItems(len(sequence.Values)) {
			return policyError("evidence_items")
		}
		for _, value := range sequence.Values {
			if !addBytes(len(value)) {
				return policyError("evidence_bytes")
			}
		}
	}
	return nil
}

func cloneEvidenceSet(value EvidenceSet) EvidenceSet {
	files := value.Files
	value.Files = make([]FileEvidence, len(files))
	for index, file := range files {
		value.Files[index] = file
		value.Files[index].Data = slices.Clone(file.Data)
	}
	commands := value.Commands
	value.Commands = make([]CommandEvidence, len(commands))
	for index, command := range commands {
		value.Commands[index] = command
		value.Commands[index].Stdout = slices.Clone(command.Stdout)
		value.Commands[index].Stderr = slices.Clone(command.Stderr)
	}
	trees := value.Trees
	value.Trees = make([]TreeEvidence, len(trees))
	for index, tree := range trees {
		value.Trees[index] = tree
		value.Trees[index].Changes = slices.Clone(tree.Changes)
	}
	sequences := value.Sequences
	value.Sequences = make([]SequenceEvidence, len(sequences))
	for index, sequence := range sequences {
		value.Sequences[index] = sequence
		value.Sequences[index].Values = slices.Clone(sequence.Values)
	}
	value.Counters = slices.Clone(value.Counters)
	return value
}

func contextError(ctx context.Context) error {
	if err := context.Cause(ctx); err != nil {
		return interruptedError(err)
	}
	return nil
}

func boolBytes(value bool) []byte {
	if value {
		return []byte{1}
	}
	return []byte{0}
}

func uint64Bytes(value uint64) []byte {
	data := make([]byte, 8)
	binary.BigEndian.PutUint64(data, value)
	return data
}

func int64Bytes(value int64) []byte {
	// #nosec G115 -- the digest deliberately preserves the signed value's two's-complement bits.
	return uint64Bytes(uint64(value))
}

func (p *PreparedEvidence) reference(id string, visibility Visibility, kind EvidenceKind) (evidenceRef, bool) {
	if p == nil || p.byID == nil {
		return evidenceRef{}, false
	}
	ref, ok := p.byID[id]
	return ref, ok && ref.visibility == visibility && ref.kind == kind
}

func (p *PreparedEvidence) citation(id string) Citation {
	ref := p.byID[id]
	return Citation{EvidenceID: id, Kind: ref.kind, Visibility: ref.visibility, SHA256: ref.digest}
}

func evidenceCatalogSHA256(inputProjectionSHA256 string, catalog []Citation) string {
	parts := make([][]byte, 0, len(catalog)*4+1)
	parts = append(parts, []byte(inputProjectionSHA256))
	for _, citation := range catalog {
		parts = append(parts, []byte(citation.EvidenceID), []byte(citation.Kind), []byte(citation.Visibility), []byte(citation.SHA256))
	}
	return hashDomain("evidence-set", parts...)
}

func (p *PreparedEvidence) requireAlive() error {
	if p == nil || p.byID == nil || !validSHA256(p.digest) {
		return evidenceError("destroyed")
	}
	return nil
}
