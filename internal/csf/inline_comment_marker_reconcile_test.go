package csf

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReconcileInlineCommentMarkerInsertion(t *testing.T) {
	before := []byte(`<p>alpha <strong>selected &amp; nested</strong> omega</p><p>tail</p>`)
	after := []byte(`<p>alpha <ac:inline-comment-marker ac:ref="new-ref"><strong>selected &amp; nested</strong></ac:inline-comment-marker> omega</p><p>tail</p>`)
	matched, err := ReconcileInlineCommentMarkerInsertion(before, after, "new-ref", "selected & nested")
	if err != nil || !matched {
		t.Fatalf("matched=%t err=%v", matched, err)
	}
}

func TestReconcileInlineCommentMarkerInsertionFailsClosed(t *testing.T) {
	before := []byte(`<p>alpha selected omega</p>`)
	valid := []byte(`<p>alpha <ac:inline-comment-marker ac:ref="new-ref">selected</ac:inline-comment-marker> omega</p>`)
	tests := []struct {
		name      string
		before    []byte
		after     []byte
		markerRef string
		selection string
	}{
		{name: "body also changed", before: before, after: []byte(`<p>changed <ac:inline-comment-marker ac:ref="new-ref">selected</ac:inline-comment-marker> omega</p>`), markerRef: "new-ref", selection: "selected"},
		{name: "wrong selection", before: before, after: valid, markerRef: "new-ref", selection: "other"},
		{name: "wrong ref", before: before, after: valid, markerRef: "other-ref", selection: "selected"},
		{name: "duplicate ref", before: before, after: []byte(`<p>alpha <ac:inline-comment-marker ac:ref="new-ref">selected</ac:inline-comment-marker> omega</p><p><ac:inline-comment-marker ac:ref="new-ref">extra</ac:inline-comment-marker></p>`), markerRef: "new-ref", selection: "selected"},
		{name: "preexisting ref", before: []byte(`<p><ac:inline-comment-marker ac:ref="new-ref">old</ac:inline-comment-marker> alpha selected omega</p>`), after: valid, markerRef: "new-ref", selection: "selected"},
		{name: "empty ref", before: before, after: valid, selection: "selected"},
		{name: "empty selection", before: before, after: valid, markerRef: "new-ref"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matched, err := ReconcileInlineCommentMarkerInsertion(test.before, test.after, test.markerRef, test.selection)
			if err != nil || matched {
				t.Fatalf("matched=%t err=%v", matched, err)
			}
		})
	}
}

func TestReconcileInlineCommentMarkerInsertionReportsMalformedCSF(t *testing.T) {
	if matched, err := ReconcileInlineCommentMarkerInsertion([]byte(`<p>before</p>`), []byte(`<p>broken`), "new-ref", "broken"); err == nil || matched {
		t.Fatalf("matched=%t err=%v", matched, err)
	}
}

func FuzzReconcileInlineCommentMarkerInsertionNeverAcceptsAdditionalMutation(f *testing.F) {
	f.Add("selected")
	f.Add(" café 😀 & <exact> ")
	f.Fuzz(func(t *testing.T, selection string) {
		if selection == "" || !utf8.ValidString(selection) ||
			strings.IndexFunc(selection, func(value rune) bool { return !validXML10TextRune(value) }) >= 0 {
			t.Skip()
		}
		var escaped bytes.Buffer
		if err := xml.EscapeText(&escaped, []byte(selection)); err != nil {
			t.Fatal(err)
		}
		before := append(append([]byte(`<p>prefix`), escaped.Bytes()...), []byte(`suffix</p>`)...)
		after := append([]byte(`<p>prefix<ac:inline-comment-marker ac:ref="new-ref">`), escaped.Bytes()...)
		after = append(after, []byte(`</ac:inline-comment-marker>suffix</p>`)...)
		matched, err := ReconcileInlineCommentMarkerInsertion(before, after, "new-ref", selection)
		if err != nil || !matched {
			t.Fatalf("valid wrapper matched=%t err=%v", matched, err)
		}
		after = append(after, []byte(`<p>unrelated mutation</p>`)...)
		matched, _ = ReconcileInlineCommentMarkerInsertion(before, after, "new-ref", selection)
		if matched {
			t.Fatal("accepted marker insertion plus unrelated mutation")
		}
	})
}

func validXML10TextRune(value rune) bool {
	switch {
	case value == '\t' || value == '\n' || value == '\r':
		return true
	case value >= 0x20 && value <= 0xd7ff:
		return true
	case value >= 0xe000 && value <= 0xfffd:
		return true
	case value >= 0x10000 && value <= 0x10ffff:
		return true
	default:
		return false
	}
}
