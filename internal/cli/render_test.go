package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

// seedJiraMirror writes a minimal Jira mirror (a .wiki substrate + .json snapshot
// carrying description/comment/sprint fields) so the offline `jira render` command
// has something to re-render. It returns the mirror root.
func seedJiraMirror(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "PROJ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{
		"summary":     "Fix the thing",
		"description": "h1. Heading",
		"status":      map[string]any{"name": "Open"},
		"issuetype":   map[string]any{"name": "Bug"},
		"project":     map[string]any{"key": "PROJ"},
		"comment": map[string]any{"comments": []any{
			map[string]any{"id": "c1", "author": map[string]any{"displayName": "carol"}, "created": "2026-01-02", "body": "a comment"},
		}},
		"customfield_10020": []any{"com.atlassian.greenhopper.service.sprint.Sprint@1[id=5,state=ACTIVE,name=Sprint 7]"},
	}
	snap := map[string]any{"key": "PROJ-42", "id": "1001", "fields": fields}
	b, _ := json.MarshalIndent(snap, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "PROJ-42.json"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "PROJ-42.wiki"), []byte("h1. Heading"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestJiraRenderGolden locks the JSON shape of `jira render`. A relative --into is
// used so the emitted paths are host-independent.
func TestJiraRenderGolden(t *testing.T) {
	root := seedJiraMirror(t)
	out, stderr, code := runCLIFull(t, nil, "jira", "render", root)
	if code != exitOK {
		t.Fatalf("jira render: exit %d (stdout=%q stderr=%q)", code, out, stderr)
	}
	// Normalize the absolute mirror root (a t.TempDir) so the golden is stable.
	got := strings.ReplaceAll(out, root, "<ROOT>")
	assertGolden(t, "jira_render.json", []byte(got))
}

// TestJiraRenderFlagPlumbing verifies --render-exclude/--render-include reach the
// section set: exclude drops the Comments section on full; include adds Sprint on
// default.
func TestJiraRenderFlagPlumbing(t *testing.T) {
	mdPath := func(root string) string { return filepath.Join(root, "PROJ", "PROJ-42.md") }

	t.Run("exclude comments on full", func(t *testing.T) {
		root := seedJiraMirror(t)
		_, stderr, code := runCLIFull(t, nil, "jira", "render", root, "--render-profile", "full", "--render-exclude", "comments")
		if code != exitOK {
			t.Fatalf("render: exit %d stderr=%q", code, stderr)
		}
		md, err := os.ReadFile(mdPath(root))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(md), "## Comments") {
			t.Errorf("--render-exclude comments should drop the section:\n%s", md)
		}
		if !strings.Contains(string(md), "## Sprint") {
			t.Errorf("full should still render Sprint:\n%s", md)
		}
	})

	t.Run("include sprint on default", func(t *testing.T) {
		root := seedJiraMirror(t)
		_, stderr, code := runCLIFull(t, nil, "jira", "render", root, "--render-include", "sprint")
		if code != exitOK {
			t.Fatalf("render: exit %d stderr=%q", code, stderr)
		}
		md, err := os.ReadFile(mdPath(root))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(md), "## Sprint") {
			t.Errorf("--render-include sprint should add the section on default:\n%s", md)
		}
	})

	t.Run("unknown section warns on stderr", func(t *testing.T) {
		root := seedJiraMirror(t)
		_, stderr, code := runCLIFull(t, nil, "jira", "render", root, "--render-include", "bogus")
		if code != exitOK {
			t.Fatalf("render: exit %d", code)
		}
		if !strings.Contains(stderr, "unknown include section") {
			t.Errorf("expected an unknown-section warning on stderr, got %q", stderr)
		}
	})
}

// seedConfMirrorCLI writes one Confluence page mirror for the offline conf render.
func seedConfMirrorCLI(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	page := &domain.Resource{
		ID: "1001", Title: "My Page", SpaceKey: "DOCS", Version: 3,
		Body: []byte("<p>Body text.</p>"),
	}
	dir, slug, err := m.ClaimPageDir(page.SpaceKey, page.Ancestors, page.Title, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestConfRenderGolden locks the JSON shape of `conf render`.
func TestConfRenderGolden(t *testing.T) {
	root := seedConfMirrorCLI(t)
	out, stderr, code := runCLIFull(t, nil, "conf", "render", root)
	if code != exitOK {
		t.Fatalf("conf render: exit %d (stdout=%q stderr=%q)", code, out, stderr)
	}
	got := strings.ReplaceAll(out, root, "<ROOT>")
	assertGolden(t, "conf_render.json", []byte(got))
}
