package agenteval

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

const confluencePageWorkflowExpand = "body.storage,version,space,ancestors,metadata.labels"

func startRepositoryConfluencePageWorkflowProcess(
	t *testing.T,
	fixture MockFixture,
	admissions []MCPInvocation,
	routeSequence []int,
) *SyntheticATLProcess {
	t.Helper()
	if len(admissions) == 0 || len(routeSequence) == 0 {
		t.Fatalf("Confluence page workflow requires admissions and a backend sequence: admissions=%d sequence=%d",
			len(admissions), len(routeSequence))
	}
	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	routeNames := make([]string, len(prepared.Routes))
	for index := range prepared.Routes {
		route := prepared.Routes[index]
		name := fmt.Sprintf("page-workflow-route-%d", index+1)
		if route.Method != "GET" || route.Status != 200 ||
			len(route.QueryContains) != 0 || len(route.RequestBody) != 0 || len(route.Responses) != 0 {
			t.Fatalf("Confluence page workflow route %d is not one static GET: %+v", index, route)
		}
		switch {
		case route.Path == fixture.ConfluenceContext+"/rest/api/search":
			if len(route.QueryEquals) == 0 {
				t.Fatalf("Confluence search route %d does not retain an exact query", index)
			}
			route.QueryEquals = maps.Clone(route.QueryEquals)
			route.QueryEquals["expand"] = "content.version,content.space"
		case strings.HasPrefix(route.Path, fixture.ConfluenceContext+"/rest/api/content/"):
			if len(route.QueryEquals) != 0 {
				t.Fatalf("Confluence page route %d unexpectedly retains a query: %v", index, route.QueryEquals)
			}
			route.QueryEquals = map[string]string{"expand": confluencePageWorkflowExpand}
		default:
			t.Fatalf("Confluence page workflow route %d has unsupported path %q", index, route.Path)
		}
		if len(fixture.RequestSequence) != 0 {
			if route.Name != name {
				t.Fatalf("retained Confluence route %d name=%q want=%q", index, route.Name, name)
			}
		} else if route.Name != "" {
			t.Fatalf("unsequenced Confluence route %d has retained name %q", index, route.Name)
		}
		route.Name = name
		route.closedQuery = true
		prepared.Routes[index] = route
		routeNames[index] = name
	}
	sequence := make([]string, len(routeSequence))
	for index, routeIndex := range routeSequence {
		if routeIndex < 0 || routeIndex >= len(routeNames) {
			t.Fatalf("Confluence backend sequence step %d names route %d of %d",
				index, routeIndex, len(routeNames))
		}
		sequence[index] = routeNames[routeIndex]
	}
	if len(fixture.RequestSequence) != 0 && !slices.Equal(fixture.RequestSequence, sequence) {
		t.Fatalf("retained Confluence request sequence=%v want=%v", fixture.RequestSequence, sequence)
	}
	prepared.RequestSequence = sequence
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare Confluence page workflow fixture: %v", err)
	}
	return startRepositoryConfluenceEvidenceProcess(t, prepared, admissions)
}

func callRepositoryConfluencePageWorkflow(
	t *testing.T,
	process *SyntheticATLProcess,
	invocation MCPInvocation,
) SyntheticMCPResult {
	t.Helper()
	result, message, ok := callRepositoryConfluenceEvidence(t, process, invocation)
	if !ok {
		t.Fatalf("Confluence page workflow call failed: %s", message)
	}
	return result
}

func decodeRepositoryConfluencePageResolution(
	t *testing.T,
	result SyntheticMCPResult,
) ConfluencePageResolutionView {
	t.Helper()
	view, err := DecodeConfluencePageResolutionView(bytes.NewReader(result.StructuredContent))
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func decodeRepositoryConfluenceSearchPage(
	t *testing.T,
	result SyntheticMCPResult,
) ConfluenceSearchPageView {
	t.Helper()
	view, err := DecodeConfluenceSearchPageView(bytes.NewReader(result.StructuredContent))
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func decodeRepositoryConfluencePageOutline(
	t *testing.T,
	result SyntheticMCPResult,
) ConfluencePageOutlineView {
	t.Helper()
	view, err := DecodeConfluencePageOutlineView(bytes.NewReader(result.StructuredContent))
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func decodeRepositoryConfluencePageSection(
	t *testing.T,
	result SyntheticMCPResult,
) ConfluencePageSectionView {
	t.Helper()
	view, err := DecodeConfluencePageSectionView(bytes.NewReader(result.StructuredContent))
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func assertRepositoryConfluencePageWorkflowRefusesBeforeBackend(
	t *testing.T,
	process *SyntheticATLProcess,
	invocation MCPInvocation,
	wantMethods, wantMCP map[string]int,
	wantDuplicates int,
) {
	t.Helper()
	if _, err := process.CallMCPJSON(context.Background(), invocation); err == nil {
		t.Fatal("unadmitted Confluence page workflow invocation reached the backend")
	}
	summary := process.Summary()
	if process.RequestSequenceComplete() || summary.UnexpectedRequests != 0 ||
		summary.DuplicateRequests != wantDuplicates || len(summary.CLIInvocations) != 0 ||
		!equalHTTPMethods(summary.HTTPMethods, wantMethods) ||
		!equalHTTPMethods(summary.MCPInvocations, wantMCP) {
		t.Fatalf("Confluence workflow divergence was not refused pre-backend: summary=%+v sequence_complete=%t",
			summary, process.RequestSequenceComplete())
	}
}
