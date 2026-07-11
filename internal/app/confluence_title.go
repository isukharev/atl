package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const ConfluenceTitleInputCap = 4096

type ConfluenceTitleSetOpts struct {
	Title                []byte
	ExpectedVersion      int
	ExpectedProposalHash string
	Apply                bool
}

type ConfluenceTitleSetResult struct {
	ID              string `json:"id"`
	Mode            string `json:"mode"`
	Status          string `json:"status"`
	CurrentTitle    string `json:"current_title"`
	Title           string `json:"title"`
	TitleBytes      int    `json:"title_bytes"`
	TitleSHA256     string `json:"title_sha256"`
	CurrentVersion  int    `json:"current_version"`
	ExpectedVersion int    `json:"expected_version"`
	FinalVersion    int    `json:"final_version,omitempty"`
	ProposalHash    string `json:"proposal_hash"`
	Reconciled      bool   `json:"reconciled,omitempty"`
}

type confluenceTitleWriteError struct {
	message string
	cause   error
}

func (e *confluenceTitleWriteError) Error() string { return e.message }
func (e *confluenceTitleWriteError) Unwrap() error { return e.cause }

func (s *ConfluenceService) SetTitleGuarded(ctx context.Context, id string, opts ConfluenceTitleSetOpts) (*ConfluenceTitleSetResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("%w: page id is required", domain.ErrUsage)
	}
	title, err := normalizeConfluenceTitle(opts.Title)
	if err != nil {
		return nil, err
	}
	if opts.Apply && opts.ExpectedVersion <= 0 {
		return nil, fmt.Errorf("%w: --expected-version is required with --apply; run the dry-run first", domain.ErrUsage)
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) == "" {
		return nil, fmt.Errorf("%w: --expected-proposal-hash is required with --apply; run the dry-run first", domain.ErrUsage)
	}
	current, err := s.store.GetPage(ctx, id, domain.PullOpts{Format: "csf"})
	if err != nil {
		return nil, err
	}
	if current == nil || current.Version <= 0 {
		return nil, fmt.Errorf("%w: fresh page read for %s returned no usable version", domain.ErrCheckFailed, id)
	}
	if current.ID != "" && current.ID != id {
		return nil, fmt.Errorf("%w: fresh page read identity mismatch: requested %s, got %s", domain.ErrCheckFailed, id, current.ID)
	}
	if !current.BodyPresent {
		return nil, fmt.Errorf("%w: fresh page read for %s omitted native body.storage; refusing a title update that could erase the page body", domain.ErrCheckFailed, id)
	}
	proposalHash, titleHash := confluenceTitleProposalHash(id, current.Version, title)
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	expected := opts.ExpectedVersion
	if expected <= 0 {
		expected = current.Version
	}
	result := &ConfluenceTitleSetResult{
		ID: id, Mode: mode, Status: "would_apply", CurrentTitle: current.Title,
		Title: title, TitleBytes: len([]byte(title)), TitleSHA256: titleHash,
		CurrentVersion: current.Version, ExpectedVersion: expected, ProposalHash: proposalHash,
	}
	if opts.Apply && expected != current.Version {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: stale page %s: expected version %d, got %d", domain.ErrCheckFailed, id, expected, current.Version)
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) != proposalHash {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: title proposal changed since review: expected hash %q, got %q", domain.ErrCheckFailed, strings.TrimSpace(opts.ExpectedProposalHash), proposalHash)
	}
	if current.Title == title {
		result.Status = "already_satisfied"
		result.FinalVersion = current.Version
		return result, nil
	}
	if !opts.Apply {
		return result, nil
	}

	_, writeErr := s.store.UpdatePage(ctx, id, current.Version, title, current.Body, false)
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		result.Status = "failed"
		return result, &confluenceTitleWriteError{message: "Confluence rejected the title update", cause: writeErr}
	}
	verified, verifyErr := s.store.GetPage(ctx, id, domain.PullOpts{Format: "csf"})
	if verifyErr != nil || verified == nil {
		result.Status = "unknown"
		cause := writeErr
		if cause == nil {
			cause = verifyErr
		}
		return result, &confluenceTitleWriteError{message: "title update outcome is unknown; verification read failed; do not replay automatically", cause: cause}
	}
	result.Reconciled = writeErr != nil
	result.FinalVersion = verified.Version
	if verified.BodyPresent && (verified.ID == "" || verified.ID == id) && verified.Title == title && verified.Version == current.Version+1 && mirror.Hash(verified.Body) == mirror.Hash(current.Body) {
		result.Status = "applied"
		return result, nil
	}
	result.Status = "unknown"
	cause := writeErr
	if cause == nil {
		cause = fmt.Errorf("verified page state differs from the reviewed title/body/version")
	}
	return result, &confluenceTitleWriteError{message: "title update outcome is unknown; verified page state differs from the reviewed proposal; do not replay automatically", cause: cause}
}

func normalizeConfluenceTitle(raw []byte) (string, error) {
	if len(raw) > ConfluenceTitleInputCap {
		return "", fmt.Errorf("%w: title input exceeds %d bytes", domain.ErrUsage, ConfluenceTitleInputCap)
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("%w: title input is not valid UTF-8", domain.ErrUsage)
	}
	title := strings.TrimSpace(string(raw))
	if title == "" {
		return "", fmt.Errorf("%w: title must not be empty", domain.ErrUsage)
	}
	for _, r := range title {
		if r == '\n' || r == '\r' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", fmt.Errorf("%w: title must be one line without control or invisible format characters", domain.ErrUsage)
		}
	}
	return title, nil
}

func confluenceTitleProposalHash(id string, version int, title string) (proposal, titleDigest string) {
	digest := sha256.Sum256([]byte(title))
	titleDigest = hex.EncodeToString(digest[:])
	canonical, _ := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		ID            string `json:"id"`
		Version       int    `json:"version"`
		Title         string `json:"title"`
	}{SchemaVersion: 1, ID: id, Version: version, Title: title})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), titleDigest
}
