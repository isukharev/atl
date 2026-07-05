package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runCLI executes the root command in-process with an isolated config dir, no
// self-update, and the caller-provided env (typically a backend URL + PAT
// pointing at an httptest server). It returns captured stdout and the mapped
// exit code.
//
// Note: the root runs with SilenceErrors/SilenceUsage, so the command writes
// nothing to its Err writer — the failure is rendered by Execute()/writeError to
// os.Stderr, outside this harness (Execute also calls os.Exit, so it can't be
// driven in-process; writeError's JSON/text formatting is covered directly by
// TestWriteError). The testable error contract here is therefore the mapped exit
// code plus the absence of any error text on stdout, which is what these tests
// assert.
func runCLI(t *testing.T, env map[string]string, args ...string) (stdout string, code int) {
	t.Helper()
	out, _, code := runCLIFull(t, env, args...)
	return out, code
}

// runCLIFull is the full harness: it returns stdout, stderr, and the exit code.
// Env is applied via t.Setenv (so it is restored at test end and forbids
// t.Parallel). ATL_NO_UPDATE and ATL_CONFIG_DIR are always set first; caller env
// overlays them.
func runCLIFull(t *testing.T, env map[string]string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	t.Setenv("ATL_NO_UPDATE", "1")
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	// Neutralize ambient backend config so a populated dev shell can't mask a test
	// (e.g. a stray ATL_CONFLUENCE_PAT making a "no PAT" case resolve a real token).
	// The caller env below re-sets whatever the case actually needs.
	for _, k := range []string{
		"ATL_CONFLUENCE_URL", "CONFLUENCE_URL", "ATL_JIRA_URL", "JIRA_URL",
		"ATL_CONFLUENCE_PAT", "CONFLUENCE_PAT", "ATL_JIRA_PAT", "JIRA_PAT",
		"ATL_MIRROR_ROOT", "ATL_ALLOW_INSECURE",
	} {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	var outBuf, errBuf bytes.Buffer
	root := newRoot()
	root.SetArgs(args)
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	// Mirror production Execute(): codeFor is only consulted on a non-nil error.
	// codeFor(nil) would fall through to the generic exit (1), so map a nil error
	// to exitOK here.
	if err := root.ExecuteContext(context.Background()); err != nil {
		return outBuf.String(), errBuf.String(), codeFor(err)
	}
	return outBuf.String(), errBuf.String(), exitOK
}

// confEnv points the Confluence backend at srv with a dummy PAT.
func confEnv(srv *httptest.Server) map[string]string {
	return map[string]string{
		"ATL_CONFLUENCE_URL": srv.URL,
		"ATL_CONFLUENCE_PAT": "test-pat",
	}
}

// jiraEnv points the Jira backend at srv with a dummy PAT.
func jiraEnv(srv *httptest.Server) map[string]string {
	return map[string]string{
		"ATL_JIRA_URL": srv.URL,
		"ATL_JIRA_PAT": "test-pat",
	}
}

// jsonServer returns an httptest server that replies with status + body for
// every request (path-agnostic), which matches the canned-JSON style the
// adapters' own tests use.
func jsonServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// --- Success-path contract tests (golden JSON) ---

// TestConfPageMetaGolden locks the JSON shape of `conf page meta`. GetMeta parses
// version/space/ancestors/labels/restrictions; the output is the domain.PageMeta
// struct, which is fully deterministic.
func TestConfPageMetaGolden(t *testing.T) {
	const body = `{
		"id": "12345",
		"title": "Design Doc",
		"space": {"key": "ENG"},
		"version": {"number": 7},
		"ancestors": [{"id": "1", "title": "Home"}, {"id": "2", "title": "Specs"}],
		"metadata": {"labels": {"results": [{"name": "spec"}, {"name": "approved"}]}},
		"restrictions": {"read": {"restrictions": {"user": {"results": []}, "group": {"results": []}}}}
	}`
	// Note: no _links.webui in the canned response — the rendered URL would embed
	// the httptest server's random port and make the golden non-deterministic. The
	// URL field is omitempty, so dropping it keeps the golden host-independent.
	srv := jsonServer(t, http.StatusOK, body)

	out, code := runCLI(t, confEnv(srv), "conf", "page", "meta", "--id", "12345")
	if code != exitOK {
		t.Fatalf("conf page meta: exit %d, want %d (stdout=%q)", code, exitOK, out)
	}
	assertGolden(t, "conf_page_meta.json", []byte(out))
}

// TestConfPageGetGolden locks the JSON shape of `conf page get` (csf format). The
// command emits a fixed map of id/title/space/version/body/url.
func TestConfPageGetGolden(t *testing.T) {
	const body = `{
		"id": "12345",
		"title": "Design Doc",
		"space": {"key": "ENG"},
		"version": {"number": 7},
		"body": {"storage": {"value": "<p>Hello <strong>world</strong></p>"}}
	}`
	// No _links.webui: conf page get always emits a "url" key (not omitempty), so a
	// rendered URL would embed the server's random port. Leaving it empty keeps the
	// golden host-independent.
	srv := jsonServer(t, http.StatusOK, body)

	out, code := runCLI(t, confEnv(srv), "conf", "page", "get", "--id", "12345")
	if code != exitOK {
		t.Fatalf("conf page get: exit %d, want %d (stdout=%q)", code, exitOK, out)
	}
	assertGolden(t, "conf_page_get.json", []byte(out))
}

// TestJiraIssueGetGolden locks the JSON shape of `jira issue get`. The fields map
// is kept minimal so the emitted Issue (including its echoed Fields map) is
// deterministic; Go marshals map keys in sorted order.
func TestJiraIssueGetGolden(t *testing.T) {
	const body = `{
		"key": "ENG-42",
		"fields": {
			"summary": "Fix the thing",
			"description": "h1. Steps\n\nDo the work.",
			"status": {"name": "In Progress"},
			"issuetype": {"name": "Bug"},
			"project": {"key": "ENG"},
			"labels": ["backend", "urgent"]
		}
	}`
	srv := jsonServer(t, http.StatusOK, body)

	out, code := runCLI(t, jiraEnv(srv), "jira", "issue", "get", "ENG-42")
	if code != exitOK {
		t.Fatalf("jira issue get: exit %d, want %d (stdout=%q)", code, exitOK, out)
	}
	assertGolden(t, "jira_issue_get.json", []byte(out))
}

// --- Exit-code matrix (sentinel error → exit code) ---
//
// Each case drives a read command against an httptest server returning the
// triggering HTTP status; httpx.classify maps the status to a domain sentinel,
// and codeFor maps that to the process exit code. We assert the exit code and,
// for error cases, that stdout is empty (errors go to stderr).

func TestExitCodeMatrix(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   int
	}{
		{"unauthorized_401", http.StatusUnauthorized, exitAuth}, // 401 → 3
		{"forbidden_403", http.StatusForbidden, exitForbidden},  // 403 → 6
		{"notfound_404", http.StatusNotFound, exitNotFound},     // 404 → 4
		{"badrequest_400", http.StatusBadRequest, exitUsage},    // 400 → 2
		// 500 on an idempotent GET is genuinely retried 3× with jittered backoff
		// before failing, so this case takes ~1-2s — that is the retry path being
		// exercised, not a hang.
		{"servererror_500", http.StatusInternalServerError, exitGeneric}, // 500 → 1
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := jsonServer(t, tc.status, `{"message":"error"}`)
			out, code := runCLI(t, confEnv(srv), "conf", "page", "meta", "--id", "1")
			if code != tc.want {
				t.Fatalf("status %d: exit %d, want %d", tc.status, code, tc.want)
			}
			if out != "" {
				t.Errorf("status %d: stdout = %q, want empty (errors go to stderr)", tc.status, out)
			}
		})
	}
}

