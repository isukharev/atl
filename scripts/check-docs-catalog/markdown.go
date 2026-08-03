package main

import (
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

type markdownLink struct {
	Destination string
	Line        int
	Local       bool
	Relative    bool
	Path        string
	Fragment    string
}

type markdownDocument struct {
	Links   []markdownLink
	Anchors map[string]bool
}

func validateDocumentLinks(root string, entries []catalogEntry) (int, map[string]markdownDocument, error) {
	parsed := map[string]markdownDocument{}
	load := func(path string) (markdownDocument, error) {
		if document, ok := parsed[path]; ok {
			return document, nil
		}
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return markdownDocument{}, err
		}
		document := parseMarkdown(string(body))
		parsed[path] = document
		return document, nil
	}
	for _, entry := range entries {
		if _, err := load(entry.Path); err != nil {
			return 0, parsed, fmt.Errorf("read %s: %w", entry.Path, err)
		}
	}

	count := 0
	var problems []string
	for _, entry := range entries {
		document := parsed[entry.Path]
		for index := range document.Links {
			link := &document.Links[index]
			resolved, local, err := resolveLocalLink(root, entry.Path, link.Destination)
			if !local {
				continue
			}
			count++
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s:%d: link %q: %v", entry.Path, link.Line, link.Destination, err))
				continue
			}
			link.Local, link.Relative, link.Path, link.Fragment = true, resolved.Relative, resolved.Path, resolved.Fragment
			link.Destination = resolved.CanonicalDestination
			if resolved.Fragment == "" {
				continue
			}
			if !strings.EqualFold(filepath.Ext(resolved.Path), ".md") {
				problems = append(problems, fmt.Sprintf("%s:%d: local anchor target %q is not Markdown", entry.Path, link.Line, link.Destination))
				continue
			}
			target, err := load(resolved.Path)
			if err != nil || !target.Anchors[resolved.Fragment] {
				problems = append(problems, fmt.Sprintf("%s:%d: anchor %q does not case-exactly match a heading in %s", entry.Path, link.Line, resolved.Fragment, resolved.Path))
			}
		}
		parsed[entry.Path] = document
	}
	if len(problems) != 0 {
		sort.Strings(problems)
		return count, parsed, errors.New("documentation link validation failed:\n- " + strings.Join(problems, "\n- "))
	}
	return count, parsed, nil
}

type resolvedLink struct {
	Path                 string
	Fragment             string
	CanonicalDestination string
	Relative             bool
}

func resolveLocalLink(root, source, destination string) (resolvedLink, bool, error) {
	destination = strings.TrimSpace(destination)
	parsed, err := url.Parse(destination)
	if err != nil {
		return resolvedLink{}, true, fmt.Errorf("invalid destination: %w", err)
	}
	if parsed.IsAbs() || parsed.Host != "" || strings.HasPrefix(destination, "//") {
		return resolvedLink{}, false, nil
	}
	decoded, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return resolvedLink{}, true, fmt.Errorf("invalid path escaping: %w", err)
	}
	fragment, err := url.PathUnescape(parsed.Fragment)
	if err != nil {
		return resolvedLink{}, true, fmt.Errorf("invalid anchor escaping: %w", err)
	}
	var target string
	switch {
	case decoded == "":
		target = filepath.Join(root, filepath.FromSlash(source))
	case strings.HasPrefix(decoded, "/"):
		target = filepath.Join(root, filepath.FromSlash(strings.TrimLeft(decoded, "/")))
	default:
		target = filepath.Join(root, filepath.Dir(filepath.FromSlash(source)), filepath.FromSlash(decoded))
	}
	target = filepath.Clean(target)
	if !within(root, target) {
		return resolvedLink{}, true, errors.New("target escapes repository root")
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return resolvedLink{}, true, errors.New("resolve repository-relative target")
	}
	relative = filepath.ToSlash(relative)
	if _, err := canonicalPath(root, relative); err != nil {
		return resolvedLink{}, true, err
	}
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return resolvedLink{}, true, errors.New("target is not a regular non-symlink file")
	}
	canonical := relative
	if parsed.RawQuery != "" {
		canonical += "?" + parsed.RawQuery
	}
	if fragment != "" {
		canonical += "#" + fragment
	}
	return resolvedLink{
		Path:                 relative,
		Fragment:             fragment,
		CanonicalDestination: canonical,
		Relative:             !strings.HasPrefix(decoded, "/"),
	}, true, nil
}

