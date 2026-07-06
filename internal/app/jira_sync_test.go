package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

// syncTracker is a fake Tracker for the pull→status→push cycle. It models a
// single mutable server issue: Search seeds the pull, GetIssue returns the
// current server description, and Update mutates it (last-writer-wins, as Jira
// DC does). Call counters and the last Update let tests assert the description-
// only write and the no-write guarantees.
type syncTracker struct {
	domain.Tracker
	searchIssues []domain.Issue
	serverBody   string            // current remote description (single-issue tests)
	serverBodies map[string]string // per-key remote description (multi-issue tests); wins when non-nil
	getErr       error             // if set, GetIssue fails with it
	getErrOnCall int               // if >0, only the N-th GetIssue fails (1-based) with getErr
	updateErr    error

	getCalls    int
	updateCalls int
	lastUpdate  struct {
		key     string
		summary string
		body    []byte
		fields  map[string]string
	}
}

func (tr *syncTracker) Search(context.Context, string, []string, int, string) ([]domain.Issue, string, error) {
	return tr.searchIssues, "", nil
}

func (tr *syncTracker) GetIssue(_ context.Context, key string, _ []string) (*domain.Issue, error) {
	tr.getCalls++
	if tr.getErr != nil && (tr.getErrOnCall == 0 || tr.getErrOnCall == tr.getCalls) {
		return nil, tr.getErr
	}
	body := tr.serverBody
	if tr.serverBodies != nil {
		body = tr.serverBodies[key]
	}
	return &domain.Issue{Key: key, Project: "PROJ", Summary: "S", Status: "Open", Type: "Task", Body: body}, nil
}

func (tr *syncTracker) Update(_ context.Context, key, summary string, body []byte, fields map[string]string) error {
	tr.updateCalls++
	tr.lastUpdate.key = key
	tr.lastUpdate.summary = summary
	tr.lastUpdate.body = append([]byte(nil), body...)
	tr.lastUpdate.fields = fields
	if tr.updateErr != nil {
		return tr.updateErr
	}
	if tr.serverBodies != nil {
		tr.serverBodies[key] = string(body)
	} else {
		tr.serverBody = string(body)
	}
	return nil
}

// setupPulled pulls one issue into a fresh mirror and returns the service (whose
// tracker is configured so the server description starts equal to the pulled
// base — i.e. no drift) plus the mirror root and the issue's .wiki path.
func setupPulled(t *testing.T, body string) (*JiraService, *syncTracker, string, string) {
	t.Helper()
	into := t.TempDir()
	iss := domain.Issue{Key: "PROJ-1", Project: "PROJ", Summary: "S", Status: "Open", Type: "Task", Body: body}
	tr := &syncTracker{searchIssues: []domain.Issue{iss}, serverBody: body}
	svc := &JiraService{tr: tr}
	if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: into, Limit: 1}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	return svc, tr, into, filepath.Join(into, "PROJ", "PROJ-1.wiki")
}

func editWiki(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("edit .wiki: %v", err)
	}
}

// ---- S3: pull records sidecar + base ----

func TestJiraPullRecordsSidecarAndBase(t *testing.T) {
	_, _, into, wikiPath := setupPulled(t, "h1. Original\n\nbody")

	// Base copy exists under .atl/base/<KEY>.wiki with the verbatim body.
	baseFile := filepath.Join(into, ".atl", "base", "PROJ-1.wiki")
	got, err := os.ReadFile(baseFile)
	if err != nil {
		t.Fatalf("read base: %v", err)
	}
	if string(got) != "h1. Original\n\nbody" {
		t.Fatalf("base body = %q, want the pulled body", got)
	}

	// Sidecar tracks the .wiki, keyed by the issue key, with the body's hash and
	// the .wiki's relative path; a freshly-pulled file reads clean.
	m := mirror.New(into)
	lw, _, err := m.LoadWiki(wikiPath)
	if err != nil {
		t.Fatalf("LoadWiki: %v", err)
	}
	if lw.Synced == nil {
		t.Fatal("pulled issue must have a sidecar entry")
	}
	if lw.Dirty {
		t.Fatal("a freshly pulled .wiki must read clean")
	}
	if lw.Synced.Path != filepath.Join("PROJ", "PROJ-1.wiki") {
		t.Fatalf("sidecar path = %q, want PROJ/PROJ-1.wiki", lw.Synced.Path)
	}
}

// ---- S4: status ----

