package jira

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Changelog uses the DC-universal ?expand=changelog form (the paginated
// /changelog sub-resource is Cloud/DC-9+ only) and maps histories to entries.
func TestChangelogExpandsAndMaps(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query().Get("expand")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"key":"PROJ-1",
			"changelog":{"histories":[
				{"id":"10100","author":{"displayName":"Alice","name":"alice"},"created":"2026-01-01T10:00:00.000+0000",
				 "items":[{"field":"status","fromString":"To Do","toString":"In Progress"},
				          {"field":"assignee","fromString":"","toString":"alice"}]}
			]}}`))
	}))
	defer srv.Close()

	j := newTestJira(srv)
	entries, err := j.Changelog(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatalf("Changelog: %v", err)
	}
	if gotPath != "/rest/api/2/issue/PROJ-1" {
		t.Errorf("path = %q, want /rest/api/2/issue/PROJ-1", gotPath)
	}
	if gotQuery != "changelog" {
		t.Errorf("expand = %q, want changelog", gotQuery)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.ID != "10100" || e.Author != "Alice" || len(e.Items) != 2 {
		t.Fatalf("entry mismatch: %+v", e)
	}
	if e.Items[0].Field != "status" || e.Items[0].From != "To Do" || e.Items[0].To != "In Progress" {
		t.Errorf("item[0] mismatch: %+v", e.Items[0])
	}
}

func TestListCommentsMapsFromCommentEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"comments":[
			{"id":"1","author":{"displayName":"Bob"},"created":"2026-01-02","body":"hello"},
			{"id":"2","author":{"displayName":"Carol"},"created":"2026-01-03","body":"world"}
		],"total":2}`))
	}))
	defer srv.Close()

	j := newTestJira(srv)
	cs, err := j.ListComments(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if gotPath != "/rest/api/2/issue/PROJ-1/comment" {
		t.Errorf("path = %q", gotPath)
	}
	if len(cs) != 2 || cs[0].ID != "1" || cs[0].Author != "Bob" || cs[0].Body != "hello" || cs[1].ID != "2" {
		t.Fatalf("comments mismatch: %+v", cs)
	}
}

func TestDeleteCommentHitsRightPath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if err := j.DeleteComment(context.Background(), "PROJ-1", "42"); err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/rest/api/2/issue/PROJ-1/comment/42" {
		t.Errorf("got %s %s, want DELETE /rest/api/2/issue/PROJ-1/comment/42", gotMethod, gotPath)
	}
}

func TestDeleteLinkHitsRightPath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if err := j.DeleteLink(context.Background(), "10005"); err != nil {
		t.Fatalf("DeleteLink: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/rest/api/2/issueLink/10005" {
		t.Errorf("got %s %s, want DELETE /rest/api/2/issueLink/10005", gotMethod, gotPath)
	}
}

// A link's backend id must be captured so `jira issue link delete <id>` has
// something to target.
func TestGetIssueCapturesLinkIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"PROJ-1","fields":{"issuelinks":[
			{"id":"10005","type":{"name":"Blocks","inward":"is blocked by","outward":"blocks"},
			 "outwardIssue":{"key":"PROJ-2"}}
		]}}`))
	}))
	defer srv.Close()

	j := newTestJira(srv)
	is, err := j.GetIssue(context.Background(), "PROJ-1", nil)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if len(is.Links) != 1 {
		t.Fatalf("got %d links, want 1", len(is.Links))
	}
	if is.Links[0].ID != "10005" || !strings.EqualFold(is.Links[0].Key, "PROJ-2") {
		t.Errorf("link mismatch: %+v", is.Links[0])
	}
}
