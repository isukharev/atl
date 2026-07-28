package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type jiraCommentStoreStub struct {
	domain.Tracker
	user          domain.User
	comments      []domain.Comment
	listFn        func(int) ([]domain.Comment, error)
	addResult     *domain.Comment
	addErr        error
	commit        []domain.Comment
	currentCalls  int
	listCalls     int
	addCalls      int
	singleAttempt bool
	postedBody    []byte
}

func (s *jiraCommentStoreStub) CurrentUser(context.Context) (*domain.User, error) {
	s.currentCalls++
	copy := s.user
	return &copy, nil
}

func (s *jiraCommentStoreStub) ListComments(context.Context, string) ([]domain.Comment, error) {
	s.listCalls++
	if s.listFn != nil {
		comments, err := s.listFn(s.listCalls)
		return append([]domain.Comment(nil), comments...), err
	}
	return append([]domain.Comment(nil), s.comments...), nil
}

func (s *jiraCommentStoreStub) AddComment(ctx context.Context, _ string, body []byte) (*domain.Comment, error) {
	s.addCalls++
	s.singleAttempt = domain.SingleAttempt(ctx)
	s.postedBody = append([]byte(nil), body...)
	s.comments = append(s.comments, s.commit...)
	return s.addResult, s.addErr
}

func commentFixture() *jiraCommentStoreStub {
	return &jiraCommentStoreStub{
		user: domain.User{Name: "alice", Key: "user-1", DisplayName: "Alice", Email: "private@example.test"},
		comments: []domain.Comment{{
			ID: "10", Author: "Bob", AuthorName: "bob", AuthorKey: "user-2",
			Created: "2026-07-01T10:00:00.000+0000", Body: "existing",
		}},
	}
}

func previewComment(t *testing.T, store *jiraCommentStoreStub, body string) *JiraCommentAddResult {
	t.Helper()
	result, err := (&JiraService{tr: store}).AddCommentGuarded(context.Background(), "PROJ-1", JiraCommentAddOpts{Body: []byte(body)})
	if err != nil || result.Status != "would_apply" || result.ProposalHash == "" {
		t.Fatalf("preview=%+v err=%v", result, err)
	}
	return result
}

func TestJiraCommentPreviewBindsExactBodyActorAndSortedBaseline(t *testing.T) {
	store := commentFixture()
	store.comments = append(store.comments, domain.Comment{ID: "2", AuthorName: "alice", AuthorKey: "user-1", Body: "same body"})
	preview := previewComment(t, store, " same body\n")
	if preview.Body != " same body\n" || preview.BodyBytes != len(" same body\n") || preview.BodySHA256 == "" ||
		preview.Actor.Name != "alice" || preview.Actor.Key != "user-1" || preview.CurrentCount != 2 || !preview.Complete || store.addCalls != 0 {
		t.Fatalf("preview=%+v addCalls=%d", preview, store.addCalls)
	}
	store.comments[0], store.comments[1] = store.comments[1], store.comments[0]
	reordered := previewComment(t, store, " same body\n")
	if reordered.ProposalHash != preview.ProposalHash || reordered.BaselineSHA256 != preview.BaselineSHA256 {
		t.Fatalf("reordered=%+v preview=%+v", reordered, preview)
	}
	if strings.Contains(JiraCommentAddText(preview), preview.Body) {
		t.Fatalf("text leaked body: %q", JiraCommentAddText(preview))
	}
}

