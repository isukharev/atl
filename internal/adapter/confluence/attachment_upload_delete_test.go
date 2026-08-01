package confluence

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/isukharev/atl/internal/domain"
)

type failingAttachmentReader struct{}

func (failingAttachmentReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

type trackedAttachmentReader struct{ closed bool }

func (*trackedAttachmentReader) Read([]byte) (int, error) { return 0, io.EOF }
func (r *trackedAttachmentReader) Close() error           { r.closed = true; return nil }

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
	var gotBodyLength int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Atlassian-Token")
		gotContentType = r.Header.Get("Content-Type")
		gotContentLength = r.ContentLength
		rawBody, _ := io.ReadAll(r.Body)
		gotBodyLength = len(rawBody)

		// Parse multipart body
		mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if strings.HasPrefix(mediaType, "multipart/") {
			mr := multipart.NewReader(bytes.NewReader(rawBody), params["boundary"])
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
	att, err := cf.UploadAttachment(context.Background(), pageID, "test.txt", io.NopCloser(strings.NewReader("hello")), int64(len("hello")), "my comment")
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
	if gotContentLength != int64(gotBodyLength) || gotContentLength <= int64(len("hello")) {
		t.Errorf("Content-Length = %d, body bytes = %d", gotContentLength, gotBodyLength)
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
	_, err := cf.UploadAttachment(context.Background(), "pg1", "f.txt", io.NopCloser(strings.NewReader("x")), 1, "")
	if err == nil {
		t.Fatal("expected error for empty results, got nil")
	}
	// An empty but successful backend response is an invalid result, not a usage
	// fault: it must classify as a check failure (exit 8).
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("empty response error = %v, want ErrCheckFailed", err)
	}
}

func TestUploadAttachmentMalformedResponseIsCheckFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results": [`)) // truncated JSON
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, err := cf.UploadAttachment(context.Background(), "pg1", "f.txt", io.NopCloser(strings.NewReader("x")), 1, "")
	if err == nil {
		t.Fatal("expected error for malformed response, got nil")
	}
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("malformed response error = %v, want ErrCheckFailed", err)
	}
	// The JSON decode cause is preserved with %w for diagnostics.
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("malformed response error = %v, want a wrapped json cause", err)
	}
}

func TestUploadAttachmentOverflowIsUsageAndClosesSource(t *testing.T) {
	reader := &trackedAttachmentReader{}
	cf := &Confluence{c: newTestClient("http://127.0.0.1"), base: "http://127.0.0.1"}
	// A size within an int64 of the max overflows once multipart framing is added.
	_, err := cf.UploadAttachment(context.Background(), "pg1", "f.txt", reader, int64(1<<63-1), "")
	if err == nil {
		t.Fatal("multipart length overflow was accepted")
	}
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("overflow error = %v, want ErrUsage", err)
	}
	if !reader.closed {
		t.Fatal("overflow refusal did not close source")
	}
}

func TestUploadAttachmentRejectsNegativeSizeAndClosesSource(t *testing.T) {
	reader := &trackedAttachmentReader{}
	cf := &Confluence{c: newTestClient("http://127.0.0.1"), base: "http://127.0.0.1"}
	_, err := cf.UploadAttachment(context.Background(), "pg1", "f.txt", reader, -1, "")
	if err == nil {
		t.Fatal("negative size was accepted")
	}
	// A caller-supplied bad size is a usage fault (exit 2).
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("negative-size error = %v, want ErrUsage", err)
	}
	if !reader.closed {
		t.Fatal("negative-size refusal did not close source")
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
	if _, err := cf.UploadAttachment(context.Background(), "pg1", "f.txt", io.NopCloser(failingAttachmentReader{}), 1, ""); err == nil {
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
		_, err := cf.UploadAttachment(ctx, "pg1", "f.txt", io.NopCloser(blockingAttachmentReader{release: release}), 1, "")
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
		_, err := cf.UploadAttachment(context.Background(), "pg1", "f.txt", reader, 1, "")
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

func TestDeleteAttachmentRefusesRedirectReplay(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var original, redirected int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/rest/api/content/att99":
					original++
					http.Redirect(w, r, "/rest/api/content/replayed", status)
				case "/rest/api/content/replayed":
					redirected++
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
			if err := cf.DeleteAttachment(context.Background(), "att99"); err == nil {
				t.Fatal("DeleteAttachment redirect: expected refusal")
			}
			if original != 1 || redirected != 0 {
				t.Fatalf("DELETE attempts: original=%d redirected=%d, want 1 and 0", original, redirected)
			}
		})
	}
}
