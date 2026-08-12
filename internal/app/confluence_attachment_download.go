package app

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const confluenceAttachmentDownloadSchemaVersion = 1

const (
	ConfluenceAttachmentSelectorLatest  = "page_filename_latest"
	ConfluenceAttachmentSelectorVersion = "page_filename_attachment_version"
)

// ConfluenceAttachmentDownloadResult states the deliberately weaker identity
// boundary of the documented Server/Data Center download route. ATL binds the
// request to a resolved page id and filename. A positive version adds that
// selector; zero remains floating latest. The backend offers no ID-bound binary
// GET and this operation performs no immediate inventory revalidation.
type ConfluenceAttachmentDownloadResult struct {
	SchemaVersion              int    `json:"schema_version"`
	PageID                     string `json:"page_id"`
	Name                       string `json:"name"`
	OutputName                 string `json:"output_name"`
	RequestedAttachmentVersion int    `json:"requested_attachment_version"`
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

// DownloadAttachmentKnownPage exposes the selector and non-exact identity
// facts alongside the written path. It never claims that page version or
// attachment id was checked immediately before bytes were read.
func (s *ConfluenceService) DownloadAttachmentKnownPage(ctx context.Context, pageID, filename string, version int, outDir string) (*ConfluenceAttachmentDownloadResult, error) {
	if version < 0 {
		return nil, fmt.Errorf("%w: attachment version must be non-negative", domain.ErrUsage)
	}
	resolved, err := s.ResolvePageReference(ctx, pageID)
	if err != nil {
		return nil, err
	}
	ctx = resolved.Context(ctx)
	pageID = resolved.ID
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
	rc, err := s.store.DownloadAttachment(ctx, pageID, filename, version)
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
		Selector: selector, Path: p,
	}, nil
}
