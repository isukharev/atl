package confluence

import "testing"

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
}

func FuzzConfluencePageCursor(f *testing.F) {
	f.Add(0, 1, "/next")
	f.Add(7, 0, "/next")
	f.Add(7, 1, "")
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
