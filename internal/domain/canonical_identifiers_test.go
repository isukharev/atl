package domain

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var legacyAnchoredJiraIssueKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,31}-[1-9][0-9]*$`)

func legacyCLIValidJiraIssueKey(value string) bool {
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

func legacyValidConfluenceContentID(value string) bool {
	if value == "" || value[0] == '0' || strings.TrimSpace(value) != value {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

func TestCanonicalIdentifierPredicatesMatchLegacyLanguages(t *testing.T) {
	candidates := []string{
		"", "0", "00", "01", "1", "9", "10", "18446744073709551615", "18446744073709551616",
		" A-1", "A-1 ", "+1", "-1", "A-0", "A-01", "A-1", "AB-1", "A_-9", "A_B2-10",
		"aB-1", "AB--1", "AB-", "AB-١", "AB-18446744073709551616",
	}
	for prefixLength := 1; prefixLength <= 34; prefixLength++ {
		prefix := "A" + strings.Repeat("B", prefixLength-1)
		candidates = append(candidates, prefix+"-1", prefix+"-10", prefix+"-"+strings.Repeat("9", 40))
	}

	alphabet := []byte{'A', 'Z', '0', '1', '_', '-', 'a', ' '}
	var appendWords func([]byte, int)
	appendWords = func(prefix []byte, remaining int) {
		candidates = append(candidates, string(prefix))
		if remaining == 0 {
			return
		}
		for _, char := range alphabet {
			appendWords(append(prefix, char), remaining-1)
		}
	}
	appendWords(nil, 5)

	for _, value := range candidates {
		jiraCLI := legacyCLIValidJiraIssueKey(value)
		jiraApp := legacyAnchoredJiraIssueKey.MatchString(value)
		if jiraCLI != jiraApp {
			t.Fatalf("legacy Jira predicates disagree for %q: cli=%t app=%t", value, jiraCLI, jiraApp)
		}
		if got := ValidJiraIssueKey(value); got != jiraCLI {
			t.Fatalf("ValidJiraIssueKey(%q)=%t, want legacy %t", value, got, jiraCLI)
		}
		if got := ValidConfluenceContentID(value); got != legacyValidConfluenceContentID(value) {
			t.Fatalf("ValidConfluenceContentID(%q)=%t, want legacy %t", value, got, legacyValidConfluenceContentID(value))
		}
	}
}
