package corpus

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sort"
)

const (
	RetentionPolicySchemaV1 = 1
	RetentionPlanSchemaV1   = 1
	RetentionStatusSchemaV1 = 1

	// MaxRetentionPlanBytesV1 is the hard codec bound for a reviewed private
	// retention artifact. The active Store limit may make it smaller.
	MaxRetentionPlanBytesV1 int64 = 64 << 20

	retentionPlanDigestDomain     = "atl.corpus.retention-plan.v1"
	retentionUnsealedDigestDomain = "atl.corpus.retention-unsealed-inventory.v1"
)

// RetentionPolicyV1 retains the current generation plus this many active
// predecessor generations. A value below one is never a destructive policy.
type RetentionPolicyV1 struct {
	SchemaVersion      int `json:"schema_version"`
	RetainPredecessors int `json:"retain_predecessors"`
}

// RetentionInventoryRecordV1 is one exact sealed generation in a private
// reviewed inventory. Generation IDs and digests must never enter status.
type RetentionInventoryRecordV1 struct {
	GenerationID                string `json:"generation_id"`
	GenerationDigest            string `json:"generation_digest"`
	PredecessorGenerationID     string `json:"predecessor_generation_id,omitempty"`
	PredecessorGenerationDigest string `json:"predecessor_generation_digest,omitempty"`
	Totals                      Totals `json:"totals"`
}

// RetentionGenerationRefV1 identifies an exact protected or candidate record
// inside the reviewed plan.
type RetentionGenerationRefV1 struct {
	GenerationID     string `json:"generation_id"`
	GenerationDigest string `json:"generation_digest"`
}

// RetentionPlanV1 is a private exact review artifact. It contains stable local
// generation identities and must not be emitted as ordinary command status.
type RetentionPlanV1 struct {
	SchemaVersion           int                          `json:"schema_version"`
	RootDigest              string                       `json:"root_digest"`
	Current                 Pointer                      `json:"current"`
	Policy                  RetentionPolicyV1            `json:"policy"`
	Inventory               []RetentionInventoryRecordV1 `json:"inventory"`
	Protected               []RetentionGenerationRefV1   `json:"protected"`
	Candidates              []RetentionGenerationRefV1   `json:"candidates"`
	UnsealedStages          int                          `json:"unsealed_stages"`
	UnsealedInventoryDigest string                       `json:"unsealed_inventory_digest"`
	PlanDigest              string                       `json:"plan_digest"`
}

// RetentionStatusV1 is content-free. It reports counts only, never generation
// IDs, digests, paths, selectors, principals, or backend values.
type RetentionStatusV1 struct {
	SchemaVersion        int  `json:"schema_version"`
	SealedGenerations    int  `json:"sealed_generations"`
	ProtectedGenerations int  `json:"protected_generations"`
	CandidateGenerations int  `json:"candidate_generations"`
	UnsealedStages       int  `json:"unsealed_stages"`
	RemovedThisApply     int  `json:"removed_this_apply"`
	DeletedCandidates    int  `json:"deleted_candidates"`
	RemainingCandidates  int  `json:"remaining_candidates"`
	Complete             bool `json:"complete"`
}

type retentionInventorySnapshot struct {
	records        []RetentionInventoryRecordV1
	unsealedIDs    []string
	unsealedDigest string
	entries        map[string]retentionGenerationEntry
}

type retentionGenerationEntry struct {
	info os.FileInfo
}

