package app

import (
	"context"
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

func (s *qualifiedConfluenceTreeStore) TreeQualified(ctx context.Context, request domain.ConfluenceTreeRequest) (domain.ConfluenceTreePage, error) {
	s.request = request
	s.budget = domain.ReadBudgetFromContext(ctx)
	s.single = domain.SingleAttempt(ctx)
	return s.page, nil
}

func TestConfluenceTreeQualifiedInstallsAndReportsCallerBudget(t *testing.T) {
	store := &qualifiedConfluenceTreeStore{page: domain.ConfluenceTreePage{
		Pages: []domain.PageRef{{ID: "1"}}, ScannedItems: 1, Complete: true,
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
	got, err := NormalizeConfluenceTreeOpts(ConfluenceTreeOpts{Space: "DOC"})
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxItems != ConfluenceTreeDefaultMaxItems || got.MaxScannedItems != ConfluenceTreeDefaultMaxScannedItems ||
		got.MaxRequests != ConfluenceTreeDefaultMaxRequests || got.MaxResponseBytes != ConfluenceTreeDefaultResponseBytes ||
		got.Deadline != ConfluenceTreeDefaultDeadline {
		t.Fatalf("defaults=%+v", got)
	}
	for name, opts := range map[string]ConfluenceTreeOpts{
		"space":    {},
		"items":    {Space: "DOC", MaxItems: ConfluenceTreeMaxItems + 1},
		"scan":     {Space: "DOC", MaxScannedItems: ConfluenceTreeMaxScannedItems + 1},
		"requests": {Space: "DOC", MaxRequests: ConfluenceTreeMaxRequests + 1},
		"bytes":    {Space: "DOC", MaxResponseBytes: ConfluenceTreeMaxResponseBytes + 1},
		"deadline": {Space: "DOC", Deadline: ConfluenceTreeMaxDeadline + time.Nanosecond},
	} {
		if _, err := NormalizeConfluenceTreeOpts(opts); err == nil {
			t.Errorf("%s bound unexpectedly accepted", name)
		}
	}
}

func TestConfluenceTreeQualifiedRejectsPortBoundViolation(t *testing.T) {
	store := &qualifiedConfluenceTreeStore{page: domain.ConfluenceTreePage{
		Pages: []domain.PageRef{{ID: "1"}, {ID: "2"}}, ScannedItems: 2, Complete: true,
		Consistency: domain.ConfluenceTreeConsistencyLiveUnproven,
	}}
	if _, err := (&ConfluenceService{store: store}).TreeQualified(t.Context(), ConfluenceTreeOpts{
		Space: "DOC", MaxItems: 1,
	}); err == nil {
		t.Fatal("qualified port exceeded the caller item bound")
	}
}
