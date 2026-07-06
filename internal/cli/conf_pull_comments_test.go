package cli

import (
	"net/http"
	"strings"
	"testing"
)

// commentsJSON is a deterministic two-comment listing for the /child/comment
// endpoint (no host data, no volatile fields).
const commentsJSON = `{"results":[
	{"id":"c1","history":{"createdDate":"2026-01-01T00:00:00.000Z","createdBy":{"displayName":"Alice"}},"body":{"storage":{"value":"<p>first</p>"}}},
	{"id":"c2","history":{"createdDate":"2026-01-02T00:00:00.000Z","createdBy":{"displayName":"Bob"}},"body":{"storage":{"value":"<p>second</p>"}}}
],"_links":{}}`

// TestConfPullComments_Golden pins the `conf pull --comments` JSON result shape
// (per-page comment count). The volatile mirror root is masked.
func TestConfPullComments_Golden(t *testing.T) {
	cs := newConfServer(t)
	cs.page = pageJSON("100", "Alpha", 3, sampleCSF)
	cs.comments = commentsJSON

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
	cs.comments = commentsJSON

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
}

// With --comments the CLI fetches the comment endpoint exactly once for a
// single-page pull.
func TestConfPull_CommentsHitsCommentEndpoint(t *testing.T) {
	cs := newConfServer(t)
	cs.page = pageJSON("100", "Alpha", 3, sampleCSF)
	cs.comments = commentsJSON

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
	if n != 1 {
		t.Errorf("expected exactly 1 comment fetch, got %d", n)
	}
}
