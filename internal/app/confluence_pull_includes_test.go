package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type failingConfluenceAssetResolver struct{}

func (failingConfluenceAssetResolver) Resolve(context.Context, *domain.Resource, domain.Ref) ([]byte, string, error) {
	return nil, "", errors.New("synthetic asset read failure")
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
