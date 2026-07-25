package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestPlanningCSVNeutralizesFormulaCellsByDefault(t *testing.T) {
	rows := []PlanningIssueQuality{{
		Key: "=KEY", Level: "+level", Gaps: []string{"@gap", "-risk"}, Children: []string{"=CHILD"},
	}}
	safe, err := renderPlanningCSV(rows, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'=KEY", "'+level", "'@gap", "'=CHILD"} {
		if !strings.Contains(string(safe), want) {
			t.Fatalf("safe CSV missing %q: %q", want, safe)
		}
	}
	raw, err := renderPlanningCSV(rows, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "'=KEY") || !strings.Contains(string(raw), "=KEY") {
		t.Fatalf("raw CSV = %q", raw)
	}
}

func TestPlanningRawCSVRequiresCSVPath(t *testing.T) {
	svc := &JiraService{}
	_, err := svc.PlanningReport(context.Background(), PlanningReportOpts{JQL: "project = PROJ", RawCSV: true})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("error = %v, want usage", err)
	}
}

func TestExtractPlanningRefsClassifiesAndDedupes(t *testing.T) {
	refs := ExtractPlanningRefs("See https://figma.com/file/abc and https://docs.example.com/spec. Again https://figma.com/file/abc")
	if len(refs) != 2 {
		t.Fatalf("refs = %+v, want 2 unique refs", refs)
	}
	kinds := refs[0].Kind + "," + refs[1].Kind
	if !strings.Contains(kinds, "design") || !strings.Contains(kinds, "doc") {
		t.Fatalf("refs = %+v, want design and doc", refs)
	}
	for _, kind := range []string{"chat", "design", "doc", "jira", "link"} {
		if !JiraPlanningReferenceKind(kind) {
			t.Fatalf("known reference kind %q was rejected", kind)
		}
	}
	if JiraPlanningReferenceKind("private.example") || JiraPlanningReferenceKind("") {
		t.Fatal("unknown reference kind was accepted")
	}
}

func TestPlanningReportScoresGapsRefsAndEpicChildren(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "planning.csv")
	svc := &JiraService{tr: partialTracker{issues: []domain.Issue{
		{
			Key:      "PROJ-1",
			Summary:  "Parent",
			Type:     "Epic",
			Assignee: "Alice",
			Body:     "Context https://docs.example.com/spec",
			Fields:   map[string]any{"estimate": 8},
		},
		{
			Key:      "PROJ-2",
			Summary:  "Child",
			Type:     "Story",
			Assignee: "",
			Body:     "",
			Fields:   map[string]any{"epic": "PROJ-1"},
		},
	}}}

	report, err := svc.PlanningReport(context.Background(), PlanningReportOpts{
		JQL:           "project = PROJ",
		Required:      []string{"estimate"},
		EstimateField: "estimate",
		EpicField:     "epic",
		Limit:         100,
		CSVPath:       csvPath,
	})
	if err != nil {
		t.Fatalf("PlanningReport: %v", err)
	}
	if report.Count != 2 || report.CSVPath != csvPath {
		t.Fatalf("report = %+v, want count/csv path", report)
	}
	if _, err := os.ReadFile(csvPath); err != nil {
		t.Fatalf("csv was not written: %v", err)
	}
	epic := report.Issues[0]
	if epic.Key != "PROJ-1" || strings.Join(epic.Children, ",") != "PROJ-2" || len(epic.Refs) != 1 {
		t.Fatalf("epic row = %+v, want child PROJ-2 and one ref", epic)
	}
	child := report.Issues[1]
	gaps := strings.Join(child.Gaps, ",")
	for _, want := range []string{"missing_description", "missing_assignee", "missing_estimate", "missing_artifact_ref"} {
		if !strings.Contains(gaps, want) {
			t.Fatalf("child gaps = %v, want %s", child.Gaps, want)
		}
	}
}

