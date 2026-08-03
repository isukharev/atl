package app

import (
	"context"

	"github.com/isukharev/atl/internal/domain"
)

const confluenceRemoteEvidenceIncomplete = "remote_evidence_incomplete"

type confluenceRemoteMetadataEvidence struct {
	version   int
	available bool
	reason    string
}

// readConfluenceRemoteMetadataBatches validates an entire qualified batch
// before crediting any row. A malformed, partial, duplicate, unexpected, or
// omitted response therefore cannot make a subset look authoritative.
func readConfluenceRemoteMetadataBatches(ctx context.Context, reader domain.QualifiedConfluencePageMetadataBatchReader, ids []string) map[string]confluenceRemoteMetadataEvidence {
	out := make(map[string]confluenceRemoteMetadataEvidence, len(ids))
	mark := func(batch []string, reason string) {
		for _, id := range batch {
			out[id] = confluenceRemoteMetadataEvidence{reason: reason}
		}
	}
	batches, err := reader.PlanPageMetadataBatches(ids)
	if err != nil || !exactConfluenceMetadataBatchPlan(ids, batches) {
		mark(ids, confluenceRemoteEvidenceIncomplete)
		return out
	}
	for _, batch := range batches {
		page, readErr := reader.ReadPageMetadataBatch(ctx, batch)
		if readErr != nil {
			// A search-level typed error does not prove that each requested page
			// individually has that state. Keep the batch failure coarse so a 404
			// cannot be projected as authoritative per-page absence.
			mark(batch, confluenceRemoteEvidenceIncomplete)
			continue
		}
		if !page.Complete || page.PartialReason != "" || !validConfluenceMetadataBatch(batch, page.Results) {
			mark(batch, confluenceRemoteEvidenceIncomplete)
			continue
		}
		for _, result := range page.Results {
			out[result.ID] = confluenceRemoteMetadataEvidence{version: result.Version, available: true}
		}
	}
	return out
}

func exactConfluenceMetadataBatchPlan(ids []string, batches [][]string) bool {
	position := 0
	for _, batch := range batches {
		if len(batch) == 0 || len(batch) > 100 || position+len(batch) > len(ids) {
			return false
		}
		for _, id := range batch {
			if ids[position] != id {
				return false
			}
			position++
		}
	}
	return position == len(ids)
}

func validConfluenceMetadataBatch(requested []string, results []domain.PageRef) bool {
	want := make(map[string]bool, len(requested))
	for _, id := range requested {
		if id == "" || want[id] {
			return false
		}
		want[id] = true
	}
	seen := make(map[string]bool, len(results))
	for _, result := range results {
		if result.ID == "" || result.Version <= 0 || !want[result.ID] || seen[result.ID] {
			return false
		}
		seen[result.ID] = true
	}
	return len(seen) == len(want)
}
