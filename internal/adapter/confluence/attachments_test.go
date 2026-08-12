package confluence

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// attachmentPage renders one canned listing response. next is the raw
// _links.next value; an empty value means the server signals exhaustion.
func attachmentPage(next string, ids ...string) string {
	var b strings.Builder
	b.WriteString(`{"results":[`)
	for i, id := range ids {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"id":"` + id + `","title":"` + id + `.png","version":{"number":1}}`)
	}
	b.WriteString(`],"_links":{`)
	if next != "" {
		b.WriteString(`"next":"` + next + `"`)
	}
	b.WriteString(`}}`)
	return b.String()
}

func attachmentServer(t *testing.T, handler http.HandlerFunc) *Confluence {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Confluence{c: newTestClient(srv.URL), base: srv.URL}
}

// Natural exhaustion is the only path that may report a complete inventory.
func TestListAttachmentsQualifiedCompleteAcrossPages(t *testing.T) {
	cf := attachmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("expand"); got != "version,metadata" {
			t.Fatalf("legacy expand=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("start") == "0" {
			_, _ = w.Write([]byte(attachmentPage("/rest/api/content/300/child/attachment?start=2", "a1", "a2")))
			return
		}
		_, _ = w.Write([]byte(attachmentPage("", "a3")))
	})
	inventory, err := cf.ListAttachmentsQualified(context.Background(), "300")
	if err != nil {
		t.Fatalf("ListAttachmentsQualified: %v", err)
	}
	if !inventory.Complete || inventory.PartialReason != "" || len(inventory.Attachments) != 3 {
		t.Fatalf("inventory=%+v", inventory)
	}
	if inventory.Attachments[2].ID != "a3" {
		t.Errorf("attachment[2].ID = %q, want a3", inventory.Attachments[2].ID)
	}
}

func TestListAttachmentsQualifiedEmptyIsCompleteAndNonNil(t *testing.T) {
	cf := attachmentServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[],"_links":{}}`))
	})
	inventory, err := cf.ListAttachmentsQualified(context.Background(), "300")
	if err != nil {
		t.Fatalf("ListAttachmentsQualified: %v", err)
	}
	if inventory.Attachments == nil {
		t.Fatal("an exhausted empty listing must be a non-nil array, not an absent read")
	}
	if !inventory.Complete || inventory.PartialReason != "" || len(inventory.Attachments) != 0 {
		t.Fatalf("inventory=%+v", inventory)
	}
}

func TestListAttachmentsQualifiedBoundedUsesExplicitLimitsAndStableMetadata(t *testing.T) {
	requests := 0
	cf := attachmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if got := r.URL.Query().Get("expand"); got != "version,metadata,history.createdBy" {
			t.Fatalf("expand=%q", got)
		}
		_, _ = w.Write([]byte(`{"results":[{"id":"7","title":"a.bin","metadata":{"mediaType":"application/octet-stream"},"extensions":{"fileSize":3},"version":{"number":2},"history":{"createdDate":"2026-01-01","createdBy":{"userKey":"stable","username":"user","displayName":"Fixture"}}}],"_links":{"next":"/rest/api/content/300/child/attachment?start=1"}}`))
	})
	inventory, err := cf.ListAttachmentsQualifiedBounded(t.Context(), "300", domain.AttachmentReadOptions{MaxPages: 1, MaxItems: 2})
	if err != nil || inventory.Complete || inventory.PartialReason != domain.AttachmentPartialPageLimit || requests != 1 {
		t.Fatalf("inventory=%+v requests=%d error=%v", inventory, requests, err)
	}
	attachment := inventory.Attachments[0]
	if attachment.ID != "7" || attachment.Version != 2 || attachment.AuthorKey != "stable" || attachment.Created != "2026-01-01" {
		t.Fatalf("attachment=%+v", attachment)
	}
}

