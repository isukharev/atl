package agenteval

import (
	"context"
	"maps"
	"slices"
	"testing"
)

func startRepositoryJiraGraphProcess(
	t *testing.T,
	fixture MockFixture,
	invocation MCPInvocation,
	routeNames []string,
	exactQueries []map[string]string,
) *SyntheticATLProcess {
	t.Helper()
	if len(routeNames) == 0 || len(routeNames) != len(fixture.Routes) ||
		len(exactQueries) != len(fixture.Routes) {
		t.Fatalf("Jira graph route contract does not reconcile: routes=%d names=%d queries=%d",
			len(fixture.Routes), len(routeNames), len(exactQueries))
	}
	retainedSequence := len(fixture.RequestSequence) != 0
	if retainedSequence && !slices.Equal(fixture.RequestSequence, routeNames) {
		t.Fatalf("retained Jira graph request sequence drifted: got=%v want=%v",
			fixture.RequestSequence, routeNames)
	}

	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	for index := range prepared.Routes {
		route := prepared.Routes[index]
		if route.Method != "GET" || len(route.QueryContains) != 0 {
			t.Fatalf("Jira graph route %q is not a closed GET: %+v", routeNames[index], route)
		}
		if index == 0 {
			if len(route.QueryEquals) != 0 || len(exactQueries[index]) == 0 {
				t.Fatalf("Jira graph issue route does not require its runtime exact-query upgrade: %+v", route)
			}
		} else if !maps.Equal(route.QueryEquals, exactQueries[index]) {
			t.Fatalf("Jira graph route %q query drifted: retained=%v want=%v",
				routeNames[index], route.QueryEquals, exactQueries[index])
		}
		if retainedSequence {
			if route.Name != routeNames[index] {
				t.Fatalf("retained Jira graph route name drifted: got=%q want=%q",
					route.Name, routeNames[index])
			}
		} else if route.Name != "" {
			t.Fatalf("unnamed Jira graph fixture contains route name %q without a request sequence", route.Name)
		}
		route.Name = routeNames[index]
		route.QueryEquals = maps.Clone(exactQueries[index])
		route.closedQuery = true
		prepared.Routes[index] = route
	}
	prepared.RequestSequence = slices.Clone(routeNames)
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare Jira graph process fixture: %v", err)
	}

	process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: prepared,
		ScratchRoot: privateSyntheticATLScratch(t),
		MCPService:  "jira", MCPInvocations: []MCPInvocation{invocation},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close synthetic Jira graph process: %v", err)
		}
	})
	return process
}

func callRepositoryJiraGraph(
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
