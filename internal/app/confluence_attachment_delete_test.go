package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

const confluenceAttachmentDeleteTestBackend = "https://confluence.example.test/wiki"

type confluenceAttachmentDeletePageRead struct {
	page *domain.Resource
	err  error
}

type confluenceAttachmentDeleteInventoryRead struct {
	inventory domain.AttachmentInventory
	err       error
}

type confluenceAttachmentDeleteStore struct {
	domain.DocStore
	pageReads      []confluenceAttachmentDeletePageRead
	inventoryReads []confluenceAttachmentDeleteInventoryRead
	pageIndex      int
	inventoryIndex int
	deleteErr      error
	deleteCalls    int
	deleteID       string
	deleteSingle   bool
	listSingle     []bool
	listRedacted   []bool
}

func (s *confluenceAttachmentDeleteStore) GetPageByStatus(_ context.Context, id, status string, _ domain.PullOpts) (*domain.Resource, error) {
	if id != "42" || status != "current" {
		return nil, fmt.Errorf("unexpected page read id=%q status=%q", id, status)
	}
	if s.pageIndex >= len(s.pageReads) {
		return nil, errors.New("unexpected page read")
	}
	read := s.pageReads[s.pageIndex]
	s.pageIndex++
	if read.page == nil || read.err != nil {
		return read.page, read.err
	}
	page := *read.page
	page.Body = append([]byte(nil), read.page.Body...)
	page.Ancestors = append([]string(nil), read.page.Ancestors...)
	page.AncestorIDs = append([]string(nil), read.page.AncestorIDs...)
	return &page, nil
}

func (s *confluenceAttachmentDeleteStore) ListAttachmentsQualified(ctx context.Context, id string) (domain.AttachmentInventory, error) {
	if id != "42" {
		return domain.AttachmentInventory{}, fmt.Errorf("unexpected inventory id %q", id)
	}
	s.listSingle = append(s.listSingle, domain.SingleAttempt(ctx))
	s.listRedacted = append(s.listRedacted, domain.RedactedHTTPTrace(ctx))
	if s.inventoryIndex >= len(s.inventoryReads) {
		return domain.AttachmentInventory{}, errors.New("unexpected inventory read")
	}
	read := s.inventoryReads[s.inventoryIndex]
	s.inventoryIndex++
	read.inventory.Attachments = append([]domain.Attachment(nil), read.inventory.Attachments...)
	return read.inventory, read.err
}

func (s *confluenceAttachmentDeleteStore) DeleteAttachment(ctx context.Context, _ string, id string) error {
	s.deleteCalls++
	s.deleteID = id
	s.deleteSingle = domain.SingleAttempt(ctx)
	return s.deleteErr
}

type confluenceAttachmentDeleteHTTPError struct {
	status   int
	sentinel error
	detail   string
}

func (e confluenceAttachmentDeleteHTTPError) Error() string   { return e.detail }
func (e confluenceAttachmentDeleteHTTPError) HTTPStatus() int { return e.status }
func (e confluenceAttachmentDeleteHTTPError) Unwrap() error   { return e.sentinel }

func confluenceAttachmentDeletePage(version int) *domain.Resource {
	return &domain.Resource{
		ID: "42", Type: "page", Status: "current", Title: "Reviewed page", SpaceKey: "DOC",
		Version: version, Body: []byte("<p>reviewed body</p>"), BodyPresent: true,
		Parent: "10", Ancestors: []string{"Home"}, AncestorIDs: []string{"10"}, AncestorsPresent: true,
	}
}

func confluenceAttachmentDeleteInventory(attachments ...domain.Attachment) domain.AttachmentInventory {
	return domain.AttachmentInventory{Complete: true, Attachments: append([]domain.Attachment(nil), attachments...)}
}

func confluenceAttachmentDeleteBaseInventory() domain.AttachmentInventory {
	return confluenceAttachmentDeleteInventory(
		domain.Attachment{ID: "100", Title: "target.txt", MediaType: "text/plain", FileSize: 12, Version: 3, Comment: "reviewed target comment"},
		domain.Attachment{ID: "200", Title: "sibling.png", MediaType: "image/png", FileSize: 34, Version: 5, Comment: "reviewed sibling comment"},
	)
}

