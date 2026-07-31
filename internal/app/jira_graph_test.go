package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type jiraGraphTracker struct {
	domain.Tracker
	snapshot    *domain.QualifiedIssueSnapshot
	snapshotErr error
	comments    []domain.Comment
	commentsErr error
	worklogs    *domain.IssueWorklogList
	worklogsErr error
	remote      domain.JiraRemoteLinkInventory
	remoteErr   error
	snapshots   int
}

func (t *jiraGraphTracker) ReadIssueSnapshot(context.Context, string) (*domain.QualifiedIssueSnapshot, error) {
	t.snapshots++
	return t.snapshot, t.snapshotErr
}

func (t *jiraGraphTracker) ListComments(context.Context, string) ([]domain.Comment, error) {
	return t.comments, t.commentsErr
}

func (t *jiraGraphTracker) ListIssueWorklogs(context.Context, string) (*domain.IssueWorklogList, error) {
	return t.worklogs, t.worklogsErr
}

func (t *jiraGraphTracker) AddIssueWorklog(context.Context, string, domain.IssueWorklogCreate) (*domain.IssueWorklog, error) {
	panic("unexpected write")
}

func (t *jiraGraphTracker) ReadIssueRemoteLinks(context.Context, string) (domain.JiraRemoteLinkInventory, error) {
	return t.remote, t.remoteErr
}

func completeGraphFixture() *jiraGraphTracker {
	fields := map[string]any{
		"summary":     "Graph seed",
		"description": "See PROJ-2 and https://jira.example.test/browse/PROJ-3?token=secret#fragment plus pageId=44",
		"labels":      []any{"NOT-A-REFERENCE-9"},
		"parent":      map[string]any{"id": "10004", "key": "PROJ-4", "self": "https://private.invalid/api"},
		"subtasks":    []any{map[string]any{"id": "10005", "key": "PROJ-5"}},
		"issuelinks": []any{map[string]any{
			"id": "7",
			"type": map[string]any{
				"name": "Blocks", "inward": "is blocked by", "outward": "blocks",
			},
			"inwardIssue": map[string]any{"id": "10002", "key": "PROJ-2"},
		}},
		"attachment": []any{map[string]any{
			"id": "81", "filename": "design.txt",
			"content": "https://private.invalid/token-value",
		}},
		"customfield_10": "PROJ-6",
		"avatarUrls":     map[string]any{"48x48": "https://private.invalid/avatar"},
	}
	issue := domain.Issue{
		ID: "10001", Key: "PROJ-1", Summary: "Graph seed", Fields: fields,
		Links: []domain.IssueLink{{
			ID: "7", Key: "PROJ-2", Type: "is blocked by", TypeName: "Blocks", Direction: "inward",
		}},
	}
	return &jiraGraphTracker{
		snapshot: &domain.QualifiedIssueSnapshot{
			RequestedKey: "PROJ-1", ID: "10001", Key: "PROJ-1", Issue: issue,
			Fields: fields,
			Names:  map[string]string{"customfield_10": "Related notes"},
			Schema: map[string]domain.IssueFieldSchema{
				"customfield_10": {Type: "string", Custom: "example:notes"},
			},
			Properties: map[string]any{
				"docs":      "pageId 45",
				"qualified": map[string]any{"pageId": json.Number("46")},
			},
		},
		comments: []domain.Comment{{ID: "9", Body: "Comment points to PROJ-7"}},
		worklogs: &domain.IssueWorklogList{
			Worklogs: []domain.IssueWorklog{{ID: "11", Comment: "Worked with PROJ-8"}},
			Total:    1, Complete: true,
		},
		remote: domain.JiraRemoteLinkInventory{
			Links: []domain.JiraRemoteLink{{
				ID: "12", Relationship: "documents",
				ObjectURL:   "https://docs.example.test/pages/9?signature=secret",
				ObjectTitle: "Remote design",
			}},
			Total: 1,
		},
	}
}

