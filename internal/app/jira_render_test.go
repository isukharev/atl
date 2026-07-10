package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/jiramap"
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
		"customfield_10002": float64(7),
		"customfield_10003": "*Risk*\n * first\n * second",
		"customfield_10004": []any{"A", "B"},
		// A GreenHopper-serialized sprint value in an arbitrary custom field.
		"customfield_10020": []any{"com.atlassian.greenhopper.service.sprint.Sprint@1[id=5,state=ACTIVE,name=Sprint 7,startDate=2026-01-01]"},
	}
}

// richIssue is the fixture issue decoded from richFields via the pure adapter
// mapper — the same shape both the live and offline paths produce.
func richIssue() *domain.Issue {
	return jiramap.Issue("1001", "PROJ-42", richFields())
}

func TestRenderIssueProfileMinimal(t *testing.T) {
	got := string(renderIssueMarkdown(richIssue(), nil, jiraRS("minimal")))
	// Only key + summary metadata; body/description present.
	mustContain(t, got, "| Key | PROJ-42 |")
	mustContain(t, got, "| Summary | Fix the thing |")
	mustContain(t, got, "## Metadata")
	mustNotContain(t, got, "---\n")
	mustContain(t, got, "## Description")
	for _, absent := range []string{"| Status |", "| Type |", "| Priority |", "| Parent |", "| Reporter |", "## Links", "## Comments", "## Subtasks"} {
		mustNotContain(t, got, absent)
	}
}

func TestRenderIssueProfileDefaultAddsPriorityParent(t *testing.T) {
	got := string(renderIssueMarkdown(richIssue(), nil, jiraRS("default")))
	mustContain(t, got, "| Status | In Progress |")
	mustContain(t, got, "| Priority | High |")
	mustContain(t, got, "| Parent | PROJ-1 |")
	mustContain(t, got, "## Links")
	mustContain(t, got, "## Comments")
	// Full-only fields stay out of default.
	for _, absent := range []string{"| Reporter |", "| Resolution |", "| Due date |", "| Components |", "| Fix versions |", "## Subtasks", "## Sprint", "## Attachments\n"} {
		mustNotContain(t, got, absent)
	}
}

