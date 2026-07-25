package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

// confluencePartialInvariantHolds encodes the single contract both partial
// results must satisfy: partial_reason is absent exactly when the read is
// complete. TestConfluencePartialInvariantRejectsInconsistentResults keeps this
// helper load-bearing by proving it rejects both inconsistent directions.
func confluencePartialInvariantHolds(complete bool, reason string) bool {
	return complete == (reason == "")
}

// confluencePartialReasonJSON reports the emitted partial_reason and whether the
// key was present at all, so tests assert omission on the wire rather than an
// empty Go string.
func confluencePartialReasonJSON(t *testing.T, result any) (string, bool) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	value, present := decoded["partial_reason"]
	text, _ := value.(string)
	return text, present
}

type sectionStore struct {
	domain.DocStore
	page *domain.Resource
}

func (s *sectionStore) GetPage(context.Context, string, domain.PullOpts) (*domain.Resource, error) {
	return s.page, nil
}

const sectionTestCSF = `<h1>Overview</h1><p>Intro</p>` +
	`<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[<h2>Not a heading</h2>]]></ac:plain-text-body></ac:structured-macro>` +
	`<ac:layout><ac:layout-section ac:type="single"><ac:layout-cell>` +
	`<h2>Delivery Notes</h2><p><span style="color: red;">Important</span> <a href="https://example.test/runbook">runbook</a></p>` +
	`<h3>Details</h3><table><tbody><tr><th>Key</th><th>Value</th></tr><tr><td>A</td><td>1</td></tr></tbody></table>` +
	`<h2>Delivery Notes</h2><p>Second occurrence</p>` +
	`</ac:layout-cell></ac:layout-section></ac:layout><h1>Appendix</h1><p>Tail</p>`

func sectionService() *ConfluenceService {
	return &ConfluenceService{store: &sectionStore{page: &domain.Resource{ID: "42", Title: "Example", SpaceKey: "ENG", Version: 3, Body: []byte(sectionTestCSF), BodyPresent: true}}}
}

// driftingSectionStore serves its first page once and every later read from the
// next revision, which is the concurrent-edit shape the version binding exists
// to catch. It also counts reads, so a test can prove the gate adds no request.
type driftingSectionStore struct {
	domain.DocStore
	pages []*domain.Resource
	calls int
}

func (s *driftingSectionStore) GetPage(context.Context, string, domain.PullOpts) (*domain.Resource, error) {
	page := s.pages[min(s.calls, len(s.pages)-1)]
	s.calls++
	return page, nil
}

// The inserted revision prepends one more heading with the same title, so every
// later occurrence of "Status" shifts by one without any other visible change.
const (
	sectionDriftBeforeCSF = `<h1>Alpha</h1><p>Intro</p>` +
		`<h2>Status</h2><p>first status</p>` +
		`<h2>Status</h2><p>second status</p>`
	sectionDriftAfterCSF = `<h1>Alpha</h1><p>Intro</p>` +
		`<h2>Status</h2><p>inserted status</p>` +
		`<h2>Status</h2><p>first status</p>` +
		`<h2>Status</h2><p>second status</p>`
)

