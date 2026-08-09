package mirror

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const completePullTestWriteToken = "0123456789abcdef0123456789abcdef"

func appendCompletePullJournalForTest(m *Mirror, checkpoint CompletePullCheckpoint, index int, entry CompletePullJournalEntry) error {
	if err := validateCompletePullJournalEntry(checkpoint.Service, entry); err != nil {
		return err
	}
	body, err := safepath.ReadFileWithin(m.Root, filepath.Join(m.Root, filepath.FromSlash(entry.State.Path)))
	if err != nil {
		return err
	}
	if err := m.PrepareCompletePullPublication(checkpoint, index, entry, true, []CompletePullArtifact{{Path: mustArtifactPath(entry.State.Path), Data: body, Mode: 0o644}}, nil); err != nil {
		return err
	}
	return m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true)
}

const completePullTestHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestCompletePullCheckpointRoundTripModeAndRetire(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	want := CompletePullCheckpoint{
		Service: "confluence", SelectorSHA256: completePullTestHash,
		OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64),
		IDs: []string{"10", "20"}, NextIndex: 1,
	}
	if err := m.SaveCompletePullCheckpoint(want); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".atl", "complete-pulls", completePullTestHash+".json")
	manifestBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"backend_url", "token", "title", "body"} {
		if strings.Contains(string(manifestBefore), forbidden) {
			t.Fatalf("private checkpoint unexpectedly contains %q", forbidden)
		}
	}
	got, ok, err := m.CompletePullCheckpoint(completePullTestHash)
	if err != nil || !ok || got.SchemaVersion != 1 || got.NextIndex != 1 || len(got.IDs) != 2 {
		t.Fatalf("got=%+v ok=%v err=%v", got, ok, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("checkpoint mode=%v err=%v", info, err)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("checkpoint dir mode=%v err=%v", dirInfo, err)
	}
	want.NextIndex = 2
	if err := m.SaveCompletePullCheckpoint(want); err != nil {
		t.Fatal(err)
	}
	manifestAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(manifestAfter) != string(manifestBefore) {
		t.Fatal("progress update rewrote the immutable identity manifest")
	}
	progressPath := filepath.Join(root, ".atl", "complete-pulls", completePullTestHash+".progress.json")
	progressInfo, err := os.Stat(progressPath)
	if err != nil || progressInfo.Mode().Perm() != 0o600 {
		t.Fatalf("progress mode=%v err=%v", progressInfo, err)
	}
	got, ok, err = m.CompletePullCheckpoint(completePullTestHash)
	if err != nil || !ok || got.NextIndex != 2 {
		t.Fatalf("updated progress=%+v ok=%v err=%v", got, ok, err)
	}
	if err := m.RemoveCompletePullCheckpoint(completePullTestHash); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := m.CompletePullCheckpoint(completePullTestHash); err != nil || ok {
		t.Fatalf("retired checkpoint ok=%v err=%v", ok, err)
	}
}

func TestCompletePullCheckpointRejectsCorruptOrUnsafeState(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.CompletePullCheckpoint("../escape"); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unsafe hash error=%v", err)
	}
	bad := CompletePullCheckpoint{
		Service: "confluence", SelectorSHA256: completePullTestHash,
		OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64),
		IDs: []string{"10", "10"},
	}
	if err := m.SaveCompletePullCheckpoint(bad); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("duplicate checkpoint error=%v", err)
	}
	path, err := m.completePullCheckpointPath(completePullTestHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.CompletePullCheckpoint(completePullTestHash); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("corrupt checkpoint error=%v", err)
	}
}

