package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capturedAgileReq records one request the Agile adapter sent, so a test can
// assert the DC Agile REST endpoint, query params, and JSON payload.
type capturedAgileReq struct {
	method, path, query, body string
}

// agileServer spins a recording httptest server that routes by the longest
// matching "METHOD path-prefix" and replies with the canned JSON body. It
// returns a Jira adapter pointed at it plus a pointer to the captured requests.
func agileServer(t *testing.T, routes map[string]string) (*Jira, *[]capturedAgileReq) {
	t.Helper()
	var reqs []capturedAgileReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reqs = append(reqs, capturedAgileReq{r.Method, r.URL.Path, r.URL.RawQuery, string(b)})
		w.Header().Set("Content-Type", "application/json")
		best, bestLen := `{}`, -1
		for key, body := range routes {
			method, prefix, ok := strings.Cut(key, " ")
			if ok && method == r.Method && strings.HasPrefix(r.URL.Path, prefix) && len(prefix) > bestLen {
				best, bestLen = body, len(prefix)
			}
		}
		_, _ = io.WriteString(w, best)
	}))
	t.Cleanup(srv.Close)
	return newTestJira(srv), &reqs
}

func TestBoardsListsParsesAndPaginates(t *testing.T) {
	// total 2 but only 1 value on this page + isLast false → a next cursor of "1".
	const body = `{"maxResults":50,"startAt":0,"total":2,"isLast":false,"values":[
		{"id":1,"name":"ENG board","type":"scrum","location":{"projectKey":"ENG","projectName":"Engineering"}}
	]}`
	j, reqs := agileServer(t, map[string]string{"GET /rest/agile/1.0/board": body})

	boards, next, err := j.Boards(context.Background(), "ENG", 50, "")
	if err != nil {
		t.Fatalf("Boards: %v", err)
	}
	if len(boards) != 1 {
		t.Fatalf("got %d boards, want 1", len(boards))
	}
	b := boards[0]
	if b.ID != 1 || b.Name != "ENG board" || b.Type != "scrum" || b.ProjectKey != "ENG" {
		t.Errorf("board = %+v, want id=1 name='ENG board' type=scrum project=ENG", b)
	}
	if next != "1" {
		t.Errorf("next cursor = %q, want \"1\" (1 of 2 returned, isLast false)", next)
	}
	// The DC project filter parameter is projectKeyOrId.
	if q := (*reqs)[0].query; !strings.Contains(q, "projectKeyOrId=ENG") {
		t.Errorf("query = %q, want projectKeyOrId=ENG", q)
	}
}

func TestBoardsNextEmptyWhenIsLast(t *testing.T) {
	const body = `{"maxResults":50,"startAt":0,"total":1,"isLast":true,"values":[
		{"id":9,"name":"Kanban","type":"kanban"}
	]}`
	j, _ := agileServer(t, map[string]string{"GET /rest/agile/1.0/board": body})
	_, next, err := j.Boards(context.Background(), "", 50, "")
	if err != nil {
		t.Fatalf("Boards: %v", err)
	}
	if next != "" {
		t.Errorf("next = %q, want \"\" when isLast is true", next)
	}
}

func TestBoardGetByID(t *testing.T) {
	const body = `{"id":5,"name":"Platform","type":"scrum"}`
	j, reqs := agileServer(t, map[string]string{"GET /rest/agile/1.0/board/5": body})

	b, err := j.Board(context.Background(), 5)
	if err != nil {
		t.Fatalf("Board: %v", err)
	}
	if b.ID != 5 || b.Name != "Platform" {
		t.Errorf("board = %+v, want id=5 name=Platform", b)
	}
	if (*reqs)[0].path != "/rest/agile/1.0/board/5" {
		t.Errorf("path = %q, want /rest/agile/1.0/board/5", (*reqs)[0].path)
	}
}

func TestSprintsListByBoardAndState(t *testing.T) {
	const body = `{"maxResults":50,"startAt":0,"total":1,"isLast":true,"values":[
		{"id":7,"state":"active","name":"Sprint 3","goal":"ship it","startDate":"2026-06-01T00:00:00.000Z","endDate":"2026-06-15T00:00:00.000Z","originBoardId":5}
	]}`
	j, reqs := agileServer(t, map[string]string{"GET /rest/agile/1.0/board/5/sprint": body})

	sprints, next, err := j.Sprints(context.Background(), 5, "active", 50, "")
	if err != nil {
		t.Fatalf("Sprints: %v", err)
	}
	if len(sprints) != 1 {
		t.Fatalf("got %d sprints, want 1", len(sprints))
	}
	s := sprints[0]
	if s.ID != 7 || s.State != "active" || s.Name != "Sprint 3" || s.Goal != "ship it" || s.OriginBoardID != 5 {
		t.Errorf("sprint = %+v, want id=7 state=active name='Sprint 3' goal='ship it' originBoard=5", s)
	}
	if next != "" {
		t.Errorf("next = %q, want \"\" (isLast)", next)
	}
	r := (*reqs)[0]
	if r.path != "/rest/agile/1.0/board/5/sprint" {
		t.Errorf("path = %q, want /rest/agile/1.0/board/5/sprint", r.path)
	}
	if !strings.Contains(r.query, "state=active") {
		t.Errorf("query = %q, want state=active", r.query)
	}
}

