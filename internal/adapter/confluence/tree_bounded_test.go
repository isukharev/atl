package confluence

import (
	"context"
	"encoding/json"
	"fmt"
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

func qualifiedTreeRow(id string, ancestors ...string) string {
	ancestorRows := make([]string, 0, len(ancestors))
	for _, ancestor := range ancestors {
		ancestorRows = append(ancestorRows, fmt.Sprintf(`{"id":%q,"title":"Ancestor"}`, ancestor))
	}
	return fmt.Sprintf(`{"id":%q,"type":"page","title":"Page %s","space":{"key":"DOC"},"version":{"number":1},"ancestors":[%s]}`,
		id, id, strings.Join(ancestorRows, ","))
}

func qualifiedTreeResponse(rows []string, start, limit, total int, next string) string {
	return fmt.Sprintf(`{"results":[%s],"start":%d,"limit":%d,"size":%d,"totalCount":%d,"_links":{"next":%q}}`,
		strings.Join(rows, ","), start, limit, len(rows), total, next)
}

func TestTreeQualifiedPhysicalRequestBudgetStopsBelowOrchestration(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(qualifiedTreeResponse([]string{qualifiedTreeRow("1")}, 0, 20, 2, "ignored")))
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
			body:  qualifiedTreeResponse([]string{qualifiedTreeRow("1"), qualifiedTreeRow("2"), qualifiedTreeRow("3")}, 0, 20, 3, ""),
			items: 2, reason: domain.ConfluenceTreePartialItemLimit,
		},
		{
			name:  "noncontiguous start",
			body:  qualifiedTreeResponse([]string{qualifiedTreeRow("1")}, 7, 20, 8, ""),
			items: 0, reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name:  "contradictory size",
			body:  strings.Replace(qualifiedTreeResponse([]string{qualifiedTreeRow("1")}, 0, 20, 1, ""), `"size":1`, `"size":2`, 1),
			items: 0, reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name:  "missing result collection",
			body:  `{"start":0,"limit":200,"size":0,"totalCount":0,"_links":{}}`,
			items: 0, reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name: "missing coordinates", body: `{"results":[],"_links":{}}`,
			reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name: "missing total", body: `{"results":[],"start":0,"limit":20,"size":0,"_links":{}}`,
			reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name: "null total with valid sibling", body: `{"results":[],"start":0,"limit":20,"size":0,"totalCount":null,"totalSize":0,"_links":{}}`,
			reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name: "conflicting totals", body: `{"results":[],"start":0,"limit":20,"size":0,"totalCount":0,"totalSize":1,"_links":{}}`,
			reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name: "overflow total", body: `{"results":[],"start":0,"limit":20,"size":0,"totalCount":9223372036854775808,"_links":{}}`,
			reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name: "null coordinates", body: `{"results":[],"start":null,"limit":null,"size":null,"totalCount":null,"_links":null}`,
			reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name: "negative start", body: `{"results":[],"start":-1,"limit":20,"size":0,"totalCount":0,"_links":{}}`,
			reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name: "negative size", body: `{"results":[],"start":0,"limit":20,"size":-1,"totalCount":0,"_links":{}}`,
			reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name: "negative total", body: `{"results":[],"start":0,"limit":20,"size":0,"totalCount":-1,"_links":{}}`,
			reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name: "zero limit", body: `{"results":[],"start":0,"limit":0,"size":0,"totalCount":0,"_links":{}}`,
			reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name: "size exceeds limit", body: qualifiedTreeResponse([]string{qualifiedTreeRow("1"), qualifiedTreeRow("2")}, 0, 1, 2, ""),
			reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name: "terminal remainder", body: qualifiedTreeResponse([]string{qualifiedTreeRow("1")}, 0, 20, 2, ""),
			reason: domain.ConfluenceTreePartialPaginationUnqualified,
		},
		{
			name: "next beyond total", body: qualifiedTreeResponse([]string{qualifiedTreeRow("1")}, 0, 20, 1, "ignored"),
			reason: domain.ConfluenceTreePartialPaginationUnqualified,
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

func TestTreeQualifiedRejectsChangingTotalBeforeConsumingConflictingPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("start") == "0" {
			_, _ = w.Write([]byte(qualifiedTreeResponse([]string{qualifiedTreeRow("1")}, 0, 1, 2, "ignored")))
			return
		}
		_, _ = w.Write([]byte(qualifiedTreeResponse([]string{qualifiedTreeRow("2")}, 1, 1, 3, "ignored")))
	}))
	t.Cleanup(srv.Close)
	result, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).TreeQualified(t.Context(), boundedTreeRequest())
	if err != nil {
		t.Fatalf("TreeQualified: %v", err)
	}
	if result.Complete || result.PartialReason != domain.ConfluenceTreePartialPaginationUnqualified ||
		len(result.Pages) != 1 || result.Pages[0].ID != "1" || result.ScannedItems != 1 {
		t.Fatalf("result=%+v", result)
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
			name: "terminal", body: qualifiedTreeResponse([]string{qualifiedTreeRow("1")}, 0, 20, 1, ""),
			maxItems: 10, maxScanned: 20, wantPages: 1, wantScanned: 1, complete: true,
		},
		{
			name: "terminal at exact item limit", body: qualifiedTreeResponse([]string{qualifiedTreeRow("1"), qualifiedTreeRow("2")}, 0, 20, 2, ""),
			maxItems: 2, maxScanned: 20, wantPages: 2, wantScanned: 2, complete: true,
		},
		{
			name: "terminal at exact scan limit", body: qualifiedTreeResponse([]string{qualifiedTreeRow("1"), qualifiedTreeRow("2")}, 0, 2, 2, ""),
			maxItems: 10, maxScanned: 2, wantPages: 2, wantScanned: 2, complete: true,
		},
		{
			name: "stalled", body: `{"results":[],"start":0,"limit":20,"size":0,"totalCount":1,"_links":{"next":"ignored"}}`,
			maxItems: 10, maxScanned: 20, reason: domain.ConfluenceTreePartialPaginationStalled,
		},
		{
			name: "scan limit", body: qualifiedTreeResponse([]string{qualifiedTreeRow("1")}, 0, 1, 2, "ignored"),
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

func TestTreeQualifiedRefusesBackendRowsBeyondRequestedScanPage(t *testing.T) {
	var requestedLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(qualifiedTreeResponse([]string{qualifiedTreeRow("1"), qualifiedTreeRow("2")}, 0, 2, 2, "")))
	}))
	t.Cleanup(srv.Close)
	request := boundedTreeRequest()
	request.MaxScannedItems = 1
	result, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).TreeQualified(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if requestedLimit != "1" || result.Complete || result.PartialReason != domain.ConfluenceTreePartialPaginationUnqualified ||
		len(result.Pages) != 0 || result.ScannedItems != 0 {
		t.Fatalf("requested_limit=%q result=%+v", requestedLimit, result)
	}
}

