package jira

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// The Agile (GreenHopper) REST API lives under /rest/agile/1.0/ on Jira Software
// Server/Data Center. Listing endpoints page with startAt/maxResults and report
// either an isLast flag (boards/sprints) or a total (issue search); agileNext
// reconciles both into the adapter's string cursor convention.

var _ domain.Agile = (*Jira)(nil)

// agileNext computes the next startAt cursor. It stops on an explicit isLast,
// on a total that has been reached, or on an empty page.
func agileNext(startAt, count, total int, isLast *bool) string {
	if count == 0 {
		return ""
	}
	if isLast != nil && *isLast {
		return ""
	}
	if total > 0 && startAt+count >= total {
		return ""
	}
	return strconv.Itoa(startAt + count)
}

// agileLimit clamps the page size to the Agile API's bounds (max 50).
func agileLimit(limit int) int {
	if limit <= 0 || limit > 50 {
		return 50
	}
	return limit
}

type boardDTO struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Location struct {
		ProjectKey string `json:"projectKey"`
	} `json:"location"`
}

func (d boardDTO) toDomain() domain.Board {
	return domain.Board{ID: d.ID, Name: d.Name, Type: d.Type, ProjectKey: d.Location.ProjectKey}
}

// Boards lists agile boards. project (a key or numeric id) narrows the result to
// boards located in that project when non-empty.
func (j *Jira) Boards(ctx context.Context, project string, limit int, cursor string) ([]domain.Board, string, error) {
	startAt, _ := strconv.Atoi(cursor)
	q := url.Values{}
	q.Set("startAt", strconv.Itoa(startAt))
	q.Set("maxResults", strconv.Itoa(agileLimit(limit)))
	if project != "" {
		q.Set("projectKeyOrId", project)
	}
	var resp struct {
		StartAt int        `json:"startAt"`
		Total   int        `json:"total"`
		IsLast  *bool      `json:"isLast"`
		Values  []boardDTO `json:"values"`
	}
	if err := j.c.GetJSON(ctx, "/rest/agile/1.0/board?"+q.Encode(), &resp); err != nil {
		return nil, "", err
	}
	out := make([]domain.Board, 0, len(resp.Values))
	for _, b := range resp.Values {
		out = append(out, b.toDomain())
	}
	return out, agileNext(startAt, len(resp.Values), resp.Total, resp.IsLast), nil
}

// Board fetches one board by id.
func (j *Jira) Board(ctx context.Context, id int) (*domain.Board, error) {
	var d boardDTO
	if err := j.c.GetJSON(ctx, "/rest/agile/1.0/board/"+strconv.Itoa(id), &d); err != nil {
		return nil, err
	}
	b := d.toDomain()
	return &b, nil
}

type sprintDTO struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	State         string `json:"state"`
	StartDate     string `json:"startDate"`
	EndDate       string `json:"endDate"`
	CompleteDate  string `json:"completeDate"`
	Goal          string `json:"goal"`
	OriginBoardID int    `json:"originBoardId"`
}

func (d sprintDTO) toDomain() domain.Sprint {
	return domain.Sprint{
		ID: d.ID, Name: d.Name, State: d.State,
		StartDate: d.StartDate, EndDate: d.EndDate, CompleteDate: d.CompleteDate,
		Goal: d.Goal, OriginBoardID: d.OriginBoardID,
	}
}

// Sprints lists a board's sprints, optionally filtered by state.
func (j *Jira) Sprints(ctx context.Context, boardID int, state string, limit int, cursor string) ([]domain.Sprint, string, error) {
	startAt, _ := strconv.Atoi(cursor)
	q := url.Values{}
	q.Set("startAt", strconv.Itoa(startAt))
	q.Set("maxResults", strconv.Itoa(agileLimit(limit)))
	if state != "" {
		q.Set("state", state)
	}
	var resp struct {
		StartAt int         `json:"startAt"`
		Total   int         `json:"total"`
		IsLast  *bool       `json:"isLast"`
		Values  []sprintDTO `json:"values"`
	}
	if err := j.c.GetJSON(ctx, "/rest/agile/1.0/board/"+strconv.Itoa(boardID)+"/sprint?"+q.Encode(), &resp); err != nil {
		return nil, "", err
	}
	out := make([]domain.Sprint, 0, len(resp.Values))
	for _, s := range resp.Values {
		out = append(out, s.toDomain())
	}
	return out, agileNext(startAt, len(resp.Values), resp.Total, resp.IsLast), nil
}

// Sprint fetches one sprint by id.
func (j *Jira) Sprint(ctx context.Context, id int) (*domain.Sprint, error) {
	var d sprintDTO
	if err := j.c.GetJSON(ctx, "/rest/agile/1.0/sprint/"+strconv.Itoa(id), &d); err != nil {
		return nil, err
	}
	s := d.toDomain()
	return &s, nil
}

// SprintIssues lists the issues assigned to a sprint. The response is the same
// search shape as JQL search (an "issues" array), so it reuses mapIssue.
func (j *Jira) SprintIssues(ctx context.Context, sprintID int, fields []string, limit int, cursor string) ([]domain.Issue, string, error) {
	startAt, _ := strconv.Atoi(cursor)
	q := url.Values{}
	q.Set("startAt", strconv.Itoa(startAt))
	q.Set("maxResults", strconv.Itoa(agileLimit(limit)))
	if len(fields) > 0 {
		q.Set("fields", strings.Join(fields, ","))
	}
	var resp struct {
		StartAt int        `json:"startAt"`
		Total   int        `json:"total"`
		Issues  []issueDTO `json:"issues"`
	}
	if err := j.c.GetJSON(ctx, "/rest/agile/1.0/sprint/"+strconv.Itoa(sprintID)+"/issue?"+q.Encode(), &resp); err != nil {
		return nil, "", err
	}
	out := make([]domain.Issue, 0, len(resp.Issues))
	for _, d := range resp.Issues {
		out = append(out, *j.mapIssue(d))
	}
	return out, agileNext(startAt, len(resp.Issues), resp.Total, nil), nil
}

// MoveIssuesToSprint moves the given issues into a sprint.
func (j *Jira) MoveIssuesToSprint(ctx context.Context, sprintID int, keys []string) error {
	if len(keys) == 0 {
		return fmt.Errorf("%w: no issue keys given", domain.ErrUsage)
	}
	return j.c.SendJSON(ctx, "POST", "/rest/agile/1.0/sprint/"+strconv.Itoa(sprintID)+"/issue",
		map[string]any{"issues": keys}, nil)
}

// MoveIssuesToBacklog removes the given issues from any sprint (to the backlog).
func (j *Jira) MoveIssuesToBacklog(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return fmt.Errorf("%w: no issue keys given", domain.ErrUsage)
	}
	return j.c.SendJSON(ctx, "POST", "/rest/agile/1.0/backlog/issue",
		map[string]any{"issues": keys}, nil)
}
