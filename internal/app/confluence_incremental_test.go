package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type incrementalPullStore struct {
	*pullStore
	searchPages    map[string]domain.PageSearchPage
	searchSequence []domain.PageSearchPage
	searchErr      error
	queries        []string
	getCalls       int
}

func (s *incrementalPullStore) Search(_ context.Context, query string, limit int, cursor string) ([]domain.PageRef, string, error) {
	page, err := s.SearchComplete(context.Background(), query, limit, cursor)
	return page.Results, page.Next, err
}

func (s *incrementalPullStore) SearchComplete(_ context.Context, query string, _ int, cursor string) (domain.PageSearchPage, error) {
	s.queries = append(s.queries, query)
	if s.searchErr != nil {
		return domain.PageSearchPage{}, s.searchErr
	}
	if len(s.searchSequence) > 0 {
		page := s.searchSequence[0]
		s.searchSequence = s.searchSequence[1:]
		return page, nil
	}
	return s.searchPages[cursor], nil
}

func (s *incrementalPullStore) GetPage(ctx context.Context, id string, opts domain.PullOpts) (*domain.Resource, error) {
	s.getCalls++
	return s.pullStore.GetPage(ctx, id, opts)
}

func incrementalPage(id string, version int, updated string) (*domain.Resource, domain.PageRef) {
	page := &domain.Resource{ID: id, Type: "page", Title: "Page " + id, SpaceKey: "DOC", Version: version, Updated: updated, Body: []byte("<p>" + id + "</p>")}
	return page, domain.PageRef{ID: id, Title: page.Title, Space: page.SpaceKey, Version: version, Updated: updated}
}

func TestConfluenceIncrementalOrderByDetectionIgnoresQuotedText(t *testing.T) {
	if !hasUnquotedCQLOrderBy(`space=DOC order by lastmodified`) {
		t.Fatal("ORDER BY clause was not detected")
	}
	if hasUnquotedCQLOrderBy(`title = "order by example" and type=page`) {
		t.Fatal("quoted text was mistaken for an ORDER BY clause")
	}
}

func TestConfluenceIncrementalFormatsBoundaryInConfiguredCQLTimeZone(t *testing.T) {
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := parseConfluenceUpdated("2026-07-13T12:34:56Z")
	if err != nil {
		t.Fatal(err)
	}
	if got := cqlMinute(updated, location); got != "2026-07-13 14:34" {
		t.Fatalf("cql minute=%q", got)
	}
}

func TestIncrementalPullPaginatesPersistsAndSkipsKnownBoundary(t *testing.T) {
	root := t.TempDir()
	p1, h1 := incrementalPage("10", 2, "2026-07-13T12:00:10Z")
	p2, h2 := incrementalPage("20", 4, "2026-07-13T12:00:50Z")
	store := &incrementalPullStore{
		pullStore: &pullStore{pages: map[string]*domain.Resource{"10": p1, "20": p2}},
		searchPages: map[string]domain.PageSearchPage{
			"":  {Results: []domain.PageRef{h1}, Next: "1"},
			"1": {Results: []domain.PageRef{h2}, Complete: true},
		},
	}
	svc := &ConfluenceService{store: store}
	opts := PullOpts{CQL: `space = "DOC" and type = page`, Into: root, Incremental: true, Since: "2026-07-13 11:59", TimeZone: "UTC"}
	res, err := svc.Pull(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pages) != 2 || res.Incremental == nil || !res.Incremental.Complete || !res.Incremental.WatermarkAdvanced || res.Incremental.NextSince != "2026-07-13 12:00" {
		t.Fatalf("result=%+v", res)
	}
	if len(store.queries) != 4 || !strings.Contains(store.queries[0], `lastmodified >= "2026-07-13 11:59" order by lastmodified asc`) {
		t.Fatalf("queries=%v", store.queries)
	}
	w, ok, err := mirror.New(root).IncrementalWatermark(confluenceIncrementalService, res.Incremental.SelectorSHA256)
	if err != nil || !ok || w.BoundaryVersions["10"] != 2 || w.BoundaryVersions["20"] != 4 {
		t.Fatalf("watermark=%+v ok=%v err=%v", w, ok, err)
	}

	store.queries = nil
	store.getCalls = 0
	opts.Since = ""
	opts.TimeZone = ""
	res, err = svc.Pull(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pages) != 0 || res.Incremental.BoundarySkipped != 2 || store.getCalls != 0 || res.Incremental.WatermarkAdvanced {
		t.Fatalf("rerun result=%+v getCalls=%d", res, store.getCalls)
	}

	// A page that first appears at the already-recorded minute is not skipped:
	// only the exact id/version pairs in the completed boundary are replay-safe.
	p3, h3 := incrementalPage("30", 1, "2026-07-13T12:00:30Z")
	store.pages["30"] = p3
	store.searchPages = map[string]domain.PageSearchPage{"": {Results: []domain.PageRef{h1, h2, h3}, Complete: true}}
	res, err = svc.Pull(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pages) != 1 || res.Pages[0].ID != "30" || res.Incremental.BoundarySkipped != 2 {
		t.Fatalf("equal-minute new identity result=%+v", res)
	}
}

