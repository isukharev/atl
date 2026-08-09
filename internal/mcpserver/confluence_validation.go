package mcpserver

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

const (
	confluenceSearchDefaultMaxBytes        = 128 << 10
	confluenceSearchMinMaxBytes            = 1 << 10
	confluenceSearchMaxMaxBytes            = 1 << 20
	confluencePageMetadataMaxBytes         = 32 << 10
	confluencePageSectionsDefaultMaxBytes  = 256 << 10
	confluencePageSectionsMaxMaxBytes      = 1 << 20
	confluencePageSectionsMaxSelectors     = 32
	confluencePageSectionsResultOverhead   = 64 << 10
	confluenceTableSummaryDefaultMaxBytes  = 128 << 10
	confluenceTableExtractDefaultMaxBytes  = 256 << 10
	confluenceTableMinMaxBytes             = 1 << 10
	confluenceTableMaxMaxBytes             = 1 << 20
	confluenceTableMaxIndex                = 10_000
	confluenceAttachmentDefaultMaxBytes    = 128 << 10
	confluenceAttachmentMinMaxBytes        = 1 << 10
	confluenceAttachmentMaxMaxBytes        = 1 << 20
	confluenceCommentMaxPages              = 32
	confluenceCommentDefaultMaxItems       = 100
	confluenceCommentMaxMaxItems           = 1000
	confluenceCommentListDefaultMaxBytes   = 128 << 10
	confluenceCommentThreadDefaultMaxBytes = 256 << 10
	confluenceCommentMinMaxBytes           = 1 << 10
	confluenceCommentMaxMaxBytes           = 1 << 20
	jiraStructureViewDefaultMaxBytes       = 256 << 10
	jiraStructureViewMinMaxBytes           = 1 << 10
	jiraStructureViewMaxMaxBytes           = 1 << 20
	jiraStructureViewDefaultMaxRows        = 200
	jiraStructureViewMaxMaxRows            = 1000
	jiraStructureViewMaxFields             = 32
	jiraStructureMetadataMaxBytes          = 32 << 10
	jiraStructureFieldIDMaxBytes           = 256
	jiraStructureFolderIDMaxBytes          = 256
	jiraStructureFolderPathMaxBytes        = 4 << 10
	jiraIssueRefsMaxFields                 = 8
	jiraIssueRefsMaxIssues                 = 25
	jiraEvidenceDefaultMaxBytes            = 256 << 10
	jiraEvidenceMinMaxBytes                = 1 << 10
	jiraEvidenceMaxMaxBytes                = 1 << 20
)

func boundedDefault(value, defaultValue, maximum int, name string) (int, error) {
	if value == 0 {
		return defaultValue, nil
	}
	if value < 1 || value > maximum {
		return 0, fmt.Errorf("%w: %s must be between 1 and %d", domain.ErrUsage, name, maximum)
	}
	return value, nil
}

// boundedBytes resolves one positive max_bytes bound. The shared 1..maximum
// window is checked first and the tool's own minimum second, so a value inside
// the window but under the minimum is refused with the minimum it missed rather
// than with the range it satisfied.
func boundedBytes(value, defaultValue, minimum, maximum int) (int, error) {
	bounded, err := boundedDefault(value, defaultValue, maximum, "max_bytes")
	if err != nil {
		return 0, err
	}
	if bounded < minimum {
		return 0, fmt.Errorf("%w: max_bytes must be at least %d", domain.ErrUsage, minimum)
	}
	return bounded, nil
}

func boundedTableBytes(value, defaultValue int) (int, error) {
	return boundedBytes(value, defaultValue, confluenceTableMinMaxBytes, confluenceTableMaxMaxBytes)
}

func boundedConfluenceSearchBytes(value int) (int, error) {
	return boundedBytes(value, confluenceSearchDefaultMaxBytes, confluenceSearchMinMaxBytes, confluenceSearchMaxMaxBytes)
}

