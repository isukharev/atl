package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const confluenceAttachmentDeleteSchemaVersion = 1

type ConfluenceAttachmentDeleteOpts struct {
	Apply                bool
	Confirm              string
	ExpectedPageVersion  int
	ExpectedProposalHash string
}

type ConfluenceAttachmentDeleteResult struct {
	SchemaVersion         int    `json:"schema_version"`
	PageID                string `json:"page_id"`
	AttachmentID          string `json:"attachment_id"`
	Mode                  string `json:"mode"`
	Status                string `json:"status"`
	Operation             string `json:"operation"`
	ObservedState         string `json:"observed_state,omitempty"`
	CurrentPageVersion    int    `json:"current_page_version"`
	ExpectedPageVersion   int    `json:"expected_page_version"`
	FinalPageVersion      int    `json:"final_page_version,omitempty"`
	PageBodySHA256        string `json:"page_body_sha256"`
	PageBodyBytes         int    `json:"page_body_bytes"`
	PageTitleSHA256       string `json:"page_title_sha256"`
	PageHierarchySHA256   string `json:"page_hierarchy_sha256"`
	AttachmentTitleSHA256 string `json:"attachment_title_sha256"`
	MediaTypeSHA256       string `json:"media_type_sha256"`
	AttachmentFileSize    int64  `json:"attachment_file_size"`
	AttachmentVersion     int    `json:"attachment_version"`
	InventoryCount        int    `json:"inventory_count"`
	InventorySHA256       string `json:"inventory_sha256"`
	ExpectedFinalCount    int    `json:"expected_final_count"`
	ExpectedFinalSHA256   string `json:"expected_final_sha256"`
	FinalCount            int    `json:"final_count,omitempty"`
	FinalSHA256           string `json:"final_sha256,omitempty"`
	BackendSHA256         string `json:"backend_sha256"`
	ProposalHash          string `json:"proposal_hash"`
	WriteAttempted        bool   `json:"write_attempted"`
	Reconciled            bool   `json:"reconciled,omitempty"`
	Complete              bool   `json:"complete"`
	Warning               string `json:"warning"`
}

type confluenceAttachmentDeleteEvidence struct {
	ID              string `json:"id"`
	TitleSHA256     string `json:"title_sha256"`
	MediaTypeSHA256 string `json:"media_type_sha256"`
	CommentSHA256   string `json:"comment_sha256"`
	FileSize        int64  `json:"file_size"`
	Version         int    `json:"version"`
}

type confluenceAttachmentDeletePageEvidence struct {
	ID              string `json:"id"`
	Version         int    `json:"version"`
	BodySHA256      string `json:"body_sha256"`
	BodyBytes       int    `json:"body_bytes"`
	TitleSHA256     string `json:"title_sha256"`
	Space           string `json:"space"`
	Parent          string `json:"parent"`
	HierarchySHA256 string `json:"hierarchy_sha256"`
}

type confluenceAttachmentDeleteSnapshot struct {
	page            confluenceAttachmentDeletePageEvidence
	backendSHA256   string
	attachment      confluenceAttachmentDeleteEvidence
	targetPresent   bool
	inventory       []confluenceAttachmentDeleteEvidence
	inventorySHA256 string
}

type confluenceAttachmentDeleteWriteError struct {
	message   string
	cause     error
	ambiguous bool
}

func (e *confluenceAttachmentDeleteWriteError) Error() string { return e.message }

func (e *confluenceAttachmentDeleteWriteError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return operationErrorCauses(e.cause, true)
}

func (e *confluenceAttachmentDeleteWriteError) DiagnosticAmbiguousWrite() bool {
	return e != nil && e.ambiguous
}

