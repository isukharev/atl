package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// update controls golden-file regeneration. Run the golden tests with -update
// to (re)write the files under testdata/golden, then re-run without it to
// confirm they match. Defined once for the whole package's tests.
var update = flag.Bool("update", false, "update golden files")

// assertGolden compares got to testdata/golden/<name>. With -update it (re)writes
// the golden file instead of asserting. The directory is created on demand so a
// first-time -update run works on a clean checkout.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create golden dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `go test ./internal/cli/ -run %s -update` to create it)", path, err, t.Name())
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output does not match golden %s\n--- got ---\n%s\n--- want ---\n%s\n(run with -update to refresh)", path, got, want)
	}
}
