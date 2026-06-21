package mirror

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// TestEnsureScaffold creates the mirror root and a .gitignore guarding secrets;
// running it twice is idempotent and never clobbers a pre-existing .gitignore.
func TestEnsureScaffold(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mirror")
	m := New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatalf("EnsureScaffold: %v", err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		t.Fatalf("root not created as dir: err=%v", err)
	}
	gi := filepath.Join(root, ".gitignore")
	b, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	for _, want := range []string{".atl/", "credentials.json", "*.pat"} {
		if !strings.Contains(string(b), want) {
			t.Errorf(".gitignore missing %q, got:\n%s", want, b)
		}
	}
	// Second call must not overwrite an existing (user-edited) .gitignore.
	custom := []byte("# my custom ignore\n")
	if err := os.WriteFile(gi, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureScaffold(); err != nil {
		t.Fatalf("EnsureScaffold (2nd): %v", err)
	}
	got, _ := os.ReadFile(gi)
	if !bytes.Equal(got, custom) {
		t.Errorf("EnsureScaffold overwrote existing .gitignore: got %q", got)
	}
}

// TestWriteVerbatimAndState locks the core on-disk contract: the .csf bytes are
// written byte-identical to the input body, the sidecar/meta record the version
// and hash, and the pristine base copy is stored for consequence diffs.
func TestWriteVerbatimAndState(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	// Body carries entities/CDATA/whitespace that a lossy round-trip would mangle.
	body := []byte("<p>verbatim &amp; <strong>bold</strong></p>\n<!-- keep -->\n  ")
	page := &domain.Resource{
		ID: "100", Title: "My Page", SpaceKey: "ARCH", Version: 4,
		Body: body, Parent: "99", Labels: []string{"x", "y"},
	}
	dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}

	csfPath := filepath.Join(dir, slug+".csf")
	gotCSF, err := os.ReadFile(csfPath)
	if err != nil {
		t.Fatalf("read csf: %v", err)
	}
	if !bytes.Equal(gotCSF, body) {
		t.Fatalf("csf NOT verbatim:\n got %q\nwant %q", gotCSF, body)
	}

	// Sidecar state reflects this push.
	if v := m.SyncedVersion("100"); v != 4 {
		t.Errorf("SyncedVersion = %d, want 4", v)
	}
	sc, _ := m.loadSidecar()
	st := sc.Pages["100"]
	if st.Hash != Hash(body) {
		t.Errorf("sidecar hash = %q, want %q", st.Hash, Hash(body))
	}
	wantRel, _ := filepath.Rel(root, csfPath)
	if st.Path != wantRel {
		t.Errorf("sidecar path = %q, want %q", st.Path, wantRel)
	}

	// Pristine base copy stored and byte-identical.
	base, ok := m.BaseBody("100")
	if !ok {
		t.Fatal("base body not saved")
	}
	if !bytes.Equal(base, body) {
		t.Errorf("base body not verbatim: got %q", base)
	}

	// Meta sidecar JSON records version/parent/labels.
	lc, _, err := m.LoadCSF(csfPath)
	if err != nil {
		t.Fatalf("LoadCSF: %v", err)
	}
	if lc.Dirty {
		t.Error("freshly written page reported dirty")
	}
	if lc.Meta.Version != 4 || lc.Meta.Parent != "99" {
		t.Errorf("meta = %+v", lc.Meta)
	}
	if len(lc.Meta.Labels) != 2 {
		t.Errorf("meta labels = %v", lc.Meta.Labels)
	}
}