// DeleteAttachmentGuarded previews or applies one reviewed permanent
// attachment deletion. A snapshot brackets a complete attachment inventory
// with exact current-page reads, and apply repeats that snapshot immediately
// before one non-replayed DELETE. Only the exact expected post-delete
// inventory proves success.
func (s *ConfluenceService) DeleteAttachmentGuarded(ctx context.Context, pageID, attachmentID string, opts ConfluenceAttachmentDeleteOpts) (*ConfluenceAttachmentDeleteResult, error) {
	pageID = strings.TrimSpace(pageID)
	attachmentID = strings.TrimSpace(attachmentID)
	if !domain.ValidConfluenceContentID(pageID) {
		return nil, fmt.Errorf("%w: page id must be a positive numeric content id", domain.ErrUsage)
	}
	if !domain.ValidConfluenceContentID(attachmentID) {
		return nil, fmt.Errorf("%w: attachment id must be a positive numeric content id", domain.ErrUsage)
	}
	if !opts.Apply && (opts.Confirm != "" || opts.ExpectedPageVersion != 0 || strings.TrimSpace(opts.ExpectedProposalHash) != "") {
		return nil, fmt.Errorf("%w: --confirm, --expected-version, and --expected-proposal-hash require --apply", domain.ErrUsage)
	}
	if opts.Apply && opts.Confirm != "DELETE" {
		return nil, fmt.Errorf("%w: --confirm must be exactly DELETE with --apply", domain.ErrUsage)
	}
	if opts.Apply && opts.ExpectedPageVersion <= 0 {
		return nil, fmt.Errorf("%w: --expected-version is required with --apply; run the dry-run first", domain.ErrUsage)
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) == "" {
		return nil, fmt.Errorf("%w: --expected-proposal-hash is required with --apply; run the dry-run first", domain.ErrUsage)
	}

	initial, err := s.confluenceAttachmentDeleteSnapshot(ctx, pageID, attachmentID, true)
	if err != nil {
		return nil, fmt.Errorf("qualify attachment deletion proposal: %w", sanitizeConfluenceWriteCause(err))
	}
	expectedFinal := confluenceAttachmentInventoryWithout(initial.inventory, attachmentID)
	expectedFinalSHA256 := confluenceAttachmentInventoryHash(expectedFinal)
	proposalHash := confluenceAttachmentDeleteProposalHash(initial, expectedFinalSHA256)
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	expectedVersion := initial.page.Version
	if opts.ExpectedPageVersion > 0 {
		expectedVersion = opts.ExpectedPageVersion
	}
	result := &ConfluenceAttachmentDeleteResult{
		SchemaVersion: confluenceAttachmentDeleteSchemaVersion, PageID: pageID, AttachmentID: attachmentID,
		Mode: mode, Status: "would_apply", Operation: "delete", ObservedState: "present",
		CurrentPageVersion: initial.page.Version, ExpectedPageVersion: expectedVersion,
		PageBodySHA256: initial.page.BodySHA256, PageBodyBytes: initial.page.BodyBytes,
		PageTitleSHA256: initial.page.TitleSHA256, PageHierarchySHA256: initial.page.HierarchySHA256,
		AttachmentTitleSHA256: initial.attachment.TitleSHA256, MediaTypeSHA256: initial.attachment.MediaTypeSHA256,
		AttachmentFileSize: initial.attachment.FileSize, AttachmentVersion: initial.attachment.Version,
		InventoryCount: len(initial.inventory), InventorySHA256: initial.inventorySHA256,
		ExpectedFinalCount: len(expectedFinal), ExpectedFinalSHA256: expectedFinalSHA256,
		BackendSHA256: initial.backendSHA256, ProposalHash: proposalHash, Complete: true,
		Warning: "permanent attachment deletion has no server-side version CAS; apply reconciles two complete inventories before one DELETE and never replays it",
	}
	if opts.Apply && expectedVersion != initial.page.Version {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: reviewed page version changed; run the dry-run again", domain.ErrCheckFailed)
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) != proposalHash {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: attachment deletion proposal changed since review; run the dry-run again", domain.ErrCheckFailed)
	}
	if !opts.Apply {
		return result, nil
	}

	result.WriteAttempted = true
	writeErr := s.store.DeleteAttachment(domain.WithSingleAttempt(ctx), pageID, attachmentID)
	if writeDefinitelyNotAttempted(writeErr) {
		result.WriteAttempted = false
	}
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		result.Status = "not_applied"
		result.ObservedState = "present"
		return result, &confluenceAttachmentDeleteWriteError{
			message: definitiveWriteMessage("Confluence rejected the attachment deletion; it was not applied", writeErr),
			cause:   sanitizeConfluenceWriteCause(writeErr),
		}
	}

	readback, readbackErr := s.confluenceAttachmentDeleteSnapshot(ctx, pageID, attachmentID, false)
	if readbackErr != nil {
		result.Status = "outcome_unknown"
		result.Complete = false
		result.ObservedState = "unavailable"
		return result, confluenceAttachmentDeleteAmbiguousError(
			"attachment deletion outcome is unknown because exact complete readback failed; do not replay automatically",
			errors.Join(sanitizeConfluenceWriteCause(writeErr), sanitizeConfluenceWriteCause(readbackErr)),
		)
	}
	result.Reconciled = true
	result.FinalPageVersion = readback.page.Version
	result.FinalCount = len(readback.inventory)
	result.FinalSHA256 = readback.inventorySHA256
	if readback.targetPresent {
		result.ObservedState = "present"
	} else {
		result.ObservedState = "absent"
	}
	if confluenceAttachmentDeletePageMatches(initial.page, readback.page) &&
		readback.backendSHA256 == initial.backendSHA256 && !readback.targetPresent &&
		len(readback.inventory) == len(expectedFinal) && readback.inventorySHA256 == expectedFinalSHA256 {
		if writeErr == nil {
			result.Status = "applied"
		} else {
			result.Status = "recovered"
		}
		return result, nil
	}
	result.Status = "outcome_unknown"
	return result, confluenceAttachmentDeleteAmbiguousError(
		"attachment deletion outcome is unknown because exact readback differs from the reviewed expected inventory; do not replay automatically",
		sanitizeConfluenceWriteCause(writeErr),
	)
}