func boundedConfluenceAttachmentBytes(value int) (int, error) {
	return boundedBytes(value, confluenceAttachmentDefaultMaxBytes, confluenceAttachmentMinMaxBytes, confluenceAttachmentMaxMaxBytes)
}

func validatedConfluenceCommentListInput(in ConfluenceCommentListInput) (app.ConfluenceCommentInventoryOpts, app.ConfluenceCommentViewBounds, error) {
	if !canonicalPositiveDecimal(in.PageID) || in.ExpectedPageVersion < 0 {
		return app.ConfluenceCommentInventoryOpts{}, app.ConfluenceCommentViewBounds{}, fmt.Errorf("%w: page_id must be canonical positive decimal and expected_page_version must be omitted or positive", domain.ErrUsage)
	}
	maxItems, err := boundedDefault(in.MaxItems, confluenceCommentDefaultMaxItems, confluenceCommentMaxMaxItems, "max_items")
	if err != nil {
		return app.ConfluenceCommentInventoryOpts{}, app.ConfluenceCommentViewBounds{}, err
	}
	maxBytes, err := boundedBytes(in.MaxBytes, confluenceCommentListDefaultMaxBytes, confluenceCommentMinMaxBytes, confluenceCommentMaxMaxBytes)
	if err != nil {
		return app.ConfluenceCommentInventoryOpts{}, app.ConfluenceCommentViewBounds{}, err
	}
	opts := app.ConfluenceCommentInventoryOpts{
		Location: in.Location, State: in.State, Depth: in.Depth,
		ExpectedPageVersion: in.ExpectedPageVersion,
		MaxPages:            confluenceCommentMaxPages, MaxItems: maxItems,
	}
	if err := app.ValidateConfluenceCommentInventoryOpts(opts); err != nil {
		return app.ConfluenceCommentInventoryOpts{}, app.ConfluenceCommentViewBounds{}, err
	}
	return opts, app.ConfluenceCommentViewBounds{
		MaxCommentPages: confluenceCommentMaxPages, MaxItems: maxItems, MaxBytes: maxBytes,
	}, nil
}

func validatedConfluenceCommentThreadInput(in ConfluenceCommentThreadInput) (app.ConfluenceCommentThreadOpts, app.ConfluenceCommentViewBounds, error) {
	if !canonicalPositiveDecimal(in.PageID) || !canonicalPositiveDecimal(in.CommentID) || in.ExpectedPageVersion < 0 {
		return app.ConfluenceCommentThreadOpts{}, app.ConfluenceCommentViewBounds{}, fmt.Errorf("%w: page_id and comment_id must be canonical positive decimals and expected_page_version must be omitted or positive", domain.ErrUsage)
	}
	maxItems, err := boundedDefault(in.MaxItems, confluenceCommentDefaultMaxItems, confluenceCommentMaxMaxItems, "max_items")
	if err != nil {
		return app.ConfluenceCommentThreadOpts{}, app.ConfluenceCommentViewBounds{}, err
	}
	maxBytes, err := boundedBytes(in.MaxBytes, confluenceCommentThreadDefaultMaxBytes, confluenceCommentMinMaxBytes, confluenceCommentMaxMaxBytes)
	if err != nil {
		return app.ConfluenceCommentThreadOpts{}, app.ConfluenceCommentViewBounds{}, err
	}
	return app.ConfluenceCommentThreadOpts{
			ExpectedPageVersion: in.ExpectedPageVersion,
			MaxPages:            confluenceCommentMaxPages, MaxItems: maxItems,
		}, app.ConfluenceCommentViewBounds{
			MaxCommentPages: confluenceCommentMaxPages, MaxItems: maxItems, MaxBytes: maxBytes,
		}, nil
}

func canonicalPositiveDecimal(value string) bool {
	if value == "" || value[0] == '0' || strings.TrimSpace(value) != value {
		return false
	}
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0
}

func defaultConfluenceCommentSelector(value string) string {
	if value == "" {
		return "all"
	}
	return value
}

