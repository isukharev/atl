package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/wikimd"
)

// applyBody is a Description with a plain paragraph, a {panel} (a wiki-only
// construct for the loss gate), and a trailing paragraph.
const applyBody = "Intro paragraph.\n\n{panel:title=Note}\nheads up\n{panel}\n\nOutro paragraph."

// scaffoldApplyIssue seeds a fake single-issue Jira mirror (the way `jira pull`
// writes it): a `.wiki` substrate, a `<KEY>.json` snapshot, a rendered `.md` view,
// a pristine `.atl/base` copy, and a sidecar sync entry. It returns an offline
// JiraService plus the mirror root and the `.md`/`.wiki` paths.
func scaffoldApplyIssue(t *testing.T, body string) (svc *JiraService, root, mdPath, wikiPath string) {
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

	wikiPath = filepath.Join(dir, "PROJ-42.wiki")
	mdPath = filepath.Join(dir, "PROJ-42.md")
	mustWriteFile(t, wikiPath, body)
	mustWriteSnapshot(t, filepath.Join(dir, "PROJ-42.json"), is)
	mustWriteFile(t, mdPath, string(renderIssueMarkdown(is, nil, jiraRS("default"))))
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
	return NewJiraRenderer(&config.Config{}), root, mdPath, wikiPath
}

func TestJiraApply_UntouchedRoundTrips(t *testing.T) {
	svc, root, mdPath, wikiPath := scaffoldApplyIssue(t, applyBody)
	res, err := svc.Apply(mdPath, JiraApplyOpts{Into: root})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Wrote || res.Report == nil {
		t.Fatalf("result = %+v", res)
	}
	if res.Report.Converted != 0 || res.Report.Moved != 0 || res.Report.Removed != 0 {
		t.Errorf("untouched view should be all-unchanged, got %+v", res.Report)
	}
	if got := mustReadFile(t, wikiPath); got != applyBody {
		t.Errorf(".wiki not byte-identical after untouched apply:\n got=%q\nwant=%q", got, applyBody)
	}
}

func TestJiraApply_ParagraphEditMerges(t *testing.T) {
	svc, root, mdPath, wikiPath := scaffoldApplyIssue(t, applyBody)
	md := mustReadFile(t, mdPath)
	mustWriteFile(t, mdPath, strings.Replace(md, "Intro paragraph.", "Intro paragraph, edited.", 1))

	res, err := svc.Apply(mdPath, JiraApplyOpts{Into: root})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := strings.Replace(applyBody, "Intro paragraph.", "Intro paragraph, edited.", 1)
	if got := mustReadFile(t, wikiPath); got != want {
		t.Errorf("merged .wiki mismatch:\n got=%q\nwant=%q", got, want)
	}
	if res.Report.Converted != 1 || res.Report.Unchanged != 2 {
		t.Errorf("report = %+v, want 1 converted / 2 unchanged", res.Report)
	}

	// The issue now reads locally_edited AND still synced (sidecar/base untouched).
	entries, err := svc.Status(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.Key == "PROJ-42" {
			found = true
			if !e.LocallyEdited || !e.Synced {
				t.Errorf("status = %+v, want locally_edited && synced", e)
			}
		}
	}
	if !found {
		t.Fatal("PROJ-42 not found in status")
	}
}

func TestJiraApply_LossRefusedThenAllowed(t *testing.T) {
	svc, root, mdPath, wikiPath := scaffoldApplyIssue(t, applyBody)
	md := mustReadFile(t, mdPath)
	panelMD := wikimd.Render("{panel:title=Note}\nheads up\n{panel}", wikimd.Options{})
	edited := strings.Replace(md, panelMD+"\n\n", "", 1)
	if edited == md {
		t.Fatalf("panel block %q not found in view %q", panelMD, md)
	}
	mustWriteFile(t, mdPath, edited)

	// Refused: exit-8 semantics + report attached, and nothing written.
	res, err := svc.Apply(mdPath, JiraApplyOpts{Into: root})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err = %v, want ErrCheckFailed", err)
	}
	if res == nil || res.Report == nil || len(res.Report.RemovedConstructs) == 0 {
		t.Fatalf("expected a report listing removed constructs, got %+v", res)
	}
	if res.Wrote {
		t.Error("refusal must not write")
	}
	if got := mustReadFile(t, wikiPath); got != applyBody {
		t.Errorf(".wiki modified on a refusal path: %q", got)
	}

	// Allowed: proceeds, drops the panel, reports it.
	res, err = svc.Apply(mdPath, JiraApplyOpts{Into: root, AllowLoss: true})
	if err != nil {
		t.Fatalf("apply --allow-loss: %v", err)
	}
	if !res.Wrote || len(res.Report.RemovedConstructs) == 0 {
		t.Fatalf("allow-loss result = %+v", res)
	}
	if strings.Contains(mustReadFile(t, wikiPath), "{panel") {
		t.Error("panel should be gone after allow-loss apply")
	}
}

func TestJiraApply_CommentEditRefused(t *testing.T) {
	svc, root, mdPath, wikiPath := scaffoldApplyIssue(t, applyBody)
	md := mustReadFile(t, mdPath)
	if !strings.Contains(md, "## Comments") {
		t.Fatalf("fixture view lacks a Comments section: %q", md)
	}
	mustWriteFile(t, mdPath, strings.Replace(md, "a comment", "a comment (edited)", 1))

	_, err := svc.Apply(mdPath, JiraApplyOpts{Into: root})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err = %v, want ErrCheckFailed", err)
	}
	if !strings.Contains(err.Error(), "comment") {
		t.Errorf("error should point at the comment command: %v", err)
	}
	if got := mustReadFile(t, wikiPath); got != applyBody {
		t.Error(".wiki modified on a comment-edit refusal")
	}
}

