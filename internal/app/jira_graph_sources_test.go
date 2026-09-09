package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestNormalizeJiraIssueGraphSources(t *testing.T) {
	selection, err := NormalizeJiraIssueGraphSources(nil, nil, false)
	if err != nil || selection != nil {
		t.Fatalf("default selection=%+v error=%v", selection, err)
	}
	selection, err = NormalizeJiraIssueGraphSources([]string{"remote_links,issue_links", "issue_links"}, []string{"remote_links"}, false)
	if err != nil || !slices.Equal(selection.Selected, []string{"issue_links"}) || !slices.Equal(selection.Snapshot.Fields, []string{"summary", "issuelinks"}) || selection.Snapshot.Properties {
		t.Fatalf("selection=%+v error=%v", selection, err)
	}
	selection, err = NormalizeJiraIssueGraphSources([]string{"comments"}, nil, true)
	if err != nil || !slices.Equal(selection.Selected, []string{"comments", "development"}) {
		t.Fatalf("development selection=%+v error=%v", selection, err)
	}
	selection, err = NormalizeJiraIssueGraphSources(nil, []string{"comments", "development"}, false)
	if err != nil || slices.Contains(selection.Selected, "comments") || len(selection.Selected) != 7 {
		t.Fatalf("exclude selection=%+v error=%v", selection, err)
	}
}

func TestJiraGraphSourceSelectionRejectsInvalidBeforeAnyReader(t *testing.T) {
	tests := []JiraIssueGraphOptions{
		{IncludeSources: []string{}}, {ExcludeSources: []string{}},
		{IncludeSources: []string{""}}, {IncludeSources: []string{" comments"}},
		{IncludeSources: []string{"COMMENTS"}}, {IncludeSources: []string{"unknown"}},
		{IncludeSources: []string{"comments,"}}, {IncludeSources: []string{strings.Repeat("comments,", 10)}},
		{IncludeSources: []string{strings.Repeat("a", 1000)}},
		{IncludeSources: []string{"development"}},
		{ExcludeSources: []string{"development"}, IncludeDevelopment: true},
		{IncludeSources: []string{"comments"}, ExcludeSources: []string{"comments"}},
		{ExcludeSources: jiraGraphSourceOrder},
	}
	for _, opts := range tests {
		_, err := (&JiraService{}).IssueGraphWithOptions(t.Context(), "PROJ-1", opts)
		if !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("opts=%+v error=%v", opts, err)
		}
	}
	tracker := completeGraphFixture()
	_, err := (&JiraService{tr: tracker}).IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeSources: []string{"comments"}})
	if !errors.Is(err, domain.ErrCheckFailed) || tracker.snapshots != 0 {
		t.Fatalf("missing projection capability error=%v reads=%d", err, tracker.snapshots)
	}
}

type selectedGraphTracker struct {
	*jiraGraphTraversalTracker
	projections []domain.IssueSnapshotProjection
	collectors  map[string]int
}

func (t *selectedGraphTracker) ReadIssueSnapshotProjection(ctx context.Context, key string, projection domain.IssueSnapshotProjection) (*domain.QualifiedIssueSnapshot, error) {
	t.projections = append(t.projections, projection)
	return t.ReadIssueSnapshot(ctx, key)
}
func (t *selectedGraphTracker) ListComments(ctx context.Context, key string) ([]domain.Comment, error) {
	t.collectors["comments"]++
	return t.jiraGraphTraversalTracker.ListComments(ctx, key)
}
func (t *selectedGraphTracker) ListIssueWorklogs(ctx context.Context, key string) (*domain.IssueWorklogList, error) {
	t.collectors["worklogs"]++
	return t.jiraGraphTraversalTracker.ListIssueWorklogs(ctx, key)
}
func (t *selectedGraphTracker) ReadIssueRemoteLinks(ctx context.Context, key string) (domain.JiraRemoteLinkInventory, error) {
	t.collectors["remote_links"]++
	return t.jiraGraphTraversalTracker.ReadIssueRemoteLinks(ctx, key)
}
func (t *selectedGraphTracker) ReadIssueDevelopment(context.Context, string) (domain.JiraDevelopmentInventory, error) {
	t.collectors["development"]++
	return domain.JiraDevelopmentInventory{}, nil
}

func selectedGraphService(snapshots map[string]*domain.QualifiedIssueSnapshot) (*JiraService, *selectedGraphTracker) {
	service, base := traversalService(snapshots)
	tracker := &selectedGraphTracker{jiraGraphTraversalTracker: base, collectors: map[string]int{}}
	service.tr = tracker
	return service, tracker
}

func TestJiraGraphEachSelectedCollectorRunsAlone(t *testing.T) {
	for _, kind := range jiraGraphSourceKinds(true) {
		t.Run(kind, func(t *testing.T) {
			snapshot := completeGraphFixture().snapshot
			service, tracker := selectedGraphService(map[string]*domain.QualifiedIssueSnapshot{"PROJ-1": snapshot})
			result, err := service.IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeSources: []string{kind}, IncludeDevelopment: kind == "development"})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Sources) != 1 || result.Sources[0].Kind != kind || len(tracker.projections) != 1 || result.Bounds.MaxSources != result.Bounds.MaxNodes+1 {
				t.Fatalf("result=%+v calls=%+v", result, tracker.collectors)
			}
			for _, aux := range []string{"comments", "worklogs", "remote_links", "development"} {
				want := 0
				if aux == kind {
					want = 1
				}
				if tracker.collectors[aux] != want {
					t.Fatalf("%s calls=%d want=%d", aux, tracker.collectors[aux], want)
				}
			}
			for _, edge := range result.Edges {
				for _, evidence := range edge.Evidence {
					if evidence.Collector != kind {
						t.Fatalf("unselected evidence=%+v", evidence)
					}
				}
			}
		})
	}
}

