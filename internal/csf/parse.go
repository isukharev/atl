// Package csf parses Confluence Storage Format for reading (fragment extraction,
// markdown view, sanity checks) and validates it for well-formedness. It never
// re-serializes a body for the write path: the mirror stores the exact stored
// bytes and pushes them back, which is byte-stable (verified empirically). The
// DOM here is read-only and lossy by design — it exists to understand a body,
// not to reproduce it.
package csf

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

// NodeType classifies a DOM node.
type NodeType int

const (
	Element NodeType = iota
	Text             // ordinary character data
	CData            // CDATA section (e.g. code macro body)
)

// Name is a (namespace-prefix, local) pair. For undeclared CSF prefixes the
// Go XML decoder keeps the literal prefix in Space, so Space is "ac"/"ri"/"".
type Name struct {
	Space string
	Local string
}

func (n Name) String() string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

// Attr is an element attribute.
type Attr struct {
	Name  Name
	Value string
}

// Node is a read-only DOM node.
type Node struct {
	Type     NodeType
	Name     Name    // Element only
	Attr     []Attr  // Element only
	Children []*Node // Element only
	Data     string  // Text/CData payload
}

// Attrv returns the value of an attribute by (space, local), or "".
func (n *Node) Attrv(space, local string) string {
	for _, a := range n.Attr {
		if a.Name.Space == space && a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// MacroName returns the ac:name of a structured-macro/macro element, or "".
func (n *Node) MacroName() string {
	if n.Type == Element && n.Name.Space == "ac" &&
		(n.Name.Local == "structured-macro" || n.Name.Local == "macro") {
		return n.Attrv("ac", "name")
	}
	return ""
}

// decoder builds a decoder configured for CSF (HTML entities, lenient prefixes).
func decoder(raw []byte) *xml.Decoder {
	// Wrap in a synthetic root so a fragment has a single document element. We
	// prepend without a newline so line numbers stay aligned with the original.
	wrapped := make([]byte, 0, len(raw)+13)
	wrapped = append(wrapped, "<root>"...)
	wrapped = append(wrapped, raw...)
	wrapped = append(wrapped, "</root>"...)
	d := xml.NewDecoder(bytes.NewReader(wrapped))
	d.Entity = xml.HTMLEntity // resolve &nbsp; &mdash; &hellip; …
	d.Strict = true
	return d
}

// Parse builds a read-only DOM. The returned root is the synthetic wrapper; its
// Children are the top-level CSF nodes. A non-nil error means the body is not
// well-formed (use Validate for line-aware diagnostics).
func Parse(raw []byte) (*Node, error) {
	d := decoder(raw)
	root := &Node{Type: Element, Name: Name{Local: "root"}}
	stack := []*Node{root}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			el := &Node{Type: Element, Name: Name{Space: t.Name.Space, Local: t.Name.Local}}
			for _, a := range t.Attr {
				if a.Name.Local == "xmlns" || a.Name.Space == "xmlns" {
					continue
				}
				el.Attr = append(el.Attr, Attr{Name: Name{Space: a.Name.Space, Local: a.Name.Local}, Value: a.Value})
			}
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, el)
			stack = append(stack, el)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			parent := stack[len(stack)-1]
			// The decoder reports CDATA as CharData; we cannot distinguish it
			// from ordinary text at the token layer, so classify by content
			// only where it matters (handled in Validate via raw scan).
			parent.Children = append(parent.Children, &Node{Type: Text, Data: string(t)})
		}
	}
	return root, nil
}

// Walk visits every element node depth-first, calling fn. Returning false from
// fn skips that node's children.
func Walk(n *Node, fn func(*Node) bool) {
	if n.Type == Element {
		if !fn(n) {
			return
		}
	}
	for _, c := range n.Children {
		Walk(c, fn)
	}
}

// TextContent returns concatenated text of a node's subtree, trimmed.
func TextContent(n *Node) string {
	var b strings.Builder
	var rec func(*Node)
	rec = func(x *Node) {
		if x.Type == Text || x.Type == CData {
			b.WriteString(x.Data)
		}
		for _, c := range x.Children {
			rec(c)
		}
	}
	rec(n)
	return strings.TrimSpace(b.String())
}
