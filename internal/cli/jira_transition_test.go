package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

type jiraTransitionCLIServer struct {
	t           *testing.T
	srv         *httptest.Server
	mu          sync.Mutex
	applied     bool
	issueReads  int
	transitions int
	posts       int
}

func newJiraTransitionCLIServer(t *testing.T) *jiraTransitionCLIServer {
	t.Helper()
	s := &jiraTransitionCLIServer{t: t}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *jiraTransitionCLIServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-1":
		s.issueReads++
		statusID, statusName, updated := "1", "Open", "2026-07-01T00:00:00.000+0000"
		if s.applied {
			statusID, statusName, updated = "5", "Done", "2026-07-02T00:00:00.000+0000"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "10001", "key": "PROJ-1",
			"fields": map[string]any{
				"status":            map[string]any{"id": statusID, "name": statusName},
				"updated":           updated,
				"customfield_10001": 3,
			},
		})
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/PROJ-1/transitions":
		s.transitions++
		transitions := []map[string]any{}
		if !s.applied {
			transitions = append(transitions, map[string]any{
				"id": "31", "name": "Finish", "to": map[string]any{"id": "5", "name": "Done"},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"transitions": transitions})
	case r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/issue/PROJ-1/transitions":
		s.posts++
		var payload struct {
			Transition map[string]string `json:"transition"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Transition["id"] != "31" {
			s.t.Errorf("transition payload=%+v err=%v", payload, err)
		}
		s.applied = true
		w.WriteHeader(http.StatusNoContent)
	default:
		s.t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestParseUniqueTransitionFields(t *testing.T) {
	got, err := parseUniqueJiraTransitionFields([]string{"resolution={\"name\":\"Fixed\"}", "customfield_1=a=b"}, []string{"storypoints=5"})
	want := []app.JiraTransitionFieldInput{
		{Field: "resolution", Value: `{"name":"Fixed"}`},
		{Field: "customfield_1", Value: "a=b"},
		{Field: "storypoints", Value: "5", ExplicitJSON: true},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("parseUniqueKV() = %#v, %v; want %#v", got, err, want)
	}
	for _, fields := range [][]string{{"resolution=x", " resolution =y"}, {"=x"}, {"missing"}} {
		if got, err := parseUniqueJiraTransitionFields(fields, nil); err == nil || got != nil {
			t.Errorf("parseUniqueJiraTransitionFields(%q) = %#v, %v; want error", fields, got, err)
		}
	}
}

func TestParseJiraFieldInputsKeepsLegacyStringsAndValidatesExplicitJSON(t *testing.T) {
	fields, err := parseJiraFieldInputs(
		[]string{"legacy_number=5", "priority={\"name\":\"High\"}"},
		[]string{"number=5", "enabled=true", "cleared=null", "array=[1,2]", "object={\"id\":\"7\"}"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := fields["legacy_number"]; got.Value != "5" || got.ExplicitJSON {
		t.Fatalf("legacy number=%+v, want legacy string", got)
	}
	for _, key := range []string{"number", "enabled", "cleared", "array", "object"} {
		if !fields[key].ExplicitJSON {
			t.Errorf("%s was not marked explicit JSON: %+v", key, fields[key])
		}
	}
	for _, tc := range []struct {
		name   string
		fields []string
		json   []string
	}{
		{"invalid JSON", nil, []string{"number=not-json"}},
		{"empty key", nil, []string{"=5"}},
		{"missing assignment", nil, []string{"number"}},
		{"cross-form conflict", []string{"number=5"}, []string{"number=5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := parseJiraFieldInputs(tc.fields, tc.json, false); err == nil || got != nil {
				t.Fatalf("parseJiraFieldInputs() = %#v, %v; want error", got, err)
			}
		})
	}
}

func TestJiraTransitionTextOmitsReviewedValues(t *testing.T) {
	result := &app.JiraTransitionGuardedResult{
		Status: "would_apply", Key: "PROJ-1",
		CurrentStatus: app.JiraTransitionStatus{ID: "1", Name: "Open"},
		Transition:    app.JiraTransitionSelection{ID: "31", Name: "Finish", To: "Done"},
		Fields: []app.JiraTransitionField{{
			Field: "resolution", Current: "private-current", Desired: "private-desired",
		}},
		Comment: &app.JiraTransitionComment{
			Body: "private-comment", BodyBytes: 15, BodySHA256: "body-hash",
			Actor: app.JiraCommentActor{Name: "private-actor"}, BaselineSHA256: "baseline-hash",
		},
		ProposalHash: "proposal-hash",
	}
	text := jiraTransitionText(result)
	for _, secret := range []string{"private-current", "private-desired", "private-comment", "private-actor", "resolution"} {
		if strings.Contains(text, secret) {
			t.Errorf("text contains reviewed value %q: %q", secret, text)
		}
	}
	for _, want := range []string{"PROJ-1", "31", "body-hash", "baseline-hash", "proposal-hash"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q: %q", want, text)
		}
	}
}

func TestJiraTransitionMissingHashAndDuplicateFieldsFailBeforeService(t *testing.T) {
	out, _, code := runCLIFull(t, nil, "jira", "issue", "transition", "PROJ-1", "--to", "Done", "--apply")
	if code != exitUsage || out != "" {
		t.Fatalf("missing hash exit=%d stdout=%q", code, out)
	}
	out, _, code = runCLIFull(t, nil, "jira", "issue", "transition", "preview", "PROJ-1", "--to", "Done",
		"--field", "resolution=x", "--field", " resolution =y")
	if code != exitUsage || out != "" {
		t.Fatalf("duplicate field exit=%d stdout=%q", code, out)
	}
}

func TestJiraTransitionExplicitEmptyCommentFailsBeforeService(t *testing.T) {
	for _, args := range [][]string{
		{"jira", "issue", "transition", "preview", "PROJ-1", "--to", "Done", "--comment", ""},
		{"jira", "issue", "transition", "PROJ-1", "--to", "Done", "--comment", ""},
		{"jira", "issue", "transition", "PROJ-1", "--to", "Done", "--comment", "", "--apply", "--expected-proposal-hash", "reviewed"},
	} {
		out, _, code := runCLIFull(t, nil, args...)
		if code != exitUsage || out != "" {
			t.Errorf("args=%q exit=%d stdout=%q", args, code, out)
		}
	}
}

func TestJiraTransitionPreviewFieldJSONPreservesScalarType(t *testing.T) {
	server := newJiraTransitionCLIServer(t)
	out, stderr, code := runCLIFull(t, jiraEnv(server.srv), "--read-only", "jira", "issue", "transition", "preview", "PROJ-1", "--to", "Finish", "--field-json", "customfield_10001=5")
	var result app.JiraTransitionGuardedResult
	if err := json.Unmarshal([]byte(out), &result); code != exitOK || err != nil {
		t.Fatalf("preview exit=%d err=%v stdout=%s stderr=%s", code, err, out, stderr)
	}
	if len(result.Fields) != 1 || result.Fields[0].Desired != float64(5) || server.posts != 0 {
		t.Fatalf("fields=%+v posts=%d, want numeric desired value and no write", result.Fields, server.posts)
	}
}

func TestJiraTransitionPreviewDryRunAndApplyUseExactRequestCounts(t *testing.T) {
	server := newJiraTransitionCLIServer(t)
	previewOut, code := runCLI(t, jiraEnv(server.srv), "--read-only", "jira", "issue", "transition", "preview", "PROJ-1", "--to", "Finish")
	var preview app.JiraTransitionGuardedResult
	if err := json.Unmarshal([]byte(previewOut), &preview); code != exitOK || err != nil || preview.Status != "would_apply" {
		t.Fatalf("preview=%+v exit=%d err=%v out=%s", preview, code, err, previewOut)
	}
	dryOut, code := runCLI(t, jiraEnv(server.srv), "jira", "issue", "transition", "PROJ-1", "--to", "Finish")
	var dry app.JiraTransitionGuardedResult
	if err := json.Unmarshal([]byte(dryOut), &dry); code != exitOK || err != nil || dry.ProposalHash != preview.ProposalHash {
		t.Fatalf("dry=%+v exit=%d err=%v out=%s", dry, code, err, dryOut)
	}
	applyOut, code := runCLI(t, jiraEnv(server.srv), "jira", "issue", "transition", "PROJ-1", "--to", "Finish",
		"--apply", "--expected-proposal-hash", preview.ProposalHash)
	var applied app.JiraTransitionGuardedResult
	if err := json.Unmarshal([]byte(applyOut), &applied); code != exitOK || err != nil || applied.Status != "applied" ||
		applied.ProposalHash != preview.ProposalHash || applied.CurrentStatus != preview.CurrentStatus {
		t.Fatalf("applied=%+v exit=%d err=%v out=%s", applied, code, err, applyOut)
	}
	if server.issueReads != 5 || server.transitions != 4 || server.posts != 1 {
		t.Fatalf("requests issue=%d transitions=%d posts=%d, want 5/4/1", server.issueReads, server.transitions, server.posts)
	}
}
