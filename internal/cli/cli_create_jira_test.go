package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

// --- stateful Jira server (method+path routing, body capture) ---
//
// jsonServer is path-agnostic and replies with one canned body, so it cannot
// capture a POST/PUT body or vary the reply per path. jiraServer routes by
// r.Method + r.URL.Path against the real Jira REST v2 endpoints and records
// every request so a test can assert what went out on the wire (notably the
// {"fields": {...}} payload a create/update sends).
//
// Routes that matter here:
//
//	POST /rest/api/2/issue                       → create  (replies created key)
//	PUT  /rest/api/2/issue/<key>                 → update  (replies 204)
//	GET  /rest/api/2/issue/<key>                 → issue JSON (pull/get fetch)
//	GET  /rest/api/2/issue/<key>/transitions     → transitions list
//	GET  /rest/api/2/search                       → search results (pull driver)
//	GET  /rest/api/2/field                         → field defs
//	GET  /rest/api/2/issueLinkType                 → link types
//
// Anything else falls through to a 200 with `dflt`.
type jiraServer struct {
	srv *httptest.Server

	mu sync.Mutex
	// routes maps "<METHOD> <PATH-PREFIX>" → canned reply. The longest matching
	// prefix wins so "/rest/api/2/issue/X/transitions" can shadow
	// "/rest/api/2/issue/".
	routes map[string]cannedResp
	dflt   cannedResp
	reqs   []capturedReq
}

func newJiraServer(t *testing.T) *jiraServer {
	t.Helper()
	js := &jiraServer{
		routes: map[string]cannedResp{},
		dflt:   cannedResp{status: http.StatusOK, body: `{}`},
	}
	js.srv = httptest.NewServer(http.HandlerFunc(js.handle))
	t.Cleanup(js.srv.Close)
	return js
}

// route registers a canned reply for requests whose method matches and whose
// path begins with prefix. Later, longer prefixes take precedence.
func (js *jiraServer) route(method, prefix string, status int, body string) *jiraServer {
	js.mu.Lock()
	defer js.mu.Unlock()
	js.routes[method+" "+prefix] = cannedResp{status: status, body: body}
	return js
}

func (js *jiraServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := readAll(r)
	js.mu.Lock()
	js.reqs = append(js.reqs, capturedReq{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: string(body)})
	resp := js.dflt
	bestLen := -1
	for key, cr := range js.routes {
		method, prefix, ok := strings.Cut(key, " ")
		if !ok || method != r.Method || !strings.HasPrefix(r.URL.Path, prefix) {
			continue
		}
		if len(prefix) > bestLen {
			bestLen = len(prefix)
			resp = cr
		}
	}
	js.mu.Unlock()
	writeJSON(w, resp.status, resp.body)
}

func (js *jiraServer) requests() []capturedReq {
	js.mu.Lock()
	defer js.mu.Unlock()
	out := make([]capturedReq, len(js.reqs))
	copy(out, js.reqs)
	return out
}

