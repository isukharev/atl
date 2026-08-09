package mirror

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func jiraCompletePullFixture(t *testing.T) (*Mirror, CompletePullCheckpoint, CompletePullJournalEntry, []CompletePullArtifact) {
	t.Helper()
	m := New(t.TempDir())
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	body := []byte("h1. Native issue\n")
	state := SyncState{ID: "PROJ-1", Version: 0, Hash: Hash(body), Path: "PROJ/PROJ-1.wiki"}
	entry := CompletePullJournalEntry{
		Identity: "10001",
		State:    state,
		View:     ViewState{Sections: []string{"metadata", "description"}},
	}
	checkpoint := CompletePullCheckpoint{
		Service: CompletePullServiceJira, SelectorSHA256: completePullTestHash,
		OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64),
		IDs: []string{entry.Identity},
	}
	if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	snapshot := []byte("{\n  \"key\": \"PROJ-1\",\n  \"id\": \"10001\",\n  \"fields\": {}\n}\n")
	artifacts := []CompletePullArtifact{
		{Path: mustArtifactPath(state.Path), Role: CompletePullArtifactRoleNative, Data: body, Mode: 0o644},
		{Path: mustArtifactPath("PROJ/PROJ-1.json"), Role: CompletePullArtifactRoleMetadata, Data: snapshot, Mode: 0o644},
		{Path: mustArtifactPath("PROJ/PROJ-1.md"), Role: CompletePullArtifactRoleView, Data: []byte("# PROJ-1\n"), Mode: 0o644, BestEffort: true},
		{Path: mustArtifactPath(".atl/base/PROJ-1.wiki"), Role: CompletePullArtifactRoleBase, Data: body, Mode: 0o600},
		{Path: mustArtifactPath("PROJ/PROJ-1.epic-children.json"), Role: CompletePullArtifactRoleAuxiliary, Data: []byte("{}\n"), Mode: 0o644},
		{Path: mustArtifactPath("PROJ/PROJ-1.assets/image.bin"), Role: CompletePullArtifactRoleAuxiliary, Data: []byte{0, 1, 2}, Mode: 0o644},
	}
	return m, checkpoint, entry, artifacts
}

func assertJiraCompletePullRecovered(t *testing.T, m *Mirror, checkpoint CompletePullCheckpoint, entry CompletePullJournalEntry, artifacts []CompletePullArtifact) {
	t.Helper()
	if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
		t.Fatalf("recover Jira publication: %v", err)
	}
	journal, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256)
	if err != nil || !found || journal.SchemaVersion != completePullJiraJournalSchema || len(journal.Entries) != 1 || !reflect.DeepEqual(journal.Entries[0], entry) {
		t.Fatalf("Jira journal=%+v found=%t err=%v", journal, found, err)
	}
	checkpoint, err = m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true)
	if err != nil {
		t.Fatalf("recover Jira journal: %v", err)
	}
	if checkpoint.NextIndex != 1 {
		t.Fatalf("checkpoint progress=%d", checkpoint.NextIndex)
	}
	state, found, err := m.SyncStateOf(entry.State.ID)
	if err != nil || !found || state != entry.State {
		t.Fatalf("Jira state=%+v found=%t err=%v", state, found, err)
	}
	view, found, err := m.ViewStateOf(entry.State.ID)
	if err != nil || !found || !equalViewState(view, entry.View) {
		t.Fatalf("Jira view=%+v found=%t err=%v", view, found, err)
	}
	for _, artifact := range artifacts {
		got, err := os.ReadFile(filepath.Join(m.Root, filepath.FromSlash(artifact.Path.String())))
		if err != nil || !bytes.Equal(got, artifact.Data) {
			t.Fatalf("artifact role=%s path=%s bytes=%q err=%v", artifact.Role, artifact.Path, got, err)
		}
	}
	if _, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256); err != nil || found {
		t.Fatalf("retired Jira journal found=%t err=%v", found, err)
	}
}

func equalViewState(a, b ViewState) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

