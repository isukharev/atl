package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Assign uses the dedicated assignee endpoint with {"name": <user>} — not the
// generic field-update path, where a bare string assignee is rejected by DC.
func TestAssignPutsNameToAssigneeEndpoint(t *testing.T) {
	var method, path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if err := j.Assign(context.Background(), "ABC-1", "jdoe"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if method != http.MethodPut || path != "/rest/api/2/issue/ABC-1/assignee" {
		t.Errorf("got %s %s, want PUT /rest/api/2/issue/ABC-1/assignee", method, path)
	}
	if body["name"] != "jdoe" {
		t.Errorf("payload = %v, want {\"name\":\"jdoe\"}", body)
	}
}

// An empty username must serialize as an explicit {"name": null} — omitting the
// key (or sending "") would not unassign on Jira DC.
func TestAssignEmptyUsernameSendsNullName(t *testing.T) {
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	if err := j.Assign(context.Background(), "ABC-1", ""); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	v, ok := body["name"]
	if !ok || string(v) != "null" {
		t.Errorf("payload = %s, want {\"name\":null}", raw)
	}
}
