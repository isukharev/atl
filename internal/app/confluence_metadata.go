package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const ConfluencePageMetadataSchemaVersion = 1

const (
	ConfluenceRestrictionUnknown      = "unknown"
	ConfluenceRestrictionRestricted   = "restricted"
	ConfluenceRestrictionUnrestricted = "unrestricted"
)

// ConfluencePageMetadataResult is the closed, non-body page metadata projection
// used by typed agent transports. It deliberately excludes URLs, labels,
// ancestors, restriction principals, and arbitrary backend expansion payloads.
type ConfluencePageMetadataResult struct {
	SchemaVersion    int    `json:"schema_version"`
	ID               string `json:"id"`
	Title            string `json:"title"`
	Space            string `json:"space"`
	Version          int    `json:"version"`
	Updated          string `json:"updated,omitempty"`
	RestrictionState string `json:"restriction_state"`
}

// PageMetadata resolves one exact page reference and returns a closed non-body
// projection. The id returned by the store must still match the resolved page:
// a transport must never attach metadata from a different page to the caller's
// reference.
func (s *ConfluenceService) PageMetadata(ctx context.Context, reference string) (*ConfluencePageMetadataResult, error) {
	resolved, err := s.ResolvePageReference(ctx, reference)
	if err != nil {
		return nil, err
	}
	ctx = resolved.Context(ctx)
	meta, err := s.store.GetMeta(ctx, resolved.ID)
	if err != nil {
		return nil, err
	}
	if meta == nil || meta.ID != resolved.ID || strings.TrimSpace(meta.ID) == "" ||
		strings.TrimSpace(meta.Title) == "" || strings.TrimSpace(meta.Space) == "" || meta.Version < 1 {
		return nil, fmt.Errorf("%w: Confluence page metadata is not reconciled", domain.ErrCheckFailed)
	}
	restrictionState := ConfluenceRestrictionUnknown
	if meta.Restrictions != nil {
		restrictionState = ConfluenceRestrictionUnrestricted
		if *meta.Restrictions {
			restrictionState = ConfluenceRestrictionRestricted
		}
	}
	return &ConfluencePageMetadataResult{
		SchemaVersion: ConfluencePageMetadataSchemaVersion,
		ID:            meta.ID, Title: meta.Title, Space: meta.Space, Version: meta.Version,
		Updated: meta.Updated, RestrictionState: restrictionState,
	}, nil
}
