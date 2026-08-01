package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/isukharev/atl/internal/domain"
)

type confluenceInlinePreparerFake struct {
	result domain.ConfluenceInlineCommentPreparation
	err    error
	calls  int
	last   domain.ConfluenceInlineCommentPreparationRequest
	before func(int)
}

func (p *confluenceInlinePreparerFake) PrepareConfluenceInlineComment(_ context.Context, request domain.ConfluenceInlineCommentPreparationRequest) (domain.ConfluenceInlineCommentPreparation, error) {
	p.calls++
	p.last = request
	if p.before != nil {
		p.before(p.calls)
	}
	result := p.result
	result.LastFetchTime += int64(p.calls)
	result.SerializedHighlights = cloneConfluenceInlineHighlights(result.SerializedHighlights)
	result.MarkerRefs = append([]string(nil), result.MarkerRefs...)
	return result, p.err
}

func newConfluenceInlineCreateFixture() (*ConfluenceService, *confluenceCommentMutationStore, *confluenceCommentMutatorFake, *confluenceInlinePreparerFake) {
	service, store, mutator := newConfluenceCommentMutationFixture(domain.ConfluenceCommentResolutionOpen)
	store.page.Body = []byte(`<p><ac:inline-comment-marker ac:ref="ref-101">selected</ac:inline-comment-marker> choose this text</p>`)
	preparer := &confluenceInlinePreparerFake{result: domain.ConfluenceInlineCommentPreparation{
		PageID: "42", PageVersion: 7, LastFetchTime: 1700000000000,
		SearchSelection: "choose this", OriginalSelection: "choose this", HighlightedSelection: "choose this",
		NumMatches: 1, MatchIndex: 0, ViewSHA256: strings.Repeat("a", 64), MarkerRefs: []string{"ref-101"},
		SerializedHighlights: []domain.ConfluenceInlineHighlightGeometry{{
			Text: "choose this", ChildIndexPath: []int{0, 1}, PreviousTextSiblingOffset: 0,
			Length: len(utf16.Encode([]rune("choose this"))),
		}},
	}}
	service.commentPreparer = preparer
	return service, store, mutator, preparer
}

func confluenceInlineCreateOpts() ConfluenceCommentMutationOpts {
	return ConfluenceCommentMutationOpts{
		Operation: domain.ConfluenceCommentMutationInlineCreate,
		Body:      []byte(`<p>private inline body</p>`), Selection: []byte("choose this"), Occurrence: 0,
	}
}

func commitConfluenceInlineCreate(store *confluenceCommentMutationStore, request domain.ConfluenceCommentMutationRequest) {
	commentID, markerRef := "303", "ref-303"
	store.page.Version = 8
	store.page.Body = []byte(`<p><ac:inline-comment-marker ac:ref="ref-101">selected</ac:inline-comment-marker> <ac:inline-comment-marker ac:ref="ref-303">choose this</ac:inline-comment-marker> text</p>`)
	store.comments = append(store.comments, domain.ConfluenceCommentRecord{
		ID: commentID, PageID: "42", RootID: &commentID,
		Relation: domain.ConfluenceCommentRelationRoot, Location: domain.ConfluenceCommentLocationInline,
		Resolution: domain.ConfluenceCommentResolutionOpen, Version: 1,
		AuthorID: "actor-1", AuthorDisplayName: "Example User",
		CreatedAt: "2026-08-01T04:00:00.000Z", UpdatedAt: "2026-08-01T04:00:00.000Z",
		Body: "private inline body", BodyStorage: string(request.BodyStorage),
		MarkerRef: markerRef, OriginalSelection: request.OriginalSelection,
	})
}

