package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

func TestConfAttachmentGetReportsKnownPageNonExactIdentity(t *testing.T) {
	var requestURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURI = r.URL.RequestURI()
		_, _ = w.Write([]byte("synthetic attachment bytes"))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	out, code := runCLI(t, confEnv(srv), "conf", "attachment", "get", "--id", "12345",
		"--name", "diagram.png", "--version", "3", "--into", dir)
	if code != exitOK {
		t.Fatalf("exit=%d stdout=%q", code, out)
	}
	var result app.ConfluenceAttachmentDownloadResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, out)
	}
	if result.SchemaVersion != 1 || result.PageID != "12345" || result.Name != "diagram.png" ||
		result.OutputName != "diagram.png" || result.RequestedAttachmentVersion != 3 || result.Selector != app.ConfluenceAttachmentSelectorVersion ||
		result.AttachmentIDBound || result.IdentityRevalidated || result.PageVersionGated {
		t.Fatalf("result=%+v", result)
	}
	if requestURI != "/download/attachments/12345/diagram.png?version=3" {
		t.Fatalf("request URI=%q", requestURI)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "diagram.png")); err != nil || string(data) != "synthetic attachment bytes" {
		t.Fatalf("download=%q err=%v", data, err)
	}
}

func TestConfAttachmentGetPreservesCallerNameAndReportsFloatingLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
		result.RequestedAttachmentVersion != 0 || result.Selector != app.ConfluenceAttachmentSelectorLatest {
		t.Fatalf("result=%+v", result)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "diagram.png")); err != nil || string(data) != "latest bytes" {
		t.Fatalf("download=%q err=%v", data, err)
	}
}

func TestConfAttachmentGetRejectsNegativeVersionBeforeConfiguration(t *testing.T) {
	out, code := runCLI(t, nil, "conf", "attachment", "get", "--id", "12345", "--name", "x", "--version", "-1")
	if code != exitUsage || out != "" {
		t.Fatalf("exit=%d stdout=%q", code, out)
	}
}
