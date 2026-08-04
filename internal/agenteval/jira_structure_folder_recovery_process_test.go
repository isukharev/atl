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

const (
	structureFolderRecoveryMetadataRoute        = "structure_metadata"
	structureFolderRecoveryForestRoute          = "structure_forest"
	structureFolderRecoveryInventoryValuesRoute = "inventory_values"
	structureFolderRecoverySubtreeValuesRoute   = "subtree_values"
	structureFolderRecoveryInventoryIssuesRoute = "inventory_issues"
	structureFolderRecoverySubtreeIssuesRoute   = "subtree_issues"
)

func structureFolderRecoveryHonestRequestSequence() []string {
	return []string{
		structureFolderRecoveryMetadataRoute,
		structureFolderRecoveryForestRoute,
		structureFolderRecoveryInventoryValuesRoute,
		structureFolderRecoveryMetadataRoute,
		structureFolderRecoveryForestRoute,
		structureFolderRecoveryInventoryValuesRoute,
		structureFolderRecoveryInventoryIssuesRoute,
		structureFolderRecoveryMetadataRoute,
		structureFolderRecoveryForestRoute,
		structureFolderRecoveryInventoryValuesRoute,
		structureFolderRecoverySubtreeIssuesRoute,
		structureFolderRecoverySubtreeValuesRoute,
	}
}

func startStructureFolderRecoveryProcess(
	t *testing.T,
	cohort structureFolderRecoveryCohort,
	fixture MockFixture,
	admitted []MCPInvocation,
	requestSequence []string,
) *SyntheticATLProcess {
	t.Helper()
	prepared := prepareStructureFolderRecoveryProcessFixture(t, cohort, fixture, requestSequence)
	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: prepared,
		ScratchRoot: privateSyntheticATLScratch(t),
		MCPService:  "jira", MCPInvocations: slices.Clone(admitted),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close synthetic Jira Structure recovery process: %v", err)
		}
	})
	return process
}

func prepareStructureFolderRecoveryProcessFixture(
	t *testing.T,
	cohort structureFolderRecoveryCohort,
	fixture MockFixture,
	requestSequence []string,
) MockFixture {
	t.Helper()
	if cohort.structureID <= 0 || len(fixture.Routes) != 6 || len(fixture.RequestSequence) != 0 {
		t.Fatalf("retained recovery fixture contract drifted: structure=%d routes=%d sequence=%v",
			cohort.structureID, len(fixture.Routes), fixture.RequestSequence)
	}
	routeNames := []string{
		structureFolderRecoveryMetadataRoute,
		structureFolderRecoveryForestRoute,
		structureFolderRecoveryInventoryValuesRoute,
		structureFolderRecoverySubtreeValuesRoute,
		structureFolderRecoveryInventoryIssuesRoute,
		structureFolderRecoverySubtreeIssuesRoute,
	}
	wantMethods := []string{"GET", "GET", "POST", "POST", "GET", "GET"}
	structureID := strconv.FormatInt(cohort.structureID, 10)
	wantSuffixes := []string{
		"/rest/structure/2.0/structure/" + structureID,
		"/rest/structure/2.0/forest/latest",
		"/rest/structure/2.0/value",
		"/rest/structure/2.0/value",
		"/rest/api/2/search",
		"/rest/api/2/search",
	}
	wantQueries := []map[string]string{
		{"withOwner": "true", "withPermissions": "true"},
		{"s": `{"structureId":` + structureID + `}`},
		{},
		{},
		nil,
		nil,
	}

	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	for index := range prepared.Routes {
		route := prepared.Routes[index]
		if route.Name != "" || route.Method != wantMethods[index] ||
			!strings.HasSuffix(route.Path, wantSuffixes[index]) || len(route.QueryContains) != 0 {
			t.Fatalf("retained recovery route %d drifted: %+v", index, route)
		}
		if index < 4 && !maps.Equal(route.QueryEquals, wantQueries[index]) {
			t.Fatalf("retained recovery query %q drifted: got=%v want=%v",
				routeNames[index], route.QueryEquals, wantQueries[index])
		}
		if index >= 4 && !validStructureIssueQuery(route.QueryEquals) {
			t.Fatalf("retained recovery issue query %q drifted: %v", routeNames[index], route.QueryEquals)
		}
		if route.Method == "POST" {
			if len(route.RequestBody) == 0 || !json.Valid(route.RequestBody) {
				t.Fatalf("retained recovery route %q has no exact JSON request body", routeNames[index])
			}
		} else if len(route.RequestBody) != 0 {
			t.Fatalf("retained recovery GET route %q carries a request body", routeNames[index])
		}
		route.Name = routeNames[index]
		route.closedQuery = true
		prepared.Routes[index] = route
	}
	for _, name := range requestSequence {
		if !slices.Contains(routeNames, name) {
			t.Fatalf("recovery request sequence names an unreviewed route %q", name)
		}
	}
	prepared.RequestSequence = slices.Clone(requestSequence)
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare Jira Structure recovery process fixture: %v", err)
	}
	return prepared
}

func callStructureFolderRecoveryProcess(
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

func assertStructureFolderRecoveryAdmissionRefused(
	t *testing.T,
	process *SyntheticATLProcess,
	invocation MCPInvocation,
) {
	t.Helper()
	if _, err := process.CallMCPJSON(context.Background(), invocation); err == nil {
		t.Fatal("argument-divergent Jira Structure invocation crossed the exact process admission")
	}
	summary := process.Summary()
	if len(summary.HTTPMethods) != 0 || summary.UnexpectedRequests != 0 ||
		summary.DuplicateRequests != 0 || len(summary.CLIInvocations) != 0 ||
		len(summary.MCPInvocations) != 0 || process.RequestSequenceComplete() {
		t.Fatalf("argument divergence was not refused before runtime/backend work: summary=%+v complete=%t",
			summary, process.RequestSequenceComplete())
	}
}
