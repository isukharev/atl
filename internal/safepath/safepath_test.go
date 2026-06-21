package safepath

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSegmentNeutralizesTraversalAndSeparators(t *testing.T) {
	cases := map[string]string{
		"..":               "_",
		".":                "_",
		"":                 "_",
		"   ":              "_",
		"../../etc/passwd": "_..-..-etc-passwd", // separators gone; leading dot escaped
		"a/b":              "a-b",
		`a\b`:              "a-b",
		"C:":               "C-",
		".atl":             "_.atl", // reserved internal dir escaped
		".ssh":             "_.ssh",
		"normal-123":       "normal-123",
		"Пространство":     "Пространство", // unicode preserved
		"with\x00nul":      "with-nul",
		"with\nnewline":    "with-newline",
		"trailing/":        "trailing-",
		"....":             "_....", // leading dot escaped; not a traversal token
		"..\\..\\evil":     "_..-..-evil",
	}
	for in, want := range cases {
		if got := Segment(in); got != want {
			t.Errorf("Segment(%q) = %q, want %q", in, got, want)
		}
	}
	// Crucial invariant: no result is a separator or a traversal token.
	for in := range cases {
		got := Segment(in)
		if strings.ContainsAny(got, `/\`) || got == ".." || got == "." || got == "" {
			t.Errorf("Segment(%q) = %q is not a safe single segment", in, got)
		}
	}
}

func TestBaseRejectsUnusableNames(t *testing.T) {
	if _, ok := Base(".."); ok {
		t.Error("Base(..) should be rejected")
	}
	if _, ok := Base("."); ok {
		t.Error("Base(.) should be rejected")
	}
	if _, ok := Base(""); ok {
		t.Error("Base(empty) should be rejected")
	}
	got, ok := Base("../../../../home/user/.ssh/authorized_keys")
	if !ok {
		t.Fatal("Base of a traversal path should yield its basename")
	}
	if strings.ContainsAny(got, `/\`) {
		t.Errorf("Base returned %q containing a separator", got)
	}
	if got != "authorized_keys" {
		t.Errorf("Base traversal = %q, want authorized_keys", got)
	}
	if got, _ := Base("a/b/c.png"); got != "c.png" {
		t.Errorf("Base(a/b/c.png) = %q, want c.png", got)
	}
}

func TestWithinContainment(t *testing.T) {
	root := "/srv/mirror"
	in := []string{
		root,
		filepath.Join(root, "SPACE", "page.csf"),
		filepath.Join(root, ".atl", "base", "123.csf"),
	}
	for _, p := range in {
		if !Within(root, p) {
			t.Errorf("Within(%q, %q) = false, want true", root, p)
		}
	}
	out := []string{
		"/srv/other",
		filepath.Join(root, "..", "escape"),
		"/etc/passwd",
		filepath.Join(root, "..", "mirror-evil"), // sibling sharing a prefix must not pass
	}
	for _, p := range out {
		if Within(root, p) {
			t.Errorf("Within(%q, %q) = true, want false", root, p)
		}
	}
}

// A server-controlled id/key joined through Segment can never escape the root.
func TestSegmentJoinStaysWithinRoot(t *testing.T) {
	root := "/srv/mirror/.atl/base"
	for _, id := range []string{"../../../../home/user/.bashrc", "..", "/etc/passwd", "12345"} {
		target := filepath.Join(root, Segment(id)+".csf")
		if !Within(root, target) {
			t.Errorf("malicious id %q escaped: %q", id, target)
		}
	}
}