// TestVersionConflictExit5 covers the 409 → ErrVersionConflict (exit 5) path. A
// plain GET that returns 409 reaches the same classify→codeFor mapping a push
// 409 would, without needing a mirror on disk. conf page meta's single GET is the
// simplest reproducer.
func TestVersionConflictExit5(t *testing.T) {
	srv := jsonServer(t, http.StatusConflict, `{"message":"version conflict"}`)
	out, code := runCLI(t, confEnv(srv), "conf", "page", "meta", "--id", "1")
	if code != exitVersionConfl {
		t.Fatalf("409: exit %d, want %d", code, exitVersionConfl)
	}
	if out != "" {
		t.Errorf("409: stdout = %q, want empty", out)
	}
}

// TestJiraExitCodeMatrix repeats the core sentinel cases against the Jira adapter
// to prove the mapping is backend-independent (both go through httpx.classify).
func TestJiraExitCodeMatrix(t *testing.T) {
	cases := []struct {
		status int
		want   int
	}{
		{http.StatusUnauthorized, exitAuth},
		{http.StatusForbidden, exitForbidden},
		{http.StatusNotFound, exitNotFound},
	}
	for _, tc := range cases {
		srv := jsonServer(t, tc.status, `{"errorMessages":["error"]}`)
		out, code := runCLI(t, jiraEnv(srv), "jira", "issue", "get", "ENG-1")
		if code != tc.want {
			t.Errorf("jira status %d: exit %d, want %d", tc.status, code, tc.want)
		}
		if out != "" {
			t.Errorf("jira status %d: stdout = %q, want empty", tc.status, out)
		}
	}
}

