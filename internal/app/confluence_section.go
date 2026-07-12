package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/fragment"
	"github.com/isukharev/atl/internal/mirror"
)

const (
	confluenceOutlineHeadingCap   = 1000
	confluenceOutlineByteCap      = 256 << 10
	confluenceSectionDefaultBytes = 256 << 10
	confluenceSectionMaxBytes     = 1 << 20
)

type ConfluenceOutlineEntry struct {
	Index      int      `json:"index"`
	Level      int      `json:"level"`
	Title      string   `json:"title"`
	Path       []string `json:"path"`
	Occurrence int      `json:"occurrence"`
}

type ConfluencePageOutlineResult struct {
	ID            string                   `json:"id"`
	Title         string                   `json:"title"`
	Space         string                   `json:"space"`
	Version       int                      `json:"version"`
	Count         int                      `json:"count"`
	Total         int                      `json:"total"`
	Complete      bool                     `json:"complete"`
	Truncated     bool                     `json:"truncated,omitempty"`
	OriginalBytes int                      `json:"original_bytes"`
	EmittedBytes  int                      `json:"emitted_bytes"`
	Headings      []ConfluenceOutlineEntry `json:"headings"`
}

type ConfluencePageSectionOpts struct {
	Heading    string
	Occurrence int
	MaxBytes   int
}

type ConfluencePageSectionResult struct {
	ID            string   `json:"id"`
	PageTitle     string   `json:"page_title"`
	Space         string   `json:"space"`
	Version       int      `json:"version"`
	Heading       string   `json:"heading"`
	Level         int      `json:"level"`
	Path          []string `json:"path"`
	Occurrence    int      `json:"occurrence"`
	Markdown      string   `json:"markdown"`
	Complete      bool     `json:"complete"`
	Truncated     bool     `json:"truncated,omitempty"`
	OriginalBytes int      `json:"original_bytes"`
	EmittedBytes  int      `json:"emitted_bytes"`
}

type confluenceStructuralPage struct {
	page     *domain.Resource
	blocks   []mirror.Block
	headings []structuralHeading
}

type structuralHeading struct {
	ConfluenceOutlineEntry
	blockIndex int
	normalized string
}

func (s *ConfluenceService) PageOutline(ctx context.Context, reference string) (*ConfluencePageOutlineResult, error) {
	parsed, err := s.loadStructuralConfluencePage(ctx, reference)
	if err != nil {
		return nil, err
	}
	headings := make([]ConfluenceOutlineEntry, 0, min(len(parsed.headings), confluenceOutlineHeadingCap))
	originalBytes, emittedBytes := 0, 0
	truncated := false
	for _, heading := range parsed.headings {
		encoded, marshalErr := json.Marshal(heading.ConfluenceOutlineEntry)
		if marshalErr != nil {
			return nil, marshalErr
		}
		size := len(encoded) + 1
		originalBytes += size
		if truncated {
			continue
		}
		if len(headings) >= confluenceOutlineHeadingCap || emittedBytes+size > confluenceOutlineByteCap {
			truncated = true
			continue
		}
		headings = append(headings, heading.ConfluenceOutlineEntry)
		emittedBytes += size
	}
	return &ConfluencePageOutlineResult{
		ID: parsed.page.ID, Title: parsed.page.Title, Space: parsed.page.SpaceKey, Version: parsed.page.Version,
		Count: len(headings), Total: len(parsed.headings), Complete: !truncated, Truncated: truncated,
		OriginalBytes: originalBytes, EmittedBytes: emittedBytes, Headings: headings,
	}, nil
}

