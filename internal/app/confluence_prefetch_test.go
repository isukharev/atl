package app

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type prefetchStore struct {
	domain.DocStore
	delays        map[string]time.Duration
	errs          map[string]error
	refs          []domain.PageRef
	treeTruncated bool
	releases      map[string]chan struct{}
	started       chan string
	completedCh   chan string

	mu        sync.Mutex
	completed []string

	active atomic.Int32
	peak   atomic.Int32
}

func (s *prefetchStore) Search(_ context.Context, _ string, _ int, _ string) ([]domain.PageRef, string, error) {
	return append([]domain.PageRef(nil), s.refs...), "", nil
}

func (s *prefetchStore) Tree(_ context.Context, _ string, _ int) ([]domain.PageRef, bool, error) {
	return append([]domain.PageRef(nil), s.refs...), s.treeTruncated, nil
}

func (s *prefetchStore) GetPage(ctx context.Context, id string, _ domain.PullOpts) (*domain.Resource, error) {
	n := s.active.Add(1)
	defer s.active.Add(-1)
	for old := s.peak.Load(); n > old && !s.peak.CompareAndSwap(old, n); old = s.peak.Load() {
	}
	if release, ok := s.releases[id]; ok {
		select {
		case s.started <- id:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else {
		select {
		case <-time.After(s.delays[id]):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.mu.Lock()
	s.completed = append(s.completed, id)
	s.mu.Unlock()
	if s.completedCh != nil {
		s.completedCh <- id
	}
	if err := s.errs[id]; err != nil {
		return nil, err
	}
	page := completeTestPage(id)
	page.Title = "Shared"
	page.BodyPresent = true
	return page, nil
}

func (s *prefetchStore) completionOrder() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.completed...)
}

func newGatedPrefetchStore(ids ...string) *prefetchStore {
	store := &prefetchStore{
		refs:        make([]domain.PageRef, 0, len(ids)),
		releases:    make(map[string]chan struct{}, len(ids)),
		started:     make(chan string, len(ids)),
		completedCh: make(chan string, len(ids)),
	}
	for _, id := range ids {
		store.refs = append(store.refs, domain.PageRef{ID: id})
		store.releases[id] = make(chan struct{})
	}
	return store
}

func awaitPrefetchIDs(t *testing.T, ch <-chan string, count int) []string {
	t.Helper()
	ids := make([]string, 0, count)
	for len(ids) < count {
		select {
		case id := <-ch:
			ids = append(ids, id)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out after ids %v", ids)
		}
	}
	return ids
}

type pullOutcome struct {
	result *PullResult
	err    error
}

type cancellationJoinStore struct {
	domain.DocStore
	refs         []domain.PageRef
	started      chan string
	canceled     chan string
	failFirst    chan struct{}
	releaseTails chan struct{}
}

func (s *cancellationJoinStore) Search(_ context.Context, _ string, _ int, _ string) ([]domain.PageRef, string, error) {
	return append([]domain.PageRef(nil), s.refs...), "", nil
}

func (s *cancellationJoinStore) GetPage(ctx context.Context, id string, _ domain.PullOpts) (*domain.Resource, error) {
	s.started <- id
	if id == "10" {
		<-s.failFirst
		return nil, domain.ErrForbidden
	}
	<-ctx.Done()
	s.canceled <- id
	<-s.releaseTails
	return nil, ctx.Err()
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

func TestOrdinaryPullSchedulingPreservesSelectorAndWriteOrder(t *testing.T) {
	for _, tc := range []struct {
		name          string
		opts          PullOpts
		wantTruncated bool
	}{
		{name: "cql", opts: PullOpts{CQL: "space = DOC"}},
		{name: "space", opts: PullOpts{Space: "DOC"}, wantTruncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newGatedPrefetchStore("10", "20", "30")
			store.treeTruncated = tc.wantTruncated
			tc.opts.Into = t.TempDir()
			tc.opts.PagePrefetch = 3
			done := make(chan pullOutcome, 1)
			go func() {
				result, err := (&ConfluenceService{
					baseURL: confluenceTestBackendURL, store: store, requestMaxInFlight: 3,
				}).Pull(context.Background(), tc.opts)
				done <- pullOutcome{result: result, err: err}
			}()
			if started := awaitPrefetchIDs(t, store.started, 3); len(started) != 3 {
				t.Fatalf("started=%v", started)
			}
			for _, id := range []string{"30", "20", "10"} {
				close(store.releases[id])
				if completed := awaitPrefetchIDs(t, store.completedCh, 1); completed[0] != id {
					t.Fatalf("completed=%v, want %s", completed, id)
				}
			}
			outcome := <-done
			if outcome.err != nil {
				t.Fatal(outcome.err)
			}
			result := outcome.result
			gotIDs := make([]string, 0, len(result.Pages))
			gotPaths := make([]string, 0, len(result.Pages))
			for _, page := range result.Pages {
				gotIDs = append(gotIDs, page.ID)
				gotPaths = append(gotPaths, page.Path)
			}
			if !reflect.DeepEqual(gotIDs, []string{"10", "20", "30"}) {
				t.Fatalf("result ids=%v, want selector order", gotIDs)
			}
			if !reflect.DeepEqual(gotPaths, []string{
				"DOC/shared/shared.csf", "DOC/shared-20/shared-20.csf", "DOC/shared-30/shared-30.csf",
			}) {
				t.Fatalf("paths=%v, want canonical serial claim/write order", gotPaths)
			}
			if got := store.completionOrder(); len(got) != 3 || got[0] == "10" || got[2] != "10" {
				t.Fatalf("body completion order=%v, want out-of-order responses", got)
			}
			if got := store.peak.Load(); got != 3 {
				t.Fatalf("peak page prefetch=%d, want 3", got)
			}
			if result.Scheduling == nil || *result.Scheduling != (PullScheduling{PagePrefetch: 3, MaxInFlight: 3}) {
				t.Fatalf("scheduling=%+v", result.Scheduling)
			}
			wantTruncatedAt := 0
			if tc.wantTruncated {
				wantTruncatedAt = 3
			}
			if result.Truncated != tc.wantTruncated || result.TruncatedAt != wantTruncatedAt {
				t.Fatalf("truncated=%t at=%d, want %t", result.Truncated, result.TruncatedAt, tc.wantTruncated)
			}
		})
	}
}

func TestOrdinaryPullPrefetchFailureCommitsOnlyCanonicalPrefix(t *testing.T) {
	root := t.TempDir()
	store := newGatedPrefetchStore("10", "20", "30")
	store.errs = map[string]error{"20": domain.ErrForbidden}
	done := make(chan pullOutcome, 1)
	go func() {
		result, err := (&ConfluenceService{
			baseURL: confluenceTestBackendURL, store: store, requestMaxInFlight: 3,
		}).Pull(context.Background(), PullOpts{Space: "DOC", Into: root, PagePrefetch: 3})
		done <- pullOutcome{result: result, err: err}
	}()
	awaitPrefetchIDs(t, store.started, 3)
	for _, id := range []string{"30", "20", "10"} {
		close(store.releases[id])
		if completed := awaitPrefetchIDs(t, store.completedCh, 1); completed[0] != id {
			t.Fatalf("completed=%v, want %s", completed, id)
		}
	}
	outcome := <-done
	result, err := outcome.result, outcome.err
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err=%v, want forbidden", err)
	}
	if result == nil || len(result.Pages) != 1 || result.Pages[0].ID != "10" {
		t.Fatalf("result=%+v, want only committed canonical prefix 10", result)
	}
	if got := store.completionOrder(); len(got) != 3 || got[0] == "10" || got[1] == "10" || got[2] != "10" {
		t.Fatalf("completion order=%v, want prefetched tail before canonical prefix", got)
	}
	for _, id := range []string{"20", "30"} {
		if _, exists, stateErr := mirror.New(root).SyncStateOf(id); stateErr != nil || exists {
			t.Fatalf("prefetched page %s after the failure was committed: exists=%t err=%v", id, exists, stateErr)
		}
	}
}

func TestOrdinaryPullWaitsForCanceledPrefetchWorkers(t *testing.T) {
	store := &cancellationJoinStore{
		refs:         []domain.PageRef{{ID: "10"}, {ID: "20"}, {ID: "30"}},
		started:      make(chan string, 3),
		canceled:     make(chan string, 2),
		failFirst:    make(chan struct{}),
		releaseTails: make(chan struct{}),
	}
	var firstOnce, tailsOnce sync.Once
	defer firstOnce.Do(func() { close(store.failFirst) })
	defer tailsOnce.Do(func() { close(store.releaseTails) })

	root := t.TempDir()
	done := make(chan pullOutcome, 1)
	go func() {
		result, err := (&ConfluenceService{
			baseURL: confluenceTestBackendURL, store: store, requestMaxInFlight: 3,
		}).Pull(context.Background(), PullOpts{CQL: "space = DOC", Into: root, PagePrefetch: 3})
		done <- pullOutcome{result: result, err: err}
	}()
	awaitPrefetchIDs(t, store.started, 3)
	firstOnce.Do(func() { close(store.failFirst) })
	awaitPrefetchIDs(t, store.canceled, 2)

	select {
	case outcome := <-done:
		t.Fatalf("pull returned before canceled tail reads exited: %v", outcome.err)
	case <-time.After(100 * time.Millisecond):
	}
	tailsOnce.Do(func() { close(store.releaseTails) })
	outcome := <-done
	if !errors.Is(outcome.err, domain.ErrForbidden) {
		t.Fatalf("err=%v, want forbidden", outcome.err)
	}
}

func TestPullSchedulingRequiresExactTransportAndReportsOnlyOptIn(t *testing.T) {
	refs := []domain.PageRef{{ID: "10"}}
	newStore := func() *prefetchStore {
		return &prefetchStore{delays: map[string]time.Duration{"10": 0}, refs: refs}
	}

	t.Run("default remains unscheduled", func(t *testing.T) {
		result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: newStore()}).Pull(
			context.Background(), PullOpts{Space: "DOC", Into: t.TempDir()},
		)
		if err != nil || result.Scheduling != nil {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})

	t.Run("rate only uses one in flight", func(t *testing.T) {
		result, err := (&ConfluenceService{
			baseURL: confluenceTestBackendURL, store: newStore(), requestMaxInFlight: 1, requestsPerSecond: 25,
		}).Pull(context.Background(), PullOpts{CQL: "space = DOC", Into: t.TempDir(), RequestsPerSecond: 25})
		if err != nil {
			t.Fatal(err)
		}
		if result.Scheduling == nil || *result.Scheduling != (PullScheduling{PagePrefetch: 1, MaxInFlight: 1, RequestsPerSecond: 25}) {
			t.Fatalf("scheduling=%+v", result.Scheduling)
		}
	})

	t.Run("dry run qualification stays sequential", func(t *testing.T) {
		store := &prefetchStore{
			delays: map[string]time.Duration{"10": 0, "20": 0, "30": 0},
			refs:   []domain.PageRef{{ID: "10"}, {ID: "20"}, {ID: "30"}},
		}
		result, err := (&ConfluenceService{
			baseURL: confluenceTestBackendURL, store: store, requestMaxInFlight: 3,
		}).Pull(context.Background(), PullOpts{Space: "DOC", Into: t.TempDir(), DryRun: true, PagePrefetch: 3})
		if err != nil {
			t.Fatal(err)
		}
		if got := store.peak.Load(); got != 1 {
			t.Fatalf("dry-run peak=%d, want sequential qualification", got)
		}
		if result.Scheduling == nil || result.Scheduling.PagePrefetch != 3 || len(result.Pages) != 3 {
			t.Fatalf("result=%+v", result)
		}
	})

	for _, tc := range []struct {
		name    string
		service *ConfluenceService
		opts    PullOpts
	}{
		{
			name:    "single page selector",
			service: &ConfluenceService{baseURL: confluenceTestBackendURL, store: newStore(), requestMaxInFlight: 2},
			opts:    PullOpts{ID: "10", Into: t.TempDir(), PagePrefetch: 2},
		},
		{
			name:    "prefetch transport missing",
			service: &ConfluenceService{baseURL: confluenceTestBackendURL, store: newStore()},
			opts:    PullOpts{Space: "DOC", Into: t.TempDir(), PagePrefetch: 2},
		},
		{
			name:    "rate mismatch",
			service: &ConfluenceService{baseURL: confluenceTestBackendURL, store: newStore(), requestMaxInFlight: 1, requestsPerSecond: 20},
			opts:    PullOpts{Space: "DOC", Into: t.TempDir(), RequestsPerSecond: 25},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.service.Pull(context.Background(), tc.opts)
			if tc.name == "single page selector" {
				if !errors.Is(err, domain.ErrUsage) {
					t.Fatalf("err=%v, want usage failure", err)
				}
			} else if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v, want check failure", err)
			}
		})
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
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store, requestMaxInFlight: 3}).Pull(context.Background(), PullOpts{
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
