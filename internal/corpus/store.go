// Package corpus owns the backend-neutral durable boundary for immutable local
// corpus generations. Generation manifests are private inventory; receipts,
// pointers, summaries, and errors are content-free.
package corpus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	generationsDir = "generations"
	artifactsDir   = "artifacts"
	manifestFile   = "manifest.v1.json"
	receiptFile    = "receipt.v1.json"
	pointerFile    = "current.v1.json"
	publishLock    = ".publish.lock"

	privateDirMode        = os.FileMode(0o700)
	privateFileMode       = os.FileMode(0o600)
	maxStoredPointerBytes = int64(4096)
)

var (
	// ErrAlreadyExists reports an exclusive stage or member collision.
	ErrAlreadyExists = errors.New("corpus object already exists")
	// ErrNoCurrent reports a store without a published generation.
	ErrNoCurrent = errors.New("corpus store has no current generation")
	// ErrStalePredecessor reports a failed compare-and-set publication.
	ErrStalePredecessor = errors.New("corpus generation predecessor is stale")
	// ErrOutcomeUnknown reports a failure after an atomic seal or pointer write.
	ErrOutcomeUnknown = errors.New("corpus durable outcome is unknown")
	// ErrUnsupported reports a platform without the required durability model.
	ErrUnsupported = errors.New("corpus durable generations are unsupported")
)

// Options bounds every decoded or scanned generation.
type Options struct {
	Limits Limits
}

// SealOptions supplies content-free build and qualification lineage. All
// digests are lowercase SHA-256 values. PredecessorDigest is empty only for the
// first published generation.
type SealOptions struct {
	ProjectionSchema  int
	GeneratorVersion  string
	BuildState        BuildState
	PredecessorDigest string
	Qualifications    []Qualification
	TombstoneDigest   string
}

// Store is a pinned owner-only corpus root. Close it when no more operations or
// generations will be opened from it.
type Store struct {
	rootPath string
	root     *os.Root
	limits   Limits

	mu       sync.Mutex
	closed   bool
	testHook func(string) error
}

// Stage is one private, unreachable generation under construction. It is not
// resumable: a failed stage is preserved as evidence and consumers ignore it.
type Stage struct {
	store   *Store
	id      string
	members []MemberSpec
	tuples  map[memberTuple]struct{}
	paths   map[string]struct{}
	folded  map[string]struct{}
	bytes   int64
	sealed  bool
	failed  bool
	mu      sync.Mutex
}

type memberTuple struct {
	service  Service
	stableID string
	role     Role
}

// Generation is one verified generation pinned through its directory handle.
// It is codec-enforced and tamper-evident, not physically immutable against the
// filesystem owner.
type Generation struct {
	id       string
	root     *os.Root
	manifest Manifest
	receipt  Receipt
	limits   Limits
	mu       sync.Mutex
	closed   bool
}

// Initialize creates the store-owned namespace inside an existing, empty,
// owner-only directory. Requiring the caller to create the trust anchor avoids
// claiming durability for creation of its parent directory.
func Initialize(rootPath string, opts Options) (*Store, error) {
	if !platformAvailable() {
		return nil, ErrUnsupported
	}
	limits, err := normalizeLimits(opts.Limits)
	if err != nil {
		return nil, err
	}
	store, err := openPrivateStoreRoot(rootPath, limits)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = store.root.Close()
		}
	}()

	entries, err := readDirectory(store.root, ".")
	if err != nil {
		return nil, reject(ReasonIO)
	}
	if len(entries) != 0 {
		return nil, reject(ReasonMembership)
	}
	if err := store.root.Mkdir(generationsDir, privateDirMode); err != nil {
		return nil, reject(ReasonIO)
	}
	if err := verifyDirectory(store.root, generationsDir); err != nil {
		return nil, err
	}
	lock, err := store.root.OpenFile(publishLock, os.O_RDWR|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return nil, reject(ReasonIO)
	}
	if err := lock.Chmod(privateFileMode); err != nil {
		_ = lock.Close()
		return nil, reject(ReasonIO)
	}
	if err := lock.Sync(); err != nil {
		_ = lock.Close()
		return nil, reject(ReasonIO)
	}
	if err := lock.Close(); err != nil {
		return nil, reject(ReasonIO)
	}
	if err := verifyRegularFile(store.root, publishLock, privateFileMode); err != nil {
		return nil, err
	}
	if err := syncDirectory(store.root, generationsDir); err != nil {
		return nil, reject(ReasonIO)
	}
	if err := syncDirectory(store.root, "."); err != nil {
		return nil, reject(ReasonIO)
	}
	success = true
	return store, nil
}

