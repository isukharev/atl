package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// legacyAttachmentStore implements only the compatibility listing, so the
// service must fall back and refuse to claim completeness.
type legacyAttachmentStore struct {
	domain.DocStore
	meta        *domain.PageMeta
	metaErr     error
	attachments []domain.Attachment
	listErr     error
	listedID    string
	listCalls   int
}

func (s *legacyAttachmentStore) GetMeta(context.Context, string) (*domain.PageMeta, error) {
	return s.meta, s.metaErr
}

func (s *legacyAttachmentStore) ListAttachments(_ context.Context, id string) ([]domain.Attachment, error) {
	s.listedID, s.listCalls = id, s.listCalls+1
	return s.attachments, s.listErr
}

// qualifiedAttachmentStore additionally implements the optional capability.
type qualifiedAttachmentStore struct {
	legacyAttachmentStore
	inventory domain.AttachmentInventory
	qualified int
}

func (s *qualifiedAttachmentStore) ListAttachmentsQualified(_ context.Context, id string) (domain.AttachmentInventory, error) {
	s.listedID, s.qualified = id, s.qualified+1
	return s.inventory, s.listErr
}

func TestAttachmentInventoryQualifiesCompleteListing(t *testing.T) {
	store := &qualifiedAttachmentStore{
		legacyAttachmentStore: legacyAttachmentStore{meta: &domain.PageMeta{ID: "12345", Version: 7}},
		inventory: domain.AttachmentInventory{Complete: true, Attachments: []domain.Attachment{
			{ID: "att1", Title: "diagram.png", MediaType: "image/png", FileSize: 42, Version: 2, Comment: "internal", DownPath: "/download/x"},
		}},
	}
	svc := &ConfluenceService{store: store}
	got, err := svc.AttachmentInventory(context.Background(), "12345", ConfluenceAttachmentInventoryOpts{})
	if err != nil {
		t.Fatalf("AttachmentInventory: %v", err)
	}
	if got.SchemaVersion != 1 || got.PageID != "12345" || got.PageVersion != 7 || got.Count != 1 ||
		!got.Complete || got.PartialReason != "" {
		t.Fatalf("result=%+v", got)
	}
	if store.qualified != 1 || store.listCalls != 0 || store.listedID != "12345" {
		t.Fatalf("qualified=%d legacy=%d id=%q", store.qualified, store.listCalls, store.listedID)
	}
	// The application result keeps the full backend record; only the MCP
	// projection sheds the comment and download path.
	if got.Attachments[0].Comment != "internal" {
		t.Fatalf("application result must preserve the legacy record: %+v", got.Attachments[0])
	}
}

func TestAttachmentInventoryCarriesStaticPartialReason(t *testing.T) {
	for _, reason := range []string{
		domain.AttachmentPartialPageLimit,
		domain.AttachmentPartialItemLimit,
		domain.AttachmentPartialPaginationStalled,
	} {
		t.Run(reason, func(t *testing.T) {
			store := &qualifiedAttachmentStore{
				legacyAttachmentStore: legacyAttachmentStore{meta: &domain.PageMeta{ID: "12345", Version: 3}},
				inventory:             domain.AttachmentInventory{PartialReason: reason, Attachments: []domain.Attachment{{ID: "att1"}}},
			}
			svc := &ConfluenceService{store: store}
			got, err := svc.AttachmentInventory(context.Background(), "12345", ConfluenceAttachmentInventoryOpts{})
			if err != nil {
				t.Fatalf("AttachmentInventory: %v", err)
			}
			if got.Complete || got.PartialReason != reason {
				t.Fatalf("result=%+v", got)
			}
		})
	}
}

// A backend that only implements the legacy port proves nothing about
// exhaustion, so its inventory must never read as complete.
func TestAttachmentInventoryFallsBackToLegacyUnqualified(t *testing.T) {
	store := &legacyAttachmentStore{
		meta:        &domain.PageMeta{ID: "12345", Version: 4},
		attachments: []domain.Attachment{{ID: "att1", Title: "a.png"}},
	}
	svc := &ConfluenceService{store: store}
	got, err := svc.AttachmentInventory(context.Background(), "12345", ConfluenceAttachmentInventoryOpts{})
	if err != nil {
		t.Fatalf("AttachmentInventory: %v", err)
	}
	if got.Complete || got.PartialReason != domain.AttachmentPartialLegacyUnqualified || got.Count != 1 {
		t.Fatalf("result=%+v", got)
	}
	if store.listCalls != 1 {
		t.Fatalf("legacy listing calls=%d", store.listCalls)
	}
}

// A legacy store may return a nil slice; the result must still be a proven
// non-nil array so an empty inventory is not confused with an absent read.
func TestAttachmentInventoryLegacyNilSliceBecomesEmptyArray(t *testing.T) {
	store := &legacyAttachmentStore{meta: &domain.PageMeta{ID: "12345", Version: 4}}
	svc := &ConfluenceService{store: store}
	got, err := svc.AttachmentInventory(context.Background(), "12345", ConfluenceAttachmentInventoryOpts{})
	if err != nil {
		t.Fatalf("AttachmentInventory: %v", err)
	}
	if got.Attachments == nil || len(got.Attachments) != 0 || got.Count != 0 {
		t.Fatalf("result=%+v", got)
	}
}

