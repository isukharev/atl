package promotion

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const (
	promotionDecisionDirectory   = "decisions"
	promotionRollbackDirectory   = "rollbacks"
	promotionTransitionDirectory = "transitions"
	promotionCurrentName         = "current.json"
	PointerSchema                = "agent-eval/promotion-store-pointer"
	TransitionSchema             = "agent-eval/promotion-store-transition"
	maxStoreFileBytes            = MaxReceiptBytes
)

// Store is an explicit owner-only local reference root. It contains only
// content-minimized receipts and the current immutable identity pointer.
// Provider, backend, credential, and private evidence discovery are outside
// this API.
type Store struct {
	rootPath string
	root     *os.Root
}

type referencePointer struct {
	Schema           string   `json:"schema"`
	SchemaVersion    int      `json:"schema_version"`
	ContractVersion  string   `json:"contract_version"`
	Identity         Identity `json:"identity"`
	TransitionSHA256 string   `json:"transition_sha256"`
	PointerSHA256    string   `json:"pointer_sha256"`
}

type transitionRecord struct {
	Schema                   string   `json:"schema"`
	SchemaVersion            int      `json:"schema_version"`
	ContractVersion          string   `json:"contract_version"`
	Kind                     string   `json:"kind"`
	RequestSHA256            string   `json:"request_sha256"`
	ReceiptSHA256            string   `json:"receipt_sha256"`
	PreviousTransitionSHA256 string   `json:"previous_transition_sha256,omitempty"`
	From                     Identity `json:"from"`
	To                       Identity `json:"to"`
	TransitionSHA256         string   `json:"transition_sha256"`
}

func NewStore(root string) (Store, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return Store{}, fail(ErrorInvalidIdentity)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Store{}, fail(ErrorInvalidIdentity)
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Store{}, fail(ErrorInvalidIdentity)
	}
	if err := validateStoreRootPlatform(abs, info); err != nil {
		return Store{}, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil || resolved != abs {
		return Store{}, fail(ErrorInvalidIdentity)
	}
	held, err := os.OpenRoot(abs)
	if err != nil {
		return Store{}, fail(ErrorInvalidIdentity)
	}
	heldInfo, err := held.Stat(".")
	if err != nil || !os.SameFile(info, heldInfo) || !heldInfo.IsDir() || heldInfo.Mode()&os.ModeSymlink != 0 || validateStoreDirectoryPlatform(heldInfo) != nil {
		_ = held.Close()
		return Store{}, fail(ErrorInvalidIdentity)
	}
	return Store{rootPath: abs, root: held}, nil
}

func (s Store) openRoot() (*os.Root, error) {
	if s.root == nil || s.rootPath == "" {
		return nil, fail(ErrorInvalidIdentity)
	}
	return s.root, nil
}

func (s Store) ensureDirectory(root *os.Root, name string) error {
	if err := root.Mkdir(name, 0o700); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	info, err := root.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || validateStoreDirectoryPlatform(info) != nil {
		return fail(ErrorConflict)
	}
	return syncDirectory(root, ".")
}

