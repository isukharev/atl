package app

import (
	"context"
	"errors"
	"fmt"
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
	onGet                       func(string)
}

type qualifiedCompletePullStore struct {
	*completePullStore
	inventory *domain.ConfluenceCommentInventory
}

func (s *qualifiedCompletePullStore) ListConfluenceComments(_ context.Context, _ string, _ domain.ConfluenceCommentReadOptions) (domain.ConfluenceCommentInventory, error) {
	if s.inventory != nil {
		return *s.inventory, nil
	}
	return completeQualifiedComments(), nil
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
	if s.onGet != nil {
		s.onGet(id)
	}
	if len(s.searchSequence) > 0 {
		s.bodyBeforeSelectionComplete = true
	}
	s.getIDs = append(s.getIDs, id)
	return s.pullStore.GetPage(ctx, id, opts)
}

func seedCompletePullJournal(t *testing.T, root string, opts PullOpts, ids []string, accepted string) {
	t.Helper()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	bindConfluenceTestMirror(t, root)
	rs, _ := ResolveRender(nil, root, opts.Render, "confluence")
	optionsSHA256, err := completePullOptionsHash(nil, opts, rs)
	if err != nil {
		t.Fatal(err)
	}
	selectionSHA256, err := confluenceCompleteHashJSON(ids)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := mirror.CompletePullCheckpoint{
		Service: confluenceCompletePullService, SelectorSHA256: selectorHash(opts.CQL),
		OptionsSHA256: optionsSHA256, SelectionSHA256: selectionSHA256, IDs: append([]string(nil), ids...),
	}
	if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	batch, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	page := completeTestPage(accepted)
	dir, slug, err := m.ClaimPageDir(page.SpaceKey, nil, page.Title, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := batch.WriteView(dir, slug, page, nil, mirror.MDViewOpts{}); err != nil {
		t.Fatal(err)
	}
	path, err := filepath.Rel(root, filepath.Join(dir, slug+".csf"))
	if err != nil {
		t.Fatal(err)
	}
	state := mirror.SyncState{ID: page.ID, Version: page.Version, Hash: mirror.Hash(page.Body), Path: filepath.ToSlash(path)}
	entry := mirror.CompletePullJournalEntry{State: state, View: viewStateOf(rs)}
	if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, []mirror.CompletePullArtifact{{Path: state.Path, Data: page.Body, Mode: 0o644}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
		t.Fatal(err)
	}
	// Simulate a hard crash: discard the batch without flushing state.json.
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
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{CQL: "space = DOC", Into: root, Complete: true})
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

func TestCompletePullExactBatchSizeFinalizationIsIdempotent(t *testing.T) {
	root := t.TempDir()
	ids := make([]string, confluenceCompletePullBatch)
	pages := make(map[string]*domain.Resource, len(ids))
	for i := range ids {
		ids[i] = fmt.Sprintf("%03d", i+1)
		pages[ids[i]] = completeTestPage(ids[i])
	}
	selection := completeSearchPage(ids...)
	store := &completePullStore{
		pullStore:      &pullStore{pages: pages},
		searchSequence: []domain.PageSearchPage{selection, selection},
	}
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{CQL: "space = DOC", Into: root, Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete == nil || !result.Complete.Complete || result.Complete.Completed != len(ids) || result.Complete.CheckpointActive {
		t.Fatalf("complete result=%+v", result.Complete)
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
	if _, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), opts); !errors.Is(err, domain.ErrForbidden) || !strings.Contains(err.Error(), "checkpoint is at 2/3") {
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
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete.Source != "resumed" || len(result.Pages) != 1 || result.Pages[0].ID != "30" || !reflect.DeepEqual(store.getIDs, []string{"30"}) || len(store.queries) != 0 {
		t.Fatalf("result=%+v pages=%+v queries=%v getIDs=%v", result.Complete, result.Pages, store.queries, store.getIDs)
	}
}

func TestCompletePullRecoversJournalBeforeQualificationWithoutSearchOrRefetch(t *testing.T) {
	root := t.TempDir()
	opts := PullOpts{CQL: "space = DOC", Into: root, Complete: true}
	seedCompletePullJournal(t, root, opts, []string{"10", "20"}, "10")
	store := &completePullStore{pullStore: &pullStore{pages: map[string]*domain.Resource{"20": completeTestPage("20")}}}
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete == nil || result.Complete.Source != "resumed" || !result.Complete.Complete || result.Complete.Completed != 2 || !reflect.DeepEqual(store.getIDs, []string{"20"}) || len(store.queries) != 0 {
		t.Fatalf("complete=%+v queries=%v getIDs=%v", result.Complete, store.queries, store.getIDs)
	}
}

func TestCompletePullRecoversStagedPublicationBeforeQualificationWithoutSearchOrRefetch(t *testing.T) {
	root := t.TempDir()
	opts := PullOpts{CQL: "space = DOC", Into: root, Complete: true}
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	bindConfluenceTestMirror(t, root)
	rs, _ := ResolveRender(nil, root, opts.Render, "confluence")
	optionsSHA256, err := completePullOptionsHash(nil, opts, rs)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"10", "20"}
	selectionSHA256, err := confluenceCompleteHashJSON(ids)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := mirror.CompletePullCheckpoint{
		Service: confluenceCompletePullService, SelectorSHA256: selectorHash(opts.CQL),
		OptionsSHA256: optionsSHA256, SelectionSHA256: selectionSHA256, IDs: ids,
	}
	if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	page := completeTestPage("10")
	dir, slug, err := m.ClaimPageDir(page.SpaceKey, nil, page.Title, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, artifacts, err := m.PrepareCompletePullView(dir, slug, page, nil, mirror.MDViewOpts{})
	if err != nil {
		t.Fatal(err)
	}
	macroRel, err := filepath.Rel(root, confluenceJiraMacroPath(dir, slug))
	if err != nil {
		t.Fatal(err)
	}
	artifacts = append(artifacts, mirror.CompletePullArtifact{Path: filepath.ToSlash(macroRel), Remove: true})
	entry := mirror.CompletePullJournalEntry{State: state, View: viewStateOf(rs)}
	if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
		t.Fatal(err)
	}

	store := &completePullStore{pullStore: &pullStore{pages: map[string]*domain.Resource{"20": completeTestPage("20")}}}
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete == nil || result.Complete.Source != "resumed" || !result.Complete.Complete || !reflect.DeepEqual(store.getIDs, []string{"20"}) || len(store.queries) != 0 {
		t.Fatalf("complete=%+v queries=%v getIDs=%v", result.Complete, store.queries, store.getIDs)
	}
}

func TestCompletePullFlushesSharedStateOnlyAtBoundedBatchBoundary(t *testing.T) {
	root := t.TempDir()
	ids := make([]string, confluenceCompletePullBatch+1)
	pages := make(map[string]*domain.Resource, len(ids))
	for i := range ids {
		ids[i] = fmt.Sprintf("%03d", i+1)
		pages[ids[i]] = completeTestPage(ids[i])
	}
	m := mirror.New(root)
	getCount := 0
	store := &completePullStore{
		pullStore:      &pullStore{pages: pages},
		searchSequence: []domain.PageSearchPage{completeSearchPage(ids...), completeSearchPage(ids...)},
		onGet: func(_ string) {
			getCount++
			states, err := m.SyncStates()
			switch {
			case getCount <= confluenceCompletePullBatch && err == nil && len(states) != 0:
				t.Errorf("state flushed before batch boundary at body %d: %d entries", getCount, len(states))
			case getCount == confluenceCompletePullBatch+1 && (err != nil || len(states) != confluenceCompletePullBatch):
				t.Errorf("boundary state entries=%d err=%v", len(states), err)
			}
		},
	}
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{CQL: "space = DOC", Into: root, Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	states, err := m.SyncStates()
	if err != nil || len(states) != len(ids) || result.Complete == nil || !result.Complete.Complete {
		t.Fatalf("states=%d complete=%+v err=%v", len(states), result.Complete, err)
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
	svc := &ConfluenceService{baseURL: confluenceTestBackendURL, store: store}
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
	svc = &ConfluenceService{baseURL: confluenceTestBackendURL, store: &qualifiedCompletePullStore{completePullStore: store}}
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
	svc := &ConfluenceService{baseURL: confluenceTestBackendURL, store: store}
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

func TestCompletePullPartialRestartPreservesPreviousCheckpoint(t *testing.T) {
	root := t.TempDir()
	store := &completePullStore{
		pullStore: &pullStore{
			pages:   map[string]*domain.Resource{"10": completeTestPage("10"), "20": completeTestPage("20")},
			getErrs: map[string]error{"20": domain.ErrForbidden},
		},
		searchSequence: []domain.PageSearchPage{completeSearchPage("10", "20"), completeSearchPage("10", "20")},
	}
	svc := &ConfluenceService{baseURL: confluenceTestBackendURL, store: store}
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
	store.searchSequence = []domain.PageSearchPage{{
		Complete:      false,
		PartialReason: "backend returned a full search page without terminal pagination evidence",
	}}
	opts.RestartComplete = true
	if _, err := svc.Pull(context.Background(), opts); !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "terminal pagination evidence") {
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
	_, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: &completePullStore{pullStore: &pullStore{}}}).Pull(context.Background(), PullOpts{CQL: "type=page", Into: t.TempDir(), Complete: true, MaxPages: -1})
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
			_, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{CQL: "space = DOC", Into: root, Complete: true})
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
	_, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{CQL: "space = DOC", Into: root, Complete: true, Comments: true})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "comments") {
		t.Fatalf("error=%v", err)
	}
	checkpoint, ok, loadErr := mirror.New(root).CompletePullCheckpoint(selectorHash("space = DOC"))
	if loadErr != nil || !ok || checkpoint.NextIndex != 0 {
		t.Fatalf("checkpoint=%+v ok=%v err=%v", checkpoint, ok, loadErr)
	}
	journalPath := filepath.Join(root, ".atl", "complete-pulls", selectorHash("space = DOC")+".journal.json")
	if _, statErr := os.Stat(journalPath); !os.IsNotExist(statErr) {
		t.Fatalf("incomplete comments left journal: %v", statErr)
	}
}

