package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

type confluenceAttachmentDeleteHTTPFixture struct {
	t      *testing.T
	server *httptest.Server
	mu     sync.Mutex

	attachments []string
	deletes     int
	deleteCode  int
}

func newConfluenceAttachmentDeleteHTTPFixture(t *testing.T) *confluenceAttachmentDeleteHTTPFixture {
	t.Helper()
	f := &confluenceAttachmentDeleteHTTPFixture{t: t, attachments: []string{"98", "99"}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	return f
}

func (f *confluenceAttachmentDeleteHTTPFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/content/42":
		if r.URL.Query().Get("status") != "current" {
			f.t.Errorf("page status = %q, want current", r.URL.Query().Get("status"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"42","type":"page","status":"current","title":"Reviewed page","space":{"key":"DOC"},"version":{"number":7},"ancestors":[{"id":"10","title":"Home"}],"body":{"storage":{"value":"<p>body</p>"}}}`))
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/content/42/child/attachment":
		w.Header().Set("Content-Type", "application/json")
		rows := make([]string, 0, len(f.attachments))
		for _, id := range f.attachments {
			rows = append(rows, `{"id":"`+id+`","title":"file-`+id+`.txt","metadata":{"mediaType":"text/plain"},"extensions":{"fileSize":4,"comment":"reviewed"},"version":{"number":2}}`)
		}
		_, _ = w.Write([]byte(`{"results":[` + strings.Join(rows, ",") + `],"_links":{}}`))
	case r.Method == http.MethodDelete && r.URL.Path == "/rest/api/content/99":
		f.deletes++
		if f.deleteCode != 0 {
			w.WriteHeader(f.deleteCode)
			return
		}
		f.attachments = []string{"98"}
		w.WriteHeader(http.StatusNoContent)
	default:
		f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		http.NotFound(w, r)
	}
}

func (f *confluenceAttachmentDeleteHTTPFixture) deleteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deletes
}

func TestConfAttachmentDeletePreviewAndApply(t *testing.T) {
	fixture := newConfluenceAttachmentDeleteHTTPFixture(t)
	env := confEnv(fixture.server)
	out, code := runCLI(t, env, "conf", "attachment", "delete", "--page-id", "42", "--id", "99")
	if code != exitOK {
		t.Fatalf("preview exit=%d out=%s", code, out)
	}
	var preview app.ConfluenceAttachmentDeleteResult
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Status != "would_apply" || preview.Mode != "dry-run" || preview.CurrentPageVersion != 7 || preview.InventoryCount != 2 || preview.ExpectedFinalCount != 1 || preview.ProposalHash == "" || preview.WriteAttempted {
		t.Fatalf("preview = %+v", preview)
	}
	if fixture.deleteCount() != 0 {
		t.Fatal("preview attempted DELETE")
	}

	out, code = runCLI(t, env, "conf", "attachment", "delete", "--page-id", "42", "--id", "99",
		"--apply", "--confirm", "DELETE", "--expected-version", "7", "--expected-proposal-hash", preview.ProposalHash)
	if code != exitOK {
		t.Fatalf("apply exit=%d out=%s", code, out)
	}
	var applied app.ConfluenceAttachmentDeleteResult
	if err := json.Unmarshal([]byte(out), &applied); err != nil {
		t.Fatal(err)
	}
	if applied.Status != "applied" || !applied.WriteAttempted || !applied.Reconciled || applied.ObservedState != "absent" || applied.FinalCount != 1 {
		t.Fatalf("applied = %+v", applied)
	}
	if fixture.deleteCount() != 1 {
		t.Fatalf("DELETE count = %d, want 1", fixture.deleteCount())
	}
}

func TestConfAttachmentDeleteGuardsPrecedeConfigAndNetwork(t *testing.T) {
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"conf", "attachment", "delete"},
		{"conf", "attachment", "delete", "--page-id", "42", "--id", "99", "-o", "id"},
		{"conf", "attachment", "delete", "--page-id", "42", "--id", "99", "--confirm", "DELETE"},
		{"conf", "attachment", "delete", "--page-id", "42", "--id", "99", "--apply", "--confirm", "WRONG", "--expected-version", "7", "--expected-proposal-hash", "hash"},
		{"conf", "attachment", "delete", "--page-id", "42", "--id", "99", "--apply", "--confirm", "DELETE", "--expected-proposal-hash", "hash"},
		{"conf", "attachment", "delete", "--page-id", "42", "--id", "99", "--apply", "--confirm", "DELETE", "--expected-version", "7"},
	} {
		out, code := runCLI(t, map[string]string{"ATL_CONFIG_DIR": cfgDir}, args...)
		if code != exitUsage || out != "" {
			t.Fatalf("args=%v exit=%d out=%q", args, code, out)
		}
	}
}

func TestConfAttachmentDeleteHashMismatchBlocksWrite(t *testing.T) {
	fixture := newConfluenceAttachmentDeleteHTTPFixture(t)
	out, code := runCLI(t, confEnv(fixture.server), "conf", "attachment", "delete", "--page-id", "42", "--id", "99",
		"--apply", "--confirm", "DELETE", "--expected-version", "7", "--expected-proposal-hash", strings.Repeat("0", 64))
	if code != exitCheckFailed || !strings.Contains(out, `"status": "blocked"`) {
		t.Fatalf("exit=%d out=%q", code, out)
	}
	if fixture.deleteCount() != 0 {
		t.Fatal("hash mismatch attempted DELETE")
	}
}

func TestConfAttachmentDeleteOutputFailureAfterAttemptIsNoReplay(t *testing.T) {
	fixture := newConfluenceAttachmentDeleteHTTPFixture(t)
	env := confEnv(fixture.server)
	out, code := runCLI(t, env, "conf", "attachment", "delete", "--page-id", "42", "--id", "99")
	if code != exitOK {
		t.Fatalf("preview exit=%d out=%s", code, out)
	}
	var preview app.ConfluenceAttachmentDeleteResult
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("stdout unavailable")
	err := runCLIWithFailingStdoutEnv(t, env, cause, "conf", "attachment", "delete", "--page-id", "42", "--id", "99",
		"--apply", "--confirm", "DELETE", "--expected-version", "7", "--expected-proposal-hash", preview.ProposalHash)
	if !errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, cause) || !strings.Contains(err.Error(), "do not replay") {
		t.Fatalf("error = %v", err)
	}
	if fixture.deleteCount() != 1 {
		t.Fatalf("DELETE count = %d, want 1", fixture.deleteCount())
	}
}
