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
}

type pagedEpicTracker struct {
	domain.Tracker
	children []domain.Issue
	calls    int
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
	if tr.mainQueries != 1 || tr.relatedQueries != 1 {
		t.Fatalf("queries main=%d related=%d, want one each", tr.mainQueries, tr.relatedQueries)
	}
	if len(res.Issues) != 1 || res.Issues[0].EpicChildren != 2 || res.EpicChildrenTruncated {
		t.Fatalf("pull result = %+v", res)
	}
	dir := filepath.Join(root, "PROJ")
	sidecar := loadEpicChildrenSidecar(filepath.Join(dir, "PROJ-1.epic-children.json"))
	if sidecar == nil || sidecar.EpicField != "customfield_10010" || len(sidecar.Children) != 2 || sidecar.Children[0].Key != "PROJ-2" {
		t.Fatalf("sidecar = %+v", sidecar)
	}
	mdPath := filepath.Join(dir, "PROJ-1.md")
	before := mustReadFile(t, mdPath)
	for _, want := range []string{"## Epic Children", "PROJ-2 — second (Open; alice)", "PROJ-3 — third (Done)"} {
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

func TestEpicChildrenSidecarMalformedIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "P-1.epic-children.json")
	if err := os.WriteFile(path, []byte(`{"epic":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadEpicChildrenSidecar(path); got != nil {
		t.Fatalf("malformed sidecar loaded: %+v", got)
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
