package mirror

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Hello World":            "hello-world",
		"  trim  me  ":           "trim-me",
		"ADR: контест / gateway": "adr-контест-gateway",
		"":                       "untitled",
		"!!!":                    "untitled",
		"a___b...c":              "a-b-c",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugifyRuneSafeTruncation(t *testing.T) {
	long := strings.Repeat("я", 200) // 2-byte runes
	got := slugify(long)
	if !utf8.ValidString(got) {
		t.Fatalf("slug not valid UTF-8: %q", got)
	}
	if r := []rune(got); len(r) > 80 {
		t.Fatalf("slug too long: %d runes", len(r))
	}
}

func TestHashDeterministicAndDistinct(t *testing.T) {
	a := Hash([]byte("<p>x</p>"))
	b := Hash([]byte("<p>x</p>"))
	c := Hash([]byte("<p>y</p>"))
	if a != b {
		t.Error("hash not deterministic")
	}
	if a == c {
		t.Error("different content hashed equal")
	}
}

func TestPageDirHierarchy(t *testing.T) {
	m := New("/root")
	dir, slug := m.PageDir("ARCH", []string{"Home", "Sub Page"}, "My Doc")
	if slug != "my-doc" {
		t.Errorf("slug = %q", slug)
	}
	want := "/root/ARCH/home/sub-page/my-doc"
	if dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
}

func TestSidecarRoundTrip(t *testing.T) {
	m := New(t.TempDir())
	b, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	b.sc.Pages["123"] = SyncState{ID: "123", Version: 7, Hash: "deadbeef", Path: "ARCH/x/x.csf"}
	b.dirty = true
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	if v, err := m.SyncedVersion("123"); err != nil || v != 7 {
		t.Errorf("synced version = %d (err %v), want 7", v, err)
	}
	sc, _ := m.loadSidecar()
	if sc.Pages["123"].Hash != "deadbeef" {
		t.Errorf("hash = %q", sc.Pages["123"].Hash)
	}
}

func TestWriteAndLoadCSF_DirtyDetection(t *testing.T) {
	m := New(t.TempDir())
	page := &domain.Resource{ID: "1", Title: "T", SpaceKey: "S", Version: 3, Body: []byte("<p>hello</p>")}
	dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	csfPath := dir + "/" + slug + ".csf"
	lc, _, err := m.LoadCSF(csfPath)
	if err != nil {
		t.Fatal(err)
	}
	if lc.Dirty {
		t.Error("freshly written page should not be dirty")
	}
	if lc.Meta.Version != 3 {
		t.Errorf("meta version = %d", lc.Meta.Version)
	}
	// Base copy must exist for consequence diffs.
	if _, ok := m.BaseBody("1"); !ok {
		t.Error("base body not saved")
	}
}
