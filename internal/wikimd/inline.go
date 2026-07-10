package wikimd

import (
	"regexp"
	"strings"
)

// inline converts Jira wiki inline markup on a single logical line to markdown.
// It is total: a delimiter that does not form a clear, boundary-delimited span
// is emitted as literal text (never guessed), so prose peppered with `*`, `_`,
// `-`, `!` or `[` is preserved rather than mangled.
func inline(s string, opts Options) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\\':
			if i+1 < len(s) && s[i+1] == '\\' {
				b.WriteString("  \n")
				i += 2
				continue
			}
			if n := escapedBraceBold(&b, s, i, opts); n > 0 {
				i += n
				continue
			}
			b.WriteByte(c)
			i++
		case c == '{':
			if n := monospan(&b, s[i:]); n > 0 {
				i += n
				continue
			}
			if n := colorTag(&b, s[i:]); n > 0 {
				i += n
				continue
			}
			b.WriteByte(c)
			i++
		case c == '[':
			if n := mention(&b, s[i:]); n > 0 {
				i += n
				continue
			}
			if n := link(&b, s[i:], opts); n > 0 {
				i += n
				continue
			}
			b.WriteByte(c)
			i++
		case c == '!':
			if n := image(&b, s[i:], opts); n > 0 {
				i += n
				continue
			}
			b.WriteByte(c)
			i++
		case c == '*':
			if n := toggle(&b, s, i, '*', "**", opts); n > 0 {
				i += n
				continue
			}
			b.WriteByte(c)
			i++
		case c == '_':
			if n := toggle(&b, s, i, '_', "*", opts); n > 0 {
				i += n
				continue
			}
			b.WriteByte(c)
			i++
		case c == '-':
			if n := toggle(&b, s, i, '-', "~~", opts); n > 0 {
				i += n
				continue
			}
			b.WriteByte(c)
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// escapedBraceBold recognizes a legacy Jira editor shape where an emphasized
// span is serialized as `\{*}text{*}` instead of `*text*`. Treating its inner
// stars as ordinary wiki delimiters leaks the braces into Markdown. The narrow
// recognizer requires a non-empty, single-line span and leaves every other
// backslash/braced construct literal.
func escapedBraceBold(b *strings.Builder, s string, i int, opts Options) int {
	const open = `\{*}`
	const close = `{*}`
	if !strings.HasPrefix(s[i:], open) || (i > 0 && isWordByte(s[i-1])) {
		return 0
	}
	rest := s[i+len(open):]
	end := strings.Index(rest, close)
	if end <= 0 {
		return 0
	}
	content := rest[:end]
	if strings.ContainsAny(content, "\r\n") || strings.TrimSpace(content) != content {
		return 0
	}
	after := i + len(open) + end + len(close)
	if after < len(s) && isWordByte(s[after]) {
		return 0
	}
	b.WriteString("**" + inline(content, opts) + "**")
	return len(open) + end + len(close)
}

// monospan converts {{mono}} to an inline `code` span. Content is verbatim (no
// inner wiki parsing); an embedded newline is collapsed to keep the span on one
// line and a backtick in the content widens the fence so the span still closes.
func monospan(b *strings.Builder, s string) int {
	if !strings.HasPrefix(s, "{{") {
		return 0
	}
	end := strings.Index(s[2:], "}}")
	if end < 0 {
		return 0
	}
	content := strings.ReplaceAll(s[2:2+end], "\n", " ")
	fence := "`"
	if strings.Contains(content, "`") {
		fence = "``"
	}
	b.WriteString(fence + content + fence)
	return 2 + end + 2
}

// colorTag drops a {color:...} opening tag or a {color} closing tag, keeping the
// text between them in the normal flow ({color:red}text{color} → text). It emits
// nothing (the tag is dropped) and only reports how many bytes to consume.
func colorTag(_ *strings.Builder, s string) int {
	if !strings.HasPrefix(s, "{color") {
		return 0
	}
	end := strings.Index(s, "}")
	if end < 0 {
		return 0
	}
	tag := s[:end+1]
	if tag == "{color}" || strings.HasPrefix(tag, "{color:") {
		return end + 1 // consume, emit nothing
	}
	return 0
}

var mentionRe = regexp.MustCompile(`^\[~([^\]]+)\]`)

// mention converts a [~username] mention to bold @username.
func mention(b *strings.Builder, s string) int {
	m := mentionRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	user := strings.TrimSpace(m[1])
	if user == "" {
		return 0
	}
	b.WriteString("**@" + user + "**")
	return len(m[0])
}

// link converts [text|url] to [text](url) and a bare [url] to <url>. A bracket
// span that is not clearly a link (no `|`, and content that does not look like a
// URL) is left literal so ordinary bracketed prose survives.
func link(b *strings.Builder, s string, opts Options) int {
	end := strings.Index(s, "]")
	if end < 0 {
		return 0
	}
	inner := s[1:end]
	if inner == "" || strings.Contains(inner, "\n") {
		return 0
	}
	if bar := strings.Index(inner, "|"); bar >= 0 {
		text := strings.TrimSpace(inner[:bar])
		url := strings.TrimSpace(inner[bar+1:])
		// A [alias|url|tip] third field is dropped.
		if extra := strings.Index(url, "|"); extra >= 0 {
			url = strings.TrimSpace(url[:extra])
		}
		if url == "" {
			return 0
		}
		if text == "" {
			text = url
		}
		b.WriteString("[" + inline(text, opts) + "](" + escapeURLDest(url) + ")")
		return end + 1
	}
	url := strings.TrimSpace(inner)
	if !looksLikeURL(url) {
		return 0
	}
	b.WriteString("<" + escapeURLDest(url) + ">")
	return end + 1
}

