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
	confluenceSectionSelectorCap  = 32
)

// ConfluenceStructuralSchemaVersion stamps both bounded structural page reads.
// The outline and the section are one selection protocol — an outline is read to
// pick a heading, and the section that follows must be attributable to the same
// page revision — so a single marker keeps a consumer from validating one shape
// against the other's contract.
const ConfluenceStructuralSchemaVersion = 1

// Partial outline/section reads name their limiter through this closed set of
// static identifiers. Each value is a compile-time literal that never
// interpolates a heading, page id, title, space, URL, body, backend text, or
// caller value, so a client can branch on the machine-readable cause without
// any page content crossing the boundary. Only confluencePartialMaxBytes is
// eligible for a one-shot recovery attempt: for an unchanged page and valid
// rendering, re-reading the same reference/heading/occurrence with
// max_bytes >= original_bytes returns the complete section.
const (
	confluencePartialHeadingLimit = "heading_limit"
	confluencePartialByteLimit    = "byte_limit"
	confluencePartialMaxBytes     = "max_bytes"
	confluencePartialInvalidUTF8  = "invalid_utf8"
)

// ConfluenceValidOutlinePartialReason and ConfluenceValidSectionPartialReason
// expose the two closed reason sets so a transport can fail closed on a reason
// it does not recognize without duplicating the literals and drifting from
// them. Each set is exactly the reasons its own read can emit: an outline is
// bounded by heading count or encoded bytes, a section by its caller-selected
// byte bound or the defensive invalid-rendering withhold.
func ConfluenceValidOutlinePartialReason(reason string) bool {
	return reason == confluencePartialHeadingLimit || reason == confluencePartialByteLimit
}

func ConfluenceValidSectionPartialReason(reason string) bool {
	return reason == confluencePartialMaxBytes || reason == confluencePartialInvalidUTF8
}

type ConfluenceOutlineEntry struct {
	Index      int      `json:"index"`
	Level      int      `json:"level"`
	Title      string   `json:"title"`
	Path       []string `json:"path"`
	Occurrence int      `json:"occurrence"`
}

type ConfluencePageOutlineResult struct {
	SchemaVersion int                      `json:"schema_version"`
	ID            string                   `json:"id"`
	Title         string                   `json:"title"`
	Space         string                   `json:"space"`
	Version       int                      `json:"version"`
	Count         int                      `json:"count"`
	Total         int                      `json:"total"`
	Complete      bool                     `json:"complete"`
	Truncated     bool                     `json:"truncated,omitempty"`
	PartialReason string                   `json:"partial_reason,omitempty"`
	OriginalBytes int                      `json:"original_bytes"`
	EmittedBytes  int                      `json:"emitted_bytes"`
	Headings      []ConfluenceOutlineEntry `json:"headings"`
}

type ConfluencePageSectionOpts struct {
	Heading    string
	Occurrence int
	MaxBytes   int
	// ExpectedPageVersion binds this section read to the page revision the
	// caller already observed — in practice the version an outline returned
	// just before the heading was chosen. Zero leaves the read ungated, which
	// is what the CLI and existing in-process callers rely on; a positive value
	// refuses the read when the page has moved, so a repeated-heading
	// occurrence or path can never be resolved against a body the caller never
	// saw. Negative is a caller mistake, not a disabled gate.
	ExpectedPageVersion int
}

// ConfluencePageSectionSelector identifies one heading in caller order.
// Occurrence is 1-based when set; zero retains the single-section ambiguity
// check and selects the only matching heading when it is unique.
type ConfluencePageSectionSelector struct {
	Heading    string `json:"heading"`
	Occurrence int    `json:"occurrence,omitempty"`
}

type ConfluencePageSectionsOpts struct {
	Selectors           []ConfluencePageSectionSelector
	MaxBytes            int
	ExpectedPageVersion int
}

// ConfluencePageSectionEntry contains only selection-local data. Page identity
// and revision are stated once by ConfluencePageSectionsResult so every entry
// is attributable to the same fetched body.
type ConfluencePageSectionEntry struct {
	Heading       string   `json:"heading"`
	Level         int      `json:"level"`
	Path          []string `json:"path"`
	Occurrence    int      `json:"occurrence"`
	Markdown      string   `json:"markdown"`
	Complete      bool     `json:"complete"`
	Truncated     bool     `json:"truncated,omitempty"`
	PartialReason string   `json:"partial_reason,omitempty"`
	OriginalBytes int      `json:"original_bytes"`
	EmittedBytes  int      `json:"emitted_bytes"`
}

