package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const (
	// Revalidating one Jira attachment selector re-reads its parent attachment
	// field. Keep that metadata response and its physical request separate from
	// the body stream so a large inventory or a redirect cannot borrow the
	// per-body budget reserved by the complete-pull transaction.
	jiraAttachmentCaptureMetadataAttempts      = 1
	jiraAttachmentCaptureMetadataResponseBytes = 16 << 20
	jiraAttachmentCaptureMetadataDeadline      = 15 * time.Second
	jiraAttachmentCaptureBinaryAttempts        = 1
)

type corpusJiraEvidence struct {
	comments    *mirror.JiraCommentsSidecarV1
	attachments *corpusAttachmentCapture
}

// captureJiraCompleteEvidence joins qualified comment and attachment evidence
// to the same per-issue transaction as a complete pull. It captures comments
// before planning optional body capacity, then reserves all known core,
// sidecar, and retirement slots before the first binary request.
func (s *JiraService) captureJiraCompleteEvidence(
	ctx context.Context,
	m *mirror.Mirror,
	origin string,
	dryRun bool,
	issue *domain.Issue,
	paths jiraPullIssuePaths,
	state mirror.SyncState,
	artifacts []mirror.CompletePullArtifact,
	relocation *mirror.JiraIssueRelocation,
	options *corpusPullEvidenceOptions,
) ([]mirror.CompletePullArtifact, error) {
	if options == nil {
		return artifacts, nil
	}
	revision := corpusJiraParentRevision(issue)
	if revision == "" {
		return nil, fmt.Errorf("%w: Jira evidence parent revision is unavailable", domain.ErrCheckFailed)
	}
	if options.binding.Comments {
		commentByteLimit, limitErr := jiraCompleteCommentPublicationByteLimit(m, origin, issue.ID, revision, state, artifacts, options)
		if limitErr != nil {
			return nil, limitErr
		}
		comments, err := s.captureJiraCorpusComments(ctx, issue.ID, revision, options, commentByteLimit)
		if err != nil {
			return nil, err
		}
		artifacts, err = finalizeJiraCorpusEvidence(origin, issue, paths, state, artifacts, &corpusJiraEvidence{comments: comments})
		if err != nil {
			return nil, err
		}
	}
	if options.binding.Attachments {
		inventory, err := s.readJiraCorpusAttachments(ctx, issue.ID, options)
		if err != nil {
			return nil, err
		}
		stem := strings.TrimSuffix(paths.wikiRel.String(), ".wiki")
		if err := validateJiraAttachmentCapturePreflight(issue.ID, revision, stem, inventory, options); err != nil {
			return nil, err
		}
		var capture corpusAttachmentCapture
		if options.binding.AttachmentBodies {
			commentRetirements := 0
			if relocation == nil {
				metadata, metadataErr := completePullArtifactData(artifacts, paths.snapshotRel.String())
				if metadataErr != nil {
					return nil, metadataErr
				}
				commentRetirements, metadataErr = m.JiraCommentCaptureRetirementBound(issue.ID, state, metadata, artifacts, true)
				if metadataErr != nil {
					return nil, metadataErr
				}
			}
			bodyLimit, bodyByteLimit, limitErr := jiraCompleteAttachmentBodyPublicationLimit(m, issue.ID, state, artifacts, relocation, commentRetirements)
			if limitErr != nil {
				return nil, limitErr
			}
			open := func(ctx context.Context, attachment domain.Attachment) (io.ReadCloser, error) {
				return s.openRevalidatedJiraCorpusAttachment(ctx, issue.ID, attachment, options)
			}
			if dryRun {
				capture, err = captureCorpusAttachmentsWithBodyLimitInMemory(
					ctx, m.Root, mirror.CorpusSnapshotJira, issue.ID, stem, inventory, options,
					bodyLimit, true, bodyByteLimit, true, open,
				)
			} else {
				capture, err = captureCorpusAttachmentsWithBodyLimit(
					ctx, m.Root, mirror.CorpusSnapshotJira, issue.ID, stem, inventory, options,
					bodyLimit, true, bodyByteLimit, true, open,
				)
			}
		} else {
			capture, err = captureCorpusAttachments(ctx, m.Root, mirror.CorpusSnapshotJira, issue.ID, stem, inventory, options,
				func(ctx context.Context, attachment domain.Attachment) (io.ReadCloser, error) {
					return s.openRevalidatedJiraCorpusAttachment(ctx, issue.ID, attachment, options)
				},
			)
		}
		if err != nil {
			return nil, err
		}
		artifacts, err = finalizeJiraCorpusEvidence(origin, issue, paths, state, artifacts, &corpusJiraEvidence{attachments: &capture})
		if err != nil {
			return nil, err
		}
	}
	if err := s.revalidateJiraCorpusEvidenceParent(ctx, issue.ID, revision); err != nil {
		return nil, err
	}
	return artifacts, nil
}

