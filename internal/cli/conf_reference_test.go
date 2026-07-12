package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestConfPageResolveCanonicalGolden(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer srv.Close()
	out, code := runCLI(t, confEnv(srv), "conf", "page", "resolve", srv.URL+"/spaces/ENG/pages/42/Page")
	if code != exitOK || requests.Load() != 0 {
		t.Fatalf("exit=%d requests=%d output=%s", code, requests.Load(), out)
	}
	assertGolden(t, "conf_page_resolve.json", []byte(out))
}

func TestConfPageResolveShortAndViewURL(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/x/AwAG":
			http.Redirect(w, r, "/pages/viewpage.action?pageId=42", http.StatusFound)
		case "/pages/viewpage.action":
			_, _ = w.Write([]byte("page"))
		case "/rest/api/content/42":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"42","type":"page","title":"Example","space":{"key":"ENG"},"version":{"number":1},"ancestors":[],"body":{"storage":{"value":"<p>Hello</p>"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	out, code := runCLI(t, confEnv(srv), "-o", "id", "conf", "page", "resolve", "/x/AwAG")
	if code != exitOK || strings.TrimSpace(out) != "42" {
		t.Fatalf("resolve exit=%d output=%q", code, out)
	}
	view, code := runCLI(t, confEnv(srv), "conf", "page", "view", srv.URL+"/spaces/ENG/pages/42/Example")
	if code != exitOK || !strings.Contains(view, `"id": "42"`) || !strings.Contains(view, "Hello") {
		t.Fatalf("view exit=%d output=%s", code, view)
	}
	if strings.Join(paths, ",") != "/x/AwAG,/pages/viewpage.action,/rest/api/content/42" {
		t.Fatalf("paths=%v", paths)
	}
}

func TestConfPageResolveForeignURLFailsBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer srv.Close()
	_, code := runCLI(t, confEnv(srv), "conf", "page", "resolve", "https://foreign.example.test/x/AwAG")
	if code != exitUsage || requests.Load() != 0 {
		t.Fatalf("exit=%d requests=%d", code, requests.Load())
	}
}
