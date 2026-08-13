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
			_, _ = w.Write([]byte(`{"results":[{"id":"21","title":"diagram.png","type":"attachment","version":{"number":3},"container":{"id":"12345","type":"page"},"extensions":{"fileSize":99}}],"totalCount":1,"start":0,"limit":2,"size":1,"_links":{}}`))
		case r.URL.Path == "/rest/api/content/21":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"21","title":"diagram.png","type":"attachment","version":{"number":2},"container":{"id":"12345","type":"page"},"extensions":{"fileSize":26}}`))
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
		result.ObservedFileSize != 26 || result.MaxBytes != app.ConfluenceAttachmentDownloadDefaultMaxBytes ||
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
			_, _ = w.Write([]byte(`{"results":[{"id":"21","title":"nested/diagram.png","type":"attachment","version":{"number":4},"container":{"id":"12345","type":"page"},"extensions":{"fileSize":12}}],"totalCount":1,"start":0,"limit":2,"size":1,"_links":{}}`))
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
		result.ObservedFileSize != 12 || result.MaxBytes != app.ConfluenceAttachmentDownloadDefaultMaxBytes ||
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

func TestConfAttachmentGetRejectsInvalidSelectorsBeforeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
		file string
	}{
		{name: "blank id", id: " \t ", file: "x"},
		{name: "invalid opaque id", id: "bad.id", file: "x"},
		{name: "blank filename", id: "12345", file: " \t "},
		{name: "invalid filename UTF-8", id: "12345", file: string([]byte{0xff})},
		{name: "oversize filename", id: "12345", file: strings.Repeat("x", app.ConfluenceAttachmentDownloadMaxFilenameBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runCLI(t, nil, "conf", "attachment", "get", "--id", tc.id, "--name", tc.file)
			if code != exitUsage || out != "" {
				t.Fatalf("exit=%d stdout=%q", code, out)
			}
		})
	}
}

func TestConfAttachmentGetMaxBytesDefaultExplicitAndInvalidBeforeConfiguration(t *testing.T) {
	for _, value := range []string{"0", "-1", "1073741825"} {
		out, code := runCLI(t, nil, "conf", "attachment", "get", "--id", "12345", "--name", "x", "--max-bytes", value)
		if code != exitUsage || out != "" {
			t.Fatalf("max-bytes=%s exit=%d stdout=%q", value, code, out)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/child/attachment") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"id":"21","title":"x","type":"attachment","version":{"number":1},"container":{"id":"12345","type":"page"},"extensions":{"fileSize":0}}],"totalCount":1,"start":0,"limit":2,"size":1,"_links":{}}`))
			return
		}
	}))
	t.Cleanup(srv.Close)
	root := t.TempDir()
	out, code := runCLI(t, confEnv(srv), "conf", "attachment", "get", "--id", "12345", "--name", "x", "--max-bytes", "1", "--into", root)
	if code != exitOK {
		t.Fatalf("explicit max exit=%d stdout=%q", code, out)
	}
	var result app.ConfluenceAttachmentDownloadResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.MaxBytes != 1 || result.ObservedFileSize != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestConfAttachmentGetSelectedMaxEnforcesExactTransportBody(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		wantCode int
		wantBody string
	}{
		{name: "N", body: "abc", wantCode: exitOK, wantBody: "abc"},
		{name: "N plus 1", body: "abcd", wantCode: exitCheckFailed, wantBody: "sentinel"},
		{name: "N minus 1", body: "ab", wantCode: exitCheckFailed, wantBody: "sentinel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/child/attachment") {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"results":[{"id":"21","title":"x","type":"attachment","version":{"number":1},"container":{"id":"12345","type":"page"},"extensions":{"fileSize":3}}],"totalCount":1,"start":0,"limit":2,"size":1,"_links":{}}`))
					return
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			root := t.TempDir()
			target := filepath.Join(root, "x")
			if err := os.WriteFile(target, []byte("sentinel"), 0o644); err != nil {
				t.Fatal(err)
			}
			out, code := runCLI(t, confEnv(srv), "conf", "attachment", "get", "--id", "12345", "--name", "x", "--max-bytes", "3", "--into", root)
			if code != tc.wantCode || code != exitOK && out != "" {
				t.Fatalf("exit=%d stdout=%q", code, out)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != tc.wantBody {
				t.Fatalf("body=%q err=%v", got, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".tmp-") {
					t.Fatalf("temporary file leaked: %s", entry.Name())
				}
			}
		})
	}
}

func TestConfAttachmentGetObservedSizeAboveSelectedMaxIsRuntimeCheckFailure(t *testing.T) {
	var binaryRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/child/attachment") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"id":"21","title":"x","type":"attachment","version":{"number":1},"container":{"id":"12345","type":"page"},"extensions":{"fileSize":4}}],"totalCount":1,"start":0,"limit":2,"size":1,"_links":{}}`))
			return
		}
		binaryRequests++
	}))
	defer srv.Close()
	root := filepath.Join(t.TempDir(), "not-created")
	out, stderr, code := runCLIFull(t, confEnv(srv), "conf", "attachment", "get", "--id", "12345", "--name", "x", "--max-bytes", "3", "--into", root)
	if code != exitCheckFailed || out != "" || strings.Contains(stderr, "Usage:") || binaryRequests != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q binary=%d", code, out, stderr, binaryRequests)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("runtime bound refusal created output root: %v", err)
	}
}