// writeReqsTo returns the captured POST/PUT requests whose path has the given
// prefix.
func (js *jiraServer) writeReqsTo(prefix string) []capturedReq {
	var out []capturedReq
	for _, r := range js.requests() {
		if (r.method == http.MethodPost || r.method == http.MethodPut) && strings.HasPrefix(r.path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

// jiraFields extracts the {"fields": {...}} object from a captured create/update
// body.
func jiraFields(t *testing.T, body string) map[string]any {
	t.Helper()
	var p struct {
		Fields map[string]any `json:"fields"`
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("decode jira body %q: %v", body, err)
	}
	return p.Fields
}

// --- 1. conf page create: the CSF validation gate ---

const createCSF = "<p>Brand <strong>new</strong> page</p>"

// TestConfPageCreate_ValidVerbatim covers the happy path: a well-formed CSF body
// is POSTed verbatim (no conversion) to /rest/api/content, and the emitted JSON
// carries the created page's id/title/version.
func TestConfPageCreate_ValidVerbatim(t *testing.T) {
	cs := newConfServer(t)
	// POST /rest/api/content replies with the created page (id 99, v1).
	cs.writes = []cannedResp{{status: http.StatusOK, body: pageJSON("99", "Brand New", 1, createCSF)}}

	dir := t.TempDir()
	csfPath := filepath.Join(dir, "body.csf")
	if err := os.WriteFile(csfPath, []byte(createCSF), 0o644); err != nil {
		t.Fatalf("write csf: %v", err)
	}

	out, code := runCLI(t, confEnv(cs.srv),
		"conf", "page", "create", "--space", "ENG", "--title", "Brand New", "--from-file", csfPath)
	if code != exitOK {
		t.Fatalf("conf page create: exit %d, want 0 (stdout=%q)", code, out)
	}

	// Output carries the created id/title/version.
	var res struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode create result: %v\n%s", err, out)
	}
	if res.ID != "99" || res.Title != "Brand New" || res.Version != 1 {
		t.Fatalf("create result = %+v, want id=99 title=\"Brand New\" version=1", res)
	}
	// The output is host-independent (no _links.webui in the canned response, so
	// URL is empty) — lock the full JSON shape with a golden.
	assertGolden(t, "conf_page_create.json", []byte(out))

	// Exactly one POST reached the server, carrying the verbatim CSF.
	writes := cs.writeReqs()
	if len(writes) != 1 {
		t.Fatalf("expected 1 write, got %d: %+v", len(writes), writes)
	}
	if writes[0].method != http.MethodPost {
		t.Errorf("create method = %s, want POST", writes[0].method)
	}
	if writes[0].path != "/rest/api/content" {
		t.Errorf("create path = %s, want /rest/api/content", writes[0].path)
	}
	if got := putStorageValue(t, writes[0].body); got != createCSF {
		t.Fatalf("POST storage value = %q, want %q (verbatim CSF)", got, createCSF)
	}
	// The space key the CLI sends must match --space.
	var payload struct {
		Space struct {
			Key string `json:"key"`
		} `json:"space"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(writes[0].body), &payload); err != nil {
		t.Fatalf("decode POST body: %v", err)
	}
	if payload.Space.Key != "ENG" || payload.Title != "Brand New" {
		t.Errorf("POST space/title = %q/%q, want ENG/Brand New", payload.Space.Key, payload.Title)
	}
}

// TestConfPageCreate_InvalidCSFGate locks the create-time validation gate: an
// invalid CSF (unbalanced tags) must fail BEFORE any network write, mirroring
// the push gate. The stateful server must record ZERO POST, and the output must
// report the problems with a non-zero exit (usage error → exit 2).
func TestConfPageCreate_InvalidCSFGate(t *testing.T) {
	cs := newConfServer(t)

	dir := t.TempDir()
	csfPath := filepath.Join(dir, "bad.csf")
	if err := os.WriteFile(csfPath, []byte("<p>unbalanced <strong>oops</p>"), 0o644); err != nil {
		t.Fatalf("write invalid csf: %v", err)
	}

	out, code := runCLI(t, confEnv(cs.srv),
		"conf", "page", "create", "--space", "ENG", "--title", "Oops", "--from-file", csfPath)
	if code != exitUsage {
		t.Fatalf("invalid CSF create: exit %d, want %d (stdout=%q)", code, exitUsage, out)
	}

	// The headline assertion: the validation gate ran before any HTTP write.
	if writes := cs.writeReqs(); len(writes) != 0 {
		t.Fatalf("validation gate breached: %d write(s) sent before validation: %+v", len(writes), writes)
	}
	// And not a single request of any kind should have reached the server, since
	// the service is never even constructed past validation.
	if reqs := cs.requests(); len(reqs) != 0 {
		t.Fatalf("expected zero requests on invalid CSF, got %d: %+v", len(reqs), reqs)
	}

	// The problems are emitted on stdout before the usage error.
	var res struct {
		Problems []csfProblem `json:"problems"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode create problems: %v\n%s", err, out)
	}
	if len(res.Problems) == 0 {
		t.Errorf("expected problems to be reported for invalid CSF, got %s", out)
	}
}

// csfProblem mirrors the JSON shape of csf.Problem for decoding the emitted
// "problems" array without importing the concrete type's field layout.
type csfProblem struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// TestConfPageCreate_FromStdin covers the `--from-file -` path: the CSF body is
// read from os.Stdin. We redirect os.Stdin to a pipe for the duration of the
// call (readBody reads os.Stdin directly, not the cobra command's reader).
func TestConfPageCreate_FromStdin(t *testing.T) {
	cs := newConfServer(t)
	cs.writes = []cannedResp{{status: http.StatusOK, body: pageJSON("100", "Stdin Page", 1, createCSF)}}

	withStdin(t, createCSF, func() {
		out, code := runCLI(t, confEnv(cs.srv),
			"conf", "page", "create", "--space", "ENG", "--title", "Stdin Page", "--from-file", "-")
		if code != exitOK {
			t.Fatalf("conf page create (stdin): exit %d, want 0 (stdout=%q)", code, out)
		}
		if !strings.Contains(out, `"id": "100"`) {
			t.Errorf("create output = %q, want id 100", out)
		}
	})

	writes := cs.writeReqs()
	if len(writes) != 1 {
		t.Fatalf("expected 1 write, got %d", len(writes))
	}
	if got := putStorageValue(t, writes[0].body); got != createCSF {
		t.Errorf("POST storage value = %q, want %q (verbatim CSF from stdin)", got, createCSF)
	}
}

// withStdin redirects os.Stdin to a pipe carrying content for the duration of
// fn, restoring it afterward.
func withStdin(t *testing.T, content string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig })
	go func() {
		_, _ = w.WriteString(content)
		_ = w.Close()
	}()
	fn()
	_ = r.Close()
}

