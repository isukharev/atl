package jira

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestSetFieldsPreservesExplicitTypes(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.EscapedPath() != "/rest/api/2/issue/A%20B%2FC" {
			t.Errorf("request = %s %s", r.Method, r.URL.EscapedPath())
		}
		got = readFields(t, r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	err := j.SetFields(context.Background(), "A B/C", map[string]any{
		"customfield_1": "{}",
		"customfield_2": map[string]any{"id": "7"},
	})
	if err != nil {
		t.Fatalf("SetFields: %v", err)
	}
	if got["customfield_1"] != "{}" {
		t.Fatalf("literal string was retyped: %#v", got["customfield_1"])
	}
	if object, ok := got["customfield_2"].(map[string]any); !ok || object["id"] != "7" {
		t.Fatalf("object type was lost: %#v", got["customfield_2"])
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

// Jira DC 9.x removed the expand-based createmeta; FieldOptions now walks the
// two-step /createmeta/{projectKey}/issuetypes[/{id}] endpoints. createmetaMux
// serves that shape and records which paths were hit.
func createmetaMux(hit *[]string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/2/issue/createmeta/ABC/issuetypes", func(w http.ResponseWriter, r *http.Request) {
		*hit = append(*hit, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"values":[{"id":"3","name":"Task"},{"id":"5","name":"Bug"}]}`)
	})
	mux.HandleFunc("/rest/api/2/issue/createmeta/ABC/issuetypes/3", func(w http.ResponseWriter, r *http.Request) {
		*hit = append(*hit, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"values":[
			{"fieldId":"priority","name":"Priority","allowedValues":[{"name":"High"},{"name":"Low"}]}
		]}`)
	})
	// Bug (id 5) overlaps Task on "High" and adds "Critical" — used to exercise
	// the scan-all (empty --type) path and cross-type value de-duplication.
	mux.HandleFunc("/rest/api/2/issue/createmeta/ABC/issuetypes/5", func(w http.ResponseWriter, r *http.Request) {
		*hit = append(*hit, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"values":[
			{"fieldId":"priority","name":"Priority","allowedValues":[{"name":"High"},{"name":"Critical"}]}
		]}`)
	})
	return mux
}

func TestFieldOptionsReturnsValues(t *testing.T) {
	var hit []string
	srv := httptest.NewServer(createmetaMux(&hit))
	defer srv.Close()

	j := newTestJira(srv)
	opts, err := j.FieldOptions(context.Background(), "ABC", "Task", "priority")
	if err != nil {
		t.Fatalf("FieldOptions: %v", err)
	}
	if len(opts) != 2 || opts[0] != "High" || opts[1] != "Low" {
		t.Errorf("opts = %v, want [High Low]", opts)
	}
	// Both steps of the DC createmeta endpoint must have been used.
	if len(hit) != 2 || hit[0] != "/rest/api/2/issue/createmeta/ABC/issuetypes" || hit[1] != "/rest/api/2/issue/createmeta/ABC/issuetypes/3" {
		t.Errorf("unexpected createmeta paths: %v", hit)
	}
}

func TestFieldOptionsUnknownFieldNotFound(t *testing.T) {
	var hit []string
	srv := httptest.NewServer(createmetaMux(&hit))
	defer srv.Close()

	j := newTestJira(srv)
	_, err := j.FieldOptions(context.Background(), "ABC", "Task", "nosuchfield")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want wrap of domain.ErrNotFound", err)
	}
}

// TestFieldOptionsUnknownTypeNotFound verifies that naming an issue type the
// project doesn't have is reported as ErrNotFound, not a silent empty result.
func TestFieldOptionsUnknownTypeNotFound(t *testing.T) {
	var hit []string
	srv := httptest.NewServer(createmetaMux(&hit))
	defer srv.Close()

	j := newTestJira(srv)
	_, err := j.FieldOptions(context.Background(), "ABC", "Nonexistent", "priority")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want wrap of domain.ErrNotFound", err)
	}
}

// TestFieldOptionsScanAllDeduplicates verifies that an empty issue type scans
// every type in the project and returns the de-duplicated union of allowed
// values (Task: High, Low; Bug: High, Critical -> High, Low, Critical).
func TestFieldOptionsScanAllDeduplicates(t *testing.T) {
	var hit []string
	srv := httptest.NewServer(createmetaMux(&hit))
	defer srv.Close()

	j := newTestJira(srv)
	opts, err := j.FieldOptions(context.Background(), "ABC", "", "priority")
	if err != nil {
		t.Fatalf("FieldOptions: %v", err)
	}
	want := []string{"High", "Low", "Critical"}
	if len(opts) != len(want) {
		t.Fatalf("opts = %v, want %v", opts, want)
	}
	for i := range want {
		if opts[i] != want[i] {
			t.Fatalf("opts = %v, want %v", opts, want)
		}
	}
	// Both per-type endpoints must have been walked.
	if len(hit) != 3 || hit[1] != "/rest/api/2/issue/createmeta/ABC/issuetypes/3" || hit[2] != "/rest/api/2/issue/createmeta/ABC/issuetypes/5" {
		t.Errorf("unexpected createmeta paths: %v", hit)
	}
}

// TestFieldOptionsScanAllToleratesTypeError verifies that, when scanning every
// type, a single per-type createmeta failure does not sink the whole scan: the
// values already collected from healthy types are still returned.
func TestFieldOptionsScanAllToleratesTypeError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/2/issue/createmeta/ABC/issuetypes", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"values":[{"id":"3","name":"Task"},{"id":"5","name":"Bug"}]}`)
	})
	mux.HandleFunc("/rest/api/2/issue/createmeta/ABC/issuetypes/3", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"values":[
			{"fieldId":"priority","name":"Priority","allowedValues":[{"name":"High"},{"name":"Low"}]}
		]}`)
	})
	// Bug (id 5) is restricted / odd and errors on the detail endpoint.
	mux.HandleFunc("/rest/api/2/issue/createmeta/ABC/issuetypes/5", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	j := newTestJira(srv)
	opts, err := j.FieldOptions(context.Background(), "ABC", "", "priority")
	if err != nil {
		t.Fatalf("FieldOptions: %v", err)
	}
	want := []string{"High", "Low"}
	if len(opts) != len(want) || opts[0] != want[0] || opts[1] != want[1] {
		t.Fatalf("opts = %v, want %v", opts, want)
	}
}

// TestFieldOptionsScanAllAllTypesErrorSurfacesError verifies that when scanning
// every type and *no* type can be read, the underlying error is surfaced rather
// than a misleading "field not found".
func TestFieldOptionsScanAllAllTypesErrorSurfacesError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/2/issue/createmeta/ABC/issuetypes", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"values":[{"id":"3","name":"Task"},{"id":"5","name":"Bug"}]}`)
	})
	// Every per-type request fails (e.g. expired PAT): 403 -> domain.ErrForbidden.
	mux.HandleFunc("/rest/api/2/issue/createmeta/ABC/issuetypes/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	j := newTestJira(srv)
	_, err := j.FieldOptions(context.Background(), "ABC", "", "priority")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want wrap of domain.ErrForbidden", err)
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

// TestUpdate409IsNotVersionConflict pins issue #66: Jira DC has no version
// gate, so an HTTP 409 on a write (locked issue, workflow veto) must NOT
// surface as ErrVersionConflict/exit 5 — the constructor marks the client
// no-version-gate. Uses New() (not the test helper) to exercise that wiring.
func TestUpdate409IsNotVersionConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"errorMessages":["Issue is locked for editing"]}`))
	}))
	defer srv.Close()

	j := New(srv.URL, "tok", "test")
	err := j.Update(context.Background(), "PROJ-1", "", []byte("body"), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("jira 409 must not map to ErrVersionConflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "409") || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("error must carry the HTTP 409 body, got %q", err)
	}
}
