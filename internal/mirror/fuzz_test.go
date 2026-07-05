package mirror

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

// slugSeeds mirror the existing slugify test cases plus unicode/Cyrillic and an
// over-long string that must be rune-truncated.
var slugSeeds = []string{
	"Hello World",
	"  trim  me  ",
	"ADR: контест / gateway",
	"",
	"!!!",
	"a___b...c",
	"Пространство",
	strings.Repeat("я", 200),
	strings.Repeat("a", 500),
	"\x00\x01\x02",
	"---",
	"   ",
	"MiXeD CaSe 123",
}

func seedSlugs(f *testing.F) {
	for _, s := range slugSeeds {
		f.Add(s)
	}
}

// FuzzSlugify asserts the directory-slug invariants for ANY title: non-empty,
// at most 80 runes, and free of path separators.
func FuzzSlugify(f *testing.F) {
	seedSlugs(f)
	f.Fuzz(func(t *testing.T, s string) {
		got := slugify(s)
		if got == "" {
			t.Fatalf("slugify(%q) returned empty", s)
		}
		if n := utf8.RuneCountInString(got); n > 80 {
			t.Fatalf("slugify(%q) = %q has %d runes (> 80)", s, got, n)
		}
		if strings.ContainsAny(got, `/\`) {
			t.Fatalf("slugify(%q) = %q contains a path separator", s, got)
		}
	})
}

// FuzzSafeSegContainment asserts that for ANY server-controlled segment (space
// key, content id), safeSeg yields a single element that, joined under the base
// store, stays Within it — the same containment property asserted by
// TestSaveBaseCannotEscapeRoot and TestPageDirCannotEscapeRoot.
func FuzzSafeSegContainment(f *testing.F) {
	for _, s := range segmentSeedsForMirror() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, seg string) {
		const root = "/srv/mirror/.atl/base"
		sanitized := safeSeg(seg)
		if strings.ContainsAny(sanitized, `/\`) {
			t.Fatalf("safeSeg(%q) = %q contains a path separator", seg, sanitized)
		}
		if sanitized == "." || sanitized == ".." || sanitized == "" {
			t.Fatalf("safeSeg(%q) = %q is a traversal/empty token", seg, sanitized)
		}
		// Join the sanitized segment as its own path component so a regression
		// returning a bare ".." would resolve above root and trip Within; a
		// "+.csf" suffix would mask it (".." -> the literal filename "...csf").
		target := filepath.Join(root, sanitized, "page.csf")
		if !safepath.Within(root, target) {
			t.Fatalf("safeSeg(%q) escaped root: sanitized=%q target=%q", seg, sanitized, target)
		}
	})
}

// FuzzClaimPageDirContainment asserts that for ANY server-controlled space
// key, title and page id, the dir handed out by ClaimPageDir stays Within the
// mirror root and the slug is a single path component — on BOTH branches: the
// free claim and the divert to an id-suffixed slug (forced by seeding the
// plain dir with a foreign owner, so the hostile id actually lands in a path).
func FuzzClaimPageDirContainment(f *testing.F) {
	for _, s := range segmentSeedsForMirror() {
		f.Add(s, s, s)
	}
	f.Add("SPACE", "Foo Bar", "12345")
	f.Add(".atl", "base", "../../etc")
	f.Fuzz(func(t *testing.T, space, title, id string) {
		m := New(t.TempDir())
		assertContained := func(dir, slug, branch string) {
			t.Helper()
			if strings.ContainsAny(slug, `/\`) {
				t.Fatalf("%s slug %q contains a path separator (space=%q title=%q id=%q)", branch, slug, space, title, id)
			}
			// Join the slug as its own component so a bare-".." regression trips
			// Within instead of hiding inside a longer filename.
			if !safepath.Within(m.Root, filepath.Join(dir, slug, "x")) {
				t.Fatalf("%s dir escaped root: dir=%q (space=%q title=%q id=%q)", branch, dir, space, title, id)
			}
		}
		dir, slug, err := m.ClaimPageDir(space, nil, title, id)
		if err != nil {
			t.Fatalf("claim on empty root failed: %v", err)
		}
		assertContained(dir, slug, "free")

		// Seed the plain dir with an owner that can never equal id, forcing the
		// divert branch. Absurd inputs (e.g. names exceeding filesystem limits)
		// cannot be seeded — the free-branch assertions above still ran.
		plainDir, plainSlug := m.PageDir(space, nil, title)
		if os.MkdirAll(plainDir, 0o755) != nil {
			return
		}
		mb, merr := json.Marshal(Meta{ID: id + "x"})
		if merr != nil || os.WriteFile(filepath.Join(plainDir, plainSlug+".meta.json"), mb, 0o644) != nil {
			return
		}
		dir2, slug2, err := m.ClaimPageDir(space, nil, title, id)
		if id == "" {
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("empty-id collision: err=%v, want ErrCheckFailed", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("divert claim failed: %v (space=%q title=%q id=%q)", err, space, title, id)
		}
		if dir2 == plainDir {
			t.Fatalf("claim did not divert from foreign-owned dir %q (id=%q)", plainDir, id)
		}
		assertContained(dir2, slug2, "diverted")
	})
}

// segmentSeedsForMirror are hostile path-segment payloads used to seed the
// containment fuzz target.
func segmentSeedsForMirror() []string {
	return []string{
		"..",
		".",
		"",
		"../..",
		"a/b",
		`a\b`,
		"x:y",
		".atl",
		"../../../../tmp/atl-evil",
		"/etc/passwd",
		"with\x00nul",
		"Пространство",
		"///", // all separators collapse to the "_" fallback
	}
}