// --- 2. jira issue create ---

// TestJiraIssueCreate_WireFields covers the Jira create write path: a POST to
// /rest/api/2/issue must carry project/issuetype/summary/description in the
// {"fields": {...}} payload, and the emitted issue must echo the created key.
func TestJiraIssueCreate_WireFields(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodPost, "/rest/api/2/issue", http.StatusOK, `{"key":"ENG-7"}`)

	dir := t.TempDir()
	descPath := filepath.Join(dir, "desc.wiki")
	const desc = "h1. Title\n\nSome *wiki* body."
	if err := os.WriteFile(descPath, []byte(desc), 0o644); err != nil {
		t.Fatalf("write desc: %v", err)
	}

	out, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "create",
		"--project", "ENG", "--type", "Bug", "--summary", "Fix the thing",
		"--from-file", descPath)
	if code != exitOK {
		t.Fatalf("jira issue create: exit %d, want 0 (stdout=%q)", code, out)
	}

	var is domain.Issue
	if err := json.Unmarshal([]byte(out), &is); err != nil {
		t.Fatalf("decode issue: %v\n%s", err, out)
	}
	if is.Key != "ENG-7" {
		t.Errorf("created key = %q, want ENG-7", is.Key)
	}
	// The create response echoes only the inputs (Key/Summary/Project/Type/Body),
	// all deterministic — lock the JSON shape with a golden.
	assertGolden(t, "jira_issue_create.json", []byte(out))

	writes := js.writeReqsTo("/rest/api/2/issue")
	if len(writes) != 1 {
		t.Fatalf("expected 1 POST to /rest/api/2/issue, got %d: %+v", len(writes), writes)
	}
	if writes[0].method != http.MethodPost || writes[0].path != "/rest/api/2/issue" {
		t.Errorf("create req = %s %s, want POST /rest/api/2/issue", writes[0].method, writes[0].path)
	}
	f := jiraFields(t, writes[0].body)
	if f["summary"] != "Fix the thing" {
		t.Errorf("summary = %v, want \"Fix the thing\"", f["summary"])
	}
	if f["description"] != desc {
		t.Errorf("description = %v, want verbatim wiki %q", f["description"], desc)
	}
	if pr, ok := f["project"].(map[string]any); !ok || pr["key"] != "ENG" {
		t.Errorf("project = %v, want {key: ENG}", f["project"])
	}
	if it, ok := f["issuetype"].(map[string]any); !ok || it["name"] != "Bug" {
		t.Errorf("issuetype = %v, want {name: Bug}", f["issuetype"])
	}
}

// --- 3. jira issue update ---

