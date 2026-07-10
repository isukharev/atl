package app

import (
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestExtractJiraEditableRegionsRejectsReservedMarkersInsideValues(t *testing.T) {
	pristine := "prefixDESC<next>FIELDtail"
	regions := []jiraEditableRegion{
		{ID: "description", Start: len("prefix"), End: len("prefixDESC")},
		{ID: "field.customfield_1", FieldID: "customfield_1", Start: len("prefixDESC<next>"), End: len("prefixDESC<next>FIELD")},
	}
	for _, marker := range []string{"<!-- atl:section field.customfield_1 editable -->", "<!-- atl:document jira-issue -->"} {
		edited := "prefixNEW " + marker + "<next>FIELDtail"
		if _, err := extractJiraEditableRegions(edited, pristine, regions); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("marker %q error = %v, want check failure", marker, err)
		}
	}
}

func TestExtractJiraEditableRegionsRejectsCopiedNextBoundary(t *testing.T) {
	boundary := "<!-- atl:section field.customfield_1 editable --><next>"
	pristine := "prefixDESC" + boundary + "FIELDtail"
	regions := []jiraEditableRegion{
		{ID: "description", Start: len("prefix"), End: len("prefixDESC")},
		{ID: "field.customfield_1", FieldID: "customfield_1", Start: len("prefixDESC") + len(boundary), End: len("prefixDESC") + len(boundary) + len("FIELD")},
	}
	// This models an exact copy of the following generated boundary inside the
	// first editable value. Without the reserved-marker check, the first Index
	// match truncates Description and moves its remainder into the field value.
	edited := "prefixNEW" + boundary + "REST" + boundary + "FIELDtail"
	if _, err := extractJiraEditableRegions(edited, pristine, regions); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("copied boundary error = %v, want check failure", err)
	}
}
