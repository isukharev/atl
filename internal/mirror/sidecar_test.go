package mirror

import (
	"bytes"
	"encoding/json"
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

// TestSidecarSaveIsAtomicReplace pins the control that makes the sidecar
// crash-consistent: the save must replace the file's inode (temp + rename),
// not truncate-and-write in place — a symlink planted at state.json is
// replaced by a regular file and its target is never written through.
func TestSidecarSaveIsAtomicReplace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	m := New(t.TempDir())
	victim := filepath.Join(t.TempDir(), "victim")
	// Valid (empty) sidecar JSON: the pre-save load reads through the symlink,
	// and the point under test is the WRITE path, not load-time corruption.
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
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatalf("Write through planted symlink should succeed by replacing it: %v", err)
	}
	if got, _ := os.ReadFile(victim); !bytes.Equal(got, original) {
		t.Fatalf("save wrote through the symlink into its target: %q", got)
	}
	fi, err := os.Lstat(m.sidecarPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("state.json is still a symlink after save")
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("state.json mode = %v, want 0600", fi.Mode().Perm())
	}
	var sc sidecarFile
	b, _ := os.ReadFile(m.sidecarPath())
	if err := json.Unmarshal(b, &sc); err != nil {
		t.Fatalf("saved sidecar unparseable: %v", err)
	}
	if sc.Pages["1"].Version != 1 {
		t.Errorf("saved state = %+v", sc.Pages)
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
