package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const (
	ConfluenceAttachmentDiscoverySchemaVersion = 1

	ConfluenceAttachmentDiscoveryMaxItems         = 10_000
	ConfluenceAttachmentDiscoveryMaxRequests      = 100
	ConfluenceAttachmentDiscoveryMaxResponseBytes = 256 << 20
	ConfluenceAttachmentDiscoveryMaxDeadline      = 10 * time.Minute

	confluenceAttachmentDiscoveryMaxCQLBytes    = 16 << 10
	confluenceAttachmentDiscoveryMaxSpaceBytes  = 255
	confluenceAttachmentDiscoveryMaxCursorBytes = 2048

	ConfluenceAttachmentDiscoveryComplete         = "complete"
	ConfluenceAttachmentDiscoveryPartial          = "partial"
	ConfluenceAttachmentDiscoveryFailed           = "failed"
	ConfluenceAttachmentDiscoveryReadFailed       = "read_failed"
	ConfluenceAttachmentDiscoveryValidationFailed = "validation_failed"
)

type ConfluenceAttachmentDiscoveryOpts struct {
	Space            string
	CQL              string
	Cursor           string
	MaxItems         int
	MaxRequests      int
	MaxResponseBytes int64
	Deadline         time.Duration
}

type ConfluenceAttachmentDiscoveryBounds struct {
	MaxItems          int   `json:"max_items"`
	MaxRequests       int   `json:"max_requests"`
	MaxResponseBytes  int64 `json:"max_response_bytes"`
	DeadlineMillis    int64 `json:"deadline_ms"`
	RequestsUsed      int   `json:"requests_used"`
	ResponseBytesUsed int64 `json:"response_bytes_used"`
}

// ConfluenceAttachmentDiscoveryResult is a metadata-only live search prefix.
// NextCursor is a query/scope-bound offset, not a snapshot continuation.
type ConfluenceAttachmentDiscoveryResult struct {
	SchemaVersion int                                   `json:"schema_version"`
	Qualification string                                `json:"qualification"`
	Complete      bool                                  `json:"complete"`
	Reason        string                                `json:"reason,omitempty"`
	Consistency   string                                `json:"consistency"`
	ScopeSHA256   string                                `json:"scope_sha256"`
	StartOffset   int                                   `json:"start_offset"`
	NextCursor    string                                `json:"next_cursor,omitempty"`
	Count         int                                   `json:"count"`
	TotalSize     *int                                  `json:"total_size,omitempty"`
	Bounds        ConfluenceAttachmentDiscoveryBounds   `json:"bounds"`
	Attachments   []domain.ConfluenceAttachmentMetadata `json:"attachments"`
}

type confluenceAttachmentDiscoveryCursor struct {
	SchemaVersion int    `json:"schema_version"`
	ScopeSHA256   string `json:"scope_sha256"`
	Start         int    `json:"start"`
}

type confluenceAttachmentDiscoveryScope struct {
	Backend string `json:"backend"`
	Space   string `json:"space"`
	CQL     string `json:"cql"`
}

func NormalizeConfluenceAttachmentDiscoveryOpts(opts ConfluenceAttachmentDiscoveryOpts) (ConfluenceAttachmentDiscoveryOpts, error) {
	opts.Space = strings.TrimSpace(opts.Space)
	opts.CQL = strings.TrimSpace(opts.CQL)
	if !utf8.ValidString(opts.Space) || len(opts.Space) > confluenceAttachmentDiscoveryMaxSpaceBytes {
		return opts, fmt.Errorf("%w: Confluence attachment discovery space is invalid or exceeds %d bytes", domain.ErrUsage, confluenceAttachmentDiscoveryMaxSpaceBytes)
	}
	if !utf8.ValidString(opts.CQL) || len(opts.CQL) > confluenceAttachmentDiscoveryMaxCQLBytes || hasUnquotedCQLOrderBy(opts.CQL) {
		return opts, fmt.Errorf("%w: Confluence attachment discovery CQL is invalid, too large, or contains ORDER BY", domain.ErrUsage)
	}
	if opts.MaxItems < 1 || opts.MaxItems > ConfluenceAttachmentDiscoveryMaxItems {
		return opts, fmt.Errorf("%w: Confluence attachment discovery max items must be between 1 and %d", domain.ErrUsage, ConfluenceAttachmentDiscoveryMaxItems)
	}
	if opts.MaxRequests < 1 || opts.MaxRequests > ConfluenceAttachmentDiscoveryMaxRequests {
		return opts, fmt.Errorf("%w: Confluence attachment discovery max requests must be between 1 and %d", domain.ErrUsage, ConfluenceAttachmentDiscoveryMaxRequests)
	}
	if opts.MaxResponseBytes < 1 || opts.MaxResponseBytes > ConfluenceAttachmentDiscoveryMaxResponseBytes {
		return opts, fmt.Errorf("%w: Confluence attachment discovery max response bytes must be between 1 and %d", domain.ErrUsage, ConfluenceAttachmentDiscoveryMaxResponseBytes)
	}
	if opts.Deadline <= 0 || opts.Deadline > ConfluenceAttachmentDiscoveryMaxDeadline {
		return opts, fmt.Errorf("%w: Confluence attachment discovery deadline must be greater than zero and at most %s", domain.ErrUsage, ConfluenceAttachmentDiscoveryMaxDeadline)
	}
	if len(opts.Cursor) > confluenceAttachmentDiscoveryMaxCursorBytes {
		return opts, fmt.Errorf("%w: Confluence attachment discovery cursor exceeds %d bytes", domain.ErrUsage, confluenceAttachmentDiscoveryMaxCursorBytes)
	}
	return opts, nil
}

