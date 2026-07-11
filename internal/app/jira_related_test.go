package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type epicPullTracker struct {
	domain.Tracker
	mainQueries    int
	relatedQueries int
	fieldQueries   int
}

type pagedEpicTracker struct {
	domain.Tracker
	children []domain.Issue
	calls    int
}

type noEpicForbiddenFieldsTracker struct {
	domain.Tracker
	fieldCalls int
}

func (t *noEpicForbiddenFieldsTracker) Fields(context.Context) ([]domain.FieldDef, error) {
	t.fieldCalls++
	return nil, domain.ErrForbidden
}

func (t *noEpicForbiddenFieldsTracker) Search(context.Context, string, []string, int, string) ([]domain.Issue, string, error) {
	return []domain.Issue{{
		ID: "1", Key: "PROJ-1", Project: "PROJ", Type: "Task", Summary: "ordinary",
		Fields: map[string]any{"issuetype": map[string]any{"id": "3", "name": "Task"}},
	}}, "", nil
}

func (t *pagedEpicTracker) Search(_ context.Context, _ string, _ []string, limit int, cursor string) ([]domain.Issue, string, error) {
	t.calls++
	start := 0
	if cursor != "" {
		start, _ = strconv.Atoi(cursor)
	}
	end := start + limit
	if end > len(t.children) {
		end = len(t.children)
	}
	next := ""
	if end < len(t.children) {
		next = strconv.Itoa(end)
	}
	return t.children[start:end], next, nil
}

func (t *epicPullTracker) Fields(context.Context) ([]domain.FieldDef, error) {
	t.fieldQueries++
	return []domain.FieldDef{{ID: "customfield_10010", Name: "Epic Link", Custom: true}}, nil
}

func (t *epicPullTracker) Search(_ context.Context, jql string, _ []string, _ int, _ string) ([]domain.Issue, string, error) {
	if strings.Contains(jql, "cf[10010]") {
		t.relatedQueries++
		return []domain.Issue{
			{Key: "PROJ-3", Summary: "third", Status: "Done", Type: "Task", Fields: map[string]any{"customfield_10010": "PROJ-1"}},
			{Key: "PROJ-2", Summary: "second", Status: "Open", Type: "Story", Assignee: "alice", Fields: map[string]any{"customfield_10010": "PROJ-1"}},
		}, "", nil
	}
	t.mainQueries++
	return []domain.Issue{{
		ID: "1", Key: "PROJ-1", Summary: "Epic", Body: "body", Type: "Epic", Project: "PROJ",
		Fields: map[string]any{"summary": "Epic", "description": "body", "issuetype": map[string]any{"name": "Epic"}, "project": map[string]any{"key": "PROJ"}},
	}}, "", nil
}

func TestPullEpicChildrenSidecarAndOfflineRender(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Render: &config.RenderConfig{Jira: &config.RenderService{
		Profile: "full", Include: []string{SecEpicChildren},
	}}}
	tr := &epicPullTracker{}
	svc := &JiraService{tr: tr, cfg: cfg}
	res, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "key = PROJ-1", Into: root, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if tr.mainQueries != 1 || tr.relatedQueries != 1 || tr.fieldQueries != 1 {
		t.Fatalf("queries main=%d related=%d fields=%d, want one each", tr.mainQueries, tr.relatedQueries, tr.fieldQueries)
	}
	if len(res.Issues) != 1 || res.Issues[0].EpicChildren != 2 || res.EpicChildrenTruncated {
		t.Fatalf("pull result = %+v", res)
	}
	dir := filepath.Join(root, "PROJ")
	sidecar := loadEpicChildrenSidecar(root, filepath.Join(dir, "PROJ-1.epic-children.json"))
	if sidecar == nil || sidecar.EpicField != "customfield_10010" || len(sidecar.Children) != 2 || sidecar.Children[0].Key != "PROJ-2" {
		t.Fatalf("sidecar = %+v", sidecar)
	}
	mdPath := filepath.Join(dir, "PROJ-1.md")
	before := mustReadFile(t, mdPath)
	for _, want := range []string{"# Epic Children", "| Key | Summary | Status | Type | Assignee |", "| PROJ-2 | second | Open | Story | alice |", "| PROJ-3 | third | Done | Task |  |"} {
		if !strings.Contains(before, want) {
			t.Errorf("md missing %q:\n%s", want, before)
		}
	}
	vs, ok, err := mirror.New(root).ViewStateOf("PROJ-1")
	if err != nil || !ok || vs.EpicField != "customfield_10010" {
		t.Fatalf("view state ok=%v err=%v state=%+v", ok, err, vs)
	}
	wikiBefore := mustReadFile(t, filepath.Join(dir, "PROJ-1.wiki"))
	if _, err := NewJiraRenderer(cfg).Render(root, config.RenderService{}); err != nil {
		t.Fatal(err)
	}
	if got := mustReadFile(t, mdPath); got != before {
		t.Errorf("offline render differs from pull\n--- pull\n%s\n--- render\n%s", before, got)
	}
	if got := mustReadFile(t, filepath.Join(dir, "PROJ-1.wiki")); got != wikiBefore {
		t.Error("offline render changed wiki substrate")
	}
	vs, ok, err = mirror.New(root).ViewStateOf("PROJ-1")
	if err != nil || !ok || vs.EpicField != "customfield_10010" {
		t.Fatalf("offline render lost resolved epic field: ok=%v err=%v state=%+v", ok, err, vs)
	}
}

