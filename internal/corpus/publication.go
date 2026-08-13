package corpus

import (
	"context"
	"os"
	"reflect"
	"time"
)

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
	prelockTarget, err := s.openGeneration(ctx, id)
	if err != nil {
		return zero, err
	}
	defer func(target *Generation) { _ = target.Close() }(prelockTarget)
	unlock, err := s.lockPublication(ctx)
	if err != nil {
		return zero, err
	}
	defer func() { _ = unlock() }()
	if err := s.ensureRoot(); err != nil {
		return zero, err
	}
	if err := s.requireNoRetentionQuarantine(); err != nil {
		return zero, err
	}
	// Verification before waiting for the process lock is only an early reject.
	// Pin and verify the target again inside the serialized CAS window so a
	// publisher never relies on bytes observed while another publication ran.
	if err := prelockTarget.Close(); err != nil {
		return zero, err
	}
	if err := s.hit("before_locked_target_open"); err != nil {
		return zero, reject(ReasonIO)
	}
	target, err := s.openGeneration(ctx, id)
	if err != nil {
		return zero, err
	}
	defer func(target *Generation) { _ = target.Close() }(target)
	current, found, err := s.readPointer()
	if err != nil {
		return zero, err
	}
	if found && current.GenerationID == id && current.GenerationDigest == target.receipt.GenerationDigest {
		if err := s.syncAndRevalidatePinnedGeneration(ctx, target); err != nil {
			return zero, err
		}
		observed, observedFound, err := s.readPointer()
		if err != nil {
			return zero, err
		}
		if !observedFound || observed != current {
			return zero, reject(ReasonConcurrent)
		}
		if err := s.hit("before_idempotent_pointer_sync"); err != nil {
			return zero, reject(ReasonIO)
		}
		if err := syncDirectory(s.root, "."); err != nil {
			return zero, ErrOutcomeUnknown
		}
		if err := s.hit("after_pointer_sync"); err != nil {
			return zero, ErrOutcomeUnknown
		}
		committed, ok, err := s.readPointer()
		if err != nil || !ok || committed != current {
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
	if err := s.syncAndRevalidatePinnedGeneration(ctx, target); err != nil {
		return zero, err
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

func (s *Store) syncAndRevalidatePinnedGeneration(ctx context.Context, target *Generation) error {
	// A pointer can survive a crash independently of the process that sealed its
	// target. Make the verified directory entries durable, then rescan the same
	// pinned directory at the last deterministic boundary before selection.
	if err := s.hit("before_publish_target_sync"); err != nil {
		return reject(ReasonIO)
	}
	if err := syncDirectory(target.root, "."); err != nil {
		return reject(ReasonIO)
	}
	if err := s.hit("after_publish_target_sync"); err != nil {
		return reject(ReasonIO)
	}
	return s.revalidatePinnedGeneration(ctx, target)
}

func (s *Store) revalidatePinnedGeneration(ctx context.Context, target *Generation) error {
	manifestBytes, err := canonicalManifest(target.manifest, s.limits)
	if err != nil {
		return err
	}
	receiptBytes, err := canonicalReceipt(target.receipt, s.limits)
	if err != nil {
		return err
	}
	if err := scanSealed(ctx, target.root, target.manifest, manifestBytes, receiptBytes, s.limits); err != nil {
		return err
	}
	return s.ensureGenerationRoot(target.id, target.root)
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

// ConfirmCurrent serializes with publication, fully revalidates the caller's
// pinned generation, and proves that the exact pointer did not change across
// that verification. The corpus layer performs filesystem work only; callers
// must complete every remote check before entering this lock window.
func (s *Store) ConfirmCurrent(ctx context.Context, generation *Generation) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if generation == nil {
		return reject(ReasonType)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	generation.mu.Lock()
	defer generation.mu.Unlock()
	if generation.closed {
		return reject(ReasonType)
	}
	if err := s.ensureRoot(); err != nil {
		return err
	}
	unlock, err := s.lockPublication(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()

	pointer, found, err := s.readPointer()
	if err != nil {
		return err
	}
	want := Pointer{
		SchemaVersion: PointerSchemaV1, GenerationID: generation.id,
		GenerationDigest: generation.receipt.GenerationDigest,
	}
	if !found || pointer != want {
		return reject(ReasonConcurrent)
	}
	if err := s.revalidatePinnedGeneration(ctx, generation); err != nil {
		return err
	}
	if err := s.hit("after_confirm_current_revalidate"); err != nil {
		return reject(ReasonIO)
	}
	observed, observedFound, err := s.readPointer()
	if err != nil {
		return err
	}
	if !observedFound || observed != pointer {
		return reject(ReasonConcurrent)
	}
	return nil
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
	if err != nil || !exactRegularMode(info.Mode(), privateFileMode) {
		return nil, reject(ReasonMode)
	}
	file, err := s.root.OpenFile(publishLock, os.O_RDWR, 0)
	if err != nil {
		return nil, reject(ReasonIO)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !exactRegularMode(opened.Mode(), privateFileMode) {
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
