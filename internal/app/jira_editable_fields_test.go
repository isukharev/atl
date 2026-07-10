package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

const editableFieldID = "customfield_10003"

func editableFieldRender(t *testing.T) RenderSettings {
	t.Helper()
	rs, warns := computeSettings("jira", config.RenderService{
		Profile: "full",
		FieldViews: []config.JiraFieldView{{
			ID: editableFieldID, Label: "Risk Notes", Placement: "section", Format: "jira_wiki", Editable: true,
		}},
	})
	if len(warns) != 0 {
		t.Fatalf("render warnings: %v", warns)
	}
	return rs
}

func scaffoldEditableField(t *testing.T) (*JiraService, string, string, string, []byte) {
	t.Helper()
	svc, root, mdPath, wikiPath := scaffoldApplyIssue(t, applyBody)
	dir := filepath.Dir(mdPath)
	is, ok := loadIssueSnapshot(root, filepath.Join(dir, "PROJ-42.json"))
	if !ok {
		t.Fatal("snapshot did not load")
	}
	is.Body = applyBody
	rs := editableFieldRender(t)
	snapshotPath := filepath.Join(dir, "PROJ-42.json")
	mustWriteSnapshot(t, snapshotPath, is)
	before, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, mdPath, string(renderIssueMarkdown(is, nil, rs)))
	if err := mirror.New(root).SaveViewStates(map[string]mirror.ViewState{"PROJ-42": viewStateOf(rs)}); err != nil {
		t.Fatal(err)
	}
	return svc, root, mdPath, wikiPath, before
}

func TestEditableFieldRenderIsOptInAndTransientViewStaysReadonly(t *testing.T) {
	is := richIssue()
	rs := editableFieldRender(t)
	mirrorView := string(renderIssueMarkdown(is, nil, rs))
	marker := "<!-- atl:section " + jiraFieldSectionID(editableFieldID)
	if !strings.Contains(mirrorView, marker+" editable -->") {
		t.Fatalf("mirror field marker is not editable:\n%s", mirrorView)
	}
	transient := string(renderTransientIssueMarkdown(is, nil, rs))
	if !strings.Contains(transient, marker+" readonly -->") || strings.Contains(transient, marker+" editable -->") {
		t.Fatalf("transient field must remain readonly:\n%s", transient)
	}
}

func TestJiraApplyEditableFieldCreatesPendingWithoutChangingSnapshot(t *testing.T) {
	svc, root, mdPath, wikiPath, snapshotBefore := scaffoldEditableField(t)
	md := mustReadFile(t, mdPath)
	// Editors commonly normalize the final newline count while changing an
	// earlier field. That must not look like a read-only suffix edit.
	mustWriteFile(t, mdPath, strings.TrimRight(strings.Replace(md, "first", "changed", 1), "\n")+"\n")

	res, err := svc.Apply(mdPath, JiraApplyOpts{Into: root})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(res.Fields) != 1 || !res.Fields[0].Pending || res.PendingPath == "" {
		t.Fatalf("field apply result = %+v", res)
	}
	if got := mustReadFile(t, wikiPath); got != applyBody {
		t.Fatalf("field-only apply changed description substrate: %q", got)
	}
	snapshotAfter, err := os.ReadFile(strings.TrimSuffix(mdPath, ".md") + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if string(snapshotAfter) != string(snapshotBefore) {
		t.Fatal("field apply mutated the raw Jira snapshot")
	}
	pending, ok, err := loadJiraPendingFields(root, "PROJ-42")
	if err != nil || !ok {
		t.Fatalf("pending state: ok=%v err=%v", ok, err)
	}
	if len(pending.Fields) != 1 || pending.Fields[0].Base != richFields()[editableFieldID] || !strings.Contains(pending.Fields[0].Value, "changed") {
		t.Fatalf("pending = %+v", pending)
	}
	if !strings.Contains(mustReadFile(t, mdPath), "changed") {
		t.Fatal("refreshed Markdown lost the pending value")
	}

	entries, err := svc.Status(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].LocallyEdited || len(entries[0].PendingFields) != 1 || entries[0].PendingFields[0] != editableFieldID {
		t.Fatalf("status = %+v", entries)
	}
}

func TestJiraApplyEditableFieldAcceptsRelativeMirrorRoot(t *testing.T) {
	svc, root, _, _, _ := scaffoldEditableField(t)
	t.Chdir(root)
	mdPath := filepath.Join("PROJ", "PROJ-42.md")
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: "."}); err != nil {
		t.Fatalf("apply with relative --into: %v", err)
	}
}

