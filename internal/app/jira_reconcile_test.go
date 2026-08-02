package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type reconcileTracker struct {
	domain.Tracker
	issue  *domain.Issue
	calls  int
	fields []string
}

func (t *reconcileTracker) GetIssue(ctx context.Context, _ string, fields []string) (*domain.Issue, error) {
	t.calls++
	t.fields = append([]string(nil), fields...)
	if !domain.SingleAttempt(ctx) || !domain.RedactedHTTPTrace(ctx) || domain.ReadBudgetFromContext(ctx) == nil {
		return nil, domain.ErrCheckFailed
	}
	copy := *t.issue
	return &copy, nil
}

func TestJiraReconcileClassifiesAndStagesWithoutChangingWorkingBody(t *testing.T) {
	_, _, root, path := setupPulled(t, "base")
	if err := os.WriteFile(path, []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracker := &reconcileTracker{issue: &domain.Issue{ID: "10001", Key: "PROJ-1", Body: "remote", Fields: map[string]any{
		"description": "remote", "updated": "2026-08-02T12:34:56.000+0000",
	}}}
	svc := &JiraService{tr: tracker}
	result, err := svc.PreviewJiraReconcile(context.Background(), path, root)
	if err != nil {
		t.Fatal(err)
	}
	if tracker.calls != 1 || result.Classification.State != "diverged" || result.Reconciled || result.BlockSummary.Diverged != 1 || len(result.Blocks) != 1 || !reflect.DeepEqual(tracker.fields, []string{"description", "updated"}) {
		t.Fatalf("fields=%v result=%+v", tracker.fields, result)
	}
	before, _ := os.ReadFile(path)
	staged, err := svc.StageJiraReconcile(context.Background(), path, root)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || staged.Artifacts == nil || tracker.calls != 2 {
		t.Fatalf("stage changed working body or omitted artifacts: %+v", staged)
	}
}

func TestJiraReconcileRejectsLocalIntegrityBeforeRemoteRead(t *testing.T) {
	_, _, root, path := setupPulled(t, "base")
	if err := os.Remove(root + "/.atl/base/PROJ-1.wiki"); err != nil {
		t.Fatal(err)
	}
	tracker := &reconcileTracker{issue: &domain.Issue{ID: "1", Key: "PROJ-1", Fields: map[string]any{"description": "base", "updated": "2026-08-02T12:34:56Z"}}}
	_, err := (&JiraService{tr: tracker}).PreviewJiraReconcile(context.Background(), path, root)
	if err == nil || tracker.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, tracker.calls)
	}
}

func TestJiraReconcileBoundsLocalBodyBeforeRemoteRead(t *testing.T) {
	_, _, root, path := setupPulled(t, "base")
	if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, nativeReconcileMaxBodyBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	tracker := &reconcileTracker{issue: &domain.Issue{ID: "1", Key: "PROJ-1", Fields: map[string]any{"description": "base", "updated": "2026-08-02T12:34:56Z"}}}
	_, err := (&JiraService{tr: tracker}).PreviewJiraReconcile(context.Background(), path, root)
	if err == nil || tracker.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, tracker.calls)
	}
}

func TestJiraReconcileBoundsPendingRecordBeforeRemoteRead(t *testing.T) {
	_, _, root, path := setupPulled(t, "base")
	pending := &JiraPendingFields{
		Key: "PROJ-1", WikiPath: filepath.Join("PROJ", "PROJ-1.wiki"), WikiHash: mirror.Hash([]byte("base")), WikiBody: "base",
		Fields: []JiraPendingField{{ID: "customfield_10000", Base: "", Value: strings.Repeat("x", nativeReconcileMaxBodyBytes+1)}},
	}
	if err := saveJiraPendingFields(root, pending); err != nil {
		t.Fatal(err)
	}
	tracker := &reconcileTracker{issue: &domain.Issue{ID: "1", Key: "PROJ-1", Fields: map[string]any{"description": "base", "updated": "2026-08-02T12:34:56Z"}}}
	_, err := (&JiraService{tr: tracker}).PreviewJiraReconcile(context.Background(), path, root)
	if err == nil || tracker.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, tracker.calls)
	}
}

func TestJiraReconcilePendingRecordHasIndependentAggregateBound(t *testing.T) {
	_, _, root, _ := setupPulled(t, "base")
	pending := &JiraPendingFields{
		Key: "PROJ-1", WikiPath: filepath.Join("PROJ", "PROJ-1.wiki"), WikiHash: mirror.Hash([]byte("base")), WikiBody: "base",
		Fields: []JiraPendingField{{ID: "customfield_10000", Base: strings.Repeat("b", 8<<20), Value: strings.Repeat("v", 8<<20)}},
	}
	if err := saveJiraPendingFields(root, pending); err != nil {
		t.Fatal(err)
	}
	loaded, present, err := loadJiraPendingFieldsReadOnlyWithinLimit(root, "PROJ-1", nativeReconcileMaxPendingBytes)
	if err != nil || !present || loaded == nil || len(loaded.Fields) != 1 {
		t.Fatalf("present=%t err=%v loaded=%v", present, err, loaded != nil)
	}
}

func TestJiraReconcileTransactionPresenceProbeDoesNotReadBody(t *testing.T) {
	_, _, root, _ := setupPulled(t, "base")
	path := jiraPendingFieldsTxnPath(root, "PROJ-1")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("txn"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, int64(nativeReconcileMaxPendingBytes)*4); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadJiraPendingFieldsReadOnlyWithinLimit(root, "PROJ-1", nativeReconcileMaxPendingBytes); err == nil || !strings.Contains(err.Error(), "requires recovery") {
		t.Fatalf("transaction probe error=%v", err)
	}
}
