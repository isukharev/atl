package mirror

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestCorpusSnapshotCapturesConfluencePristineEvidenceOnly(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	bindCorpusSnapshotTestBackend(t, m, CorpusSnapshotConfluence)
	state := seedCorpusConfluence(t, m, "10001", "SPACE/page/page.csf", []byte("<p>pristine</p>"))
	writeCorpusTestFile(t, root, state.Path, []byte("<p>ambient edit</p>"))
	writeCorpusTestFile(t, root, "SPACE/page/page.md", []byte("ambient Markdown edit"))
	commentsPath := "SPACE/page/page.comments.json"
	writeCorpusTestFile(t, root, commentsPath, []byte("{\"comments\":[]}\n"))

	snapshot, err := m.BeginCorpusSnapshot(CorpusSnapshotConfluence, CorpusSnapshotOptions{})
	if err != nil {
		t.Fatalf("BeginCorpusSnapshot: %v", err)
	}
	if snapshot.Service() != CorpusSnapshotConfluence || snapshot.OriginSHA256() == "" ||
		!snapshot.Reconciled() || len(snapshot.Fingerprint()) != 64 {
		t.Fatalf("snapshot summary = service %q origin %q reconciled %t fingerprint %q",
			snapshot.Service(), snapshot.OriginSHA256(), snapshot.Reconciled(), snapshot.Fingerprint())
	}
	inventory := snapshot.Inventory()
	if len(inventory) != 1 || inventory[0].Native.Data != nil || inventory[0].Metadata.Data != nil {
		t.Fatalf("snapshot inventory retained content: %#v", inventory)
	}
	item, err := snapshot.ReadItem(0)
	if err != nil {
		t.Fatal(err)
	}
	if item.StateID != "10001" || item.ProviderID != "10001" ||
		string(item.Native.Data) != "<p>pristine</p>" || item.Native.Path != state.Path {
		t.Fatalf("captured item = %#v", item)
	}
	if len(item.Auxiliaries) != 1 || item.Auxiliaries[0].Path != commentsPath {
		t.Fatalf("captured auxiliaries = %#v", item.Auxiliaries)
	}
	if _, err := snapshot.ReadItem(-1); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("negative item index error = %v", err)
	}
	if _, err := snapshot.ReadItem(snapshot.Len()); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("past-end item index error = %v", err)
	}
	item.Native.Data[0] = 'X'
	item.Auxiliaries[0].Data[0] = 'X'
	again, err := snapshot.ReadItem(0)
	if err != nil || string(again.Native.Data) != "<p>pristine</p>" || string(again.Auxiliaries[0].Data) != "{\"comments\":[]}\n" {
		t.Fatalf("ReadItem did not return independent verified bytes: %#v, %v", again, err)
	}

	// Working native and derived Markdown are deliberately outside the source
	// set, so further ambient edits do not invalidate or alter the snapshot.
	writeCorpusTestFile(t, root, state.Path, []byte("<p>another ambient edit</p>"))
	writeCorpusTestFile(t, root, "SPACE/page/page.md", []byte("another Markdown edit"))
	if err := snapshot.Revalidate(); err != nil {
		t.Fatalf("ambient working edit invalidated pristine snapshot: %v", err)
	}

	writeCorpusTestFile(t, root, commentsPath, []byte("{\"comments\":[{}]}\n"))
	if _, err := snapshot.ReadItem(0); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("ReadItem after auxiliary mutation = %v", err)
	}
	if err := snapshot.Revalidate(); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("auxiliary mutation error = %v", err)
	}
}

