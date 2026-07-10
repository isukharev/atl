package jira

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

// ---------------------------------------------------------------------------
// mapIssue: drive the uncovered branches via pure-function calls (no server).
// ---------------------------------------------------------------------------

// TestMapIssueFullPopulation exercises every nested mapping at once: summary,
// description, status/issuetype/project nested objects, assignee/reporter
// display, labels array, inward+outward links, and comments.
func TestMapIssueFullPopulation(t *testing.T) {
	j := &Jira{}
	is := j.mapIssue(issueDTO{
		Key: "ABC-7",
		Fields: map[string]any{
			"summary":     "the summary",
			"description": "the body",
			"status":      map[string]any{"name": "In Progress"},
			"issuetype":   map[string]any{"name": "Bug"},
			"project":     map[string]any{"key": "ABC"},
			"assignee":    map[string]any{"displayName": "Alice A", "name": "alice"},
			"reporter":    map[string]any{"name": "bob"}, // no displayName → falls back to name
			"labels":      []any{"red", "green"},
			"issuelinks": []any{
				map[string]any{
					"type":        map[string]any{"inward": "is blocked by", "outward": "blocks", "name": "Blocks"},
					"inwardIssue": map[string]any{"key": "ABC-1"},
				},
				map[string]any{
					"type":         map[string]any{"name": "Relates"}, // no inward/outward → name fallback
					"outwardIssue": map[string]any{"key": "ABC-2"},
				},
			},
			"comment": map[string]any{
				"comments": []any{
					map[string]any{
						"id":      "1001",
						"author":  map[string]any{"displayName": "Carol C"},
						"created": "2024-01-02T03:04:05.000+0000",
						"body":    "first comment",
					},
				},
			},
		},
	})

	if is.Key != "ABC-7" {
		t.Errorf("Key = %q, want ABC-7", is.Key)
	}
	if is.Summary != "the summary" {
		t.Errorf("Summary = %q", is.Summary)
	}
	if is.Body != "the body" {
		t.Errorf("Body = %q", is.Body)
	}
	if is.Status != "In Progress" {
		t.Errorf("Status = %q, want In Progress", is.Status)
	}
	if is.Type != "Bug" {
		t.Errorf("Type = %q, want Bug", is.Type)
	}
	if is.Project != "ABC" {
		t.Errorf("Project = %q, want ABC", is.Project)
	}
	if is.Assignee != "Alice A" {
		t.Errorf("Assignee = %q, want Alice A (displayName)", is.Assignee)
	}
	if is.Reporter != "bob" {
		t.Errorf("Reporter = %q, want bob (name fallback)", is.Reporter)
	}
	if len(is.Labels) != 2 || is.Labels[0] != "red" || is.Labels[1] != "green" {
		t.Errorf("Labels = %v, want [red green]", is.Labels)
	}

	if len(is.Links) != 2 {
		t.Fatalf("Links = %v, want 2 links", is.Links)
	}
	// inward link picks the "inward" type label.
	if is.Links[0].Direction != "inward" || is.Links[0].Key != "ABC-1" || is.Links[0].Type != "is blocked by" {
		t.Errorf("inward link = %+v, want {is blocked by inward ABC-1}", is.Links[0])
	}
	// outward link with no outward label falls back to the type name.
	if is.Links[1].Direction != "outward" || is.Links[1].Key != "ABC-2" || is.Links[1].Type != "Relates" {
		t.Errorf("outward link = %+v, want {Relates outward ABC-2}", is.Links[1])
	}

	if len(is.Comments) != 1 {
		t.Fatalf("Comments = %v, want 1", is.Comments)
	}
	c := is.Comments[0]
	if c.ID != "1001" || c.Author != "Carol C" || c.Created == "" || c.Body != "first comment" {
		t.Errorf("comment = %+v", c)
	}
}