// jiraCompleteCommentPublicationByteLimit reserves the complete-pull page's
// known primary bytes and, when selected, the bounded attachment sidecar
// before the comments endpoint is read. The adapter applies this conservative
// encoded-byte envelope member by member, so a comments sidecar can always
// join the same atomic publication rather than failing after remote paging.
func jiraCompleteCommentPublicationByteLimit(
	m *mirror.Mirror,
	origin, issueID, revision string,
	state mirror.SyncState,
	artifacts []mirror.CompletePullArtifact,
	options *corpusPullEvidenceOptions,
) (int64, error) {
	if m == nil || options == nil || !options.binding.Comments || !domain.ValidJiraEvidenceParentRevision(revision) {
		return 0, fmt.Errorf("%w: Jira comment publication policy is invalid", domain.ErrCheckFailed)
	}
	knownBytes, err := completePullArtifactInputBytes(artifacts)
	if err != nil {
		return 0, err
	}
	metadata, err := completePullArtifactData(artifacts, strings.TrimSuffix(state.Path, ".wiki")+".json")
	if err != nil {
		return 0, err
	}
	header, err := mirror.EncodeJiraCommentsSidecarV1(mirror.JiraCommentsSidecarV1{
		SchemaVersion: mirror.JiraCommentsSidecarSchemaV1, Service: mirror.CorpusSnapshotJira,
		OriginSHA256: origin, ParentID: issueID, ParentRevision: revision,
		NativeSHA256: state.Hash, MetadataSHA256: mirror.Hash(metadata),
		PartialReason: domain.JiraCommentPartialPaginationStalled,
		Total:         math.MaxInt, TotalKnown: true, PageCount: domain.JiraCommentReadMaxPages,
		Comments: []mirror.JiraCommentsSidecarComment{},
	})
	if err != nil {
		return 0, fmt.Errorf("%w: Jira comments sidecar header is invalid", domain.ErrCheckFailed)
	}
	// Count can grow from 0 to the bounded inventory maximum and the final
	// framing changes by a few bytes. The exact known parent binding above is
	// already included; this small fixed suffix only covers those counters.
	const counterSuffixReserve = int64(256)
	headerReserve := int64(len(header)) + counterSuffixReserve
	reserve := int64(0)
	if options.binding.Attachments {
		reserve = mirror.MaxAttachmentSidecarPublicationBytes
	}
	if headerReserve > math.MaxInt64-reserve {
		return 0, fmt.Errorf("%w: Jira comments sidecar header is too large", domain.ErrCheckFailed)
	}
	available, err := m.CompletePullPublicationPayloadBytes(knownBytes, reserve+headerReserve)
	if err != nil {
		return 0, fmt.Errorf("%w: Jira comments cannot fit the complete-pull publication", domain.ErrCheckFailed)
	}
	if available > domain.JiraCommentReadMaxBytes {
		available = domain.JiraCommentReadMaxBytes
	}
	return available, nil
}

