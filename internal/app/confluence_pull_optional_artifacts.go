package app

import (
	"context"
	"fmt"
	"io"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const (
	// Complete-pull publication is one atomic per-page transaction. Keep a
	// substantial fixed reserve for native/base/view/comment/asset artifacts
	// and for retiring a previous attachment capture on a recrawl.
	confluenceCompletePullMaxAttachmentBodiesPerPage = 512
	// The publisher admits 256 MiB including native/page artifacts. Public
	// attachment capture has a lower independent total so attachment policy
	// cannot itself consume the entire transaction before staging begins.
	confluenceCompletePullMaxAttachmentBodyBytes = int64(64 << 20)
)

// ValidateConfluencePullOptionalArtifacts validates the public complete-pull
// optional-artifact policy without reading configuration, a backend, or the
// mirror. The CLI uses it before composition so malformed bounds cannot be
// obscured by an unrelated configuration error.
func ValidateConfluencePullOptionalArtifacts(opts PullOpts) error {
	if opts.AllowPartialArtifacts {
		if !opts.Complete {
			return fmt.Errorf("%w: --allow-partial-artifacts requires --complete", domain.ErrUsage)
		}
		if !opts.Comments && !opts.Attachments {
			return fmt.Errorf("%w: --allow-partial-artifacts requires --comments or --attachments", domain.ErrUsage)
		}
	}
	if !confluencePullPublicAttachmentFields(opts) {
		return nil
	}
	if !opts.Attachments {
		return fmt.Errorf("%w: attachment body policy and bounds require --attachments", domain.ErrUsage)
	}
	if !opts.Complete {
		return fmt.Errorf("%w: --attachments requires --complete", domain.ErrUsage)
	}
	policy := confluencePullPublicAttachmentPolicy(opts)
	if policy.AttachmentBodies && policy.MaxTotalAttachmentBytes > confluenceCompletePullMaxAttachmentBodyBytes {
		return fmt.Errorf("%w: --max-total-attachment-bytes exceeds the complete-pull body publication limit of %d", domain.ErrUsage, confluenceCompletePullMaxAttachmentBodyBytes)
	}
	return validateCorpusEvidenceOptions(policy)
}

func confluencePullPublicAttachmentFields(opts PullOpts) bool {
	return opts.Attachments || opts.AttachmentBodies || len(opts.AttachmentMediaTypes) != 0 ||
		opts.MaxAttachmentPagesPerItem != 0 || opts.MaxAttachmentsPerItem != 0 ||
		opts.MaxAttachmentBytes != 0 || opts.MaxTotalAttachmentBytes != 0
}

func confluencePullPublicAttachmentPolicy(opts PullOpts) CorpusBuildOptions {
	return CorpusBuildOptions{
		Attachments:               true,
		MaxAttachmentPagesPerItem: opts.MaxAttachmentPagesPerItem,
		MaxAttachmentsPerItem:     opts.MaxAttachmentsPerItem,
		AttachmentBodies:          opts.AttachmentBodies,
		AttachmentMediaTypes:      append([]string(nil), opts.AttachmentMediaTypes...),
		MaxAttachmentBytes:        opts.MaxAttachmentBytes,
		MaxTotalAttachmentBytes:   opts.MaxTotalAttachmentBytes,
		AllowPartialEvidence:      opts.AllowPartialArtifacts,
	}
}

// prepareConfluencePullOptionalArtifacts keeps the public complete-pull
// surface separate from the corpus builder while reusing the same bounded
// attachment policy and streaming implementation. The resulting evidence
// object is private to the app and is bound into the complete-pull receipt.
func prepareConfluencePullOptionalArtifacts(opts *PullOpts) error {
	if opts == nil {
		return fmt.Errorf("%w: Confluence pull options are unavailable", domain.ErrUsage)
	}
	if opts.evidence != nil {
		if confluencePullPublicAttachmentFields(*opts) || opts.AllowPartialArtifacts {
			return fmt.Errorf("%w: public optional-artifact flags cannot be combined with an internal evidence policy", domain.ErrUsage)
		}
		return nil
	}
	if err := ValidateConfluencePullOptionalArtifacts(*opts); err != nil {
		return err
	}
	if !confluencePullPublicAttachmentFields(*opts) {
		return nil
	}
	evidence := newCorpusPullEvidenceOptions(confluencePullPublicAttachmentPolicy(*opts))
	if evidence == nil || !evidence.binding.Attachments {
		return fmt.Errorf("%w: attachment capture policy is unavailable", domain.ErrCheckFailed)
	}
	if evidence.binding.AttachmentBodies {
		evidence.binding.MaxAttachmentBodiesPerItem = confluenceCompletePullMaxAttachmentBodiesPerPage
	}
	opts.evidence = evidence
	return nil
}

func confluencePullAttachmentsRequested(opts PullOpts) bool {
	return opts.Attachments || opts.evidence != nil && opts.evidence.binding.Attachments
}

func confluencePullAllowsPartialArtifacts(opts PullOpts) bool {
	return opts.AllowPartialArtifacts || opts.evidence != nil && opts.evidence.binding.AllowPartialEvidence
}

// openRevalidatedConfluenceCorpusAttachment refuses to attribute a
// filename-based Confluence binary route to an inventory id until the backend
// has immediately revalidated one unambiguous title/version selector and the
// resulting metadata agrees with that exact inventory record. The backend has
// no ID-bound binary endpoint or metadata+body transaction; this narrows the
// selector race and keeps the remaining limitation explicit rather than
// publishing a body under an unrelated attachment id.
func (s *ConfluenceService) openRevalidatedConfluenceCorpusAttachment(
	ctx context.Context,
	pageID string,
	attachment domain.Attachment,
	options *corpusPullEvidenceOptions,
) (io.ReadCloser, error) {
	if s == nil || options == nil || !options.binding.AttachmentBodies ||
		!domain.ValidConfluenceReadID(pageID) || !domain.ValidConfluenceReadID(attachment.ID) ||
		attachment.ID == pageID || !ValidConfluenceAttachmentDownloadFilename(attachment.Title) ||
		attachment.Version <= 0 || attachment.FileSize < 0 {
		return nil, fmt.Errorf("%w: Confluence attachment capture selector is invalid", domain.ErrCheckFailed)
	}
	revalidator, ok := s.store.(domain.QualifiedConfluenceAttachmentDownloadRevalidator)
	if !ok {
		return nil, fmt.Errorf("%w: backend cannot revalidate Confluence attachment capture selectors", domain.ErrCheckFailed)
	}
	metadataBudget, err := domain.NewReadBudget(confluenceAttachmentDownloadMaxRequests, confluenceAttachmentDownloadMaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: attachment capture metadata budget is invalid", domain.ErrCheckFailed)
	}
	metadataCtx, cancel := context.WithTimeout(
		domain.WithReadBudget(ctx, metadataBudget), confluenceAttachmentDownloadMetadataDeadline,
	)
	evidence, err := revalidator.RevalidateAttachmentDownload(
		domain.WithSingleAttempt(metadataCtx), pageID, attachment.Title, attachment.Version,
	)
	cancel()
	if err != nil {
		return nil, err
	}
	if evidence.AttachmentID != attachment.ID || evidence.PageID != pageID || evidence.Filename != attachment.Title ||
		evidence.Version != attachment.Version || evidence.FileSize != attachment.FileSize {
		return nil, fmt.Errorf("%w: attachment capture selector revalidation does not match the qualified inventory", domain.ErrCheckFailed)
	}
	binaryBudget, err := domain.NewReadBudget(confluenceAttachmentDownloadBinaryAttempts, options.binding.MaxAttachmentBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: attachment capture binary budget is invalid", domain.ErrCheckFailed)
	}
	return s.store.DownloadAttachment(
		domain.WithNoReplayRetries(domain.WithReadBudget(ctx, binaryBudget)), pageID, attachment.Title, evidence.Version,
	)
}

// restoreConfluenceCompleteAttachmentBudget continues the public aggregate
// body cap from the durable accepted prefix. The prefix accounting is written
// in the same private progress commit as attachment evidence, so a resume does
// not silently turn one complete clone into multiple independent byte budgets.
func restoreConfluenceCompleteAttachmentBudget(m *mirror.Mirror, opts *PullOpts, checkpoint mirror.CompletePullCheckpoint) error {
	if m == nil || opts == nil || !confluencePullAttachmentsRequested(*opts) || opts.evidence == nil || !opts.evidence.binding.AttachmentBodies {
		return nil
	}
	if checkpoint.Service != mirror.CompletePullServiceConfluence || checkpoint.NextIndex < 0 || checkpoint.NextIndex > len(checkpoint.IDs) {
		return fmt.Errorf("%w: complete-pull attachment budget has an invalid checkpoint", domain.ErrCheckFailed)
	}
	usage := checkpoint.Includes.Attachments.BodyBytes
	if checkpoint.NextIndex == 0 {
		if usage != 0 {
			return fmt.Errorf("%w: empty complete-pull prefix has attachment body usage", domain.ErrCheckFailed)
		}
		return nil
	}
	if !checkpoint.Includes.EvidenceComplete || checkpoint.Includes.Attachments.Published != checkpoint.NextIndex ||
		usage < 0 || usage > opts.evidence.binding.MaxTotalAttachmentBytes {
		return fmt.Errorf("%w: complete-pull attachment body usage is not bound to its accepted prefix", domain.ErrCheckFailed)
	}
	actual, err := m.VerifyConfluenceCompletePullAttachmentBodyBytes(checkpoint, opts.evidence.binding.MaxTotalAttachmentBytes)
	if err != nil || actual != usage {
		return fmt.Errorf("%w: complete-pull attachment body usage does not match its durable sidecars", domain.ErrCheckFailed)
	}
	if !opts.evidence.budget.restoreVerifiedUsage(usage) {
		return fmt.Errorf("%w: complete-pull attachment body usage conflicts with the active aggregate budget", domain.ErrCheckFailed)
	}
	return nil
}