func (s *ConfluenceService) confluenceAttachmentDeleteSnapshot(ctx context.Context, pageID, attachmentID string, requireTarget bool) (confluenceAttachmentDeleteSnapshot, error) {
	lister, ok := s.store.(domain.QualifiedAttachmentLister)
	if !ok {
		return confluenceAttachmentDeleteSnapshot{}, fmt.Errorf("%w: Confluence backend cannot prove a complete attachment inventory", domain.ErrCheckFailed)
	}
	backendSHA256, err := backendid.OriginSHA256(s.baseURL)
	if err != nil {
		return confluenceAttachmentDeleteSnapshot{}, fmt.Errorf("%w: invalid Confluence backend identity", domain.ErrCheckFailed)
	}
	before, err := s.readExactCurrentConfluencePage(ctx, pageID)
	if err != nil {
		return confluenceAttachmentDeleteSnapshot{}, err
	}
	beforeEvidence := confluenceAttachmentDeletePageEvidenceFromResource(before)
	readCtx := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx))
	firstInventory, err := lister.ListAttachmentsQualified(readCtx, pageID)
	if err != nil {
		return confluenceAttachmentDeleteSnapshot{}, err
	}
	firstEvidence, firstSelected, firstFound, err := confluenceAttachmentDeleteInventoryEvidence(firstInventory, attachmentID)
	if err != nil {
		return confluenceAttachmentDeleteSnapshot{}, err
	}
	secondInventory, err := lister.ListAttachmentsQualified(readCtx, pageID)
	if err != nil {
		return confluenceAttachmentDeleteSnapshot{}, err
	}
	secondEvidence, secondSelected, secondFound, err := confluenceAttachmentDeleteInventoryEvidence(secondInventory, attachmentID)
	if err != nil {
		return confluenceAttachmentDeleteSnapshot{}, err
	}
	if !confluenceAttachmentDeleteInventoriesEqual(firstEvidence, secondEvidence) || firstFound != secondFound || firstSelected != secondSelected {
		return confluenceAttachmentDeleteSnapshot{}, fmt.Errorf("%w: consecutive complete attachment inventories did not reconcile", domain.ErrCheckFailed)
	}
	after, err := s.readExactCurrentConfluencePage(ctx, pageID)
	if err != nil {
		return confluenceAttachmentDeleteSnapshot{}, err
	}
	afterEvidence := confluenceAttachmentDeletePageEvidenceFromResource(after)
	if !confluenceAttachmentDeletePageMatches(beforeEvidence, afterEvidence) {
		return confluenceAttachmentDeleteSnapshot{}, fmt.Errorf("%w: page changed while the attachment inventory was being read", domain.ErrCheckFailed)
	}

	if requireTarget && !firstFound {
		return confluenceAttachmentDeleteSnapshot{}, fmt.Errorf("%w: attachment is absent from the complete page inventory", domain.ErrNotFound)
	}
	return confluenceAttachmentDeleteSnapshot{
		page: beforeEvidence, backendSHA256: backendSHA256, attachment: firstSelected,
		targetPresent: firstFound, inventory: firstEvidence, inventorySHA256: confluenceAttachmentInventoryHash(firstEvidence),
	}, nil
}

func confluenceAttachmentDeleteInventoryEvidence(inventory domain.AttachmentInventory, attachmentID string) ([]confluenceAttachmentDeleteEvidence, confluenceAttachmentDeleteEvidence, bool, error) {
	if err := validateConfluenceAttachmentDeleteInventory(inventory); err != nil {
		return nil, confluenceAttachmentDeleteEvidence{}, false, err
	}
	evidence := make([]confluenceAttachmentDeleteEvidence, 0, len(inventory.Attachments))
	var selected confluenceAttachmentDeleteEvidence
	found := false
	for _, attachment := range inventory.Attachments {
		item := confluenceAttachmentDeleteEvidenceFromAttachment(attachment)
		evidence = append(evidence, item)
		if attachment.ID == attachmentID {
			selected = item
			found = true
		}
	}
	sort.Slice(evidence, func(i, j int) bool { return evidence[i].ID < evidence[j].ID })
	return evidence, selected, found, nil
}

