package app

import (
	"fmt"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const (
	ConfluencePullIncludeAssets      = "assets"
	ConfluencePullIncludeComments    = "comments"
	ConfluencePullIncludeAttachments = "attachments"

	ConfluencePullIncludeDeferred     = "deferred"
	ConfluencePullIncludeQualified    = "qualified"
	ConfluencePullIncludePartial      = "partial"
	ConfluencePullIncludeFailed       = "failed"
	ConfluencePullIncludeNotRequested = "not_requested"

	ConfluencePullIncludeReasonPreviewDeferred      = "preview_deferred"
	ConfluencePullIncludeReasonNotAttempted         = "not_attempted"
	ConfluencePullIncludeReasonResolutionIncomplete = "resolution_incomplete"
	ConfluencePullIncludeReasonInventoryIncomplete  = "inventory_incomplete"
	ConfluencePullIncludeReasonBodyIncomplete       = "body_incomplete"
	ConfluencePullIncludeReasonReadFailed           = "read_failed"
	ConfluencePullIncludeReasonStagingFailed        = "staging_failed"
)

// ValidConfluencePullIncludeReason recognizes the non-empty, content-free
// public reason taxonomy. Qualified and unrequested dimensions omit reason.
func ValidConfluencePullIncludeReason(reason string) bool {
	switch reason {
	case ConfluencePullIncludeReasonPreviewDeferred,
		ConfluencePullIncludeReasonNotAttempted,
		ConfluencePullIncludeReasonResolutionIncomplete,
		ConfluencePullIncludeReasonInventoryIncomplete,
		ConfluencePullIncludeReasonBodyIncomplete,
		ConfluencePullIncludeReasonReadFailed,
		ConfluencePullIncludeReasonStagingFailed:
		return true
	}
	return false
}

// ConfluencePullInclude qualifies one optional pull dimension. The ordered
// top-level includes array is additive: assets precede comments, which precede
// attachments. An omitted flag is distinguishable from requested work that
// preview deferred or an actual pull only partially completed.
type ConfluencePullInclude struct {
	Dimension     string `json:"dimension"`
	Requested     bool   `json:"requested"`
	Qualification string `json:"qualification"`
	// Complete is present only after actual work proves coverage or proves that
	// coverage is incomplete. Deferred and unrequested dimensions remain nil.
	Complete *bool  `json:"complete,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type confluencePullIncludeProgress struct {
	expected int
	dryRun   bool

	requested          map[string]bool
	attempted          map[string]int
	partial            map[string]int
	failed             map[string]int
	reasons            map[string]string
	evidenceIncomplete bool
}

func (r *PullResult) restoreConfluencePullIncludes(checkpoint mirror.CompletePullCheckpoint) error {
	progress := r.includeProgress
	if progress == nil {
		return fmt.Errorf("%w: Confluence pull include progress is unavailable", domain.ErrCheckFailed)
	}
	progress.evidenceIncomplete = !checkpoint.Includes.EvidenceComplete && checkpoint.NextIndex > 0
	for dimension, aggregate := range map[string]mirror.CompletePullIncludeAggregate{
		ConfluencePullIncludeAssets:      checkpoint.Includes.Assets,
		ConfluencePullIncludeComments:    checkpoint.Includes.Comments,
		ConfluencePullIncludeAttachments: checkpoint.Includes.Attachments,
	} {
		if !progress.requested[dimension] {
			if aggregate != (mirror.CompletePullIncludeAggregate{}) {
				return fmt.Errorf("%w: complete-pull checkpoint contains evidence for an unrequested include", domain.ErrCheckFailed)
			}
			continue
		}
		if checkpoint.Includes.EvidenceComplete && aggregate.Published != checkpoint.NextIndex {
			return fmt.Errorf("%w: complete-pull include evidence does not cover its durable prefix", domain.ErrCheckFailed)
		}
		progress.attempted[dimension] = aggregate.Published
		progress.partial[dimension] = aggregate.Partial
		progress.reasons[dimension] = aggregate.Reason
	}
	r.finalizeConfluencePullIncludes()
	return nil
}

func confluencePullIncludeEvidence(dimension, qualification, reason string) domain.ConfluencePullIncludeEvidence {
	return domain.ConfluencePullIncludeEvidence{Dimension: dimension, Qualification: qualification, Reason: reason}
}

func confluencePullAttachmentIncludeEvidence(qualification, reason string, bodyBytes int64) domain.ConfluencePullIncludeEvidence {
	return domain.ConfluencePullIncludeEvidence{
		Dimension: ConfluencePullIncludeAttachments, Qualification: qualification, Reason: reason, BodyBytes: bodyBytes,
	}
}

func newConfluencePullIncludes(opts PullOpts, expected int) ([]ConfluencePullInclude, *confluencePullIncludeProgress) {
	requested := map[string]bool{
		ConfluencePullIncludeAssets:      opts.Assets,
		ConfluencePullIncludeComments:    opts.Comments,
		ConfluencePullIncludeAttachments: confluencePullAttachmentsRequested(opts),
	}
	dimensions := []string{ConfluencePullIncludeAssets, ConfluencePullIncludeComments}
	// Unlike the original two dimensions, attachment capture is a newly added
	// opt-in surface. Omit its unrequested record so established pull output
	// remains byte-compatible for callers that did not ask for it.
	if requested[ConfluencePullIncludeAttachments] {
		dimensions = append(dimensions, ConfluencePullIncludeAttachments)
	}
	includes := make([]ConfluencePullInclude, 0, len(dimensions))
	for _, dimension := range dimensions {
		qualification := ConfluencePullIncludeNotRequested
		reason := ""
		if requested[dimension] {
			qualification = ConfluencePullIncludeDeferred
			reason = ConfluencePullIncludeReasonNotAttempted
			if opts.DryRun {
				reason = ConfluencePullIncludeReasonPreviewDeferred
			}
		}
		includes = append(includes, ConfluencePullInclude{
			Dimension: dimension, Requested: requested[dimension], Qualification: qualification, Reason: reason,
		})
	}
	return includes, &confluencePullIncludeProgress{
		expected: expected, dryRun: opts.DryRun, requested: requested,
		attempted: map[string]int{}, partial: map[string]int{}, failed: map[string]int{}, reasons: map[string]string{},
	}
}

func newConfluencePullResult(root string, warnings []string, opts PullOpts, expected int) *PullResult {
	includes, progress := newConfluencePullIncludes(opts, expected)
	return &PullResult{
		Root: root, Pages: []PulledPage{}, Warnings: warnings,
		Includes: includes, includeProgress: progress,
	}
}

func (r *PullResult) recordConfluencePullInclude(dimension, qualification, reason string) error {
	if !validConfluencePullIncludeRecord(dimension, qualification, reason) {
		return fmt.Errorf("%w: Confluence pull include qualification is invalid", domain.ErrCheckFailed)
	}
	progress := r.includeProgress
	if progress == nil || !progress.requested[dimension] || progress.dryRun {
		return nil
	}
	progress.attempted[dimension]++
	switch qualification {
	case ConfluencePullIncludeFailed:
		progress.failed[dimension]++
	case ConfluencePullIncludePartial:
		progress.partial[dimension]++
	}
	if reason != "" {
		progress.reasons[dimension] = reason
	}
	r.finalizeConfluencePullIncludes()
	return nil
}

// demotePublishedConfluencePullIncludes changes already-counted publication
// evidence into a failed result without counting the same page twice. This is
// used when a later durability barrier fails after page publication succeeded.
func (r *PullResult) demotePublishedConfluencePullIncludes(values []domain.ConfluencePullIncludeEvidence) error {
	progress := r.includeProgress
	if progress == nil {
		return fmt.Errorf("%w: Confluence pull include progress is unavailable", domain.ErrCheckFailed)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validConfluencePullIncludeRecord(value.Dimension, ConfluencePullIncludeFailed, ConfluencePullIncludeReasonStagingFailed) {
			return fmt.Errorf("%w: Confluence pull include qualification is invalid", domain.ErrCheckFailed)
		}
		if _, duplicate := seen[value.Dimension]; duplicate {
			continue
		}
		seen[value.Dimension] = struct{}{}
		if !progress.requested[value.Dimension] || progress.dryRun {
			continue
		}
		progress.failed[value.Dimension] = 1
		progress.reasons[value.Dimension] = ConfluencePullIncludeReasonStagingFailed
	}
	r.finalizeConfluencePullIncludes()
	return nil
}

func validConfluencePullIncludeRecord(dimension, qualification, reason string) bool {
	if dimension != ConfluencePullIncludeAssets && dimension != ConfluencePullIncludeComments && dimension != ConfluencePullIncludeAttachments {
		return false
	}
	switch qualification {
	case ConfluencePullIncludeQualified:
		return reason == ""
	case ConfluencePullIncludePartial:
		return reason == ConfluencePullIncludeReasonResolutionIncomplete ||
			reason == ConfluencePullIncludeReasonInventoryIncomplete ||
			dimension == ConfluencePullIncludeAttachments && reason == ConfluencePullIncludeReasonBodyIncomplete
	case ConfluencePullIncludeFailed:
		return reason == ConfluencePullIncludeReasonReadFailed ||
			reason == ConfluencePullIncludeReasonStagingFailed
	}
	return false
}

func (r *PullResult) finalizeConfluencePullIncludes() {
	progress := r.includeProgress
	if progress == nil {
		return
	}
	for i := range r.Includes {
		include := &r.Includes[i]
		include.Complete = nil
		include.Reason = ""
		if !progress.requested[include.Dimension] {
			include.Requested = false
			include.Qualification = ConfluencePullIncludeNotRequested
			continue
		}
		include.Requested = true
		if progress.dryRun {
			include.Qualification = ConfluencePullIncludeDeferred
			include.Reason = ConfluencePullIncludeReasonPreviewDeferred
			continue
		}
		switch {
		case progress.failed[include.Dimension] > 0:
			include.Qualification = ConfluencePullIncludeFailed
			include.Complete = confluencePullIncludeComplete(false)
			include.Reason = progress.reasons[include.Dimension]
		case progress.evidenceIncomplete:
			include.Qualification = ConfluencePullIncludePartial
			include.Complete = confluencePullIncludeComplete(false)
			// Missing evidence for an accepted legacy prefix dominates any
			// qualified or partial suffix: the aggregate cannot explain that
			// prefix and must retain the explicit migration reason.
			include.Reason = ConfluencePullIncludeReasonNotAttempted
		case progress.attempted[include.Dimension] == 0 && progress.expected > 0:
			include.Qualification = ConfluencePullIncludeDeferred
			include.Reason = ConfluencePullIncludeReasonNotAttempted
		case progress.partial[include.Dimension] > 0 || progress.attempted[include.Dimension] < progress.expected:
			include.Qualification = ConfluencePullIncludePartial
			include.Complete = confluencePullIncludeComplete(false)
			include.Reason = progress.reasons[include.Dimension]
			if include.Reason == "" {
				include.Reason = ConfluencePullIncludeReasonNotAttempted
			}
		default:
			include.Qualification = ConfluencePullIncludeQualified
			include.Complete = confluencePullIncludeComplete(true)
		}
	}
}

func confluencePullIncludeComplete(complete bool) *bool { return &complete }

// HasFailedInclude reports whether a requested optional read failed. The CLI
// uses this narrow predicate to emit the qualified result before returning the
// original non-zero error; unrelated pull failures retain the existing empty
// stdout contract.
func (r *PullResult) HasFailedInclude() bool {
	if r == nil {
		return false
	}
	for _, include := range r.Includes {
		if include.Qualification == ConfluencePullIncludeFailed {
			return true
		}
	}
	return false
}
