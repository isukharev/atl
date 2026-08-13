package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type failingConfluenceAssetResolver struct{}

func (failingConfluenceAssetResolver) Resolve(context.Context, *domain.Resource, domain.Ref) ([]byte, string, error) {
	return nil, "", errors.New("synthetic asset read failure")
}

type callbackQualifiedPullStore struct {
	*pullStore
	inventory domain.ConfluenceCommentInventory
	callback  func()
}

func (s *callbackQualifiedPullStore) ListConfluenceComments(_ context.Context, _ string, _ domain.ConfluenceCommentReadOptions) (domain.ConfluenceCommentInventory, error) {
	if s.callback != nil {
		s.callback()
	}
	return s.inventory, nil
}

type callbackQualifiedCompletePullStore struct {
	*completePullStore
	inventory domain.ConfluenceCommentInventory
	callback  func()
}

func (s *callbackQualifiedCompletePullStore) ListConfluenceComments(_ context.Context, _ string, _ domain.ConfluenceCommentReadOptions) (domain.ConfluenceCommentInventory, error) {
	if s.callback != nil {
		s.callback()
	}
	return s.inventory, nil
}

func confluencePullInclude(t *testing.T, result *PullResult, dimension string) ConfluencePullInclude {
	t.Helper()
	for _, include := range result.Includes {
		if include.Dimension == dimension {
			return include
		}
	}
	t.Fatalf("include %q missing from %+v", dimension, result.Includes)
	return ConfluencePullInclude{}
}

func TestConfluencePullIncludesQualifyActualAssetCoverage(t *testing.T) {
	body := []byte(`<p>image</p><ac:image><ri:attachment ri:filename="image.png"/></ac:image>`)
	for _, tc := range []struct {
		name     string
		resolver domain.AssetResolver
		want     string
	}{
		{name: "qualified", resolver: staticConfluenceAssetResolver{}, want: ConfluencePullIncludeQualified},
		{name: "partial", resolver: failingConfluenceAssetResolver{}, want: ConfluencePullIncludePartial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &pullStore{pages: map[string]*domain.Resource{
				"100": {ID: "100", Title: "Alpha", SpaceKey: "DOC", Version: 1, Body: body},
			}}
			result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store, assets: tc.resolver}).Pull(
				t.Context(), PullOpts{ID: "100", Into: t.TempDir(), Assets: true},
			)
			if err != nil {
				t.Fatalf("Pull: %v", err)
			}
			assets := confluencePullInclude(t, result, ConfluencePullIncludeAssets)
			if assets.Qualification != tc.want || !assets.Requested || assets.Complete == nil || *assets.Complete != (tc.want == ConfluencePullIncludeQualified) {
				t.Fatalf("assets include=%+v, want qualification %q with explicit completeness", assets, tc.want)
			}
			if tc.want == ConfluencePullIncludePartial && assets.Reason != ConfluencePullIncludeReasonResolutionIncomplete {
				t.Fatalf("assets partial reason=%q", assets.Reason)
			}
			comments := confluencePullInclude(t, result, ConfluencePullIncludeComments)
			if comments.Qualification != ConfluencePullIncludeNotRequested || comments.Requested || comments.Complete != nil || comments.Reason != "" {
				t.Fatalf("comments include=%+v, want unrequested without completeness", comments)
			}
			if result.LocalSafety != nil {
				t.Fatalf("clean actual pull added local_safety: %+v", result.LocalSafety)
			}
		})
	}
}

func TestConfluencePullIncludesQualifyPublishedEmptyAndNonemptyComments(t *testing.T) {
	rootID := "c1"
	for _, tc := range []struct {
		name      string
		inventory domain.ConfluenceCommentInventory
		wantCount int
	}{
		{name: "empty", inventory: completeQualifiedComments(), wantCount: 0},
		{name: "nonempty", inventory: completeQualifiedComments(domain.ConfluenceCommentRecord{
			ID: rootID, PageID: "100", RootID: &rootID, Relation: domain.ConfluenceCommentRelationRoot,
			Location: domain.ConfluenceCommentLocationFooter, Resolution: domain.ConfluenceCommentResolutionOpen,
			Version: 1, BodyStorage: "<p>comment</p>",
		}), wantCount: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := &pullStore{pages: map[string]*domain.Resource{
				"100": {ID: "100", Title: "Alpha", SpaceKey: "DOC", Version: 1, Body: []byte(`<p>body</p>`)},
			}}
			store := &qualifiedPullStore{pullStore: base, inventory: tc.inventory}
			result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(
				t.Context(), PullOpts{ID: "100", Into: t.TempDir(), Comments: true},
			)
			if err != nil {
				t.Fatalf("Pull: %v", err)
			}
			comments := confluencePullInclude(t, result, ConfluencePullIncludeComments)
			if comments.Qualification != ConfluencePullIncludeQualified || !comments.Requested || comments.Complete == nil || !*comments.Complete || comments.Reason != "" {
				t.Fatalf("comments include=%+v, want published complete evidence", comments)
			}
			if len(result.Pages) != 1 || result.Pages[0].Comments == nil || *result.Pages[0].Comments != tc.wantCount {
				t.Fatalf("pages=%+v, want published comment count %d", result.Pages, tc.wantCount)
			}
		})
	}
}