// TestMapIssueMissingAndNullFields drives the "absent / null / wrong type"
// branches: every nested helper must read as zero rather than panic.
func TestMapIssueMissingAndNullFields(t *testing.T) {
	j := &Jira{}
	is := j.mapIssue(issueDTO{
		Key: "EMPTY-1",
		Fields: map[string]any{
			// description absent entirely.
			"summary":   "only summary",
			"status":    nil,          // null, not a map
			"issuetype": "wrong-type", // string, not a map
			"project":   nil,
			"assignee":  nil,
			"reporter":  map[string]any{}, // empty map: no displayName, no name
			"labels":    "not-an-array",   // wrong type → skipped
			"issuelinks": []any{
				"not-a-map",                 // non-map link entry → safe, contributes nothing
				map[string]any{},            // map with neither inward nor outward issue
				map[string]any{"type": "x"}, // type is wrong type, no issues
			},
			"comment": map[string]any{
				"comments": []any{"not-a-map-comment"},
			},
		},
	})

	if is.Summary != "only summary" {
		t.Errorf("Summary = %q", is.Summary)
	}
	if is.Body != "" {
		t.Errorf("Body = %q, want empty (description absent)", is.Body)
	}
	if is.Status != "" {
		t.Errorf("Status = %q, want empty (null)", is.Status)
	}
	if is.Type != "" {
		t.Errorf("Type = %q, want empty (wrong type)", is.Type)
	}
	if is.Project != "" {
		t.Errorf("Project = %q, want empty", is.Project)
	}
	if is.Assignee != "" {
		t.Errorf("Assignee = %q, want empty", is.Assignee)
	}
	if is.Reporter != "" {
		t.Errorf("Reporter = %q, want empty (empty map)", is.Reporter)
	}
	if len(is.Labels) != 0 {
		t.Errorf("Labels = %v, want none (wrong type)", is.Labels)
	}
	if len(is.Links) != 0 {
		t.Errorf("Links = %v, want none", is.Links)
	}
	// One comment entry that is not a map decodes from a nil map → all-zero
	// Comment is appended (str(nil)=="").
	if len(is.Comments) != 1 {
		t.Fatalf("Comments = %v, want 1 (zero-value)", is.Comments)
	}
	if is.Comments[0].ID != "" || is.Comments[0].Body != "" {
		t.Errorf("zero comment = %+v, want all-empty", is.Comments[0])
	}
}

// TestMapIssueNonStringScalars verifies str()'s float64 and default branches
// via real JSON: a numeric summary and a numeric label round-trip to strings.
func TestMapIssueNonStringScalars(t *testing.T) {
	var fields map[string]any
	if err := json.Unmarshal([]byte(`{
		"summary": 42,
		"labels": [7, "tag"],
		"status": {"name": 99}
	}`), &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	j := &Jira{}
	is := j.mapIssue(issueDTO{Key: "N-1", Fields: fields})
	if is.Summary != "42" {
		t.Errorf("Summary = %q, want 42 (float64→string)", is.Summary)
	}
	if len(is.Labels) != 2 || is.Labels[0] != "7" || is.Labels[1] != "tag" {
		t.Errorf("Labels = %v, want [7 tag]", is.Labels)
	}
	if is.Status != "99" {
		t.Errorf("Status = %q, want 99", is.Status)
	}
}

// ---------------------------------------------------------------------------
// nestedDisplay / typeField: direct unit coverage of the remaining branches.
// ---------------------------------------------------------------------------

func TestNestedDisplay(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"non-map", "string", ""},
		{"displayName preferred", map[string]any{"displayName": "Dee", "name": "d"}, "Dee"},
		{"name fallback", map[string]any{"name": "ned"}, "ned"},
		{"empty displayName falls to name", map[string]any{"displayName": "", "name": "ned"}, "ned"},
		{"both missing", map[string]any{"foo": "bar"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nestedDisplay(tc.in); got != tc.want {
				t.Errorf("nestedDisplay(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTypeField(t *testing.T) {
	cases := []struct {
		name  string
		in    any
		field string
		want  any
	}{
		{"nil", nil, "inward", ""},
		{"non-map", 5, "inward", ""},
		{"field present", map[string]any{"inward": "is blocked by", "name": "Blocks"}, "inward", "is blocked by"},
		{"field empty falls to name", map[string]any{"inward": "", "name": "Blocks"}, "inward", "Blocks"},
		{"field absent falls to name", map[string]any{"name": "Relates"}, "outward", "Relates"},
		{"all empty", map[string]any{}, "outward", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := typeField(tc.in, tc.field); got != tc.want {
				t.Errorf("typeField(%v, %q) = %v, want %v", tc.in, tc.field, got, tc.want)
			}
		})
	}
}

// TestStrDefaultBranch covers str()'s default arm: a non-string/non-float/
// non-nil value (here a bool, surfaced as a label) is stringified via %v.
func TestStrDefaultBranch(t *testing.T) {
	j := &Jira{}
	is := j.mapIssue(issueDTO{
		Key:    "B-1",
		Fields: map[string]any{"labels": []any{true, false}},
	})
	if len(is.Labels) != 2 || is.Labels[0] != "true" || is.Labels[1] != "false" {
		t.Errorf("Labels = %v, want [true false] (bool via %%v)", is.Labels)
	}
}

func TestNestedNameAndKey(t *testing.T) {
	if got := nestedName(nil); got != "" {
		t.Errorf("nestedName(nil) = %q, want empty", got)
	}
	if got := nestedName("x"); got != "" {
		t.Errorf("nestedName(non-map) = %q, want empty", got)
	}
	if got := nestedKey(nil); got != "" {
		t.Errorf("nestedKey(nil) = %q, want empty", got)
	}
	if got := nestedKey(map[string]any{"key": "ABC"}); got != "ABC" {
		t.Errorf("nestedKey = %q, want ABC", got)
	}
}

// ---------------------------------------------------------------------------
// GetIssue: default field expansion, explicit fields, not-found mapping.
// ---------------------------------------------------------------------------

func TestGetIssueDefaultFields(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"key":"ABC-1","fields":{"summary":"hi","status":{"name":"Open"}}}`)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	is, err := j.GetIssue(context.Background(), "ABC-1", nil)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if is.Key != "ABC-1" || is.Summary != "hi" || is.Status != "Open" {
		t.Errorf("mapped issue = %+v", is)
	}
	if want := "/rest/api/2/issue/ABC-1?fields=" + query(defaultFields); gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

func TestGetIssueExplicitFields(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"key":"ABC-1","fields":{}}`)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if _, err := j.GetIssue(context.Background(), "ABC-1", []string{"summary", "status"}); err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if want := "/rest/api/2/issue/ABC-1?fields=" + query("summary,status"); gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

func TestGetIssuePreservesDynamicNumberPrecision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"key":"ABC-1","fields":{"customfield_1":{"large":9007199254740993}}}`)
	}))
	defer srv.Close()

	is, err := newTestJira(srv).GetIssue(context.Background(), "ABC-1", []string{"customfield_1"})
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	object, ok := is.Fields["customfield_1"].(map[string]any)
	if !ok || object["large"] != json.Number("9007199254740993") {
		t.Fatalf("large number lost precision: %#v", is.Fields["customfield_1"])
	}
}

func TestGetIssueUseNumberStillRejectsTrailingJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"key":"ABC-1","fields":{}} {}`)
	}))
	defer srv.Close()

	if _, err := newTestJira(srv).GetIssue(context.Background(), "ABC-1", []string{"summary"}); err == nil {
		t.Fatal("GetIssue should reject trailing JSON")
	}
}

