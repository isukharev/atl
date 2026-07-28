package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type transitionStatusError int

func (e transitionStatusError) Error() string   { return "private backend rejection" }
func (e transitionStatusError) HTTPStatus() int { return int(e) }
func (e transitionStatusError) Unwrap() error   { return domain.ErrForbidden }

type jiraTransitionStoreStub struct {
	domain.Tracker
	issue           domain.Issue
	transitions     []domain.TransitionDef
	user            domain.User
	comments        []domain.Comment
	issueFn         func(int) (*domain.Issue, error)
	transitionFn    func(int) ([]domain.TransitionDef, error)
	userFn          func(int) (*domain.User, error)
	commentsFn      func(int) ([]domain.Comment, error)
	writeErr        error
	commitOnWrite   bool
	postReadErr     error
	postMutation    func(*jiraTransitionStoreStub)
	issueCalls      int
	transitionCalls int
	userCalls       int
	commentCalls    int
	writeCalls      int
	singleAttempt   bool
	writtenKey      string
	written         domain.JiraTransitionRequest
}

func transitionFixture() *jiraTransitionStoreStub {
	return &jiraTransitionStoreStub{
		issue: domain.Issue{
			ID: "10001", Key: "PROJ-1", StatusID: "1", Status: "Open",
			Fields: map[string]any{
				"status":  map[string]any{"id": "1", "name": "Open"},
				"updated": "2026-07-28T00:00:00.000+0000",
				"a":       "old", "b": map[string]any{"id": "1"},
			},
		},
		transitions: []domain.TransitionDef{
			{ID: "11", Name: "Finish", To: "Done", ToID: "2"},
			{ID: "12", Name: "Done", To: "Archived", ToID: "3"},
			{ID: "13", Name: "Refresh", To: "Open", ToID: "1"},
		},
		user:          domain.User{Name: "worker", Key: "user-1"},
		comments:      []domain.Comment{{ID: "10", Body: "prior", AuthorName: "worker", AuthorKey: "user-1", Created: "earlier"}},
		commitOnWrite: true,
	}
}

func (s *jiraTransitionStoreStub) GetIssue(_ context.Context, _ string, _ []string) (*domain.Issue, error) {
	s.issueCalls++
	if s.postReadErr != nil && s.writeCalls > 0 {
		return nil, s.postReadErr
	}
	if s.issueFn != nil {
		return s.issueFn(s.issueCalls)
	}
	return cloneTransitionIssue(&s.issue), nil
}

func (s *jiraTransitionStoreStub) Transitions(context.Context, string) ([]domain.TransitionDef, error) {
	s.transitionCalls++
	if s.transitionFn != nil {
		return s.transitionFn(s.transitionCalls)
	}
	return append([]domain.TransitionDef(nil), s.transitions...), nil
}

func (s *jiraTransitionStoreStub) CurrentUser(context.Context) (*domain.User, error) {
	s.userCalls++
	if s.userFn != nil {
		return s.userFn(s.userCalls)
	}
	user := s.user
	return &user, nil
}

func (s *jiraTransitionStoreStub) ListComments(context.Context, string) ([]domain.Comment, error) {
	s.commentCalls++
	if s.commentsFn != nil {
		return s.commentsFn(s.commentCalls)
	}
	return append([]domain.Comment(nil), s.comments...), nil
}

func (s *jiraTransitionStoreStub) TransitionByID(ctx context.Context, key string, request domain.JiraTransitionRequest) error {
	s.writeCalls++
	s.singleAttempt = domain.SingleAttempt(ctx)
	s.writtenKey = key
	s.written = request
	if s.commitOnWrite {
		for _, transition := range s.transitions {
			if transition.ID == request.ID {
				s.issue.StatusID = transition.ToID
				s.issue.Status = transition.To
				s.issue.Fields["status"] = map[string]any{"id": transition.ToID, "name": transition.To}
			}
		}
		for key, value := range request.Fields {
			s.issue.Fields[key] = value
		}
		s.issue.Fields["updated"] = "2026-07-28T00:01:00.000+0000"
		if request.Comment != nil {
			s.comments = append(s.comments, domain.Comment{ID: "11", Body: string(request.Comment), AuthorName: s.user.Name, AuthorKey: s.user.Key, Created: "now"})
		}
	}
	if s.postMutation != nil {
		s.postMutation(s)
	}
	return s.writeErr
}