// RetentionInventoryStatusV1 fully verifies every sealed generation and counts
// retained unsealed stages without requiring a current pointer or constructing
// a destructive plan. The returned status contains no identities or digests.
func (s *Store) RetentionInventoryStatusV1(ctx context.Context) (RetentionStatusV1, error) {
	var zero RetentionStatusV1
	if s == nil {
		return zero, reject(ReasonType)
	}
	if err := contextError(ctx); err != nil {
		return zero, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRoot(); err != nil {
		return zero, err
	}
	unlock, err := s.lockPublication(ctx)
	if err != nil {
		return zero, err
	}
	defer func() { _ = unlock() }()
	if err := s.requireNoRetentionQuarantine(); err != nil {
		return zero, err
	}
	snapshot, err := s.scanRetentionInventory(ctx, "")
	if err != nil {
		return zero, err
	}
	return RetentionStatusV1{
		SchemaVersion: RetentionStatusSchemaV1, SealedGenerations: len(snapshot.records),
		UnsealedStages: len(snapshot.unsealedIDs), Complete: true,
	}, nil
}

// BuildRetentionPlanV1 snapshots a coherent, fully verified sealed inventory
// under the publication lock and constructs a self-digested private plan.
func BuildRetentionPlanV1(ctx context.Context, s *Store, policy RetentionPolicyV1) (RetentionPlanV1, error) {
	if s == nil {
		return RetentionPlanV1{}, reject(ReasonType)
	}
	if err := contextError(ctx); err != nil {
		return RetentionPlanV1{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRoot(); err != nil {
		return RetentionPlanV1{}, err
	}
	if err := validateRetentionPolicy(policy, s.limits); err != nil {
		return RetentionPlanV1{}, err
	}
	unlock, err := s.lockPublication(ctx)
	if err != nil {
		return RetentionPlanV1{}, err
	}
	defer func() { _ = unlock() }()
	if err := s.requireNoRetentionQuarantine(); err != nil {
		return RetentionPlanV1{}, err
	}

	pointer, found, err := s.readPointer()
	if err != nil {
		return RetentionPlanV1{}, err
	}
	if !found {
		return RetentionPlanV1{}, ErrNoCurrent
	}
	snapshot, err := s.scanRetentionInventory(ctx, "")
	if err != nil {
		return RetentionPlanV1{}, err
	}
	rootDigest, err := RootIdentityDigest(s.rootPath)
	if err != nil {
		return RetentionPlanV1{}, err
	}
	if err := s.ensureRoot(); err != nil {
		return RetentionPlanV1{}, err
	}
	observed, observedFound, err := s.readPointer()
	if err != nil || !observedFound || observed != pointer {
		return RetentionPlanV1{}, reject(ReasonConcurrent)
	}
	plan, err := buildRetentionPlan(rootDigest, pointer, policy, snapshot, s.limits)
	if err != nil {
		return RetentionPlanV1{}, err
	}
	return plan, nil
}

// Status returns the content-free preview counts for a verified plan value.
func (plan RetentionPlanV1) Status() RetentionStatusV1 {
	return RetentionStatusV1{
		SchemaVersion:     RetentionStatusSchemaV1,
		SealedGenerations: len(plan.Inventory), ProtectedGenerations: len(plan.Protected),
		CandidateGenerations: len(plan.Candidates), UnsealedStages: plan.UnsealedStages,
		RemainingCandidates: len(plan.Candidates), Complete: len(plan.Candidates) == 0,
	}
}

// CanonicalRetentionPlanV1 returns the exact reviewed private artifact bytes.
func CanonicalRetentionPlanV1(plan RetentionPlanV1, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if err := validateRetentionPlan(plan, limits, true); err != nil {
		return nil, err
	}
	want, err := retentionPlanDigest(plan)
	if err != nil {
		return nil, err
	}
	if want != plan.PlanDigest {
		return nil, reject(ReasonDigest)
	}
	data, err := marshalCanonical(plan)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > retentionPlanByteLimit(limits) {
		return nil, reject(ReasonBounds)
	}
	return data, nil
}

// ParseRetentionPlanV1 accepts only exact canonical schema-v1 bytes.
func ParseRetentionPlanV1(data []byte, limits Limits) (RetentionPlanV1, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return RetentionPlanV1{}, err
	}
	if len(data) == 0 || int64(len(data)) > retentionPlanByteLimit(limits) {
		return RetentionPlanV1{}, reject(ReasonBounds)
	}
	var plan RetentionPlanV1
	if err := decodeStrictObject(data, &plan); err != nil {
		return RetentionPlanV1{}, err
	}
	canonical, err := CanonicalRetentionPlanV1(plan, limits)
	if err != nil {
		return RetentionPlanV1{}, err
	}
	if !bytes.Equal(data, canonical) {
		return RetentionPlanV1{}, reject(ReasonFormat)
	}
	return plan, nil
}

// VerifyRetentionPlanV1 verifies the plan's complete structural and self-
// digest contract without reading a Store.
func VerifyRetentionPlanV1(plan RetentionPlanV1, limits Limits) error {
	_, err := CanonicalRetentionPlanV1(plan, limits)
	return err
}

// ApplyRetentionPlanV1 revalidates reviewed bytes and exact Store state under
// the publication lock, then atomically quarantines only still-present original
// candidates before recursive cleanup. Missing candidate subsets are accepted
// for crash-resume; every other drift fails closed. Any failure after a
// quarantine exists returns ErrOutcomeUnknown and the same plan can resume it.
func (s *Store) ApplyRetentionPlanV1(ctx context.Context, reviewed []byte, expectedDigest string) (RetentionStatusV1, error) {
	var zero RetentionStatusV1
	if s == nil {
		return zero, reject(ReasonType)
	}
	if err := contextError(ctx); err != nil {
		return zero, err
	}
	plan, err := ParseRetentionPlanV1(reviewed, s.limits)
	if err != nil {
		return zero, err
	}
	if !isLowerSHA256(expectedDigest) || expectedDigest != plan.PlanDigest {
		return zero, reject(ReasonDigest)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRoot(); err != nil {
		return zero, err
	}
	rootDigest, err := RootIdentityDigest(s.rootPath)
	if err != nil {
		return zero, err
	}
	if err := s.ensureRoot(); err != nil {
		return zero, err
	}
	if rootDigest != plan.RootDigest {
		return zero, reject(ReasonPath)
	}
	unlock, err := s.lockPublication(ctx)
	if err != nil {
		return zero, err
	}
	defer func() { _ = unlock() }()

	pointer, found, err := s.readPointer()
	if err != nil {
		return zero, err
	}
	if !found || pointer != plan.Current {
		return zero, reject(ReasonConcurrent)
	}
	quarantined, err := s.scanRetentionQuarantine(plan)
	if err != nil {
		return zero, err
	}
	outcomeStarted := quarantined.present
	snapshot, err := s.scanRetentionInventory(ctx, plan.PlanDigest)
	if err != nil {
		if outcomeStarted {
			return zero, ErrOutcomeUnknown
		}
		return zero, err
	}
	if err := validateRetentionQuarantine(plan, snapshot, quarantined); err != nil {
		if outcomeStarted {
			return zero, ErrOutcomeUnknown
		}
		return zero, err
	}
	remaining, err := reconcileRetentionApply(plan, snapshot)
	if err != nil {
		if outcomeStarted {
			return zero, ErrOutcomeUnknown
		}
		return zero, err
	}
	observed, observedFound, err := s.readPointer()
	if err != nil || !observedFound || observed != pointer {
		if outcomeStarted {
			return zero, ErrOutcomeUnknown
		}
		return zero, reject(ReasonConcurrent)
	}

	protected, err := pinRetentionRecords(ctx, s, retentionProtectedRecords(plan))
	if err != nil {
		if outcomeStarted {
			return zero, ErrOutcomeUnknown
		}
		return zero, err
	}
	defer closeRetentionCandidates(protected)
	pinned, err := pinRetentionRecords(ctx, s, remaining)
	if err != nil {
		if outcomeStarted {
			return zero, ErrOutcomeUnknown
		}
		return zero, err
	}
	if err := revalidateRetentionPinned(ctx, s, protected); err != nil {
		closeRetentionCandidates(pinned)
		if outcomeStarted {
			return zero, ErrOutcomeUnknown
		}
		return zero, err
	}
	observed, observedFound, err = s.readPointer()
	if err != nil || !observedFound || observed != pointer {
		closeRetentionCandidates(pinned)
		if outcomeStarted {
			return zero, ErrOutcomeUnknown
		}
		return zero, reject(ReasonConcurrent)
	}
	expectedEntries := cloneRetentionEntries(snapshot.entries)

	removed := 0
	for index := range pinned {
		candidate := &pinned[index]
		if err := contextError(ctx); err != nil {
			closeRetentionCandidates(pinned)
			if outcomeStarted {
				return zero, ErrOutcomeUnknown
			}
			return zero, err
		}
		if err := s.revalidatePinnedGeneration(ctx, candidate.generation); err != nil {
			closeRetentionCandidates(pinned)
			if outcomeStarted {
				return zero, ErrOutcomeUnknown
			}
			return zero, err
		}
		if err := revalidateRetentionPinned(ctx, s, protected); err != nil {
			closeRetentionCandidates(pinned)
			if outcomeStarted {
				return zero, ErrOutcomeUnknown
			}
			return zero, err
		}
		currentPointer, currentFound, pointerErr := s.readPointer()
		if pointerErr != nil || !currentFound || currentPointer != pointer {
			closeRetentionCandidates(pinned)
			if outcomeStarted {
				return zero, ErrOutcomeUnknown
			}
			return zero, reject(ReasonConcurrent)
		}
		if err := s.hit("before_retention_remove"); err != nil {
			closeRetentionCandidates(pinned)
			if outcomeStarted {
				return zero, ErrOutcomeUnknown
			}
			return zero, reject(ReasonIO)
		}
		if err := s.confirmRetentionEntries(ctx, expectedEntries, plan.PlanDigest); err != nil {
			closeRetentionCandidates(pinned)
			if outcomeStarted {
				return zero, ErrOutcomeUnknown
			}
			return zero, err
		}
		if err := s.hit("after_retention_namespace_check"); err != nil {
			closeRetentionCandidates(pinned)
			if outcomeStarted {
				return zero, ErrOutcomeUnknown
			}
			return zero, reject(ReasonIO)
		}
		if err := quarantinePinnedRetentionGeneration(s, plan.PlanDigest, candidate); err != nil {
			closeRetentionCandidates(pinned)
			if outcomeStarted {
				return zero, ErrOutcomeUnknown
			}
			return zero, err
		}
		outcomeStarted = true
		removed++
		delete(expectedEntries, candidate.record.GenerationID)
		if err := s.hit("after_retention_remove"); err != nil {
			closeRetentionCandidates(pinned)
			return zero, ErrOutcomeUnknown
		}
	}
	closeRetentionCandidates(pinned)
	if err := revalidateRetentionPinned(ctx, s, protected); err != nil {
		if outcomeStarted {
			return zero, ErrOutcomeUnknown
		}
		return zero, err
	}
	if err := s.confirmRetentionEntries(ctx, expectedEntries, plan.PlanDigest); err != nil {
		if outcomeStarted {
			return zero, ErrOutcomeUnknown
		}
		return zero, err
	}
	if err := s.purgeRetentionQuarantine(ctx, plan); err != nil {
		if outcomeStarted {
			return zero, ErrOutcomeUnknown
		}
		return zero, err
	}
	if err := s.hit("after_retention_sync"); err != nil {
		if outcomeStarted {
			return zero, ErrOutcomeUnknown
		}
		return zero, reject(ReasonIO)
	}
	committed, committedFound, err := s.readPointer()
	if err != nil || !committedFound || committed != pointer {
		if outcomeStarted {
			return zero, ErrOutcomeUnknown
		}
		return zero, reject(ReasonConcurrent)
	}
	if err := s.confirmRetentionEntries(ctx, expectedEntries, plan.PlanDigest); err != nil {
		if outcomeStarted {
			return zero, ErrOutcomeUnknown
		}
		return zero, err
	}
	return RetentionStatusV1{
		SchemaVersion:        RetentionStatusSchemaV1,
		SealedGenerations:    len(snapshot.records) - removed,
		ProtectedGenerations: len(plan.Protected), CandidateGenerations: len(plan.Candidates),
		UnsealedStages: snapshotUnsealedCount(snapshot), RemovedThisApply: removed,
		DeletedCandidates: len(plan.Candidates), RemainingCandidates: 0, Complete: true,
	}, nil
}

func buildRetentionPlan(rootDigest string, pointer Pointer, policy RetentionPolicyV1, snapshot retentionInventorySnapshot, limits Limits) (RetentionPlanV1, error) {
	recordsByID := make(map[string]RetentionInventoryRecordV1, len(snapshot.records))
	recordsByDigest := make(map[string]RetentionInventoryRecordV1, len(snapshot.records))
	for _, record := range snapshot.records {
		recordsByID[record.GenerationID] = record
		if _, duplicate := recordsByDigest[record.GenerationDigest]; duplicate {
			return RetentionPlanV1{}, reject(ReasonDigest)
		}
		recordsByDigest[record.GenerationDigest] = record
	}
	current, present := recordsByID[pointer.GenerationID]
	if !present || current.GenerationDigest != pointer.GenerationDigest {
		return RetentionPlanV1{}, reject(ReasonLineage)
	}
	protected := make([]RetentionGenerationRefV1, 0, policy.RetainPredecessors+1)
	protectedIDs := make(map[string]struct{}, policy.RetainPredecessors+1)
	for depth := 0; ; depth++ {
		if _, duplicate := protectedIDs[current.GenerationID]; duplicate {
			return RetentionPlanV1{}, reject(ReasonLineage)
		}
		protectedIDs[current.GenerationID] = struct{}{}
		protected = append(protected, retentionRef(current))
		if depth >= policy.RetainPredecessors || current.PredecessorGenerationDigest == "" {
			break
		}
		predecessor, exists := recordsByDigest[current.PredecessorGenerationDigest]
		if !exists || current.PredecessorGenerationID != "" && current.PredecessorGenerationID != predecessor.GenerationID {
			return RetentionPlanV1{}, reject(ReasonLineage)
		}
		current = predecessor
	}
	candidates := make([]RetentionGenerationRefV1, 0, len(snapshot.records)-len(protected))
	for _, record := range snapshot.records {
		if _, keep := protectedIDs[record.GenerationID]; !keep {
			candidates = append(candidates, retentionRef(record))
		}
	}
	plan := RetentionPlanV1{
		SchemaVersion: RetentionPlanSchemaV1, RootDigest: rootDigest, Current: pointer, Policy: policy,
		Inventory: append([]RetentionInventoryRecordV1(nil), snapshot.records...),
		Protected: protected, Candidates: candidates,
		UnsealedStages: len(snapshot.unsealedIDs), UnsealedInventoryDigest: snapshot.unsealedDigest,
	}
	if err := validateRetentionPlan(plan, limits, false); err != nil {
		return RetentionPlanV1{}, err
	}
	var err error
	plan.PlanDigest, err = retentionPlanDigest(plan)
	if err != nil {
		return RetentionPlanV1{}, err
	}
	if _, err := CanonicalRetentionPlanV1(plan, limits); err != nil {
		return RetentionPlanV1{}, err
	}
	return plan, nil
}

func (s *Store) scanRetentionInventory(ctx context.Context, allowedPlanDigest string) (retentionInventorySnapshot, error) {
	entries, err := s.listRetentionGenerationEntries(ctx, allowedPlanDigest)
	if err != nil {
		return retentionInventorySnapshot{}, err
	}
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > s.limits.MaxMembers {
		return retentionInventorySnapshot{}, reject(ReasonBounds)
	}
	snapshot := retentionInventorySnapshot{
		records:     make([]RetentionInventoryRecordV1, 0, len(ids)),
		unsealedIDs: make([]string, 0),
	}
	digests := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if err := contextError(ctx); err != nil {
			return retentionInventorySnapshot{}, err
		}
		root, err := s.root.OpenRoot(generationPath(id))
		if err != nil {
			return retentionInventorySnapshot{}, reject(ReasonIO)
		}
		if err := s.ensureGenerationRoot(id, root); err != nil {
			_ = root.Close()
			return retentionInventorySnapshot{}, err
		}
		receiptInfo, receiptErr := root.Lstat(receiptFile)
		if os.IsNotExist(receiptErr) {
			if err := root.Close(); err != nil {
				return retentionInventorySnapshot{}, reject(ReasonIO)
			}
			snapshot.unsealedIDs = append(snapshot.unsealedIDs, id)
			continue
		}
		if receiptErr != nil || !exactRegularMode(receiptInfo.Mode(), privateFileMode) {
			_ = root.Close()
			return retentionInventorySnapshot{}, reject(ReasonMode)
		}
		if err := root.Close(); err != nil {
			return retentionInventorySnapshot{}, reject(ReasonIO)
		}
		generation, err := s.openGeneration(ctx, id)
		if err != nil {
			return retentionInventorySnapshot{}, err
		}
		record, err := retentionRecordForGeneration(ctx, generation)
		closeErr := generation.Close()
		if err != nil {
			return retentionInventorySnapshot{}, err
		}
		if closeErr != nil {
			return retentionInventorySnapshot{}, closeErr
		}
		if _, duplicate := digests[record.GenerationDigest]; duplicate {
			return retentionInventorySnapshot{}, reject(ReasonDigest)
		}
		digests[record.GenerationDigest] = struct{}{}
		snapshot.records = append(snapshot.records, record)
	}
	finalEntries, err := s.listRetentionGenerationEntries(ctx, allowedPlanDigest)
	if err != nil || len(entries) != len(finalEntries) {
		return retentionInventorySnapshot{}, reject(ReasonConcurrent)
	}
	for id, before := range entries {
		after, present := finalEntries[id]
		if !present || !os.SameFile(before.info, after.info) {
			return retentionInventorySnapshot{}, reject(ReasonConcurrent)
		}
	}
	snapshot.unsealedDigest = retentionUnsealedInventoryDigest(snapshot.unsealedIDs)
	snapshot.entries = finalEntries
	return snapshot, nil
}

func (s *Store) listRetentionGenerationEntries(ctx context.Context, allowedPlanDigest string) (map[string]retentionGenerationEntry, error) {
	directory, directoryInfo, err := openVerifiedDirectory(s.root, generationsDir)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]retentionGenerationEntry)
	for {
		if err := contextError(ctx); err != nil {
			_ = directory.Close()
			return nil, err
		}
		batch, readErr := directory.ReadDir(1)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			_ = directory.Close()
			return nil, reject(ReasonIO)
		}
		if len(batch) == 0 {
			break
		}
		id := batch[0].Name()
		quarantinePlan, _, quarantine, quarantineErr := parseRetentionQuarantineName(id)
		if quarantine {
			if quarantineErr != nil || allowedPlanDigest == "" || quarantinePlan != allowedPlanDigest {
				_ = directory.Close()
				return nil, ErrOutcomeUnknown
			}
			info, err := s.root.Lstat(generationsDir + "/" + id)
			if err != nil || !exactDirectoryMode(info.Mode()) {
				_ = directory.Close()
				return nil, ErrOutcomeUnknown
			}
			continue
		}
		if err := validGenerationID(id); err != nil {
			_ = directory.Close()
			return nil, err
		}
		info, err := s.root.Lstat(generationPath(id))
		if err != nil || !exactDirectoryMode(info.Mode()) {
			_ = directory.Close()
			return nil, reject(ReasonMode)
		}
		if _, duplicate := entries[id]; duplicate {
			_ = directory.Close()
			return nil, reject(ReasonMembership)
		}
		entries[id] = retentionGenerationEntry{info: info}
	}
	if err := directory.Close(); err != nil {
		return nil, reject(ReasonIO)
	}
	final, err := s.root.Lstat(generationsDir)
	if err != nil || !os.SameFile(directoryInfo, final) || !exactDirectoryMode(final.Mode()) {
		return nil, reject(ReasonConcurrent)
	}
	return entries, nil
}

