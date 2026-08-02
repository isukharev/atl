package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const confluencePageTrashSchemaVersion = 1

type ConfluencePageTrashOpts struct {
	Apply                bool
	Confirm              string
	ExpectedVersion      int
	ExpectedProposalHash string
}

type ConfluencePageTrashResult struct {
	SchemaVersion   int    `json:"schema_version"`
	ID              string `json:"id"`
	Mode            string `json:"mode"`
	Status          string `json:"status"`
	Operation       string `json:"operation"`
	CurrentStatus   string `json:"current_status"`
	TargetStatus    string `json:"target_status"`
	ObservedState   string `json:"observed_state,omitempty"`
	CurrentVersion  int    `json:"current_version"`
	ExpectedVersion int    `json:"expected_version"`
	FinalVersion    int    `json:"final_version,omitempty"`
	BodySHA256      string `json:"body_sha256"`
	BodyBytes       int    `json:"body_bytes"`
	TitleSHA256     string `json:"title_sha256"`
	BackendSHA256   string `json:"backend_sha256"`
	ProposalHash    string `json:"proposal_hash"`
	Complete        bool   `json:"complete"`
	Reconciled      bool   `json:"reconciled,omitempty"`
	WriteAttempted  bool   `json:"write_attempted"`
	Warning         string `json:"warning"`
}

type confluencePageTrashSnapshot struct {
	id            string
	contentType   string
	status        string
	version       int
	bodySHA256    string
	bodyBytes     int
	titleSHA256   string
	space         string
	parent        string
	backendSHA256 string
}

type confluencePageTrashWriteError struct {
	message   string
	cause     error
	closed    bool
	ambiguous bool
}

func (e *confluencePageTrashWriteError) Error() string { return e.message }

func (e *confluencePageTrashWriteError) Unwrap() []error {
	if e == nil {
		return nil
	}
	causes := make([]error, 0, 2)
	if e.closed {
		causes = append(causes, domain.ErrCheckFailed)
	}
	if e.cause != nil {
		causes = append(causes, e.cause)
	}
	return causes
}

func (e *confluencePageTrashWriteError) DiagnosticAmbiguousWrite() bool {
	return e != nil && e.ambiguous
}