func TestCorpusSnapshotCapturesJiraNumericIdentityAndIgnoresPendingPayload(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	bindCorpusSnapshotTestBackend(t, m, CorpusSnapshotJira)
	state := seedCorpusJira(t, m, "EX-1", "10001", "EX/EX-1.wiki", []byte("h1. pristine"))
	writeCorpusTestFile(t, root, state.Path, []byte("h1. ambient edit"))
	writeCorpusTestFile(t, root, "EX/EX-1.md", []byte("ambient Markdown"))
	writeCorpusTestFile(t, root, "EX/EX-1.epic-children.json", []byte("{\"issues\":[]}\n"))
	// Export must not decode or adopt pending Jira proposals.
	writeCorpusTestFile(t, root, ".atl/pending/jira/EX-1.json", []byte("opaque pending bytes are not JSON"))

	snapshot, err := m.BeginCorpusSnapshot(CorpusSnapshotJira, CorpusSnapshotOptions{})
	if err != nil {
		t.Fatalf("BeginCorpusSnapshot: %v", err)
	}
	item, err := snapshot.ReadItem(0)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Len() != 1 || item.StateID != "EX-1" || item.ProviderID != "10001" ||
		item.Version != 0 || string(item.Native.Data) != "h1. pristine" {
		t.Fatalf("captured Jira item = %#v", item)
	}
	if len(item.Auxiliaries) != 1 || !strings.HasSuffix(item.Auxiliaries[0].Path, ".epic-children.json") {
		t.Fatalf("captured Jira auxiliaries = %#v", item.Auxiliaries)
	}

	otherRoot := t.TempDir()
	other := New(otherRoot)
	bindCorpusSnapshotTestBackend(t, other, CorpusSnapshotJira)
	seedCorpusJira(t, other, "RENAMED-9", "10001", "RENAMED/RENAMED-9.wiki", []byte("h1. pristine"))
	renamed, err := other.BeginCorpusSnapshot(CorpusSnapshotJira, CorpusSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	renamedItem, err := renamed.ReadItem(0)
	if err != nil {
		t.Fatal(err)
	}
	if renamedItem.ProviderID != item.ProviderID || renamedItem.StateID == item.StateID {
		t.Fatalf("rename identity evidence = before %#v after %#v", item, renamedItem)
	}
}

func TestCorpusSnapshotRefusesStagedLineageByDefault(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	bindCorpusSnapshotTestBackend(t, m, CorpusSnapshotJira)
	state := seedCorpusJira(t, m, "EX-1", "10001", "EX/EX-1.wiki", []byte("base"))
	if err := m.RecordStaged([]StagedContent{{ID: state.ID, Path: state.Path, Body: []byte("staged"), BaseHash: state.Hash}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginCorpusSnapshot(CorpusSnapshotJira, CorpusSnapshotOptions{}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("default staged-lineage error = %v", err)
	}
	snapshot, err := m.BeginCorpusSnapshot(CorpusSnapshotJira, CorpusSnapshotOptions{AllowUnreconciled: true})
	if err != nil {
		t.Fatalf("diagnostic snapshot: %v", err)
	}
	item, err := snapshot.ReadItem(0)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Reconciled() || string(item.Native.Data) != "base" {
		t.Fatalf("diagnostic snapshot adopted staged bytes: reconciled=%t item=%#v", snapshot.Reconciled(), item)
	}
}

func TestCorpusSnapshotRejectsCorruptOrMisboundEvidence(t *testing.T) {
	t.Run("missing binding", func(t *testing.T) {
		_, err := New(t.TempDir()).BeginCorpusSnapshot(CorpusSnapshotJira, CorpusSnapshotOptions{})
		assertCorpusSnapshotRejected(t, err)
	})
	t.Run("missing base", func(t *testing.T) {
		root := t.TempDir()
		m := New(root)
		bindCorpusSnapshotTestBackend(t, m, CorpusSnapshotJira)
		state := seedCorpusJira(t, m, "EX-1", "10001", "EX/EX-1.wiki", []byte("base"))
		if err := os.Remove(filepath.Join(root, ".atl", "base", state.ID+".wiki")); err != nil {
			t.Fatal(err)
		}
		_, err := m.BeginCorpusSnapshot(CorpusSnapshotJira, CorpusSnapshotOptions{})
		assertCorpusSnapshotRejected(t, err)
	})
	t.Run("baseline mismatch", func(t *testing.T) {
		root := t.TempDir()
		m := New(root)
		bindCorpusSnapshotTestBackend(t, m, CorpusSnapshotConfluence)
		state := seedCorpusConfluence(t, m, "10001", "SPACE/page/page.csf", []byte("base"))
		if err := m.SaveBaseExt(state.ID, []byte("changed"), ".csf"); err != nil {
			t.Fatal(err)
		}
		_, err := m.BeginCorpusSnapshot(CorpusSnapshotConfluence, CorpusSnapshotOptions{})
		assertCorpusSnapshotRejected(t, err)
	})
	t.Run("Jira nonnumeric provider id", func(t *testing.T) {
		root := t.TempDir()
		m := New(root)
		bindCorpusSnapshotTestBackend(t, m, CorpusSnapshotJira)
		state := seedCorpusJira(t, m, "EX-1", "10001", "EX/EX-1.wiki", []byte("base"))
		state.Identity = ""
		writeCorpusState(t, m, state)
		writeCorpusJSON(t, root, "EX/EX-1.json", map[string]any{
			"key": "EX-1", "id": "not-numeric", "fields": map[string]any{"summary": "Synthetic issue"},
		})
		_, err := m.BeginCorpusSnapshot(CorpusSnapshotJira, CorpusSnapshotOptions{})
		assertCorpusSnapshotRejected(t, err)
	})
	t.Run("Jira sidecar identity mismatch", func(t *testing.T) {
		root := t.TempDir()
		m := New(root)
		bindCorpusSnapshotTestBackend(t, m, CorpusSnapshotJira)
		state := seedCorpusJira(t, m, "EX-1", "10001", "EX/EX-1.wiki", []byte("base"))
		state.Identity = "10002"
		writeCorpusState(t, m, state)
		_, err := m.BeginCorpusSnapshot(CorpusSnapshotJira, CorpusSnapshotOptions{})
		assertCorpusSnapshotRejected(t, err)
	})
	t.Run("legacy Jira sidecar without identity", func(t *testing.T) {
		root := t.TempDir()
		m := New(root)
		bindCorpusSnapshotTestBackend(t, m, CorpusSnapshotJira)
		state := seedCorpusJira(t, m, "EX-1", "10001", "EX/EX-1.wiki", []byte("base"))
		state.Identity = ""
		writeCorpusState(t, m, state)
		snapshot, err := m.BeginCorpusSnapshot(CorpusSnapshotJira, CorpusSnapshotOptions{})
		if err != nil {
			t.Fatalf("BeginCorpusSnapshot legacy Jira sidecar: %v", err)
		}
		item, err := snapshot.ReadItem(0)
		if err != nil || item.ProviderID != "10001" {
			t.Fatalf("legacy Jira snapshot item = %#v, %v", item, err)
		}
	})
	t.Run("Jira path misbound", func(t *testing.T) {
		root := t.TempDir()
		m := New(root)
		bindCorpusSnapshotTestBackend(t, m, CorpusSnapshotJira)
		seedCorpusJira(t, m, "EX-1", "10001", "EX/OTHER-2.wiki", []byte("base"))
		_, err := m.BeginCorpusSnapshot(CorpusSnapshotJira, CorpusSnapshotOptions{})
		assertCorpusSnapshotRejected(t, err)
	})
	t.Run("Confluence metadata mismatch", func(t *testing.T) {
		root := t.TempDir()
		m := New(root)
		bindCorpusSnapshotTestBackend(t, m, CorpusSnapshotConfluence)
		seedCorpusConfluence(t, m, "10001", "SPACE/page/page.csf", []byte("base"))
		metadata := Meta{ID: "10002", Title: "Synthetic", Space: "SPACE", Version: 1, Hash: Hash([]byte("base"))}
		writeCorpusJSON(t, root, "SPACE/page/page.meta.json", metadata)
		_, err := m.BeginCorpusSnapshot(CorpusSnapshotConfluence, CorpusSnapshotOptions{})
		assertCorpusSnapshotRejected(t, err)
	})
	t.Run("duplicate sidecar key", func(t *testing.T) {
		root := t.TempDir()
		m := New(root)
		bindCorpusSnapshotTestBackend(t, m, CorpusSnapshotJira)
		writeCorpusTestFile(t, root, ".atl/state.json", []byte(`{"pages":{"EX-1":{"id":"EX-1","version":0,"hash":"`+strings.Repeat("a", 64)+`","path":"EX/EX-1.wiki"},"EX-1":{"id":"EX-1","version":0,"hash":"`+strings.Repeat("a", 64)+`","path":"EX/EX-1.wiki"}}}`))
		_, err := m.BeginCorpusSnapshot(CorpusSnapshotJira, CorpusSnapshotOptions{})
		assertCorpusSnapshotRejected(t, err)
	})
	t.Run("duplicate tracked path", func(t *testing.T) {
		root := t.TempDir()
		m := New(root)
		bindCorpusSnapshotTestBackend(t, m, CorpusSnapshotJira)
		hash := strings.Repeat("a", 64)
		writeCorpusTestFile(t, root, ".atl/state.json", []byte(`{"pages":{"EX-1":{"id":"EX-1","version":0,"hash":"`+hash+`","path":"EX/shared.wiki"},"EX-2":{"id":"EX-2","version":0,"hash":"`+hash+`","path":"EX/shared.wiki"}}}`))
		_, err := m.BeginCorpusSnapshot(CorpusSnapshotJira, CorpusSnapshotOptions{})
		assertCorpusSnapshotRejected(t, err)
	})
	t.Run("unsupported service", func(t *testing.T) {
		_, err := New(t.TempDir()).BeginCorpusSnapshot("aggregate", CorpusSnapshotOptions{})
		assertCorpusSnapshotRejected(t, err)
	})
	t.Run("invalid bounds", func(t *testing.T) {
		_, err := New(t.TempDir()).BeginCorpusSnapshot(CorpusSnapshotJira, CorpusSnapshotOptions{Limits: CorpusSnapshotLimits{MaxItems: -1}})
		assertCorpusSnapshotRejected(t, err)
	})
}

func TestCorpusSnapshotRevalidateDetectsPristineAndMetadataChanges(t *testing.T) {
	for _, target := range []string{"base", "metadata", "state"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			m := New(root)
			bindCorpusSnapshotTestBackend(t, m, CorpusSnapshotJira)
			state := seedCorpusJira(t, m, "EX-1", "10001", "EX/EX-1.wiki", []byte("base"))
			snapshot, err := m.BeginCorpusSnapshot(CorpusSnapshotJira, CorpusSnapshotOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch target {
			case "base":
				if err := m.SaveBaseExt(state.ID, []byte("changed"), ".wiki"); err != nil {
					t.Fatal(err)
				}
			case "metadata":
				writeCorpusJSON(t, root, "EX/EX-1.json", map[string]any{"key": "EX-1", "id": "10001", "fields": map[string]any{"summary": "changed"}})
			case "state":
				batch, err := m.BeginSync()
				if err != nil {
					t.Fatal(err)
				}
				batch.Record(SyncState{ID: state.ID, Version: 0, Hash: state.Hash, Path: "MOVED/EX-1.wiki"})
				if err := batch.Flush(); err != nil {
					t.Fatal(err)
				}
			}
			if err := snapshot.Revalidate(); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("Revalidate after %s mutation = %v", target, err)
			}
		})
	}
}

func seedCorpusConfluence(t *testing.T, m *Mirror, id, path string, body []byte) SyncState {
	t.Helper()
	state := SyncState{ID: id, Version: 1, Hash: Hash(body), Path: path}
	if err := m.SaveBaseExt(id, body, ".csf"); err != nil {
		t.Fatal(err)
	}
	metadata := Meta{ID: id, Title: "Synthetic page", Space: "SPACE", Version: state.Version, Hash: state.Hash}
	writeCorpusJSON(t, m.Root, strings.TrimSuffix(path, ".csf")+".meta.json", metadata)
	writeCorpusState(t, m, state)
	return state
}

func seedCorpusJira(t *testing.T, m *Mirror, key, providerID, path string, body []byte) SyncState {
	t.Helper()
	state := SyncState{ID: key, Identity: providerID, Version: 0, Hash: Hash(body), Path: path}
	if err := m.SaveBaseExt(key, body, ".wiki"); err != nil {
		t.Fatal(err)
	}
	writeCorpusJSON(t, m.Root, strings.TrimSuffix(path, ".wiki")+".json", map[string]any{
		"key": key, "id": providerID, "fields": map[string]any{"summary": "Synthetic issue"},
	})
	writeCorpusState(t, m, state)
	return state
}

func writeCorpusState(t *testing.T, m *Mirror, state SyncState) {
	t.Helper()
	batch, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	batch.Record(state)
	if err := batch.Flush(); err != nil {
		t.Fatal(err)
	}
}

func bindCorpusSnapshotTestBackend(t *testing.T, m *Mirror, service string) {
	t.Helper()
	if created, err := m.BindBackend(testBackendBinding(t, service, "https://backend.example.test")); err != nil || !created {
		t.Fatalf("BindBackend = %t, %v", created, err)
	}
}

func writeCorpusJSON(t *testing.T, root, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeCorpusTestFile(t, root, path, append(data, '\n'))
}

func writeCorpusTestFile(t *testing.T, root, path string, data []byte) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertCorpusSnapshotRejected(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v, want ErrCheckFailed", err)
	}
}
