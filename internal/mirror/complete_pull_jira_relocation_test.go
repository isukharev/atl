package mirror

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

func jiraRelocationFixture(t *testing.T) (*Mirror, CompletePullCheckpoint, CompletePullJournalEntry, []CompletePullArtifact, *JiraIssueRelocation) {
	t.Helper()
	m := New(t.TempDir())
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	origin := "sha256:" + strings.Repeat("a", 64)
	if _, err := m.BindBackend(BackendBinding{Service: CorpusSnapshotJira, OriginSHA256: origin}); err != nil {
		t.Fatal(err)
	}
	oldBody := []byte("old native\n")
	oldMD := []byte("<!-- atl:document jira-issue v3 -->\n\n# OLD-1\n")
	oldSnapshot := []byte("{\n  \"key\": \"OLD-1\",\n  \"id\": \"10001\",\n  \"fields\": {\"updated\": \"2026-01-01\"}\n}\n")
	oldState := SyncState{ID: "OLD-1", Identity: "10001", Hash: Hash(oldBody), Path: "OLD/OLD-1.wiki"}
	oldView := ViewState{Sections: []string{"description"}}
	if err := os.MkdirAll(filepath.Join(m.Root, "OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, data := range map[string][]byte{
		oldState.Path:    oldBody,
		"OLD/OLD-1.md":   oldMD,
		"OLD/OLD-1.json": oldSnapshot,
	} {
		if err := safepath.WriteFileWithin(m.Root, filepath.Join(m.Root, filepath.FromSlash(rel)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	comments, err := EncodeJiraCommentsSidecarV1(JiraCommentsSidecarV1{
		SchemaVersion: JiraCommentsSidecarSchemaV1, Service: CorpusSnapshotJira, OriginSHA256: origin,
		ParentID: "10001", ParentRevision: "2026-01-01", NativeSHA256: oldState.Hash, MetadataSHA256: Hash(oldSnapshot),
		Complete: true, Count: 0, TotalKnown: true, PageCount: 1, Comments: []JiraCommentsSidecarComment{},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("abc")
	attachments, err := EncodeAttachmentSidecarV1(AttachmentSidecarV1{
		SchemaVersion: AttachmentSidecarSchemaV1, Service: CorpusSnapshotJira, OriginSHA256: origin,
		ParentID: "10001", ParentRevision: "2026-01-01", NativeSHA256: oldState.Hash, MetadataSHA256: Hash(oldSnapshot),
		InventoryComplete: true, BodiesState: AttachmentBodiesComplete, Complete: true, Count: 1,
		PartialReasons: []AttachmentPartialReason{}, Attachments: []AttachmentSidecarRecord{{
			ID: "7", Filename: "a.bin", DeclaredSize: 3,
			Body: AttachmentSidecarBody{State: AttachmentBodyCaptured, Path: "OLD/OLD-1.attachments/7.body", Size: 3, SHA256: Hash(body)},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel, data := range map[string][]byte{
		"OLD/OLD-1.comments.json": comments, "OLD/OLD-1.attachments.json": attachments,
		"OLD/OLD-1.attachments/7.body": body,
	} {
		if err := safepath.MkdirAllWithin(m.Root, filepath.Dir(filepath.Join(m.Root, filepath.FromSlash(rel))), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := safepath.WriteFileWithin(m.Root, filepath.Join(m.Root, filepath.FromSlash(rel)), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.SaveBaseExt(oldState.ID, oldBody, ".wiki"); err != nil {
		t.Fatal(err)
	}
	batch, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	batch.Record(oldState)
	batch.RecordView(oldState.ID, oldView)
	if err := batch.Flush(); err != nil {
		t.Fatal(err)
	}

	newBody := []byte("new native\n")
	newState := SyncState{ID: "NEW-2", Identity: "10001", Hash: Hash(newBody), Path: "NEW/NEW-2.wiki"}
	entry := CompletePullJournalEntry{Identity: "10001", State: newState, View: oldView}
	checkpoint := CompletePullCheckpoint{
		Service: CompletePullServiceJira, SelectorSHA256: completePullTestHash,
		OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64), IDs: []string{"10001"},
	}
	if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	plan, err := m.PlanJiraIssueRelocation("10001", newState, oldMD)
	if err != nil || plan == nil {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	artifacts := []CompletePullArtifact{
		{Path: mustArtifactPath(newState.Path), Role: CompletePullArtifactRoleNative, Data: newBody, Mode: 0o644},
		{Path: mustArtifactPath("NEW/NEW-2.json"), Role: CompletePullArtifactRoleMetadata, Data: []byte("{\n  \"key\": \"NEW-2\",\n  \"id\": \"10001\",\n  \"fields\": {}\n}\n"), Mode: 0o644},
		{Path: mustArtifactPath("NEW/NEW-2.md"), Role: CompletePullArtifactRoleView, Data: []byte("new view\n"), Mode: 0o644, BestEffort: true},
		{Path: mustArtifactPath(".atl/base/NEW-2.wiki"), Role: CompletePullArtifactRoleBase, Data: newBody, Mode: 0o600},
	}
	return m, checkpoint, entry, artifacts, plan
}

func assertJiraRelocationRecovered(t *testing.T, m *Mirror, checkpoint CompletePullCheckpoint, entry CompletePullJournalEntry) {
	t.Helper()
	if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
		t.Fatal(err)
	}
	journal, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256)
	if err != nil || !found || journal.SchemaVersion != completePullJiraJournalSchema6 || len(journal.Entries) != 1 || journal.Entries[0].Previous == nil || journal.Entries[0].JiraOptionalEvidence == nil {
		t.Fatalf("schema-6 Jira relocation journal=%+v found=%t err=%v", journal, found, err)
	}
	checkpoint, err = m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.NextIndex != 1 {
		t.Fatalf("progress=%d", checkpoint.NextIndex)
	}
	if _, found, err := m.SyncStateOf("OLD-1"); err != nil || found {
		t.Fatalf("old state found=%t err=%v", found, err)
	}
	state, found, err := m.SyncStateOf(entry.State.ID)
	if err != nil || !found || state != entry.State {
		t.Fatalf("new state=%+v found=%t err=%v", state, found, err)
	}
	for _, rel := range []string{
		"OLD/OLD-1.wiki", "OLD/OLD-1.md", "OLD/OLD-1.json", ".atl/base/OLD-1.wiki",
		"OLD/OLD-1.comments.json", "OLD/OLD-1.attachments.json", "OLD/OLD-1.attachments/7.body",
	} {
		if _, err := os.Stat(filepath.Join(m.Root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("retired artifact %s remains: %v", rel, err)
		}
	}
	for _, rel := range []string{"NEW/NEW-2.wiki", "NEW/NEW-2.md", "NEW/NEW-2.json", ".atl/base/NEW-2.wiki"} {
		if _, err := os.Stat(filepath.Join(m.Root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("replacement artifact %s: %v", rel, err)
		}
	}
}

func TestJiraCompletePullRelocationRecoversEveryDurableBoundary(t *testing.T) {
	steps := []string{
		"staged_payloads", "intent", "artifact:0", "artifact:1", "artifact:2", "artifact:3",
		"fully_published", "state", "relocation:0", "relocation:1", "relocation:2", "relocation:3",
		"relocation:4", "relocation:5", "relocation:6",
		"accepted", "committed", "retired",
	}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			m, checkpoint, entry, artifacts, plan := jiraRelocationFixture(t)
			ops := defaultCompletePullPublicationOps()
			ops.after = func(got string) error {
				if got == step {
					return fmt.Errorf("crash at %s", step)
				}
				return nil
			}
			var crashErr error
			switch step {
			case "staged_payloads", "intent":
				crashErr = m.prepareCompletePullPublicationWithJira(checkpoint, 0, entry, true, artifacts, nil, plan, ops)
				if step == "staged_payloads" {
					if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
						t.Fatal(err)
					}
					var planErr error
					plan, planErr = m.PlanJiraIssueRelocation("10001", entry.State, []byte("<!-- atl:document jira-issue v3 -->\n\n# OLD-1\n"))
					if planErr != nil {
						t.Fatal(planErr)
					}
					if err := m.PrepareJiraCompletePullPublication(checkpoint, 0, entry, true, artifacts, plan); err != nil {
						t.Fatal(err)
					}
				}
			default:
				if err := m.PrepareJiraCompletePullPublication(checkpoint, 0, entry, true, artifacts, plan); err != nil {
					t.Fatal(err)
				}
				crashErr = m.recoverCompletePullPublicationWith(checkpoint.SelectorSHA256, checkpoint, true, ops)
			}
			if crashErr == nil || !strings.Contains(crashErr.Error(), "crash at "+step) {
				t.Fatalf("step %s crash error=%v", step, crashErr)
			}
			assertJiraRelocationRecovered(t, m, checkpoint, entry)
		})
	}
}

func TestJiraCompletePullRelocationRejectsChangedOldArtifact(t *testing.T) {
	m, checkpoint, entry, artifacts, plan := jiraRelocationFixture(t)
	if err := os.WriteFile(filepath.Join(m.Root, "OLD", "OLD-1.md"), []byte("local edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.PrepareJiraCompletePullPublication(checkpoint, 0, entry, true, artifacts, plan); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "NEW")); !os.IsNotExist(err) {
		t.Fatalf("changed source created replacement tree: %v", err)
	}
}

func TestJiraCompletePullRelocationPreservesUninventoriedAssets(t *testing.T) {
	m, _, entry, _, _ := jiraRelocationFixture(t)
	assetPath := filepath.Join(m.Root, "OLD", "OLD-1.assets", "local.bin")
	if err := safepath.MkdirAllWithin(m.Root, filepath.Dir(assetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := safepath.WriteFileWithin(m.Root, assetPath, []byte("local asset"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := m.PlanJiraIssueRelocation(
		"10001",
		entry.State,
		[]byte("<!-- atl:document jira-issue v3 -->\n\n# OLD-1\n"),
	)
	if plan != nil || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if got, readErr := os.ReadFile(assetPath); readErr != nil || string(got) != "local asset" {
		t.Fatalf("asset=%q err=%v", got, readErr)
	}
}
