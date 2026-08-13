package confluence

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const attachmentDiscoveryRow = `{"id":"21","title":"diagram.png","type":"attachment","version":{"number":3},"container":{"id":"10","type":"page","version":{"number":7}},"space":{"key":"DOC"},"metadata":{"mediaType":"image/png"},"extensions":{"fileSize":42}}`

func TestDiscoverAttachmentsQualifiedUsesValidatedLocalOffsetContinuation(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/rest/api/content/search" || r.URL.Query().Get("expand") != "container.version,extensions,metadata,space,version" {
			t.Fatalf("request URI=%q", r.URL.RequestURI())
		}
		if got := r.URL.Query().Get("cql"); got != `type = attachment and space = "DOC" and (creator = currentUser())` {
			t.Fatalf("cql=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("start") == "0" {
			_, _ = w.Write([]byte(`{"results":[` + attachmentDiscoveryRow + `],"start":0,"limit":2,"size":1,"totalCount":2,"_links":{"next":"https://foreign.invalid/never-follow"}}`))
			return
		}
		row := strings.ReplaceAll(attachmentDiscoveryRow, `"21"`, `"22"`)
		_, _ = w.Write([]byte(`{"results":[` + row + `],"start":1,"limit":1,"size":1,"totalSize":2,"_links":{}}`))
	}))
	t.Cleanup(srv.Close)

	page, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).DiscoverAttachmentsQualified(t.Context(), domain.ConfluenceAttachmentDiscoveryRequest{
		Space: "DOC", CQL: "creator = currentUser()", MaxItems: 2,
	})
	if err != nil {
		t.Fatalf("DiscoverAttachmentsQualified: %v", err)
	}
	if !page.Complete || page.PartialReason != "" || page.NextStart != nil || page.TotalSize == nil || *page.TotalSize != 2 || len(page.Attachments) != 2 {
		t.Fatalf("page=%+v", page)
	}
	first := page.Attachments[0]
	if first.ID != "21" || first.Title != "diagram.png" || first.Type != "attachment" || first.Version != 3 ||
		first.ContainerID != "10" || first.ContainerType != "page" || first.ContainerVersion != 7 ||
		first.Space != "DOC" || first.MediaType != "image/png" || first.FileSize != 42 {
		t.Fatalf("attachment=%+v", first)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests=%d, want 2", requests.Load())
	}
}

