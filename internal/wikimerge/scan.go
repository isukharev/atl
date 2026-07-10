package wikimerge

import (
	"regexp"
	"strings"

	"github.com/isukharev/atl/internal/mdcsf"
	"github.com/isukharev/atl/internal/wikiscanner"
)

// block is one base wiki block: the byte range [start,end) of its content in the
// base body, excluding the surrounding blank-line separators (which live in the
// gaps the assembler preserves verbatim).
type block struct {
	start, end int
}

// These recognisers mirror internal/wikimd's block scanner exactly. The block
// boundaries MUST stay consistent with how wikimd renders, because alignment
// compares edited markdown against wikimd's rendered output of each base block.
var (
	headingRe   = regexp.MustCompile(`^h([1-6])\.[ \t]+(.*)$`)
	codeOpenRe  = regexp.MustCompile(`^\{(code|noformat)(?::([^}]*))?\}(.*)$`)
	quoteOpenRe = regexp.MustCompile(`^\{quote\}(.*)$`)
	panelOpenRe = regexp.MustCompile(`^\{panel(?::([^}]*))?\}(.*)$`)
	hrRe        = regexp.MustCompile(`^-{4,}[ \t]*$`)
)

// wline is one physical line and its byte offsets in the source. end is the
// offset just past the last content byte (before the newline), so a block's byte
// range never includes the trailing newline.
type wline struct {
	text       string
	start, end int
}

// splitLinesOffsets splits s into lines, recognizing '\n', '\r', and '\r\n' as a
// single line break — the SAME normalization wikimd applies before it renders, so
// the block scanner and the renderer always agree on line structure (a bare or
// leading CR is a break for both, not opaque line content). The break bytes fall
// outside every line's [start,end) range, living in the gaps the assembler copies
// verbatim, so the merge stays byte-exact for LF and CRLF bodies alike. A trailing
// break yields a final empty line, so no source byte is ever unaccounted for.
func splitLinesOffsets(s string) []wline {
	var out []wline
	n := len(s)
	lineStart := 0
	i := 0
	for i < n {
		switch s[i] {
		case '\n':
			out = append(out, wline{s[lineStart:i], lineStart, i})
			i++
			lineStart = i
		case '\r':
			end := i
			i++
			if i < n && s[i] == '\n' { // treat \r\n as one break
				i++
			}
			out = append(out, wline{s[lineStart:end], lineStart, end})
			lineStart = i
		default:
			i++
		}
	}
	out = append(out, wline{s[lineStart:], lineStart, n})
	return out
}

// scanWikiBlocks splits a wiki body into blocks with byte offsets, using the same
// block-boundary rules as internal/wikimd's renderer. Blank lines are gaps, not
// blocks; `{code}`/`{noformat}`/`{quote}`/`{panel}` bodies, consecutive table
// (`|`) lines, and consecutive list (`*`/`#`) lines are each one block; a run of
// adjacent plain lines is one paragraph block.
func scanWikiBlocks(base string) []block {
	lines := splitLinesOffsets(base)
	var blocks []block
	add := func(from, to int) { blocks = append(blocks, block{lines[from].start, lines[to].end}) }
	i := 0
	for i < len(lines) {
		ln := lines[i].text
		switch {
		case strings.TrimSpace(ln) == "":
			i++ // blank line: a gap, preserved by the assembler
		case headingRe.MatchString(ln), hrRe.MatchString(ln):
			add(i, i)
			i++
		case codeOpenRe.MatchString(ln):
			m := codeOpenRe.FindStringSubmatch(ln)
			end := macroEnd(lines, i, m[3], "{"+m[1]+"}")
			add(i, end)
			i = end + 1
		case quoteOpenRe.MatchString(ln):
			end := macroEnd(lines, i, quoteOpenRe.FindStringSubmatch(ln)[1], "{quote}")
			add(i, end)
			i = end + 1
		case panelOpenRe.MatchString(ln):
			end := macroEnd(lines, i, panelOpenRe.FindStringSubmatch(ln)[2], "{panel}")
			add(i, end)
			i = end + 1
		case strings.HasPrefix(ln, "|"):
			j := i
			for j < len(lines) && strings.HasPrefix(lines[j].text, "|") {
				j = wikiTableRowEnd(lines, j) + 1
			}
			add(i, j-1)
			i = j
		case wikiscanner.IsListLine(ln):
			j := i
			for j < len(lines) && wikiscanner.IsListLine(lines[j].text) {
				j++
			}
			add(i, j-1)
			i = j
		default: // paragraph: adjacent plain lines up to a blank or a block start
			j := i + 1
			for j < len(lines) {
				lt := lines[j].text
				if strings.TrimSpace(lt) == "" || isSpecialStart(lt) {
					break
				}
				j++
			}
			add(i, j-1)
			i = j
		}
	}
	return blocks
}

// wikiTableRowEnd mirrors wikimd.tableRowEnd so render and apply agree that
// non-`|` continuation lines ending in `|` belong to the table block.
func wikiTableRowEnd(lines []wline, start int) int {
	return wikiscanner.TableRowEnd(len(lines), start, func(i int) string { return lines[i].text })
}

// macroEnd returns the index of the last line of a brace macro opened at line i.
// rest is the text after the opening tag on line i; closeTag is the terminator.
// A one-liner ends on its own line; an unterminated macro runs to EOF (never an
// error) — matching wikimd.collectMacroBody.
func macroEnd(lines []wline, i int, rest, closeTag string) int {
	if strings.Contains(rest, closeTag) {
		return i
	}
	for j := i + 1; j < len(lines); j++ {
		if strings.Contains(lines[j].text, closeTag) {
			return j
		}
	}
	return len(lines) - 1
}

// isSpecialStart reports a line that begins a non-paragraph block, so a paragraph
// run stops before it (the same triggers wikimd's scanner tries before its
// paragraph fallthrough).
func isSpecialStart(line string) bool {
	return headingRe.MatchString(line) || codeOpenRe.MatchString(line) ||
		quoteOpenRe.MatchString(line) || panelOpenRe.MatchString(line) ||
		hrRe.MatchString(line) || strings.HasPrefix(line, "|") || wikiscanner.IsListLine(line)
}

// splitMDBlocks splits a markdown fragment into trimmed blocks (blank-line
// separated; fenced code kept whole) using the same splitter the md edit surface
// uses, so base renders and the edited file are tokenized identically.
func splitMDBlocks(md string) []string {
	raw := mdcsf.SplitBlocks(md)
	out := make([]string, 0, len(raw))
	for _, b := range raw {
		if t := strings.TrimSpace(b); t != "" {
			out = append(out, t)
		}
	}
	return out
}