func parseMarkdown(contents string) markdownDocument {
	document := markdownDocument{Anchors: map[string]bool{}}
	lines := strings.Split(contents, "\n")
	inFence := false
	fenceMarker := byte(0)
	fenceLength := 0
	anchorCounts := map[string]int{}
	for index, original := range lines {
		trimmed := strings.TrimLeft(original, " \t")
		if marker, length, ok := markdownFence(trimmed); ok {
			if !inFence {
				inFence, fenceMarker, fenceLength = true, marker, length
			} else if marker == fenceMarker && length >= fenceLength && strings.Trim(strings.TrimLeft(trimmed, string(marker)), " \t") == "" {
				inFence = false
			}
			continue
		}
		if inFence {
			continue
		}
		if heading, ok := markdownHeading(original); ok {
			base := githubHeadingSlug(heading)
			if base != "" {
				anchor := base
				if count := anchorCounts[base]; count > 0 {
					anchor = fmt.Sprintf("%s-%d", base, count)
				}
				anchorCounts[base]++
				document.Anchors[anchor] = true
			}
		}
		if anchor, ok := explicitMarkdownAnchor(original); ok {
			document.Anchors[anchor] = true
		}
		masked := maskInlineCode(original)
		if destination, ok := referenceDestination(masked); ok {
			document.Links = append(document.Links, markdownLink{Destination: destination, Line: index + 1})
		}
		document.Links = append(document.Links, inlineLinks(masked, index+1)...)
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

func maskInlineCode(line string) string {
	masked := []byte(line)
	for start := 0; start < len(line); {
		if line[start] != '`' {
			start++
			continue
		}
		length := 1
		for start+length < len(line) && line[start+length] == '`' {
			length++
		}
		end := start + length
		found := -1
		for end < len(line) {
			if line[end] != '`' {
				end++
				continue
			}
			closing := 1
			for end+closing < len(line) && line[end+closing] == '`' {
				closing++
			}
			if closing == length {
				found = end + closing
				break
			}
			end += closing
		}
		if found < 0 {
			start += length
			continue
		}
		for position := start; position < found; position++ {
			masked[position] = ' '
		}
		start = found
	}
	return string(masked)
}

func markdownHeading(line string) (string, bool) {
	line = strings.TrimLeft(line, " ")
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || level >= len(line) || line[level] != ' ' {
		return "", false
	}
	heading := strings.TrimSpace(line[level+1:])
	heading = strings.TrimSpace(strings.TrimRight(heading, "#"))
	return heading, heading != ""
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
	for _, prefix := range []string{"<a id=\"", "<a name=\""} {
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		remainder := strings.TrimPrefix(trimmed, prefix)
		end := strings.IndexByte(remainder, '"')
		if end <= 0 {
			return "", false
		}
		anchor := remainder[:end]
		if githubHeadingSlug(anchor) != anchor {
			return "", false
		}
		return anchor, true
	}
	return "", false
}

func validateRequiredAnchors(entries []catalogEntry, documents map[string]markdownDocument) error {
	for _, entry := range entries {
		seen := map[string]bool{}
		for _, anchor := range entry.RequiredAnchors {
			if anchor == "" || seen[anchor] {
				return fmt.Errorf("catalog document %q has an empty or duplicate required anchor", entry.Path)
			}
			seen[anchor] = true
			if !documents[entry.Path].Anchors[anchor] {
				return fmt.Errorf("catalog document %q is missing required anchor %q", entry.Path, anchor)
			}
		}
	}
	return nil
}

func referenceDestination(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if len(line)-len(trimmed) > 3 || !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	close := strings.Index(trimmed, "]:")
	if close <= 1 {
		return "", false
	}
	return firstDestination(trimmed[close+2:])
}

func inlineLinks(line string, lineNumber int) []markdownLink {
	var links []markdownLink
	for index := 0; index+1 < len(line); index++ {
		if line[index] != ']' || line[index+1] != '(' {
			continue
		}
		end, depth, escaped := index+2, 1, false
		for ; end < len(line); end++ {
			character := line[end]
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			switch character {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					if destination, ok := firstDestination(line[index+2 : end]); ok {
						links = append(links, markdownLink{Destination: destination, Line: lineNumber})
					}
					index = end
				}
			}
			if depth == 0 {
				break
			}
		}
	}
	return links
}

func firstDestination(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	if value[0] == '<' {
		end := strings.IndexByte(value, '>')
		if end < 0 {
			return value, true
		}
		return value[1:end], true
	}
	for index, character := range value {
		if character == ' ' || character == '\t' {
			return strings.TrimSpace(value[:index]), true
		}
	}
	return value, true
}