func TestJiraApplyEditableFieldKeepsOriginalBaseAcrossRepeatedApply(t *testing.T) {
	svc, root, mdPath, _, _ := scaffoldEditableField(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "second", "also changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	pending, ok, err := loadJiraPendingFields(root, "PROJ-42")
	if err != nil || !ok {
		t.Fatalf("pending: ok=%v err=%v", ok, err)
	}
	if pending.Fields[0].Base != richFields()[editableFieldID] || !strings.Contains(pending.Fields[0].Value, "changed") || !strings.Contains(pending.Fields[0].Value, "also changed") {
		t.Fatalf("repeated apply lost baseline/value: %+v", pending.Fields[0])
	}
}

func TestJiraApplyEditableFieldRevertRemovesPendingState(t *testing.T) {
	svc, root, mdPath, _, _ := scaffoldEditableField(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "changed", "first", 1))
	res, err := svc.Apply(mdPath, JiraApplyOpts{Into: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fields) != 1 || res.Fields[0].Pending || res.PendingPath != "" {
		t.Fatalf("revert result = %+v", res)
	}
	if _, ok, err := loadJiraPendingFields(root, "PROJ-42"); err != nil || ok {
		t.Fatalf("pending survived revert: ok=%v err=%v", ok, err)
	}
}

func TestJiraApplyMissingEditableFieldCanBeAdded(t *testing.T) {
	svc, root, mdPath, _, _ := scaffoldEditableField(t)
	snapshotPath := strings.TrimSuffix(mdPath, ".md") + ".json"
	is, ok := loadIssueSnapshot(root, snapshotPath)
	if !ok {
		t.Fatal("snapshot did not load")
	}
	delete(is.Fields, editableFieldID)
	is.Body = applyBody
	mustWriteSnapshot(t, snapshotPath, is)
	mustWriteFile(t, mdPath, string(renderIssueMarkdown(is, nil, editableFieldRender(t))))
	md := mustReadFile(t, mdPath)
	needle := "# Risk Notes\n\n"
	if !strings.Contains(md, needle) {
		t.Fatalf("empty editable section missing:\n%s", md)
	}
	mustWriteFile(t, mdPath, strings.Replace(md, needle, needle+"New risk.\n\n", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	pending, ok, err := loadJiraPendingFields(root, "PROJ-42")
	if err != nil || !ok || len(pending.Fields) != 1 || pending.Fields[0].Base != "" || pending.Fields[0].Value != "New risk." {
		t.Fatalf("pending = %+v ok=%v err=%v", pending, ok, err)
	}
}

func TestJiraPendingFieldsRejectsIntermediateSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".atl", "pending")); err != nil {
		t.Fatal(err)
	}
	pending := &JiraPendingFields{Key: "PROJ-1", WikiPath: "PROJ/PROJ-1.wiki", Fields: []JiraPendingField{{ID: editableFieldID, Value: "x"}}}
	if err := saveJiraPendingFields(root, pending); err == nil {
		t.Fatal("pending write followed an intermediate symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "jira", "PROJ-1.json")); !os.IsNotExist(err) {
		t.Fatalf("outside file exists or stat failed unexpectedly: %v", err)
	}
}

type editableSyncTracker struct {
	domain.Tracker
	issue          domain.Issue
	setCalls       int
	lastSet        map[string]any
	setErr         error
	ambiguousWrite bool
	getCalls       int
	getErr         error
	getErrOnCall   int
	afterSet       func()
	lastGetFields  []string
}

func (tr *editableSyncTracker) Search(context.Context, string, []string, int, string) ([]domain.Issue, string, error) {
	return []domain.Issue{tr.issue}, "", nil
}

func (tr *editableSyncTracker) GetIssue(_ context.Context, _ string, fields []string) (*domain.Issue, error) {
	tr.getCalls++
	tr.lastGetFields = append([]string(nil), fields...)
	if tr.getErr != nil && (tr.getErrOnCall == 0 || tr.getErrOnCall == tr.getCalls) {
		return nil, tr.getErr
	}
	copyIssue := tr.issue
	copyIssue.Fields = make(map[string]any, len(fields))
	for _, field := range fields {
		if value, ok := tr.issue.Fields[field]; ok {
			copyIssue.Fields[field] = value
		}
	}
	return &copyIssue, nil
}

func (tr *editableSyncTracker) SetFields(_ context.Context, _ string, fields map[string]any) error {
	tr.setCalls++
	tr.lastSet = make(map[string]any, len(fields))
	for k, v := range fields {
		tr.lastSet[k] = v
	}
	if tr.setErr != nil && !tr.ambiguousWrite {
		return tr.setErr
	}
	for k, v := range fields {
		if k == "description" {
			tr.issue.Body = v.(string)
			tr.issue.Fields[k] = v
		} else {
			tr.issue.Fields[k] = v
		}
	}
	if tr.afterSet != nil {
		tr.afterSet()
	}
	if tr.setErr != nil {
		return tr.setErr
	}
	return nil
}

func setupEditablePulled(t *testing.T) (*JiraService, *editableSyncTracker, string, string, string) {
	t.Helper()
	root := t.TempDir()
	fields := richFields()
	fields["description"] = applyBody
	is := *richIssue()
	is.Body = applyBody
	is.Fields = fields
	tr := &editableSyncTracker{issue: is}
	svc := &JiraService{tr: tr}
	rs := editableFieldRender(t)
	view := config.RenderService{Profile: "full", FieldViews: rs.FieldViews}
	if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: root, Limit: 1, Render: view}); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join(root, "PROJ", "PROJ-42.md")
	wikiPath := filepath.Join(root, "PROJ", "PROJ-42.wiki")
	return svc, tr, root, mdPath, wikiPath
}