func TestConfluencePageOutlineUsesStructuralBlocks(t *testing.T) {
	result, err := sectionService().PageOutline(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Count != 5 || result.Truncated {
		t.Fatalf("result=%+v", result)
	}
	if reason, present := confluencePartialReasonJSON(t, result); present || reason != "" ||
		!confluencePartialInvariantHolds(result.Complete, result.PartialReason) {
		t.Fatalf("complete outline must omit partial_reason: reason=%q present=%t result=%+v", reason, present, result)
	}
	titles := make([]string, 0, len(result.Headings))
	for _, heading := range result.Headings {
		titles = append(titles, heading.Title)
	}
	if strings.Contains(strings.Join(titles, ","), "Not a heading") || strings.Join(titles, ",") != "Overview,Delivery Notes,Details,Delivery Notes,Appendix" {
		t.Fatalf("headings=%+v", result.Headings)
	}
	if strings.Join(result.Headings[2].Path, "/") != "Overview/Delivery Notes/Details" || result.Headings[3].Occurrence != 2 {
		t.Fatalf("paths/occurrences=%+v", result.Headings)
	}
}

func TestConfluencePageOutlineReportsHeadingCap(t *testing.T) {
	var body strings.Builder
	for i := 0; i < confluenceOutlineHeadingCap+1; i++ {
		body.WriteString("<h2>Repeated</h2>")
	}
	service := &ConfluenceService{store: &sectionStore{page: &domain.Resource{ID: "42", Version: 1, Body: []byte(body.String()), BodyPresent: true}}}
	result, err := service.PageOutline(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || !result.Truncated || result.Count != confluenceOutlineHeadingCap || result.Total != confluenceOutlineHeadingCap+1 {
		t.Fatalf("result=%+v", result)
	}
	reason, present := confluencePartialReasonJSON(t, result)
	if !present || reason != "heading_limit" || result.PartialReason != reason ||
		!confluencePartialInvariantHolds(result.Complete, result.PartialReason) {
		t.Fatalf("heading-limit outline reason=%q present=%t result=%+v", reason, present, result)
	}
	if result.EmittedBytes <= 0 || result.OriginalBytes <= result.EmittedBytes {
		t.Fatalf("heading-limit accounting lost: %+v", result)
	}
}

func TestConfluencePageOutlineReportsByteCap(t *testing.T) {
	title := strings.Repeat("x", confluenceOutlineByteCap)
	service := &ConfluenceService{store: &sectionStore{page: &domain.Resource{ID: "42", Version: 1, Body: []byte("<h2>" + title + "</h2>"), BodyPresent: true}}}
	result, err := service.PageOutline(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || !result.Truncated || result.Count != 0 || result.Total != 1 || result.OriginalBytes <= confluenceOutlineByteCap || result.EmittedBytes != 0 {
		t.Fatalf("result=%+v", result)
	}
	reason, present := confluencePartialReasonJSON(t, result)
	if !present || reason != "byte_limit" || result.PartialReason != reason ||
		!confluencePartialInvariantHolds(result.Complete, result.PartialReason) {
		t.Fatalf("byte-limit outline reason=%q present=%t result=%+v", reason, present, result)
	}
}

func TestConfluencePageOutlineHeadingLimitWinsWhenBothCapsBind(t *testing.T) {
	var body strings.Builder
	for i := 0; i < confluenceOutlineHeadingCap; i++ {
		body.WriteString("<h2>Repeated</h2>")
	}
	// The next heading crosses both caps at once. Heading count is already
	// binding before this record's encoded size is considered.
	body.WriteString("<h2>" + strings.Repeat("x", confluenceOutlineByteCap) + "</h2>")
	service := &ConfluenceService{store: &sectionStore{page: &domain.Resource{ID: "42", Version: 1, Body: []byte(body.String()), BodyPresent: true}}}
	result, err := service.PageOutline(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if result.PartialReason != "heading_limit" || result.Count != confluenceOutlineHeadingCap ||
		result.Total != confluenceOutlineHeadingCap+1 || result.OriginalBytes <= confluenceOutlineByteCap {
		t.Fatalf("simultaneous caps must retain heading-limit precedence: %+v", result)
	}
}

func TestConfluencePartialInvariantRejectsInconsistentResults(t *testing.T) {
	for _, test := range []struct {
		name     string
		complete bool
		reason   string
	}{
		{name: "partial without reason", complete: false, reason: ""},
		{name: "complete with reason", complete: true, reason: "max_bytes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if confluencePartialInvariantHolds(test.complete, test.reason) {
				t.Fatalf("invariant accepted complete=%t reason=%q", test.complete, test.reason)
			}
		})
	}
	if !confluencePartialInvariantHolds(true, "") || !confluencePartialInvariantHolds(false, "byte_limit") {
		t.Fatal("invariant rejected a consistent pair")
	}
}

func TestConfluencePageSectionRequiresDuplicateOccurrenceAndPreservesRendering(t *testing.T) {
	service := sectionService()
	_, err := service.PageSection(context.Background(), "42", ConfluencePageSectionOpts{Heading: " delivery   notes "})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("ambiguous err=%v", err)
	}
	result, err := service.PageSection(context.Background(), "42", ConfluencePageSectionOpts{Heading: "DELIVERY NOTES", Occurrence: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Delivery Notes", "### Details", `<span style="color: red">Important</span>`, "[runbook](https://example.test/runbook)", "| Key | Value |"} {
		if !strings.Contains(result.Markdown, want) {
			t.Errorf("markdown missing %q:\n%s", want, result.Markdown)
		}
	}
	if strings.Contains(result.Markdown, "Second occurrence") || strings.Contains(result.Markdown, "Appendix") || !result.Complete || result.Occurrence != 1 {
		t.Fatalf("result=%+v", result)
	}
	if reason, present := confluencePartialReasonJSON(t, result); present || reason != "" ||
		!confluencePartialInvariantHolds(result.Complete, result.PartialReason) {
		t.Fatalf("complete section must omit partial_reason: reason=%q present=%t result=%+v", reason, present, result)
	}
}

func TestConfluencePageSectionTruncatesAtBlockBoundary(t *testing.T) {
	result, err := sectionService().PageSection(context.Background(), "42", ConfluencePageSectionOpts{Heading: "Overview", MaxBytes: 80})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || !result.Truncated || !strings.Contains(result.Markdown, "truncated by atl") || result.EmittedBytes > 80 || result.OriginalBytes <= result.EmittedBytes {
		t.Fatalf("result=%+v", result)
	}
	reason, present := confluencePartialReasonJSON(t, result)
	if !present || reason != "max_bytes" || result.PartialReason != reason ||
		!confluencePartialInvariantHolds(result.Complete, result.PartialReason) {
		t.Fatalf("max_bytes section reason=%q present=%t result=%+v", reason, present, result)
	}

	// original_bytes is the exact minimum bound that returns the same section
	// complete, so one re-read at that value is the whole recovery.
	recovered, err := sectionService().PageSection(context.Background(), "42", ConfluencePageSectionOpts{Heading: "Overview", MaxBytes: result.OriginalBytes})
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Complete || recovered.Truncated || recovered.PartialReason != "" ||
		recovered.EmittedBytes != result.OriginalBytes || recovered.OriginalBytes != result.OriginalBytes ||
		strings.Contains(recovered.Markdown, "truncated by atl") {
		t.Fatalf("re-read at original_bytes=%d must be complete: %+v", result.OriginalBytes, recovered)
	}
	if reason, present := confluencePartialReasonJSON(t, recovered); present || reason != "" {
		t.Fatalf("recovered section must omit partial_reason: reason=%q present=%t", reason, present)
	}
	if below, err := sectionService().PageSection(context.Background(), "42", ConfluencePageSectionOpts{Heading: "Overview", MaxBytes: result.OriginalBytes - 1}); err != nil {
		t.Fatal(err)
	} else if below.Complete || below.PartialReason != "max_bytes" {
		t.Fatalf("original_bytes-1 must stay partial: %+v", below)
	}
}

func TestBoundedConfluenceSectionMarkdownNamesItsExactLimiter(t *testing.T) {
	fits := boundedConfluenceSectionMarkdown([]mirror.Block{{MD: "first"}, {MD: "second"}}, 1<<10)
	if fits.partialReason != "" || fits.markdown != "first\n\nsecond\n" {
		t.Fatalf("complete bound=%+v", fits)
	}
	// A whole rendered block that does not fit is the recoverable byte bound.
	byteBound := boundedConfluenceSectionMarkdown([]mirror.Block{{MD: "first"}, {MD: strings.Repeat("x", 128)}}, 64)
	if byteBound.partialReason != "max_bytes" || !strings.Contains(byteBound.markdown, "truncated by atl") {
		t.Fatalf("byte bound=%+v", byteBound)
	}
	// The defensive invalid-UTF-8 path withholds the body entirely and must not
	// be reported as a recoverable byte bound, with or without truncation.
	for _, test := range []struct {
		name     string
		blocks   []mirror.Block
		maxBytes int
	}{
		{name: "untruncated", blocks: []mirror.Block{{MD: "\xff\xfe"}}, maxBytes: 1 << 10},
		{name: "also truncated", blocks: []mirror.Block{{MD: "\xff\xfe"}, {MD: strings.Repeat("x", 128)}}, maxBytes: 64},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := boundedConfluenceSectionMarkdown(test.blocks, test.maxBytes)
			if invalid.partialReason != "invalid_utf8" || invalid.markdown != "" {
				t.Fatalf("invalid utf8 bound=%+v", invalid)
			}
		})
	}
}

