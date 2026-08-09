package mirror

import (
	"html"
	neturl "net/url"
	"strings"
	"unicode"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

// inline renders inline content of a node to a single line.
func (r *mdRenderer) inline(n *csf.Node) string {
	var b strings.Builder
	for _, c := range n.Children {
		r.inlineNode(&b, c)
	}
	return strings.TrimSpace(squeezeSpaces(b.String()))
}

// inlineNoBlock is like inline but used inside list items.
func (r *mdRenderer) inlineNoBlock(n *csf.Node) string {
	var b strings.Builder
	for _, c := range n.Children {
		if c.Type == csf.Element && (c.Name.Local == "ul" || c.Name.Local == "ol") {
			continue
		}
		r.inlineNode(&b, c)
	}
	return strings.TrimSpace(squeezeSpaces(b.String()))
}

func (r *mdRenderer) inlineNode(b *strings.Builder, n *csf.Node) {
	if n.Type == csf.Text || n.Type == csf.CData {
		text := collapseWS(n.Data)
		if r.escapeHTMLText {
			text = html.EscapeString(text)
		}
		b.WriteString(text)
		return
	}
	if n.Type != csf.Element {
		return
	}
	switch {
	case n.Name.Local == "strong" || n.Name.Local == "b":
		b.WriteString("**" + r.inline(n) + "**")
	case n.Name.Local == "em" || n.Name.Local == "i":
		b.WriteString("_" + r.inline(n) + "_")
	case n.Name.Local == "s" || n.Name.Local == "del" || n.Name.Local == "strike":
		b.WriteString("~~" + r.inline(n) + "~~")
	case n.Name.Local == "code":
		b.WriteString("`" + r.inline(n) + "`")
	case n.Name.Local == "pre":
		b.WriteString("`" + r.inline(n) + "`")
	case n.Name.Local == "time" && n.Name.Space == "":
		b.WriteString(n.Attrv("", "datetime"))
	case n.Name.Space == "ac" && n.Name.Local == "emoticon":
		if fb := n.Attrv("ac", "emoji-fallback"); fb != "" {
			b.WriteString(fb)
		} else {
			b.WriteString(":" + n.Attrv("ac", "name") + ":")
		}
	case n.Name.Local == "br":
		if r.format == confluenceMarkdownCurrent {
			b.WriteString("<br>")
		} else {
			b.WriteString(" ")
		}
	case n.Name.Local == "a":
		href := n.Attrv("", "href")
		b.WriteString("[" + r.inline(n) + "](" + href + ")")
	case n.Name.Local == "span" && n.Name.Space == "":
		if color := styleColor(n); color != "" {
			wasEscaping := r.escapeHTMLText
			r.escapeHTMLText = true
			inner := r.inline(n)
			r.escapeHTMLText = wasEscaping
			if inner != "" {
				if safe, ok := SafeCSSColor(color); ok {
					b.WriteString("<span style=\"color: " + html.EscapeString(safe) + "\">" + inner + "</span>")
				} else {
					b.WriteString("<span data-atl-color=\"" + html.EscapeString(color) + "\">" + inner + "</span>")
				}
			}
			return
		}
		for _, c := range n.Children {
			r.inlineNode(b, c)
		}
	case n.Name.Space == "ac" && n.Name.Local == "link":
		r.acLink(b, n)
	case n.Name.Space == "ac" && n.Name.Local == "image":
		r.acImage(b, n)
	case n.Name.Space == "ri" && n.Name.Local == "user":
		key := n.Attrv("ri", "userkey")
		if key == "" {
			key = n.Attrv("ri", "account-id")
		}
		if ref, ok := r.ref(domain.RefUser, key); ok {
			b.WriteString(ref.Display)
		} else {
			b.WriteString("@" + key)
		}
	case n.Name.Space == "ac" && (n.Name.Local == "structured-macro" || n.Name.Local == "macro"):
		switch name := n.Attrv("ac", "name"); name {
		case "status":
			b.WriteString("`[" + macroParam(n, "title") + "]`")
		case "drawio":
			dn := macroParam(n, "diagramName")
			if ref, ok := r.ref(domain.RefDrawio, dn); ok && ref.Asset != "" {
				b.WriteString("![diagram: " + dn + "](" + ref.Asset + ")")
			} else {
				b.WriteString("⟦drawio diagram: " + dn + "⟧")
			}
		case "jira":
			if key := macroParam(n, "key"); key != "" {
				b.WriteString("[" + key + "](" + r.resolvedLink("jira:"+key) + ")")
			} else if jql := macroParam(n, "jqlQuery"); jql != "" {
				b.WriteString("⟦jira query: " + jql + "⟧")
			} else {
				b.WriteString("⟦jira⟧")
			}
		case "toc":
			b.WriteString("⟦table of contents⟧")
		case "code":
			// Inline (mixed-with-text) code: collapse a multi-line body so the
			// span stays on one line — a literal newline in backticks is broken.
			b.WriteString("`" + collapseWS(plainBody(n)) + "`")
		default:
			b.WriteString("⟦" + name + "⟧")
		}
	default:
		// A block-level element flattened into an inline context (e.g. several
		// <p>/<div> inside a table cell or <li>) must not glue to its siblings.
		// Inline elements like <span> fall through without a separator.
		if isFlowBreak(n.Name) {
			b.WriteString(" ")
		}
		for _, c := range n.Children {
			r.inlineNode(b, c)
		}
	}
}

// isFlowBreak reports whether an element is block-level, so that when it is
// flattened into an inline string a separating space is inserted around it.
func isFlowBreak(n csf.Name) bool {
	if n.Space != "" {
		return false
	}
	switch n.Local {
	case "p", "div", "li", "tr", "blockquote", "dt", "dd", "pre",
		"h1", "h2", "h3", "h4", "h5", "h6":
		return true
	}
	return false
}

func (r *mdRenderer) acLink(b *strings.Builder, n *csf.Node) {
	// Resolve the link target from its ri:* child.
	var label, target, pageTitle, pageSpace string
	csf.Walk(n, func(x *csf.Node) bool {
		switch {
		case x.Name.Space == "ri" && x.Name.Local == "page":
			pageTitle = x.Attrv("ri", "content-title")
			pageSpace = x.Attrv("ri", "space-key")
			target = "page:" + pageTitle
		case x.Name.Space == "ri" && x.Name.Local == "attachment":
			target = "attachment:" + x.Attrv("ri", "filename")
		case x.Name.Space == "ri" && x.Name.Local == "user":
			key := x.Attrv("ri", "userkey")
			if ref, ok := r.ref(domain.RefUser, key); ok {
				label = ref.Display
			} else {
				label = "@" + key
			}
			target = "user"
		case x.Name.Space == "ac" && x.Name.Local == "link-body":
			label = r.inline(x)
		case x.Name.Space == "ac" && x.Name.Local == "plain-text-link-body":
			label = collapseWS(csf.TextContent(x))
		}
		return true
	})
	if label == "" {
		label = strings.TrimPrefix(strings.TrimPrefix(target, "page:"), "attachment:")
	}
	if target == "user" {
		b.WriteString(label)
		return
	}
	if strings.HasPrefix(target, "page:") {
		destination := r.resolvedLink("confluence-page:" + pageLinkIdentity(pageSpace, pageTitle))
		b.WriteString("[" + markdownLinkLabel(label) + "](" + destination + ")")
		return
	}
	b.WriteString("[" + label + "](" + r.resolvedLink(target) + ")")
}

func pageLinkIdentity(space, title string) string {
	identity := neturl.PathEscape(title)
	if space != "" {
		identity = neturl.PathEscape(space) + "/" + identity
	}
	return identity
}

func markdownLinkLabel(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "[", "\\[")
	return strings.ReplaceAll(s, "]", "\\]")
}

