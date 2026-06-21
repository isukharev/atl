package mirror

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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
