package mirror

import (
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

// MarkdownLinkResolver optionally replaces one renderer-owned logical target
// (for example jira:KEY or confluence-page:SPACE/Title) with a caller-owned
// Markdown destination. A nil resolver preserves the staging-view bytes.
type MarkdownLinkResolver func(target string) (destination string, ok bool)

// RenderMarkdownResolved renders the same clean body as RenderMarkdown while
// allowing an offline consumer to replace known logical links. It does not add
// the editable staging envelope, metadata, comments, or compatibility framing.
func RenderMarkdownResolved(root *csf.Node, refs []domain.Ref, resolver MarkdownLinkResolver) []byte {
	return []byte(renderMarkdownHeadingOffsetVersion(root, refs, 0, confluenceMarkdownCurrent, resolver))
}

func (r *mdRenderer) ref(kind domain.RefKind, key string) (domain.Ref, bool) {
	v, ok := r.refs[string(kind)+"\x00"+key]
	return v, ok
}

func (r *mdRenderer) resolvedLink(target string) string {
	if r.linkResolver != nil {
		if destination, ok := r.linkResolver(target); ok && destination != "" {
			return destination
		}
	}
	return target
}
