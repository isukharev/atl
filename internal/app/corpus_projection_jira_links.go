package app

import (
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

func corpusJiraIssueLinksComplete(raw []any, mapped []domain.IssueLink) bool {
	if len(raw) != len(mapped) {
		return false
	}
	for index, value := range raw {
		link, ok := value.(map[string]any)
		if !ok {
			return false
		}
		inwardValue, inwardPresent := link["inwardIssue"]
		outwardValue, outwardPresent := link["outwardIssue"]
		if inwardPresent == outwardPresent {
			return false
		}
		targetValue := inwardValue
		direction := "inward"
		if outwardPresent {
			targetValue = outwardValue
			direction = "outward"
		}
		target, ok := targetValue.(map[string]any)
		if !ok {
			return false
		}
		key, ok := target["key"].(string)
		linkType, typeOK := link["type"].(map[string]any)
		name, nameOK := linkType["name"].(string)
		phrase, phraseOK := linkType[direction].(string)
		relationNamed := nameOK && strings.TrimSpace(name) != "" || phraseOK && strings.TrimSpace(phrase) != ""
		if !ok || strings.TrimSpace(key) == "" || !typeOK || !relationNamed ||
			mapped[index].Direction != direction || !strings.EqualFold(mapped[index].Key, key) {
			return false
		}
	}
	return true
}