func validateConfluenceCommentBinding(result *app.ConfluenceCommentInventoryResult, pageID string, expectedPageVersion int, query app.ConfluenceCommentQuery) error {
	if result == nil || result.PageID != pageID || result.PageVersion < 1 ||
		result.PageVersionGated != (expectedPageVersion > 0) ||
		(expectedPageVersion > 0 && result.PageVersion != expectedPageVersion) || result.Query != query {
		return fmt.Errorf("%w: Confluence comment result is not bound to the request", domain.ErrCheckFailed)
	}
	if query.Mode == "thread" {
		for _, comment := range result.Comments {
			if comment.ID == query.CommentID {
				return nil
			}
		}
		return fmt.Errorf("%w: Confluence comment thread does not contain the selected identity", domain.ErrCheckFailed)
	}
	return nil
}

// validateAttachmentInventory refuses evidence the transport cannot vouch for.
// The version check is the point of the tool: the application layer already
// gated on expectedVersion, so a result that reports any other version means
// the inventory and the caller's page read are not the same revision.
func validateAttachmentInventory(inventory *app.ConfluenceAttachmentInventoryResult, expectedVersion int) error {
	if inventory == nil || inventory.SchemaVersion != 1 || strings.TrimSpace(inventory.PageID) == "" ||
		inventory.PageVersion != expectedVersion || inventory.Attachments == nil ||
		inventory.Count != len(inventory.Attachments) {
		return fmt.Errorf("%w: Confluence attachment inventory is not reconciled", domain.ErrCheckFailed)
	}
	if inventory.Complete != (inventory.PartialReason == "") ||
		(inventory.PartialReason != "" && !domain.ValidAttachmentPartialReason(inventory.PartialReason)) {
		return fmt.Errorf("%w: Confluence attachment inventory completeness is not reconciled", domain.ErrCheckFailed)
	}
	seen := make(map[string]struct{}, len(inventory.Attachments))
	for _, attachment := range inventory.Attachments {
		if strings.TrimSpace(attachment.ID) == "" || attachment.FileSize < 0 || attachment.Version < 0 {
			return fmt.Errorf("%w: Confluence attachment identity is invalid", domain.ErrCheckFailed)
		}
		if _, duplicate := seen[attachment.ID]; duplicate {
			return fmt.Errorf("%w: Confluence attachment ids are not unique", domain.ErrCheckFailed)
		}
		seen[attachment.ID] = struct{}{}
	}
	return nil
}

// validateConfluenceOutlineResult and validateConfluenceSectionResult fail
// closed on a structural read whose own provenance, completeness, or byte
// accounting does not reconcile. Every check is content-agnostic — it looks only
// at identity integers, counts, lengths, and the closed reason sets — so a page
// with no headings, an empty section body, or unusual text is never rejected for
// what it says. The point is that a client may treat these results as evidence
// about a specific page revision, which is only safe if a self-inconsistent
// result can never reach it.
func validateConfluenceOutlineResult(out *app.ConfluencePageOutlineResult) error {
	if out == nil || out.SchemaVersion != app.ConfluenceStructuralSchemaVersion ||
		strings.TrimSpace(out.ID) == "" || out.Version < 1 {
		return fmt.Errorf("%w: Confluence page outline provenance is not reconciled", domain.ErrCheckFailed)
	}
	// An absent heading slice is not the same evidence as an empty one: the
	// first proves nothing was enumerated, the second proves nothing exists.
	if out.Headings == nil || out.Count != len(out.Headings) || out.Total < out.Count ||
		out.EmittedBytes < 0 || out.OriginalBytes < out.EmittedBytes {
		return fmt.Errorf("%w: Confluence page outline accounting is not reconciled", domain.ErrCheckFailed)
	}
	for i, heading := range out.Headings {
		if heading.Index != i+1 || heading.Level < 1 || heading.Level > 6 ||
			strings.TrimSpace(heading.Title) == "" || len(heading.Path) == 0 ||
			heading.Path[len(heading.Path)-1] != heading.Title || heading.Occurrence < 1 {
			return fmt.Errorf("%w: Confluence page outline selection metadata is not reconciled", domain.ErrCheckFailed)
		}
	}
	if err := validateConfluenceStructuralCompleteness(
		out.Complete, out.Truncated, out.PartialReason, app.ConfluenceValidOutlinePartialReason, "outline",
	); err != nil {
		return err
	}
	// A complete outline emitted every heading it counted; a partial one, by
	// definition, withheld at least one.
	if out.Complete != (out.Count == out.Total) {
		return fmt.Errorf("%w: Confluence page outline completeness contradicts its heading counts", domain.ErrCheckFailed)
	}
	return nil
}

