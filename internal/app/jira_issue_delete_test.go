package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type jiraIssueDeleteStore struct {
	domain.Tracker
	issue          domain.Issue
	issueFn        func(int, string, []string) (*domain.Issue, error)
	writeErr       error
	commit         bool
	readCalls      int
	deleteCalls    int
	lookups        []string
	fields         [][]string
	deletedID      string
	deleteSubtasks bool
	singleAttempt  bool
}

func jiraIssueDeleteFixture() *jiraIssueDeleteStore {
	return &jiraIssueDeleteStore{
		issue: domain.Issue{ID: "10001", Key: "PROJ-1", Fields: map[string]any{
			"updated":  "2026-08-02T20:00:00.000+0000",
			"subtasks": []any{},
		}},
		commit: true,
	}
}

func (s *jiraIssueDeleteStore) GetIssue(_ context.Context, lookup string, fields []string) (*domain.Issue, error) {
	s.readCalls++
	s.lookups = append(s.lookups, lookup)
	s.fields = append(s.fields, append([]string(nil), fields...))
	if s.issueFn != nil {
		return s.issueFn(s.readCalls, lookup, fields)
	}
	if s.commit && s.deleteCalls > 0 {
		return nil, domain.ErrNotFound
	}
	return cloneJiraIssueDeleteIssue(&s.issue), nil
}

func (s *jiraIssueDeleteStore) DeleteIssue(ctx context.Context, selector string, deleteSubtasks bool) error {
	s.deleteCalls++
	s.deletedID = selector
	s.deleteSubtasks = deleteSubtasks
	s.singleAttempt = domain.SingleAttempt(ctx)
	return s.writeErr
}

func cloneJiraIssueDeleteIssue(issue *domain.Issue) *domain.Issue {
	copy := *issue
	copy.Fields = make(map[string]any, len(issue.Fields))
	for key, value := range issue.Fields {
		copy.Fields[key] = value
	}
	return &copy
}

func jiraIssueDeletePreview(t *testing.T, store *jiraIssueDeleteStore, deleteSubtasks bool) *JiraIssueDeleteResult {
	t.Helper()
	result, err := (&JiraService{tr: store, baseURL: "https://jira.example.test"}).DeleteIssueGuarded(
		context.Background(), "PROJ-1", JiraIssueDeleteOpts{DeleteSubtasks: deleteSubtasks})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	return result
}

func jiraIssueDeleteApplyOpts(preview *JiraIssueDeleteResult) JiraIssueDeleteOpts {
	return JiraIssueDeleteOpts{
		Apply: true, Confirm: "DELETE", DeleteSubtasks: preview.DeleteSubtasks,
		ExpectedUpdated: preview.CurrentUpdated, ExpectedProposalHash: preview.ProposalHash,
	}
}

func TestJiraIssueDeleteGuardedPreviewBindsExactEvidence(t *testing.T) {
	store := jiraIssueDeleteFixture()
	result := jiraIssueDeletePreview(t, store, false)
	if result.Status != "would_apply" || result.Mode != "dry-run" || result.Key != "PROJ-1" || result.IssueID != "10001" || result.Subtasks == nil ||
		result.CurrentUpdated == "" || result.ProposalHash == "" || result.IssueIDSHA256 == "" ||
		result.SubtasksSHA256 == "" || result.BackendSHA256 == "" || !result.Complete || !result.PermissionRelative {
		t.Fatalf("unexpected preview: %+v", result)
	}
	if store.readCalls != 1 || store.deleteCalls != 0 || store.lookups[0] != "PROJ-1" ||
		!reflect.DeepEqual(store.fields[0], []string{"updated", "subtasks"}) {
		t.Fatalf("unexpected calls: reads=%d deletes=%d lookups=%v fields=%v", store.readCalls, store.deleteCalls, store.lookups, store.fields)
	}
}

func TestJiraIssueDeleteGuardedSubtasksRequireReviewedCascade(t *testing.T) {
	store := jiraIssueDeleteFixture()
	store.issue.Fields["subtasks"] = []any{map[string]any{"id": "10002", "key": "PROJ-2"}}
	result, err := (&JiraService{tr: store, baseURL: "https://jira.example.test"}).DeleteIssueGuarded(context.Background(), "PROJ-1", JiraIssueDeleteOpts{})
	if result == nil || result.Status != "blocked" || result.SubtaskCount != 1 || !errors.Is(err, domain.ErrCheckFailed) || store.deleteCalls != 0 {
		t.Fatalf("unreviewed cascade: result=%+v err=%v deletes=%d", result, err, store.deleteCalls)
	}

	store = jiraIssueDeleteFixture()
	store.issue.Fields["subtasks"] = []any{map[string]any{"id": "10002", "key": "PROJ-2"}}
	result = jiraIssueDeletePreview(t, store, true)
	if result.Status != "would_apply" || !result.DeleteSubtasks || result.SubtaskCount != 1 || len(result.Subtasks) != 1 || result.Subtasks[0].Key != "PROJ-2" {
		t.Fatalf("reviewed cascade preview: %+v", result)
	}
}