// TestJiraIssueUpdate_WireFields covers the Jira update write path: a PUT to
// /rest/api/2/issue/<key> must carry the intended summary/description/extra
// fields, and the command exits 0 with a "updated" status.
func TestJiraIssueUpdate_WireFields(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodPut, "/rest/api/2/issue/", http.StatusNoContent, ``)

	dir := t.TempDir()
	descPath := filepath.Join(dir, "desc.wiki")
	const desc = "Updated *body*."
	if err := os.WriteFile(descPath, []byte(desc), 0o644); err != nil {
		t.Fatalf("write desc: %v", err)
	}

	out, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "update", "ENG-7",
		"--summary", "New summary", "--from-file", descPath,
		"--field", "priority={\"name\":\"High\"}")
	if code != exitOK {
		t.Fatalf("jira issue update: exit %d, want 0 (stdout=%q)", code, out)
	}

	var res struct {
		Key    string `json:"key"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode update result: %v\n%s", err, out)
	}
	if res.Key != "ENG-7" || res.Status != "updated" {
		t.Errorf("update result = %+v, want key=ENG-7 status=updated", res)
	}

	writes := js.writeReqsTo("/rest/api/2/issue/")
	if len(writes) != 1 {
		t.Fatalf("expected 1 PUT, got %d: %+v", len(writes), writes)
	}
	if writes[0].method != http.MethodPut || writes[0].path != "/rest/api/2/issue/ENG-7" {
		t.Errorf("update req = %s %s, want PUT /rest/api/2/issue/ENG-7", writes[0].method, writes[0].path)
	}
	f := jiraFields(t, writes[0].body)
	if f["summary"] != "New summary" {
		t.Errorf("summary = %v, want \"New summary\"", f["summary"])
	}
	if f["description"] != desc {
		t.Errorf("description = %v, want %q", f["description"], desc)
	}
	// Extra --field with a JSON-object value is coerced to an object on the wire.
	if pr, ok := f["priority"].(map[string]any); !ok || pr["name"] != "High" {
		t.Errorf("priority = %v, want {name: High} (coerced object)", f["priority"])
	}
}

// TestJiraIssueUpdate_NotFoundExit4 proves the sentinel→exit mapping on the Jira
// write path: a 404 on the PUT maps to ErrNotFound (exit 4).
func TestJiraIssueUpdate_NotFoundExit4(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodPut, "/rest/api/2/issue/", http.StatusNotFound, `{"errorMessages":["no such issue"]}`)

	out, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "update", "ENG-404", "--summary", "x")
	if code != exitNotFound {
		t.Fatalf("update 404: exit %d, want %d (stdout=%q)", code, exitNotFound, out)
	}
	if out != "" {
		t.Errorf("update 404: stdout = %q, want empty (errors go to stderr)", out)
	}
}

// --- 4. jira pull ---

const pullWikiBody = "h1. Heading\n\nNative *wiki* body — unconverted."

// TestJiraPull_MirrorLayoutAndByteStable drives `jira pull`: the search
// projection carries the full requested fields (as a real backend does), no
// per-issue re-fetch happens (#65), and the mirror is laid out as
// <project>/<KEY>.md + <KEY>.json. The wiki body in the .md must be stored
// verbatim (no Markdown round-trip).
func TestJiraPull_MirrorLayoutAndByteStable(t *testing.T) {
	js := newJiraServer(t)
	// Search returns one hit with the full field projection; total=1 so there
	// is no next cursor. No per-issue route exists — a re-fetch would 404.
	searchBody, _ := json.Marshal(map[string]any{
		"issues": []map[string]any{{
			"id":  "1042",
			"key": "ENG-42",
			"fields": map[string]any{
				"summary":       "Pulled issue",
				"description":   pullWikiBody,
				"status":        map[string]any{"name": "Open"},
				"issuetype":     map[string]any{"name": "Story"},
				"project":       map[string]any{"key": "ENG"},
				"customfield_1": "team-a",
			},
		}},
		"startAt": 0, "maxResults": 50, "total": 1,
	})
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, string(searchBody))

	into := t.TempDir()
	out, code := runCLI(t, jiraEnv(js.srv), "jira", "pull", "--jql", "project=ENG", "--into", into, "--fields", "customfield_1")
	if code != exitOK {
		t.Fatalf("jira pull: exit %d, want 0 (stdout=%q)", code, out)
	}

	var res struct {
		Issues []app.JiraPulled `json:"issues"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode pull result: %v\n%s", err, out)
	}
	if len(res.Issues) != 1 || res.Issues[0].Key != "ENG-42" {
		t.Fatalf("pull result = %+v, want one issue ENG-42", res.Issues)
	}

	// Mirror layout: <into>/ENG/ENG-42.wiki (verbatim substrate), ENG-42.md
	// (rendered view), and ENG-42.json.
	// The native wiki body is stored verbatim in the .wiki substrate — byte-for
	// -byte, no conversion (the editable source of truth, like .csf).
	wikiPath := filepath.Join(into, "ENG", "ENG-42.wiki")
	wb, err := os.ReadFile(wikiPath)
	if err != nil {
		t.Fatalf("read pulled .wiki: %v", err)
	}
	if string(wb) != pullWikiBody {
		t.Errorf("pulled .wiki not byte-identical to the body\n got: %q\nwant: %q", wb, pullWikiBody)
	}
	// The .md is a rendered read view: the raw wiki markup is converted and the
	// verbatim wiki no longer appears there.
	mdPath := filepath.Join(into, "ENG", "ENG-42.md")
	mdb, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read pulled .md: %v", err)
	}
	if !strings.Contains(string(mdb), "# Heading") || strings.Contains(string(mdb), "h1. Heading") {
		t.Errorf("pulled .md is not a rendered view\n--- md ---\n%s", mdb)
	}
	// The reported path is relative to `into`.
	if res.Issues[0].Path != filepath.Join("ENG", "ENG-42.md") {
		t.Errorf("reported path = %q, want %q", res.Issues[0].Path, filepath.Join("ENG", "ENG-42.md"))
	}

	jsonPath := filepath.Join(into, "ENG", "ENG-42.json")
	jb, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("expected sidecar .json at %s: %v", jsonPath, err)
	}
	var snap app.JiraIssueSnapshot
	if err := json.Unmarshal(jb, &snap); err != nil {
		t.Fatalf("decode sidecar .json: %v\n%s", err, jb)
	}
	if snap.Key != "ENG-42" || snap.ID != "1042" || snap.Fields["customfield_1"] != "team-a" {
		t.Errorf("snapshot = %+v, want key/id/customfield_1", snap)
	}
	var sawCustomField bool
	for _, r := range js.requests() {
		if r.method == http.MethodGet && r.path == "/rest/api/2/search" && strings.Contains(r.query, "customfield_1") {
			sawCustomField = true
		}
		// The search projection suffices; a per-issue re-fetch would double the
		// HTTP round trips (#65).
		if strings.HasPrefix(r.path, "/rest/api/2/issue/") {
			t.Errorf("pull made a per-issue request %s %s, want none", r.method, r.path)
		}
	}
	if !sawCustomField {
		t.Errorf("expected pull --fields customfield_1 on the search request, got %+v", js.requests())
	}
}