func confluenceAttachmentDeleteExpectedInventory() domain.AttachmentInventory {
	return confluenceAttachmentDeleteInventory(
		domain.Attachment{ID: "200", Title: "sibling.png", MediaType: "image/png", FileSize: 34, Version: 5, Comment: "reviewed sibling comment"},
	)
}

func confluenceAttachmentDeletePageQueue(page *domain.Resource, count int) []confluenceAttachmentDeletePageRead {
	reads := make([]confluenceAttachmentDeletePageRead, count)
	for i := range reads {
		reads[i].page = page
	}
	return reads
}

func confluenceAttachmentDeleteInventoryQueue(inventory domain.AttachmentInventory, count int) []confluenceAttachmentDeleteInventoryRead {
	reads := make([]confluenceAttachmentDeleteInventoryRead, count)
	for i := range reads {
		reads[i].inventory = inventory
	}
	return reads
}

func previewConfluenceAttachmentDelete(t *testing.T, inventory domain.AttachmentInventory) *ConfluenceAttachmentDeleteResult {
	t.Helper()
	store := &confluenceAttachmentDeleteStore{
		pageReads:      confluenceAttachmentDeletePageQueue(confluenceAttachmentDeletePage(7), 2),
		inventoryReads: confluenceAttachmentDeleteInventoryQueue(inventory, 2),
	}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceAttachmentDeleteTestBackend}).DeleteAttachmentGuarded(
		context.Background(), "42", "100", ConfluenceAttachmentDeleteOpts{})
	if err != nil || result == nil || result.Status != "would_apply" || result.ProposalHash == "" || result.WriteAttempted ||
		store.inventoryIndex != 2 || store.deleteCalls != 0 {
		t.Fatalf("preview=%+v err=%v deletes=%d", result, err, store.deleteCalls)
	}
	return result
}

