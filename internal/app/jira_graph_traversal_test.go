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

type jiraGraphTraversalTracker struct {
	domain.Tracker
	snapshots   map[string]*domain.QualifiedIssueSnapshot
	errors      map[string]error
	reads       map[string]int
	readOrder   []string
	commentsErr error
	comments    []domain.Comment
	worklogsErr error
	remoteErr   error
	remoteLinks domain.JiraRemoteLinkInventory
}

func (t *jiraGraphTraversalTracker) ReadIssueSnapshot(_ context.Context, key string) (*domain.QualifiedIssueSnapshot, error) {
	t.reads[key]++
	t.readOrder = append(t.readOrder, key)
	if err := t.errors[key]; err != nil {
		return nil, err
	}
	return t.snapshots[key], nil
}

func (t *jiraGraphTraversalTracker) ListComments(context.Context, string) ([]domain.Comment, error) {
	if t.commentsErr != nil {
		return nil, t.commentsErr
	}
	if t.comments != nil {
		return t.comments, nil
	}
	return []domain.Comment{}, nil
}

func (t *jiraGraphTraversalTracker) ListIssueWorklogs(context.Context, string) (*domain.IssueWorklogList, error) {
	if t.worklogsErr != nil {
		return nil, t.worklogsErr
	}
	return &domain.IssueWorklogList{Worklogs: []domain.IssueWorklog{}, Total: 0, Complete: true}, nil
}

func (t *jiraGraphTraversalTracker) AddIssueWorklog(context.Context, string, domain.IssueWorklogCreate) (*domain.IssueWorklog, error) {
	panic("unexpected write")
}

func (t *jiraGraphTraversalTracker) ReadIssueRemoteLinks(context.Context, string) (domain.JiraRemoteLinkInventory, error) {
	if t.remoteErr != nil {
		return domain.JiraRemoteLinkInventory{}, t.remoteErr
	}
	if t.remoteLinks.Total != 0 || len(t.remoteLinks.Links) != 0 || t.remoteLinks.Unsupported != 0 {
		return t.remoteLinks, nil
	}
	return domain.JiraRemoteLinkInventory{Links: []domain.JiraRemoteLink{}}, nil
}

func traversalSnapshot(key string, neighbors []string, narrative string) *domain.QualifiedIssueSnapshot {
	links := make([]any, 0, len(neighbors))
	for index, neighbor := range neighbors {
		links = append(links, map[string]any{
			"id":           fmt.Sprint(index + 1),
			"type":         map[string]any{"name": "Relates", "outward": "relates to"},
			"outwardIssue": map[string]any{"id": fmt.Sprint(10_000 + index), "key": neighbor},
		})
	}
	fields := map[string]any{
		"issuelinks":  links,
		"attachment":  []any{},
		"description": narrative,
	}
	id := "id-" + key
	return &domain.QualifiedIssueSnapshot{
		RequestedKey: key,
		ID:           id,
		Key:          key,
		Issue:        domain.Issue{ID: id, Key: key, Summary: key, Fields: fields},
		Fields:       fields,
		Names:        map[string]string{"description": "Description"},
		Schema:       map[string]domain.IssueFieldSchema{"description": {Type: "string"}},
		Properties:   map[string]any{},
	}
}

func traversalService(snapshots map[string]*domain.QualifiedIssueSnapshot) (*JiraService, *jiraGraphTraversalTracker) {
	tracker := &jiraGraphTraversalTracker{
		snapshots: snapshots,
		errors:    map[string]error{},
		reads:     map[string]int{},
	}
	return &JiraService{tr: tracker, baseURL: "https://jira.example.test"}, tracker
}

