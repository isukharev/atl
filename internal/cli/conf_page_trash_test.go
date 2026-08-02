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
	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
)

type confluenceTrashHTTPFixture struct {
	t            *testing.T
	server       *httptest.Server
	mu           sync.Mutex
	trashed      bool
	gets         int
	deletes      int
	deleteStatus int
}

func newConfluenceTrashHTTPFixture(t *testing.T) *confluenceTrashHTTPFixture {
	t.Helper()
	fixture := &confluenceTrashHTTPFixture{t: t}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *confluenceTrashHTTPFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/rest/api/content/42" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		f.gets++
		status := r.URL.Query().Get("status")
		if status == "current" && f.trashed || status == "trashed" && !f.trashed {
			http.NotFound(w, r)
			return
		}
		if status != "current" && status != "trashed" {
			f.t.Errorf("GET status = %q", status)
			http.Error(w, "bad status", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"42","type":"page","status":"` + status + `","title":"Reviewed title","space":{"key":"DOC"},"version":{"number":7},"ancestors":[{"id":"10","title":"Home"}],"body":{"storage":{"value":"<p>body</p>"}}}`))
	case http.MethodDelete:
		f.deletes++
		if r.URL.Query().Get("status") != "current" {
			f.t.Errorf("DELETE status = %q, want current", r.URL.Query().Get("status"))
		}
		if f.deleteStatus != 0 {
			w.WriteHeader(f.deleteStatus)
			return
		}
		f.trashed = true
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *confluenceTrashHTTPFixture) counts() (gets, deletes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets, f.deletes
}

func TestConfPageTrashPreviewAndApply(t *testing.T) {
	fixture := newConfluenceTrashHTTPFixture(t)
	env := confEnv(fixture.server)
	out, code := runCLI(t, env, "conf", "page", "delete", "--id", "42")
	if code != exitOK {
		t.Fatalf("preview exit=%d out=%s", code, out)
	}
	var preview app.ConfluencePageTrashResult
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Status != "would_apply" || preview.Mode != "dry-run" || preview.CurrentVersion != 7 || preview.ProposalHash == "" || preview.WriteAttempted {
		t.Fatalf("preview = %+v", preview)
	}
	if _, deletes := fixture.counts(); deletes != 0 {
		t.Fatalf("preview deletes = %d", deletes)
	}

	out, code = runCLI(t, env, "conf", "page", "delete", "--id", "42",
		"--apply", "--confirm", "TRASH", "--expected-version", "7", "--expected-proposal-hash", preview.ProposalHash)
	if code != exitOK {
		t.Fatalf("apply exit=%d out=%s", code, out)
	}
	var applied app.ConfluencePageTrashResult
	if err := json.Unmarshal([]byte(out), &applied); err != nil {
		t.Fatal(err)
	}
	if applied.Status != "applied" || applied.Mode != "apply" || !applied.WriteAttempted || !applied.Reconciled || applied.ObservedState != "trashed" {
		t.Fatalf("applied = %+v", applied)
	}
	if _, deletes := fixture.counts(); deletes != 1 {
		t.Fatalf("apply deletes = %d, want 1", deletes)
	}
}

func TestConfPageTrashApplyGatesBeforeConfigAndNetwork(t *testing.T) {
	fixture := newConfluenceTrashHTTPFixture(t)
	env := confEnv(fixture.server)
	tests := [][]string{
		{"conf", "page", "delete", "--id", "42", "--confirm", "TRASH"},
		{"conf", "page", "delete", "--id", "42", "--apply", "--expected-version", "7", "--expected-proposal-hash", "hash"},
		{"conf", "page", "delete", "--id", "42", "--apply", "--confirm", "WRONG", "--expected-version", "7", "--expected-proposal-hash", "hash"},
		{"conf", "page", "delete", "--id", "42", "--apply", "--confirm", "TRASH", "--expected-proposal-hash", "hash"},
		{"conf", "page", "delete", "--id", "42", "--apply", "--confirm", "TRASH", "--expected-version", "7"},
	}
	for _, args := range tests {
		beforeGets, beforeDeletes := fixture.counts()
		out, code := runCLI(t, env, args...)
		if code != exitUsage || out != "" {
			t.Fatalf("args=%v exit=%d out=%q", args, code, out)
		}
		afterGets, afterDeletes := fixture.counts()
		if afterGets != beforeGets || afterDeletes != beforeDeletes {
			t.Fatalf("args=%v caused effects: GET %d->%d DELETE %d->%d", args, beforeGets, afterGets, beforeDeletes, afterDeletes)
		}
	}
}

func TestConfPageTrashApplyGatesPrecedeInvalidConfig(t *testing.T) {
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := [][]string{
		{"conf", "page", "delete"},
		{"conf", "page", "delete", "--id", "42", "--apply", "--confirm", "WRONG", "--expected-version", "7", "--expected-proposal-hash", "hash"},
		{"conf", "page", "delete", "--id", "42", "--apply", "--confirm", "TRASH", "--expected-version", "0", "--expected-proposal-hash", "hash"},
		{"conf", "page", "delete", "--id", "42", "--apply", "--confirm", "TRASH", "--expected-version", "7"},
	}
	for _, args := range tests {
		out, code := runCLI(t, map[string]string{"ATL_CONFIG_DIR": cfgDir}, args...)
		if code != exitUsage || out != "" {
			t.Fatalf("args=%v exit=%d out=%q, want usage before invalid config", args, code, out)
		}
	}
}

func TestConfPageTrashHashMismatchBlocksDelete(t *testing.T) {
	fixture := newConfluenceTrashHTTPFixture(t)
	out, code := runCLI(t, confEnv(fixture.server), "conf", "page", "delete", "--id", "42",
		"--apply", "--confirm", "TRASH", "--expected-version", "7", "--expected-proposal-hash", strings.Repeat("0", 64))
	if code != exitCheckFailed || !strings.Contains(out, `"status": "blocked"`) {
		t.Fatalf("exit=%d out=%q", code, out)
	}
	if _, deletes := fixture.counts(); deletes != 0 {
		t.Fatalf("deletes = %d, want 0", deletes)
	}
}

func TestConfPageTrashOutputFailureAfterAttemptIsNoReplayCheckFailure(t *testing.T) {
	fixture := newConfluenceTrashHTTPFixture(t)
	env := confEnv(fixture.server)
	out, code := runCLI(t, env, "conf", "page", "delete", "--id", "42")
	if code != exitOK {
		t.Fatalf("preview exit=%d out=%s", code, out)
	}
	var preview app.ConfluencePageTrashResult
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("stdout unavailable")
	err := runCLIWithFailingStdoutEnv(t, env, cause, "conf", "page", "delete", "--id", "42",
		"--apply", "--confirm", "TRASH", "--expected-version", "7", "--expected-proposal-hash", preview.ProposalHash)
	if !errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, cause) || !strings.Contains(err.Error(), "do not replay") {
		t.Fatalf("error = %v", err)
	}
	if _, deletes := fixture.counts(); deletes != 1 {
		t.Fatalf("deletes = %d, want 1", deletes)
	}
}

func TestConfPageTrashOutputFailureAfterDefinitiveRejectionIsNoReplayCheckFailure(t *testing.T) {
	fixture := newConfluenceTrashHTTPFixture(t)
	env := confEnv(fixture.server)
	out, code := runCLI(t, env, "conf", "page", "delete", "--id", "42")
	if code != exitOK {
		t.Fatalf("preview exit=%d out=%s", code, out)
	}
	var preview app.ConfluencePageTrashResult
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err)
	}
	fixture.deleteStatus = http.StatusForbidden
	cause := errors.New("stdout unavailable")
	err := runCLIWithFailingStdoutEnv(t, env, cause, "conf", "page", "delete", "--id", "42",
		"--apply", "--confirm", "TRASH", "--expected-version", "7", "--expected-proposal-hash", preview.ProposalHash)
	if !errors.Is(err, domain.ErrCheckFailed) || errors.Is(err, domain.ErrForbidden) || codeFor(err) != exitCheckFailed || !errors.Is(err, cause) || !strings.Contains(err.Error(), "do not replay") {
		t.Fatalf("error = %v", err)
	}
	if kind, remediation := diagnostic.Classify(err); kind != "check_failed" || remediation != "review_failed_check" {
		t.Fatalf("diagnostic = %s/%s, want check_failed/review_failed_check", kind, remediation)
	}
	if _, deletes := fixture.counts(); deletes != 1 {
		t.Fatalf("deletes = %d, want 1", deletes)
	}
}