func TestCompletePullCheckpointIgnoresStaleProgressAfterAtomicRestart(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	old := CompletePullCheckpoint{
		Service: "confluence", SelectorSHA256: completePullTestHash,
		OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64),
		IDs: []string{"10", "20"}, NextIndex: 1,
	}
	if err := m.SaveCompletePullCheckpoint(old); err != nil {
		t.Fatal(err)
	}
	// Model the only cross-file restart crash window: the immutable selection
	// was atomically replaced, but the old tiny progress file still exists.
	restarted := old
	restarted.SchemaVersion = completePullCheckpointSchema
	restarted.OptionsSHA256 = strings.Repeat("d", 64)
	restarted.SelectionSHA256 = strings.Repeat("e", 64)
	restarted.IDs = []string{"30"}
	restarted.NextIndex = 0
	b, err := json.MarshalIndent(restarted, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path, err := m.completePullCheckpointPath(completePullTestHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := safepath.WriteFileWithin(root, path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok, err := m.CompletePullCheckpoint(completePullTestHash)
	if err != nil || !ok || got.NextIndex != 0 || !reflect.DeepEqual(got.IDs, []string{"30"}) {
		t.Fatalf("restarted checkpoint=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestCompletePullCheckpointDoesNotReuseProgressAcrossServices(t *testing.T) {
	for _, tc := range []struct {
		name string
		old  CompletePullService
		new  CompletePullService
	}{
		{name: "Confluence to Jira", old: CompletePullServiceConfluence, new: CompletePullServiceJira},
		{name: "Jira to Confluence", old: CompletePullServiceJira, new: CompletePullServiceConfluence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(t.TempDir())
			checkpoint := CompletePullCheckpoint{
				Service: tc.old, SelectorSHA256: completePullTestHash,
				OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64),
				IDs: []string{"10", "20"}, NextIndex: 1,
			}
			if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
				t.Fatal(err)
			}
			progressPath, _ := m.completePullProgressPath(checkpoint.SelectorSHA256)
			progressBytes, err := os.ReadFile(progressPath)
			if err != nil {
				t.Fatal(err)
			}
			if tc.old == CompletePullServiceConfluence {
				want := fmt.Sprintf("{\n  \"schema_version\": 1,\n  \"selector_sha256\": %q,\n  \"options_sha256\": %q,\n  \"selection_sha256\": %q,\n  \"next_index\": 1\n}\n", checkpoint.SelectorSHA256, checkpoint.OptionsSHA256, checkpoint.SelectionSHA256)
				if string(progressBytes) != want {
					t.Fatalf("legacy Confluence progress bytes changed:\n%s", progressBytes)
				}
			}
			if tc.old == CompletePullServiceJira && (!bytes.Contains(progressBytes, []byte(`"schema_version": 2`)) || !bytes.Contains(progressBytes, []byte(`"service": "jira"`))) {
				t.Fatalf("Jira progress is not service-qualified: %s", progressBytes)
			}

			checkpoint.Service = tc.new
			checkpoint.SchemaVersion = completePullCheckpointSchema
			checkpoint.NextIndex = 0
			manifest, err := json.MarshalIndent(checkpoint, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			manifestPath, _ := m.completePullCheckpointPath(checkpoint.SelectorSHA256)
			if err := safepath.WriteFileWithin(m.Root, manifestPath, append(manifest, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			got, found, err := m.CompletePullCheckpoint(checkpoint.SelectorSHA256)
			if err != nil || !found || got.Service != tc.new || got.NextIndex != 0 {
				t.Fatalf("cross-service restart=%+v found=%t err=%v", got, found, err)
			}
		})
	}
}

func TestCompletePullCheckpointRejectsInvalidServiceQualifiedProgress(t *testing.T) {
	m := New(t.TempDir())
	checkpoint := CompletePullCheckpoint{
		Service: CompletePullServiceJira, SelectorSHA256: completePullTestHash,
		OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64),
		IDs: []string{"10"},
	}
	if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	progressPath, _ := m.completePullProgressPath(checkpoint.SelectorSHA256)
	for name, progress := range map[string]completePullProgress{
		"unversioned": {
			Service: CompletePullServiceJira, SelectorSHA256: checkpoint.SelectorSHA256,
			OptionsSHA256: checkpoint.OptionsSHA256, SelectionSHA256: checkpoint.SelectionSHA256,
		},
		"future": {
			SchemaVersion: completePullJiraProgressSchema + 1, Service: CompletePullServiceJira,
			SelectorSHA256: checkpoint.SelectorSHA256, OptionsSHA256: checkpoint.OptionsSHA256,
			SelectionSHA256: checkpoint.SelectionSHA256,
		},
		"missing service": {
			SchemaVersion: completePullJiraProgressSchema, SelectorSHA256: checkpoint.SelectorSHA256,
			OptionsSHA256: checkpoint.OptionsSHA256, SelectionSHA256: checkpoint.SelectionSHA256,
		},
	} {
		t.Run(name, func(t *testing.T) {
			b, err := json.Marshal(progress)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(progressPath, append(b, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.CompletePullCheckpoint(checkpoint.SelectorSHA256); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("invalid progress error=%v", err)
			}
		})
	}
}

func completePullJournalFixture(t *testing.T, ids ...string) (*Mirror, CompletePullCheckpoint, []CompletePullJournalEntry) {
	t.Helper()
	root := t.TempDir()
	m := New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	selectionHash := Hash([]byte(strings.Join(ids, "\x00")))
	checkpoint := CompletePullCheckpoint{
		Service: "confluence", SelectorSHA256: completePullTestHash,
		OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: selectionHash, IDs: append([]string(nil), ids...),
	}
	if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	batch, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]CompletePullJournalEntry, 0, len(ids))
	for _, id := range ids {
		page := &domain.Resource{ID: id, Title: "Page " + id, SpaceKey: "DOC", Version: 2, Body: []byte("<p>" + id + "</p>")}
		dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
		if err := batch.WriteView(dir, slug, page, nil, MDViewOpts{}); err != nil {
			t.Fatal(err)
		}
		state, ok := batch.dirtyPages[id]
		if !ok {
			t.Fatalf("missing pending state for %s", id)
		}
		entries = append(entries, CompletePullJournalEntry{State: state, View: ViewState{Sections: []string{"content"}}})
	}
	// Deliberately discard the in-memory batch without Flush: page artifacts
	// landed, but only the journal may establish their durable commit prefix.
	return m, checkpoint, entries
}

func TestCompletePullJournalRecoversEveryCrossFileBoundary(t *testing.T) {
	for _, stage := range []string{"journal", "sidecar", "progress", "retired"} {
		t.Run(stage, func(t *testing.T) {
			m, checkpoint, entries := completePullJournalFixture(t, "10")
			if err := appendCompletePullJournalForTest(m, checkpoint, 0, entries[0]); err != nil {
				t.Fatal(err)
			}
			journal, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256)
			if err != nil || !found {
				t.Fatalf("journal found=%t err=%v", found, err)
			}
			switch stage {
			case "sidecar":
				if err := m.mergeCompletePullJournal(journal); err != nil {
					t.Fatal(err)
				}
			case "progress":
				if err := m.mergeCompletePullJournal(journal); err != nil {
					t.Fatal(err)
				}
				checkpoint.NextIndex = 1
				if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
					t.Fatal(err)
				}
			case "retired":
				if err := m.mergeCompletePullJournal(journal); err != nil {
					t.Fatal(err)
				}
				checkpoint.NextIndex = 1
				if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
					t.Fatal(err)
				}
				if err := m.removeCompletePullJournal(checkpoint.SelectorSHA256); err != nil {
					t.Fatal(err)
				}
			}

			got, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true)
			if err != nil || got.NextIndex != 1 {
				t.Fatalf("recovered=%+v err=%v", got, err)
			}
			loaded, ok, err := m.CompletePullCheckpoint(checkpoint.SelectorSHA256)
			if err != nil || !ok || loaded.NextIndex != 1 {
				t.Fatalf("checkpoint=%+v ok=%t err=%v", loaded, ok, err)
			}
			state, ok, err := m.SyncStateOf("10")
			if err != nil || !ok || state != entries[0].State {
				t.Fatalf("state=%+v ok=%t err=%v", state, ok, err)
			}
			if _, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256); err != nil || found {
				t.Fatalf("retired journal found=%t err=%v", found, err)
			}
		})
	}
}