func TestConfluenceAttachmentDeleteProposalIsOrderIndependentAndBindsEveryEvidenceField(t *testing.T) {
	baseInventory := confluenceAttachmentDeleteBaseInventory()
	reversed := confluenceAttachmentDeleteInventory(baseInventory.Attachments[1], baseInventory.Attachments[0])
	basePreview := previewConfluenceAttachmentDelete(t, baseInventory)
	reversedPreview := previewConfluenceAttachmentDelete(t, reversed)
	if basePreview.ProposalHash != reversedPreview.ProposalHash || basePreview.InventorySHA256 != reversedPreview.InventorySHA256 {
		t.Fatalf("inventory order changed proposal: base=%+v reversed=%+v", basePreview, reversedPreview)
	}

	baseInventoryEvidence := []confluenceAttachmentDeleteEvidence{
		confluenceAttachmentDeleteEvidenceFromAttachment(baseInventory.Attachments[0]),
		confluenceAttachmentDeleteEvidenceFromAttachment(baseInventory.Attachments[1]),
	}
	base := confluenceAttachmentDeleteSnapshot{
		page:          confluenceAttachmentDeletePageEvidenceFromResource(confluenceAttachmentDeletePage(7)),
		backendSHA256: "backend", attachment: baseInventoryEvidence[0], targetPresent: true,
		inventory: baseInventoryEvidence,
	}
	base.inventorySHA256 = confluenceAttachmentInventoryHash(base.inventory)
	proposal := func(snapshot confluenceAttachmentDeleteSnapshot) string {
		snapshot.inventorySHA256 = confluenceAttachmentInventoryHash(snapshot.inventory)
		expected := confluenceAttachmentInventoryWithout(snapshot.inventory, "100")
		return confluenceAttachmentDeleteProposalHash(snapshot, confluenceAttachmentInventoryHash(expected))
	}
	baseHash := proposal(base)
	tests := map[string]func(*confluenceAttachmentDeleteSnapshot){
		"backend":         func(v *confluenceAttachmentDeleteSnapshot) { v.backendSHA256 = "other" },
		"page id":         func(v *confluenceAttachmentDeleteSnapshot) { v.page.ID = "43" },
		"page version":    func(v *confluenceAttachmentDeleteSnapshot) { v.page.Version++ },
		"page body hash":  func(v *confluenceAttachmentDeleteSnapshot) { v.page.BodySHA256 = "other" },
		"page body bytes": func(v *confluenceAttachmentDeleteSnapshot) { v.page.BodyBytes++ },
		"page title":      func(v *confluenceAttachmentDeleteSnapshot) { v.page.TitleSHA256 = "other" },
		"page space":      func(v *confluenceAttachmentDeleteSnapshot) { v.page.Space = "OTHER" },
		"page parent":     func(v *confluenceAttachmentDeleteSnapshot) { v.page.Parent = "11" },
		"page hierarchy":  func(v *confluenceAttachmentDeleteSnapshot) { v.page.HierarchySHA256 = "other" },
		"target id": func(v *confluenceAttachmentDeleteSnapshot) {
			v.attachment.ID, v.inventory[0].ID = "101", "101"
		},
		"target title": func(v *confluenceAttachmentDeleteSnapshot) {
			v.attachment.TitleSHA256, v.inventory[0].TitleSHA256 = "other", "other"
		},
		"target media type": func(v *confluenceAttachmentDeleteSnapshot) {
			v.attachment.MediaTypeSHA256, v.inventory[0].MediaTypeSHA256 = "other", "other"
		},
		"target comment": func(v *confluenceAttachmentDeleteSnapshot) {
			v.attachment.CommentSHA256, v.inventory[0].CommentSHA256 = "other", "other"
		},
		"target size": func(v *confluenceAttachmentDeleteSnapshot) {
			v.attachment.FileSize, v.inventory[0].FileSize = 13, 13
		},
		"target version": func(v *confluenceAttachmentDeleteSnapshot) {
			v.attachment.Version, v.inventory[0].Version = 4, 4
		},
		"sibling id":         func(v *confluenceAttachmentDeleteSnapshot) { v.inventory[1].ID = "201" },
		"sibling title":      func(v *confluenceAttachmentDeleteSnapshot) { v.inventory[1].TitleSHA256 = "other" },
		"sibling media type": func(v *confluenceAttachmentDeleteSnapshot) { v.inventory[1].MediaTypeSHA256 = "other" },
		"sibling comment":    func(v *confluenceAttachmentDeleteSnapshot) { v.inventory[1].CommentSHA256 = "other" },
		"sibling size":       func(v *confluenceAttachmentDeleteSnapshot) { v.inventory[1].FileSize++ },
		"sibling version":    func(v *confluenceAttachmentDeleteSnapshot) { v.inventory[1].Version++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := base
			changed.inventory = append([]confluenceAttachmentDeleteEvidence(nil), base.inventory...)
			mutate(&changed)
			if got := proposal(changed); got == baseHash {
				t.Fatalf("proposal hash did not bind %s", name)
			}
		})
	}
}

func TestConfluenceAttachmentDeleteApplyGuardsPrecedeReads(t *testing.T) {
	tests := map[string]struct {
		pageID       string
		attachmentID string
		opts         ConfluenceAttachmentDeleteOpts
	}{
		"page id":       {pageID: "page", attachmentID: "100", opts: ConfluenceAttachmentDeleteOpts{}},
		"attachment id": {pageID: "42", attachmentID: "attachment", opts: ConfluenceAttachmentDeleteOpts{}},
		"confirmation": {pageID: "42", attachmentID: "100", opts: ConfluenceAttachmentDeleteOpts{
			Apply: true, Confirm: "wrong", ExpectedPageVersion: 7, ExpectedProposalHash: "hash"}},
		"version": {pageID: "42", attachmentID: "100", opts: ConfluenceAttachmentDeleteOpts{
			Apply: true, Confirm: "DELETE", ExpectedProposalHash: "hash"}},
		"hash": {pageID: "42", attachmentID: "100", opts: ConfluenceAttachmentDeleteOpts{
			Apply: true, Confirm: "DELETE", ExpectedPageVersion: 7}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store := &confluenceAttachmentDeleteStore{}
			result, err := (&ConfluenceService{store: store, baseURL: confluenceAttachmentDeleteTestBackend}).DeleteAttachmentGuarded(
				context.Background(), test.pageID, test.attachmentID, test.opts)
			if result != nil || !errors.Is(err, domain.ErrUsage) || store.pageIndex != 0 || store.inventoryIndex != 0 || store.deleteCalls != 0 {
				t.Fatalf("result=%+v err=%v pages=%d inventories=%d deletes=%d", result, err, store.pageIndex, store.inventoryIndex, store.deleteCalls)
			}
		})
	}
}

