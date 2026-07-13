package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

type jiraFieldInspectTracker struct {
	domain.Tracker
	defs      []domain.FieldDef
	issue     *domain.Issue
	requested []string
}

func (t *jiraFieldInspectTracker) Fields(context.Context) ([]domain.FieldDef, error) {
	return append([]domain.FieldDef(nil), t.defs...), nil
}

func (t *jiraFieldInspectTracker) GetIssue(_ context.Context, _ string, fields []string) (*domain.Issue, error) {
	t.requested = append([]string(nil), fields...)
	copy := *t.issue
	return &copy, nil
}

func TestResolveJiraFieldSelectorsByIDAndExactName(t *testing.T) {
	defs := []domain.FieldDef{
		{ID: "summary", Name: "Summary"},
		{ID: "customfield_1", Name: "Delivery Notes", Custom: true},
	}
	resolved, err := ResolveJiraFieldSelectors(defs, []string{"Delivery Notes", "summary", "DELIVERY NOTES"})
	if err != nil || len(resolved) != 2 || resolved[0].ID != "customfield_1" || resolved[1].ID != "summary" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	_, err = ResolveJiraFieldSelectors(append(defs, domain.FieldDef{ID: "customfield_2", Name: "Delivery Notes"}), []string{"delivery notes"})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "customfield_1, customfield_2") {
		t.Fatalf("ambiguity error=%v", err)
	}
	if _, err := ResolveJiraFieldSelectors(defs, []string{"Missing"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing error=%v", err)
	}
}

func TestJiraIssueFieldsDefaultsToNonEmptyCompactValues(t *testing.T) {
	tracker := &jiraFieldInspectTracker{
		defs: []domain.FieldDef{
			{ID: "summary", Name: "Summary", Schema: "string"},
			{ID: "assignee", Name: "Assignee", Schema: "user"},
			{ID: "customfield_1", Name: "Impact", Custom: true, Schema: "option"},
			{ID: "customfield_2", Name: "Empty", Custom: true, Schema: "string"},
		},
		issue: &domain.Issue{Key: "PROJ-1", Fields: map[string]any{
			"summary":       "Plan",
			"assignee":      map[string]any{"name": "alice", "displayName": "Alice", "emailAddress": "private@example.test", "avatarUrls": map[string]any{"48x48": "https://example.test/avatar"}, "self": "https://example.test/user", "active": true},
			"customfield_1": map[string]any{"id": "7", "value": "High", "self": "https://example.test/option"},
			"customfield_2": nil,
		}},
	}
	result, err := (&JiraService{tr: tracker}).IssueFields(context.Background(), "PROJ-1", JiraIssueFieldsOpts{})
	if err != nil || result.Mode != "compact" || !result.NonEmptyOnly || result.Count != 3 || tracker.requested[0] != "*all" {
		t.Fatalf("result=%+v requested=%v err=%v", result, tracker.requested, err)
	}
	encoded, _ := json.Marshal(result)
	for _, forbidden := range []string{"private@example.test", "avatar", "https://"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("compact output leaked %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"kind":"user"`) || !strings.Contains(string(encoded), `"kind":"option"`) {
		t.Fatalf("compact kinds missing: %s", encoded)
	}
}

func TestJiraIssueFieldsSelectorsIncludeEmptyAndRaw(t *testing.T) {
	tracker := &jiraFieldInspectTracker{
		defs:  []domain.FieldDef{{ID: "customfield_1", Name: "Risk", Custom: true}, {ID: "customfield_2", Name: "Empty", Custom: true}},
		issue: &domain.Issue{Key: "PROJ-1", Fields: map[string]any{"customfield_1": map[string]any{"private": "kept"}}},
	}
	result, err := (&JiraService{tr: tracker}).IssueFields(context.Background(), "PROJ-1", JiraIssueFieldsOpts{
		Selectors: []string{"Risk", "Empty"}, IncludeEmpty: true, Raw: true,
	})
	emptyFound := false
	for _, field := range result.Fields {
		emptyFound = emptyFound || (field.ID == "customfield_2" && field.Empty)
	}
	if err != nil || result.Mode != "raw" || result.Count != 2 || !emptyFound || strings.Join(tracker.requested, ",") != "customfield_1,customfield_2" {
		t.Fatalf("result=%+v requested=%v err=%v", result, tracker.requested, err)
	}
}

func TestJiraIssueFieldsIncludeEmptyUnionsCatalogAndObservedFields(t *testing.T) {
	tracker := &jiraFieldInspectTracker{
		defs: []domain.FieldDef{{ID: "summary", Name: "Summary", Schema: "string"}},
		issue: &domain.Issue{Key: "PROJ-1", Fields: map[string]any{
			"summary": nil, "pluginfield_1": "populated but absent from field catalog",
		}},
	}
	result, err := (&JiraService{tr: tracker}).IssueFields(context.Background(), "PROJ-1", JiraIssueFieldsOpts{IncludeEmpty: true})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]JiraIssueFieldRecord{}
	for _, field := range result.Fields {
		found[field.ID] = field
	}
	if len(found) != 2 || !found["summary"].Empty || found["pluginfield_1"].Empty {
		t.Fatalf("fields=%+v", result.Fields)
	}
}

