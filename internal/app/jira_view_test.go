package app

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

func TestJiraIssueViewUsesExactConfiguredProjection(t *testing.T) {
	tr := &recordingTracker{issue: &domain.Issue{
		Key:     "PROJ-1",
		Summary: "Transient view",
		Status:  "Open",
		Body:    "h1. Context\n\nBody.",
		Fields:  map[string]any{"customfield_1": "high"},
	}}
	cfg := &config.Config{Render: &config.RenderConfig{Jira: &config.RenderService{
		Profile:    "minimal",
		Include:    []string{SecStatus, SecCustomFields},
		FieldViews: []config.JiraFieldView{{ID: "customfield_1", Label: "Risk"}},
	}}}
	svc := &JiraService{tr: tr, cfg: cfg}

	res, err := svc.ViewIssue(context.Background(), "PROJ-1", JiraIssueViewOpts{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("ViewIssue: %v", err)
	}
	wantFields := []string{"summary", "description", "customfield_1", "status"}
	if !reflect.DeepEqual(tr.issueFields, wantFields) {
		t.Fatalf("fields = %v, want exact projection %v", tr.issueFields, wantFields)
	}
	for _, want := range []string{"# PROJ-1 — Transient view", "| Status | Open |", "| Risk | high |", "## Context"} {
		if !strings.Contains(res.Markdown, want) {
			t.Errorf("markdown missing %q:\n%s", want, res.Markdown)
		}
	}
	if !strings.Contains(res.Markdown, "<!-- atl:section description readonly -->") || strings.Contains(res.Markdown, "<!-- atl:section description editable -->") {
		t.Fatalf("transient Description marker must be read-only:\n%s", res.Markdown)
	}
}

func TestJiraIssueViewFetchesConfiguredEpicChildrenWithoutSidecar(t *testing.T) {
	tr := &recordingTracker{
		issue: &domain.Issue{
			Key:     "PROJ-1",
			Summary: "Epic",
			Type:    "Epic",
			Fields:  map[string]any{"issuetype": map[string]any{"name": "Epic", "hierarchyLevel": float64(1)}},
		},
		issues: []domain.Issue{{
			Key: "PROJ-2", Summary: "Child", Status: "Open", Type: "Story",
			Fields: map[string]any{"customfield_10001": "PROJ-1"},
		}},
	}
	svc := &JiraService{tr: tr, cfg: &config.Config{}}
	res, err := svc.ViewIssue(context.Background(), "PROJ-1", JiraIssueViewOpts{
		Root: t.TempDir(),
		Render: config.RenderService{
			Profile:   "minimal",
			Include:   []string{SecEpicChildren},
			EpicField: "customfield_10001",
		},
	})
	if err != nil {
		t.Fatalf("ViewIssue: %v", err)
	}
	if tr.searchJQL == "" || !strings.Contains(tr.searchJQL, "cf[10001]") {
		t.Fatalf("epic child query = %q", tr.searchJQL)
	}
	if want := []string{"summary", "description"}; !reflect.DeepEqual(tr.issueFields, want) {
		t.Fatalf("main fields = %v, want exact explicit-epic projection %v", tr.issueFields, want)
	}
	if want := []string{"summary", "status", "assignee", "customfield_10001"}; !reflect.DeepEqual(tr.searchFields, want) {
		t.Fatalf("related fields = %v, want exact transient row projection %v", tr.searchFields, want)
	}
	if !strings.Contains(res.Markdown, "# Epic Children") || !strings.Contains(res.Markdown, "PROJ-2 — Child") {
		t.Fatalf("transient related section missing:\n%s", res.Markdown)
	}
}
