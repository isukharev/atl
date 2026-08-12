package app

import "github.com/isukharev/atl/internal/domain"

func forbiddenConfluenceCommentInventory(page *domain.Resource, opts ConfluenceCommentInventoryOpts) *ConfluenceCommentInventoryResult {
	unknown := domain.ConfluenceCapabilityUnknown
	return buildConfluenceCommentInventoryResult(page, domain.ConfluenceCommentInventory{
		Comments: []domain.ConfluenceCommentRecord{}, CommentsComplete: false, ThreadsComplete: false,
		PartialReasons: []string{domain.ConfluenceCommentPartialForbidden},
		Capabilities: domain.ConfluenceCommentCapabilities{
			Footer: unknown, Inline: unknown, Resolved: unknown, DepthAll: unknown,
			ThreadAncestry: unknown, InlineProperties: unknown, Resolution: unknown,
		},
		Diagnostics: []domain.ConfluenceCommentDiagnostic{{Code: domain.ConfluenceCommentPartialForbidden}},
	}, opts)
}
