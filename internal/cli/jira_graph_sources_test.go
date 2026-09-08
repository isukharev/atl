package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

func TestJiraIssueGraphSourcesRejectBeforeConfig(t *testing.T) {
	for _, flags := range [][]string{
		{"--include-sources="}, {"--exclude-sources="}, {"--include-sources", "unknown"},
		{"--include-sources", "comments,"}, {"--include-sources", " comments"},
		{"--include-sources", "development"}, {"--include-development", "--exclude-sources", "development"},
		{"--include-sources", "comments", "--exclude-sources", "comments"},
		{"--include-sources", strings.Repeat("comments,", 10)},
	} {
		args := append([]string{"jira", "issue", "graph", "PROJ-1"}, flags...)
		output, code := runCLI(t, nil, args...)
		if code != exitUsage {
			t.Fatalf("flags=%v exit=%d output=%s", flags, code, output)
		}
	}
}

func TestJiraIssueGraphSourcesSelectRequestsAndStrictQualification(t *testing.T) {
	for _, test := range []struct {
		source         string
		code, requests int
		complete       bool
	}{
		{"issue_links", exitOK, 1, true}, {"comments", exitCheckFailed, 2, false},
	} {
		t.Run(test.source, func(t *testing.T) {
			paths := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/rest/api/2/issue/PROJ-1" {
					wantFields := "summary"
					if test.source == "issue_links" {
						wantFields += ",issuelinks"
					}
					if r.URL.Query().Get("fields") != wantFields || r.URL.Query().Has("properties") || r.URL.Query().Get("expand") != "names,schema" {
						t.Errorf("query=%s", r.URL.RawQuery)
					}
					_, _ = w.Write([]byte(`{"id":"1","key":"PROJ-1","fields":{"summary":"Seed","issuelinks":[],"description":"PROJ-9"},"names":{},"schema":{},"properties":[]}`))
					return
				}
				if r.URL.Path != "/rest/api/2/issue/PROJ-1/comment" {
					t.Errorf("unselected request=%s", r.URL.Path)
				}
				w.WriteHeader(http.StatusForbidden)
			}))
			defer server.Close()
			output, code := runCLI(t, jiraEnv(server), "jira", "issue", "graph", "PROJ-1", "--include-sources", test.source, "--strict")
			if code != test.code || len(paths) != test.requests {
				t.Fatalf("exit=%d requests=%v output=%s", code, paths, output)
			}
			var result app.JiraIssueGraphResult
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatal(err)
			}
			if result.Complete != test.complete || result.SourceSelection == nil || !slices.Equal(result.SourceSelection.Selected, []string{test.source}) || len(result.Sources) != 1 {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}
