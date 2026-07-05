package mdwiki

import (
	"regexp"
	"strings"
)

// inline converts inline markdown to Jira wiki inline markup. Delimiters that
// do not pair up render literally (escaped) — plain text is always safe to
// emit; only constructs wiki cannot express unambiguously are rejected.
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
			return "", unsupported("page link (Confluence-only)", clip(s[i:]))
		case strings.HasPrefix(s[i:], "[~"):
			n := mention(&b, s[i:])
			if n == 0 {
				b.WriteString(`\[~`)
				i += 2
				continue
			}
			i += n
		case s[i] == '[':
			n, err := link(&b, s, i)
			if err != nil {
				return "", err
			}
			if n == 0 {
				b.WriteString(`\[`)
				i++
				continue
			}
			i += n
		case strings.HasPrefix(s[i:], "**") || strings.HasPrefix(s[i:], "__"):
			if s[i] == '_' && intraword(s, i, 2) {
				b.WriteString("__") // intraword underscores are inert in wiki too
				i += 2
				continue
			}
			n, err := emphasis(&b, s, i, s[i:i+2], "*")
			if err != nil {
				return "", err
			}
			if n == 0 {
				b.WriteString(escapeRun(s[i : i+2]))
				i += 2
				continue
			}
			i += n
		case strings.HasPrefix(s[i:], "~~"):
			n, err := emphasis(&b, s, i, "~~", "-")
			if err != nil {
				return "", err
			}
			if n == 0 {
				b.WriteString(`\~\~`)
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
			n, err := emphasis(&b, s, i, s[i:i+1], "_")
			if err != nil {
				return "", err
			}
			if n == 0 {
				b.WriteString(escapeRun(s[i : i+1]))
				i++
				continue
			}
			i += n
		case s[i] == '\\' && i+1 < len(s) && isEscapable(s[i+1]):
			b.WriteString(escapeRun(s[i+1 : i+2]))
			i += 2
		default:
			b.WriteString(escapeChar(s, i))
			i++
		}
	}
	return b.String(), nil
}

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

// alwaysEscape are wiki-active regardless of position: macro braces, link
// brackets, image embeds, table pipes.
const alwaysEscape = "{}[]!|"

// toggles open/close wiki inline formatting when they sit on a word boundary:
// * bold, _ italic, - strikethrough, + inserted, ^ super, ~ sub, ? citation.
const toggles = "*_-+^~?"

// escapeChar emits s[i] with a backslash when Jira would otherwise interpret
// it. Toggle characters are escaped only in an opening position (start/space
// before, non-space after) — without a viable opener the closer is inert.
func escapeChar(s string, i int) string {
	c := s[i] // single byte: multi-byte runes fall through untouched via s[i:i+1]
	if strings.IndexByte(alwaysEscape, c) >= 0 {
		return `\` + s[i:i+1]
	}
	if strings.IndexByte(toggles, c) >= 0 {
		beforeOpen := i == 0 || s[i-1] == ' ' || s[i-1] == '\t' || s[i-1] == '(' || s[i-1] == '"'
		afterOpen := i+1 < len(s) && s[i+1] != ' ' && s[i+1] != '\t'
		if beforeOpen && afterOpen {
			return `\` + s[i:i+1]
		}
	}
	return s[i : i+1]
}

// escapeRun backslash-escapes every byte of a literal (ASCII) delimiter run.
func escapeRun(s string) string {
	var b strings.Builder
	for j := 0; j < len(s); j++ {
		b.WriteString(`\` + s[j:j+1])
	}
	return b.String()
}

// codeSpan handles `code` → {{code}}. Content that would break out of the
// monospace braces is refused.
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
	content := strings.TrimSpace(s[open : open+end])
	if strings.ContainsAny(content, "{}") {
		return 0, unsupported("code span containing braces", clip(content))
	}
	b.WriteString("{{" + content + "}}")
	return open + end + open, nil
}

// emphasis converts a paired md delimiter to the wiki toggle char. Wiki
// toggles only take effect on word boundaries, so a span whose outside edge
// touches a word character cannot be expressed — refused, not misrendered.
func emphasis(b *strings.Builder, s string, i int, delim, wiki string) (int, error) {
	inner := s[i+len(delim):]
	end := strings.Index(inner, delim)
	if end <= 0 {
		return 0, nil
	}
	content := inner[:end]
	if strings.TrimSpace(content) == "" ||
		strings.HasPrefix(content, " ") || strings.HasSuffix(content, " ") {
		return 0, nil
	}
	if isWordByte2(s, i-1) || isWordByte2(s, i+len(delim)+end+len(delim)) {
		return 0, unsupported("emphasis without word boundaries (wiki limitation)", clip(s[i:]))
	}
	out, err := inline(content)
	if err != nil {
		return 0, err
	}
	b.WriteString(wiki + out + wiki)
	return len(delim) + end + len(delim), nil
}

func isWordByte2(s string, i int) bool {
	return i >= 0 && i < len(s) && isWordByte(s[i])
}

var mentionRe = regexp.MustCompile(`^\[~[A-Za-z0-9@._-]+\]`)

// mention passes a [~username] through verbatim (DC mention syntax has no md
// counterpart; agents write it directly). Returns bytes consumed, 0 if the
// bracket run is not a mention.
func mention(b *strings.Builder, s string) int {
	m := mentionRe.FindString(s)
	if m == "" {
		return 0
	}
	b.WriteString(m)
	return len(m)
}

// link handles [text](url) → [text|url]; [KEY](jira:KEY) → bare KEY autolink.
func link(b *strings.Builder, s string, i int) (int, error) {
	rest := s[i:]
	closeBr := strings.Index(rest, "]")
	if closeBr < 0 || closeBr+1 >= len(rest) || rest[closeBr+1] != '(' {
		return 0, nil
	}
	end, depth := -1, 0
	for j := closeBr + 2; j < len(rest) && end < 0; j++ {
		switch rest[j] {
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
	text := rest[1:closeBr]
	url := rest[closeBr+2 : end]
	switch {
	case strings.HasPrefix(url, "http://"), strings.HasPrefix(url, "https://"),
		strings.HasPrefix(url, "mailto:"):
		if strings.ContainsAny(url, "|]") {
			return 0, unsupported("link URL containing wiki delimiters", clip(url))
		}
		inner, err := inline(text)
		if err != nil {
			return 0, err
		}
		if strings.ContainsAny(inner, "|]") {
			return 0, unsupported("link text containing wiki delimiters", clip(text))
		}
		b.WriteString("[" + inner + "|" + url + "]")
		return end + 1, nil
	case strings.HasPrefix(url, "jira:"):
		key := strings.TrimPrefix(url, "jira:")
		if key == "" || text != key {
			return 0, unsupported("jira link with text differing from key", clip(url))
		}
		b.WriteString(key) // bare issue keys autolink in Jira
		return end + 1, nil
	default:
		return 0, unsupported("link scheme", clip(url))
	}
}