func TestConfluenceSectionSelectionErrorCarriesCountsOnly(t *testing.T) {
	ambiguous := &ConfluenceSectionSelectionError{Available: 3}
	if ambiguous.Error() != domain.ErrCheckFailed.Error() || ambiguous.Unwrap() != domain.ErrCheckFailed {
		t.Fatalf("ambiguous error=%q unwrap=%v", ambiguous.Error(), ambiguous.Unwrap())
	}
	stale := &ConfluenceSectionSelectionError{Requested: 4, Available: 2}
	if stale.Error() != domain.ErrNotFound.Error() || stale.Unwrap() != domain.ErrNotFound {
		t.Fatalf("stale error=%q unwrap=%v", stale.Error(), stale.Unwrap())
	}
}

func TestConfluencePageSectionSelectionErrorsAreTypedAndPreserveMessages(t *testing.T) {
	for _, test := range []struct {
		name                 string
		opts                 ConfluencePageSectionOpts
		requested, available int
		sentinel, other      error
		message              string
	}{
		{
			name: "ambiguous", opts: ConfluencePageSectionOpts{Heading: "Delivery Notes"},
			requested: 0, available: 2, sentinel: domain.ErrCheckFailed, other: domain.ErrNotFound,
			message: `check failed: Confluence heading "Delivery Notes" occurs 2 times; pass --occurrence 1..2`,
		},
		{
			name: "out of range", opts: ConfluencePageSectionOpts{Heading: "Delivery Notes", Occurrence: 5},
			requested: 5, available: 2, sentinel: domain.ErrNotFound, other: domain.ErrCheckFailed,
			message: `not found: Confluence heading "Delivery Notes" has 2 occurrence(s), not 5`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := sectionService().PageSection(context.Background(), "42", test.opts)
			var selection *ConfluenceSectionSelectionError
			if !errors.As(err, &selection) {
				t.Fatalf("error = %#v, want *ConfluenceSectionSelectionError", err)
			}
			if selection.Requested != test.requested || selection.Available != test.available {
				t.Fatalf("selection=%+v", selection)
			}
			if !errors.Is(err, test.sentinel) || errors.Is(err, test.other) {
				t.Fatalf("sentinel mapping lost: %v", err)
			}
			if err.Error() != test.message {
				t.Fatalf("message=%q want %q", err.Error(), test.message)
			}
		})
	}
}

