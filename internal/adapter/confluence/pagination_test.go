package confluence

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestConfluencePageCursorPreservesSignalOnlyPagination(t *testing.T) {
	tests := []struct {
		name      string
		start     int
		rows      int
		next      string
		wantState confluencePageAdvance
		wantStart int
	}{
		{name: "terminal empty page", rows: 0, wantState: confluencePageExhausted},
		{name: "terminal populated page", rows: 3, wantState: confluencePageExhausted},
		{name: "relative link is a signal", rows: 2, next: "/ignored", wantState: confluencePageMore, wantStart: 2},
		{name: "absolute link is not followed", start: 2, rows: 4, next: "https://foreign.example.invalid/not-followed", wantState: confluencePageMore, wantStart: 6},
		{name: "empty continuing page stalls", start: 7, next: "/ignored", wantState: confluencePageStalled, wantStart: 7},
		{name: "negative count stalls", start: 7, rows: -1, next: "/ignored", wantState: confluencePageStalled, wantStart: 7},
		{name: "overflow stalls", start: int(^uint(0) >> 1), rows: 1, next: "/ignored", wantState: confluencePageStalled, wantStart: int(^uint(0) >> 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cursor := confluencePageCursor{start: test.start}
			if got := cursor.advance(test.rows, test.next); got != test.wantState {
				t.Fatalf("advance=%d, want %d", got, test.wantState)
			}
			if got := cursor.startAt(); got != test.wantStart {
				t.Fatalf("start=%d, want %d", got, test.wantStart)
			}
		})
	}
	cursor := confluencePageCursor{start: math.MaxInt}
	if end, ok := cursor.checkedEnd(1); ok || end != math.MaxInt {
		t.Fatalf("overflow end=%d ok=%t, want unchanged false", end, ok)
	}
}

func TestSearchCompleteRejectsNoncontiguousAndOverflowPages(t *testing.T) {
	tests := []struct {
		name         string
		cursor       string
		body         string
		wantReason   string
		wantComplete bool
	}{
		{
			name:       "noncontiguous",
			cursor:     "1",
			body:       `{"start":2,"results":[{"content":{"id":"1"}}],"_links":{"next":"/ignored"}}`,
			wantReason: "non-contiguous",
		},
		{
			name:       "overflow",
			cursor:     strconv.Itoa(math.MaxInt),
			body:       `{"start":` + strconv.Itoa(math.MaxInt) + `,"results":[{"content":{"id":"1"}}],"_links":{"next":"/ignored"}}`,
			wantReason: "overflowed",
		},
		{
			name:       "negative total",
			body:       `{"start":0,"totalCount":-1,"results":[],"_links":{}}`,
			wantReason: "negative total",
		},
		{
			name:       "end beyond total",
			cursor:     "1",
			body:       `{"start":1,"totalCount":2,"results":[{"content":{"id":"1"}},{"content":{"id":"2"}}],"_links":{}}`,
			wantReason: "beyond its reported total",
		},
		{
			name:       "next at total",
			body:       `{"start":0,"totalCount":1,"results":[{"content":{"id":"1"}}],"_links":{"next":"/ignored"}}`,
			wantReason: "after reaching its reported total",
		},
		{
			name:       "terminal before total",
			body:       `{"start":0,"totalCount":2,"results":[{"content":{"id":"1"}}],"_links":{}}`,
			wantReason: "only 1 were reachable",
		},
		{
			name:         "terminal exactly at MaxInt total",
			cursor:       strconv.Itoa(math.MaxInt - 1),
			body:         `{"start":` + strconv.Itoa(math.MaxInt-1) + `,"totalCount":` + strconv.Itoa(math.MaxInt) + `,"results":[{"content":{"id":"1"}}],"_links":{}}`,
			wantComplete: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)
			adapter := &Confluence{c: newTestClient(server.URL), base: server.URL}
			page, err := adapter.SearchComplete(context.Background(), "type=page", 1, test.cursor)
			if err != nil {
				t.Fatal(err)
			}
			if page.Complete != test.wantComplete || page.Next != "" || !strings.Contains(page.PartialReason, test.wantReason) {
				t.Fatalf("page=%+v, want complete=%t reason containing %q", page, test.wantComplete, test.wantReason)
			}
		})
	}
}

func FuzzConfluencePageCursor(f *testing.F) {
	f.Add(0, 1, "/next")
	f.Add(7, 0, "/next")
	f.Add(7, 1, "")
	f.Add(math.MaxInt, 1, "/next")
	f.Fuzz(func(t *testing.T, start, rows int, next string) {
		if start < 0 {
			start = -(start + 1)
		}
		cursor := confluencePageCursor{start: start}
		state := cursor.advance(rows, next)
		switch state {
		case confluencePageMore:
			if next == "" || rows <= 0 || cursor.startAt() <= start {
				t.Fatalf("progress without positive bounded continuation: start=%d rows=%d next=%q got=%d", start, rows, next, cursor.startAt())
			}
		case confluencePageExhausted:
			if next != "" || cursor.startAt() != start {
				t.Fatalf("invalid exhaustion: start=%d rows=%d next=%q got=%d", start, rows, next, cursor.startAt())
			}
		case confluencePageStalled:
			if next == "" || cursor.startAt() != start {
				t.Fatalf("invalid stall: start=%d rows=%d next=%q got=%d", start, rows, next, cursor.startAt())
			}
		default:
			t.Fatalf("unknown state %d", state)
		}
	})
}
