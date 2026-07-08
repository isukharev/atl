package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

// richFields is a complete raw Jira fields map carrying every renderable field,
// so it doubles as a realistic `<KEY>.json` snapshot: decoding it through the
// pure mapper reproduces the same issue the live search path would.
func richFields() map[string]any {
	return map[string]any{
		"summary":     "Fix the thing",
		"description": "Native *wiki* body.",
		"status":      map[string]any{"name": "In Progress"},
		"issuetype":   map[string]any{"name": "Bug"},
		"project":     map[string]any{"key": "PROJ"},
		"assignee":    map[string]any{"displayName": "alice"},
		"reporter":    map[string]any{"displayName": "bob"},
		"labels":      []any{"backend", "urgent"},
		"priority":    map[string]any{"name": "High"},
		"parent":      map[string]any{"key": "PROJ-1"},
		"resolution":  map[string]any{"name": "Fixed"},
		"duedate":     "2026-02-01",
		"created":     "2026-01-01T10:00:00.000+0000",
		"updated":     "2026-01-05T12:00:00.000+0000",
		"components":  []any{map[string]any{"name": "backend"}, map[string]any{"name": "api"}},
		"fixVersions": []any{map[string]any{"name": "v2.0"}},
		"subtasks": []any{
			map[string]any{"key": "PROJ-43", "fields": map[string]any{"summary": "child task"}},
		},
		"attachment": []any{
			map[string]any{"id": "1", "filename": "spec.pdf", "mimeType": "application/pdf", "size": float64(2048), "content": "http://x/1"},
		},
		"issuelinks": []any{
			map[string]any{"id": "l1", "type": map[string]any{"name": "Blocks", "outward": "blocks"}, "outwardIssue": map[string]any{"key": "PROJ-7"}},
		},
		"comment": map[string]any{"comments": []any{
			map[string]any{"id": "c1", "author": map[string]any{"displayName": "carol"}, "created": "2026-01-02", "body": "a comment"},
		}},
		"customfield_10001": "custom scalar",
		// A GreenHopper-serialized sprint value in an arbitrary custom field.
		"customfield_10020": []any{"com.atlassian.greenhopper.service.sprint.Sprint@1[id=5,state=ACTIVE,name=Sprint 7,startDate=2026-01-01]"},
	}
}

// richIssue is the fixture issue decoded from richFields via the pure adapter
// mapper — the same shape both the live and offline paths produce.
func richIssue() *domain.Issue {
	return jiraadapter.MapIssueFields("1001", "PROJ-42", richFields())
}

func TestRenderIssueProfileMinimal(t *testing.T) {
	got := string(renderIssueMarkdown(richIssue(), nil, jiraRS("minimal")))
	// Only key + summary in frontmatter; body/description present.
	mustContain(t, got, "key: PROJ-42")
	mustContain(t, got, "summary: Fix the thing")
	mustContain(t, got, "## Description")
	for _, absent := range []string{"status:", "type:", "priority:", "parent:", "reporter:", "## Links", "## Comments", "## Subtasks"} {
		mustNotContain(t, got, absent)
	}
}

func TestRenderIssueProfileDefaultAddsPriorityParent(t *testing.T) {
	got := string(renderIssueMarkdown(richIssue(), nil, jiraRS("default")))
	mustContain(t, got, "status: In Progress")
	mustContain(t, got, "priority: High")
	mustContain(t, got, "parent: PROJ-1")
	mustContain(t, got, "## Links")
	mustContain(t, got, "## Comments")
	// Full-only fields stay out of default.
	for _, absent := range []string{"reporter:", "resolution:", "duedate:", "components:", "fix_versions:", "## Subtasks", "## Sprint", "## Attachments\n"} {
		mustNotContain(t, got, absent)
	}
}

func TestRenderIssueProfileFull(t *testing.T) {
	got := string(renderIssueMarkdown(richIssue(), nil, jiraRSFull(t)))
	mustContain(t, got, "reporter: bob")
	mustContain(t, got, "resolution: Fixed")
	mustContain(t, got, "duedate: 2026-02-01")
	mustContain(t, got, "components: [backend, api]")
	mustContain(t, got, "fix_versions: [v2.0]")
	mustContain(t, got, "customfield_10001: custom scalar")
	mustContain(t, got, "## Subtasks")
	mustContain(t, got, "- PROJ-43 — child task")
	mustContain(t, got, "## Attachments")
	mustContain(t, got, "- spec.pdf (2.0 KB, application/pdf)")
	mustContain(t, got, "## Sprint")
	mustContain(t, got, "- Sprint 7")
	mustContain(t, got, "## Comments")
}

