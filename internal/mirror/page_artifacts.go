package mirror

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

// commentSidecar carries a page's comments for a pull that requested them (nil
// when --comments was off). It is auxiliary read-only data: the comment bytes
// never enter the content hash, are never copied to .atl/base/, and never affect
// dirty/drift/push gating — only the two sidecar files and Meta summaries.
type commentSidecar struct {
	encoded   []byte
	display   []domain.Comment
	v2        *ConfluenceCommentsSidecarV2
	truncated bool
}

// CompletePullArtifact is one fully prepared canonical page artifact for the
// crash-recoverable complete-pull publisher. Path is a constructed public or
// pristine-base destination. Data is copied into a private staging area before
// any destination is mutated. Remove expresses the intentional retirement of
// an owned auxiliary artifact. BestEffort is reserved for derived Markdown
// views: publication may establish either these exact bytes or absence,
// preserving the longstanding render-failure contract.
type CompletePullArtifact struct {
	Path       ArtifactPath
	Data       []byte
	Mode       os.FileMode
	Remove     bool
	BestEffort bool
}

// PrepareCompletePullView builds every page/base artifact without touching the
// filesystem. Complete pulls stage this complete set before publication;
// ordinary and incremental pulls keep using WriteView.
func (m *Mirror) PrepareCompletePullView(dir, slug string, page *domain.Resource, refs []domain.Ref, mdOpts MDViewOpts) (SyncState, []CompletePullArtifact, error) {
	return m.preparePageFiles(dir, slug, page, refs, nil, mdOpts)
}

// PrepareCompletePullConfluenceComments is the qualified-comment counterpart
// of PrepareCompletePullView.
func (m *Mirror) PrepareCompletePullConfluenceComments(dir, slug string, page *domain.Resource, refs []domain.Ref, sidecar ConfluenceCommentsSidecarV2, display []domain.Comment, truncated bool, mdOpts MDViewOpts) (SyncState, []CompletePullArtifact, error) {
	encoded, err := EncodeConfluenceCommentsSidecarV2(sidecar)
	if err != nil {
		return SyncState{}, nil, err
	}
	decoded, err := DecodeConfluenceCommentsSidecar(encoded)
	if err != nil || decoded.V2 == nil {
		return SyncState{}, nil, fmt.Errorf("%w: canonical Confluence comments sidecar could not be decoded", domain.ErrCheckFailed)
	}
	canonical := *decoded.V2
	display = orderCommentProjection(canonical.Comments, display)
	mdOpts.Comments = orderCommentProjection(canonical.Comments, mdOpts.Comments)
	mdOpts.CommentView = orderCommentProjection(canonical.Comments, mdOpts.CommentView)
	if mdOpts.QualifiedComments != nil {
		displayCanonical, displayErr := qualifiedCommentsDisplayTimes(canonical, *mdOpts.QualifiedComments)
		if displayErr != nil {
			return SyncState{}, nil, displayErr
		}
		mdOpts.Comments = nil
		mdOpts.QualifiedComments = &displayCanonical
	}
	return m.preparePageFiles(dir, slug, page, refs, &commentSidecar{
		encoded: encoded, display: display, v2: &canonical, truncated: truncated,
	}, mdOpts)
}

func mirrorRelativePath(root, target string) (string, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: page artifact escapes mirror root: %s", domain.ErrCheckFailed, target)
	}
	return filepath.ToSlash(rel), nil
}

