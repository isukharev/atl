package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

func TestSyntheticTopicDiscoveryUsesTwoSearchesAndOnlyBoundedSelectedEvidence(t *testing.T) {
	backend := startAgentEvalFixture(t, "cross-service-topic-discovery")
	env := backend.Environment()

	confOut, code := runCLI(t, env, "conf", "search", "--cql", `siteSearch ~ "Orchid retry worker"`, "--limit", "10")
	if code != exitOK {
		t.Fatalf("conf search exit=%d output=%s", code, confOut)
	}
	var confSearch app.ConfluenceSearchResult
	if err := json.Unmarshal([]byte(confOut), &confSearch); err != nil {
		t.Fatal(err)
	}
	if !confSearch.Complete || confSearch.Truncated || confSearch.Count != 3 || confSearch.Results[0].ID != "8101" {
		t.Fatalf("conf search=%+v", confSearch)
	}

	jiraOut, code := runCLI(t, env, "jira", "issue", "search", "--jql", `text ~ "Orchid retry worker" ORDER BY updated DESC`, "--columns", "key,summary,status,updated", "--limit", "10")
	if code != exitOK {
		t.Fatalf("jira search exit=%d output=%s", code, jiraOut)
	}
	var jiraSearch app.IssueList
	if err := json.Unmarshal([]byte(jiraOut), &jiraSearch); err != nil {
		t.Fatal(err)
	}
	if !jiraSearch.Page.Complete || jiraSearch.Page.Truncated || len(jiraSearch.Rows) != 3 || jiraSearch.Rows[0].Key != "OPS-42" {
		t.Fatalf("jira search=%+v", jiraSearch)
	}

	outline, code := runCLI(t, env, "conf", "page", "outline", "8101")
	if code != exitOK || !strings.Contains(outline, `"title": "Decision"`) {
		t.Fatalf("outline exit=%d output=%s", code, outline)
	}
	section, code := runCLI(t, env, "conf", "page", "section", "8101", "--heading", "Decision", "--max-bytes", "32768")
	if code != exitOK || !strings.Contains(section, "25 percent") || !strings.Contains(section, "Runtime") || strings.Contains(section, "Historical capacity") {
		t.Fatalf("section exit=%d output=%s", code, section)
	}

	fieldOut, code := runCLI(t, env, "jira", "issue", "field", "get", "OPS-42", "--field", "Description", "--max-bytes", "16384")
	if code != exitOK {
		t.Fatalf("field get exit=%d output=%s", code, fieldOut)
	}
	var field app.JiraIssueFieldEvidenceResult
	if err := json.Unmarshal([]byte(fieldOut), &field); err != nil {
		t.Fatal(err)
	}
	value, _ := field.Value.(string)
	if !field.Complete || !strings.Contains(value, "Capacity test pending") {
		t.Fatalf("field=%+v", field)
	}

	methods, unexpected, duplicates := backend.Summary()
	if methods["GET"] != 6 || len(methods) != 1 || unexpected != 0 || duplicates != 1 {
		t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
	}
}

func TestSyntheticConfluenceSearchTextKeepsQualificationAndCandidateEvidence(t *testing.T) {
	backend := startAgentEvalFixture(t, "cross-service-topic-discovery")
	out, code := runCLI(t, backend.Environment(), "-o", "text", "conf", "search", "--cql", `siteSearch ~ "Orchid retry worker"`, "--limit", "10")
	if code != exitOK || !strings.Contains(out, "complete: true; rows: 3") || !strings.Contains(out, "| ID | Version | Space | Title | Excerpt |") || !strings.Contains(out, "Current operating decision") {
		t.Fatalf("search exit=%d output=%s", code, out)
	}
	methods, unexpected, duplicates := backend.Summary()
	if methods["GET"] != 1 || len(methods) != 1 || unexpected != 0 || duplicates != 0 {
		t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
	}
}
