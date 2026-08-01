package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/compatibility"
	"github.com/isukharev/atl/internal/domain"
)

type confluenceCommentMutationStore struct {
	*recordingStore
	user       domain.ConfluenceUserIdentity
	comments   []domain.ConfluenceCommentRecord
	partial    bool
	userCalls  int
	listCalls  int
	beforeList func(int)
}

func (s *confluenceCommentMutationStore) CurrentConfluenceUser(context.Context) (domain.ConfluenceUserIdentity, error) {
	s.userCalls++
	return s.user, nil
}

func (s *confluenceCommentMutationStore) ListConfluenceComments(_ context.Context, pageID string, opts domain.ConfluenceCommentReadOptions) (domain.ConfluenceCommentInventory, error) {
	s.listCalls++
	if s.beforeList != nil {
		s.beforeList(s.listCalls)
	}
	wantVersion := s.page.Version
	if pageID != "42" || opts.ParentVersion != wantVersion || !opts.DepthAll || len(opts.Locations) != 0 {
		return domain.ConfluenceCommentInventory{}, errors.New("unexpected qualified read")
	}
	inventory := completeQualifiedComments(append([]domain.ConfluenceCommentRecord(nil), s.comments...)...)
	if s.partial {
		inventory.CommentsComplete = false
		inventory.PartialReasons = []string{domain.ConfluenceCommentPartialPageLimit}
	}
	return inventory, nil
}

type confluenceCommentMutatorFake struct {
	store               *confluenceCommentMutationStore
	result              domain.ConfluenceCommentMutationResult
	err                 error
	commit              func(*confluenceCommentMutationStore, domain.ConfluenceCommentMutationRequest)
	calls               int
	singleAttempt       bool
	request             domain.ConfluenceCommentMutationRequest
	notAttempted        bool
	omitAttemptMetadata bool
}

func (m *confluenceCommentMutatorFake) MutateConfluenceComment(ctx context.Context, request domain.ConfluenceCommentMutationRequest) (domain.ConfluenceCommentMutationResult, error) {
	m.calls++
	m.singleAttempt = domain.SingleAttempt(ctx)
	m.request = request
	if m.commit != nil {
		m.commit(m.store, request)
	}
	if m.err == nil {
		return m.result, nil
	}
	if m.omitAttemptMetadata {
		return m.result, m.err
	}
	return m.result, confluenceCommentMutationAttemptError{cause: m.err, attempted: !m.notAttempted}
}

type confluenceCommentMutationAttemptError struct {
	cause     error
	attempted bool
}

func (e confluenceCommentMutationAttemptError) Error() string                  { return e.cause.Error() }
func (e confluenceCommentMutationAttemptError) Unwrap() error                  { return e.cause }
func (e confluenceCommentMutationAttemptError) DiagnosticWriteAttempted() bool { return e.attempted }
func (e confluenceCommentMutationAttemptError) HTTPStatus() int {
	var status interface{ HTTPStatus() int }
	if errors.As(e.cause, &status) {
		return status.HTTPStatus()
	}
	return 0
}

func confluenceCommentMutationActivation() compatibility.Activation {
	return compatibility.Activation{
		ProviderID: compatibility.ConfluenceInlineCommentsDCProfileID,
		Version:    "9.5.2", BuildNumber: "12345",
	}
}

func confluenceInlineRoot(resolution domain.ConfluenceCommentResolution) domain.ConfluenceCommentRecord {
	rootID := "101"
	return domain.ConfluenceCommentRecord{
		ID: rootID, PageID: "42", RootID: &rootID,
		Relation: domain.ConfluenceCommentRelationRoot, Location: domain.ConfluenceCommentLocationInline,
		Resolution: resolution, Version: 2, AuthorID: "author-2", AuthorDisplayName: "Author Two",
		CreatedAt: "2026-08-01T01:00:00.000Z", UpdatedAt: "2026-08-01T01:00:00.000Z",
		Body: "root", BodyStorage: "<p>root</p>", MarkerRef: "ref-101", OriginalSelection: "selected",
	}
}

