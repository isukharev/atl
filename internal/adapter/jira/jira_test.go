package jira

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

// newTestJira wires a Jira adapter at the test server's URL.
func newTestJira(srv *httptest.Server) *Jira {
	return &Jira{c: httpx.New(srv.URL, "tok", "test"), base: srv.URL}
}

// readFields decodes the {"fields": {...}} body sent to create/update.
func readFields(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var payload struct {
		Fields map[string]any `json:"fields"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode body %q: %v", body, err)
	}
	return payload.Fields
}

// --- Finding 1: extra --field values are coerced to JSON when valid ---

func TestCreateCoercesExtraFieldValues(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = readFields(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"key":"ABC-1"}`)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	_, err := j.Create(context.Background(), "ABC", "Task", "summary", nil, map[string]string{
		"priority":    `{"name":"High"}`,
		"labels":      `["a","b"]`,
		"storypoints": `5`,
		"flag":        `true`,
		"empty":       `null`,
		"plainword":   "bare",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// JSON object decodes to a map, not a quoted string.
	prio, ok := got["priority"].(map[string]any)
	if !ok {
		t.Fatalf("priority sent as %T (%v), want object", got["priority"], got["priority"])
	}
	if prio["name"] != "High" {
		t.Errorf("priority.name = %v, want High", prio["name"])
	}

	// JSON array decodes to a slice.
	labels, ok := got["labels"].([]any)
	if !ok || len(labels) != 2 || labels[0] != "a" || labels[1] != "b" {
		t.Errorf("labels sent as %T (%v), want [a b]", got["labels"], got["labels"])
	}

	// Bare scalars stay plain strings — a value that merely looks like JSON
	// (number/bool/null) is NOT retyped, since Jira would reject it for a
	// text/label/version field. Only objects/arrays are decoded.
	for k, want := range map[string]string{
		"storypoints": "5",
		"flag":        "true",
		"empty":       "null",
		"plainword":   "bare",
	} {
		if s, ok := got[k].(string); !ok || s != want {
			t.Errorf("%s sent as %T (%v), want string %q", k, got[k], got[k], want)
		}
	}
}

func TestUpdateCoercesExtraFieldValues(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		got = readFields(t, r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	err := j.Update(context.Background(), "ABC-1", "", nil, map[string]string{
		"components": `[{"name":"backend"}]`,
		"plainword":  "bare",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	comps, ok := got["components"].([]any)
	if !ok || len(comps) != 1 {
		t.Fatalf("components sent as %T (%v), want array", got["components"], got["components"])
	}
	if m, ok := comps[0].(map[string]any); !ok || m["name"] != "backend" {
		t.Errorf("components[0] = %v, want {name:backend}", comps[0])
	}
	if s, ok := got["plainword"].(string); !ok || s != "bare" {
		t.Errorf("plainword sent as %T (%v), want string \"bare\"", got["plainword"], got["plainword"])
	}
}

// --- Finding 2: mapIssue leaves Version at 0 (no API counter to populate) ---

func TestMapIssueVersionIsZero(t *testing.T) {
	j := &Jira{}
	is := j.mapIssue(issueDTO{
		Key: "ABC-1",
		Fields: map[string]any{
			"summary": "hello",
			"updated": "2024-01-02T03:04:05.000+0000",
		},
	})
	if is.Version != 0 {
		t.Errorf("Version = %d, want 0 (Jira DC has no per-issue version counter)", is.Version)
	}
}

// --- Finding 3: FieldOptions returns ErrNotFound when the field is absent ---

const createmetaBody = `{
  "projects": [
    {
      "issuetypes": [
        {
          "fields": {
            "priority": {
              "name": "Priority",
              "allowedValues": [
                {"name": "High"},
                {"name": "Low"}
              ]
            }
          }
        }
      ]
    }
  ]
}`

func TestFieldOptionsReturnsValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, createmetaBody)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	opts, err := j.FieldOptions(context.Background(), "ABC", "Task", "priority")
	if err != nil {
		t.Fatalf("FieldOptions: %v", err)
	}
	if len(opts) != 2 || opts[0] != "High" || opts[1] != "Low" {
		t.Errorf("opts = %v, want [High Low]", opts)
	}
}

func TestFieldOptionsUnknownFieldNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, createmetaBody)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	_, err := j.FieldOptions(context.Background(), "ABC", "Task", "nosuchfield")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want wrap of domain.ErrNotFound", err)
	}
}

// --- Finding 4: mapIssue gives Raw its own copy, not an alias of Fields ---

func TestMapIssueRawIsIndependentCopy(t *testing.T) {
	j := &Jira{}
	is := j.mapIssue(issueDTO{
		Key:    "ABC-1",
		Fields: map[string]any{"summary": "hello"},
	})
	// Mutating Raw must not leak into the exported Fields map.
	is.Raw["injected"] = "x"
	if _, present := is.Fields["injected"]; present {
		t.Errorf("Fields was mutated through Raw alias; want independent copies")
	}
}
