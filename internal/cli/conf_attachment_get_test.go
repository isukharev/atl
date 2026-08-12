package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

func TestConfAttachmentGetReportsKnownPageNonExactIdentity(t *testing.T) {
	var requestURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/child/attachment"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"id":"21","title":"diagram.png","type":"attachment","version":{"number":3},"container":{"id":"12345","type":"page"}}],"totalCount":1,"start":0,"limit":2,"size":1,"_links":{}}`))
		case r.URL.Path == "/rest/api/content/21":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"21","title":"diagram.png","type":"attachment","version":{"number":2},"container":{"id":"12345","type":"page"}}`))
		case strings.HasPrefix(r.URL.Path, "/download/attachments/"):
			requestURI = r.URL.RequestURI()
			_, _ = w.Write([]byte("synthetic attachment bytes"))
		default:
			t.Fatalf("unexpected request=%q", r.URL.RequestURI())
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	out, code := runCLI(t, confEnv(srv), "conf", "attachment", "get", "--id", "12345",
		"--name", "diagram.png", "--version", "2", "--into", dir)
	if code != exitOK {
		t.Fatalf("exit=%d stdout=%q", code, out)
	}
	var result app.ConfluenceAttachmentDownloadResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, out)
	}
	if result.SchemaVersion != 1 || result.PageID != "12345" || result.Name != "diagram.png" ||
		result.OutputName != "diagram.png" || result.RequestedAttachmentVersion != 2 || result.ObservedAttachmentVersion != 2 ||
		result.ObservedAttachmentID != "21" || result.Selector != app.ConfluenceAttachmentSelectorVersion ||
		result.AttachmentIDBound || !result.IdentityRevalidated || result.PageVersionGated {
		t.Fatalf("result=%+v", result)
	}
	if requestURI != "/download/attachments/12345/diagram.png?version=2" {
		t.Fatalf("request URI=%q", requestURI)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "diagram.png")); err != nil || string(data) != "synthetic attachment bytes" {
		t.Fatalf("download=%q err=%v", data, err)
	}
}

func TestConfAttachmentGetPreservesCallerNameAndReportsFloatingLatest(t *testing.T) {
	var requestURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/child/attachment") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"id":"21","title":"nested/diagram.png","type":"attachment","version":{"number":4},"container":{"id":"12345","type":"page"}}],"totalCount":1,"start":0,"limit":2,"size":1,"_links":{}}`))
			return
		}
		requestURI = r.URL.RequestURI()
		_, _ = w.Write([]byte("latest bytes"))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	out, code := runCLI(t, confEnv(srv), "conf", "attachment", "get", "--id", "12345",
		"--name", "nested/diagram.png", "--into", dir)
	if code != exitOK {
		t.Fatalf("exit=%d stdout=%q", code, out)
	}
	var result app.ConfluenceAttachmentDownloadResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, out)
	}
	if result.Name != "nested/diagram.png" || result.OutputName != "diagram.png" ||
		result.RequestedAttachmentVersion != 0 || result.ObservedAttachmentVersion != 4 || result.ObservedAttachmentID != "21" ||
		result.Selector != app.ConfluenceAttachmentSelectorLatest || !result.IdentityRevalidated {
		t.Fatalf("result=%+v", result)
	}
	if requestURI != "/download/attachments/12345/nested%2Fdiagram.png?version=4" {
		t.Fatalf("request URI=%q", requestURI)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "diagram.png")); err != nil || string(data) != "latest bytes" {
		t.Fatalf("download=%q err=%v", data, err)
	}
}

func TestConfAttachmentGetFailedQualificationMakesNoBinaryRequestOrWrite(t *testing.T) {
	var binaryRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/download/attachments/") {
			binaryRequests++
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[],"totalCount":0,"start":0,"limit":2,"size":0,"_links":{}}`))
	}))
	t.Cleanup(srv.Close)
	root := filepath.Join(t.TempDir(), "not-created")
	out, code := runCLI(t, confEnv(srv), "conf", "attachment", "get", "--id", "12345", "--name", "missing.png", "--into", root)
	if code == exitOK || out != "" || binaryRequests != 0 {
		t.Fatalf("exit=%d stdout=%q binary_requests=%d", code, out, binaryRequests)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("failed qualification created output root: %v", err)
	}
}

func TestConfAttachmentGetRejectsNegativeVersionBeforeConfiguration(t *testing.T) {
	out, code := runCLI(t, nil, "conf", "attachment", "get", "--id", "12345", "--name", "x", "--version", "-1")
	if code != exitUsage || out != "" {
		t.Fatalf("exit=%d stdout=%q", code, out)
	}
}