// Open validates and pins an initialized store without adopting or repairing
// any partial namespace.
func Open(rootPath string, opts Options) (*Store, error) {
	if !platformAvailable() {
		return nil, ErrUnsupported
	}
	limits, err := normalizeLimits(opts.Limits)
	if err != nil {
		return nil, err
	}
	store, err := openPrivateStoreRoot(rootPath, limits)
	if err != nil {
		return nil, err
	}
	if err := verifyDirectory(store.root, generationsDir); err != nil {
		_ = store.root.Close()
		return nil, err
	}
	if err := verifyRegularFile(store.root, publishLock, privateFileMode); err != nil {
		_ = store.root.Close()
		if os.IsNotExist(err) {
			return nil, reject(ReasonMembership)
		}
		return nil, err
	}
	return store, nil
}

func openPrivateStoreRoot(rootPath string, limits Limits) (*Store, error) {
	if strings.TrimSpace(rootPath) == "" {
		return nil, reject(ReasonPath)
	}
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, reject(ReasonPath)
	}
	ambient, err := os.Lstat(abs)
	if err != nil || !ambient.IsDir() || ambient.Mode().Perm() != privateDirMode {
		return nil, reject(ReasonMode)
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, reject(ReasonIO)
	}
	pinned, err := root.Stat(".")
	if err != nil || !os.SameFile(ambient, pinned) || !pinned.IsDir() || pinned.Mode().Perm() != privateDirMode {
		_ = root.Close()
		return nil, reject(ReasonConcurrent)
	}
	return &Store{rootPath: abs, root: root, limits: limits}, nil
}

// Close releases the pinned store root. Existing Generation handles remain
// independently pinned and must be closed separately.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if err := s.root.Close(); err != nil {
		return reject(ReasonIO)
	}
	return nil
}

// Begin creates a collision-resistant private stage. Its random ID carries no
// backend or object identity.
func (s *Store) Begin() (*Stage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRoot(); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 8; attempt++ {
		id, err := randomToken(16)
		if err != nil {
			return nil, reject(ReasonIO)
		}
		rel := generationPath(id)
		if err := s.root.Mkdir(rel, privateDirMode); err != nil {
			if os.IsExist(err) {
				continue
			}
			return nil, reject(ReasonIO)
		}
		genRoot, err := s.root.OpenRoot(rel)
		if err != nil {
			return nil, reject(ReasonIO)
		}
		if err := verifyDirectory(genRoot, "."); err != nil {
			_ = genRoot.Close()
			return nil, err
		}
		if err := genRoot.Mkdir(artifactsDir, privateDirMode); err != nil {
			_ = genRoot.Close()
			return nil, reject(ReasonIO)
		}
		if err := verifyDirectory(genRoot, artifactsDir); err != nil {
			_ = genRoot.Close()
			return nil, err
		}
		for _, dir := range []string{artifactsDir, "."} {
			if err := syncDirectory(genRoot, dir); err != nil {
				_ = genRoot.Close()
				return nil, reject(ReasonIO)
			}
		}
		if err := genRoot.Close(); err != nil {
			return nil, reject(ReasonIO)
		}
		for _, dir := range []string{generationsDir, "."} {
			if err := syncDirectory(s.root, dir); err != nil {
				return nil, reject(ReasonIO)
			}
		}
		return &Stage{
			store: s, id: id, members: make([]MemberSpec, 0),
			tuples: make(map[memberTuple]struct{}), paths: make(map[string]struct{}), folded: make(map[string]struct{}),
		}, nil
	}
	return nil, ErrAlreadyExists
}

