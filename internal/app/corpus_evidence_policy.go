package app

import (
	"fmt"
	"mime"
	"sort"
	"strings"
	"sync"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
)

const (
	corpusBuildMaxCommentPagesPerItem    = 100
	corpusBuildMaxCommentsPerItem        = 10_000
	corpusBuildMaxAttachmentPagesPerItem = 100
	corpusBuildMaxAttachmentsPerItem     = 10_000
	corpusBuildMaxAttachmentMediaTypes   = 64
	corpusBuildMaxAttachmentBytes        = int64(64 << 20)
	corpusBuildMaxAttachmentTotalBytes   = int64(256 << 20)
)

type corpusEvidenceBinding struct {
	Comments                  bool     `json:"comments"`
	MaxCommentPagesPerItem    int      `json:"max_comment_pages_per_item,omitempty"`
	MaxCommentsPerItem        int      `json:"max_comments_per_item,omitempty"`
	Attachments               bool     `json:"attachments"`
	MaxAttachmentPagesPerItem int      `json:"max_attachment_pages_per_item,omitempty"`
	MaxAttachmentsPerItem     int      `json:"max_attachments_per_item,omitempty"`
	AttachmentBodies          bool     `json:"attachment_bodies"`
	AttachmentMediaTypes      []string `json:"attachment_media_types"`
	// MaxAttachmentBodiesPerItem is an optional internal publication bound.
	// Corpus builds leave it at zero; public complete pulls set it so one page
	// cannot consume the complete-pull publisher's finite artifact budget.
	MaxAttachmentBodiesPerItem int   `json:"max_attachment_bodies_per_item,omitempty"`
	MaxAttachmentBytes         int64 `json:"max_attachment_bytes,omitempty"`
	MaxTotalAttachmentBytes    int64 `json:"max_total_attachment_bytes,omitempty"`
	AllowPartialEvidence       bool  `json:"allow_partial_evidence"`
}

type corpusAttachmentCaptureBudget struct {
	mu       sync.Mutex
	maximum  int64
	reserved int64
}