type ConfluencePageSectionsResult struct {
	SchemaVersion    int                          `json:"schema_version"`
	ID               string                       `json:"id"`
	PageTitle        string                       `json:"page_title"`
	Space            string                       `json:"space"`
	Version          int                          `json:"version"`
	PageVersionGated bool                         `json:"page_version_gated"`
	RequestedCount   int                          `json:"requested_count"`
	ReturnedCount    int                          `json:"returned_count"`
	Reconciled       bool                         `json:"reconciled"`
	Complete         bool                         `json:"complete"`
	Truncated        bool                         `json:"truncated,omitempty"`
	OriginalBytes    int                          `json:"original_bytes"`
	EmittedBytes     int                          `json:"emitted_bytes"`
	MaxBytes         int                          `json:"max_bytes"`
	Sections         []ConfluencePageSectionEntry `json:"sections"`
}

type ConfluencePageSectionResult struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	PageTitle     string `json:"page_title"`
	Space         string `json:"space"`
	Version       int    `json:"version"`
	// PageVersionGated is true only when a positive expected version was
	// supplied and matched the version this body was read at. It is the
	// difference between "the page happened to be at version N" and "this
	// selection was bound to version N", which is what a consumer needs to know
	// before attributing an occurrence to a revision.
	PageVersionGated bool     `json:"page_version_gated"`
	Heading          string   `json:"heading"`
	Level            int      `json:"level"`
	Path             []string `json:"path"`
	Occurrence       int      `json:"occurrence"`
	Markdown         string   `json:"markdown"`
	Complete         bool     `json:"complete"`
	Truncated        bool     `json:"truncated,omitempty"`
	PartialReason    string   `json:"partial_reason,omitempty"`
	OriginalBytes    int      `json:"original_bytes"`
	EmittedBytes     int      `json:"emitted_bytes"`
}

// ConfluenceSectionSelectionError reports a recoverable page-section selection
// mistake. Requested == 0 means the heading repeats and no occurrence was
// supplied; Requested > 0 means the requested 1-based occurrence exceeds the
// available matches. It deliberately carries only integer counts — never a
// heading, page id, title, space, URL, body, or backend text — so a transport
// can distinguish a recoverable caller-side selection mistake from an
// unavailable source without disclosing page content. It unwraps to
// domain.ErrCheckFailed when ambiguous and domain.ErrNotFound when out of
// range, so sentinel-driven exit codes and classification stay unchanged.
type ConfluenceSectionSelectionError struct {
	Requested int
	Available int
}

func (e *ConfluenceSectionSelectionError) Error() string { return e.Unwrap().Error() }

func (e *ConfluenceSectionSelectionError) Unwrap() error {
	if e.Requested == 0 {
		return domain.ErrCheckFailed
	}
	return domain.ErrNotFound
}

func (e *ConfluenceSectionSelectionError) DiagnosticSelection() (requested, available, matches int) {
	if e == nil {
		return 0, 0, 0
	}
	matches = 0
	if e.Requested == 0 {
		matches = e.Available
	}
	return e.Requested, e.Available, matches
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
	parsed, err := s.loadStructuralConfluencePage(ctx, reference, 0)
	if err != nil {
		return nil, err
	}
	headings := make([]ConfluenceOutlineEntry, 0, min(len(parsed.headings), confluenceOutlineHeadingCap))
	originalBytes, emittedBytes := 0, 0
	// partialReason records the first limiter that stopped emission and keeps
	// counting the remaining headings for the original-byte total.
	partialReason := ""
	for _, heading := range parsed.headings {
		encoded, marshalErr := json.Marshal(heading.ConfluenceOutlineEntry)
		if marshalErr != nil {
			return nil, marshalErr
		}
		size := len(encoded) + 1
		originalBytes += size
		if partialReason != "" {
			continue
		}
		if len(headings) >= confluenceOutlineHeadingCap {
			partialReason = confluencePartialHeadingLimit
			continue
		}
		if emittedBytes+size > confluenceOutlineByteCap {
			partialReason = confluencePartialByteLimit
			continue
		}
		headings = append(headings, heading.ConfluenceOutlineEntry)
		emittedBytes += size
	}
	return &ConfluencePageOutlineResult{
		SchemaVersion: ConfluenceStructuralSchemaVersion,
		ID:            parsed.page.ID, Title: parsed.page.Title, Space: parsed.page.SpaceKey, Version: parsed.page.Version,
		Count: len(headings), Total: len(parsed.headings),
		Complete: partialReason == "", Truncated: partialReason != "", PartialReason: partialReason,
		OriginalBytes: originalBytes, EmittedBytes: emittedBytes, Headings: headings,
	}, nil
}

