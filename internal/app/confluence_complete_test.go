package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type completePullStore struct {
	*pullStore
	searchSequence              []domain.PageSearchPage
	queries                     []string
	getIDs                      []string
	bodyBeforeSelectionComplete bool
}

func (s *completePullStore) Search(ctx context.Context, query string, limit int, cursor string) ([]domain.PageRef, string, error) {
	page, err := s.SearchComplete(ctx, query, limit, cursor)
	return page.Results, page.Next, err
}

func (s *completePullStore) SearchComplete(_ context.Context, query string, _ int, _ string) (domain.PageSearchPage, error) {
	s.queries = append(s.queries, query)
	if len(s.searchSequence) == 0 {
		return domain.PageSearchPage{}, errors.New("unexpected complete search")
	}
	page := s.searchSequence[0]
	s.searchSequence = s.searchSequence[1:]
	return page, nil
}

func (s *completePullStore) GetPage(ctx context.Context, id string, opts domain.PullOpts) (*domain.Resource, error) {
	if len(s.searchSequence) > 0 {
		s.bodyBeforeSelectionComplete = true
	}
	s.getIDs = append(s.getIDs, id)
	return s.pullStore.GetPage(ctx, id, opts)
}

func completeTestPage(id string) *domain.Resource {
	return &domain.Resource{ID: id, Type: "page", Title: "Page " + id, SpaceKey: "DOC", Version: 1, Body: []byte("<p>" + id + "</p>")}
}

func completeSearchPage(ids ...string) domain.PageSearchPage {
	refs := make([]domain.PageRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, domain.PageRef{ID: id})
	}
	return domain.PageSearchPage{Results: refs, Complete: true}
}

