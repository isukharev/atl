package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

// --- stateful, method+path-routing httptest server ---
//
// The package's existing jsonServer is path-agnostic and replies with one canned
// body for every request, so it cannot capture a PUT body or vary the response
// per call. confServer routes by r.Method + r.URL.Path against the real
// Confluence adapter endpoints:
//
//	GET /rest/api/content/<id>   → page JSON (serves the configured page)
//	PUT /rest/api/content/<id>   → records the request, replies from the queue
//
// It records every captured request so a test can assert what went out on the
// wire (notably version.number inside a PUT body), and can vary the PUT/POST
// response per call via a queue of canned replies.
type confServer struct {
	srv *httptest.Server

	mu sync.Mutex
	// page is the JSON served on every GET /rest/api/content/<id>. Tests mutate
	// it (e.g. to report a drifted version for --force).
	page string
	// writes is the queue of responses for write requests (PUT/POST), consumed in
	// order. When exhausted the last entry is reused.
	writes []cannedResp
	// gets is an optional queue overriding the GET response per call (status+body),
	// consumed in order; when nil or exhausted, `page` with 200 is served. Used to
	// make the post-push refresh GET fail while an earlier GET succeeds.
	gets []cannedResp
	// comments is the JSON served on GET /rest/api/content/<id>/child/comment (the
	// `pull --comments` / `comment list` path). Empty means an empty listing.
	comments string

	// captured requests, in arrival order.
	reqs []capturedReq
}

type cannedResp struct {
	status int
	body   string
}

// capturedReq is one observed request.
type capturedReq struct {
	method string
	path   string
	query  string
	body   string
}

func newConfServer(t *testing.T) *confServer {
	t.Helper()
	cs := &confServer{}
	cs.srv = httptest.NewServer(http.HandlerFunc(cs.handle))
	t.Cleanup(cs.srv.Close)
	return cs
}

func (cs *confServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := readAll(r)
	cs.mu.Lock()
	cs.reqs = append(cs.reqs, capturedReq{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: string(body)})
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/child/comment"):
		// Comment listing (pull --comments / comment list). Checked before the
		// generic content GET below, which would otherwise serve the page body.
		body := cs.comments
		cs.mu.Unlock()
		if body == "" {
			body = `{"results":[],"_links":{}}`
		}
		writeJSON(w, http.StatusOK, body)
		return
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/api/content/"):
		resp := cannedResp{status: http.StatusOK, body: cs.page}
		// gets overrides per call; the final queued entry sticks (so a GET that
		// httpx retries on 5xx keeps failing across all attempts).
		if len(cs.gets) == 1 {
			resp = cs.gets[0]
		} else if len(cs.gets) > 1 {
			resp = cs.gets[0]
			cs.gets = cs.gets[1:]
		}
		cs.mu.Unlock()
		writeJSON(w, resp.status, resp.body)
		return
	case (r.Method == http.MethodPut || r.Method == http.MethodPost) && strings.HasPrefix(r.URL.Path, "/rest/api/content"):
		var resp cannedResp
		switch {
		case len(cs.writes) == 0:
			resp = cannedResp{status: http.StatusOK, body: cs.page}
		case len(cs.writes) == 1:
			resp = cs.writes[0] // last one sticks
		default:
			resp = cs.writes[0]
			cs.writes = cs.writes[1:]
		}
		cs.mu.Unlock()
		writeJSON(w, resp.status, resp.body)
		return
	default:
		cs.mu.Unlock()
		writeJSON(w, http.StatusOK, cs.page)
	}
}

// requests returns a copy of the captured requests.
func (cs *confServer) requests() []capturedReq {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	out := make([]capturedReq, len(cs.reqs))
	copy(out, cs.reqs)
	return out
}

// writeReqs returns the captured PUT/POST requests against /rest/api/content.
func (cs *confServer) writeReqs() []capturedReq {
	var out []capturedReq
	for _, r := range cs.requests() {
		if (r.method == http.MethodPut || r.method == http.MethodPost) && strings.HasPrefix(r.path, "/rest/api/content") {
			out = append(out, r)
		}
	}
	return out
}

