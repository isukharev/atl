package app

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/csf"
)

// Integration tests hit a live Confluence/Jira Data Center instance. They are
// gated on ATL_INTEGRATION=1 plus a PAT, a URL, and a disposable page ID, and
// are read-only / dry-run so they are safe to re-run. Point them at a scratch
// page you own. Run with:
//
//	ATL_INTEGRATION=1 \
//	  CONFLUENCE_URL=https://confluence.example.com \
//	  TEST_CONFLUENCE_PAT=… \
//	  ATL_TEST_PAGE_ID=<id of a throwaway page> \
//	  go test ./internal/app/ -run Integration -v
func testPageID(t *testing.T) string {
	t.Helper()
	id := os.Getenv("ATL_TEST_PAGE_ID")
	if id == "" {
		t.Skip("set ATL_TEST_PAGE_ID to a disposable page ID to run this test")
	}
	return id
}

func skipUnlessIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("ATL_INTEGRATION") == "" {
		t.Skip("set ATL_INTEGRATION=1 to run live integration tests")
	}
	if os.Getenv("TEST_CONFLUENCE_PAT") == "" && os.Getenv("ATL_CONFLUENCE_PAT") == "" {
		t.Skip("no Confluence PAT in env")
	}
}

func TestIntegrationConfluencePullValidateDryRun(t *testing.T) {
	skipUnlessIntegration(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfluenceURL == "" {
		t.Skip("CONFLUENCE_URL not set")
	}
	svc, err := NewConfluence(cfg, "integration-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dir := t.TempDir()

	// Pull the test page into a temp mirror.
	res, err := svc.Pull(ctx, PullOpts{ID: testPageID(t), Into: dir})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(res.Pages) != 1 {
		t.Fatalf("expected 1 page, got %d", len(res.Pages))
	}
	p := res.Pages[0]
	if p.Version < 1 {
		t.Errorf("unexpected version %d", p.Version)
	}

	// The pulled .csf must be well-formed native storage.
	csfPath := dir + "/" + strings.TrimSuffix(p.Path, ".csf") + ".csf"
	body, err := os.ReadFile(csfPath)
	if err != nil {
		t.Fatalf("read mirrored csf: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("empty csf body")
	}
	if csf.HasErrors(csf.Validate(body)) {
		t.Errorf("live page CSF failed validation")
	}

	// A clean page is not dirty.
	st, err := svc.Status(ctx, dir, false)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, e := range st {
		if e.LocallyEdited {
			t.Errorf("freshly pulled page reported as edited: %s", e.Path)
		}
	}

	// Dry-run push must not mutate anything and must report no validation errors.
	pr, err := svc.Push(ctx, csfPath, PushOpts{DryRun: true, Into: dir})
	if err != nil {
		t.Fatalf("dry-run push: %v", err)
	}
	if len(pr.Items) != 1 || pr.Items[0].Pushed {
		t.Errorf("dry-run should not push: %+v", pr.Items)
	}
}

// TestIntegrationConfluenceHistory guards the DC version-list endpoint
// (/rest/experimental/content/{id}/version): on Confluence Data Center the
// Cloud-style /rest/api/content/{id}/version path 404s. Read-only.
func TestIntegrationConfluenceHistory(t *testing.T) {
	skipUnlessIntegration(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfluenceURL == "" {
		t.Skip("CONFLUENCE_URL not set")
	}
	svc, err := NewConfluence(cfg, "integration-test")
	if err != nil {
		t.Fatal(err)
	}
	vs, err := svc.History(context.Background(), testPageID(t))
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(vs) == 0 {
		t.Fatal("expected at least one version record")
	}
	if vs[0].Number < 1 {
		t.Errorf("unexpected newest version number %d", vs[0].Number)
	}
}

// skipUnlessJiraIntegration is the Jira counterpart of skipUnlessIntegration.
func skipUnlessJiraIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("ATL_INTEGRATION") == "" {
		t.Skip("set ATL_INTEGRATION=1 to run live integration tests")
	}
	if os.Getenv("TEST_JIRA_PAT") == "" && os.Getenv("ATL_JIRA_PAT") == "" {
		t.Skip("no Jira PAT in env")
	}
}

// TestIntegrationJiraFieldOptions guards the two-step DC createmeta endpoints
// (/createmeta/{projectKey}/issuetypes[/{id}]): Jira DC 9.x removed the older
// expand-based /createmeta query. Point ATL_TEST_JIRA_FIELD at a field that has
// allowed values (e.g. priority). Read-only.
func TestIntegrationJiraFieldOptions(t *testing.T) {
	skipUnlessJiraIntegration(t)
	project := os.Getenv("ATL_TEST_JIRA_PROJECT")
	field := os.Getenv("ATL_TEST_JIRA_FIELD")
	if project == "" || field == "" {
		t.Skip("set ATL_TEST_JIRA_PROJECT and ATL_TEST_JIRA_FIELD to run this test")
	}
	issueType := os.Getenv("ATL_TEST_JIRA_ISSUETYPE") // optional; empty scans all types
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JiraURL == "" {
		t.Skip("JIRA_URL not set")
	}
	svc, err := NewJira(cfg, "integration-test")
	if err != nil {
		t.Fatal(err)
	}
	opts, err := svc.FieldOptions(context.Background(), project, issueType, field)
	if err != nil {
		t.Fatalf("field-options: %v", err)
	}
	if len(opts) == 0 {
		t.Errorf("no allowed values for field %q in %s/%s — point ATL_TEST_JIRA_FIELD at a field with options (e.g. priority)", field, project, issueType)
	}
}
