package confluence

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func attachmentDownloadRevalidationContext(t *testing.T, requests int, responseBytes int64) context.Context {
	t.Helper()
	budget, err := domain.NewReadBudget(requests, responseBytes)
	if err != nil {
		t.Fatal(err)
	}
	return domain.WithSingleAttempt(domain.WithReadBudget(t.Context(), budget))
}

func attachmentDownloadMetadataJSON(id, pageID, title string, version int) string {
	return `{"id":"` + id + `","title":"` + title + `","type":"attachment","version":{"number":` +
		strconv.Itoa(version) + `},"container":{"id":"` + pageID + `","type":"page"}}`
}

func attachmentDownloadListingJSON(total int, rows ...string) string {
	return `{"results":[` + strings.Join(rows, ",") + `],"totalCount":` + strconv.Itoa(total) +
		`,"start":0,"limit":2,"size":` + strconv.Itoa(len(rows)) + `,"_links":{}}`
}

func TestRevalidateAttachmentDownloadResolvesCurrentAndHistoricalVersions(t *testing.T) {
	for _, tc := range []struct {
		name             string
		requestedVersion int
		wantRequests     int32
	}{
		{name: "current", requestedVersion: 0, wantRequests: 1},
		{name: "historical", requestedVersion: 2, wantRequests: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/child/attachment"):
					if r.URL.Query().Get("filename") != "diagram.png" || r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("start") != "0" {
						t.Fatalf("listing request=%q", r.URL.RequestURI())
					}
					_, _ = w.Write([]byte(attachmentDownloadListingJSON(1, attachmentDownloadMetadataJSON("21", "10", "diagram.png", 3))))
				case r.URL.Path == "/rest/api/content/21":
					if r.URL.Query().Get("version") != "2" {
						t.Fatalf("historical request=%q", r.URL.RequestURI())
					}
					_, _ = w.Write([]byte(attachmentDownloadMetadataJSON("21", "10", "diagram.png", 2)))
				default:
					t.Fatalf("unexpected request=%q", r.URL.RequestURI())
				}
			}))
			t.Cleanup(srv.Close)
			cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
			evidence, err := cf.RevalidateAttachmentDownload(attachmentDownloadRevalidationContext(t, 2, 1<<20), "10", "diagram.png", tc.requestedVersion)
			if err != nil {
				t.Fatalf("RevalidateAttachmentDownload: %v", err)
			}
			wantVersion := tc.requestedVersion
			if wantVersion == 0 {
				wantVersion = 3
			}
			if evidence.AttachmentID != "21" || evidence.PageID != "10" || evidence.Filename != "diagram.png" || evidence.Version != wantVersion || requests.Load() != tc.wantRequests {
				t.Fatalf("evidence=%+v requests=%d", evidence, requests.Load())
			}
		})
	}
}

func TestRevalidateAttachmentDownloadRejectsAbsentDuplicateAndVersionMismatch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		listing    string
		historical string
		version    int
		want       error
	}{
		{name: "absent", listing: attachmentDownloadListingJSON(0), want: domain.ErrNotFound},
		{name: "missing total evidence", listing: `{"results":[],"start":0,"limit":2,"size":0,"_links":{}}`, want: domain.ErrCheckFailed},
		{name: "unreachable filtered result", listing: `{"results":[],"totalCount":1,"start":0,"limit":2,"size":0,"_links":{}}`, want: domain.ErrCheckFailed},
		{name: "backend filter returned nonmatching title", listing: attachmentDownloadListingJSON(1,
			attachmentDownloadMetadataJSON("21", "10", "other.png", 3)), want: domain.ErrCheckFailed},
		{name: "duplicate", listing: attachmentDownloadListingJSON(2,
			attachmentDownloadMetadataJSON("21", "10", "diagram.png", 3),
			attachmentDownloadMetadataJSON("22", "10", "diagram.png", 1)), want: domain.ErrCheckFailed},
		{name: "version mismatch", listing: attachmentDownloadListingJSON(1, attachmentDownloadMetadataJSON("21", "10", "diagram.png", 3)),
			historical: attachmentDownloadMetadataJSON("21", "10", "diagram.png", 4), version: 2, want: domain.ErrCheckFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/child/attachment") {
					_, _ = w.Write([]byte(tc.listing))
					return
				}
				_, _ = w.Write([]byte(tc.historical))
			}))
			t.Cleanup(srv.Close)
			cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
			if _, err := cf.RevalidateAttachmentDownload(attachmentDownloadRevalidationContext(t, 2, 1<<20), "10", "diagram.png", tc.version); !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want %v", err, tc.want)
			}
		})
	}
}

func TestRevalidateAttachmentDownloadRequiresBoundedContextAndHonorsByteBudget(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(attachmentDownloadListingJSON(0)))
	}))
	t.Cleanup(srv.Close)
	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	if _, err := cf.RevalidateAttachmentDownload(t.Context(), "10", "diagram.png", 0); !errors.Is(err, domain.ErrCheckFailed) || requests.Load() != 0 {
		t.Fatalf("unbounded err=%v requests=%d", err, requests.Load())
	}
	if _, err := cf.RevalidateAttachmentDownload(attachmentDownloadRevalidationContext(t, 1, 8), "10", "diagram.png", 0); !errors.Is(err, domain.ErrReadResponseBudgetExhausted) || requests.Load() != 1 {
		t.Fatalf("byte-bound err=%v requests=%d", err, requests.Load())
	}
}