func TestIssueRefsSupportsKeyAndJQL(t *testing.T) {
	svc := &JiraService{tr: partialTracker{issues: []domain.Issue{
		{
			Key:     "PROJ-2",
			Summary: "Second",
			Type:    "Story",
			Body:    "Spec https://docs.example.com/spec",
			Comments: []domain.Comment{{
				Body: "Design https://figma.com/file/abc",
			}},
		},
		{
			Key:     "PROJ-1",
			Summary: "First",
			Type:    "Bug",
			Body:    "No links",
		},
	}}}

	one, err := svc.IssueRefs(context.Background(), JiraIssueRefsOpts{Key: "PROJ-2"})
	if err != nil {
		t.Fatalf("IssueRefs key: %v", err)
	}
	if one.Count != 1 || !one.Complete || !one.Selection.Complete || !one.Issues[0].Complete || one.Issues[0].Key != "PROJ-2" || len(one.Issues[0].Refs) != 2 {
		t.Fatalf("one refs = %+v, want two refs for PROJ-2", one)
	}
	if one.Summary.IssueCount != 1 || one.Summary.ReferenceCount != 2 || one.Summary.ReferenceKindCounts["doc"] != 1 || one.Summary.ReferenceKindCounts["design"] != 1 || one.Summary.SourceValueCounts["description"] != 1 || one.Summary.SourceValueCounts["comments"] != 1 {
		t.Fatalf("one summary = %+v", one.Summary)
	}
	if !one.Summary.CountMatchesIssues || !one.Summary.SelectionCountMatchesIssues || !one.Summary.ReferenceCountMatchesKinds || !one.Summary.IssueSummariesReconciled || !one.Summary.CompleteMatchesInputs || !one.Summary.TruncatedMatchesInputs {
		t.Fatalf("one reconciliation = %+v", one.Summary)
	}
	issueSummary := one.Issues[0].ReferenceSummary
	if issueSummary.ReferenceCount != 2 || issueSummary.SourceCount != 2 || issueSummary.CompleteSourceCount != 2 || issueSummary.IncompleteSourceCount != 0 || !issueSummary.ReferenceCountMatchesKinds || !issueSummary.CompleteMatchesSources || !issueSummary.TruncatedMatchesSources {
		t.Fatalf("issue summary = %+v", issueSummary)
	}

	all, err := svc.IssueRefs(context.Background(), JiraIssueRefsOpts{JQL: "project = PROJ", Limit: 10})
	if err != nil {
		t.Fatalf("IssueRefs jql: %v", err)
	}
	if all.Count != 2 || !all.Complete || all.Issues[0].Key != "PROJ-1" || all.Issues[1].Key != "PROJ-2" {
		t.Fatalf("all refs = %+v, want sorted issue rows", all.Issues)
	}
	if all.Summary.IssueCount != 2 || all.Summary.CompleteIssueCount != 2 || all.Summary.ReferenceCount != 2 || !all.Summary.IssueSummariesReconciled {
		t.Fatalf("all summary = %+v", all.Summary)
	}
}