func TestConfluenceInlineCreatePreviewAndApply(t *testing.T) {
	service, _, mutator, preparer := newConfluenceInlineCreateFixture()
	opts := confluenceInlineCreateOpts()
	preview := previewConfluenceCommentMutation(t, service, opts)
	if preview.Occurrence == nil || *preview.Occurrence != 0 || preview.MatchIndex == nil || *preview.MatchIndex != 0 ||
		preview.NumMatches != 1 || preview.HighlightCount != 1 ||
		preview.SelectionSHA256 == "" || preview.SelectionBytes != len(opts.Selection) || preview.PageBodySHA256 == "" ||
		preview.GeometrySHA256 == "" || preview.MarkerCount == nil || *preview.MarkerCount != 1 || preview.MarkerSHA256 == "" || preparer.calls != 1 || mutator.calls != 0 {
		t.Fatalf("preview=%+v preparations=%d writes=%d", preview, preparer.calls, mutator.calls)
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private inline body", "choose this", "170000000000", "9.5.2", "12345", strings.Repeat("a", 64)} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("preview leaked %q: %s", forbidden, encoded)
		}
	}

	mutator.result = domain.ConfluenceCommentMutationResult{
		Operation: domain.ConfluenceCommentMutationInlineCreate,
		ThreadID:  "303", CommentID: "303", MarkerRef: "ref-303",
		OriginalSelection: "choose this", PageVersion: 8,
	}
	mutator.commit = commitConfluenceInlineCreate
	opts.Apply = true
	opts.ExpectedProposalHash = preview.ProposalHash
	result, err := service.MutateCommentGuarded(context.Background(), "42", opts)
	if err != nil || result == nil || result.Status != "applied" || result.CommentID != "303" || result.ThreadID != "303" ||
		result.MarkerRef != "ref-303" || result.ResultPageVersion != 8 || !result.Reconciled || mutator.calls != 1 ||
		!mutator.singleAttempt || preparer.calls != 3 || mutator.request.LastFetchTime != 1700000000003 ||
		mutator.request.PageVersion != 7 || mutator.request.MatchIndex != 0 || mutator.request.NumMatches != 1 {
		t.Fatalf("result=%+v err=%v writes=%d preparations=%d request=%+v", result, err, mutator.calls, preparer.calls, mutator.request)
	}
}

func TestConfluenceInlineCreateReconcilesNormalizedSearchWithRawDOMSelection(t *testing.T) {
	service, store, mutator, preparer := newConfluenceInlineCreateFixture()
	const rawSelection = "choose\u00a0this"
	store.page.Body = []byte("<p><ac:inline-comment-marker ac:ref=\"ref-101\">selected</ac:inline-comment-marker> choose\u00a0this text</p>")
	preparer.result.SearchSelection = "choose this"
	preparer.result.OriginalSelection = "choose this"
	preparer.result.HighlightedSelection = rawSelection
	preparer.result.SerializedHighlights[0].Text = rawSelection
	preparer.result.SerializedHighlights[0].Length = len(utf16.Encode([]rune(rawSelection)))

	opts := confluenceInlineCreateOpts()
	opts.Selection = []byte(" \ufeffchoose\u00a0this\n")
	preview := previewConfluenceCommentMutation(t, service, opts)
	mutator.result = domain.ConfluenceCommentMutationResult{
		Operation: domain.ConfluenceCommentMutationInlineCreate,
		ThreadID:  "303", CommentID: "303", MarkerRef: "ref-303",
		OriginalSelection: "choose this", PageVersion: 8,
	}
	mutator.commit = func(store *confluenceCommentMutationStore, request domain.ConfluenceCommentMutationRequest) {
		commentID, markerRef := "303", "ref-303"
		store.page.Version = 8
		store.page.Body = []byte("<p><ac:inline-comment-marker ac:ref=\"ref-101\">selected</ac:inline-comment-marker> <ac:inline-comment-marker ac:ref=\"ref-303\">choose\u00a0this</ac:inline-comment-marker> text</p>")
		store.comments = append(store.comments, domain.ConfluenceCommentRecord{
			ID: commentID, PageID: "42", RootID: &commentID,
			Relation: domain.ConfluenceCommentRelationRoot, Location: domain.ConfluenceCommentLocationInline,
			Resolution: domain.ConfluenceCommentResolutionOpen, Version: 1,
			AuthorID: "actor-1", AuthorDisplayName: "Example User",
			CreatedAt: "2026-08-01T04:00:00.000Z", UpdatedAt: "2026-08-01T04:00:00.000Z",
			Body: "private inline body", BodyStorage: string(request.BodyStorage),
			MarkerRef: markerRef, OriginalSelection: request.OriginalSelection,
		})
	}
	opts.Apply = true
	opts.ExpectedProposalHash = preview.ProposalHash
	result, err := service.MutateCommentGuarded(context.Background(), "42", opts)
	if err != nil || result == nil || result.Status != "applied" || !result.Reconciled || mutator.calls != 1 {
		t.Fatalf("result=%+v err=%v request=%+v", result, err, mutator.request)
	}
	if mutator.request.SearchSelection != "choose this" || mutator.request.OriginalSelection != "choose this" ||
		mutator.request.SerializedHighlights[0].Text != rawSelection {
		t.Fatalf("normalized request = %+v", mutator.request)
	}
}

