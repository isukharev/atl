package mirror

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func corruptSidecar(t *testing.T, m *Mirror) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(m.sidecarPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.sidecarPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCorruptSidecarFailsLoudly: a corrupt state.json must be a loud,
// actionable error on every path that consults it — never a silent reset that
// makes all pages read as never-synced with drift detection disabled.
func TestCorruptSidecarFailsLoudly(t *testing.T) {
	m := New(t.TempDir())
	page := &domain.Resource{ID: "1", Title: "Doc", SpaceKey: "S", Version: 1, Body: []byte("<p>x</p>")}
	dir, slug, err := m.ClaimPageDir(page.SpaceKey, nil, page.Title, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	corruptSidecar(t, m)

	assertLoud := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: corrupt sidecar silently ignored", name)
		}
		if !errors.Is(err, domain.ErrCheckFailed) {
			t.Errorf("%s: corruption error = %v, want ErrCheckFailed (branchable exit 8)", name, err)
		}
		if !strings.Contains(err.Error(), "corrupt mirror sidecar") || !strings.Contains(err.Error(), m.sidecarPath()) {
			t.Errorf("%s: error not actionable: %v", name, err)
		}
	}
	_, _, err = m.LoadCSF(filepath.Join(dir, slug+".csf"))
	assertLoud("LoadCSF", err)
	_, err = m.ListCSF()
	assertLoud("ListCSF", err)
	_, err = m.BeginSync()
	assertLoud("BeginSync", err)
	assertLoud("Write", m.Write(dir, slug, page, nil))
	_, err = m.SyncedVersion(page.ID)
	assertLoud("SyncedVersion", err)
}

