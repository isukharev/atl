package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestConfStatusCorruptMirrorMetadataMapsToExitEight(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "page.csf"), []byte("<p>x</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := runCLI(t, confEnv(srv), "conf", "status", root); code != exitCheckFailed {
		t.Fatalf("conf status exit=%d stdout=%q, want %d", code, out, exitCheckFailed)
	}
}
