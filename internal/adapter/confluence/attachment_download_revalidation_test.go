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
	return attachmentDownloadMetadataJSONSize(id, pageID, title, version, 23)
}

func attachmentDownloadMetadataJSONSize(id, pageID, title string, version int, size int64) string {
	return `{"id":"` + id + `","title":"` + title + `","type":"attachment","version":{"number":` +
		strconv.Itoa(version) + `},"container":{"id":"` + pageID + `","type":"page"},"extensions":{"fileSize":` +
		strconv.FormatInt(size, 10) + `}}`
}

func attachmentDownloadListingJSON(total int, rows ...string) string {
	return `{"results":[` + strings.Join(rows, ",") + `],"totalCount":` + strconv.Itoa(total) +
		`,"start":0,"limit":2,"size":` + strconv.Itoa(len(rows)) + `,"_links":{}}`
}

func attachmentDownloadTerminalListingWithoutTotalJSON(rows ...string) string {
	return `{"results":[` + strings.Join(rows, ",") + `],"start":0,"limit":200,"size":` + strconv.Itoa(len(rows)) + `,"_links":{}}`
}

func TestRevalidateAttachmentDownloadResolvesCurrentAndHistoricalVersions(t *testing.T) {
	for _, tc := range []struct {
		name             string
		requestedVersion int
		wantRequests     int32
	}{
		{name: "current", requestedVersion: 0, wantRequests: 1},
		{name: "positive current", requestedVersion: 3, wantRequests: 1},
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
			if evidence.AttachmentID != "21" || evidence.PageID != "10" || evidence.Filename != "diagram.png" || evidence.Version != wantVersion || evidence.FileSize != 23 || requests.Load() != tc.wantRequests {
				t.Fatalf("evidence=%+v requests=%d", evidence, requests.Load())
			}
		})
	}
}

func TestRevalidateAttachmentDownloadRequiresVersionSpecificNonnegativeFileSize(t *testing.T) {
	for _, tc := range []struct {
		name       string
		current    string
		historical string
		version    int
		wantSize   int64
		wantErr    error
	}{
		{name: "current missing size", current: strings.Replace(attachmentDownloadMetadataJSON("21", "10", "diagram.png", 3), `,"extensions":{"fileSize":23}`, "", 1), wantErr: domain.ErrCheckFailed},
		{name: "current null extensions", current: strings.Replace(attachmentDownloadMetadataJSON("21", "10", "diagram.png", 3), `"extensions":{"fileSize":23}`, `"extensions":null`, 1), wantErr: domain.ErrCheckFailed},
		{name: "current null size", current: strings.Replace(attachmentDownloadMetadataJSON("21", "10", "diagram.png", 3), `"fileSize":23`, `"fileSize":null`, 1), wantErr: domain.ErrCheckFailed},
		{name: "current null container", current: strings.Replace(attachmentDownloadMetadataJSON("21", "10", "diagram.png", 3), `"container":{"id":"10","type":"page"}`, `"container":null`, 1), wantErr: domain.ErrCheckFailed},
		{name: "current null version", current: strings.Replace(attachmentDownloadMetadataJSON("21", "10", "diagram.png", 3), `"version":{"number":3}`, `"version":null`, 1), wantErr: domain.ErrCheckFailed},
		{name: "current negative size", current: attachmentDownloadMetadataJSONSize("21", "10", "diagram.png", 3, -1), wantErr: domain.ErrCheckFailed},
		{name: "historical missing size", current: attachmentDownloadMetadataJSONSize("21", "10", "diagram.png", 3, 99), historical: strings.Replace(attachmentDownloadMetadataJSON("21", "10", "diagram.png", 2), `,"extensions":{"fileSize":23}`, "", 1), version: 2, wantErr: domain.ErrCheckFailed},
		{name: "historical null extensions", current: attachmentDownloadMetadataJSONSize("21", "10", "diagram.png", 3, 99), historical: strings.Replace(attachmentDownloadMetadataJSON("21", "10", "diagram.png", 2), `"extensions":{"fileSize":23}`, `"extensions":null`, 1), version: 2, wantErr: domain.ErrCheckFailed},
		{name: "historical null size", current: attachmentDownloadMetadataJSONSize("21", "10", "diagram.png", 3, 99), historical: strings.Replace(attachmentDownloadMetadataJSON("21", "10", "diagram.png", 2), `"fileSize":23`, `"fileSize":null`, 1), version: 2, wantErr: domain.ErrCheckFailed},
		{name: "historical null container", current: attachmentDownloadMetadataJSONSize("21", "10", "diagram.png", 3, 99), historical: strings.Replace(attachmentDownloadMetadataJSON("21", "10", "diagram.png", 2), `"container":{"id":"10","type":"page"}`, `"container":null`, 1), version: 2, wantErr: domain.ErrCheckFailed},
		{name: "historical null version", current: attachmentDownloadMetadataJSONSize("21", "10", "diagram.png", 3, 99), historical: strings.Replace(attachmentDownloadMetadataJSON("21", "10", "diagram.png", 2), `"version":{"number":2}`, `"version":null`, 1), version: 2, wantErr: domain.ErrCheckFailed},
		{name: "historical negative size", current: attachmentDownloadMetadataJSONSize("21", "10", "diagram.png", 3, 99), historical: attachmentDownloadMetadataJSONSize("21", "10", "diagram.png", 2, -1), version: 2, wantErr: domain.ErrCheckFailed},
		{name: "historical size wins", current: attachmentDownloadMetadataJSONSize("21", "10", "diagram.png", 3, 99), historical: attachmentDownloadMetadataJSONSize("21", "10", "diagram.png", 2, 7), version: 2, wantSize: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/child/attachment") {
					_, _ = w.Write([]byte(attachmentDownloadListingJSON(1, tc.current)))
					return
				}
				if r.URL.Query().Get("status") != "historical" {
					t.Errorf("historical status=%q", r.URL.Query().Get("status"))
				}
				_, _ = w.Write([]byte(tc.historical))
			}))
			t.Cleanup(srv.Close)
			evidence, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).RevalidateAttachmentDownload(
				attachmentDownloadRevalidationContext(t, 2, 1<<20), "10", "diagram.png", tc.version)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err=%v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || evidence.FileSize != tc.wantSize {
				t.Fatalf("evidence=%+v err=%v", evidence, err)
			}
		})
	}
}

