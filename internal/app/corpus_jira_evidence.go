package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type corpusJiraEvidence struct {
	comments    *mirror.JiraCommentsSidecarV1
	attachments *corpusAttachmentCapture
}

func (s *JiraService) captureJiraCorpusEvidence(
	ctx context.Context,
	m *mirror.Mirror,
	issue *domain.Issue,
	paths jiraPullIssuePaths,
	options *corpusPullEvidenceOptions,
) (*corpusJiraEvidence, error) {
	if options == nil {
		return nil, nil
	}
	revision := corpusJiraParentRevision(issue)
	if revision == "" {
		return nil, fmt.Errorf("%w: Jira evidence parent revision is unavailable", domain.ErrCheckFailed)
	}
	evidence := &corpusJiraEvidence{}
	if options.binding.Comments {
		sidecar, err := s.captureJiraCorpusComments(ctx, issue.ID, revision, options)
		if err != nil {
			return nil, err
		}
		evidence.comments = sidecar
	}
	if options.binding.Attachments {
		inventory, err := s.readJiraCorpusAttachments(ctx, issue.ID, options)
		if err != nil {
			return nil, err
		}
		stem := strings.TrimSuffix(paths.wikiRel.String(), ".wiki")
		capture, err := captureCorpusAttachments(ctx, m.Root, mirror.CorpusSnapshotJira, issue.ID, stem, inventory, options,
			func(ctx context.Context, attachment domain.Attachment) (io.ReadCloser, error) {
				if strings.TrimSpace(attachment.DownPath) == "" {
					return nil, fmt.Errorf("%w: Jira attachment download reference is unavailable", domain.ErrCheckFailed)
				}
				return s.tr.StreamAttachment(ctx, attachment.DownPath)
			})
		if err != nil {
			return nil, err
		}
		evidence.attachments = &capture
	}
	current, err := s.tr.GetIssue(ctx, issue.ID, []string{"updated"})
	if err != nil {
		return nil, err
	}
	if current == nil || current.ID != issue.ID || corpusJiraParentRevision(current) != revision {
		return nil, fmt.Errorf("%w: Jira evidence parent changed during capture", domain.ErrCheckFailed)
	}
	return evidence, nil
}

func (s *JiraService) captureJiraCorpusComments(ctx context.Context, issueID, revision string, options *corpusPullEvidenceOptions) (*mirror.JiraCommentsSidecarV1, error) {
	reader, ok := s.tr.(domain.QualifiedJiraCommentReader)
	if !ok {
		if !options.binding.AllowPartialEvidence {
			return nil, fmt.Errorf("%w: backend cannot qualify Jira comments", domain.ErrCheckFailed)
		}
		return unavailableJiraCommentsSidecar(issueID, revision, mirror.JiraCommentsPartialUnsupported), nil
	}
	inventory, err := reader.ListJiraCommentsQualified(ctx, issueID, domain.JiraCommentReadOptions{
		MaxPages: options.binding.MaxCommentPagesPerItem, MaxItems: options.binding.MaxCommentsPerItem,
	})
	if err != nil {
		if options.binding.AllowPartialEvidence && errors.Is(err, domain.ErrForbidden) {
			return unavailableJiraCommentsSidecar(issueID, revision, mirror.JiraCommentsPartialForbidden), nil
		}
		return nil, err
	}
	if !inventory.Complete && !options.binding.AllowPartialEvidence {
		return nil, fmt.Errorf("%w: requested Jira comments are incomplete", domain.ErrCheckFailed)
	}
	comments := make([]mirror.JiraCommentsSidecarComment, 0, len(inventory.Comments))
	for _, comment := range inventory.Comments {
		parentID := comment.ParentID
		if parentID == "0" {
			parentID = ""
		}
		comments = append(comments, mirror.JiraCommentsSidecarComment{
			ID: comment.ID, AuthorKey: comment.AuthorKey, AuthorName: comment.AuthorName,
			AuthorDisplayName: comment.Author, CreatedAt: comment.Created, UpdatedAt: comment.Updated,
			ParentID: parentID, Body: comment.Body,
		})
	}
	return &mirror.JiraCommentsSidecarV1{
		SchemaVersion: mirror.JiraCommentsSidecarSchemaV1, Service: mirror.CorpusSnapshotJira,
		ParentID: issueID, ParentRevision: revision, Complete: inventory.Complete,
		PartialReason: inventory.PartialReason, Count: len(comments), Total: inventory.Total,
		TotalKnown: inventory.TotalKnown, PageCount: inventory.PageCount, Comments: comments,
	}, nil
}