func TestDiscoverAttachmentsQualifiedTerminalAtExactItemLimitWins(t *testing.T) {
	for _, tc := range []struct {
		name       string
		total      int
		next       string
		complete   bool
		partial    string
		wantCursor bool
	}{
		{name: "terminal", total: 1, complete: true},
		{name: "nonterminal", total: 2, next: "ignored", partial: domain.ConfluenceAttachmentDiscoveryPartialItemLimit, wantCursor: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"results":[` + attachmentDiscoveryRow + `],"start":0,"limit":1,"size":1,"totalCount":` +
					string(rune('0'+tc.total)) + `,"_links":{"next":"` + tc.next + `"}}`))
			}))
			t.Cleanup(srv.Close)
			page, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).DiscoverAttachmentsQualified(t.Context(), domain.ConfluenceAttachmentDiscoveryRequest{MaxItems: 1})
			if err != nil {
				t.Fatal(err)
			}
			if page.Complete != tc.complete || page.PartialReason != tc.partial || (page.NextStart != nil) != tc.wantCursor || len(page.Attachments) != 1 {
				t.Fatalf("page=%+v", page)
			}
		})
	}
}

func TestDiscoverAttachmentsQualifiedBudgetsReturnQualifiedPrefix(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[` + attachmentDiscoveryRow + `],"start":0,"limit":2,"size":1,"totalCount":2,"_links":{"next":"ignored"}}`))
	}))
	t.Cleanup(srv.Close)
	budget, err := domain.NewReadBudget(1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ctx := domain.WithSingleAttempt(domain.WithReadBudget(t.Context(), budget))
	page, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).DiscoverAttachmentsQualified(ctx, domain.ConfluenceAttachmentDiscoveryRequest{MaxItems: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.Complete || page.PartialReason != domain.ConfluenceAttachmentDiscoveryPartialRequestLimit || page.NextStart == nil || *page.NextStart != 1 || len(page.Attachments) != 1 {
		t.Fatalf("page=%+v", page)
	}
	if requests.Load() != 1 || budget.Usage().Attempts != 1 {
		t.Fatalf("requests=%d usage=%+v", requests.Load(), budget.Usage())
	}
}

func TestDiscoverAttachmentsQualifiedRejectsMalformedMetadataAndCoordinates(t *testing.T) {
	for _, body := range []string{
		`{"results":[],"start":0,"limit":1,"size":0,"_links":{}}`,
		`{"results":[],"start":0,"limit":1,"size":0,"totalCount":null,"totalSize":null,"_links":{}}`,
		`{"results":[],"start":0,"limit":1,"size":0,"totalCount":null,"totalSize":0,"_links":{}}`,
		`{"results":[],"start":0,"limit":1,"size":0,"totalCount":0,"totalSize":1,"_links":{}}`,
		`{"results":[],"start":0,"limit":1,"size":0,"totalCount":9223372036854775808,"_links":{}}`,
		`{"results":[],"start":null,"limit":1,"size":0,"totalCount":0,"_links":{}}`,
		`{"results":[],"start":0,"limit":1,"size":0,"totalCount":1,"_links":{}}`,
		`{"results":[` + strings.ReplaceAll(attachmentDiscoveryRow, `"type":"attachment"`, `"type":"page"`) + `],"start":0,"limit":1,"size":1,"totalCount":1,"_links":{}}`,
		`{"results":[` + strings.ReplaceAll(attachmentDiscoveryRow, `"key":"DOC"`, `"key":"OTHER"`) + `],"start":0,"limit":1,"size":1,"totalCount":1,"_links":{}}`,
		`{"results":[` + strings.Replace(attachmentDiscoveryRow, `"id":"21"`, `"id":"10"`, 1) + `],"start":0,"limit":1,"size":1,"totalCount":1,"_links":{}}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		page, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).DiscoverAttachmentsQualified(t.Context(), domain.ConfluenceAttachmentDiscoveryRequest{Space: "DOC", MaxItems: 1})
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if page.Complete || page.PartialReason != domain.ConfluenceAttachmentDiscoveryPartialPaginationUnqualified || len(page.Attachments) != 0 || page.NextStart == nil || *page.NextStart != 0 {
			t.Fatalf("body=%s page=%+v", body, page)
		}
	}
}

func TestDiscoverAttachmentsQualifiedTotalAliasWireContract(t *testing.T) {
	for _, tc := range []struct {
		name     string
		totals   string
		complete bool
	}{
		{name: "official only", totals: `"totalCount":1`, complete: true},
		{name: "observed only", totals: `"totalSize":1`, complete: true},
		{name: "equal dual", totals: `"totalCount":1,"totalSize":1`, complete: true},
		{name: "null official", totals: `"totalCount":null,"totalSize":1`},
		{name: "null observed", totals: `"totalCount":1,"totalSize":null`},
		{name: "conflict", totals: `"totalCount":1,"totalSize":2`},
		{name: "fraction", totals: `"totalCount":1.0`},
		{name: "overflow", totals: `"totalCount":9223372036854775808`},
		{name: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				comma := ""
				if tc.totals != "" {
					comma = "," + tc.totals
				}
				_, _ = w.Write([]byte(`{"results":[` + attachmentDiscoveryRow + `],"start":0,"limit":1,"size":1` + comma + `,"_links":{}}`))
			}))
			defer srv.Close()
			page, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).DiscoverAttachmentsQualified(
				t.Context(), domain.ConfluenceAttachmentDiscoveryRequest{MaxItems: 1})
			if err != nil {
				t.Fatal(err)
			}
			if tc.complete {
				if !page.Complete || len(page.Attachments) != 1 || page.TotalSize == nil || *page.TotalSize != 1 {
					t.Fatalf("page=%+v", page)
				}
				return
			}
			if page.Complete || page.PartialReason != domain.ConfluenceAttachmentDiscoveryPartialPaginationUnqualified ||
				len(page.Attachments) != 0 || page.NextStart == nil || *page.NextStart != 0 {
				t.Fatalf("page=%+v", page)
			}
		})
	}
}

func TestDiscoverAttachmentsQualifiedRejectsFinalPageOverCallerItemBound(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		second := strings.ReplaceAll(strings.ReplaceAll(attachmentDiscoveryRow, `"21"`, `"22"`), `"diagram.png"`, `"two.png"`)
		_, _ = w.Write([]byte(`{"results":[` +
			attachmentDiscoveryRow + `,` + second +
			`],"start":0,"limit":2,"size":2,"totalCount":2,"_links":{}}`))
	}))
	t.Cleanup(srv.Close)
	result, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).DiscoverAttachmentsQualified(
		t.Context(),
		domain.ConfluenceAttachmentDiscoveryRequest{Start: 0, MaxItems: 1},
	)
	if err != nil {
		t.Fatalf("DiscoverAttachmentsQualified: %v", err)
	}
	if result.Complete || result.PartialReason != domain.ConfluenceAttachmentDiscoveryPartialPaginationUnqualified ||
		len(result.Attachments) != 0 || result.NextStart == nil || *result.NextStart != 0 || requests != 1 {
		t.Fatalf("result=%+v requests=%d, want content-free checked offset 0", result, requests)
	}
}

func TestDiscoverAttachmentsQualifiedDeadlineReturnsClosedReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	page, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).DiscoverAttachmentsQualified(ctx, domain.ConfluenceAttachmentDiscoveryRequest{MaxItems: 1})
	if err != nil || page.PartialReason != domain.ConfluenceAttachmentDiscoveryPartialDeadline {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}