func TestRevalidateAttachmentDownloadRejectsFutureVersionBeforeHistoricalRequest(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(attachmentDownloadListingJSON(1, attachmentDownloadMetadataJSON("21", "10", "diagram.png", 3))))
	}))
	t.Cleanup(srv.Close)
	_, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).RevalidateAttachmentDownload(
		attachmentDownloadRevalidationContext(t, 2, 1<<20), "10", "diagram.png", 4)
	if !errors.Is(err, domain.ErrCheckFailed) || requests.Load() != 1 {
		t.Fatalf("err=%v requests=%d", err, requests.Load())
	}
}

func TestRevalidateAttachmentDownloadAcceptsOpaqueReadIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(attachmentDownloadListingJSON(1, attachmentDownloadMetadataJSON("att_opaque-1", "page_opaque-1", "diagram.png", 3))))
	}))
	t.Cleanup(srv.Close)
	evidence, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).RevalidateAttachmentDownload(
		attachmentDownloadRevalidationContext(t, 1, 1<<20), "page_opaque-1", "diagram.png", 0)
	if err != nil || evidence.AttachmentID != "att_opaque-1" || evidence.PageID != "page_opaque-1" {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func TestRevalidateAttachmentDownloadAcceptsTerminalUniqueResultWithoutTotalCount(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("filename") != "diagram.png" || r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("start") != "0" {
			t.Fatalf("listing request=%q", r.URL.RequestURI())
		}
		_, _ = w.Write([]byte(attachmentDownloadTerminalListingWithoutTotalJSON(
			attachmentDownloadMetadataJSON("21", "10", "diagram.png", 3),
		)))
	}))
	t.Cleanup(srv.Close)
	evidence, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).RevalidateAttachmentDownload(
		attachmentDownloadRevalidationContext(t, 1, 1<<20), "10", "diagram.png", 0)
	if err != nil || evidence.AttachmentID != "21" || evidence.PageID != "10" || evidence.Filename != "diagram.png" || evidence.Version != 3 || evidence.FileSize != 23 || requests.Load() != 1 {
		t.Fatalf("evidence=%+v requests=%d err=%v", evidence, requests.Load(), err)
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
		{name: "terminal empty result without total", listing: attachmentDownloadTerminalListingWithoutTotalJSON(), want: domain.ErrNotFound},
		{name: "unreachable filtered result", listing: `{"results":[],"totalCount":1,"start":0,"limit":2,"size":0,"_links":{}}`, want: domain.ErrCheckFailed},
		{name: "terminal duplicate without total", listing: attachmentDownloadTerminalListingWithoutTotalJSON(
			attachmentDownloadMetadataJSON("21", "10", "diagram.png", 3),
			attachmentDownloadMetadataJSON("22", "10", "diagram.png", 1),
		), want: domain.ErrCheckFailed},
		{name: "size mismatch without total", listing: `{"results":[` + attachmentDownloadMetadataJSON("21", "10", "diagram.png", 3) + `],"start":0,"limit":200,"size":2,"_links":{}}`, want: domain.ErrCheckFailed},
		{name: "next page without total", listing: `{"results":[` + attachmentDownloadMetadataJSON("21", "10", "diagram.png", 3) + `],"start":0,"limit":2,"size":1,"_links":{"next":"more"}}`, want: domain.ErrCheckFailed},
		{name: "backend filter returned nonmatching title", listing: attachmentDownloadListingJSON(1,
			attachmentDownloadMetadataJSON("21", "10", "other.png", 3)), want: domain.ErrCheckFailed},
		{name: "attachment equals container", listing: attachmentDownloadListingJSON(1,
			attachmentDownloadMetadataJSON("10", "10", "diagram.png", 3)), want: domain.ErrCheckFailed},
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