func TestIssueGraphBuildsDeterministicQualifiedDirectGraph(t *testing.T) {
	tracker := completeGraphFixture()
	service := &JiraService{
		tr: tracker, baseURL: "https://jira.example.test",
	}
	first, err := service.IssueGraph(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.IssueGraph(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("graph is nondeterministic:\n%s\n%s", firstJSON, secondJSON)
	}
	if tracker.snapshots != 2 {
		t.Fatalf("snapshot reads = %d, want 2", tracker.snapshots)
	}
	if !first.Complete || first.Truncated || first.Bounds.ExpandedNodes != 1 || first.Bounds.FollowedNodes != 0 {
		t.Fatalf("qualification = %#v", first)
	}
	if first.Summary.NodeCount != len(first.Nodes) || first.Summary.EdgeCount != len(first.Edges) ||
		first.Summary.SourceCount != len(first.Sources) || !first.Summary.EvidenceCountMatchesEdges ||
		len(first.Summary.SourceStatusCounts) != 6 || !first.Summary.SourceStatusCountsMatch ||
		!first.Summary.IncompleteCountMatches {
		t.Fatalf("summary = %#v", first.Summary)
	}
	if source := graphSourceByKind(t, first, "issue_properties"); source.Stability != domain.ArtifactStabilityPublicAPI {
		t.Fatalf("properties source = %#v", source)
	}
	output := string(firstJSON)
	for _, forbidden := range []string{"private.invalid", "token-value", "avatar", "signature=secret", "token=secret"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("output contains forbidden value %q: %s", forbidden, output)
		}
	}
	for _, wanted := range []string{
		`"id":"jira:issue:PROJ-2"`, `"kind":"jira_link"`,
		`"kind":"child_of"`, `"kind":"parent_of"`,
		`"id":"jira:attachment:81"`, `"id":"confluence:page:44"`,
		`"id":"confluence:page:46"`,
		`"external_id":"PROJ-7"`, `"collector":"worklogs"`,
	} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("output missing %q: %s", wanted, output)
		}
	}
}

func TestIssueGraphQualifiesAuxiliaryFailuresWithoutDiscardingSeed(t *testing.T) {
	tracker := completeGraphFixture()
	tracker.comments = nil
	tracker.commentsErr = domain.ErrForbidden
	tracker.worklogs = nil
	tracker.worklogsErr = errors.New("temporary transport detail")
	tracker.remote = domain.JiraRemoteLinkInventory{}
	tracker.remoteErr = domain.ErrNotFound

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraph(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Summary.IncompleteSourceCount != 3 {
		t.Fatalf("qualification = %#v", result.Summary)
	}
	statuses := map[string]domain.ArtifactGraphSource{}
	for _, source := range result.Sources {
		statuses[source.Kind] = source
	}
	if statuses["comments"].Status != domain.ArtifactSourceForbidden ||
		statuses["worklogs"].Status != domain.ArtifactSourcePartial ||
		statuses["worklogs"].PartialReason != domain.ArtifactPartialRequestFailed ||
		statuses["remote_links"].Status != domain.ArtifactSourceUnsupported {
		t.Fatalf("sources = %#v", statuses)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "temporary transport detail") {
		t.Fatalf("raw error leaked: %s", encoded)
	}
}

func TestIssueGraphRejectsInvalidKeyBeforeSnapshot(t *testing.T) {
	tracker := completeGraphFixture()
	_, err := (&JiraService{tr: tracker}).IssueGraph(context.Background(), "not a key")
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("error = %v", err)
	}
	if tracker.snapshots != 0 {
		t.Fatalf("snapshot reads = %d", tracker.snapshots)
	}
}

func TestIssueGraphRejectsMismatchedSnapshotIdentity(t *testing.T) {
	tracker := completeGraphFixture()
	tracker.snapshot.Key = "PROJ-2"
	tracker.snapshot.Issue.Key = "PROJ-2"

	_, err := (&JiraService{tr: tracker}).IssueGraph(context.Background(), "PROJ-1")
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v", err)
	}
}

func TestExtractGraphReferencesRedactsQueriesAndDoesNotDuplicateURLKey(t *testing.T) {
	refs := extractGraphReferences(
		"https://jira.example.test/browse/PROJ-9?token=secret#frag",
		"https://jira.example.test", "", true,
	)
	if len(refs) != 1 {
		t.Fatalf("refs = %#v", refs)
	}
	if refs[0].Node.ID != "jira:issue:PROJ-9" || strings.Contains(refs[0].Node.URL, "secret") ||
		strings.Contains(refs[0].Node.URL, "#") {
		t.Fatalf("ref = %#v", refs[0])
	}
}