func validateConfluencePageMetadataResult(out *app.ConfluencePageMetadataResult) error {
	if out == nil || out.SchemaVersion != app.ConfluencePageMetadataSchemaVersion ||
		strings.TrimSpace(out.ID) == "" || strings.TrimSpace(out.Title) == "" ||
		strings.TrimSpace(out.Space) == "" || out.Version < 1 {
		return fmt.Errorf("%w: Confluence page metadata provenance is not reconciled", domain.ErrCheckFailed)
	}
	switch out.RestrictionState {
	case app.ConfluenceRestrictionUnknown, app.ConfluenceRestrictionRestricted, app.ConfluenceRestrictionUnrestricted:
		return nil
	default:
		return fmt.Errorf("%w: Confluence page restriction state is not reconciled", domain.ErrCheckFailed)
	}
}

// boundedOutput encodes one result and refuses it when the encoding exceeds the
// bound. Callers must supply static subject text rather than backend-controlled
// values, because an MCP client sees only this message: it has to name the
// result that was withheld and how to get it. The oversize error carries
// domain.ErrOutputLimit so a client can tell a bound it may raise from a check
// that failed.
func boundedOutput(value any, maxBytes int, encodeMessage, oversizeMessage string) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: %s", domain.ErrCheckFailed, encodeMessage)
	}
	if len(encoded) > maxBytes {
		return fmt.Errorf("%w: %w: %s", domain.ErrCheckFailed, domain.ErrOutputLimit, oversizeMessage)
	}
	return nil
}

// availableResult refuses an absent result before it is encoded. A typed nil
// pointer is as absent as a nil interface, and encoding either one would emit
// `null` as if the backend had reconciled it.
func availableResult(value any, subject string) error {
	unavailable := value == nil
	if !unavailable {
		reflected := reflect.ValueOf(value)
		unavailable = reflected.Kind() == reflect.Pointer && reflected.IsNil()
	}
	if unavailable {
		return fmt.Errorf("%w: %s is unavailable", domain.ErrCheckFailed, subject)
	}
	return nil
}

// boundedConfluencePageMetadataOutput runs after the metadata result is already
// reconciled, so it bounds without an availability check.
func boundedConfluencePageMetadataOutput(out *app.ConfluencePageMetadataResult) error {
	return boundedOutput(out, confluencePageMetadataMaxBytes,
		"encode Confluence page metadata",
		"Confluence page metadata exceeds its output bound")
}