// ID returns the content-free random stage identifier.
func (s *Stage) ID() string { return s.id }

// Add copies one member into codec-owned storage. The caller must discard the
// entire stage after an error; partial evidence is preserved and cannot seal.
func (s *Stage) Add(ctx context.Context, spec MemberSpec, reader io.Reader) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	defer func() {
		if err != nil {
			s.failed = true
		}
	}()
	if s.sealed || s.failed {
		return ErrAlreadyExists
	}
	if reader == nil {
		return reject(ReasonType)
	}
	if len(s.members) >= s.store.limits.MaxMembers || s.bytes >= s.store.limits.MaxTotalBytes {
		return reject(ReasonBounds)
	}
	if err := validateMemberSpec(spec, s.store.limits); err != nil {
		return err
	}
	tuple := memberTuple{service: spec.Service, stableID: spec.StableID, role: spec.Role}
	if _, exists := s.tuples[tuple]; exists {
		return ErrAlreadyExists
	}
	if _, exists := s.paths[spec.Path]; exists {
		return ErrAlreadyExists
	}
	foldedPath := foldPath(spec.Path)
	if _, exists := s.folded[foldedPath]; exists {
		return ErrAlreadyExists
	}
	genRoot, err := s.openStageRoot()
	if err != nil {
		return err
	}
	defer func() { _ = genRoot.Close() }()
	if err := ensureArtifactParents(genRoot, spec.Path); err != nil {
		return err
	}
	maximum := s.store.limits.MaxMemberBytes
	if remaining := s.store.limits.MaxTotalBytes - s.bytes; remaining < maximum {
		maximum = remaining
	}
	written, err := writeStageMember(ctx, genRoot, spec.Path, reader, maximum)
	if err != nil {
		return err
	}
	if err := s.store.hit("after_member_link"); err != nil {
		return reject(ReasonIO)
	}
	if err := syncArtifactPath(genRoot, spec.Path); err != nil {
		return reject(ReasonIO)
	}
	if err := s.store.hit("after_member_sync"); err != nil {
		return reject(ReasonIO)
	}
	if err := s.store.ensureGenerationRoot(s.id, genRoot); err != nil {
		return err
	}
	s.members = append(s.members, spec)
	s.tuples[tuple] = struct{}{}
	s.paths[spec.Path] = struct{}{}
	s.folded[foldedPath] = struct{}{}
	s.bytes += written
	return nil
}