func cloneTransitionIssue(issue *domain.Issue) *domain.Issue {
	copy := *issue
	copy.Fields = make(map[string]any, len(issue.Fields))
	for key, value := range issue.Fields {
		copy.Fields[key] = value
	}
	return &copy
}

func transitionHash(t *testing.T, opts JiraTransitionGuardedOpts) string {
	t.Helper()
	store := transitionFixture()
	result, err := (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", opts)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	return result.ProposalHash
}

func transitionApplyOpts(t *testing.T, opts JiraTransitionGuardedOpts) JiraTransitionGuardedOpts {
	t.Helper()
	opts.ExpectedProposalHash = transitionHash(t, opts)
	opts.Apply = true
	return opts
}

func TestTransitionGuardedPreviewIsDeterministicAndNamePrecedesTarget(t *testing.T) {
	opts := JiraTransitionGuardedOpts{To: "done", Fields: []JiraTransitionFieldInput{{Field: "b", Value: `{"id":"2"}`}, {Field: "a", Value: "new"}}}
	store := transitionFixture()
	result, err := (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "would_apply" || result.Mode != "dry-run" || result.Transition.ID != "12" || result.Transition.To != "Archived" {
		t.Fatalf("result=%+v", result)
	}
	if got := []string{result.Fields[0].Field, result.Fields[1].Field}; !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("field order=%v", got)
	}
	if store.writeCalls != 0 || len(result.ProposalHash) != 64 {
		t.Fatalf("writes=%d hash=%q", store.writeCalls, result.ProposalHash)
	}
	reversed := JiraTransitionGuardedOpts{To: "done", Fields: []JiraTransitionFieldInput{{Field: "a", Value: "new"}, {Field: "b", Value: `{"id":"2"}`}}}
	if got := transitionHash(t, reversed); got != result.ProposalHash {
		t.Fatalf("input order changed hash: %s != %s", got, result.ProposalHash)
	}
}

func TestTransitionGuardedRejectsDuplicateFieldsAndAmbiguousTransitions(t *testing.T) {
	_, err := (&JiraService{tr: transitionFixture()}).TransitionGuarded(context.Background(), "PROJ-1", JiraTransitionGuardedOpts{
		To: "Done", Fields: []JiraTransitionFieldInput{{Field: "a", Value: "1"}, {Field: "a", Value: "2"}},
	})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("duplicate fields err=%v", err)
	}
	store := transitionFixture()
	store.transitions = append(store.transitions, domain.TransitionDef{ID: "14", Name: "finish", To: "Done", ToID: "2"})
	_, err = (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", JiraTransitionGuardedOpts{To: "Finish"})
	if !errors.Is(err, domain.ErrCheckFailed) || store.writeCalls != 0 {
		t.Fatalf("ambiguous err=%v writes=%d", err, store.writeCalls)
	}
	store = transitionFixture()
	store.transitions[1].ID = store.transitions[0].ID
	_, err = (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", JiraTransitionGuardedOpts{To: "Finish"})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("duplicate transition identity err=%v", err)
	}
}

func TestTransitionGuardedFieldCoercionKeepsScalarsAndMalformedStructuredTextExact(t *testing.T) {
	for _, value := range []string{"123", "true", "null", `{"id":"2"} trailing`} {
		if got := coerceJiraTransitionField(value); got != value {
			t.Fatalf("value %q was unexpectedly retyped as %#v", value, got)
		}
	}
	if got := coerceJiraTransitionField(`{"id":"2"}`); reflect.ValueOf(got).Kind() != reflect.Map {
		t.Fatalf("valid object was not typed: %#v", got)
	}
}

func TestParseJiraUpdatedTimeAcceptsOnlyPreciseJiraDatetimes(t *testing.T) {
	for _, value := range []string{
		"2026-07-28T00:00:00.000+0000",
		"2026-07-28T00:00:00+0000",
		"2026-07-28T00:00:00.123456789Z",
		"2026-07-28T02:00:00+02:00",
	} {
		if _, err := parseJiraUpdatedTime(value); err != nil {
			t.Fatalf("valid Jira datetime %q: %v", value, err)
		}
	}
	for _, value := range []string{"", "2026-07-28", "2026-07-28 00:00", "now", " 2026-07-28T00:00:00Z"} {
		if _, err := parseJiraUpdatedTime(value); err == nil {
			t.Fatalf("unsupported updated marker %q was accepted", value)
		}
	}
}

func TestTransitionGuardedHashMismatchAndEveryPrewriteDriftMakeNoWrite(t *testing.T) {
	base := JiraTransitionGuardedOpts{To: "Finish", Fields: []JiraTransitionFieldInput{{Field: "a", Value: "new"}}, Comment: []byte("reviewed")}
	t.Run("hash mismatch", func(t *testing.T) {
		store := transitionFixture()
		base.Apply = true
		base.ExpectedProposalHash = strings.Repeat("0", 64)
		result, err := (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", base)
		if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "conflict" || store.writeCalls != 0 {
			t.Fatalf("result=%+v err=%v writes=%d", result, err, store.writeCalls)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*jiraTransitionStoreStub)
	}{
		{"updated", func(s *jiraTransitionStoreStub) {
			s.issueFn = func(call int) (*domain.Issue, error) {
				issue := cloneTransitionIssue(&s.issue)
				if call == 2 {
					issue.Fields["updated"] = "changed"
				}
				return issue, nil
			}
		}},
		{"field", func(s *jiraTransitionStoreStub) {
			s.issueFn = func(call int) (*domain.Issue, error) {
				issue := cloneTransitionIssue(&s.issue)
				if call == 2 {
					issue.Fields["a"] = "changed"
				}
				return issue, nil
			}
		}},
		{"transition", func(s *jiraTransitionStoreStub) {
			s.transitionFn = func(call int) ([]domain.TransitionDef, error) {
				out := append([]domain.TransitionDef(nil), s.transitions...)
				if call == 2 {
					out[0].ID = "99"
				}
				return out, nil
			}
		}},
		{"actor", func(s *jiraTransitionStoreStub) {
			s.userFn = func(call int) (*domain.User, error) {
				user := s.user
				if call == 2 {
					user.Key = "changed"
				}
				return &user, nil
			}
		}},
		{"comments", func(s *jiraTransitionStoreStub) {
			s.commentsFn = func(call int) ([]domain.Comment, error) {
				out := append([]domain.Comment(nil), s.comments...)
				if call == 2 {
					out = append(out, domain.Comment{ID: "99", Body: "concurrent", AuthorName: "other"})
				}
				return out, nil
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := transitionFixture()
			tc.mutate(store)
			result, err := (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", transitionApplyOpts(t, JiraTransitionGuardedOpts{To: "Finish", Fields: base.Fields, Comment: base.Comment}))
			if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "conflict" || store.writeCalls != 0 {
				t.Fatalf("result=%+v err=%v writes=%d", result, err, store.writeCalls)
			}
		})
	}
}

func TestTransitionGuardedTargetStatusIDDriftChangesHashAndBlocksBeforeWrite(t *testing.T) {
	previewStore := transitionFixture()
	preview, err := (&JiraService{tr: previewStore}).TransitionGuarded(context.Background(), "PROJ-1", JiraTransitionGuardedOpts{To: "Finish"})
	if err != nil {
		t.Fatal(err)
	}
	changedStore := transitionFixture()
	changedStore.transitions[0].ToID = "22"
	changed, err := (&JiraService{tr: changedStore}).TransitionGuarded(context.Background(), "PROJ-1", JiraTransitionGuardedOpts{To: "Finish"})
	if err != nil {
		t.Fatal(err)
	}
	if changed.ProposalHash == preview.ProposalHash {
		t.Fatal("target status id was not bound into the proposal hash")
	}

	applyStore := transitionFixture()
	applyStore.transitionFn = func(call int) ([]domain.TransitionDef, error) {
		transitions := append([]domain.TransitionDef(nil), applyStore.transitions...)
		if call == 2 {
			transitions[0].ToID = "22"
		}
		return transitions, nil
	}
	result, err := (&JiraService{tr: applyStore}).TransitionGuarded(context.Background(), "PROJ-1", JiraTransitionGuardedOpts{
		To: "Finish", Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "conflict" || applyStore.writeCalls != 0 {
		t.Fatalf("result=%+v err=%v writes=%d", result, err, applyStore.writeCalls)
	}
}

func TestTransitionGuardedRejectsMalformedUpdatedEvidenceBeforeAndAfterWrite(t *testing.T) {
	t.Run("initial baseline", func(t *testing.T) {
		store := transitionFixture()
		store.issue.Fields["updated"] = "not-a-jira-datetime"
		result, err := (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", JiraTransitionGuardedOpts{To: "Finish"})
		if result != nil || !errors.Is(err, domain.ErrCheckFailed) || store.writeCalls != 0 {
			t.Fatalf("result=%+v err=%v writes=%d", result, err, store.writeCalls)
		}
	})

	t.Run("immediate prewrite", func(t *testing.T) {
		store := transitionFixture()
		store.issueFn = func(call int) (*domain.Issue, error) {
			issue := cloneTransitionIssue(&store.issue)
			if call == 2 {
				issue.Fields["updated"] = "not-a-jira-datetime"
			}
			return issue, nil
		}
		result, err := (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", transitionApplyOpts(t, JiraTransitionGuardedOpts{To: "Finish"}))
		if result.Status != "conflict" || !errors.Is(err, domain.ErrCheckFailed) || store.writeCalls != 0 {
			t.Fatalf("result=%+v err=%v writes=%d", result, err, store.writeCalls)
		}
	})

	t.Run("readback", func(t *testing.T) {
		store := transitionFixture()
		store.postMutation = func(s *jiraTransitionStoreStub) { s.issue.Fields["updated"] = "not-a-jira-datetime" }
		result, err := (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", transitionApplyOpts(t, JiraTransitionGuardedOpts{To: "Finish"}))
		var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
		if result.Status != "unverifiable" || result.Complete || !errors.Is(err, domain.ErrCheckFailed) ||
			!errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() || store.writeCalls != 1 {
			t.Fatalf("result=%+v err=%v writes=%d", result, err, store.writeCalls)
		}
	})
}

func TestTransitionGuardedOlderReadbackUpdatedCannotProveApplied(t *testing.T) {
	store := transitionFixture()
	store.postMutation = func(s *jiraTransitionStoreStub) {
		s.issue.Fields["updated"] = "2026-07-27T23:59:00.000+0000"
	}
	result, err := (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", transitionApplyOpts(t, JiraTransitionGuardedOpts{To: "Finish"}))
	var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
	if result.Status != "conflict" || !result.Complete || !errors.Is(err, domain.ErrCheckFailed) ||
		!errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() || store.writeCalls != 1 {
		t.Fatalf("result=%+v err=%v writes=%d", result, err, store.writeCalls)
	}
}

func TestTransitionGuardedSanitizesPrewriteFailureAndPreservesCause(t *testing.T) {
	leaky := errors.Join(domain.ErrForbidden, errors.New("private backend prose"))
	store := transitionFixture()
	store.issueFn = func(call int) (*domain.Issue, error) {
		if call == 2 {
			return nil, leaky
		}
		return cloneTransitionIssue(&store.issue), nil
	}
	result, err := (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", transitionApplyOpts(t, JiraTransitionGuardedOpts{To: "Finish"}))
	if result.Status != "conflict" || store.writeCalls != 0 || strings.Contains(err.Error(), "private") {
		t.Fatalf("result=%+v err=%v writes=%d", result, err, store.writeCalls)
	}
	if !errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("typed causes lost: %v", err)
	}
}

func TestTransitionGuardedApplySendsExactSingleAttemptRequest(t *testing.T) {
	store := transitionFixture()
	opts := transitionApplyOpts(t, JiraTransitionGuardedOpts{
		To: "Finish", Comment: []byte("reviewed\nwiki"),
		Fields: []JiraTransitionFieldInput{{Field: "a", Value: "001"}, {Field: "b", Value: `{"id":"2"}`}},
	})
	result, err := (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", opts)
	if err != nil || result.Status != "applied" || store.writeCalls != 1 || !store.singleAttempt || store.writtenKey != "PROJ-1" {
		t.Fatalf("result=%+v err=%v calls=%d single=%v request=%+v", result, err, store.writeCalls, store.singleAttempt, store.written)
	}
	if store.written.ID != "11" || store.written.Fields["a"] != "001" || string(store.written.Comment) != "reviewed\nwiki" {
		t.Fatalf("request=%+v", store.written)
	}
	if got := store.written.Fields["b"].(map[string]any)["id"]; got != "2" {
		t.Fatalf("typed field=%#v", store.written.Fields["b"])
	}
	if result.Comment == nil || result.Comment.Created == nil || result.Comment.Created.ID != "11" {
		t.Fatalf("comment result=%+v", result.Comment)
	}
}

func TestTransitionGuardedReadbackDoesNotRequireAppliedTransitionOrActorLookup(t *testing.T) {
	store := transitionFixture()
	store.postMutation = func(s *jiraTransitionStoreStub) {
		// Jira commonly removes the applied transition from the new status.
		s.transitions = []domain.TransitionDef{{ID: "90", Name: "Reopen", To: "Open", ToID: "1"}}
	}
	result, err := (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", transitionApplyOpts(t, JiraTransitionGuardedOpts{
		To: "Finish", Comment: []byte("reviewed"),
	}))
	if err != nil || result.Status != "applied" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if store.transitionCalls != 2 || store.userCalls != 2 || store.issueCalls != 3 || store.commentCalls != 3 {
		t.Fatalf("calls issue/transitions/user/comments=%d/%d/%d/%d, want 3/2/2/3",
			store.issueCalls, store.transitionCalls, store.userCalls, store.commentCalls)
	}
}

func TestTransitionGuardedAppliedResultPreservesProposalBoundBaseline(t *testing.T) {
	opts := JiraTransitionGuardedOpts{
		To: "Finish", Comment: []byte("reviewed"),
		Fields: []JiraTransitionFieldInput{{Field: "a", Value: "new"}},
	}
	preview, err := (&JiraService{tr: transitionFixture()}).TransitionGuarded(context.Background(), "PROJ-1", opts)
	if err != nil {
		t.Fatal(err)
	}
	store := transitionFixture()
	opts.Apply = true
	opts.ExpectedProposalHash = preview.ProposalHash
	applied, err := (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", opts)
	if err != nil || applied.Status != "applied" || !applied.Reconciled {
		t.Fatalf("result=%+v err=%v", applied, err)
	}
	if applied.ProposalHash != preview.ProposalHash || applied.CurrentStatus != preview.CurrentStatus ||
		!reflect.DeepEqual(applied.Fields, preview.Fields) {
		t.Fatalf("proposal baseline changed after reconciliation:\npreview=%+v\napplied=%+v", preview, applied)
	}
	if applied.Comment == nil || preview.Comment == nil ||
		applied.Comment.Body != preview.Comment.Body ||
		applied.Comment.BodySHA256 != preview.Comment.BodySHA256 ||
		applied.Comment.BodyBytes != preview.Comment.BodyBytes ||
		applied.Comment.Actor != preview.Comment.Actor ||
		applied.Comment.CurrentCount != preview.Comment.CurrentCount ||
		applied.Comment.BaselineSHA256 != preview.Comment.BaselineSHA256 {
		t.Fatalf("comment proposal baseline changed:\npreview=%+v\napplied=%+v", preview.Comment, applied.Comment)
	}
	if applied.Comment.Created == nil || applied.Comment.Created.ID != "11" {
		t.Fatalf("outcome attribution missing: %+v", applied.Comment)
	}
}

func TestTransitionGuardedWriteOutcomeMatrix(t *testing.T) {
	for _, tc := range []struct {
		name          string
		writeErr      error
		commit        bool
		postReadErr   error
		postMutation  func(*jiraTransitionStoreStub)
		wantStatus    string
		wantErr       bool
		wantAmbiguous bool
		wantComplete  bool
	}{
		{name: "2xx exact", commit: true, wantStatus: "applied", wantComplete: true},
		{name: "timeout exact", writeErr: errors.New("timeout"), commit: true, wantStatus: "applied", wantComplete: true},
		{name: "408 exact", writeErr: transitionStatusError(408), commit: true, wantStatus: "applied", wantComplete: true},
		{name: "425 exact", writeErr: transitionStatusError(425), commit: true, wantStatus: "applied", wantComplete: true},
		{name: "429 exact", writeErr: transitionStatusError(429), commit: true, wantStatus: "applied", wantComplete: true},
		{name: "5xx exact", writeErr: transitionStatusError(503), commit: true, wantStatus: "applied", wantComplete: true},
		{name: "redirect exact", writeErr: transitionStatusError(307), commit: true, wantStatus: "applied", wantComplete: true},
		{name: "definitive 400", writeErr: transitionStatusError(400), wantStatus: "not_applied", wantErr: true, wantComplete: true},
		{name: "definitive 409", writeErr: transitionStatusError(409), wantStatus: "not_applied", wantErr: true, wantComplete: true},
		{name: "timeout unchanged", writeErr: errors.New("timeout"), wantStatus: "unverifiable", wantErr: true, wantAmbiguous: true, wantComplete: true},
		{name: "2xx divergent", commit: true, postMutation: func(s *jiraTransitionStoreStub) {
			s.issue.StatusID = "9"
			s.issue.Status = "Other"
			s.issue.Fields["status"] = map[string]any{"id": "9", "name": "Other"}
		}, wantStatus: "conflict", wantErr: true, wantAmbiguous: true, wantComplete: true},
		{name: "2xx partial fields", commit: true, postMutation: func(s *jiraTransitionStoreStub) {
			s.issue.Fields["a"] = "other"
		}, wantStatus: "conflict", wantErr: true, wantAmbiguous: true, wantComplete: true},
		{name: "readback failed", writeErr: errors.New("timeout"), postReadErr: errors.New("read unavailable"), wantStatus: "unverifiable", wantErr: true, wantAmbiguous: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := transitionFixture()
			store.writeErr, store.commitOnWrite, store.postReadErr, store.postMutation = tc.writeErr, tc.commit, tc.postReadErr, tc.postMutation
			result, err := (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", transitionApplyOpts(t, JiraTransitionGuardedOpts{To: "Finish", Fields: []JiraTransitionFieldInput{{Field: "a", Value: "new"}}}))
			if result.Status != tc.wantStatus || (err != nil) != tc.wantErr || result.Complete != tc.wantComplete || store.writeCalls != 1 {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, store.writeCalls)
			}
			var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
			gotAmbiguous := errors.As(err, &ambiguous) && ambiguous.DiagnosticAmbiguousWrite()
			if gotAmbiguous != tc.wantAmbiguous {
				t.Fatalf("ambiguous=%v want=%v err=%v", gotAmbiguous, tc.wantAmbiguous, err)
			}
			if tc.wantAmbiguous && !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("unsafe postwrite error lost ErrCheckFailed: %v", err)
			}
		})
	}
}

func TestTransitionGuardedCommentReconciliationAllowsConcurrentDistinctAndRejectsDuplicates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutation   func(*jiraTransitionStoreStub)
		wantStatus string
		wantErr    bool
	}{
		{name: "concurrent distinct", mutation: func(s *jiraTransitionStoreStub) {
			s.comments = append(s.comments, domain.Comment{ID: "12", Body: "other", AuthorName: "other"})
		}, wantStatus: "applied"},
		{name: "duplicate attribution", mutation: func(s *jiraTransitionStoreStub) {
			s.comments = append(s.comments, domain.Comment{ID: "12", Body: "reviewed", AuthorName: s.user.Name, AuthorKey: s.user.Key})
		}, wantStatus: "conflict", wantErr: true},
		{name: "baseline changed", mutation: func(s *jiraTransitionStoreStub) { s.comments[0].Body = "edited" }, wantStatus: "conflict", wantErr: true},
		{name: "baseline missing", mutation: func(s *jiraTransitionStoreStub) { s.comments = s.comments[1:] }, wantStatus: "conflict", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := transitionFixture()
			store.postMutation = tc.mutation
			result, err := (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", transitionApplyOpts(t, JiraTransitionGuardedOpts{To: "Finish", Comment: []byte("reviewed")}))
			if result.Status != tc.wantStatus || (err != nil) != tc.wantErr {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestTransitionGuardedExecutesSelfTransition(t *testing.T) {
	store := transitionFixture()
	result, err := (&JiraService{tr: store}).TransitionGuarded(context.Background(), "PROJ-1", transitionApplyOpts(t, JiraTransitionGuardedOpts{To: "Refresh"}))
	if err != nil || result.Status != "applied" || store.writeCalls != 1 || result.Transition.ToID != result.CurrentStatus.ID {
		t.Fatalf("result=%+v err=%v writes=%d", result, err, store.writeCalls)
	}
}