func TestCompletePullQualifiesCanonicalSelectionBeforeBodies(t *testing.T) {
	root := t.TempDir()
	store := &completePullStore{
		pullStore:      &pullStore{pages: map[string]*domain.Resource{"10": completeTestPage("10"), "20": completeTestPage("20")}},
		searchSequence: []domain.PageSearchPage{completeSearchPage("20", "10"), completeSearchPage("10", "20")},
	}
	result, err := (&ConfluenceService{store: store}).Pull(context.Background(), PullOpts{CQL: "space = DOC", Into: root, Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete == nil || !result.Complete.Complete || result.Complete.Source != "new" || result.Complete.Total != 2 || result.Complete.Completed != 2 || result.Complete.Remaining != 0 || result.Complete.CheckpointActive {
		t.Fatalf("complete result=%+v", result.Complete)
	}
	if !reflect.DeepEqual(store.getIDs, []string{"10", "20"}) || len(store.queries) != 2 || store.bodyBeforeSelectionComplete {
		t.Fatalf("queries=%v getIDs=%v", store.queries, store.getIDs)
	}
	if !strings.Contains(store.queries[0], "type = page") {
		t.Fatalf("complete query=%q", store.queries[0])
	}
	if _, ok, err := mirror.New(root).CompletePullCheckpoint(result.Complete.SelectorSHA256); err != nil || ok {
		t.Fatalf("completed checkpoint ok=%v err=%v", ok, err)
	}
}

func TestCompletePullResumesDurablePrefixWithoutSearchOrRefetch(t *testing.T) {
	root := t.TempDir()
	store := &completePullStore{
		pullStore: &pullStore{
			pages:   map[string]*domain.Resource{"10": completeTestPage("10"), "20": completeTestPage("20"), "30": completeTestPage("30")},
			getErrs: map[string]error{"30": domain.ErrForbidden},
		},
		searchSequence: []domain.PageSearchPage{completeSearchPage("10", "20", "30"), completeSearchPage("10", "20", "30")},
	}
	opts := PullOpts{CQL: "space = DOC", Into: root, Complete: true}
	if _, err := (&ConfluenceService{store: store}).Pull(context.Background(), opts); !errors.Is(err, domain.ErrForbidden) || !strings.Contains(err.Error(), "checkpoint is at 2/3") {
		t.Fatalf("first pull error=%v", err)
	}
	selectorSHA256 := selectorHash("space = DOC")
	checkpoint, ok, err := mirror.New(root).CompletePullCheckpoint(selectorSHA256)
	if err != nil || !ok || checkpoint.NextIndex != 2 {
		t.Fatalf("checkpoint=%+v ok=%v err=%v", checkpoint, ok, err)
	}
	if !reflect.DeepEqual(store.getIDs, []string{"10", "20", "30"}) {
		t.Fatalf("first getIDs=%v", store.getIDs)
	}
	delete(store.getErrs, "30")
	store.getIDs = nil
	store.queries = nil
	result, err := (&ConfluenceService{store: store}).Pull(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete.Source != "resumed" || len(result.Pages) != 1 || result.Pages[0].ID != "30" || !reflect.DeepEqual(store.getIDs, []string{"30"}) || len(store.queries) != 0 {
		t.Fatalf("result=%+v pages=%+v queries=%v getIDs=%v", result.Complete, result.Pages, store.queries, store.getIDs)
	}
}

func TestCompletePullOptionDriftFailsClosedAndExplicitRestartReplacesSnapshot(t *testing.T) {
	root := t.TempDir()
	store := &completePullStore{
		pullStore: &pullStore{
			pages:   map[string]*domain.Resource{"10": completeTestPage("10"), "20": completeTestPage("20")},
			getErrs: map[string]error{"20": domain.ErrForbidden}, comments: map[string][]domain.Comment{},
		},
		searchSequence: []domain.PageSearchPage{completeSearchPage("10", "20"), completeSearchPage("10", "20")},
	}
	svc := &ConfluenceService{store: store}
	base := PullOpts{CQL: "space = DOC", Into: root, Complete: true}
	if _, err := svc.Pull(context.Background(), base); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("seed error=%v", err)
	}
	store.getIDs = nil
	if _, err := svc.Pull(context.Background(), PullOpts{CQL: base.CQL, Into: root, Complete: true, Comments: true}); !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "options changed") {
		t.Fatalf("option drift error=%v", err)
	}
	if len(store.getIDs) != 0 {
		t.Fatalf("option drift fetched bodies: %v", store.getIDs)
	}
	delete(store.getErrs, "20")
	store.searchSequence = []domain.PageSearchPage{completeSearchPage("10", "20"), completeSearchPage("10", "20")}
	restarted, err := svc.Pull(context.Background(), PullOpts{CQL: base.CQL, Into: root, Complete: true, Comments: true, RestartComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Complete.Source != "restarted" || !restarted.Complete.Complete || !reflect.DeepEqual(store.getIDs, []string{"10", "20"}) {
		t.Fatalf("restarted=%+v getIDs=%v", restarted.Complete, store.getIDs)
	}
}

func TestCompletePullFailedRestartPreservesPreviousCheckpoint(t *testing.T) {
	root := t.TempDir()
	store := &completePullStore{
		pullStore: &pullStore{
			pages:   map[string]*domain.Resource{"10": completeTestPage("10"), "20": completeTestPage("20")},
			getErrs: map[string]error{"20": domain.ErrForbidden},
		},
		searchSequence: []domain.PageSearchPage{completeSearchPage("10", "20"), completeSearchPage("10", "20")},
	}
	svc := &ConfluenceService{store: store}
	opts := PullOpts{CQL: "space = DOC", Into: root, Complete: true}
	if _, err := svc.Pull(context.Background(), opts); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("seed error=%v", err)
	}
	m := mirror.New(root)
	before, ok, err := m.CompletePullCheckpoint(selectorHash(opts.CQL))
	if err != nil || !ok || before.NextIndex != 1 {
		t.Fatalf("before=%+v ok=%v err=%v", before, ok, err)
	}
	store.getIDs = nil
	store.searchSequence = []domain.PageSearchPage{completeSearchPage("10", "20"), completeSearchPage("10", "30")}
	opts.RestartComplete = true
	if _, err := svc.Pull(context.Background(), opts); !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "selection changed") {
		t.Fatalf("restart error=%v", err)
	}
	after, ok, err := m.CompletePullCheckpoint(selectorHash(opts.CQL))
	if err != nil || !ok || !reflect.DeepEqual(after, before) {
		t.Fatalf("after=%+v before=%+v ok=%v err=%v", after, before, ok, err)
	}
	if len(store.getIDs) != 0 {
		t.Fatalf("failed restart fetched bodies: %v", store.getIDs)
	}
}

func TestCompletePullRejectsNegativeCapAtAppBoundary(t *testing.T) {
	_, err := (&ConfluenceService{store: &completePullStore{pullStore: &pullStore{}}}).Pull(context.Background(), PullOpts{CQL: "type=page", Into: t.TempDir(), Complete: true, MaxPages: -1})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("error=%v", err)
	}
}