func TestJiraPushFieldOnlyIsDiscoveredAndRefreshesSnapshot(t *testing.T) {
	svc, tr, root, mdPath, _ := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}

	preview, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Items) != 1 || len(preview.Items[0].Fields) != 1 || tr.setCalls != 0 {
		t.Fatalf("preview=%+v setCalls=%d", preview, tr.setCalls)
	}
	result, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || !result.Items[0].Pushed || tr.setCalls != 1 || !strings.Contains(tr.lastSet[editableFieldID].(string), "changed") {
		t.Fatalf("push=%+v lastSet=%v calls=%d", result, tr.lastSet, tr.setCalls)
	}
	if _, ok, err := loadJiraPendingFields(root, "PROJ-42"); err != nil || ok {
		t.Fatalf("pending not cleared: ok=%v err=%v", ok, err)
	}
	b, err := os.ReadFile(filepath.Join(root, "PROJ", "PROJ-42.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snap JiraIssueSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	if got, _ := snap.Fields[editableFieldID].(string); !strings.Contains(got, "changed") {
		t.Fatalf("snapshot was not refreshed: %v", snap.Fields[editableFieldID])
	}
}

func TestJiraPushDescriptionAndFieldUsesOneTypedWrite(t *testing.T) {
	svc, tr, root, mdPath, _ := setupEditablePulled(t)
	md := mustReadFile(t, mdPath)
	md = strings.Replace(md, "Intro paragraph.", "Intro changed.", 1)
	md = strings.Replace(md, "first", "risk changed", 1)
	mustWriteFile(t, mdPath, md)
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true}); err != nil {
		t.Fatal(err)
	}
	if tr.setCalls != 1 || !strings.Contains(tr.lastSet["description"].(string), "Intro changed") || !strings.Contains(tr.lastSet[editableFieldID].(string), "risk changed") {
		t.Fatalf("atomic values=%v calls=%d", tr.lastSet, tr.setCalls)
	}
}

func TestJiraPushPendingFieldDriftRefusedEvenWithForce(t *testing.T) {
	svc, tr, root, mdPath, _ := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	tr.issue.Fields[editableFieldID] = "remote changed"
	res, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true, Force: true})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("drift err=%v result=%+v", err, res)
	}
	if tr.setCalls != 0 || len(res.Items) != 1 || !res.Items[0].FieldDrifted {
		t.Fatalf("drift must fail before write: calls=%d result=%+v", tr.setCalls, res)
	}
}

func TestJiraPushRejectsPendingFieldOutsideRecordedEditableView(t *testing.T) {
	svc, tr, root, mdPath, _ := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	pending, ok, err := loadJiraPendingFields(root, "PROJ-42")
	if err != nil || !ok {
		t.Fatalf("pending: ok=%v err=%v", ok, err)
	}
	pending.Fields[0].ID = "summary"
	if err := saveJiraPendingFields(root, pending); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unauthorized pending err=%v result=%+v", err, res)
	}
	if tr.setCalls != 0 {
		t.Fatalf("unauthorized pending field reached write: %d", tr.setCalls)
	}
}

