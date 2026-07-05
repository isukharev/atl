package csf

import (
	"strings"
	"testing"
)

// TestParseOffsets pins the offset semantics the block merge builds on: an
// element's [Start,End) range is exactly its source bytes, from the leading
// '<' of the start tag through the '>' closing the end tag.
func TestParseOffsets(t *testing.T) {
	raw := []byte(`<h1>Title</h1><p>Hello <strong>world</strong>&nbsp;!</p><hr/>`)
	root, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if root.Start != 0 || root.End != len(raw) {
		t.Fatalf("root span = [%d,%d), want [0,%d)", root.Start, root.End, len(raw))
	}
	var els []*Node
	for _, c := range root.Children {
		if c.Type == Element {
			els = append(els, c)
		}
	}
	if len(els) != 3 {
		t.Fatalf("top-level elements = %d, want 3", len(els))
	}
	want := []string{`<h1>Title</h1>`, `<p>Hello <strong>world</strong>&nbsp;!</p>`, `<hr/>`}
	for i, el := range els {
		if got := string(raw[el.Start:el.End]); got != want[i] {
			t.Errorf("element %d span = %q, want %q", i, got, want[i])
		}
	}

	// The nested <strong> spans its own tag pair inside the <p>.
	p := els[1]
	var strong *Node
	Walk(p, func(n *Node) bool {
		if n.Name.Local == "strong" {
			strong = n
		}
		return true
	})
	if strong == nil {
		t.Fatal("no strong node")
	}
	if got := string(raw[strong.Start:strong.End]); got != `<strong>world</strong>` {
		t.Errorf("strong span = %q", got)
	}

	// A text node's raw span still contains the unresolved entity even though
	// Data carries the decoded rune.
	var entText *Node
	for _, c := range p.Children {
		if c.Type == Text && strings.Contains(c.Data, "!") {
			entText = c
		}
	}
	if entText == nil {
		t.Fatal("no entity text node")
	}
	if got := string(raw[entText.Start:entText.End]); got != "&nbsp;!" {
		t.Errorf("entity text span = %q, want %q", got, "&nbsp;!")
	}
}

// TestParseOffsetsCDATA covers a CDATA body: the raw span includes the CDATA
// markers while Data is the payload.
func TestParseOffsetsCDATA(t *testing.T) {
	raw := []byte(`<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[if a < b]]></ac:plain-text-body></ac:structured-macro>`)
	root, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	macro := root.Children[0]
	if got := string(raw[macro.Start:macro.End]); got != string(raw) {
		t.Errorf("macro span = %q, want the whole input", got)
	}
	var body *Node
	Walk(root, func(n *Node) bool {
		if n.Name.Local == "plain-text-body" {
			body = n
		}
		return true
	})
	if body == nil || len(body.Children) == 0 {
		t.Fatal("no plain-text-body content")
	}
	data := body.Children[0]
	if got := string(raw[data.Start:data.End]); got != "<![CDATA[if a < b]]>" {
		t.Errorf("cdata span = %q", got)
	}
	if data.Data != "if a < b" {
		t.Errorf("cdata payload = %q", data.Data)
	}
}