func TestCompletePullSelectionAnomaliesFailBeforeBodiesOrCheckpoint(t *testing.T) {
	tests := []struct {
		name  string
		pages []domain.PageSearchPage
	}{
		{name: "changed", pages: []domain.PageSearchPage{completeSearchPage("10"), completeSearchPage("20")}},
		{name: "duplicate", pages: []domain.PageSearchPage{{Results: []domain.PageRef{{ID: "10"}, {ID: "10"}}, Complete: true}}},
		{name: "partial", pages: []domain.PageSearchPage{{Complete: false, PartialReason: "backend omitted continuation"}}},
		{name: "repeated cursor", pages: []domain.PageSearchPage{
			{Results: []domain.PageRef{{ID: "10"}}, Next: "same"},
			{Results: []domain.PageRef{{ID: "20"}}, Next: "same"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			store := &completePullStore{pullStore: &pullStore{}, searchSequence: tt.pages}
			_, err := (&ConfluenceService{store: store}).Pull(context.Background(), PullOpts{CQL: "space = DOC", Into: root, Complete: true})
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error=%v", err)
			}
			if len(store.getIDs) != 0 {
				t.Fatalf("body reads=%v", store.getIDs)
			}
			if _, ok, loadErr := mirror.New(root).CompletePullCheckpoint(selectorHash("space = DOC")); loadErr != nil || ok {
				t.Fatalf("checkpoint ok=%v err=%v", ok, loadErr)
			}
		})
	}
}

func TestCollectCompletePullIDsHasNoOrdinaryCapButHonorsExplicitCap(t *testing.T) {
	pages := make([]domain.PageSearchPage, 0, 11)
	for page := 0; page < 11; page++ {
		count := 100
		if page == 10 {
			count = 1
		}
		refs := make([]domain.PageRef, 0, count)
		for i := 0; i < count; i++ {
			refs = append(refs, domain.PageRef{ID: idFor(page+1, i)})
		}
		next := ""
		complete := true
		if page < 10 {
			next = idFor(page+1, 0)
			complete = false
		}
		pages = append(pages, domain.PageSearchPage{Results: refs, Next: next, Complete: complete})
	}
	store := &completePullStore{pullStore: &pullStore{}, searchSequence: append([]domain.PageSearchPage(nil), pages...)}
	ids, err := collectCompletePullIDs(context.Background(), store, "type=page", 0)
	if err != nil || len(ids) != 1001 {
		t.Fatalf("ids=%d err=%v", len(ids), err)
	}
	store.searchSequence = append([]domain.PageSearchPage(nil), pages...)
	if _, err := collectCompletePullIDs(context.Background(), store, "type=page", 1000); !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "--max-pages=1000") {
		t.Fatalf("cap error=%v", err)
	}
}

