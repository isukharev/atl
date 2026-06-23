package app

import (
	"context"
	"fmt"

	"github.com/isukharev/atl/internal/domain"
)

// Boards lists agile boards, optionally filtered to a project (key or id).
func (s *JiraService) Boards(ctx context.Context, project string, limit int, cursor string) ([]domain.Board, string, error) {
	return s.agile.Boards(ctx, project, limit, cursor)
}

// Board fetches one board by id.
func (s *JiraService) Board(ctx context.Context, id int) (*domain.Board, error) {
	return s.agile.Board(ctx, id)
}

// Sprints lists a board's sprints, optionally filtered by state.
func (s *JiraService) Sprints(ctx context.Context, boardID int, state string, limit int, cursor string) ([]domain.Sprint, string, error) {
	return s.agile.Sprints(ctx, boardID, state, limit, cursor)
}

// Sprint fetches one sprint by id.
func (s *JiraService) Sprint(ctx context.Context, id int) (*domain.Sprint, error) {
	return s.agile.Sprint(ctx, id)
}

// SprintIssues lists the issues assigned to a sprint.
func (s *JiraService) SprintIssues(ctx context.Context, sprintID int, fields []string, limit int, cursor string) ([]domain.Issue, string, error) {
	return s.agile.SprintIssues(ctx, sprintID, fields, limit, cursor)
}

// SprintCurrent returns the board's single active sprint. It is a use-case
// convenience over Sprints(state=active); an empty result is a not-found
// condition (exit 4) rather than a silent nil.
func (s *JiraService) SprintCurrent(ctx context.Context, boardID int) (*domain.Sprint, error) {
	sprints, _, err := s.agile.Sprints(ctx, boardID, "active", 50, "")
	if err != nil {
		return nil, err
	}
	if len(sprints) == 0 {
		return nil, fmt.Errorf("%w: no active sprint on board %d", domain.ErrNotFound, boardID)
	}
	return &sprints[0], nil
}

// AddToSprint moves issues (by key) into a sprint.
func (s *JiraService) AddToSprint(ctx context.Context, sprintID int, keys []string) error {
	return s.agile.MoveIssuesToSprint(ctx, sprintID, keys)
}

// RemoveFromSprint moves issues (by key) out of any sprint, back to the backlog.
func (s *JiraService) RemoveFromSprint(ctx context.Context, keys []string) error {
	return s.agile.MoveIssuesToBacklog(ctx, keys)
}