func TestConfluencePullIncludesReportFailedRequestedRead(t *testing.T) {
	store := &pullStore{
		pages: map[string]*domain.Resource{
			"100": {ID: "100", Title: "Alpha", SpaceKey: "DOC", Version: 1, Body: []byte(`<p>body</p>`)},
		},
		commentsErr: errors.New("synthetic comment read failure"),
	}
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(
		t.Context(), PullOpts{ID: "100", Into: t.TempDir(), Comments: true},
	)
	if err == nil {
		t.Fatal("Pull unexpectedly succeeded")
	}
	comments := confluencePullInclude(t, result, ConfluencePullIncludeComments)
	if comments.Qualification != ConfluencePullIncludeFailed || !comments.Requested || comments.Complete == nil || *comments.Complete || comments.Reason != ConfluencePullIncludeReasonReadFailed {
		t.Fatalf("comments include=%+v, want failed read with complete=false", comments)
	}
	if !result.HasFailedInclude() {
		t.Fatal("failed include was not observable to the CLI")
	}
}

func TestConfluencePullReadFailureDemotesOnlyActuallyStagedSiblingAssets(t *testing.T) {
	bodyWithAsset := []byte(`<ac:image><ri:attachment ri:filename="image.png"/></ac:image>`)
	for _, tc := range []struct {
		name       string
		body       []byte
		wantAssets string
	}{
		{name: "no staged assets", body: []byte(`<p>body</p>`), wantAssets: ConfluencePullIncludeDeferred},
		{name: "staged asset", body: bodyWithAsset, wantAssets: ConfluencePullIncludeFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &pullStore{
				pages: map[string]*domain.Resource{
					"100": {ID: "100", Title: "Alpha", SpaceKey: "DOC", Version: 1, Body: tc.body},
				},
				commentsErr: errors.New("synthetic comment read failure"),
			}
			result, err := (&ConfluenceService{
				baseURL: confluenceTestBackendURL, store: store, assets: staticConfluenceAssetResolver{},
			}).Pull(t.Context(), PullOpts{ID: "100", Into: t.TempDir(), Assets: true, Comments: true})
			if err == nil {
				t.Fatal("Pull unexpectedly succeeded")
			}
			comments := confluencePullInclude(t, result, ConfluencePullIncludeComments)
			if comments.Qualification != ConfluencePullIncludeFailed || comments.Reason != ConfluencePullIncludeReasonReadFailed {
				t.Fatalf("comments include=%+v", comments)
			}
			assets := confluencePullInclude(t, result, ConfluencePullIncludeAssets)
			if assets.Qualification != tc.wantAssets {
				t.Fatalf("assets include=%+v, want %q", assets, tc.wantAssets)
			}
			if tc.wantAssets == ConfluencePullIncludeDeferred {
				if assets.Complete != nil || assets.Reason != ConfluencePullIncludeReasonNotAttempted {
					t.Fatalf("unstaged assets include=%+v", assets)
				}
			} else if assets.Complete == nil || *assets.Complete || assets.Reason != ConfluencePullIncludeReasonStagingFailed {
				t.Fatalf("staged assets include=%+v", assets)
			}
		})
	}
}

