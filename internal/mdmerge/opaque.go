package mdmerge

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mdcsf"
	"github.com/isukharev/atl/internal/mirror"
)

// marker is an opaque inline element from a dropped base block: the markdown
// text it renders to and its exact base bytes. When an edited block still
// contains the marker text, the merge substitutes the original bytes instead
// of converting — identity (macro parameters, user keys, link bodies) is
// preserved.
type marker struct {
	md    string
	bytes []byte
	used  bool
	// copyable markers come from base content that keeps its bytes (unchanged
	// table rows/cells): substitution splices a clone and does not consume
	// them, so the same mention or link may be copied into several places.
	// Macros are never copyable — cloning would duplicate their macro-id.
	copyable bool
}

// collectMarkers gathers opaque inline elements from every base block that is
// not kept verbatim, in document order.
func collectMarkers(nodes []*csf.Node, kept []bool, base []byte, refs []domain.Ref) []*marker {
	var out []*marker
	for i, n := range nodes {
		if kept[i] {
			continue
		}
		collectOpaque(n, base, refs, &out)
	}
	// Longest markers first so "[[Page One]]" wins over a "[[Page]]" that is
	// its substring; document order breaks ties.
	sort.SliceStable(out, func(a, b int) bool { return len(out[a].md) > len(out[b].md) })
	return out
}

// collectOpaque walks a block subtree collecting elements whose markdown
// rendering is opaque (identity would be lost in a md→CSF round trip). It
// does not descend into a collected element.
func collectOpaque(n *csf.Node, base []byte, refs []domain.Ref, out *[]*marker) {
	for _, c := range n.Children {
		if c.Type != csf.Element {
			continue
		}
		if isOpaqueInline(c) {
			md := mirror.RenderInline(c, refs)
			if md != "" && c.End > c.Start {
				*out = append(*out, &marker{md: md, bytes: base[c.Start:c.End]})
			}
			continue // never descend into a collected element
		}
		collectOpaque(c, base, refs, out)
	}
}

// isOpaqueInline reports elements that render to markers or resolved display
// text: links (page/user/attachment), images, macros, bare mentions, colored
// spans.
func isOpaqueInline(n *csf.Node) bool {
	switch {
	case n.Name.Space == "ac":
		switch n.Name.Local {
		case "link", "image", "structured-macro", "macro":
			return true
		}
	case n.Name.Space == "ri" && n.Name.Local == "user":
		return true
	case n.Name.Space == "" && n.Name.Local == "span":
		// A colored span renders as protected readable HTML.
		return spanHasColor(n)
	}
	return false
}

func spanHasColor(n *csf.Node) bool {
	if strings.TrimSpace(n.Attrv("", "data-color")) != "" {
		return true
	}
	style := n.Attrv("", "style")
	for _, decl := range strings.Split(style, ";") {
		k, _, ok := strings.Cut(decl, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), "color") {
			return true
		}
	}
	return false
}

// substituteMarkers replaces marker texts found in txt with sentinel tokens
// that survive conversion as plain alphanumeric text, returning the rewritten
// text and the token→bytes map to splice back after conversion.
func substituteMarkers(txt string, markers []*marker) (string, map[string][]byte, error) {
	subst := map[string][]byte{}
	for idx, m := range markers {
		if m.used || m.md == "" {
			continue
		}
		pos := boundaryIndex(txt, m.md)
		if pos < 0 {
			continue
		}
		// A plain-text marker (a resolved display name) can collide with
		// ordinary prose. Guessing the occurrence silently relocates the
		// fragment — refuse instead.
		if isPlainMarker(m.md) {
			if boundaryIndex(txt[pos+len(m.md):], m.md) >= 0 {
				return "", nil, fmt.Errorf("marker text %q appears more than once in the edited block — position is ambiguous", m.md)
			}
			if distinctBytesFor(markers, m.md) > 1 {
				return "", nil, fmt.Errorf("several different fragments render to the same text %q — mapping is ambiguous", m.md)
			}
		}
		token := fmt.Sprintf("atlopaque%dz", idx)
		if strings.Contains(txt, token) {
			continue // paranoia: never collide with literal text
		}
		txt = txt[:pos] + token + txt[pos+len(m.md):]
		subst[token] = m.bytes
		if !m.copyable {
			m.used = true
		}
	}
	return txt, subst, nil
}