func TestCompletePullJournalIsPrivateBoundedAndConsecutive(t *testing.T) {
	ids := make([]string, 26)
	for i := range ids {
		ids[i] = strconv.Itoa(i + 1)
	}
	m, checkpoint, entries := completePullJournalFixture(t, ids...)
	for i := 0; i < maxCompletePullJournalEntries; i++ {
		if err := appendCompletePullJournalForTest(m, checkpoint, i, entries[i]); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := appendCompletePullJournalForTest(m, checkpoint, maxCompletePullJournalEntries, entries[maxCompletePullJournalEntries]); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("entry bound error=%v", err)
	}
	path, err := m.completePullJournalPath(checkpoint.SelectorSHA256)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 || len(b) > maxCompletePullJournalBytes {
		t.Fatalf("journal info=%v bytes=%d err=%v", info, len(b), err)
	}
	for _, forbidden := range []string{"<p>", "Page 01", "backend_url", "access_token"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("journal retained forbidden content %q", forbidden)
		}
	}

	m2, checkpoint2, entries2 := completePullJournalFixture(t, "10")
	large := entries2[0]
	large.View.FieldViews = []FieldViewState{{ID: strings.Repeat("x", maxCompletePullJournalBytes)}}
	if err := appendCompletePullJournalForTest(m2, checkpoint2, 0, large); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("byte bound error=%v", err)
	}
}

