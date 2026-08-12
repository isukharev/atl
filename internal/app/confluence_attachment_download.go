package app

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const confluenceAttachmentDownloadSchemaVersion = 1

const (
	confluenceAttachmentDownloadMaxRequests      = 5
	confluenceAttachmentDownloadMaxResponseBytes = 2 << 20
	confluenceAttachmentDownloadMetadataDeadline = 15 * time.Second
)

const (
	ConfluenceAttachmentSelectorLatest  = "page_filename_latest"
	ConfluenceAttachmentSelectorVersion = "page_filename_attachment_version"
)

// ConfluenceAttachmentDownloadResult states the deliberately weaker identity
// boundary of the documented Server/Data Center download route. ATL
// immediately revalidates a resolved page id, exact caller filename, and
// positive attachment version, then uses that version in the filename-based
// binary GET. The backend offers no ID-bound binary GET, page CAS, or atomic
// metadata+bytes transaction.
type ConfluenceAttachmentDownloadResult struct {
	SchemaVersion              int    `json:"schema_version"`
	PageID                     string `json:"page_id"`
	Name                       string `json:"name"`
	OutputName                 string `json:"output_name"`
	RequestedAttachmentVersion int    `json:"requested_attachment_version"`
	ObservedAttachmentID       string `json:"observed_attachment_id"`
	ObservedAttachmentVersion  int    `json:"observed_attachment_version"`
	Selector                   string `json:"selector"`
	AttachmentIDBound          bool   `json:"attachment_id_bound"`
	IdentityRevalidated        bool   `json:"identity_revalidated"`
	PageVersionGated           bool   `json:"page_version_gated"`
	Path                       string `json:"path"`
}

// DownloadAttachment streams a page attachment by filename into outDir (an
// atomic write: an interrupted transfer never leaves a truncated file). It is
// retained as the path-only application compatibility surface.
func (s *ConfluenceService) DownloadAttachment(ctx context.Context, pageID, filename string, version int, outDir string) (string, error) {
	result, err := s.DownloadAttachmentKnownPage(ctx, pageID, filename, version, outDir)
	if err != nil {
		return "", err
	}
	return result.Path, nil
}

// DownloadAttachmentKnownPage exposes the revalidated selector and deliberately
// non-exact binary identity facts alongside the written path.
func (s *ConfluenceService) DownloadAttachmentKnownPage(ctx context.Context, pageID, filename string, version int, outDir string) (*ConfluenceAttachmentDownloadResult, error) {
	if version < 0 {
		return nil, fmt.Errorf("%w: attachment version must be non-negative", domain.ErrUsage)
	}
	if outDir == "" {
		outDir = "."
	}
	safeName, ok := safepath.Base(filename)
	if !ok {
		return nil, fmt.Errorf("%w: unsafe attachment filename %q", domain.ErrUsage, filename)
	}
	p := filepath.Join(outDir, safeName)
	if !safepath.Within(outDir, p) {
		return nil, fmt.Errorf("%w: attachment path would escape output directory", domain.ErrUsage)
	}
	budget, err := domain.NewReadBudget(confluenceAttachmentDownloadMaxRequests, confluenceAttachmentDownloadMaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: attachment download revalidation budget is invalid", domain.ErrCheckFailed)
	}
	ctx = domain.WithSingleAttempt(domain.WithReadBudget(ctx, budget))
	metadataCtx, cancel := context.WithTimeout(ctx, confluenceAttachmentDownloadMetadataDeadline)
	resolved, err := s.ResolvePageReference(metadataCtx, pageID)
	if err != nil {
		cancel()
		return nil, err
	}
	ctx = resolved.Context(ctx)
	metadataCtx = resolved.Context(metadataCtx)
	pageID = resolved.ID
	revalidator, ok := s.store.(domain.QualifiedConfluenceAttachmentDownloadRevalidator)
	if !ok {
		cancel()
		return nil, fmt.Errorf("%w: backend cannot revalidate attachment download selectors", domain.ErrCheckFailed)
	}
	evidence, err := revalidator.RevalidateAttachmentDownload(metadataCtx, pageID, filename, version)
	cancel()
	if err != nil {
		return nil, err
	}
	if evidence.PageID != pageID || evidence.Filename != filename || !domain.ValidConfluenceContentID(evidence.AttachmentID) || evidence.Version <= 0 ||
		(version > 0 && evidence.Version != version) {
		return nil, fmt.Errorf("%w: attachment download selector revalidation is inconsistent", domain.ErrCheckFailed)
	}
	// Always download the positive version observed by the immediate metadata
	// check. In particular, caller version 0 never remains a floating GET.
	rc, err := s.store.DownloadAttachment(ctx, pageID, filename, evidence.Version)
	if err != nil {
		return nil, err // fail before MkdirAll: a 404 must not leave an empty outDir
	}
	defer rc.Close()
	if err := safepath.MkdirAllWithin(outDir, outDir, 0o755); err != nil {
		return nil, err
	}
	if _, err := safepath.WriteReaderAtomicWithin(outDir, p, rc, 0o644); err != nil {
		return nil, err
	}
	selector := ConfluenceAttachmentSelectorLatest
	if version > 0 {
		selector = ConfluenceAttachmentSelectorVersion
	}
	return &ConfluenceAttachmentDownloadResult{
		SchemaVersion: confluenceAttachmentDownloadSchemaVersion,
		PageID:        pageID, Name: filename, OutputName: safeName, RequestedAttachmentVersion: version,
		ObservedAttachmentID: evidence.AttachmentID, ObservedAttachmentVersion: evidence.Version,
		Selector: selector, AttachmentIDBound: false, IdentityRevalidated: true, PageVersionGated: false, Path: p,
	}, nil
}