// collectCopyable gathers clone-safe opaque inline elements (mentions, links,
// images, colored spans — everything but identity-carrying macros) from base
// content that keeps its bytes, so their text can be copied elsewhere.
func collectCopyable(n *csf.Node, base []byte, refs []domain.Ref, out *[]*marker) {
	for _, c := range n.Children {
		if c.Type != csf.Element {
			continue
		}
		if isOpaqueInline(c) {
			// The macro check must cover the whole subtree: a colored span or
			// link can wrap a macro, and cloning those bytes would duplicate
			// its macro-id just the same.
			if !nodeHasMacro(c) {
				md := mirror.RenderInline(c, refs)
				if md != "" && c.End > c.Start {
					*out = append(*out, &marker{md: md, bytes: base[c.Start:c.End], copyable: true})
				}
			}
			continue // never descend into a collected element
		}
		collectCopyable(c, base, refs, out)
	}
}

// convertBlock converts one edited markdown block, substituting marker texts
// with their original base bytes.
func convertBlock(txt string, markers []*marker) ([]byte, error) {
	// Fenced code is converted verbatim; markers inside it are just text.
	if strings.HasPrefix(strings.TrimSpace(txt), "```") {
		return mdcsf.Convert(txt)
	}
	txt, subst, err := substituteMarkers(txt, markers)
	if err != nil {
		return nil, err
	}
	out, err := mdcsf.Convert(txt)
	if err != nil {
		return nil, err
	}
	for token, raw := range subst {
		out = bytes.Replace(out, []byte(token), raw, 1)
	}
	if len(subst) > 0 {
		if err := checkSpliceNesting(out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// isPlainMarker reports a marker with no marker syntax — bare resolved text
// (mention display names). Bracketed markers cannot collide with prose.
func isPlainMarker(md string) bool {
	return !strings.ContainsAny(md, "[]⟦!(")
}

// distinctBytesFor counts distinct byte payloads among unused markers that
// render to the same text.
func distinctBytesFor(markers []*marker, md string) int {
	seen := map[string]bool{}
	for _, m := range markers {
		if !m.used && m.md == md {
			seen[string(m.bytes)] = true
		}
	}
	return len(seen)
}

// checkSpliceNesting rejects structurally invalid placements that byte
// substitution can produce and well-formedness validation cannot see: a link
// or block macro spliced into an inline context (<a> in <a>, macro in <code>
// or a heading).
func checkSpliceNesting(block []byte) error {
	root, err := csf.Parse(block)
	if err != nil {
		return fmt.Errorf("substituted block does not parse: %w", err)
	}
	var bad error
	var walk func(n *csf.Node, inA, inInline bool)
	walk = func(n *csf.Node, inA, inInline bool) {
		for _, c := range n.Children {
			if c.Type != csf.Element || bad != nil {
				continue
			}
			isA := c.Name.Space == "" && c.Name.Local == "a"
			isLink := isA || c.Name.Space == "ac" && c.Name.Local == "link"
			if inA && isLink {
				bad = fmt.Errorf("a link fragment cannot be substituted inside another link")
				return
			}
			if inInline && c.MacroName() != "" && mirror.IsBlockMacro(c.MacroName()) {
				bad = fmt.Errorf("a block macro cannot be substituted into an inline context (code span or heading)")
				return
			}
			inline := inInline || c.Name.Space == "" &&
				(c.Name.Local == "code" || isHeadingName(c.Name.Local))
			walk(c, inA || isA, inline)
		}
	}
	walk(root, false, false)
	return bad
}

func isHeadingName(local string) bool {
	return len(local) == 2 && local[0] == 'h' && local[1] >= '1' && local[1] <= '6'
}

// boundaryIndex finds md in txt at a position not glued to letters/digits on
// either side, so a mention display name "Ann" never matches inside
// "Announcement". Markers containing markup characters match anywhere.
func boundaryIndex(txt, md string) int {
	from := 0
	for {
		i := strings.Index(txt[from:], md)
		if i < 0 {
			return -1
		}
		i += from
		if isPlainMarker(md) { // plain-text marker: require word boundaries
			prev, _ := utf8.DecodeLastRuneInString(txt[:i])
			next, _ := utf8.DecodeRuneInString(txt[i+len(md):])
			if (i > 0 && isWordRune(prev)) || (i+len(md) < len(txt) && isWordRune(next)) {
				from = i + 1
				continue
			}
		}
		return i
	}
}

// isWordRune treats letters and digits of any script as word runes (so a
// display name never matches inside a longer word) but not punctuation —
// multibyte quotes/brackets are legitimate boundaries.
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