func TestAttachmentInventoryEmptyQualifiedListingIsNonNil(t *testing.T) {
	store := &qualifiedAttachmentStore{
		legacyAttachmentStore: legacyAttachmentStore{meta: &domain.PageMeta{ID: "12345", Version: 1}},
		inventory:             domain.AttachmentInventory{Complete: true, Attachments: []domain.Attachment{}},
	}
	svc := &ConfluenceService{store: store}
	got, err := svc.AttachmentInventory(context.Background(), "12345", ConfluenceAttachmentInventoryOpts{})
	if err != nil {
		t.Fatalf("AttachmentInventory: %v", err)
	}
	if got.Attachments == nil || got.Count != 0 || !got.Complete {
		t.Fatalf("result=%+v", got)
	}
}

// The gate must fail before the attachment request: an inventory read from a
// different revision is exactly the evidence mismatch this contract prevents.
func TestAttachmentInventoryVersionMismatchPreventsListing(t *testing.T) {
	store := &qualifiedAttachmentStore{
		legacyAttachmentStore: legacyAttachmentStore{meta: &domain.PageMeta{ID: "12345", Version: 9}},
		inventory:             domain.AttachmentInventory{Complete: true, Attachments: []domain.Attachment{{ID: "att1"}}},
	}
	svc := &ConfluenceService{store: store}
	_, err := svc.AttachmentInventory(context.Background(), "12345", ConfluenceAttachmentInventoryOpts{ExpectedPageVersion: 7})
	var mismatch *ConfluencePageVersionMismatchError
	if !errors.As(err, &mismatch) || mismatch.Expected != 7 || mismatch.Current != 9 {
		t.Fatalf("err=%v mismatch=%+v", err, mismatch)
	}
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("mismatch must unwrap to the check-failed sentinel: %v", err)
	}
	if store.qualified != 0 || store.listCalls != 0 {
		t.Fatalf("a refused gate must issue no attachment request: qualified=%d legacy=%d", store.qualified, store.listCalls)
	}
}

// The typed error is transport-facing evidence, so its text is fixed and names
// only the two integer versions.
func TestConfluencePageVersionMismatchErrorCarriesOnlyIntegers(t *testing.T) {
	err := &ConfluencePageVersionMismatchError{Expected: 7, Current: 9}
	want := domain.ErrCheckFailed.Error() + ": Confluence page version mismatch: expected 7, current 9"
	if err.Error() != want {
		t.Fatalf("message=%q want %q", err.Error(), want)
	}
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("mismatch must unwrap to the check-failed sentinel: %v", err)
	}
}

func TestAttachmentInventoryMatchingVersionProceeds(t *testing.T) {
	store := &qualifiedAttachmentStore{
		legacyAttachmentStore: legacyAttachmentStore{meta: &domain.PageMeta{ID: "12345", Version: 7}},
		inventory:             domain.AttachmentInventory{Complete: true, Attachments: []domain.Attachment{}},
	}
	svc := &ConfluenceService{store: store}
	got, err := svc.AttachmentInventory(context.Background(), "12345", ConfluenceAttachmentInventoryOpts{ExpectedPageVersion: 7})
	if err != nil {
		t.Fatalf("AttachmentInventory: %v", err)
	}
	if got.PageVersion != 7 || store.qualified != 1 {
		t.Fatalf("result=%+v qualified=%d", got, store.qualified)
	}
}

func TestAttachmentInventoryRejectsUnreconciledPageIdentity(t *testing.T) {
	for name, meta := range map[string]*domain.PageMeta{
		"missing metadata": nil,
		"empty id":         {ID: "  ", Version: 3},
		"other page":       {ID: "999", Version: 3},
		"absent version":   {ID: "12345", Version: 0},
	} {
		t.Run(name, func(t *testing.T) {
			store := &qualifiedAttachmentStore{legacyAttachmentStore: legacyAttachmentStore{meta: meta}}
			svc := &ConfluenceService{store: store}
			_, err := svc.AttachmentInventory(context.Background(), "12345", ConfluenceAttachmentInventoryOpts{})
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v", err)
			}
			if store.qualified != 0 {
				t.Fatalf("unreconciled identity must issue no attachment request")
			}
		})
	}
}

