package app

import (
	"context"
	"fmt"

	"github.com/isukharev/atl/internal/domain"
)

const confluenceHistorySchemaVersion = 1

// ConfluenceHistoryResult is the qualified page-history listing. Versions is
// always a non-nil array, so an empty result is proven-empty evidence rather
// than an absent read; Complete is true only when the backend version listing
// was exhausted, and every false value carries a static PartialReason from the
// closed domain set. PageID is the resolved content id the versions belong to.
type ConfluenceHistoryResult struct {
	SchemaVersion int              `json:"schema_version"`
	PageID        string           `json:"page_id"`
	Count         int              `json:"count"`
	Complete      bool             `json:"complete"`
	PartialReason string           `json:"partial_reason,omitempty"`
	Versions      []domain.Version `json:"versions"`
}

// History resolves one page reference and lists its version records, preferring
// the qualified capability so a capped or stalled prefix cannot be mistaken for
// an exhausted listing. A backend that implements only the compatibility port
// proves nothing about exhaustion, so its listing stays partial
// (legacy_unqualified) with a proven non-nil array rather than being promoted
// to complete evidence.
func (s *ConfluenceService) History(ctx context.Context, reference string) (*ConfluenceHistoryResult, error) {
	resolved, err := s.ResolvePageReference(ctx, reference)
	if err != nil {
		return nil, err
	}
	id := resolved.ID
	inventory := domain.VersionInventory{PartialReason: domain.HistoryPartialLegacyUnqualified}
	if qualified, ok := s.store.(domain.QualifiedHistoryReader); ok {
		inventory, err = qualified.HistoryQualified(ctx, id)
	} else {
		inventory.Versions, err = s.store.History(ctx, id)
		if inventory.Versions == nil {
			inventory.Versions = []domain.Version{}
		}
	}
	if err != nil {
		return nil, err
	}
	if err := validateConfluenceHistory(inventory); err != nil {
		return nil, err
	}
	return &ConfluenceHistoryResult{
		SchemaVersion: confluenceHistorySchemaVersion,
		PageID:        id,
		Count:         len(inventory.Versions),
		Complete:      inventory.Complete,
		PartialReason: inventory.PartialReason,
		Versions:      inventory.Versions,
	}, nil
}

func validateConfluenceHistory(inventory domain.VersionInventory) error {
	if inventory.Versions == nil {
		return fmt.Errorf("%w: Confluence page history is unavailable", domain.ErrCheckFailed)
	}
	if inventory.Complete && inventory.PartialReason != "" {
		return fmt.Errorf("%w: complete Confluence page history has a partial reason", domain.ErrCheckFailed)
	}
	if !inventory.Complete && !domain.ValidHistoryPartialReason(inventory.PartialReason) {
		return fmt.Errorf("%w: partial Confluence page history has no recognized reason", domain.ErrCheckFailed)
	}
	prev := 0
	for i, version := range inventory.Versions {
		if version.Number <= 0 {
			return fmt.Errorf("%w: Confluence page history contains a non-positive version number", domain.ErrCheckFailed)
		}
		if i > 0 && version.Number >= prev {
			// History is newest-first, so version numbers must strictly descend; a
			// duplicate or an out-of-order record breaks the exhaustion claim.
			return fmt.Errorf("%w: Confluence page history is not strictly newest-first", domain.ErrCheckFailed)
		}
		prev = version.Number
	}
	return nil
}