func TestCompletePullJournalMustStartAtDurableProgress(t *testing.T) {
	m, checkpoint, entries := completePullJournalFixture(t, "10", "20")
	if err := appendCompletePullJournalForTest(m, checkpoint, 1, entries[1]); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("skipped-prefix append error=%v", err)
	}
	if _, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256); err != nil || found {
		t.Fatalf("skipped-prefix journal found=%t err=%v", found, err)
	}
}

func TestCompletePullProgressCannotSplitOrOutpaceJournal(t *testing.T) {
	m, checkpoint, entries := completePullJournalFixture(t, "10", "20", "30")
	for i := 0; i < 2; i++ {
		if err := appendCompletePullJournalForTest(m, checkpoint, i, entries[i]); err != nil {
			t.Fatal(err)
		}
	}
	for _, next := range []int{1, 3} {
		candidate := checkpoint
		candidate.NextIndex = next
		if err := m.SaveCompletePullCheckpoint(candidate); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("next=%d error=%v", next, err)
		}
		got, ok, err := m.CompletePullCheckpoint(checkpoint.SelectorSHA256)
		if err != nil || !ok || got.NextIndex != 0 {
			t.Fatalf("next=%d checkpoint=%+v ok=%t err=%v", next, got, ok, err)
		}
	}
	if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
		t.Fatalf("idempotent journal-start progress: %v", err)
	}
	covered := checkpoint
	covered.NextIndex = 2
	if err := m.SaveCompletePullCheckpoint(covered); err != nil {
		t.Fatalf("exact journal-end progress: %v", err)
	}
}

func TestCompletePullJournalRejectsCorruptMismatchedOrTamperedState(t *testing.T) {
	t.Run("corrupt", func(t *testing.T) {
		m, checkpoint, _ := completePullJournalFixture(t, "10")
		path, _ := m.completePullJournalPath(checkpoint.SelectorSHA256)
		if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("corrupt error=%v", err)
		}
	})

	t.Run("binding", func(t *testing.T) {
		m, checkpoint, entries := completePullJournalFixture(t, "10")
		if err := appendCompletePullJournalForTest(m, checkpoint, 0, entries[0]); err != nil {
			t.Fatal(err)
		}
		checkpoint.OptionsSHA256 = strings.Repeat("d", 64)
		if _, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("binding error=%v", err)
		}
	})

	t.Run("gap", func(t *testing.T) {
		m, checkpoint, entries := completePullJournalFixture(t, "10", "20")
		journal := completePullJournal{
			SchemaVersion: completePullJournalSchema, Service: checkpoint.Service,
			SelectorSHA256: checkpoint.SelectorSHA256, OptionsSHA256: checkpoint.OptionsSHA256,
			SelectionSHA256: checkpoint.SelectionSHA256, StartIndex: 0,
			Entries: []CompletePullJournalEntry{entries[1]}, WriteToken: completePullTestWriteToken,
		}
		b, err := json.Marshal(journal)
		if err != nil {
			t.Fatal(err)
		}
		path, _ := m.completePullJournalPath(checkpoint.SelectorSHA256)
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("gap error=%v", err)
		}
	})

	t.Run("invalid state", func(t *testing.T) {
		m, checkpoint, entries := completePullJournalFixture(t, "10")
		for name, mutate := range map[string]func(*CompletePullJournalEntry){
			"version": func(entry *CompletePullJournalEntry) { entry.State.Version = 0 },
			"hash":    func(entry *CompletePullJournalEntry) { entry.State.Hash = "bad" },
			"path":    func(entry *CompletePullJournalEntry) { entry.State.Path = "../escape.csf" },
			"reserved path alias": func(entry *CompletePullJournalEntry) {
				entry.State.Path = ".ATL/base/10.csf"
			},
			"absolute path": func(entry *CompletePullJournalEntry) {
				entry.State.Path = filepath.Join(string(filepath.Separator), "escape.csf")
			},
		} {
			t.Run(name, func(t *testing.T) {
				entry := entries[0]
				mutate(&entry)
				if err := appendCompletePullJournalForTest(m, checkpoint, 0, entry); !errors.Is(err, domain.ErrCheckFailed) {
					t.Fatalf("invalid state error=%v", err)
				}
			})
		}
	})

	t.Run("tampered artifact", func(t *testing.T) {
		m, checkpoint, entries := completePullJournalFixture(t, "10")
		if err := appendCompletePullJournalForTest(m, checkpoint, 0, entries[0]); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(m.Root, filepath.FromSlash(entries[0].State.Path))
		if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("tamper error=%v", err)
		}
		loaded, ok, err := m.CompletePullCheckpoint(checkpoint.SelectorSHA256)
		if err != nil || !ok || loaded.NextIndex != 0 {
			t.Fatalf("checkpoint=%+v ok=%t err=%v", loaded, ok, err)
		}
	})

	t.Run("orphan", func(t *testing.T) {
		m, checkpoint, entries := completePullJournalFixture(t, "10")
		if err := appendCompletePullJournalForTest(m, checkpoint, 0, entries[0]); err != nil {
			t.Fatal(err)
		}
		manifest, _ := m.completePullCheckpointPath(checkpoint.SelectorSHA256)
		progress, _ := m.completePullProgressPath(checkpoint.SelectorSHA256)
		if err := os.Remove(manifest); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(progress); err != nil {
			t.Fatal(err)
		}
		if _, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, CompletePullCheckpoint{}, false); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("orphan error=%v", err)
		}
	})
}

