package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

const kanbanConfigBody = `{
	"id":5,"name":"Flow","type":"kanban","filter":{"id":"42"},
	"subQuery":{"query":"fixVersion is EMPTY"},
	"columnConfig":{"constraintType":"issueCount","columns":[
		{"name":"To Do","statuses":[{"id":"11"}]},
		{"name":"Done","statuses":[{"id":"12"}]}
	]},"ranking":{"rankCustomFieldId":10019}
}`

const boardIssuesBody = `{"startAt":0,"maxResults":50,"total":2,"issues":[
	{"id":"10001","key":"ENG-1","fields":{"summary":"=Formula","status":{"id":"11","name":"Open"},"assignee":{"displayName":"Owner"},"priority":{"name":"High"},"issuetype":{"name":"Story"}}},
	{"id":"10002","key":"ENG-2","fields":{"summary":"Second","status":{"id":"99","name":"Custom"}}}
]}`

const boardsBody = `{"maxResults":50,"startAt":0,"total":1,"isLast":true,"values":[
	{"id":5,"name":"ENG board","type":"scrum","location":{"projectKey":"ENG"}}
]}`

// TestJiraBoardList_EmitsFiltersAndID covers `jira board list`: JSON shape, the
// project filter on the wire, and the `-o id` projection (board ids).
func TestJiraBoardList_EmitsFiltersAndID(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/agile/1.0/board", http.StatusOK, boardsBody)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "board", "list", "--project", "ENG")
	if code != exitOK {
		t.Fatalf("board list: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		Boards     []domain.Board `json:"boards"`
		NextCursor string         `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode boards: %v\n%s", err, out)
	}
	if len(res.Boards) != 1 || res.Boards[0].ID != 5 || res.Boards[0].ProjectKey != "ENG" {
		t.Fatalf("boards = %+v, want one id=5 project=ENG", res.Boards)
	}
	// List commands expose a pagination cursor (empty here: isLast=true).
	if res.NextCursor != "" {
		t.Errorf("next_cursor = %q, want \"\" (isLast)", res.NextCursor)
	}
	// The project filter goes out as projectKeyOrId (the DC param name).
	var saw bool
	for _, r := range js.requests() {
		if r.method == http.MethodGet && strings.HasPrefix(r.path, "/rest/agile/1.0/board") && strings.Contains(r.query, "projectKeyOrId=ENG") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected GET /rest/agile/1.0/board?...projectKeyOrId=ENG, got %+v", js.requests())
	}

	// -o id prints the board ids one per line.
	idOut, code := runCLI(t, jiraEnv(js.srv), "jira", "board", "list", "-o", "id")
	if code != exitOK {
		t.Fatalf("board list -o id: exit %d, want 0", code)
	}
	if strings.TrimSpace(idOut) != "5" {
		t.Errorf("board list -o id = %q, want \"5\"", idOut)
	}
}

func TestJiraBoardConfigAndKanbanViewNeverCallSprintOrBacklog(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/agile/1.0/board/5/configuration", http.StatusOK, kanbanConfigBody)
	js.route(http.MethodGet, "/rest/agile/1.0/board/5/issue", http.StatusOK, boardIssuesBody)

	configOut, code := runCLI(t, jiraEnv(js.srv), "jira", "board", "config", "5")
	if code != exitOK || !strings.Contains(configOut, `"filter_id": "42"`) || !strings.Contains(configOut, `"status_ids"`) {
		t.Fatalf("board config exit=%d output=%q", code, configOut)
	}
	viewOut, code := runCLI(t, jiraEnv(js.srv), "jira", "board", "view", "5", "-o", "text")
	if code != exitOK {
		t.Fatalf("board view exit=%d output=%q", code, viewOut)
	}
	for _, want := range []string{"# Jira issues", "Source: board `5`", "| 0 | ENG-1 |", "| To Do |", "| Unmapped |"} {
		if !strings.Contains(viewOut, want) {
			t.Fatalf("view missing %q:\n%s", want, viewOut)
		}
	}
	for _, request := range js.requests() {
		if strings.Contains(request.path, "/sprint") || strings.Contains(request.path, "/backlog") {
			t.Fatalf("Kanban view called incompatible endpoint: %+v", request)
		}
	}
}

func TestJiraBoardKanbanBacklogRefusesBeforeBacklogEndpoint(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/agile/1.0/board/5/configuration", http.StatusOK, kanbanConfigBody)

	_, code := runCLI(t, jiraEnv(js.srv), "jira", "board", "backlog", "5")
	if code != exitUsage {
		t.Fatalf("Kanban backlog exit=%d, want usage", code)
	}
	for _, request := range js.requests() {
		if strings.Contains(request.path, "/backlog") {
			t.Fatalf("called Kanban backlog endpoint: %+v", request)
		}
	}
}

func TestJiraBoardExportCSVNeutralizesFormula(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/agile/1.0/board/5/configuration", http.StatusOK, kanbanConfigBody)
	js.route(http.MethodGet, "/rest/agile/1.0/board/5/issue", http.StatusOK, boardIssuesBody)
	outPath := filepath.Join(t.TempDir(), "board.csv")

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "board", "export", "5", "--scope", "board", "--format", "csv", "--out", outPath)
	if code != exitOK {
		t.Fatalf("board export exit=%d output=%q", code, out)
	}
	data, err := os.ReadFile(outPath)
	if err != nil || !strings.Contains(string(data), "'=Formula") || !strings.Contains(string(data), "Unmapped") {
		t.Fatalf("board CSV=%q err=%v", data, err)
	}
}

// TestJiraBoardGet_BadIDExit2 rejects a non-numeric board id before any request.
func TestJiraBoardGet_BadIDExit2(t *testing.T) {
	js := newJiraServer(t)
	_, code := runCLI(t, jiraEnv(js.srv), "jira", "board", "get", "not-a-number")
	if code != exitUsage {
		t.Fatalf("board get with bad id: exit %d, want %d", code, exitUsage)
	}
	if n := len(js.requests()); n != 0 {
		t.Errorf("bad id must not contact the server, got %d requests", n)
	}
}

// TestJiraSprintList_RequiresBoard fails closed without --board, contacting no
// server.
func TestJiraSprintList_RequiresBoard(t *testing.T) {
	js := newJiraServer(t)
	_, code := runCLI(t, jiraEnv(js.srv), "jira", "sprint", "list")
	if code != exitUsage {
		t.Fatalf("sprint list without --board: exit %d, want %d", code, exitUsage)
	}
	if n := len(js.requests()); n != 0 {
		t.Errorf("missing --board must not contact the server, got %d requests", n)
	}
}

// TestJiraBoardList_CoreInstance404Exit4 proves the feature's "optional
// capability" premise: on a Jira Core / Service-Management-only instance the
// Agile endpoints 404, which must surface as exit 4 (not a generic error).
func TestJiraBoardList_CoreInstance404Exit4(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/agile/1.0/board", http.StatusNotFound,
		`{"errorMessages":["No Agile API on this instance"]}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "board", "list")
	if code != exitNotFound {
		t.Fatalf("Agile 404: exit %d, want %d (stdout=%q)", code, exitNotFound, out)
	}
	if out != "" {
		t.Errorf("Agile 404: stdout = %q, want empty (errors go to stderr)", out)
	}
}

const sprintsBody = `{"maxResults":50,"startAt":0,"total":1,"isLast":true,"values":[
	{"id":7,"state":"active","name":"Sprint 3","goal":"ship","originBoardId":5}
]}`

// A zero/garbage --board must be rejected before any request, consistent with
// how positional ids are pre-validated by atoiArg.
func TestJiraSprintList_RejectsZeroBoard(t *testing.T) {
	js := newJiraServer(t)
	_, code := runCLI(t, jiraEnv(js.srv), "jira", "sprint", "list", "--board", "0")
	if code != exitUsage {
		t.Fatalf("sprint list --board 0: exit %d, want %d", code, exitUsage)
	}
	if n := len(js.requests()); n != 0 {
		t.Errorf("--board 0 must not contact the server, got %d requests", n)
	}
}

func TestJiraSprintCurrent_RejectsZeroBoard(t *testing.T) {
	js := newJiraServer(t)
	_, code := runCLI(t, jiraEnv(js.srv), "jira", "sprint", "current", "--board", "0")
	if code != exitUsage {
		t.Fatalf("sprint current --board 0: exit %d, want %d", code, exitUsage)
	}
	if n := len(js.requests()); n != 0 {
		t.Errorf("--board 0 must not contact the server, got %d requests", n)
	}
}

// No active sprint on the board is a not-found condition (exit 4) at the CLI
// boundary, not a silent empty result.
func TestJiraSprintCurrent_NoneExit4(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/agile/1.0/board/5/sprint", http.StatusOK, `{"values":[]}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "sprint", "current", "--board", "5")
	if code != exitNotFound {
		t.Fatalf("sprint current (none active): exit %d, want %d (stdout=%q)", code, exitNotFound, out)
	}
	if out != "" {
		t.Errorf("sprint current (none active): stdout = %q, want empty", out)
	}
}

func TestJiraSprintList_EmitsStateAndID(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/agile/1.0/board/5/sprint", http.StatusOK, sprintsBody)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "sprint", "list", "--board", "5", "--state", "active")
	if code != exitOK {
		t.Fatalf("sprint list: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		Sprints    []domain.Sprint `json:"sprints"`
		NextCursor string          `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode sprints: %v\n%s", err, out)
	}
	if len(res.Sprints) != 1 || res.Sprints[0].ID != 7 || res.Sprints[0].State != "active" {
		t.Fatalf("sprints = %+v, want one id=7 active", res.Sprints)
	}
	if res.NextCursor != "" {
		t.Errorf("next_cursor = %q, want \"\" (isLast)", res.NextCursor)
	}
	// The state filter reaches the wire.
	var saw bool
	for _, r := range js.requests() {
		if strings.Contains(r.query, "state=active") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected state=active on the wire, got %+v", js.requests())
	}
}

func TestJiraSprintCurrent_Emits(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/agile/1.0/board/5/sprint", http.StatusOK, sprintsBody)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "sprint", "current", "--board", "5")
	if code != exitOK {
		t.Fatalf("sprint current: exit %d, want 0 (stdout=%q)", code, out)
	}
	var s domain.Sprint
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("decode sprint: %v\n%s", err, out)
	}
	if s.ID != 7 || s.State != "active" {
		t.Errorf("current sprint = %+v, want id=7 active", s)
	}
}

func TestJiraSprintIssues_EmitsKeys(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/agile/1.0/sprint/7/issue", http.StatusOK,
		`{"startAt":0,"maxResults":50,"total":1,"issues":[{"key":"ENG-1","fields":{"summary":"x","status":{"name":"Open"}}}]}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "sprint", "issues", "7", "-o", "id")
	if code != exitOK {
		t.Fatalf("sprint issues -o id: exit %d, want 0 (stdout=%q)", code, out)
	}
	if strings.TrimSpace(out) != "ENG-1" {
		t.Errorf("sprint issues -o id = %q, want \"ENG-1\"", out)
	}
}

func TestJiraSprintAdd_WiresPost(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodPost, "/rest/agile/1.0/sprint/7/issue", http.StatusNoContent, ``)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "sprint", "add", "7", "ENG-1", "ENG-2")
	if code != exitOK {
		t.Fatalf("sprint add: exit %d, want 0 (stdout=%q)", code, out)
	}
	writes := js.writeReqsTo("/rest/agile/1.0/sprint/7/issue")
	if len(writes) != 1 {
		t.Fatalf("expected 1 POST, got %d: %+v", len(writes), writes)
	}
	var payload struct {
		Issues []string `json:"issues"`
	}
	if err := json.Unmarshal([]byte(writes[0].body), &payload); err != nil {
		t.Fatalf("decode body %q: %v", writes[0].body, err)
	}
	if len(payload.Issues) != 2 || payload.Issues[0] != "ENG-1" || payload.Issues[1] != "ENG-2" {
		t.Errorf("issues = %v, want [ENG-1 ENG-2]", payload.Issues)
	}
}

