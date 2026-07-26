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

// historyPage renders one canned version-listing response. next is the raw
// _links.next value; an empty value means the server signals exhaustion.
// numbers are the version numbers on the page, emitted verbatim so a test can
// control ordering.
func historyPage(next string, numbers ...int) string {
	var b strings.Builder
	b.WriteString(`{"results":[`)
	for i, n := range numbers {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"number":` + strconv.Itoa(n) + `,"when":"2026-01-0` + strconv.Itoa(i+1) + `","by":{"displayName":"Author` + strconv.Itoa(n) + `"}}`)
	}
	b.WriteString(`],"_links":{`)
	if next != "" {
		b.WriteString(`"next":"` + next + `"`)
	}
	b.WriteString(`}}`)
	return b.String()
}

func historyServer(t *testing.T, handler http.HandlerFunc) *Confluence {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Confluence{c: newTestClient(srv.URL), base: srv.URL}
}

// Natural exhaustion across pages is the only path that may report a complete
// inventory.
func TestHistoryQualifiedCompleteAcrossPages(t *testing.T) {
	cf := historyServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("start") == "0" {
			_, _ = w.Write([]byte(historyPage("/rest/experimental/content/70/version?start=2", 3, 2)))
			return
		}
		_, _ = w.Write([]byte(historyPage("", 1)))
	})
	inventory, err := cf.HistoryQualified(context.Background(), "70")
	if err != nil {
		t.Fatalf("HistoryQualified: %v", err)
	}
	if !inventory.Complete || inventory.PartialReason != "" || len(inventory.Versions) != 3 {
		t.Fatalf("inventory=%+v", inventory)
	}
	if inventory.Versions[2].Number != 1 {
		t.Errorf("version[2].Number = %d, want 1", inventory.Versions[2].Number)
	}
}

// A single terminal page with no next cursor is exhaustion even when empty, and
// the empty listing must be a non-nil array, not an absent read.
func TestHistoryQualifiedEmptyIsCompleteAndNonNil(t *testing.T) {
	cf := historyServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[],"_links":{}}`))
	})
	inventory, err := cf.HistoryQualified(context.Background(), "70")
	if err != nil {
		t.Fatalf("HistoryQualified: %v", err)
	}
	if inventory.Versions == nil {
		t.Fatal("an exhausted empty listing must be a non-nil array, not an absent read")
	}
	if !inventory.Complete || inventory.PartialReason != "" || len(inventory.Versions) != 0 {
		t.Fatalf("inventory=%+v", inventory)
	}
}

// A terminal empty page that follows a populated one is still exhaustion: the
// server dropped its next cursor, so no fabricated partial reason applies.
func TestHistoryQualifiedTerminalEmptyPageIsComplete(t *testing.T) {
	cf := historyServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("start") == "0" {
			_, _ = w.Write([]byte(historyPage("/rest/experimental/content/70/version?start=1", 2)))
			return
		}
		// Advertises no next cursor while returning nothing: the listing is done.
		_, _ = w.Write([]byte(`{"results":[],"_links":{}}`))
	})
	inventory, err := cf.HistoryQualified(context.Background(), "70")
	if err != nil {
		t.Fatalf("HistoryQualified: %v", err)
	}
	if !inventory.Complete || inventory.PartialReason != "" || len(inventory.Versions) != 1 {
		t.Fatalf("inventory=%+v", inventory)
	}
}

// A server that keeps advertising more pages is stopped by the page cap, and
// the inventory must say so instead of reading as exhausted.
func TestHistoryQualifiedReportsPageLimit(t *testing.T) {
	requests := 0
	cf := historyServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		next := "/rest/experimental/content/70/version?start=" + strconv.Itoa(start+1)
		// Newest-first: emit a strictly descending number on each page.
		_, _ = w.Write([]byte(historyPage(next, maxPages-start)))
	})
	inventory, err := cf.HistoryQualified(context.Background(), "70")
	if err != nil {
		t.Fatalf("HistoryQualified: %v", err)
	}
	if inventory.Complete || inventory.PartialReason != domain.HistoryPartialPageLimit {
		t.Fatalf("inventory complete=%v reason=%q", inventory.Complete, inventory.PartialReason)
	}
	if len(inventory.Versions) != maxPages || requests != maxPages {
		t.Fatalf("collected %d versions over %d requests, want %d of each", len(inventory.Versions), requests, maxPages)
	}
}