func validateConfluenceSectionResult(out *app.ConfluencePageSectionResult, in ConfluenceSectionInput) error {
	if out == nil || out.SchemaVersion != app.ConfluenceStructuralSchemaVersion ||
		strings.TrimSpace(out.ID) == "" || out.Version < 1 {
		return fmt.Errorf("%w: Confluence page section provenance is not reconciled", domain.ErrCheckFailed)
	}
	// The gate claim must say exactly what this request asked for. A bound
	// request has to come back gated at the revision it named; an unbound one has
	// to admit it is ungated instead of borrowing authority no caller granted,
	// which is what would let a consumer read page_version_gated as proof.
	switch {
	case in.ExpectedPageVersion < 0:
		return fmt.Errorf("%w: Confluence page section gate request is not reconciled", domain.ErrCheckFailed)
	case in.ExpectedPageVersion == 0 && out.PageVersionGated:
		return fmt.Errorf("%w: Confluence page section claims a binding the request never made", domain.ErrCheckFailed)
	case in.ExpectedPageVersion > 0 && (!out.PageVersionGated || out.Version != in.ExpectedPageVersion):
		return fmt.Errorf("%w: Confluence page section is not bound to the expected page version", domain.ErrCheckFailed)
	}
	if strings.TrimSpace(out.Heading) == "" || len(out.Path) == 0 ||
		out.Path[len(out.Path)-1] != out.Heading ||
		out.Level < 1 || out.Level > 6 || out.Occurrence < 1 {
		return fmt.Errorf("%w: Confluence page section selection is not reconciled", domain.ErrCheckFailed)
	}
	requestedOccurrence := in.Occurrence
	if requestedOccurrence == 0 {
		requestedOccurrence = 1
	}
	if normalizeConfluenceHeading(in.Heading) != normalizeConfluenceHeading(out.Heading) ||
		out.Occurrence != requestedOccurrence {
		return fmt.Errorf("%w: Confluence page section does not match the requested selection", domain.ErrCheckFailed)
	}
	if err := validateConfluenceStructuralCompleteness(
		out.Complete, out.Truncated, out.PartialReason, app.ConfluenceValidSectionPartialReason, "section",
	); err != nil {
		return err
	}
	if out.EmittedBytes != len(out.Markdown) || out.OriginalBytes < out.EmittedBytes || !utf8.ValidString(out.Markdown) {
		return fmt.Errorf("%w: Confluence page section byte accounting is not reconciled", domain.ErrCheckFailed)
	}
	// original_bytes is the exact bound that returns this same rendering
	// complete, so it equals the emitted size when nothing was withheld and
	// strictly exceeds it when something was.
	if out.Complete != (out.OriginalBytes == out.EmittedBytes) {
		return fmt.Errorf("%w: Confluence page section completeness contradicts its byte accounting", domain.ErrCheckFailed)
	}
	return nil
}

func validatedConfluenceSectionsInput(in ConfluenceSectionsInput) ([]app.ConfluencePageSectionSelector, int, error) {
	if strings.TrimSpace(in.Reference) == "" {
		return nil, 0, fmt.Errorf("%w: reference is required", domain.ErrUsage)
	}
	if in.ExpectedPageVersion < 0 {
		return nil, 0, fmt.Errorf("%w: expected_page_version must be omitted or a positive page version", domain.ErrUsage)
	}
	if len(in.Selectors) == 0 || len(in.Selectors) > confluencePageSectionsMaxSelectors {
		return nil, 0, fmt.Errorf("%w: selectors must contain between 1 and %d entries", domain.ErrUsage, confluencePageSectionsMaxSelectors)
	}
	maxBytes, err := boundedDefault(in.MaxBytes, confluencePageSectionsDefaultMaxBytes, confluencePageSectionsMaxMaxBytes, "max_bytes")
	if err != nil {
		return nil, 0, err
	}
	selectors := make([]app.ConfluencePageSectionSelector, 0, len(in.Selectors))
	for _, selector := range in.Selectors {
		heading := normalizeConfluenceHeading(selector.Heading)
		if heading == "" {
			return nil, 0, fmt.Errorf("%w: selector heading is required", domain.ErrUsage)
		}
		if selector.Occurrence < 0 {
			return nil, 0, fmt.Errorf("%w: selector occurrence must be omitted or positive", domain.ErrUsage)
		}
		selectors = append(selectors, app.ConfluencePageSectionSelector{
			Heading: selector.Heading, Occurrence: selector.Occurrence,
		})
	}
	return selectors, maxBytes, nil
}