func TestJiraCompletePullPublicationRecoversEveryBoundary(t *testing.T) {
	steps := []string{
		"staged_payloads", "intent",
		"artifact:0", "artifact:1", "artifact:2", "artifact:3", "artifact:4", "artifact:5",
		"fully_published", "accepted", "committed", "retired",
	}
	for _, step := range steps {
		step := step
		t.Run(step, func(t *testing.T) {
			m, checkpoint, entry, artifacts := jiraCompletePullFixture(t)
			crashingOps := func() completePullPublicationOps {
				ops := defaultCompletePullPublicationOps()
				ops.after = func(got string) error {
					if got == step {
						return fmt.Errorf("crash at %s", step)
					}
					return nil
				}
				return ops
			}
			var crashErr error
			switch step {
			case "staged_payloads", "intent":
				crashErr = m.prepareCompletePullPublicationWith(checkpoint, 0, entry, true, artifacts, nil, crashingOps())
				if step == "staged_payloads" {
					if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
						t.Fatalf("clean abandoned Jira stage: %v", err)
					}
					if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
						t.Fatalf("restage Jira publication: %v", err)
					}
				}
			default:
				if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
					t.Fatal(err)
				}
				crashErr = m.recoverCompletePullPublicationWith(checkpoint.SelectorSHA256, checkpoint, true, crashingOps())
			}
			if crashErr == nil {
				t.Fatalf("step %s did not interrupt publication", step)
			} else if !strings.Contains(crashErr.Error(), "crash at "+step) {
				t.Fatalf("step %s failed before its fault boundary: %v", step, crashErr)
			}
			assertJiraCompletePullRecovered(t, m, checkpoint, entry, artifacts)
		})
	}
}

func TestJiraCompletePullRejectsImpossibleVariantsBeforeStaging(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CompletePullCheckpoint, *CompletePullJournalEntry, *[]CompletePullArtifact)
	}{
		{name: "missing immutable identity", mutate: func(_ *CompletePullCheckpoint, entry *CompletePullJournalEntry, _ *[]CompletePullArtifact) {
			entry.Identity = ""
		}},
		{name: "non-numeric identity", mutate: func(_ *CompletePullCheckpoint, entry *CompletePullJournalEntry, _ *[]CompletePullArtifact) {
			entry.Identity = "issue-id"
		}},
		{name: "non-canonical identity", mutate: func(checkpoint *CompletePullCheckpoint, entry *CompletePullJournalEntry, _ *[]CompletePullArtifact) {
			checkpoint.IDs[0] = "010001"
			entry.Identity = "010001"
		}},
		{name: "positive version", mutate: func(_ *CompletePullCheckpoint, entry *CompletePullJournalEntry, _ *[]CompletePullArtifact) {
			entry.State.Version = 1
		}},
		{name: "wrong extension", mutate: func(_ *CompletePullCheckpoint, entry *CompletePullJournalEntry, _ *[]CompletePullArtifact) {
			entry.State.Path = "PROJ/PROJ-1.csf"
		}},
		{name: "key path mismatch", mutate: func(_ *CompletePullCheckpoint, entry *CompletePullJournalEntry, _ *[]CompletePullArtifact) {
			entry.State.Path = "PROJ/OTHER.wiki"
		}},
		{name: "missing role", mutate: func(_ *CompletePullCheckpoint, _ *CompletePullJournalEntry, artifacts *[]CompletePullArtifact) {
			(*artifacts)[0].Role = ""
		}},
		{name: "unknown role", mutate: func(_ *CompletePullCheckpoint, _ *CompletePullJournalEntry, artifacts *[]CompletePullArtifact) {
			(*artifacts)[0].Role = "unknown"
		}},
		{name: "metadata role on view", mutate: func(_ *CompletePullCheckpoint, _ *CompletePullJournalEntry, artifacts *[]CompletePullArtifact) {
			(*artifacts)[2].Role = CompletePullArtifactRoleMetadata
		}},
		{name: "public base class", mutate: func(_ *CompletePullCheckpoint, _ *CompletePullJournalEntry, artifacts *[]CompletePullArtifact) {
			(*artifacts)[3].Path = ArtifactPath{value: ".atl/base/PROJ-1.wiki", class: artifactPathClassPublic}
		}},
		{name: "wrong base identity", mutate: func(_ *CompletePullCheckpoint, _ *CompletePullJournalEntry, artifacts *[]CompletePullArtifact) {
			(*artifacts)[3].Path = mustArtifactPath(".atl/base/OTHER.wiki")
		}},
		{name: "view not best effort", mutate: func(_ *CompletePullCheckpoint, _ *CompletePullJournalEntry, artifacts *[]CompletePullArtifact) {
			(*artifacts)[2].BestEffort = false
		}},
		{name: "asset removal", mutate: func(_ *CompletePullCheckpoint, _ *CompletePullJournalEntry, artifacts *[]CompletePullArtifact) {
			(*artifacts)[5].Data = nil
			(*artifacts)[5].Mode = 0
			(*artifacts)[5].Remove = true
		}},
		{name: "native payload drift", mutate: func(_ *CompletePullCheckpoint, _ *CompletePullJournalEntry, artifacts *[]CompletePullArtifact) {
			(*artifacts)[0].Data = []byte("changed native bytes")
		}},
		{name: "base payload drift", mutate: func(_ *CompletePullCheckpoint, _ *CompletePullJournalEntry, artifacts *[]CompletePullArtifact) {
			(*artifacts)[3].Data = []byte("changed base bytes")
		}},
		{name: "snapshot identity drift", mutate: func(_ *CompletePullCheckpoint, _ *CompletePullJournalEntry, artifacts *[]CompletePullArtifact) {
			(*artifacts)[1].Data = []byte(`{"key":"PROJ-1","id":"10002","fields":{}}`)
		}},
		{name: "snapshot fields missing", mutate: func(_ *CompletePullCheckpoint, _ *CompletePullJournalEntry, artifacts *[]CompletePullArtifact) {
			(*artifacts)[1].Data = []byte(`{"key":"PROJ-1","id":"10001"}`)
		}},
		{name: "service mismatch", mutate: func(checkpoint *CompletePullCheckpoint, _ *CompletePullJournalEntry, _ *[]CompletePullArtifact) {
			checkpoint.Service = CompletePullServiceConfluence
		}},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m, checkpoint, entry, artifacts := jiraCompletePullFixture(t)
			tc.mutate(&checkpoint, &entry, &artifacts)
			if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error=%v", err)
			}
			dir, err := m.completePullPublicationDir(checkpoint.SelectorSHA256)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("invalid variant created a publication stage: %v", err)
			}
		})
	}
}

