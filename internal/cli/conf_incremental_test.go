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
		case "/rest/api/content/search":
			fmt.Fprint(w, `{"results":[{"id":"100","type":"page","title":"Alpha","space":{"key":"ENG"},"version":{"number":3,"when":"2026-07-13T12:34:56Z"},"ancestors":[],"_links":{"webui":"/spaces/ENG/pages/100"}}],"start":0,"limit":1,"size":1,"totalSize":1,"_links":{}}`)
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

func TestConfPullCompleteGoldenAndReadOnly(t *testing.T) {
	srv, requests := incrementalConfServer(t)
	defer srv.Close()
	root := t.TempDir()
	out, code := runCLI(t, confEnv(srv), "--read-only", "conf", "pull", "--complete", "--cql", "space=ENG and type=page", "--into", root)
	if code != exitOK {
		t.Fatalf("exit=%d out=%q", code, out)
	}
	assertGolden(t, "conf_pull_complete.json", normalizeIncrementalCLI(out, root))
	searches := 0
	for _, request := range *requests {
		if !strings.HasPrefix(request, http.MethodGet+" ") {
			t.Fatalf("complete pull made non-GET request: %s", request)
		}
		if strings.Contains(request, "/rest/api/content/search?") {
			searches++
		}
	}
	if searches != 2 {
		t.Fatalf("complete pull search requests=%d want=2; all=%v", searches, *requests)
	}
}

func TestConfPullCompleteTextGolden(t *testing.T) {
	srv, _ := incrementalConfServer(t)
	defer srv.Close()
	root := t.TempDir()
	out, code := runCLI(t, confEnv(srv), "conf", "pull", "--complete", "--space", "ENG", "--into", root, "-o", "text")
	if code != exitOK {
		t.Fatalf("exit=%d out=%q", code, out)
	}
	assertGolden(t, "conf_pull_complete.txt", normalizeIncrementalCLI(out, root))
}

func TestConfPullOrdinarySchedulingAndExplicitDefaults(t *testing.T) {
	for _, tc := range []struct {
		name           string
		flags          []string
		wantScheduling string
	}{
		{name: "prefetch", flags: []string{"--page-prefetch", "2"}, wantScheduling: `"page_prefetch": 2`},
		{name: "rate only", flags: []string{"--requests-per-second", "1000"}, wantScheduling: `"max_in_flight": 1`},
		{name: "explicit defaults", flags: []string{"--page-prefetch", "1", "--requests-per-second", "0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := incrementalConfServer(t)
			defer srv.Close()
			args := []string{"conf", "pull", "--cql", "space=ENG and type=page", "--into", t.TempDir()}
			args = append(args, tc.flags...)
			out, code := runCLI(t, confEnv(srv), args...)
			if code != exitOK {
				t.Fatalf("exit=%d out=%q", code, out)
			}
			if tc.wantScheduling == "" {
				if strings.Contains(out, `"scheduling"`) {
					t.Fatalf("explicit default flags installed/reported a scheduler: %s", out)
				}
			} else if !strings.Contains(out, `"scheduling"`) || !strings.Contains(out, tc.wantScheduling) {
				t.Fatalf("scheduled ordinary pull omitted policy %q: %s", tc.wantScheduling, out)
			}
		})
	}
}

func TestConfPullIncrementalFlagsFailBeforeConfig(t *testing.T) {
	for _, args := range [][]string{
		{"conf", "pull", "--incremental", "--id", "100"},
		{"conf", "pull", "--incremental"},
		{"conf", "pull", "--cql", "type=page", "--since", "2026-07-13T12:00:00Z"},
		{"conf", "pull", "--cql", "type=page", "--time-zone", "UTC"},
		{"conf", "pull", "--incremental", "--cql", "type=page", "--time-zone", ""},
		{"conf", "pull", "--incremental", "--cql", "type=page", "--max-pages", "-1"},
		{"conf", "pull", "--incremental", "--cql", "type=page", "--time-zone", "UTC"},
		{"conf", "pull", "--incremental", "--space", "ENG", "--depth", "2"},
		{"conf", "pull", "--complete", "--id", "100"},
		{"conf", "pull", "--complete"},
		{"conf", "pull", "--complete", "--incremental", "--cql", "type=page"},
		{"conf", "pull", "--restart-complete", "--cql", "type=page"},
		{"conf", "pull", "--complete", "--cql", "type=page", "--max-pages", "-1"},
		{"conf", "pull", "--complete", "--cql", "type=page", "--since", "2026-07-13T12:00:00Z"},
		{"conf", "pull", "--complete", "--space", "ENG", "--depth", "2"},
		{"conf", "pull", "--id", "100", "--page-prefetch", "2"},
		{"conf", "pull", "--complete", "--cql", "type=page", "--page-prefetch", "0"},
		{"conf", "pull", "--complete", "--cql", "type=page", "--page-prefetch", "9"},
		{"conf", "pull", "--complete", "--cql", "type=page", "--requests-per-second", "-1"},
		{"conf", "pull", "--complete", "--cql", "type=page", "--requests-per-second", "1001"},
		{"conf", "pull", "--id", "100", "--attachments"},
		{"conf", "pull", "--complete", "--space", "ENG", "--attachments"},
		{"conf", "pull", "--attachment-bodies"},
		{"conf", "pull", "--attachment-media-type", "text/plain"},
		{"conf", "pull", "--complete", "--space", "ENG", "--attachments", "--max-attachment-pages-per-page", "1", "--max-attachments-per-page", "1", "--attachment-bodies"},
		{"conf", "pull", "--complete", "--space", "ENG", "--attachments", "--max-attachment-pages-per-page", "1", "--max-attachments-per-page", "1", "--attachment-media-type", "text/plain"},
		{"conf", "pull", "--complete", "--space", "ENG", "--attachments", "--max-attachment-pages-per-page", "101", "--max-attachments-per-page", "1"},
		{"conf", "pull", "--complete", "--space", "ENG", "--attachments", "--max-attachment-pages-per-page", "1", "--max-attachments-per-page", "10001"},
		{"conf", "pull", "--complete", "--space", "ENG", "--attachments", "--max-attachment-pages-per-page", "1", "--max-attachments-per-page", "1", "--attachment-bodies", "--attachment-media-type", "Text/Plain", "--max-attachment-bytes", "1", "--max-total-attachment-bytes", "1"},
		{"conf", "pull", "--complete", "--space", "ENG", "--attachments", "--max-attachment-pages-per-page", "1", "--max-attachments-per-page", "1", "--attachment-bodies", "--attachment-media-type", "text/plain", "--max-attachment-bytes", "2", "--max-total-attachment-bytes", "1"},
		{"conf", "pull", "--complete", "--space", "ENG", "--allow-partial-artifacts"},
	} {
		if _, code := runCLI(t, nil, args...); code != exitUsage {
			t.Fatalf("args=%v exit=%d", args, code)
		}
	}
}
