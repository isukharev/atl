package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestRepositoryContext7Documentation(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	report, err := validateRepository(root)
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
		"context7.json": `{"$schema":"https://context7.com/schema/context7.json","url":"https://context7.com/isukharev/atl","public_key":"pk_fixture","branch":"stable","previousVersions":[{"tag":"v0.1.0"}],"folders":["docs"],"excludeFolders":[],"excludeFiles":[],"rules":["rule"]}`,
		"VERSION":       "0.1.0\n",
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

func TestYAMLChildBlockIsolatesSiblingJobs(t *testing.T) {
	workflow := `jobs:
  unrelated:
    continue-on-error: true
    environment: context7
    env:
      KEY: ${{ secrets.CONTEXT7_API_KEY }}
  refresh-context7:
    runs-on: ubuntu-latest
    steps:
      - run: curl https://context7.com/api/v1/refresh
`
	block, err := yamlChildBlock(workflow, "jobs", "refresh-context7")
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"continue-on-error", "environment: context7", "secrets.CONTEXT7_API_KEY"} {
		if strings.Contains(block, leaked) {
			t.Fatalf("target block inherited sibling fragment %q:\n%s", leaked, block)
		}
	}
}

func TestValidateAutomationRejectsControlsOnlyInSiblingJobs(t *testing.T) {
	releaseSibling := `  unrelated:
    continue-on-error: true
    environment: context7
    env:
      KEY: ${{ secrets.CONTEXT7_API_KEY }}
    steps:
      - run: git push origin refs/heads/stable
      - run: curl https://context7.com/api/v1/refresh
`
	releaseTarget := `  refresh-context7:
    continue-on-error: true
    environment: context7
    env:
      KEY: ${{ secrets.CONTEXT7_API_KEY }}
    steps:
      - run: git push origin refs/heads/stable
      - run: curl https://context7.com/api/v1/refresh
`
	manualSibling := `  unrelated:
    environment: context7
    env:
      KEY: ${{ secrets.CONTEXT7_API_KEY }}
    steps:
      - run: curl https://context7.com/api/v1/refresh
`
	manualTarget := `  refresh:
    environment: context7
    env:
      KEY: ${{ secrets.CONTEXT7_API_KEY }}
    steps:
      - run: curl https://context7.com/api/v1/refresh
`
	writeWorkflows := func(t *testing.T, releaseTarget, manualTarget string) string {
		t.Helper()
		root := t.TempDir()
		workflowDir := filepath.Join(root, ".github", "workflows")
		if err := os.MkdirAll(workflowDir, 0o755); err != nil {
			t.Fatal(err)
		}
		release := "jobs:\n" + releaseSibling + releaseTarget
		manual := "on:\n  workflow_dispatch:\njobs:\n" + manualSibling + manualTarget
		for name, content := range map[string]string{"release.yml": release, "context7-refresh.yml": manual} {
			if err := os.WriteFile(filepath.Join(workflowDir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}

	for _, fragment := range []string{
		"continue-on-error: true", "environment: context7", "secrets.CONTEXT7_API_KEY",
		"refs/heads/stable", "https://context7.com/api/v1/refresh",
	} {
		t.Run("release_"+fragment, func(t *testing.T) {
			target := strings.ReplaceAll(releaseTarget, fragment, "removed")
			err := validateAutomation(writeWorkflows(t, target, manualTarget))
			if err == nil || !strings.Contains(err.Error(), "job refresh-context7 must contain "+strconv.Quote(fragment)) {
				t.Fatalf("validation error=%v", err)
			}
		})
	}
	for _, fragment := range []string{
		"environment: context7", "secrets.CONTEXT7_API_KEY", "https://context7.com/api/v1/refresh",
	} {
		t.Run("manual_"+fragment, func(t *testing.T) {
			target := strings.ReplaceAll(manualTarget, fragment, "removed")
			err := validateAutomation(writeWorkflows(t, releaseTarget, target))
			if err == nil || !strings.Contains(err.Error(), "job refresh must contain "+strconv.Quote(fragment)) {
				t.Fatalf("validation error=%v", err)
			}
		})
	}
	if err := validateAutomation(writeWorkflows(t, releaseTarget, manualTarget)); err != nil {
		t.Fatalf("valid job-specific controls: %v", err)
	}
}

func TestValidateAutomationRejectsMissingManualTrigger(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	release := `jobs:
  unrelated:
    continue-on-error: true
    environment: context7
    env:
      KEY: ${{ secrets.CONTEXT7_API_KEY }}
  refresh-context7:
    continue-on-error: true
    environment: context7
    env:
      KEY: ${{ secrets.CONTEXT7_API_KEY }}
    steps:
      - run: git push origin refs/heads/stable
      - run: curl https://context7.com/api/v1/refresh
`
	manual := `on:
  push:
jobs:
  refresh:
    environment: context7
    env:
      KEY: ${{ secrets.CONTEXT7_API_KEY }}
    steps:
      - run: curl https://context7.com/api/v1/refresh
`
	for name, content := range map[string]string{"release.yml": release, "context7-refresh.yml": manual} {
		if err := os.WriteFile(filepath.Join(workflowDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := validateAutomation(root)
	if err == nil || !strings.Contains(err.Error(), `missing child "workflow_dispatch"`) {
		t.Fatalf("validation error=%v", err)
	}
}