func TestJiraIssueDeleteGuardedRejectsIncompleteOrMalformedEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.Issue)
	}{
		{"missing updated", func(issue *domain.Issue) { delete(issue.Fields, "updated") }},
		{"malformed updated", func(issue *domain.Issue) { issue.Fields["updated"] = "now" }},
		{"missing subtasks", func(issue *domain.Issue) { delete(issue.Fields, "subtasks") }},
		{"null subtasks", func(issue *domain.Issue) { issue.Fields["subtasks"] = nil }},
		{"malformed subtask", func(issue *domain.Issue) {
			issue.Fields["subtasks"] = []any{map[string]any{"id": "x", "key": "PROJ-2"}}
		}},
		{"duplicate subtask", func(issue *domain.Issue) {
			issue.Fields["subtasks"] = []any{
				map[string]any{"id": "10002", "key": "PROJ-2"},
				map[string]any{"id": "10002", "key": "PROJ-3"},
			}
		}},
		{"malformed issue id", func(issue *domain.Issue) { issue.ID = "key-like" }},
		{"moved key", func(issue *domain.Issue) { issue.Key = "PROJ-9" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := jiraIssueDeleteFixture()
			test.mutate(&store.issue)
			result, err := (&JiraService{tr: store, baseURL: "https://jira.example.test"}).DeleteIssueGuarded(context.Background(), "PROJ-1", JiraIssueDeleteOpts{DeleteSubtasks: true})
			if result != nil || !errors.Is(err, domain.ErrCheckFailed) || store.deleteCalls != 0 {
				t.Fatalf("result=%+v err=%v deletes=%d", result, err, store.deleteCalls)
			}
		})
	}
}

func TestJiraIssueDeleteGuardedApplyUsesImmutableIDOnceAndReconciles(t *testing.T) {
	previewStore := jiraIssueDeleteFixture()
	preview := jiraIssueDeletePreview(t, previewStore, false)
	store := jiraIssueDeleteFixture()
	result, err := (&JiraService{tr: store, baseURL: "https://jira.example.test"}).DeleteIssueGuarded(
		context.Background(), "PROJ-1", jiraIssueDeleteApplyOpts(preview))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Status != "applied" || !result.WriteAttempted || !result.Reconciled || result.ObservedState != "absent" ||
		store.deleteCalls != 1 || store.deletedID != "10001" || store.deleteSubtasks || !store.singleAttempt ||
		!reflect.DeepEqual(store.lookups, []string{"PROJ-1", "10001", "10001"}) {
		t.Fatalf("result=%+v store=%+v", result, store)
	}
}

func TestJiraIssueDeleteGuardedApplyPreservesReviewedCascadeIntent(t *testing.T) {
	previewStore := jiraIssueDeleteFixture()
	previewStore.issue.Fields["subtasks"] = []any{map[string]any{"id": "10002", "key": "PROJ-2"}}
	preview := jiraIssueDeletePreview(t, previewStore, true)
	store := jiraIssueDeleteFixture()
	store.issue.Fields["subtasks"] = []any{map[string]any{"id": "10002", "key": "PROJ-2"}}
	result, err := (&JiraService{tr: store, baseURL: "https://jira.example.test"}).DeleteIssueGuarded(
		context.Background(), "PROJ-1", jiraIssueDeleteApplyOpts(preview))
	if err != nil || result.Status != "applied" || store.deleteCalls != 1 || !store.deleteSubtasks || store.deletedID != "10001" {
		t.Fatalf("result=%+v err=%v store=%+v", result, err, store)
	}
}

func TestJiraIssueDeleteGuardedApplyBlocksChangedProposal(t *testing.T) {
	preview := jiraIssueDeletePreview(t, jiraIssueDeleteFixture(), false)
	store := jiraIssueDeleteFixture()
	store.issueFn = func(call int, _ string, _ []string) (*domain.Issue, error) {
		issue := cloneJiraIssueDeleteIssue(&store.issue)
		if call == 2 {
			issue.Fields["updated"] = "2026-08-02T20:01:00.000+0000"
		}
		return issue, nil
	}
	result, err := (&JiraService{tr: store, baseURL: "https://jira.example.test"}).DeleteIssueGuarded(
		context.Background(), "PROJ-1", jiraIssueDeleteApplyOpts(preview))
	if result == nil || result.Status != "blocked" || !errors.Is(err, domain.ErrCheckFailed) || result.WriteAttempted || store.deleteCalls != 0 {
		t.Fatalf("result=%+v err=%v store=%+v", result, err, store)
	}
}