func jiraCompleteAttachmentBodyPublicationLimit(
	m *mirror.Mirror,
	identity string,
	state mirror.SyncState,
	artifacts []mirror.CompletePullArtifact,
	relocation *mirror.JiraIssueRelocation,
	otherRetirements int,
) (int, int64, error) {
	if m == nil || !canonicalPositiveNumericString(identity) || state.Identity != identity || otherRetirements < 0 {
		return 0, 0, fmt.Errorf("%w: Jira attachment publication identity is invalid", domain.ErrCheckFailed)
	}
	replacement := len(artifacts) + 1 // attachment sidecar, plus body slots below
	retirement := 0
	var err error
	if relocation != nil {
		retirement, err = m.JiraIssueRelocationPublicationArtifactCount(relocation)
	} else {
		retirement, err = m.JiraAttachmentCaptureBodyReplacementBound(identity, state)
		retirement += otherRetirements
	}
	if err != nil {
		return 0, 0, err
	}
	slots, err := m.CompletePullPublicationArtifactSlots(replacement, retirement)
	if err != nil {
		return 0, 0, err
	}
	if slots > jiraCompletePullMaxAttachmentBodiesPerIssue {
		slots = jiraCompletePullMaxAttachmentBodiesPerIssue
	}
	knownBytes, err := completePullArtifactInputBytes(artifacts)
	if err != nil {
		return 0, 0, err
	}
	bodyBytes, err := m.CompletePullPublicationPayloadBytes(knownBytes, mirror.MaxAttachmentSidecarPublicationBytes)
	if err != nil {
		return 0, 0, err
	}
	if bodyBytes > jiraCompletePullMaxAttachmentBodyBytes {
		bodyBytes = jiraCompletePullMaxAttachmentBodyBytes
	}
	return slots, bodyBytes, nil
}

func (s *JiraService) revalidateJiraCorpusEvidenceParent(ctx context.Context, issueID, revision string) error {
	current, err := s.tr.GetIssue(ctx, issueID, []string{"updated"})
	if err != nil {
		return err
	}
	if current == nil || current.ID != issueID || corpusJiraParentRevision(current) != revision {
		return fmt.Errorf("%w: Jira evidence parent changed during capture", domain.ErrCheckFailed)
	}
	return nil
}

// openRevalidatedJiraCorpusAttachment binds an immediately re-read attachment
// field record to the inventory before streaming a body. The generic Tracker
// stream surface deliberately takes only an opaque URL, so this adapter
// capability is the boundary that prevents a changed parent field from being
// published under the old attachment identity.
func (s *JiraService) openRevalidatedJiraCorpusAttachment(
	ctx context.Context,
	issueID string,
	attachment domain.Attachment,
	options *corpusPullEvidenceOptions,
) (io.ReadCloser, error) {
	if s == nil || options == nil || !options.binding.AttachmentBodies ||
		options.binding.MaxAttachmentBytes <= 0 {
		return nil, fmt.Errorf("%w: Jira attachment capture selector is invalid", domain.ErrCheckFailed)
	}
	return s.openRevalidatedJiraAttachmentWithLimit(ctx, issueID, attachment, options.binding.MaxAttachmentBytes)
}

// openRevalidatedJiraAttachmentWithLimit is the shared exact-selector read
// boundary for qualified complete pulls and the separate resumable attachment
// materializer. Both paths retain the same one-request metadata and binary
// limits; only their independently reviewed local publication envelopes differ.
func (s *JiraService) openRevalidatedJiraAttachmentWithLimit(
	ctx context.Context,
	issueID string,
	attachment domain.Attachment,
	maximum int64,
) (io.ReadCloser, error) {
	if s == nil || maximum <= 0 || !canonicalPositiveNumericString(issueID) || !canonicalPositiveNumericString(attachment.ID) ||
		attachment.FileSize < 0 || attachment.FileSize > maximum {
		return nil, fmt.Errorf("%w: Jira attachment capture selector is invalid", domain.ErrCheckFailed)
	}
	revalidator, ok := s.tr.(domain.QualifiedJiraAttachmentDownloadRevalidator)
	if !ok {
		return nil, fmt.Errorf("%w: backend cannot revalidate Jira attachment capture selectors", domain.ErrCheckFailed)
	}
	metadataBudget, err := domain.NewReadBudget(jiraAttachmentCaptureMetadataAttempts, jiraAttachmentCaptureMetadataResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: Jira attachment capture metadata budget is invalid", domain.ErrCheckFailed)
	}
	metadataCtx, cancel := context.WithTimeout(
		domain.WithReadBudget(ctx, metadataBudget), jiraAttachmentCaptureMetadataDeadline,
	)
	evidence, err := revalidator.RevalidateJiraAttachmentDownload(domain.WithSingleAttempt(metadataCtx), issueID, attachment.ID)
	cancel()
	if err != nil {
		return nil, err
	}
	current := evidence.Attachment
	if evidence.ParentID != issueID || current.ID != attachment.ID || current.Title != attachment.Title ||
		current.MediaType != attachment.MediaType || current.FileSize != attachment.FileSize || current.Created != attachment.Created ||
		current.Author != attachment.Author || current.AuthorName != attachment.AuthorName || current.AuthorKey != attachment.AuthorKey ||
		strings.TrimSpace(current.DownPath) == "" {
		return nil, fmt.Errorf("%w: Jira attachment selector revalidation does not match the qualified inventory", domain.ErrCheckFailed)
	}
	binaryBudget, err := domain.NewReadBudget(jiraAttachmentCaptureBinaryAttempts, maximum)
	if err != nil {
		return nil, fmt.Errorf("%w: Jira attachment capture binary budget is invalid", domain.ErrCheckFailed)
	}
	return s.tr.StreamAttachment(domain.WithNoReplayRetries(domain.WithReadBudget(ctx, binaryBudget)), current.DownPath)
}

