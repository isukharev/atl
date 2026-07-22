package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

func TestJiraMirrorSnapshotIsContentFreeAndReconciled(t *testing.T) {
	_, _, root, wikiPath := setupPulled(t, "private wiki body")
	if err := os.WriteFile(wikiPath, []byte("private local edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := SnapshotJiraMirror(root)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || !result.Reconciled || result.Local.Present != 1 || result.Local.LocallyEdited != 1 ||
		result.Native.Modified != 1 || result.Native.BaselineValid != 1 || result.Snapshot.KeyMatched != 1 ||
		result.Render.Current != 1 || result.Render.StateRecorded != 1 || result.Remote.Eligible != 1 || result.Remote.Attempted != 0 {
		t.Fatalf("snapshot=%+v", result)
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"PROJ-1", "private wiki body", "private local edit", root, wikiPath} {
		if strings.Contains(string(body), private) {
			t.Fatalf("snapshot leaked %q: %s", private, body)
		}
	}
}

func TestJiraMirrorSnapshotEmptyMirrorReconciles(t *testing.T) {
	result, err := SnapshotJiraMirror(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || !result.Reconciled || result.Native.Total != 0 || result.Local.Present != 0 ||
		result.Snapshot.Expected != 0 || result.Render.Expected != 0 || result.Remote.NotAttempted != 0 {
		t.Fatalf("snapshot=%+v", result)
	}
}

func TestJiraMirrorSnapshotDoesNotAttributeDuplicateEvidence(t *testing.T) {
	_, _, root, wikiPath := setupPulled(t, "base")
	duplicateDir := filepath.Join(root, "COPY")
	if err := os.MkdirAll(duplicateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(duplicateDir, "PROJ-1.wiki"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := SnapshotJiraMirror(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Local.Present != 2 || result.Local.Tracked != 1 || result.Local.Untracked != 1 || result.Local.NonCanonical != 1 ||
		result.Native.Unchanged != 1 || result.Native.NonCanonical != 1 || result.Native.BaselinePresent != 1 ||
		result.Native.BaselineMissing != 1 || result.Render.StateRecorded != 1 || result.Render.StateMissing != 1 ||
		result.Remote.Eligible != 1 || !result.Reconciled {
		t.Fatalf("wiki=%s snapshot=%+v", wikiPath, result)
	}
}

func TestJiraMirrorSnapshotIncludesTrackedSubstrateRemovedFromDisk(t *testing.T) {
	_, _, root, wikiPath := setupPulled(t, "base")
	if err := os.Remove(wikiPath); err != nil {
		t.Fatal(err)
	}

	result, err := SnapshotJiraMirror(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Local.Present != 0 || result.Native.Total != 1 || result.Native.Removed != 1 ||
		result.Native.BaselinePresent != 1 || result.Native.BaselineValid != 1 || !result.Reconciled {
		t.Fatalf("snapshot=%+v", result)
	}
}

func TestJiraMirrorSnapshotRejectsMisboundRawSnapshot(t *testing.T) {
	_, _, root, wikiPath := setupPulled(t, "base")
	rawPath := strings.TrimSuffix(wikiPath, wikiExt) + ".json"
	body, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot JiraIssueSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Key = "OTHER-9"
	body, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rawPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	result, snapshotErr := SnapshotJiraMirror(root)
	if !errors.Is(snapshotErr, domain.ErrCheckFailed) || result == nil || result.Complete || !result.Reconciled ||
		result.Snapshot.Valid != 1 || result.Snapshot.KeyMismatched != 1 {
		t.Fatalf("err=%v snapshot=%+v", snapshotErr, result)
	}
	for _, private := range []string{"PROJ-1", "OTHER-9", root} {
		if strings.Contains(snapshotErr.Error(), private) {
			t.Fatalf("error leaked %q: %v", private, snapshotErr)
		}
	}
}

func TestJiraMirrorSnapshotRejectsMalformedRawSnapshot(t *testing.T) {
	_, _, root, wikiPath := setupPulled(t, "base")
	if err := os.WriteFile(strings.TrimSuffix(wikiPath, wikiExt)+".json", []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, snapshotErr := SnapshotJiraMirror(root)
	if !errors.Is(snapshotErr, domain.ErrCheckFailed) || result == nil || result.Complete ||
		result.Snapshot.Present != 1 || result.Snapshot.Readable != 1 || result.Snapshot.Invalid != 1 || !result.Reconciled {
		t.Fatalf("err=%v snapshot=%+v", snapshotErr, result)
	}
}

func TestJiraMirrorSnapshotBaselineMismatchIsQualified(t *testing.T) {
	_, _, root, _ := setupPulled(t, "base")
	if err := os.WriteFile(filepath.Join(root, ".atl", "base", "PROJ-1.wiki"), []byte("wrong"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, snapshotErr := SnapshotJiraMirror(root)
	if !errors.Is(snapshotErr, domain.ErrCheckFailed) || result == nil || result.Complete || !result.Reconciled ||
		result.Native.BaselineMismatch != 1 || result.Native.BaselineInvalid != 1 || result.Remote.Eligible != 0 {
		t.Fatalf("err=%v snapshot=%+v", snapshotErr, result)
	}
}

func TestJiraMirrorSnapshotDistinguishesUnreadableBaseline(t *testing.T) {
	_, _, root, _ := setupPulled(t, "base")
	basePath := filepath.Join(root, ".atl", "base", "PROJ-1.wiki")
	if err := os.Remove(basePath); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.wiki")
	if err := os.WriteFile(outside, []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, basePath); err != nil {
		t.Fatal(err)
	}

	result, snapshotErr := SnapshotJiraMirror(root)
	if !errors.Is(snapshotErr, domain.ErrCheckFailed) || result == nil || result.Complete ||
		result.Native.Unreadable != 1 || result.Native.BaselineUnreadable != 1 || !result.Reconciled {
		t.Fatalf("err=%v snapshot=%+v", snapshotErr, result)
	}
}

func TestJiraMirrorSnapshotReadsPendingWithoutRecoveryOrWrites(t *testing.T) {
	_, _, root, wikiPath := setupPulled(t, "base")
	rel, err := filepath.Rel(root, wikiPath)
	if err != nil {
		t.Fatal(err)
	}
	pending := &JiraPendingFields{
		Key: "PROJ-1", WikiPath: rel, WikiBody: "base", WikiHash: mirror.Hash([]byte("base")),
		Fields: []JiraPendingField{{ID: "customfield_1", Base: "before", Value: "after"}},
	}
	if err := saveJiraPendingFields(root, pending); err != nil {
		t.Fatal(err)
	}
	result, err := SnapshotJiraMirror(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Pending.Total != 1 || result.Pending.Valid != 1 || result.Pending.Bound != 1 || result.Pending.FieldEdits != 1 || !result.Complete {
		t.Fatalf("snapshot=%+v", result)
	}

	pending.BeforeWikiHash = pending.WikiHash
	if err := stageJiraPendingTransaction(root, pending); err != nil {
		t.Fatal(err)
	}
	txnPath := jiraPendingFieldsTxnPath(root, pending.Key)
	result, snapshotErr := SnapshotJiraMirror(root)
	if !errors.Is(snapshotErr, domain.ErrCheckFailed) || result.Pending.ActiveTransactions != 1 || result.Complete {
		t.Fatalf("err=%v snapshot=%+v", snapshotErr, result)
	}
	if _, err := os.Stat(txnPath); err != nil {
		t.Fatalf("snapshot recovered or removed transaction: %v", err)
	}
}

func TestJiraMirrorSnapshotRejectsUnboundPendingRecord(t *testing.T) {
	_, _, root, _ := setupPulled(t, "base")
	pending := &JiraPendingFields{
		Key: "PROJ-1", WikiPath: filepath.Join("OTHER", "PROJ-1.wiki"), WikiBody: "base", WikiHash: mirror.Hash([]byte("base")),
		Fields: []JiraPendingField{{ID: "customfield_1", Base: "before", Value: "after"}},
	}
	if err := saveJiraPendingFields(root, pending); err != nil {
		t.Fatal(err)
	}

	result, snapshotErr := SnapshotJiraMirror(root)
	if !errors.Is(snapshotErr, domain.ErrCheckFailed) || result == nil || result.Complete ||
		result.Pending.Valid != 1 || result.Pending.Bound != 0 || result.Pending.Unbound != 1 || !result.Reconciled {
		t.Fatalf("err=%v snapshot=%+v", snapshotErr, result)
	}
}

func TestJiraViewMarkerClass(t *testing.T) {
	tests := map[string]string{
		jiraIssueDocumentMarker + "\n# current":          "current",
		jiraIssueDocumentMarkerV2 + "\n# legacy":         "legacy",
		jiraIssueDocumentMarkerV1 + "\n# legacy":         "legacy",
		"<!-- atl:document jira-issue -->\n# legacy":     "legacy",
		"<!-- atl:document jira-issue v99 -->\n# future": "unsupported",
		"# no marker": "missing",
	}
	for body, want := range tests {
		if got := jiraViewMarkerClass([]byte(body)); got != want {
			t.Errorf("jiraViewMarkerClass(%q)=%q want %q", body, got, want)
		}
	}
}

type jiraSnapshotTracker struct {
	domain.Tracker
	body          string
	key           string
	calls         int
	singleAttempt bool
	err           error
}

func (t *jiraSnapshotTracker) GetIssue(ctx context.Context, key string, _ []string) (*domain.Issue, error) {
	t.calls++
	t.singleAttempt = domain.SingleAttempt(ctx)
	if t.err != nil {
		return nil, t.err
	}
	responseKey := t.key
	if responseKey == "" {
		responseKey = key
	}
	return &domain.Issue{Key: responseKey, Body: t.body, Fields: map[string]any{}}, nil
}

func TestJiraMirrorRemoteSnapshotRejectsMismatchedIssueIdentity(t *testing.T) {
	_, _, root, _ := setupPulled(t, "base")
	tracker := &jiraSnapshotTracker{body: "base", key: "OTHER-9"}
	result, err := (&JiraService{tr: tracker}).SnapshotMirror(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	if tracker.calls != 1 || result.Remote.Attempted != 1 || result.Remote.Checked != 0 ||
		result.Remote.InSync != 0 || result.Remote.Unavailable != 1 || result.Complete {
		t.Fatalf("calls=%d snapshot=%+v", tracker.calls, result)
	}
}

func TestJiraMirrorRemoteSnapshotUsesOneSingleAttemptProbe(t *testing.T) {
	_, _, root, _ := setupPulled(t, "base")
	tracker := &jiraSnapshotTracker{body: "base"}
	result, err := (&JiraService{tr: tracker}).SnapshotMirror(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	if tracker.calls != 1 || !tracker.singleAttempt || result.Remote.Attempted != 1 || result.Remote.Checked != 1 ||
		result.Remote.InSync != 1 || result.Remote.Drifted != 0 || result.Remote.Unavailable != 0 || !result.Remote.Reconciled {
		t.Fatalf("calls=%d single=%t snapshot=%+v", tracker.calls, tracker.singleAttempt, result)
	}
}

func TestJiraMirrorRemoteSnapshotStopsAtFailedLocalPreflight(t *testing.T) {
	_, _, root, _ := setupPulled(t, "base")
	if err := os.WriteFile(filepath.Join(root, ".atl", "base", "PROJ-1.wiki"), []byte("wrong"), 0o600); err != nil {
		t.Fatal(err)
	}
	tracker := &jiraSnapshotTracker{body: "base"}
	result, snapshotErr := (&JiraService{tr: tracker}).SnapshotMirror(context.Background(), root, true)
	if !errors.Is(snapshotErr, domain.ErrCheckFailed) || tracker.calls != 0 || result.Remote.Attempted != 0 || !result.Remote.Requested {
		t.Fatalf("calls=%d err=%v snapshot=%+v", tracker.calls, snapshotErr, result)
	}
}