// TestWriteDirtyContract exercises the dirty-detection contract across an
// unchanged re-Write (clean) and a local on-disk edit (dirty).
func TestWriteDirtyContract(t *testing.T) {
	m := New(t.TempDir())
	page := &domain.Resource{ID: "7", Title: "Doc", SpaceKey: "S", Version: 1, Body: []byte("<p>v1</p>")}
	dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	csfPath := filepath.Join(dir, slug+".csf")

	// Re-writing the same synced version with the same body stays clean.
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	lc, _, err := m.LoadCSF(csfPath)
	if err != nil {
		t.Fatal(err)
	}
	if lc.Dirty {
		t.Error("unchanged re-write should be clean")
	}

	// A local edit to the .csf (without recording a new sync) makes it dirty.
	if err := os.WriteFile(csfPath, []byte("<p>edited locally</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	lc, _, err = m.LoadCSF(csfPath)
	if err != nil {
		t.Fatal(err)
	}
	if !lc.Dirty {
		t.Error("locally edited page should be dirty")
	}
	if lc.Synced == nil || lc.Synced.Hash == lc.Current {
		t.Errorf("expected synced hash to differ from current; synced=%+v current=%s", lc.Synced, lc.Current)
	}
}

// TestAssetSinkWritesAndContains verifies AssetSink writes the asset bytes under
// <dir>/<slug>.assets/, returns a page-relative path, and never lets a hostile
// backend filename escape the assets directory.
func TestAssetSinkWritesAndContains(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	dir := filepath.Join(root, "S", "page")
	slug := "page"
	sink := m.AssetSink(dir, slug)

	data := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x00}
	rel, err := sink.Put("image.png", data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if rel != "page.assets/image.png" {
		t.Errorf("rel = %q, want page.assets/image.png", rel)
	}
	onDisk := filepath.Join(dir, rel)
	got, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("read asset: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("asset bytes not verbatim: got %v", got)
	}

	assetsDir := filepath.Join(dir, slug+".assets")

	// A traversal filename is reduced to its base name and stays inside .assets.
	rel2, err := sink.Put("../../etc/evil.txt", []byte("x"))
	if err != nil {
		t.Fatalf("Put traversal: %v", err)
	}
	abs2, err := filepath.Abs(filepath.Join(dir, rel2))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(abs2, assetsDir+string(filepath.Separator)) {
		t.Errorf("traversal asset escaped: %q not under %q", abs2, assetsDir)
	}
	// Nothing was written outside the mirror root.
	escaped := filepath.Clean(filepath.Join(dir, "..", "..", "etc", "evil.txt"))
	if _, err := os.Stat(escaped); err == nil {
		t.Fatalf("traversal asset wrote outside root: %s", escaped)
	}

	// A name with no usable basename is refused.
	if _, err := sink.Put("..", []byte("x")); err == nil {
		t.Error("expected error for unsafe asset name \"..\"")
	}
}

// TestListCSF enumerates every tracked .csf under a populated mirror and ignores
// non-csf files and the .atl sidecar dir.
func TestListCSF(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	pages := []*domain.Resource{
		{ID: "1", Title: "Alpha", SpaceKey: "S", Version: 1, Body: []byte("<p>a</p>")},
		{ID: "2", Title: "Beta", SpaceKey: "S", Version: 1, Body: []byte("<p>b</p>")},
		{ID: "3", Title: "Gamma", SpaceKey: "T", Version: 1, Body: []byte("<p>c</p>")},
	}
	for _, p := range pages {
		dir, slug := m.PageDir(p.SpaceKey, nil, p.Title)
		if err := m.Write(dir, slug, p, nil); err != nil {
			t.Fatal(err)
		}
	}
	// A stray non-csf file must be ignored.
	if err := os.WriteFile(filepath.Join(root, "S", "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := m.ListCSF()
	if err != nil {
		t.Fatalf("ListCSF: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("ListCSF found %d entries, want 3", len(out))
	}
	for _, lc := range out {
		if !strings.HasSuffix(lc.Path, ".csf") {
			t.Errorf("non-csf path in result: %q", lc.Path)
		}
		if strings.Contains(lc.Path, string(filepath.Separator)+".atl"+string(filepath.Separator)) {
			t.Errorf("ListCSF walked into .atl: %q", lc.Path)
		}
		if lc.Dirty {
			t.Errorf("freshly written %q reported dirty", lc.Path)
		}
	}
	// Results are sorted by path (deterministic ordering).
	for i := 1; i < len(out); i++ {
		if out[i-1].Path > out[i].Path {
			t.Errorf("ListCSF not sorted: %q > %q", out[i-1].Path, out[i].Path)
		}
	}
}
