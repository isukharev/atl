package mirror

import (
	"fmt"

	"github.com/isukharev/atl/internal/domain"
)

// CompletePullIncludeAggregate is the content-free durable summary for one
// optional Confluence pull dimension. Published counts pages whose complete
// artifact publication was accepted; Partial is the subset with incomplete
// backend evidence.
type CompletePullIncludeAggregate struct {
	Published int    `json:"published"`
	Partial   int    `json:"partial"`
	Reason    string `json:"reason,omitempty"`
	// BodyBytes is private aggregate accounting for durably published
	// attachment-body payloads. It is schema-v4-only and never appears in the
	// public pull result.
	BodyBytes int64 `json:"body_bytes,omitempty"`
}

// CompletePullIncludeProgress is stored only in the mutable Confluence
// progress sidecar. EvidenceComplete=false means a legacy accepted prefix had
// no per-page include evidence and must never be upgraded to complete.
type CompletePullIncludeProgress struct {
	EvidenceComplete bool                         `json:"evidence_complete"`
	Assets           CompletePullIncludeAggregate `json:"assets"`
	Comments         CompletePullIncludeAggregate `json:"comments"`
	Attachments      CompletePullIncludeAggregate `json:"attachments,omitempty"`
}

func validateCompletePullIncludeProgress(value CompletePullIncludeProgress, nextIndex int) error {
	schema := completePullConfluenceProgressSchema
	if value.Attachments != (CompletePullIncludeAggregate{}) {
		schema = completePullConfluenceProgressSchema4
	}
	return validateCompletePullIncludeProgressSchema(schema, value, nextIndex)
}

// validateCompletePullIncludeProgressSchema keeps the attachment aggregate
// behind the schema that introduced it. Schema v3 is retained for recovery of
// old checkpoints, not as an extensible envelope for new evidence.
func validateCompletePullIncludeProgressSchema(schema int, value CompletePullIncludeProgress, nextIndex int) error {
	if schema != completePullConfluenceProgressSchema && schema != completePullConfluenceProgressSchema4 {
		return fmt.Errorf("%w: unsupported Confluence complete-pull include schema", domain.ErrCheckFailed)
	}
	if schema == completePullConfluenceProgressSchema && value.Attachments != (CompletePullIncludeAggregate{}) {
		return fmt.Errorf("%w: legacy Confluence complete-pull progress contains attachment evidence", domain.ErrCheckFailed)
	}
	if schema == completePullConfluenceProgressSchema4 && value.Attachments == (CompletePullIncludeAggregate{}) {
		return fmt.Errorf("%w: attachment-aware Confluence complete-pull progress omits attachment evidence", domain.ErrCheckFailed)
	}
	for dimension, aggregate := range map[string]CompletePullIncludeAggregate{
		domain.ConfluencePullIncludeAssets:      value.Assets,
		domain.ConfluencePullIncludeComments:    value.Comments,
		domain.ConfluencePullIncludeAttachments: value.Attachments,
	} {
		if aggregate.Published < 0 || aggregate.Partial < 0 || aggregate.Partial > aggregate.Published || aggregate.Published > nextIndex ||
			aggregate.BodyBytes < 0 || dimension != domain.ConfluencePullIncludeAttachments && aggregate.BodyBytes != 0 ||
			aggregate.BodyBytes > 0 && aggregate.Published == 0 {
			return fmt.Errorf("%w: complete-pull %s include progress is outside its durable prefix", domain.ErrCheckFailed, dimension)
		}
		if value.EvidenceComplete && aggregate.Published != 0 && aggregate.Published != nextIndex {
			return fmt.Errorf("%w: complete-pull %s include evidence does not cover its durable prefix", domain.ErrCheckFailed, dimension)
		}
		if aggregate.Partial == 0 && aggregate.Reason != "" {
			return fmt.Errorf("%w: complete-pull %s include progress has a reason without partial evidence", domain.ErrCheckFailed, dimension)
		}
		if aggregate.Partial > 0 {
			evidence := domain.ConfluencePullIncludeEvidence{Dimension: dimension, Qualification: domain.ConfluencePullIncludePartial, Reason: aggregate.Reason}
			if !domain.ValidConfluencePullIncludeEvidence(evidence) {
				return fmt.Errorf("%w: complete-pull %s include progress has an invalid reason", domain.ErrCheckFailed, dimension)
			}
		}
	}
	return nil
}