func TestIncrementalPullRejectsPartialSelectionWithoutWatermark(t *testing.T) {
	root := t.TempDir()
	_, hit := incrementalPage("10", 1, "2026-07-13T12:00:00Z")
	store := &incrementalPullStore{
		pullStore:   &pullStore{pages: map[string]*domain.Resource{}},
		searchPages: map[string]domain.PageSearchPage{"": {Results: []domain.PageRef{hit}, Complete: false, PartialReason: "missing continuation"}},
	}
	svc := &ConfluenceService{store: store}
	_, err := svc.Pull(context.Background(), PullOpts{CQL: "type=page", Into: root, Incremental: true, Since: "2026-07-13 11:00", TimeZone: "UTC"})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "watermark unchanged") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".atl", "incremental.json")); !os.IsNotExist(statErr) {
		t.Fatalf("incremental state unexpectedly exists: %v", statErr)
	}
}

func TestIncrementalPullRejectsSelectionThatMovesBetweenPasses(t *testing.T) {
	root := t.TempDir()
	_, h1 := incrementalPage("10", 1, "2026-07-13T12:00:00Z")
	_, h2 := incrementalPage("20", 1, "2026-07-13T12:01:00Z")
	store := &incrementalPullStore{
		pullStore: &pullStore{},
		searchSequence: []domain.PageSearchPage{
			{Results: []domain.PageRef{h1}, Complete: true},
			{Results: []domain.PageRef{h1, h2}, Complete: true},
		},
	}
	_, err := (&ConfluenceService{store: store}).Pull(context.Background(), PullOpts{CQL: "type=page", Into: root, Incremental: true, Since: "2026-07-13 11:00", TimeZone: "UTC"})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "changed during pagination") || store.getCalls != 0 {
		t.Fatalf("err=%v getCalls=%d", err, store.getCalls)
	}
}