func confluenceAttachmentDeleteInventoriesEqual(a, b []confluenceAttachmentDeleteEvidence) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func validateConfluenceAttachmentDeleteInventory(inventory domain.AttachmentInventory) error {
	if err := validateConfluenceAttachmentInventory(inventory); err != nil {
		return err
	}
	if !inventory.Complete || inventory.PartialReason != "" {
		return fmt.Errorf("%w: permanent deletion requires a complete attachment inventory", domain.ErrCheckFailed)
	}
	for _, attachment := range inventory.Attachments {
		if !domain.ValidConfluenceContentID(attachment.ID) || strings.TrimSpace(attachment.Title) == "" || attachment.Version <= 0 {
			return fmt.Errorf("%w: attachment inventory omitted canonical id, title, or positive version evidence", domain.ErrCheckFailed)
		}
	}
	return nil
}

func confluenceAttachmentDeletePageEvidenceFromResource(page *domain.Resource) confluenceAttachmentDeletePageEvidence {
	titleSum := sha256.Sum256([]byte(page.Title))
	return confluenceAttachmentDeletePageEvidence{
		ID: page.ID, Version: page.Version, BodySHA256: mirror.Hash(page.Body), BodyBytes: len(page.Body),
		TitleSHA256: hex.EncodeToString(titleSum[:]), Space: page.SpaceKey, Parent: page.Parent,
		HierarchySHA256: confluencePageHierarchyHash(page.AncestorIDs, page.Ancestors),
	}
}

func confluenceAttachmentDeleteEvidenceFromAttachment(attachment domain.Attachment) confluenceAttachmentDeleteEvidence {
	titleSum := sha256.Sum256([]byte(attachment.Title))
	mediaSum := sha256.Sum256([]byte(attachment.MediaType))
	commentSum := sha256.Sum256([]byte(attachment.Comment))
	return confluenceAttachmentDeleteEvidence{
		ID: attachment.ID, TitleSHA256: hex.EncodeToString(titleSum[:]),
		MediaTypeSHA256: hex.EncodeToString(mediaSum[:]), CommentSHA256: hex.EncodeToString(commentSum[:]),
		FileSize: attachment.FileSize, Version: attachment.Version,
	}
}

func confluenceAttachmentInventoryWithout(inventory []confluenceAttachmentDeleteEvidence, attachmentID string) []confluenceAttachmentDeleteEvidence {
	result := make([]confluenceAttachmentDeleteEvidence, 0, len(inventory))
	for _, attachment := range inventory {
		if attachment.ID != attachmentID {
			result = append(result, attachment)
		}
	}
	return result
}

func confluenceAttachmentInventoryHash(inventory []confluenceAttachmentDeleteEvidence) string {
	canonical, _ := json.Marshal(inventory)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func confluenceAttachmentDeleteProposalHash(snapshot confluenceAttachmentDeleteSnapshot, expectedFinalSHA256 string) string {
	canonical, _ := json.Marshal(struct {
		SchemaVersion       int                                    `json:"schema_version"`
		Operation           string                                 `json:"operation"`
		BackendSHA256       string                                 `json:"backend_sha256"`
		Page                confluenceAttachmentDeletePageEvidence `json:"page"`
		Attachment          confluenceAttachmentDeleteEvidence     `json:"attachment"`
		InventorySHA256     string                                 `json:"inventory_sha256"`
		InventoryCount      int                                    `json:"inventory_count"`
		ExpectedFinalSHA256 string                                 `json:"expected_final_sha256"`
		ExpectedFinalCount  int                                    `json:"expected_final_count"`
	}{
		SchemaVersion: confluenceAttachmentDeleteSchemaVersion, Operation: "delete_attachment",
		BackendSHA256: snapshot.backendSHA256, Page: snapshot.page, Attachment: snapshot.attachment,
		InventorySHA256: snapshot.inventorySHA256, InventoryCount: len(snapshot.inventory),
		ExpectedFinalSHA256: expectedFinalSHA256, ExpectedFinalCount: len(snapshot.inventory) - 1,
	})
	return guardedProposalDigest(canonical)
}

func confluenceAttachmentDeletePageMatches(a, b confluenceAttachmentDeletePageEvidence) bool {
	return a == b
}

func confluenceAttachmentDeleteAmbiguousError(message string, cause error) error {
	return &confluenceAttachmentDeleteWriteError{message: message, cause: cause, ambiguous: true}
}

func ConfluenceAttachmentDeleteText(result *ConfluenceAttachmentDeleteResult) string {
	if result == nil {
		return ""
	}
	return fmt.Sprintf("status: %s\npage_id: %s\nattachment_id: %s\npage_version: %d\nproposal_hash: %s\nobserved_state: %s",
		result.Status, result.PageID, result.AttachmentID, result.CurrentPageVersion, result.ProposalHash, result.ObservedState)
}
