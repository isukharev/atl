package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type prefetchStore struct {
	domain.DocStore
	delays map[string]time.Duration
	errs   map[string]error

	active atomic.Int32
	peak   atomic.Int32
}

func (s *prefetchStore) GetPage(ctx context.Context, id string, _ domain.PullOpts) (*domain.Resource, error) {
	n := s.active.Add(1)
	defer s.active.Add(-1)
	for old := s.peak.Load(); n > old && !s.peak.CompareAndSwap(old, n); old = s.peak.Load() {
	}
	select {
	case <-time.After(s.delays[id]):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := s.errs[id]; err != nil {
		return nil, err
	}
	page := completeTestPage(id)
	page.BodyPresent = true
	return page, nil
}

func TestOrderedPagePrefetchBoundsWindowAndReturnsCanonicalOrder(t *testing.T) {
	store := &prefetchStore{delays: map[string]time.Duration{"10": 40 * time.Millisecond, "20": time.Millisecond, "30": time.Millisecond}}
	p := newOrderedPagePrefetch(context.Background(), store, []string{"10", "20", "30"}, 2, false)
	defer p.close()
	for _, id := range []string{"10", "20", "30"} {
		page, err := p.nextPage(id)
		if err != nil || page.ID != id {
			t.Fatalf("nextPage(%s) page=%+v err=%v", id, page, err)
		}
	}
	if got := store.peak.Load(); got != 2 {
		t.Fatalf("peak page prefetch = %d, want 2", got)
	}
}

type failingCompletePrefetchStore struct {
	domain.DocStore
	mu          sync.Mutex
	searchCalls int
	getIDs      []string
}

func (s *failingCompletePrefetchStore) Search(ctx context.Context, query string, limit int, cursor string) ([]domain.PageRef, string, error) {
	page, err := s.SearchComplete(ctx, query, limit, cursor)
	return page.Results, page.Next, err
}

func (s *failingCompletePrefetchStore) SearchComplete(context.Context, string, int, string) (domain.PageSearchPage, error) {
	s.mu.Lock()
	s.searchCalls++
	s.mu.Unlock()
	return completeSearchPage("10", "20", "30"), nil
}

func (s *failingCompletePrefetchStore) GetPage(ctx context.Context, id string, _ domain.PullOpts) (*domain.Resource, error) {
	s.mu.Lock()
	s.getIDs = append(s.getIDs, id)
	s.mu.Unlock()
	if id == "10" {
		time.Sleep(20 * time.Millisecond)
	}
	if id == "20" {
		return nil, domain.ErrForbidden
	}
	select {
	case <-time.After(time.Millisecond):
		page := completeTestPage(id)
		page.BodyPresent = true
		return page, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestCompletePullPrefetchFailureCheckpointsOnlyCommittedPrefix(t *testing.T) {
	root := t.TempDir()
	store := &failingCompletePrefetchStore{}
	result, err := (&ConfluenceService{store: store, requestMaxInFlight: 3}).Pull(context.Background(), PullOpts{
		CQL: "space = DOC", Into: root, Complete: true, PagePrefetch: 3,
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err=%v, want forbidden", err)
	}
	if result == nil || len(result.Pages) != 1 || result.Pages[0].ID != "10" {
		t.Fatalf("result=%+v, want only committed canonical prefix 10", result)
	}
	checkpoint, ok, loadErr := mirror.New(root).CompletePullCheckpoint(selectorHash("space = DOC"))
	if loadErr != nil || !ok || checkpoint.NextIndex != 1 {
		t.Fatalf("checkpoint ok=%v next=%d err=%v", ok, checkpoint.NextIndex, loadErr)
	}
	if _, exists, stateErr := mirror.New(root).SyncStateOf("30"); stateErr != nil || exists {
		t.Fatal("prefetched page after the failure was incorrectly committed")
	}
}
