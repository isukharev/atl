package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/domain"
)

func jiraGraphServer(t *testing.T) (*httptest.Server, *[]string) {
	return jiraGraphServerWithDescription(t, "See PROJ-2 and pageId=7")
}

func jiraGraphServerWithDescription(t *testing.T, description string) (*httptest.Server, *[]string) {
	t.Helper()
	requests := []string{}
	descriptionJSON, err := json.Marshal(description)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		if got := request.Header.Get("Authorization"); got != "Bearer test-pat" {
			t.Fatalf("Jira authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/rest/api/2/issue/PROJ-1":
			if request.URL.Query().Get("fields") != "*all" ||
				request.URL.Query().Get("properties") != "*all" ||
				request.URL.Query().Get("expand") != "names,schema" {
				t.Fatalf("snapshot query = %s", request.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{
				"id":"10001","key":"PROJ-1",
				"fields":{
					"summary":"Graph seed",
					"description":`+string(descriptionJSON)+`,
					"issuelinks":[],
					"parent":null,
					"subtasks":[],
					"attachment":[{"id":"4","filename":"design.txt","content":"https://private.invalid/download"}]
				},
				"names":{"summary":"Summary","description":"Description"},
				"schema":{"summary":{"type":"string","system":"summary"},"description":{"type":"string","system":"description"}},
				"properties":{}
			}`)
		case "/rest/api/2/issue/PROJ-1/comment":
			_, _ = io.WriteString(w, `{"startAt":0,"total":0,"comments":[]}`)
		case "/rest/api/2/issue/PROJ-1/worklog":
			_, _ = io.WriteString(w, `{"startAt":0,"total":0,"worklogs":[]}`)
		case "/rest/api/2/issue/PROJ-1/remotelink":
			_, _ = io.WriteString(w, `[]`)
		case "/rest/dev-status/1.0/issue/summary":
			_, _ = io.WriteString(w, `{
				"errors":[],"configErrors":[],"summary":{
					"repository":{"overall":{"count":1},"byInstanceType":{"GitLab":{"count":1}}},
					"branch":{"overall":{"count":1},"byInstanceType":{"GitLab":{"count":1}}},
					"pullrequest":{"overall":{"count":1},"byInstanceType":{"GitLab":{"count":1}}}
				}
			}`)
		case "/rest/dev-status/1.0/issue/detail":
			project := "https://git.example.test/platform/widget"
			switch request.URL.Query().Get("dataType") {
			case "repository":
				_, _ = io.WriteString(w, `{"errors":[],"configErrors":[],"detail":[{"repositories":[{"url":"`+project+`","commits":[{"id":"0123456789abcdef0123456789abcdef01234567","url":"`+project+`/-/commit/0123456789abcdef0123456789abcdef01234567"}]}]}]}`)
			case "branch":
				_, _ = io.WriteString(w, `{"errors":[],"detail":[{"branches":[{"name":"feature/graph-proof","url":"`+project+`/-/tree/feature%2Fgraph-proof","repository":{"url":"`+project+`"}}]}]}`)
			case "pullrequest":
				_, _ = io.WriteString(w, `{"errors":[],"configErrors":[],"detail":[{"pullRequests":[{"id":"42","url":"`+project+`/-/merge_requests/42","status":"OPEN","repository":{"url":"`+project+`"}}]}]}`)
			default:
				t.Fatalf("unexpected Development selector %s", request.URL.RawQuery)
			}
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.RequestURI())
		}
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func TestJiraIssueGraphJSONAndTextGoldens(t *testing.T) {
	server, requests := jiraGraphServer(t)
	output, code := runCLI(t, jiraEnv(server), "--read-only", "jira", "issue", "graph", "PROJ-1")
	if code != exitOK {
		t.Fatalf("json exit=%d output=%s", code, output)
	}
	if strings.Contains(output, "private.invalid") {
		t.Fatalf("attachment download URL leaked: %s", output)
	}
	assertGolden(t, "jira_issue_graph.json", []byte(output))
	if len(*requests) != 4 {
		t.Fatalf("requests = %#v", *requests)
	}

	text, code := runCLI(t, jiraEnv(server), "-o", "text", "jira", "issue", "graph", "PROJ-1")
	if code != exitOK {
		t.Fatalf("text exit=%d output=%s", code, text)
	}
	assertGolden(t, "jira_issue_graph.md", []byte(text))
}

func TestJiraIssueGraphDevelopmentJSONAndTextGoldens(t *testing.T) {
	server, requests := jiraGraphServer(t)
	output, code := runCLI(t, jiraEnv(server), "jira", "issue", "graph", "PROJ-1", "--include-development")
	if code != exitOK {
		t.Fatalf("json exit=%d output=%s", code, output)
	}
	assertGolden(t, "jira_issue_graph_development.json", []byte(output))
	if len(*requests) != 8 {
		t.Fatalf("requests = %#v", *requests)
	}
	for _, request := range (*requests)[4:] {
		if strings.Contains(request, "git.example.test") {
			t.Fatalf("artifact URL was requested: %s", request)
		}
	}

	text, code := runCLI(t, jiraEnv(server), "-o", "text", "jira", "issue", "graph", "PROJ-1", "--include-development")
	if code != exitOK {
		t.Fatalf("text exit=%d output=%s", code, text)
	}
	assertGolden(t, "jira_issue_graph_development.md", []byte(text))
}

func TestJiraIssueGraphURLTextGolden(t *testing.T) {
	server, requests := jiraGraphServerWithDescription(t,
		"See https://external.example.test/guide/a&b "+
			"https://external.example.test/docs?token=private#fragment "+
			"https://external.example.test/token/secret")
	text, code := runCLI(t, jiraEnv(server), "-o", "text", "jira", "issue", "graph", "PROJ-1")
	if code != exitOK {
		t.Fatalf("text exit=%d output=%s", code, text)
	}
	assertGolden(t, "jira_issue_graph_urls.md", []byte(text))
	for _, forbidden := range []string{"token=private", "#fragment", "/token/secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unsafe URL %q leaked: %s", forbidden, text)
		}
	}
	if len(*requests) != 4 {
		t.Fatalf("requests = %#v", *requests)
	}
}

func TestJiraIssueGraphRejectsIDAndArityBeforeNetwork(t *testing.T) {
	server, requests := jiraGraphServer(t)
	for _, args := range [][]string{
		{"-o", "id", "jira", "issue", "graph", "PROJ-1"},
		{"jira", "issue", "graph"},
		{"jira", "issue", "graph", "PROJ-1", "PROJ-2"},
	} {
		output, code := runCLI(t, jiraEnv(server), args...)
		if code != exitUsage || output != "" {
			t.Fatalf("args=%v exit=%d output=%q", args, code, output)
		}
	}
	if len(*requests) != 0 {
		t.Fatalf("requests = %#v", *requests)
	}
}

func TestJiraIssueGraphDepthZeroFormsAreByteIdenticalSchemaV2(t *testing.T) {
	server, requests := jiraGraphServer(t)
	baseline, code := runCLI(t, jiraEnv(server), "jira", "issue", "graph", "PROJ-1")
	if code != exitOK {
		t.Fatalf("baseline exit=%d output=%s", code, baseline)
	}
	for _, args := range [][]string{
		{"jira", "issue", "graph", "PROJ-1", "--depth", "0"},
		{"jira", "issue", "graph", "PROJ-1", "--resolve", "none"},
		{"jira", "issue", "graph", "PROJ-1", "--depth", "0", "--resolve", "none"},
	} {
		output, code := runCLI(t, jiraEnv(server), args...)
		if code != exitOK {
			t.Fatalf("args=%v exit=%d output=%s", args, code, output)
		}
		var envelope struct {
			SchemaVersion int `json:"schema_version"`
		}
		if err := json.Unmarshal([]byte(output), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.SchemaVersion != 2 {
			t.Fatalf("args=%v schema=%d", args, envelope.SchemaVersion)
		}
		if output != baseline {
			t.Fatalf("args=%v output differs from default\ndefault=%s\nexplicit=%s", args, baseline, output)
		}
	}
	if len(*requests) != 16 {
		t.Fatalf("requests = %#v", *requests)
	}
}

func TestJiraIssueGraphCustomBoundUsesSchemaV2(t *testing.T) {
	server, requests := jiraGraphServer(t)
	output, code := runCLI(t, jiraEnv(server), "jira", "issue", "graph", "PROJ-1", "--max-nodes", "100")
	if code != exitOK {
		t.Fatalf("exit=%d output=%s", code, output)
	}
	var envelope struct {
		SchemaVersion int `json:"schema_version"`
		Bounds        struct {
			MaxNodes          int `json:"max_nodes"`
			RequestsUsed      int `json:"requests_used"`
			ResponseBytesUsed int `json:"response_bytes_used"`
		} `json:"bounds"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != 2 || envelope.Bounds.MaxNodes != 100 ||
		envelope.Bounds.RequestsUsed != 4 || envelope.Bounds.ResponseBytesUsed <= 0 {
		t.Fatalf("v2 envelope = %#v", envelope)
	}
	if len(*requests) != 4 {
		t.Fatalf("requests = %#v", *requests)
	}

	text, code := runCLI(t, jiraEnv(server), "-o", "text", "jira", "issue", "graph", "PROJ-1", "--max-nodes", "100")
	if code != exitOK || !strings.Contains(text, "- Transport: `4/100` attempts;") ||
		!strings.Contains(text, "| Node | Depth | Source |") {
		t.Fatalf("text exit=%d output=%s", code, text)
	}
}

func TestJiraIssueGraphConfluenceResolutionUsesConfiguredBackendOnce(t *testing.T) {
	jira, jiraRequests := jiraGraphServer(t)
	confluenceRequests := []string{}
	confluence := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		confluenceRequests = append(confluenceRequests, request.Method+" "+request.URL.RequestURI())
		if got := request.Header.Get("Authorization"); got != "Bearer confluence-secret" {
			t.Fatalf("Confluence authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"7","title":"Resolved page"}`)
	}))
	t.Cleanup(confluence.Close)
	env := jiraEnv(jira)
	env["ATL_CONFLUENCE_URL"] = confluence.URL
	env["ATL_CONFLUENCE_PAT"] = "confluence-secret"
	output, code := runCLI(t, env, "jira", "issue", "graph", "PROJ-1", "--resolve", "confluence")
	if code != exitOK {
		t.Fatalf("exit=%d output=%s", code, output)
	}
	if len(*jiraRequests) != 4 || len(confluenceRequests) != 1 ||
		confluenceRequests[0] != "GET /rest/api/content/7" {
		t.Fatalf("Jira requests=%#v Confluence requests=%#v", *jiraRequests, confluenceRequests)
	}
	var result struct {
		SchemaVersion int `json:"schema_version"`
		Nodes         []struct {
			ID    string `json:"id"`
			State string `json:"state"`
			Label string `json:"label"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 2 {
		t.Fatalf("schema = %d", result.SchemaVersion)
	}
	found := false
	for _, node := range result.Nodes {
		if node.ID == "confluence:page:7" {
			found = node.State == "resolved" && node.Label == "Resolved page"
		}
	}
	if !found {
		t.Fatalf("resolved node missing: %s", output)
	}
}

func TestJiraIssueGraphRejectsInvalidV2FlagsBeforeConfigOrNetwork(t *testing.T) {
	for _, args := range [][]string{
		{"jira", "issue", "graph", "PROJ-1", "--depth", "4"},
		{"jira", "issue", "graph", "PROJ-1", "--resolve", "other"},
		{"jira", "issue", "graph", "PROJ-1", "--max-nodes", "0"},
		{"jira", "issue", "graph", "PROJ-1", "--max-requests", "129"},
		{"jira", "issue", "graph", "PROJ-1", "--max-bytes", "-1"},
	} {
		output, code := runCLI(t, nil, args...)
		if code != exitUsage || output != "" {
			t.Fatalf("args=%v exit=%d output=%q", args, code, output)
		}
	}
}

func TestJiraIssueGraphRejectsSemanticFlagsBeforePersistentPreRun(t *testing.T) {
	root := newRoot()
	preRuns := 0
	root.PersistentPreRunE = func(*cobra.Command, []string) error {
		preRuns++
		return nil
	}
	root.SetArgs([]string{"jira", "issue", "graph", "PROJ-1", "--depth", "4"})
	if err := root.ExecuteContext(context.Background()); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("error = %v, want ErrUsage", err)
	}
	if preRuns != 0 {
		t.Fatalf("persistent pre-runs = %d, want 0", preRuns)
	}
}

func TestJiraIssueGraphRequestBudgetCountsPhysicalAttempts(t *testing.T) {
	server, requests := jiraGraphServer(t)
	output, code := runCLI(t, jiraEnv(server),
		"jira", "issue", "graph", "PROJ-1", "--max-requests", "1")
	if code != exitOK {
		t.Fatalf("exit=%d output=%s", code, output)
	}
	if len(*requests) != 1 {
		t.Fatalf("requests = %#v", *requests)
	}
	var result struct {
		Complete bool `json:"complete"`
		Bounds   struct {
			RequestsUsed int `json:"requests_used"`
		} `json:"bounds"`
		Sources []struct {
			Kind          string `json:"kind"`
			Status        string `json:"status"`
			PartialReason string `json:"partial_reason"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Bounds.RequestsUsed != 1 {
		t.Fatalf("result = %#v", result)
	}
	foundComments := false
	for _, source := range result.Sources {
		if source.Kind == "comments" {
			foundComments = true
			if source.Status != "partial" || source.PartialReason != "request_limit" {
				t.Fatalf("comments source = %#v", source)
			}
		}
	}
	if !foundComments {
		t.Fatal("comments source missing")
	}
}

func TestJiraIssueGraphRootByteBudgetEmitsQualifiedPartialGraph(t *testing.T) {
	server, requests := jiraGraphServer(t)
	output, code := runCLI(t, jiraEnv(server),
		"jira", "issue", "graph", "PROJ-1", "--max-bytes", "1")
	if code != exitOK {
		t.Fatalf("exit=%d output=%s", code, output)
	}
	if len(*requests) != 1 {
		t.Fatalf("requests = %#v", *requests)
	}
	var result struct {
		Complete bool `json:"complete"`
		Bounds   struct {
			ExpandedNodes     int `json:"expanded_node_count"`
			RequestsUsed      int `json:"requests_used"`
			ResponseBytesUsed int `json:"response_bytes_used"`
		} `json:"bounds"`
		Frontier []struct {
			Reason string `json:"reason"`
		} `json:"frontier"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Bounds.ExpandedNodes != 0 ||
		result.Bounds.RequestsUsed != 1 || result.Bounds.ResponseBytesUsed != 1 ||
		len(result.Frontier) != 1 || result.Frontier[0].Reason != "byte_limit" {
		t.Fatalf("root byte result = %#v", result)
	}
}

func TestJiraIssueGraphMissingConfluenceDependencyIsQualified(t *testing.T) {
	server, _ := jiraGraphServer(t)
	output, code := runCLI(t, jiraEnv(server),
		"jira", "issue", "graph", "PROJ-1", "--resolve", "confluence")
	if code != exitOK {
		t.Fatalf("exit=%d output=%s", code, output)
	}
	var result struct {
		Complete bool `json:"complete"`
		Sources  []struct {
			Kind          string `json:"kind"`
			Status        string `json:"status"`
			PartialReason string `json:"partial_reason"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.Complete {
		t.Fatal("missing Confluence dependency unexpectedly complete")
	}
	for _, source := range result.Sources {
		if source.Kind == "confluence_metadata" {
			if source.Status != "skipped" || source.PartialReason != "dependency_unavailable" {
				t.Fatalf("Confluence source = %#v", source)
			}
			return
		}
	}
	t.Fatal("Confluence metadata source missing")
}

func TestJiraIssueGraphStrictEmitsIncompleteGraphThenFails(t *testing.T) {
	server, requests := jiraGraphServer(t)
	output, code := runCLI(t, jiraEnv(server),
		"jira", "issue", "graph", "PROJ-1", "--max-nodes", "1", "--strict")
	if code != exitCheckFailed || output == "" {
		t.Fatalf("exit=%d output=%q", code, output)
	}
	var result struct {
		SchemaVersion int  `json:"schema_version"`
		Complete      bool `json:"complete"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 2 || result.Complete {
		t.Fatalf("strict result = %#v", result)
	}
	if len(*requests) != 4 {
		t.Fatalf("requests = %#v", *requests)
	}
}