func TestExtractGraphReferencesRemovesUnknownQueryValuesAndSensitivePaths(t *testing.T) {
	refs := extractGraphReferences(
		"https://example.test/artifact/12?ordinary=private-query-canary&x-amz-signature=private-signature-canary",
		"", "", true,
	)
	if len(refs) != 1 || refs[0].Node.URL != "https://example.test/artifact/12?redacted=redacted" {
		t.Fatalf("query ref = %#v", refs)
	}
	encoded, _ := json.Marshal(refs)
	if strings.Contains(string(encoded), "private-query-canary") ||
		strings.Contains(string(encoded), "private-signature-canary") {
		t.Fatalf("query value leaked: %s", encoded)
	}

	refs = extractGraphReferences(
		"https://example.test/download/session-private-path-canary-0123456789",
		"", "", true,
	)
	if len(refs) != 1 || refs[0].Node.URL != "" ||
		!strings.HasPrefix(refs[0].Node.ID, "candidate:url:") {
		t.Fatalf("sensitive path ref = %#v", refs)
	}
	encoded, _ = json.Marshal(refs)
	if strings.Contains(string(encoded), "private-path-canary") {
		t.Fatalf("path value leaked: %s", encoded)
	}

	longAlphabeticToken := "abcdefghijklmnopqrstuvwxyzabcdef"
	refs = extractGraphReferences(
		"https://example.test/invite/"+longAlphabeticToken,
		"", "", true,
	)
	if len(refs) != 1 || refs[0].Node.URL != "" {
		t.Fatalf("long path token ref = %#v", refs)
	}
	encoded, _ = json.Marshal(refs)
	if strings.Contains(string(encoded), longAlphabeticToken) {
		t.Fatalf("long path token leaked: %s", encoded)
	}
}

func TestExtractGraphReferencesPreservesConfluenceQueryIdentityAfterRedaction(t *testing.T) {
	refs := extractGraphReferences(
		"https://conf.example.test/pages/viewpage.action?pageId=123&ordinary=private-query-canary",
		"", "https://conf.example.test", true,
	)
	if len(refs) != 1 || refs[0].Node.ID != "confluence:page:123" ||
		refs[0].Node.URL != "https://conf.example.test/pages/viewpage.action?redacted=redacted" {
		t.Fatalf("refs = %#v", refs)
	}
	encoded, _ := json.Marshal(refs)
	if strings.Contains(string(encoded), "private-query-canary") {
		t.Fatalf("query leaked: %s", encoded)
	}
}

func TestWalkGraphValueStopsAtInspectionBounds(t *testing.T) {
	budget := &graphExtractBudget{MaxBytes: 3}
	visited := 0
	walkGraphValue(map[string]any{"a": "four", "b": "x"}, "/fields/custom", true, budget,
		func(any, string, bool) { visited++ })
	if !budget.Clipped || visited != 0 {
		t.Fatalf("budget = %#v, visited = %d", budget, visited)
	}
}

func TestWalkGraphValueBoundsContainersAndPointerTokens(t *testing.T) {
	largeObject := map[string]any{}
	for index := 0; index <= graphWalkMaxObject; index++ {
		largeObject[fmt.Sprintf("key-%03d", index)] = nil
	}
	for name, value := range map[string]any{
		"object": largeObject,
		"array":  make([]any, graphWalkMaxArray+1),
	} {
		t.Run(name, func(t *testing.T) {
			budget := &graphExtractBudget{MaxBytes: jiraGraphMaxSourceBytes}
			visited := 0
			walkGraphValue(value, "/properties/root", true, budget, func(any, string, bool) {
				visited++
			})
			if !budget.Clipped || visited != 0 {
				t.Fatalf("budget = %#v, visited = %d", budget, visited)
			}
		})
	}

	pointer := "/properties/" + strings.Repeat("x", graphPointerMaxBytes)
	budget := &graphExtractBudget{MaxBytes: jiraGraphMaxSourceBytes}
	visited := 0
	walkGraphValue("PROJ-2", pointer, true, budget, func(any, string, bool) {
		visited++
	})
	if !budget.Clipped || visited != 0 {
		t.Fatalf("pointer budget = %#v, visited = %d", budget, visited)
	}
}

func TestIssueGraphQualifiesMalformedStructuredRows(t *testing.T) {
	tracker := completeGraphFixture()
	tracker.snapshot.Fields["issuelinks"] = []any{
		"malformed-row",
		map[string]any{
			"id": "8",
			"type": map[string]any{
				"name": "Relates", "outward": "relates to",
			},
			"outwardIssue": map[string]any{"id": "10009", "key": "PROJ-9"},
		},
	}

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraph(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	source := graphSourceByKind(t, result, "issue_links")
	if source.Status != domain.ArtifactSourcePartial ||
		source.PartialReason != domain.ArtifactPartialMalformed ||
		source.Complete || source.Count != 2 {
		t.Fatalf("source = %#v", source)
	}
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), `"external_id":"PROJ-9"`) ||
		strings.Contains(string(encoded), "malformed-row") {
		t.Fatalf("qualified projection = %s", encoded)
	}
}

