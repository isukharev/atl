package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type qualifiedCommentStore struct {
	*recordingStore
	inventory domain.ConfluenceCommentInventory
	readErr   error
	readCalls int
	readOpts  domain.ConfluenceCommentReadOptions
}

func (s *qualifiedCommentStore) ListConfluenceComments(_ context.Context, _ string, opts domain.ConfluenceCommentReadOptions) (domain.ConfluenceCommentInventory, error) {
	s.readCalls++
	s.readOpts = opts
	return s.inventory, s.readErr
}

func completeCommentCapabilities() domain.ConfluenceCommentCapabilities {
	observed := domain.ConfluenceCapabilityObserved
	return domain.ConfluenceCommentCapabilities{
		Footer: observed, Inline: observed, Resolved: observed, DepthAll: observed,
		ThreadAncestry: observed, InlineProperties: observed, Resolution: observed,
	}
}

func completeQualifiedComments(records ...domain.ConfluenceCommentRecord) domain.ConfluenceCommentInventory {
	if records == nil {
		records = []domain.ConfluenceCommentRecord{}
	}
	return domain.ConfluenceCommentInventory{
		Comments: records, CommentsComplete: true, ThreadsComplete: true,
		PartialReasons: []string{}, Capabilities: completeCommentCapabilities(),
		Diagnostics: []domain.ConfluenceCommentDiagnostic{},
	}
}

func TestConfluenceCommentInventoryQualifiesThreadsAndAnchors(t *testing.T) {
	rootID := "101"
	store := &qualifiedCommentStore{
		recordingStore: &recordingStore{page: &domain.Resource{
			ID: "42", Version: 7, BodyPresent: true,
			Body: []byte(`<p>before <ac:inline-comment-marker ac:ref="ref-1"><strong>selected</strong> text</ac:inline-comment-marker> after</p>`),
		}},
		inventory: completeQualifiedComments(
			domain.ConfluenceCommentRecord{
				ID: "102", PageID: "42", ParentID: &rootID, RootID: &rootID,
				Relation:   domain.ConfluenceCommentRelationReply,
				Location:   domain.ConfluenceCommentLocationUnknown,
				Resolution: domain.ConfluenceCommentResolutionUnknown,
				Version:    1, CreatedAt: "2026-01-02", Body: "reply", BodyStorage: "<p>reply</p>",
			},
			domain.ConfluenceCommentRecord{
				ID: rootID, PageID: "42", RootID: &rootID,
				Relation:   domain.ConfluenceCommentRelationRoot,
				Location:   domain.ConfluenceCommentLocationInline,
				Resolution: domain.ConfluenceCommentResolutionOpen,
				Version:    2, AuthorID: "user-key", AuthorDisplayName: "Reader", CreatedAt: "2026-01-01",
				Body: "root", BodyStorage: "<p>root</p>", MarkerRef: "ref-1", OriginalSelection: "selected text",
			},
		),
	}

	result, err := (&ConfluenceService{store: store}).CommentInventory(context.Background(), "42", ConfluenceCommentInventoryOpts{})
	if err != nil {
		t.Fatalf("CommentInventory() error = %v", err)
	}
	if !result.Complete || !result.CommentsComplete || !result.ThreadsComplete || !result.AnchorsComplete {
		t.Fatalf("completeness = %+v", result)
	}
	if result.SchemaVersion != 2 || result.PageVersion != 7 || result.PageVersionGated || result.Count != 2 || result.RootCount != 1 {
		t.Fatalf("result aggregates = %+v", result)
	}
	if len(result.Comments) != 2 || result.Comments[0].ID != rootID || result.Comments[1].ID != "102" {
		t.Fatalf("thread order = %+v", result.Comments)
	}
	anchor := result.Comments[0].Anchor
	if anchor == nil || anchor.Status != domain.ConfluenceAnchorMatched || anchor.ObservedSelection != "selected text" {
		t.Fatalf("anchor = %+v", anchor)
	}
	if result.Comments[0].Author.ID != "user-key" || result.Comments[1].Anchor != nil {
		t.Fatalf("comment projection = %+v", result.Comments)
	}
	if !store.readOpts.DepthAll || len(store.readOpts.Locations) != 0 {
		t.Fatalf("read options = %+v", store.readOpts)
	}
}

