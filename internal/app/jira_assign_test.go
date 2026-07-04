package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// assignTracker records Assign/CurrentUser calls for JiraService.Assign tests.
type assignTracker struct {
	domain.Tracker

	assignKey  string
	assignUser string
	assignErr  error
	me         *domain.User
	meErr      error
}

func (f *assignTracker) Assign(_ context.Context, key, username string) error {
	f.assignKey, f.assignUser = key, username
	return f.assignErr
}

func (f *assignTracker) CurrentUser(_ context.Context) (*domain.User, error) {
	return f.me, f.meErr
}

func TestAssignPassesUsernameThrough(t *testing.T) {
	tr := &assignTracker{}
	svc := &JiraService{tr: tr}
	got, err := svc.Assign(context.Background(), "ENG-1", "jdoe", false)
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if got != "jdoe" || tr.assignKey != "ENG-1" || tr.assignUser != "jdoe" {
		t.Errorf("got assignee %q, tracker saw (%q,%q)", got, tr.assignKey, tr.assignUser)
	}
}

func TestAssignMeResolvesCurrentUser(t *testing.T) {
	tr := &assignTracker{me: &domain.User{Name: "ivan", DisplayName: "Ivan"}}
	svc := &JiraService{tr: tr}
	got, err := svc.Assign(context.Background(), "ENG-1", "", true)
	if err != nil {
		t.Fatalf("Assign --me: %v", err)
	}
	if got != "ivan" || tr.assignUser != "ivan" {
		t.Errorf("assignee = %q (tracker %q), want ivan", got, tr.assignUser)
	}
}

// A current user without a DC username cannot be assigned — that must surface
// as a usage error before any write, not as a silent unassign.
func TestAssignMeWithoutUsernameIsUsageError(t *testing.T) {
	tr := &assignTracker{me: &domain.User{DisplayName: "Cloud Only"}}
	svc := &JiraService{tr: tr}
	_, err := svc.Assign(context.Background(), "ENG-1", "", true)
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
	if tr.assignKey != "" {
		t.Errorf("Assign must not be called on resolve failure, saw key %q", tr.assignKey)
	}
}

func TestAssignEmptyUnassigns(t *testing.T) {
	tr := &assignTracker{}
	svc := &JiraService{tr: tr}
	got, err := svc.Assign(context.Background(), "ENG-1", "", false)
	if err != nil {
		t.Fatalf("Assign unassign: %v", err)
	}
	if got != "" || tr.assignKey != "ENG-1" || tr.assignUser != "" {
		t.Errorf("unassign: got %q, tracker (%q,%q)", got, tr.assignKey, tr.assignUser)
	}
}
