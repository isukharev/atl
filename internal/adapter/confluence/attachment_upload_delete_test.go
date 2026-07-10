package confluence

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type failingAttachmentReader struct{}

func (failingAttachmentReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

type blockingAttachmentReader struct{ release <-chan struct{} }

func (r blockingAttachmentReader) Read([]byte) (int, error) {
	<-r.release
	return 0, io.EOF
}

type closableBlockingAttachmentReader struct {
	release chan struct{}
	once    sync.Once
}

func (r *closableBlockingAttachmentReader) Read([]byte) (int, error) {
	<-r.release
	return 0, io.EOF
}

func (r *closableBlockingAttachmentReader) Close() error {
	r.once.Do(func() { close(r.release) })
	return nil
}

func TestUploadAttachmentMultipart(t *testing.T) {
	const pageID = "42"
	var gotMethod, gotPath, gotToken, gotContentType string
	var gotFile []byte
	var gotComment string
	var gotContentLength int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Atlassian-Token")
		gotContentType = r.Header.Get("Content-Type")
		gotContentLength = r.ContentLength

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
	att, err := cf.UploadAttachment(context.Background(), pageID, "test.txt", io.NopCloser(strings.NewReader("hello")), "my comment")
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
	if gotContentLength != -1 {
		t.Errorf("Content-Length = %d, want streaming/chunked request", gotContentLength)
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
	_, err := cf.UploadAttachment(context.Background(), "pg1", "f.txt", io.NopCloser(strings.NewReader("x")), "")
	if err == nil {
		t.Fatal("expected error for empty results, got nil")
	}
}

func TestUploadAttachmentPropagatesStreamingReaderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	if _, err := cf.UploadAttachment(context.Background(), "pg1", "f.txt", io.NopCloser(failingAttachmentReader{}), ""); err == nil {
		t.Fatal("streaming reader error was ignored")
	}
}

func TestUploadAttachmentCancellationDoesNotWaitForBlockedReader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release := make(chan struct{})
	done := make(chan error, 1)
	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	go func() {
		_, err := cf.UploadAttachment(ctx, "pg1", "f.txt", io.NopCloser(blockingAttachmentReader{release: release}), "")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("canceled upload returned nil")
		}
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("canceled upload waited for a blocked source reader")
	}
	close(release)
}

func TestUploadAttachmentEarlySuccessClosesBlockedSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"att1","title":"f.txt"}]}`))
	}))
	defer srv.Close()
	reader := &closableBlockingAttachmentReader{release: make(chan struct{})}
	done := make(chan error, 1)
	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	go func() {
		_, err := cf.UploadAttachment(context.Background(), "pg1", "f.txt", reader, "")
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		_ = reader.Close()
		<-done
		t.Fatal("early successful response left multipart producer blocked")
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
