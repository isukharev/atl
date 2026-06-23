package confluence

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUploadAttachmentMultipart(t *testing.T) {
	const pageID = "42"
	var gotMethod, gotPath, gotToken, gotContentType string
	var gotFile []byte
	var gotComment string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Atlassian-Token")
		gotContentType = r.Header.Get("Content-Type")

		// Parse multipart body
		mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if strings.HasPrefix(mediaType, "multipart/") {
			mr := multipart.NewReader(r.Body, params["boundary"])
			for {
				p, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					break
				}
				switch p.FormName() {
				case "file":
					gotFile, _ = io.ReadAll(p)
				case "comment":
					b, _ := io.ReadAll(p)
					gotComment = string(b)
				}
				p.Close()
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results": [{"id": "att1", "title": "test.txt", "version": {"number": 1}}]}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	att, err := cf.UploadAttachment(context.Background(), pageID, "test.txt", []byte("hello"), "my comment")
	if err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	wantPath := "/rest/api/content/" + pageID + "/child/attachment"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if gotToken != "nocheck" {
		t.Errorf("X-Atlassian-Token = %q, want nocheck", gotToken)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data") {
		t.Errorf("Content-Type = %q, want multipart/form-data prefix", gotContentType)
	}
	if string(gotFile) != "hello" {
		t.Errorf("file bytes = %q, want hello", gotFile)
	}
	if gotComment != "my comment" {
		t.Errorf("comment field = %q, want my comment", gotComment)
	}
	if att.ID != "att1" {
		t.Errorf("att.ID = %q, want att1", att.ID)
	}
}

func TestUploadAttachmentEmptyResponseError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results": []}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, err := cf.UploadAttachment(context.Background(), "pg1", "f.txt", []byte("x"), "")
	if err == nil {
		t.Fatal("expected error for empty results, got nil")
	}
}

func TestDeleteAttachment(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	err := cf.DeleteAttachment(context.Background(), "att99")
	if err != nil {
		t.Fatalf("DeleteAttachment: %v", err)
	}
	if gotMethod != "DELETE" {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/rest/api/content/att99" {
		t.Errorf("path = %q, want /rest/api/content/att99", gotPath)
	}
}
