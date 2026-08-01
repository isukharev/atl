package cli

import (
	"net/http"
	"strings"
	"testing"
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