func writeExclusive(root *os.Root, name string, data []byte, idempotent bool) error {
	if len(data) > maxStoreFileBytes {
		return fail(ErrorLimitExceeded)
	}
	if info, err := root.Lstat(name); err == nil {
		if !idempotent {
			return fail(ErrorConflict)
		}
		existing, readErr := readRegularBounded(root, name)
		if readErr != nil || !bytes.Equal(existing, data) {
			return fail(ErrorConflict)
		}
		if info.Mode().Perm() != 0o600 {
			return fail(ErrorConflict)
		}
		return syncDirectory(root, filepath.Dir(name))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tempName := "." + filepath.Base(name) + ".tmp." + randomTransitionSuffix()
	_ = root.Remove(tempName)
	f, err := root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = root.Remove(tempName)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := root.Link(tempName, name); err != nil {
		return err
	}
	if err := root.Remove(tempName); err != nil {
		return err
	}
	cleanup = false
	return syncDirectory(root, filepath.Dir(name))
}

func readRegularBounded(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || validateStoreRegularFilePlatform(info) != nil || info.Size() < 0 || info.Size() > maxStoreFileBytes {
		return nil, fail(ErrorConflict)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0o600 || !os.SameFile(info, opened) {
		return nil, fail(ErrorConflict)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxStoreFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxStoreFileBytes {
		return nil, fail(ErrorLimitExceeded)
	}
	return data, nil
}

func syncDirectory(root *os.Root, name string) error {
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	return errors.Join(err, closeErr)
}

func randomTransitionSuffix() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(raw[:])
}

func encodePointer(pointer referencePointer) ([]byte, error) {
	if pointer.Schema != PointerSchema || pointer.SchemaVersion != SchemaVersion || pointer.ContractVersion != ContractVersion || validateIdentity(pointer.Identity) != nil || !validDigest(pointer.TransitionSHA256) || !validDigest(pointer.PointerSHA256) {
		return nil, fail(ErrorInvalidReceipt)
	}
	copyPointer := pointer
	copyPointer.PointerSHA256 = ""
	digest, err := digestJSON(copyPointer)
	if err != nil || digest != pointer.PointerSHA256 {
		return nil, fail(ErrorInvalidReceipt)
	}
	data, err := json.Marshal(pointer)
	if err != nil || len(data) > MaxReceiptBytes {
		return nil, fail(ErrorLimitExceeded)
	}
	return append(data, '\n'), nil
}

func decodePointer(data []byte) (referencePointer, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var pointer referencePointer
	if err := decoder.Decode(&pointer); err != nil {
		return referencePointer{}, fail(ErrorInvalidReceipt)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return referencePointer{}, fail(ErrorInvalidReceipt)
	}
	canonical, err := encodePointer(pointer)
	if err != nil || !bytes.Equal(canonical, data) {
		return referencePointer{}, fail(ErrorInvalidReceipt)
	}
	return pointer, nil
}

func (s Store) readCurrent(root *os.Root) (referencePointer, bool, error) {
	data, err := readRegularBounded(root, promotionCurrentName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return referencePointer{}, false, nil
		}
		return referencePointer{}, false, err
	}
	pointer, err := decodePointer(data)
	if err != nil {
		return referencePointer{}, false, err
	}
	return pointer, true, nil
}

func (s Store) Current() (Identity, bool, error) {
	root, err := s.openRoot()
	if err != nil {
		return Identity{}, false, err
	}
	pointer, present, err := s.readCurrent(root)
	if err != nil || !present {
		return Identity{}, present, err
	}
	return pointer.Identity, true, nil
}

// Close releases the held store root. Callers should close a store after the
// complete operation; methods intentionally retain the same root descriptor
// for admission-to-mutation identity binding.
func (s Store) Close() error {
	if s.root == nil {
		return nil
	}
	return s.root.Close()
}

func writePointer(root *os.Root, identity Identity, transitionSHA256 string) error {
	pointer := referencePointer{Schema: PointerSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion, Identity: identity, TransitionSHA256: transitionSHA256}
	digest, err := digestJSON(pointer)
	if err != nil {
		return err
	}
	pointer.PointerSHA256 = digest
	data, err := encodePointer(pointer)
	if err != nil {
		return err
	}
	tempName := "." + promotionCurrentName + "." + digest + ".tmp." + randomTransitionSuffix()
	f, err := root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			_ = root.Remove(tempName)
			f, err = root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		}
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = root.Remove(tempName)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = root.Remove(tempName)
		return err
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tempName)
		return err
	}
	if err := root.Rename(tempName, promotionCurrentName); err != nil {
		_ = root.Remove(tempName)
		return err
	}
	if err := syncDirectory(root, "."); err != nil {
		return fail(ErrorOutcomeUnknown)
	}
	return nil
}

func transitionWithoutDigest(record transitionRecord) transitionRecord {
	record.TransitionSHA256 = ""
	return record
}

func validateTransition(record transitionRecord) error {
	fromEmpty := record.Kind == "promotion" && identityZero(record.From) && record.PreviousTransitionSHA256 == ""
	if record.Schema != TransitionSchema || record.SchemaVersion != SchemaVersion || record.ContractVersion != ContractVersion ||
		(record.Kind != "promotion" && record.Kind != "rollback") || (!fromEmpty && validateIdentity(record.From) != nil) ||
		validateIdentity(record.To) != nil || (!fromEmpty && identityEqual(record.From, record.To)) ||
		!validDigest(record.RequestSHA256) || !validDigest(record.ReceiptSHA256) || !validDigest(record.TransitionSHA256) {
		return fail(ErrorInvalidReceipt)
	}
	if record.PreviousTransitionSHA256 != "" && !validDigest(record.PreviousTransitionSHA256) {
		return fail(ErrorInvalidReceipt)
	}
	digest, err := digestJSON(transitionWithoutDigest(record))
	if err != nil || digest != record.TransitionSHA256 {
		return fail(ErrorInvalidReceipt)
	}
	return nil
}

func identityZero(identity Identity) bool { return identity == (Identity{}) }

func encodeTransition(record transitionRecord) ([]byte, error) {
	if err := validateTransition(record); err != nil {
		return nil, err
	}
	data, err := json.Marshal(record)
	if err != nil || len(data) > maxStoreFileBytes {
		return nil, fail(ErrorLimitExceeded)
	}
	return append(data, '\n'), nil
}