func TestPullConfiguredEpicDisplayNameSurvivesOfflineRender(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Render: &config.RenderConfig{Jira: &config.RenderService{
		Profile: "full", Include: []string{SecEpicChildren}, EpicField: "Epic Link",
	}}}
	svc := &JiraService{tr: &epicPullTracker{}, cfg: cfg}
	if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "key = PROJ-1", Into: root, Limit: 1}); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join(root, "PROJ", "PROJ-1.md")
	before := mustReadFile(t, mdPath)
	if _, err := NewJiraRenderer(cfg).Render(root, config.RenderService{}); err != nil {
		t.Fatal(err)
	}
	if got := mustReadFile(t, mdPath); got != before {
		t.Fatalf("offline display-name render differs from pull\n--- pull\n%s\n--- render\n%s", before, got)
	}
	sidecar := loadEpicChildrenSidecar(root, filepath.Join(root, "PROJ", "PROJ-1.epic-children.json"))
	if sidecar == nil || sidecar.EpicSelector != "Epic Link" || sidecar.EpicField != "customfield_10010" {
		t.Fatalf("sidecar did not preserve selector affinity: %+v", sidecar)
	}
	vs, ok, err := mirror.New(root).ViewStateOf("PROJ-1")
	if err != nil || !ok || vs.EpicField != "customfield_10010" {
		t.Fatalf("view state = %+v ok=%v err=%v", vs, ok, err)
	}
}