// TestCheckFailedExit8 locks the one sentinel that is not produced by an HTTP
// status: ErrCheckFailed (exit 8). `jira issue check` succeeds at the transport
// level but a missing --require field makes it a logical failure, so it belongs
// in the sentinel→exit contract matrix alongside the HTTP-driven cases. The
// report is still emitted on stdout before the non-zero exit.
func TestCheckFailedExit8(t *testing.T) {
	srv := jsonServer(t, http.StatusOK, `{"key":"ENG-1","fields":{}}`)
	out, code := runCLI(t, jiraEnv(srv), "jira", "issue", "check", "ENG-1", "--require", "summary")
	if code != exitCheckFailed {
		t.Fatalf("check with a missing required field: exit %d, want %d (stdout=%q)", code, exitCheckFailed, out)
	}
	if !strings.Contains(out, `"ok": false`) {
		t.Errorf("expected the report (ok:false) on stdout before exit 8, got %q", out)
	}
}

// TestUsageErrorMissingFlagExit2 covers a use-case-level usage error (a required
// flag is missing) — distinct from the cobra flag-parse error already covered in
// exitcode_test.go. No server is contacted because the command bails on
// validation first.
func TestUsageErrorMissingFlagExit2(t *testing.T) {
	out, code := runCLI(t, nil, "conf", "page", "meta")
	if code != exitUsage {
		t.Fatalf("missing --id: exit %d, want %d", code, exitUsage)
	}
	if out != "" {
		t.Errorf("missing --id: stdout = %q, want empty", out)
	}
}

// TestMissingBackendURLExitConfig confirms an unconfigured backend URL is a
// config error (exit 7 — "not set up"), distinct from a usage error, surfaced by
// wire.NewConfluence before any HTTP call.
func TestMissingBackendURLExitConfig(t *testing.T) {
	out, code := runCLI(t, nil, "conf", "page", "meta", "--id", "1")
	if code != exitConfig {
		t.Fatalf("no backend URL: exit %d, want %d", code, exitConfig)
	}
	if out != "" {
		t.Errorf("no backend URL: stdout = %q, want empty", out)
	}
}

// TestMissingPATExitConfig confirms that a configured URL but no PAT is also a
// config error (exit 7), not a server-side auth rejection (exit 3): the token is
// simply not set up yet. CheckSecureURL passes for the https URL, so the failure
// comes from auth.Token before any request is made.
func TestMissingPATExitConfig(t *testing.T) {
	env := map[string]string{"ATL_CONFLUENCE_URL": "https://confluence.example.com"}
	out, code := runCLI(t, env, "conf", "page", "meta", "--id", "1")
	if code != exitConfig {
		t.Fatalf("no PAT: exit %d, want %d", code, exitConfig)
	}
	if out != "" {
		t.Errorf("no PAT: stdout = %q, want empty", out)
	}
}

// TestInsecureBackendURLExitUsage pins the wire-level ErrUsage wrap for a
// non-https backend URL on a non-loopback host (exit 2, ATL_ALLOW_INSECURE is
// the documented override — neutralized by the harness). The PAT is present so
// the failure can only come from CheckSecureURL inside NewConfluence/NewJira,
// before any HTTP call; a regression to a bare error would degrade to exit 1.
func TestInsecureBackendURLExitUsage(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		args []string
	}{
		{"confluence", map[string]string{
			"ATL_CONFLUENCE_URL": "http://confluence.example.com",
			"ATL_CONFLUENCE_PAT": "test-pat",
		}, []string{"conf", "page", "meta", "--id", "1"}},
		{"jira", map[string]string{
			"ATL_JIRA_URL": "http://jira.example.com",
			"ATL_JIRA_PAT": "test-pat",
		}, []string{"jira", "issue", "get", "X-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Error text rendering happens in production Execute() (see the
			// harness comment) — the in-process contract is the exit code and a
			// clean stdout.
			out, code := runCLI(t, tc.env, tc.args...)
			if code != exitUsage {
				t.Fatalf("insecure URL: exit %d, want %d", code, exitUsage)
			}
			if out != "" {
				t.Errorf("insecure URL: stdout = %q, want empty", out)
			}
		})
	}
}
