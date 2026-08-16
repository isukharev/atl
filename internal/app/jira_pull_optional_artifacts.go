package app

import (
	"fmt"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const (
	// A complete-pull page is one atomic publication. Keep public Jira body
	// capture well below the publisher's global capacity so native, metadata,
	// comment evidence, and bounded replacement work always retain room.
	jiraCompletePullMaxAttachmentBodiesPerIssue = 512
	jiraCompletePullMaxAttachmentBodyBytes      = int64(64 << 20) // one issue transaction
)

// ValidateJiraPullOptionalArtifacts validates the public, complete-only
// evidence options without reading configuration, a backend, or a mirror.
// It intentionally reuses the corpus policy grammar so MIME, count, and byte
// bounds have one canonical interpretation across evidence consumers.
func ValidateJiraPullOptionalArtifacts(opts JiraPullOpts) error {
	if !jiraPullPublicOptionalArtifactFields(opts) {
		return nil
	}
	if !opts.Complete {
		return fmt.Errorf("%w: qualified comments and attachment evidence require --complete", domain.ErrUsage)
	}
	policy := jiraPullPublicEvidencePolicy(opts)
	if policy.AttachmentBodies && policy.MaxTotalAttachmentBytes > corpusBuildMaxAttachmentTotalBytes {
		return fmt.Errorf("%w: --max-total-attachment-bytes exceeds the complete-pull aggregate body limit of %d", domain.ErrUsage, corpusBuildMaxAttachmentTotalBytes)
	}
	return validateCorpusEvidenceOptions(policy)
}

func jiraPullPublicOptionalArtifactFields(opts JiraPullOpts) bool {
	return opts.Comments || opts.MaxCommentPagesPerItem != 0 || opts.MaxCommentsPerItem != 0 ||
		opts.Attachments || opts.AttachmentBodies || len(opts.AttachmentMediaTypes) != 0 ||
		opts.MaxAttachmentsPerItem != 0 || opts.MaxAttachmentBytes != 0 || opts.MaxTotalAttachmentBytes != 0
}

func jiraPullPublicEvidencePolicy(opts JiraPullOpts) CorpusBuildOptions {
	policy := CorpusBuildOptions{
		Comments:                opts.Comments,
		MaxCommentPagesPerItem:  opts.MaxCommentPagesPerItem,
		MaxCommentsPerItem:      opts.MaxCommentsPerItem,
		Attachments:             opts.Attachments,
		MaxAttachmentsPerItem:   opts.MaxAttachmentsPerItem,
		AttachmentBodies:        opts.AttachmentBodies,
		AttachmentMediaTypes:    append([]string(nil), opts.AttachmentMediaTypes...),
		MaxAttachmentBytes:      opts.MaxAttachmentBytes,
		MaxTotalAttachmentBytes: opts.MaxTotalAttachmentBytes,
	}
	if opts.Attachments {
		// Jira exposes one exact attachment field rather than a paginated
		// attachment endpoint. Set the shared page field only when attachment
		// capture is actually selected; otherwise a comments-only policy would
		// incorrectly look like an orphaned attachment bound.
		policy.MaxAttachmentPagesPerItem = 1
	}
	return policy
}

// prepareJiraPullOptionalArtifacts makes the public request private to the app
// layer and binds it to the complete-pull receipt. Corpus construction already
// supplies evidence directly, so public flags and an internal policy may never
// be combined into an ambiguous options hash.
func prepareJiraPullOptionalArtifacts(opts *JiraPullOpts) error {
	if opts == nil {
		return fmt.Errorf("%w: Jira pull options are unavailable", domain.ErrUsage)
	}
	if opts.evidence != nil {
		if jiraPullPublicOptionalArtifactFields(*opts) {
			return fmt.Errorf("%w: public optional-artifact flags cannot be combined with an internal evidence policy", domain.ErrUsage)
		}
		return nil
	}
	if err := ValidateJiraPullOptionalArtifacts(*opts); err != nil {
		return err
	}
	if !jiraPullPublicOptionalArtifactFields(*opts) {
		return nil
	}
	evidence := newCorpusPullEvidenceOptions(jiraPullPublicEvidencePolicy(*opts))
	if evidence == nil {
		return fmt.Errorf("%w: Jira complete-pull evidence policy is unavailable", domain.ErrCheckFailed)
	}
	if evidence.binding.AttachmentBodies {
		evidence.binding.MaxAttachmentBodiesPerItem = jiraCompletePullMaxAttachmentBodiesPerIssue
	}
	opts.evidence = evidence
	return nil
}

func jiraPullEvidenceRequested(opts JiraPullOpts) bool {
	return opts.evidence != nil && (opts.evidence.binding.Comments || opts.evidence.binding.Attachments)
}

// restoreJiraCompleteAttachmentBudget rebuilds aggregate captured-body usage
// from the completed prefix's private sidecars. Jira journal schemas predate
// public attachment evidence, so durable artifacts—not a mutable progress
// counter—are the source of truth on resume.
func restoreJiraCompleteAttachmentBudget(m *mirror.Mirror, opts *JiraPullOpts, checkpoint mirror.CompletePullCheckpoint) error {
	if m == nil || opts == nil || opts.evidence == nil || !opts.evidence.binding.Attachments || !opts.evidence.binding.AttachmentBodies {
		return nil
	}
	if checkpoint.Service != mirror.CompletePullServiceJira || checkpoint.NextIndex < 0 || checkpoint.NextIndex > len(checkpoint.IDs) {
		return fmt.Errorf("%w: complete-pull Jira attachment budget has an invalid checkpoint", domain.ErrCheckFailed)
	}
	usage, err := m.VerifyJiraCompletePullAttachmentBodyBytes(checkpoint, opts.evidence.binding.MaxTotalAttachmentBytes)
	if err != nil || usage < 0 || usage > opts.evidence.binding.MaxTotalAttachmentBytes {
		return fmt.Errorf("%w: complete-pull Jira attachment body usage does not match its durable sidecars", domain.ErrCheckFailed)
	}
	if !opts.evidence.budget.restoreVerifiedUsage(usage) {
		return fmt.Errorf("%w: complete-pull Jira attachment body usage conflicts with the active aggregate budget", domain.ErrCheckFailed)
	}
	return nil
}