func TestJiraCommentApplySuccessUsesOnePOSTAndCompleteReadback(t *testing.T) {
	store := commentFixture()
	preview := previewComment(t, store, "reviewed body")
	store.commit = []domain.Comment{
		{ID: "20", Author: "Alice", AuthorName: "alice", AuthorKey: "user-1", Created: "2026-07-02", Body: "reviewed body"},
		{ID: "21", AuthorName: "carol", AuthorKey: "user-3", Created: "2026-07-02", Body: "concurrent distinct"},
	}
	store.addResult = &domain.Comment{ID: "20"}
	result, err := (&JiraService{tr: store}).AddCommentGuarded(context.Background(), "PROJ-1", JiraCommentAddOpts{
		Body: []byte("reviewed body"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	if err != nil || result.Status != "applied" || !result.Reconciled || result.Created == nil || result.Created.ID != "20" ||
		store.addCalls != 1 || store.listCalls != 4 || store.currentCalls != 2 || !store.singleAttempt || string(store.postedBody) != "reviewed body" {
		t.Fatalf("result=%+v err=%v calls=current:%d list:%d add:%d single=%t body=%q",
			result, err, store.currentCalls, store.listCalls, store.addCalls, store.singleAttempt, store.postedBody)
	}
}

type commentStatusError int

func (e commentStatusError) Error() string   { return "request failed" }
func (e commentStatusError) HTTPStatus() int { return int(e) }

func TestJiraCommentWriteOutcomesNeverReplay(t *testing.T) {
	tests := []struct {
		name          string
		writeErr      error
		commit        bool
		wantStatus    string
		wantLists     int
		wantErr       bool
		wantAmbiguous bool
	}{
		{name: "definitive 400", writeErr: commentStatusError(400), wantStatus: "not_applied", wantLists: 3, wantErr: true},
		{name: "transport timeout committed", writeErr: context.DeadlineExceeded, commit: true, wantStatus: "applied", wantLists: 4},
		{name: "429 committed", writeErr: commentStatusError(429), commit: true, wantStatus: "applied", wantLists: 4},
		{name: "500 without evidence", writeErr: commentStatusError(500), wantStatus: "unverifiable", wantLists: 4, wantErr: true, wantAmbiguous: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := commentFixture()
			preview := previewComment(t, store, "reviewed")
			store.addErr = tt.writeErr
			if tt.commit {
				store.commit = []domain.Comment{{ID: "20", AuthorName: "alice", AuthorKey: "user-1", Body: "reviewed"}}
			}
			result, err := (&JiraService{tr: store}).AddCommentGuarded(context.Background(), "PROJ-1", JiraCommentAddOpts{
				Body: []byte("reviewed"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
			})
			if result.Status != tt.wantStatus || (err != nil) != tt.wantErr || store.addCalls != 1 || store.listCalls != tt.wantLists || !store.singleAttempt {
				t.Fatalf("result=%+v err=%v add=%d lists=%d single=%t", result, err, store.addCalls, store.listCalls, store.singleAttempt)
			}
			var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
			gotAmbiguous := errors.As(err, &ambiguous) && ambiguous.DiagnosticAmbiguousWrite()
			if gotAmbiguous != tt.wantAmbiguous {
				t.Fatalf("ambiguous=%t want=%t err=%v", gotAmbiguous, tt.wantAmbiguous, err)
			}
		})
	}
}

func TestJiraCommentReadbackFailureAndDuplicateBodyFailClosed(t *testing.T) {
	t.Run("readback failure", func(t *testing.T) {
		store := commentFixture()
		preview := previewComment(t, store, "reviewed")
		store.addErr = context.DeadlineExceeded
		store.listFn = func(call int) ([]domain.Comment, error) {
			if call == 4 {
				return nil, errors.New("readback unavailable")
			}
			return store.comments, nil
		}
		result, err := (&JiraService{tr: store}).AddCommentGuarded(context.Background(), "PROJ-1", JiraCommentAddOpts{
			Body: []byte("reviewed"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
		})
		assertCommentAmbiguous(t, result, err, "unverifiable")
		if result.Complete || store.addCalls != 1 || store.listCalls != 4 {
			t.Fatalf("result=%+v add=%d lists=%d", result, store.addCalls, store.listCalls)
		}
	})

	t.Run("duplicate new body", func(t *testing.T) {
		store := commentFixture()
		preview := previewComment(t, store, "same")
		store.addErr = context.DeadlineExceeded
		store.commit = []domain.Comment{
			{ID: "20", AuthorName: "alice", AuthorKey: "user-1", Body: "same"},
			{ID: "21", AuthorName: "alice", AuthorKey: "user-1", Body: "same"},
		}
		result, err := (&JiraService{tr: store}).AddCommentGuarded(context.Background(), "PROJ-1", JiraCommentAddOpts{
			Body: []byte("same"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
		})
		assertCommentAmbiguous(t, result, err, "conflict")
		if store.addCalls != 1 {
			t.Fatalf("POST replayed: %d", store.addCalls)
		}
	})
}

func TestJiraCommentRejectsStaleBodyMalformedBaselineAndPrePOSTDrift(t *testing.T) {
	t.Run("stale hash", func(t *testing.T) {
		store := commentFixture()
		result, err := (&JiraService{tr: store}).AddCommentGuarded(context.Background(), "PROJ-1", JiraCommentAddOpts{
			Body: []byte("one"), Apply: true, ExpectedProposalHash: "stale",
		})
		if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "conflict" || store.addCalls != 0 {
			t.Fatalf("result=%+v err=%v add=%d", result, err, store.addCalls)
		}
	})

	t.Run("changed body", func(t *testing.T) {
		store := commentFixture()
		preview := previewComment(t, store, "one")
		result, err := (&JiraService{tr: store}).AddCommentGuarded(context.Background(), "PROJ-1", JiraCommentAddOpts{
			Body: []byte("two"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
		})
		if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "conflict" || store.addCalls != 0 {
			t.Fatalf("result=%+v err=%v add=%d", result, err, store.addCalls)
		}
	})

	for name, comments := range map[string][]domain.Comment{
		"missing id":   {{ID: ""}},
		"spaced id":    {{ID: " 10 "}},
		"duplicate id": {{ID: "10"}, {ID: "10"}},
	} {
		t.Run(name, func(t *testing.T) {
			store := commentFixture()
			store.comments = comments
			if _, err := (&JiraService{tr: store}).AddCommentGuarded(context.Background(), "PROJ-1", JiraCommentAddOpts{Body: []byte("x")}); !errors.Is(err, domain.ErrCheckFailed) || store.addCalls != 0 {
				t.Fatalf("err=%v add=%d", err, store.addCalls)
			}
		})
	}

	t.Run("pre-POST drift", func(t *testing.T) {
		store := commentFixture()
		preview := previewComment(t, store, "reviewed")
		store.listFn = func(call int) ([]domain.Comment, error) {
			comments := append([]domain.Comment(nil), store.comments...)
			if call == 3 {
				comments = append(comments, domain.Comment{ID: "11", AuthorName: "carol", Body: "concurrent"})
			}
			return comments, nil
		}
		result, err := (&JiraService{tr: store}).AddCommentGuarded(context.Background(), "PROJ-1", JiraCommentAddOpts{
			Body: []byte("reviewed"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
		})
		if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "conflict" || store.addCalls != 0 || store.listCalls != 3 {
			t.Fatalf("result=%+v err=%v add=%d lists=%d", result, err, store.addCalls, store.listCalls)
		}
	})

	t.Run("pre-POST failure preserves typed cause without backend prose", func(t *testing.T) {
		store := commentFixture()
		preview := previewComment(t, store, "reviewed")
		store.listFn = func(call int) ([]domain.Comment, error) {
			if call == 3 {
				return nil, &commentLeakyForbiddenError{}
			}
			return store.comments, nil
		}
		result, err := (&JiraService{tr: store}).AddCommentGuarded(context.Background(), "PROJ-1", JiraCommentAddOpts{
			Body: []byte("reviewed"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
		})
		var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
		isAmbiguous := errors.As(err, &ambiguous) && ambiguous.DiagnosticAmbiguousWrite()
		if result == nil || result.Status != "conflict" || result.Complete || store.addCalls != 0 ||
			!errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, domain.ErrForbidden) ||
			strings.Contains(err.Error(), "private backend detail") || isAmbiguous {
			t.Fatalf("result=%+v err=%v add=%d ambiguous=%T", result, err, store.addCalls, ambiguous)
		}
	})
}

type commentLeakyForbiddenError struct{}

func (*commentLeakyForbiddenError) Error() string { return "private backend detail" }
func (*commentLeakyForbiddenError) Unwrap() error { return domain.ErrForbidden }

func TestJiraCommentReconciliationRejectsChangedOrMissingBaselineMembers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]domain.Comment) []domain.Comment
	}{
		{name: "missing", mutate: func(comments []domain.Comment) []domain.Comment { return comments[1:] }},
		{name: "changed", mutate: func(comments []domain.Comment) []domain.Comment {
			comments[0].Body = "edited concurrently"
			return comments
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := commentFixture()
			preview := previewComment(t, store, "reviewed")
			store.addErr = context.DeadlineExceeded
			store.commit = []domain.Comment{{ID: "20", AuthorName: "alice", AuthorKey: "user-1", Body: "reviewed"}}
			store.listFn = func(call int) ([]domain.Comment, error) {
				comments := append([]domain.Comment(nil), store.comments...)
				if call == 4 {
					comments = tc.mutate(comments)
				}
				return comments, nil
			}
			result, err := (&JiraService{tr: store}).AddCommentGuarded(context.Background(), "PROJ-1", JiraCommentAddOpts{
				Body: []byte("reviewed"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
			})
			assertCommentAmbiguous(t, result, err, "conflict")
		})
	}
}

func TestValidateJiraCommentBodyIsBoundedUTF8NonemptyAndByteStable(t *testing.T) {
	body := []byte("  native *wiki*\n")
	got, err := ValidateJiraCommentBody(body)
	if err != nil || string(got) != string(body) {
		t.Fatalf("body=%q err=%v", got, err)
	}
	for _, invalid := range [][]byte{nil, []byte(" \n\t"), {0xff}, make([]byte, JiraCommentBodyMaxBytes+1)} {
		if _, err := ValidateJiraCommentBody(invalid); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("input length=%d err=%v", len(invalid), err)
		}
	}
}

func assertCommentAmbiguous(t *testing.T, result *JiraCommentAddResult, err error, status string) {
	t.Helper()
	var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
	if result == nil || result.Status != status || !errors.Is(err, domain.ErrCheckFailed) ||
		!errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() {
		t.Fatalf("result=%+v err=%v ambiguous=%T", result, err, ambiguous)
	}
}
