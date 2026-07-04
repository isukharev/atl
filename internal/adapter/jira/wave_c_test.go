package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Transition can set fields on the transition (e.g. resolution=Fixed at Done).
func TestTransitionSendsFields(t *testing.T) {
	var postBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/2/issue/ABC-1/transitions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"transitions":[{"id":"31","name":"Done","to":{"name":"Done"}}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &postBody)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	j := newTestJira(srv)
	err := j.Transition(context.Background(), "ABC-1", "Done", "", map[string]string{
		"resolution": `{"name":"Fixed"}`,
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	tr, _ := postBody["transition"].(map[string]any)
	if tr["id"] != "31" {
		t.Errorf("transition id = %v, want 31", tr["id"])
	}
	fl, ok := postBody["fields"].(map[string]any)
	if !ok {
		t.Fatalf("fields not sent: %v", postBody)
	}
	res, _ := fl["resolution"].(map[string]any)
	if res["name"] != "Fixed" {
		t.Errorf("resolution.name = %v, want Fixed", res["name"])
	}
}

// Transition with no fields must not include a "fields" key (empty object would
// make Jira reject the transition on some screens).
func TestTransitionOmitsEmptyFields(t *testing.T) {
	var postBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/2/issue/ABC-1/transitions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"transitions":[{"id":"31","name":"Done","to":{"name":"Done"}}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &postBody)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	j := newTestJira(srv)
	if err := j.Transition(context.Background(), "ABC-1", "Done", "", nil); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if _, present := postBody["fields"]; present {
		t.Errorf("empty fields should be omitted, got %v", postBody)
	}
}

func TestDeleteIssueHitsRightPath(t *testing.T) {
	var method, path, subtasks string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, subtasks = r.Method, r.URL.Path, r.URL.Query().Get("deleteSubtasks")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if err := j.DeleteIssue(context.Background(), "ABC-1", true); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}
	if method != http.MethodDelete || path != "/rest/api/2/issue/ABC-1" || subtasks != "true" {
		t.Errorf("got %s %s deleteSubtasks=%s", method, path, subtasks)
	}
}

func TestUpdateLabelsSendsAddRemove(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if err := j.UpdateLabels(context.Background(), "ABC-1", []string{"urgent"}, []string{"stale"}); err != nil {
		t.Fatalf("UpdateLabels: %v", err)
	}
	upd, _ := body["update"].(map[string]any)
	ops, _ := upd["labels"].([]any)
	if len(ops) != 2 {
		t.Fatalf("want 2 label ops, got %v", upd["labels"])
	}
	add, _ := ops[0].(map[string]any)
	rem, _ := ops[1].(map[string]any)
	if add["add"] != "urgent" || rem["remove"] != "stale" {
		t.Errorf("label ops mismatch: %v", ops)
	}
}

func TestCurrentUserMapsDCFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/myself" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"name":"jdoe","key":"jdoe","displayName":"Jane Doe","emailAddress":"redacted","active":true}`)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	u, err := j.CurrentUser(context.Background())
	if err != nil {
		t.Fatalf("CurrentUser: %v", err)
	}
	if u.Name != "jdoe" || u.Key != "jdoe" || u.DisplayName != "Jane Doe" || u.Email != "redacted" || !u.Active {
		t.Errorf("user mismatch: %+v", u)
	}
}

func TestSearchUsersUsesUsernameParam(t *testing.T) {
	var q string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/user/search" {
			t.Errorf("path = %q", r.URL.Path)
		}
		q = r.URL.Query().Get("username")
		_, _ = io.WriteString(w, `[{"name":"jdoe","displayName":"Jane Doe","active":true},
			{"name":"jsmith","displayName":"John Smith","active":false}]`)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	us, err := j.SearchUsers(context.Background(), "j", 50)
	if err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	if q != "j" {
		t.Errorf("query param username=%q, want j (DC uses username, not query)", q)
	}
	if len(us) != 2 || us[0].Name != "jdoe" || us[1].Name != "jsmith" {
		t.Errorf("users mismatch: %+v", us)
	}
}

func TestGetUserByUsername(t *testing.T) {
	var q string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/user" {
			t.Errorf("path = %q", r.URL.Path)
		}
		q = r.URL.Query().Get("username")
		_, _ = io.WriteString(w, `{"name":"jdoe","key":"jdoe","displayName":"Jane Doe","active":true}`)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	u, err := j.GetUser(context.Background(), "jdoe")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if q != "jdoe" || u.DisplayName != "Jane Doe" {
		t.Errorf("get user: q=%q ret=%+v", q, u)
	}
}