func TestConfluenceCommentInventoryKeepsCompletenessDimensionsIndependent(t *testing.T) {
	rootID := "101"
	store := &qualifiedCommentStore{
		recordingStore: &recordingStore{page: &domain.Resource{ID: "42", Version: 7, Body: []byte(`<p>no marker</p>`)}},
		inventory: completeQualifiedComments(domain.ConfluenceCommentRecord{
			ID: rootID, PageID: "42", RootID: &rootID,
			Relation:   domain.ConfluenceCommentRelationRoot,
			Location:   domain.ConfluenceCommentLocationInline,
			Resolution: domain.ConfluenceCommentResolutionOpen,
			Version:    1, BodyStorage: "<p>x</p>", MarkerRef: "missing",
		}),
	}
	result, err := (&ConfluenceService{store: store}).CommentInventory(context.Background(), "42", ConfluenceCommentInventoryOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.CommentsComplete || !result.ThreadsComplete || result.AnchorsComplete || result.Complete {
		t.Fatalf("completeness = %+v", result)
	}
	if !reflect.DeepEqual(result.PartialReasons, []string{domain.ConfluenceCommentPartialAnchorMissing}) ||
		result.Comments[0].Anchor == nil || result.Comments[0].Anchor.Status != domain.ConfluenceAnchorMissing {
		t.Fatalf("anchor qualification = %+v", result)
	}
}

func TestConfluenceCommentInventoryAnchorAmbiguousAndUnavailable(t *testing.T) {
	rootID := "101"
	baseInventory := completeQualifiedComments(domain.ConfluenceCommentRecord{
		ID: rootID, PageID: "42", RootID: &rootID,
		Relation:   domain.ConfluenceCommentRelationRoot,
		Location:   domain.ConfluenceCommentLocationInline,
		Resolution: domain.ConfluenceCommentResolutionOpen,
		Version:    1, BodyStorage: "<p>x</p>", MarkerRef: "same",
	})
	for _, test := range []struct {
		name   string
		body   []byte
		omit   bool
		status domain.ConfluenceAnchorStatus
		reason string
	}{
		{"ambiguous", []byte(`<ac:inline-comment-marker ac:ref="same">one</ac:inline-comment-marker><ac:inline-comment-marker ac:ref="same">two</ac:inline-comment-marker>`), false, domain.ConfluenceAnchorAmbiguous, domain.ConfluenceCommentPartialAnchorAmbiguous},
		{"unavailable", nil, true, domain.ConfluenceAnchorUnavailable, domain.ConfluenceCommentPartialPageBodyUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &qualifiedCommentStore{
				recordingStore: &recordingStore{page: &domain.Resource{ID: "42", Version: 7, Body: test.body}, omitBody: test.omit},
				inventory:      baseInventory,
			}
			result, err := (&ConfluenceService{store: store}).CommentInventory(context.Background(), "42", ConfluenceCommentInventoryOpts{})
			if err != nil {
				t.Fatal(err)
			}
			if result.AnchorsComplete || result.Complete || result.Comments[0].Anchor == nil || result.Comments[0].Anchor.Status != test.status ||
				!containsAppString(result.PartialReasons, test.reason) {
				t.Fatalf("anchor result = %+v", result)
			}
		})
	}
}

func TestConfluenceCommentInventoryVersionGatePrecedesCommentRead(t *testing.T) {
	store := &qualifiedCommentStore{
		recordingStore: &recordingStore{page: &domain.Resource{ID: "42", Version: 8, Body: []byte("<p>x</p>")}},
		inventory:      completeQualifiedComments(),
	}
	_, err := (&ConfluenceService{store: store}).CommentInventory(context.Background(), "42", ConfluenceCommentInventoryOpts{ExpectedPageVersion: 7})
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("error = %v, want ErrVersionConflict", err)
	}
	if store.readCalls != 0 {
		t.Fatalf("qualified reader called %d times after failed version gate", store.readCalls)
	}
}

