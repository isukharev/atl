package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const (
	confluenceTreeSchemaVersion          = 1
	confluenceTreeDefaultMaxItems        = 2_000
	confluenceTreeMaxItems               = 2_000
	confluenceTreeDefaultMaxScannedItems = 20_000
	confluenceTreeMaxScannedItems        = 20_000
	confluenceTreeDefaultMaxRequests     = 100
	confluenceTreeMaxRequests            = 100
	confluenceTreeDefaultResponseBytes   = 64 << 20
	confluenceTreeMaxResponseBytes       = 256 << 20
	confluenceTreeDefaultDeadline        = 2 * time.Minute
	confluenceTreeMaxDeadline            = 10 * time.Minute
)

const (
	ConfluenceTreeDefaultMaxItems        = confluenceTreeDefaultMaxItems
	ConfluenceTreeMaxItems               = confluenceTreeMaxItems
	ConfluenceTreeDefaultMaxScannedItems = confluenceTreeDefaultMaxScannedItems
	ConfluenceTreeMaxScannedItems        = confluenceTreeMaxScannedItems
	ConfluenceTreeDefaultMaxRequests     = confluenceTreeDefaultMaxRequests
	ConfluenceTreeMaxRequests            = confluenceTreeMaxRequests
	ConfluenceTreeDefaultResponseBytes   = confluenceTreeDefaultResponseBytes
	ConfluenceTreeMaxResponseBytes       = confluenceTreeMaxResponseBytes
	ConfluenceTreeDefaultDeadline        = confluenceTreeDefaultDeadline
	ConfluenceTreeMaxDeadline            = confluenceTreeMaxDeadline
)

// ConfluenceTreeOpts selects and bounds one hierarchy traversal. Zero values
// select documented finite defaults.
type ConfluenceTreeOpts struct {
	Space            string
	Depth            int
	MaxItems         int
	MaxScannedItems  int
	MaxRequests      int
	MaxResponseBytes int64
	Deadline         time.Duration
}

// ConfluenceTreeBounds reports both caller-selected limits and physical usage.
type ConfluenceTreeBounds struct {
	MaxItems          int   `json:"max_items"`
	MaxScannedItems   int   `json:"max_scanned_items"`
	MaxRequests       int   `json:"max_requests"`
	MaxResponseBytes  int64 `json:"max_response_bytes"`
	DeadlineMillis    int64 `json:"deadline_ms"`
	ScannedItems      int   `json:"scanned_items"`
	RequestsUsed      int   `json:"requests_used"`
	ResponseBytesUsed int64 `json:"response_bytes_used"`
}

// ConfluenceTreeResult is the qualified, bounded tree envelope.
type ConfluenceTreeResult struct {
	SchemaVersion int                  `json:"schema_version"`
	Space         string               `json:"space"`
	Depth         int                  `json:"depth"`
	Count         int                  `json:"count"`
	Complete      bool                 `json:"complete"`
	Truncated     bool                 `json:"truncated,omitempty"`
	PartialReason string               `json:"partial_reason,omitempty"`
	Consistency   string               `json:"consistency"`
	Bounds        ConfluenceTreeBounds `json:"bounds"`
	Pages         []domain.PageRef     `json:"pages"`
}

// NormalizeConfluenceTreeOpts applies finite defaults and refuses values above
// the reviewed adapter and command ceilings.
func NormalizeConfluenceTreeOpts(opts ConfluenceTreeOpts) (ConfluenceTreeOpts, error) {
	setInt := func(value, defaultValue, maximum int, name string) (int, error) {
		if value == 0 {
			return defaultValue, nil
		}
		if value < 0 || value > maximum {
			return 0, fmt.Errorf("%w: Confluence tree %s must be between 1 and %d", domain.ErrUsage, name, maximum)
		}
		return value, nil
	}
	var err error
	if strings.TrimSpace(opts.Space) == "" {
		return opts, fmt.Errorf("%w: Confluence tree space is required", domain.ErrUsage)
	}
	if opts.Depth < 0 {
		return opts, fmt.Errorf("%w: Confluence tree depth must be non-negative", domain.ErrUsage)
	}
	if opts.MaxItems, err = setInt(opts.MaxItems, confluenceTreeDefaultMaxItems, confluenceTreeMaxItems, "max items"); err != nil {
		return opts, err
	}
	if opts.MaxScannedItems, err = setInt(opts.MaxScannedItems, confluenceTreeDefaultMaxScannedItems, confluenceTreeMaxScannedItems, "max scanned items"); err != nil {
		return opts, err
	}
	if opts.MaxRequests, err = setInt(opts.MaxRequests, confluenceTreeDefaultMaxRequests, confluenceTreeMaxRequests, "max requests"); err != nil {
		return opts, err
	}
	if opts.MaxResponseBytes == 0 {
		opts.MaxResponseBytes = confluenceTreeDefaultResponseBytes
	} else if opts.MaxResponseBytes < 0 || opts.MaxResponseBytes > confluenceTreeMaxResponseBytes {
		return opts, fmt.Errorf("%w: Confluence tree max response bytes must be between 1 and %d", domain.ErrUsage, confluenceTreeMaxResponseBytes)
	}
	if opts.Deadline == 0 {
		opts.Deadline = confluenceTreeDefaultDeadline
	} else if opts.Deadline < 0 || opts.Deadline > confluenceTreeMaxDeadline {
		return opts, fmt.Errorf("%w: Confluence tree deadline must be greater than zero and at most %s", domain.ErrUsage, confluenceTreeMaxDeadline)
	}
	return opts, nil
}

