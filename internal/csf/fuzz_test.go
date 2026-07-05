package csf

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// csfSeeds are small literals that exercise the lexer/decoder edge cases:
// empty input, a bare element, malformed markup, entities/CDATA, deeply nested
// elements, an XML prolog (which the synthetic <root> wrapper moves out of
// prolog position), and raw control bytes.
var csfSeeds = [][]byte{
	[]byte(""),
	[]byte("<p>"),
	[]byte("<p>hello</p>"),
	[]byte("<p>unclosed"),
	[]byte("</p>"),
	[]byte("<p><p></p>"),
	[]byte("&nbsp;&mdash;&hellip;"),
	[]byte("<p>&amp; &lt; &gt;</p>"),
	[]byte("<![CDATA[if a < b && c > d]]>"),
	[]byte("<ac:structured-macro ac:name=\"code\"><ac:plain-text-body><![CDATA[x<y]]></ac:plain-text-body></ac:structured-macro>"),
	[]byte("<a><b><c><d><e><f><g><h></h></g></f></e></d></c></b></a>"),
	[]byte("<?xml version=\"1.0\"?><p>x</p>"),
	[]byte("<!DOCTYPE html><p>x</p>"),
	[]byte("<p attr=\"value with \\\"quote\\\"\">x</p>"),
	[]byte("<ri:attachment ri:filename=\"\"/>"),
	[]byte("<ac:structured-macro ac:name=\"\"/>"),
	[]byte("\x00\x01\x02 control bytes"),
	[]byte("<root>nested literal root</root>"),
	[]byte("text only, no markup"),
	[]byte(`<p xmlns:ac="x" ac:foo="1">y</p>`), // namespaced attrs the parser skips
	[]byte("</a></b></c>"),                     // unbalanced closes stress the stack guard
}

// seedCSF feeds the on-disk sample plus the small literals into f.Add.
func seedCSF(f *testing.F) {
	if sample, err := os.ReadFile(filepath.Join("testdata", "sample.csf")); err == nil {
		f.Add(sample)
	}
	for _, s := range csfSeeds {
		f.Add(s)
	}
}

// FuzzParse asserts Parse never panics on any input, returns a non-nil node on
// success, and — critically for the write path — never mutates the caller's
// bytes (byte-stability: the mirror stores and re-pushes the exact stored bytes).
func FuzzParse(f *testing.F) {
	seedCSF(f)
	f.Fuzz(func(t *testing.T, raw []byte) {
		// Keep an independent copy to detect any in-place mutation of the input.
		orig := append([]byte(nil), raw...)

		node, err := Parse(raw)

		if !bytes.Equal(raw, orig) {
			t.Fatalf("Parse mutated its input: before=%q after=%q", orig, raw)
		}
		if err == nil && node == nil {
			t.Fatal("Parse returned nil node with nil error")
		}
		if err == nil {
			checkOffsets(t, node, raw, 0, len(raw))
		}
	})
}

// checkOffsets asserts the offset invariants the md→CSF block merge relies on:
// every element's range is in-bounds, starts at '<', ends at '>', nests inside
// its parent, and siblings appear in document order without overlapping.
func checkOffsets(t *testing.T, n *Node, raw []byte, lo, hi int) {
	t.Helper()
	prevEnd := lo
	for _, c := range n.Children {
		if c.Start < prevEnd || c.End < c.Start || c.End > hi {
			t.Fatalf("offset violation: %s [%d,%d) outside [%d,%d) of parent %s in %q",
				c.Name, c.Start, c.End, prevEnd, hi, n.Name, raw)
		}
		if c.Type == Element {
			if c.Start >= len(raw) || raw[c.Start] != '<' {
				t.Fatalf("element %s Start=%d does not point at '<' in %q", c.Name, c.Start, raw)
			}
			if c.End < 1 || raw[c.End-1] != '>' {
				t.Fatalf("element %s End=%d does not follow '>' in %q", c.Name, c.End, raw)
			}
			checkOffsets(t, c, raw, c.Start, c.End)
		}
		prevEnd = c.End
	}
}

// FuzzValidate asserts Validate never panics, the returned diagnostics are
// safely iterable, HasErrors never panics, and any reported line/col stay
// non-negative (lineCol must never underflow on malformed/empty input).
func FuzzValidate(f *testing.F) {
	seedCSF(f)
	f.Fuzz(func(t *testing.T, raw []byte) {
		problems := Validate(raw)
		for _, p := range problems {
			if p.Line < 0 || p.Col < 0 {
				t.Fatalf("Validate reported negative position: line=%d col=%d rule=%q", p.Line, p.Col, p.Rule)
			}
			_ = p.Severity
			_ = p.Rule
			_ = p.Message
		}
		_ = HasErrors(problems)
	})
}