func TestRenderIssueProfileFull(t *testing.T) {
	got := string(renderIssueMarkdown(richIssue(), nil, jiraRSFull(t)))
	mustContain(t, got, "| Reporter | bob |")
	mustContain(t, got, "| Resolution | Fixed |")
	mustContain(t, got, "| Due date | 2026-02-01 |")
	mustContain(t, got, "| Components | backend, api |")
	mustContain(t, got, "| Fix versions | v2.0 |")
	mustContain(t, got, "| customfield&#95;10001 | custom scalar |")
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

func TestRenderConfiguredFieldViews(t *testing.T) {
	rs, warns := computeSettings("jira", config.RenderService{
		Profile:      "full",
		CustomFields: []string{"customfield_10002"}, // typed view below must own it once
		FieldViews: []config.JiraFieldView{
			{ID: "customfield_10002", Label: "Impact"},
			{ID: "customfield_10003", Label: "Risk Notes", Placement: "section", Format: "jira_wiki"},
			{ID: "customfield_10004", Label: "Objectives", Placement: "section", Format: "list"},
			{ID: "customfield_missing", Label: "Empty Field", Placement: "section", ShowEmpty: true},
		},
	})
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	got := string(renderIssueMarkdown(richIssue(), nil, rs))
	for _, want := range []string{
		"| Impact | 7 |", "## Risk Notes", "**Risk**", "## Objectives", "- A", "- B", "## Empty Field", "_Not set._",
	} {
		mustContain(t, got, want)
	}
	if strings.Count(got, "| Impact | 7 |") != 1 || strings.Contains(got, "| customfield_10002 |") {
		t.Errorf("typed view should own duplicate legacy field once:\n%s", got)
	}
	projection := strings.Join(jiraPullFields(nil, rs), ",")
	for _, id := range []string{"customfield_10002", "customfield_10003", "customfield_10004", "customfield_missing"} {
		if !strings.Contains(projection, id) {
			t.Errorf("projection missing %s: %s", id, projection)
		}
	}
}

func TestConfiguredTemporalAndScalarListFormats(t *testing.T) {
	fields := richFields()
	fields["customfield_date"] = "2026-01-02T23:30:00.000+0300"
	fields["customfield_datetime"] = "2026-01-02T23:30:00.125+0300"
	fields["customfield_bad_date"] = "not-a-date"
	fields["customfield_scalar_list"] = "single"
	is := jiramap.Issue("1001", "PROJ-42", fields)
	rs, warns := computeSettings("jira", config.RenderService{
		Profile: "full",
		FieldViews: []config.JiraFieldView{
			{ID: "customfield_date", Label: "Release date", Format: "date"},
			{ID: "customfield_datetime", Label: "Release at", Format: "datetime"},
			{ID: "customfield_bad_date", Label: "Raw bad date", Format: "date"},
			{ID: "customfield_scalar_list", Label: "Owners", Placement: "section", Format: "list"},
		},
	})
	if len(warns) != 0 {
		t.Fatal(warns)
	}
	got := string(renderIssueMarkdown(is, nil, rs))
	for _, want := range []string{
		`| Release date | 2026-01-02 |`,
		`| Release at | 2026-01-02T23:30:00.125+03:00 |`,
		"| Raw bad date | not-a-date |",
		"## Owners\n\n- single",
	} {
		mustContain(t, got, want)
	}
}

func TestJiraMetadataTableEscapesStructuralValues(t *testing.T) {
	is := richIssue()
	is.Labels = []string{"true", "a,b", `C:\temp`}
	is.Fields["components"] = []any{
		map[string]any{"name": "null"},
		map[string]any{"name": "line\nbreak"},
	}
	got := string(renderIssueMarkdown(is, nil, jiraRSFull(t)))
	mustContain(t, got, `| Labels | true, a,b, C:&#92;temp |`)
	mustContain(t, got, `| Components | null, line break |`)
}

func TestJiraMetadataTableKeepsServerValuesPlainText(t *testing.T) {
	is := richIssue()
	is.Assignee = `![pixel](https://example.invalid/pixel) <img src=x> <!-- note --> *bold*`
	got := string(renderIssueMarkdown(is, nil, jiraRSFull(t)))
	mustNotContain(t, got, "![pixel]")
	mustNotContain(t, got, "<img")
	mustNotContain(t, got, "<!--")
	mustNotContain(t, got, "*bold*")
	mustContain(t, got, `&#33;&#91;pixel&#93;(https://example.invalid/pixel) &lt;img src=x&gt; &lt;&#33;-- note --&gt; &#42;bold&#42;`)
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
	if !strings.Contains(md, "| Reporter | bob |") {
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

func TestJiraRenderSupportsSymlinkedTrustRoot(t *testing.T) {
	physical := t.TempDir()
	m := mirror.New(physical)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(physical, "PROJ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteSnapshot(t, filepath.Join(dir, "PROJ-42.json"), richIssue())
	logical := filepath.Join(t.TempDir(), "mirror-link")
	if err := os.Symlink(physical, logical); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	res, err := NewJiraRenderer(&config.Config{}).Render(logical, config.RenderService{Profile: "minimal"})
	if err != nil || len(res.Rendered) != 1 {
		t.Fatalf("render through trust-root symlink = %+v, err=%v", res, err)
	}
	if _, err := os.Stat(filepath.Join(physical, "PROJ", "PROJ-42.md")); err != nil {
		t.Fatalf("rendered view missing: %v", err)
	}
}

func TestJiraRenderRefusesDescendantSnapshotSymlink(t *testing.T) {
	root := t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "PROJ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	mustWriteSnapshot(t, outside, richIssue())
	if err := os.Symlink(outside, filepath.Join(dir, "PROJ-42.json")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := NewJiraRenderer(&config.Config{}).Render(root, config.RenderService{}); err == nil {
		t.Fatal("render silently accepted a descendant snapshot symlink")
	}
}

func TestAssetsOnDiskRefusesDescendantDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "PROJ")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "1-outside.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "PROJ-42.assets")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if got := assetsOnDisk(root, dir, "PROJ-42"); len(got) != 0 {
		t.Fatalf("assets imported through descendant symlink: %+v", got)
	}
}

func TestAssetsOnDiskIgnoresFinalAssetSymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "PROJ")
	assetsDir := filepath.Join(dir, "PROJ-42.assets")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private.png")
	if err := os.WriteFile(outside, []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(assetsDir, "1-private.png")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if got := assetsOnDisk(root, dir, "PROJ-42"); len(got) != 0 {
		t.Fatalf("final asset symlink was indexed: %+v", got)
	}
}

func TestJiraRenderWarnsWhenEpicSidecarMissing(t *testing.T) {
	root := t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "PROJ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fields := richFields()
	fields["issuetype"] = map[string]any{"name": "Epic"}
	mustWriteSnapshot(t, filepath.Join(dir, "PROJ-42.json"), jiramap.Issue("1001", "PROJ-42", fields))
	res, err := NewJiraRenderer(&config.Config{}).Render(root, config.RenderService{Profile: "full", Include: []string{SecEpicChildren}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "no sidecar") {
		t.Fatalf("warnings = %v, want missing-sidecar warning", res.Warnings)
	}
}

func TestJiraRenderIgnoresMismatchedEpicSidecar(t *testing.T) {
	root := t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "PROJ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fields := richFields()
	fields["issuetype"] = map[string]any{"name": "Epic", "hierarchyLevel": float64(1)}
	mustWriteSnapshot(t, filepath.Join(dir, "PROJ-42.json"), jiramap.Issue("1001", "PROJ-42", fields))
	if err := writeEpicChildrenSidecar(root, filepath.Join(dir, "PROJ-42.epic-children.json"), JiraEpicChildrenSidecar{
		Epic: "OTHER-1", EpicField: "customfield_10010", Children: []JiraEpicChild{{Key: "OTHER-2"}},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := NewJiraRenderer(&config.Config{}).Render(root, config.RenderService{
		Profile: "full", Include: []string{SecEpicChildren}, EpicField: "customfield_10010",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, " "), "mismatched") {
		t.Fatalf("warnings = %v, want mismatched sidecar warning", res.Warnings)
	}
	md := mustReadFile(t, filepath.Join(dir, "PROJ-42.md"))
	if strings.Contains(md, "OTHER-2") || strings.Contains(md, "## Epic Children") {
		t.Fatalf("mismatched sidecar leaked into view:\n%s", md)
	}
}

func TestJiraRenderIgnoresSidecarFromDifferentEpicField(t *testing.T) {
	root := t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "PROJ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fields := richFields()
	fields["issuetype"] = map[string]any{"name": "Epic", "hierarchyLevel": float64(1)}
	mustWriteSnapshot(t, filepath.Join(dir, "PROJ-42.json"), jiramap.Issue("1001", "PROJ-42", fields))
	if err := writeEpicChildrenSidecar(root, filepath.Join(dir, "PROJ-42.epic-children.json"), JiraEpicChildrenSidecar{
		Epic: "PROJ-42", EpicField: "customfield_10010", Children: []JiraEpicChild{{Key: "RELATED-99"}},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := NewJiraRenderer(&config.Config{}).Render(root, config.RenderService{
		Profile: "full", Include: []string{SecEpicChildren}, EpicField: "customfield_10011",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, " "), "mismatched") {
		t.Fatalf("warnings = %v, want mismatched-field warning", res.Warnings)
	}
	if md := mustReadFile(t, filepath.Join(dir, "PROJ-42.md")); strings.Contains(md, "RELATED-99") {
		t.Fatalf("old-field child leaked into view:\n%s", md)
	}
	vs, ok, err := m.ViewStateOf("PROJ-42")
	if err != nil || !ok || vs.EpicField != "customfield_10011" {
		t.Fatalf("view state = %+v ok=%v err=%v", vs, ok, err)
	}
}

func TestJiraRenderRejectsExplicitSidecarTarget(t *testing.T) {
	root := t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "PROJ-1.epic-children.json")
	if err := os.WriteFile(path, []byte(`{"epic":"PROJ-1","epic_field":"customfield_1","children":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewJiraRenderer(&config.Config{}).Render(path, config.RenderService{}); err == nil {
		t.Fatal("explicit epic sidecar target was accepted as an issue snapshot")
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
