package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/jiramap"
	"github.com/isukharev/atl/internal/mirror"
)

type mutatingAssetPullTracker struct {
	*assetPullTracker
	mutate func()
}

func (t *mutatingAssetPullTracker) StreamAttachment(ctx context.Context, contentURL string) (io.ReadCloser, error) {
	if t.mutate != nil {
		t.mutate()
		t.mutate = nil
	}
	return t.assetPullTracker.StreamAttachment(ctx, contentURL)
}

func pullLocalIssue(key, body string) domain.Issue {
	issue := jiramap.Issue("id-"+key, key, map[string]any{
		"summary": key, "description": body,
		"project":   map[string]any{"key": "PROJ"},
		"status":    map[string]any{"name": "Open"},
		"issuetype": map[string]any{"name": "Task"},
	})
	return *issue
}

func TestJiraPullPreservesDirtyNativeAndContinuesCleanSiblings(t *testing.T) {
	root := t.TempDir()
	issues := []domain.Issue{pullLocalIssue("PROJ-1", "remote one"), pullLocalIssue("PROJ-2", "remote two")}
	tracker := &assetPullTracker{t: t, issues: issues}
	svc := &JiraService{tr: tracker}
	if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: root}); err != nil {
		t.Fatal(err)
	}
	dirtyPath := filepath.Join(root, "PROJ", "PROJ-1.wiki")
	if err := os.WriteFile(dirtyPath, []byte("local edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracker.issues[0].Body = "new remote one"
	tracker.issues[0].Fields["description"] = "new remote one"
	tracker.issues[1].Body = "new remote two"
	tracker.issues[1].Fields["description"] = "new remote two"

	result, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: root})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("pull error = %v, want check failure", err)
	}
	if got := string(mustReadBytes(t, dirtyPath)); got != "local edit" {
		t.Fatalf("dirty native body was overwritten: %q", got)
	}
	if got := string(mustReadBytes(t, filepath.Join(root, "PROJ", "PROJ-2.wiki"))); got != "new remote two" {
		t.Fatalf("clean sibling was not refreshed: %q", got)
	}
	if result.LocalSafety == nil || result.LocalSafety.Blocked != 1 || result.LocalSafety.Complete {
		t.Fatalf("local safety = %+v", result.LocalSafety)
	}
	if len(result.Issues) != 2 || result.Issues[0].Status != pullLocalBlocked {
		t.Fatalf("issues = %+v", result.Issues)
	}
}

func TestJiraPullStashesDirtyNativeBeforeOverwrite(t *testing.T) {
	root := t.TempDir()
	tracker := &assetPullTracker{t: t, issues: []domain.Issue{pullLocalIssue("PROJ-1", "remote")}}
	svc := &JiraService{tr: tracker}
	if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "key=PROJ-1", Into: root}); err != nil {
		t.Fatal(err)
	}
	wiki := filepath.Join(root, "PROJ", "PROJ-1.wiki")
	if err := os.WriteFile(wiki, []byte("local edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracker.issues[0].Body = "new remote"
	tracker.issues[0].Fields["description"] = "new remote"

	result, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "key=PROJ-1", Into: root, StashLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(mustReadBytes(t, wiki)); got != "new remote" {
		t.Fatalf("wiki = %q", got)
	}
	if result.LocalSafety == nil || len(result.LocalSafety.Actions) != 1 || result.LocalSafety.Actions[0].Status != pullLocalStashed {
		t.Fatalf("local safety = %+v", result.LocalSafety)
	}
	stash := filepath.Join(root, filepath.FromSlash(result.LocalSafety.Actions[0].StashPath))
	if got := string(mustReadBytes(t, stash)); got != "local edit" {
		t.Fatalf("stash = %q", got)
	}
}

func TestJiraPullDryRunWritesNothing(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "absent")
	tracker := &assetPullTracker{t: t, issues: []domain.Issue{pullLocalIssue("PROJ-1", "remote")}}
	result, err := (&JiraService{tr: tracker}).Pull(context.Background(), JiraPullOpts{JQL: "key=PROJ-1", Into: root, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run created mirror root: %v", statErr)
	}
	if result.LocalSafety == nil || !result.LocalSafety.DryRun || len(result.Issues) != 1 || result.Issues[0].Status != "would_pull" {
		t.Fatalf("result = %+v", result)
	}
}

func TestJiraPullPreservesEditedDerivedViewEvenWithNativeOverride(t *testing.T) {
	root := t.TempDir()
	tracker := &assetPullTracker{t: t, issues: []domain.Issue{pullLocalIssue("PROJ-1", "remote")}}
	svc := &JiraService{tr: tracker}
	if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "key=PROJ-1", Into: root}); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join(root, "PROJ", "PROJ-1.md")
	edited := append(mustReadBytes(t, mdPath), []byte("\nlocal note\n")...)
	if err := os.WriteFile(mdPath, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	tracker.issues[0].Body = "new remote"
	tracker.issues[0].Fields["description"] = "new remote"

	result, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "key=PROJ-1", Into: root, OverwriteLocal: true})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("pull error = %v, want check failure", err)
	}
	if got := mustReadBytes(t, mdPath); string(got) != string(edited) {
		t.Fatal("edited derived view was overwritten")
	}
	if result.LocalSafety == nil || result.LocalSafety.Actions[0].Reason != "derived_view_modified" {
		t.Fatalf("local safety = %+v", result.LocalSafety)
	}
}