func TestConfluenceInlineCreateProposalBindsStablePreparationButNotRequestTime(t *testing.T) {
	service, _, _, _ := newConfluenceInlineCreateFixture()
	snapshot, err := service.confluenceInlineCreateSnapshot(context.Background(), "42", "choose this", 0)
	if err != nil {
		t.Fatal(err)
	}
	base := confluenceInlineCreateProposalHash(snapshot, "body", 7, "selection", 11, 0)
	volatile := snapshot
	volatile.preparation.LastFetchTime++
	if got := confluenceInlineCreateProposalHash(volatile, "body", 7, "selection", 11, 0); got != base {
		t.Fatal("volatile request time changed reviewed proposal")
	}
	variants := []struct {
		name   string
		mutate func(*confluenceInlineCreateSnapshot)
	}{
		{name: "page body", mutate: func(value *confluenceInlineCreateSnapshot) { value.pageBodySHA256 = "different" }},
		{name: "view", mutate: func(value *confluenceInlineCreateSnapshot) { value.preparation.ViewSHA256 = strings.Repeat("b", 64) }},
		{name: "geometry", mutate: func(value *confluenceInlineCreateSnapshot) { value.preparation.SerializedHighlights[0].Length++ }},
		{name: "search selection", mutate: func(value *confluenceInlineCreateSnapshot) { value.preparation.SearchSelection = "different" }},
		{name: "wire selection", mutate: func(value *confluenceInlineCreateSnapshot) { value.preparation.OriginalSelection = "different" }},
		{name: "highlighted selection", mutate: func(value *confluenceInlineCreateSnapshot) { value.highlightedSelection = "different" }},
		{name: "match count", mutate: func(value *confluenceInlineCreateSnapshot) { value.preparation.NumMatches++ }},
		{name: "marker", mutate: func(value *confluenceInlineCreateSnapshot) { value.markers[0].Ref = "different" }},
		{name: "actor", mutate: func(value *confluenceInlineCreateSnapshot) { value.actor.ID = "different" }},
		{name: "activation", mutate: func(value *confluenceInlineCreateSnapshot) { value.configuredIdentity = "different" }},
		{name: "baseline", mutate: func(value *confluenceInlineCreateSnapshot) { value.baselineSHA256 = "different" }},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			value := snapshot
			value.markers = append([]confluenceInlineMarkerEvidence(nil), snapshot.markers...)
			value.preparation.SerializedHighlights = cloneConfluenceInlineHighlights(snapshot.preparation.SerializedHighlights)
			variant.mutate(&value)
			if got := confluenceInlineCreateProposalHash(value, "body", 7, "selection", 11, 0); got == base {
				t.Fatal("stable evidence did not change proposal")
			}
		})
	}
}

func TestConfluenceInlineCreateFailsClosedBeforeWrite(t *testing.T) {
	t.Run("input matrix", func(t *testing.T) {
		service, store, mutator, preparer := newConfluenceInlineCreateFixture()
		for _, opts := range []ConfluenceCommentMutationOpts{
			{Operation: domain.ConfluenceCommentMutationInlineCreate, Body: []byte(`<p>x</p>`)},
			{Operation: domain.ConfluenceCommentMutationInlineCreate, Body: []byte(`<p>x</p>`), Selection: []byte("x"), Occurrence: -1},
			{Operation: domain.ConfluenceCommentMutationInlineCreate, ThreadID: "101", Body: []byte(`<p>x</p>`), Selection: []byte("x")},
			{Operation: domain.ConfluenceCommentMutationReply, ThreadID: "101", Body: []byte(`<p>x</p>`), Selection: []byte("x")},
		} {
			if _, err := service.MutateCommentGuarded(context.Background(), "42", opts); err == nil {
				t.Fatalf("accepted %+v", opts)
			}
		}
		if store.listCalls != 0 || preparer.calls != 0 || mutator.calls != 0 {
			t.Fatalf("lists=%d preparations=%d writes=%d", store.listCalls, preparer.calls, mutator.calls)
		}
	})

	t.Run("partial baseline", func(t *testing.T) {
		service, store, mutator, preparer := newConfluenceInlineCreateFixture()
		store.partial = true
		_, err := service.MutateCommentGuarded(context.Background(), "42", confluenceInlineCreateOpts())
		if !errors.Is(err, domain.ErrCheckFailed) || preparer.calls != 0 || mutator.calls != 0 {
			t.Fatalf("err=%v preparations=%d writes=%d", err, preparer.calls, mutator.calls)
		}
	})

	t.Run("selection collision", func(t *testing.T) {
		service, _, mutator, preparer := newConfluenceInlineCreateFixture()
		preparer.err = domain.ErrCheckFailed
		_, err := service.MutateCommentGuarded(context.Background(), "42", confluenceInlineCreateOpts())
		if !errors.Is(err, domain.ErrCheckFailed) || preparer.calls != 1 || mutator.calls != 0 || strings.Contains(err.Error(), "choose this") {
			t.Fatalf("err=%v preparations=%d writes=%d", err, preparer.calls, mutator.calls)
		}
	})

	t.Run("immediate geometry drift", func(t *testing.T) {
		service, _, mutator, preparer := newConfluenceInlineCreateFixture()
		opts := confluenceInlineCreateOpts()
		preview := previewConfluenceCommentMutation(t, service, opts)
		preparer.before = func(call int) {
			if call == 3 {
				preparer.result.ViewSHA256 = strings.Repeat("b", 64)
			}
		}
		opts.Apply = true
		opts.ExpectedProposalHash = preview.ProposalHash
		result, err := service.MutateCommentGuarded(context.Background(), "42", opts)
		if result == nil || result.Status != "conflict" || !errors.Is(err, domain.ErrCheckFailed) || mutator.calls != 0 || preparer.calls != 3 {
			t.Fatalf("result=%+v err=%v preparations=%d writes=%d", result, err, preparer.calls, mutator.calls)
		}
	})
}

