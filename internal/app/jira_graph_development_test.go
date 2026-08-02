package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type jiraGraphDevelopmentTracker struct {
	*jiraGraphTracker
	inventory domain.JiraDevelopmentInventory
	err       error
	reads     int
}

func (t *jiraGraphDevelopmentTracker) ReadIssueDevelopment(context.Context, string) (domain.JiraDevelopmentInventory, error) {
	t.reads++
	return t.inventory, t.err
}

func developmentGraphFixture() *jiraGraphTracker {
	return completeGraphFixture()
}

func completeDevelopmentInventory() domain.JiraDevelopmentInventory {
	return domain.JiraDevelopmentInventory{
		Projects: []domain.JiraDevelopmentProject{{Host: "git.example.test", ProjectPath: "platform/widget"}},
		Commits: []domain.JiraDevelopmentCommit{{
			Host: "git.example.test", ProjectPath: "platform/widget",
			SHA: "0123456789abcdef0123456789abcdef01234567",
		}},
		Branches: []domain.JiraDevelopmentBranch{{
			Host: "git.example.test", ProjectPath: "platform/widget", Name: "feature/graph|proof",
		}},
		MergeRequests: []domain.JiraDevelopmentMergeRequest{{
			Host: "git.example.test", ProjectPath: "platform/widget", IID: "42", State: "open",
		}},
	}
}

func TestIssueGraphDevelopmentIsExplicitOptInAndDefaultBytesRemainStable(t *testing.T) {
	baseline := developmentGraphFixture()
	baselineResult, err := (&JiraService{tr: baseline, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tracker := &jiraGraphDevelopmentTracker{jiraGraphTracker: developmentGraphFixture(), inventory: completeDevelopmentInventory()}
	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if tracker.reads != 0 {
		t.Fatalf("development reads = %d, want 0", tracker.reads)
	}
	want, _ := json.Marshal(baselineResult)
	got, _ := json.Marshal(result)
	if string(got) != string(want) {
		t.Fatalf("default graph bytes changed\n got: %s\nwant: %s", got, want)
	}
}

func TestIssueGraphDevelopmentProjectsMinimalDeterministicIdentities(t *testing.T) {
	tracker := &jiraGraphDevelopmentTracker{jiraGraphTracker: developmentGraphFixture(), inventory: completeDevelopmentInventory()}
	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeDevelopment: true})
	if err != nil {
		t.Fatal(err)
	}
	if tracker.reads != 1 || !result.Complete || !result.Bounds.IncludeDevelopment || result.Bounds.MaxSources != result.Bounds.MaxNodes*9+1 {
		t.Fatalf("unexpected opt-in result: reads=%d complete=%t bounds=%+v", tracker.reads, result.Complete, result.Bounds)
	}
	var source *domain.ArtifactGraphSource
	for index := range result.Sources {
		if result.Sources[index].Kind == "development" {
			source = &result.Sources[index]
		}
	}
	if source == nil || source.Status != domain.ArtifactSourceComplete || source.Count != 3 || source.Stability != domain.ArtifactStabilityExperimentalAPI {
		t.Fatalf("development source = %+v", source)
	}
	developmentNodes, developmentEdges := 0, 0
	for _, node := range result.Nodes {
		if strings.HasPrefix(node.Kind, "gitlab_") {
			developmentNodes++
			if node.SCM == nil || node.Label != "" || node.ExternalID != "" || node.Stability != domain.ArtifactStabilityExperimentalAPI {
				t.Fatalf("unsafe development node: %+v", node)
			}
			if node.Kind == "gitlab_merge_request" && node.SCM.MergeRequestState != "open" {
				t.Fatalf("merge-request state = %+v", node.SCM)
			}
		}
	}
	for _, edge := range result.Edges {
		if strings.HasPrefix(edge.Kind, "development_") {
			developmentEdges++
			if len(edge.Evidence) != 1 || len(edge.Evidence[0].SourceID) != 64 {
				t.Fatalf("unsafe development evidence: %+v", edge.Evidence)
			}
		}
	}
	if developmentNodes != 4 || developmentEdges != 4 {
		t.Fatalf("development topology nodes=%d edges=%d", developmentNodes, developmentEdges)
	}
	markdown := JiraIssueGraphMarkdown(result)
	if !strings.Contains(markdown, "feature/graph\\|proof") || !strings.Contains(markdown, "merge_request:42") || strings.Contains(markdown, "unknown-user") {
		t.Fatalf("coordinate markdown is missing or unsafe:\n%s", markdown)
	}
}

