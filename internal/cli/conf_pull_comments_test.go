package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

// commentsJSON is a deterministic two-comment listing for the /child/comment
// endpoint (no host data, no volatile fields).
const commentsJSON = `{"results":[
	{"id":"c1","type":"comment","history":{"createdDate":"2026-01-01T00:00:00.000Z","createdBy":{"userKey":"u1","displayName":"Alice"}},"version":{"number":1,"when":"2026-01-01T00:00:00.000Z"},"ancestors":[{"id":"100","type":"page"}],"body":{"storage":{"value":"<p>first</p>","representation":"storage"}},"extensions":{"location":"footer"}},
	{"id":"c2","type":"comment","history":{"createdDate":"2026-01-02T00:00:00.000Z","createdBy":{"userKey":"u2","displayName":"Bob"}},"version":{"number":1,"when":"2026-01-02T00:00:00.000Z"},"ancestors":[{"id":"100","type":"page"}],"body":{"storage":{"value":"<p>second</p>","representation":"storage"}},"extensions":{"location":"footer"}}
],"start":0,"limit":100,"size":2,"_links":{}}`

func qualifiedCommentResponses() map[string]string {
	empty := `{"results":[],"start":0,"limit":100,"size":0,"_links":{}}`
	return map[string]string{"footer": commentsJSON, "inline": empty, "resolved": empty}
}

// TestConfPullComments_Golden pins the `conf pull --comments` JSON result shape
// (per-page comment count). The volatile mirror root is masked.
func TestConfPullComments_Golden(t *testing.T) {
	cs := newConfServer(t)
	cs.page = pageJSON("100", "Alpha", 3, sampleCSF)
	cs.commentsByLocation = qualifiedCommentResponses()

	into := t.TempDir()
	out, code := runCLI(t, confEnv(cs.srv), "conf", "pull", "--id", "100", "--into", into, "--comments")
	if code != exitOK {
		t.Fatalf("conf pull --comments: exit %d, want 0 (stdout=%q)", code, out)
	}
	assertGolden(t, "conf_pull_comments.json", []byte(strings.ReplaceAll(out, into, "<ROOT>")))
}

// Without --comments the CLI must never contact the comment endpoint (identical
// HTTP traffic to today).
func TestConfPull_NoCommentsNoCommentRequest(t *testing.T) {
	cs := newConfServer(t)
	cs.page = pageJSON("100", "Alpha", 3, sampleCSF)
	cs.commentsByLocation = qualifiedCommentResponses()

	into := t.TempDir()
	out, code := runCLI(t, confEnv(cs.srv), "conf", "pull", "--id", "100", "--into", into)
	if code != exitOK {
		t.Fatalf("conf pull: exit %d, want 0 (stdout=%q)", code, out)
	}
	for _, r := range cs.requests() {
		if r.method == http.MethodGet && strings.HasSuffix(r.path, "/child/comment") {
			t.Errorf("pull without --comments hit the comment endpoint: %+v", r)
		}
	}
	var result app.PullResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode pull result: %v", err)
	}
	for _, include := range result.Includes {
		if include.Requested || include.Qualification != app.ConfluencePullIncludeNotRequested || include.Complete != nil || include.Reason != "" {
			t.Fatalf("unrequested include=%+v, want not_requested", include)
		}
	}
}

func TestConfPullPreviewDefersIncludesWithoutCommentOrAssetGETs(t *testing.T) {
	cs := newConfServer(t)
	cs.page = pageJSON("100", "Alpha", 3, `<p>image</p><ac:image><ri:attachment ri:filename="image.png"/></ac:image>`)
	cs.commentsByLocation = qualifiedCommentResponses()

	out, code := runCLI(t, confEnv(cs.srv), "conf", "pull", "--id", "100", "--into", t.TempDir(), "--dry-run", "--assets", "--comments")
	if code != exitOK {
		t.Fatalf("preview exit=%d stdout=%q", code, out)
	}
	var result app.PullResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode preview result: %v", err)
	}
	for _, include := range result.Includes {
		if !include.Requested || include.Qualification != app.ConfluencePullIncludeDeferred || include.Complete != nil || include.Reason != app.ConfluencePullIncludeReasonPreviewDeferred {
			t.Fatalf("preview include=%+v, want deferred", include)
		}
	}
	for _, request := range cs.requests() {
		if strings.Contains(request.path, "/child/comment") || strings.Contains(request.path, "/download/attachments/") {
			t.Fatalf("preview made deferred include GET: %+v", request)
		}
	}
}