func TestConfluenceAttachmentDeleteRejectsPartialAndMalformedInventories(t *testing.T) {
	validAttachment := domain.Attachment{ID: "100", Title: "target.txt", Version: 1}
	tests := map[string]domain.AttachmentInventory{
		"page limit":             {PartialReason: domain.AttachmentPartialPageLimit, Attachments: []domain.Attachment{validAttachment}},
		"item limit":             {PartialReason: domain.AttachmentPartialItemLimit, Attachments: []domain.Attachment{validAttachment}},
		"stalled":                {PartialReason: domain.AttachmentPartialPaginationStalled, Attachments: []domain.Attachment{validAttachment}},
		"legacy":                 {PartialReason: domain.AttachmentPartialLegacyUnqualified, Attachments: []domain.Attachment{validAttachment}},
		"nil collection":         {Complete: true},
		"complete with reason":   {Complete: true, PartialReason: domain.AttachmentPartialPageLimit, Attachments: []domain.Attachment{validAttachment}},
		"partial without reason": {Attachments: []domain.Attachment{validAttachment}},
		"duplicate id":           {Complete: true, Attachments: []domain.Attachment{validAttachment, validAttachment}},
		"noncanonical id":        {Complete: true, Attachments: []domain.Attachment{{ID: "attachment", Title: "target.txt", Version: 1}}},
		"empty title":            {Complete: true, Attachments: []domain.Attachment{{ID: "100", Title: " ", Version: 1}}},
		"zero version":           {Complete: true, Attachments: []domain.Attachment{{ID: "100", Title: "target.txt"}}},
		"negative size":          {Complete: true, Attachments: []domain.Attachment{{ID: "100", Title: "target.txt", Version: 1, FileSize: -1}}},
	}
	for name, inventory := range tests {
		t.Run(name, func(t *testing.T) {
			store := &confluenceAttachmentDeleteStore{
				pageReads:      confluenceAttachmentDeletePageQueue(confluenceAttachmentDeletePage(7), 1),
				inventoryReads: []confluenceAttachmentDeleteInventoryRead{{inventory: inventory}},
			}
			result, err := (&ConfluenceService{store: store, baseURL: confluenceAttachmentDeleteTestBackend}).DeleteAttachmentGuarded(
				context.Background(), "42", "100", ConfluenceAttachmentDeleteOpts{})
			if result != nil || !errors.Is(err, domain.ErrCheckFailed) || store.deleteCalls != 0 {
				t.Fatalf("result=%+v err=%v deletes=%d", result, err, store.deleteCalls)
			}
		})
	}
}

func TestConfluenceAttachmentDeleteRejectsPageDriftAcrossInventory(t *testing.T) {
	store := &confluenceAttachmentDeleteStore{
		pageReads: []confluenceAttachmentDeletePageRead{
			{page: confluenceAttachmentDeletePage(7)},
			{page: confluenceAttachmentDeletePage(8)},
		},
		inventoryReads: confluenceAttachmentDeleteInventoryQueue(confluenceAttachmentDeleteBaseInventory(), 2),
	}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceAttachmentDeleteTestBackend}).DeleteAttachmentGuarded(
		context.Background(), "42", "100", ConfluenceAttachmentDeleteOpts{})
	if result != nil || !errors.Is(err, domain.ErrCheckFailed) || store.deleteCalls != 0 {
		t.Fatalf("result=%+v err=%v deletes=%d", result, err, store.deleteCalls)
	}
}

func TestConfluenceAttachmentDeleteRejectsConsecutiveCompleteInventoryDrift(t *testing.T) {
	shifted := confluenceAttachmentDeleteBaseInventory()
	shifted.Attachments[1] = domain.Attachment{
		ID: "201", Title: "new-sibling.png", MediaType: "image/png", FileSize: 35, Version: 1, Comment: "concurrent child",
	}
	store := &confluenceAttachmentDeleteStore{
		pageReads: confluenceAttachmentDeletePageQueue(confluenceAttachmentDeletePage(7), 1),
		inventoryReads: []confluenceAttachmentDeleteInventoryRead{
			{inventory: confluenceAttachmentDeleteBaseInventory()},
			{inventory: shifted},
		},
	}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceAttachmentDeleteTestBackend}).DeleteAttachmentGuarded(
		context.Background(), "42", "100", ConfluenceAttachmentDeleteOpts{})
	if result != nil || !errors.Is(err, domain.ErrCheckFailed) || store.inventoryIndex != 2 || store.pageIndex != 1 || store.deleteCalls != 0 {
		t.Fatalf("result=%+v err=%v pages=%d inventories=%d deletes=%d", result, err, store.pageIndex, store.inventoryIndex, store.deleteCalls)
	}
}

