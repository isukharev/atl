package app

import (
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestValidateJiraIssueGraphKeyRequiresExactCanonicalIdentity(t *testing.T) {
	for _, key := range []string{"PROJ-1", "A_2-999"} {
		if err := ValidateJiraIssueGraphKey(key); err != nil {
			t.Errorf("ValidateJiraIssueGraphKey(%q): %v", key, err)
		}
	}
	for _, key := range []string{"", "proj-1", " PROJ-1", "PROJ-1 ", "PROJ-0", "P-1"} {
		if err := ValidateJiraIssueGraphKey(key); !errors.Is(err, domain.ErrUsage) {
			t.Errorf("ValidateJiraIssueGraphKey(%q) error=%v, want usage", key, err)
		}
	}
}

func TestValidateJiraIssueGraphResultIsLoadBearing(t *testing.T) {
	service, _ := traversalService(map[string]*domain.QualifiedIssueSnapshot{
		"PROJ-1": traversalSnapshot("PROJ-1", nil, ""),
	})
	result, err := service.IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateJiraIssueGraphResult(result); err != nil {
		t.Fatalf("valid result: %v", err)
	}
	result.Summary.NodeCount++
	if err := ValidateJiraIssueGraphResult(result); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("mutated result error=%v, want check failure", err)
	}
}
