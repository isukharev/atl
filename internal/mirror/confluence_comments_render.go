package mirror

import (
	"fmt"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

// RenderQualifiedCommentsMarkdown renders a qualified comments sidecar as a
// deterministic, read-only Markdown view. Only complete, internally
// consistent ancestry is presented as a thread; every other record remains
// visible in the unattached section.
func RenderQualifiedCommentsMarkdown(sidecar *ConfluenceCommentsSidecarV2) []byte {
	var b strings.Builder
	b.WriteString("# Comments\n\n")
	if sidecar == nil {
		b.WriteString("Comment inventory is unavailable.\n")
		return []byte(b.String())
	}

	complete := sidecar.CommentsComplete && sidecar.ThreadsComplete && sidecar.AnchorsComplete
	fmt.Fprintf(&b, "**Inventory:** %s\n\n", completenessWord(complete))
	fmt.Fprintf(&b, "**Completeness:** comments %s · threads %s · anchors %s\n\n",
		completenessWord(sidecar.CommentsComplete),
		completenessWord(sidecar.ThreadsComplete),
		completenessWord(sidecar.AnchorsComplete),
	)
	if reasons := closedConfluenceCommentPartialReasons(sidecar.PartialReasons); len(reasons) != 0 {
		b.WriteString("**Partial reasons:** ")
		for i, reason := range reasons {
			if i != 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "`%s`", reason)
		}
		b.WriteString("\n\n")
	} else if !complete {
		b.WriteString("**Partial reasons:** unavailable\n\n")
	}

	if len(sidecar.Comments) == 0 {
		if sidecar.CommentsComplete {
			b.WriteString("No comments.\n")
		} else {
			b.WriteString("No comments were returned; the partial inventory does not prove that the page has no comments.\n")
		}
		renderConfluenceCommentDiagnostics(&b, sidecar.Diagnostics)
		return []byte(b.String())
	}

	forest := newConfluenceCommentRenderForest(sidecar.Comments)
	for _, root := range forest.roots {
		forest.renderThread(&b, root, 0)
	}
	if len(forest.unattached) != 0 {
		b.WriteString("## Unattached comments\n\n")
		b.WriteString("These records have unavailable or inconsistent ancestry and are not presented as a thread.\n\n")
		for _, index := range forest.unattached {
			renderConfluenceComment(&b, forest.records[index].comment, 3, 0, true)
		}
	}
	renderConfluenceCommentDiagnostics(&b, sidecar.Diagnostics)

	return []byte(strings.TrimRight(b.String(), "\n") + "\n")
}

func renderConfluenceCommentDiagnostics(b *strings.Builder, input []ConfluenceCommentsSidecarDiagnostic) {
	if diagnostics := renderableConfluenceCommentDiagnostics(input); len(diagnostics) != 0 {
		if b.Len() != 0 && !strings.HasSuffix(b.String(), "\n\n") {
			b.WriteByte('\n')
		}
		b.WriteString("## Diagnostics\n\n")
		for _, diagnostic := range diagnostics {
			b.WriteString("- " + diagnostic + "\n")
		}
		b.WriteByte('\n')
	}
}

func renderableConfluenceCommentDiagnostics(input []ConfluenceCommentsSidecarDiagnostic) []string {
	out := make([]string, 0, len(input))
	for _, diagnostic := range input {
		switch diagnostic.Code {
		case domain.ConfluenceCommentDiagnosticOrphanMarker:
			out = append(out, "An inline marker has no mirrored comment.")
		case domain.ConfluenceCommentDiagnosticOriginalSelectionChanged:
			out = append(out, "A reported original selection differs from the current matched selection.")
		default:
			if domain.ValidConfluenceCommentPartialReason(diagnostic.Code) {
				out = append(out, "Partial comment evidence: `"+diagnostic.Code+"`.")
			}
		}
	}
	sort.Strings(out)
	return out
}

