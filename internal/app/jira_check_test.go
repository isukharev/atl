package app

import (
	"context"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestCheckReportsMissingFields(t *testing.T) {
	tr := &waveBTracker{issue: &domain.Issue{Key: "K-1", Fields: map[string]any{
		"assignee":    nil,                            // empty
		"priority":    map[string]any{"name": "High"}, // set
		"description": "",                             // empty
		"components":  []any{},                        // empty slice
		"summary":     "has text",                     // set
	}}}
	svc := &JiraService{tr: tr}

	r, err := svc.Check(context.Background(), "K-1",
		[]string{"assignee", "summary"},
		[]string{"priority", "description", "components"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if r.OK {
		t.Error("OK should be false when a required field is empty")
	}
	if len(r.MissingRequired) != 1 || r.MissingRequired[0] != "assignee" {
		t.Errorf("MissingRequired = %v, want [assignee]", r.MissingRequired)
	}
	// priority is set → not in warn; description + components empty → in warn.
	if got := r.MissingWarn; len(got) != 2 || got[0] != "description" || got[1] != "components" {
		t.Errorf("MissingWarn = %v, want [description components]", got)
	}
}

// A field named in both --require and (default) --warn must be reported only as
// required, never duplicated into the warn list.
func TestCheckExcludesRequiredFromWarn(t *testing.T) {
	tr := &waveBTracker{issue: &domain.Issue{Key: "K-3", Fields: map[string]any{
		"assignee": nil, // empty; listed in both require and warn below
		"priority": nil, // empty; warn-only
	}}}
	svc := &JiraService{tr: tr}

	r, err := svc.Check(context.Background(), "K-3",
		[]string{"assignee"},
		[]string{"assignee", "priority"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	for _, w := range r.MissingWarn {
		if w == "assignee" {
			t.Fatalf("assignee reported in both required and warn: %+v", r)
		}
	}
	if len(r.MissingRequired) != 1 || r.MissingRequired[0] != "assignee" {
		t.Errorf("MissingRequired = %v, want [assignee]", r.MissingRequired)
	}
	if len(r.MissingWarn) != 1 || r.MissingWarn[0] != "priority" {
		t.Errorf("MissingWarn = %v, want [priority]", r.MissingWarn)
	}
}

func TestCheckOKWhenRequiredPresent(t *testing.T) {
	tr := &waveBTracker{issue: &domain.Issue{Key: "K-2", Fields: map[string]any{
		"assignee": map[string]any{"displayName": "Jane"},
	}}}
	svc := &JiraService{tr: tr}

	r, err := svc.Check(context.Background(), "K-2", []string{"assignee"}, nil)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !r.OK || len(r.MissingRequired) != 0 {
		t.Errorf("expected OK with no missing required, got %+v", r)
	}
}