func TestAttachmentInventoryRejectsInvalidSnapshots(t *testing.T) {
	for name, inventory := range map[string]domain.AttachmentInventory{
		"nil collection":         {Complete: true},
		"complete with reason":   {Complete: true, PartialReason: domain.AttachmentPartialPageLimit, Attachments: []domain.Attachment{}},
		"partial without reason": {Attachments: []domain.Attachment{}},
		"unknown reason":         {PartialReason: "backend said so", Attachments: []domain.Attachment{}},
		"empty attachment id":    {Complete: true, Attachments: []domain.Attachment{{ID: " "}}},
		"duplicate id":           {Complete: true, Attachments: []domain.Attachment{{ID: "att1"}, {ID: "att1"}}},
		"negative size":          {Complete: true, Attachments: []domain.Attachment{{ID: "att1", FileSize: -1}}},
		"negative version":       {Complete: true, Attachments: []domain.Attachment{{ID: "att1", Version: -1}}},
		"zero version":           {Complete: true, Attachments: []domain.Attachment{{ID: "att1", Version: 0}}},
	} {
		t.Run(name, func(t *testing.T) {
			store := &qualifiedAttachmentStore{
				legacyAttachmentStore: legacyAttachmentStore{meta: &domain.PageMeta{ID: "12345", Version: 2}},
				inventory:             inventory,
			}
			svc := &ConfluenceService{store: store}
			if _, err := svc.AttachmentInventory(context.Background(), "12345", ConfluenceAttachmentInventoryOpts{}); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestAttachmentInventoryPropagatesBackendErrors(t *testing.T) {
	store := &qualifiedAttachmentStore{
		legacyAttachmentStore: legacyAttachmentStore{
			meta: &domain.PageMeta{ID: "12345", Version: 2}, listErr: domain.ErrForbidden,
		},
	}
	svc := &ConfluenceService{store: store}
	if _, err := svc.AttachmentInventory(context.Background(), "12345", ConfluenceAttachmentInventoryOpts{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err=%v", err)
	}
}

func TestAttachmentInventoryRejectsEmptyReference(t *testing.T) {
	store := &qualifiedAttachmentStore{}
	svc := &ConfluenceService{store: store}
	if _, err := svc.AttachmentInventory(context.Background(), "  ", ConfluenceAttachmentInventoryOpts{}); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("err=%v", err)
	}
}

// ConfluenceService.Attachments stays the unqualified compatibility surface:
// it resolves the reference, returns the slice, and reports no completeness.
func TestConfluenceServiceAttachmentsRemainsUnqualified(t *testing.T) {
	store := &qualifiedAttachmentStore{
		legacyAttachmentStore: legacyAttachmentStore{
			meta:        &domain.PageMeta{ID: "12345", Version: 7},
			attachments: []domain.Attachment{{ID: "att1", Title: "a.png", Comment: "kept"}},
		},
	}
	svc := &ConfluenceService{store: store}
	got, err := svc.Attachments(context.Background(), "12345")
	if err != nil {
		t.Fatalf("Attachments: %v", err)
	}
	if len(got) != 1 || got[0].ID != "att1" || got[0].Comment != "kept" {
		t.Fatalf("attachments=%+v", got)
	}
	if store.listCalls != 1 {
		t.Fatalf("legacy calls=%d", store.listCalls)
	}
}

// The MCP-facing projection must shed the author comment and download path and
// keep the collection non-nil.
func TestProjectConfluenceAttachmentInventoryDropsCommentAndDownloadPath(t *testing.T) {
	projected := ProjectConfluenceAttachmentInventory(&ConfluenceAttachmentInventoryResult{
		SchemaVersion: 1, PageID: "12345", PageVersion: 7, Count: 1, Complete: true,
		Attachments: []domain.Attachment{
			{ID: "att1", Title: "diagram.png", MediaType: "image/png", FileSize: 42, Version: 2,
				Comment: "SYNTHETIC-COMMENT", DownPath: "/download/attachments/12345/diagram.png"},
		},
	})
	if projected == nil || len(projected.Attachments) != 1 {
		t.Fatalf("projection=%+v", projected)
	}
	attachment := projected.Attachments[0]
	if attachment.ID != "att1" || attachment.Title != "diagram.png" || attachment.MediaType != "image/png" ||
		attachment.FileSize != 42 || attachment.Version != 2 {
		t.Fatalf("attachment=%+v", attachment)
	}
	// The projection type has no comment or download-path member at all, so the
	// only way either could reappear is a future field addition.
	if fields := projectedAttachmentFieldNames(); len(fields) != 5 {
		t.Fatalf("sanitized attachment projection gained fields: %v", fields)
	}
	if ProjectConfluenceAttachmentInventory(nil) != nil {
		t.Fatal("a nil result must project to nil")
	}
}

func TestProjectConfluenceAttachmentInventoryKeepsEmptyArray(t *testing.T) {
	projected := ProjectConfluenceAttachmentInventory(&ConfluenceAttachmentInventoryResult{
		SchemaVersion: 1, PageID: "12345", PageVersion: 1, Complete: true, Attachments: []domain.Attachment{},
	})
	if projected.Attachments == nil || len(projected.Attachments) != 0 {
		t.Fatalf("projection=%+v", projected)
	}
}

// projectedAttachmentFieldNames lists the sanitized projection's members so a
// future field addition has to be reviewed rather than shipped silently.
func projectedAttachmentFieldNames() []string {
	viewType := reflect.TypeOf(ConfluenceAttachmentView{})
	names := make([]string, 0, viewType.NumField())
	for i := 0; i < viewType.NumField(); i++ {
		names = append(names, viewType.Field(i).Name)
	}
	return names
}
