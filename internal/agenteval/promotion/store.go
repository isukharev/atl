package promotion

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const (
	promotionDecisionDirectory = "decisions"
	promotionRollbackDirectory = "rollbacks"
	promotionCurrentName       = "current.json"
)

// Store is an explicit owner-only local reference root. It contains only
// content-minimized receipts and the current immutable identity pointer.
// Provider, backend, credential, and private evidence discovery are outside
// this API.
type Store struct{ root string }

type referencePointer struct {
	Schema          string   `json:"schema"`
	SchemaVersion   int      `json:"schema_version"`
	ContractVersion string   `json:"contract_version"`
	Identity        Identity `json:"identity"`
	PointerSHA256   string   `json:"pointer_sha256"`
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
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return Store{}, fail(ErrorInvalidIdentity)
	}
	return Store{root: abs}, nil
}

func (s Store) openRoot() (*os.Root, error) {
	if s.root == "" {
		return nil, fail(ErrorInvalidIdentity)
	}
	return os.OpenRoot(s.root)
}

func (s Store) ensureDirectory(root *os.Root, name string) error {
	if err := root.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := root.Stat(name)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fail(ErrorConflict)
	}
	return nil
}

func writeExclusive(root *os.Root, name string, data []byte) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		existing, readErr := root.ReadFile(name)
		if readErr != nil || !bytes.Equal(existing, data) {
			return fail(ErrorConflict)
		}
		return nil
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func encodePointer(pointer referencePointer) ([]byte, error) {
	if pointer.Schema != Schema || pointer.SchemaVersion != SchemaVersion || pointer.ContractVersion != ContractVersion || validateIdentity(pointer.Identity) != nil || !validDigest(pointer.PointerSHA256) {
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
	data, err := root.ReadFile(promotionCurrentName)
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
	defer func() { _ = root.Close() }()
	pointer, present, err := s.readCurrent(root)
	if err != nil || !present {
		return Identity{}, present, err
	}
	return pointer.Identity, true, nil
}

func writePointer(root *os.Root, identity Identity) error {
	pointer := referencePointer{Schema: Schema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion, Identity: identity}
	digest, err := digestJSON(pointer)
	if err != nil {
		return err
	}
	pointer.PointerSHA256 = digest
	data, err := encodePointer(pointer)
	if err != nil {
		return err
	}
	tempName := "." + promotionCurrentName + "." + digest + ".tmp"
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
	return nil
}

func (s Store) recordDecision(root *os.Root, receipt DecisionReceipt) error {
	data, err := EncodeDecision(receipt)
	if err != nil {
		return err
	}
	if err := s.ensureDirectory(root, promotionDecisionDirectory); err != nil {
		return err
	}
	return writeExclusive(root, filepath.Join(promotionDecisionDirectory, receipt.ReceiptSHA256+".json"), data)
}

func (s Store) recordRollback(root *os.Root, receipt RollbackReceipt) error {
	data, err := EncodeRollback(receipt)
	if err != nil {
		return err
	}
	if err := s.ensureDirectory(root, promotionRollbackDirectory); err != nil {
		return err
	}
	return writeExclusive(root, filepath.Join(promotionRollbackDirectory, receipt.ReceiptSHA256+".json"), data)
}

// RecordDecision preserves a promote or refuse receipt without changing the
// current reference. It is idempotent for the same receipt and conflicts on a
// digest collision with different bytes.
func (s Store) RecordDecision(receipt DecisionReceipt) error {
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
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
	if err := ValidateDecision(receipt); err != nil || receipt.Decision != DecisionPromote {
		return fail(ErrorPromotionRefused)
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
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
		if expectedCurrent == nil || current.Identity != *expectedCurrent || current.Identity != receipt.Reference {
			return fail(ErrorConflict)
		}
	} else if expectedCurrent != nil {
		return fail(ErrorConflict)
	}
	if err := s.recordDecision(root, receipt); err != nil {
		return err
	}
	return writePointer(root, receipt.Candidate)
}

// ApplyRollback records and applies an exact rollback only if the current
// pointer still names receipt.Current. It never searches for a prior alias.
func (s Store) ApplyRollback(receipt RollbackReceipt) error {
	if err := ValidateRollback(receipt); err != nil {
		return err
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	lock, err := acquireStoreLock(root)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	current, present, err := s.readCurrent(root)
	if err != nil || !present || current.Identity != receipt.Current {
		return fail(ErrorConflict)
	}
	if err := s.recordRollback(root, receipt); err != nil {
		return err
	}
	return writePointer(root, receipt.Restore)
}