func (s *ConfluenceService) PageSection(ctx context.Context, reference string, opts ConfluencePageSectionOpts) (*ConfluencePageSectionResult, error) {
	maxBytes := opts.MaxBytes
	if maxBytes == 0 {
		maxBytes = confluenceSectionDefaultBytes
	}
	result, err := s.PageSections(ctx, reference, ConfluencePageSectionsOpts{
		Selectors: []ConfluencePageSectionSelector{{Heading: opts.Heading, Occurrence: opts.Occurrence}},
		MaxBytes:  maxBytes, ExpectedPageVersion: opts.ExpectedPageVersion,
	})
	if err != nil {
		return nil, err
	}
	section := result.Sections[0]
	return &ConfluencePageSectionResult{
		SchemaVersion: result.SchemaVersion,
		ID:            result.ID, PageTitle: result.PageTitle, Space: result.Space, Version: result.Version,
		PageVersionGated: result.PageVersionGated,
		Heading:          section.Heading, Level: section.Level, Path: section.Path, Occurrence: section.Occurrence,
		Markdown: section.Markdown,
		Complete: section.Complete, Truncated: section.Truncated, PartialReason: section.PartialReason,
		OriginalBytes: section.OriginalBytes, EmittedBytes: section.EmittedBytes,
	}, nil
}

type confluenceSectionSelection struct {
	heading structuralHeading
	blocks  []mirror.Block
}

// PageSections returns ordered sections from one reconciled page body. All
// selectors are resolved before any output is assembled, so one invalid
// selector fails the request as a unit.
func (s *ConfluenceService) PageSections(ctx context.Context, reference string, opts ConfluencePageSectionsOpts) (*ConfluencePageSectionsResult, error) {
	if len(opts.Selectors) == 0 {
		return nil, fmt.Errorf("%w: at least one heading selector is required", domain.ErrUsage)
	}
	if len(opts.Selectors) > confluenceSectionSelectorCap {
		return nil, fmt.Errorf("%w: no more than %d heading selectors are allowed", domain.ErrUsage, confluenceSectionSelectorCap)
	}
	for _, selector := range opts.Selectors {
		if normalizeHeadingSelector(selector.Heading) == "" {
			return nil, fmt.Errorf("%w: --heading is required", domain.ErrUsage)
		}
		if selector.Occurrence < 0 {
			return nil, fmt.Errorf("%w: --occurrence must be >= 1 when set", domain.ErrUsage)
		}
	}
	if opts.ExpectedPageVersion < 0 {
		return nil, fmt.Errorf("%w: --expected-version must be >= 1 when set", domain.ErrUsage)
	}
	if opts.MaxBytes < 1 || opts.MaxBytes > confluenceSectionMaxBytes {
		return nil, fmt.Errorf("%w: --max-bytes must be between 1 and %d", domain.ErrUsage, confluenceSectionMaxBytes)
	}

	parsed, err := s.loadStructuralConfluencePage(ctx, reference, opts.ExpectedPageVersion)
	if err != nil {
		return nil, err
	}
	selections := make([]confluenceSectionSelection, 0, len(opts.Selectors))
	for _, selector := range opts.Selectors {
		selection, selectionErr := selectConfluenceSection(parsed, selector)
		if selectionErr != nil {
			return nil, selectionErr
		}
		selections = append(selections, selection)
	}

	sections := make([]ConfluencePageSectionEntry, 0, len(selections))
	remainingBytes := opts.MaxBytes
	originalBytes, emittedBytes := 0, 0
	allComplete, anyTruncated := true, false
	for index, selection := range selections {
		remainingSelectors := len(selections) - index
		sectionMaxBytes := (remainingBytes + remainingSelectors - 1) / remainingSelectors
		originalBytesForSection := confluenceSectionMarkdownBytes(selection.blocks)
		bounded := boundedConfluenceSectionMarkdown(selection.blocks, sectionMaxBytes)
		entry := ConfluencePageSectionEntry{
			Heading: selection.heading.Title, Level: selection.heading.Level,
			Path: selection.heading.Path, Occurrence: selection.heading.Occurrence,
			Markdown:      bounded.markdown,
			PartialReason: bounded.partialReason,
			OriginalBytes: originalBytesForSection, EmittedBytes: len(bounded.markdown),
		}
		entry.Complete = entry.OriginalBytes == entry.EmittedBytes
		entry.Truncated = !entry.Complete
		sections = append(sections, entry)
		remainingBytes -= entry.EmittedBytes
		originalBytes += entry.OriginalBytes
		emittedBytes += entry.EmittedBytes
		allComplete = allComplete && entry.Complete
		anyTruncated = anyTruncated || entry.Truncated
	}
	returnedCount := len(sections)
	reconciled := returnedCount == len(opts.Selectors)
	return &ConfluencePageSectionsResult{
		SchemaVersion: ConfluenceStructuralSchemaVersion,
		ID:            parsed.page.ID, PageTitle: parsed.page.Title, Space: parsed.page.SpaceKey, Version: parsed.page.Version,
		PageVersionGated: opts.ExpectedPageVersion > 0,
		RequestedCount:   len(opts.Selectors), ReturnedCount: returnedCount, Reconciled: reconciled,
		Complete: reconciled && allComplete, Truncated: anyTruncated,
		OriginalBytes: originalBytes, EmittedBytes: emittedBytes, MaxBytes: opts.MaxBytes,
		Sections: sections,
	}, nil
}