func TestConfPullPreviewTextQualifiesDeferredIncludes(t *testing.T) {
	cs := newConfServer(t)
	cs.page = pageJSON("100", "Alpha", 3, sampleCSF)
	out, code := runCLI(t, confEnv(cs.srv), "conf", "pull", "--id", "100", "--into", t.TempDir(), "--dry-run", "--assets", "--comments", "-o", "text")
	if code != exitOK || !strings.Contains(out, "include: assets requested=true qualification=deferred reason=preview_deferred") ||
		!strings.Contains(out, "include: comments requested=true qualification=deferred reason=preview_deferred") {
		t.Fatalf("preview text exit=%d output=%q", code, out)
	}
}

func TestConfPullFailedIncludeEmitsQualifiedResultBeforeError(t *testing.T) {
	cs := newConfServer(t)
	cs.page = pageJSON("100", "Alpha", 3, sampleCSF)
	cs.commentsByLocation = map[string]string{
		"footer":   "{",
		"inline":   `{"results":[],"start":0,"limit":100,"size":0,"_links":{}}`,
		"resolved": `{"results":[],"start":0,"limit":100,"size":0,"_links":{}}`,
	}

	out, _, code := runCLIFull(t, confEnv(cs.srv), "conf", "pull", "--id", "100", "--into", t.TempDir(), "--comments")
	if code != exitGeneric {
		t.Fatalf("failed include exit=%d stdout=%q", code, out)
	}
	var result app.PullResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode failed-include result: %v\n%s", err, out)
	}
	if !result.HasFailedInclude() {
		t.Fatalf("result=%+v, want failed include", result)
	}
	if result.LocalSafety != nil {
		t.Fatalf("failed optional read added local_safety: %+v", result.LocalSafety)
	}
}

func TestConfPullAssetPublicationFailureEmitsQualifiedResultBeforeError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce the chmod-based publication fault fixture")
	}
	root := t.TempDir()
	pageDir := filepath.Join(root, "ENG", "alpha")
	if err := os.MkdirAll(pageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(pageDir, 0o755) })
	page := pageJSON("100", "Alpha", 3, `<p>image</p><ac:image><ri:attachment ri:filename="image.png"/></ac:image>`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/download/attachments/") {
			if err := os.Chmod(pageDir, 0o555); err != nil {
				http.Error(w, "synthetic setup failed", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte("image bytes"))
			return
		}
		if strings.Contains(r.URL.Path, "/child/comment") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[],"start":0,"limit":100,"size":0,"_links":{}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)

	out, stderr, code := runCLIFull(t, confEnv(srv), "conf", "pull", "--id", "100", "--into", root, "--assets", "--comments")
	if code == exitOK {
		t.Fatalf("publication failure exited 0: stdout=%q stderr=%q", out, stderr)
	}
	var result app.PullResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode asset publication failure: %v\n%s", err, out)
	}
	for _, include := range result.Includes {
		if include.Qualification != app.ConfluencePullIncludeFailed || include.Complete == nil || *include.Complete ||
			include.Reason != app.ConfluencePullIncludeReasonStagingFailed {
			t.Fatalf("staged include=%+v stdout=%q stderr=%q", include, out, stderr)
		}
	}
	if result.LocalSafety != nil {
		t.Fatalf("asset publication failure added local_safety: %+v", result.LocalSafety)
	}
}