// jiraRSFull resolves the full profile with the fixture's custom field
// configured, the way a real pull would from config.
func jiraRSFull(t *testing.T) RenderSettings {
	t.Helper()
	rs, warns := computeSettings("jira", config.RenderService{
		Profile:      "full",
		CustomFields: []string{"customfield_10001"},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	return rs
}

func TestPullFieldsWidenForFull(t *testing.T) {
	rs := jiraRSFull(t)
	got := jiraPullFields(nil, rs)
	joined := strings.Join(got, ",")
	for _, want := range []string{"priority", "parent", "reporter", "created", "updated", "resolution", "duedate", "components", "fixVersions", "subtasks", "customfield_10001"} {
		if !strings.Contains(joined, want) {
			t.Errorf("full pull fields missing %q: %v", want, got)
		}
	}
	// Default must NOT widen with full-only fields.
	def := strings.Join(jiraPullFields(nil, jiraRS("default")), ",")
	for _, absent := range []string{"resolution", "duedate", "fixVersions", "subtasks"} {
		if strings.Contains(def, absent) {
			t.Errorf("default pull fields should not include %q: %s", absent, def)
		}
	}
}

// TestJiraRenderOfflineStable pulls a fake mirror, flips the profile via a local
// config file, re-renders offline, and asserts only the .md changed while the
// .wiki/.json substrate (and thus `jira status`) stays clean.
func TestJiraRenderOfflineStable(t *testing.T) {
	into := t.TempDir()
	// Seed a mirror by hand: a .wiki substrate + a .json snapshot + sidecar via the
	// mirror engine, mimicking what pull writes.
	svc := NewJiraRenderer(&config.Config{})
	dir := filepath.Join(into, "PROJ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Record a sync state so status has a baseline.
	m := mirror.New(into)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	body := "Native *wiki* body."
	mustWriteFile(t, filepath.Join(dir, "PROJ-42.wiki"), body)
	mustWriteSnapshot(t, filepath.Join(dir, "PROJ-42.json"), richIssue())
	if err := m.SaveBaseExt("PROJ-42", []byte(body), ".wiki"); err != nil {
		t.Fatal(err)
	}
	batch, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	batch.Record(mirror.SyncState{ID: "PROJ-42", Version: 0, Hash: mirror.Hash([]byte(body)), Path: "PROJ/PROJ-42.wiki"})
	if err := batch.Flush(); err != nil {
		t.Fatal(err)
	}

	wikiBefore := mustReadFile(t, filepath.Join(dir, "PROJ-42.wiki"))
	jsonBefore := mustReadFile(t, filepath.Join(dir, "PROJ-42.json"))

	// Render with a full override: only the .md is produced/rewritten.
	res, err := svc.Render(into, config.RenderService{Profile: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rendered) != 1 || res.Rendered[0].Key != "PROJ-42" {
		t.Fatalf("render result unexpected: %+v", res)
	}
	md := mustReadFile(t, filepath.Join(dir, "PROJ-42.md"))
	if !strings.Contains(md, "reporter: bob") {
		t.Errorf("full render missing reporter: %s", md)
	}
	// Substrate untouched.
	if mustReadFile(t, filepath.Join(dir, "PROJ-42.wiki")) != wikiBefore {
		t.Error(".wiki was modified by render")
	}
	if mustReadFile(t, filepath.Join(dir, "PROJ-42.json")) != jsonBefore {
		t.Error(".json was modified by render")
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// mustWriteSnapshot writes a <KEY>.json snapshot the way pull does.
func mustWriteSnapshot(t *testing.T, path string, is *domain.Issue) {
	t.Helper()
	snap := JiraIssueSnapshot{Key: is.Key, ID: is.ID, Fields: is.Fields}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, path, string(b)+"\n")
}

// A numeric custom field (story points, scores — encoding/json gives float64)
// must render verbatim, integers without a fractional tail.
func TestRenderFieldValueNumeric(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{float64(13), "13"},
		{float64(0.5), "0.5"},
		{json.Number("42"), "42"},
		{[]any{float64(1), float64(2)}, "1, 2"},
	}
	for _, c := range cases {
		if got := renderFieldValue(c.in); got != c.want {
			t.Errorf("renderFieldValue(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	fields := map[string]any{"customfield_10001": float64(13)}
	v, ok := customFieldValue(fields, "customfield_10001")
	if !ok || v != "13" {
		t.Errorf("customFieldValue numeric = (%q, %v), want (\"13\", true)", v, ok)
	}
}
