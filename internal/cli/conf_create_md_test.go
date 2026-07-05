package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- conf page create --from-md: markdown-authored page bodies ---

const createMD = "# Title\n\nIntro with **bold**.\n\n- one\n- two\n"

// createMDCSF is what mdcsf.ConvertDocument produces for createMD — pinned
// here so a converter drift that changes what create sends is caught.
const createMDCSF = "<h1>Title</h1>\n<p>Intro with <strong>bold</strong>.</p>\n<ul><li>one</li><li>two</li></ul>"

// TestConfPageCreate_FromMD covers the happy path: the markdown body is
// converted whole-document to CSF and THAT is what goes over the wire.
func TestConfPageCreate_FromMD(t *testing.T) {
	cs := newConfServer(t)
	cs.writes = []cannedResp{{status: http.StatusOK, body: pageJSON("101", "MD Page", 1, createMDCSF)}}

	dir := t.TempDir()
	mdPath := filepath.Join(dir, "body.md")
	if err := os.WriteFile(mdPath, []byte(createMD), 0o644); err != nil {
		t.Fatalf("write md: %v", err)
	}

	out, code := runCLI(t, confEnv(cs.srv),
		"conf", "page", "create", "--space", "ENG", "--title", "MD Page", "--from-md", mdPath)
	if code != exitOK {
		t.Fatalf("conf page create --from-md: exit %d, want 0 (stdout=%q)", code, out)
	}
	if !strings.Contains(out, `"id": "101"`) {
		t.Errorf("create output = %q, want id 101", out)
	}

	writes := cs.writeReqs()
	if len(writes) != 1 {
		t.Fatalf("expected 1 write, got %d: %+v", len(writes), writes)
	}
	if got := putStorageValue(t, writes[0].body); got != createMDCSF {
		t.Fatalf("POST storage value = %q, want converted CSF %q", got, createMDCSF)
	}
}

// TestConfPageCreate_FromMDStdin covers `--from-md -`: markdown on stdin.
func TestConfPageCreate_FromMDStdin(t *testing.T) {
	cs := newConfServer(t)
	cs.writes = []cannedResp{{status: http.StatusOK, body: pageJSON("102", "MD Stdin", 1, createMDCSF)}}

	withStdin(t, createMD, func() {
		out, code := runCLI(t, confEnv(cs.srv),
			"conf", "page", "create", "--space", "ENG", "--title", "MD Stdin", "--from-md", "-")
		if code != exitOK {
			t.Fatalf("conf page create --from-md -: exit %d, want 0 (stdout=%q)", code, out)
		}
	})
	writes := cs.writeReqs()
	if len(writes) != 1 {
		t.Fatalf("expected 1 write, got %d", len(writes))
	}
	if got := putStorageValue(t, writes[0].body); got != createMDCSF {
		t.Fatalf("POST storage value = %q, want %q", got, createMDCSF)
	}
}

// TestConfPageCreate_FromMDUnsupportedExit8 locks the fail-closed contract: an
// unconvertible markdown block refuses with ErrCheckFailed (exit 8, same as
// `conf apply`), names the offending block, and sends NOTHING to the backend.
func TestConfPageCreate_FromMDUnsupportedExit8(t *testing.T) {
	cs := newConfServer(t)

	dir := t.TempDir()
	mdPath := filepath.Join(dir, "bad.md")
	bad := "# Fine\n\n![diagram](attachment:x.png)\n"
	if err := os.WriteFile(mdPath, []byte(bad), 0o644); err != nil {
		t.Fatalf("write md: %v", err)
	}

	// The harness returns the mapped exit code; the "block 2 (...)" naming in
	// the error text is pinned by TestConvertDocumentFailsClosed in mdcsf.
	out, code := runCLI(t, confEnv(cs.srv),
		"conf", "page", "create", "--space", "ENG", "--title", "Bad", "--from-md", mdPath)
	if code != exitCheckFailed {
		t.Fatalf("unsupported md: exit %d, want %d (stdout=%q)", code, exitCheckFailed, out)
	}
	if reqs := cs.requests(); len(reqs) != 0 {
		t.Fatalf("fail-closed breached: %d request(s) sent: %+v", len(reqs), reqs)
	}
}

// TestConfPageCreate_FromMDExclusiveWithFromFile: setting both body flags is a
// usage error before anything is read or sent.
func TestConfPageCreate_FromMDExclusiveWithFromFile(t *testing.T) {
	cs := newConfServer(t)

	dir := t.TempDir()
	mdPath := filepath.Join(dir, "b.md")
	csfPath := filepath.Join(dir, "b.csf")
	for _, p := range []string{mdPath, csfPath} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	_, code := runCLI(t, confEnv(cs.srv),
		"conf", "page", "create", "--space", "ENG", "--title", "Both",
		"--from-file", csfPath, "--from-md", mdPath)
	if code != exitUsage {
		t.Fatalf("both body flags: exit %d, want %d", code, exitUsage)
	}
	if reqs := cs.requests(); len(reqs) != 0 {
		t.Fatalf("expected zero requests, got %d: %+v", len(reqs), reqs)
	}
}

// TestConfPageCreate_FromMDEmptyDoc: an empty markdown body is refused (exit 8),
// not created as a blank page.
func TestConfPageCreate_FromMDEmptyDoc(t *testing.T) {
	cs := newConfServer(t)

	dir := t.TempDir()
	mdPath := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(mdPath, []byte("\n\n"), 0o644); err != nil {
		t.Fatalf("write md: %v", err)
	}

	_, code := runCLI(t, confEnv(cs.srv),
		"conf", "page", "create", "--space", "ENG", "--title", "Empty", "--from-md", mdPath)
	if code != exitCheckFailed {
		t.Fatalf("empty md doc: exit %d, want %d", code, exitCheckFailed)
	}
	if reqs := cs.requests(); len(reqs) != 0 {
		t.Fatalf("expected zero requests, got %d: %+v", len(reqs), reqs)
	}
}
