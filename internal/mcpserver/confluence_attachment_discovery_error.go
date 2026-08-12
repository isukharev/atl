package mcpserver

import "github.com/isukharev/atl/internal/diagnostic"

// Attachment discovery has its own fully static failure policy. Search errors
// may contain CQL fragments, filenames, titles, backend URLs, or response text;
// none may cross the MCP boundary, including alongside a structured failed DTO.
var confluenceAttachmentDiscoveryReadPolicy = toolErrorPolicy{
	operation: diagnostic.OperationConfluenceAttachmentRead,
	fallback:  staticMessage("Confluence attachment discovery failed"),
	kinds: map[toolErrorKind]toolErrorRule{
		"usage_error":           staticMessage("invalid Confluence attachment discovery request"),
		"configuration_error":   staticMessage("Confluence attachment discovery is not configured"),
		"authentication_failed": staticMessage("Confluence attachment discovery authentication failed"),
		"forbidden":             staticMessage("Confluence attachment discovery access is forbidden"),
		"not_found":             staticMessage("Confluence attachment discovery scope was not found"),
		"check_failed":          staticMessage("Confluence attachment discovery failed validation"),
		"output_limit_exceeded": staticMessageWithRemediation("Confluence attachment discovery exceeds the selected output bound", "narrow_selection_or_raise_bound"),
		"rate_limited":          staticMessage("Confluence attachment discovery rate limit was exhausted"),
		"api_error":             staticMessage("Confluence attachment discovery API request failed"),
		"transport_error":       staticMessage("Confluence attachment discovery transport failed"),
	},
}

func classifiedConfluenceAttachmentDiscoveryRead(err error) error {
	return confluenceAttachmentDiscoveryReadPolicy.classify(err)
}