func TestConfPullCommentPublicationFailureEmitsQualifiedResultBeforeError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce the chmod-based publication fault fixture")
	}
	root := t.TempDir()
	pageDir := filepath.Join(root, "ENG", "alpha")
	if err := os.MkdirAll(pageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(pageDir, 0o755) })
	page := pageJSON("100", "Alpha", 3, sampleCSF)
	commentReads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/child/comment") {
			commentReads++
			if commentReads == 3 {
				if err := os.Chmod(pageDir, 0o555); err != nil {
					http.Error(w, "synthetic setup failed", http.StatusInternalServerError)
					return
				}
			}
			_, _ = w.Write([]byte(`{"results":[],"start":0,"limit":100,"size":0,"_links":{}}`))
			return
		}
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)

	out, stderr, code := runCLIFull(t, confEnv(srv), "conf", "pull", "--id", "100", "--into", root, "--comments")
	if code == exitOK {
		t.Fatalf("publication failure exited 0: stdout=%q stderr=%q", out, stderr)
	}
	var result app.PullResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode comment publication failure: %v\n%s", err, out)
	}
	comments := result.Includes[1]
	if comments.Dimension != app.ConfluencePullIncludeComments || comments.Qualification != app.ConfluencePullIncludeFailed ||
		comments.Complete == nil || *comments.Complete || comments.Reason != app.ConfluencePullIncludeReasonStagingFailed {
		t.Fatalf("comments include=%+v stdout=%q stderr=%q", comments, out, stderr)
	}
	if result.Includes[0].Qualification != app.ConfluencePullIncludeNotRequested || result.LocalSafety != nil {
		t.Fatalf("unrelated result state=%+v", result)
	}
}