func TestJiraCompletePullJournalRejectsTamperedSnapshotIdentity(t *testing.T) {
	m, checkpoint, entry, artifacts := jiraCompletePullFixture(t)
	if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(m.Root, "PROJ", "PROJ-1.json")
	if err := os.WriteFile(snapshot, []byte("{\"key\":\"PROJ-1\",\"id\":\"99999\",\"fields\":{}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("tampered snapshot error=%v", err)
	}
	if journal, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256); err != nil || !found || len(journal.Entries) != 1 {
		t.Fatalf("journal evidence was not preserved: %+v found=%t err=%v", journal, found, err)
	}
	loaded, found, err := m.CompletePullCheckpoint(checkpoint.SelectorSHA256)
	if err != nil || !found || loaded.NextIndex != 0 {
		t.Fatalf("checkpoint advanced after tamper: %+v found=%t err=%v", loaded, found, err)
	}
}

func TestJiraCompletePullRecoveryRejectsTamperedDurableVariants(t *testing.T) {
	t.Run("intent", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			mutate func(*completePullPublicationIntent)
		}{
			{name: "legacy schema", mutate: func(intent *completePullPublicationIntent) { intent.SchemaVersion = completePullPublicationSchema }},
			{name: "other service", mutate: func(intent *completePullPublicationIntent) { intent.Service = CompletePullServiceConfluence }},
			{name: "missing immutable identity", mutate: func(intent *completePullPublicationIntent) { intent.Entry.Identity = "" }},
			{name: "unknown role", mutate: func(intent *completePullPublicationIntent) { intent.Artifacts[0].Role = "unknown" }},
			{name: "extension drift", mutate: func(intent *completePullPublicationIntent) { intent.Entry.State.Path = "PROJ/PROJ-1.csf" }},
		} {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				m, checkpoint, entry, artifacts := jiraCompletePullFixture(t)
				if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
					t.Fatal(err)
				}
				dir, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
				intentPath := filepath.Join(dir, "intent.json")
				b, err := os.ReadFile(intentPath)
				if err != nil {
					t.Fatal(err)
				}
				var intent completePullPublicationIntent
				if err := decodeCompletePullJSON(intentPath, b, &intent); err != nil {
					t.Fatal(err)
				}
				tc.mutate(&intent)
				b, err = json.MarshalIndent(intent, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(intentPath, append(b, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
					t.Fatalf("tampered intent error=%v", err)
				}
				if _, err := os.Stat(intentPath); err != nil {
					t.Fatalf("tampered intent was not preserved: %v", err)
				}
			})
		}
	})

	t.Run("journal", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			mutate func(*completePullJournal)
		}{
			{name: "legacy schema", mutate: func(journal *completePullJournal) { journal.SchemaVersion = completePullJournalSchema }},
			{name: "other service", mutate: func(journal *completePullJournal) { journal.Service = CompletePullServiceConfluence }},
			{name: "identity drift", mutate: func(journal *completePullJournal) { journal.Entries[0].Identity = "10002" }},
			{name: "key path drift", mutate: func(journal *completePullJournal) { journal.Entries[0].State.Path = "PROJ/OTHER.wiki" }},
		} {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				m, checkpoint, entry, artifacts := jiraCompletePullFixture(t)
				if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
					t.Fatal(err)
				}
				if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
					t.Fatal(err)
				}
				journalPath, _ := m.completePullJournalPath(checkpoint.SelectorSHA256)
				b, err := os.ReadFile(journalPath)
				if err != nil {
					t.Fatal(err)
				}
				var journal completePullJournal
				if err := decodeCompletePullJSON(journalPath, b, &journal); err != nil {
					t.Fatal(err)
				}
				tc.mutate(&journal)
				b, err = json.MarshalIndent(journal, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(journalPath, append(b, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
					t.Fatalf("tampered journal error=%v", err)
				}
				if _, err := os.Stat(journalPath); err != nil {
					t.Fatalf("tampered journal was not preserved: %v", err)
				}
				loaded, found, err := m.CompletePullCheckpoint(checkpoint.SelectorSHA256)
				if err != nil || !found || loaded.NextIndex != 0 {
					t.Fatalf("checkpoint advanced after journal tamper: %+v found=%t err=%v", loaded, found, err)
				}
			})
		}
	})
}