func TestJiraPushRejectsPendingPathDifferentFromSidecar(t *testing.T) {
	svc, tr, root, mdPath, wikiPath := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	pending, ok, err := loadJiraPendingFields(root, "PROJ-42")
	if err != nil || !ok {
		t.Fatalf("pending: ok=%v err=%v", ok, err)
	}
	otherDir := filepath.Join(root, "OTHER")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	otherWiki := filepath.Join(otherDir, "PROJ-42.wiki")
	mustWriteFile(t, otherWiki, mustReadFile(t, wikiPath))
	pending.WikiPath = filepath.Join("OTHER", "PROJ-42.wiki")
	if err := saveJiraPendingFields(root, pending); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true})
	if !errors.Is(err, domain.ErrCheckFailed) || tr.setCalls != 0 {
		t.Fatalf("mismatched path was not refused: err=%v calls=%d", err, tr.setCalls)
	}
}

func TestJiraPendingTransactionRecoversCombinedApply(t *testing.T) {
	svc, _, root, mdPath, _ := setupEditablePulled(t)
	md := mustReadFile(t, mdPath)
	md = strings.Replace(md, "Intro paragraph.", "Intro changed.", 1)
	md = strings.Replace(md, "first", "risk changed", 1)
	mustWriteFile(t, mdPath, md)
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	final := jiraPendingFieldsPath(root, "PROJ-42")
	txn := jiraPendingFieldsTxnPath(root, "PROJ-42")
	if err := safepath.RenameWithin(root, final, txn); err != nil {
		t.Fatal(err)
	}
	pending, ok, err := loadJiraPendingFields(root, "PROJ-42")
	if err != nil || !ok || len(pending.Fields) != 1 {
		t.Fatalf("transaction recovery: pending=%+v ok=%v err=%v", pending, ok, err)
	}
	if _, err := os.Stat(txn); !os.IsNotExist(err) {
		t.Fatalf("transaction was not promoted: %v", err)
	}
}

func TestJiraPendingTransactionRecoversFieldOnlyApply(t *testing.T) {
	svc, _, root, mdPath, _ := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "risk changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	final := jiraPendingFieldsPath(root, "PROJ-42")
	txn := jiraPendingFieldsTxnPath(root, "PROJ-42")
	if err := safepath.RenameWithin(root, final, txn); err != nil {
		t.Fatal(err)
	}
	pending, ok, err := loadJiraPendingFields(root, "PROJ-42")
	if err != nil || !ok || len(pending.Fields) != 1 || pending.BeforeWikiHash != pending.WikiHash {
		t.Fatalf("field-only transaction recovery: pending=%+v ok=%v err=%v", pending, ok, err)
	}
}

func TestJiraPendingTransactionRejectsWikiBodyHashMismatch(t *testing.T) {
	svc, _, root, mdPath, _ := setupEditablePulled(t)
	md := strings.Replace(mustReadFile(t, mdPath), "Intro paragraph.", "Intro changed.", 1)
	md = strings.Replace(md, "first", "risk changed", 1)
	mustWriteFile(t, mdPath, md)
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	final := jiraPendingFieldsPath(root, "PROJ-42")
	txn := jiraPendingFieldsTxnPath(root, "PROJ-42")
	if err := safepath.RenameWithin(root, final, txn); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(txn)
	if err != nil {
		t.Fatal(err)
	}
	var pending JiraPendingFields
	if err := json.Unmarshal(b, &pending); err != nil {
		t.Fatal(err)
	}
	pending.WikiBody = "tampered"
	b, _ = json.MarshalIndent(pending, "", "  ")
	if err := safepath.WriteFileWithin(root, txn, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadJiraPendingFields(root, "PROJ-42"); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("tampered transaction was not rejected: %v", err)
	}
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatalf("tampered transaction was promoted: %v", err)
	}
}

