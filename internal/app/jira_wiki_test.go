package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// wikiPath returns the on-disk .wiki substrate path for a pulled issue.
func wikiPath(into, project, key string) string {
	return filepath.Join(into, project, key+".wiki")
}

// The .wiki substrate must be byte-identical to the issue body from the search
// response — no trailing-newline normalization, no CRLF rewriting, and an empty
// body still produces an (empty) file so the substrate always exists.
func TestJiraPullWritesVerbatimWiki(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"simple", "h1. Title\n\nBody with *wiki* markup."},
		{"crlf", "line one\r\nline two\r\n"},
		{"trailing newline", "ends with newline\n"},
		{"no trailing newline", "no trailing newline"},
		{"empty", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			into := t.TempDir()
			iss := domain.Issue{Key: "PROJ-1", Project: "PROJ", Summary: "S", Status: "Open", Type: "Task", Body: c.body}
			tr := &assetPullTracker{t: t, issues: []domain.Issue{iss}}
			svc := &JiraService{tr: tr}
			if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: into, Limit: 1}); err != nil {
				t.Fatalf("pull: %v", err)
			}
			got, err := os.ReadFile(wikiPath(into, "PROJ", "PROJ-1"))
			if err != nil {
				t.Fatalf("read .wiki: %v", err)
			}
			if string(got) != c.body {
				t.Errorf(".wiki not byte-identical:\n got: %q\nwant: %q", got, c.body)
			}
		})
	}
}

// The .md is a pure rendered read view: wiki headings, code fences, and links
// are converted, and the raw wiki markup no longer appears there. The verbatim
// body lives only in the .wiki file.
func TestJiraPullMarkdownIsRenderedView(t *testing.T) {
	into := t.TempDir()
	iss := domain.Issue{
		Key: "PROJ-2", Project: "PROJ", Summary: "S", Status: "Open", Type: "Task",
		Body: "h2. Foo\n\n{code:go}\nfmt.Println(x)\n{code}\n\nsee [docs|https://x/y].",
	}
	tr := &assetPullTracker{t: t, issues: []domain.Issue{iss}}
	svc := &JiraService{tr: tr}
	if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: into, Limit: 1}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	md := readMD(t, into, "PROJ", "PROJ-2")
	mustContain(t, md, "# Description")
	mustContain(t, md, "### Foo")              // h2. nests below generated Description
	mustContain(t, md, "```go")                // fenced code with language
	mustContain(t, md, "fmt.Println(x)")       // code body preserved
	mustContain(t, md, "[docs](https://x/y).") // link converted
	mustNotContain(t, md, "h2. Foo")           // raw wiki heading gone
	mustNotContain(t, md, "{code:go}")         // raw wiki macro gone
	mustNotContain(t, md, "# Description (Jira wiki)")
}

// With --assets, an inline `!screenshot.png!` embed resolves to the downloaded
// local asset path; without --assets the same embed renders as unresolved-image
// inline code.
func TestJiraPullInlineImageResolution(t *testing.T) {
	t.Run("resolved with --assets", func(t *testing.T) {
		into := t.TempDir()
		iss := issueWithAttachments("PROJ-3", "PROJ", att("55", "screenshot.png", "image/png", "/c/55"))
		iss.Body = "before !screenshot.png! after"
		tr := &assetPullTracker{t: t, issues: []domain.Issue{iss}, blobs: map[string][]byte{"/c/55": []byte("PNG")}}
		svc := &JiraService{tr: tr}
		if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: into, Limit: 1, Assets: true}); err != nil {
			t.Fatalf("pull: %v", err)
		}
		md := readMD(t, into, "PROJ", "PROJ-3")
		mustContain(t, md, "![screenshot.png](PROJ-3.assets/55-screenshot.png)")
	})

	t.Run("unresolved without --assets", func(t *testing.T) {
		into := t.TempDir()
		iss := issueWithAttachments("PROJ-4", "PROJ", att("55", "screenshot.png", "image/png", "/c/55"))
		iss.Body = "before !screenshot.png! after"
		tr := &assetPullTracker{t: t, issues: []domain.Issue{iss}, blobs: map[string][]byte{"/c/55": []byte("PNG")}}
		svc := &JiraService{tr: tr}
		if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: into, Limit: 1}); err != nil {
			t.Fatalf("pull: %v", err)
		}
		md := readMD(t, into, "PROJ", "PROJ-4")
		mustContain(t, md, "`!screenshot.png!`")
		mustNotContain(t, md, ".assets/")
	})
}

// guardRender must never let a renderer panic escape: it returns the fallback
// stub, and the stub must point at the .wiki source of truth without embedding
// any body. On the happy path it returns the render output unchanged.
func TestGuardRender(t *testing.T) {
	if got := guardRender(jiraDescStub, func() string { panic("boom") }); got != jiraDescStub {
		t.Errorf("panic path = %q, want stub %q", got, jiraDescStub)
	}
	if got := guardRender(jiraDescStub, func() string { return "rendered" }); got != "rendered" {
		t.Errorf("happy path = %q, want %q", got, "rendered")
	}
}

// The description stub contract: it references the .wiki substrate (source of
// truth) and never embeds the wiki body itself.
func TestJiraDescStubContract(t *testing.T) {
	if !strings.Contains(jiraDescStub, ".wiki") {
		t.Errorf("description stub %q must point at the .wiki source of truth", jiraDescStub)
	}
}
