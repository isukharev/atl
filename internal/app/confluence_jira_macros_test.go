package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const jiraQueryMacroCSF = `<p>Plan</p><p><ac:structured-macro ac:name="jira"><ac:parameter ac:name="jqlQuery">project = PROJ</ac:parameter><ac:parameter ac:name="columns">key,summary,type,status</ac:parameter><ac:parameter ac:name="maximumIssues">20</ac:parameter></ac:structured-macro></p>`

func TestConfluencePageViewEnrichesJiraMacroWithoutPerIssueReads(t *testing.T) {
	tracker := &recordingTracker{issues: []domain.Issue{{ID: "10001", Key: "PROJ-1", Summary: "First", Type: "Story", Status: "Open", Fields: map[string]any{}}}}
	service := &ConfluenceService{
		store:    &recordingStore{page: &domain.Resource{ID: "42", Title: "Plan", SpaceKey: "DOC", Version: 1, Body: []byte(jiraQueryMacroCSF)}},
		jiraRead: tracker, cfg: &config.Config{},
	}
	result, err := service.ViewPage(context.Background(), "42", ConfluencePageViewOpts{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if tracker.issueKey != "" || tracker.searchJQL != "project = PROJ" || strings.Join(tracker.searchFields, ",") != "summary,issuetype,status" {
		t.Fatalf("tracker issue=%q jql=%q fields=%v", tracker.issueKey, tracker.searchJQL, tracker.searchFields)
	}
	for _, want := range []string{"⟦jira query: project = PROJ⟧", mirror.ConfluenceJiraMacrosMarker, "# Jira Queries", "| Key | Summary | Type | Status |", "| PROJ-1 | First | Story | Open |"} {
		if !strings.Contains(result.Markdown, want) {
			t.Fatalf("view missing %q:\n%s", want, result.Markdown)
		}
	}
}

func TestConfluenceJiraMacroUsesNamedConfluenceProjection(t *testing.T) {
	tracker := &recordingTracker{issues: []domain.Issue{{ID: "10001", Key: "PROJ-1", Fields: map[string]any{"priority": "High"}}}}
	cfg := &config.Config{JiraListViews: map[string]config.JiraListView{
		"planning": {ConfluenceMacro: []string{"key", "priority"}},
	}}
	body := `<ac:structured-macro ac:name="jira"><ac:parameter ac:name="jqlQuery">project = PROJ</ac:parameter></ac:structured-macro>`
	service := &ConfluenceService{store: &recordingStore{page: &domain.Resource{ID: "42", Body: []byte(body)}}, jiraRead: tracker, cfg: cfg}
	result, err := service.ViewPage(context.Background(), "42", ConfluencePageViewOpts{Root: t.TempDir(), JiraView: "planning"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tracker.searchFields, ",") != "priority" {
		t.Fatalf("search fields=%v", tracker.searchFields)
	}
	if !strings.Contains(result.Markdown, "| Key | Priority |") || !strings.Contains(result.Markdown, "| PROJ-1 | High |") {
		t.Fatalf("named macro view:\n%s", result.Markdown)
	}
}

func TestConfluenceJiraMacroUnknownViewFailsBeforePageRead(t *testing.T) {
	store := &recordingStore{page: &domain.Resource{ID: "42", Body: []byte(jiraQueryMacroCSF)}}
	service := &ConfluenceService{store: store, cfg: &config.Config{}}
	_, err := service.ViewPage(context.Background(), "42", ConfluencePageViewOpts{JiraView: "missing"})
	if !errors.Is(err, domain.ErrUsage) || store.getID != "" {
		t.Fatalf("error=%v get_id=%q", err, store.getID)
	}
}

func TestConfluencePullUnknownJiraViewWritesNothing(t *testing.T) {
	store := &recordingStore{page: &domain.Resource{ID: "42", Body: []byte(jiraQueryMacroCSF)}}
	service := &ConfluenceService{store: store, cfg: &config.Config{}}
	root := filepath.Join(t.TempDir(), "mirror")
	_, err := service.Pull(context.Background(), PullOpts{ID: "42", Into: root, JiraView: "missing"})
	if !errors.Is(err, domain.ErrUsage) || store.getID != "" {
		t.Fatalf("error=%v get_id=%q", err, store.getID)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("mirror root was created: %v", statErr)
	}
}

func TestConfluenceJiraMacroPullSidecarKeepsOfflineRenderAndApplyStable(t *testing.T) {
	tracker := &recordingTracker{issues: []domain.Issue{{ID: "10001", Key: "PROJ-1", Summary: "First", Status: "Open", Fields: map[string]any{}}}}
	page := &domain.Resource{ID: "42", Title: "Plan", SpaceKey: "DOC", Version: 1, Body: []byte(jiraQueryMacroCSF)}
	root := t.TempDir()
	service := &ConfluenceService{store: &recordingStore{page: page}, jiraRead: tracker, cfg: &config.Config{}}
	result, err := service.Pull(context.Background(), PullOpts{ID: "42", Into: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pages) != 1 {
		t.Fatalf("pull=%+v", result)
	}
	csfPath := filepath.Join(root, filepath.FromSlash(result.Pages[0].Path))
	base := strings.TrimSuffix(csfPath, ".csf")
	mdBefore, err := os.ReadFile(base + ".md")
	if err != nil || !strings.Contains(string(mdBefore), mirror.ConfluenceJiraMacrosMarker) {
		t.Fatalf("pulled md=%q err=%v", mdBefore, err)
	}
	if _, err := os.Stat(base + ".jira-macros.json"); err != nil {
		t.Fatalf("sidecar: %v", err)
	}
	if _, err := NewConfluenceRenderer(&config.Config{}).Render(root, config.RenderService{}); err != nil {
		t.Fatal(err)
	}
	mdAfter, _ := os.ReadFile(base + ".md")
	if string(mdAfter) != string(mdBefore) {
		t.Fatalf("offline render drifted:\nbefore=%s\nafter=%s", mdBefore, mdAfter)
	}
	if _, err := Apply(base+".md", ApplyOpts{Into: root, DryRun: true}); err != nil {
		t.Fatalf("untouched apply: %v", err)
	}
}

func TestConfluenceJiraMacroTamperedSidecarFailsApplyClosed(t *testing.T) {
	tracker := &recordingTracker{issues: []domain.Issue{{ID: "10001", Key: "PROJ-1", Summary: "First", Status: "Open", Fields: map[string]any{}}}}
	page := &domain.Resource{ID: "42", Title: "Plan", SpaceKey: "DOC", Version: 1, Body: []byte(jiraQueryMacroCSF)}
	root := t.TempDir()
	result, err := (&ConfluenceService{store: &recordingStore{page: page}, jiraRead: tracker, cfg: &config.Config{}}).Pull(context.Background(), PullOpts{ID: "42", Into: root})
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSuffix(filepath.Join(root, filepath.FromSlash(result.Pages[0].Path)), ".csf")
	sidecarPath := base + ".jira-macros.json"
	sidecar, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(sidecar), `"page_id": "42"`, `"page_id": "other"`, 1)
	if err := os.WriteFile(sidecarPath, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	csfBefore, _ := os.ReadFile(base + ".csf")
	if _, err := Apply(base+".md", ApplyOpts{Into: root, DryRun: true}); !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "conf pull") {
		t.Fatalf("tampered sidecar apply error=%v", err)
	}
	csfAfter, _ := os.ReadFile(base + ".csf")
	if string(csfAfter) != string(csfBefore) {
		t.Fatal("failed apply changed CSF")
	}
	rendered, err := NewConfluenceRenderer(&config.Config{}).Render(root, config.RenderService{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered.Warnings) != 1 {
		t.Fatalf("render warnings=%v", rendered.Warnings)
	}
	md, _ := os.ReadFile(base + ".md")
	if strings.Contains(string(md), mirror.ConfluenceJiraMacrosMarker) {
		t.Fatalf("render retained untrusted enrichment:\n%s", md)
	}
}

func TestConfluenceJiraMacroWithoutJiraKeepsPlaceholderAndWarns(t *testing.T) {
	service := &ConfluenceService{
		store: &recordingStore{page: &domain.Resource{ID: "42", Body: []byte(jiraQueryMacroCSF)}},
		cfg:   &config.Config{}, jiraReadReason: "Jira credentials are not configured",
	}
	result, err := service.ViewPage(context.Background(), "42", ConfluencePageViewOpts{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Markdown, "jira query: project = PROJ") || strings.Contains(result.Markdown, mirror.ConfluenceJiraMacrosMarker) || len(result.Warnings) != 1 {
		t.Fatalf("markdown=%s warnings=%v", result.Markdown, result.Warnings)
	}
}
