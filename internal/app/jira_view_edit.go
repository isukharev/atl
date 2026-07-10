package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

type jiraEditableRegion struct {
	ID       string // description or field id
	FieldID  string
	Start    int
	End      int
	BaseWiki string
}

func jiraEditableRegions(prefix, desc string, fieldRegions []jiraEditableFieldRegion) []jiraEditableRegion {
	regions := []jiraEditableRegion{{ID: "description", Start: len(prefix), End: len(prefix) + len(desc)}}
	offset := len(prefix) + len(desc)
	for _, field := range fieldRegions {
		regions = append(regions, jiraEditableRegion{
			ID: "field." + field.FieldID, FieldID: field.FieldID,
			Start: offset + field.Start, End: offset + field.End, BaseWiki: field.BaseWiki,
		})
	}
	sort.Slice(regions, func(i, j int) bool { return regions[i].Start < regions[j].Start })
	return regions
}

// extractJiraEditableRegions binds edited bodies to the stable region layout
// produced by the pristine renderer. Every byte outside editable bodies must
// remain exact (apart from final newline count), so metadata/comments/markers
// and non-opted fields fail closed.
func extractJiraEditableRegions(edited, pristine string, regions []jiraEditableRegion) (map[string]string, error) {
	values := make(map[string]string, len(regions))
	pristineCursor, editedCursor := 0, 0
	for i, region := range regions {
		if region.Start < pristineCursor || region.End < region.Start || region.End > len(pristine) {
			return nil, fmt.Errorf("invalid editable Jira view region %q", region.ID)
		}
		fixed := pristine[pristineCursor:region.Start]
		if !strings.HasPrefix(edited[editedCursor:], fixed) {
			return nil, fmt.Errorf("%w: generated/read-only Jira view content before %q changed; restore markers, headings, metadata, comments, links, and non-editable fields", domain.ErrCheckFailed, region.ID)
		}
		editedCursor += len(fixed)
		pristineCursor = region.End

		if i == len(regions)-1 {
			tail := pristine[region.End:]
			tail = strings.TrimRight(tail, "\n")
			editedTail := strings.TrimRight(edited[editedCursor:], "\n")
			if tail == "" {
				values[region.ID] = strings.Trim(edited[editedCursor:], "\n")
				editedCursor = len(edited)
				pristineCursor = len(pristine)
				break
			}
			if !strings.HasSuffix(editedTail, tail) {
				return nil, fmt.Errorf("%w: generated/read-only Jira view content after %q changed", domain.ErrCheckFailed, region.ID)
			}
			values[region.ID] = strings.Trim(editedTail[:len(editedTail)-len(tail)], "\n")
			editedCursor = len(edited)
			pristineCursor = len(pristine)
			break
		}

		nextFixed := pristine[region.End:regions[i+1].Start]
		if nextFixed == "" {
			return nil, fmt.Errorf("invalid adjacent editable Jira view regions %q and %q", region.ID, regions[i+1].ID)
		}
		idx := strings.Index(edited[editedCursor:], nextFixed)
		if idx < 0 {
			return nil, fmt.Errorf("%w: generated/read-only Jira view content after %q changed", domain.ErrCheckFailed, region.ID)
		}
		values[region.ID] = strings.Trim(edited[editedCursor:editedCursor+idx], "\n")
		editedCursor += idx
	}
	remainingPristine := strings.TrimRight(pristine[pristineCursor:], "\n")
	remainingEdited := strings.TrimRight(edited[editedCursor:], "\n")
	if remainingEdited != remainingPristine {
		return nil, fmt.Errorf("%w: generated/read-only Jira view suffix changed", domain.ErrCheckFailed)
	}
	return values, nil
}
