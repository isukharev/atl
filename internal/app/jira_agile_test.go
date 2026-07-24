package app

import (
	"context"
	"encoding/json"
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

func TestBoardSnapshotEpicRollupIsDeterministic(t *testing.T) {
	f := &fakeAgile{
		config: &domain.BoardConfiguration{ID: 5, Name: "Plan", Type: "kanban"},
		boardIssues: []domain.Issue{
			{ID: "1", Key: "EPIC-2", Status: "Open", Fields: map[string]any{"status": map[string]any{"name": "Open"}, "updated": "2026-04-01T09:00:00.000+0000"}},
			{ID: "2", Key: "CHILD-3", Status: "Closed", Fields: map[string]any{"status": map[string]any{"name": "Closed"}, "updated": "2026-04-03T12:00:00.000+0000", "customfield_10001": "EPIC-2"}},
			{ID: "3", Key: "EPIC-1", Status: "Open", Fields: map[string]any{"status": map[string]any{"name": "Open"}, "updated": "2026-04-01T08:00:00.000+0000"}},
			{ID: "4", Key: "CHILD-2", Status: "In Progress", Fields: map[string]any{"status": map[string]any{"name": "In Progress"}, "updated": "2026-04-04T12:00:00.000+0000", "customfield_10001": "EPIC-1"}},
			{ID: "5", Key: "CHILD-1", Status: "Done", Fields: map[string]any{"status": map[string]any{"name": "Done"}, "updated": "2026-04-02T12:00:00.000+0000", "customfield_10001": "EPIC-1"}},
		},
	}
	snapshot, err := (&JiraService{agile: f}).BoardSnapshot(t.Context(), 5, BoardSnapshotOpts{
		Scope: "board", Columns: []string{"key", "status", "updated", "customfield_10001"},
		EpicField: "customfield_10001", DoneStatuses: []string{"closed, Done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rollup := snapshot.EpicRollup
	if rollup == nil || !rollup.Complete || rollup.EpicField != "customfield_10001" ||
		len(rollup.DoneStatuses) != 2 || rollup.DoneStatuses[0] != "closed" || rollup.DoneStatuses[1] != "Done" ||
		len(rollup.Epics) != 2 {
		t.Fatalf("rollup=%+v", rollup)
	}
	first, second := rollup.Epics[0], rollup.Epics[1]
	if first.Key != "EPIC-1" || !first.ParentPresent || first.ChildCount != 2 || first.DoneChildCount != 1 ||
		first.LatestChildUpdated != "2026-04-04T12:00:00.000+0000" || first.TimestampedChildren != 2 ||
		!first.TimestampCoverageComplete || len(first.StatusCounts) != 2 ||
		first.StatusCounts[0] != (BoardStatusCount{Status: "Done", Count: 1}) ||
		first.StatusCounts[1] != (BoardStatusCount{Status: "In Progress", Count: 1}) {
		t.Fatalf("first epic=%+v", first)
	}
	if second.Key != "EPIC-2" || !second.ParentPresent || second.ChildCount != 1 ||
		second.DoneChildCount != 1 || second.LatestChildUpdated != "2026-04-03T12:00:00.000+0000" {
		t.Fatalf("second epic=%+v", second)
	}
}

func TestBoardEpicRollupReportsIncompleteEvidence(t *testing.T) {
	rollup, err := boardEpicRollup([]BoardSnapshotRow{{
		Key: "CHILD-1", Status: "Open",
		Values: map[string]any{"customfield_10001": "EPIC-1"},
	}}, map[string]string{"CHILD-1": "EPIC-1"}, "customfield_10001", []string{"Done"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if rollup.Complete || len(rollup.Epics) != 1 || rollup.Epics[0].ParentPresent ||
		rollup.Epics[0].TimestampCoverageComplete || rollup.Epics[0].MissingUpdatedChildren != 1 {
		t.Fatalf("rollup=%+v", rollup)
	}
}

func TestBoardEpicRollupInheritsSnapshotTruncation(t *testing.T) {
	rows := []BoardSnapshotRow{
		{Key: "EPIC-1", Status: "Open", Values: map[string]any{"updated": "2026-04-01T00:00:00Z"}},
		{Key: "CHILD-1", Status: "Done", Values: map[string]any{"epic": "EPIC-1", "updated": "2026-04-02T00:00:00Z"}},
	}
	rollup, err := boardEpicRollup(rows, map[string]string{"CHILD-1": "EPIC-1"}, "epic", []string{"Done"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rollup.Complete || !rollup.Epics[0].ParentPresent || !rollup.Epics[0].TimestampCoverageComplete {
		t.Fatalf("rollup=%+v", rollup)
	}
}

func TestBoardSnapshotEpicRollupCountsIssueInBothScopesOnce(t *testing.T) {
	parent := domain.Issue{
		ID: "1", Key: "EPIC-1", Status: "Open",
		Fields: map[string]any{"status": map[string]any{"name": "Open"}, "updated": "2026-04-01T00:00:00Z"},
	}
	child := domain.Issue{
		ID: "2", Key: "CHILD-1", Status: "Done",
		Fields: map[string]any{"status": map[string]any{"name": "Done"}, "updated": "2026-04-02T00:00:00Z", "epic": "EPIC-1"},
	}
	f := &fakeAgile{
		config:        &domain.BoardConfiguration{ID: 5, Type: "scrum"},
		boardIssues:   []domain.Issue{parent, child},
		backlogIssues: []domain.Issue{child},
	}
	snapshot, err := (&JiraService{agile: f}).BoardSnapshot(t.Context(), 5, BoardSnapshotOpts{
		Scope: "all", Columns: []string{"key", "status", "updated", "epic"},
		EpicField: "epic", DoneStatuses: []string{"Done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RowCount != 2 || snapshot.EpicRollup.Epics[0].ChildCount != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestBoardEpicRollupRejectsMalformedEvidence(t *testing.T) {
	tests := []struct {
		name string
		row  BoardSnapshotRow
	}{
		{name: "status", row: BoardSnapshotRow{Key: "CHILD-1", Values: map[string]any{"epic": "EPIC-1", "updated": "2026-04-01T00:00:00Z"}}},
		{name: "updated type", row: BoardSnapshotRow{Key: "CHILD-1", Status: "Open", Values: map[string]any{"epic": "EPIC-1", "updated": 7}}},
		{name: "updated syntax", row: BoardSnapshotRow{Key: "CHILD-1", Status: "Open", Values: map[string]any{"epic": "EPIC-1", "updated": "yesterday"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := boardEpicRollup([]BoardSnapshotRow{tt.row}, map[string]string{"CHILD-1": "EPIC-1"}, "epic", []string{"Done"}, true)
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v, want check failed", err)
			}
		})
	}
}

func TestBoardSnapshotEpicRollupRejectsMalformedRawRelations(t *testing.T) {
	tests := []struct {
		name     string
		relation any
	}{
		{name: "number", relation: 7},
		{name: "list", relation: []any{"EPIC-1"}},
		{name: "object without key", relation: map[string]any{"name": "EPIC-1"}},
		{name: "object with non-string key", relation: map[string]any{"key": 7}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeAgile{
				config: &domain.BoardConfiguration{ID: 5, Type: "kanban"},
				boardIssues: []domain.Issue{{
					Key: "CHILD-1", Status: "Open",
					Fields: map[string]any{
						"status":  map[string]any{"name": "Open"},
						"updated": "2026-04-01T00:00:00Z",
						"epic":    tt.relation,
					},
				}},
			}
			_, err := (&JiraService{agile: f}).BoardSnapshot(t.Context(), 5, BoardSnapshotOpts{
				Scope: "board", Columns: []string{"key", "status", "updated", "epic"},
				EpicField: "epic", DoneStatuses: []string{"Done"},
			})
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("relation=%#v err=%v, want check failed", tt.relation, err)
			}
		})
	}
}

func TestBoardSnapshotEpicRollupUsesExactParentObjectKey(t *testing.T) {
	f := &fakeAgile{
		config: &domain.BoardConfiguration{ID: 5, Type: "kanban"},
		boardIssues: []domain.Issue{
			{Key: "EPIC-1", Status: "Open", Fields: map[string]any{"status": map[string]any{"name": "Open"}, "updated": "2026-04-01T00:00:00Z"}},
			{
				Key: "CHILD-1", Status: "Done",
				Fields: map[string]any{
					"status":  map[string]any{"name": "Done"},
					"updated": "2026-04-02T00:00:00Z",
					"parent":  map[string]any{"key": "EPIC-1", "name": "Misleading label"},
				},
			},
		},
	}
	snapshot, err := (&JiraService{agile: f}).BoardSnapshot(t.Context(), 5, BoardSnapshotOpts{
		Scope: "board", Columns: []string{"key", "status", "updated", "parent"},
		EpicField: "parent", DoneStatuses: []string{"Done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Rows[1].Values["parent"] != "Misleading label" {
		t.Fatalf("presentation row changed: %#v", snapshot.Rows[1].Values["parent"])
	}
	if len(snapshot.EpicRollup.Epics) != 1 || snapshot.EpicRollup.Epics[0].Key != "EPIC-1" ||
		!snapshot.EpicRollup.Epics[0].ParentPresent {
		t.Fatalf("rollup=%+v", snapshot.EpicRollup)
	}
}

func TestBoardSnapshotEpicRollupOptionsFailBeforeBackendRead(t *testing.T) {
	tests := []BoardSnapshotOpts{
		{DoneStatuses: []string{"Done"}},
		{EpicField: "epic"},
		{Columns: []string{"key", "status", "updated"}, EpicField: "epic", DoneStatuses: []string{"Done"}},
		{Columns: []string{"key", "status", "epic"}, EpicField: "epic", DoneStatuses: []string{"Done"}},
		{Columns: []string{"key", "status", "updated", "epic"}, EpicField: "epic", DoneStatuses: []string{"Done", "done"}},
		{Columns: []string{"key", "status", "updated", "epic"}, EpicField: "epic", DoneStatuses: []string{""}},
	}
	for _, opts := range tests {
		f := &fakeAgile{}
		_, err := (&JiraService{agile: f}).BoardSnapshot(t.Context(), 5, opts)
		if !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("opts=%+v err=%v, want usage", opts, err)
		}
		if f.boardIssueCalls != 0 || f.backlogCalls != 0 {
			t.Fatalf("opts=%+v read backend", opts)
		}
	}
}

func TestBoardSnapshotWithoutRollupPreservesJSONShape(t *testing.T) {
	data, err := json.Marshal(&BoardSnapshot{SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "epic_rollup") {
		t.Fatalf("unexpected optional field: %s", data)
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
		Board: &domain.BoardConfiguration{Name: "Plan", Type: "kanban"}, Scope: "board", Complete: true,
		Projection: BoardProjection{Columns: []string{"position", "key", "status", "board.column", "summary", "customfield_10001"}, Fields: []string{"status", "summary", "customfield_10001"}},
		Rows:       []BoardSnapshotRow{{Key: "ENG-1", Status: "Open", Column: "To Do", Values: map[string]any{"summary": "First", "customfield_10001": "Team A"}}},
		RowCount:   1,
	}
	md := BoardSnapshotMarkdown(snapshot)
	for _, want := range []string{"Kanban board", "Source: board", "— Plan", "| # | Key | Status | Column | Summary | customfield_10001 |", "| 0 | ENG-1 | Open | To Do | First | Team A |"} {
		if !strings.Contains(md, want) {
			t.Fatalf("Markdown missing %q:\n%s", want, md)
		}
	}
}

func TestBoardMarkdownEscapesEpicRollupValues(t *testing.T) {
	snapshot := &BoardSnapshot{
		Board: &domain.BoardConfiguration{Name: "Plan", Type: "kanban"}, Scope: "board", Complete: true,
		Projection: BoardProjection{Columns: []string{"key"}, Fields: []string{}},
		EpicRollup: &BoardEpicRollup{
			EpicField: "custom|field", DoneStatuses: []string{"Done\nClosed"}, Complete: true,
			Epics: []BoardEpicRollupEntry{{
				Key: "EPIC|1", ParentPresent: true, ChildCount: 1, DoneChildCount: 1,
				TimestampCoverageComplete: true, StatusCounts: []BoardStatusCount{{Status: "Done|Closed", Count: 1}},
			}},
		},
	}
	md := BoardSnapshotMarkdown(snapshot)
	for _, want := range []string{"## Epic rollup", "custom\\|field", "Done Closed", "EPIC\\|1", "Done\\|Closed=1"} {
		if !strings.Contains(md, want) {
			t.Fatalf("Markdown missing %q:\n%s", want, md)
		}
	}
}
