package mdcsf

import (
	"strings"
)

// inline converts inline markdown to CSF inline XHTML. Delimiters that do not
// pair up render literally (escaped) — plain text is always safe to emit;
// only constructs that would need invented structure are rejected.
func inline(s string) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(s) {
		switch {
		case s[i] == '`':
			n, err := codeSpan(&b, s[i:])
			if err != nil {
				return "", err
			}
			if n == 0 { // unmatched backtick: literal
				b.WriteString("`")
				i++
				continue
			}
			i += n
		case strings.HasPrefix(s[i:], "⟦"):
			return "", unsupported("opaque placeholder", clip(s[i:]))
		case strings.HasPrefix(s[i:], "!["):
			return "", unsupported("image", clip(s[i:]))
		case strings.HasPrefix(s[i:], "[["):
			n, err := pageLink(&b, s[i:])
			if err != nil {
				return "", err
			}
			i += n
		case s[i] == '[':
			n, err := link(&b, s[i:])
			if err != nil {
				return "", err
			}
			if n == 0 {
				b.WriteString("[")
				i++
				continue
			}
			i += n
		case strings.HasPrefix(s[i:], "**") || strings.HasPrefix(s[i:], "__"):
			if s[i] == '_' && intraword(s, i, 2) {
				b.WriteString("__")
				i += 2
				continue
			}
			n, err := emphasis(&b, s[i:], s[i:i+2], "strong")
			if err != nil {
				return "", err
			}
			if n == 0 {
				b.WriteString(escapeText(s[i : i+2]))
				i += 2
				continue
			}
			i += n
		case strings.HasPrefix(s[i:], "~~"):
			n, err := emphasis(&b, s[i:], "~~", "s")
			if err != nil {
				return "", err
			}
			if n == 0 {
				b.WriteString("~~")
				i += 2
				continue
			}
			i += n
		case s[i] == '*' || s[i] == '_':
			if s[i] == '_' && intraword(s, i, 1) {
				b.WriteString("_")
				i++
				continue
			}
			n, err := emphasis(&b, s[i:], s[i:i+1], "em")
			if err != nil {
				return "", err
			}
			if n == 0 {
				b.WriteString(escapeText(s[i : i+1]))
				i++
				continue
			}
			i += n
		case s[i] == '\\' && i+1 < len(s) && isEscapable(s[i+1]):
			b.WriteString(escapeText(s[i+1 : i+2]))
			i += 2
		default:
			b.WriteString(escapeText(s[i : i+1]))
			i++
		}
	}
	return b.String(), nil
}

// intraword reports whether the underscore run at s[i:i+n] sits inside a
// word (GFM: `_` never opens emphasis intraword — snake_case stays literal).
func intraword(s string, i, n int) bool {
	before := i > 0 && isWordByte(s[i-1])
	after := i+n < len(s) && isWordByte(s[i+n])
	return before && after
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isEscapable(c byte) bool {
	return strings.IndexByte("\\`*_[]|~#", c) >= 0
}

// codeSpan handles `code` (and “code with ` inside“). Returns bytes
// consumed, 0 if no closing run exists.
func codeSpan(b *strings.Builder, s string) (int, error) {
	open := 1
	if strings.HasPrefix(s, "``") {
		open = 2
	}
	delim := s[:open]
	end := strings.Index(s[open:], delim)
	if end < 0 {
		return 0, nil
	}
	content := s[open : open+end]
	b.WriteString("<code>" + escapeText(strings.TrimSpace(content)) + "</code>")
	return open + end + open, nil
}

// emphasis handles a paired delimiter (** __ * _ ~~). Returns bytes consumed,
// 0 when the delimiter never closes (caller emits it literally). The opener
// must touch text on the inside and the span must be non-empty — otherwise
// snake_case identifiers and stray asterisks would sprout markup.
func emphasis(b *strings.Builder, s, delim, tag string) (int, error) {
	inner := s[len(delim):]
	end := strings.Index(inner, delim)
	if end <= 0 {
		return 0, nil
	}
	content := inner[:end]
	if strings.TrimSpace(content) == "" ||
		strings.HasPrefix(content, " ") || strings.HasSuffix(content, " ") {
		return 0, nil
	}
	out, err := inline(content)
	if err != nil {
		return 0, err
	}
	b.WriteString("<" + tag + ">" + out + "</" + tag + ">")
	return len(delim) + end + len(delim), nil
}

// pageLink handles [[Page Title]] → an ac:link to a page by title.
func pageLink(b *strings.Builder, s string) (int, error) {
	end := strings.Index(s, "]]")
	if end < 0 {
		return 0, unsupported("unterminated page link", clip(s))
	}
	title := strings.TrimSpace(s[len("[["):end])
	if title == "" {
		return 0, unsupported("empty page link", clip(s))
	}
	b.WriteString(`<ac:link><ri:page ri:content-title="` + escapeAttr(title) + `"/></ac:link>`)
	return end + len("]]"), nil
}

// link handles [text](url). Returns 0 when the bracket is not a link (caller
// emits '[' literally). Marker schemes the renderer produces for opaque
// targets (jira:, attachment:, page:) must be substituted from base bytes by
// the caller before conversion — refuse them here.
func link(b *strings.Builder, s string) (int, error) {
	closeBr := strings.Index(s, "]")
	if closeBr < 0 || closeBr+1 >= len(s) || s[closeBr+1] != '(' {
		return 0, nil
	}
	// GFM link destination: parens inside the URL are allowed when balanced
	// (wiki-style ".../Foo_(bar)"); the destination ends at the ')' that
	// returns the depth to zero.
	end, depth := -1, 0
	for j := closeBr + 2; j < len(s) && end < 0; j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				end = j
			} else {
				depth--
			}
		}
	}
	if end < 0 {
		return 0, nil
	}
	text := s[1:closeBr]
	url := s[closeBr+2 : end]
	switch {
	case strings.HasPrefix(url, "http://"), strings.HasPrefix(url, "https://"),
		strings.HasPrefix(url, "mailto:"), strings.HasPrefix(url, "/"), strings.HasPrefix(url, "#"):
		inner, err := inline(text)
		if err != nil {
			return 0, err
		}
		b.WriteString(`<a href="` + escapeAttr(url) + `">` + inner + `</a>`)
		return end + 1, nil
	default:
		return 0, unsupported("link scheme", clip(url))
	}
}

func escapeText(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func escapeAttr(s string) string {
	s = escapeText(s)
	return strings.ReplaceAll(s, `"`, "&quot;")
}

func clip(s string) string {
	r := []rune(s)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}
