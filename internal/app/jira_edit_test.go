package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// EditDescription = fetch → textedit splice → write back. These tests pin the
// sentinel mapping (no-match → ErrNotFound, ambiguous → ErrUsage), the exact
// bytes sent to Update, and that nothing is written on dry-run or refusal.

func TestEditDescriptionReplaces(t *testing.T) {
	tr := &recordingTracker{issue: &domain.Issue{Key: "PROJ-1", Body: "a OLD b"}}
	svc := &JiraService{tr: tr}

	before, res, err := svc.EditDescription(context.Background(), "PROJ-1", "OLD", "NEW", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if before != "a OLD b" || res.Text != "a NEW b" {
		t.Errorf("before=%q text=%q", before, res.Text)
	}
	if tr.issueKey != "PROJ-1" || len(tr.issueFields) != 1 || tr.issueFields[0] != "description" {
		t.Errorf("fetch: key=%q fields=%v", tr.issueKey, tr.issueFields)
	}
	if tr.updateKey != "PROJ-1" || string(tr.updateBody) != "a NEW b" {
		t.Errorf("update: key=%q body=%q", tr.updateKey, tr.updateBody)
	}
	if tr.updateSumm != "" || tr.updateFields != nil {
		t.Errorf("update must touch only the description: summary=%q fields=%v", tr.updateSumm, tr.updateFields)
	}
}

func TestEditDescriptionDryRunDoesNotWrite(t *testing.T) {
	tr := &recordingTracker{issue: &domain.Issue{Key: "PROJ-1", Body: "a OLD b"}}
	svc := &JiraService{tr: tr}

	_, res, err := svc.EditDescription(context.Background(), "PROJ-1", "OLD", "NEW", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "a NEW b" {
		t.Errorf("dry-run text = %q", res.Text)
	}
	if tr.updateKey != "" || tr.updateBody != nil {
		t.Errorf("dry-run must not call Update: key=%q body=%q", tr.updateKey, tr.updateBody)
	}
}

func TestEditDescriptionNoMatchIsNotFound(t *testing.T) {
	tr := &recordingTracker{issue: &domain.Issue{Key: "PROJ-1", Body: "a b c"}}
	svc := &JiraService{tr: tr}

	_, _, err := svc.EditDescription(context.Background(), "PROJ-1", "ZZZ", "x", false, false)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("no-match: want ErrNotFound, got %v", err)
	}
	if tr.updateKey != "" {
		t.Error("no-match must not call Update")
	}
}

func TestEditDescriptionAmbiguousIsUsage(t *testing.T) {
	tr := &recordingTracker{issue: &domain.Issue{Key: "PROJ-1", Body: "dup x dup"}}
	svc := &JiraService{tr: tr}

	_, _, err := svc.EditDescription(context.Background(), "PROJ-1", "dup", "y", false, false)
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("ambiguous: want ErrUsage, got %v", err)
	}
	if tr.updateKey != "" {
		t.Error("ambiguous must not call Update")
	}
}

func TestEditDescriptionAllReplacesEvery(t *testing.T) {
	tr := &recordingTracker{issue: &domain.Issue{Key: "PROJ-1", Body: "dup x dup"}}
	svc := &JiraService{tr: tr}

	_, res, err := svc.EditDescription(context.Background(), "PROJ-1", "dup", "y", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "y x y" || len(res.Matches) != 2 {
		t.Errorf("all: text=%q matches=%d", res.Text, len(res.Matches))
	}
	if string(tr.updateBody) != "y x y" {
		t.Errorf("update body = %q", tr.updateBody)
	}
}

func TestEditDescriptionEmptyBodyIsNotFound(t *testing.T) {
	tr := &recordingTracker{issue: &domain.Issue{Key: "PROJ-1", Body: ""}}
	svc := &JiraService{tr: tr}

	_, _, err := svc.EditDescription(context.Background(), "PROJ-1", "x", "y", false, false)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("empty description: want ErrNotFound, got %v", err)
	}
	if tr.updateKey != "" {
		t.Error("empty description must not call Update")
	}
}

func TestEditDescriptionFetchErrorPassesThrough(t *testing.T) {
	tr := &recordingTracker{err: domain.ErrAuth}
	svc := &JiraService{tr: tr}

	_, _, err := svc.EditDescription(context.Background(), "PROJ-1", "x", "y", false, false)
	if !errors.Is(err, domain.ErrAuth) {
		t.Fatalf("fetch error: want ErrAuth passthrough, got %v", err)
	}
}

// The matcher's invisible-tolerant pass must reach the remote description too
// (NBSP in the stored wiki text, plain space in the needle).
func TestEditDescriptionInvisibleTolerant(t *testing.T) {
	tr := &recordingTracker{issue: &domain.Issue{Key: "PROJ-1", Body: "timeout\u00a0= 300"}}
	svc := &JiraService{tr: tr}

	_, res, err := svc.EditDescription(context.Background(), "PROJ-1", "timeout = 300", "timeout = 600", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "timeout = 600" {
		t.Errorf("text = %q", res.Text)
	}
}