func TestGetIssueEscapesKey(t *testing.T) {
	var gotRawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"key":"A B/C","fields":{}}`)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if _, err := j.GetIssue(context.Background(), "A B/C", []string{"summary"}); err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	// PathEscape encodes the space and slash so the key is one path segment.
	if want := "/rest/api/2/issue/A%20B%2FC"; gotRawPath != want {
		t.Errorf("escaped path = %q, want %q", gotRawPath, want)
	}
}

func TestGetIssueNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errorMessages":["does not exist"]}`, http.StatusNotFound)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	_, err := j.GetIssue(context.Background(), "NONE-1", nil)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want wrap of ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Search: JQL pagination cursoring across multiple pages.
// ---------------------------------------------------------------------------

// searchPage builds a canned search response page.
func searchPage(t *testing.T, startAt, maxResults, total int, keys ...string) string {
	t.Helper()
	type dto struct {
		Key    string         `json:"key"`
		Fields map[string]any `json:"fields"`
	}
	var resp struct {
		Issues     []dto `json:"issues"`
		StartAt    int   `json:"startAt"`
		MaxResults int   `json:"maxResults"`
		Total      int   `json:"total"`
	}
	resp.StartAt, resp.MaxResults, resp.Total = startAt, maxResults, total
	for _, k := range keys {
		resp.Issues = append(resp.Issues, dto{Key: k, Fields: map[string]any{"summary": k + " sum"}})
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestSearchPaginationLoop(t *testing.T) {
	// total=5, two pages of 2 then a final page of 1. The caller drives the loop
	// using the returned cursor; assert startAt cursoring and stop condition.
	type call struct {
		startAt    string
		maxResults string
	}
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		calls = append(calls, call{q.Get("startAt"), q.Get("maxResults")})
		if q.Get("jql") != "project = ABC" {
			t.Errorf("jql = %q", q.Get("jql"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch q.Get("startAt") {
		case "0":
			_, _ = io.WriteString(w, searchPage(t, 0, 2, 5, "ABC-1", "ABC-2"))
		case "2":
			_, _ = io.WriteString(w, searchPage(t, 2, 2, 5, "ABC-3", "ABC-4"))
		case "4":
			_, _ = io.WriteString(w, searchPage(t, 4, 2, 5, "ABC-5"))
		default:
			t.Errorf("unexpected startAt %q", q.Get("startAt"))
		}
	}))
	defer srv.Close()

	j := newTestJira(srv)
	var all []domain.Issue
	cursor := ""
	for i := 0; i < 10; i++ { // safety bound
		page, next, err := j.Search(context.Background(), "project = ABC", nil, 2, cursor)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		all = append(all, page...)
		if next == "" {
			break
		}
		cursor = next
	}

	if len(all) != 5 {
		t.Fatalf("aggregated %d issues, want 5: %+v", len(all), all)
	}
	for i, want := range []string{"ABC-1", "ABC-2", "ABC-3", "ABC-4", "ABC-5"} {
		if all[i].Key != want {
			t.Errorf("issue[%d] = %q, want %q", i, all[i].Key, want)
		}
	}
	wantCalls := []call{{"0", "2"}, {"2", "2"}, {"4", "2"}}
	if len(calls) != len(wantCalls) {
		t.Fatalf("made %d requests, want %d: %+v", len(calls), len(wantCalls), calls)
	}
	for i := range wantCalls {
		if calls[i] != wantCalls[i] {
			t.Errorf("call[%d] = %+v, want %+v", i, calls[i], wantCalls[i])
		}
	}
}

// TestSearchCursorStopsWhenComplete: a single page already covering total
// returns an empty next cursor.
func TestSearchCursorStopsWhenComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, searchPage(t, 0, 50, 2, "X-1", "X-2"))
	}))
	defer srv.Close()

	j := newTestJira(srv)
	page, next, err := j.Search(context.Background(), "x", nil, 0, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(page) != 2 {
		t.Errorf("got %d issues, want 2", len(page))
	}
	if next != "" {
		t.Errorf("next = %q, want empty (already complete)", next)
	}
}