func TestTreeQualifiedRejectsIncompleteOrContradictoryRows(t *testing.T) {
	base := qualifiedTreeRow("1")
	for _, tc := range []struct {
		name string
		row  string
	}{
		{name: "missing type", row: strings.Replace(base, `,"type":"page"`, "", 1)},
		{name: "null type", row: strings.Replace(base, `"type":"page"`, `"type":null`, 1)},
		{name: "wrong type", row: strings.Replace(base, `"type":"page"`, `"type":"attachment"`, 1)},
		{name: "missing title", row: strings.Replace(base, `,"title":"Page 1"`, "", 1)},
		{name: "null title", row: strings.Replace(base, `"title":"Page 1"`, `"title":null`, 1)},
		{name: "blank title", row: strings.Replace(base, `"title":"Page 1"`, `"title":"  "`, 1)},
		{name: "missing space", row: strings.Replace(base, `,"space":{"key":"DOC"}`, "", 1)},
		{name: "null space", row: strings.Replace(base, `"space":{"key":"DOC"}`, `"space":null`, 1)},
		{name: "wrong space", row: strings.Replace(base, `"key":"DOC"`, `"key":"OTHER"`, 1)},
		{name: "missing version", row: strings.Replace(base, `,"version":{"number":1}`, "", 1)},
		{name: "null version", row: strings.Replace(base, `"version":{"number":1}`, `"version":null`, 1)},
		{name: "zero version", row: strings.Replace(base, `"number":1`, `"number":0`, 1)},
		{name: "missing ancestors", row: strings.Replace(base, `,"ancestors":[]`, "", 1)},
		{name: "null ancestors", row: strings.Replace(base, `"ancestors":[]`, `"ancestors":null`, 1)},
		{name: "malformed parent", row: qualifiedTreeRow("1", ".")},
		{name: "oversize opaque parent", row: qualifiedTreeRow("1", strings.Repeat("x", 257))},
		{name: "self parent", row: qualifiedTreeRow("1", "1")},
		{name: "duplicate ancestors", row: qualifiedTreeRow("1", "2", "2")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(qualifiedTreeResponse([]string{tc.row}, 0, 20, 1, "")))
			}))
			defer srv.Close()
			page, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).TreeQualified(t.Context(), boundedTreeRequest())
			if err != nil {
				t.Fatal(err)
			}
			if page.Complete || page.PartialReason != domain.ConfluenceTreePartialPaginationUnqualified || len(page.Pages) != 0 || page.ScannedItems != 0 {
				t.Fatalf("page=%+v", page)
			}
		})
	}
}

