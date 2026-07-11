package app

import (
	"context"
	"errors"
	"strings"
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

	boards          []domain.Board
	next            string
	sprints         []domain.Sprint
	board           *domain.Board
	sprint          *domain.Sprint
	issues          []domain.Issue
	config          *domain.BoardConfiguration
	boardIssues     []domain.Issue
	backlogIssues   []domain.Issue
	boardIssueCalls int
	backlogCalls    int
	err             error
}

func (f *fakeAgile) Boards(_ context.Context, project string, limit int, cursor string) ([]domain.Board, string, error) {
	f.boardsProject, f.boardsLimit, f.boardsCursor = project, limit, cursor
	return f.boards, f.next, f.err
}

func (f *fakeAgile) Board(_ context.Context, _ int) (*domain.Board, error) {
	return f.board, f.err
}

func (f *fakeAgile) BoardConfiguration(_ context.Context, _ int) (*domain.BoardConfiguration, error) {
	return f.config, f.err
}

func (f *fakeAgile) BoardIssues(_ context.Context, _ int, _ []string, _ string, _ int, _ string) ([]domain.Issue, string, error) {
	f.boardIssueCalls++
	return f.boardIssues, "", f.err
}

func (f *fakeAgile) BoardBacklog(_ context.Context, _ int, _ []string, _ string, _ int, _ string) ([]domain.Issue, string, error) {
	f.backlogCalls++
	return f.backlogIssues, "", f.err
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

func TestBoardSnapshotMapsColumnsAndScopeMembership(t *testing.T) {
	f := &fakeAgile{
		config: &domain.BoardConfiguration{ID: 5, Name: "Plan", Type: "scrum", Columns: []domain.BoardColumn{{Name: "Doing", StatusIDs: []string{"2"}}}},
		boardIssues: []domain.Issue{
			{ID: "1", Key: "ENG-1", Status: "In progress", StatusID: "2", Fields: map[string]any{"summary": "First", "status": map[string]any{"id": "2", "name": "In progress"}}},
			{ID: "2", Key: "ENG-2", Status: "Unknown", StatusID: "9", Fields: map[string]any{"summary": "Second", "status": map[string]any{"id": "9", "name": "Unknown"}}},
		},
		backlogIssues: []domain.Issue{
			{ID: "2", Key: "ENG-2", Status: "Unknown", StatusID: "9", Fields: map[string]any{"summary": "Second"}},
			{ID: "3", Key: "ENG-3", Status: "In progress", StatusID: "2", Fields: map[string]any{"summary": "Third"}},
		},
	}
	snapshot, err := (&JiraService{agile: f}).BoardSnapshot(t.Context(), 5, BoardSnapshotOpts{Scope: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Rows) != 3 || !snapshot.BacklogFetched || !snapshot.Rows[0].ColumnMapped || snapshot.Rows[0].Column != "Doing" || snapshot.Rows[1].ColumnMapped || !snapshot.Rows[1].InBoard || !snapshot.Rows[1].InBacklog || snapshot.Rows[2].InBoard || !snapshot.Rows[2].InBacklog {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestBoardSnapshotKanbanDoesNotCallSprintOrBacklog(t *testing.T) {
	f := &fakeAgile{
		config:      &domain.BoardConfiguration{ID: 5, Name: "Flow", Type: "kanban", Columns: []domain.BoardColumn{}},
		boardIssues: []domain.Issue{{ID: "1", Key: "ENG-1", Fields: map[string]any{}}},
	}
	snapshot, err := (&JiraService{agile: f}).BoardSnapshot(t.Context(), 5, BoardSnapshotOpts{Scope: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if f.backlogCalls != 0 || snapshot.BacklogFetched || len(snapshot.Rows) != 1 {
		t.Fatalf("backlog_calls=%d snapshot=%+v", f.backlogCalls, snapshot)
	}
}

type repeatedBoardPageAgile struct{ domain.Agile }

func (repeatedBoardPageAgile) BoardConfiguration(context.Context, int) (*domain.BoardConfiguration, error) {
	return &domain.BoardConfiguration{ID: 5, Type: "scrum", Columns: []domain.BoardColumn{}}, nil
}

func (repeatedBoardPageAgile) BoardIssues(_ context.Context, _ int, _ []string, _ string, _ int, cursor string) ([]domain.Issue, string, error) {
	if cursor == "" {
		return []domain.Issue{{Key: "ENG-1"}}, "1", nil
	}
	return []domain.Issue{{Key: "ENG-1"}}, "", nil
}

func TestBoardSnapshotRejectsDuplicateAcrossPages(t *testing.T) {
	_, err := (&JiraService{agile: repeatedBoardPageAgile{}}).BoardSnapshot(t.Context(), 5, BoardSnapshotOpts{Scope: "board"})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err=%v, want check failed", err)
	}
}

type limitedBoardAgile struct{ domain.Agile }

func (limitedBoardAgile) BoardConfiguration(context.Context, int) (*domain.BoardConfiguration, error) {
	return &domain.BoardConfiguration{ID: 5, Type: "scrum", Columns: []domain.BoardColumn{}}, nil
}

func (limitedBoardAgile) BoardIssues(context.Context, int, []string, string, int, string) ([]domain.Issue, string, error) {
	return []domain.Issue{{Key: "ENG-1"}, {Key: "ENG-2"}}, "2", nil
}

func TestBoardSnapshotLimitIsExplicitTruncation(t *testing.T) {
	snapshot, err := (&JiraService{agile: limitedBoardAgile{}}).BoardSnapshot(t.Context(), 5, BoardSnapshotOpts{Scope: "board", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Complete || !snapshot.Truncated || snapshot.RowCount != 2 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestBoardJSONLUsesCompactIdentityInsteadOfRepeatingColumns(t *testing.T) {
	snapshot := &BoardSnapshot{
		SchemaVersion: 1,
		Board:         &domain.BoardConfiguration{ID: 5, Name: "Plan", Type: "kanban", Columns: []domain.BoardColumn{{Name: "A", StatusIDs: []string{"1"}}}},
		Scope:         "board", Projection: BoardProjection{Kind: "jira-fields-v1", Fields: []string{"summary"}, Ordering: "backend-rank"},
		Rows:     []BoardSnapshotRow{{Key: "ENG-1", Column: "A", ColumnMapped: true, Values: map[string]any{"summary": "First"}}},
		RowCount: 1, Complete: true,
	}
	data, err := renderBoardSnapshot("jsonl", snapshot, false)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"board_id":5`) || !strings.Contains(text, `"row_count":1`) || strings.Contains(text, `"board":{"columns"`) {
		t.Fatalf("JSONL=%s", text)
	}
}

func TestBoardMarkdownUsesRequestedProjectionFields(t *testing.T) {
	snapshot := &BoardSnapshot{
		Board: &domain.BoardConfiguration{Name: "Plan"}, Scope: "board", Complete: true,
		Projection: BoardProjection{Columns: []string{"position", "key", "status", "board.column", "summary", "customfield_10001"}, Fields: []string{"status", "summary", "customfield_10001"}},
		Rows:       []BoardSnapshotRow{{Key: "ENG-1", Status: "Open", Column: "To Do", Values: map[string]any{"summary": "First", "customfield_10001": "Team A"}}},
		RowCount:   1,
	}
	md := BoardSnapshotMarkdown(snapshot)
	for _, want := range []string{"| # | Key | Status | Column | Summary | customfield_10001 |", "| 0 | ENG-1 | Open | To Do | First | Team A |"} {
		if !strings.Contains(md, want) {
			t.Fatalf("Markdown missing %q:\n%s", want, md)
		}
	}
}