func TestConfluenceInlineCreateValidationDefersPinnedWhitespaceSemanticsToPreparer(t *testing.T) {
	opts := confluenceInlineCreateOpts()
	opts.Selection = []byte("\u0085")
	if _, err := validateConfluenceCommentMutationOpts(opts); err != nil {
		t.Fatalf("pinned non-whitespace selection rejected before preparation: %v", err)
	}
}

func TestConfluenceInlineCreateAmbiguousAttemptReconcilesWithoutReplay(t *testing.T) {
	t.Run("committed timeout", func(t *testing.T) {
		service, _, mutator, _ := newConfluenceInlineCreateFixture()
		opts := confluenceInlineCreateOpts()
		preview := previewConfluenceCommentMutation(t, service, opts)
		mutator.err = context.DeadlineExceeded
		mutator.commit = commitConfluenceInlineCreate
		opts.Apply = true
		opts.ExpectedProposalHash = preview.ProposalHash
		result, err := service.MutateCommentGuarded(context.Background(), "42", opts)
		if err != nil || result.Status != "recovered" || result.CommentID != "303" || mutator.calls != 1 {
			t.Fatalf("result=%+v err=%v writes=%d", result, err, mutator.calls)
		}
	})

	t.Run("uncommitted failure", func(t *testing.T) {
		service, _, mutator, _ := newConfluenceInlineCreateFixture()
		opts := confluenceInlineCreateOpts()
		preview := previewConfluenceCommentMutation(t, service, opts)
		mutator.err = confluenceCommentMutationStatusError(500)
		opts.Apply = true
		opts.ExpectedProposalHash = preview.ProposalHash
		result, err := service.MutateCommentGuarded(context.Background(), "42", opts)
		var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
		if result == nil || result.Status != "outcome_unknown" || !errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() || mutator.calls != 1 {
			t.Fatalf("result=%+v err=%v writes=%d", result, err, mutator.calls)
		}
	})

	t.Run("unexpected page mutation", func(t *testing.T) {
		service, _, mutator, _ := newConfluenceInlineCreateFixture()
		opts := confluenceInlineCreateOpts()
		preview := previewConfluenceCommentMutation(t, service, opts)
		mutator.err = context.DeadlineExceeded
		mutator.commit = func(store *confluenceCommentMutationStore, request domain.ConfluenceCommentMutationRequest) {
			commitConfluenceInlineCreate(store, request)
			store.page.Body = append(store.page.Body, []byte(`<p>other change</p>`)...)
		}
		opts.Apply = true
		opts.ExpectedProposalHash = preview.ProposalHash
		result, err := service.MutateCommentGuarded(context.Background(), "42", opts)
		if result == nil || result.Status != "outcome_unknown" || err == nil || mutator.calls != 1 {
			t.Fatalf("result=%+v err=%v writes=%d", result, err, mutator.calls)
		}
	})
}