// Seal performs two exact inventory passes around canonical manifest creation,
// writes the receipt exclusively last, and finally re-verifies the sealed tree.
func (s *Stage) Seal(ctx context.Context, opts SealOptions) (generation *Generation, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	defer func() {
		if err != nil {
			s.failed = true
		}
	}()
	if s.sealed || s.failed {
		return nil, ErrAlreadyExists
	}
	genRoot, err := s.openStageRoot()
	if err != nil {
		return nil, err
	}
	defer func() {
		if genRoot != nil {
			_ = genRoot.Close()
		}
	}()

	first, err := scanStage(ctx, genRoot, s.members, nil, s.store.limits)
	if err != nil {
		return nil, err
	}
	if err := s.store.hit("after_first_inventory"); err != nil {
		return nil, reject(ReasonIO)
	}
	qualifications := append([]Qualification(nil), opts.Qualifications...)
	sort.Slice(qualifications, func(i, j int) bool { return qualifications[i].Service < qualifications[j].Service })
	manifest := Manifest{
		SchemaVersion:     ManifestSchemaV1,
		ProjectionSchema:  opts.ProjectionSchema,
		GeneratorVersion:  opts.GeneratorVersion,
		BuildState:        opts.BuildState,
		PredecessorDigest: opts.PredecessorDigest,
		Qualifications:    qualifications,
		TombstoneDigest:   opts.TombstoneDigest,
		Members:           first,
		Totals:            totalsFor(first),
	}
	manifestBytes, err := canonicalManifest(manifest, s.store.limits)
	if err != nil {
		return nil, err
	}
	if err := writeExclusiveRegular(genRoot, manifestFile, manifestBytes); err != nil {
		return nil, err
	}
	if err := s.store.hit("after_manifest_link"); err != nil {
		return nil, reject(ReasonIO)
	}
	if err := syncDirectory(genRoot, "."); err != nil {
		return nil, reject(ReasonIO)
	}
	if err := s.store.hit("after_manifest_sync"); err != nil {
		return nil, reject(ReasonIO)
	}
	if err := s.store.hit("before_second_inventory"); err != nil {
		return nil, reject(ReasonIO)
	}
	second, err := scanStage(ctx, genRoot, s.members, manifestBytes, s.store.limits)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(first, second) {
		return nil, reject(ReasonConcurrent)
	}
	if err := s.store.hit("after_second_inventory"); err != nil {
		return nil, reject(ReasonIO)
	}
	inventory, err := inventoryDigest(first)
	if err != nil {
		return nil, err
	}
	receipt := Receipt{
		SchemaVersion:     ReceiptSchemaV1,
		ManifestSchema:    ManifestSchemaV1,
		ProjectionSchema:  manifest.ProjectionSchema,
		GeneratorVersion:  manifest.GeneratorVersion,
		BuildState:        manifest.BuildState,
		PredecessorDigest: manifest.PredecessorDigest,
		Qualifications:    append([]Qualification(nil), manifest.Qualifications...),
		TombstoneDigest:   manifest.TombstoneDigest,
		Totals:            manifest.Totals,
		ManifestDigest:    manifestDigest(manifestBytes),
		InventoryDigest:   inventory,
	}
	receipt.GenerationDigest, err = generationDigest(receipt)
	if err != nil {
		return nil, err
	}
	receiptBytes, err := canonicalReceipt(receipt, s.store.limits)
	if err != nil {
		return nil, err
	}
	if err := s.store.hit("before_receipt_link"); err != nil {
		return nil, reject(ReasonIO)
	}
	if err := writeExclusiveRegular(genRoot, receiptFile, receiptBytes); err != nil {
		return nil, err
	}
	if err := s.store.hit("after_receipt_link"); err != nil {
		return nil, ErrOutcomeUnknown
	}
	if err := syncDirectory(genRoot, "."); err != nil {
		return nil, ErrOutcomeUnknown
	}
	if err := s.store.hit("after_receipt_sync"); err != nil {
		return nil, ErrOutcomeUnknown
	}
	if err := s.store.ensureGenerationRoot(s.id, genRoot); err != nil {
		return nil, err
	}
	if err := genRoot.Close(); err != nil {
		return nil, reject(ReasonIO)
	}
	genRoot = nil
	generation, err = s.store.openGeneration(ctx, s.id)
	if err != nil {
		return nil, err
	}
	s.sealed = true
	return generation, nil
}

func (s *Stage) openStageRoot() (*os.Root, error) {
	if err := s.store.ensureRoot(); err != nil {
		return nil, err
	}
	if err := validGenerationID(s.id); err != nil {
		return nil, err
	}
	root, err := s.store.root.OpenRoot(generationPath(s.id))
	if err != nil {
		return nil, reject(ReasonIO)
	}
	if err := s.store.ensureGenerationRoot(s.id, root); err != nil {
		_ = root.Close()
		return nil, err
	}
	for _, reserved := range []string{manifestFile, receiptFile} {
		if _, err := root.Lstat(reserved); err == nil {
			_ = root.Close()
			return nil, ErrAlreadyExists
		} else if !os.IsNotExist(err) {
			_ = root.Close()
			return nil, reject(ReasonIO)
		}
	}
	return root, nil
}

// Verify pins and fully verifies an explicitly named sealed generation.
func (s *Store) Verify(ctx context.Context, id string) (*Generation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.openGeneration(ctx, id)
}