func TestCompletePullRelocationCapturesJournalStateBeforeRequiredFlush(t *testing.T) {
	root := t.TempDir()
	m := mirror.New(root)
	old := &domain.Resource{ID: "10", Type: "page", Title: "Old title", SpaceKey: "DOC", Version: 1, Body: []byte("<p>old</p>")}
	dir, slug := m.PageDir(old.SpaceKey, nil, old.Title)
	if err := m.Write(dir, slug, old, nil); err != nil {
		t.Fatal(err)
	}
	bindConfluenceTestMirror(t, root)
	updated := completeTestPage("10")
	updated.Title = "New title"
	updated.Version = 2
	store := &completePullStore{
		pullStore: &pullStore{
			pages:   map[string]*domain.Resource{"10": updated, "20": completeTestPage("20")},
			getErrs: map[string]error{"20": domain.ErrForbidden},
		},
		searchSequence: []domain.PageSearchPage{completeSearchPage("10", "20"), completeSearchPage("10", "20")},
	}
	_, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{CQL: "space = DOC", Into: root, Complete: true})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("pull error=%v", err)
	}
	checkpoint, ok, loadErr := m.CompletePullCheckpoint(selectorHash("space = DOC"))
	if loadErr != nil || !ok || checkpoint.NextIndex != 1 {
		t.Fatalf("checkpoint=%+v ok=%t err=%v", checkpoint, ok, loadErr)
	}
	state, ok, stateErr := m.SyncStateOf("10")
	if stateErr != nil || !ok || !strings.Contains(filepath.ToSlash(state.Path), "new-title") {
		t.Fatalf("state=%+v ok=%t err=%v", state, ok, stateErr)
	}
}