func (r *mdRenderer) acImage(b *strings.Builder, n *csf.Node) {
	// Decorative icons (e.g. Jira issue-type avatars next to issue links) only
	// add noise to a prose view; skip them.
	if strings.Contains(n.Attrv("ac", "class"), "icon") {
		return
	}
	var fn, url string
	csf.Walk(n, func(x *csf.Node) bool {
		switch {
		case x.Name.Space == "ri" && x.Name.Local == "attachment" && fn == "":
			fn = x.Attrv("ri", "filename")
		case x.Name.Space == "ri" && x.Name.Local == "url" && url == "":
			url = x.Attrv("ri", "value")
		}
		return true
	})
	if fn != "" {
		if ref, ok := r.ref(domain.RefImage, fn); ok && ref.Asset != "" {
			b.WriteString("![" + fn + "](" + ref.Asset + ")")
		} else {
			b.WriteString("![" + fn + "](" + r.resolvedLink("attachment:"+fn) + ")")
		}
		return
	}
	if url != "" {
		b.WriteString("![](" + url + ")")
	}
}

func styleColor(n *csf.Node) string {
	if color := strings.TrimSpace(n.Attrv("", "data-color")); color != "" {
		return color
	}
	style := n.Attrv("", "style")
	for _, decl := range strings.Split(style, ";") {
		k, v, ok := strings.Cut(decl, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "color") {
			continue
		}
		if color := strings.TrimSpace(v); color != "" {
			return color
		}
	}
	return ""
}

