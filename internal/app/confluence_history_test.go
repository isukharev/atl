package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// legacyHistoryStore implements only the compatibility History port, so the
// service must fall back and refuse to claim completeness.
type legacyHistoryStore struct {
	domain.DocStore
	versions   []domain.Version
	historyErr error
	historyID  string
	calls      int
}

func (s *legacyHistoryStore) History(_ context.Context, id string) ([]domain.Version, error) {
	s.historyID, s.calls = id, s.calls+1
	return s.versions, s.historyErr
}

// qualifiedHistoryStore additionally implements the optional capability.
type qualifiedHistoryStore struct {
	legacyHistoryStore
	inventory domain.VersionInventory
	qualified int
}

func (s *qualifiedHistoryStore) HistoryQualified(_ context.Context, id string) (domain.VersionInventory, error) {
	s.historyID, s.qualified = id, s.qualified+1
	return s.inventory, s.historyErr
}

func TestHistoryQualifiesCompleteListing(t *testing.T) {
	store := &qualifiedHistoryStore{
		inventory: domain.VersionInventory{Complete: true, Versions: []domain.Version{
			{Number: 3, When: "2026-01-03", By: "Carol", Message: "third"},
			{Number: 2, When: "2026-01-02", By: "Bob"},
			{Number: 1, When: "2026-01-01", By: "Alice"},
		}},
	}
	svc := &ConfluenceService{store: store}
	got, err := svc.History(context.Background(), "12345")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if got.SchemaVersion != 1 || got.PageID != "12345" || got.Count != 3 || !got.Complete || got.PartialReason != "" {
		t.Fatalf("result=%+v", got)
	}
	if store.qualified != 1 || store.calls != 0 || store.historyID != "12345" {
		t.Fatalf("qualified=%d legacy=%d id=%q", store.qualified, store.calls, store.historyID)
	}
	if got.Versions[0].Number != 3 || got.Versions[0].Message != "third" || got.Versions[2].By != "Alice" {
		t.Fatalf("application result must preserve the full version rows: %+v", got.Versions)
	}
}

func TestHistoryCarriesStaticPartialReason(t *testing.T) {
	for _, reason := range []string{
		domain.HistoryPartialPageLimit,
		domain.HistoryPartialItemLimit,
		domain.HistoryPartialPaginationStalled,
	} {
		t.Run(reason, func(t *testing.T) {
			store := &qualifiedHistoryStore{
				inventory: domain.VersionInventory{PartialReason: reason, Versions: []domain.Version{{Number: 5}}},
			}
			svc := &ConfluenceService{store: store}
			got, err := svc.History(context.Background(), "12345")
			if err != nil {
				t.Fatalf("History: %v", err)
			}
			if got.Complete || got.PartialReason != reason {
				t.Fatalf("result=%+v", got)
			}
		})
	}
}

// A backend that only implements the legacy port proves nothing about
// exhaustion, so its listing must never read as complete.
func TestHistoryFallsBackToLegacyUnqualified(t *testing.T) {
	store := &legacyHistoryStore{versions: []domain.Version{{Number: 2}, {Number: 1}}}
	svc := &ConfluenceService{store: store}
	got, err := svc.History(context.Background(), "12345")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if got.Complete || got.PartialReason != domain.HistoryPartialLegacyUnqualified || got.Count != 2 {
		t.Fatalf("result=%+v", got)
	}
	if store.calls != 1 {
		t.Fatalf("legacy history calls=%d", store.calls)
	}
}

// A legacy store may return a nil slice; the result must still be a proven
// non-nil array so an empty history is not confused with an absent read.
func TestHistoryLegacyNilSliceBecomesEmptyArray(t *testing.T) {
	store := &legacyHistoryStore{}
	svc := &ConfluenceService{store: store}
	got, err := svc.History(context.Background(), "12345")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if got.Versions == nil || len(got.Versions) != 0 || got.Count != 0 {
		t.Fatalf("result=%+v", got)
	}
	if got.Complete || got.PartialReason != domain.HistoryPartialLegacyUnqualified {
		t.Fatalf("legacy empty history must stay partial: %+v", got)
	}
}

func TestHistoryEmptyQualifiedListingIsNonNil(t *testing.T) {
	store := &qualifiedHistoryStore{
		inventory: domain.VersionInventory{Complete: true, Versions: []domain.Version{}},
	}
	svc := &ConfluenceService{store: store}
	got, err := svc.History(context.Background(), "12345")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if got.Versions == nil || len(got.Versions) != 0 || got.Count != 0 || !got.Complete || got.PartialReason != "" {
		t.Fatalf("an exhausted empty history must be proven non-nil evidence: %+v", got)
	}
}

// The qualified capability is preferred whenever the backend implements it, so
// the legacy port is never consulted for an evidence-facing read.
func TestHistoryPrefersQualifiedCapability(t *testing.T) {
	store := &qualifiedHistoryStore{
		legacyHistoryStore: legacyHistoryStore{versions: []domain.Version{{Number: 9}}},
		inventory:          domain.VersionInventory{Complete: true, Versions: []domain.Version{{Number: 1}}},
	}
	svc := &ConfluenceService{store: store}
	got, err := svc.History(context.Background(), "12345")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if store.qualified != 1 || store.calls != 0 {
		t.Fatalf("qualified=%d legacy=%d", store.qualified, store.calls)
	}
	if got.Versions[0].Number != 1 {
		t.Fatalf("result did not come from the qualified capability: %+v", got)
	}
}

// Invalid snapshots must be refused before emission so a broken contract cannot
// masquerade as evidence.
func TestHistoryRejectsInvalidSnapshots(t *testing.T) {
	cases := map[string]domain.VersionInventory{
		"nil collection":          {Complete: true},
		"complete with reason":    {Complete: true, PartialReason: domain.HistoryPartialPageLimit, Versions: []domain.Version{{Number: 1}}},
		"partial without reason":  {Versions: []domain.Version{{Number: 1}}},
		"partial unknown reason":  {PartialReason: "backend says stop", Versions: []domain.Version{{Number: 1}}},
		"non-positive number":     {Complete: true, Versions: []domain.Version{{Number: 0}}},
		"duplicate version":       {Complete: true, Versions: []domain.Version{{Number: 2}, {Number: 2}}},
		"out of order not newest": {Complete: true, Versions: []domain.Version{{Number: 1}, {Number: 2}}},
	}
	for name, inventory := range cases {
		t.Run(name, func(t *testing.T) {
			store := &qualifiedHistoryStore{inventory: inventory}
			svc := &ConfluenceService{store: store}
			if _, err := svc.History(context.Background(), "12345"); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("invalid snapshot accepted: %v", err)
			}
		})
	}
}

func TestHistoryPropagatesBackendErrors(t *testing.T) {
	store := &qualifiedHistoryStore{legacyHistoryStore: legacyHistoryStore{historyErr: domain.ErrForbidden}}
	svc := &ConfluenceService{store: store}
	if _, err := svc.History(context.Background(), "12345"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("backend error not propagated: %v", err)
	}
}

func TestHistoryRejectsEmptyReference(t *testing.T) {
	svc := &ConfluenceService{store: &qualifiedHistoryStore{}}
	if _, err := svc.History(context.Background(), "   "); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("empty reference must be a usage error: %v", err)
	}
}
