package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

func qualifiedCommentServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	paths := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/child/comment") {
			if location := r.URL.Query().Get("location"); location == "footer" || location == "" {
				_, _ = w.Write([]byte(`{"results":[{
					"id":"101","type":"comment","status":"current",
					"history":{"createdDate":"2026-08-01T01:02:03.000Z","createdBy":{"userKey":"user-1","displayName":"Example User"}},
					"version":{"number":2,"when":"2026-08-01T01:03:04.000Z"},
					"ancestors":[],
					"body":{"storage":{"value":"<p>Footer note</p>","representation":"storage"}},
					"extensions":{"location":"footer"}
				}],"start":0,"limit":100,"size":1,"_links":{}}`))
				return
			}
			_, _ = w.Write([]byte(`{"results":[],"start":0,"limit":100,"size":0,"_links":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"12345","type":"page","title":"Synthetic Page","space":{"key":"ENG"},
			"version":{"number":7},"body":{"storage":{"value":"<p>Page body</p>","representation":"storage"}}
		}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

func TestConfCommentListQualifiedGolden(t *testing.T) {
	srv, paths := qualifiedCommentServer(t)
	out, code := runCLI(t, confEnv(srv), "conf", "comment", "list", "--id", "12345")
	if code != exitOK {
		t.Fatalf("conf comment list: exit %d (stdout=%q)", code, out)
	}
	assertGolden(t, "conf_comment_list_v2.json", []byte(out))
	if len(*paths) != 4 {
		t.Fatalf("request count = %d, want page + three fixed locations: %v", len(*paths), *paths)
	}
	for _, selector := range []string{"footer", "inline", "resolved"} {
		found := false
		for _, path := range *paths {
			if strings.Contains(path, "location="+selector) && strings.Contains(path, "depth=all") {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing fixed %s/depth=all query: %v", selector, *paths)
		}
	}
}

func TestConfCommentThreadReturnsExactRoot(t *testing.T) {
	srv, _ := qualifiedCommentServer(t)
	out, code := runCLI(t, confEnv(srv), "conf", "comment", "thread", "--id", "12345", "--comment-id", "101", "--expected-version", "7")
	if code != exitOK {
		t.Fatalf("conf comment thread: exit %d (stdout=%q)", code, out)
	}
	var result struct {
		PageVersionGated bool `json:"page_version_gated"`
		Count            int  `json:"count"`
		Query            struct {
			Mode      string `json:"mode"`
			CommentID string `json:"comment_id"`
		} `json:"query"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if !result.PageVersionGated || result.Count != 1 || result.Query.Mode != "thread" || result.Query.CommentID != "101" {
		t.Fatalf("thread contract = %s", out)
	}
}

func TestConfCommentListLegacyFlatPreservesOldShape(t *testing.T) {
	srv, paths := qualifiedCommentServer(t)
	out, code := runCLI(t, confEnv(srv), "conf", "comment", "list", "--id", "12345", "--legacy-flat")
	if code != exitOK {
		t.Fatalf("legacy conf comment list: exit %d (stdout=%q)", code, out)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result["comments"] == nil {
		t.Fatalf("legacy shape changed: %s", out)
	}
	if len(*paths) != 1 || !strings.HasSuffix(strings.Split((*paths)[0], "?")[0], "/child/comment") {
		t.Fatalf("legacy mode made unexpected requests: %v", *paths)
	}
}

func TestConfCommentValidationPrecedesConfiguration(t *testing.T) {
	tests := [][]string{
		{"conf", "comment", "list", "--id", "123", "--location", "else"},
		{"conf", "comment", "list", "--id", "123", "--state", "else"},
		{"conf", "comment", "list", "--id", "123", "--depth", "else"},
		{"conf", "comment", "list", "--id", "123", "--expected-version", "-1"},
		{"conf", "comment", "list", "--id", "123", "--legacy-flat", "--location", "all"},
		{"conf", "comment", "thread", "--id", "123", "--comment-id", "not-numeric"},
		{"conf", "comment", "thread", "--id", "123", "--comment-id", "1", "--expected-version", "-1"},
	}
	for _, args := range tests {
		out, code := runCLI(t, nil, args...)
		if code != exitUsage || out != "" {
			t.Fatalf("%v: exit=%d stdout=%q, want usage before config", args, code, out)
		}
	}
}

type guardedConfluenceCommentCLIServer struct {
	srv      *httptest.Server
	comments []string
	post     int
	reads    int
	postCode int
}

func newGuardedConfluenceCommentCLIServer(t *testing.T) *guardedConfluenceCommentCLIServer {
	t.Helper()
	state := &guardedConfluenceCommentCLIServer{comments: []string{"10"}}
	state.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/user/current":
			_, _ = w.Write([]byte(`{"userKey":"user-1","displayName":"Example User"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/content/12345":
			state.reads++
			_, _ = w.Write([]byte(`{"id":"12345","type":"page","title":"Synthetic Page","space":{"key":"DOC"},"version":{"number":7}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/child/comment"):
			state.reads++
			rows := make([]string, 0, len(state.comments))
			for _, id := range state.comments {
				body, actor := "<p>existing</p>", "user-2"
				if id != "10" {
					body, actor = "<p>reviewed</p>", "user-1"
				}
				rows = append(rows, `{"id":"`+id+`","type":"comment","status":"current","history":{"createdDate":"2026-08-01T01:02:03.000Z","createdBy":{"userKey":"`+actor+`","displayName":"Example User"}},"version":{"number":1,"when":"2026-08-01T01:02:03.000Z"},"ancestors":[{"id":"12345","type":"page"}],"body":{"storage":{"value":"`+body+`","representation":"storage"}},"extensions":{"location":"footer"}}`)
			}
			_, _ = w.Write([]byte(`{"results":[` + strings.Join(rows, ",") + `],"start":0,"limit":100,"size":` +
				fmt.Sprint(len(rows)) + `,"_links":{}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/content":
			state.post++
			if state.postCode != 0 {
				state.comments = append(state.comments, "20")
				w.WriteHeader(state.postCode)
				return
			}
			state.comments = append(state.comments, "20")
			_, _ = w.Write([]byte(`{"id":"20"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(state.srv.Close)
	return state
}

func TestConfCommentGuardedPreviewAndApply(t *testing.T) {
	server := newGuardedConfluenceCommentCLIServer(t)
	body := writeConfluenceCommentBody(t, "<p>reviewed</p>")
	previewOut, code := runCLI(t, confEnv(server.srv), "--read-only", "conf", "comment", "preview", "--id", "12345", "--from-file", body)
	if code != exitOK || server.post != 0 || !strings.Contains(previewOut, `"status": "would_apply"`) {
		t.Fatalf("preview exit=%d post=%d out=%q", code, server.post, previewOut)
	}
	var preview struct {
		ProposalHash string `json:"proposal_hash"`
	}
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil || preview.ProposalHash == "" {
		t.Fatalf("preview contract=%q err=%v", previewOut, err)
	}
	applyOut, code := runCLI(t, confEnv(server.srv), "conf", "comment", "add", "--id", "12345", "--from-file", body,
		"--apply", "--expected-proposal-hash", preview.ProposalHash)
	if code != exitOK || server.post != 1 || !strings.Contains(applyOut, `"status": "applied"`) || !strings.Contains(applyOut, `"reconciled": true`) {
		t.Fatalf("apply exit=%d post=%d out=%q", code, server.post, applyOut)
	}
}

func TestConfCommentGuardedValidationAndPolicyPrecedeNetwork(t *testing.T) {
	server := newGuardedConfluenceCommentCLIServer(t)
	if _, code := runCLI(t, confEnv(server.srv), "conf", "comment", "add", "--id", "12345", "--from-file", "/definitely/missing/comment.csf", "--apply"); code != exitUsage || server.reads != 0 || server.post != 0 {
		t.Fatalf("missing hash exit=%d reads=%d post=%d", code, server.reads, server.post)
	}
	if _, code := runCLI(t, confEnv(server.srv), "--read-only", "conf", "comment", "add", "--id", "12345", "--from-file", "/definitely/missing/comment.csf"); code != exitCheckFailed || server.reads != 0 || server.post != 0 {
		t.Fatalf("read-only add exit=%d reads=%d post=%d", code, server.reads, server.post)
	}
	oversized := writeConfluenceCommentBytes(t, make([]byte, app.ConfluenceFooterCommentBodyMaxBytes+1))
	if _, code := runCLI(t, confEnv(server.srv), "conf", "comment", "preview", "--id", "12345", "--from-file", oversized); code != exitUsage || server.reads != 0 || server.post != 0 {
		t.Fatalf("oversized preview exit=%d reads=%d post=%d", code, server.reads, server.post)
	}
}

func writeConfluenceCommentBody(t *testing.T, body string) string {
	t.Helper()
	return writeConfluenceCommentBytes(t, []byte(body))
}

func writeConfluenceCommentBytes(t *testing.T, body []byte) string {
	t.Helper()
	path := t.TempDir() + "/comment.csf"
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