// SafeCSSColor accepts only inert CSS color values. It deliberately excludes
// var(), url(), declarations and arbitrary functions so a server-controlled
// page cannot turn a derived Markdown preview into an active network/style
// injection surface.
func SafeCSSColor(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 96 {
		return "", false
	}
	if value[0] == '#' {
		n := len(value) - 1
		if n != 3 && n != 4 && n != 6 && n != 8 {
			return "", false
		}
		for _, r := range value[1:] {
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return "", false
			}
		}
		return value, true
	}
	lettersOnly := true
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			lettersOnly = false
			break
		}
	}
	if lettersOnly {
		return value, true
	}
	lower := strings.ToLower(value)
	for _, fn := range []string{"rgb(", "rgba(", "hsl(", "hsla("} {
		if !strings.HasPrefix(lower, fn) || !strings.HasSuffix(lower, ")") {
			continue
		}
		inside := value[len(fn) : len(value)-1]
		if strings.TrimSpace(inside) == "" {
			return "", false
		}
		for _, r := range inside {
			if (r >= '0' && r <= '9') || strings.ContainsRune(" \t.,%/+-", r) {
				continue
			}
			return "", false
		}
		return value, true
	}
	return "", false
}

// collapseWS squeezes internal whitespace runs to a single space but, unlike a
// bare strings.Fields/Join, preserves a single leading/trailing space when the
// node had one. That boundary space is what keeps words from gluing together
// across inline elements (e.g. "word <strong>bold</strong>" → "word **bold**").
func collapseWS(s string) string {
	out := strings.Join(strings.Fields(s), " ")
	if out == "" {
		// Whitespace-only node between inline elements: keep one space.
		if s != "" {
			return " "
		}
		return ""
	}
	if hasLeadingSpace(s) {
		out = " " + out
	}
	if hasTrailingSpace(s) {
		out += " "
	}
	return out
}

func hasLeadingSpace(s string) bool {
	for _, r := range s {
		return unicode.IsSpace(r)
	}
	return false
}

func hasTrailingSpace(s string) bool {
	r := []rune(s)
	return len(r) > 0 && unicode.IsSpace(r[len(r)-1])
}

// squeezeSpaces collapses runs of ASCII spaces to one. Adjacent text nodes can
// each contribute a boundary space; this neutralizes the resulting double space.
func squeezeSpaces(s string) string {
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}

func normalizeBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s) + "\n"
}
