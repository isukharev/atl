package csf

import "testing"

func TestTableSpanBoundsServerAttribute(t *testing.T) {
	n := &Node{Type: Element, Attr: []Attr{
		{Name: Name{Local: "colspan"}, Value: "100"},
		{Name: Name{Local: "rowspan"}, Value: "101"},
	}}
	if got := TableSpan(n, "colspan"); got != MaxTableSpan || TableSpanExceedsMax(n, "colspan") {
		t.Fatalf("at-cap colspan = %d/exceeds=%t", got, TableSpanExceedsMax(n, "colspan"))
	}
	if got := TableSpan(n, "rowspan"); got != MaxTableSpan || !TableSpanExceedsMax(n, "rowspan") {
		t.Fatalf("over-cap rowspan = %d/exceeds=%t", got, TableSpanExceedsMax(n, "rowspan"))
	}
	for _, value := range []string{"2000000", "999999999999999999999999999999999999999999999999"} {
		n.Attr[0].Value = value
		if got := TableSpan(n, "colspan"); got != MaxTableSpan || !TableSpanExceedsMax(n, "colspan") {
			t.Errorf("oversized colspan %q = %d/exceeds=%t", value, got, TableSpanExceedsMax(n, "colspan"))
		}
	}
	for _, value := range []string{"", "0", "-1", "not-a-number"} {
		n.Attr[0].Value = value
		if got := TableSpan(n, "colspan"); got != 1 {
			t.Errorf("TableSpan(%q) = %d, want 1", value, got)
		}
		if TableSpanExceedsMax(n, "colspan") {
			t.Errorf("malformed span %q reported as oversized", value)
		}
	}
	n.Attr[0].Value = " +2 "
	if got := TableSpan(n, "colspan"); got != 2 || TableSpanExceedsMax(n, "colspan") {
		t.Fatalf("trimmed positive span = %d/exceeds=%t", got, TableSpanExceedsMax(n, "colspan"))
	}
}
