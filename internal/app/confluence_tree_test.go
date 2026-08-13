package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

type qualifiedConfluenceTreeStore struct {
	domain.DocStore
	request domain.ConfluenceTreeRequest
	budget  *domain.ReadBudget
	single  bool
	page    domain.ConfluenceTreePage
}

func qualifiedTreePageRef(id string) domain.PageRef {
	return domain.PageRef{ID: id, Title: "Page " + id, Space: "DOC", Version: 1}
}

func (s *qualifiedConfluenceTreeStore) TreeQualified(ctx context.Context, request domain.ConfluenceTreeRequest) (domain.ConfluenceTreePage, error) {
	s.request = request
	s.budget = domain.ReadBudgetFromContext(ctx)
	s.single = domain.SingleAttempt(ctx)
	return s.page, nil
}

func TestConfluenceTreeQualifiedInstallsAndReportsCallerBudget(t *testing.T) {
	store := &qualifiedConfluenceTreeStore{page: domain.ConfluenceTreePage{
		Pages: []domain.PageRef{qualifiedTreePageRef("1")}, ScannedItems: 1, Complete: true,
		Consistency: domain.ConfluenceTreeConsistencyLiveUnproven,
	}}
	result, err := (&ConfluenceService{store: store}).TreeQualified(t.Context(), ConfluenceTreeOpts{
		Space: "DOC", Depth: 2, MaxItems: 3, MaxScannedItems: 4,
		MaxRequests: 5, MaxResponseBytes: 6, Deadline: time.Second,
	})
	if err != nil {
		t.Fatalf("TreeQualified: %v", err)
	}
	if store.request != (domain.ConfluenceTreeRequest{Space: "DOC", Depth: 2, MaxItems: 3, MaxScannedItems: 4}) {
		t.Fatalf("adapter request=%+v", store.request)
	}
	if store.budget == nil || !store.single {
		t.Fatalf("transport policy budget=%p single=%v", store.budget, store.single)
	}
	if !result.Complete || result.Count != 1 || result.Bounds.MaxRequests != 5 || result.Bounds.MaxResponseBytes != 6 || result.Bounds.DeadlineMillis != 1000 {
		t.Fatalf("result=%+v", result)
	}
}

func TestNormalizeConfluenceTreeOptsKeepsFiniteDefaultsAndBounds(t *testing.T) {
	got, err := NormalizeConfluenceTreeOpts(ConfluenceTreeOpts{Space: "  DOC  "})
	if err != nil {
		t.Fatal(err)
	}
	if got.Space != "DOC" || got.MaxItems != ConfluenceTreeDefaultMaxItems || got.MaxScannedItems != ConfluenceTreeDefaultMaxScannedItems ||
		got.MaxRequests != ConfluenceTreeDefaultMaxRequests || got.MaxResponseBytes != ConfluenceTreeDefaultResponseBytes ||
		got.Deadline != ConfluenceTreeDefaultDeadline {
		t.Fatalf("defaults=%+v", got)
	}
	for name, opts := range map[string]ConfluenceTreeOpts{
		"space":               {},
		"invalid space UTF-8": {Space: string([]byte{0xff})},
		"oversize space":      {Space: strings.Repeat("x", ConfluenceTreeMaxSpaceBytes+1)},
		"items":               {Space: "DOC", MaxItems: ConfluenceTreeMaxItems + 1},
		"scan":                {Space: "DOC", MaxScannedItems: ConfluenceTreeMaxScannedItems + 1},
		"requests":            {Space: "DOC", MaxRequests: ConfluenceTreeMaxRequests + 1},
		"bytes":               {Space: "DOC", MaxResponseBytes: ConfluenceTreeMaxResponseBytes + 1},
		"deadline":            {Space: "DOC", Deadline: ConfluenceTreeMaxDeadline + time.Nanosecond},
	} {
		if _, err := NormalizeConfluenceTreeOpts(opts); err == nil {
			t.Errorf("%s bound unexpectedly accepted", name)
		}
	}
}

func TestConfluenceTreeQualifiedRejectsPortBoundViolation(t *testing.T) {
	store := &qualifiedConfluenceTreeStore{page: domain.ConfluenceTreePage{
		Pages: []domain.PageRef{qualifiedTreePageRef("1"), qualifiedTreePageRef("2")}, ScannedItems: 2, Complete: true,
		Consistency: domain.ConfluenceTreeConsistencyLiveUnproven,
	}}
	if _, err := (&ConfluenceService{store: store}).TreeQualified(t.Context(), ConfluenceTreeOpts{
		Space: "DOC", MaxItems: 1,
	}); err == nil {
		t.Fatal("qualified port exceeded the caller item bound")
	}
}

func TestConfluenceTreeQualifiedRejectsInvalidCustomPortMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  domain.PageRef
	}{
		{name: "invalid id", ref: domain.PageRef{ID: ".", Title: "Page", Space: "DOC", Version: 1}},
		{name: "blank title", ref: domain.PageRef{ID: "1", Title: " ", Space: "DOC", Version: 1}},
		{name: "wrong space", ref: domain.PageRef{ID: "1", Title: "Page", Space: "OTHER", Version: 1}},
		{name: "zero version", ref: domain.PageRef{ID: "1", Title: "Page", Space: "DOC"}},
		{name: "invalid parent", ref: domain.PageRef{ID: "1", Title: "Page", Space: "DOC", Version: 1, Parent: "."}},
		{name: "self parent", ref: domain.PageRef{ID: "1", Title: "Page", Space: "DOC", Version: 1, Parent: "1"}},
		{name: "oversize opaque parent", ref: domain.PageRef{ID: "1", Title: "Page", Space: "DOC", Version: 1, Parent: strings.Repeat("x", 257)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &qualifiedConfluenceTreeStore{page: domain.ConfluenceTreePage{
				Pages: []domain.PageRef{tc.ref}, ScannedItems: 1, Complete: true,
				Consistency: domain.ConfluenceTreeConsistencyLiveUnproven,
			}}
			if _, err := (&ConfluenceService{store: store}).TreeQualified(t.Context(), ConfluenceTreeOpts{Space: "DOC"}); err == nil {
				t.Fatal("invalid custom-port metadata was accepted")
			}
		})
	}
}

func TestConfluenceTreeQualifiedAcceptsOpaqueCustomPortMetadata(t *testing.T) {
	ref := qualifiedTreePageRef("page_opaque-1")
	ref.Parent = "parent_opaque-1"
	store := &qualifiedConfluenceTreeStore{page: domain.ConfluenceTreePage{
		Pages: []domain.PageRef{ref}, ScannedItems: 1, Complete: true,
		Consistency: domain.ConfluenceTreeConsistencyLiveUnproven,
	}}
	result, err := (&ConfluenceService{store: store}).TreeQualified(t.Context(), ConfluenceTreeOpts{Space: "DOC"})
	if err != nil || result.Count != 1 || result.Pages[0].Parent != "parent_opaque-1" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

type legacyConfluenceTreeStore struct {
	domain.DocStore
	pages []domain.PageRef
}

func (s *legacyConfluenceTreeStore) Tree(context.Context, string, int) ([]domain.PageRef, bool, error) {
	return s.pages, true, nil
}

func TestConfluenceTreeLegacyFallbackRejectsUnqualifiedRows(t *testing.T) {
	store := &legacyConfluenceTreeStore{pages: []domain.PageRef{{ID: "1"}}}
	if _, err := (&ConfluenceService{store: store}).TreeQualified(t.Context(), ConfluenceTreeOpts{Space: "DOC"}); err == nil {
		t.Fatal("legacy tree row without exact metadata was accepted")
	}
}