// --- 5. opportunistic breadth on thin emit-only paths ---

// TestJiraTransitions_EmitAndPath asserts the transitions list is emitted and
// the adapter GETs the right endpoint.
func TestJiraTransitions_EmitAndPath(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/", http.StatusOK,
		`{"transitions":[{"id":"11","name":"Start Progress","to":{"name":"In Progress"}}]}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "transitions", "--key", "ENG-1")
	if code != exitOK {
		t.Fatalf("jira transitions: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		Transitions []domain.TransitionDef `json:"transitions"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode transitions: %v\n%s", err, out)
	}
	if len(res.Transitions) != 1 || res.Transitions[0].Name != "Start Progress" || res.Transitions[0].To != "In Progress" {
		t.Errorf("transitions = %+v, want one Start Progress→In Progress", res.Transitions)
	}
	// The adapter GETs /rest/api/2/issue/<key>/transitions.
	var saw bool
	for _, r := range js.requests() {
		if r.method == http.MethodGet && r.path == "/rest/api/2/issue/ENG-1/transitions" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected GET /rest/api/2/issue/ENG-1/transitions, got %+v", js.requests())
	}
}

// TestJiraFields_Emit asserts `jira fields` emits the field defs from
// /rest/api/2/field.
func TestJiraFields_Emit(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/field", http.StatusOK,
		`[{"id":"summary","name":"Summary","custom":false,"schema":{"type":"string"}},{"id":"customfield_1","name":"Epic Link","custom":true,"schema":{"type":"any"}}]`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "fields")
	if code != exitOK {
		t.Fatalf("jira fields: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		Fields []domain.FieldDef `json:"fields"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode fields: %v\n%s", err, out)
	}
	if len(res.Fields) != 2 || res.Fields[0].ID != "summary" || res.Fields[1].Name != "Epic Link" {
		t.Errorf("fields = %+v, want summary + Epic Link", res.Fields)
	}
}