func TestJiraPendingRecoveryDoesNotReapLiveApplyTransaction(t *testing.T) {
	svc, _, root, mdPath, _ := setupEditablePulled(t)
	md := strings.Replace(mustReadFile(t, mdPath), "Intro paragraph.", "Intro changed.", 1)
	md = strings.Replace(md, "first", "risk changed", 1)
	mustWriteFile(t, mdPath, md)
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	final := jiraPendingFieldsPath(root, "PROJ-42")
	txn := jiraPendingFieldsTxnPath(root, "PROJ-42")
	if err := safepath.RenameWithin(root, final, txn); err != nil {
		t.Fatal(err)
	}
	lock, err := lockJiraPendingFields(root, "PROJ-42")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadJiraPendingFields(root, "PROJ-42"); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("concurrent recovery did not respect live lock: %v", err)
	}
	if _, err := os.Stat(txn); err != nil {
		t.Fatalf("live transaction was reaped: %v", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := loadJiraPendingFields(root, "PROJ-42"); err != nil || !ok {
		t.Fatalf("post-unlock recovery failed: ok=%v err=%v", ok, err)
	}
}

func TestJiraApplyRefusesConcurrentIssueLock(t *testing.T) {
	svc, root, mdPath, wikiPath, _ := scaffoldEditableField(t)
	before := mustReadFile(t, wikiPath)
	lock, err := lockJiraPendingFields(root, "PROJ-42")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Unlock() }()
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("concurrent apply lock error=%v", err)
	}
	if got := mustReadFile(t, wikiPath); got != before {
		t.Fatal("concurrent apply changed wiki")
	}
}

func TestJiraIssueLockSurvivesAtomicWikiReplacement(t *testing.T) {
	_, root, _, wikiPath, _ := scaffoldEditableField(t)
	first, err := lockJiraPendingFields(root, "PROJ-42")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Unlock() }()
	if err := safepath.WriteFileWithin(root, wikiPath, []byte("atomic replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	if second, err := lockJiraPendingFields(root, "PROJ-42"); !errors.Is(err, domain.ErrCheckFailed) || second != nil {
		t.Fatalf("atomic wiki replacement bypassed stable lock: lock=%v err=%v", second, err)
	}
}

func TestJiraRebaseLockTimeWikiHashCheckDetectsExternalSave(t *testing.T) {
	root := t.TempDir()
	wikiPath := filepath.Join(root, "PROJ-1.wiki")
	mustWriteFile(t, wikiPath, "reviewed")
	want := mirror.Hash([]byte("reviewed"))
	mustWriteFile(t, wikiPath, "external save")
	matches, err := jiraWikiHasHash(root, wikiPath, want)
	if err != nil {
		t.Fatal(err)
	}
	if matches {
		t.Fatal("lock-time hash check accepted a concurrent external wiki save")
	}
}

func TestJiraPendingDriftCanBeRebasedAfterFreshPull(t *testing.T) {
	svc, tr, root, mdPath, _ := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "local change", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	tr.issue.Fields[editableFieldID] = "remote change"
	view := config.RenderService{Profile: "full", FieldViews: editableFieldRender(t).FieldViews}
	if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: root, Limit: 1, Render: view}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mustReadFile(t, mdPath), "local change") {
		t.Fatal("fresh pull did not preserve the pending proposal for review")
	}
	if _, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("genuine drift should block before rebase: %v", err)
	}
	res, err := svc.Apply(mdPath, JiraApplyOpts{Into: root, RebasePending: true})
	if err != nil || !res.Rebased {
		t.Fatalf("rebase pending: err=%v result=%+v", err, res)
	}
	if _, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true}); err != nil {
		t.Fatalf("push after explicit rebase: %v", err)
	}
}

func TestJiraPendingDriftPullPreservesCombinedDescriptionProposal(t *testing.T) {
	svc, tr, root, mdPath, wikiPath := setupEditablePulled(t)
	md := strings.Replace(mustReadFile(t, mdPath), "Intro paragraph.", "Local Description.", 1)
	md = strings.Replace(md, "first", "local field", 1)
	mustWriteFile(t, mdPath, md)
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	tr.issue.Fields[editableFieldID] = "remote field change"
	view := config.RenderService{Profile: "full", FieldViews: editableFieldRender(t).FieldViews}
	if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: root, Limit: 1, Render: view}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mustReadFile(t, wikiPath), "Local Description.") || !strings.Contains(mustReadFile(t, mdPath), "Local Description.") {
		t.Fatal("fresh pull destroyed the combined local Description proposal")
	}
	res, err := svc.Apply(mdPath, JiraApplyOpts{Into: root, RebasePending: true})
	if err != nil || !res.Rebased {
		t.Fatalf("combined rebase: err=%v result=%+v", err, res)
	}
	if _, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true}); err != nil {
		t.Fatalf("combined push after rebase: %v", err)
	}
	if !strings.Contains(tr.lastSet["description"].(string), "Local Description.") || !strings.Contains(tr.lastSet[editableFieldID].(string), "local field") {
		t.Fatalf("combined proposal not preserved: %v", tr.lastSet)
	}
}