// TreeQualified installs the physical transport budget and wall-clock deadline
// before entering the adapter. Budget exhaustion returns a qualified prefix;
// ordinary backend errors remain errors and do not masquerade as partial data.
func (s *ConfluenceService) TreeQualified(ctx context.Context, opts ConfluenceTreeOpts) (*ConfluenceTreeResult, error) {
	opts, err := NormalizeConfluenceTreeOpts(opts)
	if err != nil {
		return nil, err
	}
	budget, err := domain.NewReadBudget(opts.MaxRequests, opts.MaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: Confluence tree read budget is invalid", domain.ErrCheckFailed)
	}
	readCtx, cancel := context.WithTimeout(ctx, opts.Deadline)
	defer cancel()
	readCtx = domain.WithSingleAttempt(domain.WithReadBudget(readCtx, budget))

	page := domain.ConfluenceTreePage{
		Pages: []domain.PageRef{}, Consistency: domain.ConfluenceTreeConsistencyLiveUnproven,
		PartialReason: domain.ConfluenceTreePartialLegacyUnqualified,
	}
	if reader, ok := s.store.(domain.QualifiedConfluenceTreeReader); ok {
		page, err = reader.TreeQualified(readCtx, domain.ConfluenceTreeRequest{
			Space: opts.Space, Depth: opts.Depth, MaxItems: opts.MaxItems, MaxScannedItems: opts.MaxScannedItems,
		})
	} else {
		var truncated bool
		page.Pages, truncated, err = s.store.Tree(readCtx, opts.Space, opts.Depth)
		if page.Pages == nil {
			page.Pages = []domain.PageRef{}
		}
		page.ScannedItems = len(page.Pages)
		if truncated {
			page.PartialReason = domain.ConfluenceTreePartialLegacyUnqualified
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrReadAttemptBudgetExhausted):
			page.PartialReason, err = domain.ConfluenceTreePartialRequestLimit, nil
		case errors.Is(err, domain.ErrReadResponseBudgetExhausted):
			page.PartialReason, err = domain.ConfluenceTreePartialResponseByteLimit, nil
		case errors.Is(readCtx.Err(), context.DeadlineExceeded):
			page.PartialReason, err = domain.ConfluenceTreePartialDeadline, nil
		}
	}
	if err != nil {
		return nil, err
	}
	if validationErr := validateConfluenceTreePage(page, opts); validationErr != nil {
		return nil, validationErr
	}
	usage := budget.Usage()
	return &ConfluenceTreeResult{
		SchemaVersion: confluenceTreeSchemaVersion, Space: opts.Space, Depth: opts.Depth,
		Count: len(page.Pages), Complete: page.Complete, Truncated: !page.Complete, PartialReason: page.PartialReason,
		Consistency: page.Consistency,
		Bounds: ConfluenceTreeBounds{
			MaxItems: opts.MaxItems, MaxScannedItems: opts.MaxScannedItems,
			MaxRequests: opts.MaxRequests, MaxResponseBytes: opts.MaxResponseBytes,
			DeadlineMillis: opts.Deadline.Milliseconds(), ScannedItems: page.ScannedItems,
			RequestsUsed: usage.Attempts, ResponseBytesUsed: usage.ResponseBytes,
		},
		Pages: page.Pages,
	}, nil
}

func validateConfluenceTreePage(page domain.ConfluenceTreePage, opts ConfluenceTreeOpts) error {
	if page.Pages == nil || page.Consistency != domain.ConfluenceTreeConsistencyLiveUnproven {
		return fmt.Errorf("%w: Confluence tree qualification is unavailable", domain.ErrCheckFailed)
	}
	if page.Complete && page.PartialReason != "" {
		return fmt.Errorf("%w: complete Confluence tree has a partial reason", domain.ErrCheckFailed)
	}
	if !page.Complete && !domain.ValidConfluenceTreePartialReason(page.PartialReason) {
		return fmt.Errorf("%w: partial Confluence tree has no recognized reason", domain.ErrCheckFailed)
	}
	if page.ScannedItems < len(page.Pages) {
		return fmt.Errorf("%w: Confluence tree scanned count is inconsistent", domain.ErrCheckFailed)
	}
	if len(page.Pages) > opts.MaxItems || page.ScannedItems > opts.MaxScannedItems {
		return fmt.Errorf("%w: Confluence tree exceeded a caller item bound", domain.ErrCheckFailed)
	}
	seen := make(map[string]struct{}, len(page.Pages))
	for _, ref := range page.Pages {
		if ref.ID == "" {
			return fmt.Errorf("%w: Confluence tree contains an empty page id", domain.ErrCheckFailed)
		}
		if _, duplicate := seen[ref.ID]; duplicate {
			return fmt.Errorf("%w: Confluence tree contains a duplicate page id", domain.ErrCheckFailed)
		}
		seen[ref.ID] = struct{}{}
	}
	return nil
}