func TestJiraPullDryRunDoesNotRecoverPendingTransaction(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "PROJ"), 0o755); err != nil {
		t.Fatal(err)
	}
	wikiPath := filepath.Join(root, "PROJ", "PROJ-1.wiki")
	if err := os.WriteFile(wikiPath, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending := &JiraPendingFields{
		Key: "PROJ-1", WikiPath: filepath.ToSlash(filepath.Join("PROJ", "PROJ-1.wiki")),
		BeforeWikiHash: mirror.Hash([]byte("before")), WikiHash: mirror.Hash([]byte("after")), WikiBody: "after",
		Fields: []JiraPendingField{{ID: "customfield_1", Base: "before", Value: "after"}},
	}
	if err := stageJiraPendingTransaction(root, pending); err != nil {
		t.Fatal(err)
	}
	txnPath := jiraPendingFieldsTxnPath(root, "PROJ-1")
	txnBefore := mustReadBytes(t, txnPath)
	tracker := &assetPullTracker{t: t, issues: []domain.Issue{pullLocalIssue("PROJ-1", "remote")}}
	_, err := (&JiraService{tr: tracker}).Pull(context.Background(), JiraPullOpts{JQL: "key=PROJ-1", Into: root, DryRun: true})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error=%v", err)
	}
	if got := mustReadBytes(t, txnPath); string(got) != string(txnBefore) {
		t.Fatal("dry-run changed the transaction")
	}
	if _, statErr := os.Stat(jiraPendingFieldsPath(root, "PROJ-1")); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run promoted pending transaction: %v", statErr)
	}
	if got := string(mustReadBytes(t, wikiPath)); got != "before" {
		t.Fatalf("dry-run changed wiki: %q", got)
	}
}

func TestJiraPullRevalidatesDerivedViewAfterAssetFetch(t *testing.T) {
	root := t.TempDir()
	issue := pullLocalIssue("PROJ-1", "remote")
	tracker := &assetPullTracker{t: t, issues: []domain.Issue{issue}}
	svc := &JiraService{tr: tracker}
	if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "key=PROJ-1", Into: root}); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join(root, "PROJ", "PROJ-1.md")
	beforeWiki := mustReadBytes(t, filepath.Join(root, "PROJ", "PROJ-1.wiki"))
	tracker.issues[0].Fields["attachment"] = []any{att("1", "image.png", "image/png", "image")}
	mutating := &mutatingAssetPullTracker{assetPullTracker: tracker, mutate: func() {
		if err := os.WriteFile(mdPath, []byte("concurrent Markdown edit"), 0o644); err != nil {
			t.Fatal(err)
		}
	}}
	svc.tr = mutating
	result, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "key=PROJ-1", Into: root, Assets: true})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error=%v", err)
	}
	if result.LocalSafety == nil || result.LocalSafety.Blocked != 1 || len(result.Issues) != 1 || result.Issues[0].Status != pullLocalBlocked {
		t.Fatalf("result=%+v", result)
	}
	if got := string(mustReadBytes(t, mdPath)); got != "concurrent Markdown edit" {
		t.Fatalf("concurrent view overwritten: %q", got)
	}
	if got := mustReadBytes(t, filepath.Join(root, "PROJ", "PROJ-1.wiki")); string(got) != string(beforeWiki) {
		t.Fatalf("native body changed unexpectedly: %q", got)
	}
}