func TestConfAttachmentGetMetadataFiveAttemptsAndSixthRefusal(t *testing.T) {
	for _, extraRedirect := range []bool{false, true} {
		name := "five attempts succeed"
		if extraRedirect {
			name = "sixth refused before transport"
		}
		t.Run(name, func(t *testing.T) {
			var metadataRequests, binaryRequests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/x/AwAG":
					metadataRequests++
					location := "/display/DOC/Page"
					if extraRedirect {
						location = "/short-hop"
					}
					http.Redirect(w, r, location, http.StatusFound)
				case r.URL.Path == "/short-hop":
					metadataRequests++
					http.Redirect(w, r, "/display/DOC/Page", http.StatusFound)
				case r.URL.Path == "/display/DOC/Page":
					metadataRequests++
					w.WriteHeader(http.StatusOK)
				case r.URL.Path == "/rest/api/search":
					metadataRequests++
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"results":[{"content":{"id":"12345","type":"page","title":"Page","space":{"key":"DOC"},"version":{"number":1}}}],"start":0,"size":1,"totalCount":1,"_links":{}}`))
				case strings.HasSuffix(r.URL.Path, "/child/attachment"):
					metadataRequests++
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"results":[{"id":"21","title":"x","type":"attachment","version":{"number":3},"container":{"id":"12345","type":"page"},"extensions":{"fileSize":3}}],"totalCount":1,"start":0,"limit":2,"size":1,"_links":{}}`))
				case r.URL.Path == "/rest/api/content/21":
					metadataRequests++
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"21","title":"x","type":"attachment","version":{"number":2},"container":{"id":"12345","type":"page"},"extensions":{"fileSize":3}}`))
				case strings.HasPrefix(r.URL.Path, "/download/attachments/"):
					binaryRequests++
					_, _ = w.Write([]byte("abc"))
				default:
					t.Fatalf("unexpected request=%q", r.URL.RequestURI())
				}
			}))
			defer srv.Close()
			root := filepath.Join(t.TempDir(), "out")
			out, code := runCLI(t, confEnv(srv), "conf", "attachment", "get", "--id", "/x/AwAG", "--name", "x", "--version", "2", "--into", root)
			if extraRedirect {
				if code == exitOK || code == exitUsage || out != "" || metadataRequests != 5 || binaryRequests != 0 {
					t.Fatalf("exit=%d out=%q metadata=%d binary=%d", code, out, metadataRequests, binaryRequests)
				}
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatalf("sixth-attempt refusal created root: %v", err)
				}
				return
			}
			if code != exitOK || metadataRequests != 5 || binaryRequests != 1 {
				t.Fatalf("exit=%d out=%q metadata=%d binary=%d", code, out, metadataRequests, binaryRequests)
			}
			if data, err := os.ReadFile(filepath.Join(root, "x")); err != nil || string(data) != "abc" {
				t.Fatalf("body=%q err=%v", data, err)
			}
		})
	}
}