func TestIssueRefsSummaryCountsDeduplicatedReferencesOncePerIssue(t *testing.T) {
	shared := "https://docs.example.com/spec"
	tracker := qualifiedRefsTracker{
		issues: []domain.Issue{{Key: "PROJ-1", Body: shared}, {Key: "PROJ-2", Body: shared}},
		comments: map[string][]domain.Comment{"PROJ-1": {
			{Body: shared},
			{Body: "https://figma.com/file/abc"},
		}, "PROJ-2": {}},
	}
	result, err := (&JiraService{tr: tracker}).IssueRefs(context.Background(), JiraIssueRefsOpts{JQL: "project = PROJ", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Issues[0].Refs) != 2 || len(result.Issues[1].Refs) != 1 || result.Summary.ReferenceCount != 3 || result.Summary.ReferenceKindCounts["doc"] != 2 || result.Summary.ReferenceKindCounts["design"] != 1 {
		t.Fatalf("result=%+v", result)
	}
	if intMapTotal(result.Summary.ReferenceKindCounts) != result.Summary.ReferenceCount || !result.Summary.ReferenceCountMatchesKinds {
		t.Fatalf("summary=%+v", result.Summary)
	}
}

func TestIssueRefsSummaryReconcilesEmptySelection(t *testing.T) {
	result, err := (&JiraService{tr: qualifiedRefsTracker{}}).IssueRefs(context.Background(), JiraIssueRefsOpts{JQL: "project = NONE", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Count != 0 || result.Summary.IssueCount != 0 || result.Summary.ReferenceCount != 0 || len(result.Summary.ReferenceKindCounts) != 0 || len(result.Summary.SourceValueCounts) != 0 {
		t.Fatalf("result=%+v", result)
	}
	if !result.Summary.CountMatchesIssues || !result.Summary.SelectionCountMatchesIssues || !result.Summary.ReferenceCountMatchesKinds || !result.Summary.IssueSummariesReconciled || !result.Summary.CompleteMatchesInputs || !result.Summary.TruncatedMatchesInputs {
		t.Fatalf("summary=%+v", result.Summary)
	}
}

type qualifiedRefsTracker struct {
	domain.Tracker
	issues      []domain.Issue
	pages       map[string][]domain.Issue
	next        map[string]string
	comments    map[string][]domain.Comment
	commentErrs map[string]error
	fieldDefs   []domain.FieldDef
	fieldCalls  *int
	requested   *[][]string
}

func (t qualifiedRefsTracker) Fields(context.Context) ([]domain.FieldDef, error) {
	if t.fieldCalls != nil {
		(*t.fieldCalls)++
	}
	return append([]domain.FieldDef(nil), t.fieldDefs...), nil
}

func (t qualifiedRefsTracker) GetIssue(_ context.Context, key string, fields []string) (*domain.Issue, error) {
	if t.requested != nil {
		*t.requested = append(*t.requested, append([]string(nil), fields...))
	}
	for i := range t.issues {
		if t.issues[i].Key == key {
			issue := t.issues[i]
			return &issue, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (t qualifiedRefsTracker) Search(_ context.Context, _ string, fields []string, _ int, cursor string) ([]domain.Issue, string, error) {
	if t.requested != nil {
		*t.requested = append(*t.requested, append([]string(nil), fields...))
	}
	if t.pages != nil {
		return append([]domain.Issue(nil), t.pages[cursor]...), t.next[cursor], nil
	}
	return append([]domain.Issue(nil), t.issues...), "", nil
}

func (t qualifiedRefsTracker) ListComments(_ context.Context, key string) ([]domain.Comment, error) {
	if err := t.commentErrs[key]; err != nil {
		return nil, err
	}
	if comments, ok := t.comments[key]; ok {
		return append([]domain.Comment(nil), comments...), nil
	}
	for _, issue := range t.issues {
		if issue.Key == key {
			return append([]domain.Comment(nil), issue.Comments...), nil
		}
	}
	return nil, domain.ErrNotFound
}

func TestIssueRefsExtractsQualifiedDescriptionFieldsAndComments(t *testing.T) {
	tracker := qualifiedRefsTracker{
		issues: []domain.Issue{{
			Key: "PROJ-1", Body: "description https://docs.example.com/description",
			Fields: map[string]any{"customfield_10001": "field https://docs.example.com/field"},
		}},
		comments: map[string][]domain.Comment{"PROJ-1": {{Body: "comment https://docs.example.com/comment"}}},
	}
	result, err := (&JiraService{tr: tracker}).IssueRefs(context.Background(), JiraIssueRefsOpts{
		Key: "PROJ-1", Fields: []string{"customfield_10001"},
	})
	if err != nil {
		t.Fatal(err)
	}
	issue := result.Issues[0]
	if !result.Complete || !issue.Complete || len(issue.Refs) != 3 || len(issue.Sources) != 3 {
		t.Fatalf("result=%+v issue=%+v", result, issue)
	}
	for _, source := range []string{"description", "field.customfield_10001", "comments"} {
		if !issue.Sources[source].Complete {
			t.Fatalf("source %s = %+v", source, issue.Sources[source])
		}
	}
}

func TestIssueRefsResolvesDisplayNameBeforeFetchAndExtraction(t *testing.T) {
	var fieldCalls int
	var requested [][]string
	tracker := qualifiedRefsTracker{
		issues: []domain.Issue{{
			Key:    "PROJ-1",
			Fields: map[string]any{"customfield_10001": "field https://docs.example.com/field"},
		}},
		comments:   map[string][]domain.Comment{"PROJ-1": {}},
		fieldDefs:  []domain.FieldDef{{ID: "customfield_10001", Name: "Delivery Notes", Custom: true}},
		fieldCalls: &fieldCalls,
		requested:  &requested,
	}
	result, err := (&JiraService{tr: tracker}).IssueRefs(context.Background(), JiraIssueRefsOpts{
		Key: "PROJ-1", Fields: []string{"Delivery Notes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fieldCalls != 1 || len(requested) != 1 || !slices.Contains(requested[0], "customfield_10001") || slices.Contains(requested[0], "Delivery Notes") {
		t.Fatalf("fieldCalls=%d requested=%v", fieldCalls, requested)
	}
	issue := result.Issues[0]
	if !result.Complete || len(issue.Refs) != 1 || !issue.Sources["field.customfield_10001"].Complete {
		t.Fatalf("result=%+v issue=%+v", result, issue)
	}
}

func TestIssueRefsTechnicalIDSkipsCatalog(t *testing.T) {
	var fieldCalls int
	tracker := qualifiedRefsTracker{
		issues:   []domain.Issue{{Key: "PROJ-1", Fields: map[string]any{"customfield_10001": "https://docs.example.com/field"}}},
		comments: map[string][]domain.Comment{"PROJ-1": {}}, fieldCalls: &fieldCalls,
	}
	if _, err := (&JiraService{tr: tracker}).IssueRefs(context.Background(), JiraIssueRefsOpts{Key: "PROJ-1", Fields: []string{"customfield_10001"}}); err != nil {
		t.Fatal(err)
	}
	if fieldCalls != 0 {
		t.Fatalf("field catalog calls = %d, want 0", fieldCalls)
	}
}

func TestIssueRefsRejectsUnknownAndAmbiguousDisplayNames(t *testing.T) {
	tests := []struct {
		name string
		defs []domain.FieldDef
		want error
	}{
		{name: "unknown", defs: []domain.FieldDef{{ID: "customfield_1", Name: "Other"}}, want: domain.ErrNotFound},
		{name: "ambiguous", defs: []domain.FieldDef{{ID: "customfield_1", Name: "Delivery Notes"}, {ID: "customfield_2", Name: "delivery notes"}}, want: domain.ErrCheckFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker := qualifiedRefsTracker{fieldDefs: test.defs}
			_, err := (&JiraService{tr: tracker}).IssueRefs(context.Background(), JiraIssueRefsOpts{Key: "PROJ-1", Fields: []string{"Delivery Notes"}})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestIssueRefsJQLLimitQualifiesSelectionTruncation(t *testing.T) {
	tracker := qualifiedRefsTracker{
		issues: []domain.Issue{{Key: "PROJ-1"}},
		pages:  map[string][]domain.Issue{"": {{Key: "PROJ-1"}}}, next: map[string]string{"": "1"},
		comments: map[string][]domain.Comment{"PROJ-1": {}},
	}
	result, err := (&JiraService{tr: tracker}).IssueRefs(context.Background(), JiraIssueRefsOpts{JQL: "project = PROJ", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || !result.Truncated || result.Selection.Complete || !result.Selection.Truncated || len(result.Warnings) == 0 {
		t.Fatalf("result=%+v", result)
	}
	if !result.Summary.CountMatchesIssues || !result.Summary.SelectionCountMatchesIssues || !result.Summary.CompleteMatchesInputs || !result.Summary.TruncatedMatchesInputs {
		t.Fatalf("summary=%+v", result.Summary)
	}
}

func TestIssueRefsExactLimitAtBackendExhaustionIsComplete(t *testing.T) {
	tracker := qualifiedRefsTracker{
		issues: []domain.Issue{{Key: "PROJ-1"}},
		pages:  map[string][]domain.Issue{"": {{Key: "PROJ-1"}}}, next: map[string]string{"": ""},
		comments: map[string][]domain.Comment{"PROJ-1": {}},
	}
	result, err := (&JiraService{tr: tracker}).IssueRefs(context.Background(), JiraIssueRefsOpts{JQL: "project = PROJ", Limit: 1})
	if err != nil || !result.Complete || result.Truncated || !result.Selection.Complete {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestIssueRefsMarksRecoverableCommentAndFieldTruncationPartial(t *testing.T) {
	tracker := qualifiedRefsTracker{
		issues: []domain.Issue{{
			Key: "PROJ-1", Comments: []domain.Comment{{Body: "embedded https://docs.example.com/comment"}},
			Fields: map[string]any{"customfield_10001": strings.Repeat("x", jiraDigestTextCap) + " https://docs.example.com/clipped"},
		}},
		commentErrs: map[string]error{"PROJ-1": domain.ErrCheckFailed},
	}
	result, err := (&JiraService{tr: tracker}).IssueRefs(context.Background(), JiraIssueRefsOpts{
		Key: "PROJ-1", Fields: []string{"customfield_10001"},
	})
	if err != nil {
		t.Fatal(err)
	}
	issue := result.Issues[0]
	if result.Complete || !result.Truncated || issue.Complete || !issue.Truncated || issue.Sources["comments"].Complete || !issue.Sources["field.customfield_10001"].TextTruncated {
		t.Fatalf("result=%+v issue=%+v", result, issue)
	}
	if result.Summary.IncompleteIssueCount != 1 || result.Summary.IncompleteSourceCount != 2 || result.Summary.TruncatedSourceCount != 1 || !result.Summary.CompleteMatchesInputs || !result.Summary.TruncatedMatchesInputs || !result.Summary.IssueSummariesReconciled {
		t.Fatalf("summary=%+v", result.Summary)
	}
	if issue.ReferenceSummary.IncompleteSourceCount != 2 || issue.ReferenceSummary.TruncatedSourceCount != 1 || !issue.ReferenceSummary.CompleteMatchesSources || !issue.ReferenceSummary.TruncatedMatchesSources {
		t.Fatalf("issue summary=%+v", issue.ReferenceSummary)
	}
	if len(issue.Refs) != 1 || issue.Refs[0].URL != "https://docs.example.com/comment" {
		t.Fatalf("refs=%+v", issue.Refs)
	}
}

func TestIssueRefsDoesNotHideTransportCommentFailure(t *testing.T) {
	transportErr := errors.New("transport failed")
	tracker := qualifiedRefsTracker{
		issues: []domain.Issue{{Key: "PROJ-1"}}, commentErrs: map[string]error{"PROJ-1": transportErr},
	}
	_, err := (&JiraService{tr: tracker}).IssueRefs(context.Background(), JiraIssueRefsOpts{Key: "PROJ-1"})
	if !errors.Is(err, transportErr) {
		t.Fatalf("err=%v", err)
	}
}

func TestIssueRefsRejectsNegativeLimit(t *testing.T) {
	_, err := (&JiraService{}).IssueRefs(context.Background(), JiraIssueRefsOpts{JQL: "project = PROJ", Limit: -1})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("err=%v", err)
	}
}

func TestIssueRefsViewProjectionEmitsClosedEvidenceKeysOnly(t *testing.T) {
	tracker := qualifiedRefsTracker{
		issues: []domain.Issue{{
			Key: "PROJ-1", Summary: "canary narrative", Type: "canary type",
			Body: "https://canary.example.test/secret " + strings.Repeat("x", jiraDigestTextCap),
		}},
		comments: map[string][]domain.Comment{"PROJ-1": {}},
	}
	full, err := (&JiraService{tr: tracker}).IssueRefs(context.Background(), JiraIssueRefsOpts{Key: "PROJ-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !full.Truncated || len(full.Warnings) == 0 || len(full.Issues[0].Warnings) == 0 ||
		len(full.Issues[0].Refs) != 1 || !strings.Contains(full.Issues[0].Refs[0].URL, "canary") ||
		full.Issues[0].Summary != "canary narrative" || full.Issues[0].Type != "canary type" {
		t.Fatalf("full=%+v issue=%+v", full, full.Issues[0])
	}

	view := JiraIssueRefsViewProjection(full)
	if view.SchemaVersion != 1 || view.Count != full.Count || view.Complete != full.Complete ||
		view.Truncated != full.Truncated ||
		view.Selection.Mode != full.Selection.Mode || view.Selection.Count != full.Selection.Count ||
		view.Selection.Limit != full.Selection.Limit || view.Selection.Complete != full.Selection.Complete ||
		view.Selection.Truncated != full.Selection.Truncated || view.Selection.Warning != full.Selection.Warning ||
		!slices.Equal(view.Warnings, full.Warnings) || len(view.Issues) != 1 {
		t.Fatalf("view=%+v", view)
	}
	issue := view.Issues[0]
	if issue.Key != "PROJ-1" || issue.Complete || !issue.Truncated || len(issue.Sources) != 2 ||
		issue.Sources["description"].Count != 1 || !issue.Sources["description"].TextTruncated ||
		issue.Sources["description"].Warning == "" || !issue.Sources["comments"].Complete {
		t.Fatalf("issue=%+v", issue)
	}
	if issue.ReferenceSummary.ReferenceCount != 1 || issue.ReferenceSummary.ReferenceKindCounts["link"] != 1 ||
		issue.ReferenceSummary.SourceValueCounts["description"] != 1 || issue.ReferenceSummary.TruncatedSourceCount != 1 {
		t.Fatalf("issue summary=%+v", issue.ReferenceSummary)
	}
	if view.Summary.ReferenceCount != 1 || view.Summary.ReferenceKindCounts["link"] != 1 ||
		view.Summary.SourceValueCounts["description"] != 1 || !view.Summary.IssueSummariesReconciled {
		t.Fatalf("summary=%+v", view.Summary)
	}

	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"canary", "https://", "\"refs\"", "\"jql\"", "\"summary\":\"", "\"type\""} {
		if strings.Contains(string(data), leak) {
			t.Fatalf("projection leaked %q: %s", leak, data)
		}
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	wantTop := []string{"complete", "count", "issues", "schema_version", "selection", "summary", "truncated", "warnings"}
	if got := sortedJSONKeys(top); !slices.Equal(got, wantTop) {
		t.Fatalf("top-level keys = %v, want %v", got, wantTop)
	}
	var selection map[string]json.RawMessage
	if err := json.Unmarshal(top["selection"], &selection); err != nil {
		t.Fatal(err)
	}
	if got, want := sortedJSONKeys(selection), []string{"complete", "count", "mode"}; !slices.Equal(got, want) {
		t.Fatalf("selection keys = %v, want %v", got, want)
	}
	var summary map[string]json.RawMessage
	if err := json.Unmarshal(top["summary"], &summary); err != nil {
		t.Fatal(err)
	}
	wantSummary := []string{
		"complete_issue_count", "complete_matches_inputs", "complete_source_count", "count_matches_issues",
		"incomplete_issue_count", "incomplete_source_count", "issue_count", "issue_summaries_reconciled",
		"reference_count", "reference_count_matches_kinds", "reference_kind_counts",
		"selection_count_matches_issues", "source_count", "source_value_counts",
		"truncated_matches_inputs", "truncated_source_count",
	}
	if got := sortedJSONKeys(summary); !slices.Equal(got, wantSummary) {
		t.Fatalf("summary keys = %v, want %v", got, wantSummary)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(top["issues"], &rows); err != nil {
		t.Fatal(err)
	}
	wantIssue := []string{"complete", "key", "reference_summary", "sources", "truncated"}
	if got := sortedJSONKeys(rows[0]); !slices.Equal(got, wantIssue) {
		t.Fatalf("issue keys = %v, want %v", got, wantIssue)
	}
	var sources map[string]map[string]json.RawMessage
	if err := json.Unmarshal(rows[0]["sources"], &sources); err != nil {
		t.Fatal(err)
	}
	if got, want := sortedJSONKeys(sources["comments"]), []string{"complete", "count"}; !slices.Equal(got, want) {
		t.Fatalf("comments source keys = %v, want %v", got, want)
	}
	if got, want := sortedJSONKeys(sources["description"]), []string{"complete", "count", "text_truncated", "warning"}; !slices.Equal(got, want) {
		t.Fatalf("description source keys = %v, want %v", got, want)
	}
	var referenceSummary map[string]json.RawMessage
	if err := json.Unmarshal(rows[0]["reference_summary"], &referenceSummary); err != nil {
		t.Fatal(err)
	}
	wantReferenceSummary := []string{
		"complete_matches_sources", "complete_source_count", "incomplete_source_count",
		"reference_count", "reference_count_matches_kinds", "reference_kind_counts",
		"source_count", "source_value_counts", "truncated_matches_sources", "truncated_source_count",
	}
	if got := sortedJSONKeys(referenceSummary); !slices.Equal(got, wantReferenceSummary) {
		t.Fatalf("reference summary keys = %v, want %v", got, wantReferenceSummary)
	}
}

func TestIssueRefsViewProjectionDeepCopiesMutableFactsAndHandlesNil(t *testing.T) {
	tracker := qualifiedRefsTracker{
		issues:   []domain.Issue{{Key: "PROJ-1", Body: "https://docs.example.com/spec"}},
		comments: map[string][]domain.Comment{"PROJ-1": {}},
		pages:    map[string][]domain.Issue{"": {{Key: "PROJ-1", Body: "https://docs.example.com/spec"}}},
		next:     map[string]string{"": "1"},
	}
	full, err := (&JiraService{tr: tracker}).IssueRefs(context.Background(), JiraIssueRefsOpts{JQL: "project = PROJ", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Warnings) == 0 || full.Summary.SourceValueCounts["description"] != 1 {
		t.Fatalf("full=%+v", full)
	}
	view := JiraIssueRefsViewProjection(full)

	view.Warnings[0] = "mutated warning"
	view.Summary.ReferenceKindCounts["doc"] = 99
	view.Summary.SourceValueCounts["description"] = 99
	view.Issues[0].Sources["description"] = JiraIssueRefsSourceView{Count: 99}
	view.Issues[0].ReferenceSummary.SourceValueCounts["description"] = 99
	if full.Warnings[0] == "mutated warning" || full.Summary.ReferenceKindCounts["doc"] != 1 ||
		full.Summary.SourceValueCounts["description"] != 1 || full.Issues[0].Sources["description"].Count != 1 ||
		full.Issues[0].ReferenceSummary.SourceValueCounts["description"] != 1 {
		t.Fatalf("projection mutation reached the source result: %+v", full)
	}

	fresh := JiraIssueRefsViewProjection(full)
	full.Warnings[0] = "source warning"
	full.Summary.ReferenceKindCounts["doc"] = 42
	full.Summary.SourceValueCounts["description"] = 42
	full.Issues[0].Sources["description"] = JiraIssueRefsSource{Count: 42}
	full.Issues[0].ReferenceSummary.SourceValueCounts["description"] = 42
	if fresh.Warnings[0] == "source warning" || fresh.Summary.ReferenceKindCounts["doc"] != 1 ||
		fresh.Summary.SourceValueCounts["description"] != 1 || fresh.Issues[0].Sources["description"].Count != 1 ||
		fresh.Issues[0].ReferenceSummary.SourceValueCounts["description"] != 1 {
		t.Fatalf("source mutation reached the projection: %+v", fresh)
	}

	if JiraIssueRefsViewProjection(nil) != nil {
		t.Fatal("nil result must produce nil projection")
	}
}

func sortedJSONKeys(object map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func TestIssueTreeGroupsEpicsExternalEpicsAndOrphans(t *testing.T) {
	svc := &JiraService{tr: partialTracker{issues: []domain.Issue{
		{Key: "PROJ-2", Summary: "Child", Type: "Story", Fields: map[string]any{"epic": "PROJ-1"}},
		{Key: "PROJ-1", Summary: "Parent", Type: "Epic", Fields: map[string]any{}},
		{Key: "PROJ-3", Summary: "External child", Type: "Story", Fields: map[string]any{"epic": "PROJ-X"}},
		{Key: "PROJ-4", Summary: "Orphan", Type: "Task", Fields: map[string]any{}},
	}}}

	tree, err := svc.IssueTree(context.Background(), JiraIssueTreeOpts{JQL: "project = PROJ", EpicField: "epic", Limit: 10})
	if err != nil {
		t.Fatalf("IssueTree: %v", err)
	}
	if tree.Count != 4 || tree.EpicField != "epic" {
		t.Fatalf("tree header = %+v, want count/epic field", tree)
	}
	if len(tree.Epics) != 1 || tree.Epics[0].Key != "PROJ-1" || len(tree.Epics[0].Children) != 1 || tree.Epics[0].Children[0].Key != "PROJ-2" {
		t.Fatalf("epics = %+v, want PROJ-1 -> PROJ-2", tree.Epics)
	}
	if len(tree.ExternalEpics) != 1 || tree.ExternalEpics[0].Key != "PROJ-X" || !tree.ExternalEpics[0].External || tree.ExternalEpics[0].Children[0].Key != "PROJ-3" {
		t.Fatalf("external epics = %+v, want external PROJ-X -> PROJ-3", tree.ExternalEpics)
	}
	if len(tree.Orphans) != 1 || tree.Orphans[0].Key != "PROJ-4" {
		t.Fatalf("orphans = %+v, want PROJ-4", tree.Orphans)
	}
}
