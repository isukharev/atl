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

func boundedTreeRequest() domain.ConfluenceTreeRequest {
	return domain.ConfluenceTreeRequest{Space: "DOC", MaxItems: 10, MaxScannedItems: 20}
}

func TestTreeQualifiedPhysicalRequestBudgetStopsBelowOrchestration(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"1","title":"Page","space":{"key":"DOC"},"version":{"number":1}}],"start":0,"size":1,"_links":{"next":"ignored"}}`))
	}))
	t.Cleanup(srv.Close)
	budget, err := domain.NewReadBudget(1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ctx := domain.WithSingleAttempt(domain.WithReadBudget(t.Context(), budget))
	result, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).TreeQualified(ctx, boundedTreeRequest())
	if err != nil {
		t.Fatalf("TreeQualified: %v", err)
	}
	if result.Complete || result.PartialReason != domain.ConfluenceTreePartialRequestLimit || len(result.Pages) != 1 {
		t.Fatalf("result=%+v", result)
	}
	if requests.Load() != 1 || budget.Usage().Attempts != 1 {
		t.Fatalf("physical requests=%d usage=%+v, want one", requests.Load(), budget.Usage())
	}
}

func TestTreeQualifiedAggregateResponseByteBudgetReturnsNoUnparsedBytes(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"1","title":"` + strings.Repeat("x", 128) + `"}],"_links":{}}`))
	}))
	t.Cleanup(srv.Close)
	budget, err := domain.NewReadBudget(1, 32)
	if err != nil {
		t.Fatal(err)
	}
	ctx := domain.WithSingleAttempt(domain.WithReadBudget(t.Context(), budget))
	result, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).TreeQualified(ctx, boundedTreeRequest())
	if err != nil {
		t.Fatalf("TreeQualified: %v", err)
	}
	if result.Complete || result.PartialReason != domain.ConfluenceTreePartialResponseByteLimit || len(result.Pages) != 0 {
		t.Fatalf("result=%+v", result)
	}
	if requests.Load() != 1 || budget.Usage().ResponseBytes != 32 {
		t.Fatalf("requests=%d usage=%+v", requests.Load(), budget.Usage())
	}
}

func TestTreeQualifiedItemLimitAndPaginationCoordinatesAreClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		items  int
		reason string
	}{
		{
			name:  "item limit",
			body:  `{"results":[{"id":"1"},{"id":"2"},{"id":"3"}],"start":0,"size":3,"_links":{}}`,
			items: 2, reason: domain.ConfluenceTreePartialItemLimit,
		},
		{
			name:  "noncontiguous start",
			body:  `{"results":[{"id":"1"}],"start":7,"size":1,"_links":{}}`,
			items: 0, reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name:  "contradictory size",
			body:  `{"results":[{"id":"1"}],"start":0,"size":2,"_links":{}}`,
			items: 0, reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name:  "missing result collection",
			body:  `{"start":0,"size":0,"_links":{}}`,
			items: 0, reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			request := boundedTreeRequest()
			request.MaxItems = 2
			result, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).TreeQualified(t.Context(), request)
			if err != nil {
				t.Fatalf("TreeQualified: %v", err)
			}
			if result.Complete || result.PartialReason != tc.reason || len(result.Pages) != tc.items {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestTreeQualifiedTerminalStalledAndScanLimitAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        string
		maxItems    int
		maxScanned  int
		wantPages   int
		wantScanned int
		complete    bool
		reason      string
	}{
		{
			name: "terminal", body: `{"results":[{"id":"1"}],"start":0,"size":1,"_links":{}}`,
			maxItems: 10, maxScanned: 20, wantPages: 1, wantScanned: 1, complete: true,
		},
		{
			name: "terminal at exact item limit", body: `{"results":[{"id":"1"},{"id":"2"}],"start":0,"size":2,"_links":{}}`,
			maxItems: 2, maxScanned: 20, wantPages: 2, wantScanned: 2, complete: true,
		},
		{
			name: "terminal at exact scan limit", body: `{"results":[{"id":"1"},{"id":"2"}],"start":0,"size":2,"_links":{}}`,
			maxItems: 10, maxScanned: 2, wantPages: 2, wantScanned: 2, complete: true,
		},
		{
			name: "stalled", body: `{"results":[],"start":0,"size":0,"_links":{"next":"ignored"}}`,
			maxItems: 10, maxScanned: 20, reason: domain.ConfluenceTreePartialPaginationStalled,
		},
		{
			name: "scan limit", body: `{"results":[{"id":"1"},{"id":"2"}],"start":0,"size":2,"_links":{}}`,
			maxItems: 10, maxScanned: 1, wantPages: 1, wantScanned: 1, reason: domain.ConfluenceTreePartialScanLimit,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			request := boundedTreeRequest()
			request.MaxItems = tc.maxItems
			request.MaxScannedItems = tc.maxScanned
			result, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).TreeQualified(t.Context(), request)
			if err != nil {
				t.Fatalf("TreeQualified: %v", err)
			}
			if result.Complete != tc.complete || result.PartialReason != tc.reason ||
				len(result.Pages) != tc.wantPages || result.ScannedItems != tc.wantScanned {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestTreeQualifiedDeadlineReturnsQualifiedPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	result, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).TreeQualified(ctx, boundedTreeRequest())
	if err != nil {
		t.Fatalf("TreeQualified: %v", err)
	}
	if result.Complete || result.PartialReason != domain.ConfluenceTreePartialDeadline || len(result.Pages) != 0 {
		t.Fatalf("result=%+v", result)
	}
}