func TestJiraStatusStates(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		svc, _, into, _ := setupPulled(t, "body")
		entries, err := svc.Status(context.Background(), into, false)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if len(entries) != 1 || entries[0].LocallyEdited || !entries[0].Synced {
			t.Fatalf("clean status = %+v", entries)
		}
	})

	t.Run("dirty", func(t *testing.T) {
		svc, _, into, wikiPath := setupPulled(t, "body")
		editWiki(t, wikiPath, "edited body")
		entries, err := svc.Status(context.Background(), into, false)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if !entries[0].LocallyEdited || !entries[0].Synced {
			t.Fatalf("dirty status = %+v", entries)
		}
	})

	t.Run("never-synced", func(t *testing.T) {
		into := t.TempDir()
		m := mirror.New(into)
		if err := m.EnsureScaffold(); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(into, "PROJ")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		editWiki(t, filepath.Join(dir, "PROJ-9.wiki"), "orphan")
		svc := &JiraService{tr: &syncTracker{}}
		entries, err := svc.Status(context.Background(), into, false)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if len(entries) != 1 || !entries[0].LocallyEdited || entries[0].Synced {
			t.Fatalf("never-synced status = %+v (want locally_edited + synced:false)", entries)
		}
	})

	t.Run("remote drifted", func(t *testing.T) {
		svc, tr, into, _ := setupPulled(t, "base body")
		tr.serverBody = "remote changed underneath" // differs from base
		entries, err := svc.Status(context.Background(), into, true)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if !entries[0].RemoteDrifted {
			t.Fatalf("expected remote_drifted, got %+v", entries[0])
		}
	})

	t.Run("remote error is not in-sync", func(t *testing.T) {
		svc, tr, into, _ := setupPulled(t, "base body")
		tr.getErr = domain.ErrForbidden
		entries, err := svc.Status(context.Background(), into, true)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if entries[0].RemoteError == "" || entries[0].RemoteDrifted {
			t.Fatalf("uncheckable issue must record remote_error and not read drifted: %+v", entries[0])
		}
	})

	t.Run("corrupt sidecar is loud", func(t *testing.T) {
		svc, _, into, _ := setupPulled(t, "body")
		if err := os.WriteFile(filepath.Join(into, ".atl", "state.json"), []byte("{ not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := svc.Status(context.Background(), into, false)
		if !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("corrupt sidecar must be ErrCheckFailed, got %v", err)
		}
	})
}

// ---- S5: push ----

func TestJiraPushDryRunByDefault(t *testing.T) {
	svc, tr, _, wikiPath := setupPulled(t, "line one\nline two\n")
	editWiki(t, wikiPath, "line one\nline two changed\n")

	res, err := svc.Push(context.Background(), wikiPath, JiraPushOpts{}) // no Apply
	if err != nil {
		t.Fatalf("dry-run push: %v", err)
	}
	if tr.updateCalls != 0 {
		t.Fatalf("dry-run must not call Update, got %d calls", tr.updateCalls)
	}
	if len(res.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(res.Items))
	}
	it := res.Items[0]
	if !it.DryRun || it.Pushed {
		t.Fatalf("item should be dry-run, not pushed: %+v", it)
	}
	if it.Diff == "" || !strings.Contains(it.Diff, "line two changed") {
		t.Fatalf("dry-run must carry a diff of the edit, got %q", it.Diff)
	}
}

func TestJiraPushApplyWritesDescriptionOnly(t *testing.T) {
	svc, tr, into, wikiPath := setupPulled(t, "before")
	editWiki(t, wikiPath, "after")

	res, err := svc.Push(context.Background(), wikiPath, JiraPushOpts{Apply: true})
	if err != nil {
		t.Fatalf("apply push: %v", err)
	}
	if tr.updateCalls != 1 {
		t.Fatalf("apply must call Update exactly once, got %d", tr.updateCalls)
	}
	// Description-only allowlist: empty summary, nil fields, body == the local edit.
	if tr.lastUpdate.summary != "" || tr.lastUpdate.fields != nil {
		t.Fatalf("Update must touch neither summary nor fields, got summary=%q fields=%v", tr.lastUpdate.summary, tr.lastUpdate.fields)
	}
	if string(tr.lastUpdate.body) != "after" {
		t.Fatalf("Update body = %q, want the local body", tr.lastUpdate.body)
	}
	if !res.Items[0].Pushed || res.Items[0].DryRun {
		t.Fatalf("item should be pushed, not dry-run: %+v", res.Items[0])
	}
	// Refresh: base + sidecar now track the pushed body, so status reads clean.
	entries, err := svc.Status(context.Background(), into, false)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if entries[0].LocallyEdited {
		t.Fatalf("after apply+refresh the file must read clean, got %+v", entries[0])
	}
	if base, ok := mirror.New(into).BaseBodyExt("PROJ-1", ".wiki"); !ok || string(base) != "after" {
		t.Fatalf("base after apply = %q ok=%v, want the pushed body", base, ok)
	}
}

func TestJiraPushDriftRefused(t *testing.T) {
	svc, tr, _, wikiPath := setupPulled(t, "base")
	editWiki(t, wikiPath, "local edit")
	tr.serverBody = "remote moved" // drift vs base

	res, err := svc.Push(context.Background(), wikiPath, JiraPushOpts{Apply: true})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("drift must refuse with ErrCheckFailed (exit 8), got %v", err)
	}
	if errors.Is(err, domain.ErrVersionConflict) {
		t.Fatal("Jira drift must NEVER be ErrVersionConflict (issue #66)")
	}
	if tr.updateCalls != 0 {
		t.Fatalf("a refused push must not call Update, got %d", tr.updateCalls)
	}
	if !res.Items[0].Drifted {
		t.Fatalf("item should be marked drifted: %+v", res.Items[0])
	}
}