func TestResolveJiraFieldSelectorsCallsIDCollisionsSelectors(t *testing.T) {
	_, err := ResolveJiraFieldSelectors([]domain.FieldDef{{ID: "Status"}, {ID: "status"}}, []string{"STATUS"})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "field selector") || strings.Contains(err.Error(), "field name") {
		t.Fatalf("err=%v", err)
	}
}

func TestCompactJiraFieldValueBoundsStringsArraysAndDepth(t *testing.T) {
	value := []any{strings.Repeat("x", jiraCompactFieldStringCap+10)}
	for range jiraCompactFieldArrayCap {
		value = append(value, "item")
	}
	compact, truncated := compactJiraFieldValue(value, 0)
	items := compact.([]any)
	if !truncated || len(items) != jiraCompactFieldArrayCap || len(items[0].(string)) != jiraCompactFieldStringCap {
		t.Fatalf("compact len=%d truncated=%t", len(items), truncated)
	}
}

func TestResolveRenderFieldSelectorsRecordsIDsAndHumanLabels(t *testing.T) {
	tracker := &jiraFieldInspectTracker{defs: []domain.FieldDef{{ID: "customfield_1", Name: "Delivery Notes", Custom: true}}}
	service := &JiraService{tr: tracker}
	rs := RenderSettings{CustomFields: []string{"Delivery Notes"}, FieldViews: []config.JiraFieldView{}}
	resolved, err := service.resolveRenderFieldSelectors(context.Background(), rs)
	if err != nil || len(resolved.CustomFields) != 0 || len(resolved.FieldViews) != 1 || resolved.FieldViews[0].ID != "customfield_1" || resolved.FieldViews[0].Label != "Delivery Notes" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
}

func TestViewIssueResolvesConfiguredFieldNameBeforeRequest(t *testing.T) {
	tracker := &jiraFieldInspectTracker{
		defs:  []domain.FieldDef{{ID: "customfield_1", Name: "Delivery Notes", Custom: true}},
		issue: &domain.Issue{Key: "PROJ-1", Summary: "Plan", Fields: map[string]any{"summary": "Plan", "description": "", "customfield_1": "On track"}},
	}
	service := &JiraService{tr: tracker, cfg: &config.Config{}}
	result, err := service.ViewIssue(context.Background(), "PROJ-1", JiraIssueViewOpts{Render: config.RenderService{
		Profile: "minimal", Include: []string{SecCustomFields}, CustomFields: []string{"Delivery Notes"},
	}})
	if err != nil || !strings.Contains(result.Markdown, "| Delivery Notes | On track |") || !strings.Contains(strings.Join(tracker.requested, ","), "customfield_1") {
		t.Fatalf("markdown=%q requested=%v err=%v", result.Markdown, tracker.requested, err)
	}
}