func TestIssueGraphWithOptionsTraversesCyclesAndDiamondsOnce(t *testing.T) {
	service, tracker := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2", "PROJ-3"}, ""),
		"PROJ-2": traversalSnapshot("PROJ-2", []string{"PROJ-4"}, ""),
		"PROJ-3": traversalSnapshot("PROJ-3", []string{"PROJ-4"}, ""),
		"PROJ-4": traversalSnapshot("PROJ-4", []string{"PROJ-1"}, ""),
	})

	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"PROJ-1", "PROJ-2", "PROJ-3", "PROJ-4"} {
		if tracker.reads[key] != 1 {
			t.Fatalf("reads[%s] = %d", key, tracker.reads[key])
		}
	}
	if result.SchemaVersion != 2 || result.Bounds.AttemptedNodes != 4 || result.Bounds.ExpandedNodes != 4 || result.Bounds.FollowedNodes != 3 {
		t.Fatalf("bounds = %#v", result.Bounds)
	}
	if len(result.Sources) != 4*len(jiraGraphSourceOrder) {
		t.Fatalf("sources = %d", len(result.Sources))
	}
	depths := map[string]int{}
	for _, node := range result.Nodes {
		depths[node.ID] = node.Depth
	}
	if depths["jira:issue:PROJ-1"] != 0 || depths["jira:issue:PROJ-2"] != 1 ||
		depths["jira:issue:PROJ-3"] != 1 || depths["jira:issue:PROJ-4"] != 2 {
		t.Fatalf("depths = %#v", depths)
	}
	for _, edge := range result.Edges {
		for _, evidence := range edge.Evidence {
			if evidence.SourceNodeID != edge.From {
				t.Fatalf("edge/evidence provenance = %#v / %#v", edge, evidence)
			}
		}
	}
}

func TestIssueGraphWithOptionsIsDeterministic(t *testing.T) {
	snapshots := map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-3", "PROJ-2"}, ""),
		"PROJ-2": traversalSnapshot("PROJ-2", nil, ""),
		"PROJ-3": traversalSnapshot("PROJ-3", nil, ""),
	}
	service, _ := traversalService(snapshots)
	first, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	service, _ = traversalService(snapshots)
	second, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	left, _ := json.Marshal(first)
	right, _ := json.Marshal(second)
	if string(left) != string(right) {
		t.Fatalf("nondeterministic output\n%s\n%s", left, right)
	}
}

func TestIssueGraphWithOptionsDoesNotFollowNarrativeMentions(t *testing.T) {
	service, tracker := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", nil, "PROJ-9"),
		"PROJ-9": traversalSnapshot("PROJ-9", nil, ""),
	})
	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 3})
	if err != nil {
		t.Fatal(err)
	}
	if tracker.reads["PROJ-9"] != 0 {
		t.Fatalf("narrative issue reads = %d", tracker.reads["PROJ-9"])
	}
	for _, node := range result.Nodes {
		if node.ID == "jira:issue:PROJ-9" && node.State != domain.ArtifactNodeUnresolved {
			t.Fatalf("narrative node = %#v", node)
		}
	}
}

func TestIssueGraphWithOptionsDepthZeroDoesNotFollowExactRelation(t *testing.T) {
	service, tracker := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2"}, ""),
	})
	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if tracker.reads["PROJ-2"] != 0 || result.Bounds.ExpandedNodes != 1 || result.Bounds.FollowedNodes != 0 {
		t.Fatalf("depth-zero traversal reads=%#v bounds=%#v", tracker.reads, result.Bounds)
	}
	foundStub := false
	for _, node := range result.Nodes {
		if node.ID == "jira:issue:PROJ-2" {
			foundStub = node.State == domain.ArtifactNodeStub && !node.Expanded && node.Depth == 1
		}
	}
	if !foundStub {
		t.Fatalf("exact depth-one stub missing: %#v", result.Nodes)
	}
}

func TestIssueGraphWithOptionsDoesNotFollowSameOriginNarrativeURL(t *testing.T) {
	service, tracker := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", nil, "https://jira.example.test/browse/PROJ-9"),
		"PROJ-9": traversalSnapshot("PROJ-9", nil, ""),
	})
	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 3})
	if err != nil {
		t.Fatal(err)
	}
	if tracker.reads["PROJ-9"] != 0 {
		t.Fatalf("narrative service URL reads = %d", tracker.reads["PROJ-9"])
	}
	for _, node := range result.Nodes {
		if node.ID == "jira:issue:PROJ-9" && node.State != domain.ArtifactNodeStub {
			t.Fatalf("narrative service URL node = %#v", node)
		}
	}
}

