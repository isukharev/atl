package domain

import (
	"strconv"
	"strings"
)

// ValidConfluenceReadID reports whether value is the bounded opaque content-id
// language accepted by Confluence read routes and page-reference resolution.
// Guarded write paths deliberately retain the stricter positive-decimal
// ValidConfluenceContentID predicate below.
func ValidConfluenceReadID(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

// ValidConfluenceContentID reports whether value is the canonical positive
// decimal spelling accepted by guarded Confluence write paths.
func ValidConfluenceContentID(value string) bool {
	if value == "" || value[0] == '0' || strings.TrimSpace(value) != value {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

// ValidJiraIssueKey reports whether value is the canonical Jira issue-key
// spelling accepted by guarded Jira write paths.
func ValidJiraIssueKey(value string) bool {
	dash := strings.LastIndexByte(value, '-')
	if dash < 2 || dash > 32 || dash == len(value)-1 || value[0] < 'A' || value[0] > 'Z' || value[dash+1] == '0' {
		return false
	}
	for _, char := range value[:dash] {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	for _, char := range value[dash+1:] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
