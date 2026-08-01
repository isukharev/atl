package mirror

import (
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestRenderConfluenceCommentsMarkdownEmptyComplete(t *testing.T) {
	sidecar := completeCommentsSidecarForRender()
	got := string(RenderConfluenceCommentsMarkdown(&sidecar))
	want := "# Comments\n\n**Inventory:** complete\n\n**Completeness:** comments complete · threads complete · anchors complete\n\nNo comments.\n"
	if got != want {
		t.Fatalf("rendered Markdown:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderConfluenceCommentsMarkdownIsDeterministicAndNested(t *testing.T) {
	rootID := "root"
	replyID := "reply"
	comments := []ConfluenceCommentsSidecarComment{
		renderComment("late-root", "Zed", "2026-02-01T00:00:00Z", domain.ConfluenceCommentRelationRoot, nil, stringPointer("late-root")),
		renderComment("deep-5", "Depth Five", "2026-01-01T00:05:00Z", domain.ConfluenceCommentRelationReply, stringPointer("deep-4"), &rootID),
		renderComment("deep-3", "Depth Three", "2026-01-01T00:03:00Z", domain.ConfluenceCommentRelationReply, stringPointer("deep-2"), &rootID),
		renderComment(rootID, "Root", "2026-01-01T00:00:00Z", domain.ConfluenceCommentRelationRoot, nil, &rootID),
		renderComment("deep-4", "Depth Four", "2026-01-01T00:04:00Z", domain.ConfluenceCommentRelationReply, stringPointer("deep-3"), &rootID),
		renderComment(replyID, "Earlier sibling", "2026-01-01T00:01:00Z", domain.ConfluenceCommentRelationReply, &rootID, &rootID),
		renderComment("deep-2", "Depth Two", "2026-01-01T00:02:00Z", domain.ConfluenceCommentRelationReply, &replyID, &rootID),
		renderComment("later-sibling", "Later sibling", "2026-01-01T00:01:30Z", domain.ConfluenceCommentRelationReply, &rootID, &rootID),
	}
	sidecar := completeCommentsSidecarForRender()
	sidecar.Comments = comments

	first := string(RenderConfluenceCommentsMarkdown(&sidecar))
	for left, right := 0, len(comments)-1; left < right; left, right = left+1, right-1 {
		comments[left], comments[right] = comments[right], comments[left]
	}
	second := string(RenderConfluenceCommentsMarkdown(&sidecar))
	if first != second {
		t.Fatalf("render depends on input order:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	ordered := []string{
		"## Comment by Root", "### Reply by Earlier sibling", "#### Reply by Depth Two",
		"##### Reply by Depth Three", "###### Reply by Depth Four",
		"###### Reply by Depth Five", "thread depth 5", "### Reply by Later sibling",
		"## Comment by Zed",
	}
	position := -1
	for _, fragment := range ordered {
		next := strings.Index(first[position+1:], fragment)
		if next < 0 {
			t.Fatalf("missing or out-of-order fragment %q in:\n%s", fragment, first)
		}
		position += next + 1
	}
}

func TestRenderConfluenceCommentsMarkdownQualifiesAnchors(t *testing.T) {
	statuses := []struct {
		id       string
		status   domain.ConfluenceAnchorStatus
		original string
		observed string
	}{
		{id: "matched", status: domain.ConfluenceAnchorMatched, original: "old text", observed: "current text"},
		{id: "missing", status: domain.ConfluenceAnchorMissing, original: "reported missing", observed: "must not be current"},
		{id: "ambiguous", status: domain.ConfluenceAnchorAmbiguous, original: "reported ambiguous"},
		{id: "unavailable", status: domain.ConfluenceAnchorUnavailable, original: "reported unavailable"},
	}
	sidecar := completeCommentsSidecarForRender()
	for i, item := range statuses {
		comment := renderComment(item.id, item.id, "2026-01-01T00:0"+string(rune('0'+i))+":00Z", domain.ConfluenceCommentRelationRoot, nil, &item.id)
		comment.Location = domain.ConfluenceCommentLocationInline
		comment.Anchor = &ConfluenceCommentsSidecarAnchor{Status: item.status, OriginalSelection: item.original, ObservedSelection: item.observed}
		sidecar.Comments = append(sidecar.Comments, comment)
	}
	nilAnchor := renderComment("nil-anchor", "nil anchor", "2026-01-02T00:00:00Z", domain.ConfluenceCommentRelationRoot, nil, stringPointer("nil-anchor"))
	nilAnchor.Location = domain.ConfluenceCommentLocationInline
	sidecar.Comments = append(sidecar.Comments, nilAnchor)

	got := string(RenderConfluenceCommentsMarkdown(&sidecar))
	for _, want := range []string{
		"**Current inline selection:** current text",
		"**Anchor:** missing", "**Reported original selection:** reported missing",
		"**Anchor:** ambiguous", "**Reported original selection:** reported ambiguous",
		"**Anchor:** unavailable", "**Reported original selection:** reported unavailable",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"old text", "must not be current"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("render leaked unqualified anchor text %q:\n%s", forbidden, got)
		}
	}
	if strings.Count(got, "**Anchor:** unavailable") != 2 {
		t.Errorf("unavailable anchor count = %d, want 2:\n%s", strings.Count(got, "**Anchor:** unavailable"), got)
	}
}

func TestRenderConfluenceCommentsMarkdownCompletenessUsesOnlyClosedReasons(t *testing.T) {
	sidecar := completeCommentsSidecarForRender()
	sidecar.Complete = false
	sidecar.ThreadsComplete = false
	sidecar.AnchorsComplete = false
	sidecar.PartialReasons = []string{
		domain.ConfluenceCommentPartialMalformedAncestry,
		"private backend said something",
		domain.ConfluenceCommentPartialAnchorMissing,
		domain.ConfluenceCommentPartialMalformedAncestry,
	}
	got := string(RenderConfluenceCommentsMarkdown(&sidecar))
	if !strings.Contains(got, "**Inventory:** partial") ||
		!strings.Contains(got, "comments complete · threads partial · anchors partial") ||
		!strings.Contains(got, "`anchor_missing`, `malformed_ancestry`") {
		t.Fatalf("missing qualified completeness:\n%s", got)
	}
	if strings.Contains(got, "private backend") {
		t.Fatalf("render exposed an open-ended reason:\n%s", got)
	}
}

func TestRenderConfluenceCommentsMarkdownUsesClosedDiagnostics(t *testing.T) {
	sidecar := completeCommentsSidecarForRender()
	sidecar.Diagnostics = []ConfluenceCommentsSidecarDiagnostic{
		{Code: domain.ConfluenceCommentDiagnosticOriginalSelectionChanged, CommentID: "secret-record-id"},
		{Code: "backend supplied explanation"},
		{Code: domain.ConfluenceCommentDiagnosticOrphanMarker, MarkerRef: "private-marker"},
	}
	got := string(RenderConfluenceCommentsMarkdown(&sidecar))
	for _, want := range []string{
		"## Diagnostics",
		"An inline marker has no mirrored comment.",
		"A reported original selection differs from the current matched selection.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"backend supplied", "private-marker", "secret-record-id"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("render exposed diagnostic detail %q:\n%s", forbidden, got)
		}
	}
}

func TestRenderConfluenceCommentsMarkdownRetainsMalformedAncestry(t *testing.T) {
	rootID := "root"
	missing := "missing"
	cycleA, cycleB := "cycle-a", "cycle-b"
	sidecar := completeCommentsSidecarForRender()
	sidecar.ThreadsComplete = false
	sidecar.Complete = false
	sidecar.PartialReasons = []string{domain.ConfluenceCommentPartialMalformedAncestry}
	sidecar.Comments = []ConfluenceCommentsSidecarComment{
		renderComment(rootID, "Valid root", "2026-01-01T00:00:00Z", domain.ConfluenceCommentRelationRoot, nil, &rootID),
		renderComment("valid-reply", "Valid reply", "2026-01-01T00:01:00Z", domain.ConfluenceCommentRelationReply, &rootID, &rootID),
		renderComment("orphan", "Orphan", "2026-01-01T00:02:00Z", domain.ConfluenceCommentRelationReply, &missing, &rootID),
		renderComment(cycleA, "Cycle A", "2026-01-01T00:03:00Z", domain.ConfluenceCommentRelationReply, &cycleB, &rootID),
		renderComment(cycleB, "Cycle B", "2026-01-01T00:04:00Z", domain.ConfluenceCommentRelationReply, &cycleA, &rootID),
		renderComment("unknown", "Unknown relation", "2026-01-01T00:05:00Z", domain.ConfluenceCommentRelationUnknown, nil, nil),
	}

	got := string(RenderConfluenceCommentsMarkdown(&sidecar))
	if !strings.Contains(got, "## Comment by Valid root") || !strings.Contains(got, "### Reply by Valid reply") {
		t.Fatalf("valid tree was not rendered:\n%s", got)
	}
	if !strings.Contains(got, "## Unattached comments") || strings.Count(got, "**Thread:** unattached") != 4 {
		t.Fatalf("malformed records were not qualified as unattached:\n%s", got)
	}
	for _, author := range []string{"Orphan", "Cycle A", "Cycle B", "Unknown relation"} {
		if strings.Count(got, "by "+author+" —") != 1 {
			t.Errorf("record %q was dropped or duplicated:\n%s", author, got)
		}
	}
}

func TestRenderConfluenceCommentsMarkdownBodyStorageAndFallback(t *testing.T) {
	firstID, secondID := "native", "fallback"
	native := renderComment(firstID, "Native", "2026-01-01T00:00:00Z", domain.ConfluenceCommentRelationRoot, nil, &firstID)
	native.Body = "plain body must be replaced"
	native.BodyStorage = "<p>Native <strong>storage</strong>.</p><h1>Inside</h1>"
	fallback := renderComment(secondID, "Fallback", "2026-01-01T00:01:00Z", domain.ConfluenceCommentRelationRoot, nil, &secondID)
	fallback.Body = "Plain fallback body"
	fallback.BodyStorage = "<p>broken"
	sidecar := completeCommentsSidecarForRender()
	sidecar.Comments = []ConfluenceCommentsSidecarComment{fallback, native}

	got := string(RenderConfluenceCommentsMarkdown(&sidecar))
	for _, want := range []string{"Native **storage**.", "### Inside", "Plain fallback body"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing body fragment %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "plain body must be replaced") {
		t.Errorf("plain body was not replaced by native storage:\n%s", got)
	}
}

func TestRenderConfluenceCommentsMarkdownDemotesNestedBodyHeadings(t *testing.T) {
	rootID := "root"
	root := renderComment(rootID, "Root", "2026-01-01T00:00:00Z", domain.ConfluenceCommentRelationRoot, nil, &rootID)
	reply := renderComment("reply", "Reply", "2026-01-01T00:01:00Z", domain.ConfluenceCommentRelationReply, &rootID, &rootID)
	reply.BodyStorage = "<h1>Reply section</h1><h4>Deep section</h4>"
	sidecar := completeCommentsSidecarForRender()
	sidecar.Comments = []ConfluenceCommentsSidecarComment{reply, root}

	got := string(RenderConfluenceCommentsMarkdown(&sidecar))
	if !strings.Contains(got, "### Reply by Reply") || !strings.Contains(got, "#### Reply section") {
		t.Fatalf("reply body heading did not remain below its thread heading:\n%s", got)
	}
	if !strings.Contains(got, "**Deep section**") || strings.Contains(got, "###### Deep section") {
		t.Fatalf("overflowing reply body heading was not demoted to strong text:\n%s", got)
	}
}

func TestRenderConfluenceCommentsMarkdownShowsQualifiedMetadata(t *testing.T) {
	openID, resolvedID, unknownID := "open", "resolved", "unknown"
	open := renderComment(openID, "Open", "2026-01-01T00:00:00Z", domain.ConfluenceCommentRelationRoot, nil, &openID)
	resolved := renderComment(resolvedID, "Resolved", "2026-01-01T00:01:00Z", domain.ConfluenceCommentRelationRoot, nil, &resolvedID)
	resolved.Location, resolved.Resolution = domain.ConfluenceCommentLocationInline, domain.ConfluenceCommentResolutionResolved
	resolved.Anchor = &ConfluenceCommentsSidecarAnchor{Status: domain.ConfluenceAnchorUnavailable}
	unknown := renderComment(unknownID, "Unknown", "2026-01-01T00:02:00Z", domain.ConfluenceCommentRelationRoot, nil, &unknownID)
	unknown.Location, unknown.Resolution = domain.ConfluenceCommentLocation("invalid"), domain.ConfluenceCommentResolution("invalid")
	sidecar := completeCommentsSidecarForRender()
	sidecar.Comments = []ConfluenceCommentsSidecarComment{unknown, resolved, open}

	got := string(RenderConfluenceCommentsMarkdown(&sidecar))
	for _, want := range []string{
		"**Location:** footer · **State:** open",
		"**Location:** inline · **State:** resolved",
		"**Location:** unknown · **State:** unknown",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing metadata %q:\n%s", want, got)
		}
	}
}

func completeCommentsSidecarForRender() ConfluenceCommentsSidecarV2 {
	return ConfluenceCommentsSidecarV2{
		SchemaVersion: ConfluenceCommentsSidecarSchemaVersion,
		Complete:      true, CommentsComplete: true, ThreadsComplete: true, AnchorsComplete: true,
		Comments: []ConfluenceCommentsSidecarComment{}, PartialReasons: []string{}, Diagnostics: []ConfluenceCommentsSidecarDiagnostic{},
	}
}

func renderComment(id, author, created string, relation domain.ConfluenceCommentRelation, parentID, rootID *string) ConfluenceCommentsSidecarComment {
	return ConfluenceCommentsSidecarComment{
		ID: id, ParentID: parentID, RootID: rootID, Relation: relation,
		Location: domain.ConfluenceCommentLocationFooter, Resolution: domain.ConfluenceCommentResolutionOpen,
		Author: ConfluenceCommentsSidecarAuthor{DisplayName: author}, CreatedAt: created, Body: "Body for " + author,
	}
}

func stringPointer(value string) *string { return &value }