func confluenceInlineReply(id, actor, body string) domain.ConfluenceCommentRecord {
	rootID, parentID := "101", "101"
	return domain.ConfluenceCommentRecord{
		ID: id, PageID: "42", ParentID: &parentID, RootID: &rootID,
		Relation: domain.ConfluenceCommentRelationReply, Location: domain.ConfluenceCommentLocationInline,
		Resolution: domain.ConfluenceCommentResolutionOpen, Version: 1,
		AuthorID: actor, AuthorDisplayName: "Example User",
		CreatedAt: "2026-08-01T02:00:00.000Z", UpdatedAt: "2026-08-01T02:00:00.000Z",
		Body: body, BodyStorage: body, MarkerRef: "ref-101", OriginalSelection: "selected",
	}
}

func newConfluenceCommentMutationFixture(resolution domain.ConfluenceCommentResolution) (*ConfluenceService, *confluenceCommentMutationStore, *confluenceCommentMutatorFake) {
	store := &confluenceCommentMutationStore{
		recordingStore: &recordingStore{page: &domain.Resource{
			ID: "42", Type: "page", Version: 7,
			Body: []byte(`<p><ac:inline-comment-marker ac:ref="ref-101">selected</ac:inline-comment-marker></p>`),
		}},
		user:     domain.ConfluenceUserIdentity{ID: "actor-1", DisplayName: "Example User"},
		comments: []domain.ConfluenceCommentRecord{confluenceInlineRoot(resolution)},
	}
	mutator := &confluenceCommentMutatorFake{store: store}
	activation := confluenceCommentMutationActivation()
	service := &ConfluenceService{
		store: store, baseURL: "https://confluence.example.test",
		commentMutator: mutator, commentMutationActivation: &activation,
	}
	return service, store, mutator
}

func previewConfluenceCommentMutation(t *testing.T, service *ConfluenceService, opts ConfluenceCommentMutationOpts) *ConfluenceCommentMutationGuardedResult {
	t.Helper()
	result, err := service.MutateCommentGuarded(context.Background(), "42", opts)
	if err != nil || result == nil || (result.Status != "would_apply" && result.Status != "no_op") || result.ProposalHash == "" {
		t.Fatalf("preview=%+v err=%v", result, err)
	}
	return result
}