// TrashPageGuarded previews or applies one reviewed current-to-trashed page
// transition. Confluence exposes no delete-time version CAS, so apply performs
// a second exact state read immediately before one non-replayed DELETE and
// reconciles every possibly committed outcome from explicit status reads.
func (s *ConfluenceService) TrashPageGuarded(ctx context.Context, id string, opts ConfluencePageTrashOpts) (*ConfluencePageTrashResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("%w: page id is required", domain.ErrUsage)
	}
	if opts.Apply && opts.Confirm != "TRASH" {
		return nil, fmt.Errorf("%w: --confirm must be exactly TRASH with --apply", domain.ErrUsage)
	}
	if opts.Apply && opts.ExpectedVersion <= 0 {
		return nil, fmt.Errorf("%w: --expected-version is required with --apply; run the dry-run first", domain.ErrUsage)
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) == "" {
		return nil, fmt.Errorf("%w: --expected-proposal-hash is required with --apply; run the dry-run first", domain.ErrUsage)
	}

	initial, err := s.confluencePageTrashSnapshot(ctx, id)
	if err != nil {
		return nil, err
	}
	proposalHash := confluencePageTrashProposalHash(initial)
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	expectedVersion := opts.ExpectedVersion
	if expectedVersion <= 0 {
		expectedVersion = initial.version
	}
	result := &ConfluencePageTrashResult{
		SchemaVersion: confluencePageTrashSchemaVersion,
		ID:            id, Mode: mode, Status: "would_apply", Operation: "trash",
		CurrentStatus: initial.status, TargetStatus: "trashed", ObservedState: initial.status,
		CurrentVersion: initial.version, ExpectedVersion: expectedVersion,
		BodySHA256: initial.bodySHA256, BodyBytes: initial.bodyBytes,
		TitleSHA256: initial.titleSHA256, BackendSHA256: initial.backendSHA256,
		ProposalHash: proposalHash, Complete: true,
		Warning: "Confluence has no delete-time version CAS; apply revalidates immediately before one status=current DELETE and never replays it",
	}
	if opts.Apply && expectedVersion != initial.version {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: reviewed page version changed; run the dry-run again", domain.ErrCheckFailed)
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) != proposalHash {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: page trash proposal changed since review; run the dry-run again", domain.ErrCheckFailed)
	}
	if initial.status == "trashed" {
		result.Status = "already_satisfied"
		result.FinalVersion = initial.version
		return result, nil
	}
	if !opts.Apply {
		return result, nil
	}

	prewrite, err := s.confluencePageTrashSnapshot(ctx, id)
	if err != nil {
		result.Status = "blocked"
		result.Complete = false
		return result, &confluencePageTrashWriteError{
			message: "page trash proposal could not be revalidated immediately before the write",
			cause:   sanitizeConfluenceWriteCause(err), closed: true,
		}
	}
	if prewrite.status != "current" || confluencePageTrashProposalHash(prewrite) != proposalHash {
		result.Status = "blocked"
		result.ObservedState = prewrite.status
		return result, fmt.Errorf("%w: page state changed during trash preflight; run the dry-run again", domain.ErrCheckFailed)
	}

	result.WriteAttempted = true
	writeErr := s.store.DeletePage(domain.WithSingleAttempt(ctx), id)
	if writeErr != nil && definitiveWriteRejection(writeErr) && !errors.Is(writeErr, domain.ErrNotFound) {
		result.Status = "not_applied"
		result.ObservedState = "current"
		return result, &confluencePageTrashWriteError{
			message: "Confluence rejected the page trash operation; it was not applied",
			cause:   sanitizeConfluenceWriteCause(writeErr),
		}
	}

	readback, readbackErr := s.confluencePageTrashSnapshot(ctx, id)
	if readbackErr != nil {
		result.Status = "outcome_unknown"
		result.Complete = false
		if errors.Is(readbackErr, domain.ErrNotFound) {
			result.ObservedState = "not_found"
		} else {
			result.ObservedState = "unavailable"
		}
		return result, confluencePageTrashAmbiguousError(
			"page trash outcome is unknown; exact current/trashed readback failed; do not replay automatically",
			errors.Join(sanitizeConfluenceWriteCause(writeErr), sanitizeConfluenceWriteCause(readbackErr)),
		)
	}
	result.Reconciled = true
	result.ObservedState = readback.status
	result.FinalVersion = readback.version
	if confluencePageTrashMatches(prewrite, readback) {
		if writeErr == nil {
			result.Status = "applied"
		} else {
			result.Status = "recovered"
		}
		return result, nil
	}
	result.Status = "outcome_unknown"
	return result, confluencePageTrashAmbiguousError(
		"page trash outcome is unknown; exact readback differs from the reviewed page state; do not replay automatically",
		sanitizeConfluenceWriteCause(writeErr),
	)
}

func (s *ConfluenceService) confluencePageTrashSnapshot(ctx context.Context, id string) (confluencePageTrashSnapshot, error) {
	reader, ok := s.store.(domain.ConfluencePageStatusReader)
	if !ok {
		return confluencePageTrashSnapshot{}, fmt.Errorf("%w: Confluence backend does not expose exact current/trashed page reads", domain.ErrCheckFailed)
	}
	backendSHA256, err := backendid.OriginSHA256(s.baseURL)
	if err != nil {
		return confluencePageTrashSnapshot{}, fmt.Errorf("%w: invalid Confluence backend identity", domain.ErrCheckFailed)
	}
	read := func(status string) (*domain.Resource, error) {
		readCtx := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx))
		return reader.GetPageByStatus(readCtx, id, status, domain.PullOpts{Format: "csf"})
	}
	page, currentErr := read("current")
	status := "current"
	if currentErr != nil {
		if !errors.Is(currentErr, domain.ErrNotFound) {
			return confluencePageTrashSnapshot{}, currentErr
		}
		page, err = read("trashed")
		status = "trashed"
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return confluencePageTrashSnapshot{}, fmt.Errorf("%w: page is neither current nor visible in trash", domain.ErrNotFound)
			}
			return confluencePageTrashSnapshot{}, err
		}
	}
	if err := validateConfluencePageTrashRead(page, id, status); err != nil {
		return confluencePageTrashSnapshot{}, err
	}
	titleSum := sha256.Sum256([]byte(page.Title))
	return confluencePageTrashSnapshot{
		id: page.ID, contentType: page.Type, status: page.Status, version: page.Version,
		bodySHA256: mirror.Hash(page.Body), bodyBytes: len(page.Body),
		titleSHA256: hex.EncodeToString(titleSum[:]), space: page.SpaceKey,
		parent: page.Parent, backendSHA256: backendSHA256,
	}, nil
}