func (m *Mirror) preparePageFiles(dir, slug string, page *domain.Resource, refs []domain.Ref, cs *commentSidecar, mdOpts MDViewOpts) (SyncState, []CompletePullArtifact, error) {
	csfRel, err := mirrorRelativePath(m.Root, filepath.Join(dir, slug+".csf"))
	if err != nil {
		return SyncState{}, nil, err
	}
	csfArtifactPath, err := NewPublicArtifactPath(csfRel)
	if err != nil {
		return SyncState{}, nil, err
	}
	artifacts := []CompletePullArtifact{{Path: csfArtifactPath, Data: append([]byte(nil), page.Body...), Mode: 0o644}}
	md := []byte(MDUnavailableStub)
	if root, parseErr := csf.Parse(page.Body); parseErr == nil {
		md = RenderMarkdownOpts(root, refs, mdOpts)
	}
	mdRel, err := mirrorRelativePath(m.Root, filepath.Join(dir, slug+".md"))
	if err != nil {
		return SyncState{}, nil, err
	}
	mdArtifactPath, err := NewPublicArtifactPath(mdRel)
	if err != nil {
		return SyncState{}, nil, err
	}
	artifacts = append(artifacts, CompletePullArtifact{Path: mdArtifactPath, Data: md, Mode: 0o644, BestEffort: true})
	meta := Meta{
		ID: page.ID, Title: page.Title, Space: page.SpaceKey, Version: page.Version,
		Hash: Hash(page.Body), Parent: page.Parent, Ancestors: page.Ancestors,
		Labels: page.Labels, Updated: page.Updated, Restricted: page.Restricted, Refs: refs,
	}
	if cs != nil {
		commentArtifacts, commentErr := m.preparePageCommentArtifacts(dir, slug, cs, mdOpts, &meta)
		if commentErr != nil {
			return SyncState{}, nil, commentErr
		}
		artifacts = append(artifacts, commentArtifacts...)
	}
	mb, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return SyncState{}, nil, err
	}
	metaRel, err := mirrorRelativePath(m.Root, filepath.Join(dir, slug+".meta.json"))
	if err != nil {
		return SyncState{}, nil, err
	}
	metaArtifactPath, err := NewPublicArtifactPath(metaRel)
	if err != nil {
		return SyncState{}, nil, err
	}
	baseRel := filepath.ToSlash(filepath.Join(".atl", "base", safepath.Segment(page.ID)+".csf"))
	baseArtifactPath, err := newPrivateArtifactPath(baseRel)
	if err != nil {
		return SyncState{}, nil, err
	}
	artifacts = append(artifacts,
		CompletePullArtifact{Path: metaArtifactPath, Data: append(mb, '\n'), Mode: 0o644},
		CompletePullArtifact{Path: baseArtifactPath, Data: append([]byte(nil), page.Body...), Mode: 0o600},
	)
	return SyncState{ID: page.ID, Version: page.Version, Hash: Hash(page.Body), Path: csfRel}, artifacts, nil
}

func (m *Mirror) preparePageCommentArtifacts(dir, slug string, cs *commentSidecar, mdOpts MDViewOpts, meta *Meta) ([]CompletePullArtifact, error) {
	commentsJSONRel, err := mirrorRelativePath(m.Root, filepath.Join(dir, slug+".comments.json"))
	if err != nil {
		return nil, err
	}
	commentsJSONPath, err := NewPublicArtifactPath(commentsJSONRel)
	if err != nil {
		return nil, err
	}
	commentsMDRel, err := mirrorRelativePath(m.Root, filepath.Join(dir, slug+".comments.md"))
	if err != nil {
		return nil, err
	}
	commentsMDPath, err := NewPublicArtifactPath(commentsMDRel)
	if err != nil {
		return nil, err
	}
	display := cs.display
	if mdOpts.CommentView != nil {
		display = mdOpts.CommentView
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
		{Path: commentsJSONPath, Data: append([]byte(nil), cs.encoded...), Mode: 0o644},
		{Path: commentsMDPath, Data: RenderCommentsMarkdown(display), Mode: 0o644, BestEffort: true},
	}, nil
}

// writePageFiles writes the page artifacts (.csf, .md view, .meta.json, base
// copy) and returns the .csf path relative to the mirror root; sidecar state
// is recorded by the caller. When cs is non-nil it also writes the comment
// sidecar files and stamps the comment counters into .meta.json.
func (m *Mirror) writePageFiles(dir, slug string, page *domain.Resource, refs []domain.Ref, cs *commentSidecar, mdOpts MDViewOpts) (rel string, err error) {
	state, artifacts, err := m.preparePageFiles(dir, slug, page, refs, cs, mdOpts)
	if err != nil {
		return "", err
	}
	for _, artifact := range artifacts {
		target, pathErr := artifactPathTarget(m.Root, artifact.Path)
		if pathErr != nil {
			return "", pathErr
		}
		if err := safepath.MkdirAllWithin(m.Root, filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		if err := safepath.WriteFileWithin(m.Root, target, artifact.Data, artifact.Mode); err != nil {
			if artifact.BestEffort {
				_ = safepath.RemoveWithin(m.Root, target)
				continue
			}
			return "", err
		}
	}
	return filepath.FromSlash(state.Path), nil
}