func retentionRecordForGeneration(ctx context.Context, generation *Generation) (RetentionInventoryRecordV1, error) {
	manifest := generation.Manifest()
	receipt := generation.Receipt()
	record := RetentionInventoryRecordV1{
		GenerationID: generation.ID(), GenerationDigest: receipt.GenerationDigest,
		PredecessorGenerationDigest: receipt.PredecessorDigest, Totals: receipt.Totals,
	}
	tombstones := make([]Member, 0, 1)
	for _, member := range manifest.Members {
		if member.Role == RoleTombstone {
			tombstones = append(tombstones, member)
		}
	}
	if manifest.TombstoneDigest == "" {
		if len(tombstones) != 0 {
			return RetentionInventoryRecordV1{}, reject(ReasonLineage)
		}
		return record, nil
	}
	if len(tombstones) != 1 || tombstones[0].SHA256 != manifest.TombstoneDigest {
		return RetentionInventoryRecordV1{}, reject(ReasonLineage)
	}
	var data bytes.Buffer
	member := tombstones[0]
	if _, err := generation.CopyMember(ctx, member.Service, member.StableID, member.Role, &data); err != nil {
		return RetentionInventoryRecordV1{}, err
	}
	delta, err := ParseGenerationDelta(data.Bytes(), generation.limits)
	if err != nil || delta.PredecessorGenerationDigest != receipt.PredecessorDigest {
		return RetentionInventoryRecordV1{}, reject(ReasonLineage)
	}
	record.PredecessorGenerationID = delta.PredecessorGenerationID
	return record, nil
}

func retentionRef(record RetentionInventoryRecordV1) RetentionGenerationRefV1 {
	return RetentionGenerationRefV1{GenerationID: record.GenerationID, GenerationDigest: record.GenerationDigest}
}
