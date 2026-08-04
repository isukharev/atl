package agenteval

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type structureProcessContract struct {
	structureID     int64
	requestSequence []string
}

func startRepositoryJiraStructureProcess(
	t *testing.T,
	fixture MockFixture,
	admitted []MCPInvocation,
	expected structureProcessContract,
) *SyntheticATLProcess {
	t.Helper()
	if expected.structureID <= 0 || len(admitted) == 0 || len(fixture.Routes) != 4 {
		t.Fatalf("Jira Structure process contract is incomplete: structure=%d admissions=%d routes=%d",
			expected.structureID, len(admitted), len(fixture.Routes))
	}
	routeNames := []string{"metadata", "forest", "values", "issues"}
	baseSequence := slices.Clone(routeNames)
	qualificationSequence := []string{"metadata", "metadata", "forest", "values", "issues"}
	if !slices.Equal(expected.requestSequence, baseSequence) &&
		!slices.Equal(expected.requestSequence, qualificationSequence) {
		t.Fatalf("Jira Structure request sequence is not a reviewed route: %v", expected.requestSequence)
	}
	if len(fixture.RequestSequence) != 0 {
		t.Fatalf("retained Jira Structure fixture unexpectedly names a request sequence: %v", fixture.RequestSequence)
	}

	structureID := strconv.FormatInt(expected.structureID, 10)
	wantMethods := []string{"GET", "GET", "POST", "GET"}
	wantSuffixes := []string{
		"/rest/structure/2.0/structure/" + structureID,
		"/rest/structure/2.0/forest/latest",
		"/rest/structure/2.0/value",
		"/rest/api/2/search",
	}
	wantQueries := []map[string]string{
		{"withOwner": "true", "withPermissions": "true"},
		{"s": `{"structureId":` + structureID + `}`},
		{},
		nil,
	}

	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	for index := range prepared.Routes {
		route := prepared.Routes[index]
		if route.Name != "" || route.Method != wantMethods[index] ||
			!strings.HasSuffix(route.Path, wantSuffixes[index]) || len(route.QueryContains) != 0 {
			t.Fatalf("retained Jira Structure route %d drifted: %+v", index, route)
		}
		if index < 3 && !maps.Equal(route.QueryEquals, wantQueries[index]) {
			t.Fatalf("retained Jira Structure query %q drifted: got=%v want=%v",
				routeNames[index], route.QueryEquals, wantQueries[index])
		}
		if index == 3 && !validStructureIssueQuery(route.QueryEquals) {
			t.Fatalf("retained Jira Structure issue query drifted: %v", route.QueryEquals)
		}
		if index == 2 {
			if len(route.RequestBody) == 0 || !json.Valid(route.RequestBody) {
				t.Fatal("retained Jira Structure value route has no exact JSON body")
			}
		} else if len(route.RequestBody) != 0 {
			t.Fatalf("retained Jira Structure GET route %q unexpectedly carries a request body", routeNames[index])
		}
		route.Name = routeNames[index]
		route.closedQuery = true
		prepared.Routes[index] = route
	}
	prepared.RequestSequence = slices.Clone(expected.requestSequence)
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare Jira Structure process fixture: %v", err)
	}

	process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: prepared,
		ScratchRoot: privateSyntheticATLScratch(t),
		MCPService:  "jira", MCPInvocations: slices.Clone(admitted),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close synthetic Jira Structure process: %v", err)
		}
	})
	return process
}

func validStructureIssueQuery(query map[string]string) bool {
	return len(query) == 5 && strings.HasPrefix(query["jql"], "id in (") &&
		strings.HasSuffix(query["jql"], ")") && query["fields"] == "summary,status,issuetype,project" &&
		query["startAt"] == "0" && query["maxResults"] == "100" && query["validateQuery"] == "false"
}

func callRepositoryJiraStructure(
	t *testing.T,
	process *SyntheticATLProcess,
	invocation MCPInvocation,
) SyntheticMCPResult {
	t.Helper()
	result, err := process.CallMCPJSON(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func repositoryStructureMCPFinal(
	t *testing.T,
	view JiraStructureView,
	structureID, rootRow int64,
	expectedPath []string,
) []byte {
	t.Helper()
	if view.Structure.ID != structureID || view.Structure.Name == "" ||
		view.Selection == nil || view.Selection.Kind != "folder-path" ||
		view.Selection.RowID != rootRow || !slices.Equal(view.Selection.Path, expectedPath) ||
		len(view.Rows) == 0 || view.Rows[0].RowID != rootRow ||
		view.Rows[0].ItemID != view.Selection.FolderID ||
		!slices.Equal(view.Projection.Attributes, []string{"key", "summary", "status"}) {
		t.Fatalf("selected Jira Structure view drifted: %+v", view)
	}

	orderedRows := make([]map[string]any, 0, len(view.Rows))
	inaccessibleRows := make([]int64, 0, len(view.InaccessibleRows))
	seenIssueIDs := map[string]bool{}
	accessibleIssueRows, inaccessibleIssueRows, repeatedIssueOccurrences, nonIssueRows := 0, 0, 0, 0
	for _, row := range view.Rows {
		if row.RelativeDepth == nil {
			t.Fatalf("selected Jira Structure row %d has no relative depth", row.RowID)
		}
		if row.ItemType == "issue" {
			if row.Accessible {
				accessibleIssueRows++
			} else {
				inaccessibleIssueRows++
				inaccessibleRows = append(inaccessibleRows, row.RowID)
			}
			if seenIssueIDs[row.ItemID] {
				repeatedIssueOccurrences++
			}
			seenIssueIDs[row.ItemID] = true
		} else {
			nonIssueRows++
		}
		orderedRows = append(orderedRows, map[string]any{
			"row_id": row.RowID, "relative_depth": *row.RelativeDepth,
			"item_type": row.ItemType, "item_id": row.ItemID, "accessible": row.Accessible,
		})
	}
	if view.IssueCount != len(seenIssueIDs) ||
		!slices.Equal(inaccessibleRows, view.InaccessibleRows) ||
		view.Complete != (len(inaccessibleRows) == 0) {
		t.Fatalf("selected Jira Structure counts drifted: view=%+v unique_issues=%d inaccessible=%v",
			view, len(seenIssueIDs), inaccessibleRows)
	}

	final, err := json.Marshal(map[string]any{
		"structure_id": view.Structure.ID, "structure_name": view.Structure.Name,
		"selection": map[string]any{
			"kind": view.Selection.Kind, "folder_id": view.Selection.FolderID,
			"row_id": view.Selection.RowID, "path": view.Selection.Path,
		},
		"projection_fields": view.Projection.Attributes,
		"counts": map[string]any{
			"row_count": len(view.Rows), "issue_count": len(seenIssueIDs),
			"accessible_issue_rows": accessibleIssueRows, "inaccessible_issue_rows": inaccessibleIssueRows,
			"repeated_issue_occurrences": repeatedIssueOccurrences, "non_issue_rows": nonIssueRows,
		},
		"ordered_rows": orderedRows, "inaccessible_rows": view.InaccessibleRows,
		"complete": view.Complete, "warnings_count": len(view.Warnings),
		"embedded_instruction_treated_as_data": true, "content_mutations": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	return final
}