func selectConfluenceSection(parsed *confluenceStructuralPage, selector ConfluencePageSectionSelector) (confluenceSectionSelection, error) {
	headingSelector := normalizeHeadingSelector(selector.Heading)
	var matches []int
	for i, heading := range parsed.headings {
		if heading.normalized == headingSelector {
			matches = append(matches, i)
		}
	}
	if len(matches) == 0 {
		return confluenceSectionSelection{}, fmt.Errorf("%w: Confluence heading %q was not found", domain.ErrNotFound, strings.TrimSpace(selector.Heading))
	}
	// The typed selection error carries the machine-readable counts; the
	// wrapping prose keeps the existing human-facing message byte for byte.
	if selector.Occurrence == 0 && len(matches) > 1 {
		return confluenceSectionSelection{}, fmt.Errorf("%w: Confluence heading %q occurs %d times; pass --occurrence 1..%d", &ConfluenceSectionSelectionError{Available: len(matches)}, strings.TrimSpace(selector.Heading), len(matches), len(matches))
	}
	occurrence := selector.Occurrence
	if occurrence == 0 {
		occurrence = 1
	}
	if occurrence > len(matches) {
		return confluenceSectionSelection{}, fmt.Errorf("%w: Confluence heading %q has %d occurrence(s), not %d", &ConfluenceSectionSelectionError{Requested: occurrence, Available: len(matches)}, strings.TrimSpace(selector.Heading), len(matches), occurrence)
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
	selected.Occurrence = occurrence
	return confluenceSectionSelection{heading: selected, blocks: parsed.blocks[selected.blockIndex:endBlock]}, nil
}

func (s *ConfluenceService) loadStructuralConfluencePage(ctx context.Context, reference string, expectedVersion int) (*confluenceStructuralPage, error) {
	resolved, err := s.ResolvePageReference(ctx, reference)
	if err != nil {
		return nil, err
	}
	page, err := s.store.GetPage(ctx, resolved.ID, domain.PullOpts{Format: "csf"})
	if err != nil {
		return nil, err
	}
	// Validate provenance before parsing or rendering any body. Both outline
	// and section results claim a concrete page identity and revision, so
	// neither may proceed from an unattributable response. A positive expected
	// version is checked against this same response, before heading selection,
	// and therefore adds no backend request.
	if page == nil || strings.TrimSpace(page.ID) == "" || page.ID != resolved.ID || page.Version < 1 {
		return nil, fmt.Errorf("%w: Confluence page %s identity is not reconciled for outline/section", domain.ErrCheckFailed, resolved.ID)
	}
	if expectedVersion > 0 && expectedVersion != page.Version {
		return nil, &ConfluencePageVersionMismatchError{Expected: expectedVersion, Current: page.Version}
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

func confluenceSectionMarkdownBytes(blocks []mirror.Block) int {
	size, parts := 0, 0
	for _, block := range blocks {
		if text := strings.TrimSpace(block.MD); text != "" {
			if parts > 0 {
				size += 2
			}
			size += len(text)
			parts++
		}
	}
	if parts > 0 {
		size++
	}
	return size
}

// confluenceSectionBody is the internal result of one bounding pass. Carrying
// the reason out of the pass that produced it keeps the emitted limiter exact
// instead of inferring it from the finished bytes afterwards.
type confluenceSectionBody struct {
	markdown string
	// partialReason is empty exactly when the whole section fit the bound.
	partialReason string
}

func boundedConfluenceSectionMarkdown(blocks []mirror.Block, maxBytes int) confluenceSectionBody {
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
		// Defensive: the rendering is withheld in full rather than emitted, so
		// this partial read is terminal and must never be reported as a
		// recoverable byte bound.
		return confluenceSectionBody{partialReason: confluencePartialInvalidUTF8}
	}
	if truncated {
		return confluenceSectionBody{markdown: result, partialReason: confluencePartialMaxBytes}
	}
	return confluenceSectionBody{markdown: result}
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
