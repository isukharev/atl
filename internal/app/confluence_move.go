package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type ConfluenceMoveOpts struct {
	Parent               string
	ExpectedVersion      int
	ExpectedParent       string
	ExpectedParentSet    bool
	ExpectedProposalHash string
	Apply                bool
}

type ConfluenceMoveResult struct {
	ID              string `json:"id"`
	Mode            string `json:"mode"`
	Status          string `json:"status"`
	CurrentParent   string `json:"current_parent"`
	Parent          string `json:"parent"`
	CurrentVersion  int    `json:"current_version"`
	ExpectedVersion int    `json:"expected_version"`
	ExpectedParent  string `json:"expected_parent"`
	TargetVersion   int    `json:"target_version"`
	FinalVersion    int    `json:"final_version,omitempty"`
	ProposalHash    string `json:"proposal_hash"`
	Reconciled      bool   `json:"reconciled,omitempty"`
}

type confluenceMoveWriteError struct {
	message string
	cause   error
}

func (e *confluenceMoveWriteError) Error() string { return e.message }
func (e *confluenceMoveWriteError) Unwrap() error { return e.cause }

func (s *ConfluenceService) MoveGuarded(ctx context.Context, id string, opts ConfluenceMoveOpts) (*ConfluenceMoveResult, error) {
	id = strings.TrimSpace(id)
	parent := strings.TrimSpace(opts.Parent)
	if id == "" || parent == "" {
		return nil, fmt.Errorf("%w: page id and parent id are required", domain.ErrUsage)
	}
	if id == parent {
		return nil, fmt.Errorf("%w: a page cannot be its own parent", domain.ErrCheckFailed)
	}
	if opts.Apply && opts.ExpectedVersion <= 0 {
		return nil, fmt.Errorf("%w: --expected-version is required with --apply; run the dry-run first", domain.ErrUsage)
	}
	if opts.Apply && !opts.ExpectedParentSet {
		return nil, fmt.Errorf("%w: --expected-parent is required with --apply; use --expected-parent= for a top-level page", domain.ErrUsage)
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) == "" {
		return nil, fmt.Errorf("%w: --expected-proposal-hash is required with --apply; run the dry-run first", domain.ErrUsage)
	}

	current, err := s.store.GetPage(ctx, id, domain.PullOpts{Format: "csf"})
	if err != nil {
		return nil, err
	}
	if err := validateMovePageRead(current, id, true); err != nil {
		return nil, err
	}
	target, err := s.store.GetPage(ctx, parent, domain.PullOpts{Format: "csf"})
	if err != nil {
		return nil, err
	}
	if err := validateMovePageRead(target, parent, false); err != nil {
		return nil, err
	}
	if current.SpaceKey != "" && target.SpaceKey != "" && current.SpaceKey != target.SpaceKey {
		return nil, fmt.Errorf("%w: source and target parent are in different spaces", domain.ErrCheckFailed)
	}
	if slices.Contains(target.AncestorIDs, id) {
		return nil, fmt.Errorf("%w: target parent %s is a descendant of page %s", domain.ErrCheckFailed, parent, id)
	}

	proposalHash := confluenceMoveProposalHash(id, current.Version, current.Parent, parent)
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	expectedVersion := opts.ExpectedVersion
	if expectedVersion <= 0 {
		expectedVersion = current.Version
	}
	expectedParent := current.Parent
	if opts.ExpectedParentSet {
		expectedParent = strings.TrimSpace(opts.ExpectedParent)
	}
	result := &ConfluenceMoveResult{
		ID: id, Mode: mode, Status: "would_apply", CurrentParent: current.Parent,
		Parent: parent, CurrentVersion: current.Version, ExpectedVersion: expectedVersion,
		ExpectedParent: expectedParent, TargetVersion: target.Version, ProposalHash: proposalHash,
	}
	if current.Parent == parent {
		result.Status = "already_satisfied"
		result.FinalVersion = current.Version
		return result, nil
	}
	if !opts.Apply {
		return result, nil
	}
	if expectedVersion != current.Version {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: stale page %s: expected version %d, got %d", domain.ErrCheckFailed, id, expectedVersion, current.Version)
	}
	if expectedParent != current.Parent {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: page %s parent changed: expected %q, got %q", domain.ErrCheckFailed, id, expectedParent, current.Parent)
	}
	if strings.TrimSpace(opts.ExpectedProposalHash) != proposalHash {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: move proposal changed since review: expected hash %q, got %q", domain.ErrCheckFailed, strings.TrimSpace(opts.ExpectedProposalHash), proposalHash)
	}

	_, writeErr := s.store.MovePage(ctx, id, parent, current.Version, current.Title, current.Body)
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		result.Status = "failed"
		return result, &confluenceMoveWriteError{message: "Confluence rejected the page move", cause: writeErr}
	}
	verified, verifyErr := s.store.GetPage(ctx, id, domain.PullOpts{Format: "csf"})
	if verifyErr != nil || verified == nil {
		result.Status = "unknown"
		cause := writeErr
		if cause == nil {
			cause = verifyErr
		}
		return result, &confluenceMoveWriteError{message: "page move outcome is unknown; verification read failed; do not replay automatically", cause: cause}
	}
	result.Reconciled = writeErr != nil
	result.FinalVersion = verified.Version
	if verified.BodyPresent && validMoveHierarchy(verified) && verified.ID == id && verified.Type == "page" &&
		verified.Parent == parent && verified.Title == current.Title && verified.SpaceKey == current.SpaceKey &&
		verified.Version == current.Version+1 && mirror.Hash(verified.Body) == mirror.Hash(current.Body) {
		result.Status = "applied"
		return result, nil
	}
	result.Status = "unknown"
	cause := writeErr
	if cause == nil {
		cause = fmt.Errorf("verified page state differs from the reviewed move")
	}
	return result, &confluenceMoveWriteError{message: "page move outcome is unknown; verified page state differs from the reviewed proposal; do not replay automatically", cause: cause}
}