// RenderConfluenceCommentsMarkdown is an explicit-name alias retained for
// callers that distinguish this view from legacy, unqualified comments.
func RenderConfluenceCommentsMarkdown(sidecar *ConfluenceCommentsSidecarV2) []byte {
	return RenderQualifiedCommentsMarkdown(sidecar)
}

func completenessWord(complete bool) string {
	if complete {
		return "complete"
	}
	return "partial"
}

func closedConfluenceCommentPartialReasons(input []string) []string {
	seen := make(map[string]struct{}, len(input))
	reasons := make([]string, 0, len(input))
	for _, reason := range input {
		if !domain.ValidConfluenceCommentPartialReason(reason) {
			continue
		}
		if _, duplicate := seen[reason]; duplicate {
			continue
		}
		seen[reason] = struct{}{}
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	return reasons
}

type confluenceCommentRenderRecord struct {
	comment ConfluenceCommentsSidecarComment
	key     string
}

type confluenceCommentRenderForest struct {
	records    []confluenceCommentRenderRecord
	byID       map[string][]int
	state      []uint8
	rootOf     []int
	children   map[int][]int
	roots      []int
	unattached []int
}

func newConfluenceCommentRenderForest(comments []ConfluenceCommentsSidecarComment) *confluenceCommentRenderForest {
	forest := &confluenceCommentRenderForest{
		records:  make([]confluenceCommentRenderRecord, len(comments)),
		byID:     make(map[string][]int, len(comments)),
		state:    make([]uint8, len(comments)),
		rootOf:   make([]int, len(comments)),
		children: make(map[int][]int),
	}
	for i, comment := range comments {
		forest.records[i] = confluenceCommentRenderRecord{comment: comment, key: confluenceCommentRenderKey(comment)}
	}
	sort.Slice(forest.records, func(i, j int) bool { return forest.records[i].key < forest.records[j].key })
	for i := range forest.records {
		id := forest.records[i].comment.ID
		forest.byID[id] = append(forest.byID[id], i)
		forest.rootOf[i] = -1
	}

	for i := range forest.records {
		root, valid := forest.resolveRoot(i)
		if !valid {
			forest.unattached = append(forest.unattached, i)
			continue
		}
		comment := forest.records[i].comment
		if i == root {
			forest.roots = append(forest.roots, i)
			continue
		}
		parent := forest.uniqueIndex(comment.ParentID)
		forest.children[parent] = append(forest.children[parent], i)
	}
	return forest
}

func (f *confluenceCommentRenderForest) resolveRoot(index int) (int, bool) {
	switch f.state[index] {
	case 1:
		return -1, false
	case 2:
		return f.rootOf[index], true
	case 3:
		return -1, false
	}
	f.state[index] = 1
	comment := f.records[index].comment
	valid := false
	root := -1
	switch comment.Relation {
	case domain.ConfluenceCommentRelationRoot:
		valid = comment.ParentID == nil && comment.RootID != nil && *comment.RootID == comment.ID && len(f.byID[comment.ID]) == 1
		if valid {
			root = index
		}
	case domain.ConfluenceCommentRelationReply:
		parent := f.uniqueIndex(comment.ParentID)
		if comment.RootID != nil && *comment.RootID != "" && parent >= 0 && parent != index {
			parentRoot, parentValid := f.resolveRoot(parent)
			valid = parentValid && f.records[parentRoot].comment.ID == *comment.RootID
			if valid {
				root = parentRoot
			}
		}
	}
	if valid {
		f.state[index], f.rootOf[index] = 2, root
		return root, true
	}
	f.state[index], f.rootOf[index] = 3, -1
	return -1, false
}

func (f *confluenceCommentRenderForest) uniqueIndex(id *string) int {
	if id == nil {
		return -1
	}
	indices := f.byID[*id]
	if len(indices) != 1 {
		return -1
	}
	return indices[0]
}

func (f *confluenceCommentRenderForest) renderThread(b *strings.Builder, index, depth int) {
	heading := depth + 2
	if heading > 6 {
		heading = 6
	}
	renderConfluenceComment(b, f.records[index].comment, heading, depth, false)
	for _, child := range f.children[index] {
		f.renderThread(b, child, depth+1)
	}
}

func renderConfluenceComment(b *strings.Builder, comment ConfluenceCommentsSidecarComment, heading, depth int, unattached bool) {
	author := pageSectionValue(comment.Author.DisplayName)
	if author == "" {
		author = "Unknown author"
	}
	timestamp := pageSectionValue(comment.CreatedAt)
	if timestamp == "" {
		timestamp = "unknown time"
	}
	kind := "Comment"
	if comment.Relation == domain.ConfluenceCommentRelationReply && !unattached {
		kind = "Reply"
	}
	if unattached {
		kind = "Unattached comment"
	}
	depthLabel := ""
	if depth+2 > 6 {
		depthLabel = fmt.Sprintf(" · thread depth %d", depth)
	}
	fmt.Fprintf(b, "%s %s by %s — %s%s\n\n", strings.Repeat("#", heading), kind, author, timestamp, depthLabel)

	location := string(comment.Location)
	if !domain.ValidConfluenceCommentLocation(comment.Location) {
		location = string(domain.ConfluenceCommentLocationUnknown)
	}
	resolution := string(comment.Resolution)
	if !domain.ValidConfluenceCommentResolution(comment.Resolution) {
		resolution = string(domain.ConfluenceCommentResolutionUnknown)
	}
	fmt.Fprintf(b, "**Location:** %s · **State:** %s\n\n", location, resolution)
	if unattached {
		b.WriteString("**Thread:** unattached; ancestry unavailable or inconsistent\n\n")
	}
	renderConfluenceCommentAnchor(b, comment)

	body := strings.TrimSpace(comment.Body)
	if comment.BodyStorage != "" {
		if root, err := csf.Parse([]byte(comment.BodyStorage)); err == nil {
			body = strings.TrimSpace(renderQualifiedCommentMarkdown(root, heading))
		}
	}
	if body != "" {
		b.WriteString(body)
		b.WriteString("\n\n")
	}
}

func renderConfluenceCommentAnchor(b *strings.Builder, comment ConfluenceCommentsSidecarComment) {
	if comment.Anchor == nil {
		if comment.Location == domain.ConfluenceCommentLocationInline {
			b.WriteString("**Anchor:** unavailable\n\n")
		}
		return
	}
	anchor := comment.Anchor
	if anchor.Status == domain.ConfluenceAnchorMatched && strings.TrimSpace(anchor.ObservedSelection) != "" {
		fmt.Fprintf(b, "**Current inline selection:** %s\n\n", pageSectionValue(anchor.ObservedSelection))
		return
	}
	status := anchor.Status
	if !domain.ValidConfluenceAnchorStatus(status) || status == domain.ConfluenceAnchorMatched {
		status = domain.ConfluenceAnchorUnavailable
	}
	fmt.Fprintf(b, "**Anchor:** %s\n\n", status)
	if strings.TrimSpace(anchor.OriginalSelection) != "" {
		fmt.Fprintf(b, "**Reported original selection:** %s\n\n", pageSectionValue(anchor.OriginalSelection))
	}
}

func confluenceCommentRenderKey(comment ConfluenceCommentsSidecarComment) string {
	parent, root := "", ""
	if comment.ParentID != nil {
		parent = *comment.ParentID
	}
	if comment.RootID != nil {
		root = *comment.RootID
	}
	fields := []string{
		comment.CreatedAt, comment.ID, string(comment.Relation), parent, root,
		string(comment.Location), string(comment.Resolution), comment.Author.DisplayName,
		comment.Author.ID, comment.UpdatedAt, fmt.Sprint(comment.Version), comment.BodyStorage, comment.Body,
	}
	if comment.Anchor != nil {
		fields = append(fields, string(comment.Anchor.Status), comment.Anchor.MarkerRef,
			comment.Anchor.OriginalSelection, comment.Anchor.ObservedSelection)
	}
	// Quoted formatting preserves field boundaries even when backend-controlled
	// text contains newlines, NUL bytes, or other separators.
	return fmt.Sprintf("%q", fields)
}