func TestJiraIssueDeleteGuardedDefinitiveRejectionIsNotApplied(t *testing.T) {
	preview := jiraIssueDeletePreview(t, jiraIssueDeleteFixture(), false)
	store := jiraIssueDeleteFixture()
	store.writeErr = transitionStatusError(403)
	result, err := (&JiraService{tr: store, baseURL: "https://jira.example.test"}).DeleteIssueGuarded(
		context.Background(), "PROJ-1", jiraIssueDeleteApplyOpts(preview))
	if result == nil || result.Status != "not_applied" || !result.WriteAttempted || result.Reconciled ||
		!errors.Is(err, domain.ErrForbidden) || strings.Contains(err.Error(), "private backend rejection") || store.deleteCalls != 1 || store.readCalls != 2 {
		t.Fatalf("result=%+v err=%v store=%+v", result, err, store)
	}
}

func TestJiraIssueDeleteGuardedAmbiguousAbsenceRemainsUnknown(t *testing.T) {
	preview := jiraIssueDeletePreview(t, jiraIssueDeleteFixture(), false)
	store := jiraIssueDeleteFixture()
	store.writeErr = context.DeadlineExceeded
	result, err := (&JiraService{tr: store, baseURL: "https://jira.example.test"}).DeleteIssueGuarded(
		context.Background(), "PROJ-1", jiraIssueDeleteApplyOpts(preview))
	var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
	if result == nil || result.Status != "outcome_unknown" || !result.Reconciled || result.ObservedState != "absent" ||
		!errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() || strings.Contains(err.Error(), "deadline") || store.deleteCalls != 1 {
		t.Fatalf("result=%+v err=%v store=%+v", result, err, store)
	}
}

func TestJiraIssueDeleteGuardedVisibleReadbackRemainsUnknown(t *testing.T) {
	preview := jiraIssueDeletePreview(t, jiraIssueDeleteFixture(), false)
	store := jiraIssueDeleteFixture()
	store.commit = false
	result, err := (&JiraService{tr: store, baseURL: "https://jira.example.test"}).DeleteIssueGuarded(
		context.Background(), "PROJ-1", jiraIssueDeleteApplyOpts(preview))
	if result == nil || result.Status != "outcome_unknown" || !result.Reconciled || result.ObservedState != "present" ||
		!errors.Is(err, domain.ErrCheckFailed) || store.deleteCalls != 1 {
		t.Fatalf("result=%+v err=%v store=%+v", result, err, store)
	}
}

func TestJiraIssueDeleteGuardedValidatesInvocationBeforeRead(t *testing.T) {
	tests := []struct {
		name string
		key  string
		opts JiraIssueDeleteOpts
	}{
		{"bad key", "proj-1", JiraIssueDeleteOpts{}},
		{"preview guard", "PROJ-1", JiraIssueDeleteOpts{ExpectedUpdated: "x"}},
		{"bad confirm", "PROJ-1", JiraIssueDeleteOpts{Apply: true, Confirm: "yes", ExpectedUpdated: "x", ExpectedProposalHash: "x"}},
		{"missing updated", "PROJ-1", JiraIssueDeleteOpts{Apply: true, Confirm: "DELETE", ExpectedProposalHash: "x"}},
		{"missing hash", "PROJ-1", JiraIssueDeleteOpts{Apply: true, Confirm: "DELETE", ExpectedUpdated: "x"}},
		{"malformed updated", "PROJ-1", JiraIssueDeleteOpts{Apply: true, Confirm: "DELETE", ExpectedUpdated: "not-a-time", ExpectedProposalHash: strings.Repeat("a", 64)}},
		{"malformed hash", "PROJ-1", JiraIssueDeleteOpts{Apply: true, Confirm: "DELETE", ExpectedUpdated: "2026-08-02T20:00:00.000+0000", ExpectedProposalHash: strings.Repeat("A", 64)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := jiraIssueDeleteFixture()
			result, err := (&JiraService{tr: store, baseURL: "https://jira.example.test"}).DeleteIssueGuarded(context.Background(), test.key, test.opts)
			if result != nil || !errors.Is(err, domain.ErrUsage) || store.readCalls != 0 || store.deleteCalls != 0 {
				t.Fatalf("result=%+v err=%v store=%+v", result, err, store)
			}
		})
	}
}