func (s *JiraService) captureJiraCorpusComments(ctx context.Context, issueID, revision string, options *corpusPullEvidenceOptions, maximum int64) (*mirror.JiraCommentsSidecarV1, error) {
	if options == nil || !options.binding.Comments || maximum <= 0 || maximum > domain.JiraCommentReadMaxBytes {
		return nil, fmt.Errorf("%w: Jira comments capture policy is unavailable", domain.ErrCheckFailed)
	}
	reader, ok := s.tr.(domain.QualifiedJiraCommentReader)
	if !ok {
		if !options.binding.AllowPartialEvidence {
			return nil, fmt.Errorf("%w: backend cannot qualify Jira comments", domain.ErrCheckFailed)
		}
		return unavailableJiraCommentsSidecar(issueID, revision, mirror.JiraCommentsPartialUnsupported), nil
	}
	inventory, err := reader.ListJiraCommentsQualified(ctx, issueID, domain.JiraCommentReadOptions{
		MaxPages: options.binding.MaxCommentPagesPerItem, MaxItems: options.binding.MaxCommentsPerItem, MaxBytes: maximum,
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
	if err := domain.ValidateJiraCommentInventory(inventory); err != nil {
		return nil, err
	}
	comments := make([]mirror.JiraCommentsSidecarComment, 0, len(inventory.Comments))
	for _, comment := range inventory.Comments {
		parentID := comment.ParentID
		if parentID == "0" {
			parentID = ""
		}
		comment.ParentID = parentID
		if !domain.ValidJiraCommentEvidenceRecord(comment) {
			return nil, fmt.Errorf("%w: Jira comment cannot be represented in qualified evidence", domain.ErrCheckFailed)
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
	if !domain.ValidJiraEvidenceParentRevision(revision) {
		return ""
	}
	return revision
}

func finalizeJiraCorpusEvidence(
	origin string,
	issue *domain.Issue,
	paths jiraPullIssuePaths,
	state mirror.SyncState,
	artifacts []mirror.CompletePullArtifact,
	evidence *corpusJiraEvidence,
) ([]mirror.CompletePullArtifact, error) {
	if evidence == nil {
		return artifacts, nil
	}
	if strings.TrimSpace(origin) == "" {
		return nil, fmt.Errorf("%w: Jira evidence origin is invalid", domain.ErrCheckFailed)
	}
	stem := strings.TrimSuffix(paths.wikiRel.String(), ".wiki")
	metadata, err := completePullArtifactData(artifacts, paths.snapshotRel.String())
	if err != nil {
		return nil, err
	}
	metadataHash := mirror.Hash(metadata)
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
		attachmentArtifacts, err := finalizeCorpusAttachmentCaptureForOrigin(
			origin, mirror.CorpusSnapshotJira, stem, issue.ID, 0, corpusJiraParentRevision(issue),
			state.Hash, metadataHash, *evidence.attachments,
		)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, attachmentArtifacts...)
	}
	return artifacts, nil
}