func decodeTransition(data []byte) (transitionRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record transitionRecord
	if err := decoder.Decode(&record); err != nil {
		return transitionRecord{}, fail(ErrorInvalidReceipt)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return transitionRecord{}, fail(ErrorInvalidReceipt)
	}
	canonical, err := encodeTransition(record)
	if err != nil || !bytes.Equal(canonical, data) {
		return transitionRecord{}, fail(ErrorInvalidReceipt)
	}
	return record, nil
}

func (s Store) recordTransition(root *os.Root, record transitionRecord) error {
	if err := s.ensureDirectory(root, promotionTransitionDirectory); err != nil {
		return err
	}
	if err := checkTransitionCapacity(root); err != nil {
		return err
	}
	data, err := encodeTransition(record)
	if err != nil {
		return err
	}
	return writeExclusive(root, transitionName(record.Kind, record.RequestSHA256), data, false)
}

func (s Store) recordDecision(root *os.Root, receipt DecisionReceipt) error {
	data, err := EncodeDecision(receipt)
	if err != nil {
		return err
	}
	if err := s.ensureDirectory(root, promotionDecisionDirectory); err != nil {
		return err
	}
	return writeExclusive(root, filepath.Join(promotionDecisionDirectory, receipt.ReceiptSHA256+".json"), data, true)
}

func (s Store) recordRollback(root *os.Root, receipt RollbackReceipt) error {
	data, err := EncodeRollback(receipt)
	if err != nil {
		return err
	}
	if err := s.ensureDirectory(root, promotionRollbackDirectory); err != nil {
		return err
	}
	return writeExclusive(root, filepath.Join(promotionRollbackDirectory, receipt.ReceiptSHA256+".json"), data, true)
}

// RecordDecision preserves a promote or refuse receipt without changing the
// current reference. It is idempotent for the same receipt and conflicts on a
// digest collision with different bytes.
func (s Store) RecordDecision(receipt DecisionReceipt) error {
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	lock, err := acquireStoreLock(root)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	return s.recordDecision(root, receipt)
}