// Publish atomically selects a fully verified generation. The receipt's
// predecessor is compared with the verified current pointer while a
// crash-scoped store lock is held. A post-rename failure is never rolled back.
func (s *Store) Publish(ctx context.Context, id string) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var zero Summary
	if err := s.ensureRoot(); err != nil {
		return zero, err
	}
	target, err := s.openGeneration(ctx, id)
	if err != nil {
		return zero, err
	}
	defer func() { _ = target.Close() }()
	unlock, err := s.lockPublication(ctx)
	if err != nil {
		return zero, err
	}
	defer func() { _ = unlock() }()
	if err := s.ensureRoot(); err != nil {
		return zero, err
	}
	// Verification before waiting for the process lock is only an early reject.
	// Pin and verify the target again inside the serialized CAS window so a
	// publisher never relies on bytes observed while another publication ran.
	if err := target.Close(); err != nil {
		return zero, err
	}
	target, err = s.openGeneration(ctx, id)
	if err != nil {
		return zero, err
	}
	current, found, err := s.readPointer()
	if err != nil {
		return zero, err
	}
	if found && current.GenerationID == id && current.GenerationDigest == target.receipt.GenerationDigest {
		if err := syncDirectory(s.root, "."); err != nil {
			return zero, ErrOutcomeUnknown
		}
		return target.Summary(), nil
	}
	predecessor := ""
	if found {
		previous, err := s.openGeneration(ctx, current.GenerationID)
		if err != nil {
			return zero, err
		}
		if previous.receipt.GenerationDigest != current.GenerationDigest {
			_ = previous.Close()
			return zero, reject(ReasonDigest)
		}
		predecessor = previous.receipt.GenerationDigest
		if err := previous.Close(); err != nil {
			return zero, err
		}
	}
	if target.receipt.PredecessorDigest != predecessor {
		return zero, ErrStalePredecessor
	}
	pointer := Pointer{SchemaVersion: PointerSchemaV1, GenerationID: id, GenerationDigest: target.receipt.GenerationDigest}
	pointerBytes, err := canonicalPointer(pointer)
	if err != nil {
		return zero, err
	}
	if err := s.hit("before_pointer_write"); err != nil {
		return zero, reject(ReasonIO)
	}
	observed, observedFound, err := s.readPointer()
	if err != nil {
		return zero, err
	}
	if observedFound != found || (found && observed != current) {
		return zero, reject(ReasonConcurrent)
	}
	renamed, err := s.replacePointer(pointerBytes)
	if err != nil {
		if renamed {
			return zero, ErrOutcomeUnknown
		}
		return zero, err
	}
	if err := s.hit("after_pointer_rename"); err != nil {
		return zero, ErrOutcomeUnknown
	}
	if err := syncDirectory(s.root, "."); err != nil {
		return zero, ErrOutcomeUnknown
	}
	if err := s.hit("after_pointer_sync"); err != nil {
		return zero, ErrOutcomeUnknown
	}
	committed, ok, err := s.readPointer()
	if err != nil || !ok || committed != pointer {
		return zero, ErrOutcomeUnknown
	}
	return target.Summary(), nil
}

// SelectCurrent returns a pinned generation that was named by one coherent
// pointer read. A concurrent publication may make it old immediately after
// selection; old generations remain valid and are never removed here.
func (s *Store) SelectCurrent(ctx context.Context) (*Generation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRoot(); err != nil {
		return nil, err
	}
	pointer, found, err := s.readPointer()
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNoCurrent
	}
	generation, err := s.openGeneration(ctx, pointer.GenerationID)
	if err != nil {
		return nil, err
	}
	if generation.receipt.GenerationDigest != pointer.GenerationDigest {
		_ = generation.Close()
		return nil, reject(ReasonDigest)
	}
	return generation, nil
}