func TestCompletePullRestartRequiresJournalRecoveryBeforeReplacement(t *testing.T) {
	m, checkpoint, entries := completePullJournalFixture(t, "10")
	if err := appendCompletePullJournalForTest(m, checkpoint, 0, entries[0]); err != nil {
		t.Fatal(err)
	}
	restarted := checkpoint
	restarted.OptionsSHA256 = strings.Repeat("d", 64)
	restarted.SelectionSHA256 = strings.Repeat("e", 64)
	restarted.IDs = []string{"20"}
	if err := m.SaveCompletePullCheckpoint(restarted); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("replacement with journal error=%v", err)
	}
	recovered, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true)
	if err != nil || recovered.NextIndex != 1 {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	if err := m.SaveCompletePullCheckpoint(restarted); err != nil {
		t.Fatalf("replacement after recovery: %v", err)
	}
	got, ok, err := m.CompletePullCheckpoint(checkpoint.SelectorSHA256)
	if err != nil || !ok || !reflect.DeepEqual(got.IDs, []string{"20"}) || got.NextIndex != 0 {
		t.Fatalf("restarted=%+v ok=%t err=%v", got, ok, err)
	}
}

func TestCompletePullJournalRecoversOwnedJournalSidecarAndProgressResidue(t *testing.T) {
	for _, kind := range []string{"journal rewrite", "sidecar", "progress"} {
		t.Run(kind, func(t *testing.T) {
			ids := []string{"10"}
			if kind == "journal rewrite" {
				ids = append(ids, "20")
			}
			m, checkpoint, entries := completePullJournalFixture(t, ids...)
			if err := appendCompletePullJournalForTest(m, checkpoint, 0, entries[0]); err != nil {
				t.Fatal(err)
			}
			journal, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256)
			if err != nil || !found {
				t.Fatalf("journal found=%t err=%v", found, err)
			}
			var residue string
			switch kind {
			case "journal rewrite":
				path, _ := m.completePullJournalPath(checkpoint.SelectorSHA256)
				residue = filepath.Join(filepath.Dir(path), completePullJournalTemp(journal.WriteToken))
			case "sidecar":
				residue = filepath.Join(filepath.Dir(m.sidecarPath()), completePullSidecarTemp(journal.WriteToken))
			case "progress":
				path, _ := m.completePullProgressPath(checkpoint.SelectorSHA256)
				residue = filepath.Join(filepath.Dir(path), completePullProgressTemp(journal.WriteToken))
			}
			if err := os.WriteFile(residue, []byte("partial"), 0o600); err != nil {
				t.Fatal(err)
			}
			if kind == "journal rewrite" {
				if err := appendCompletePullJournalForTest(m, checkpoint, 1, entries[1]); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(residue); !os.IsNotExist(err) {
				t.Fatalf("owned %s residue survived: %v", kind, err)
			}
		})
	}
}

func TestCompletePullBatchRequiresJournalOnlyForDirtyState(t *testing.T) {
	m, checkpoint, _ := completePullJournalFixture(t, "10")
	clean, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	if err := clean.FlushCompletePull(checkpoint); err != nil {
		t.Fatalf("clean final flush without a journal: %v", err)
	}
	dirty, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	dirty.Record(SyncState{ID: "10", Version: 1, Hash: strings.Repeat("a", 64), Path: "DOC/page-10/page-10.csf"})
	if err := dirty.FlushCompletePull(checkpoint); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("dirty ownerless flush error=%v", err)
	}
}