func TestPullCanonicalizesDirectEpicField(t *testing.T) {
	root := t.TempDir()
	tr := &epicPullTracker{}
	cfg := &config.Config{Render: &config.RenderConfig{Jira: &config.RenderService{
		Profile: "full", Include: []string{SecEpicChildren}, EpicField: "CUSTOMFIELD_10010",
	}}}
	if _, err := (&JiraService{tr: tr, cfg: cfg}).Pull(context.Background(), JiraPullOpts{
		JQL: "key = PROJ-1", Into: root, Limit: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if tr.fieldQueries != 0 || tr.relatedQueries != 1 {
		t.Fatalf("field queries=%d related queries=%d", tr.fieldQueries, tr.relatedQueries)
	}
	sidecar := loadEpicChildrenSidecar(root, filepath.Join(root, "PROJ", "PROJ-1.epic-children.json"))
	if sidecar == nil || sidecar.EpicField != "customfield_10010" {
		t.Fatalf("sidecar = %+v, want canonical field id", sidecar)
	}
}

func TestConfiguredEpicDisplayNameRejectsSidecarFromOldSelector(t *testing.T) {
	sidecar := &JiraEpicChildrenSidecar{
		Epic: "PROJ-1", EpicField: "customfield_10010", EpicSelector: "Old Epic Link",
	}
	if compatibleEpicSidecar(sidecar, "PROJ-1", "New Epic Link") {
		t.Fatal("sidecar from a different configured display name was accepted")
	}
}

func TestResolveEpicFieldConfiguredIDSkipsCatalog(t *testing.T) {
	tr := &epicPullTracker{}
	svc := &JiraService{tr: tr}

	got, err := svc.resolveEpicField(context.Background(), "customfield_10010")
	if err != nil {
		t.Fatal(err)
	}
	if got != "customfield_10010" {
		t.Fatalf("resolved field = %q, want customfield_10010", got)
	}
	if tr.fieldQueries != 0 {
		t.Fatalf("field catalog queries = %d, want 0 for an explicit field ID", tr.fieldQueries)
	}
}

func TestResolveEpicFieldCanonicalizesDirectSelector(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{"Parent", "parent"}, {"CUSTOMFIELD_10010", "customfield_10010"}} {
		got, err := (&JiraService{}).resolveEpicField(context.Background(), tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("resolveEpicField(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestIsEpicIssueRejectsHigherHierarchyLevels(t *testing.T) {
	issue := domain.Issue{Type: "Initiative", Fields: map[string]any{
		"issuetype": map[string]any{"name": "Initiative", "hierarchyLevel": float64(2)},
	}}
	if isEpicIssue(issue) {
		t.Fatal("hierarchy level above Epic was classified as Epic")
	}
}

func TestPullDefersEpicFieldResolutionForNonEpicResults(t *testing.T) {
	root := t.TempDir()
	tr := &noEpicForbiddenFieldsTracker{}
	cfg := &config.Config{Render: &config.RenderConfig{Jira: &config.RenderService{
		Profile: "full", Include: []string{SecEpicChildren},
	}}}
	res, err := (&JiraService{tr: tr, cfg: cfg}).Pull(context.Background(), JiraPullOpts{
		JQL: "project = PROJ", Into: root, Limit: 1,
	})
	if err != nil {
		t.Fatalf("ordinary pull should not require the field catalog: %v", err)
	}
	if tr.fieldCalls != 0 || len(res.Issues) != 1 {
		t.Fatalf("field calls=%d result=%+v", tr.fieldCalls, res)
	}
}

func TestLocalizedEpicWithConfiguredFieldIsGroupedByChildren(t *testing.T) {
	tr := &pagedEpicTracker{children: []domain.Issue{{
		Key: "PROJ-2", Summary: "child", Fields: map[string]any{"customfield_10010": "PROJ-1"},
	}}}
	svc := &JiraService{tr: tr}
	got, _, err := svc.fetchEpicChildrenPage(context.Background(), []domain.Issue{{
		Key: "PROJ-1", Type: "Эпик", Fields: map[string]any{"issuetype": map[string]any{"id": "10000", "name": "Эпик"}},
	}}, "customfield_10010")
	if err != nil {
		t.Fatal(err)
	}
	if sidecar, ok := got["PROJ-1"]; !ok || len(sidecar.Children) != 1 || sidecar.Children[0].Key != "PROJ-2" {
		t.Fatalf("localized epic was not inferred from related children: %+v", got)
	}
}

func TestEpicChildrenSidecarMalformedIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "P-1.epic-children.json")
	if err := os.WriteFile(path, []byte(`{"epic":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadEpicChildrenSidecar(filepath.Dir(path), path); got != nil {
		t.Fatalf("malformed sidecar loaded: %+v", got)
	}
}

func TestEpicChildrenSidecarWithoutResolvedFieldIsIgnored(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "P-1.epic-children.json")
	if err := os.WriteFile(path, []byte(`{"epic":"P-1","children":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadEpicChildrenSidecar(root, path); got != nil {
		t.Fatalf("partial sidecar loaded: %+v", got)
	}
}

func TestFetchEpicChildrenPageTruncatesExplicitly(t *testing.T) {
	children := make([]domain.Issue, jiraEpicChildrenCap+1)
	for i := range children {
		children[i] = domain.Issue{
			Key:    fmt.Sprintf("PROJ-%04d", i+2),
			Fields: map[string]any{"customfield_10010": "PROJ-1"},
		}
	}
	tr := &pagedEpicTracker{children: children}
	svc := &JiraService{tr: tr}
	got, truncated, err := svc.fetchEpicChildrenPage(context.Background(), []domain.Issue{{Key: "PROJ-1", Type: "Epic"}}, "customfield_10010")
	if err != nil {
		t.Fatal(err)
	}
	sidecar := got["PROJ-1"]
	if !truncated || !sidecar.Truncated || sidecar.TruncatedAt != jiraEpicChildrenCap || len(sidecar.Children) != jiraEpicChildrenCap {
		t.Fatalf("truncation wrong: global=%v sidecar=%+v", truncated, sidecar)
	}
	if tr.calls != jiraEpicChildrenCap/100 {
		t.Errorf("search calls = %d, want %d paginated calls (never per child)", tr.calls, jiraEpicChildrenCap/100)
	}
}