func (s *ConfluenceService) DiscoverAttachments(ctx context.Context, opts ConfluenceAttachmentDiscoveryOpts) (*ConfluenceAttachmentDiscoveryResult, error) {
	opts, err := NormalizeConfluenceAttachmentDiscoveryOpts(opts)
	if err != nil {
		return nil, err
	}
	scopeHash, err := confluenceAttachmentDiscoveryScopeHash(s.baseURL, opts.Space, opts.CQL)
	if err != nil {
		return nil, fmt.Errorf("%w: Confluence attachment discovery scope cannot be encoded", domain.ErrCheckFailed)
	}
	start, err := decodeConfluenceAttachmentDiscoveryCursor(opts.Cursor, scopeHash)
	if err != nil {
		return nil, err
	}
	budget, err := domain.NewReadBudget(opts.MaxRequests, opts.MaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: Confluence attachment discovery read budget is invalid", domain.ErrCheckFailed)
	}
	readCtx, cancel := context.WithTimeout(ctx, opts.Deadline)
	defer cancel()
	readCtx = domain.WithSingleAttempt(domain.WithReadBudget(readCtx, budget))

	page := domain.ConfluenceAttachmentDiscoveryPage{
		Attachments: []domain.ConfluenceAttachmentMetadata{}, Start: start,
		Consistency: domain.ConfluenceAttachmentDiscoveryConsistencyLiveUnproven,
	}
	discoverer, ok := s.store.(domain.QualifiedConfluenceAttachmentDiscoverer)
	if !ok {
		err = fmt.Errorf("%w: backend cannot provide qualified Confluence attachment discovery", domain.ErrCheckFailed)
	} else {
		page, err = discoverer.DiscoverAttachmentsQualified(readCtx, domain.ConfluenceAttachmentDiscoveryRequest{
			Space: opts.Space, CQL: opts.CQL, Start: start, MaxItems: opts.MaxItems,
		})
	}
	failureReason := ""
	if err != nil {
		failureReason = ConfluenceAttachmentDiscoveryReadFailed
		page = failedConfluenceAttachmentDiscoveryPage(start)
	} else if validationErr := validateConfluenceAttachmentDiscoveryPage(page, start, opts.MaxItems); validationErr != nil {
		err = validationErr
		failureReason = ConfluenceAttachmentDiscoveryValidationFailed
		page = failedConfluenceAttachmentDiscoveryPage(start)
	}
	usage := budget.Usage()
	result := &ConfluenceAttachmentDiscoveryResult{
		SchemaVersion: ConfluenceAttachmentDiscoverySchemaVersion,
		Qualification: ConfluenceAttachmentDiscoveryPartial,
		Consistency:   domain.ConfluenceAttachmentDiscoveryConsistencyLiveUnproven,
		ScopeSHA256:   scopeHash, StartOffset: start, Count: len(page.Attachments), TotalSize: page.TotalSize,
		Bounds: ConfluenceAttachmentDiscoveryBounds{
			MaxItems: opts.MaxItems, MaxRequests: opts.MaxRequests, MaxResponseBytes: opts.MaxResponseBytes,
			DeadlineMillis: opts.Deadline.Milliseconds(), RequestsUsed: usage.Attempts, ResponseBytesUsed: usage.ResponseBytes,
		},
		Attachments: page.Attachments,
	}
	if err != nil {
		result.Qualification = ConfluenceAttachmentDiscoveryFailed
		result.Reason = failureReason
	} else if page.Complete {
		result.Qualification = ConfluenceAttachmentDiscoveryComplete
		result.Complete = true
	} else {
		result.Reason = page.PartialReason
	}
	if err == nil && page.NextStart != nil {
		result.NextCursor, _ = encodeConfluenceAttachmentDiscoveryCursor(scopeHash, *page.NextStart)
	}
	if validationErr := ValidateConfluenceAttachmentDiscoveryResult(result); validationErr != nil {
		if err != nil {
			return result, errors.Join(err, validationErr)
		}
		return result, validationErr
	}
	return result, err
}