// One oversized response must stop exactly at the item cap rather than silently
// exceeding it.
func TestHistoryQualifiedReportsItemLimitWithoutExceedingIt(t *testing.T) {
	numbers := make([]int, 0, maxItems+1)
	for i := 0; i < maxItems+1; i++ {
		numbers = append(numbers, maxItems+1-i) // strictly descending, all positive
	}
	body := historyPage("", numbers...)
	cf := historyServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	inventory, err := cf.HistoryQualified(context.Background(), "70")
	if err != nil {
		t.Fatalf("HistoryQualified: %v", err)
	}
	if inventory.Complete || inventory.PartialReason != domain.HistoryPartialItemLimit {
		t.Fatalf("inventory complete=%v reason=%q", inventory.Complete, inventory.PartialReason)
	}
	if len(inventory.Versions) != maxItems {
		t.Fatalf("collected %d versions, want exactly the item cap %d", len(inventory.Versions), maxItems)
	}
}

// A page that advertises more while returning nothing cannot make progress;
// reporting exhaustion there would fabricate completeness.
func TestHistoryQualifiedReportsStalledPagination(t *testing.T) {
	requests := 0
	cf := historyServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("start") == "0" {
			_, _ = w.Write([]byte(historyPage("/rest/experimental/content/70/version?start=1", 5)))
			return
		}
		// Still advertises a next cursor but returns no rows: paging is stalled.
		_, _ = w.Write([]byte(historyPage("/rest/experimental/content/70/version?start=1")))
	})
	inventory, err := cf.HistoryQualified(context.Background(), "70")
	if err != nil {
		t.Fatalf("HistoryQualified: %v", err)
	}
	if inventory.Complete || inventory.PartialReason != domain.HistoryPartialPaginationStalled {
		t.Fatalf("inventory=%+v", inventory)
	}
	if len(inventory.Versions) != 1 || requests != 2 {
		t.Fatalf("collected %d versions over %d requests", len(inventory.Versions), requests)
	}
}

// The legacy compatibility surface keeps returning the slice and drops the
// qualification, so existing internal callers are unaffected.
func TestHistoryDelegatesAndDropsQualification(t *testing.T) {
	requests := 0
	cf := historyServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		next := "/rest/experimental/content/70/version?start=" + strconv.Itoa(start+1)
		_, _ = w.Write([]byte(historyPage(next, maxPages-start)))
	})
	got, err := cf.History(context.Background(), "70")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != maxPages || requests != maxPages {
		t.Fatalf("legacy listing collected %d versions over %d requests", len(got), requests)
	}
	if got[0].Number != maxPages {
		t.Errorf("version[0].Number = %d, want %d", got[0].Number, maxPages)
	}
}

func TestHistoryQualifiedPropagatesBackendErrors(t *testing.T) {
	cf := historyServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	inventory, err := cf.HistoryQualified(context.Background(), "70")
	if err == nil {
		t.Fatalf("expected a backend error, got inventory=%+v", inventory)
	}
	if inventory.Versions != nil || inventory.Complete {
		t.Fatalf("a failed read must not return a usable inventory: %+v", inventory)
	}
}

// The adapter satisfies both the legacy port method and the optional qualified
// capability, which is what lets the application layer select at runtime.
var (
	_ domain.DocStore               = (*Confluence)(nil)
	_ domain.QualifiedHistoryReader = (*Confluence)(nil)
)
