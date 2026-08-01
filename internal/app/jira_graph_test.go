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
				"summary":        {Type: "string", System: "summary"},
				"description":    {Type: "string", System: "description"},
				"labels":         {Type: "array", Items: "string", System: "labels"},
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
	first, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
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
	for _, source := range first.Sources {
		if want := jiraGraphSourceStability(source.Kind); source.Stability != want {
			t.Fatalf("source %q stability = %q, want %q", source.Kind, source.Stability, want)
		}
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

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
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
	_, err := (&JiraService{tr: tracker}).IssueGraphWithOptions(context.Background(), "not a key", JiraIssueGraphOptions{})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("error = %v", err)
	}
	if tracker.snapshots != 0 {
		t.Fatalf("snapshot reads = %d", tracker.snapshots)
	}
}

func TestIssueGraphPreservesTrimmedCLIKeyCompatibility(t *testing.T) {
	tracker := completeGraphFixture()
	result, err := (&JiraService{tr: tracker}).IssueGraphWithOptions(context.Background(), "  PROJ-1  ", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.RootID != "jira:issue:PROJ-1" || tracker.snapshots != 1 {
		t.Fatalf("result=%+v snapshots=%d", result, tracker.snapshots)
	}
}

func TestIssueGraphRejectsMismatchedSnapshotIdentity(t *testing.T) {
	tracker := completeGraphFixture()
	tracker.snapshot.RequestedKey = "PROJ-2"

	_, err := (&JiraService{tr: tracker}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
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

func TestExtractGraphReferencesFindsAdjacentBareJiraKeysWithoutEmbeddedFalsePositives(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{name: "spaces", text: "PROJ-1 PROJ-2", want: []string{"jira:issue:PROJ-1", "jira:issue:PROJ-2"}},
		{name: "comma", text: "PROJ-1,PROJ-2", want: []string{"jira:issue:PROJ-1", "jira:issue:PROJ-2"}},
		{name: "parentheses", text: "(PROJ-1)(PROJ-2)", want: []string{"jira:issue:PROJ-1", "jira:issue:PROJ-2"}},
		{name: "embedded", text: "xPROJ-1 PROJ-2x _PROJ-3 PROJ-4_ 9PROJ-5 PROJ-6z", want: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			refs := extractGraphReferences(test.text, "", "", true)
			got := make([]string, 0, len(refs))
			for _, ref := range refs {
				if ref.Extraction == "jira_key" {
					got = append(got, ref.Node.ID)
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(test.want) {
				t.Fatalf("keys = %#v, want %#v", got, test.want)
			}
		})
	}
}

func FuzzExtractGraphReferencesBareJiraKeyBoundaries(f *testing.F) {
	for _, seed := range [][2]byte{{' ', ' '}, {',', ')'}, {'(', '('}, {'x', ' '}, {'_', ','}, {'9', 'x'}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, left, right byte) {
		text := string([]byte{left}) + "PROJ-1" + string([]byte{right})
		refs := extractGraphReferences(text, "", "", true)
		found := false
		for _, ref := range refs {
			found = found || ref.Extraction == "jira_key" && ref.Node.ID == "jira:issue:PROJ-1"
		}
		want := !graphASCIIWordByte(left) && !graphASCIIWordByte(right)
		if found != want {
			t.Fatalf("boundaries (%q, %q): found=%t want=%t refs=%#v", left, right, found, want, refs)
		}
	})
}

func FuzzExtractGraphReferencesAdjacentBareJiraKeys(f *testing.F) {
	for _, separator := range []byte{' ', ',', ')', '(', '-', '\n', 'x', '_', '9'} {
		f.Add(separator)
	}
	f.Fuzz(func(t *testing.T, separator byte) {
		refs := extractGraphReferences("PROJ-1"+string([]byte{separator})+"PROJ-2", "", "", true)
		found := map[string]bool{}
		for _, ref := range refs {
			if ref.Extraction == "jira_key" {
				found[ref.Node.ID] = true
			}
		}
		wantBoth := !graphASCIIWordByte(separator)
		if found["jira:issue:PROJ-1"] != wantBoth || found["jira:issue:PROJ-2"] != wantBoth {
			t.Fatalf("separator %q: keys=%#v wantBoth=%t", separator, found, wantBoth)
		}
	})
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

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
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

			result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
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

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source := graphSourceByKind(t, result, "hierarchy")
	if source.Status != domain.ArtifactSourcePartial ||
		source.PartialReason != domain.ArtifactPartialMalformed || source.Complete {
		t.Fatalf("source = %#v", source)
	}
}

func TestIssueGraphInvalidFieldSchemaSkipsInspectionAndQualifiesSource(t *testing.T) {
	tests := []struct {
		name   string
		schema *domain.IssueFieldSchema
	}{
		{name: "missing"},
		{name: "zero", schema: &domain.IssueFieldSchema{}},
		{name: "blank type", schema: &domain.IssueFieldSchema{Type: "  "}},
		{name: "unknown type", schema: &domain.IssueFieldSchema{Type: "opaque"}},
		{name: "system any", schema: &domain.IssueFieldSchema{Type: "any", System: "description"}},
		{name: "array missing items", schema: &domain.IssueFieldSchema{Type: "array"}},
		{name: "array wildcard items", schema: &domain.IssueFieldSchema{Type: "array", Items: "any"}},
		{name: "array unknown items", schema: &domain.IssueFieldSchema{Type: "array", Items: "opaque"}},
		{name: "mismatched system", schema: &domain.IssueFieldSchema{Type: "string", System: "summary"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker := completeGraphFixture()
			tracker.snapshot.Fields["description"] = []any{"PROJ-90", "https://docs.example.test/reference/schema-canary"}
			if test.schema == nil {
				delete(tracker.snapshot.Schema, "description")
			} else {
				tracker.snapshot.Schema["description"] = *test.schema
			}

			result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
			if err != nil {
				t.Fatal(err)
			}
			source := graphSourceByKind(t, result, "issue_fields")
			if source.Status != domain.ArtifactSourcePartial || source.PartialReason != domain.ArtifactPartialMalformed || source.Complete {
				t.Fatalf("source = %#v", source)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "PROJ-90") || strings.Contains(string(encoded), "schema-canary") {
				t.Fatalf("field with invalid schema was inspected: %s", encoded)
			}
		})
	}
}

func TestIssueGraphEmptyFieldNeedsNoWalkerMetadata(t *testing.T) {
	for _, value := range []any{nil, "", "  ", []any{}, map[string]any{}, json.Number("7"), float64(7), true} {
		tracker := completeGraphFixture()
		tracker.snapshot.Fields["customfield_11"] = value
		delete(tracker.snapshot.Names, "customfield_11")
		delete(tracker.snapshot.Schema, "customfield_11")

		result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
		if err != nil {
			t.Fatal(err)
		}
		source := graphSourceByKind(t, result, "issue_fields")
		if !source.Complete || source.Status != domain.ArtifactSourceComplete || source.Count != len(tracker.snapshot.Fields) {
			t.Fatalf("value %#v source = %#v", value, source)
		}
	}
}

func TestIssueGraphReferenceBearingFieldRequiresWalkerMetadata(t *testing.T) {
	for _, value := range []any{
		"PROJ-97 https://docs.example.test/reference/string",
		[]any{"PROJ-97", "https://docs.example.test/reference/array"},
		map[string]any{"note": "PROJ-97 https://docs.example.test/reference/object"},
		struct{ Text string }{Text: "PROJ-97"},
	} {
		tracker := completeGraphFixture()
		tracker.snapshot.Fields["customfield_11"] = value
		delete(tracker.snapshot.Names, "customfield_11")
		delete(tracker.snapshot.Schema, "customfield_11")

		result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
		if err != nil {
			t.Fatal(err)
		}
		source := graphSourceByKind(t, result, "issue_fields")
		if source.Status != domain.ArtifactSourcePartial || source.PartialReason != domain.ArtifactPartialMalformed {
			t.Fatalf("value %#v source = %#v", value, source)
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "PROJ-97") || strings.Contains(string(encoded), "reference/") {
			t.Fatalf("unqualified value %#v was inspected: %s", value, encoded)
		}
	}
}

func TestIssueGraphMissingCustomFieldNameDisablesBareKeysAndQualifiesSource(t *testing.T) {
	tracker := completeGraphFixture()
	tracker.snapshot.Fields["customfield_10"] = "PROJ-91 https://docs.example.test/reference/custom-field"
	delete(tracker.snapshot.Names, "customfield_10")

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source := graphSourceByKind(t, result, "issue_fields")
	if source.Status != domain.ArtifactSourcePartial || source.PartialReason != domain.ArtifactPartialMalformed || source.Complete {
		t.Fatalf("source = %#v", source)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "PROJ-91") || !strings.Contains(string(encoded), "https://docs.example.test/reference/custom-field") {
		t.Fatalf("custom-field qualification = %s", encoded)
	}
}

func TestIssueGraphKnownAnySchemaAllowsURLsWithoutBareKeyInference(t *testing.T) {
	tracker := completeGraphFixture()
	tracker.snapshot.Fields["customfield_10"] = "PROJ-94 https://docs.example.test/reference/any-field"
	tracker.snapshot.Schema["customfield_10"] = domain.IssueFieldSchema{Type: "any", Custom: "example:any"}

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source := graphSourceByKind(t, result, "issue_fields")
	if !source.Complete || source.Status != domain.ArtifactSourceComplete {
		t.Fatalf("source = %#v", source)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "PROJ-94") || !strings.Contains(string(encoded), "https://docs.example.test/reference/any-field") {
		t.Fatalf("any-schema qualification = %s", encoded)
	}
}

func TestIssueGraphKnownAnySchemaStillSuppressesIdentitySubtrees(t *testing.T) {
	tracker := completeGraphFixture()
	tracker.snapshot.Fields["customfield_10"] = map[string]any{
		"owner": "PROJ-95 https://identity.example.test/owner",
		"note":  "https://docs.example.test/reference/visible",
	}
	tracker.snapshot.Schema["customfield_10"] = domain.IssueFieldSchema{Type: "any", Custom: "example:any"}

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "PROJ-95") || strings.Contains(string(encoded), "identity.example.test") ||
		!strings.Contains(string(encoded), "https://docs.example.test/reference/visible") {
		t.Fatalf("any-schema identity suppression = %s", encoded)
	}
}

func TestIssueGraphRejectsContradictoryFieldSchemaDiscriminators(t *testing.T) {
	tests := []struct {
		name    string
		fieldID string
		schema  domain.IssueFieldSchema
	}{
		{name: "custom id with system discriminator", fieldID: "customfield_10", schema: domain.IssueFieldSchema{Type: "string", System: "summary"}},
		{name: "system id with custom discriminator", fieldID: "description", schema: domain.IssueFieldSchema{Type: "string", System: "description", Custom: "example:description"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker := completeGraphFixture()
			tracker.snapshot.Fields[test.fieldID] = "PROJ-96 https://docs.example.test/reference/contradictory"
			tracker.snapshot.Schema[test.fieldID] = test.schema

			result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
			if err != nil {
				t.Fatal(err)
			}
			source := graphSourceByKind(t, result, "issue_fields")
			if source.Status != domain.ArtifactSourcePartial || source.PartialReason != domain.ArtifactPartialMalformed {
				t.Fatalf("source = %#v", source)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "PROJ-96") || strings.Contains(string(encoded), "contradictory") {
				t.Fatalf("contradictory field schema was inspected: %s", encoded)
			}
		})
	}
}

func TestIssueGraphIgnoresExtraFieldMetadata(t *testing.T) {
	baselineTracker := completeGraphFixture()
	baseline, err := (&JiraService{tr: baselineTracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	extraTracker := completeGraphFixture()
	extraTracker.snapshot.Names["customfield_999"] = "Unused notes"
	extraTracker.snapshot.Schema["customfield_999"] = domain.IssueFieldSchema{Type: "string", Custom: "example:unused"}
	extra, err := (&JiraService{tr: extraTracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	baselineJSON, _ := json.Marshal(baseline)
	extraJSON, _ := json.Marshal(extra)
	if string(baselineJSON) != string(extraJSON) {
		t.Fatalf("extra metadata changed graph:\n%s\n%s", baselineJSON, extraJSON)
	}
}

func TestIssueGraphUnknownFieldIDIsPartialAndCannotEnableBareScanning(t *testing.T) {
	tracker := completeGraphFixture()
	tracker.snapshot.Fields["narrative_alias"] = "PROJ-92 https://docs.example.test/reference/unknown-field"
	tracker.snapshot.Names["narrative_alias"] = "Delivery notes"
	tracker.snapshot.Schema["narrative_alias"] = domain.IssueFieldSchema{Type: "string"}

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source := graphSourceByKind(t, result, "issue_fields")
	if source.Status != domain.ArtifactSourcePartial || source.PartialReason != domain.ArtifactPartialMalformed {
		t.Fatalf("source = %#v", source)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "PROJ-92") || !strings.Contains(string(encoded), "https://docs.example.test/reference/unknown-field") {
		t.Fatalf("unknown field qualification = %s", encoded)
	}
}

func TestIssueGraphAcceptsSystemFieldIdentityFromSchemaWithoutBareInference(t *testing.T) {
	tracker := completeGraphFixture()
	tracker.snapshot.Fields["systemnotes"] = "PROJ-93 https://docs.example.test/reference/system-field"
	tracker.snapshot.Names["systemnotes"] = "Delivery notes"
	tracker.snapshot.Schema["systemnotes"] = domain.IssueFieldSchema{Type: "string", System: "systemnotes"}

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source := graphSourceByKind(t, result, "issue_fields")
	if !source.Complete || source.Status != domain.ArtifactSourceComplete {
		t.Fatalf("source = %#v", source)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "PROJ-93") || !strings.Contains(string(encoded), "https://docs.example.test/reference/system-field") {
		t.Fatalf("system field qualification = %s", encoded)
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

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
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

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
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

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
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

	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{
		MaxNodes: jiraGraphMaxNodes, MaxEdges: jiraGraphMaxEdges, MaxEvidence: jiraGraphMaxEvidence,
	})
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
