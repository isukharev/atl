package jira

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestReadProjectsMapsAtomicInventory(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = io.WriteString(w, `[{"id":"2","key":"OPS","name":"Operations","projectTypeKey":"business","archived":true}]`)
	}))
	defer server.Close()
	projects, err := newTestJira(server).ReadProjects(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if query != "includeArchived=true" || len(projects) != 1 || projects[0].Key != "OPS" || projects[0].Archived == nil || !*projects[0].Archived {
		t.Fatalf("query=%q projects=%+v", query, projects)
	}
}

func TestReadCreateMetadataSelectsExactTypeAndKeepsValuesPrivate(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		switch r.URL.Path {
		case "/rest/api/2/issue/createmeta/OPS/issuetypes":
			_, _ = io.WriteString(w, `{"isLast":true,"values":[{"id":"10","name":"Task","subtask":false},{"id":"11","name":"Sub-task","subtask":true}]}`)
		case "/rest/api/2/issue/createmeta/OPS/issuetypes/10":
			_, _ = io.WriteString(w, `{"isLast":true,"values":[{"fieldId":"summary","name":"Summary","required":true},{"fieldId":"priority","name":"Priority","required":false,"allowedValues":[{"name":"Secret Choice","value":"private-id"}]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	metadata, err := newTestJira(server).ReadCreateMetadata(context.Background(), "OPS", "Task")
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.JiraCreateField{
		{FieldID: "priority", Name: "Priority", HasAllowedValues: true},
		{FieldID: "summary", Name: "Summary", Required: true},
	}
	if metadata.IssueType.ID != "10" || !reflect.DeepEqual(metadata.Fields, want) {
		t.Fatalf("metadata=%+v", metadata)
	}
	if len(paths) != 2 {
		t.Fatalf("requests=%v, want exactly two", paths)
	}
}

func TestReadCreateMetadataRejectsMissingAndAmbiguousSelectors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"isLast":true,"values":[{"id":"10","name":"Task"},{"id":"11","name":"Task"}]}`)
	}))
	defer server.Close()
	j := newTestJira(server)
	if _, err := j.ReadCreateMetadata(context.Background(), "OPS", "Missing"); !errors.Is(err, domain.ErrNotFound) ||
		!strings.Contains(err.Error(), "atl jira issue types --project PROJECT") || strings.Contains(err.Error(), "OPS") {
		t.Fatalf("missing selector error=%v", err)
	}
	if _, err := j.ReadCreateMetadata(context.Background(), "OPS", "Task"); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("ambiguous selector error=%v", err)
	}
}

func TestReadCreateIssueTypesFollowsQualifiedShortPage(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("startAt") == "0" {
			_, _ = io.WriteString(w, `{"startAt":0,"total":2,"values":[{"id":"10","name":"Task"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"startAt":1,"total":2,"isLast":true,"values":[{"id":"11","name":"Story"}]}`)
	}))
	defer server.Close()
	types, err := newTestJira(server).ReadCreateIssueTypes(context.Background(), "OPS")
	if err != nil || requests != 2 || len(types) != 2 {
		t.Fatalf("types=%+v requests=%d err=%v", types, requests, err)
	}
}

func TestReadCreateIssueTypesRejectsAdvertisedOverLimitBeforeSecondRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = io.WriteString(w, `{"startAt":1,"total":1001,"values":[{"id":"10","name":"Task"}]}`)
	}))
	defer server.Close()
	_, err := newTestJira(server).ReadCreateIssueTypes(context.Background(), "OPS")
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "1000 item limit") || requests != 1 {
		t.Fatalf("requests=%d err=%v", requests, err)
	}
}

func TestReadCreateMetadataRejectsNonContiguousOffsets(t *testing.T) {
	tests := []struct {
		name string
		path string
		call func(*Jira) error
	}{
		{
			name: "issue types",
			path: "/rest/api/2/issue/createmeta/OPS/issuetypes",
			call: func(j *Jira) error {
				_, err := j.ReadCreateIssueTypes(context.Background(), "OPS")
				return err
			},
		},
		{
			name: "fields",
			path: "/rest/api/2/issue/createmeta/OPS/issuetypes/10",
			call: func(j *Jira) error {
				_, err := j.readCreateFields(context.Background(), "OPS", "10")
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.path {
					http.NotFound(w, r)
					return
				}
				_, _ = io.WriteString(w, `{"startAt":1,"total":1,"isLast":true,"values":[]}`)
			}))
			t.Cleanup(server.Close)
			err := test.call(newTestJira(server))
			if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "returned offset 1 while 0 was requested") {
				t.Fatalf("error=%v, want non-contiguous offset rejection", err)
			}
		})
	}
}

func TestLinkEpicWithFieldUsesResolvedFieldWithoutDiscoveryRequest(t *testing.T) {
	var gotMethod, gotPath string
	var fields map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		fields = readFields(t, r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if err := newTestJira(server).LinkEpicWithField(context.Background(), "OPS-1", "OPS-9", "customfield_42"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotPath != "/rest/api/2/issue/OPS-1" || fields["customfield_42"] != "OPS-9" {
		t.Fatalf("method=%s path=%s fields=%v", gotMethod, gotPath, fields)
	}
}
