package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const confluenceAttachmentInventorySchemaVersion = 1

// ConfluenceAttachmentInventoryOpts binds one inventory read to evidence the
// caller already holds. ExpectedPageVersion <= 0 means no gate; a positive
// value must equal the page's current version or the read is refused before any
// attachment request is issued.
type ConfluenceAttachmentInventoryOpts struct {
	ExpectedPageVersion int
	MaxPages            int
	MaxItems            int
}

// ConfluenceAttachmentInventoryResult is the qualified attachment listing.
// Attachments is always a non-nil array, so an empty result is proven-empty
// evidence; Complete is true only when the backend listing was exhausted, and
// every false value carries a static PartialReason. PageVersion is observed
// immediately before the attachment request, so a caller can record which
// page-body revision passed the pre-list gate without implying an atomic
// page/attachment snapshot.
type ConfluenceAttachmentInventoryResult struct {
	SchemaVersion int                 `json:"schema_version"`
	PageID        string              `json:"page_id"`
	PageVersion   int                 `json:"page_version"`
	Count         int                 `json:"count"`
	Complete      bool                `json:"complete"`
	PartialReason string              `json:"partial_reason,omitempty"`
	Attachments   []domain.Attachment `json:"attachments"`
}

// ConfluencePageVersionMismatchError reports that the page moved away from the
// version the caller bound its request to. It deliberately carries only the two
// integer versions — never a page id, title, space, URL, body, attachment
// filename, or backend text — so a transport can report a recoverable staleness
// condition without disclosing page content. It unwraps to domain.ErrCheckFailed
// so sentinel-driven exit codes and classification stay unchanged.
type ConfluencePageVersionMismatchError struct {
	Expected int
	Current  int
}

func (e *ConfluencePageVersionMismatchError) Error() string {
	return fmt.Sprintf("%v: Confluence page version mismatch: expected %d, current %d", e.Unwrap(), e.Expected, e.Current)
}

func (e *ConfluencePageVersionMismatchError) Unwrap() error { return domain.ErrCheckFailed }

func (e *ConfluencePageVersionMismatchError) DiagnosticVersionMismatch() (expected, observed int) {
	if e == nil {
		return 0, 0
	}
	return e.Expected, e.Current
}

// AttachmentInventory resolves one page reference, reads its current metadata,
// optionally gates on an expected version, and only then lists attachments. The
// ordering matters: a mismatch must cost no attachment request. This is a
// pre-list consistency gate, not a claim that page metadata and every paginated
// attachment response form one atomic backend snapshot.
func (s *ConfluenceService) AttachmentInventory(ctx context.Context, reference string, opts ConfluenceAttachmentInventoryOpts) (*ConfluenceAttachmentInventoryResult, error) {
	resolved, err := s.ResolvePageReference(ctx, reference)
	if err != nil {
		return nil, err
	}
	ctx = resolved.Context(ctx)
	meta, err := s.store.GetMeta(ctx, resolved.ID)
	if err != nil {
		return nil, err
	}
	if meta == nil || strings.TrimSpace(meta.ID) == "" || meta.ID != resolved.ID || meta.Version <= 0 {
		return nil, fmt.Errorf("%w: Confluence page identity is not reconciled", domain.ErrCheckFailed)
	}
	if opts.ExpectedPageVersion > 0 && opts.ExpectedPageVersion != meta.Version {
		return nil, &ConfluencePageVersionMismatchError{Expected: opts.ExpectedPageVersion, Current: meta.Version}
	}
	return s.attachmentInventoryForParent(ctx, meta.ID, meta.Version, opts)
}

// attachmentInventoryForParent binds an inventory to a page snapshot already
// reconciled by the caller. Complete pull uses it to avoid a second metadata
// read whose page bytes could differ from the native substrate being staged.
func (s *ConfluenceService) attachmentInventoryForParent(ctx context.Context, pageID string, pageVersion int, opts ConfluenceAttachmentInventoryOpts) (*ConfluenceAttachmentInventoryResult, error) {
	if strings.TrimSpace(pageID) == "" || pageVersion <= 0 ||
		opts.ExpectedPageVersion > 0 && opts.ExpectedPageVersion != pageVersion {
		return nil, fmt.Errorf("%w: Confluence attachment parent is not reconciled", domain.ErrCheckFailed)
	}
	var err error
	inventory := domain.AttachmentInventory{PartialReason: domain.AttachmentPartialLegacyUnqualified}
	if opts.MaxPages > 0 || opts.MaxItems > 0 {
		if err := domain.ValidateAttachmentReadOptions(domain.AttachmentReadOptions{MaxPages: opts.MaxPages, MaxItems: opts.MaxItems}); err != nil {
			return nil, err
		}
		bounded, ok := s.store.(domain.BoundedQualifiedAttachmentLister)
		if !ok {
			return nil, fmt.Errorf("%w: backend cannot enforce explicit attachment bounds", domain.ErrCheckFailed)
		}
		inventory, err = bounded.ListAttachmentsQualifiedBounded(ctx, pageID, domain.AttachmentReadOptions{MaxPages: opts.MaxPages, MaxItems: opts.MaxItems})
	} else if qualified, ok := s.store.(domain.QualifiedAttachmentLister); ok {
		inventory, err = qualified.ListAttachmentsQualified(ctx, pageID)
	} else {
		// A legacy store proves nothing about exhaustion, so the inventory stays
		// partial rather than being promoted to complete evidence.
		inventory.Attachments, err = s.store.ListAttachments(ctx, pageID)
		if inventory.Attachments == nil {
			inventory.Attachments = []domain.Attachment{}
		}
	}
	if err != nil {
		return nil, err
	}
	if err := validateConfluenceAttachmentInventory(inventory); err != nil {
		return nil, err
	}
	return &ConfluenceAttachmentInventoryResult{
		SchemaVersion: confluenceAttachmentInventorySchemaVersion,
		PageID:        pageID, PageVersion: pageVersion,
		Count: len(inventory.Attachments), Complete: inventory.Complete,
		PartialReason: inventory.PartialReason, Attachments: inventory.Attachments,
	}, nil
}