// TestSearchEmptyResultNoCursor: zero issues but a non-zero total must NOT loop
// forever — the len(resp.Issues) > 0 guard returns an empty cursor.
func TestSearchEmptyResultNoCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, searchPage(t, 0, 50, 100)) // total 100, zero issues
	}))
	defer srv.Close()

	j := newTestJira(srv)
	page, next, err := j.Search(context.Background(), "x", nil, 50, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(page) != 0 {
		t.Errorf("got %d issues, want 0", len(page))
	}
	if next != "" {
		t.Errorf("next = %q, want empty (no progress guard)", next)
	}
}

// TestSearchClampsLimitAndDefaultFields verifies the maxResults clamp (limit<=0
// or >100 → 50) and the default search field set.
func TestSearchClampsLimitAndDefaultFields(t *testing.T) {
	const wantFields = "summary,status,issuetype,project,assignee,labels"
	cases := []struct {
		name    string
		limit   int
		wantMax string
	}{
		{"zero clamps to 50", 0, "50"},
		{"negative clamps to 50", -3, "50"},
		{"over 100 clamps to 50", 250, "50"},
		{"in-range kept", 25, "25"},
		{"exactly 100 kept", 100, "100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMax, gotFields string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMax = r.URL.Query().Get("maxResults")
				gotFields = r.URL.Query().Get("fields")
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, searchPage(t, 0, 50, 0))
			}))
			defer srv.Close()

			j := newTestJira(srv)
			if _, _, err := j.Search(context.Background(), "x", nil, tc.limit, ""); err != nil {
				t.Fatalf("Search: %v", err)
			}
			if gotMax != tc.wantMax {
				t.Errorf("maxResults = %q, want %q", gotMax, tc.wantMax)
			}
			if gotFields != wantFields {
				t.Errorf("fields = %q, want %q", gotFields, wantFields)
			}
		})
	}
}

func TestSearchExplicitFields(t *testing.T) {
	var gotFields string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFields = r.URL.Query().Get("fields")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, searchPage(t, 0, 50, 0))
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if _, _, err := j.Search(context.Background(), "x", []string{"summary", "key"}, 10, ""); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotFields != "summary,key" {
		t.Errorf("fields = %q, want summary,key", gotFields)
	}
}

func TestSearchErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad jql", http.StatusBadRequest)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	_, _, err := j.Search(context.Background(), "garbage(", nil, 10, "")
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("err = %v, want wrap of ErrUsage (400)", err)
	}
}

// ---------------------------------------------------------------------------
// Transition: POST body shape + comment update + error mapping.
// ---------------------------------------------------------------------------