func TestConfluencePageSectionMissingHeadingStaysUntyped(t *testing.T) {
	_, err := sectionService().PageSection(context.Background(), "42", ConfluencePageSectionOpts{Heading: "Missing"})
	var selection *ConfluenceSectionSelectionError
	if errors.As(err, &selection) {
		t.Fatalf("zero-match error must not be a selection error: %#v", selection)
	}
	if !errors.Is(err, domain.ErrNotFound) || err.Error() != `not found: Confluence heading "Missing" was not found` {
		t.Fatalf("err=%v", err)
	}
}

func TestConfluencePageSectionValidatesSelection(t *testing.T) {
	service := sectionService()
	for _, opts := range []ConfluencePageSectionOpts{{}, {Heading: "Missing"}, {Heading: "Overview", Occurrence: -1}, {Heading: "Overview", MaxBytes: confluenceSectionMaxBytes + 1}} {
		_, err := service.PageSection(context.Background(), "42", opts)
		if err == nil {
			t.Fatalf("opts=%+v unexpectedly succeeded", opts)
		}
	}
}

func TestConfluenceStructuralReadsStampTheirSchemaVersion(t *testing.T) {
	service := sectionService()
	outline, err := service.PageOutline(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	section, err := service.PageSection(context.Background(), "42", ConfluencePageSectionOpts{Heading: "Overview"})
	if err != nil {
		t.Fatal(err)
	}
	if outline.SchemaVersion != ConfluenceStructuralSchemaVersion || section.SchemaVersion != ConfluenceStructuralSchemaVersion {
		t.Fatalf("outline=%d section=%d want %d", outline.SchemaVersion, section.SchemaVersion, ConfluenceStructuralSchemaVersion)
	}
	for _, result := range []any{outline, section} {
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded["schema_version"] != float64(1) {
			t.Fatalf("schema_version absent from %T on the wire: %s", result, encoded)
		}
	}
}

func TestConfluencePageSectionExpectedVersionGate(t *testing.T) {
	for _, test := range []struct {
		name     string
		expected int
		gated    bool
	}{
		{name: "matching positive version binds the read", expected: 3, gated: true},
		{name: "zero preserves the ungated caller contract", expected: 0, gated: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &sectionStore{page: &domain.Resource{ID: "42", Title: "Example", SpaceKey: "ENG", Version: 3, Body: []byte(sectionTestCSF), BodyPresent: true}}
			result, err := (&ConfluenceService{store: store}).PageSection(context.Background(), "42", ConfluencePageSectionOpts{
				Heading: "Overview", ExpectedPageVersion: test.expected,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.PageVersionGated != test.gated || result.Version != 3 {
				t.Fatalf("gated=%t version=%d want gated=%t version=3", result.PageVersionGated, result.Version, test.gated)
			}
			// page_version_gated is a wire-visible claim, not an internal flag.
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded["page_version_gated"] != test.gated {
				t.Fatalf("page_version_gated=%v want %t: %s", decoded["page_version_gated"], test.gated, encoded)
			}
		})
	}
}

func TestConfluencePageSectionRejectsNegativeExpectedVersionAsUsage(t *testing.T) {
	store := &sectionStore{page: &domain.Resource{ID: "42", Version: 3, Body: []byte(sectionTestCSF), BodyPresent: true}}
	_, err := (&ConfluenceService{store: store}).PageSection(context.Background(), "42", ConfluencePageSectionOpts{
		Heading: "Overview", ExpectedPageVersion: -1,
	})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("err=%v want usage", err)
	}
	var mismatch *ConfluencePageVersionMismatchError
	if errors.As(err, &mismatch) {
		t.Fatalf("a negative bound is a caller mistake, not a staleness report: %+v", mismatch)
	}
}

func TestConfluencePageSectionVersionMismatchIsTypedAndCostsNoExtraRequest(t *testing.T) {
	store := &driftingSectionStore{pages: []*domain.Resource{
		{ID: "42", Title: "Example", SpaceKey: "ENG", Version: 6, Body: []byte(sectionDriftAfterCSF), BodyPresent: true},
	}}
	_, err := (&ConfluenceService{store: store}).PageSection(context.Background(), "42", ConfluencePageSectionOpts{
		Heading: "Status", Occurrence: 1, ExpectedPageVersion: 5,
	})
	var mismatch *ConfluencePageVersionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error = %#v, want *ConfluencePageVersionMismatchError", err)
	}
	if mismatch.Expected != 5 || mismatch.Current != 6 {
		t.Fatalf("mismatch=%+v want expected=5 current=6", mismatch)
	}
	if !errors.Is(err, domain.ErrCheckFailed) || errors.Is(err, domain.ErrUsage) || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("sentinel mapping lost: %v", err)
	}
	// The gate is decided from the body this read already fetched, so a refusal
	// must not cost a second page read.
	if store.calls != 1 {
		t.Fatalf("page reads=%d want 1", store.calls)
	}
}