func TestConfluenceAttachmentDeleteReviewedVersionAndHashMismatchBlockWrite(t *testing.T) {
	preview := previewConfluenceAttachmentDelete(t, confluenceAttachmentDeleteBaseInventory())
	tests := map[string]ConfluenceAttachmentDeleteOpts{
		"version": {Apply: true, Confirm: "DELETE", ExpectedPageVersion: 8, ExpectedProposalHash: preview.ProposalHash},
		"hash":    {Apply: true, Confirm: "DELETE", ExpectedPageVersion: 7, ExpectedProposalHash: strings.Repeat("0", 64)},
	}
	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			store := &confluenceAttachmentDeleteStore{
				pageReads:      confluenceAttachmentDeletePageQueue(confluenceAttachmentDeletePage(7), 2),
				inventoryReads: confluenceAttachmentDeleteInventoryQueue(confluenceAttachmentDeleteBaseInventory(), 2),
			}
			result, err := (&ConfluenceService{store: store, baseURL: confluenceAttachmentDeleteTestBackend}).DeleteAttachmentGuarded(
				context.Background(), "42", "100", opts)
			if result == nil || result.Status != "blocked" || result.WriteAttempted || !errors.Is(err, domain.ErrCheckFailed) || store.deleteCalls != 0 {
				t.Fatalf("result=%+v err=%v deletes=%d", result, err, store.deleteCalls)
			}
		})
	}
}

func TestConfluenceAttachmentDeleteFullInventoryDriftBlocksBeforeWrite(t *testing.T) {
	preview := previewConfluenceAttachmentDelete(t, confluenceAttachmentDeleteBaseInventory())
	drifted := confluenceAttachmentDeleteBaseInventory()
	drifted.Attachments[1].Comment = "changed sibling comment"
	store := &confluenceAttachmentDeleteStore{
		pageReads:      confluenceAttachmentDeletePageQueue(confluenceAttachmentDeletePage(7), 2),
		inventoryReads: confluenceAttachmentDeleteInventoryQueue(drifted, 2),
	}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceAttachmentDeleteTestBackend}).DeleteAttachmentGuarded(
		context.Background(), "42", "100", ConfluenceAttachmentDeleteOpts{
			Apply: true, Confirm: "DELETE", ExpectedPageVersion: 7, ExpectedProposalHash: preview.ProposalHash,
		})
	if result == nil || result.Status != "blocked" || result.WriteAttempted || !errors.Is(err, domain.ErrCheckFailed) || store.deleteCalls != 0 {
		t.Fatalf("result=%+v err=%v deletes=%d", result, err, store.deleteCalls)
	}
}