func TestIssueGraphWithOptionsDoesNotFollowSameOriginRemoteLink(t *testing.T) {
	service, tracker := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", nil, ""),
		"PROJ-9": traversalSnapshot("PROJ-9", nil, ""),
	})
	tracker.remoteLinks = domain.JiraRemoteLinkInventory{
		Total: 1,
		Links: []domain.JiraRemoteLink{{
			ID: "1", Relationship: "references",
			ObjectURL: "https://jira.example.test/browse/PROJ-9", ObjectTitle: "Remote issue",
		}},
	}
	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 3})
	if err != nil {
		t.Fatal(err)
	}
	if tracker.reads["PROJ-9"] != 0 {
		t.Fatalf("remote-link service URL reads = %d", tracker.reads["PROJ-9"])
	}
	for _, node := range result.Nodes {
		if node.ID == "jira:issue:PROJ-9" && node.State != domain.ArtifactNodeStub {
			t.Fatalf("remote-link service URL node = %#v", node)
		}
	}
}

func TestIssueGraphWithOptionsAcceptsOpaqueCandidateURLIdentity(t *testing.T) {
	service, _ := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", nil, "https://external.example.test/token/secret"),
	})
	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range result.Nodes {
		if strings.HasPrefix(node.ID, "candidate:url:") {
			if node.URL != "" || node.State != domain.ArtifactNodeUnresolved {
				t.Fatalf("opaque URL node = %#v", node)
			}
			return
		}
	}
	t.Fatal("opaque URL node missing")
}

func TestIssueGraphWithOptionsPromotesExactRelationAfterNarrativeCandidate(t *testing.T) {
	service, tracker := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2"}, "PROJ-3"),
		"PROJ-2": traversalSnapshot("PROJ-2", []string{"PROJ-3"}, ""),
		"PROJ-3": traversalSnapshot("PROJ-3", nil, ""),
	})
	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	if tracker.reads["PROJ-3"] != 1 {
		t.Fatalf("promoted structured target reads = %d", tracker.reads["PROJ-3"])
	}
	for _, node := range result.Nodes {
		if node.ID == "jira:issue:PROJ-3" &&
			(node.State != domain.ArtifactNodeResolved || !node.Expanded || node.Depth != 2) {
			t.Fatalf("promoted target = %#v", node)
		}
	}
}

func TestIssueGraphWithOptionsReconcilesMovedKeyCollisionOnce(t *testing.T) {
	moved := traversalSnapshot("PROJ-3", nil, "")
	moved.RequestedKey = "PROJ-2"
	service, tracker := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2", "PROJ-3"}, ""),
		"PROJ-2": moved,
		"PROJ-3": traversalSnapshot("PROJ-3", nil, ""),
	})
	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if tracker.reads["PROJ-2"] != 1 || tracker.reads["PROJ-3"] != 0 {
		t.Fatalf("moved-key reads = %#v", tracker.reads)
	}
	if result.Bounds.AttemptedNodes != 2 || result.Bounds.ExpandedNodes != 2 ||
		result.Bounds.FollowedNodes != 1 {
		t.Fatalf("moved-key bounds = %#v", result.Bounds)
	}
	var links []domain.ArtifactGraphEdge
	for _, edge := range result.Edges {
		if edge.Kind == "jira_link" {
			links = append(links, edge)
		}
	}
	if len(links) != 1 || links[0].To != "jira:issue:PROJ-3" || len(links[0].Evidence) != 2 {
		t.Fatalf("reconciled links = %#v", links)
	}
}

func TestIssueGraphWithOptionsCanonicalizesMovedAliasDiscoveredLater(t *testing.T) {
	moved := traversalSnapshot("PROJ-3", nil, "")
	moved.RequestedKey = "PROJ-2"
	service, tracker := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2", "PROJ-4"}, ""),
		"PROJ-2": moved,
		"PROJ-4": traversalSnapshot("PROJ-4", []string{"PROJ-2"}, ""),
	})
	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	if tracker.reads["PROJ-2"] != 1 || tracker.reads["PROJ-3"] != 0 {
		t.Fatalf("alias reads = %#v", tracker.reads)
	}
	for _, node := range result.Nodes {
		if node.ID == "jira:issue:PROJ-2" {
			t.Fatalf("moved alias was recreated: %#v", node)
		}
	}
	for _, edge := range result.Edges {
		if edge.To == "jira:issue:PROJ-2" || edge.From == "jira:issue:PROJ-2" {
			t.Fatalf("edge retained moved alias: %#v", edge)
		}
	}
}