func TestConfPull_LocalSafetyResultPrecedesExitEight(t *testing.T) {
	cs := newConfServer(t)
	cs.page = pageJSON("100", "Alpha", 3, sampleCSF)
	root := t.TempDir()
	out, code := runCLI(t, confEnv(cs.srv), "conf", "pull", "--id", "100", "--into", root)
	if code != exitOK {
		t.Fatalf("initial pull exit=%d stdout=%q", code, out)
	}
	var initial app.PullResult
	if err := json.Unmarshal([]byte(out), &initial); err != nil || len(initial.Pages) != 1 {
		t.Fatalf("initial result=%+v err=%v", initial, err)
	}
	csfPath := filepath.Join(root, filepath.FromSlash(initial.Pages[0].Path))
	if err := os.WriteFile(csfPath, []byte("<p>local edit</p>"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, stderr, code := runCLIFull(t, confEnv(cs.srv), "conf", "pull", "--id", "100", "--into", root)
	if code != exitCheckFailed {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	var result app.PullResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode qualified stdout: %v\n%s", err, out)
	}
	if result.LocalSafety == nil || result.LocalSafety.Blocked != 1 {
		t.Fatalf("result=%+v", result)
	}
	if got, err := os.ReadFile(csfPath); err != nil || string(got) != "<p>local edit</p>" {
		t.Fatalf("csf=%q err=%v", got, err)
	}
}

func TestConfPullDryRunTextIncludesPageStatus(t *testing.T) {
	cs := newConfServer(t)
	cs.page = pageJSON("100", "Alpha", 3, sampleCSF)
	out, code := runCLI(t, confEnv(cs.srv), "conf", "pull", "--id", "100", "--into", filepath.Join(t.TempDir(), "absent"), "--dry-run", "-o", "text")
	if code != exitOK || !strings.Contains(out, "would_pull") || !strings.Contains(out, "local-safety: complete=true dry_run=true") {
		t.Fatalf("exit=%d output=%q", code, out)
	}
}

// With --comments the CLI exhausts the three fixed qualified selectors for a
// single-page pull.
func TestConfPull_CommentsHitsCommentEndpoint(t *testing.T) {
	cs := newConfServer(t)
	cs.page = pageJSON("100", "Alpha", 3, sampleCSF)
	cs.commentsByLocation = qualifiedCommentResponses()

	into := t.TempDir()
	if _, code := runCLI(t, confEnv(cs.srv), "conf", "pull", "--id", "100", "--into", into, "--comments"); code != exitOK {
		t.Fatalf("conf pull --comments: exit %d", code)
	}
	n := 0
	for _, r := range cs.requests() {
		if r.method == http.MethodGet && strings.HasSuffix(r.path, "/child/comment") {
			n++
		}
	}
	if n != 3 {
		t.Errorf("expected exactly 3 qualified comment fetches, got %d", n)
	}
}

// Attachment capture is deliberately separate from rendered image assets: a
// complete pull takes one bounded inventory read, revalidates its exact
// filename selector immediately before the body GET, optionally streams only
// an allowlisted body, and reports only a qualified count in its public result.
func TestConfPullCompleteAttachmentsCapturesBoundedArtifact(t *testing.T) {
	cs := newConfServer(t)
	cs.page = strings.Replace(pageJSON("100", "Alpha", 3, sampleCSF), `"ENG"`, `"DOC"`, 1)
	cs.completeSearch = `{"results":[{"id":"100","type":"page","title":"Alpha","space":{"key":"DOC"},"version":{"number":3},"ancestors":[],"_links":{"webui":"/spaces/DOC/pages/100"}}],"start":0,"limit":100,"size":1,"totalCount":1,"_links":{}}`
	cs.attachments = `{"results":[{"id":"21","title":"manual.txt","metadata":{"mediaType":"text/plain"},"extensions":{"fileSize":3},"version":{"number":1},"history":{"createdDate":"2026-01-01T00:00:00Z","createdBy":{"userKey":"u1"}},"_links":{"download":"/download/attachments/100/manual.txt"}}],"start":0,"limit":200,"size":1,"_links":{}}`
	// Confluence-compatible child attachment listings can be terminal and
	// uniquely matched without echoing totalCount. The adapter must preserve
	// the exact selector gate without treating that optional aggregate as a
	// prerequisite for the binary GET.
	cs.attachmentRevalidation = `{"results":[{"id":"21","type":"attachment","title":"manual.txt","container":{"id":"100","type":"page"},"extensions":{"fileSize":3},"version":{"number":1}}],"start":0,"limit":2,"size":1,"totalCount":1,"_links":{}}`
	cs.attachmentBodies = map[string]string{"/download/attachments/100/manual.txt": "abc"}

	into := t.TempDir()
	out, code := runCLI(t, confEnv(cs.srv), "conf", "pull", "--complete", "--space", "DOC", "--into", into,
		"--attachments", "--max-attachment-pages-per-page", "1", "--max-attachments-per-page", "1",
		"--attachment-bodies", "--attachment-media-type", "text/plain", "--max-attachment-bytes", "16", "--max-total-attachment-bytes", "32")
	if code != exitOK {
		t.Fatalf("complete attachment pull: exit %d, want 0 (stdout=%q)", code, out)
	}
	if strings.Contains(out, "manual.txt") || strings.Contains(out, "download/attachments") {
		t.Fatalf("attachment provenance leaked into pull result: %q", out)
	}
	var result app.PullResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode pull result: %v", err)
	}
	if len(result.Pages) != 1 || result.Pages[0].Attachments == nil || *result.Pages[0].Attachments != 1 {
		t.Fatalf("pages=%+v", result.Pages)
	}
	if len(result.Includes) != 3 || result.Includes[2].Dimension != app.ConfluencePullIncludeAttachments ||
		result.Includes[2].Qualification != app.ConfluencePullIncludeQualified || result.Includes[2].Complete == nil || !*result.Includes[2].Complete {
		t.Fatalf("includes=%+v", result.Includes)
	}
	stem := strings.TrimSuffix(filepath.Join(into, filepath.FromSlash(result.Pages[0].Path)), ".csf")
	if _, err := os.Stat(stem + ".attachments.json"); err != nil {
		t.Fatalf("attachment sidecar: %v", err)
	}
	if data, err := os.ReadFile(stem + ".attachments/21.body"); err != nil || string(data) != "abc" {
		t.Fatalf("attachment body=%q error=%v", data, err)
	}
	searches, inventories, revalidations, bodies := 0, 0, 0, 0
	for _, request := range cs.requests() {
		if request.method != http.MethodGet {
			t.Fatalf("attachment pull made non-GET request: %+v", request)
		}
		switch {
		case request.path == "/rest/api/content/search":
			searches++
		case strings.HasSuffix(request.path, "/child/attachment"):
			if request.query == "expand=container%2Cextensions%2Cversion&filename=manual.txt&limit=2&start=0" {
				revalidations++
			} else {
				inventories++
			}
		case strings.HasPrefix(request.path, "/download/attachments/"):
			bodies++
		}
	}
	if searches != 2 || inventories != 1 || revalidations != 1 || bodies != 1 {
		t.Fatalf("requests: searches=%d inventories=%d revalidations=%d bodies=%d all=%+v", searches, inventories, revalidations, bodies, cs.requests())
	}
}