func TestConfluenceAttachmentDeleteApplySuccessAndAmbiguousRecovery(t *testing.T) {
	preview := previewConfluenceAttachmentDelete(t, confluenceAttachmentDeleteBaseInventory())
	for name, writeErr := range map[string]error{
		"success":            nil,
		"ambiguous recovery": errors.New("private connection detail"),
	} {
		t.Run(name, func(t *testing.T) {
			store := &confluenceAttachmentDeleteStore{
				pageReads: confluenceAttachmentDeletePageQueue(confluenceAttachmentDeletePage(7), 4),
				inventoryReads: []confluenceAttachmentDeleteInventoryRead{
					{inventory: confluenceAttachmentDeleteBaseInventory()},
					{inventory: confluenceAttachmentDeleteBaseInventory()},
					{inventory: confluenceAttachmentDeleteExpectedInventory()},
					{inventory: confluenceAttachmentDeleteExpectedInventory()},
				},
				deleteErr: writeErr,
			}
			result, err := (&ConfluenceService{store: store, baseURL: confluenceAttachmentDeleteTestBackend}).DeleteAttachmentGuarded(
				context.Background(), "42", "100", ConfluenceAttachmentDeleteOpts{
					Apply: true, Confirm: "DELETE", ExpectedPageVersion: 7, ExpectedProposalHash: preview.ProposalHash,
				})
			wantStatus := "applied"
			if writeErr != nil {
				wantStatus = "recovered"
			}
			if err != nil || result == nil || result.Status != wantStatus || !result.WriteAttempted || !result.Reconciled || !result.Complete ||
				result.ObservedState != "absent" || result.FinalCount != 1 || result.FinalSHA256 != result.ExpectedFinalSHA256 ||
				store.deleteCalls != 1 || store.deleteID != "100" || !store.deleteSingle {
				t.Fatalf("result=%+v err=%v store=%+v", result, err, store)
			}
			if len(store.listSingle) != 4 || !store.listSingle[0] || !store.listSingle[1] || !store.listSingle[2] || !store.listSingle[3] ||
				!store.listRedacted[0] || !store.listRedacted[1] || !store.listRedacted[2] || !store.listRedacted[3] {
				t.Fatalf("inventory request policies: single=%v redacted=%v", store.listSingle, store.listRedacted)
			}
		})
	}
}

func TestConfluenceAttachmentDeleteUnexpectedReadbacksAreOutcomeUnknown(t *testing.T) {
	preview := previewConfluenceAttachmentDelete(t, confluenceAttachmentDeleteBaseInventory())
	retained := confluenceAttachmentDeleteBaseInventory()
	siblingDrift := confluenceAttachmentDeleteExpectedInventory()
	siblingDrift.Attachments[0].Version++
	tests := []struct {
		name           string
		post           []confluenceAttachmentDeleteInventoryRead
		wantComplete   bool
		wantReconciled bool
		wantState      string
	}{
		{name: "target retained", post: confluenceAttachmentDeleteInventoryQueue(retained, 2), wantComplete: true, wantReconciled: true, wantState: "present"},
		{name: "sibling drift", post: confluenceAttachmentDeleteInventoryQueue(siblingDrift, 2), wantComplete: true, wantReconciled: true, wantState: "absent"},
		{name: "post read failure", post: []confluenceAttachmentDeleteInventoryRead{{err: errors.New("private backend detail")}}, wantState: "unavailable"},
		{name: "post partial", post: []confluenceAttachmentDeleteInventoryRead{{inventory: domain.AttachmentInventory{
			PartialReason: domain.AttachmentPartialPageLimit, Attachments: siblingDrift.Attachments,
		}}}, wantState: "unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inventoryReads := confluenceAttachmentDeleteInventoryQueue(confluenceAttachmentDeleteBaseInventory(), 2)
			inventoryReads = append(inventoryReads, test.post...)
			store := &confluenceAttachmentDeleteStore{
				pageReads:      confluenceAttachmentDeletePageQueue(confluenceAttachmentDeletePage(7), 4),
				inventoryReads: inventoryReads,
			}
			result, err := (&ConfluenceService{store: store, baseURL: confluenceAttachmentDeleteTestBackend}).DeleteAttachmentGuarded(
				context.Background(), "42", "100", ConfluenceAttachmentDeleteOpts{
					Apply: true, Confirm: "DELETE", ExpectedPageVersion: 7, ExpectedProposalHash: preview.ProposalHash,
				})
			var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
			if result == nil || result.Status != "outcome_unknown" || result.Complete != test.wantComplete || result.Reconciled != test.wantReconciled ||
				result.ObservedState != test.wantState || !errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &ambiguous) ||
				!ambiguous.DiagnosticAmbiguousWrite() || store.deleteCalls != 1 {
				t.Fatalf("result=%+v err=%v deletes=%d", result, err, store.deleteCalls)
			}
			if strings.Contains(err.Error(), "private backend detail") {
				t.Fatalf("error leaked backend detail: %v", err)
			}
		})
	}
}