func TestIssueGraphWithOptionsQualifiesNeighborFailure(t *testing.T) {
	service, tracker := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2"}, ""),
	})
	tracker.errors["PROJ-2"] = domain.ErrForbidden
	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Bounds.AttemptedNodes != 2 || result.Bounds.ExpandedNodes != 1 || len(result.Sources) != 16 {
		t.Fatalf("result summary = %#v, sources=%d", result.Bounds, len(result.Sources))
	}
	if result.Bounds.FollowedNodes != 1 {
		t.Fatalf("followed nodes = %d", result.Bounds.FollowedNodes)
	}
	for _, node := range result.Nodes {
		if node.ID == "jira:issue:PROJ-2" && node.State != domain.ArtifactNodeForbidden {
			t.Fatalf("failed node = %#v", node)
		}
	}
	for _, source := range result.Sources {
		if source.NodeID == "jira:issue:PROJ-2" && source.Status != domain.ArtifactSourceForbidden {
			t.Fatalf("failed source = %#v", source)
		}
	}
}

func TestIssueGraphWithOptionsAdmitsNodeEdgeEvidenceAtomically(t *testing.T) {
	service, _ := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2"}, ""),
	})
	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{
		Depth: 1, MaxNodes: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 1 || len(result.Edges) != 0 || result.Summary.EvidenceCount != 0 {
		t.Fatalf("non-atomic projection: nodes=%d edges=%d evidence=%d", len(result.Nodes), len(result.Edges), result.Summary.EvidenceCount)
	}
	if len(result.Frontier) != 1 || result.Frontier[0].NodeID != "jira:issue:PROJ-2" || result.Frontier[0].Reason != domain.ArtifactPartialOutputLimit {
		t.Fatalf("frontier = %#v", result.Frontier)
	}
}

func TestIssueGraphWithOptionsMarksEveryDroppedEvidenceCollectorPartial(t *testing.T) {
	service, tracker := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", nil, "PROJ-2"),
	})
	tracker.comments = []domain.Comment{{ID: "1", Body: "PROJ-2"}}
	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{MaxNodes: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"issue_fields", "comments"} {
		source := graphV2Source(t, result, result.RootID, kind)
		if source.Status != domain.ArtifactSourcePartial ||
			source.PartialReason != domain.ArtifactPartialOutputLimit || !source.Truncated {
			t.Fatalf("%s source = %#v", kind, source)
		}
	}
}

func TestIssueGraphWithOptionsValidatesBoundsBeforeReading(t *testing.T) {
	service, tracker := traversalService(nil)
	_, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 4})
	if !errors.Is(err, domain.ErrUsage) || len(tracker.reads) != 0 {
		t.Fatalf("error=%v reads=%#v", err, tracker.reads)
	}
}

func TestIssueGraphWithOptionsSortsTheWholeDepthFrontier(t *testing.T) {
	service, tracker := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2", "PROJ-3"}, ""),
		"PROJ-2": traversalSnapshot("PROJ-2", []string{"PROJ-9"}, ""),
		"PROJ-3": traversalSnapshot("PROJ-3", []string{"PROJ-4"}, ""),
		"PROJ-4": traversalSnapshot("PROJ-4", nil, ""),
		"PROJ-9": traversalSnapshot("PROJ-9", nil, ""),
	})
	_, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PROJ-1", "PROJ-2", "PROJ-3", "PROJ-4", "PROJ-9"}
	if fmt.Sprint(tracker.readOrder) != fmt.Sprint(want) {
		t.Fatalf("read order = %#v, want %#v", tracker.readOrder, want)
	}
}

func TestIssueGraphWithOptionsClassifiesTransportBudgetAndFrontier(t *testing.T) {
	service, tracker := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2"}, ""),
	})
	tracker.commentsErr = domain.ErrReadAttemptBudgetExhausted
	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	source := graphV2Source(t, result, "jira:issue:PROJ-1", "comments")
	if source.PartialReason != domain.ArtifactPartialRequestLimit || !source.Truncated || source.Complete {
		t.Fatalf("comments source = %#v", source)
	}
	if len(result.Frontier) != 1 || result.Frontier[0].NodeID != "jira:issue:PROJ-2" ||
		result.Frontier[0].Reason != domain.ArtifactPartialRequestLimit {
		t.Fatalf("frontier = %#v", result.Frontier)
	}
}

