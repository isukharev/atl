package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRepositoryContext7Documentation(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	report, err := validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Documents < 5 || report.Snippets < 20 {
		t.Fatalf("unexpectedly sparse Context7 corpus: %+v", report)
	}
}

func TestValidateRejectsImplicitRootMarkdownAndSnippetlessDocs(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"context7.json": `{"$schema":"https://context7.com/schema/context7.json","url":"https://context7.com/isukharev/atl","public_key":"pk_fixture","folders":["docs"],"excludeFolders":[],"excludeFiles":[],"rules":["rule"]}`,
		"README.md":     "# Project\n\n```sh\nproject --help\n```\n",
		"AGENTS.md":     "# Internal instructions\n",
		"docs/usage.md": "# Usage without examples\n",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := validate(root)
	if err == nil || !strings.Contains(err.Error(), "AGENTS.md") || !strings.Contains(err.Error(), "no non-empty named fenced snippet") {
		t.Fatalf("validation error=%v", err)
	}
}
