package safepath

import (
	"path/filepath"
	"strings"
	"testing"
)

// segmentSeeds mirror the hand-picked inputs in
// TestSegmentNeutralizesTraversalAndSeparators plus extra traversal payloads.
var segmentSeeds = []string{
	"..",
	".",
	"",
	"   ",
	"../../etc/passwd",
	"a/b",
	`a\b`,
	"C:",
	".atl",
	".ssh",
	"normal-123",
	"Пространство",
	"with\x00nul",
	"with\nnewline",
	"trailing/",
	"....",
	"..\\..\\evil",
	"/etc/passwd",
	"../../../../home/user/.bashrc",
	"con\x7fdel",
	"\x1bescape",
	"///",          // all separators collapse to the "_" fallback
	"\x00\x00\x00", // all control bytes
}

func seedSegments(f *testing.F) {
	for _, s := range segmentSeeds {
		f.Add(s)
	}
}

// FuzzSegment asserts the universal invariants of Segment for ANY input: the
// result is a single, real path element — no separators, never a traversal
// token, never empty.
func FuzzSegment(f *testing.F) {
	seedSegments(f)
	f.Fuzz(func(t *testing.T, s string) {
		got := Segment(s)
		if strings.ContainsAny(got, `/\`) {
			t.Fatalf("Segment(%q) = %q contains a path separator", s, got)
		}
		if got == "." || got == ".." || got == "" {
			t.Fatalf("Segment(%q) = %q is a traversal/empty token", s, got)
		}
		if filepath.Base(got) != got {
			t.Fatalf("Segment(%q) = %q is not a single path element (Base=%q)", s, got, filepath.Base(got))
		}
	})
}

// FuzzSegmentJoinStaysWithinRoot is the defense-in-depth containment check: a
// server-controlled id, joined through Segment, must never resolve outside the
// mirror's base store. The no-separator / no-traversal-token guarantee that makes
// this hold is carried directly by FuzzSegment; here we additionally prove the
// composed Join+Within behaves. The sanitized id is joined as its OWN path
// component (not concatenated into a filename) so that a regression letting a
// bare ".." through would resolve above root and trip Within — a "+.csf" suffix
// would mask it by turning ".." into the harmless literal filename "...csf".
func FuzzSegmentJoinStaysWithinRoot(f *testing.F) {
	seedSegments(f)
	f.Add("../../../../home/user/.bashrc")
	f.Add("/etc/passwd")
	f.Fuzz(func(t *testing.T, id string) {
		const root = "/srv/mirror/.atl/base"
		seg := Segment(id)
		target := filepath.Join(root, seg, "page.csf")
		if !Within(root, target) {
			t.Fatalf("id %q escaped root: Segment=%q target=%q", id, seg, target)
		}
	})
}

// FuzzBase asserts that whenever Base accepts a name, the returned base is a
// single safe element (no separators, never a traversal/empty token).
func FuzzBase(f *testing.F) {
	seedSegments(f)
	f.Add("a/b/c.png")
	f.Add("../../../../home/user/.ssh/authorized_keys")
	f.Fuzz(func(t *testing.T, name string) {
		got, ok := Base(name)
		if !ok {
			return
		}
		if strings.ContainsAny(got, `/\`) {
			t.Fatalf("Base(%q) = %q contains a path separator", name, got)
		}
		if got == "." || got == ".." || got == "" {
			t.Fatalf("Base(%q) = %q is a traversal/empty token", name, got)
		}
	})
}