func TestJiraGraphSelectionTraversesOnlySelectedStructuredRelations(t *testing.T) {
	snapshots := map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2"}, "PROJ-3"),
		"PROJ-2": traversalSnapshot("PROJ-2", nil, "PROJ-4"),
	}
	service, tracker := selectedGraphService(snapshots)
	tracker.commentsErr, tracker.remoteErr, tracker.worklogsErr = domain.ErrForbidden, domain.ErrForbidden, domain.ErrForbidden
	result, err := service.IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{Depth: 2, IncludeSources: []string{"issue_links"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Bounds.ExpandedNodes != 2 || len(result.Sources) != 2 || len(result.Nodes) != 2 || len(result.Frontier) != 0 || len(tracker.collectors) != 0 || !slices.Equal(tracker.readOrder, []string{"PROJ-1", "PROJ-2"}) {
		t.Fatalf("result=%+v calls=%+v", result, tracker)
	}
	for _, source := range result.Sources {
		if source.Kind != "issue_links" || source.NodeDepth == nil {
			t.Fatalf("source=%+v", source)
		}
	}
	for _, projection := range tracker.projections {
		if !reflect.DeepEqual(projection, result.SourceSelection.Snapshot) {
			t.Fatalf("projection=%+v", projection)
		}
	}
	compact, err := ProjectJiraIssueGraphCompact(result, JiraIssueGraphProjectionOptions{Projection: "compact", Selectors: []string{"none"}})
	if err != nil || !reflect.DeepEqual(compact.SourceSelection, result.SourceSelection) || !compact.Complete {
		t.Fatalf("compact=%+v err=%v", compact, err)
	}
	compact.SourceSelection.Selected[0] = "comments"
	if result.SourceSelection.Selected[0] != "issue_links" {
		t.Fatal("compact aliases collection scope")
	}
}

func TestJiraGraphHierarchyDisclosesSupportingFieldsWithoutNarrativeInspection(t *testing.T) {
	snapshot := traversalSnapshot("PROJ-1", nil, "PROJ-3 https://docs.example.test/secret")
	snapshot.Fields["customfield_12"] = "PROJ-2"
	snapshot.Names["customfield_12"] = "Epic Link"
	snapshot.Schema["customfield_12"] = domain.IssueFieldSchema{Type: "string", Custom: "example:epic-link"}
	delete(snapshot.Schema, "description")
	service, _ := selectedGraphService(map[string]*domain.QualifiedIssueSnapshot{"PROJ-1": snapshot})
	result, err := service.IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeSources: []string{"hierarchy"}})
	if err != nil || !result.Complete || len(result.Edges) != 1 || result.Edges[0].Kind != "epic_of" || result.SourceSelection.Snapshot.SupportingFieldsReason != "hierarchy_discovery" || !slices.Equal(result.SourceSelection.Snapshot.Fields, []string{"*all"}) {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestJiraGraphSelectedBoundsAndFailedNodes(t *testing.T) {
	for _, failure := range []error{domain.ErrForbidden, domain.ErrReadAttemptBudgetExhausted, domain.ErrReadResponseBudgetExhausted} {
		service, tracker := selectedGraphService(map[string]*domain.QualifiedIssueSnapshot{"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2"}, "")})
		tracker.errors["PROJ-2"] = failure
		result, err := service.IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{Depth: 1, IncludeSources: []string{"issue_links"}})
		if err != nil || result.Complete || len(result.Sources) != 2 || result.Summary.IncompleteSourceCount != 1 {
			t.Fatalf("failure=%v result=%+v error=%v", failure, result, err)
		}
	}
	service, _ := selectedGraphService(map[string]*domain.QualifiedIssueSnapshot{"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2"}, "")})
	result, err := service.IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{Depth: 2, IncludeSources: []string{"issue_links"}, MaxNodes: 1})
	if err != nil || result.Complete || !result.Truncated || len(result.Sources) != 1 || len(result.Frontier) != 1 {
		t.Fatalf("bounded result=%+v error=%v", result, err)
	}
}

func TestJiraGraphSourceSelectionWireRejectsFalseQualification(t *testing.T) {
	service, _ := selectedGraphService(map[string]*domain.QualifiedIssueSnapshot{"PROJ-1": traversalSnapshot("PROJ-1", nil, "")})
	result, err := service.IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeSources: []string{"issue_links"}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	for _, mutate := range []func(*JiraIssueGraphResult){
		func(r *JiraIssueGraphResult) { r.SourceSelection.SchemaVersion++ },
		func(r *JiraIssueGraphResult) { r.SourceSelection = nil },
		func(r *JiraIssueGraphResult) { r.SourceSelection.Selected = nil },
		func(r *JiraIssueGraphResult) { r.SourceSelection.Omitted = nil },
		func(r *JiraIssueGraphResult) {
			r.SourceSelection.Selected = append(r.SourceSelection.Selected, "issue_links")
		},
		func(r *JiraIssueGraphResult) { r.SourceSelection.Snapshot.Fields = []string{"*all"} },
		func(r *JiraIssueGraphResult) { r.SourceSelection.Snapshot.Properties = true },
		func(r *JiraIssueGraphResult) {
			r.SourceSelection.Snapshot.SupportingFieldsReason = "hierarchy_discovery"
		},
		func(r *JiraIssueGraphResult) { r.Sources[0].Kind = "comments" },
	} {
		var candidate JiraIssueGraphResult
		if err := json.Unmarshal(encoded, &candidate); err != nil {
			t.Fatal(err)
		}
		mutate(&candidate)
		if err := ValidateJiraIssueGraphResult(&candidate); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("accepted invalid selection %+v", candidate.SourceSelection)
		}
	}
}