func TestJiraSprintRemove_WiresBacklog(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodPost, "/rest/agile/1.0/backlog/issue", http.StatusNoContent, ``)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "sprint", "remove", "ENG-9")
	if code != exitOK {
		t.Fatalf("sprint remove: exit %d, want 0 (stdout=%q)", code, out)
	}
	writes := js.writeReqsTo("/rest/agile/1.0/backlog/issue")
	if len(writes) != 1 {
		t.Fatalf("expected 1 POST to backlog, got %d: %+v", len(writes), writes)
	}
}

// TestJiraBoardGet_Golden / TestJiraSprintGet_Golden pin the single-object JSON
// shapes (fully deterministic — no host data).
func TestJiraBoardGet_Golden(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/agile/1.0/board/5", http.StatusOK,
		`{"id":5,"name":"ENG board","type":"scrum","location":{"projectKey":"ENG"}}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "board", "get", "5")
	if code != exitOK {
		t.Fatalf("board get: exit %d, want 0 (stdout=%q)", code, out)
	}
	assertGolden(t, "jira_board_get.json", []byte(out))
}

func TestJiraSprintGet_Golden(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/agile/1.0/sprint/7", http.StatusOK,
		`{"id":7,"state":"closed","name":"Sprint 2","goal":"done","originBoardId":5}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "sprint", "get", "7")
	if code != exitOK {
		t.Fatalf("sprint get: exit %d, want 0 (stdout=%q)", code, out)
	}
	assertGolden(t, "jira_sprint_get.json", []byte(out))
}
