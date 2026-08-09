package jira

import (
	"math"
	"testing"
)

func TestJiraOffsetCursorClassifiesProgress(t *testing.T) {
	totalTwo := 2
	totalZero := 0
	tests := []struct {
		name      string
		start     int
		returned  int
		rows      int
		total     *int
		wantMatch bool
		wantState jiraOffsetAdvance
		wantNext  int
		wantStart int
	}{
		{name: "contiguous optional total", rows: 1, wantMatch: true, wantState: jiraOffsetMore, wantNext: 1, wantStart: 1},
		{name: "complete", start: 1, returned: 1, rows: 1, total: &totalTwo, wantMatch: true, wantState: jiraOffsetComplete, wantNext: 2, wantStart: 1},
		{name: "empty total is complete", total: &totalZero, wantMatch: true, wantState: jiraOffsetComplete},
		{name: "empty unknown total stalls", wantMatch: true, wantState: jiraOffsetStalled},
		{name: "beyond total", start: 1, returned: 1, rows: 2, total: &totalTwo, wantMatch: true, wantState: jiraOffsetBeyondTotal, wantNext: 3, wantStart: 1},
		{name: "overflow", start: int(^uint(0) >> 1), returned: int(^uint(0) >> 1), rows: 1, wantMatch: true, wantState: jiraOffsetOverflow, wantNext: int(^uint(0) >> 1), wantStart: int(^uint(0) >> 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cursor := jiraOffsetCursor{startAt: test.start}
			if got := cursor.matches(test.returned); got != test.wantMatch {
				t.Fatalf("matches=%t, want %t", got, test.wantMatch)
			}
			decision := cursor.advance(test.rows, test.total)
			if decision.state != test.wantState || decision.next != test.wantNext {
				t.Fatalf("decision=%+v, want state=%d next=%d", decision, test.wantState, test.wantNext)
			}
			if got := cursor.requested(); got != test.wantStart {
				t.Fatalf("start=%d, want %d", got, test.wantStart)
			}
		})
	}
	cursor := jiraOffsetCursor{startAt: 2}
	if cursor.matches(1) || cursor.requested() != 2 {
		t.Fatalf("offset mismatch changed cursor: start=%d", cursor.requested())
	}
}

func FuzzJiraOffsetCursor(f *testing.F) {
	f.Add(0, 1, -1)
	f.Add(7, 0, -1)
	f.Add(7, 2, 8)
	f.Add(math.MaxInt, 1, -1)
	f.Fuzz(func(t *testing.T, start, rows, totalValue int) {
		if start < 0 {
			start = -(start + 1)
		}
		var total *int
		if totalValue >= 0 {
			total = &totalValue
		}
		cursor := jiraOffsetCursor{startAt: start}
		decision := cursor.advance(rows, total)
		if decision.next < 0 {
			t.Fatalf("negative next offset: start=%d rows=%d decision=%+v", start, rows, decision)
		}
		if decision.state == jiraOffsetMore {
			if rows <= 0 || cursor.requested() != decision.next || decision.next <= start {
				t.Fatalf("invalid progress: start=%d rows=%d total=%v decision=%+v", start, rows, total, decision)
			}
		} else if cursor.requested() != start {
			t.Fatalf("terminal decision mutated cursor: start=%d rows=%d total=%v decision=%+v got=%d", start, rows, total, decision, cursor.requested())
		}
	})
}