func TestCompletePullAnchorPartialStillCompletesSelection(t *testing.T) {
	root := t.TempDir()
	rootID := "c1"
	inventory := completeQualifiedComments(domain.ConfluenceCommentRecord{
		ID: rootID, PageID: "10", RootID: &rootID,
		Relation: domain.ConfluenceCommentRelationRoot, Location: domain.ConfluenceCommentLocationInline,
		Resolution: domain.ConfluenceCommentResolutionOpen, Version: 1,
		Body: "comment", BodyStorage: "<p>comment</p>", MarkerRef: "missing-marker",
	})
	store := &qualifiedCompletePullStore{
		completePullStore: &completePullStore{
			pullStore:      &pullStore{pages: map[string]*domain.Resource{"10": completeTestPage("10")}},
			searchSequence: []domain.PageSearchPage{completeSearchPage("10"), completeSearchPage("10")},
		},
		inventory: &inventory,
	}
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{
		CQL: "space = DOC", Into: root, Complete: true, Comments: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete == nil || !result.Complete.Complete || result.Complete.CheckpointActive {
		t.Fatalf("complete result=%+v", result.Complete)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "anchors") {
		t.Fatalf("warnings=%v", result.Warnings)
	}
	if _, ok, loadErr := mirror.New(root).CompletePullCheckpoint(result.Complete.SelectorSHA256); loadErr != nil || ok {
		t.Fatalf("completed checkpoint ok=%v err=%v", ok, loadErr)
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

func TestCompletePullLocalEditStopsAtBlockedCheckpoint(t *testing.T) {
	root := t.TempDir()
	_, seed := seedConfluenceSafetyPages(t, root, "10", "20")
	csfPath := pulledPathForID(t, root, seed, "20")
	if err := os.WriteFile(csfPath, []byte("<p>local edit</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &completePullStore{
		pullStore:      &pullStore{pages: map[string]*domain.Resource{"10": completeTestPage("10"), "20": completeTestPage("20")}},
		searchSequence: []domain.PageSearchPage{completeSearchPage("10", "20"), completeSearchPage("10", "20")},
	}
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{CQL: "space = DOC", Into: root, Complete: true})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "local native edits") {
		t.Fatalf("error=%v", err)
	}
	if result == nil || result.Complete == nil || result.Complete.Completed != 1 || result.LocalSafety == nil || result.LocalSafety.Blocked != 1 {
		t.Fatalf("result=%+v", result)
	}
	if !reflect.DeepEqual(store.getIDs, []string{"10"}) {
		t.Fatalf("body reads=%v", store.getIDs)
	}
	checkpoint, ok, loadErr := mirror.New(root).CompletePullCheckpoint(selectorHash("space = DOC"))
	if loadErr != nil || !ok || checkpoint.NextIndex != 1 {
		t.Fatalf("checkpoint=%+v ok=%v err=%v", checkpoint, ok, loadErr)
	}
}