func validateConfluenceSectionsResult(out *app.ConfluencePageSectionsResult, in ConfluenceSectionsInput, maxBytes int) error {
	if out == nil || out.SchemaVersion != app.ConfluenceStructuralSchemaVersion ||
		strings.TrimSpace(out.ID) == "" || !utf8.ValidString(out.ID) ||
		!utf8.ValidString(out.PageTitle) || !utf8.ValidString(out.Space) || out.Version < 1 {
		return fmt.Errorf("%w: Confluence page sections provenance is not reconciled", domain.ErrCheckFailed)
	}
	switch {
	case in.ExpectedPageVersion < 0:
		return fmt.Errorf("%w: Confluence page sections gate request is not reconciled", domain.ErrCheckFailed)
	case in.ExpectedPageVersion == 0 && out.PageVersionGated:
		return fmt.Errorf("%w: Confluence page sections claim a binding the request never made", domain.ErrCheckFailed)
	case in.ExpectedPageVersion > 0 && (!out.PageVersionGated || out.Version != in.ExpectedPageVersion):
		return fmt.Errorf("%w: Confluence page sections are not bound to the expected page version", domain.ErrCheckFailed)
	}
	if out.Sections == nil || out.MaxBytes != maxBytes || out.RequestedCount != len(in.Selectors) ||
		out.ReturnedCount != len(out.Sections) || out.ReturnedCount != out.RequestedCount || !out.Reconciled ||
		out.OriginalBytes < 0 || out.EmittedBytes < 0 || out.OriginalBytes < out.EmittedBytes || out.EmittedBytes > maxBytes {
		return fmt.Errorf("%w: Confluence page sections aggregate accounting is not reconciled", domain.ErrCheckFailed)
	}
	originalBytes, emittedBytes := 0, 0
	remainingBytes := maxBytes
	allComplete, anyTruncated := true, false
	for i, section := range out.Sections {
		remainingSelectors := len(out.Sections) - i
		sectionMaxBytes := (remainingBytes + remainingSelectors - 1) / remainingSelectors
		requested := in.Selectors[i]
		requestedOccurrence := requested.Occurrence
		if requestedOccurrence == 0 {
			requestedOccurrence = 1
		}
		if strings.TrimSpace(section.Heading) == "" || !utf8.ValidString(section.Heading) || len(section.Path) == 0 ||
			len(section.Path) > section.Level ||
			section.Path[len(section.Path)-1] != section.Heading || section.Level < 1 || section.Level > 6 ||
			normalizeConfluenceHeading(section.Heading) != normalizeConfluenceHeading(requested.Heading) ||
			section.Occurrence != requestedOccurrence {
			return fmt.Errorf("%w: Confluence page sections selection order is not reconciled", domain.ErrCheckFailed)
		}
		for _, component := range section.Path {
			if strings.TrimSpace(component) == "" || !utf8.ValidString(component) {
				return fmt.Errorf("%w: Confluence page sections path is not reconciled", domain.ErrCheckFailed)
			}
		}
		if err := validateConfluenceStructuralCompleteness(
			section.Complete, section.Truncated, section.PartialReason, app.ConfluenceValidSectionPartialReason, "section",
		); err != nil {
			return err
		}
		if section.OriginalBytes < 0 || section.EmittedBytes < 0 || section.OriginalBytes < section.EmittedBytes ||
			section.EmittedBytes > sectionMaxBytes ||
			section.PartialReason == "max_bytes" && section.OriginalBytes <= sectionMaxBytes ||
			section.EmittedBytes != len(section.Markdown) || !utf8.ValidString(section.Markdown) ||
			section.Complete != (section.OriginalBytes == section.EmittedBytes) {
			return fmt.Errorf("%w: Confluence page sections byte accounting is not reconciled", domain.ErrCheckFailed)
		}
		// Compare against the declared totals before adding so adversarial reader
		// values cannot overflow int and wrap into an apparently valid sum.
		if section.OriginalBytes > out.OriginalBytes-originalBytes || section.EmittedBytes > out.EmittedBytes-emittedBytes {
			return fmt.Errorf("%w: Confluence page sections aggregate byte totals are not reconciled", domain.ErrCheckFailed)
		}
		originalBytes += section.OriginalBytes
		emittedBytes += section.EmittedBytes
		remainingBytes -= section.EmittedBytes
		allComplete = allComplete && section.Complete
		anyTruncated = anyTruncated || section.Truncated
	}
	if originalBytes != out.OriginalBytes || emittedBytes != out.EmittedBytes ||
		out.Complete != (out.Reconciled && allComplete) || out.Truncated != anyTruncated {
		return fmt.Errorf("%w: Confluence page sections completeness is not reconciled", domain.ErrCheckFailed)
	}
	return validateConfluenceSectionsMetadataBound(out)
}

