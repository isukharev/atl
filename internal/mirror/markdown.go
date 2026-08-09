package mirror

import (
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

type confluenceMarkdownFormat uint8

const (
	confluenceMarkdownCurrent confluenceMarkdownFormat = iota
	confluenceMarkdownV5
)

// RenderMarkdown produces the read-only markdown view of a CSF body: legible
// prose for grep/Read, with opaque fragments shown as resolved placeholders
// (⟦…⟧) and images/diagrams as ![](assets/…) links. It is intentionally lossy —
// the .csf file remains the editable source of truth.
func RenderMarkdown(root *csf.Node, refs []domain.Ref) []byte {
	return []byte(renderMarkdownHeadingOffsetVersion(root, refs, 0, confluenceMarkdownCurrent))
}

func renderMarkdownHeadingOffsetVersion(root *csf.Node, refs []domain.Ref, headingOffset int, format confluenceMarkdownFormat, resolvers ...MarkdownLinkResolver) string {
	r := newMDRendererOffsetVersion(refs, headingOffset, format, resolvers...)
	var b strings.Builder
	forEachBlockNode(root, func(n *csf.Node) {
		r.block(&b, n)
	})
	return normalizeBlankLines(b.String())
}

// MDViewOpts carries the profile-driven additions to a Confluence markdown view.
// Metadata/comments are optional; ReadOnly switches the body boundary for a
// transient document that has no writeback baseline. A zero value renders the
// standard editable mirror envelope around RenderMarkdown's output. The app
// layer assembles these from the page metadata and, for Comments, the
// `<slug>.comments.json` sidecar (absent → nil → the section is skipped).
type MDViewOpts struct {
	PageFields        []PageField
	Comments          []domain.Comment
	QualifiedComments *ConfluenceCommentsSidecarV2
	CommentView       []domain.Comment
	JiraMacros        []JiraMacroView
	ReadOnly          bool
}

// PageField is one already-resolved, read-only Confluence metadata value. The
// renderer owns structural and Markdown escaping; callers supply plain text.
type PageField struct {
	ID        string
	Label     string
	Placement string
	Values    []string
	ShowEmpty bool
}

// RenderMarkdownOpts renders a versioned derived view with stable generated
// boundaries, optional YAML metadata, and a trailing Comments section.
func RenderMarkdownOpts(root *csf.Node, refs []domain.Ref, opts MDViewOpts) []byte {
	prefix, body, suffix := renderMarkdownViewPartsVersion(root, refs, opts, confluenceMarkdownCurrent)
	return []byte(prefix + body + suffix)
}

// RenderMarkdownOptsV5 reconstructs the exact previously supported derived
// view. It exists only for guarded migration: new writes must use
// RenderMarkdownOpts and the current document marker.
func RenderMarkdownOptsV5(root *csf.Node, refs []domain.Ref, opts MDViewOpts) []byte {
	prefix, body, suffix := renderMarkdownViewPartsVersion(root, refs, opts, confluenceMarkdownV5)
	return []byte(prefix + body + suffix)
}

// RenderMarkdownViewParts renders the view as three concatenable parts —
// prefix (generated metadata), body, suffix (the "# Comments" section) — such that
// prefix+body+suffix is byte-identical to RenderMarkdownOpts(root, refs, opts).
// The split exists for `conf apply`: the editable body must be located by these
// structural anchors (the metadata above and the Comments section below are
// read-only in the view), NOT by re-parsing headings — a body heading renders
// as a top-level `## ` line and would be misread as a generated section.
func RenderMarkdownViewParts(root *csf.Node, refs []domain.Ref, opts MDViewOpts) (prefix, body, suffix string) {
	return renderMarkdownViewPartsVersion(root, refs, opts, confluenceMarkdownCurrent)
}

func renderMarkdownViewPartsVersion(root *csf.Node, refs []domain.Ref, opts MDViewOpts, format confluenceMarkdownFormat) (prefix, body, suffix string) {
	body = renderMarkdownHeadingOffsetVersion(root, refs, 0, format)
	marker := ConfluenceDocumentMarker
	if format == confluenceMarkdownV5 {
		marker = ConfluenceDocumentMarkerV5
	}
	prefix = marker + "\n"
	if fields := renderPageFields(opts.PageFields); fields != "" {
		prefix += fields + "\n\n"
	}
	bodyMarker := ConfluenceBodyMarker
	if opts.ReadOnly {
		bodyMarker = ConfluenceBodyReadOnlyMarker
	}
	prefix += bodyMarker + "\n# Content\n\n"
	if len(opts.JiraMacros) > 0 {
		var generated strings.Builder
		generated.WriteString("\n" + ConfluenceJiraMacrosMarker + "\n# Jira Queries\n\n")
		for _, macro := range opts.JiraMacros {
			fmt.Fprintf(&generated, "## Jira Query %d\n\n%s", macro.Index+1, strings.TrimSpace(macro.Markdown))
			if macro.Truncated || !macro.Complete {
				generated.WriteString("\n\n> **Partial:** this macro result is truncated; refresh the page view to retrieve current rows.")
			}
			generated.WriteString("\n\n")
		}
		suffix = generated.String()
	}
	if opts.QualifiedComments != nil {
		suffix += "\n" + ConfluenceCommentsMarker + "\n" + string(renderQualifiedCommentsMarkdownVersion(opts.QualifiedComments, format))
	} else if len(opts.Comments) > 0 {
		suffix += "\n" + ConfluenceCommentsMarker + "\n" + string(renderCommentsMarkdownVersion(opts.Comments, format))
	}
	// RenderMarkdownOpts applies TrimRight(whole, "\n")+"\n" to the concatenation.
	// Reproduce it by trimming the assembled whole, then re-slicing at the raw
	// part boundaries (clamped): slicing one string at increasing offsets keeps
	// prefix+body+suffix == whole byte-for-byte in every case, including a body
	// that is entirely trailing newlines.
	full := prefix + body + suffix
	full = strings.TrimRight(full, "\n") + "\n"
	pEnd := len(prefix)
	if pEnd > len(full) {
		pEnd = len(full)
	}
	bEnd := len(prefix) + len(body)
	if bEnd > len(full) {
		bEnd = len(full)
	}
	return full[:pEnd], full[pEnd:bEnd], full[bEnd:]
}

func renderPageFields(fields []PageField) string {
	var metadata []PageField
	var sections []PageField
	for _, field := range fields {
		if len(field.Values) == 0 && !field.ShowEmpty {
			continue
		}
		if field.Placement == "section" {
			sections = append(sections, field)
		} else {
			metadata = append(metadata, field)
		}
	}
	if len(metadata) == 0 && len(sections) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(ConfluencePageFieldsMarker + "\n")
	if len(metadata) > 0 {
		b.WriteString("# Metadata\n\n| Field | Value |\n| --- | --- |\n")
		for _, field := range metadata {
			fmt.Fprintf(&b, "| %s | %s |\n", pageTableValue(field.Label), pageTableValue(strings.Join(field.Values, ", ")))
		}
		b.WriteByte('\n')
	}
	for _, field := range sections {
		fmt.Fprintf(&b, "<!-- atl:section page-field.%s readonly -->\n# %s\n\n", safeMarkerID(field.ID), pageTableValue(field.Label))
		if len(field.Values) == 0 {
			b.WriteString("_Empty_\n\n")
		} else if len(field.Values) == 1 {
			b.WriteString(pageSectionValue(field.Values[0]) + "\n\n")
		} else {
			for _, value := range field.Values {
				b.WriteString("- " + pageSectionValue(value) + "\n")
			}
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func pageSectionValue(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	escaped := pageTableValue(s)
	if s == "" {
		return escaped
	}
	switch s[0] {
	case '-':
		return "&#45;" + escaped[1:]
	case '+':
		return "&#43;" + escaped[1:]
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i > 0 && i < len(s) && (s[i] == '.' || s[i] == ')') {
		entity := "&#46;"
		if s[i] == ')' {
			entity = "&#41;"
		}
		return escaped[:i] + entity + escaped[i+1:]
	}
	return escaped
}

func pageTableValue(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.NewReplacer(
		"&", "&amp;", "\\", "&#92;", "|", "&#124;", "<", "&lt;", ">", "&gt;",
		"`", "&#96;", "*", "&#42;", "_", "&#95;", "~", "&#126;",
		"[", "&#91;", "]", "&#93;", "!", "&#33;", "#", "&#35;",
	).Replace(s)
}

func safeMarkerID(s string) string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('_')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

type mdRenderer struct {
	refs                    map[string]domain.Ref
	linkResolver            MarkdownLinkResolver
	headingOffset           int
	headingOverflowAsStrong bool
	escapeHTMLText          bool
	format                  confluenceMarkdownFormat
}

func newMDRenderer(refs []domain.Ref) *mdRenderer {
	return newMDRendererOffset(refs, 0)
}

func newMDRendererOffset(refs []domain.Ref, headingOffset int) *mdRenderer {
	return newMDRendererOffsetVersion(refs, headingOffset, confluenceMarkdownCurrent)
}

func newMDRendererOffsetVersion(refs []domain.Ref, headingOffset int, format confluenceMarkdownFormat, resolvers ...MarkdownLinkResolver) *mdRenderer {
	byKey := map[string]domain.Ref{}
	for _, r := range refs {
		byKey[string(r.Kind)+"\x00"+r.Key] = r
	}
	var resolver MarkdownLinkResolver
	if len(resolvers) > 0 {
		resolver = resolvers[0]
	}
	return &mdRenderer{refs: byKey, linkResolver: resolver, headingOffset: headingOffset, format: format}
}