func TestTreeQualifiedValidatesEachPageAtomically(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("start") == "0" {
			_, _ = w.Write([]byte(qualifiedTreeResponse([]string{qualifiedTreeRow("1")}, 0, 2, 3, "ignored")))
			return
		}
		invalid := strings.Replace(qualifiedTreeRow("3"), `"title":"Page 3"`, `"title":" "`, 1)
		_, _ = w.Write([]byte(qualifiedTreeResponse([]string{qualifiedTreeRow("2"), invalid}, 1, 2, 3, "")))
	}))
	t.Cleanup(srv.Close)
	request := boundedTreeRequest()
	request.MaxScannedItems = 3
	page, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).TreeQualified(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if page.Complete || page.PartialReason != domain.ConfluenceTreePartialPaginationUnqualified ||
		len(page.Pages) != 1 || page.Pages[0].ID != "1" || page.ScannedItems != 1 {
		t.Fatalf("page=%+v", page)
	}
}

func TestTreeQualifiedAcceptsBoundedOpaqueReadIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(qualifiedTreeResponse([]string{qualifiedTreeRow("page_opaque-1", "parent_opaque-1")}, 0, 20, 1, "")))
	}))
	t.Cleanup(srv.Close)
	page, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).TreeQualified(t.Context(), boundedTreeRequest())
	if err != nil || !page.Complete || len(page.Pages) != 1 || page.Pages[0].Parent != "parent_opaque-1" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestQualifiedConfluenceContentSearchTotalWireSpellings(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
		ok   bool
	}{
		{name: "official totalCount", body: `{"totalCount":7}`, want: 7, ok: true},
		{name: "observed totalSize", body: `{"totalSize":7}`, want: 7, ok: true},
		{name: "equal dual", body: `{"totalCount":7,"totalSize":7}`, want: 7, ok: true},
		{name: "conflicting dual", body: `{"totalCount":7,"totalSize":8}`},
		{name: "missing", body: `{}`},
		{name: "null official with valid sibling", body: `{"totalCount":null,"totalSize":7}`},
		{name: "null observed with valid sibling", body: `{"totalCount":7,"totalSize":null}`},
		{name: "negative", body: `{"totalCount":-1}`},
		{name: "fractional", body: `{"totalCount":1.5}`},
		{name: "string", body: `{"totalCount":"7"}`},
		{name: "exponent", body: `{"totalCount":7e0}`},
		{name: "int64 overflow", body: `{"totalCount":9223372036854775808}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire struct {
				TotalCount confluenceContentSearchWireTotal `json:"totalCount"`
				TotalSize  confluenceContentSearchWireTotal `json:"totalSize"`
			}
			if err := json.Unmarshal([]byte(tc.body), &wire); err != nil {
				t.Fatal(err)
			}
			got, ok := qualifiedConfluenceContentSearchTotal(wire.TotalCount, wire.TotalSize)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got=(%d,%t), want=(%d,%t)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestTreeQualifiedAcceptsObservedTotalSizeWireSpelling(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := strings.Replace(qualifiedTreeResponse([]string{qualifiedTreeRow("1")}, 0, 20, 1, ""), `"totalCount":1`, `"totalSize":1`, 1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	page, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).TreeQualified(t.Context(), boundedTreeRequest())
	if err != nil || !page.Complete || len(page.Pages) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestTreeQualifiedTotalAliasWireContract(t *testing.T) {
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
				_, _ = w.Write([]byte(`{"results":[` + qualifiedTreeRow("1") + `],"start":0,"limit":20,"size":1` + comma + `,"_links":{}}`))
			}))
			defer srv.Close()
			page, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).TreeQualified(t.Context(), boundedTreeRequest())
			if err != nil {
				t.Fatal(err)
			}
			if tc.complete {
				if !page.Complete || len(page.Pages) != 1 || page.ScannedItems != 1 {
					t.Fatalf("page=%+v", page)
				}
				return
			}
			if page.Complete || page.PartialReason != domain.ConfluenceTreePartialPaginationUnqualified || len(page.Pages) != 0 || page.ScannedItems != 0 {
				t.Fatalf("page=%+v", page)
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
