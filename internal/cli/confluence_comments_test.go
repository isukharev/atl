package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
