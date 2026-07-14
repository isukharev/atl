package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func incrementalConfServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	requests := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/search":
			fmt.Fprint(w, `{"results":[{"content":{"id":"100","type":"page","title":"Alpha","space":{"key":"ENG"},"version":{"number":3,"when":"2026-07-13T12:34:56Z"}}}],"size":1,"totalCount":1,"_links":{}}`)
		case "/rest/api/content/100":
			fmt.Fprint(w, `{"id":"100","type":"page","title":"Alpha","space":{"key":"ENG"},"version":{"number":3,"when":"2026-07-13T12:34:56Z"},"body":{"storage":{"value":"<p>alpha</p>"}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, &requests
}

func normalizeIncrementalCLI(value, root string) []byte {
	value = strings.ReplaceAll(value, root, "<ROOT>")
	value = regexp.MustCompile(`[0-9a-f]{64}`).ReplaceAllString(value, "<SHA256>")
	return []byte(value)
}

func TestConfPullIncrementalGoldenAndReadOnly(t *testing.T) {
	srv, requests := incrementalConfServer(t)
	defer srv.Close()
	root := t.TempDir()
	out, code := runCLI(t, confEnv(srv), "--read-only", "conf", "pull", "--incremental", "--cql", "space=ENG and type=page", "--since", "2026-07-13T12:00:00Z", "--into", root)
	if code != exitOK {
		t.Fatalf("exit=%d out=%q", code, out)
	}
	assertGolden(t, "conf_pull_incremental.json", normalizeIncrementalCLI(out, root))
	for _, request := range *requests {
		if !strings.HasPrefix(request, http.MethodGet+" ") {
			t.Fatalf("incremental pull made non-GET request: %s", request)
		}
	}
}

func TestConfPullIncrementalTextGolden(t *testing.T) {
	srv, _ := incrementalConfServer(t)
	defer srv.Close()
	root := t.TempDir()
	out, code := runCLI(t, confEnv(srv), "conf", "pull", "--incremental", "--space", "ENG", "--since", "2026-07-13T12:00:00Z", "--into", root, "-o", "text")
	if code != exitOK {
		t.Fatalf("exit=%d out=%q", code, out)
	}
	assertGolden(t, "conf_pull_incremental.txt", normalizeIncrementalCLI(out, root))
}

func TestConfPullIncrementalFlagsFailBeforeConfig(t *testing.T) {
	for _, args := range [][]string{
		{"conf", "pull", "--incremental", "--id", "100"},
		{"conf", "pull", "--incremental"},
		{"conf", "pull", "--cql", "type=page", "--since", "2026-07-13T12:00:00Z"},
		{"conf", "pull", "--cql", "type=page", "--time-zone", "UTC"},
		{"conf", "pull", "--incremental", "--cql", "type=page", "--max-pages", "-1"},
		{"conf", "pull", "--incremental", "--cql", "type=page", "--time-zone", "UTC"},
	} {
		if _, code := runCLI(t, nil, args...); code != exitUsage {
			t.Fatalf("args=%v exit=%d", args, code)
		}
	}
}