// A server that keeps advertising more pages is stopped by the page cap, and
// the inventory must say so instead of reading as exhausted.
func TestListAttachmentsQualifiedReportsPageLimit(t *testing.T) {
	requests := 0
	cf := attachmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		next := "/rest/api/content/300/child/attachment?start=" + strconv.Itoa(start+1)
		_, _ = w.Write([]byte(attachmentPage(next, "a"+strconv.Itoa(start))))
	})
	inventory, err := cf.ListAttachmentsQualified(context.Background(), "300")
	if err != nil {
		t.Fatalf("ListAttachmentsQualified: %v", err)
	}
	if inventory.Complete || inventory.PartialReason != domain.AttachmentPartialPageLimit {
		t.Fatalf("inventory=%+v", inventory)
	}
	if len(inventory.Attachments) != maxPages || requests != maxPages {
		t.Fatalf("collected %d attachments over %d requests, want %d of each", len(inventory.Attachments), requests, maxPages)
	}
}

// One oversized response must stop exactly at the item cap rather than silently
// exceeding it.
func TestListAttachmentsQualifiedReportsItemLimitWithoutExceedingIt(t *testing.T) {
	ids := make([]string, 0, maxItems+1)
	for i := 0; i < maxItems+1; i++ {
		ids = append(ids, "a"+strconv.Itoa(i))
	}
	body := attachmentPage("", ids...)
	cf := attachmentServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	inventory, err := cf.ListAttachmentsQualified(context.Background(), "300")
	if err != nil {
		t.Fatalf("ListAttachmentsQualified: %v", err)
	}
	if inventory.Complete || inventory.PartialReason != domain.AttachmentPartialItemLimit {
		t.Fatalf("inventory complete=%v reason=%q", inventory.Complete, inventory.PartialReason)
	}
	if len(inventory.Attachments) != maxItems {
		t.Fatalf("collected %d attachments, want exactly the item cap %d", len(inventory.Attachments), maxItems)
	}
}

// A page that advertises more while returning nothing cannot make progress;
// reporting exhaustion there would fabricate completeness.
func TestListAttachmentsQualifiedReportsStalledPagination(t *testing.T) {
	requests := 0
	cf := attachmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("start") == "0" {
			_, _ = w.Write([]byte(attachmentPage("/rest/api/content/300/child/attachment?start=1", "a1")))
			return
		}
		_, _ = w.Write([]byte(attachmentPage("/rest/api/content/300/child/attachment?start=1")))
	})
	inventory, err := cf.ListAttachmentsQualified(context.Background(), "300")
	if err != nil {
		t.Fatalf("ListAttachmentsQualified: %v", err)
	}
	if inventory.Complete || inventory.PartialReason != domain.AttachmentPartialPaginationStalled {
		t.Fatalf("inventory=%+v", inventory)
	}
	if len(inventory.Attachments) != 1 || requests != 2 {
		t.Fatalf("collected %d attachments over %d requests", len(inventory.Attachments), requests)
	}
}

// The legacy compatibility surface keeps returning the slice and drops the
// qualification, so existing internal callers are unaffected.
func TestListAttachmentsDelegatesAndDropsQualification(t *testing.T) {
	requests := 0
	cf := attachmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		next := "/rest/api/content/300/child/attachment?start=" + strconv.Itoa(start+1)
		_, _ = w.Write([]byte(attachmentPage(next, "a"+strconv.Itoa(start))))
	})
	got, err := cf.ListAttachments(context.Background(), "300")
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(got) != maxPages || requests != maxPages {
		t.Fatalf("legacy listing collected %d attachments over %d requests", len(got), requests)
	}
	if got[0].ID != "a0" {
		t.Errorf("attachment[0].ID = %q, want a0", got[0].ID)
	}
}

func TestListAttachmentsQualifiedPropagatesBackendErrors(t *testing.T) {
	cf := attachmentServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	inventory, err := cf.ListAttachmentsQualified(context.Background(), "300")
	if err == nil {
		t.Fatalf("expected a backend error, got inventory=%+v", inventory)
	}
	if inventory.Attachments != nil || inventory.Complete {
		t.Fatalf("a failed read must not return a usable inventory: %+v", inventory)
	}
}

// The adapter satisfies both the legacy port method and the optional qualified
// capability, which is what lets the application layer select at runtime.
var (
	_ domain.DocStore                         = (*Confluence)(nil)
	_ domain.QualifiedAttachmentLister        = (*Confluence)(nil)
	_ domain.BoundedQualifiedAttachmentLister = (*Confluence)(nil)
)