func TestJiraApply_FrontmatterEditRefused(t *testing.T) {
	svc, root, mdPath, wikiPath := scaffoldApplyIssue(t, applyBody)
	md := mustReadFile(t, mdPath)
	mustWriteFile(t, mdPath, strings.Replace(md, "summary: Fix the thing", "summary: Hijacked", 1))

	_, err := svc.Apply(mdPath, JiraApplyOpts{Into: root})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err = %v, want ErrCheckFailed", err)
	}
	if got := mustReadFile(t, wikiPath); got != applyBody {
		t.Error(".wiki modified on a frontmatter-edit refusal")
	}
}

func TestJiraApply_DivergedWikiRefused(t *testing.T) {
	svc, root, mdPath, wikiPath := scaffoldApplyIssue(t, applyBody)
	mustWriteFile(t, wikiPath, applyBody+"\n\nDirect wiki edit.")

	_, err := svc.Apply(mdPath, JiraApplyOpts{Into: root})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err = %v, want ErrCheckFailed (diverged base)", err)
	}
}

func TestJiraApply_MissingBaseRefused(t *testing.T) {
	svc, root, mdPath, _ := scaffoldApplyIssue(t, applyBody)
	if err := os.Remove(filepath.Join(root, ".atl", "base", "PROJ-42.wiki")); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Apply(mdPath, JiraApplyOpts{Into: root})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (no base)", err)
	}
}

func TestJiraApply_DryRunWritesNothing(t *testing.T) {
	svc, root, mdPath, wikiPath := scaffoldApplyIssue(t, applyBody)
	md := mustReadFile(t, mdPath)
	mustWriteFile(t, mdPath, strings.Replace(md, "Intro paragraph.", "Intro paragraph, edited.", 1))

	res, err := svc.Apply(mdPath, JiraApplyOpts{Into: root, DryRun: true})
	if err != nil {
		t.Fatalf("dry-run apply: %v", err)
	}
	if res.Wrote {
		t.Error("dry-run must not write")
	}
	if got := mustReadFile(t, wikiPath); got != applyBody {
		t.Error("dry-run modified the .wiki")
	}
	if res.Report.Converted != 1 {
		t.Errorf("dry-run should still report the merge: %+v", res.Report)
	}
}

func TestJiraApply_CRLFEditedFileAccepted(t *testing.T) {
	svc, root, mdPath, wikiPath := scaffoldApplyIssue(t, applyBody)
	md := mustReadFile(t, mdPath)
	// Simulate a Windows editor rewriting the whole file with CRLF endings.
	crlf := strings.ReplaceAll(md, "\n", "\r\n")
	mustWriteFile(t, mdPath, crlf)

	res, err := svc.Apply(mdPath, JiraApplyOpts{Into: root})
	if err != nil {
		t.Fatalf("apply CRLF-edited view: %v", err)
	}
	if res.Report.Converted != 0 {
		t.Errorf("CRLF-only re-encoding is not a content edit, got %+v", res.Report)
	}
	if got := mustReadFile(t, wikiPath); got != applyBody {
		t.Errorf(".wiki changed after a CRLF-only edit:\n got=%q\nwant=%q", got, applyBody)
	}
}

func TestJiraApply_NotMdIsUsage(t *testing.T) {
	svc := NewJiraRenderer(&config.Config{})
	_, err := svc.Apply("PROJ-42.wiki", JiraApplyOpts{})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
}

// A wiki h2. heading inside the Description renders as a top-level "## " line in
// the md view. It is BODY content, not a generated section — an untouched apply
// must round-trip the whole body, not truncate at the heading (regression:
// review finding on #159).
func TestJiraApply_BodyH2HeadingRoundTrips(t *testing.T) {
	base := "Intro\n\nh2. Sub\n\nTail paragraph."
	svc, root, mdPath, wikiPath := scaffoldApplyIssue(t, base)
	res, err := svc.Apply(mdPath, JiraApplyOpts{Into: root})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := mustReadFile(t, wikiPath); got != base {
		t.Fatalf(".wiki truncated/altered after untouched apply:\n got=%q\nwant=%q", got, base)
	}
	if res.Report.Removed != 0 {
		t.Errorf("untouched apply reported removed blocks: %+v", res.Report)
	}
}

// A body heading whose text collides with a generated section name must still be
// treated as body content.
func TestJiraApply_BodyHeadingNamedCommentsRoundTrips(t *testing.T) {
	base := "Intro\n\nh2. Comments\n\nnot the comments section"
	svc, root, mdPath, wikiPath := scaffoldApplyIssue(t, base)
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := mustReadFile(t, wikiPath); got != base {
		t.Fatalf(".wiki altered:\n got=%q\nwant=%q", got, base)
	}
}

// Editing a paragraph AFTER a body h2. heading merges normally: the heading and
// everything else keep their exact base bytes.
func TestJiraApply_EditAfterBodyH2Merges(t *testing.T) {
	base := "Intro\n\nh2. Sub\n\nTail paragraph."
	svc, root, mdPath, wikiPath := scaffoldApplyIssue(t, base)
	md := mustReadFile(t, mdPath)
	edited := strings.Replace(md, "Tail paragraph.", "Tail paragraph, edited.", 1)
	mustWriteFile(t, mdPath, edited)
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := "Intro\n\nh2. Sub\n\nTail paragraph, edited."
	if got := mustReadFile(t, wikiPath); got != want {
		t.Fatalf("merged wiki:\n got=%q\nwant=%q", got, want)
	}
}
