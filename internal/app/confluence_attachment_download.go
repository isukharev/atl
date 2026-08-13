package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const confluenceAttachmentDownloadSchemaVersion = 1

const (
	confluenceAttachmentDownloadMaxRequests      = 5
	confluenceAttachmentDownloadMaxResponseBytes = 2 << 20
	confluenceAttachmentDownloadMetadataDeadline = 15 * time.Second
	confluenceAttachmentDownloadBinaryAttempts   = 5
)

const (
	ConfluenceAttachmentDownloadDefaultMaxBytes  int64 = 64 << 20
	ConfluenceAttachmentDownloadMaxBytes         int64 = 1 << 30
	ConfluenceAttachmentDownloadMaxFilenameBytes       = 255
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
	ObservedFileSize           int64  `json:"observed_file_size"`
	MaxBytes                   int64  `json:"max_bytes"`
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
	result, err := s.DownloadAttachmentKnownPageWithOptions(ctx, pageID, filename, version, outDir, ConfluenceAttachmentDownloadOptions{})
	if err != nil {
		return "", err
	}
	return result.Path, nil
}

// DownloadAttachmentKnownPage exposes the revalidated selector and deliberately
// non-exact binary identity facts alongside the written path.
func (s *ConfluenceService) DownloadAttachmentKnownPage(ctx context.Context, pageID, filename string, version int, outDir string) (*ConfluenceAttachmentDownloadResult, error) {
	return s.DownloadAttachmentKnownPageWithOptions(ctx, pageID, filename, version, outDir, ConfluenceAttachmentDownloadOptions{})
}

type ConfluenceAttachmentDownloadOptions struct {
	MaxBytes int64
}

func ValidConfluenceAttachmentDownloadFilename(filename string) bool {
	return strings.TrimSpace(filename) != "" && utf8.ValidString(filename) && len(filename) <= ConfluenceAttachmentDownloadMaxFilenameBytes
}

func NormalizeConfluenceAttachmentDownloadOptions(options ConfluenceAttachmentDownloadOptions) (ConfluenceAttachmentDownloadOptions, error) {
	if options.MaxBytes == 0 {
		options.MaxBytes = ConfluenceAttachmentDownloadDefaultMaxBytes
	}
	if options.MaxBytes < 1 || options.MaxBytes > ConfluenceAttachmentDownloadMaxBytes {
		return options, fmt.Errorf("%w: --max-bytes must be between 1 and %d", domain.ErrUsage, ConfluenceAttachmentDownloadMaxBytes)
	}
	return options, nil
}

