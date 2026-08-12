package app

import (
	"fmt"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const (
	ConfluencePullIncludeAssets   = "assets"
	ConfluencePullIncludeComments = "comments"

	ConfluencePullIncludeDeferred     = "deferred"
	ConfluencePullIncludeQualified    = "qualified"
	ConfluencePullIncludePartial      = "partial"
	ConfluencePullIncludeFailed       = "failed"
	ConfluencePullIncludeNotRequested = "not_requested"

	ConfluencePullIncludeReasonPreviewDeferred      = "preview_deferred"
	ConfluencePullIncludeReasonNotAttempted         = "not_attempted"
	ConfluencePullIncludeReasonResolutionIncomplete = "resolution_incomplete"
	ConfluencePullIncludeReasonInventoryIncomplete  = "inventory_incomplete"
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
		ConfluencePullIncludeReasonReadFailed,
		ConfluencePullIncludeReasonStagingFailed:
		return true
	}
	return false
}

// ConfluencePullInclude qualifies one optional pull dimension. The ordered
// top-level includes array is additive: assets always precede comments, and an
// omitted flag is distinguishable from requested work that preview deferred or
// an actual pull only partially completed.
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
		ConfluencePullIncludeAssets: checkpoint.Includes.Assets, ConfluencePullIncludeComments: checkpoint.Includes.Comments,
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

func newConfluencePullIncludes(opts PullOpts, expected int) ([]ConfluencePullInclude, *confluencePullIncludeProgress) {
	requested := map[string]bool{
		ConfluencePullIncludeAssets:   opts.Assets,
		ConfluencePullIncludeComments: opts.Comments,
	}
	includes := make([]ConfluencePullInclude, 0, len(requested))
	for _, dimension := range []string{ConfluencePullIncludeAssets, ConfluencePullIncludeComments} {
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

func validConfluencePullIncludeRecord(dimension, qualification, reason string) bool {
	if dimension != ConfluencePullIncludeAssets && dimension != ConfluencePullIncludeComments {
		return false
	}
	switch qualification {
	case ConfluencePullIncludeQualified:
		return reason == ""
	case ConfluencePullIncludePartial:
		return reason == ConfluencePullIncludeReasonResolutionIncomplete ||
			reason == ConfluencePullIncludeReasonInventoryIncomplete
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