func TestIncrementalPullRejectsDirtyTargetBeforeRemoteReads(t *testing.T) {
	root := t.TempDir()
	old, _ := incrementalPage("10", 1, "2026-07-13T11:00:00Z")
	seed := &pullStore{pages: map[string]*domain.Resource{"10": old}}
	if _, err := (&ConfluenceService{store: seed}).Pull(context.Background(), PullOpts{ID: "10", Into: root}); err != nil {
		t.Fatal(err)
	}
	states, err := mirror.New(root).SyncStates()
	if err != nil || len(states) != 1 {
		t.Fatalf("states=%v err=%v", states, err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(states[0].Path)), []byte("<p>local edit</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	newPage, hit := incrementalPage("10", 2, "2026-07-13T12:00:00Z")
	store := &incrementalPullStore{
		pullStore:   &pullStore{pages: map[string]*domain.Resource{"10": newPage}},
		searchPages: map[string]domain.PageSearchPage{"": {Results: []domain.PageRef{hit}, Complete: true}},
	}
	_, err = (&ConfluenceService{store: store}).Pull(context.Background(), PullOpts{CQL: "type=page", Into: root, Incremental: true, Since: "2026-07-13 11:00", TimeZone: "UTC"})
	if !errors.Is(err, domain.ErrCheckFailed) || store.getCalls != 0 {
		t.Fatalf("err=%v getCalls=%d", err, store.getCalls)
	}
}

func TestIncrementalPullRejectsUnappliedMarkdownBeforeRemoteReads(t *testing.T) {
	root := t.TempDir()
	old, _ := incrementalPage("10", 1, "2026-07-13T11:00:00Z")
	seed := &ConfluenceService{store: &pullStore{pages: map[string]*domain.Resource{"10": old}}}
	if _, err := seed.Pull(context.Background(), PullOpts{ID: "10", Into: root}); err != nil {
		t.Fatal(err)
	}
	states, err := mirror.New(root).SyncStates()
	if err != nil || len(states) != 1 {
		t.Fatalf("states=%v err=%v", states, err)
	}
	mdPath := strings.TrimSuffix(filepath.Join(root, filepath.FromSlash(states[0].Path)), ".csf") + ".md"
	md, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mdPath, append(md, []byte("\nlocal edit\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	newPage, hit := incrementalPage("10", 2, "2026-07-13T12:00:00Z")
	store := &incrementalPullStore{
		pullStore:   &pullStore{pages: map[string]*domain.Resource{"10": newPage}},
		searchPages: map[string]domain.PageSearchPage{"": {Results: []domain.PageRef{hit}, Complete: true}},
	}
	_, err = (&ConfluenceService{store: store}).Pull(context.Background(), PullOpts{CQL: "type=page", Into: root, Incremental: true, Since: "2026-07-13 11:00", TimeZone: "UTC"})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "unapplied Markdown") || store.getCalls != 0 {
		t.Fatalf("err=%v getCalls=%d", err, store.getCalls)
	}
}

func TestIncrementalPullInterruptedRunKeepsOldBoundaryAndResumes(t *testing.T) {
	root := t.TempDir()
	p1, h1 := incrementalPage("10", 1, "2026-07-13T12:00:00Z")
	p2, h2 := incrementalPage("20", 1, "2026-07-13T12:01:00Z")
	store := &incrementalPullStore{
		pullStore:   &pullStore{pages: map[string]*domain.Resource{"10": p1, "20": p2}, getErrs: map[string]error{"20": domain.ErrForbidden}},
		searchPages: map[string]domain.PageSearchPage{"": {Results: []domain.PageRef{h1, h2}, Complete: true}},
	}
	svc := &ConfluenceService{store: store}
	opts := PullOpts{CQL: "type=page", Into: root, Incremental: true, Since: "2026-07-13 11:00", TimeZone: "UTC"}
	if _, err := svc.Pull(context.Background(), opts); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err=%v", err)
	}
	hash := selectorHash("type=page")
	if _, ok, err := mirror.New(root).IncrementalWatermark(confluenceIncrementalService, hash); err != nil || ok {
		t.Fatalf("watermark advanced after interruption: ok=%v err=%v", ok, err)
	}
	delete(store.getErrs, "20")
	store.getCalls = 0
	res, err := svc.Pull(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pages) != 2 || store.getCalls != 2 || !res.Incremental.WatermarkAdvanced {
		t.Fatalf("resume result=%+v calls=%d", res, store.getCalls)
	}
}

func TestIncrementalPullExplicitCapFailsClosed(t *testing.T) {
	root := t.TempDir()
	_, h1 := incrementalPage("10", 1, "2026-07-13T12:00:00Z")
	_, h2 := incrementalPage("20", 1, "2026-07-13T12:01:00Z")
	store := &incrementalPullStore{pullStore: &pullStore{}, searchPages: map[string]domain.PageSearchPage{"": {Results: []domain.PageRef{h1, h2}, Complete: true}}}
	_, err := (&ConfluenceService{store: store}).Pull(context.Background(), PullOpts{CQL: "type=page", Into: root, Incremental: true, Since: "2026-07-13 11:00", TimeZone: "UTC", MaxPages: 1})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "--max-pages=1") {
		t.Fatalf("err=%v", err)
	}
}
