package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func parentBudgetContext(t *testing.T, attempts int) (context.Context, *domain.ReadBudget) {
	t.Helper()
	parent, err := domain.NewReadBudget(attempts, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	return domain.WithReadBudget(t.Context(), parent), parent
}

func requireAttemptExhaustion(t *testing.T, err error, parent *domain.ReadBudget, attempts int) {
	t.Helper()
	if !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) {
		var causes []error
		if multi, ok := err.(interface{ Unwrap() []error }); ok {
			causes = multi.Unwrap()
		}
		t.Fatalf("error=%v causes=%v, want attempt exhaustion", err, causes)
	}
	var attempted interface{ DiagnosticWriteAttempted() bool }
	if !errors.As(err, &attempted) || attempted.DiagnosticWriteAttempted() {
		t.Fatalf("error=%v is not typed pre-dispatch refusal", err)
	}
	if got := parent.Usage().Attempts; got != attempts {
		t.Fatalf("parent attempts=%d want=%d", got, attempts)
	}
}

func TestGuardedLinkParentAttemptExhaustionAtEveryStage(t *testing.T) {
	reviewed, err := NewJiraService(JiraDependencies{Tracker: guardedLinkFixture(false), BaseURL: "https://jira.example.test"}).GuardedLink(
		t.Context(), JiraGuardedLinkOpts{Operation: "add", From: "APP-1", To: "OPS-2", Type: "blocks"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, status string
		attempts     int
		write        bool
	}{
		{name: "initial", attempts: 0},
		{name: "prewrite", attempts: 3, status: "blocked"},
		{name: "write admission", attempts: 6, status: "not_applied"},
		{name: "closeout", attempts: 7, status: "outcome_unknown", write: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, parent := parentBudgetContext(t, test.attempts)
			port := guardedLinkFixture(false)
			result, err := NewJiraService(JiraDependencies{Tracker: port, BaseURL: "https://jira.example.test"}).GuardedLink(ctx, JiraGuardedLinkOpts{
				Operation: "add", From: "APP-1", To: "OPS-2", Type: "blocks", Apply: true, ExpectedProposalHash: reviewed.ProposalHash,
			})
			requireAttemptExhaustion(t, err, parent, test.attempts)
			if test.status == "" {
				if result != nil {
					t.Fatalf("initial result=%+v", result)
				}
				return
			}
			if result == nil || result.Status != test.status || result.WriteAttempted != test.write {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

type budgetedGuardedLabelStore struct{ *guardedLabelStore }

func (s *budgetedGuardedLabelStore) ReadGuardedLabelSnapshot(ctx context.Context, reference string) (domain.JiraGuardedLabelSnapshot, error) {
	if err := domain.ReadBudgetFromContext(ctx).TakeAttempt(); err != nil {
		return domain.JiraGuardedLabelSnapshot{}, err
	}
	return s.guardedLabelStore.ReadGuardedLabelSnapshot(ctx, reference)
}

func (s *budgetedGuardedLabelStore) WriteGuardedLabelDelta(ctx context.Context, write domain.JiraGuardedLabelWrite) error {
	if err := domain.ReadBudgetFromContext(ctx).TakeAttempt(); err != nil {
		return err
	}
	return s.guardedLabelStore.WriteGuardedLabelDelta(ctx, write)
}

func TestGuardedLabelsParentAttemptExhaustionAtEveryStage(t *testing.T) {
	const before, after = "2026-08-23T10:00:00Z", "2026-08-23T10:00:01Z"
	reviewed, err := guardedLabelService(&guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{guardedLabelSnapshot(before, "old")}}).GuardedLabels(
		t.Context(), "OPS-1", JiraGuardedLabelOpts{Add: []string{"new"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, status string
		attempts     int
		write        bool
	}{
		{name: "initial", attempts: 0, status: "blocked"},
		{name: "prewrite", attempts: 1, status: "blocked"},
		{name: "write admission", attempts: 2, status: "blocked"},
		{name: "closeout", attempts: 3, status: "outcome_unknown", write: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, parent := parentBudgetContext(t, test.attempts)
			base := &guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{
				guardedLabelSnapshot(before, "old"), guardedLabelSnapshot(before, "old"), guardedLabelSnapshot(after, "new", "old"),
			}}
			result, err := (&JiraService{tr: &budgetedGuardedLabelStore{base}, baseURL: guardedLabelTestBackend}).GuardedLabels(ctx, "OPS-1", JiraGuardedLabelOpts{
				Add: []string{"new"}, Apply: true, ExpectedProposalHash: reviewed.ProposalHash,
			})
			requireAttemptExhaustion(t, err, parent, test.attempts)
			if result == nil || result.Status != test.status || result.WriteAttempted != test.write {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestGuardedCommentParentAttemptExhaustionAtEveryStage(t *testing.T) {
	reviewed := previewBudgetedGuardedComment(t)
	for _, test := range []struct {
		name, status string
		attempts     int
		write        bool
	}{
		{name: "initial", attempts: 0, status: "blocked"},
		{name: "prewrite", attempts: 3, status: "blocked"},
		{name: "write admission", attempts: 6, status: "blocked"},
		{name: "closeout", attempts: 7, status: "outcome_unknown", write: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, parent := parentBudgetContext(t, test.attempts)
			port := &budgetedJiraCommentPort{listPages: []int{1, 1, 1}}
			result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).AddCommentGuarded(ctx, "PROJ-1", JiraCommentAddOpts{
				Body: []byte("body"), Apply: true, ExpectedProposalHash: reviewed.ProposalHash, SatisfactionPolicy: jiraCommentSatisfactionAppendAlways,
			})
			requireAttemptExhaustion(t, err, parent, test.attempts)
			if result == nil || result.Status != test.status || result.WriteAttempted != test.write {
				t.Fatalf("result=%+v", result)
			}
			if test.name == "closeout" && !strings.Contains(err.Error(), "outcome is unknown; do not replay automatically") {
				t.Fatalf("closeout error=%q, want outcome-unknown no-replay guidance", err)
			}
		})
	}
}

type budgetedGuardedFieldPort struct{ *guardedFieldPortStub }

func (p *budgetedGuardedFieldPort) ReadGuardedFieldCatalog(ctx context.Context, selected []string) (domain.JiraGuardedFieldCatalog, error) {
	if err := domain.ReadBudgetFromContext(ctx).TakeAttempt(); err != nil {
		return domain.JiraGuardedFieldCatalog{}, err
	}
	return p.guardedFieldPortStub.ReadGuardedFieldCatalog(ctx, selected)
}
func (p *budgetedGuardedFieldPort) ReadGuardedFieldIssue(ctx context.Context, reference string, selected []string) (domain.JiraGuardedFieldIssue, error) {
	if err := domain.ReadBudgetFromContext(ctx).TakeAttempt(); err != nil {
		return domain.JiraGuardedFieldIssue{}, err
	}
	return p.guardedFieldPortStub.ReadGuardedFieldIssue(ctx, reference, selected)
}
func (p *budgetedGuardedFieldPort) WriteGuardedFields(ctx context.Context, write domain.JiraGuardedFieldWrite) error {
	if err := domain.ReadBudgetFromContext(ctx).TakeAttempt(); err != nil {
		return err
	}
	return p.guardedFieldPortStub.WriteGuardedFields(ctx, write)
}

func TestGuardedFieldsParentAttemptExhaustionAtEveryStage(t *testing.T) {
	proposals := guardedFieldProposals()
	current := map[string]any{"customfield_1": "old", "plugin.vendor": map[string]any{"id": "1"}}
	reviewed := guardedFieldPreview(t, proposals, current)
	for _, test := range []struct {
		name, status string
		attempts     int
		write        bool
	}{
		{name: "initial", attempts: 0, status: "blocked"},
		{name: "prewrite", attempts: 2, status: "blocked"},
		{name: "write admission", attempts: 4, status: "blocked"},
		{name: "closeout", attempts: 5, status: "unknown", write: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, parent := parentBudgetContext(t, test.attempts)
			port := &budgetedGuardedFieldPort{&guardedFieldPortStub{issues: []domain.JiraGuardedFieldIssue{
				guardedFieldIssue("2026-08-23T10:00:00.000+0000", current),
				guardedFieldIssue("2026-08-23T10:00:00.000+0000", current),
				guardedFieldIssue("2026-08-23T10:00:01.000+0000", map[string]any{"customfield_1": "h2. Progress", "plugin.vendor": map[string]any{"id": "2", "large": json.Number("9007199254740993")}}),
			}}}
			result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).SetFieldsGuarded(ctx, "PROJ-1", JiraFieldSetOpts{
				Proposals: proposals, AllowFields: []string{"customfield_1", "plugin.vendor"}, Apply: true,
				ExpectedUpdated: reviewed.ActualUpdated, ExpectedProposalHash: reviewed.ProposalHash,
			})
			requireAttemptExhaustion(t, err, parent, test.attempts)
			if result == nil || result.Status != test.status || result.WriteAttempted != test.write {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}
