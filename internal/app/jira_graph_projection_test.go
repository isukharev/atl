package app

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestNormalizeJiraIssueGraphProjection(t *testing.T) {
	tests := []struct {
		name               string
		projection         string
		selectors          []string
		includeDevelopment bool
		want               JiraIssueGraphProjectionOptions
	}{
		{
			name: "default full",
			want: JiraIssueGraphProjectionOptions{
				Projection: JiraIssueGraphProjectionFull, Selectors: []string{},
			},
		},
		{
			name:       "compact defaults to URLs",
			projection: " COMPACT ",
			want: JiraIssueGraphProjectionOptions{
				Projection: JiraIssueGraphProjectionCompact, Selectors: []string{JiraIssueGraphSelectorURLs},
			},
		},
		{
			name:               "compact Development default adds SCM",
			projection:         JiraIssueGraphProjectionCompact,
			includeDevelopment: true,
			want: JiraIssueGraphProjectionOptions{
				Projection: JiraIssueGraphProjectionCompact,
				Selectors:  []string{JiraIssueGraphSelectorURLs, JiraIssueGraphSelectorSCM},
			},
		},
		{
			name:               "repeat and comma selectors are canonical",
			projection:         JiraIssueGraphProjectionCompact,
			selectors:          []string{" SCM, urls ", "URLS", "scm"},
			includeDevelopment: true,
			want: JiraIssueGraphProjectionOptions{
				Projection: JiraIssueGraphProjectionCompact,
				Selectors:  []string{JiraIssueGraphSelectorURLs, JiraIssueGraphSelectorSCM},
			},
		},
		{
			name:       "none remains explicit",
			projection: JiraIssueGraphProjectionCompact,
			selectors:  []string{" NONE ", "none"},
			want: JiraIssueGraphProjectionOptions{
				Projection: JiraIssueGraphProjectionCompact, Selectors: []string{JiraIssueGraphSelectorNone},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeJiraIssueGraphProjection(test.projection, test.selectors, test.includeDevelopment)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("normalized = %#v, want %#v", got, test.want)
			}
		})
	}

	invalid := []struct {
		name               string
		projection         string
		selectors          []string
		includeDevelopment bool
	}{
		{name: "unknown projection", projection: "brief"},
		{name: "selector with full", projection: JiraIssueGraphProjectionFull, selectors: []string{"urls"}},
		{name: "selector with default full", selectors: []string{"urls"}},
		{name: "SCM without Development", projection: JiraIssueGraphProjectionCompact, selectors: []string{"scm"}},
		{name: "none combined", projection: JiraIssueGraphProjectionCompact, selectors: []string{"none,urls"}},
		{name: "empty repeated value", projection: JiraIssueGraphProjectionCompact, selectors: []string{"urls", ""}},
		{name: "empty comma value", projection: JiraIssueGraphProjectionCompact, selectors: []string{"urls,,scm"}, includeDevelopment: true},
		{name: "unknown selector", projection: JiraIssueGraphProjectionCompact, selectors: []string{"links"}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeJiraIssueGraphProjection(test.projection, test.selectors, test.includeDevelopment)
			if !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("error = %v, want usage", err)
			}
		})
	}
}

