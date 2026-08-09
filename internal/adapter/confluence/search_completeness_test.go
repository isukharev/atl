package confluence

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestSearchCompleteQualifiesTerminalEvidence(t *testing.T) {
	tests := []struct {
		name         string
		rows         int
		limit        int
		cursor       string
		totals       map[string]any
		nextLink     string
		wantComplete bool
		wantNext     string
		wantReason   string
		wantTotal    *int
	}{
		{name: "short terminal page without total", rows: 24, limit: 25, wantComplete: true},
		{name: "full terminal page without total", rows: 25, limit: 25, wantReason: "full search page without terminal pagination evidence"},
		{name: "full terminal page with null totals", rows: 25, limit: 25, totals: map[string]any{"totalCount": nil, "totalSize": nil}, wantReason: "full search page without terminal pagination evidence"},
		{name: "totalCount proves terminal page", rows: 25, limit: 25, totals: map[string]any{"totalCount": 25}, wantComplete: true, wantTotal: searchTotal(25)},
		{name: "totalSize proves terminal page", rows: 25, limit: 25, totals: map[string]any{"totalSize": 25}, wantComplete: true, wantTotal: searchTotal(25)},
		{name: "matching totals prove terminal page", rows: 25, limit: 25, totals: map[string]any{"totalCount": 25, "totalSize": 25}, wantComplete: true, wantTotal: searchTotal(25)},
		{name: "offset total proves terminal page", rows: 25, limit: 25, cursor: "25", totals: map[string]any{"totalSize": 50}, wantComplete: true, wantTotal: searchTotal(50)},
		{name: "totalCount proves unreachable matches", rows: 24, limit: 25, totals: map[string]any{"totalCount": 25}, wantReason: "25 total matches but only 24 were reachable", wantTotal: searchTotal(25)},
		{name: "totalSize proves unreachable matches", rows: 24, limit: 25, totals: map[string]any{"totalSize": 25}, wantReason: "25 total matches but only 24 were reachable", wantTotal: searchTotal(25)},
		{name: "contradictory totals", rows: 24, limit: 25, totals: map[string]any{"totalCount": 24, "totalSize": 25}, wantReason: "contradictory total match counts"},
		{name: "negative totalCount", limit: 25, totals: map[string]any{"totalCount": -1}, wantReason: "negative total match count"},
		{name: "negative totalSize", limit: 25, totals: map[string]any{"totalSize": -1}, wantReason: "negative total match count"},
		{name: "results exceed total", rows: 2, limit: 25, totals: map[string]any{"totalSize": 1}, wantReason: "beyond its reported total", wantTotal: searchTotal(1)},
		{name: "qualified continuation", rows: 25, limit: 25, nextLink: "/rest/api/search?start=25", wantNext: "25"},
		{name: "continuation contradicts total", rows: 25, limit: 25, totals: map[string]any{"totalSize": 25}, nextLink: "/rest/api/search?start=25", wantReason: "after reaching its reported total", wantTotal: searchTotal(25)},
		{name: "empty continuing page", limit: 25, nextLink: "/rest/api/search?start=0", wantReason: "empty page with a next link"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := searchCompletenessResponse(t, test.rows, test.totals, test.nextLink)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Query().Get("limit") != strconv.Itoa(test.limit) || request.URL.Query().Get("start") != firstNonEmpty(test.cursor, "0") {
					t.Fatalf("unexpected pagination query: %s", request.URL.RawQuery)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			t.Cleanup(server.Close)

			adapter := &Confluence{c: newTestClient(server.URL), base: server.URL}
			page, err := adapter.SearchComplete(context.Background(), "type=page", test.limit, test.cursor)
			if err != nil {
				t.Fatal(err)
			}
			if page.Complete != test.wantComplete || page.Next != test.wantNext || len(page.Results) != test.rows {
				t.Fatalf("page=%+v, want complete=%t next=%q rows=%d", page, test.wantComplete, test.wantNext, test.rows)
			}
			if test.wantReason == "" && page.PartialReason != "" {
				t.Fatalf("partial reason=%q, want empty", page.PartialReason)
			}
			if test.wantReason != "" && !strings.Contains(page.PartialReason, test.wantReason) {
				t.Fatalf("partial reason=%q, want substring %q", page.PartialReason, test.wantReason)
			}
			if !equalSearchTotal(page.ExactTotal, test.wantTotal) {
				t.Fatalf("exact total=%v, want %v", page.ExactTotal, test.wantTotal)
			}
		})
	}
}

func searchTotal(total int) *int { return &total }

func equalSearchTotal(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func TestSearchCompleteRejectsMalformedTotalEvidence(t *testing.T) {
	for _, field := range []string{"totalCount", "totalSize"} {
		t.Run(field, func(t *testing.T) {
			body := searchCompletenessResponse(t, 25, map[string]any{field: "25"}, "")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			t.Cleanup(server.Close)
			adapter := &Confluence{c: newTestClient(server.URL), base: server.URL}
			page, err := adapter.SearchComplete(context.Background(), "type=page", 25, "")
			if err == nil || len(page.Results) != 0 || page.Complete || page.Next != "" {
				t.Fatalf("page=%+v err=%v, want closed decode failure", page, err)
			}
		})
	}
}

func searchCompletenessResponse(t *testing.T, rows int, totals map[string]any, nextLink string) []byte {
	t.Helper()
	results := make([]map[string]any, rows)
	for index := range results {
		results[index] = map[string]any{
			"content": map[string]any{
				"id": strconv.Itoa(index + 1), "title": "Synthetic page",
				"space": map[string]any{"key": "DOC"}, "version": map[string]any{"number": 1},
			},
		}
	}
	body := map[string]any{
		"results": results,
		"size":    rows,
		"_links":  map[string]any{"next": nextLink},
	}
	for key, value := range totals {
		body[key] = value
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