func TestConfluenceAttachmentDeleteFinalPageEvidenceDriftIsOutcomeUnknown(t *testing.T) {
	preview := previewConfluenceAttachmentDelete(t, confluenceAttachmentDeleteBaseInventory())
	store := &confluenceAttachmentDeleteStore{
		pageReads: []confluenceAttachmentDeletePageRead{
			{page: confluenceAttachmentDeletePage(7)},
			{page: confluenceAttachmentDeletePage(7)},
			{page: confluenceAttachmentDeletePage(8)},
			{page: confluenceAttachmentDeletePage(8)},
		},
		inventoryReads: append(
			confluenceAttachmentDeleteInventoryQueue(confluenceAttachmentDeleteBaseInventory(), 2),
			confluenceAttachmentDeleteInventoryQueue(confluenceAttachmentDeleteExpectedInventory(), 2)...,
		),
	}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceAttachmentDeleteTestBackend}).DeleteAttachmentGuarded(
		context.Background(), "42", "100", ConfluenceAttachmentDeleteOpts{
			Apply: true, Confirm: "DELETE", ExpectedPageVersion: 7, ExpectedProposalHash: preview.ProposalHash,
		})
	var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
	if result == nil || result.Status != "outcome_unknown" || !result.Complete || !result.Reconciled || result.ObservedState != "absent" ||
		result.FinalSHA256 != result.ExpectedFinalSHA256 || result.FinalPageVersion != 8 || !errors.Is(err, domain.ErrCheckFailed) ||
		!errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() || store.deleteCalls != 1 {
		t.Fatalf("result=%+v err=%v deletes=%d", result, err, store.deleteCalls)
	}
}

func TestConfluenceAttachmentDeleteDefinitiveRejectionIsNotAppliedAndContentFree(t *testing.T) {
	preview := previewConfluenceAttachmentDelete(t, confluenceAttachmentDeleteBaseInventory())
	store := &confluenceAttachmentDeleteStore{
		pageReads:      confluenceAttachmentDeletePageQueue(confluenceAttachmentDeletePage(7), 2),
		inventoryReads: confluenceAttachmentDeleteInventoryQueue(confluenceAttachmentDeleteBaseInventory(), 2),
		deleteErr:      confluenceAttachmentDeleteHTTPError{status: 403, sentinel: domain.ErrForbidden, detail: "private backend rejection"},
	}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceAttachmentDeleteTestBackend}).DeleteAttachmentGuarded(
		context.Background(), "42", "100", ConfluenceAttachmentDeleteOpts{
			Apply: true, Confirm: "DELETE", ExpectedPageVersion: 7, ExpectedProposalHash: preview.ProposalHash,
		})
	if result == nil || result.Status != "not_applied" || !result.WriteAttempted || result.Reconciled ||
		!errors.Is(err, domain.ErrForbidden) || store.deleteCalls != 1 || strings.Contains(err.Error(), "private backend rejection") {
		t.Fatalf("result=%+v err=%v deletes=%d", result, err, store.deleteCalls)
	}
}

func TestConfluenceAttachmentDeleteResultsAndErrorsDoNotExposeAttachmentContent(t *testing.T) {
	const sensitiveTitle = "private-title.txt"
	const sensitiveComment = "private attachment comment"
	inventory := confluenceAttachmentDeleteInventory(domain.Attachment{
		ID: "100", Title: sensitiveTitle, MediaType: "text/plain", FileSize: 9, Version: 1, Comment: sensitiveComment,
	})
	preview := previewConfluenceAttachmentDelete(t, inventory)
	formatted := fmt.Sprintf("%+v", preview)
	if strings.Contains(formatted, sensitiveTitle) || strings.Contains(formatted, sensitiveComment) || preview.AttachmentTitleSHA256 == "" {
		t.Fatalf("preview exposed attachment content: %+v", preview)
	}

	store := &confluenceAttachmentDeleteStore{
		pageReads:      confluenceAttachmentDeletePageQueue(confluenceAttachmentDeletePage(7), 2),
		inventoryReads: confluenceAttachmentDeleteInventoryQueue(inventory, 2),
	}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceAttachmentDeleteTestBackend}).DeleteAttachmentGuarded(
		context.Background(), "42", "100", ConfluenceAttachmentDeleteOpts{
			Apply: true, Confirm: "DELETE", ExpectedPageVersion: 7, ExpectedProposalHash: strings.Repeat("0", 64),
		})
	if result == nil || result.Status != "blocked" || err == nil || strings.Contains(err.Error(), sensitiveTitle) || strings.Contains(err.Error(), sensitiveComment) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