func (s *Store) openGeneration(ctx context.Context, id string) (*Generation, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := s.ensureRoot(); err != nil {
		return nil, err
	}
	if err := validGenerationID(id); err != nil {
		return nil, err
	}
	rel := generationPath(id)
	root, err := s.root.OpenRoot(rel)
	if err != nil {
		return nil, reject(ReasonIO)
	}
	success := false
	defer func() {
		if !success {
			_ = root.Close()
		}
	}()
	if err := s.ensureGenerationRoot(id, root); err != nil {
		return nil, err
	}
	manifestBytes, err := readRequiredRegularBytes(root, manifestFile, s.limits.MaxManifestBytes)
	if err != nil {
		return nil, err
	}
	manifest, err := parseManifest(manifestBytes, s.limits)
	if err != nil {
		return nil, err
	}
	receiptBytes, err := readRequiredRegularBytes(root, receiptFile, s.limits.MaxManifestBytes)
	if err != nil {
		return nil, err
	}
	receipt, err := parseReceipt(receiptBytes, s.limits)
	if err != nil {
		return nil, err
	}
	if err := verifyManifestReceipt(manifest, manifestBytes, receipt); err != nil {
		return nil, err
	}
	if err := scanSealed(ctx, root, manifest, manifestBytes, receiptBytes, s.limits); err != nil {
		return nil, err
	}
	if err := s.ensureGenerationRoot(id, root); err != nil {
		return nil, err
	}
	success = true
	return &Generation{id: id, root: root, manifest: manifest, receipt: receipt, limits: s.limits}, nil
}

func verifyManifestReceipt(manifest Manifest, manifestBytes []byte, receipt Receipt) error {
	if receipt.ManifestSchema != manifest.SchemaVersion || receipt.ProjectionSchema != manifest.ProjectionSchema ||
		receipt.GeneratorVersion != manifest.GeneratorVersion || receipt.BuildState != manifest.BuildState ||
		receipt.PredecessorDigest != manifest.PredecessorDigest || receipt.TombstoneDigest != manifest.TombstoneDigest ||
		receipt.Totals != manifest.Totals || !reflect.DeepEqual(receipt.Qualifications, manifest.Qualifications) {
		return reject(ReasonLineage)
	}
	if receipt.ManifestDigest != manifestDigest(manifestBytes) {
		return reject(ReasonDigest)
	}
	inventory, err := inventoryDigest(manifest.Members)
	if err != nil {
		return err
	}
	if receipt.InventoryDigest != inventory {
		return reject(ReasonDigest)
	}
	digest, err := generationDigest(receipt)
	if err != nil {
		return err
	}
	if receipt.GenerationDigest != digest {
		return reject(ReasonDigest)
	}
	return nil
}

func (s *Store) ensureRoot() error {
	if s == nil || s.root == nil || s.closed {
		return reject(ReasonIO)
	}
	ambient, err := os.Lstat(s.rootPath)
	if err != nil || !ambient.IsDir() || ambient.Mode().Perm() != privateDirMode {
		return reject(ReasonConcurrent)
	}
	pinned, err := s.root.Stat(".")
	if err != nil || !os.SameFile(ambient, pinned) || !pinned.IsDir() || pinned.Mode().Perm() != privateDirMode {
		return reject(ReasonConcurrent)
	}
	return nil
}

func (s *Store) ensureGenerationRoot(id string, root *os.Root) error {
	if err := s.ensureRoot(); err != nil {
		return err
	}
	ambient, err := s.root.Lstat(generationPath(id))
	if err != nil || !ambient.IsDir() || ambient.Mode().Perm() != privateDirMode {
		return reject(ReasonConcurrent)
	}
	pinned, err := root.Stat(".")
	if err != nil || !os.SameFile(ambient, pinned) || !pinned.IsDir() || pinned.Mode().Perm() != privateDirMode {
		return reject(ReasonConcurrent)
	}
	return nil
}

func (s *Store) readPointer() (Pointer, bool, error) {
	var zero Pointer
	data, err := readRegularBytes(s.root, pointerFile, maxStoredPointerBytes)
	if os.IsNotExist(err) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, err
	}
	pointer, err := parsePointer(data)
	if err != nil {
		return zero, false, err
	}
	return pointer, true, nil
}