func validateConfluenceAttachmentInventory(inventory domain.AttachmentInventory) error {
	if inventory.Attachments == nil {
		return fmt.Errorf("%w: Confluence attachment inventory is unavailable", domain.ErrCheckFailed)
	}
	if inventory.Complete && inventory.PartialReason != "" {
		return fmt.Errorf("%w: complete Confluence attachment inventory has a partial reason", domain.ErrCheckFailed)
	}
	if !inventory.Complete && !domain.ValidAttachmentPartialReason(inventory.PartialReason) {
		return fmt.Errorf("%w: partial Confluence attachment inventory has no recognized reason", domain.ErrCheckFailed)
	}
	seen := make(map[string]struct{}, len(inventory.Attachments))
	for _, attachment := range inventory.Attachments {
		if strings.TrimSpace(attachment.ID) == "" {
			return fmt.Errorf("%w: Confluence attachment inventory contains an empty attachment id", domain.ErrCheckFailed)
		}
		if _, exists := seen[attachment.ID]; exists {
			return fmt.Errorf("%w: Confluence attachment inventory contains a duplicate attachment id", domain.ErrCheckFailed)
		}
		seen[attachment.ID] = struct{}{}
		// Older listing-only ports can truthfully expose an attachment identity
		// without download-selector metadata. Keep that inventory compatibility:
		// the body-capture path independently requires a positive version before
		// it issues either a selector revalidation or a binary request.
		if attachment.FileSize < 0 || attachment.Version < 0 || inventory.Complete && attachment.Version == 0 {
			return fmt.Errorf("%w: Confluence attachment inventory contains a negative size or invalid version", domain.ErrCheckFailed)
		}
	}
	return nil
}

// ConfluenceAttachmentView is the sanitized per-attachment projection. It is a
// separate type from domain.Attachment on purpose: the backend record also
// carries an author-authored comment and a download path, and neither may reach
// a typed agent transport. Title is untrusted backend evidence, never an
// instruction.
type ConfluenceAttachmentView struct {
	ID        string `json:"id" jsonschema:"stable attachment content id"`
	Title     string `json:"title" jsonschema:"attachment filename as untrusted backend evidence"`
	MediaType string `json:"media_type,omitempty" jsonschema:"backend-reported media type"`
	FileSize  int64  `json:"file_size" jsonschema:"attachment size in bytes as reported by the backend"`
	Version   int    `json:"version" jsonschema:"attachment version number"`
}

// ConfluenceAttachmentInventoryView is the sanitized inventory projection: page
// identity, the bound page version, counts, completeness, and metadata-only
// attachment identity. It carries no page title, URL, attachment comment,
// download path, or attachment bytes.
type ConfluenceAttachmentInventoryView struct {
	SchemaVersion int                        `json:"schema_version"`
	PageID        string                     `json:"page_id"`
	PageVersion   int                        `json:"page_version"`
	Count         int                        `json:"count"`
	Complete      bool                       `json:"complete"`
	PartialReason string                     `json:"partial_reason,omitempty"`
	Attachments   []ConfluenceAttachmentView `json:"attachments"`
}

// ProjectConfluenceAttachmentInventory copies the qualified inventory into its
// sanitized projection field by field. Projecting explicitly (rather than
// clearing fields on the source record) keeps a future domain.Attachment field
// from silently reaching a client.
func ProjectConfluenceAttachmentInventory(result *ConfluenceAttachmentInventoryResult) *ConfluenceAttachmentInventoryView {
	if result == nil {
		return nil
	}
	attachments := make([]ConfluenceAttachmentView, 0, len(result.Attachments))
	for _, attachment := range result.Attachments {
		attachments = append(attachments, ConfluenceAttachmentView{
			ID: attachment.ID, Title: attachment.Title, MediaType: attachment.MediaType,
			FileSize: attachment.FileSize, Version: attachment.Version,
		})
	}
	return &ConfluenceAttachmentInventoryView{
		SchemaVersion: result.SchemaVersion, PageID: result.PageID, PageVersion: result.PageVersion,
		Count: result.Count, Complete: result.Complete, PartialReason: result.PartialReason,
		Attachments: attachments,
	}
}