func TestIssueGraphTreatsOmittedOptionalHierarchyAsEmpty(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"omitted": func(fields map[string]any) {
			delete(fields, "parent")
			delete(fields, "subtasks")
		},
		"null": func(fields map[string]any) {
			fields["parent"] = nil
			fields["subtasks"] = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			tracker := completeGraphFixture()
			mutate(tracker.snapshot.Fields)

			result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraph(context.Background(), "PROJ-1")
			if err != nil {
				t.Fatal(err)
			}
			source := graphSourceByKind(t, result, "hierarchy")
			if source.Status != domain.ArtifactSourceEmpty || !source.Complete || source.Count != 0 {
				t.Fatalf("source = %#v", source)
			}
		})
	}
}

func TestIssueGraphKeepsPresentMalformedHierarchyPartial(t *testing.T) {
	tracker := completeGraphFixture()
	tracker.snapshot.Fields["parent"] = "PROJ-4"
	tracker.snapshot.Fields["subtasks"] = map[string]any{"key": "PROJ-5"}

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraph(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	source := graphSourceByKind(t, result, "hierarchy")
	if source.Status != domain.ArtifactSourcePartial ||
		source.PartialReason != domain.ArtifactPartialMalformed || source.Complete {
		t.Fatalf("source = %#v", source)
	}
}

func TestIssueGraphSanitizesIdentitySubtreesAndPropertyKeys(t *testing.T) {
	tracker := completeGraphFixture()
	tracker.snapshot.Properties["private-property-key-canary"] = map[string]any{
		"icon":  map[string]any{"url": "https://private-icon-canary.example.test/a"},
		"owner": map[string]any{"profile": "https://private-owner-canary.example.test/u"},
		"safe":  map[string]any{"pageId": json.Number("47")},
		"by_id": map[string]any{"123456789012": "PROJ-88"},
	}
	tracker.snapshot.Fields["customfield_99"] = map[string]any{
		"url": "https://private-user-canary.example.test/u",
	}
	tracker.snapshot.Schema["customfield_99"] = domain.IssueFieldSchema{Type: "user"}

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraph(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	for _, forbidden := range []string{
		"private-property-key-canary", "private-icon-canary",
		"private-owner-canary", "private-user-canary", "123456789012",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("output contains %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"id":"confluence:page:47"`) {
		t.Fatalf("numeric page id missing: %s", encoded)
	}
}

func TestIssueGraphRejectsNonCanonicalStructuredPageID(t *testing.T) {
	tracker := completeGraphFixture()
	tracker.snapshot.Properties["invalid"] = map[string]any{"pageId": json.Number("1e3")}

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraph(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	source := graphSourceByKind(t, result, "issue_properties")
	if source.Status != domain.ArtifactSourcePartial ||
		source.PartialReason != domain.ArtifactPartialMalformed {
		t.Fatalf("source = %#v", source)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "1e3") {
		t.Fatalf("invalid page id leaked: %s", encoded)
	}
}

func TestIssueGraphReconcilesBareAndStructuredJiraIdentityRegardlessOfSourceOrder(t *testing.T) {
	tracker := completeGraphFixture()
	tracker.snapshot.Fields["description"] = "PROJ-9"
	tracker.remote = domain.JiraRemoteLinkInventory{
		Links: []domain.JiraRemoteLink{{
			ID: "19", ObjectURL: "https://jira.example.test/browse/PROJ-9",
		}},
		Total: 1,
	}

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraph(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	matches := 0
	for _, node := range result.Nodes {
		if node.ID == "jira:issue:PROJ-9" {
			matches++
			if node.State != domain.ArtifactNodeStub {
				t.Fatalf("node = %#v", node)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("matching nodes = %d, nodes = %#v", matches, result.Nodes)
	}
}

func TestIssueGraphNeverExceedsFixedNodeOrEdgeBounds(t *testing.T) {
	tracker := completeGraphFixture()
	var description strings.Builder
	for index := 1; index <= jiraGraphMaxNodes+100; index++ {
		description.WriteString(" https://example.test/artifact/")
		fmt.Fprint(&description, index)
	}
	tracker.snapshot.Fields["description"] = description.String()
	tracker.snapshot.Issue.Fields["description"] = description.String()

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraph(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.Complete || len(result.Nodes) > jiraGraphMaxNodes ||
		len(result.Edges) > jiraGraphMaxEdges || result.Summary.EvidenceCount > jiraGraphMaxEvidence {
		t.Fatalf("bounds not enforced: nodes=%d edges=%d evidence=%d complete=%t truncated=%t",
			len(result.Nodes), len(result.Edges), result.Summary.EvidenceCount, result.Complete, result.Truncated)
	}
}

func graphSourceByKind(t *testing.T, result *JiraIssueGraphResult, kind string) domain.ArtifactGraphSource {
	t.Helper()
	for _, source := range result.Sources {
		if source.Kind == kind {
			return source
		}
	}
	t.Fatalf("source %q not found", kind)
	return domain.ArtifactGraphSource{}
}
