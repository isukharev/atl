package wikimerge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/wikimd"
	"github.com/isukharev/atl/internal/wikiscanner"
)

// SemanticBlock is a content-free fingerprint of one native Jira wiki block.
// The hash covers the normalized read semantics, never the source text.
type SemanticBlock struct {
	Kind   string
	SHA256 string
}

// SemanticBlocks exposes the exact block scanner used by Jira apply for a
// bounded, classification-only three-way comparison.
func SemanticBlocks(body []byte, max int) ([]SemanticBlock, error) {
	source := string(body)
	blocks := scanWikiBlocks(source)
	if max < 0 || len(blocks) > max {
		return nil, fmt.Errorf("wiki body has %d blocks, exceeds limit %d", len(blocks), max)
	}
	out := make([]SemanticBlock, 0, len(blocks))
	for _, block := range blocks {
		raw := source[block.start:block.end]
		rendered := wikimd.Render(raw, wikimd.Options{})
		sum := sha256.Sum256([]byte(rendered))
		out = append(out, SemanticBlock{Kind: reconcileWikiBlockKind(raw), SHA256: hex.EncodeToString(sum[:])})
	}
	return out, nil
}

func reconcileWikiBlockKind(raw string) string {
	line := raw
	if at := strings.IndexAny(line, "\r\n"); at >= 0 {
		line = line[:at]
	}
	switch {
	case wikiscanner.IsHeading(line):
		return "heading"
	case wikiscanner.IsCodeOpen(line):
		return "code"
	case wikiscanner.IsQuoteOpen(line):
		return "quote"
	case wikiscanner.IsPanelOpen(line):
		return "panel"
	case strings.HasPrefix(line, "|"):
		return "table"
	case wikiscanner.IsListLine(line):
		return "list"
	case wikiscanner.IsHorizontalRule(line):
		return "rule"
	default:
		return "paragraph"
	}
}