func validateConfluencePageTrashRead(page *domain.Resource, id, status string) error {
	if page == nil || page.ID != id || page.Type != "page" || page.Status != status || page.Version <= 0 || !page.BodyPresent {
		return fmt.Errorf("%w: exact %s page read omitted required identity, type, status, version, or native body", domain.ErrCheckFailed, status)
	}
	if strings.TrimSpace(page.Title) == "" || strings.TrimSpace(page.SpaceKey) == "" {
		return fmt.Errorf("%w: exact %s page read omitted title or space identity", domain.ErrCheckFailed, status)
	}
	if !page.AncestorsPresent || len(page.Ancestors) != len(page.AncestorIDs) {
		return fmt.Errorf("%w: exact %s page read omitted ancestor identity", domain.ErrCheckFailed, status)
	}
	for _, ancestorID := range page.AncestorIDs {
		if strings.TrimSpace(ancestorID) == "" {
			return fmt.Errorf("%w: exact %s page read contains an empty ancestor identity", domain.ErrCheckFailed, status)
		}
	}
	if len(page.AncestorIDs) == 0 {
		if page.Parent != "" {
			return fmt.Errorf("%w: exact %s top-level page read contains a contradictory parent", domain.ErrCheckFailed, status)
		}
	} else if page.Parent != page.AncestorIDs[len(page.AncestorIDs)-1] {
		return fmt.Errorf("%w: exact %s page read contains contradictory parent and ancestor identities", domain.ErrCheckFailed, status)
	}
	return nil
}

func confluencePageTrashProposalHash(snapshot confluencePageTrashSnapshot) string {
	canonical, _ := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		Operation     string `json:"operation"`
		BackendSHA256 string `json:"backend_sha256"`
		ID            string `json:"id"`
		ContentType   string `json:"content_type"`
		Status        string `json:"status"`
		TargetStatus  string `json:"target_status"`
		Version       int    `json:"version"`
		BodySHA256    string `json:"body_sha256"`
		BodyBytes     int    `json:"body_bytes"`
		TitleSHA256   string `json:"title_sha256"`
		Space         string `json:"space"`
		Parent        string `json:"parent"`
	}{
		SchemaVersion: confluencePageTrashSchemaVersion, Operation: "trash",
		BackendSHA256: snapshot.backendSHA256, ID: snapshot.id, ContentType: snapshot.contentType,
		Status: snapshot.status, TargetStatus: "trashed", Version: snapshot.version,
		BodySHA256: snapshot.bodySHA256, BodyBytes: snapshot.bodyBytes,
		TitleSHA256: snapshot.titleSHA256, Space: snapshot.space, Parent: snapshot.parent,
	})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func confluencePageTrashMatches(before, after confluencePageTrashSnapshot) bool {
	return after.status == "trashed" && after.id == before.id && after.contentType == before.contentType &&
		after.version == before.version && after.bodySHA256 == before.bodySHA256 && after.bodyBytes == before.bodyBytes &&
		after.titleSHA256 == before.titleSHA256 && after.space == before.space && after.parent == before.parent &&
		after.backendSHA256 == before.backendSHA256
}

func confluencePageTrashAmbiguousError(message string, cause error) error {
	return &confluencePageTrashWriteError{message: message, cause: cause, closed: true, ambiguous: true}
}

func ConfluencePageTrashText(result *ConfluencePageTrashResult) string {
	if result == nil {
		return ""
	}
	return fmt.Sprintf("status: %s\npage_id: %s\nversion: %d\nproposal_hash: %s\nobserved_state: %s",
		result.Status, result.ID, result.CurrentVersion, result.ProposalHash, result.ObservedState)
}