func TestJiraRenderPreservesCombinedPendingDescription(t *testing.T) {
	svc, _, root, mdPath, _ := setupEditablePulled(t)
	md := strings.Replace(mustReadFile(t, mdPath), "Intro paragraph.", "Local Description.", 1)
	md = strings.Replace(md, "first", "local field", 1)
	mustWriteFile(t, mdPath, md)
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	view := config.RenderService{Profile: "full", FieldViews: editableFieldRender(t).FieldViews}
	if _, err := svc.Render(root, view); err != nil {
		t.Fatal(err)
	}
	rendered := mustReadFile(t, mdPath)
	if !strings.Contains(rendered, "Local Description.") || !strings.Contains(rendered, "local field") {
		t.Fatalf("offline render erased combined pending proposal:\n%s", rendered)
	}
	if _, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true}); err != nil {
		t.Fatalf("push after offline render: %v", err)
	}
}

func TestJiraFieldOnlyPendingPullAdoptsRemoteDescription(t *testing.T) {
	svc, tr, root, mdPath, wikiPath := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "local field", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	tr.issue.Body = "Remote Description D2."
	tr.issue.Fields["description"] = tr.issue.Body
	tr.issue.Fields[editableFieldID] = "remote field change"
	view := config.RenderService{Profile: "full", FieldViews: editableFieldRender(t).FieldViews}
	if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: root, Limit: 1, Render: view}); err != nil {
		t.Fatal(err)
	}
	if got := mustReadFile(t, wikiPath); got != "Remote Description D2." {
		t.Fatalf("field-only pending invented a local Description proposal: %q", got)
	}
	if !strings.Contains(mustReadFile(t, mdPath), "Remote Description D2.") {
		t.Fatal("rendered view did not adopt remote Description")
	}
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root, RebasePending: true}); err != nil {
		t.Fatalf("field rebase: %v", err)
	}
	if _, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true}); err != nil {
		t.Fatalf("field push: %v", err)
	}
	if _, sentDescription := tr.lastSet["description"]; sentDescription {
		t.Fatalf("field-only recovery tried to revert remote Description: %v", tr.lastSet)
	}
}

func TestJiraPendingCanBeExplicitlyReboundToReviewedDirectWikiEdit(t *testing.T) {
	svc, tr, root, mdPath, wikiPath := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "field changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, wikiPath, applyBody+"\n\nDirect reviewed edit.")
	if _, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("hash mismatch should refuse before explicit rebind: %v", err)
	}
	res, err := svc.Apply(mdPath, JiraApplyOpts{Into: root, RebasePending: true})
	if err != nil || !res.Rebased {
		t.Fatalf("explicit direct-wiki rebind: err=%v result=%+v", err, res)
	}
	if _, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true}); err != nil {
		t.Fatalf("push rebound combined edit: %v", err)
	}
	if tr.setCalls != 1 || !strings.Contains(tr.lastSet["description"].(string), "Direct reviewed edit") {
		t.Fatalf("combined rebind values=%v calls=%d", tr.lastSet, tr.setCalls)
	}
}

func TestJiraDirectWikiRebindRefusesUnappliedMarkdownFieldEdit(t *testing.T) {
	svc, _, root, mdPath, wikiPath := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "staged", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "second", "not applied", 1))
	mustWriteFile(t, wikiPath, applyBody+"\n\nDirect reviewed edit.")
	_, err := svc.Apply(mdPath, JiraApplyOpts{Into: root, RebasePending: true})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "unapplied Markdown edits") {
		t.Fatalf("unapplied field edit was not protected: %v", err)
	}
	if !strings.Contains(mustReadFile(t, mdPath), "not applied") {
		t.Fatal("refused rebind overwrote the unapplied Markdown field edit")
	}
}

