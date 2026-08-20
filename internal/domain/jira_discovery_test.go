package domain

import (
	"errors"
	"testing"
)

func TestResolveJiraIssueType(t *testing.T) {
	types := []JiraIssueType{
		{ID: "10", Name: "Task"},
		{ID: "11", Name: "Story"},
		{ID: "12", Name: "Story"},
	}
	tests := []struct {
		name     string
		selector string
		wantID   string
		wantErr  error
	}{
		{name: "id", selector: "10", wantID: "10"},
		{name: "unique exact name", selector: "Task", wantID: "10"},
		{name: "missing", selector: "Missing", wantErr: ErrNotFound},
		{name: "ambiguous name", selector: "Story", wantErr: ErrCheckFailed},
		{name: "empty", selector: " ", wantErr: ErrUsage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveJiraIssueType(types, test.selector)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ResolveJiraIssueType error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr == nil && got.ID != test.wantID {
				t.Fatalf("resolved id = %q, want %q", got.ID, test.wantID)
			}
		})
	}
}
