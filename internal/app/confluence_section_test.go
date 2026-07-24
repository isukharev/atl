package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

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

func TestConfluencePageOutlineUsesStructuralBlocks(t *testing.T) {
	result, err := sectionService().PageOutline(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Count != 5 {
		t.Fatalf("result=%+v", result)
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
	service := &ConfluenceService{store: &sectionStore{page: &domain.Resource{ID: "42", Body: []byte(body.String()), BodyPresent: true}}}
	result, err := service.PageOutline(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || !result.Truncated || result.Count != confluenceOutlineHeadingCap || result.Total != confluenceOutlineHeadingCap+1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestConfluencePageOutlineReportsByteCap(t *testing.T) {
	title := strings.Repeat("x", confluenceOutlineByteCap)
	service := &ConfluenceService{store: &sectionStore{page: &domain.Resource{ID: "42", Body: []byte("<h2>" + title + "</h2>"), BodyPresent: true}}}
	result, err := service.PageOutline(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || !result.Truncated || result.Count != 0 || result.Total != 1 || result.OriginalBytes <= confluenceOutlineByteCap || result.EmittedBytes != 0 {
		t.Fatalf("result=%+v", result)
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
}

func TestConfluencePageSectionTruncatesAtBlockBoundary(t *testing.T) {
	result, err := sectionService().PageSection(context.Background(), "42", ConfluencePageSectionOpts{Heading: "Overview", MaxBytes: 80})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || !result.Truncated || !strings.Contains(result.Markdown, "truncated by atl") || result.EmittedBytes > 80 || result.OriginalBytes <= result.EmittedBytes {
		t.Fatalf("result=%+v", result)
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
