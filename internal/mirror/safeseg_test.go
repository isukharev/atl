package mirror

import (
	"path/filepath"
	"strings"
	"testing"
)

// A space key (or --space value) must never let a page directory escape the
// mirror root, even if a hostile server returns "." / ".." or a separator.
func TestPageDirCannotEscapeRoot(t *testing.T) {
	m := New("/m")
	for _, space := range []string{"..", ".", "../..", "a/b", `a\b`, "x:y", ""} {
		dir, _ := m.PageDir(space, nil, "title")
		clean := filepath.Clean(dir)
		if !strings.HasPrefix(clean, "/m/") && clean != "/m" {
			t.Errorf("space %q produced dir %q outside mirror root", space, clean)
		}
		// No individual path segment may be a traversal element.
		for _, seg := range strings.Split(clean, string(filepath.Separator)) {
			if seg == ".." || seg == "." {
				t.Errorf("space %q produced dir %q with traversal segment %q", space, clean, seg)
			}
		}
	}
}
