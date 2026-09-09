package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestJiraMirrorSnapshotDiscoversInterruptedCompletePull(t *testing.T) {
	root := t.TempDir()
	tracker := newCompleteJiraTracker()
	tracker.getErrorAt = "10"
	service := &JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}
	first, err := service.Pull(t.Context(), JiraPullOpts{Complete: true, Project: "PROJ", MaxIssues: 2, Into: root})
	if err == nil || first.Complete == nil || first.Complete.Completed != 1 {
		t.Fatalf("interrupted pull=%+v err=%v", first, err)
	}
	// Legacy mirrors can lack the persistent coordination file. Inspection
	// must not create it even while discovering an existing checkpoint.
	if err := os.Remove(jiraPendingFieldsLockPath(root)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotJiraMirror(root)
	if err != nil || snapshot.SchemaVersion != 2 || !snapshot.Complete || !snapshot.Reconciled || snapshot.CompletePull.Status != "resumable" || snapshot.CompletePull.Selected != 2 || snapshot.CompletePull.Completed != 1 || snapshot.CompletePull.Remaining != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if _, err := os.Stat(jiraPendingFieldsLockPath(root)); !os.IsNotExist(err) {
		t.Fatalf("snapshot created coordination file: %v", err)
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{root, "PROJ", "native nine", first.Complete.SelectorSHA256} {
		if strings.Contains(string(body), private) {
			t.Fatalf("snapshot exposed fixture content: %s", body)
		}
	}
}

func TestJiraMirrorSnapshotCompletePullStopsRemotePreflight(t *testing.T) {
	for _, state := range []string{"recovery_pending", "invalid", "inventory_limit"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			tracker := newCompleteJiraTracker()
			tracker.getErrorAt = "10"
			service := &JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}
			first, err := service.Pull(t.Context(), JiraPullOpts{Complete: true, Project: "PROJ", MaxIssues: 2, Into: root})
			if err == nil || first.Complete == nil {
				t.Fatal("expected interrupted fixture")
			}
			dir := filepath.Join(root, ".atl", "complete-pulls")
			if state == "recovery_pending" {
				if err := os.Mkdir(filepath.Join(dir, first.Complete.SelectorSHA256+".publish"), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				path := filepath.Join(dir, first.Complete.SelectorSHA256+".json")
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
				if state == "inventory_limit" {
					if err := os.Truncate(path, (64<<20)+1); err != nil {
						t.Fatal(err)
					}
				}
			}
			remote := &jiraSnapshotTracker{body: "unused"}
			for _, inspect := range []func() (*JiraMirrorSnapshot, error){
				func() (*JiraMirrorSnapshot, error) { return PreflightJiraMirrorRemoteSnapshot(root) },
				func() (*JiraMirrorSnapshot, error) {
					return (&JiraService{tr: remote, baseURL: jiraMirrorTestBackendURL}).SnapshotMirror(t.Context(), root, true)
				},
			} {
				got, err := inspect()
				if !errors.Is(err, domain.ErrCheckFailed) || got == nil || got.Complete || got.CompletePull.Status != state || !got.RemoteRequested || got.Remote.Attempted != 0 || remote.calls != 0 {
					t.Fatalf("snapshot=%+v calls=%d err=%v", got, remote.calls, err)
				}
			}
		})
	}
}

type snapshotBlockedCompleteTracker struct {
	*jiraCompleteTracker
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (tracker *snapshotBlockedCompleteTracker) GetIssue(ctx context.Context, key string, fields []string) (*domain.Issue, error) {
	tracker.once.Do(func() { close(tracker.entered); <-tracker.release })
	return tracker.jiraCompleteTracker.GetIssue(ctx, key, fields)
}

func TestJiraMirrorSnapshotCoordinatesWithCompletePullWriter(t *testing.T) {
	root := t.TempDir()
	tracker := &snapshotBlockedCompleteTracker{jiraCompleteTracker: newCompleteJiraTracker(), entered: make(chan struct{}), release: make(chan struct{})}
	tracker.getErrorAt = "10"
	done := make(chan error, 1)
	go func() {
		_, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).Pull(t.Context(), JiraPullOpts{Complete: true, Project: "PROJ", MaxIssues: 2, Into: root})
		done <- err
	}()
	select {
	case <-tracker.entered:
	case err := <-done:
		t.Fatalf("writer ended before fixture barrier: %v", err)
	}
	got, inspectErr := SnapshotJiraMirror(root)
	close(tracker.release)
	if err := <-done; err == nil {
		t.Fatal("fixture did not interrupt")
	}
	if got != nil || !errors.Is(inspectErr, domain.ErrCheckFailed) {
		t.Fatalf("writer overlapped inspection: %+v err=%v", got, inspectErr)
	}
	guard, err := beginMirrorSnapshotLock(root, jiraPendingFieldsLockPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = guard.finish() }()
	second := newCompleteJiraTracker()
	_, err = (&JiraService{tr: second, baseURL: jiraMirrorTestBackendURL}).Pull(t.Context(), JiraPullOpts{Complete: true, Project: "PROJ", MaxIssues: 2, Into: root})
	if !errors.Is(err, domain.ErrCheckFailed) || second.searchCall != 0 || len(second.getCalls) != 0 {
		t.Fatalf("inspection failed to exclude writer: search=%d gets=%d err=%v", second.searchCall, len(second.getCalls), err)
	}
}