func validateMovePageRead(page *domain.Resource, expectedID string, requireBody bool) error {
	if page == nil || page.Version <= 0 {
		return fmt.Errorf("%w: fresh page read for %s returned no usable version", domain.ErrCheckFailed, expectedID)
	}
	if page.ID != expectedID {
		return fmt.Errorf("%w: fresh page read identity mismatch: requested %s, got %s", domain.ErrCheckFailed, expectedID, page.ID)
	}
	if page.Type != "page" {
		return fmt.Errorf("%w: fresh page read for %s returned content type %q", domain.ErrCheckFailed, expectedID, page.Type)
	}
	if strings.TrimSpace(page.SpaceKey) == "" {
		return fmt.Errorf("%w: fresh page read for %s omitted its space identity", domain.ErrCheckFailed, expectedID)
	}
	if requireBody && !page.BodyPresent {
		return fmt.Errorf("%w: fresh page read for %s omitted native body.storage; refusing a move that could erase the page body", domain.ErrCheckFailed, expectedID)
	}
	if requireBody && strings.TrimSpace(page.Title) == "" {
		return fmt.Errorf("%w: fresh page read for %s omitted its title; refusing move", domain.ErrCheckFailed, expectedID)
	}
	if !validMoveHierarchy(page) {
		return fmt.Errorf("%w: fresh page read for %s returned incomplete ancestor identities", domain.ErrCheckFailed, expectedID)
	}
	return nil
}

func validMoveHierarchy(page *domain.Resource) bool {
	if page == nil || !page.AncestorsPresent || len(page.Ancestors) != len(page.AncestorIDs) {
		return false
	}
	for _, id := range page.AncestorIDs {
		if strings.TrimSpace(id) == "" {
			return false
		}
	}
	return true
}

func confluenceMoveProposalHash(id string, version int, currentParent, parent string) string {
	canonical, _ := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		ID            string `json:"id"`
		Version       int    `json:"version"`
		CurrentParent string `json:"current_parent"`
		Parent        string `json:"parent"`
	}{SchemaVersion: 1, ID: id, Version: version, CurrentParent: currentParent, Parent: parent})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