// DownloadAttachmentKnownPageWithOptions admits a version-specific declared
// size before opening the binary route or creating its destination directory,
// then writes only a body of exactly that length.
func (s *ConfluenceService) DownloadAttachmentKnownPageWithOptions(ctx context.Context, pageID, filename string, version int, outDir string, options ConfluenceAttachmentDownloadOptions) (*ConfluenceAttachmentDownloadResult, error) {
	options, err := NormalizeConfluenceAttachmentDownloadOptions(options)
	if err != nil {
		return nil, err
	}
	if version < 0 {
		return nil, fmt.Errorf("%w: attachment version must be non-negative", domain.ErrUsage)
	}
	if !ValidConfluenceAttachmentDownloadFilename(filename) {
		return nil, fmt.Errorf("%w: attachment filename must be nonblank valid UTF-8 and at most %d bytes", domain.ErrUsage, ConfluenceAttachmentDownloadMaxFilenameBytes)
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
	metadataBudget, err := domain.NewReadBudget(confluenceAttachmentDownloadMaxRequests, confluenceAttachmentDownloadMaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: attachment download revalidation budget is invalid", domain.ErrCheckFailed)
	}
	metadataCtx, cancel := context.WithTimeout(domain.WithReadBudget(ctx, metadataBudget), confluenceAttachmentDownloadMetadataDeadline)
	resolved, err := s.ResolvePageReference(metadataCtx, pageID)
	if err != nil {
		cancel()
		return nil, err
	}
	ctx = resolved.Context(ctx)
	binaryCtx := ctx
	metadataCtx = resolved.Context(metadataCtx)
	pageID = resolved.ID
	revalidator, ok := s.store.(domain.QualifiedConfluenceAttachmentDownloadRevalidator)
	if !ok {
		cancel()
		return nil, fmt.Errorf("%w: backend cannot revalidate attachment download selectors", domain.ErrCheckFailed)
	}
	evidence, err := revalidator.RevalidateAttachmentDownload(domain.WithSingleAttempt(metadataCtx), pageID, filename, version)
	cancel()
	if err != nil {
		return nil, err
	}
	if evidence.PageID != pageID || evidence.Filename != filename || !domain.ValidConfluenceReadID(evidence.AttachmentID) || evidence.AttachmentID == pageID || evidence.Version <= 0 || evidence.FileSize < 0 ||
		(version > 0 && evidence.Version != version) {
		return nil, fmt.Errorf("%w: attachment download selector revalidation is inconsistent", domain.ErrCheckFailed)
	}
	if evidence.FileSize > options.MaxBytes {
		return nil, fmt.Errorf("%w: %w: attachment body exceeds max_bytes", domain.ErrCheckFailed, domain.ErrOutputLimit)
	}
	binaryBudget, err := domain.NewReadBudget(confluenceAttachmentDownloadBinaryAttempts, options.MaxBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: attachment binary request budget is invalid", domain.ErrCheckFailed)
	}
	binaryCtx = domain.WithNoReplayRetries(domain.WithReadBudget(binaryCtx, binaryBudget))
	// Always download the positive version observed by the immediate metadata
	// check. In particular, caller version 0 never remains a floating GET.
	rc, err := s.store.DownloadAttachment(binaryCtx, pageID, filename, evidence.Version)
	if err != nil {
		return nil, err // fail before MkdirAll: a 404 must not leave an empty outDir
	}
	reader := &exactAttachmentDownloadReader{ctx: binaryCtx, source: rc, remaining: evidence.FileSize}
	defer reader.abort()
	if err := safepath.MkdirAllWithin(outDir, outDir, 0o755); err != nil {
		return nil, err
	}
	if _, err := safepath.WriteReaderAtomicWithin(outDir, p, reader, 0o644); err != nil {
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
		ObservedFileSize: evidence.FileSize, MaxBytes: options.MaxBytes,
		Selector: selector, AttachmentIDBound: false, IdentityRevalidated: true, PageVersionGated: false, Path: p,
	}, nil
}

type exactAttachmentDownloadReader struct {
	ctx       context.Context
	source    io.ReadCloser
	remaining int64
	terminal  error
	closed    bool
}

func (r *exactAttachmentDownloadReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if r.terminal != nil {
		return 0, r.terminal
	}
	if err := r.ctx.Err(); err != nil {
		return 0, r.finish(err)
	}
	if r.remaining > 0 {
		limit := int64(len(buffer))
		if limit > r.remaining {
			limit = r.remaining
		}
		n, err := r.source.Read(buffer[:limit])
		r.remaining -= int64(n)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if r.remaining == 0 && n > 0 {
					err = nil
				} else {
					err = fmt.Errorf("%w: attachment body is shorter than declared size", domain.ErrCheckFailed)
				}
			} else {
				err = fmt.Errorf("%w: attachment body read failed", domain.ErrCheckFailed)
			}
			if err != nil {
				return n, r.finish(err)
			}
		}
		if n == 0 {
			return 0, r.finish(fmt.Errorf("%w: attachment body made no progress", domain.ErrCheckFailed))
		}
		return n, nil
	}
	var probe [1]byte
	n, err := r.source.Read(probe[:])
	if n > 0 {
		return 0, r.finish(fmt.Errorf("%w: attachment body is longer than declared size", domain.ErrCheckFailed))
	}
	if err == nil {
		return 0, r.finish(fmt.Errorf("%w: attachment body EOF probe made no progress", domain.ErrCheckFailed))
	}
	if err != nil && !errors.Is(err, io.EOF) {
		if errors.Is(err, domain.ErrReadResponseBudgetExhausted) {
			err = fmt.Errorf("%w: attachment body is longer than declared size", domain.ErrCheckFailed)
		} else {
			err = fmt.Errorf("%w: attachment body EOF probe failed", domain.ErrCheckFailed)
		}
		return 0, r.finish(err)
	}
	if err := r.ctx.Err(); err != nil {
		return 0, r.finish(err)
	}
	return 0, r.finish(nil)
}

func (r *exactAttachmentDownloadReader) finish(readErr error) error {
	if r.terminal != nil {
		return r.terminal
	}
	if !r.closed {
		r.closed = true
		if closeErr := r.source.Close(); closeErr != nil {
			readErr = errors.Join(readErr, fmt.Errorf("%w: attachment body close failed", domain.ErrCheckFailed))
		}
	}
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		readErr = errors.Join(readErr, ctxErr)
	}
	if readErr == nil {
		r.terminal = io.EOF
	} else {
		r.terminal = readErr
	}
	return r.terminal
}

func (r *exactAttachmentDownloadReader) abort() {
	if !r.closed {
		r.closed = true
		_ = r.source.Close()
	}
}