func TestSprintGetByID(t *testing.T) {
	const body = `{"id":7,"state":"closed","name":"Sprint 2","completeDate":"2026-05-30T00:00:00.000Z","originBoardId":5}`
	j, reqs := agileServer(t, map[string]string{"GET /rest/agile/1.0/sprint/7": body})

	s, err := j.Sprint(context.Background(), 7)
	if err != nil {
		t.Fatalf("Sprint: %v", err)
	}
	if s.ID != 7 || s.State != "closed" || s.CompleteDate == "" {
		t.Errorf("sprint = %+v, want id=7 state=closed completeDate set", s)
	}
	if (*reqs)[0].path != "/rest/agile/1.0/sprint/7" {
		t.Errorf("path = %q, want /rest/agile/1.0/sprint/7", (*reqs)[0].path)
	}
}

func TestSprintIssuesParsesAndPaginates(t *testing.T) {
	const body = `{"startAt":0,"maxResults":50,"total":2,"issues":[
		{"key":"ENG-1","fields":{"summary":"first","status":{"name":"Open"}}}
	]}`
	j, reqs := agileServer(t, map[string]string{"GET /rest/agile/1.0/sprint/7/issue": body})

	issues, next, err := j.SprintIssues(context.Background(), 7, nil, 50, "")
	if err != nil {
		t.Fatalf("SprintIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].Key != "ENG-1" || issues[0].Summary != "first" || issues[0].Status != "Open" {
		t.Fatalf("issues = %+v, want one ENG-1 'first' Open", issues)
	}
	if next != "1" {
		t.Errorf("next = %q, want \"1\" (1 of 2 via total)", next)
	}
	if (*reqs)[0].path != "/rest/agile/1.0/sprint/7/issue" {
		t.Errorf("path = %q, want /rest/agile/1.0/sprint/7/issue", (*reqs)[0].path)
	}
}

func TestMoveIssuesToSprintWiresPost(t *testing.T) {
	j, reqs := agileServer(t, map[string]string{"POST /rest/agile/1.0/sprint/7/issue": ``})

	if err := j.MoveIssuesToSprint(context.Background(), 7, []string{"ENG-1", "ENG-2"}); err != nil {
		t.Fatalf("MoveIssuesToSprint: %v", err)
	}
	r := (*reqs)[0]
	if r.method != http.MethodPost || r.path != "/rest/agile/1.0/sprint/7/issue" {
		t.Fatalf("req = %s %s, want POST /rest/agile/1.0/sprint/7/issue", r.method, r.path)
	}
	var payload struct {
		Issues []string `json:"issues"`
	}
	if err := json.Unmarshal([]byte(r.body), &payload); err != nil {
		t.Fatalf("decode body %q: %v", r.body, err)
	}
	if len(payload.Issues) != 2 || payload.Issues[0] != "ENG-1" || payload.Issues[1] != "ENG-2" {
		t.Errorf("issues = %v, want [ENG-1 ENG-2]", payload.Issues)
	}
}

func TestMoveIssuesToBacklogWiresPost(t *testing.T) {
	j, reqs := agileServer(t, map[string]string{"POST /rest/agile/1.0/backlog/issue": ``})

	if err := j.MoveIssuesToBacklog(context.Background(), []string{"ENG-9"}); err != nil {
		t.Fatalf("MoveIssuesToBacklog: %v", err)
	}
	r := (*reqs)[0]
	if r.method != http.MethodPost || r.path != "/rest/agile/1.0/backlog/issue" {
		t.Fatalf("req = %s %s, want POST /rest/agile/1.0/backlog/issue", r.method, r.path)
	}
	var payload struct {
		Issues []string `json:"issues"`
	}
	if err := json.Unmarshal([]byte(r.body), &payload); err != nil {
		t.Fatalf("decode body %q: %v", r.body, err)
	}
	if len(payload.Issues) != 1 || payload.Issues[0] != "ENG-9" {
		t.Errorf("issues = %v, want [ENG-9]", payload.Issues)
	}
}

// MoveIssuesToSprint with no keys is a usage error and must not contact the
// server (an empty {"issues":[]} POST would be a wasted round-trip).
func TestMoveIssuesToSprintEmptyIsUsageError(t *testing.T) {
	j, reqs := agileServer(t, map[string]string{"POST /rest/agile/1.0/sprint/7/issue": ``})
	if err := j.MoveIssuesToSprint(context.Background(), 7, nil); err == nil {
		t.Fatal("MoveIssuesToSprint(nil): want error, got nil")
	}
	if len(*reqs) != 0 {
		t.Errorf("sent %d requests, want 0 (validate before the wire)", len(*reqs))
	}
}