// validateCompletePullIncludeAdvance keeps one immutable selection's evidence
// monotonic. A stale writer may neither erase a durable prefix nor attach new
// evidence to pages that were already accepted without it. A legacy journal
// may conservatively demote EvidenceComplete while advancing the prefix.
func validateCompletePullIncludeAdvance(previous CompletePullIncludeProgress, previousIndex int, next CompletePullIncludeProgress, nextIndex int) error {
	if nextIndex < previousIndex {
		return fmt.Errorf("%w: complete-pull progress cannot move behind its durable prefix", domain.ErrCheckFailed)
	}
	delta := nextIndex - previousIndex
	if !previous.EvidenceComplete && next.EvidenceComplete {
		return fmt.Errorf("%w: complete-pull include evidence cannot improve an unqualified durable prefix", domain.ErrCheckFailed)
	}
	if previous.EvidenceComplete && !next.EvidenceComplete && delta == 0 {
		return fmt.Errorf("%w: complete-pull include evidence cannot change without advancing its durable prefix", domain.ErrCheckFailed)
	}
	for dimension, pair := range map[string][2]CompletePullIncludeAggregate{
		domain.ConfluencePullIncludeAssets:      {previous.Assets, next.Assets},
		domain.ConfluencePullIncludeComments:    {previous.Comments, next.Comments},
		domain.ConfluencePullIncludeAttachments: {previous.Attachments, next.Attachments},
	} {
		before, after := pair[0], pair[1]
		publishedDelta := after.Published - before.Published
		partialDelta := after.Partial - before.Partial
		bodyBytesDelta := after.BodyBytes - before.BodyBytes
		if publishedDelta < 0 || partialDelta < 0 || bodyBytesDelta < 0 {
			return fmt.Errorf("%w: complete-pull %s include evidence cannot be erased", domain.ErrCheckFailed, dimension)
		}
		if publishedDelta > delta || partialDelta > publishedDelta {
			return fmt.Errorf("%w: complete-pull %s include evidence exceeds newly durable progress", domain.ErrCheckFailed, dimension)
		}
		if partialDelta == 0 && after.Reason != before.Reason {
			return fmt.Errorf("%w: complete-pull %s include reason changed without new partial evidence", domain.ErrCheckFailed, dimension)
		}
		if dimension != domain.ConfluencePullIncludeAttachments && bodyBytesDelta != 0 {
			return fmt.Errorf("%w: complete-pull %s include body accounting is invalid", domain.ErrCheckFailed, dimension)
		}
		if dimension == domain.ConfluencePullIncludeAttachments && bodyBytesDelta != 0 && publishedDelta == 0 {
			return fmt.Errorf("%w: complete-pull attachment body accounting cannot change without a newly durable page", domain.ErrCheckFailed)
		}
	}
	return nil
}

func validateCompletePullIncludeEvidence(values []domain.ConfluencePullIncludeEvidence) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !domain.ValidConfluencePullIncludeEvidence(value) {
			return fmt.Errorf("%w: complete-pull include evidence is invalid", domain.ErrCheckFailed)
		}
		if _, duplicate := seen[value.Dimension]; duplicate {
			return fmt.Errorf("%w: complete-pull include evidence repeats a dimension", domain.ErrCheckFailed)
		}
		seen[value.Dimension] = struct{}{}
	}
	return nil
}

func applyCompletePullIncludeEvidence(progress *CompletePullIncludeProgress, values []domain.ConfluencePullIncludeEvidence) error {
	if err := validateCompletePullIncludeEvidence(values); err != nil {
		return err
	}
	for _, value := range values {
		aggregate := &progress.Assets
		switch value.Dimension {
		case domain.ConfluencePullIncludeComments:
			aggregate = &progress.Comments
		case domain.ConfluencePullIncludeAttachments:
			aggregate = &progress.Attachments
		}
		aggregate.Published++
		if value.BodyBytes > 0 {
			if aggregate.BodyBytes > (1<<63-1)-value.BodyBytes {
				return fmt.Errorf("%w: complete-pull attachment body accounting overflows", domain.ErrCheckFailed)
			}
			aggregate.BodyBytes += value.BodyBytes
		}
		if value.Qualification == domain.ConfluencePullIncludePartial {
			aggregate.Partial++
			aggregate.Reason = value.Reason
		}
	}
	return nil
}

// RecordPublished applies one page's already-published evidence to the mutable
// aggregate. Callers persist it together with the corresponding durable prefix.
func (progress *CompletePullIncludeProgress) RecordPublished(values []domain.ConfluencePullIncludeEvidence) error {
	return applyCompletePullIncludeEvidence(progress, values)
}

func confluencePullEntryEvidence(entry CompletePullJournalEntry) []domain.ConfluencePullIncludeEvidence {
	if entry.Includes == nil {
		return nil
	}
	return *entry.Includes
}