func TestConfluencePullCommentPublicationAndFlushFailuresStayVisible(t *testing.T) {
	for _, failure := range []string{"comment publication", "sidecar flush"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			page := &domain.Resource{ID: "100", Title: "Alpha", SpaceKey: "DOC", Version: 1, Body: []byte(`<p>body</p>`)}
			store := &callbackQualifiedPullStore{
				pullStore: &pullStore{pages: map[string]*domain.Resource{"100": page}},
				inventory: completeQualifiedComments(),
			}
			store.callback = func() {
				switch failure {
				case "comment publication":
					dir, slug := mirror.New(root).PageDir(page.SpaceKey, page.Ancestors, page.Title)
					if err := os.MkdirAll(filepath.Join(dir, slug+".comments.json"), 0o755); err != nil {
						t.Fatal(err)
					}
				case "sidecar flush":
					statePath := filepath.Join(root, ".atl", "state.json")
					if err := os.MkdirAll(statePath, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(
				t.Context(), PullOpts{ID: "100", Into: root, Assets: true, Comments: true},
			)
			if err == nil || result == nil || !result.HasFailedInclude() {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			comments := confluencePullInclude(t, result, ConfluencePullIncludeComments)
			if comments.Qualification != ConfluencePullIncludeFailed || comments.Complete == nil || *comments.Complete || comments.Reason != ConfluencePullIncludeReasonStagingFailed {
				t.Fatalf("comments include=%+v", comments)
			}
			assets := confluencePullInclude(t, result, ConfluencePullIncludeAssets)
			if assets.Qualification != ConfluencePullIncludeDeferred || assets.Complete != nil || assets.Reason != ConfluencePullIncludeReasonNotAttempted {
				t.Fatalf("unstaged assets include=%+v", assets)
			}
		})
	}
}

func TestConfluenceCompletePullStagingAndProgressFlushFailuresDemoteComments(t *testing.T) {
	for _, failure := range []string{"publication staging", "journal flush", "progress flush"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			page := completeTestPage("10")
			selection := completeSearchPage("10")
			store := &callbackQualifiedCompletePullStore{
				completePullStore: &completePullStore{
					pullStore:      &pullStore{pages: map[string]*domain.Resource{"10": page}},
					searchSequence: []domain.PageSearchPage{selection, selection},
				},
				inventory: completeQualifiedComments(),
			}
			selector, _, selectorErr := completePullSelector(PullOpts{Space: "DOC", Complete: true})
			if selectorErr != nil {
				t.Fatal(selectorErr)
			}
			selectorSHA256 := selectorHash(selector)
			store.callback = func() {
				switch failure {
				case "publication staging":
					dir, slug := mirror.New(root).PageDir(page.SpaceKey, page.Ancestors, page.Title)
					if err := os.MkdirAll(filepath.Join(dir, slug+".comments.json"), 0o755); err != nil {
						t.Fatal(err)
					}
				case "journal flush":
					journalPath := filepath.Join(root, ".atl", "complete-pulls", selectorSHA256+".journal.json")
					if err := os.Mkdir(journalPath, 0o700); err != nil {
						t.Fatal(err)
					}
				case "progress flush":
					progressPath := filepath.Join(root, ".atl", "complete-pulls", selectorSHA256+".progress.json")
					if err := os.Remove(progressPath); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(progressPath, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(
				t.Context(), PullOpts{Space: "DOC", Into: root, Complete: true, Assets: true, Comments: true},
			)
			if err == nil || result == nil || !result.HasFailedInclude() {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			comments := confluencePullInclude(t, result, ConfluencePullIncludeComments)
			if comments.Qualification != ConfluencePullIncludeFailed || comments.Complete == nil || *comments.Complete || comments.Reason != ConfluencePullIncludeReasonStagingFailed {
				t.Fatalf("comments include=%+v", comments)
			}
			assets := confluencePullInclude(t, result, ConfluencePullIncludeAssets)
			if failure != "progress flush" {
				if assets.Qualification != ConfluencePullIncludeDeferred || assets.Complete != nil || assets.Reason != ConfluencePullIncludeReasonNotAttempted {
					t.Fatalf("unpublished empty assets include=%+v", assets)
				}
			} else if assets.Qualification != ConfluencePullIncludeQualified || assets.Complete == nil || !*assets.Complete || assets.Reason != "" {
				t.Fatalf("durably published empty assets include=%+v", assets)
			}

			if failure != "progress flush" {
				return
			}
			progressPath := filepath.Join(root, ".atl", "complete-pulls", selectorSHA256+".progress.json")
			if err := os.Remove(progressPath); err != nil {
				t.Fatal(err)
			}
			store.callback = nil
			store.getIDs = nil
			store.queries = nil
			resumed, resumeErr := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(
				t.Context(), PullOpts{Space: "DOC", Into: root, Complete: true, Assets: true, Comments: true},
			)
			if resumeErr != nil || resumed.Complete == nil || !resumed.Complete.Complete || len(store.getIDs) != 0 || len(store.queries) != 0 {
				t.Fatalf("resumed=%+v getIDs=%v queries=%v error=%v", resumed, store.getIDs, store.queries, resumeErr)
			}
			comments = confluencePullInclude(t, resumed, ConfluencePullIncludeComments)
			if comments.Qualification != ConfluencePullIncludeQualified || comments.Complete == nil || !*comments.Complete {
				t.Fatalf("resumed comments include=%+v", comments)
			}
			assets = confluencePullInclude(t, resumed, ConfluencePullIncludeAssets)
			if assets.Qualification != ConfluencePullIncludeQualified || assets.Complete == nil || !*assets.Complete {
				t.Fatalf("resumed assets include=%+v", assets)
			}
		})
	}
}

func TestConfluencePullIncludesKeepPreviewRequestedWorkDeferred(t *testing.T) {
	store := &pullStore{pages: map[string]*domain.Resource{
		"100": {ID: "100", Title: "Alpha", SpaceKey: "DOC", Version: 1, Body: []byte(`<p>body</p>`)},
	}}
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store, assets: failingConfluenceAssetResolver{}}).Pull(
		t.Context(), PullOpts{ID: "100", Into: t.TempDir(), Assets: true, Comments: true, DryRun: true},
	)
	if err != nil {
		t.Fatalf("Pull preview: %v", err)
	}
	for _, dimension := range []string{ConfluencePullIncludeAssets, ConfluencePullIncludeComments} {
		include := confluencePullInclude(t, result, dimension)
		if include.Qualification != ConfluencePullIncludeDeferred || !include.Requested || include.Complete != nil || include.Reason != ConfluencePullIncludeReasonPreviewDeferred {
			t.Fatalf("%s include=%+v, want requested preview deferral without completeness", dimension, include)
		}
	}
	if store.listCommentsCalls != 0 {
		t.Fatalf("preview made %d comment reads", store.listCommentsCalls)
	}
}

func TestConfluencePullIncludeRecordRejectsOpenOrMissingReason(t *testing.T) {
	result := &PullResult{}
	result.Includes, result.includeProgress = newConfluencePullIncludes(PullOpts{Assets: true}, 1)
	for _, tc := range []struct {
		qualification string
		reason        string
	}{
		{qualification: ConfluencePullIncludeFailed},
		{qualification: ConfluencePullIncludePartial, reason: "backend said something"},
		{qualification: ConfluencePullIncludeDeferred, reason: ConfluencePullIncludeReasonNotAttempted},
	} {
		if err := result.recordConfluencePullInclude(ConfluencePullIncludeAssets, tc.qualification, tc.reason); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("qualification=%q reason=%q err=%v", tc.qualification, tc.reason, err)
		}
	}
	include := confluencePullInclude(t, result, ConfluencePullIncludeAssets)
	if include.Qualification != ConfluencePullIncludeDeferred || include.Complete != nil || include.Reason != ConfluencePullIncludeReasonNotAttempted {
		t.Fatalf("invalid records mutated include=%+v", include)
	}
}

func TestConfluencePullPublicationFailureDemotesOnlyStagedIncludes(t *testing.T) {
	result := &PullResult{}
	result.Includes, result.includeProgress = newConfluencePullIncludes(PullOpts{Assets: true, Comments: true}, 1)
	run := &confluencePullRun{opts: PullOpts{Assets: true, Comments: true}, result: result}
	syntheticErr := errors.New("synthetic publication failure")
	if got := run.failStagedConfluencePullIncludes(nil, syntheticErr); !errors.Is(got, syntheticErr) {
		t.Fatalf("empty-stage error=%v", got)
	}
	if include := confluencePullInclude(t, result, ConfluencePullIncludeAssets); include.Qualification != ConfluencePullIncludeDeferred {
		t.Fatalf("generic publication failure changed empty asset stage: %+v", include)
	}
	evidence := []domain.ConfluencePullIncludeEvidence{
		confluencePullIncludeEvidence(ConfluencePullIncludeAssets, ConfluencePullIncludeQualified, ""),
		confluencePullIncludeEvidence(ConfluencePullIncludeComments, ConfluencePullIncludeQualified, ""),
	}
	if got := run.failStagedConfluencePullIncludes(evidence, syntheticErr); !errors.Is(got, syntheticErr) {
		t.Fatalf("publication error=%v", got)
	}
	for _, dimension := range []string{ConfluencePullIncludeAssets, ConfluencePullIncludeComments} {
		include := confluencePullInclude(t, result, dimension)
		if include.Qualification != ConfluencePullIncludeFailed || include.Complete == nil || *include.Complete ||
			include.Reason != ConfluencePullIncludeReasonStagingFailed {
			t.Fatalf("%s publication failure include=%+v", dimension, include)
		}
	}
}
