package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// fakeAgile embeds domain.Agile so only the methods a test needs are
// implemented; it records call-through args and returns canned values/errors.
type fakeAgile struct {
	domain.Agile

	boardsProject string
	boardsLimit   int
	boardsCursor  string
	sprintsBoard  int
	sprintsState  string
	addSprintID   int
	addKeys       []string
	backlogKeys   []string

	boards  []domain.Board
	next    string
	sprints []domain.Sprint
	board   *domain.Board
	sprint  *domain.Sprint
	issues  []domain.Issue
	err     error
}

func (f *fakeAgile) Boards(_ context.Context, project string, limit int, cursor string) ([]domain.Board, string, error) {
	f.boardsProject, f.boardsLimit, f.boardsCursor = project, limit, cursor
	return f.boards, f.next, f.err
}

func (f *fakeAgile) Board(_ context.Context, _ int) (*domain.Board, error) {
	return f.board, f.err
}

func (f *fakeAgile) Sprints(_ context.Context, boardID int, state string, _ int, _ string) ([]domain.Sprint, string, error) {
	f.sprintsBoard, f.sprintsState = boardID, state
	return f.sprints, f.next, f.err
}

func (f *fakeAgile) Sprint(_ context.Context, _ int) (*domain.Sprint, error) {
	return f.sprint, f.err
}

func (f *fakeAgile) SprintIssues(_ context.Context, _ int, _ []string, _ int, _ string) ([]domain.Issue, string, error) {
	return f.issues, f.next, f.err
}

func (f *fakeAgile) MoveIssuesToSprint(_ context.Context, sprintID int, keys []string) error {
	f.addSprintID, f.addKeys = sprintID, keys
	return f.err
}

func (f *fakeAgile) MoveIssuesToBacklog(_ context.Context, keys []string) error {
	f.backlogKeys = keys
	return f.err
}

func TestBoardsPassesThrough(t *testing.T) {
	f := &fakeAgile{boards: []domain.Board{{ID: 1, Name: "B"}}, next: "5"}
	svc := &JiraService{agile: f}

	boards, next, err := svc.Boards(context.Background(), "ENG", 25, "0")
	if err != nil {
		t.Fatalf("Boards: %v", err)
	}
	if len(boards) != 1 || next != "5" {
		t.Fatalf("boards=%v next=%q, want one board next=5", boards, next)
	}
	if f.boardsProject != "ENG" || f.boardsLimit != 25 || f.boardsCursor != "0" {
		t.Errorf("passed project=%q limit=%d cursor=%q, want ENG/25/0", f.boardsProject, f.boardsLimit, f.boardsCursor)
	}
}

// SprintCurrent asks the backend for active sprints and returns the first one.
func TestSprintCurrentPicksActive(t *testing.T) {
	f := &fakeAgile{sprints: []domain.Sprint{{ID: 7, Name: "Sprint 3", State: "active"}}}
	svc := &JiraService{agile: f}

	s, err := svc.SprintCurrent(context.Background(), 5)
	if err != nil {
		t.Fatalf("SprintCurrent: %v", err)
	}
	if s.ID != 7 {
		t.Errorf("sprint id = %d, want 7", s.ID)
	}
	if f.sprintsBoard != 5 || f.sprintsState != "active" {
		t.Errorf("queried board=%d state=%q, want 5/active", f.sprintsBoard, f.sprintsState)
	}
}

// No active sprint is a not-found condition (exit 4), not a silent empty result.
func TestSprintCurrentNoneIsNotFound(t *testing.T) {
	f := &fakeAgile{sprints: nil}
	svc := &JiraService{agile: f}

	_, err := svc.SprintCurrent(context.Background(), 5)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want wrap of domain.ErrNotFound", err)
	}
}

func TestAddRemoveSprintPassThrough(t *testing.T) {
	f := &fakeAgile{}
	svc := &JiraService{agile: f}

	if err := svc.AddToSprint(context.Background(), 7, []string{"ENG-1"}); err != nil {
		t.Fatalf("AddToSprint: %v", err)
	}
	if f.addSprintID != 7 || len(f.addKeys) != 1 || f.addKeys[0] != "ENG-1" {
		t.Errorf("add recorded sprint=%d keys=%v, want 7/[ENG-1]", f.addSprintID, f.addKeys)
	}

	if err := svc.RemoveFromSprint(context.Background(), []string{"ENG-2"}); err != nil {
		t.Fatalf("RemoveFromSprint: %v", err)
	}
	if len(f.backlogKeys) != 1 || f.backlogKeys[0] != "ENG-2" {
		t.Errorf("backlog recorded keys=%v, want [ENG-2]", f.backlogKeys)
	}
}