func readAll(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// pageJSON builds a content JSON body for a page with the given id/title/version
// and CSF storage value.
func pageJSON(id, title string, version int, csf string) string {
	v := struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Title string `json:"title"`
		Space struct {
			Key string `json:"key"`
		} `json:"space"`
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
		Body struct {
			Storage struct {
				Value string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
	}{ID: id, Type: "page", Title: title}
	v.Space.Key = "ENG"
	v.Version.Number = version
	v.Body.Storage.Value = csf
	b, _ := json.Marshal(v)
	return string(b)
}

// putVersionNumber extracts version.number from a captured PUT/POST body.
func putVersionNumber(t *testing.T, body string) int {
	t.Helper()
	var p struct {
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("decode PUT body %q: %v", body, err)
	}
	return p.Version.Number
}

// putStorageValue extracts body.storage.value from a captured PUT/POST body.
func putStorageValue(t *testing.T, body string) string {
	t.Helper()
	var p struct {
		Body struct {
			Storage struct {
				Value string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("decode PUT body %q: %v", body, err)
	}
	return p.Body.Storage.Value
}

// --- helpers tying pull → locate .csf → edit → push together ---

const sampleCSF = "<p>Hello <strong>world</strong></p>"

// pullPage pulls id into mirror root `into` (a TempDir) and returns the absolute
// path to the written .csf, derived from the emitted PullResult.Path (relative
// to root). It asserts exit 0.
func pullPage(t *testing.T, cs *confServer, into, id string) string {
	t.Helper()
	out, code := runCLI(t, confEnv(cs.srv), "conf", "pull", "--id", id, "--into", into)
	if code != exitOK {
		t.Fatalf("conf pull: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res app.PullResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode pull result: %v\n%s", err, out)
	}
	if len(res.Pages) != 1 {
		t.Fatalf("expected 1 pulled page, got %d: %+v", len(res.Pages), res.Pages)
	}
	return filepath.Join(into, res.Pages[0].Path)
}

// --- pull ---

// TestConfPull_ByteStableAndSidecar locks the read path: the .csf written to disk
// must be byte-identical to the server's body.storage.value (no Markdown
// round-trip), the .md view exists (best-effort), and the sidecar records
// SyncedVersion == the server version.
func TestConfPull_ByteStableAndSidecar(t *testing.T) {
	cs := newConfServer(t)
	const ver = 7
	cs.page = pageJSON("12345", "Design Doc", ver, sampleCSF)
	into := t.TempDir()

	csfPath := pullPage(t, cs, into, "12345")

	// .csf is byte-identical to the server storage value.
	gotCSF, err := os.ReadFile(csfPath)
	if err != nil {
		t.Fatalf("read .csf: %v", err)
	}
	if string(gotCSF) != sampleCSF {
		t.Fatalf("pulled .csf not byte-stable:\n got %q\nwant %q", gotCSF, sampleCSF)
	}

	// .md view exists alongside.
	mdPath := strings.TrimSuffix(csfPath, ".csf") + ".md"
	if _, err := os.Stat(mdPath); err != nil {
		t.Errorf(".md view missing: %v", err)
	}

	// Sidecar records SyncedVersion == server version.
	scPath := filepath.Join(into, ".atl", "state.json")
	scb, err := os.ReadFile(scPath)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var sc struct {
		Pages map[string]struct {
			Version int `json:"version"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(scb, &sc); err != nil {
		t.Fatalf("decode sidecar: %v", err)
	}
	if sc.Pages["12345"].Version != ver {
		t.Fatalf("sidecar SyncedVersion = %d, want %d", sc.Pages["12345"].Version, ver)
	}
}

// --- push: the core ---

// dirtyMirror pulls a page at version `synced`, then edits the .csf locally so it
// is dirty. Returns the mirror root and the .csf path. The server's page is left
// reporting version `synced` (so the pre-read/refresh see no drift) unless a test
// mutates cs.page afterward.
func dirtyMirror(t *testing.T, cs *confServer, synced int) (root, csfPath string) {
	t.Helper()
	root = t.TempDir()
	cs.page = pageJSON("12345", "Design Doc", synced, sampleCSF)
	csfPath = pullPage(t, cs, root, "12345")
	if err := os.WriteFile(csfPath, []byte(editedCSF), 0o644); err != nil {
		t.Fatalf("dirty the .csf: %v", err)
	}
	return root, csfPath
}

const editedCSF = "<p>Hello <strong>edited world</strong></p>"

// TestConfPush_HappyGateArithmetic is the headline test: a dirty page synced at
// v7 is pushed; the PUT must carry version.number == 8 (synced+1), the emitted
// PushResult must report Pushed=true and NewVersion==8.
func TestConfPush_HappyGateArithmetic(t *testing.T) {
	cs := newConfServer(t)
	const synced = 7
	root, csfPath := dirtyMirror(t, cs, synced)
	// PUT replies with the post-push content (version synced+1). After the PUT,
	// pushOne re-fetches via GET; cs.page still reports v7 — to keep the refresh
	// clean and the result deterministic, bump cs.page to the new version now.
	cs.page = pageJSON("12345", "Design Doc", synced+1, sampleCSF)
	cs.writes = []cannedResp{{status: http.StatusOK, body: pageJSON("12345", "Design Doc", synced+1, editedCSF)}}

	out, code := runCLI(t, confEnv(cs.srv), "conf", "push", csfPath, "--into", root)
	if code != exitOK {
		t.Fatalf("conf push: exit %d, want 0 (stdout=%q)", code, out)
	}

	var res app.PushResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode push result: %v\n%s", err, out)
	}
	if len(res.Items) != 1 {
		t.Fatalf("expected 1 item, got %d: %+v", len(res.Items), res.Items)
	}
	it := res.Items[0]
	if !it.Pushed || it.NewVersion != synced+1 {
		t.Fatalf("expected Pushed=true NewVersion=%d, got %+v", synced+1, it)
	}

	// Headline assertion: the gate arithmetic, locked on the wire.
	writes := cs.writeReqs()
	if len(writes) != 1 {
		t.Fatalf("expected exactly 1 write request, got %d: %+v", len(writes), writes)
	}
	if writes[0].method != http.MethodPut {
		t.Errorf("write method = %s, want PUT", writes[0].method)
	}
	if writes[0].path != "/rest/api/content/12345" {
		t.Errorf("write path = %s, want /rest/api/content/12345", writes[0].path)
	}
	if got := putVersionNumber(t, writes[0].body); got != synced+1 {
		t.Fatalf("PUT version.number = %d, want %d (synced+1)", got, synced+1)
	}
	// The pushed body must be the locally-edited CSF, verbatim (no conversion).
	if got := putStorageValue(t, writes[0].body); got != editedCSF {
		t.Fatalf("PUT storage value = %q, want %q (verbatim edited CSF)", got, editedCSF)
	}
}

// TestConfPush_ConflictExit5 covers the version gate refusing on a 409 PUT: the
// process exits 5 and nothing is reported as pushed. Per the adapter, a 409 maps
// to ErrVersionConflict, which pushOne records as Skipped="version-conflict".
func TestConfPush_ConflictExit5(t *testing.T) {
	cs := newConfServer(t)
	root, csfPath := dirtyMirror(t, cs, 7)
	cs.writes = []cannedResp{{status: http.StatusConflict, body: `{"message":"version conflict"}`}}

	out, code := runCLI(t, confEnv(cs.srv), "conf", "push", csfPath, "--into", root)
	if code != exitVersionConfl {
		t.Fatalf("conf push 409: exit %d, want %d (stdout=%q)", code, exitVersionConfl, out)
	}
	var res app.PushResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode push result: %v\n%s", err, out)
	}
	it := res.Items[0]
	if it.Pushed {
		t.Errorf("nothing should be reported pushed on a conflict, got %+v", it)
	}
	if it.Skipped != "version-conflict" {
		t.Errorf("expected Skipped=version-conflict, got %+v", it)
	}
}

// TestConfPush_ForceTargetsCurrentPlusOne covers --force: the server reports a
// drifted current version on the adapter's pre-read GET (?expand=version); the
// PUT must target current+1, overriding the synced-based gate. Exit 0.
func TestConfPush_ForceTargetsCurrentPlusOne(t *testing.T) {
	cs := newConfServer(t)
	const synced = 7
	const current = 9 // remote drifted past synced
	root, csfPath := dirtyMirror(t, cs, synced)
	// All GETs (the force pre-read and the post-push refresh) report current=9.
	cs.page = pageJSON("12345", "Design Doc", current, sampleCSF)
	cs.writes = []cannedResp{{status: http.StatusOK, body: pageJSON("12345", "Design Doc", current+1, editedCSF)}}

	out, code := runCLI(t, confEnv(cs.srv), "conf", "push", csfPath, "--into", root, "--force")
	if code != exitOK {
		t.Fatalf("conf push --force: exit %d, want 0 (stdout=%q)", code, out)
	}

	writes := cs.writeReqs()
	if len(writes) != 1 {
		t.Fatalf("expected 1 write, got %d: %+v", len(writes), writes)
	}
	if got := putVersionNumber(t, writes[0].body); got != current+1 {
		t.Fatalf("forced PUT version.number = %d, want %d (current+1)", got, current+1)
	}
}

// TestConfPush_InvalidCSFGate locks "HasErrors is the push gate": an invalid .csf
// (unbalanced tags) must fail validation BEFORE any HTTP write. The stateful
// server asserts no PUT/POST was ever received, and the output reports the
// problem (INVALID / Problems).
func TestConfPush_InvalidCSFGate(t *testing.T) {
	cs := newConfServer(t)
	root, csfPath := dirtyMirror(t, cs, 7)
	// Overwrite with malformed CSF (unbalanced tag).
	if err := os.WriteFile(csfPath, []byte("<p>unbalanced <strong>oops</p>"), 0o644); err != nil {
		t.Fatalf("write invalid csf: %v", err)
	}

	out, code := runCLI(t, confEnv(cs.srv), "conf", "push", csfPath, "--into", root)
	if code != exitGeneric {
		t.Fatalf("invalid CSF push: exit %d, want %d (stdout=%q)", code, exitGeneric, out)
	}

	// No write must have reached the server.
	if writes := cs.writeReqs(); len(writes) != 0 {
		t.Fatalf("validation gate breached: %d write(s) sent: %+v", len(writes), writes)
	}

	var res app.PushResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode push result: %v\n%s", err, out)
	}
	it := res.Items[0]
	if it.Pushed {
		t.Errorf("invalid CSF must not be pushed, got %+v", it)
	}
	if len(it.Problems) == 0 {
		t.Errorf("expected Problems to be reported for invalid CSF, got %+v", it)
	}
	if !csfHasErr(it.Problems) {
		t.Errorf("expected an error-severity problem, got %+v", it.Problems)
	}
}

// TestConfPush_DryRunNoWrite covers --dry-run: no PUT is sent, the item reports
// DryRun=true, drift is surfaced when the remote moved past synced, exit 0.
func TestConfPush_DryRunNoWrite(t *testing.T) {
	cs := newConfServer(t)
	const synced = 7
	root, csfPath := dirtyMirror(t, cs, synced)
	// Remote drifted to v9; the dry-run drift probe (GET) should surface it.
	cs.page = pageJSON("12345", "Design Doc", 9, sampleCSF)

	out, code := runCLI(t, confEnv(cs.srv), "conf", "push", csfPath, "--into", root, "--dry-run")
	if code != exitOK {
		t.Fatalf("conf push --dry-run: exit %d, want 0 (stdout=%q)", code, out)
	}
	if writes := cs.writeReqs(); len(writes) != 0 {
		t.Fatalf("dry-run must not write, got %d write(s): %+v", len(writes), writes)
	}

	var res app.PushResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode push result: %v\n%s", err, out)
	}
	it := res.Items[0]
	if !it.DryRun {
		t.Errorf("expected DryRun=true, got %+v", it)
	}
	if it.Pushed {
		t.Errorf("dry-run must not push, got %+v", it)
	}
	if !it.Drifted {
		t.Errorf("dry-run should surface remote drift (synced %d, remote 9), got %+v", synced, it)
	}
}

// TestConfPush_RefreshFailureIsWarning covers the documented "refresh failure is
// a warning, not an error" semantic: the PUT succeeds (200) but the follow-up
// re-fetch GET fails (500), yet the push still exits 0 and the item carries a
// Warning.
//
// Confirmed from internal/app/confluence.go pushOne: after a successful
// UpdatePage it calls store.GetPage to refresh the mirror; a GetPage error sets
// item.Warning and returns a nil error.
func TestConfPush_RefreshFailureIsWarning(t *testing.T) {
	cs := newConfServer(t)
	const synced = 7
	root, csfPath := dirtyMirror(t, cs, synced)
	// dirtyMirror's pull already consumed the initial GET. Now make every
	// subsequent GET fail with 500 (the sticky single-entry queue keeps failing
	// across httpx's idempotent-GET retries), so the post-push refresh fetch
	// surfaces as an error rather than silently succeeding on a retry.
	cs.gets = []cannedResp{{status: http.StatusInternalServerError, body: `{"message":"boom"}`}}
	cs.writes = []cannedResp{{status: http.StatusOK, body: pageJSON("12345", "Design Doc", synced+1, editedCSF)}}

	out, code := runCLI(t, confEnv(cs.srv), "conf", "push", csfPath, "--into", root)
	if code != exitOK {
		t.Fatalf("push with failed refresh: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res app.PushResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode push result: %v\n%s", err, out)
	}
	it := res.Items[0]
	if !it.Pushed || it.NewVersion != synced+1 {
		t.Fatalf("expected Pushed=true NewVersion=%d, got %+v", synced+1, it)
	}
	if it.Warning == "" {
		t.Errorf("expected a Warning for the failed post-push refresh, got %+v", it)
	}
}

// --- -o text rendering ---

// TestConfPush_TextHappy goldens the pushText output for a successful push.
func TestConfPush_TextHappy(t *testing.T) {
	cs := newConfServer(t)
	const synced = 7
	root, csfPath := dirtyMirror(t, cs, synced)
	cs.page = pageJSON("12345", "Design Doc", synced+1, sampleCSF)
	cs.writes = []cannedResp{{status: http.StatusOK, body: pageJSON("12345", "Design Doc", synced+1, editedCSF)}}

	out, _, code := runCLIFull(t, confEnv(cs.srv), "conf", "push", csfPath, "--into", root, "-o", "text")
	if code != exitOK {
		t.Fatalf("conf push -o text: exit %d, want 0 (stdout=%q)", code, out)
	}
	// The path embeds a TempDir; reduce to the basename line for a host-independent
	// golden ("pushed v8\t<path>"). Assert the state token directly instead.
	if !strings.HasPrefix(out, "pushed v8\t") {
		t.Fatalf("text output = %q, want prefix %q", out, "pushed v8\t")
	}
	assertGolden(t, "conf_push_text_happy.txt", []byte(maskPath(out, csfPath)))
}

// TestConfPush_TextInvalid goldens the pushText output for an invalid push.
func TestConfPush_TextInvalid(t *testing.T) {
	cs := newConfServer(t)
	root, csfPath := dirtyMirror(t, cs, 7)
	if err := os.WriteFile(csfPath, []byte("<p>bad <strong>x</p>"), 0o644); err != nil {
		t.Fatalf("write invalid csf: %v", err)
	}

	out, _, code := runCLIFull(t, confEnv(cs.srv), "conf", "push", csfPath, "--into", root, "-o", "text")
	if code != exitGeneric {
		t.Fatalf("conf push -o text (invalid): exit %d, want %d (stdout=%q)", code, exitGeneric, out)
	}
	if !strings.HasPrefix(out, "INVALID\t") {
		t.Fatalf("text output = %q, want prefix %q", out, "INVALID\t")
	}
	assertGolden(t, "conf_push_text_invalid.txt", []byte(maskPath(out, csfPath)))
}

// maskPath replaces the volatile absolute .csf path (under a TempDir) with a
// stable token so the golden is host-independent.
func maskPath(s, csfPath string) string {
	return strings.ReplaceAll(s, csfPath, "<CSF>")
}

// TestConfPush_MissingTargetNoPanic pins the nil-result guard: a push target
// that cannot be stat'ed must not panic in -o text (pushText(nil)), must not
// print a stray "null" in JSON mode, and must exit 4 (not found), not 1.
func TestConfPush_MissingTargetNoPanic(t *testing.T) {
	cs := newConfServer(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist.csf")
	for _, format := range []string{"json", "text"} {
		out, code := runCLI(t, confEnv(cs.srv), "conf", "push", missing, "-o", format)
		if code != exitNotFound {
			t.Fatalf("-o %s: exit %d, want %d", format, code, exitNotFound)
		}
		if strings.TrimSpace(out) != "" {
			t.Fatalf("-o %s: expected empty stdout, got %q", format, out)
		}
	}
}
