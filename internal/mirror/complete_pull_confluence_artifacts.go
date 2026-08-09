package mirror

import (
	"fmt"
	"path/filepath"

	"github.com/isukharev/atl/internal/domain"
)

func confluenceCompletePullBasePath(entry CompletePullJournalEntry) string {
	return filepath.ToSlash(filepath.Join(".atl", "base", entry.State.ID+".csf"))
}

func validateConfluenceCompletePullPayloads(entry CompletePullJournalEntry, artifacts []CompletePullArtifact) error {
	nativeCount, baseCount := 0, 0
	for _, artifact := range artifacts {
		switch artifact.Path.String() {
		case entry.State.Path:
			nativeCount++
			if artifact.Remove || artifact.BestEffort || artifact.Mode != 0o644 || Hash(artifact.Data) != entry.State.Hash {
				return fmt.Errorf("Confluence native artifact does not match the accepted page state")
			}
		case confluenceCompletePullBasePath(entry):
			baseCount++
			if artifact.Remove || artifact.BestEffort || artifact.Mode != 0o600 || Hash(artifact.Data) != entry.State.Hash {
				return fmt.Errorf("Confluence pristine-base artifact does not match the accepted page state")
			}
		}
	}
	if nativeCount != 1 || baseCount != 1 {
		return fmt.Errorf("Confluence publication requires exactly one native and pristine-base artifact")
	}
	return nil
}

func validateConfluenceCompletePullIntent(entry CompletePullJournalEntry, artifacts []completePullPublicationArtifact) error {
	nativeCount, baseCount := 0, 0
	for _, artifact := range artifacts {
		switch artifact.Path {
		case entry.State.Path:
			nativeCount++
			if artifact.Remove || artifact.BestEffort || artifact.Mode != 0o644 || artifact.SHA256 != entry.State.Hash {
				return fmt.Errorf("Confluence native artifact does not match the accepted page state")
			}
		case confluenceCompletePullBasePath(entry):
			baseCount++
			if artifact.Remove || artifact.BestEffort || artifact.Mode != 0o600 || artifact.SHA256 != entry.State.Hash {
				return fmt.Errorf("Confluence pristine-base artifact does not match the accepted page state")
			}
		}
	}
	if nativeCount != 1 || baseCount != 1 {
		return fmt.Errorf("Confluence publication requires exactly one native and pristine-base artifact")
	}
	return nil
}

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