func TestJiraDirectWikiRebindRefusesUnappliedMarkdownDescriptionEdit(t *testing.T) {
	svc, _, root, mdPath, wikiPath := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "staged", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "Intro paragraph.", "Not applied description.", 1))
	mustWriteFile(t, wikiPath, applyBody+"\n\nDirect reviewed edit.")
	_, err := svc.Apply(mdPath, JiraApplyOpts{Into: root, RebasePending: true})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "Description has unapplied") {
		t.Fatalf("unapplied Description was not protected: %v", err)
	}
	if !strings.Contains(mustReadFile(t, mdPath), "Not applied description") {
		t.Fatal("refused rebind overwrote the unapplied Description")
	}
}

func TestJiraRenderCannotHidePendingEditableField(t *testing.T) {
	svc, _, root, mdPath, _ := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Render(root, config.RenderService{
		Profile: "full", Exclude: []string{SecCustomFields}, FieldViews: editableFieldRender(t).FieldViews,
	})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("render hid pending field without refusal: %v", err)
	}
	if !strings.Contains(mustReadFile(t, mdPath), "changed") {
		t.Fatal("failed render overwrote the visible pending proposal")
	}
}

func TestJiraPushAmbiguousTypedWriteReconcilesWithoutReplay(t *testing.T) {
	svc, tr, root, mdPath, _ := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	tr.setErr = errors.New("connection lost after commit")
	tr.ambiguousWrite = true
	res, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true})
	if err != nil {
		t.Fatalf("reconciled push: %v (%+v)", err, res)
	}
	if tr.setCalls != 1 || len(res.Items) != 1 || !res.Items[0].Pushed {
		t.Fatalf("ambiguous write replayed or not accepted: calls=%d result=%+v", tr.setCalls, res)
	}
}

func TestJiraPushAmbiguousReconciliationChecksFullDesiredSet(t *testing.T) {
	svc, tr, root, mdPath, _ := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	const secondID = "customfield_second"
	pending, ok, err := loadJiraPendingFields(root, "PROJ-42")
	if err != nil || !ok {
		t.Fatalf("pending: ok=%v err=%v", ok, err)
	}
	pending.Fields = append(pending.Fields, JiraPendingField{ID: secondID, Base: "second base", Value: "second desired"})
	if err := saveJiraPendingFields(root, pending); err != nil {
		t.Fatal(err)
	}
	m := mirror.New(root)
	view, ok, err := m.ViewStateOf("PROJ-42")
	if err != nil || !ok {
		t.Fatal(err)
	}
	view.FieldViews = append(view.FieldViews, mirror.FieldViewState{ID: secondID, Label: "Second", Placement: "section", Format: "jira_wiki", Editable: true})
	if err := m.SaveViewStates(map[string]mirror.ViewState{"PROJ-42": view}); err != nil {
		t.Fatal(err)
	}
	tr.issue.Fields[secondID] = "second desired" // already satisfied at preflight
	tr.setErr = errors.New("connection lost after commit")
	tr.ambiguousWrite = true
	tr.afterSet = func() { tr.issue.Fields[secondID] = "concurrent change" }
	_, err = svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true})
	if err == nil {
		t.Fatal("partial desired end state was incorrectly reconciled as success")
	}
	if _, ok, _ := loadJiraPendingFields(root, "PROJ-42"); !ok {
		t.Fatal("pending state was cleared after incomplete reconciliation")
	}
}

type definitiveEditableWriteError struct{ secret string }

func (e definitiveEditableWriteError) Error() string   { return "backend echoed " + e.secret }
func (e definitiveEditableWriteError) HTTPStatus() int { return 400 }

func TestJiraPushDefinitiveTypedErrorIsSanitizedAndNotReconciled(t *testing.T) {
	svc, tr, root, mdPath, _ := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	const secret = "private pending value"
	tr.setErr = definitiveEditableWriteError{secret: secret}
	_, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("definitive error was not safely reported: %v", err)
	}
	if tr.getCalls != 1 {
		t.Fatalf("definitive rejection triggered reconciliation GET: %d", tr.getCalls)
	}
}

func TestJiraPushRecoversAfterRemoteCommitAndRefreshFailure(t *testing.T) {
	svc, tr, root, mdPath, _ := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	tr.getErr = errors.New("refresh unavailable")
	tr.getErrOnCall = 2
	first, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true})
	if err != nil || first.Items[0].Warning == "" {
		t.Fatalf("first push should succeed with refresh warning: err=%v result=%+v", err, first)
	}
	if _, ok, _ := loadJiraPendingFields(root, "PROJ-42"); !ok {
		t.Fatal("pending was cleared despite refresh failure")
	}
	tr.getErr = nil
	second, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true})
	if err != nil || !second.Items[0].Pushed {
		t.Fatalf("recovery push: err=%v result=%+v", err, second)
	}
	if tr.setCalls != 1 {
		t.Fatalf("recovery replayed typed write: %d", tr.setCalls)
	}
	if _, ok, _ := loadJiraPendingFields(root, "PROJ-42"); ok {
		t.Fatal("recovery did not clear pending state")
	}
}

