package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type jiraWatcherStoreStub struct {
	domain.Tracker
	state                  domain.IssueWatcherList
	currentUser            *domain.User
	currentErr             error
	writeErr               error
	verificationErr        error
	verificationNil        bool
	verificationIncomplete bool
	skipMutation           bool
	noCommitOnError        bool
	listCalls              int
	addCalls               int
	removeCalls            int
}

func (s *jiraWatcherStoreStub) ListIssueWatchers(context.Context, string) (*domain.IssueWatcherList, error) {
	s.listCalls++
	if s.listCalls > 1 {
		if s.verificationErr != nil {
			return nil, s.verificationErr
		}
		if s.verificationNil {
			return nil, nil
		}
		if s.verificationIncomplete {
			copy := s.state
			copy.Watchers = append([]domain.IssueWatcher(nil), s.state.Watchers...)
			copy.Complete = false
			return &copy, nil
		}
	}
	copy := s.state
	copy.Watchers = append([]domain.IssueWatcher(nil), s.state.Watchers...)
	return &copy, nil
}

func (s *jiraWatcherStoreStub) AddIssueWatcher(_ context.Context, _, username string) error {
	s.addCalls++
	if !s.skipMutation && (!s.noCommitOnError || s.writeErr == nil) && !watcherPresent(s.state.Watchers, username) {
		s.state.Watchers = append(s.state.Watchers, domain.IssueWatcher{Name: username, DisplayName: username, Active: true})
		s.state.WatchCount++
	}
	return s.writeErr
}

func (s *jiraWatcherStoreStub) RemoveIssueWatcher(_ context.Context, _, username string) error {
	s.removeCalls++
	if !s.skipMutation && (!s.noCommitOnError || s.writeErr == nil) {
		filtered := s.state.Watchers[:0]
		for _, watcher := range s.state.Watchers {
			if watcher.Name != username {
				filtered = append(filtered, watcher)
			}
		}
		s.state.Watchers = filtered
		s.state.WatchCount = len(filtered)
	}
	return s.writeErr
}

type jiraWatcherStatusError int

func (e jiraWatcherStatusError) Error() string   { return "rejected" }
func (e jiraWatcherStatusError) HTTPStatus() int { return int(e) }

func (s *jiraWatcherStoreStub) CurrentUser(context.Context) (*domain.User, error) {
	return s.currentUser, s.currentErr
}

func watcherPresent(watchers []domain.IssueWatcher, username string) bool {
	for _, watcher := range watchers {
		if watcher.Name == username {
			return true
		}
	}
	return false
}