// escapeAlt escapes text for a markdown bracket span (image alt / link text):
// backslashes and square brackets would close the span early and corrupt the
// read view around server-supplied names.
func escapeAlt(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`)
	return r.Replace(s)
}

// escapeDest percent-encodes a LOCAL path for a markdown link destination:
// spaces, parentheses, angle brackets and quotes break a bare (dest), and `%`
// is encoded first so a literal percent in a filename survives decoding.
func escapeDest(s string) string {
	r := strings.NewReplacer(
		"%", "%25", " ", "%20", "(", "%28", ")", "%29",
		"<", "%3C", ">", "%3E", `"`, "%22",
	)
	return r.Replace(s)
}

// escapeURLDest encodes a URL for a markdown link destination. Unlike
// escapeDest it leaves `%` alone — wiki URLs are typically already
// percent-encoded, and re-encoding would double-escape them.
func escapeURLDest(s string) string {
	r := strings.NewReplacer(
		" ", "%20", "(", "%28", ")", "%29",
		"<", "%3C", ">", "%3E", `"`, "%22",
	)
	return r.Replace(s)
}

func looksLikeURL(s string) bool {
	return strings.Contains(s, "://") || strings.HasPrefix(s, "mailto:") || strings.HasPrefix(s, "#")
}

// image converts a wiki image embed. External `!http://...!` → ![](url); an
// attachment `!name.png!` resolves against opts.Images to a local link when
// downloaded, else renders as inline code signaling an unresolved image. `!` in
// ordinary prose (no filename-shaped content) is left literal.
func image(b *strings.Builder, s string, opts Options) int {
	end := strings.Index(s[1:], "!")
	if end < 0 {
		return 0
	}
	inner := s[1 : 1+end]
	// Jira image syntax is tight: no whitespace directly inside the bangs.
	// Rejecting a padded span keeps prose like "Done! v1.2! yes" literal.
	if inner != strings.TrimSpace(inner) {
		return 0
	}
	name := inner
	if bar := strings.Index(inner, "|"); bar >= 0 {
		name = inner[:bar]
	}
	name = strings.TrimSpace(name)
	if !looksLikeImageRef(name) {
		return 0
	}
	consumed := 1 + end + 1
	switch {
	case strings.Contains(name, "://"):
		b.WriteString("![](" + escapeURLDest(name) + ")")
	default:
		if path, ok := opts.Images[name]; ok && path != "" {
			b.WriteString("![" + escapeAlt(name) + "](" + escapeDest(path) + ")")
		} else {
			b.WriteString("`!" + name + "!`")
		}
	}
	return consumed
}

// imageExtRe matches a filename-style extension tail (".png", ".jpeg", …).
var imageExtRe = regexp.MustCompile(`\.[A-Za-z0-9]{1,5}$`)

func looksLikeImageRef(name string) bool {
	if name == "" || strings.Contains(name, "\t") {
		return false
	}
	if strings.Contains(name, "://") {
		return true
	}
	if !strings.Contains(name, ".") {
		return false
	}
	// Spaces are legal in Jira attachment filenames ("Screenshot 2026….png"),
	// but a space-bearing span is only treated as an image when it ends in a
	// clear filename extension — otherwise exclamation-marked prose that happens
	// to contain a dot would render as a bogus image ref.
	if strings.Contains(name, " ") {
		return imageExtRe.MatchString(name)
	}
	return true
}

// toggle converts a boundary-delimited wiki phrase modifier (*bold*, _italic_,
// -strike-) to its markdown wrapper. The opening delimiter must sit on a word
// boundary and be followed by non-space; the closing delimiter must be preceded
// by non-space and sit on a word boundary. If no such pair exists the run is not
// a modifier and the caller emits it literally.
func toggle(b *strings.Builder, s string, i int, delim byte, wrap string, opts Options) int {
	if i > 0 && isWordByte(s[i-1]) {
		return 0
	}
	if i+1 >= len(s) || s[i+1] == delim || isSpaceByte(s[i+1]) {
		return 0
	}
	for j := i + 1; j < len(s); j++ {
		if s[j] != delim {
			continue
		}
		if isSpaceByte(s[j-1]) {
			continue // content ends with a space: not a closer
		}
		if j+1 < len(s) && isWordByte(s[j+1]) {
			continue // closer not on a word boundary
		}
		content := s[i+1 : j]
		if content == "" {
			return 0
		}
		b.WriteString(wrap + inline(content, opts) + wrap)
		return j - i + 1
	}
	return 0
}

func isWordByte(c byte) bool {
	return c == '_' ||
		c >= '0' && c <= '9' ||
		c >= 'a' && c <= 'z' ||
		c >= 'A' && c <= 'Z' ||
		c >= 0x80
}

func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' }