func unavailableJiraCommentsSidecar(issueID, revision, reason string) *mirror.JiraCommentsSidecarV1 {
	return &mirror.JiraCommentsSidecarV1{
		SchemaVersion: mirror.JiraCommentsSidecarSchemaV1, Service: mirror.CorpusSnapshotJira,
		ParentID: issueID, ParentRevision: revision, PartialReason: reason,
		Comments: []mirror.JiraCommentsSidecarComment{},
	}
}

func (s *JiraService) readJiraCorpusAttachments(ctx context.Context, issueID string, options *corpusPullEvidenceOptions) (domain.AttachmentInventory, error) {
	reader, ok := s.tr.(domain.QualifiedJiraAttachmentReader)
	if !ok {
		if !options.binding.AllowPartialEvidence {
			return domain.AttachmentInventory{}, fmt.Errorf("%w: backend cannot qualify Jira attachments", domain.ErrCheckFailed)
		}
		return domain.AttachmentInventory{Attachments: []domain.Attachment{}, PartialReason: mirror.AttachmentInventoryUnsupported}, nil
	}
	inventory, err := reader.ListJiraAttachmentsQualified(ctx, issueID, domain.JiraAttachmentReadOptions{MaxItems: options.binding.MaxAttachmentsPerItem})
	if err != nil {
		if options.binding.AllowPartialEvidence && errors.Is(err, domain.ErrForbidden) {
			return domain.AttachmentInventory{Attachments: []domain.Attachment{}, PartialReason: mirror.AttachmentInventoryForbidden}, nil
		}
		return domain.AttachmentInventory{}, err
	}
	return domain.AttachmentInventory(inventory), nil
}

func corpusJiraParentRevision(issue *domain.Issue) string {
	if issue == nil || issue.Fields == nil {
		return ""
	}
	revision, _ := issue.Fields["updated"].(string)
	if strings.TrimSpace(revision) != revision {
		return ""
	}
	return revision
}

func finalizeJiraCorpusEvidence(
	m *mirror.Mirror,
	issue *domain.Issue,
	paths jiraPullIssuePaths,
	state mirror.SyncState,
	artifacts []mirror.CompletePullArtifact,
	evidence *corpusJiraEvidence,
) ([]mirror.CompletePullArtifact, error) {
	if evidence == nil {
		return artifacts, nil
	}
	stem := strings.TrimSuffix(paths.wikiRel.String(), ".wiki")
	metadata, err := completePullArtifactData(artifacts, paths.snapshotRel.String())
	if err != nil {
		return nil, err
	}
	metadataHash := mirror.Hash(metadata)
	origin, err := corpusMirrorOrigin(m, mirror.CorpusSnapshotJira)
	if err != nil {
		return nil, err
	}
	if evidence.comments != nil {
		sidecar := *evidence.comments
		sidecar.OriginSHA256 = origin
		sidecar.NativeSHA256 = state.Hash
		sidecar.MetadataSHA256 = metadataHash
		encoded, err := mirror.EncodeJiraCommentsSidecarV1(sidecar)
		if err != nil {
			return nil, err
		}
		path, err := mirror.NewPublicArtifactPath(stem + ".comments.json")
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, mirror.CompletePullArtifact{
			Path: path, Role: mirror.CompletePullArtifactRoleAuxiliary, Data: encoded, Mode: 0o600,
		})
	}
	if evidence.attachments != nil {
		attachmentArtifacts, err := finalizeCorpusAttachmentCapture(
			m, mirror.CorpusSnapshotJira, stem, issue.ID, 0, corpusJiraParentRevision(issue),
			state.Hash, metadataHash, *evidence.attachments,
		)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, attachmentArtifacts...)
	}
	return artifacts, nil
}