func ValidateConfluenceAttachmentDiscoveryResult(result *ConfluenceAttachmentDiscoveryResult) error {
	if result == nil || result.SchemaVersion != ConfluenceAttachmentDiscoverySchemaVersion ||
		result.Attachments == nil || !validLowerSHA256(result.ScopeSHA256) || result.StartOffset < 0 ||
		result.Count != len(result.Attachments) || result.Consistency != domain.ConfluenceAttachmentDiscoveryConsistencyLiveUnproven {
		return fmt.Errorf("%w: Confluence attachment discovery result is unavailable or inconsistent", domain.ErrCheckFailed)
	}
	switch result.Qualification {
	case ConfluenceAttachmentDiscoveryComplete:
		if !result.Complete || result.Reason != "" || result.NextCursor != "" {
			return fmt.Errorf("%w: complete Confluence attachment discovery is not reconciled", domain.ErrCheckFailed)
		}
	case ConfluenceAttachmentDiscoveryPartial:
		if result.Complete || !domain.ValidConfluenceAttachmentDiscoveryPartialReason(result.Reason) || result.NextCursor == "" {
			return fmt.Errorf("%w: partial Confluence attachment discovery is not reconciled", domain.ErrCheckFailed)
		}
	case ConfluenceAttachmentDiscoveryFailed:
		if result.Complete || (result.Reason != ConfluenceAttachmentDiscoveryReadFailed && result.Reason != ConfluenceAttachmentDiscoveryValidationFailed) || result.NextCursor != "" {
			return fmt.Errorf("%w: failed Confluence attachment discovery is not reconciled", domain.ErrCheckFailed)
		}
	default:
		return fmt.Errorf("%w: Confluence attachment discovery qualification is invalid", domain.ErrCheckFailed)
	}
	if result.Bounds.MaxItems < 1 || result.Bounds.MaxItems > ConfluenceAttachmentDiscoveryMaxItems ||
		result.Bounds.MaxRequests < 1 || result.Bounds.MaxRequests > ConfluenceAttachmentDiscoveryMaxRequests ||
		result.Bounds.MaxResponseBytes < 1 || result.Bounds.MaxResponseBytes > ConfluenceAttachmentDiscoveryMaxResponseBytes ||
		result.Bounds.DeadlineMillis < 1 || result.Bounds.DeadlineMillis > ConfluenceAttachmentDiscoveryMaxDeadline.Milliseconds() ||
		result.Bounds.RequestsUsed < 0 || result.Bounds.RequestsUsed > result.Bounds.MaxRequests ||
		result.Bounds.ResponseBytesUsed < 0 || result.Bounds.ResponseBytesUsed > result.Bounds.MaxResponseBytes ||
		result.Count > result.Bounds.MaxItems {
		return fmt.Errorf("%w: Confluence attachment discovery bounds are inconsistent", domain.ErrCheckFailed)
	}
	if result.TotalSize != nil {
		end := result.StartOffset + result.Count
		if *result.TotalSize < 0 || end < result.StartOffset || end > *result.TotalSize || result.Complete && end != *result.TotalSize {
			return fmt.Errorf("%w: Confluence attachment discovery total is inconsistent", domain.ErrCheckFailed)
		}
	}
	seen := make(map[string]struct{}, len(result.Attachments))
	for _, item := range result.Attachments {
		if !validConfluenceAttachmentMetadata(item) {
			return fmt.Errorf("%w: Confluence attachment discovery contains invalid metadata", domain.ErrCheckFailed)
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return fmt.Errorf("%w: Confluence attachment discovery contains duplicate attachment ids", domain.ErrCheckFailed)
		}
		seen[item.ID] = struct{}{}
	}
	if result.NextCursor != "" {
		next, err := decodeConfluenceAttachmentDiscoveryCursor(result.NextCursor, result.ScopeSHA256)
		if err != nil || next != result.StartOffset+result.Count {
			return fmt.Errorf("%w: Confluence attachment discovery continuation is invalid", domain.ErrCheckFailed)
		}
	}
	return nil
}