const transitionsBody = `{"transitions":[
	{"id":"11","name":"Start Progress","to":{"name":"In Progress"}},
	{"id":"21","name":"Done","to":{"name":"Closed"}}
]}`

func TestTransitionPostsResolvedIDWithComment(t *testing.T) {
	var transPath string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/ABC-1/transitions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, transitionsBody)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/issue/ABC-1/transitions":
			transPath = r.URL.Path
			b, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(b, &body); err != nil {
				t.Fatalf("decode transition body %q: %v", b, err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	j := newTestJira(srv)
	// Match by the target status name ("Closed" → transition id 21).
	if err := j.Transition(context.Background(), "ABC-1", "Closed", "moving along", nil); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if transPath != "/rest/api/2/issue/ABC-1/transitions" {
		t.Errorf("POST path = %q", transPath)
	}
	tr, ok := body["transition"].(map[string]any)
	if !ok || tr["id"] != "21" {
		t.Fatalf("transition payload = %v, want id 21 (matched on To name)", body["transition"])
	}
	upd, ok := body["update"].(map[string]any)
	if !ok {
		t.Fatalf("update missing: %v", body)
	}
	comments, ok := upd["comment"].([]any)
	if !ok || len(comments) != 1 {
		t.Fatalf("update.comment = %v", upd["comment"])
	}
	add, ok := comments[0].(map[string]any)["add"].(map[string]any)
	if !ok || add["body"] != "moving along" {
		t.Errorf("comment add = %v, want body 'moving along'", comments[0])
	}
}

func TestTransitionMatchesByTransitionName(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, transitionsBody)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	// "Start Progress" is a transition NAME (not a to-status); case-insensitive.
	if err := j.Transition(context.Background(), "ABC-1", "start progress", "", nil); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	tr := body["transition"].(map[string]any)
	if tr["id"] != "11" {
		t.Errorf("transition id = %v, want 11", tr["id"])
	}
	if _, hasUpdate := body["update"]; hasUpdate {
		t.Errorf("update present with empty comment: %v", body)
	}
}

func TestTransitionUnknownTargetIsUsageError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			t.Errorf("should not POST when target unknown")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, transitionsBody)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	err := j.Transition(context.Background(), "ABC-1", "Nonexistent", "", nil)
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
	// Error message should list available transition names.
	if got := err.Error(); !contains(got, "Start Progress") || !contains(got, "Done") {
		t.Errorf("error %q should list available transitions", got)
	}
}

func TestTransitionPropagatesListError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	err := j.Transition(context.Background(), "ABC-1", "Done", "", nil)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden from the Transitions() lookup", err)
	}
}

// ---------------------------------------------------------------------------
// AddComment: request body, response mapping, error mapping.
// ---------------------------------------------------------------------------

