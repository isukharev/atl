package main

import (
	"fmt"
	"html"
	"net/url"
	"strings"
	"unicode"
)

type heading struct {
	Level  int
	Text   string
	Anchor string
}

type markdownDocument struct {
	Headings []heading
	Anchors  map[string]bool
}

type githubSlugger struct {
	used       map[string]bool
	nextSuffix map[string]int
}

func newGitHubSlugger() *githubSlugger {
	return &githubSlugger{used: map[string]bool{}, nextSuffix: map[string]int{}}
}

func (slugger *githubSlugger) slug(text string) string {
	base := githubHeadingSlug(text)
	if base == "" {
		return ""
	}
	anchor := base
	if slugger.used[anchor] {
		suffix := slugger.nextSuffix[base] + 1
		for slugger.used[fmt.Sprintf("%s-%d", base, suffix)] {
			suffix++
		}
		slugger.nextSuffix[base] = suffix
		anchor = fmt.Sprintf("%s-%d", base, suffix)
	}
	slugger.used[anchor] = true
	return anchor
}

func parseMarkdown(contents string) markdownDocument {
	document := markdownDocument{Anchors: map[string]bool{}}
	slugger := newGitHubSlugger()
	inFence := false
	fenceMarker := byte(0)
	fenceLength := 0
	for _, original := range strings.Split(contents, "\n") {
		trimmed := strings.TrimLeft(original, " \t")
		if marker, length, ok := markdownFence(trimmed); ok {
			if !inFence {
				inFence, fenceMarker, fenceLength = true, marker, length
			} else if marker == fenceMarker && length >= fenceLength && fenceClose(trimmed, marker, length) {
				inFence = false
			}
			continue
		}
		if inFence {
			continue
		}
		if level, text, ok := parseATXHeading(original); ok {
			anchor := slugger.slug(text)
			if anchor != "" {
				document.Headings = append(document.Headings, heading{Level: level, Text: text, Anchor: anchor})
				document.Anchors[anchor] = true
			}
		}
		if anchor, ok := explicitMarkdownAnchor(original); ok {
			document.Anchors[anchor] = true
		}
	}
	return document
}

func markdownFence(line string) (byte, int, bool) {
	if len(line) < 3 || line[0] != '`' && line[0] != '~' {
		return 0, 0, false
	}
	marker := line[0]
	length := 0
	for length < len(line) && line[length] == marker {
		length++
	}
	return marker, length, length >= 3
}

func fenceClose(line string, marker byte, length int) bool {
	return strings.Trim(strings.TrimLeft(line[length:], string(marker)), " \t") == ""
}

func parseATXHeading(line string) (int, string, bool) {
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent > 3 {
		return 0, "", false
	}
	line = line[indent:]
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || level >= len(line) || line[level] != ' ' && line[level] != '\t' {
		return 0, "", false
	}
	text := strings.TrimSpace(line[level+1:])
	if trailing := trailingHeadingSequence(text); trailing >= 0 {
		text = strings.TrimSpace(text[:trailing])
	}
	return level, text, text != ""
}

func trailingHeadingSequence(text string) int {
	end := len(text)
	for end > 0 && text[end-1] == '#' {
		end--
	}
	if end == len(text) || end == 0 || text[end-1] != ' ' && text[end-1] != '\t' {
		return -1
	}
	return end
}

func githubHeadingSlug(text string) string {
	var slug strings.Builder
	for _, current := range strings.ToLower(markdownHeadingVisibleText(text)) {
		switch {
		case current == '-' || current == '_':
			slug.WriteRune(current)
		case current == ' ' || current == '\t':
			slug.WriteByte('-')
		case unicode.IsLetter(current) || unicode.IsDigit(current):
			slug.WriteRune(current)
		}
	}
	return slug.String()
}

func markdownHeadingVisibleText(text string) string {
	var visible strings.Builder
	for index := 0; index < len(text); {
		if text[index] == '`' {
			if contents, next, ok := markdownCodeSpan(text, index); ok {
				visible.WriteString(contents)
				index = next
				continue
			}
		}
		if text[index] == '<' {
			if close := strings.IndexByte(text[index+1:], '>'); close >= 0 {
				inner := text[index+1 : index+close+1]
				if markdownAutolink(inner) {
					visible.WriteString(inner)
				}
				index += close + 2
				continue
			}
		}
		if text[index] == '&' {
			if end := strings.IndexByte(text[index:], ';'); end > 0 {
				entity := text[index : index+end+1]
				if decoded := html.UnescapeString(entity); decoded != entity {
					visible.WriteString(decoded)
					index += end + 1
					continue
				}
			}
		}
		labelStart := index
		if text[index] == '!' && index+1 < len(text) && text[index+1] == '[' {
			labelStart++
		}
		if text[labelStart] == '[' {
			if close := matchingDelimiter(text, labelStart, '[', ']'); close >= 0 {
				after := close + 1
				if after < len(text) && text[after] == '(' {
					if destinationEnd := matchingDelimiter(text, after, '(', ')'); destinationEnd >= 0 {
						visible.WriteString(markdownHeadingVisibleText(text[labelStart+1 : close]))
						index = destinationEnd + 1
						continue
					}
				}
				if after < len(text) && text[after] == '[' {
					if referenceEnd := matchingDelimiter(text, after, '[', ']'); referenceEnd >= 0 {
						visible.WriteString(markdownHeadingVisibleText(text[labelStart+1 : close]))
						index = referenceEnd + 1
						continue
					}
				}
			}
		}
		if text[index] != '`' {
			visible.WriteByte(text[index])
		}
		index++
	}
	return visible.String()
}

func markdownCodeSpan(text string, start int) (string, int, bool) {
	length := 0
	for start+length < len(text) && text[start+length] == '`' {
		length++
	}
	for index := start + length; index < len(text); {
		if text[index] != '`' {
			index++
			continue
		}
		closing := 0
		for index+closing < len(text) && text[index+closing] == '`' {
			closing++
		}
		if closing == length {
			contents := strings.ReplaceAll(text[start+length:index], "\n", " ")
			if len(contents) >= 2 && contents[0] == ' ' && contents[len(contents)-1] == ' ' &&
				strings.Trim(contents, " ") != "" {
				contents = contents[1 : len(contents)-1]
			}
			return contents, index + closing, true
		}
		index += closing
	}
	return "", start, false
}

func markdownAutolink(inner string) bool {
	if strings.ContainsAny(inner, " \t\r\n<>") {
		return false
	}
	if parsed, err := url.Parse(inner); err == nil && parsed.Scheme != "" {
		return true
	}
	at := strings.IndexByte(inner, '@')
	return at > 0 && at < len(inner)-1 && strings.Contains(inner[at+1:], ".")
}

func matchingDelimiter(text string, start int, opening, closing byte) int {
	depth, escaped := 0, false
	for index := start; index < len(text); index++ {
		if escaped {
			escaped = false
			continue
		}
		if text[index] == '\\' {
			escaped = true
			continue
		}
		switch text[index] {
		case opening:
			depth++
		case closing:
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func explicitMarkdownAnchor(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	for _, prefix := range []string{"<a id=\"", "<a name=\"", "<a id='", "<a name='"} {
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		quote := prefix[len(prefix)-1]
		remainder := strings.TrimPrefix(trimmed, prefix)
		end := strings.IndexByte(remainder, quote)
		if end <= 0 {
			return "", false
		}
		return html.UnescapeString(remainder[:end]), true
	}
	return "", false
}