func (budget *corpusAttachmentCaptureBudget) reserve(size int64) bool {
	if budget == nil || size < 0 {
		return false
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if size > budget.maximum-budget.reserved {
		return false
	}
	budget.reserved += size
	return true
}

func (budget *corpusAttachmentCaptureBudget) release(size int64) {
	if budget == nil || size <= 0 {
		return
	}
	budget.mu.Lock()
	budget.reserved -= size
	if budget.reserved < 0 {
		budget.reserved = 0
	}
	budget.mu.Unlock()
}

func (budget *corpusAttachmentCaptureBudget) usage() int64 {
	if budget == nil {
		return 0
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.reserved
}

// remaining reports the atomically observed portion of the aggregate body
// budget that a new page may reserve. Capture plans against this value before
// it opens a body: strict complete pulls must not transfer a prefix which the
// restored aggregate cannot retain.
func (budget *corpusAttachmentCaptureBudget) remaining() int64 {
	if budget == nil {
		return 0
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.maximum - budget.reserved
}

// restoreVerifiedUsage incorporates a durable per-service prefix into a
// capture budget without discarding evidence already charged by another
// service in the same corpus generation. A direct complete-pull resume starts
// at zero and adopts its verified prefix. A corpus build shares one budget
// across Confluence and Jira, so a later service whose own prefix is smaller
// must never reset the aggregate back to zero.
func (budget *corpusAttachmentCaptureBudget) restoreVerifiedUsage(usage int64) bool {
	if budget == nil || usage < 0 {
		return false
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if usage > budget.maximum {
		return false
	}
	if budget.reserved == 0 {
		budget.reserved = usage
		return true
	}
	return budget.reserved >= usage
}

type corpusPullEvidenceOptions struct {
	binding corpusEvidenceBinding
	budget  *corpusAttachmentCaptureBudget
}

func newCorpusPullEvidenceOptions(options CorpusBuildOptions) *corpusPullEvidenceOptions {
	evidence, _ := newCorpusPullEvidenceOptionsWithUsage(options, 0)
	return evidence
}

func newCorpusPullEvidenceOptionsWithUsage(options CorpusBuildOptions, attachmentBodyBytes int64) (*corpusPullEvidenceOptions, error) {
	binding := corpusEvidenceBindingFromOptions(options)
	if !binding.Comments && !binding.Attachments {
		if attachmentBodyBytes != 0 {
			return nil, corpus.ErrIntegrity
		}
		return nil, nil
	}
	var budget *corpusAttachmentCaptureBudget
	if binding.AttachmentBodies {
		if attachmentBodyBytes < 0 || attachmentBodyBytes > binding.MaxTotalAttachmentBytes {
			return nil, corpus.ErrIntegrity
		}
		budget = &corpusAttachmentCaptureBudget{maximum: binding.MaxTotalAttachmentBytes, reserved: attachmentBodyBytes}
	} else if attachmentBodyBytes != 0 {
		return nil, corpus.ErrIntegrity
	}
	return &corpusPullEvidenceOptions{binding: binding, budget: budget}, nil
}

func corpusEvidenceBindingFromOptions(options CorpusBuildOptions) corpusEvidenceBinding {
	mediaTypes := append([]string{}, options.AttachmentMediaTypes...)
	sort.Strings(mediaTypes)
	return corpusEvidenceBinding{
		Comments: options.Comments, MaxCommentPagesPerItem: options.MaxCommentPagesPerItem,
		MaxCommentsPerItem: options.MaxCommentsPerItem,
		Attachments:        options.Attachments, MaxAttachmentPagesPerItem: options.MaxAttachmentPagesPerItem,
		MaxAttachmentsPerItem: options.MaxAttachmentsPerItem,
		AttachmentBodies:      options.AttachmentBodies, AttachmentMediaTypes: mediaTypes,
		MaxAttachmentBytes: options.MaxAttachmentBytes, MaxTotalAttachmentBytes: options.MaxTotalAttachmentBytes,
		AllowPartialEvidence: options.AllowPartialEvidence,
	}
}

func validateCorpusEvidenceOptions(options CorpusBuildOptions) error {
	if options.Comments {
		if options.MaxCommentPagesPerItem <= 0 || options.MaxCommentPagesPerItem > corpusBuildMaxCommentPagesPerItem ||
			options.MaxCommentsPerItem <= 0 || options.MaxCommentsPerItem > corpusBuildMaxCommentsPerItem {
			return fmt.Errorf("%w: requested comments require valid per-item page and count bounds", domain.ErrUsage)
		}
	} else if options.MaxCommentPagesPerItem != 0 || options.MaxCommentsPerItem != 0 {
		return fmt.Errorf("%w: comment bounds require --comments", domain.ErrUsage)
	}
	if options.Attachments {
		if options.MaxAttachmentPagesPerItem <= 0 || options.MaxAttachmentPagesPerItem > corpusBuildMaxAttachmentPagesPerItem ||
			options.MaxAttachmentsPerItem <= 0 || options.MaxAttachmentsPerItem > corpusBuildMaxAttachmentsPerItem {
			return fmt.Errorf("%w: requested attachments require valid per-item page and count bounds", domain.ErrUsage)
		}
	} else if options.MaxAttachmentPagesPerItem != 0 || options.MaxAttachmentsPerItem != 0 {
		return fmt.Errorf("%w: attachment bounds require --attachments", domain.ErrUsage)
	}
	if options.AttachmentBodies {
		if !options.Attachments || len(options.AttachmentMediaTypes) == 0 || len(options.AttachmentMediaTypes) > corpusBuildMaxAttachmentMediaTypes ||
			options.MaxAttachmentBytes <= 0 || options.MaxAttachmentBytes > corpusBuildMaxAttachmentBytes ||
			options.MaxTotalAttachmentBytes < options.MaxAttachmentBytes || options.MaxTotalAttachmentBytes > corpusBuildMaxAttachmentTotalBytes {
			return fmt.Errorf("%w: attachment bodies require inventory, an explicit MIME allowlist, and valid byte bounds", domain.ErrUsage)
		}
		seen := make(map[string]struct{}, len(options.AttachmentMediaTypes))
		for _, value := range options.AttachmentMediaTypes {
			mediaType, parameters, err := mime.ParseMediaType(value)
			if err != nil || len(parameters) != 0 || value != strings.ToLower(strings.TrimSpace(value)) || mediaType != value || strings.Contains(value, "*") {
				return fmt.Errorf("%w: attachment MIME allowlist contains a non-canonical exact media type", domain.ErrUsage)
			}
			if _, duplicate := seen[value]; duplicate {
				return fmt.Errorf("%w: attachment MIME allowlist contains a duplicate", domain.ErrUsage)
			}
			seen[value] = struct{}{}
		}
	} else if len(options.AttachmentMediaTypes) != 0 || options.MaxAttachmentBytes != 0 || options.MaxTotalAttachmentBytes != 0 {
		return fmt.Errorf("%w: attachment body policy requires --attachment-bodies", domain.ErrUsage)
	}
	if options.AllowPartialEvidence && !options.Comments && !options.Attachments {
		return fmt.Errorf("%w: partial evidence policy requires a requested evidence dimension", domain.ErrUsage)
	}
	return nil
}

func corpusAttachmentMediaAllowed(allowlist []string, reported string) bool {
	mediaType, _, err := mime.ParseMediaType(reported)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	index := sort.SearchStrings(allowlist, mediaType)
	return index < len(allowlist) && allowlist[index] == mediaType
}