func TestJiraWatchersListPreviewAndGuardedAdd(t *testing.T) {
	store := &jiraWatcherStoreStub{state: domain.IssueWatcherList{
		WatchCount: 1, Complete: true, Watchers: []domain.IssueWatcher{{Name: "alice", DisplayName: "Alice", Active: true}},
	}}
	service := &JiraService{tr: store}
	listed, err := service.ListWatchers(context.Background(), "PROJ-1")
	if err != nil || !listed.Complete || listed.WatchCount != 1 || listed.Watchers[0].Name != "alice" {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	preview, err := service.MutateWatcherGuarded(context.Background(), "PROJ-1", JiraWatcherMutationOpts{Operation: "add", Username: " bob "})
	if err != nil || preview.Status != "would_apply" || preview.Username != "bob" || preview.ProposalHash == "" || store.addCalls != 0 {
		t.Fatalf("preview=%+v calls=%d err=%v", preview, store.addCalls, err)
	}
	applied, err := service.MutateWatcherGuarded(context.Background(), "PROJ-1", JiraWatcherMutationOpts{
		Operation: "add", Username: "bob", ExpectedProposalHash: preview.ProposalHash, Apply: true,
	})
	if err != nil || applied.Status != "applied" || len(applied.Final) != 2 || store.addCalls != 1 {
		t.Fatalf("applied=%+v calls=%d err=%v", applied, store.addCalls, err)
	}
}

func TestJiraWatchersMeResolutionAndApplyGate(t *testing.T) {
	store := &jiraWatcherStoreStub{
		state:       domain.IssueWatcherList{WatchCount: 1, Complete: true, Watchers: []domain.IssueWatcher{{Name: "me"}}},
		currentUser: &domain.User{Name: "me", DisplayName: "Current"},
	}
	service := &JiraService{tr: store}
	result, err := service.MutateWatcherGuarded(context.Background(), "PROJ-1", JiraWatcherMutationOpts{
		Operation: "add", Me: true, ExpectedProposalHash: "stale", Apply: true,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || result.IdentitySource != "me" || store.addCalls != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, store.addCalls, err)
	}
	preview, err := service.MutateWatcherGuarded(context.Background(), "PROJ-1", JiraWatcherMutationOpts{Operation: "add", Me: true})
	if err != nil || preview.Status != "already_satisfied" || preview.Username != "me" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
}

func TestJiraWatchersReconcileAmbiguousWriteAndRefuseIncompleteState(t *testing.T) {
	store := &jiraWatcherStoreStub{state: domain.IssueWatcherList{Complete: true}, writeErr: errors.New("connection lost")}
	service := &JiraService{tr: store}
	preview, err := service.MutateWatcherGuarded(context.Background(), "PROJ-1", JiraWatcherMutationOpts{Operation: "add", Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.MutateWatcherGuarded(context.Background(), "PROJ-1", JiraWatcherMutationOpts{
		Operation: "add", Username: "alice", ExpectedProposalHash: preview.ProposalHash, Apply: true,
	})
	if err != nil || result.Status != "applied" || !result.Reconciled || store.addCalls != 1 {
		t.Fatalf("result=%+v calls=%d err=%v", result, store.addCalls, err)
	}

	incomplete := &jiraWatcherStoreStub{state: domain.IssueWatcherList{WatchCount: 2, Complete: false, Truncated: true, Watchers: []domain.IssueWatcher{{Name: "visible"}}}}
	_, err = (&JiraService{tr: incomplete}).MutateWatcherGuarded(context.Background(), "PROJ-1", JiraWatcherMutationOpts{Operation: "remove", Username: "visible"})
	if !errors.Is(err, domain.ErrCheckFailed) || incomplete.removeCalls != 0 {
		t.Fatalf("incomplete mutation err=%v calls=%d", err, incomplete.removeCalls)
	}
}

func TestJiraWatchersGuardedApplyOutcomeMatrix(t *testing.T) {
	tests := []struct {
		name           string
		store          *jiraWatcherStoreStub
		wantStatus     string
		wantErr        bool
		wantAmbiguous  bool
		wantReconciled bool
	}{
		{
			name:       "successful apply has exactly prewrite and readback lists",
			store:      &jiraWatcherStoreStub{state: domain.IssueWatcherList{Complete: true}},
			wantStatus: "applied",
		},
		{
			name:       "definitive rejection is failed after complete readback",
			store:      &jiraWatcherStoreStub{state: domain.IssueWatcherList{Complete: true}, writeErr: jiraWatcherStatusError(400), noCommitOnError: true},
			wantStatus: "failed", wantErr: true, wantReconciled: true,
		},
		{
			name:       "failed readback is ambiguous",
			store:      &jiraWatcherStoreStub{state: domain.IssueWatcherList{Complete: true}, verificationErr: errors.New("verification unavailable")},
			wantStatus: "unknown", wantErr: true, wantAmbiguous: true,
		},
		{
			name:       "nil readback is ambiguous",
			store:      &jiraWatcherStoreStub{state: domain.IssueWatcherList{Complete: true}, verificationNil: true},
			wantStatus: "unknown", wantErr: true, wantAmbiguous: true,
		},
		{
			name:       "incomplete readback is ambiguous",
			store:      &jiraWatcherStoreStub{state: domain.IssueWatcherList{Complete: true}, verificationIncomplete: true},
			wantStatus: "unknown", wantErr: true, wantAmbiguous: true,
		},
		{
			name:       "complete goal mismatch is ambiguous",
			store:      &jiraWatcherStoreStub{state: domain.IssueWatcherList{Complete: true}, skipMutation: true},
			wantStatus: "unknown", wantErr: true, wantAmbiguous: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &JiraService{tr: test.store}
			preview, err := service.MutateWatcherGuarded(context.Background(), "PROJ-1", JiraWatcherMutationOpts{
				Operation: "add", Username: "alice",
			})
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			test.store.listCalls = 0
			result, err := service.MutateWatcherGuarded(context.Background(), "PROJ-1", JiraWatcherMutationOpts{
				Operation: "add", Username: "alice", Apply: true, ExpectedProposalHash: preview.ProposalHash,
			})
			if result == nil || result.Status != test.wantStatus || (err != nil) != test.wantErr ||
				test.store.listCalls != 2 || test.store.addCalls != 1 || result.Reconciled != test.wantReconciled {
				t.Fatalf("result=%+v err=%v lists=%d adds=%d", result, err, test.store.listCalls, test.store.addCalls)
			}
			var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
			gotAmbiguous := errors.As(err, &ambiguous) && ambiguous.DiagnosticAmbiguousWrite()
			if gotAmbiguous != test.wantAmbiguous {
				t.Fatalf("ambiguous=%t want=%t err=%v", gotAmbiguous, test.wantAmbiguous, err)
			}
		})
	}
}

func TestNormalizeJiraWatcherUsernameRejectsUnsafeInput(t *testing.T) {
	for _, username := range []string{"", "bad\nname", string([]byte{0xff})} {
		if _, err := normalizeJiraWatcherUsername(username); !errors.Is(err, domain.ErrUsage) {
			t.Errorf("username=%q err=%v", username, err)
		}
	}
}