func TestSidecarAndBaseReadsRefuseDescendantSymlinks(t *testing.T) {
	t.Run("sidecar directory", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "state.json"), []byte(`{"pages":{}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, ".atl")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := New(root).SyncedVersion("1"); err == nil {
			t.Fatal("sidecar read followed a descendant symlink")
		}
	})

	t.Run("base file", func(t *testing.T) {
		root := t.TempDir()
		baseDir := filepath.Join(root, ".atl", "base")
		if err := os.MkdirAll(baseDir, 0o755); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.csf")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(baseDir, "1.csf")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, ok := New(root).BaseBody("1"); ok {
			t.Fatal("base read followed a final-component symlink")
		}
	})
}

// A symlink planted at state.json is rejected during the mandatory pre-write
// state read, so outside state can neither be imported nor overwritten.
func TestSidecarSaveRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	m := New(t.TempDir())
	victim := filepath.Join(t.TempDir(), "victim")
	// Valid JSON ensures the refusal is specifically the path boundary.
	original := []byte("{\n  \"pages\": {}\n}\n")
	if err := os.WriteFile(victim, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(m.sidecarPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, m.sidecarPath()); err != nil {
		t.Fatal(err)
	}
	page := &domain.Resource{ID: "1", Title: "Doc", SpaceKey: "S", Version: 1, Body: []byte("<p>x</p>")}
	dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
	if err := m.Write(dir, slug, page, nil); err == nil {
		t.Fatal("Write accepted a symlinked sidecar")
	}
	if got, _ := os.ReadFile(victim); !bytes.Equal(got, original) {
		t.Fatalf("save wrote through the symlink into its target: %q", got)
	}
	fi, err := os.Lstat(m.sidecarPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("refused operation unexpectedly replaced the sidecar symlink")
	}
}

// TestSyncBatchFlushOnce: a batch records N pages in memory, preserves
// pre-existing entries, and persists everything in a single Flush; Flush after
// success is a no-op.
func TestSyncBatchFlushOnce(t *testing.T) {
	m := New(t.TempDir())
	// Pre-existing entry from an earlier single-page write.
	old := &domain.Resource{ID: "old", Title: "Old", SpaceKey: "S", Version: 9, Body: []byte("<p>old</p>")}
	dirOld, slugOld := m.PageDir(old.SpaceKey, nil, old.Title)
	if err := m.Write(dirOld, slugOld, old, nil); err != nil {
		t.Fatal(err)
	}

	b, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	pages := []*domain.Resource{
		{ID: "1", Title: "One", SpaceKey: "S", Version: 1, Body: []byte("<p>1</p>")},
		{ID: "2", Title: "Two", SpaceKey: "S", Version: 2, Body: []byte("<p>2</p>")},
		{ID: "3", Title: "Three", SpaceKey: "S", Version: 3, Body: []byte("<p>3</p>")},
	}
	for _, p := range pages {
		dir, slug, err := m.ClaimPageDir(p.SpaceKey, nil, p.Title, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Write(dir, slug, p, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Nothing hits the sidecar until Flush.
	if sc, err := m.loadSidecar(); err != nil || len(sc.Pages) != 1 {
		t.Fatalf("sidecar written before Flush: %+v (err %v)", sc.Pages, err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	sc, err := m.loadSidecar()
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Pages) != 4 {
		t.Fatalf("sidecar has %d entries, want 4 (3 batched + 1 pre-existing): %+v", len(sc.Pages), sc.Pages)
	}
	if sc.Pages["old"].Version != 9 {
		t.Errorf("pre-existing entry lost: %+v", sc.Pages["old"])
	}
	for _, p := range pages {
		st := sc.Pages[p.ID]
		if st.Version != p.Version || st.Hash != Hash(p.Body) {
			t.Errorf("entry %s = %+v", p.ID, st)
		}
	}
	// Flush after success must be a cheap no-op (safe on deferred paths).
	before, _ := os.Stat(m.sidecarPath())
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(m.sidecarPath())
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("no-op Flush rewrote the sidecar")
	}
}

// TestViewStateRoundTrip: RecordView through a batch persists, ViewStateOf reads
// it back, and unrecorded ids report ok=false.
func TestViewStateRoundTrip(t *testing.T) {
	m := New(t.TempDir())
	b, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	b.RecordView("P1", ViewState{
		Sections: []string{"comments", "frontmatter"}, CustomFields: []string{"customfield_1"},
		FieldViews: []FieldViewState{{ID: "customfield_2", Label: "Score", Placement: "section", Format: "jira_wiki", Editable: true}},
		PageFields: []FieldViewState{{ID: "updated", Label: "Changed", Placement: "metadata", Format: "date"}},
		EpicField:  "customfield_3",
	})
	// Nothing hits the sidecar until Flush.
	if _, ok, err := m.ViewStateOf("P1"); err != nil || ok {
		t.Fatalf("view visible before Flush (ok=%v err=%v)", ok, err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	vs, ok, err := m.ViewStateOf("P1")
	if err != nil || !ok {
		t.Fatalf("ViewStateOf after flush: ok=%v err=%v", ok, err)
	}
	if len(vs.Sections) != 2 || vs.Sections[0] != "comments" || len(vs.CustomFields) != 1 {
		t.Errorf("round-tripped view state wrong: %+v", vs)
	}
	if len(vs.FieldViews) != 1 || vs.FieldViews[0].Label != "Score" || !vs.FieldViews[0].Editable || vs.EpicField != "customfield_3" {
		t.Errorf("round-tripped extended view state wrong: %+v", vs)
	}
	if len(vs.PageFields) != 1 || vs.PageFields[0].ID != "updated" || vs.PageFields[0].Format != "date" {
		t.Errorf("round-tripped Confluence page fields wrong: %+v", vs)
	}
	if _, ok, err := m.ViewStateOf("missing"); err != nil || ok {
		t.Errorf("unrecorded id should report ok=false (ok=%v err=%v)", ok, err)
	}
}

// TestSaveViewStatesMerge: SaveViewStates merges into existing views without
// disturbing the pages map or other view entries.
func TestSaveViewStatesMerge(t *testing.T) {
	m := New(t.TempDir())
	// Seed a page sync entry so we can prove SaveViewStates leaves it intact.
	p := &domain.Resource{ID: "1", Title: "One", SpaceKey: "S", Version: 1, Body: []byte("<p>1</p>")}
	dir, slug := m.PageDir(p.SpaceKey, nil, p.Title)
	if err := m.Write(dir, slug, p, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveViewStates(map[string]ViewState{"1": {Sections: []string{"frontmatter"}}}); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveViewStates(map[string]ViewState{"2": {Sections: []string{"comments"}}}); err != nil {
		t.Fatal(err)
	}
	sc, err := m.loadSidecar()
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Pages) != 1 || sc.Pages["1"].Version != 1 {
		t.Errorf("pages map disturbed by SaveViewStates: %+v", sc.Pages)
	}
	if len(sc.Views) != 2 || sc.Views["1"].Sections[0] != "frontmatter" || sc.Views["2"].Sections[0] != "comments" {
		t.Errorf("views not merged: %+v", sc.Views)
	}
	// Empty map is a no-op.
	if err := m.SaveViewStates(nil); err != nil {
		t.Errorf("nil SaveViewStates: %v", err)
	}
}

// TestViewStateNilMapOnOldSidecar: a state.json written before the views map
// existed loads without panicking and reports no recorded views.
func TestViewStateNilMapOnOldSidecar(t *testing.T) {
	m := New(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(m.sidecarPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	// A pre-upgrade sidecar: pages only, no "views" key.
	if err := os.WriteFile(m.sidecarPath(), []byte(`{"pages":{"P1":{"id":"P1","version":2,"hash":"h","path":"p.csf"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := m.ViewStateOf("P1"); err != nil || ok {
		t.Fatalf("old sidecar: ok=%v err=%v", ok, err)
	}
	// Recording into it must succeed (the nil map is initialized on load).
	if err := m.SaveViewStates(map[string]ViewState{"P1": {Sections: []string{"comments"}}}); err != nil {
		t.Fatal(err)
	}
	if vs, ok, _ := m.ViewStateOf("P1"); !ok || len(vs.Sections) != 1 {
		t.Errorf("view not recorded onto old sidecar: %+v ok=%v", vs, ok)
	}
}

func TestDisjointBatchesMergeAgainstLatestSharedSidecar(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	first, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	first.Record(SyncState{ID: "CONF-1", Version: 2, Hash: "conf", Path: "page.csf"})
	first.RecordView("CONF-1", ViewState{Sections: []string{"page_fields"}})
	second.Record(SyncState{ID: "JIRA-1", Version: 0, Hash: "jira", Path: "JIRA-1.wiki"})
	second.RecordView("JIRA-1", ViewState{Sections: []string{"metadata"}})
	if err := first.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := second.Flush(); err != nil {
		t.Fatal(err)
	}
	sc, err := m.loadSidecar()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"CONF-1", "JIRA-1"} {
		if _, ok := sc.Pages[id]; !ok {
			t.Fatalf("page state %s was lost: %+v", id, sc.Pages)
		}
		if _, ok := sc.Views[id]; !ok {
			t.Fatalf("view state %s was lost: %+v", id, sc.Views)
		}
	}
}

func TestSaveViewStatesMergesWithAlreadyOpenBatch(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	batch, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	batch.Record(SyncState{ID: "PAGE", Version: 3, Hash: "body", Path: "page.csf"})
	if err := m.SaveViewStates(map[string]ViewState{"ISSUE": {Sections: []string{"metadata"}}}); err != nil {
		t.Fatal(err)
	}
	if err := batch.Flush(); err != nil {
		t.Fatal(err)
	}
	sc, err := m.loadSidecar()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sc.Pages["PAGE"]; !ok {
		t.Fatalf("page patch missing: %+v", sc.Pages)
	}
	if _, ok := sc.Views["ISSUE"]; !ok {
		t.Fatalf("view patch lost: %+v", sc.Views)
	}
}