func TestAddComment(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"9001","created":"2024-05-06T07:08:09.000+0000","author":{"displayName":"Zed Z"}}`)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	c, err := j.AddComment(context.Background(), "ABC-1", []byte("hello *world*"))
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if gotPath != "/rest/api/2/issue/ABC-1/comment" {
		t.Errorf("path = %q", gotPath)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("decode sent body %q: %v", gotBody, err)
	}
	if sent["body"] != "hello *world*" {
		t.Errorf("sent body = %q", sent["body"])
	}
	if c.ID != "9001" || c.Author != "Zed Z" || c.Created == "" {
		t.Errorf("returned comment = %+v", c)
	}
	if c.Body != "hello *world*" {
		t.Errorf("returned body = %q, want echo of input", c.Body)
	}
}

func TestAddCommentNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no issue", http.StatusNotFound)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	_, err := j.AddComment(context.Background(), "ABC-1", []byte("x"))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Link: payload (inward/outward orientation) + error mapping.
// ---------------------------------------------------------------------------

func TestLinkPayload(t *testing.T) {
	var gotPath string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatalf("decode link body %q: %v", b, err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if err := j.Link(context.Background(), "ABC-1", "ABC-2", "Blocks"); err != nil {
		t.Fatalf("Link: %v", err)
	}
	if gotPath != "/rest/api/2/issueLink" {
		t.Errorf("path = %q", gotPath)
	}
	typ, _ := body["type"].(map[string]any)
	if typ["name"] != "Blocks" {
		t.Errorf("type.name = %v, want Blocks", typ["name"])
	}
	// "from" is the outward issue, "to" is the inward issue.
	out, _ := body["outwardIssue"].(map[string]any)
	in, _ := body["inwardIssue"].(map[string]any)
	if out["key"] != "ABC-1" {
		t.Errorf("outwardIssue.key = %v, want ABC-1 (from)", out["key"])
	}
	if in["key"] != "ABC-2" {
		t.Errorf("inwardIssue.key = %v, want ABC-2 (to)", in["key"])
	}
}

func TestLinkForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	err := j.Link(context.Background(), "ABC-1", "ABC-2", "Blocks")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

// ---------------------------------------------------------------------------
// LinkEpic: resolves the "Epic Link" custom field, then PUTs {field: epic}.
// ---------------------------------------------------------------------------

const fieldsBody = `[
	{"id":"summary","name":"Summary","custom":false,"schema":{"type":"string"}},
	{"id":"customfield_10010","name":"Epic Link","custom":true,"schema":{"type":"any"}}
]`

func TestLinkEpicSetsResolvedField(t *testing.T) {
	var putPath string
	var fields map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/field":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, fieldsBody)
		case r.Method == http.MethodPut && r.URL.Path == "/rest/api/2/issue/ABC-2":
			putPath = r.URL.Path
			fields = readFields(t, r)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if err := j.LinkEpic(context.Background(), "ABC-2", "ABC-100"); err != nil {
		t.Fatalf("LinkEpic: %v", err)
	}
	if putPath != "/rest/api/2/issue/ABC-2" {
		t.Errorf("PUT path = %q", putPath)
	}
	if fields["customfield_10010"] != "ABC-100" {
		t.Errorf("epic field payload = %v, want customfield_10010=ABC-100", fields)
	}
}

func TestLinkEpicNoEpicFieldIsUsageError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			t.Errorf("should not PUT when Epic Link field absent")
		}
		w.Header().Set("Content-Type", "application/json")
		// Field list without an "Epic Link" field.
		_, _ = io.WriteString(w, `[{"id":"summary","name":"Summary","custom":false,"schema":{"type":"string"}}]`)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	err := j.LinkEpic(context.Background(), "ABC-2", "ABC-100")
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage (no Epic Link field)", err)
	}
}

func TestLinkEpicPropagatesFieldsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	err := j.LinkEpic(context.Background(), "ABC-2", "ABC-100")
	if !errors.Is(err, domain.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth from Fields() lookup", err)
	}
}

// ---------------------------------------------------------------------------
// meta.go: Fields, Transitions, LinkTypes, ListAttachments, DownloadAttachment.
// ---------------------------------------------------------------------------

func TestFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/field" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, fieldsBody)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	fields, err := j.Fields(context.Background())
	if err != nil {
		t.Fatalf("Fields: %v", err)
	}
	if len(fields) != 2 {
		t.Fatalf("got %d fields, want 2", len(fields))
	}
	if fields[0].ID != "summary" || fields[0].Name != "Summary" || fields[0].Custom || fields[0].Schema != "string" {
		t.Errorf("fields[0] = %+v", fields[0])
	}
	if fields[1].ID != "customfield_10010" || fields[1].Name != "Epic Link" || !fields[1].Custom || fields[1].Schema != "any" {
		t.Errorf("fields[1] = %+v (custom field)", fields[1])
	}
}

func TestFieldsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if _, err := j.Fields(context.Background()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

func TestTransitions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/issue/ABC-1/transitions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, transitionsBody)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	trs, err := j.Transitions(context.Background(), "ABC-1")
	if err != nil {
		t.Fatalf("Transitions: %v", err)
	}
	if len(trs) != 2 {
		t.Fatalf("got %d transitions, want 2", len(trs))
	}
	if trs[0].ID != "11" || trs[0].Name != "Start Progress" || trs[0].To != "In Progress" {
		t.Errorf("trs[0] = %+v", trs[0])
	}
	if trs[1].ID != "21" || trs[1].Name != "Done" || trs[1].To != "Closed" {
		t.Errorf("trs[1] = %+v", trs[1])
	}
}

func TestTransitionsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no issue", http.StatusNotFound)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if _, err := j.Transitions(context.Background(), "ABC-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestLinkTypes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/issueLinkType" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"issueLinkTypes":[{"name":"Blocks"},{"name":"Relates"},{"name":"Duplicate"}]}`)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	names, err := j.LinkTypes(context.Background())
	if err != nil {
		t.Fatalf("LinkTypes: %v", err)
	}
	if len(names) != 3 || names[0] != "Blocks" || names[1] != "Relates" || names[2] != "Duplicate" {
		t.Errorf("names = %v, want [Blocks Relates Duplicate]", names)
	}
}

func TestLinkTypesEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"issueLinkTypes":[]}`)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	names, err := j.LinkTypes(context.Background())
	if err != nil {
		t.Fatalf("LinkTypes: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("names = %v, want empty", names)
	}
}

func TestListAttachments(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/issue/ABC-1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"fields":{"attachment":[
			{"id":"42","filename":"diagram.png","mimeType":"image/png","size":1234,"content":"/secure/attachment/42/diagram.png"},
			{"id":"43","filename":"notes.txt","mimeType":"text/plain","size":7,"content":"/secure/attachment/43/notes.txt"}
		]}}`)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	atts, err := j.ListAttachments(context.Background(), "ABC-1")
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if gotQuery != "fields=attachment" {
		t.Errorf("query = %q, want fields=attachment", gotQuery)
	}
	if len(atts) != 2 {
		t.Fatalf("got %d attachments, want 2", len(atts))
	}
	if atts[0].ID != "42" || atts[0].Title != "diagram.png" || atts[0].MediaType != "image/png" || atts[0].FileSize != 1234 {
		t.Errorf("atts[0] = %+v", atts[0])
	}
	if atts[0].DownPath == "" {
		t.Errorf("atts[0].DownPath empty, want content URL")
	}
}

func TestListAttachmentsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"fields":{}}`)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	atts, err := j.ListAttachments(context.Background(), "ABC-1")
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(atts) != 0 {
		t.Errorf("atts = %v, want empty", atts)
	}
}

func TestListAttachmentsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no issue", http.StatusNotFound)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if _, err := j.ListAttachments(context.Background(), "ABC-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDownloadAttachmentByID(t *testing.T) {
	const payload = "PNGDATA\x00\x01"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/2/issue/ABC-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"fields":{"attachment":[
				{"id":"42","filename":"diagram.png","mimeType":"image/png","size":9,"content":"/secure/attachment/42/diagram.png"}
			]}}`)
		case "/secure/attachment/42/diagram.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = io.WriteString(w, payload)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	j := newTestJira(srv)
	rc, name, err := j.DownloadAttachment(context.Background(), "ABC-1", "42")
	if err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	data, rerr := io.ReadAll(rc)
	rc.Close()
	if rerr != nil {
		t.Fatalf("read stream: %v", rerr)
	}
	if string(data) != payload {
		t.Errorf("data = %q, want %q", data, payload)
	}
	if name != "diagram.png" {
		t.Errorf("name = %q, want diagram.png", name)
	}
}

func TestDownloadAttachmentByFilename(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/2/issue/ABC-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"fields":{"attachment":[
				{"id":"42","filename":"notes.txt","mimeType":"text/plain","size":3,"content":"/secure/attachment/42/notes.txt"}
			]}}`)
		case "/secure/attachment/42/notes.txt":
			_, _ = io.WriteString(w, "abc")
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	j := newTestJira(srv)
	// Match by Title (filename) rather than ID.
	rc, name, err := j.DownloadAttachment(context.Background(), "ABC-1", "notes.txt")
	if err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	data, rerr := io.ReadAll(rc)
	rc.Close()
	if rerr != nil {
		t.Fatalf("read stream: %v", rerr)
	}
	if string(data) != "abc" || name != "notes.txt" {
		t.Errorf("data=%q name=%q", data, name)
	}
}

func TestDownloadAttachmentUnknownIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"fields":{"attachment":[
			{"id":"42","filename":"a.txt","content":"/secure/attachment/42/a.txt"}
		]}}`)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	_, _, err := j.DownloadAttachment(context.Background(), "ABC-1", "999")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for unknown attachment id", err)
	}
}

func TestDownloadAttachmentListErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	_, _, err := j.DownloadAttachment(context.Background(), "ABC-1", "42")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden from the list step", err)
	}
}