func TestCompletePullTruncatedCommentsDoNotAdvanceCheckpoint(t *testing.T) {
	root := t.TempDir()
	store := &completePullStore{
		pullStore: &pullStore{
			pages:    map[string]*domain.Resource{"10": completeTestPage("10")},
			comments: map[string][]domain.Comment{"10": {{ID: "c1"}}}, commentsTruncated: map[string]bool{"10": true},
		},
		searchSequence: []domain.PageSearchPage{completeSearchPage("10"), completeSearchPage("10")},
	}
	_, err := (&ConfluenceService{store: store}).Pull(context.Background(), PullOpts{CQL: "space = DOC", Into: root, Complete: true, Comments: true})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "comments") {
		t.Fatalf("error=%v", err)
	}
	checkpoint, ok, loadErr := mirror.New(root).CompletePullCheckpoint(selectorHash("space = DOC"))
	if loadErr != nil || !ok || checkpoint.NextIndex != 0 {
		t.Fatalf("checkpoint=%+v ok=%v err=%v", checkpoint, ok, loadErr)
	}
}

func TestCompletePullBindingCoversPullAffectingOptions(t *testing.T) {
	rs := RenderSettings{Sections: map[string]bool{"body": true}, DisplayTimeZone: "UTC"}
	base, err := completePullOptionsHash(nil, PullOpts{}, rs)
	if err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string]PullOpts{
		"assets":   {Assets: true},
		"comments": {Comments: true},
	} {
		got, err := completePullOptionsHash(nil, candidate, rs)
		if err != nil || got == base {
			t.Fatalf("%s hash=%q base=%q err=%v", name, got, base, err)
		}
	}
	changedRender := rs
	changedRender.DisplayTimeZone = "Europe/Berlin"
	if got, err := completePullOptionsHash(nil, PullOpts{}, changedRender); err != nil || got == base {
		t.Fatalf("render hash=%q base=%q err=%v", got, base, err)
	}
	cfg := &config.Config{JiraListViews: config.DefaultJiraListViews()}
	macroRS := rs
	macroRS.ExpandJiraMacros = true
	defaultView, err := completePullOptionsHash(cfg, PullOpts{}, macroRS)
	if err != nil {
		t.Fatal(err)
	}
	fullView, err := completePullOptionsHash(cfg, PullOpts{JiraView: "full"}, macroRS)
	if err != nil || fullView == defaultView {
		t.Fatalf("Jira view hashes default=%q full=%q err=%v", defaultView, fullView, err)
	}
}

func TestCompletePullLocalEditFailsBeforeBodyReadsOrCheckpoint(t *testing.T) {
	root := t.TempDir()
	seedStore := &pullStore{pages: map[string]*domain.Resource{"10": completeTestPage("10")}}
	seed, err := (&ConfluenceService{store: seedStore}).Pull(context.Background(), PullOpts{ID: "10", Into: root})
	if err != nil {
		t.Fatal(err)
	}
	csfPath := filepath.Join(root, filepath.FromSlash(seed.Pages[0].Path))
	if err := os.WriteFile(csfPath, []byte("<p>local edit</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &completePullStore{
		pullStore:      &pullStore{pages: map[string]*domain.Resource{"10": completeTestPage("10")}},
		searchSequence: []domain.PageSearchPage{completeSearchPage("10"), completeSearchPage("10")},
	}
	_, err = (&ConfluenceService{store: store}).Pull(context.Background(), PullOpts{CQL: "space = DOC", Into: root, Complete: true})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "local native edits") {
		t.Fatalf("error=%v", err)
	}
	if len(store.getIDs) != 0 {
		t.Fatalf("body reads=%v", store.getIDs)
	}
	if _, ok, loadErr := mirror.New(root).CompletePullCheckpoint(selectorHash("space = DOC")); loadErr != nil || ok {
		t.Fatalf("checkpoint ok=%v err=%v", ok, loadErr)
	}
}