func TestIssueGraphWithOptionsSelectsBudgetFrontierReasonDeterministically(t *testing.T) {
	service, tracker := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2"}, ""),
	})
	tracker.commentsErr = domain.ErrReadResponseBudgetExhausted
	tracker.worklogsErr = domain.ErrReadAttemptBudgetExhausted
	for range 20 {
		result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Frontier) != 1 || result.Frontier[0].Reason != domain.ArtifactPartialByteLimit {
			t.Fatalf("frontier = %#v", result.Frontier)
		}
	}
}

type traversalConfluenceReader struct {
	calls    []string
	metadata map[string]domain.ConfluenceGraphPageMetadata
	errors   map[string]error
}

func (r *traversalConfluenceReader) ReadGraphPageMetadata(_ context.Context, id string) (domain.ConfluenceGraphPageMetadata, error) {
	r.calls = append(r.calls, id)
	if err := r.errors[id]; err != nil {
		return domain.ConfluenceGraphPageMetadata{}, err
	}
	if metadata, ok := r.metadata[id]; ok {
		return metadata, nil
	}
	return domain.ConfluenceGraphPageMetadata{ID: id, Title: "Resolved page"}, nil
}

func TestIssueGraphWithOptionsResolvesConfluenceOnlyWhenRequested(t *testing.T) {
	snapshot := traversalSnapshot("PROJ-1", nil, "")
	snapshot.Properties["page"] = map[string]any{"pageId": "47"}
	service, _ := traversalService(map[string]*domain.QualifiedIssueSnapshot{"PROJ-1": snapshot})
	reader := &traversalConfluenceReader{}
	service.graphConfluence = reader

	without, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.calls) != 0 {
		t.Fatalf("flag-off calls = %#v", reader.calls)
	}
	for _, source := range without.Sources {
		if source.Kind == "confluence_metadata" {
			t.Fatalf("flag-off source = %#v", source)
		}
	}

	with, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{ResolveConfluence: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.calls) != 1 || reader.calls[0] != "47" {
		t.Fatalf("resolver calls = %#v", reader.calls)
	}
	for _, node := range with.Nodes {
		if node.ID == "confluence:page:47" && (node.State != domain.ArtifactNodeResolved || node.Label != "Resolved page") {
			t.Fatalf("resolved node = %#v", node)
		}
	}
	source := graphV2Source(t, with, with.RootID, "confluence_metadata")
	if source.Status != domain.ArtifactSourceComplete || !source.Complete || source.Count != 1 {
		t.Fatalf("resolver source = %#v", source)
	}
}

func TestIssueGraphWithOptionsValidatorIsLoadBearing(t *testing.T) {
	service, _ := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2"}, ""),
		"PROJ-2": traversalSnapshot("PROJ-2", nil, ""),
	})
	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*JiraIssueGraphResult)
	}{
		{name: "summary", mutate: func(r *JiraIssueGraphResult) { r.Summary.NodeCount++ }},
		{name: "node state", mutate: func(r *JiraIssueGraphResult) { r.Nodes[0].State = "invented" }},
		{name: "edge current", mutate: func(r *JiraIssueGraphResult) { r.Edges[0].Current = false }},
		{name: "evidence source", mutate: func(r *JiraIssueGraphResult) {
			r.Edges[0].Evidence[0].SourceNodeID = "jira:issue:PROJ-9"
		}},
		{name: "source stability", mutate: func(r *JiraIssueGraphResult) {
			r.Sources[0].Stability = domain.ArtifactStabilityHeuristic
		}},
		{name: "unexpected warning", mutate: func(r *JiraIssueGraphResult) {
			r.Warnings = []string{"dynamic"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var mutated JiraIssueGraphResult
			if unmarshalErr := json.Unmarshal(encoded, &mutated); unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			test.mutate(&mutated)
			if !errors.Is(validateJiraGraphV2Result(&mutated), domain.ErrCheckFailed) {
				t.Fatal("mutated graph unexpectedly validated")
			}
		})
	}
}