func TestProjectJiraIssueGraphCompactIsDeterministicPrivateAndNonMutating(t *testing.T) {
	tracker := completeGraphFixture()
	description := strings.Join([]string{
		"https://safe.example.test/artifact/12?token=private-query-canary#fragment",
		"https://safe.example.test/download/session-private-path-canary-0123456789",
	}, " ")
	tracker.snapshot.Fields["description"] = description
	tracker.snapshot.Issue.Fields["description"] = description
	tracker.comments = nil
	tracker.commentsErr = domain.ErrForbidden
	development := &jiraGraphDevelopmentTracker{
		jiraGraphTracker: tracker,
		inventory:        completeDevelopmentInventory(),
	}
	full, err := (&JiraService{tr: development, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(
		t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeDevelopment: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	fullBefore, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}

	opts, err := NormalizeJiraIssueGraphProjection(JiraIssueGraphProjectionCompact, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ProjectJiraIssueGraphCompact(full, opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ProjectJiraIssueGraphCompact(full, opts)
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
		t.Fatalf("compact projection is nondeterministic:\n%s\n%s", firstJSON, secondJSON)
	}
	if first.SchemaVersion != 1 || first.RootID != full.RootID ||
		first.Complete != full.Complete || first.Truncated != full.Truncated ||
		!reflect.DeepEqual(first.Bounds, full.Bounds) {
		t.Fatalf("qualification was not preserved: compact=%+v full=%+v", first, full)
	}
	if !reflect.DeepEqual(first.Projection.Selected, []string{"urls", "scm"}) || len(first.Projection.Omitted) != 0 {
		t.Fatalf("projection metadata = %#v", first.Projection)
	}

	urlCount, scmCount := 0, 0
	safeSeen, opaqueSeen := false, false
	for index, fact := range first.Facts {
		if fact.Depth < 1 || len(fact.SourceNodeIDs) == 0 || !sort.StringsAreSorted(fact.SourceNodeIDs) {
			t.Fatalf("fact %d is unqualified or unordered: %#v", index, fact)
		}
		for _, sourceNodeID := range fact.SourceNodeIDs {
			if !strings.HasPrefix(sourceNodeID, "jira:issue:") {
				t.Fatalf("fact %d source is not a content-free node ID: %q", index, sourceNodeID)
			}
		}
		switch fact.Class {
		case JiraIssueGraphSelectorURLs:
			urlCount++
			if fact.Kind != "url" || fact.SCM != nil {
				t.Fatalf("URL fact contains non-URL fields: %#v", fact)
			}
			if fact.URL == "https://safe.example.test/artifact/12?redacted=redacted" {
				safeSeen = true
			}
			if fact.URL == "" && strings.HasPrefix(fact.NodeID, "candidate:url:") {
				opaqueSeen = true
			}
		case JiraIssueGraphSelectorSCM:
			scmCount++
			if fact.SCM == nil || fact.URL != "" || !strings.HasPrefix(fact.Kind, "gitlab_") {
				t.Fatalf("SCM fact is not coordinate-only: %#v", fact)
			}
		default:
			t.Fatalf("unknown fact class: %#v", fact)
		}
	}
	if !safeSeen || !opaqueSeen || scmCount != 4 {
		t.Fatalf("selected facts: safe=%t opaque=%t urls=%d scm=%d facts=%#v", safeSeen, opaqueSeen, urlCount, scmCount, first.Facts)
	}
	encoded := string(firstJSON)
	for _, forbidden := range []string{
		"private-query-canary", "private-path-canary", "#fragment",
		"https://git.example.test", "/-/commit/", "/-/tree/", "/-/merge_requests/",
		"json_pointer", "source_id", "collector",
	} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("compact projection leaked %q: %s", forbidden, encoded)
		}
	}

	if len(first.Sources) != 2 || first.Sources[0].Kind != "comments" ||
		first.Sources[1].Kind != "development" || first.Sources[1].Count != 3 ||
		first.Sources[1].Status != domain.ArtifactSourceComplete {
		t.Fatalf("retained sources = %#v", first.Sources)
	}
	assertJiraIssueGraphCompactSummary(t, full, first, urlCount, scmCount)

	first.Projection.Selected[0] = "changed"
	first.Summary.Collected.SourceStatusCounts["complete"]++
	first.Summary.Projected.SourceStatusCounts["complete"]++
	first.Facts[0].SourceNodeIDs[0] = "changed"
	for index := range first.Facts {
		if first.Facts[index].SCM != nil {
			first.Facts[index].SCM.Host = "changed"
			break
		}
	}
	*first.Sources[0].NodeDepth = 99
	first.Warnings[0] = "changed"
	fullAfter, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if string(fullAfter) != string(fullBefore) {
		t.Fatalf("compact projection mutated full graph:\n before: %s\n after: %s", fullBefore, fullAfter)
	}
}

func TestProjectJiraIssueGraphCompactQualifiesDevelopmentStates(t *testing.T) {
	t.Run("complete empty retained", func(t *testing.T) {
		tracker := &jiraGraphDevelopmentTracker{
			jiraGraphTracker: developmentGraphFixture(),
			inventory:        domain.JiraDevelopmentInventory{},
		}
		full, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(
			t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeDevelopment: true},
		)
		if err != nil {
			t.Fatal(err)
		}
		compact, err := ProjectJiraIssueGraphCompact(full, JiraIssueGraphProjectionOptions{
			Projection: JiraIssueGraphProjectionCompact, Selectors: []string{JiraIssueGraphSelectorSCM},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(compact.Facts) != 0 || len(compact.Sources) != 1 ||
			compact.Sources[0].Kind != "development" || compact.Sources[0].Count != 0 ||
			compact.Sources[0].Status != domain.ArtifactSourceEmpty || !compact.Sources[0].Complete {
			t.Fatalf("complete-empty Development projection = %+v", compact)
		}
		if !reflect.DeepEqual(compact.Projection.Selected, []string{"scm"}) ||
			!reflect.DeepEqual(compact.Projection.Omitted, []string{"urls"}) {
			t.Fatalf("projection metadata = %#v", compact.Projection)
		}
		assertJiraIssueGraphCompactSummary(t, full, compact, 0, 0)
	})

	t.Run("incomplete retained without facts", func(t *testing.T) {
		full, err := (&JiraService{tr: developmentGraphFixture(), baseURL: "https://jira.example.test"}).IssueGraphWithOptions(
			t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeDevelopment: true},
		)
		if err != nil {
			t.Fatal(err)
		}
		compact, err := ProjectJiraIssueGraphCompact(full, JiraIssueGraphProjectionOptions{
			Projection: JiraIssueGraphProjectionCompact, Selectors: []string{JiraIssueGraphSelectorSCM},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(compact.Facts) != 0 || len(compact.Sources) != 1 ||
			compact.Sources[0].Kind != "development" || compact.Sources[0].Complete ||
			compact.Sources[0].Status != domain.ArtifactSourceUnsupported || compact.Complete {
			t.Fatalf("incomplete Development projection = %+v", compact)
		}
		if compact.Summary.Collected.IncompleteSourceCount != 1 ||
			compact.Summary.Projected.IncompleteSourceCount != 1 {
			t.Fatalf("incomplete reconciliation = %#v", compact.Summary)
		}
		assertJiraIssueGraphCompactSummary(t, full, compact, 0, 0)
	})

	t.Run("unselected complete Development is omitted", func(t *testing.T) {
		tracker := &jiraGraphDevelopmentTracker{
			jiraGraphTracker: developmentGraphFixture(), inventory: completeDevelopmentInventory(),
		}
		full, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(
			t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeDevelopment: true},
		)
		if err != nil {
			t.Fatal(err)
		}
		compact, err := ProjectJiraIssueGraphCompact(full, JiraIssueGraphProjectionOptions{
			Projection: JiraIssueGraphProjectionCompact, Selectors: []string{JiraIssueGraphSelectorURLs},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, fact := range compact.Facts {
			if fact.Class == JiraIssueGraphSelectorSCM || fact.SCM != nil {
				t.Fatalf("unselected SCM fact = %#v", fact)
			}
		}
		for _, source := range compact.Sources {
			if source.Kind == "development" {
				t.Fatalf("unselected complete Development source = %#v", source)
			}
		}
		if !reflect.DeepEqual(compact.Projection.Selected, []string{"urls"}) ||
			!reflect.DeepEqual(compact.Projection.Omitted, []string{"scm"}) {
			t.Fatalf("projection metadata = %#v", compact.Projection)
		}
	})
}

func TestProjectJiraIssueGraphCompactPreservesBoundsFrontierAndIncompleteSources(t *testing.T) {
	service, _ := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", []string{"PROJ-2"}, ""),
	})
	full, err := service.IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{Depth: 1, MaxNodes: 1})
	if err != nil {
		t.Fatal(err)
	}
	fullBefore, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	compact, err := ProjectJiraIssueGraphCompact(full, JiraIssueGraphProjectionOptions{
		Projection: JiraIssueGraphProjectionCompact, Selectors: []string{JiraIssueGraphSelectorNone},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(compact.Facts) != 0 || len(compact.Projection.Selected) != 0 ||
		!reflect.DeepEqual(compact.Projection.Omitted, []string{"urls", "scm"}) {
		t.Fatalf("qualification-only projection = %+v", compact)
	}
	if compact.Complete != full.Complete || compact.Truncated != full.Truncated ||
		!reflect.DeepEqual(compact.Bounds, full.Bounds) ||
		!reflect.DeepEqual(compact.Frontier, full.Frontier) ||
		!reflect.DeepEqual(compact.Warnings, full.Warnings) {
		t.Fatalf("bounded qualification was not preserved: compact=%+v full=%+v", compact, full)
	}
	if len(compact.Frontier) != 1 || compact.Frontier[0].NodeID != "jira:issue:PROJ-2" ||
		compact.Frontier[0].Reason != domain.ArtifactPartialOutputLimit {
		t.Fatalf("frontier = %#v", compact.Frontier)
	}
	if len(compact.Sources) != full.Summary.IncompleteSourceCount {
		t.Fatalf("retained sources=%d, incomplete=%d", len(compact.Sources), full.Summary.IncompleteSourceCount)
	}
	for _, source := range compact.Sources {
		if source.Complete {
			t.Fatalf("complete source survived qualification-only projection: %#v", source)
		}
	}
	assertJiraIssueGraphCompactSummary(t, full, compact, 0, 0)

	compact.Frontier[0].NodeID = "changed"
	compact.Warnings[0] = "changed"
	*compact.Sources[0].NodeDepth = 99
	fullAfter, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if string(fullBefore) != string(fullAfter) {
		t.Fatalf("qualified projection mutated full graph:\n before: %s\n after: %s", fullBefore, fullAfter)
	}
}

func TestProjectJiraIssueGraphCompactRejectsMalformedFullGraphBeforeOptions(t *testing.T) {
	full, err := (&JiraService{tr: completeGraphFixture(), baseURL: "https://jira.example.test"}).IssueGraphWithOptions(
		t.Context(), "PROJ-1", JiraIssueGraphOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	var malformed JiraIssueGraphResult
	if err := json.Unmarshal(encoded, &malformed); err != nil {
		t.Fatal(err)
	}
	malformed.Summary.NodeCount++
	_, err = ProjectJiraIssueGraphCompact(&malformed, JiraIssueGraphProjectionOptions{Projection: "invalid"})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v, want graph validation before option usage", err)
	}
}

func TestProjectJiraIssueGraphCompactRejectsNoncanonicalURLFact(t *testing.T) {
	tracker := completeGraphFixture()
	tracker.snapshot.Fields["description"] = "https://safe.example.test/artifact/12"
	tracker.snapshot.Issue.Fields["description"] = tracker.snapshot.Fields["description"]
	full, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(
		t.Context(), "PROJ-1", JiraIssueGraphOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for index := range full.Nodes {
		if full.Nodes[index].Kind == "url" {
			full.Nodes[index].URL = "https://safe.example.test/token/private-path-canary"
			found = true
			break
		}
	}
	if !found {
		t.Fatal("URL fixture node missing")
	}
	if err := ValidateJiraIssueGraphResult(full); err != nil {
		t.Fatalf("fixture should exercise the projector's stricter safe-URL boundary: %v", err)
	}
	_, err = ProjectJiraIssueGraphCompact(full, JiraIssueGraphProjectionOptions{
		Projection: JiraIssueGraphProjectionCompact, Selectors: []string{JiraIssueGraphSelectorURLs},
	})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v, want fail-closed URL projection", err)
	}
}

func assertJiraIssueGraphCompactSummary(t *testing.T, full *JiraIssueGraphResult, compact *JiraIssueGraphCompactResult, urlCount, scmCount int) {
	t.Helper()
	summary := compact.Summary
	if summary.Collected.NodeCount != full.Summary.NodeCount ||
		summary.Collected.EdgeCount != full.Summary.EdgeCount ||
		summary.Collected.EvidenceCount != full.Summary.EvidenceCount ||
		summary.Collected.SourceCount != full.Summary.SourceCount ||
		summary.Collected.IncompleteSourceCount != full.Summary.IncompleteSourceCount ||
		!reflect.DeepEqual(summary.Collected.SourceStatusCounts, full.Summary.SourceStatusCounts) {
		t.Fatalf("collected summary = %#v, full = %#v", summary.Collected, full.Summary)
	}
	if summary.Projected.FactCount != len(compact.Facts) ||
		summary.Projected.SourceCount != len(compact.Sources) ||
		summary.Projected.URLCount != urlCount || summary.Projected.SCMCount != scmCount ||
		summary.Projected.IncompleteSourceCount != jiraIssueGraphIncompleteSourceCount(compact.Sources) ||
		!reflect.DeepEqual(summary.Projected.SourceStatusCounts, jiraIssueGraphSourceStatusCounts(compact.Sources)) {
		t.Fatalf("projected summary = %#v", summary.Projected)
	}
	if !summary.CollectedCountsMatchFull || !summary.ProjectedFactCountMatchesFacts ||
		!summary.FactClassCountsMatchFacts || !summary.ProjectedSourceCountMatchesSources ||
		!summary.SourceStatusCountsMatchSources || !summary.IncompleteCountMatchesSources {
		t.Fatalf("reconciliation flags = %#v", summary)
	}
}