// TestDownloadAttachmentReturnsRawServerFilename pins the *actual* adapter
// behavior: DownloadAttachment returns the server-supplied filename VERBATIM —
// it does NOT sanitize. A hostile filename containing path-traversal sequences
// is returned unchanged from the adapter; sanitization is the caller's job
// (internal/app/jira.go runs safepath.Base on it before writing to disk). This
// test documents that contract and then demonstrates that safepath.Base — the
// real sanitizer used downstream — neutralizes the traversal.
func TestDownloadAttachmentReturnsRawServerFilename(t *testing.T) {
	const hostile = "../../../etc/passwd"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/2/issue/ABC-1" {
			w.Header().Set("Content-Type", "application/json")
			// filename contains traversal; encode via json.Marshal to be safe.
			b, _ := json.Marshal(map[string]any{
				"fields": map[string]any{
					"attachment": []any{map[string]any{
						"id":       "42",
						"filename": hostile,
						"content":  "/secure/attachment/42/evil",
					}},
				},
			})
			_, _ = w.Write(b)
			return
		}
		_, _ = io.WriteString(w, "evil-bytes")
	}))
	defer srv.Close()

	j := newTestJira(srv)
	rc, name, err := j.DownloadAttachment(context.Background(), "ABC-1", "42")
	if err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	data, rerr := io.ReadAll(rc)
	rc.Close()
	if rerr != nil {
		t.Fatalf("read stream: %v", rerr)
	}
	if string(data) != "evil-bytes" {
		t.Errorf("data = %q", data)
	}
	// The adapter does NOT sanitize: it hands back the raw server filename.
	if name != hostile {
		t.Fatalf("adapter returned %q; expected the raw server filename %q (adapter does not sanitize)", name, hostile)
	}

	// Downstream, safepath.Base reduces it to a single safe component with no
	// separators and no traversal token — this is what the app layer applies.
	safe, ok := safepath.Base(name)
	if !ok {
		t.Fatalf("safepath.Base(%q) returned ok=false", name)
	}
	if safe != "passwd" {
		t.Errorf("safepath.Base(%q) = %q, want %q (traversal stripped)", name, safe, "passwd")
	}
	if contains(safe, "/") || contains(safe, "\\") || safe == ".." {
		t.Errorf("sanitized name %q still unsafe", safe)
	}
}

func TestUploadAttachmentMultipart(t *testing.T) {
	var gotPath, gotToken, gotFilename, gotData string
	var gotContentLength int64
	var gotTransferEncoding []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Atlassian-Token")
		gotContentLength = r.ContentLength
		gotTransferEncoding = append([]string(nil), r.TransferEncoding...)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		files := r.MultipartForm.File["file"]
		if len(files) != 1 {
			t.Fatalf("multipart file field count = %d, want 1", len(files))
		}
		gotFilename = files[0].Filename
		f, err := files[0].Open()
		if err != nil {
			t.Fatalf("open multipart file: %v", err)
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			t.Fatalf("read multipart file: %v", err)
		}
		gotData = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"id":"44","filename":"report.xlsx","mimeType":"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet","size":10,"content":"/secure/attachment/44/report.xlsx"}]`)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	att, err := j.UploadAttachment(context.Background(), "ABC-1", "report.xlsx", strings.NewReader("xlsx bytes"), int64(len("xlsx bytes")))
	if err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}
	if gotPath != "/rest/api/2/issue/ABC-1/attachments" {
		t.Fatalf("path = %q, want attachments endpoint", gotPath)
	}
	if gotToken != "no-check" {
		t.Fatalf("X-Atlassian-Token = %q, want no-check", gotToken)
	}
	if gotFilename != "report.xlsx" || gotData != "xlsx bytes" {
		t.Fatalf("multipart filename=%q data=%q", gotFilename, gotData)
	}
	if gotContentLength <= int64(len("xlsx bytes")) {
		t.Fatalf("ContentLength = %d, want multipart length greater than file bytes", gotContentLength)
	}
	if len(gotTransferEncoding) != 0 {
		t.Fatalf("TransferEncoding = %v, want no chunked encoding", gotTransferEncoding)
	}
	if att.ID != "44" || att.Title != "report.xlsx" || att.MediaType == "" || att.FileSize != 10 {
		t.Fatalf("attachment = %+v, want uploaded metadata", att)
	}
}

func TestUploadAttachmentRequestBuildErrorDoesNotDeadlock(t *testing.T) {
	j := New("http://[::1", "tok", "test")
	done := make(chan error, 1)
	go func() {
		_, err := j.UploadAttachment(context.Background(), "ABC-1", "report.xlsx", strings.NewReader(strings.Repeat("x", 1<<20)), 1<<20)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("UploadAttachment returned nil error for invalid base URL")
		}
	case <-time.After(time.Second):
		t.Fatal("UploadAttachment hung after request build failed before consuming the pipe")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// query is the canonical net/url query-escaping used to build expected paths.
func query(s string) string { return neturl.QueryEscape(s) }

// contains is a tiny substring helper (avoids pulling strings into assertions).
func contains(s, sub string) bool { return strings.Contains(s, sub) }

// New builds the Jira adapter — covers the public constructor and its
// base-URL trimming.
func TestNewTrimsTrailingSlash(t *testing.T) {
	j := New("https://jira.example.com/", "tok", "1.0.0")
	if j.base != "https://jira.example.com" {
		t.Errorf("base = %q, want trailing slash trimmed", j.base)
	}
	if j.c == nil {
		t.Errorf("client is nil")
	}
}