// validateConfluenceSectionsMetadataBound prevents backend-controlled page and
// heading metadata from being amplified across repeated selectors before the
// encoded-result limit can be checked. JSON may escape every input byte as six
// output bytes, so the accounting is deliberately conservative and includes a
// fixed allowance for field names, punctuation, numbers, and booleans.
func validateConfluenceSectionsMetadataBound(out *app.ConfluencePageSectionsResult) error {
	remaining := confluencePageSectionsResultOverhead
	consumeFixed := func(size int) bool {
		if size > remaining {
			return false
		}
		remaining -= size
		return true
	}
	consumeString := func(value string) bool {
		const quotes = 2
		if !utf8.ValidString(value) || remaining < quotes || len(value) > (remaining-quotes)/6 {
			return false
		}
		remaining -= quotes + 6*len(value)
		return true
	}
	if !consumeFixed(512) || !consumeString(out.ID) || !consumeString(out.PageTitle) || !consumeString(out.Space) {
		return fmt.Errorf("%w: Confluence page sections metadata exceeds its pre-encoding allowance", domain.ErrCheckFailed)
	}
	for _, section := range out.Sections {
		if !consumeFixed(256) || !consumeString(section.Heading) || !consumeString(section.PartialReason) {
			return fmt.Errorf("%w: Confluence page sections metadata exceeds its pre-encoding allowance", domain.ErrCheckFailed)
		}
		for _, component := range section.Path {
			if !consumeFixed(32) || !consumeString(component) {
				return fmt.Errorf("%w: Confluence page sections metadata exceeds its pre-encoding allowance", domain.ErrCheckFailed)
			}
		}
	}
	return nil
}

func boundedConfluenceSectionsOutput(out *app.ConfluencePageSectionsResult) error {
	return boundedOutput(out, confluencePageSectionsMaxMaxBytes+confluencePageSectionsResultOverhead,
		"encode Confluence page sections",
		"Confluence page sections exceed their bounded content and metadata allowance")
}