func TestConfluenceCommentMutationReplyPreviewIsBodyFreeAndBindsProvider(t *testing.T) {
	service, store, mutator := newConfluenceCommentMutationFixture(domain.ConfluenceCommentResolutionOpen)
	body := []byte("<p>private reply</p>")
	preview := previewConfluenceCommentMutation(t, service, ConfluenceCommentMutationOpts{
		Operation: domain.ConfluenceCommentMutationReply, ThreadID: "101", Body: body,
	})
	if preview.Provider.ID != compatibility.ConfluenceInlineCommentsDCProfileID || preview.BodySHA256 == "" ||
		preview.BodyBytes != len(body) || preview.ThreadVersion != 2 || preview.SourceState != domain.ConfluenceCommentResolutionOpen ||
		preview.BaselineSHA256 == "" || !preview.Complete || mutator.calls != 0 || store.listCalls != 1 {
		t.Fatalf("preview=%+v writes=%d lists=%d", preview, mutator.calls, store.listCalls)
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private reply", "9.5.2", "12345", "configured_identity"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("preview leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestConfluenceCommentMutationProposalHashBindsReviewedInputs(t *testing.T) {
	service, _, _ := newConfluenceCommentMutationFixture(domain.ConfluenceCommentResolutionOpen)
	snapshot, err := service.confluenceCommentMutationSnapshot(context.Background(), "42", domain.ConfluenceCommentMutationReply, "101")
	if err != nil {
		t.Fatal(err)
	}
	base := confluenceCommentMutationProposalHash(snapshot, domain.ConfluenceCommentMutationReply, "body-a", 7)
	variants := []struct {
		name      string
		snapshot  confluenceCommentMutationSnapshot
		operation domain.ConfluenceCommentMutationOperation
		bodyHash  string
		bodyBytes int
	}{
		{name: "backend", snapshot: snapshot, operation: domain.ConfluenceCommentMutationReply, bodyHash: "body-a", bodyBytes: 7},
		{name: "configured identity", snapshot: snapshot, operation: domain.ConfluenceCommentMutationReply, bodyHash: "body-a", bodyBytes: 7},
		{name: "page version", snapshot: snapshot, operation: domain.ConfluenceCommentMutationReply, bodyHash: "body-a", bodyBytes: 7},
		{name: "thread version", snapshot: snapshot, operation: domain.ConfluenceCommentMutationReply, bodyHash: "body-a", bodyBytes: 7},
		{name: "actor", snapshot: snapshot, operation: domain.ConfluenceCommentMutationReply, bodyHash: "body-a", bodyBytes: 7},
		{name: "capability", snapshot: snapshot, operation: domain.ConfluenceCommentMutationReply, bodyHash: "body-a", bodyBytes: 7},
		{name: "baseline", snapshot: snapshot, operation: domain.ConfluenceCommentMutationReply, bodyHash: "body-a", bodyBytes: 7},
		{name: "operation", snapshot: snapshot, operation: domain.ConfluenceCommentMutationResolve, bodyHash: "body-a", bodyBytes: 7},
		{name: "body hash", snapshot: snapshot, operation: domain.ConfluenceCommentMutationReply, bodyHash: "body-b", bodyBytes: 7},
		{name: "body bytes", snapshot: snapshot, operation: domain.ConfluenceCommentMutationReply, bodyHash: "body-a", bodyBytes: 8},
	}
	variants[0].snapshot.backend = "https://other.example.test"
	variants[1].snapshot.configuredIdentity = "different"
	variants[2].snapshot.pageVersion++
	variants[3].snapshot.target.Version++
	variants[4].snapshot.actor.ID = "actor-9"
	variants[5].snapshot.capabilities.Resolution = domain.ConfluenceCapabilityDocumented
	variants[6].snapshot.baselineSHA256 = "different"
	for _, variant := range variants {
		if got := confluenceCommentMutationProposalHash(variant.snapshot, variant.operation, variant.bodyHash, variant.bodyBytes); got == base {
			t.Errorf("%s did not change proposal hash", variant.name)
		}
	}
}

func TestConfluenceCommentMutationRejectsClosedOperationMatrixBeforeReads(t *testing.T) {
	for _, test := range []struct {
		name string
		opts ConfluenceCommentMutationOpts
	}{
		{name: "unknown operation", opts: ConfluenceCommentMutationOpts{Operation: "delete", ThreadID: "101"}},
		{name: "invalid thread", opts: ConfluenceCommentMutationOpts{Operation: domain.ConfluenceCommentMutationResolve, ThreadID: "0"}},
		{name: "reply body missing", opts: ConfluenceCommentMutationOpts{Operation: domain.ConfluenceCommentMutationReply, ThreadID: "101"}},
		{name: "reply body invalid", opts: ConfluenceCommentMutationOpts{Operation: domain.ConfluenceCommentMutationReply, ThreadID: "101", Body: []byte("<p>")}},
		{name: "resolve with body", opts: ConfluenceCommentMutationOpts{Operation: domain.ConfluenceCommentMutationResolve, ThreadID: "101", Body: []byte("<p>x</p>")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, store, mutator := newConfluenceCommentMutationFixture(domain.ConfluenceCommentResolutionOpen)
			_, err := service.MutateCommentGuarded(context.Background(), "42", test.opts)
			if err == nil || store.userCalls != 0 || store.listCalls != 0 || mutator.calls != 0 {
				t.Fatalf("err=%v users=%d lists=%d writes=%d", err, store.userCalls, store.listCalls, mutator.calls)
			}
		})
	}
}

func TestConfluenceCommentMutationReplyApplyWritesOnceAndReconciles(t *testing.T) {
	service, _, mutator := newConfluenceCommentMutationFixture(domain.ConfluenceCommentResolutionOpen)
	body := []byte("<p>reviewed reply</p>")
	preview := previewConfluenceCommentMutation(t, service, ConfluenceCommentMutationOpts{
		Operation: domain.ConfluenceCommentMutationReply, ThreadID: "101", Body: body,
	})
	mutator.result = domain.ConfluenceCommentMutationResult{
		Operation: domain.ConfluenceCommentMutationReply, ThreadID: "101", CommentID: "202",
	}
	mutator.commit = func(store *confluenceCommentMutationStore, request domain.ConfluenceCommentMutationRequest) {
		store.comments = append(store.comments, confluenceInlineReply("202", "actor-1", string(request.BodyStorage)))
	}
	result, err := service.MutateCommentGuarded(context.Background(), "42", ConfluenceCommentMutationOpts{
		Operation: domain.ConfluenceCommentMutationReply, ThreadID: "101", Body: body,
		Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	if err != nil || result == nil || result.Status != "applied" || result.CommentID != "202" || !result.Reconciled ||
		mutator.calls != 1 || !mutator.singleAttempt || mutator.request.PageID != "42" || mutator.request.ThreadID != "101" ||
		string(mutator.request.BodyStorage) != string(body) {
		t.Fatalf("result=%+v err=%v writes=%d single=%t request=%+v", result, err, mutator.calls, mutator.singleAttempt, mutator.request)
	}
}

func TestConfluenceCommentMutationResolutionApplyAndNoOp(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation domain.ConfluenceCommentMutationOperation
		before    domain.ConfluenceCommentResolution
		after     domain.ConfluenceCommentResolution
		resolved  bool
	}{
		{name: "resolve", operation: domain.ConfluenceCommentMutationResolve, before: domain.ConfluenceCommentResolutionOpen, after: domain.ConfluenceCommentResolutionResolved, resolved: true},
		{name: "reopen", operation: domain.ConfluenceCommentMutationReopen, before: domain.ConfluenceCommentResolutionResolved, after: domain.ConfluenceCommentResolutionOpen, resolved: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, mutator := newConfluenceCommentMutationFixture(test.before)
			preview := previewConfluenceCommentMutation(t, service, ConfluenceCommentMutationOpts{Operation: test.operation, ThreadID: "101"})
			mutator.result = domain.ConfluenceCommentMutationResult{Operation: test.operation, ThreadID: "101", CommentID: "101", Resolved: test.resolved}
			mutator.commit = func(store *confluenceCommentMutationStore, _ domain.ConfluenceCommentMutationRequest) {
				store.comments[0].Resolution = test.after
				store.comments[0].Version++
				store.comments[0].UpdatedAt = "2026-08-01T03:00:00.000Z"
			}
			result, err := service.MutateCommentGuarded(context.Background(), "42", ConfluenceCommentMutationOpts{
				Operation: test.operation, ThreadID: "101", Apply: true, ExpectedProposalHash: preview.ProposalHash,
			})
			if err != nil || result.Status != "applied" || result.CommentID != "101" || !result.Reconciled || mutator.calls != 1 {
				t.Fatalf("result=%+v err=%v writes=%d", result, err, mutator.calls)
			}

			noOp := previewConfluenceCommentMutation(t, service, ConfluenceCommentMutationOpts{Operation: test.operation, ThreadID: "101"})
			if noOp.Status != "no_op" || mutator.calls != 1 {
				t.Fatalf("no-op=%+v writes=%d", noOp, mutator.calls)
			}
			appliedNoOp, err := service.MutateCommentGuarded(context.Background(), "42", ConfluenceCommentMutationOpts{
				Operation: test.operation, ThreadID: "101", Apply: true, ExpectedProposalHash: noOp.ProposalHash,
			})
			if err != nil || appliedNoOp.Status != "no_op" || mutator.calls != 1 {
				t.Fatalf("applied no-op=%+v err=%v writes=%d", appliedNoOp, err, mutator.calls)
			}
		})
	}
}

func TestConfluenceCommentMutationRejectsDriftAndPartialEvidenceBeforeWrite(t *testing.T) {
	t.Run("proposal drift", func(t *testing.T) {
		service, store, mutator := newConfluenceCommentMutationFixture(domain.ConfluenceCommentResolutionOpen)
		preview := previewConfluenceCommentMutation(t, service, ConfluenceCommentMutationOpts{Operation: domain.ConfluenceCommentMutationResolve, ThreadID: "101"})
		store.comments[0].Version++
		result, err := service.MutateCommentGuarded(context.Background(), "42", ConfluenceCommentMutationOpts{
			Operation: domain.ConfluenceCommentMutationResolve, ThreadID: "101", Apply: true, ExpectedProposalHash: preview.ProposalHash,
		})
		if result == nil || result.Status != "conflict" || !errors.Is(err, domain.ErrCheckFailed) || mutator.calls != 0 {
			t.Fatalf("result=%+v err=%v writes=%d", result, err, mutator.calls)
		}
	})

	t.Run("partial inventory", func(t *testing.T) {
		service, store, mutator := newConfluenceCommentMutationFixture(domain.ConfluenceCommentResolutionOpen)
		store.partial = true
		_, err := service.MutateCommentGuarded(context.Background(), "42", ConfluenceCommentMutationOpts{Operation: domain.ConfluenceCommentMutationResolve, ThreadID: "101"})
		if !errors.Is(err, domain.ErrCheckFailed) || mutator.calls != 0 {
			t.Fatalf("err=%v writes=%d", err, mutator.calls)
		}
	})

	t.Run("immediate prewrite drift", func(t *testing.T) {
		service, store, mutator := newConfluenceCommentMutationFixture(domain.ConfluenceCommentResolutionOpen)
		preview := previewConfluenceCommentMutation(t, service, ConfluenceCommentMutationOpts{Operation: domain.ConfluenceCommentMutationResolve, ThreadID: "101"})
		store.beforeList = func(call int) {
			if call == 3 {
				store.comments[0].Version++
			}
		}
		result, err := service.MutateCommentGuarded(context.Background(), "42", ConfluenceCommentMutationOpts{
			Operation: domain.ConfluenceCommentMutationResolve, ThreadID: "101", Apply: true, ExpectedProposalHash: preview.ProposalHash,
		})
		if result == nil || result.Status != "conflict" || !errors.Is(err, domain.ErrCheckFailed) || mutator.calls != 0 {
			t.Fatalf("result=%+v err=%v writes=%d", result, err, mutator.calls)
		}
	})
}

type confluenceCommentMutationStatusError int

func (e confluenceCommentMutationStatusError) Error() string   { return "private backend prose" }
func (e confluenceCommentMutationStatusError) HTTPStatus() int { return int(e) }

func TestConfluenceCommentMutationAmbiguousOutcomesNeverReplay(t *testing.T) {
	t.Run("missing attempt metadata remains ambiguous", func(t *testing.T) {
		service, _, mutator := newConfluenceCommentMutationFixture(domain.ConfluenceCommentResolutionOpen)
		body := []byte("<p>x</p>")
		preview := previewConfluenceCommentMutation(t, service, ConfluenceCommentMutationOpts{Operation: domain.ConfluenceCommentMutationReply, ThreadID: "101", Body: body})
		mutator.err = confluenceCommentMutationStatusError(500)
		mutator.omitAttemptMetadata = true
		result, err := service.MutateCommentGuarded(context.Background(), "42", ConfluenceCommentMutationOpts{
			Operation: domain.ConfluenceCommentMutationReply, ThreadID: "101", Body: body, Apply: true, ExpectedProposalHash: preview.ProposalHash,
		})
		var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
		if result == nil || result.Status != "outcome_unknown" || !errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})

	t.Run("qualification failure does not reconcile", func(t *testing.T) {
		service, store, mutator := newConfluenceCommentMutationFixture(domain.ConfluenceCommentResolutionOpen)
		preview := previewConfluenceCommentMutation(t, service, ConfluenceCommentMutationOpts{Operation: domain.ConfluenceCommentMutationResolve, ThreadID: "101"})
		mutator.err = domain.ErrCheckFailed
		mutator.notAttempted = true
		result, err := service.MutateCommentGuarded(context.Background(), "42", ConfluenceCommentMutationOpts{
			Operation: domain.ConfluenceCommentMutationResolve, ThreadID: "101", Apply: true, ExpectedProposalHash: preview.ProposalHash,
		})
		if result == nil || result.Status != "not_applied" || !errors.Is(err, domain.ErrCheckFailed) || mutator.calls != 1 || store.listCalls != 3 {
			t.Fatalf("result=%+v err=%v writes=%d reads=%d", result, err, mutator.calls, store.listCalls)
		}
	})

	t.Run("committed timeout recovers reply", func(t *testing.T) {
		service, _, mutator := newConfluenceCommentMutationFixture(domain.ConfluenceCommentResolutionOpen)
		body := []byte("<p>x</p>")
		preview := previewConfluenceCommentMutation(t, service, ConfluenceCommentMutationOpts{Operation: domain.ConfluenceCommentMutationReply, ThreadID: "101", Body: body})
		mutator.err = context.DeadlineExceeded
		mutator.commit = func(store *confluenceCommentMutationStore, request domain.ConfluenceCommentMutationRequest) {
			store.comments = append(store.comments, confluenceInlineReply("202", "actor-1", string(request.BodyStorage)))
		}
		result, err := service.MutateCommentGuarded(context.Background(), "42", ConfluenceCommentMutationOpts{
			Operation: domain.ConfluenceCommentMutationReply, ThreadID: "101", Body: body, Apply: true, ExpectedProposalHash: preview.ProposalHash,
		})
		if err != nil || result.Status != "recovered" || result.CommentID != "202" || mutator.calls != 1 {
			t.Fatalf("result=%+v err=%v writes=%d", result, err, mutator.calls)
		}
	})

	t.Run("uncommitted server failure is unknown", func(t *testing.T) {
		service, _, mutator := newConfluenceCommentMutationFixture(domain.ConfluenceCommentResolutionOpen)
		body := []byte("<p>x</p>")
		preview := previewConfluenceCommentMutation(t, service, ConfluenceCommentMutationOpts{Operation: domain.ConfluenceCommentMutationReply, ThreadID: "101", Body: body})
		mutator.err = confluenceCommentMutationStatusError(500)
		result, err := service.MutateCommentGuarded(context.Background(), "42", ConfluenceCommentMutationOpts{
			Operation: domain.ConfluenceCommentMutationReply, ThreadID: "101", Body: body, Apply: true, ExpectedProposalHash: preview.ProposalHash,
		})
		var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
		if result == nil || result.Status != "outcome_unknown" || !errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() ||
			mutator.calls != 1 || strings.Contains(err.Error(), "private backend prose") {
			t.Fatalf("result=%+v err=%v writes=%d", result, err, mutator.calls)
		}
	})

	t.Run("definitive rejection does not reconcile", func(t *testing.T) {
		service, store, mutator := newConfluenceCommentMutationFixture(domain.ConfluenceCommentResolutionOpen)
		preview := previewConfluenceCommentMutation(t, service, ConfluenceCommentMutationOpts{Operation: domain.ConfluenceCommentMutationResolve, ThreadID: "101"})
		mutator.err = confluenceCommentMutationStatusError(400)
		result, err := service.MutateCommentGuarded(context.Background(), "42", ConfluenceCommentMutationOpts{
			Operation: domain.ConfluenceCommentMutationResolve, ThreadID: "101", Apply: true, ExpectedProposalHash: preview.ProposalHash,
		})
		if result == nil || result.Status != "not_applied" || err == nil || mutator.calls != 1 || store.listCalls != 3 || strings.Contains(err.Error(), "private backend prose") {
			t.Fatalf("result=%+v err=%v writes=%d lists=%d", result, err, mutator.calls, store.listCalls)
		}
	})
}