// ApplyPromotion records the receipt and advances the pointer only when the
// caller supplies the exact current identity (or no pointer exists). A
// refusal can be recorded with RecordDecision but can never mutate state.
func (s Store) ApplyPromotion(receipt DecisionReceipt, expectedCurrent *Identity) error {
	if err := ValidateDecision(receipt); err != nil {
		return err
	}
	if receipt.Decision != DecisionPromote {
		return fail(ErrorPromotionRefused)
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	lock, err := acquireStoreLock(root)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	current, present, err := s.readCurrent(root)
	if err != nil {
		return err
	}
	if present {
		// A prior pointer rename may have completed before its root fsync
		// failed. Repair that directory entry before treating the visible
		// pointer as an ordinary conflict.
		if err := syncDirectory(root, "."); err != nil {
			return fail(ErrorOutcomeUnknown)
		}
		if expectedCurrent == nil || current.Identity != *expectedCurrent || current.Identity != receipt.Reference {
			return fail(ErrorConflict)
		}
	} else if expectedCurrent != nil {
		return fail(ErrorConflict)
	}
	if existing, found, err := s.readTransition(root, "promotion", receipt.ReceiptSHA256); err != nil {
		return err
	} else if found {
		if present && current.TransitionSHA256 == existing.TransitionSHA256 && current.Identity == existing.To {
			return fail(ErrorConflict)
		}
		if (!present && existing.From == receipt.Reference && existing.PreviousTransitionSHA256 == "") ||
			(present && current.Identity == existing.From && current.TransitionSHA256 == existing.PreviousTransitionSHA256) {
			if err := syncDirectory(root, promotionTransitionDirectory); err != nil {
				return err
			}
			return writePointer(root, existing.To, existing.TransitionSHA256)
		}
		return fail(ErrorConflict)
	}
	if err := checkTransitionCapacity(root); err != nil {
		return err
	}
	if err := s.recordDecision(root, receipt); err != nil {
		return err
	}
	from := receipt.Reference
	previous := ""
	if present {
		from = current.Identity
		previous = current.TransitionSHA256
	}
	record := transitionRecord{Schema: TransitionSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion, Kind: "promotion", RequestSHA256: receipt.ReceiptSHA256, ReceiptSHA256: receipt.ReceiptSHA256,
		PreviousTransitionSHA256: previous, From: from, To: receipt.Candidate}
	digest, err := digestJSON(transitionWithoutDigest(record))
	if err != nil {
		return fail(ErrorInvalidReceipt)
	}
	record.TransitionSHA256 = digest
	if err := s.recordTransition(root, record); err != nil {
		return err
	}
	return writePointer(root, receipt.Candidate, record.TransitionSHA256)
}

// ApplyRollback validates a planned request, proves that the exact restore
// identity was the immediately preceding promotion, and returns a separate
// applied receipt. The request cannot be replayed after a later transition.
func (s Store) ApplyRollback(receipt RollbackReceipt) (RollbackReceipt, error) {
	if receipt.Restored || receipt.RequestSHA256 != "" {
		return RollbackReceipt{}, fail(ErrorInvalidRollback)
	}
	if err := ValidateRollback(receipt); err != nil {
		return RollbackReceipt{}, err
	}
	root, err := s.openRoot()
	if err != nil {
		return RollbackReceipt{}, err
	}
	lock, err := acquireStoreLock(root)
	if err != nil {
		return RollbackReceipt{}, err
	}
	defer func() { _ = lock.Close() }()
	current, present, err := s.readCurrent(root)
	if err != nil {
		return RollbackReceipt{}, err
	}
	if !present {
		return RollbackReceipt{}, fail(ErrorConflict)
	}
	// See ApplyPromotion: a visible pointer can survive a failed root fsync;
	// repair its directory durability before applying the rollback guard.
	if err := syncDirectory(root, "."); err != nil {
		return RollbackReceipt{}, fail(ErrorOutcomeUnknown)
	}
	if current.Identity != receipt.Current {
		return RollbackReceipt{}, fail(ErrorConflict)
	}
	if existing, found, err := s.readTransition(root, "rollback", receipt.ReceiptSHA256); err != nil {
		return RollbackReceipt{}, err
	} else if found {
		if current.Identity == existing.To && current.TransitionSHA256 == existing.TransitionSHA256 {
			return RollbackReceipt{}, fail(ErrorConflict)
		}
		if current.Identity != existing.From || current.TransitionSHA256 != existing.PreviousTransitionSHA256 {
			return RollbackReceipt{}, fail(ErrorConflict)
		}
		if err := syncDirectory(root, promotionTransitionDirectory); err != nil {
			return RollbackReceipt{}, err
		}
		applied, err := s.readRollbackReceipt(root, existing.ReceiptSHA256)
		if err != nil {
			return RollbackReceipt{}, err
		}
		if !applied.Restored || applied.RequestSHA256 != receipt.ReceiptSHA256 || applied.Current != receipt.Current || applied.Restore != receipt.Restore {
			return RollbackReceipt{}, fail(ErrorConflict)
		}
		if err := writePointer(root, existing.To, existing.TransitionSHA256); err != nil {
			return RollbackReceipt{}, err
		}
		return applied, nil
	}
	prior, found, err := s.readTransitionByDigest(root, "promotion", current.TransitionSHA256)
	if err != nil {
		return RollbackReceipt{}, err
	}
	if !found || prior.From != receipt.Restore || prior.To != receipt.Current || prior.TransitionSHA256 != current.TransitionSHA256 {
		return RollbackReceipt{}, fail(ErrorConflict)
	}
	if err := checkTransitionCapacity(root); err != nil {
		return RollbackReceipt{}, err
	}
	applied := receipt
	applied.Restored = true
	applied.RequestSHA256 = receipt.ReceiptSHA256
	applied.ReceiptSHA256 = ""
	digest, err := digestJSON(rollbackWithoutDigest(applied))
	if err != nil {
		return RollbackReceipt{}, fail(ErrorInvalidRollback)
	}
	applied.ReceiptSHA256 = digest
	if err := s.recordRollback(root, applied); err != nil {
		return RollbackReceipt{}, err
	}
	record := transitionRecord{Schema: TransitionSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion, Kind: "rollback", RequestSHA256: receipt.ReceiptSHA256, ReceiptSHA256: applied.ReceiptSHA256,
		PreviousTransitionSHA256: current.TransitionSHA256, From: receipt.Current, To: receipt.Restore}
	digest, err = digestJSON(transitionWithoutDigest(record))
	if err != nil {
		return RollbackReceipt{}, fail(ErrorInvalidRollback)
	}
	record.TransitionSHA256 = digest
	if err := s.recordTransition(root, record); err != nil {
		return RollbackReceipt{}, err
	}
	if err := writePointer(root, receipt.Restore, record.TransitionSHA256); err != nil {
		return RollbackReceipt{}, err
	}
	return applied, nil
}

func (s Store) readRollbackReceipt(root *os.Root, receiptSHA256 string) (RollbackReceipt, error) {
	data, err := readRegularBounded(root, filepath.Join(promotionRollbackDirectory, receiptSHA256+".json"))
	if err != nil {
		return RollbackReceipt{}, err
	}
	decoder := bytes.NewReader(data)
	var receipt RollbackReceipt
	if receipt, err = DecodeRollback(decoder); err != nil {
		return RollbackReceipt{}, err
	}
	return receipt, nil
}