func TestConfluenceCommentInventoryPropagatesExplicitBounds(t *testing.T) {
	store := &qualifiedCommentStore{
		recordingStore: &recordingStore{page: &domain.Resource{ID: "42", Version: 7, BodyPresent: true, Body: []byte("<p>x</p>")}},
		inventory:      completeQualifiedComments(),
	}
	_, err := (&ConfluenceService{store: store}).CommentInventory(context.Background(), "42", ConfluenceCommentInventoryOpts{
		Location: "footer", MaxPages: 3, MaxItems: 17,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.readOpts.MaxPages != 3 || store.readOpts.MaxItems != 17 {
		t.Fatalf("read options = %+v", store.readOpts)
	}
}

func TestConfluenceCommentInventoryRejectsExplicitBoundsOnLegacyReader(t *testing.T) {
	store := &recordingStore{page: &domain.Resource{ID: "42", Version: 7, BodyPresent: true, Body: []byte("<p>x</p>")}}
	_, err := (&ConfluenceService{store: store}).CommentInventory(context.Background(), "42", ConfluenceCommentInventoryOpts{MaxPages: 1})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v, want ErrCheckFailed", err)
	}
}

func TestConfluenceCommentInventoryResolvedSelectorNormalizesLocation(t *testing.T) {
	rootID := "201"
	store := &qualifiedCommentStore{
		recordingStore: &recordingStore{page: &domain.Resource{ID: "42", Version: 7, Body: []byte(`<ac:inline-comment-marker ac:ref="r">selection</ac:inline-comment-marker>`)}},
		inventory: completeQualifiedComments(domain.ConfluenceCommentRecord{
			ID: rootID, PageID: "42", RootID: &rootID,
			Relation:   domain.ConfluenceCommentRelationRoot,
			Location:   domain.ConfluenceCommentLocationInline,
			Resolution: domain.ConfluenceCommentResolutionResolved,
			Version:    1, MarkerRef: "r", BodyStorage: "<p>x</p>",
		}),
	}
	result, err := (&ConfluenceService{store: store}).CommentInventory(context.Background(), "42", ConfluenceCommentInventoryOpts{Location: "resolved"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Comments) != 1 || result.Comments[0].Location != domain.ConfluenceCommentLocationInline ||
		result.Comments[0].Resolution != domain.ConfluenceCommentResolutionResolved {
		t.Fatalf("resolved projection = %+v", result.Comments)
	}
	if !reflect.DeepEqual(store.readOpts.Locations, []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorResolved}) {
		t.Fatalf("selected locations = %+v", store.readOpts.Locations)
	}
}

func TestConfluenceCommentInventoryResolvedSelectorDoesNotClassifyUnselectedMarkers(t *testing.T) {
	rootID := "201"
	store := &qualifiedCommentStore{
		recordingStore: &recordingStore{page: &domain.Resource{ID: "42", Version: 7, Body: []byte(`<ac:inline-comment-marker ac:ref="resolved">selection</ac:inline-comment-marker><ac:inline-comment-marker ac:ref="open">other</ac:inline-comment-marker>`)}},
		inventory: completeQualifiedComments(domain.ConfluenceCommentRecord{
			ID: rootID, PageID: "42", RootID: &rootID,
			Relation: domain.ConfluenceCommentRelationRoot, Location: domain.ConfluenceCommentLocationInline,
			Resolution: domain.ConfluenceCommentResolutionResolved, MarkerRef: "resolved", Version: 1,
		}),
	}
	result, err := (&ConfluenceService{store: store}).CommentInventory(context.Background(), "42", ConfluenceCommentInventoryOpts{Location: "resolved"})
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == domain.ConfluenceCommentDiagnosticOrphanMarker {
			t.Fatalf("resolved-only result classified unselected marker as orphan: %+v", result.Diagnostics)
		}
	}
}