func failedConfluenceAttachmentDiscoveryPage(start int) domain.ConfluenceAttachmentDiscoveryPage {
	return domain.ConfluenceAttachmentDiscoveryPage{
		Attachments: []domain.ConfluenceAttachmentMetadata{}, Start: start,
		Consistency: domain.ConfluenceAttachmentDiscoveryConsistencyLiveUnproven,
	}
}

func validateConfluenceAttachmentDiscoveryPage(page domain.ConfluenceAttachmentDiscoveryPage, start, maxItems int) error {
	if page.Attachments == nil || page.Start != start || len(page.Attachments) > maxItems ||
		page.Consistency != domain.ConfluenceAttachmentDiscoveryConsistencyLiveUnproven {
		return fmt.Errorf("%w: Confluence attachment discovery page is unavailable or inconsistent", domain.ErrCheckFailed)
	}
	end := start + len(page.Attachments)
	if end < start {
		return fmt.Errorf("%w: Confluence attachment discovery page coordinates overflow", domain.ErrCheckFailed)
	}
	if page.Complete {
		if page.PartialReason != "" || page.NextStart != nil || page.TotalSize == nil || *page.TotalSize != end {
			return fmt.Errorf("%w: complete Confluence attachment discovery page is not terminal", domain.ErrCheckFailed)
		}
	} else if !domain.ValidConfluenceAttachmentDiscoveryPartialReason(page.PartialReason) || page.NextStart == nil || *page.NextStart != end {
		return fmt.Errorf("%w: partial Confluence attachment discovery page has no safe continuation", domain.ErrCheckFailed)
	}
	if page.TotalSize != nil && (*page.TotalSize < 0 || end > *page.TotalSize) {
		return fmt.Errorf("%w: Confluence attachment discovery page total is inconsistent", domain.ErrCheckFailed)
	}
	seen := make(map[string]struct{}, len(page.Attachments))
	for _, item := range page.Attachments {
		if !validConfluenceAttachmentMetadata(item) {
			return fmt.Errorf("%w: Confluence attachment discovery page contains invalid metadata", domain.ErrCheckFailed)
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return fmt.Errorf("%w: Confluence attachment discovery page repeats attachment identity", domain.ErrCheckFailed)
		}
		seen[item.ID] = struct{}{}
	}
	return nil
}

func validConfluenceAttachmentMetadata(item domain.ConfluenceAttachmentMetadata) bool {
	return domain.ValidConfluenceContentID(item.ID) && strings.TrimSpace(item.Title) != "" && item.Type == "attachment" && item.Version > 0 &&
		domain.ValidConfluenceContentID(item.ContainerID) && (item.ContainerType == "page" || item.ContainerType == "blogpost") &&
		item.ContainerVersion > 0 && strings.TrimSpace(item.Space) != "" && strings.TrimSpace(item.MediaType) != "" && item.FileSize >= 0
}

func confluenceAttachmentDiscoveryScopeHash(backend, space, cql string) (string, error) {
	encoded, err := json.Marshal(confluenceAttachmentDiscoveryScope{Backend: strings.TrimRight(backend, "/"), Space: space, CQL: cql})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func encodeConfluenceAttachmentDiscoveryCursor(scopeHash string, start int) (string, error) {
	if !validLowerSHA256(scopeHash) || start < 0 {
		return "", fmt.Errorf("invalid Confluence attachment discovery continuation")
	}
	encoded, err := json.Marshal(confluenceAttachmentDiscoveryCursor{
		SchemaVersion: ConfluenceAttachmentDiscoverySchemaVersion, ScopeSHA256: scopeHash, Start: start,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeConfluenceAttachmentDiscoveryCursor(token, scopeHash string) (int, error) {
	if token == "" {
		return 0, nil
	}
	if len(token) > confluenceAttachmentDiscoveryMaxCursorBytes {
		return 0, fmt.Errorf("%w: Confluence attachment discovery cursor is too large", domain.ErrUsage)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != token {
		return 0, fmt.Errorf("%w: Confluence attachment discovery cursor is invalid", domain.ErrUsage)
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var cursor confluenceAttachmentDiscoveryCursor
	if err := decoder.Decode(&cursor); err != nil {
		return 0, fmt.Errorf("%w: Confluence attachment discovery cursor is invalid", domain.ErrUsage)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF || cursor.SchemaVersion != ConfluenceAttachmentDiscoverySchemaVersion ||
		cursor.ScopeSHA256 != scopeHash || !validLowerSHA256(cursor.ScopeSHA256) || cursor.Start < 0 {
		return 0, fmt.Errorf("%w: Confluence attachment discovery cursor does not match this query and scope", domain.ErrUsage)
	}
	return cursor.Start, nil
}

func validLowerSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