func TestJiraPushPostWriteConcurrentOverwriteFailsClosed(t *testing.T) {
	svc, tr, root, mdPath, _ := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	tr.afterSet = func() { tr.issue.Fields[editableFieldID] = "concurrent overwrite" }
	res, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("post-write mismatch did not fail closed: err=%v result=%+v", err, res)
	}
	if len(res.Items) != 1 || !res.Items[0].Pushed || !res.Items[0].FieldDrifted || res.Items[0].Failed == "" {
		t.Fatalf("post-write mismatch item=%+v", res.Items)
	}
	if _, ok, _ := loadJiraPendingFields(root, "PROJ-42"); !ok {
		t.Fatal("post-write mismatch cleared pending state")
	}
}

func TestJiraPushPostWriteDescriptionOnlyOverwriteDoesNotClaimFieldDrift(t *testing.T) {
	svc, tr, root, mdPath, _ := setupEditablePulled(t)
	md := strings.Replace(mustReadFile(t, mdPath), "Intro paragraph.", "Description changed.", 1)
	md = strings.Replace(md, "first", "field changed", 1)
	mustWriteFile(t, mdPath, md)
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	tr.afterSet = func() {
		tr.issue.Body = "concurrent Description overwrite"
		tr.issue.Fields["description"] = tr.issue.Body
	}
	res, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("post-write Description mismatch did not fail: %v", err)
	}
	if len(res.Items) != 1 || !res.Items[0].Drifted || res.Items[0].FieldDrifted {
		t.Fatalf("Description-only mismatch attribution=%+v", res.Items)
	}
}

func TestJiraPushRecoversAfterPendingClearFailure(t *testing.T) {
	svc, tr, root, mdPath, _ := setupEditablePulled(t)
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	pendingDir := filepath.Join(root, ".atl", "pending", "jira")
	tr.afterSet = func() { _ = os.Chmod(pendingDir, 0o500) }
	first, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true})
	_ = os.Chmod(pendingDir, 0o700)
	if err != nil || first.Items[0].Warning == "" {
		t.Fatalf("first push should warn on clear: err=%v result=%+v", err, first)
	}
	tr.afterSet = nil
	second, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true})
	if err != nil || !second.Items[0].Pushed || tr.setCalls != 1 {
		t.Fatalf("clear recovery replayed or failed: err=%v calls=%d result=%+v", err, tr.setCalls, second)
	}
}

func TestJiraPushPreservesExtraSnapshotFields(t *testing.T) {
	svc, tr, root, mdPath, _ := setupEditablePulled(t)
	tr.issue.Fields["customfield_extra"] = map[string]any{"value": "keep"}
	// Seed a field captured by an earlier pull --fields but absent from the
	// current render profile and ordinary refresh projection.
	snapshotPath := filepath.Join(root, "PROJ", "PROJ-42.json")
	b, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	var snap JiraIssueSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	snap.Fields["customfield_extra"] = map[string]any{"value": "keep"}
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("customfield_preserved_%04d", i)
		snap.Fields[id] = i
		tr.issue.Fields[id] = i
	}
	encoded, _ := json.MarshalIndent(snap, "", "  ")
	mustWriteFile(t, snapshotPath, string(append(encoded, '\n')))
	mustWriteFile(t, mdPath, strings.Replace(mustReadFile(t, mdPath), "first", "changed", 1))
	if _, err := svc.Apply(mdPath, JiraApplyOpts{Into: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Push(context.Background(), root, JiraPushOpts{Into: root, Apply: true}); err != nil {
		t.Fatal(err)
	}
	after, ok := loadIssueSnapshot(root, snapshotPath)
	if !ok || after.Fields["customfield_extra"] == nil {
		t.Fatalf("extra snapshot field was dropped: %+v", after)
	}
	if len(tr.lastGetFields) >= 100 {
		t.Fatalf("refresh expanded request with preserved snapshot keys: %d fields", len(tr.lastGetFields))
	}
}
