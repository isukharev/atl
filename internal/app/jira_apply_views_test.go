package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

// scaffoldApplyIssueFull seeds a single-issue mirror whose .md view was written
// under the FULL profile and whose view state is recorded in the sidecar — the
// shape `jira pull --render-profile full` produces. The ambient config passed to
// the service is empty (i.e. the DEFAULT profile), so an apply that honored the
// ambient config instead of the recorded view would reconstruct a different
// pristine view and spuriously refuse (#166).
func scaffoldApplyIssueFull(t *testing.T, body string) (svc *JiraService, root, mdPath, wikiPath string) {
	t.Helper()
	root = t.TempDir()
	dir := filepath.Join(root, "PROJ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	fields := richFields()
	fields["description"] = body
	is := jiraadapter.MapIssueFields("1001", "PROJ-42", fields)
	is.Body = body

	rsFull := jiraRSFull(t)
	wikiPath = filepath.Join(dir, "PROJ-42.wiki")
	mdPath = filepath.Join(dir, "PROJ-42.md")
	mustWriteFile(t, wikiPath, body)
	mustWriteSnapshot(t, filepath.Join(dir, "PROJ-42.json"), is)
	mustWriteFile(t, mdPath, string(renderIssueMarkdown(is, nil, rsFull)))
	if err := m.SaveBaseExt("PROJ-42", []byte(body), ".wiki"); err != nil {
		t.Fatal(err)
	}
	batch, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	batch.Record(mirror.SyncState{ID: "PROJ-42", Version: 0, Hash: mirror.Hash([]byte(body)), Path: "PROJ/PROJ-42.wiki"})
	batch.RecordView("PROJ-42", viewStateOf(rsFull))
	if err := batch.Flush(); err != nil {
		t.Fatal(err)
	}
	return NewJiraRenderer(&config.Config{}), root, mdPath, wikiPath
}

// The Jira half of #166: a full-profile view applied with no flags reproduces the
// recorded (full) pristine view, so an untouched apply succeeds and the .wiki
// stays byte-identical — even though the ambient config is the default profile.
func TestJiraApply_FullProfileRecordedViewUntouched(t *testing.T) {
	svc, root, mdPath, wikiPath := scaffoldApplyIssueFull(t, applyBody)
	res, err := svc.Apply(mdPath, JiraApplyOpts{Into: root})
	if err != nil {
		t.Fatalf("apply full-profile view with no flags: %v", err)
	}
	if res.Report.Converted != 0 || res.Report.Removed != 0 {
		t.Errorf("untouched full view should be all-unchanged, got %+v", res.Report)
	}
	if got := mustReadFile(t, wikiPath); got != applyBody {
		t.Errorf(".wiki not byte-identical after untouched full apply:\n got=%q\nwant=%q", got, applyBody)
	}
}

// Explicit --render-* flags override the recorded view: pointing apply at a
// profile that does NOT match the on-disk view reconstructs a different pristine
// view, so the untouched full .md no longer anchors and the apply refuses.
func TestJiraApply_ExplicitFlagOverridesRecordedView(t *testing.T) {
	svc, root, mdPath, _ := scaffoldApplyIssueFull(t, applyBody)
	_, err := svc.Apply(mdPath, JiraApplyOpts{Into: root, Render: config.RenderService{Profile: "minimal"}})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("mismatched override should refuse, got %v", err)
	}
}

// Pre-upgrade mirror (no recorded view): apply falls back to the ambient config,
// exactly today's behavior. The scaffold writes the .md under the default profile
// and records NO view state; a default-config apply reproduces it and succeeds.
func TestJiraApply_NoRecordedViewFallsBackToAmbient(t *testing.T) {
	svc, root, mdPath, wikiPath := scaffoldApplyIssue(t, applyBody)
	// Assert no view was recorded by the scaffold (pre-upgrade shape).
	if _, ok, err := mirror.New(root).ViewStateOf("PROJ-42"); err != nil || ok {
		t.Fatalf("scaffold unexpectedly recorded a view (ok=%v err=%v)", ok, err)
	}
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatalf("apply with no recorded view: %v", err)
	}
	if got := mustReadFile(t, wikiPath); got != applyBody {
		t.Errorf(".wiki not byte-identical: got=%q want=%q", got, applyBody)
	}
}
