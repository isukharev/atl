package corpus

import (
	"context"
	"os"
	"reflect"
)

type retentionPinnedCandidate struct {
	record     RetentionInventoryRecordV1
	generation *Generation
}

func reconcileRetentionApply(plan RetentionPlanV1, snapshot retentionInventorySnapshot) ([]RetentionInventoryRecordV1, error) {
	if len(snapshot.unsealedIDs) != plan.UnsealedStages || snapshot.unsealedDigest != plan.UnsealedInventoryDigest {
		return nil, reject(ReasonConcurrent)
	}
	original := make(map[string]RetentionInventoryRecordV1, len(plan.Inventory))
	protected := make(map[string]struct{}, len(plan.Protected))
	candidates := make(map[string]struct{}, len(plan.Candidates))
	for _, record := range plan.Inventory {
		original[record.GenerationID] = record
	}
	for _, ref := range plan.Protected {
		protected[ref.GenerationID] = struct{}{}
	}
	for _, ref := range plan.Candidates {
		candidates[ref.GenerationID] = struct{}{}
	}
	current := make(map[string]RetentionInventoryRecordV1, len(snapshot.records))
	for _, record := range snapshot.records {
		want, exists := original[record.GenerationID]
		if !exists || !reflect.DeepEqual(record, want) {
			return nil, reject(ReasonConcurrent)
		}
		current[record.GenerationID] = record
	}
	for id := range protected {
		if _, exists := current[id]; !exists {
			return nil, reject(ReasonConcurrent)
		}
	}
	remaining := make([]RetentionInventoryRecordV1, 0, len(candidates))
	for _, record := range plan.Inventory {
		if _, candidate := candidates[record.GenerationID]; !candidate {
			continue
		}
		if currentRecord, exists := current[record.GenerationID]; exists {
			remaining = append(remaining, currentRecord)
		}
	}
	return remaining, nil
}

func retentionProtectedRecords(plan RetentionPlanV1) []RetentionInventoryRecordV1 {
	records := make(map[string]RetentionInventoryRecordV1, len(plan.Inventory))
	for _, record := range plan.Inventory {
		records[record.GenerationID] = record
	}
	result := make([]RetentionInventoryRecordV1, 0, len(plan.Protected))
	for _, ref := range plan.Protected {
		result = append(result, records[ref.GenerationID])
	}
	return result
}

func pinRetentionRecords(ctx context.Context, s *Store, records []RetentionInventoryRecordV1) ([]retentionPinnedCandidate, error) {
	pinned := make([]retentionPinnedCandidate, 0, len(records))
	for _, record := range records {
		generation, err := s.openGeneration(ctx, record.GenerationID)
		if err != nil {
			closeRetentionCandidates(pinned)
			return nil, err
		}
		actual, recordErr := retentionRecordForGeneration(ctx, generation)
		if recordErr != nil || !reflect.DeepEqual(actual, record) {
			_ = generation.Close()
			closeRetentionCandidates(pinned)
			return nil, reject(ReasonConcurrent)
		}
		pinned = append(pinned, retentionPinnedCandidate{record: record, generation: generation})
	}
	return pinned, nil
}

func revalidateRetentionPinned(ctx context.Context, s *Store, pinned []retentionPinnedCandidate) error {
	for index := range pinned {
		if err := s.revalidatePinnedGeneration(ctx, pinned[index].generation); err != nil {
			return err
		}
	}
	return nil
}

func cloneRetentionEntries(entries map[string]retentionGenerationEntry) map[string]retentionGenerationEntry {
	result := make(map[string]retentionGenerationEntry, len(entries))
	for id, entry := range entries {
		result[id] = entry
	}
	return result
}

func (s *Store) confirmRetentionEntries(ctx context.Context, expected map[string]retentionGenerationEntry, allowedPlanDigest string) error {
	observed, err := s.listRetentionGenerationEntries(ctx, allowedPlanDigest)
	if err != nil {
		return err
	}
	if len(observed) != len(expected) {
		return reject(ReasonConcurrent)
	}
	for id, want := range expected {
		got, exists := observed[id]
		if !exists || !os.SameFile(want.info, got.info) {
			return reject(ReasonConcurrent)
		}
	}
	return nil
}

func closeRetentionCandidates(candidates []retentionPinnedCandidate) {
	for index := range candidates {
		_ = candidates[index].generation.Close()
	}
}

func snapshotUnsealedCount(snapshot retentionInventorySnapshot) int {
	return len(snapshot.unsealedIDs)
}