func (s *ConfluenceService) PageSection(ctx context.Context, reference string, opts ConfluencePageSectionOpts) (*ConfluencePageSectionResult, error) {
	headingSelector := normalizeHeadingSelector(opts.Heading)
	if headingSelector == "" {
		return nil, fmt.Errorf("%w: --heading is required", domain.ErrUsage)
	}
	if opts.Occurrence < 0 {
		return nil, fmt.Errorf("%w: --occurrence must be >= 1 when set", domain.ErrUsage)
	}
	maxBytes := opts.MaxBytes
	if maxBytes == 0 {
		maxBytes = confluenceSectionDefaultBytes
	}
	if maxBytes < 1 || maxBytes > confluenceSectionMaxBytes {
		return nil, fmt.Errorf("%w: --max-bytes must be between 1 and %d", domain.ErrUsage, confluenceSectionMaxBytes)
	}
	parsed, err := s.loadStructuralConfluencePage(ctx, reference)
	if err != nil {
		return nil, err
	}
	var matches []int
	for i, heading := range parsed.headings {
		if heading.normalized == headingSelector {
			matches = append(matches, i)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: Confluence heading %q was not found", domain.ErrNotFound, strings.TrimSpace(opts.Heading))
	}
	if opts.Occurrence == 0 && len(matches) > 1 {
		return nil, fmt.Errorf("%w: Confluence heading %q occurs %d times; pass --occurrence 1..%d", domain.ErrCheckFailed, strings.TrimSpace(opts.Heading), len(matches), len(matches))
	}
	occurrence := opts.Occurrence
	if occurrence == 0 {
		occurrence = 1
	}
	if occurrence > len(matches) {
		return nil, fmt.Errorf("%w: Confluence heading %q has %d occurrence(s), not %d", domain.ErrNotFound, strings.TrimSpace(opts.Heading), len(matches), occurrence)
	}
	selectedHeadingIndex := matches[occurrence-1]
	selected := parsed.headings[selectedHeadingIndex]
	endBlock := len(parsed.blocks)
	for _, candidate := range parsed.headings[selectedHeadingIndex+1:] {
		if candidate.Level <= selected.Level {
			endBlock = candidate.blockIndex
			break
		}
	}
	selectedBlocks := parsed.blocks[selected.blockIndex:endBlock]
	originalMarkdown := joinConfluenceSectionBlocks(selectedBlocks)
	markdown, truncated := boundedConfluenceSectionMarkdown(selectedBlocks, maxBytes)
	return &ConfluencePageSectionResult{
		ID: parsed.page.ID, PageTitle: parsed.page.Title, Space: parsed.page.SpaceKey, Version: parsed.page.Version,
		Heading: selected.Title, Level: selected.Level, Path: selected.Path, Occurrence: occurrence,
		Markdown: markdown, Complete: !truncated, Truncated: truncated,
		OriginalBytes: len(originalMarkdown), EmittedBytes: len(markdown),
	}, nil
}

func (s *ConfluenceService) loadStructuralConfluencePage(ctx context.Context, reference string) (*confluenceStructuralPage, error) {
	resolved, err := s.ResolvePageReference(ctx, reference)
	if err != nil {
		return nil, err
	}
	page, err := s.store.GetPage(ctx, resolved.ID, domain.PullOpts{Format: "csf"})
	if err != nil {
		return nil, err
	}
	if err := requireConfluenceNativeBody(page, resolved.ID, "outline/section"); err != nil {
		return nil, err
	}
	root, err := csf.Parse(page.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: page %s CSF cannot be inspected structurally: %v", domain.ErrCheckFailed, resolved.ID, err)
	}
	refs := fragment.Extract(root)
	blocks, nodes := mirror.RenderBlockNodes(root, refs)
	parsed := &confluenceStructuralPage{page: page, blocks: blocks}
	ancestry := []ConfluenceOutlineEntry{}
	occurrences := map[string]int{}
	for blockIndex, node := range nodes {
		level, ok := confluenceHeadingLevel(node)
		if !ok {
			continue
		}
		title := strings.Join(strings.Fields(csf.TextContent(node)), " ")
		if title == "" {
			title = "(untitled)"
		}
		for len(ancestry) > 0 && ancestry[len(ancestry)-1].Level >= level {
			ancestry = ancestry[:len(ancestry)-1]
		}
		path := make([]string, 0, len(ancestry)+1)
		for _, parent := range ancestry {
			path = append(path, parent.Title)
		}
		path = append(path, title)
		normalized := normalizeHeadingSelector(title)
		occurrences[normalized]++
		entry := ConfluenceOutlineEntry{Index: len(parsed.headings) + 1, Level: level, Title: title, Path: path, Occurrence: occurrences[normalized]}
		parsed.headings = append(parsed.headings, structuralHeading{ConfluenceOutlineEntry: entry, blockIndex: blockIndex, normalized: normalized})
		ancestry = append(ancestry, entry)
	}
	return parsed, nil
}

func confluenceHeadingLevel(node *csf.Node) (int, bool) {
	if node == nil || node.Type != csf.Element || node.Name.Space != "" || len(node.Name.Local) != 2 || node.Name.Local[0] != 'h' || node.Name.Local[1] < '1' || node.Name.Local[1] > '6' {
		return 0, false
	}
	return int(node.Name.Local[1] - '0'), true
}

func normalizeHeadingSelector(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func joinConfluenceSectionBlocks(blocks []mirror.Block) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if text := strings.TrimSpace(block.MD); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n"
}

func boundedConfluenceSectionMarkdown(blocks []mirror.Block, maxBytes int) (string, bool) {
	parts := make([]string, 0, len(blocks))
	size := 0
	truncated := false
	for _, block := range blocks {
		text := strings.TrimSpace(block.MD)
		if text == "" {
			continue
		}
		addition := len(text) + 1
		if len(parts) > 0 {
			addition++
		}
		if size+addition > maxBytes {
			truncated = true
			break
		}
		parts = append(parts, text)
		size += addition
	}
	result := ""
	if len(parts) > 0 {
		result = strings.Join(parts, "\n\n") + "\n"
	}
	if truncated {
		marker := "\n[... truncated by atl ...]\n"
		for len(result)+len(marker) > maxBytes && len(parts) > 0 {
			parts = parts[:len(parts)-1]
			result = ""
			if len(parts) > 0 {
				result = strings.Join(parts, "\n\n") + "\n"
			}
		}
		if len(marker) <= maxBytes {
			result += marker
		}
	}
	if !utf8.ValidString(result) {
		return "", true
	}
	return result, truncated
}

func ConfluenceOutlineMarkdown(result *ConfluencePageOutlineResult) string {
	if result == nil {
		return ""
	}
	var out strings.Builder
	for _, heading := range result.Headings {
		indent := strings.Repeat("  ", max(0, heading.Level-1))
		fmt.Fprintf(&out, "%s- %s (h%d, occurrence %d)\n", indent, heading.Title, heading.Level, heading.Occurrence)
	}
	return strings.TrimRight(out.String(), "\n")
}
