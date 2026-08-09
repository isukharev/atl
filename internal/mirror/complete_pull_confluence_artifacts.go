package mirror

import (
	"path/filepath"

	"github.com/isukharev/atl/internal/domain"
)

func (m *Mirror) prepareCompletePullCommentArtifacts(dir, slug string, cs *commentSidecar, mdOpts MDViewOpts, meta *Meta) ([]CompletePullArtifact, error) {
	commentsJSONRel, err := PublicArtifactPathWithin(m.Root, filepath.Join(dir, slug+".comments.json"))
	if err != nil {
		return nil, err
	}
	displayComments := cs.display
	if mdOpts.CommentView != nil {
		displayComments = mdOpts.CommentView
	}
	commentsMDRel, err := PublicArtifactPathWithin(m.Root, filepath.Join(dir, slug+".comments.md"))
	if err != nil {
		return nil, err
	}
	meta.CommentsPulled = true
	meta.CommentCount = len(cs.display)
	meta.CommentsTruncated = cs.truncated
	if cs.v2 != nil {
		meta.CommentSidecarVersion = cs.v2.SchemaVersion
		meta.CommentCount = cs.v2.Count
		meta.CommentRootCount = cs.v2.RootCount
		meta.CommentsComplete = boolPointer(cs.v2.CommentsComplete)
		meta.CommentThreadsComplete = boolPointer(cs.v2.ThreadsComplete)
		meta.CommentAnchorsComplete = boolPointer(cs.v2.AnchorsComplete)
		meta.CommentPartialReasons = append([]string(nil), cs.v2.PartialReasons...)
		for _, comment := range cs.v2.Comments {
			if comment.Location == domain.ConfluenceCommentLocationInline && comment.Resolution == domain.ConfluenceCommentResolutionOpen {
				meta.OpenInlineCommentCount++
			}
		}
	}
	return []CompletePullArtifact{
		{Path: commentsJSONRel, Data: append([]byte(nil), cs.encoded...), Mode: 0o644},
		{Path: commentsMDRel, Data: RenderCommentsMarkdown(displayComments), Mode: 0o644, BestEffort: true},
	}, nil
}