func TestJiraFields_Filters(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/field", http.StatusOK,
		`[{"id":"summary","name":"Summary","custom":false,"schema":{"type":"string"}},{"id":"customfield_1","name":"Epic Link","custom":true,"schema":{"type":"any"}},{"id":"customfield_2","name":"Team","custom":true,"schema":{"type":"string"}}]`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "fields", "--name-like", "epic")
	if code != exitOK {
		t.Fatalf("jira fields --name-like: exit %d, want 0 (stdout=%q)", code, out)
	}
	var byName struct {
		Fields []domain.FieldDef `json:"fields"`
	}
	if err := json.Unmarshal([]byte(out), &byName); err != nil {
		t.Fatalf("decode fields: %v\n%s", err, out)
	}
	if len(byName.Fields) != 1 || byName.Fields[0].ID != "customfield_1" {
		t.Fatalf("name-like fields = %+v, want only customfield_1", byName.Fields)
	}

	out, code = runCLI(t, jiraEnv(js.srv), "jira", "fields", "--id", "customfield_2")
	if code != exitOK {
		t.Fatalf("jira fields --id: exit %d, want 0 (stdout=%q)", code, out)
	}
	var byID struct {
		Fields []domain.FieldDef `json:"fields"`
	}
	if err := json.Unmarshal([]byte(out), &byID); err != nil {
		t.Fatalf("decode fields by id: %v\n%s", err, out)
	}
	if len(byID.Fields) != 1 || byID.Fields[0].Name != "Team" {
		t.Fatalf("id fields = %+v, want only Team", byID.Fields)
	}

	out, code = runCLI(t, jiraEnv(js.srv), "jira", "fields", "--custom", "true", "--schema", "string", "--id-like", "customfield")
	if code != exitOK {
		t.Fatalf("jira fields custom/schema/id-like: exit %d, want 0 (stdout=%q)", code, out)
	}
	var composed struct {
		Fields []domain.FieldDef `json:"fields"`
	}
	if err := json.Unmarshal([]byte(out), &composed); err != nil {
		t.Fatalf("decode composed fields: %v\n%s", err, out)
	}
	if len(composed.Fields) != 1 || composed.Fields[0].ID != "customfield_2" {
		t.Fatalf("composed fields = %+v, want only customfield_2", composed.Fields)
	}

	_, code = runCLI(t, jiraEnv(js.srv), "jira", "fields", "--custom", "maybe")
	if code != exitUsage {
		t.Fatalf("jira fields --custom maybe exit = %d, want %d", code, exitUsage)
	}
}

// TestJiraLinkTypes_Emit asserts `jira link-types` emits the configured link
// type names from /rest/api/2/issueLinkType.
func TestJiraLinkTypes_Emit(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issueLinkType", http.StatusOK,
		`{"issueLinkTypes":[{"name":"Blocks"},{"name":"Relates"}]}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "link-types")
	if code != exitOK {
		t.Fatalf("jira link-types: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		LinkTypes []string `json:"link_types"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode link types: %v\n%s", err, out)
	}
	if len(res.LinkTypes) != 2 || res.LinkTypes[0] != "Blocks" || res.LinkTypes[1] != "Relates" {
		t.Errorf("link types = %v, want [Blocks Relates]", res.LinkTypes)
	}
}

// TestConfPageDelete_WireMethod covers `conf page delete`: a DELETE is sent to
// /rest/api/content/<id> and the command reports status "trashed".
func TestConfPageDelete_WireMethod(t *testing.T) {
	cs := newConfServer(t)

	out, code := runCLI(t, confEnv(cs.srv), "conf", "page", "delete", "--id", "12345")
	if code != exitOK {
		t.Fatalf("conf page delete: exit %d, want 0 (stdout=%q)", code, out)
	}
	if !strings.Contains(out, `"status": "trashed"`) {
		t.Errorf("delete output = %q, want status trashed", out)
	}
	var saw bool
	for _, r := range cs.requests() {
		if r.method == http.MethodDelete && r.path == "/rest/api/content/12345" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected DELETE /rest/api/content/12345, got %+v", cs.requests())
	}
}
