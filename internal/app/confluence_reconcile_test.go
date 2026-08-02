package app

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type reconcileDocStore struct {
	domain.DocStore
	page  *domain.Resource
	calls int
}

func (s *reconcileDocStore) GetPage(ctx context.Context, _ string, _ domain.PullOpts) (*domain.Resource, error) {
	s.calls++
	if !domain.SingleAttempt(ctx) || !domain.RedactedHTTPTrace(ctx) || domain.ReadBudgetFromContext(ctx) == nil {
		return nil, domain.ErrCheckFailed
	}
	copy := *s.page
	return &copy, nil
}

func TestConfluenceReconcileClassifiesAndStagesWithoutChangingWorkingBody(t *testing.T) {
	tests := []struct {
		name, ours, theirs, state string
		converged, conflict       bool
	}{
		{name: "unchanged", ours: "<p>x</p>", theirs: "<p>x</p>", state: "unchanged"},
		{name: "local", ours: "<p>local</p>", theirs: "<p>x</p>", state: "local_only"},
		{name: "remote", ours: "<p>x</p>", theirs: "<p>remote</p>", state: "remote_only"},
		{name: "converged", ours: "<p>same</p>", theirs: "<p>same</p>", state: "unchanged", converged: true},
		{name: "diverged", ours: "<p>local</p>", theirs: "<p>remote</p>", state: "diverged", conflict: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, path := syncedMirror(t, 3)
			if err := os.WriteFile(path, []byte(tc.ours), 0o644); err != nil {
				t.Fatal(err)
			}
			remoteVersion := 3
			if tc.theirs != "<p>x</p>" {
				remoteVersion = 4
			}
			store := &reconcileDocStore{page: &domain.Resource{ID: "123", Type: "page", Version: remoteVersion, Body: []byte(tc.theirs), BodyPresent: true}}
			svc := &ConfluenceService{baseURL: confluenceTestBackendURL, store: store}
			result, err := svc.PreviewConfluenceReconcile(context.Background(), path, root)
			if err != nil {
				t.Fatal(err)
			}
			if store.calls != 1 || result.Classification.State != tc.state || result.Classification.Converged != tc.converged || result.Classification.Conflict != tc.conflict || result.Reconciled == tc.conflict {
				t.Fatalf("calls=%d result=%+v", store.calls, result)
			}
			if tc.conflict && (result.BlockSummary.Diverged != 1 || len(result.Blocks) != 1 || result.Blocks[0].State != "diverged") {
				t.Fatalf("three-way block result=%+v summary=%+v", result.Blocks, result.BlockSummary)
			}
			before, _ := os.ReadFile(path)
			staged, err := svc.StageConfluenceReconcile(context.Background(), path, root)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) || staged.Artifacts == nil || store.calls != 2 {
				t.Fatalf("stage changed working body or omitted artifacts: %+v", staged)
			}
		})
	}
}

func TestConfluenceReconcileRejectsLocalIntegrityBeforeRemoteRead(t *testing.T) {
	root, path := syncedMirror(t, 3)
	if err := os.Remove(root + "/.atl/base/123.csf"); err != nil {
		t.Fatal(err)
	}
	store := &reconcileDocStore{page: &domain.Resource{ID: "123", Type: "page", Version: 3, Body: []byte("<p>x</p>"), BodyPresent: true}}
	_, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).PreviewConfluenceReconcile(context.Background(), path, root)
	if err == nil || store.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, store.calls)
	}
}

func TestConfluenceReconcileBoundsLocalBlocksBeforeRemoteRead(t *testing.T) {
	root, path := syncedMirror(t, 3)
	if err := os.WriteFile(path, []byte(strings.Repeat("<p>x</p>", nativeReconcileMaxBlocks+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &reconcileDocStore{page: &domain.Resource{ID: "123", Type: "page", Version: 3, Body: []byte("<p>x</p>"), BodyPresent: true}}
	_, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).PreviewConfluenceReconcile(context.Background(), path, root)
	if err == nil || store.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, store.calls)
	}
}