func TestCompletePullCheckpointRejectsUnknownServiceAndNonNumericIdentity(t *testing.T) {
	m := New(t.TempDir())
	base := CompletePullCheckpoint{
		Service: CompletePullServiceJira, SelectorSHA256: completePullTestHash,
		OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64), IDs: []string{"10001"},
	}
	unknown := base
	unknown.Service = "other"
	if err := m.SaveCompletePullCheckpoint(unknown); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unknown service error=%v", err)
	}
	nonNumeric := base
	nonNumeric.IDs = []string{"PROJ-1"}
	if err := m.SaveCompletePullCheckpoint(nonNumeric); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("non-numeric identity error=%v", err)
	}
}

func TestConfluenceCompletePullDurableShapeRemainsSchema2(t *testing.T) {
	m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
	if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
		t.Fatal(err)
	}
	dir, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
	intentBytes, err := os.ReadFile(filepath.Join(dir, "intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(intentBytes, []byte("\"schema_version\": 2")) || bytes.Contains(intentBytes, []byte("\"identity\"")) || bytes.Contains(intentBytes, []byte("\"role\"")) {
		t.Fatalf("legacy Confluence intent shape changed: %s", intentBytes)
	}
	if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
		t.Fatal(err)
	}
	journalPath, _ := m.completePullJournalPath(checkpoint.SelectorSHA256)
	journalBytes, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(journalBytes, []byte("\"schema_version\": 2")) || bytes.Contains(journalBytes, []byte("\"identity\"")) || bytes.Contains(journalBytes, []byte("\"role\"")) {
		t.Fatalf("legacy Confluence journal shape changed: %s", journalBytes)
	}
}