func TestIssueGraphWithOptionsQualifiesConfluenceMetadataResults(t *testing.T) {
	tests := []struct {
		name       string
		reader     *traversalConfluenceReader
		wantState  domain.ArtifactGraphNodeState
		wantStatus domain.ArtifactGraphSourceStatus
		wantReason string
		complete   bool
	}{
		{name: "missing is complete", reader: &traversalConfluenceReader{errors: map[string]error{"47": domain.ErrNotFound}}, wantState: domain.ArtifactNodeMissing, wantStatus: domain.ArtifactSourceComplete, complete: true},
		{name: "forbidden is incomplete", reader: &traversalConfluenceReader{errors: map[string]error{"47": domain.ErrForbidden}}, wantState: domain.ArtifactNodeForbidden, wantStatus: domain.ArtifactSourceForbidden},
		{name: "check failure is malformed", reader: &traversalConfluenceReader{errors: map[string]error{"47": domain.ErrCheckFailed}}, wantState: domain.ArtifactNodeStub, wantStatus: domain.ArtifactSourcePartial, wantReason: domain.ArtifactPartialMalformed},
		{name: "blank title is malformed", reader: &traversalConfluenceReader{metadata: map[string]domain.ConfluenceGraphPageMetadata{"47": {ID: "47"}}}, wantState: domain.ArtifactNodeStub, wantStatus: domain.ArtifactSourcePartial, wantReason: domain.ArtifactPartialMalformed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := traversalSnapshot("PROJ-1", nil, "")
			snapshot.Properties["page"] = map[string]any{"pageId": "47"}
			service, _ := traversalService(map[string]*domain.QualifiedIssueSnapshot{"PROJ-1": snapshot})
			service.graphConfluence = test.reader
			result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{ResolveConfluence: true})
			if err != nil {
				t.Fatal(err)
			}
			source := graphV2Source(t, result, result.RootID, "confluence_metadata")
			if source.Status != test.wantStatus || source.PartialReason != test.wantReason || source.Complete != test.complete {
				t.Fatalf("source = %#v", source)
			}
			for _, node := range result.Nodes {
				if node.ID == "confluence:page:47" && node.State != test.wantState {
					t.Fatalf("node = %#v", node)
				}
			}
		})
	}
}

func TestIssueGraphWithOptionsDoesNotCountRefusedNodeAttempt(t *testing.T) {
	service, tracker := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2"}, ""),
	})
	tracker.errors["PROJ-2"] = domain.ErrReadAttemptBudgetExhausted
	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Bounds.AttemptedNodes != 1 || result.Bounds.FollowedNodes != 0 || result.Bounds.ExpandedNodes != 1 {
		t.Fatalf("bounds = %#v", result.Bounds)
	}
}

func TestIssueGraphWithOptionsQualifiesRootByteBudgetExhaustion(t *testing.T) {
	service, tracker := traversalService(nil)
	tracker.errors["PROJ-1"] = domain.ErrReadResponseBudgetExhausted
	result, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || !result.Truncated || result.Bounds.AttemptedNodes != 1 ||
		result.Bounds.ExpandedNodes != 0 || result.Bounds.FollowedNodes != 0 ||
		len(result.Nodes) != 1 || result.Nodes[0].State != domain.ArtifactNodeUnresolved ||
		len(result.Frontier) != 1 || result.Frontier[0].Reason != domain.ArtifactPartialByteLimit {
		t.Fatalf("root budget result = %#v", result)
	}
	for _, source := range result.Sources {
		if source.Status != domain.ArtifactSourcePartial || !source.Truncated ||
			source.PartialReason != domain.ArtifactPartialByteLimit {
			t.Fatalf("root budget source = %#v", source)
		}
	}
	withResolution, err := service.IssueGraphWithOptions(context.Background(), "PROJ-1", JiraIssueGraphOptions{ResolveConfluence: true})
	if err != nil {
		t.Fatal(err)
	}
	source := graphV2Source(t, withResolution, withResolution.RootID, "confluence_metadata")
	if len(withResolution.Sources) != len(jiraGraphSourceOrder)+1 ||
		source.Status != domain.ArtifactSourcePartial || !source.Truncated ||
		source.PartialReason != domain.ArtifactPartialByteLimit {
		t.Fatalf("root budget resolution source = %#v", source)
	}
}

func graphV2Source(t *testing.T, result *JiraIssueGraphResult, nodeID, kind string) domain.ArtifactGraphSource {
	t.Helper()
	for _, source := range result.Sources {
		if source.NodeID == nodeID && source.Kind == kind {
			return source
		}
	}
	t.Fatalf("source %s/%s missing", nodeID, kind)
	return domain.ArtifactGraphSource{}
}