func TestIssueGraphDevelopmentQualifiesUnsupportedAndMalformedAtomically(t *testing.T) {
	t.Run("unsupported reader", func(t *testing.T) {
		result, err := (&JiraService{tr: developmentGraphFixture(), baseURL: "https://jira.example.test"}).IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeDevelopment: true})
		if err != nil {
			t.Fatal(err)
		}
		assertDevelopmentSource(t, result, domain.ArtifactSourceUnsupported, "", false)
	})

	t.Run("malformed inventory", func(t *testing.T) {
		inventory := completeDevelopmentInventory()
		inventory.Commits = append(inventory.Commits, inventory.Commits[0])
		tracker := &jiraGraphDevelopmentTracker{jiraGraphTracker: developmentGraphFixture(), inventory: inventory}
		result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeDevelopment: true})
		if err != nil {
			t.Fatal(err)
		}
		assertDevelopmentSource(t, result, domain.ArtifactSourcePartial, domain.ArtifactPartialMalformed, false)
		assertNoDevelopmentFacts(t, result)
	})

	t.Run("backend error text is not emitted", func(t *testing.T) {
		tracker := &jiraGraphDevelopmentTracker{jiraGraphTracker: developmentGraphFixture(), err: errors.New("backend token=private-value")}
		result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeDevelopment: true})
		if err != nil {
			t.Fatal(err)
		}
		assertDevelopmentSource(t, result, domain.ArtifactSourcePartial, domain.ArtifactPartialRequestFailed, false)
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), "private-value") {
			t.Fatalf("backend error leaked: %s", encoded)
		}
	})

	t.Run("adapter cap is an output limit", func(t *testing.T) {
		tracker := &jiraGraphDevelopmentTracker{
			jiraGraphTracker: developmentGraphFixture(),
			err:              fmt.Errorf("%w: %w", domain.ErrCheckFailed, domain.ErrOutputLimit),
		}
		result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeDevelopment: true})
		if err != nil {
			t.Fatal(err)
		}
		assertDevelopmentSource(t, result, domain.ArtifactSourcePartial, domain.ArtifactPartialOutputLimit, true)
		assertNoDevelopmentFacts(t, result)
	})

	t.Run("projection bound drops every development fact", func(t *testing.T) {
		tracker := &jiraGraphDevelopmentTracker{jiraGraphTracker: developmentGraphFixture(), inventory: completeDevelopmentInventory()}
		result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeDevelopment: true, MaxNodes: 1})
		if err != nil {
			t.Fatal(err)
		}
		assertDevelopmentSource(t, result, domain.ArtifactSourcePartial, domain.ArtifactPartialOutputLimit, true)
		assertNoDevelopmentFacts(t, result)
	})
}

func TestIssueGraphDevelopmentValidatorRejectsClosedContractMutations(t *testing.T) {
	tracker := &jiraGraphDevelopmentTracker{jiraGraphTracker: developmentGraphFixture(), inventory: completeDevelopmentInventory()}
	result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeDevelopment: true})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*JiraIssueGraphResult){
		"missing opt in": func(r *JiraIssueGraphResult) { r.Bounds.IncludeDevelopment = false },
		"source count": func(r *JiraIssueGraphResult) {
			for index := range r.Sources {
				if r.Sources[index].Kind == "development" {
					r.Sources[index].Count++
					return
				}
			}
		},
		"source stability": func(r *JiraIssueGraphResult) {
			for index := range r.Sources {
				if r.Sources[index].Kind == "development" {
					r.Sources[index].Stability = domain.ArtifactStabilityPublicAPI
					return
				}
			}
		},
		"node identity": func(r *JiraIssueGraphResult) {
			for index := range r.Nodes {
				if r.Nodes[index].SCM != nil {
					r.Nodes[index].SCM.Host = "Git.Example.Test"
					return
				}
			}
		},
		"SCM on Jira": func(r *JiraIssueGraphResult) {
			r.Nodes[0].SCM = &domain.ArtifactGraphSCMIdentity{Host: "git.example.test", ProjectPath: "platform/widget"}
		},
		"edge stability": func(r *JiraIssueGraphResult) {
			for index := range r.Edges {
				if strings.HasPrefix(r.Edges[index].Kind, "development_") {
					r.Edges[index].Stability = domain.ArtifactStabilityPublicAPI
					return
				}
			}
		},
		"evidence hash": func(r *JiraIssueGraphResult) {
			for index := range r.Edges {
				if strings.HasPrefix(r.Edges[index].Kind, "development_") {
					r.Edges[index].Evidence[0].SourceID = strings.Repeat("b", 64)
					return
				}
			}
		},
		"edge narrative": func(r *JiraIssueGraphResult) {
			for index := range r.Edges {
				if strings.HasPrefix(r.Edges[index].Kind, "development_") {
					r.Edges[index].Relation = "untrusted content"
					r.Edges[index].ID = graphEdgeID(r.Edges[index])
					return
				}
			}
		},
		"missing project edge": func(r *JiraIssueGraphResult) {
			projectID := ""
			for _, node := range r.Nodes {
				if node.Kind == "gitlab_project" {
					projectID = node.ID
					break
				}
			}
			nodes := r.Nodes[:0]
			for _, node := range r.Nodes {
				if node.ID != projectID {
					nodes = append(nodes, node)
				}
			}
			r.Nodes = nodes
			edges := r.Edges[:0]
			for _, edge := range r.Edges {
				if edge.To != projectID {
					edges = append(edges, edge)
				}
			}
			r.Edges = edges
			r.Summary.NodeCount--
			r.Summary.EdgeCount--
			r.Summary.EvidenceCount--
		},
		"empty with artifacts": func(r *JiraIssueGraphResult) {
			for index := range r.Sources {
				if r.Sources[index].Kind == "development" {
					r.Sources[index].Status = domain.ArtifactSourceEmpty
					r.Summary.SourceStatusCounts["complete"]--
					r.Summary.SourceStatusCounts["empty"]++
					return
				}
			}
		},
		"GitLab edge source": func(r *JiraIssueGraphResult) {
			gitlabID := ""
			for _, node := range r.Nodes {
				if strings.HasPrefix(node.Kind, "gitlab_") {
					gitlabID = node.ID
					break
				}
			}
			r.Edges[0].From = gitlabID
			for index := range r.Edges[0].Evidence {
				r.Edges[0].Evidence[index].SourceNodeID = gitlabID
			}
			r.Edges[0].ID = graphEdgeID(r.Edges[0])
			sort.Slice(r.Edges, func(i, j int) bool { return graphEdgeSortKey(r.Edges[i]) < graphEdgeSortKey(r.Edges[j]) })
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			raw, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var changed JiraIssueGraphResult
			if unmarshalErr := json.Unmarshal(raw, &changed); unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			mutate(&changed)
			if !errors.Is(validateJiraGraphV2Result(&changed), domain.ErrCheckFailed) {
				t.Fatal("mutated Development graph unexpectedly validated")
			}
		})
	}
}

