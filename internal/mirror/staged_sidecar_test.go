package mirror

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestStagedLineageMigratesConservativelyAndBindsExactNativeBytes(t *testing.T) {
	m := New(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(m.sidecarPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	oldState := `{"pages":{"P1":{"id":"P1","version":7,"hash":"remote","path":"SPACE/p.csf"}}}`
	if err := os.WriteFile(m.sidecarPath(), []byte(oldState), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := m.StagedStateOf("P1"); err != nil || ok {
		t.Fatalf("old sidecar staged state: ok=%v err=%v", ok, err)
	}
	first := []byte("<p>ATL-produced</p>\n")
	second := []byte("h1. ATL-produced\r\n")
	firstBaseHash := Hash([]byte("<p>remote base</p>"))
	secondBaseHash := Hash([]byte("h1. remote base"))
	if err := m.RecordStaged([]StagedContent{
		{ID: "P1", Path: "SPACE/p.csf", Body: first, BaseHash: firstBaseHash},
		{ID: "ISSUE-2", Path: "ISSUE/ISSUE-2.wiki", Body: second, BaseHash: secondBaseHash},
	}); err != nil {
		t.Fatal(err)
	}

	states, err := m.StagedStatesOf([]string{"ISSUE-2", "missing", "P1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("staged states = %+v", states)
	}
	if got := states["P1"]; got.ID != "P1" || got.Path != "SPACE/p.csf" || got.Hash != Hash(first) || got.BaseHash != firstBaseHash {
		t.Errorf("P1 staged binding = %+v", got)
	}
	if got := states["ISSUE-2"]; got.ID != "ISSUE-2" || got.Path != "ISSUE/ISSUE-2.wiki" || got.Hash != Hash(second) || got.BaseHash != secondBaseHash {
		t.Errorf("ISSUE-2 staged binding = %+v", got)
	}
	remote, ok, err := m.SyncStateOf("P1")
	if err != nil || !ok || remote.Version != 7 || remote.Hash != "remote" || remote.Path != "SPACE/p.csf" {
		t.Fatalf("remote state changed by staging: state=%+v ok=%v err=%v", remote, ok, err)
	}
	if _, ok := m.BaseBody("P1"); ok {
		t.Fatal("recording staged lineage created or rewrote a pristine base")
	}

	updated := []byte("<p>ATL-produced update</p>")
	updatedBaseHash := Hash([]byte("<p>new remote base</p>"))
	if err := m.RecordStaged([]StagedContent{{ID: "P1", Path: "SPACE/renamed.csf", Body: updated, BaseHash: updatedBaseHash}}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := m.StagedStateOf("P1")
	if err != nil || !ok || got.Path != "SPACE/renamed.csf" || got.Hash != Hash(updated) || got.BaseHash != updatedBaseHash {
		t.Fatalf("updated staged binding = %+v ok=%v err=%v", got, ok, err)
	}
}

func TestRecordStagedRejectsInvalidIdentityAndPathBeforeWrite(t *testing.T) {
	baseHash := Hash([]byte("base"))
	tests := []struct {
		name     string
		contents []StagedContent
	}{
		{name: "empty identity", contents: []StagedContent{{Path: "p.csf", Body: []byte("x"), BaseHash: baseHash}}},
		{name: "empty path", contents: []StagedContent{{ID: "P1", Body: []byte("x"), BaseHash: baseHash}}},
		{name: "absolute path", contents: []StagedContent{{ID: "P1", Path: "/p.csf", Body: []byte("x"), BaseHash: baseHash}}},
		{name: "traversal", contents: []StagedContent{{ID: "P1", Path: "../p.csf", Body: []byte("x"), BaseHash: baseHash}}},
		{name: "non canonical", contents: []StagedContent{{ID: "P1", Path: "SPACE/../p.csf", Body: []byte("x"), BaseHash: baseHash}}},
		{name: "backslash", contents: []StagedContent{{ID: "P1", Path: `SPACE\p.csf`, Body: []byte("x"), BaseHash: baseHash}}},
		{name: "drive path", contents: []StagedContent{{ID: "P1", Path: "C:/p.csf", Body: []byte("x"), BaseHash: baseHash}}},
		{name: "missing base hash", contents: []StagedContent{{ID: "P1", Path: "p.csf", Body: []byte("x")}}},
		{name: "invalid base hash", contents: []StagedContent{{ID: "P1", Path: "p.csf", Body: []byte("x"), BaseHash: "invalid"}}},
		{name: "duplicate identity", contents: []StagedContent{
			{ID: "P1", Path: "one.csf", Body: []byte("one"), BaseHash: baseHash},
			{ID: "P1", Path: "two.csf", Body: []byte("two"), BaseHash: baseHash},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New(t.TempDir())
			err := m.RecordStaged(tc.contents)
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("RecordStaged error = %v", err)
			}
			if _, statErr := os.Stat(m.sidecarPath()); !os.IsNotExist(statErr) {
				t.Fatalf("invalid record wrote sidecar: %v", statErr)
			}
		})
	}
}

func TestStagedLineageSemanticCorruptionFailsLoudly(t *testing.T) {
	validHash := Hash([]byte("native"))
	validBaseHash := Hash([]byte("base"))
	tests := []struct {
		name string
		json string
	}{
		{name: "identity mismatch", json: `{"pages":{},"staged":{"P1":{"id":"P2","hash":"` + validHash + `","base_hash":"` + validBaseHash + `","path":"p.csf"}}}`},
		{name: "invalid hash", json: `{"pages":{},"staged":{"P1":{"id":"P1","hash":"short","base_hash":"` + validBaseHash + `","path":"p.csf"}}}`},
		{name: "invalid base hash", json: `{"pages":{},"staged":{"P1":{"id":"P1","hash":"` + validHash + `","base_hash":"short","path":"p.csf"}}}`},
		{name: "unsafe path", json: `{"pages":{},"staged":{"P1":{"id":"P1","hash":"` + validHash + `","base_hash":"` + validBaseHash + `","path":"../p.csf"}}}`},
		{name: "private base", json: `{"pages":{},"staged":{"P1":{"id":"P1","hash":"` + validHash + `","base_hash":"` + validBaseHash + `","path":".atl/base/P1.csf"}}}`},
		{name: "private case alias", json: `{"pages":{},"staged":{"P1":{"id":"P1","hash":"` + validHash + `","base_hash":"` + validBaseHash + `","path":".ATL/base/P1.csf"}}}`},
		{name: "overlong path", json: `{"pages":{},"staged":{"P1":{"id":"P1","hash":"` + validHash + `","base_hash":"` + validBaseHash + `","path":"` + strings.Repeat("a", maxArtifactPathBytes+1) + `"}}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New(t.TempDir())
			if err := os.MkdirAll(filepath.Dir(m.sidecarPath()), 0o755); err != nil {
				t.Fatal(err)
			}
			evidence := []byte(tc.json)
			if err := os.WriteFile(m.sidecarPath(), evidence, 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := m.StagedStateOf("P1")
			if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "invalid staged lineage") || !strings.Contains(err.Error(), m.sidecarPath()) {
				t.Fatalf("semantic corruption error = %v", err)
			}
			if err := m.ClearStaged("P1"); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("mutation silently repaired corrupt state: %v", err)
			}
			got, readErr := os.ReadFile(m.sidecarPath())
			if readErr != nil || !bytes.Equal(got, evidence) {
				t.Fatalf("invalid staged evidence changed: got=%q err=%v", got, readErr)
			}
		})
	}
}

func TestStagedPatchesMergeWithStaleSyncBatchAndClearSelectively(t *testing.T) {
	m := New(t.TempDir())
	stale, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RecordStaged([]StagedContent{
		{ID: "CONF-1", Path: "SPACE/page.csf", Body: []byte("conf staged"), BaseHash: Hash([]byte("conf base"))},
		{ID: "JIRA-1", Path: "JIRA/JIRA-1.wiki", Body: []byte("jira staged"), BaseHash: Hash([]byte("jira base"))},
	}); err != nil {
		t.Fatal(err)
	}
	stale.Record(SyncState{ID: "OTHER", Version: 2, Hash: "remote", Path: "other.csf"})
	stale.RecordView("OTHER", ViewState{Sections: []string{"metadata"}})
	if err := stale.Flush(); err != nil {
		t.Fatal(err)
	}
	states, err := m.StagedStatesOf([]string{"CONF-1", "JIRA-1"})
	if err != nil || len(states) != 2 {
		t.Fatalf("stale sync batch lost staged entries: states=%+v err=%v", states, err)
	}

	// A sync batch for CONF-1 opened before a later staged update must clear the
	// latest entry by identity, not save its stale snapshot and resurrect data.
	staleClear, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RecordStaged([]StagedContent{{ID: "CONF-1", Path: "SPACE/new.csf", Body: []byte("new staged"), BaseHash: Hash([]byte("new base"))}}); err != nil {
		t.Fatal(err)
	}
	staleClear.Record(SyncState{ID: "CONF-1", Version: 3, Hash: "new remote", Path: "SPACE/new.csf"})
	if err := staleClear.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := m.StagedStateOf("CONF-1"); err != nil || ok {
		t.Fatalf("successful sync did not clear latest staged lineage: ok=%v err=%v", ok, err)
	}
	if _, ok, err := m.StagedStateOf("JIRA-1"); err != nil || !ok {
		t.Fatalf("cross-service staged lineage was lost: ok=%v err=%v", ok, err)
	}

	if err := m.ClearStaged("missing", "JIRA-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := m.StagedStateOf("JIRA-1"); err != nil || ok {
		t.Fatalf("clear did not remove selected staged lineage: ok=%v err=%v", ok, err)
	}
	if state, ok, err := m.SyncStateOf("OTHER"); err != nil || !ok || state.Version != 2 {
		t.Fatalf("clear disturbed remote state: state=%+v ok=%v err=%v", state, ok, err)
	}
	if view, ok, err := m.ViewStateOf("OTHER"); err != nil || !ok || len(view.Sections) != 1 {
		t.Fatalf("clear disturbed view state: state=%+v ok=%v err=%v", view, ok, err)
	}
}

func TestSyncBatchWriteClearsStagedLineage(t *testing.T) {
	m := New(t.TempDir())
	page := &domain.Resource{ID: "P1", Title: "Page", SpaceKey: "SPACE", Version: 4, Body: []byte("<p>remote</p>")}
	dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
	stagedPath, err := filepath.Rel(m.Root, filepath.Join(dir, slug+".csf"))
	if err != nil {
		t.Fatal(err)
	}
	stagedPath = filepath.ToSlash(stagedPath)
	if err := m.RecordStaged([]StagedContent{{ID: page.ID, Path: stagedPath, Body: []byte("<p>staged</p>"), BaseHash: Hash(page.Body)}}); err != nil {
		t.Fatal(err)
	}
	batch, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	if err := batch.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := m.StagedStateOf(page.ID); err != nil || !ok {
		t.Fatalf("staged lineage cleared before batch commit: ok=%v err=%v", ok, err)
	}
	if err := batch.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := m.StagedStateOf(page.ID); err != nil || ok {
		t.Fatalf("Write sync did not clear staged lineage: ok=%v err=%v", ok, err)
	}
}