func TestConfluenceCommentInventoryStateFilterIsPartialWhenResolutionUnknown(t *testing.T) {
	rootID := "101"
	store := &qualifiedCommentStore{
		recordingStore: &recordingStore{page: &domain.Resource{ID: "42", Version: 7, Body: []byte("<p>x</p>")}},
		inventory: completeQualifiedComments(domain.ConfluenceCommentRecord{
			ID: rootID, PageID: "42", RootID: &rootID,
			Relation: domain.ConfluenceCommentRelationRoot, Location: domain.ConfluenceCommentLocationFooter,
			Resolution: domain.ConfluenceCommentResolutionUnknown, Version: 1,
		}),
	}
	result, err := (&ConfluenceService{store: store}).CommentInventory(context.Background(), "42", ConfluenceCommentInventoryOpts{Location: "footer", State: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.CommentsComplete || result.Count != 0 ||
		!containsAppString(result.PartialReasons, domain.ConfluenceCommentPartialResolutionUnavailable) {
		t.Fatalf("state-filter qualification = %+v", result)
	}
}

func TestConfluenceCommentInventoryStateFilterDoesNotCreateOrphanMarker(t *testing.T) {
	rootID := "201"
	store := &qualifiedCommentStore{
		recordingStore: &recordingStore{page: &domain.Resource{ID: "42", Version: 7, Body: []byte(`<ac:inline-comment-marker ac:ref="resolved">selection</ac:inline-comment-marker>`)}},
		inventory: completeQualifiedComments(domain.ConfluenceCommentRecord{
			ID: rootID, PageID: "42", RootID: &rootID,
			Relation: domain.ConfluenceCommentRelationRoot, Location: domain.ConfluenceCommentLocationInline,
			Resolution: domain.ConfluenceCommentResolutionResolved, MarkerRef: "resolved", Version: 1,
		}),
	}
	result, err := (&ConfluenceService{store: store}).CommentInventory(context.Background(), "42", ConfluenceCommentInventoryOpts{Location: "inline", State: "open"})
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == domain.ConfluenceCommentDiagnosticOrphanMarker {
			t.Fatalf("state-filtered marker classified as orphan: %+v", result.Diagnostics)
		}
	}
}

func TestConfluenceCommentInventoryUnknownLocationMarkerIsNotOrphaned(t *testing.T) {
	rootID := "201"
	store := &qualifiedCommentStore{
		recordingStore: &recordingStore{page: &domain.Resource{ID: "42", Version: 7, Body: []byte(`<ac:inline-comment-marker ac:ref="qualified-later">selection</ac:inline-comment-marker>`)}},
		inventory: completeQualifiedComments(domain.ConfluenceCommentRecord{
			ID: rootID, PageID: "42", RootID: &rootID,
			Relation: domain.ConfluenceCommentRelationRoot, Location: domain.ConfluenceCommentLocationUnknown,
			Resolution: domain.ConfluenceCommentResolutionOpen, MarkerRef: "qualified-later", Version: 1,
		}),
	}
	store.inventory.CommentsComplete = false
	store.inventory.PartialReasons = []string{domain.ConfluenceCommentPartialLocationUnavailable}
	result, err := (&ConfluenceService{store: store}).CommentInventory(context.Background(), "42", ConfluenceCommentInventoryOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Comments[0].Anchor == nil || result.Comments[0].Anchor.Status != domain.ConfluenceAnchorMatched {
		t.Fatalf("unknown-location anchor = %+v", result.Comments[0].Anchor)
	}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == domain.ConfluenceCommentDiagnosticOrphanMarker {
			t.Fatalf("joined marker classified as orphan: %+v", result.Diagnostics)
		}
	}
}

func TestConfluenceCommentInventoryRootFilterPreservesUnknownThreadCompleteness(t *testing.T) {
	store := &qualifiedCommentStore{
		recordingStore: &recordingStore{page: &domain.Resource{ID: "42", Version: 7, Body: []byte("<p>x</p>")}},
		inventory: completeQualifiedComments(domain.ConfluenceCommentRecord{
			ID: "101", PageID: "42", Relation: domain.ConfluenceCommentRelationUnknown,
			Location: domain.ConfluenceCommentLocationFooter, Resolution: domain.ConfluenceCommentResolutionUnknown, Version: 1,
		}),
	}
	store.inventory.ThreadsComplete = false
	store.inventory.PartialReasons = []string{domain.ConfluenceCommentPartialMalformedAncestry}
	result, err := (&ConfluenceService{store: store}).CommentInventory(context.Background(), "42", ConfluenceCommentInventoryOpts{Location: "footer", Depth: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.ThreadsComplete || result.Count != 0 {
		t.Fatalf("root-filter qualification = %+v", result)
	}
}

func TestConfluenceCommentThreadExactSelection(t *testing.T) {
	rootID, otherID := "101", "201"
	store := &qualifiedCommentStore{
		recordingStore: &recordingStore{page: &domain.Resource{ID: "42", Version: 7, Body: []byte("<p>x</p>")}},
		inventory: completeQualifiedComments(
			domain.ConfluenceCommentRecord{ID: rootID, PageID: "42", RootID: &rootID, Relation: domain.ConfluenceCommentRelationRoot, Location: domain.ConfluenceCommentLocationFooter, Resolution: domain.ConfluenceCommentResolutionUnknown, Version: 1},
			domain.ConfluenceCommentRecord{ID: "102", PageID: "42", ParentID: &rootID, RootID: &rootID, Relation: domain.ConfluenceCommentRelationReply, Location: domain.ConfluenceCommentLocationUnknown, Resolution: domain.ConfluenceCommentResolutionUnknown, Version: 1},
			domain.ConfluenceCommentRecord{ID: otherID, PageID: "42", RootID: &otherID, Relation: domain.ConfluenceCommentRelationRoot, Location: domain.ConfluenceCommentLocationFooter, Resolution: domain.ConfluenceCommentResolutionUnknown, Version: 1},
		),
	}
	result, err := (&ConfluenceService{store: store}).CommentThread(context.Background(), "42", "102", 7)
	if err != nil {
		t.Fatal(err)
	}
	if result.Query.Mode != "thread" || !result.PageVersionGated || result.Count != 2 || result.RootCount != 1 || result.Comments[0].ID != rootID || result.Comments[1].ID != "102" {
		t.Fatalf("thread result = %+v", result)
	}
}

func TestConfluenceCommentThreadWithOptionsPropagatesExplicitBounds(t *testing.T) {
	rootID := "101"
	store := &qualifiedCommentStore{
		recordingStore: &recordingStore{page: &domain.Resource{ID: "42", Version: 7, BodyPresent: true, Body: []byte("<p>x</p>")}},
		inventory: completeQualifiedComments(domain.ConfluenceCommentRecord{
			ID: rootID, PageID: "42", RootID: &rootID, Relation: domain.ConfluenceCommentRelationRoot,
			Location: domain.ConfluenceCommentLocationFooter, Resolution: domain.ConfluenceCommentResolutionOpen,
			Version: 1, BodyStorage: "<p>body</p>",
		}),
	}
	_, err := (&ConfluenceService{store: store}).CommentThreadWithOptions(context.Background(), "42", rootID, ConfluenceCommentThreadOpts{
		ExpectedPageVersion: 7, MaxPages: 4, MaxItems: 23,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.readOpts.MaxPages != 4 || store.readOpts.MaxItems != 23 {
		t.Fatalf("read options = %+v", store.readOpts)
	}
}

func TestConfluenceCommentThreadAbsenceRequiresCompleteInventory(t *testing.T) {
	store := &qualifiedCommentStore{
		recordingStore: &recordingStore{page: &domain.Resource{ID: "42", Version: 7, Body: []byte("<p>x</p>")}},
		inventory:      completeQualifiedComments(),
	}
	_, err := (&ConfluenceService{store: store}).CommentThread(context.Background(), "42", "999", 0)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("complete absence error = %v, want ErrNotFound", err)
	}
	store.inventory.CommentsComplete = false
	store.inventory.PartialReasons = []string{domain.ConfluenceCommentPartialPageLimit}
	_, err = (&ConfluenceService{store: store}).CommentThread(context.Background(), "42", "999", 0)
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("partial absence error = %v, want ErrCheckFailed", err)
	}
}

func TestConfluenceCommentThreadAbsenceIgnoresUnrelatedAnchorPartial(t *testing.T) {
	store := &qualifiedCommentStore{
		recordingStore: &recordingStore{page: &domain.Resource{ID: "42", Version: 7, Body: []byte(`<ac:inline-comment-marker ac:ref="same">one</ac:inline-comment-marker><ac:inline-comment-marker ac:ref="same">two</ac:inline-comment-marker>`)}},
		inventory: completeQualifiedComments(domain.ConfluenceCommentRecord{
			ID: "101", PageID: "42", Relation: domain.ConfluenceCommentRelationRoot,
			Location: domain.ConfluenceCommentLocationInline, Resolution: domain.ConfluenceCommentResolutionOpen,
			RootID: func() *string { value := "101"; return &value }(), MarkerRef: "same", Version: 1,
		}),
	}
	_, err := (&ConfluenceService{store: store}).CommentThread(context.Background(), "42", "999", 0)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("anchor-partial absence error = %v, want ErrNotFound", err)
	}
}

func TestConfluenceCommentInventoryLegacyFallbackIsExplicitlyPartial(t *testing.T) {
	store := &recordingStore{
		page:     &domain.Resource{ID: "42", Version: 7, Body: []byte("<p>x</p>")},
		comments: []domain.Comment{{ID: "1", Author: "Reader", Body: "body", BodyStorage: "<p>body</p>"}},
	}
	result, err := (&ConfluenceService{store: store}).CommentInventory(context.Background(), "42", ConfluenceCommentInventoryOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.CommentsComplete || result.ThreadsComplete || result.AnchorsComplete ||
		!reflect.DeepEqual(result.PartialReasons, []string{domain.ConfluenceCommentPartialLegacyUnqualified}) {
		t.Fatalf("legacy qualification = %+v", result)
	}
}

func TestValidateConfluenceCommentInventoryOpts(t *testing.T) {
	for _, opts := range []ConfluenceCommentInventoryOpts{
		{Location: "else"}, {State: "else"}, {Depth: "else"}, {ExpectedPageVersion: -1},
		{MaxPages: -1}, {MaxItems: -1},
	} {
		if err := ValidateConfluenceCommentInventoryOpts(opts); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("ValidateConfluenceCommentInventoryOpts(%+v) = %v, want ErrUsage", opts, err)
		}
	}
}

func TestProjectConfluenceCommentListViewIsBodyAndSelectionFree(t *testing.T) {
	result := completeCommentProjectionResult("list")
	view, err := ProjectConfluenceCommentListView(result, ConfluenceCommentViewBounds{MaxCommentPages: 32, MaxItems: 100, MaxBytes: 128 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if view.SchemaVersion != ConfluenceCommentViewSchemaVersion || view.Count != 1 || view.Comments[0].ID != "101" ||
		view.Comments[0].Anchor == nil || view.Comments[0].Anchor.Status != domain.ConfluenceAnchorMatched {
		t.Fatalf("view = %+v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{
		[]byte(`"body"`), []byte(`"body_text"`), []byte(`"body_storage"`),
		[]byte("PRIVATE-NATIVE"), []byte("PRIVATE-ORIGINAL"), []byte("PRIVATE-OBSERVED"),
	} {
		if bytes.Contains(encoded, forbidden) {
			t.Fatalf("list projection leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestProjectConfluenceCommentThreadViewDerivesPlainTextFromNativeStorage(t *testing.T) {
	result := completeCommentProjectionResult("thread")
	result.Comments[0].Body = "PRIVATE-UNTRUSTED-CONVENIENCE-FIELD"
	result.Comments[0].BodyStorage = `<p>Hello <strong>thread</strong></p>`
	view, err := ProjectConfluenceCommentThreadView(result, ConfluenceCommentViewBounds{MaxCommentPages: 32, MaxItems: 100, MaxBytes: 256 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if view.Comments[0].BodyText == nil || *view.Comments[0].BodyText != "Hello thread" {
		t.Fatalf("body text = %+v", view.Comments[0].BodyText)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"PRIVATE-UNTRUSTED-CONVENIENCE-FIELD", "<strong>", "body_storage", "PRIVATE-ORIGINAL", "PRIVATE-OBSERVED",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("thread projection leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestProjectConfluenceCommentThreadViewFailsClosedOnMalformedNativeStorage(t *testing.T) {
	result := completeCommentProjectionResult("thread")
	result.Comments[0].Body = "raw fallback"
	result.Comments[0].BodyStorage = `<p><broken></p>`
	view, err := ProjectConfluenceCommentThreadView(result, ConfluenceCommentViewBounds{MaxCommentPages: 1, MaxItems: 1, MaxBytes: 1024})
	if view != nil || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("view=%+v error=%v, want fail-closed projection", view, err)
	}
}

func TestConfluenceCommentViewValidatorsRejectUnreconciledEvidence(t *testing.T) {
	badSource := completeCommentProjectionResult("list")
	badSource.Comments[0].ID = "0101"
	if projected, err := ProjectConfluenceCommentListView(badSource, ConfluenceCommentViewBounds{MaxCommentPages: 1, MaxItems: 1, MaxBytes: 1024}); projected != nil || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("non-canonical source identity projected=%+v error=%v", projected, err)
	}
	badSource = completeCommentProjectionResult("list")
	badSource.Comments[0].Author.ID = "reader@example.invalid"
	if projected, err := ProjectConfluenceCommentListView(badSource, ConfluenceCommentViewBounds{MaxCommentPages: 1, MaxItems: 1, MaxBytes: 1024}); projected != nil || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("email-like source author projected=%+v error=%v", projected, err)
	}
	for name, mutate := range map[string]func(*ConfluenceCommentInventoryResult){
		"URL timestamp": func(result *ConfluenceCommentInventoryResult) {
			result.Comments[0].UpdatedAt = "https://private.invalid/value"
		},
		"email marker": func(result *ConfluenceCommentInventoryResult) {
			result.Comments[0].Anchor.MarkerRef = "owner@example.invalid"
		},
		"URL diagnostic marker": func(result *ConfluenceCommentInventoryResult) {
			result.Diagnostics = []ConfluenceCommentResultDiagnostic{{Code: domain.ConfluenceCommentDiagnosticOrphanMarker, MarkerRef: "https://private.invalid"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := completeCommentProjectionResult("list")
			mutate(bad)
			if projected, err := ProjectConfluenceCommentListView(bad, ConfluenceCommentViewBounds{MaxCommentPages: 1, MaxItems: 1, MaxBytes: 1024}); projected != nil || !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("privacy-unsafe source projected=%+v error=%v", projected, err)
			}
		})
	}

	list, err := ProjectConfluenceCommentListView(completeCommentProjectionResult("list"), ConfluenceCommentViewBounds{MaxCommentPages: 1, MaxItems: 1, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := ProjectConfluenceCommentThreadView(completeCommentProjectionResult("thread"), ConfluenceCommentViewBounds{MaxCommentPages: 1, MaxItems: 1, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}

	badList := *list
	badList.Comments = nil
	if err := ValidateConfluenceCommentListView(&badList); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("nil list collection error = %v", err)
	}
	badList = *list
	badList.Bounds.MaxItems = 0
	if err := ValidateConfluenceCommentListView(&badList); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("invalid list bounds error = %v", err)
	}
	badList = *list
	badList.RootCount = 0
	if err := ValidateConfluenceCommentListView(&badList); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("invalid list root count error = %v", err)
	}
	badList = *list
	badList.Comments = append([]ConfluenceCommentListViewRecord(nil), list.Comments...)
	badList.Comments[0].Relation = domain.ConfluenceCommentRelation("invented")
	if err := ValidateConfluenceCommentListView(&badList); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("invalid list relation error = %v", err)
	}
	badList = *list
	badList.Comments = append([]ConfluenceCommentListViewRecord(nil), list.Comments...)
	badList.Comments[0].ID = "0101"
	if err := ValidateConfluenceCommentListView(&badList); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("non-canonical list identity error = %v", err)
	}
	badList = *list
	badList.Comments = append([]ConfluenceCommentListViewRecord(nil), list.Comments...)
	badList.Comments[0].Anchor = &ConfluenceCommentViewAnchor{Status: domain.ConfluenceAnchorMatched}
	if err := ValidateConfluenceCommentListView(&badList); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("invalid list anchor error = %v", err)
	}

	badThread := *thread
	badThread.Comments = append([]ConfluenceCommentThreadViewRecord(nil), thread.Comments...)
	badThread.Comments[0].BodyText = nil
	if err := ValidateConfluenceCommentThreadView(&badThread); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unqualified missing body error = %v", err)
	}
	badThread = *thread
	badThread.Diagnostics = []ConfluenceCommentViewDiagnostic{{Code: "backend prose"}}
	if err := ValidateConfluenceCommentThreadView(&badThread); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("invalid diagnostic error = %v", err)
	}
	badThread = *thread
	badThread.Diagnostics = []ConfluenceCommentViewDiagnostic{{Code: domain.ConfluenceCommentDiagnosticOrphanMarker, CommentID: "not-an-id"}}
	if err := ValidateConfluenceCommentThreadView(&badThread); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("non-canonical diagnostic identity error = %v", err)
	}
	replyRoot, missingParent := "101", "999"
	if err := validateConfluenceCommentThreadAncestry(map[string]ConfluenceCommentResultRecord{
		"101": {ID: "101", RootID: &replyRoot, Relation: domain.ConfluenceCommentRelationRoot},
		"102": {ID: "102", ParentID: &missingParent, RootID: &replyRoot, Relation: domain.ConfluenceCommentRelationReply},
	}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("missing ancestry error = %v", err)
	}
}

func completeCommentProjectionResult(mode string) *ConfluenceCommentInventoryResult {
	rootID := "101"
	commentID := ""
	if mode == "thread" {
		commentID = rootID
	}
	return &ConfluenceCommentInventoryResult{
		SchemaVersion: confluenceCommentInventorySchemaVersion,
		PageID:        "42", PageVersion: 7, PageVersionGated: true,
		Query:    ConfluenceCommentQuery{Mode: mode, Location: "all", State: "all", Depth: "all", CommentID: commentID},
		Complete: true, CommentsComplete: true, ThreadsComplete: true, AnchorsComplete: true,
		Count: 1, RootCount: 1, PartialReasons: []string{}, Capabilities: completeCommentCapabilities(),
		Comments: []ConfluenceCommentResultRecord{{
			ID: rootID, PageID: "42", RootID: &rootID, Relation: domain.ConfluenceCommentRelationRoot,
			Location: domain.ConfluenceCommentLocationInline, Resolution: domain.ConfluenceCommentResolutionOpen,
			Version: 1, Author: ConfluenceCommentAuthor{ID: "user-1", DisplayName: "Reader"},
			CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-02T00:00:00+0000", Body: "PRIVATE-NATIVE",
			BodyStorage: `<p>PRIVATE-NATIVE</p>`, Anchor: &ConfluenceInlineAnchor{
				MarkerRef: "marker-1", OriginalSelection: "PRIVATE-ORIGINAL", ObservedSelection: "PRIVATE-OBSERVED",
				Status: domain.ConfluenceAnchorMatched,
			},
		}},
		Diagnostics: []ConfluenceCommentResultDiagnostic{},
	}
}

func containsAppString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
