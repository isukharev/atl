package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type jiraWatcherStoreStub struct {
	domain.Tracker
	state       domain.IssueWatcherList
	currentUser *domain.User
	currentErr  error
	writeErr    error
	addCalls    int
	removeCalls int
}

func (s *jiraWatcherStoreStub) ListIssueWatchers(context.Context, string) (*domain.IssueWatcherList, error) {
	copy := s.state
	copy.Watchers = append([]domain.IssueWatcher(nil), s.state.Watchers...)
	return &copy, nil
}

func (s *jiraWatcherStoreStub) AddIssueWatcher(_ context.Context, _, username string) error {
	s.addCalls++
	if !watcherPresent(s.state.Watchers, username) {
		s.state.Watchers = append(s.state.Watchers, domain.IssueWatcher{Name: username, DisplayName: username, Active: true})
		s.state.WatchCount++
	}
	return s.writeErr
}

func (s *jiraWatcherStoreStub) RemoveIssueWatcher(_ context.Context, _, username string) error {
	s.removeCalls++
	filtered := s.state.Watchers[:0]
	for _, watcher := range s.state.Watchers {
		if watcher.Name != username {
			filtered = append(filtered, watcher)
		}
	}
	s.state.Watchers = filtered
	s.state.WatchCount = len(filtered)
	return s.writeErr
}

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

func TestNormalizeJiraWatcherUsernameRejectsUnsafeInput(t *testing.T) {
	for _, username := range []string{"", "bad\nname", string([]byte{0xff})} {
		if _, err := normalizeJiraWatcherUsername(username); !errors.Is(err, domain.ErrUsage) {
			t.Errorf("username=%q err=%v", username, err)
		}
	}
}