func TestConfluencePageSectionGatedReadIssuesOneRequest(t *testing.T) {
	store := &driftingSectionStore{pages: []*domain.Resource{
		{ID: "42", Title: "Example", SpaceKey: "ENG", Version: 3, Body: []byte(sectionTestCSF), BodyPresent: true},
	}}
	if _, err := (&ConfluenceService{store: store}).PageSection(context.Background(), "42", ConfluencePageSectionOpts{
		Heading: "Overview", ExpectedPageVersion: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 {
		t.Fatalf("gated section issued %d page reads, want 1", store.calls)
	}
}

// TestConfluencePageSectionGateRefusesOccurrenceDriftAcrossVersions is the
// regression this binding exists for. An outline is read at one version, the
// page gains another heading with the same title, and the ungated read silently
// resolves the same occurrence number to a different section. The gate turns
// that substitution into a refusal.
func TestConfluencePageSectionGateRefusesOccurrenceDriftAcrossVersions(t *testing.T) {
	store := &driftingSectionStore{pages: []*domain.Resource{
		{ID: "42", Title: "Example", SpaceKey: "ENG", Version: 5, Body: []byte(sectionDriftBeforeCSF), BodyPresent: true},
		{ID: "42", Title: "Example", SpaceKey: "ENG", Version: 6, Body: []byte(sectionDriftAfterCSF), BodyPresent: true},
	}}
	service := &ConfluenceService{store: store}

	outline, err := service.PageOutline(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if outline.Version != 5 || outline.Count != 3 {
		t.Fatalf("outline=%+v want version 5 with three headings", outline)
	}
	statuses := 0
	for _, heading := range outline.Headings {
		if heading.Title == "Status" {
			statuses++
		}
	}
	if statuses != 2 {
		t.Fatalf("outline must show exactly two Status headings before the edit: %+v", outline.Headings)
	}

	// Ungated, occurrence 2 now selects what used to be occurrence 1. Nothing in
	// the result marks the substitution beyond the bumped version.
	drifted, err := service.PageSection(context.Background(), "42", ConfluencePageSectionOpts{Heading: "Status", Occurrence: 2})
	if err != nil {
		t.Fatal(err)
	}
	if drifted.Version != 6 || drifted.PageVersionGated {
		t.Fatalf("ungated read=%+v want version 6 and no gate", drifted)
	}
	if !strings.Contains(drifted.Markdown, "first status") || strings.Contains(drifted.Markdown, "second status") {
		t.Fatalf("drift precondition lost; occurrence 2 must have moved: %q", drifted.Markdown)
	}

	// Bound to the version the outline reported, the same request is refused.
	_, err = service.PageSection(context.Background(), "42", ConfluencePageSectionOpts{
		Heading: "Status", Occurrence: 2, ExpectedPageVersion: outline.Version,
	})
	var mismatch *ConfluencePageVersionMismatchError
	if !errors.As(err, &mismatch) || mismatch.Expected != 5 || mismatch.Current != 6 {
		t.Fatalf("gated read must refuse the drifted page: err=%v mismatch=%+v", err, mismatch)
	}
}

func TestConfluenceStructuralReadsRejectUnreconciledPageIdentity(t *testing.T) {
	for _, test := range []struct {
		name string
		page *domain.Resource
	}{
		{name: "missing id", page: &domain.Resource{Version: 3, Body: []byte(sectionTestCSF), BodyPresent: true}},
		{name: "foreign id", page: &domain.Resource{ID: "43", Version: 3, Body: []byte(sectionTestCSF), BodyPresent: true}},
		{name: "no version", page: &domain.Resource{ID: "42", Body: []byte(sectionTestCSF), BodyPresent: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &ConfluenceService{store: &sectionStore{page: test.page}}
			// An unattributable body must never pass as either an outline or an
			// ungated section success: both outputs make page/revision claims.
			if _, err := service.PageOutline(context.Background(), "42"); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("outline err=%v want check failed", err)
			}
			_, err := service.PageSection(
				context.Background(), "42", ConfluencePageSectionOpts{Heading: "Overview"},
			)
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v want check failed", err)
			}
		})
	}
}

func TestConfluenceStructuralPartialReasonSetsAreClosed(t *testing.T) {
	if !ConfluenceValidOutlinePartialReason(confluencePartialHeadingLimit) ||
		!ConfluenceValidOutlinePartialReason(confluencePartialByteLimit) ||
		ConfluenceValidOutlinePartialReason(confluencePartialMaxBytes) ||
		ConfluenceValidOutlinePartialReason("") || ConfluenceValidOutlinePartialReason("unknown") {
		t.Fatal("outline partial-reason set is not closed to its own limiters")
	}
	if !ConfluenceValidSectionPartialReason(confluencePartialMaxBytes) ||
		!ConfluenceValidSectionPartialReason(confluencePartialInvalidUTF8) ||
		ConfluenceValidSectionPartialReason(confluencePartialHeadingLimit) ||
		ConfluenceValidSectionPartialReason("") || ConfluenceValidSectionPartialReason("unknown") {
		t.Fatal("section partial-reason set is not closed to its own limiters")
	}
}