func TestJiraDevelopmentProjectKeyRequiresCanonicalCoordinates(t *testing.T) {
	for _, invalid := range []domain.JiraDevelopmentProject{
		{Host: "git.example.test:443", ProjectPath: "platform/widget"},
		{Host: "git.example.test:0443", ProjectPath: "platform/widget"},
		{Host: "git.example.test:70000", ProjectPath: "platform/widget"},
		{Host: "git.example.test", ProjectPath: "platform/widget.git"},
	} {
		if _, ok := jiraDevelopmentProjectKey(invalid.Host, invalid.ProjectPath); ok {
			t.Fatalf("non-canonical project accepted: %+v", invalid)
		}
	}
	exactHost := strings.Repeat("a", jiraDevelopmentMaxURLBytes-len("https://")-len("/")-len("g/p"))
	if _, ok := jiraDevelopmentProjectKey(exactHost, "g/p"); !ok {
		t.Fatal("canonical URL at bound rejected")
	}
	if _, ok := jiraDevelopmentProjectKey(exactHost+"a", "g/p"); ok {
		t.Fatal("canonical URL above bound accepted")
	}
	overlongArtifact := domain.JiraDevelopmentInventory{
		Projects: []domain.JiraDevelopmentProject{{Host: exactHost, ProjectPath: "g/p"}},
		Commits: []domain.JiraDevelopmentCommit{{
			Host: exactHost, ProjectPath: "g/p", SHA: "0123456789abcdef0123456789abcdef01234567",
		}},
	}
	if _, ok := validateJiraDevelopmentInventory(overlongArtifact); ok {
		t.Fatal("artifact URL above bound accepted")
	}
}

func TestReconcileDevelopmentGraphNodeMergesUnknownMergeRequestStateInEitherOrder(t *testing.T) {
	node := func(state string) domain.ArtifactGraphNode {
		return domain.ArtifactGraphNode{
			ID: "gitlab:merge_request:hash:42", Kind: "gitlab_merge_request", Service: "gitlab",
			SCM: &domain.ArtifactGraphSCMIdentity{
				Host: "git.example.test", ProjectPath: "platform/widget",
				MergeRequestIID: "42", MergeRequestState: state,
			},
		}
	}
	for _, pair := range [][2]string{{"unknown", "open"}, {"open", "unknown"}} {
		merged, ok := reconcileDevelopmentGraphNode(node(pair[0]), node(pair[1]))
		if !ok || merged.SCM.MergeRequestState != "open" {
			t.Fatalf("pair=%v merged=%+v ok=%t", pair, merged.SCM, ok)
		}
	}
	if _, ok := reconcileDevelopmentGraphNode(node("open"), node("closed")); ok {
		t.Fatal("conflicting concrete merge-request states reconciled")
	}
}

func assertDevelopmentSource(t *testing.T, result *JiraIssueGraphResult, status domain.ArtifactGraphSourceStatus, reason string, truncated bool) {
	t.Helper()
	for _, source := range result.Sources {
		if source.Kind == "development" {
			if source.Status != status || source.Complete || source.PartialReason != reason || source.Truncated != truncated {
				t.Fatalf("development source = %+v", source)
			}
			return
		}
	}
	t.Fatal("development source missing")
}

func assertNoDevelopmentFacts(t *testing.T, result *JiraIssueGraphResult) {
	t.Helper()
	for _, node := range result.Nodes {
		if strings.HasPrefix(node.Kind, "gitlab_") {
			t.Fatalf("unexpected development node: %+v", node)
		}
	}
	for _, edge := range result.Edges {
		if strings.HasPrefix(edge.Kind, "development_") {
			t.Fatalf("unexpected development edge: %+v", edge)
		}
	}
}