func normalizeConfluenceHeading(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func validateConfluenceStructuralCompleteness(complete, truncated bool, reason string, known func(string) bool, kind string) error {
	if complete != (reason == "") || truncated == complete {
		return fmt.Errorf("%w: Confluence page %s completeness is not reconciled", domain.ErrCheckFailed, kind)
	}
	if reason != "" && !known(reason) {
		return fmt.Errorf("%w: Confluence page %s reports an unrecognized partial reason", domain.ErrCheckFailed, kind)
	}
	return nil
}

// The inventory is never clipped: a partial attachment list would be exactly
// the false-absence evidence this tool exists to prevent.
func boundedAttachmentInventoryOutput(value *app.ConfluenceAttachmentInventoryView, maxBytes int) error {
	if err := availableResult(value, "Confluence attachment inventory"); err != nil {
		return err
	}
	return boundedOutput(value, maxBytes,
		"encode Confluence attachment inventory",
		"Confluence attachment inventory exceeds max_bytes; raise the bound")
}

func boundedConfluenceCommentOutput(value any, maxBytes int) error {
	if err := availableResult(value, "Confluence comment result"); err != nil {
		return err
	}
	return boundedOutput(value, maxBytes,
		"encode Confluence comment result",
		"Confluence comment result exceeds max_bytes; narrow the selection or raise the bound")
}

func boundedConfluenceSearchOutput(value *app.ConfluenceSearchResult, maxBytes int) error {
	if err := availableResult(value, "Confluence search result"); err != nil {
		return err
	}
	return boundedOutput(value, maxBytes,
		"encode Confluence search result",
		"Confluence search result exceeds max_bytes; narrow CQL or lower the row limit before raising the bound")
}

func validateTableSummary(summary *app.ConfluenceTableSummary, table, expectedPageVersion int) error {
	if summary == nil || summary.SchemaVersion != app.ConfluenceTableSchemaVersion ||
		summary.CellContract != app.ConfluenceTableCellContract ||
		strings.TrimSpace(summary.PageID) == "" || summary.Version < 1 ||
		summary.PageVersionGated != (expectedPageVersion > 0) ||
		(expectedPageVersion > 0 && summary.Version != expectedPageVersion) ||
		summary.Table != table || summary.TableCount < 0 ||
		summary.ReturnedTableCount != len(summary.Tables) || !summary.SelectionReconciled {
		return fmt.Errorf("%w: table summary is not reconciled", domain.ErrCheckFailed)
	}
	if table == 0 && len(summary.Tables) != summary.TableCount {
		return fmt.Errorf("%w: table summary is not reconciled", domain.ErrCheckFailed)
	}
	if table > 0 && (summary.TableCount < table || len(summary.Tables) != 1 || summary.Tables[0].Index != table) {
		return fmt.Errorf("%w: selected table summary is not reconciled", domain.ErrCheckFailed)
	}
	for index, record := range summary.Tables {
		expectedIndex := index + 1
		if table > 0 {
			expectedIndex = table
		}
		if record.Index != expectedIndex || !record.Rectangular || !record.CellCountReconciled {
			return fmt.Errorf("%w: table summary record is not reconciled", domain.ErrCheckFailed)
		}
	}
	return nil
}

func validateSelectedTableExtract(extract *app.ConfluenceTableExtract, table, expectedPageVersion int) error {
	if extract == nil || extract.SchemaVersion != app.ConfluenceTableSchemaVersion ||
		extract.CellContract != app.ConfluenceTableCellContract ||
		strings.TrimSpace(extract.PageID) == "" || extract.Version < 1 ||
		extract.PageVersionGated != (expectedPageVersion > 0) ||
		(expectedPageVersion > 0 && extract.Version != expectedPageVersion) ||
		extract.Table != table || extract.TableCount < table ||
		extract.ReturnedTableCount != len(extract.Tables) || !extract.SelectionReconciled ||
		len(extract.Tables) != 1 || extract.Tables[0].Index != table {
		return fmt.Errorf("%w: selected table extract is not reconciled", domain.ErrCheckFailed)
	}
	selected := extract.Tables[0]
	if selected.RowCount < 0 || selected.ColumnCount < 0 || selected.RowCount != len(selected.Rows) {
		return fmt.Errorf("%w: selected table dimensions are not reconciled", domain.ErrCheckFailed)
	}
	for rowIndex, row := range selected.Rows {
		if row.Index != rowIndex+1 || len(row.Cells) != selected.ColumnCount {
			return fmt.Errorf("%w: selected table rows are not reconciled", domain.ErrCheckFailed)
		}
		for columnIndex, cell := range row.Cells {
			if cell.Row != rowIndex+1 || cell.Column != columnIndex+1 {
				return fmt.Errorf("%w: selected table cells are not reconciled", domain.ErrCheckFailed)
			}
		}
	}
	summary := app.SummarizeConfluenceTables(extract)
	if summary == nil || !summary.SelectionReconciled || len(summary.Tables) != 1 || selected.Summary != summary.Tables[0] {
		return fmt.Errorf("%w: selected table summary is not reconciled", domain.ErrCheckFailed)
	}
	return nil
}

func boundedTableOutput(value any, maxBytes int) error {
	if err := availableResult(value, "table result"); err != nil {
		return err
	}
	return boundedOutput(value, maxBytes,
		"encode table result",
		"table result exceeds max_bytes; select one table or raise the bound")
}