func (s *Store) replacePointer(data []byte) (bool, error) {
	token, err := randomToken(12)
	if err != nil {
		return false, reject(ReasonIO)
	}
	temp := ".current-" + token
	file, err := s.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return false, reject(ReasonIO)
	}
	defer func() { _ = s.root.Remove(temp) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return false, reject(ReasonIO)
	}
	if err := file.Chmod(privateFileMode); err != nil {
		_ = file.Close()
		return false, reject(ReasonIO)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return false, reject(ReasonIO)
	}
	if err := file.Close(); err != nil {
		return false, reject(ReasonIO)
	}
	if err := s.hit("after_pointer_temp_sync"); err != nil {
		return false, reject(ReasonIO)
	}
	if err := s.root.Rename(temp, pointerFile); err != nil {
		return false, reject(ReasonIO)
	}
	return true, nil
}

func (s *Store) lockPublication(ctx context.Context) (func() error, error) {
	info, err := s.root.Lstat(publishLock)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != privateFileMode {
		return nil, reject(ReasonMode)
	}
	file, err := s.root.OpenFile(publishLock, os.O_RDWR, 0)
	if err != nil {
		return nil, reject(ReasonIO)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != privateFileMode {
		_ = file.Close()
		return nil, reject(ReasonConcurrent)
	}
	links, err := regularFileLinkCount(file)
	if err != nil || links != 1 {
		_ = file.Close()
		return nil, reject(ReasonType)
	}
	for {
		unlock, acquired, err := tryExclusiveLock(file)
		if err != nil {
			_ = file.Close()
			return nil, reject(ReasonIO)
		}
		if acquired {
			return func() error {
				unlockErr := unlock()
				closeErr := file.Close()
				if unlockErr != nil || closeErr != nil {
					return reject(ReasonIO)
				}
				return nil
			}, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, contextError(ctx)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (s *Store) hit(step string) error {
	if s.testHook == nil {
		return nil
	}
	return s.testHook(step)
}

func validGenerationID(id string) error {
	if len(id) != 32 || id != strings.ToLower(id) {
		return reject(ReasonFormat)
	}
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 16 {
		return reject(ReasonFormat)
	}
	return nil
}

func generationPath(id string) string { return generationsDir + "/" + id }

func randomToken(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func totalsFor(members []Member) Totals {
	totals := Totals{Members: len(members)}
	for _, member := range members {
		totals.Bytes += member.Size
	}
	return totals
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return reject(ReasonType)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("corpus operation stopped: %w", err)
	}
	return nil
}

// ID returns the content-free random generation identifier.
func (g *Generation) ID() string { return g.id }

// Manifest returns a defensive copy of the private exact inventory.
func (g *Generation) Manifest() Manifest {
	g.mu.Lock()
	defer g.mu.Unlock()
	manifest := g.manifest
	manifest.Qualifications = append([]Qualification(nil), manifest.Qualifications...)
	manifest.Members = append([]Member(nil), manifest.Members...)
	return manifest
}

// Receipt returns a defensive copy of the content-free seal receipt.
func (g *Generation) Receipt() Receipt {
	g.mu.Lock()
	defer g.mu.Unlock()
	receipt := g.receipt
	receipt.Qualifications = append([]Qualification(nil), receipt.Qualifications...)
	return receipt
}

// Summary returns the public content-free aggregate view.
func (g *Generation) Summary() Summary {
	g.mu.Lock()
	defer g.mu.Unlock()
	services := make([]Service, 0, len(g.receipt.Qualifications))
	for _, qualification := range g.receipt.Qualifications {
		services = append(services, qualification.Service)
	}
	return Summary{
		GenerationDigest: g.receipt.GenerationDigest,
		ManifestSchema:   g.receipt.ManifestSchema,
		ReceiptSchema:    g.receipt.SchemaVersion,
		ProjectionSchema: g.receipt.ProjectionSchema,
		GeneratorVersion: g.receipt.GeneratorVersion,
		BuildState:       g.receipt.BuildState,
		Services:         services,
		Totals:           g.receipt.Totals,
	}
}

// Close releases the pinned generation directory.
func (g *Generation) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	if err := g.root.Close(); err != nil {
		return reject(ReasonIO)
	}
	return nil
}