func TestJiraPushForceOverDrift(t *testing.T) {
	svc, tr, _, wikiPath := setupPulled(t, "base")
	editWiki(t, wikiPath, "local edit")
	tr.serverBody = "remote moved"

	res, err := svc.Push(context.Background(), wikiPath, JiraPushOpts{Apply: true, Force: true})
	if err != nil {
		t.Fatalf("force push over drift: %v", err)
	}
	if tr.updateCalls != 1 {
		t.Fatalf("force must write, got %d Update calls", tr.updateCalls)
	}
	if string(tr.lastUpdate.body) != "local edit" {
		t.Fatalf("force wrote %q, want the local edit", tr.lastUpdate.body)
	}
	if !res.Items[0].DriftOverridden {
		t.Fatalf("item should note drift_overridden: %+v", res.Items[0])
	}
}

func TestJiraPushUnchangedSkipped(t *testing.T) {
	svc, tr, _, wikiPath := setupPulled(t, "body")

	res, err := svc.Push(context.Background(), wikiPath, JiraPushOpts{Apply: true})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if tr.updateCalls != 0 {
		t.Fatalf("an unchanged file must not be pushed, got %d Update calls", tr.updateCalls)
	}
	if res.Items[0].Skipped != "unchanged" {
		t.Fatalf("item should be skipped unchanged: %+v", res.Items[0])
	}
}

func TestJiraPushRefreshFailureIsWarning(t *testing.T) {
	svc, tr, _, wikiPath := setupPulled(t, "before")
	editWiki(t, wikiPath, "after")
	// Fail only the refresh GetIssue (call #2: #1 is the drift check).
	tr.getErr = domain.ErrForbidden
	tr.getErrOnCall = 2

	res, err := svc.Push(context.Background(), wikiPath, JiraPushOpts{Apply: true})
	if err != nil {
		t.Fatalf("a refresh failure must be a warning, not an error, got %v", err)
	}
	if tr.updateCalls != 1 {
		t.Fatalf("Update should still have run once, got %d", tr.updateCalls)
	}
	if !res.Items[0].Pushed || res.Items[0].Warning == "" {
		t.Fatalf("item should be pushed with a refresh warning: %+v", res.Items[0])
	}
}

func TestJiraPushServer409NotVersionConflict(t *testing.T) {
	svc, tr, _, wikiPath := setupPulled(t, "before")
	editWiki(t, wikiPath, "after")
	// The Jira adapter surfaces a 409 as a generic error (not a mapped sentinel).
	tr.updateErr = errors.New("jira: PUT ... 409: Issue is locked for editing")

	res, err := svc.Push(context.Background(), wikiPath, JiraPushOpts{Apply: true})
	if err == nil {
		t.Fatal("expected the Update error to propagate")
	}
	if errors.Is(err, domain.ErrVersionConflict) {
		t.Fatal("a server 409 must NOT become ErrVersionConflict (issue #66)")
	}
	if res.Items[0].Failed == "" {
		t.Fatalf("item should record the failure: %+v", res.Items[0])
	}
}

func TestJiraPushNotPulledRefused(t *testing.T) {
	into := t.TempDir()
	m := mirror.New(into)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(into, "PROJ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	wikiPath := filepath.Join(dir, "PROJ-9.wiki")
	editWiki(t, wikiPath, "never pulled")
	svc := &JiraService{tr: &syncTracker{}}

	_, err := svc.Push(context.Background(), wikiPath, JiraPushOpts{Apply: true, Into: into})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("pushing a never-pulled file must be a usage error (pull first), got %v", err)
	}
}

func TestJiraPushDirOnlyDirty(t *testing.T) {
	into := t.TempDir()
	issues := []domain.Issue{
		{Key: "PROJ-1", Project: "PROJ", Summary: "S", Status: "Open", Type: "Task", Body: "one"},
		{Key: "PROJ-2", Project: "PROJ", Summary: "S", Status: "Open", Type: "Task", Body: "two"},
	}
	tr := &syncTracker{searchIssues: issues, serverBodies: map[string]string{"PROJ-1": "one", "PROJ-2": "two"}}
	svc := &JiraService{tr: tr}
	if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "x", Into: into, Limit: 0}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	// Edit only PROJ-2.
	editWiki(t, filepath.Join(into, "PROJ", "PROJ-2.wiki"), "two edited")

	res, err := svc.Push(context.Background(), into, JiraPushOpts{Into: into}) // dry-run dir push
	if err != nil {
		t.Fatalf("dir push: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Key != "PROJ-2" {
		t.Fatalf("a dir push must touch only the dirty file, got %+v", res.Items)
	}
}
