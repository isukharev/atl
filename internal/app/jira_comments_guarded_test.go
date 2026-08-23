package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

type jiraGuardedCommentStub struct {
	domain.Tracker
	actor          domain.JiraGuardedCommentActor
	actorFn        func(int) (domain.JiraGuardedCommentActor, error)
	issueFn        func(int) (domain.JiraGuardedCommentIssue, error)
	inventoryFn    func(int) (domain.JiraCommentInventory, error)
	ack            domain.JiraGuardedCommentAcknowledgement
	writeErr       error
	actorCalls     int
	issueCalls     int
	inventoryCalls int
	writeCalls     int
	singleAttempt  bool
	written        domain.JiraGuardedCommentWrite
	issueRefs      []string
	inventoryIDs   []string
}

type budgetedJiraCommentPort struct {
	domain.Tracker
	listPages             []int
	actorCalls            int
	issueCalls            int
	inventoryCalls        int
	writeCalls            int
	cancel                context.CancelFunc
	cancelOnInventoryCall int
	cancelOnWrite         bool
	waitForWriteDeadline  bool
	firstBudget           *domain.ReadBudget
	firstDeadline         time.Time
	closeoutBudget        *domain.ReadBudget
	closeoutDeadline      time.Time
	closeoutSingle        bool
	closeoutContextErr    error
}

func (p *budgetedJiraCommentPort) take(ctx context.Context, count int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	budget := domain.ReadBudgetFromContext(ctx)
	for range count {
		if err := budget.TakeAttempt(); err != nil {
			return err
		}
	}
	return nil
}

func (p *budgetedJiraCommentPort) ReadGuardedCommentActor(ctx context.Context) (domain.JiraGuardedCommentActor, error) {
	p.actorCalls++
	if p.actorCalls == 1 {
		p.firstBudget = domain.ReadBudgetFromContext(ctx)
		p.firstDeadline, _ = ctx.Deadline()
	}
	if err := p.take(ctx, 1); err != nil {
		return domain.JiraGuardedCommentActor{}, err
	}
	return domain.JiraGuardedCommentActor{Name: "writer", Key: "writer-key", Complete: true}, nil
}

func (p *budgetedJiraCommentPort) ReadGuardedCommentIssue(ctx context.Context, _ string) (domain.JiraGuardedCommentIssue, error) {
	p.issueCalls++
	if p.issueCalls == 3 {
		p.closeoutBudget = domain.ReadBudgetFromContext(ctx)
		p.closeoutDeadline, _ = ctx.Deadline()
		p.closeoutSingle = domain.SingleAttempt(ctx)
		p.closeoutContextErr = ctx.Err()
	}
	if err := p.take(ctx, 1); err != nil {
		return domain.JiraGuardedCommentIssue{}, err
	}
	updated := "2026-08-22T10:00:00Z"
	if p.issueCalls == 3 {
		updated = "2026-08-22T10:00:01Z"
	}
	return domain.JiraGuardedCommentIssue{ID: "101", Key: "PROJ-1", Project: "PROJ", Updated: updated, Complete: true}, nil
}

func (p *budgetedJiraCommentPort) ListJiraCommentsQualified(ctx context.Context, _ string, opts domain.JiraCommentReadOptions) (domain.JiraCommentInventory, error) {
	p.inventoryCalls++
	if opts.MaxPages != 100 || opts.MaxItems != 10_000 || opts.MaxBytes != 16<<20 {
		return domain.JiraCommentInventory{}, errors.New("wrong inventory bounds")
	}
	pages := 1
	if p.inventoryCalls <= len(p.listPages) {
		pages = p.listPages[p.inventoryCalls-1]
	}
	if err := p.take(ctx, pages); err != nil {
		return domain.JiraCommentInventory{}, err
	}
	comments := []domain.Comment{{ID: "9", AuthorName: "other", AuthorKey: "other-key", Created: "2026-08-21T09:00:00Z", Updated: "2026-08-21T09:00:00Z", Body: "existing"}}
	if p.inventoryCalls == 3 {
		comments = append(comments, domain.Comment{ID: "10", AuthorName: "writer", AuthorKey: "writer-key", Created: "2026-08-22T10:00:01Z", Updated: "2026-08-22T10:00:01Z", Body: "body"})
	}
	if p.cancelOnInventoryCall == p.inventoryCalls && p.cancel != nil {
		p.cancel()
	}
	return completeCommentInventory(comments), nil
}

func (p *budgetedJiraCommentPort) WriteGuardedComment(ctx context.Context, _ domain.JiraGuardedCommentWrite) (domain.JiraGuardedCommentAcknowledgement, error) {
	p.writeCalls++
	if err := p.take(ctx, 1); err != nil {
		return domain.JiraGuardedCommentAcknowledgement{}, err
	}
	if p.cancelOnWrite && p.cancel != nil {
		p.cancel()
		return domain.JiraGuardedCommentAcknowledgement{}, context.Canceled
	}
	if p.waitForWriteDeadline {
		<-ctx.Done()
		return domain.JiraGuardedCommentAcknowledgement{}, ctx.Err()
	}
	return domain.JiraGuardedCommentAcknowledgement{ID: "10"}, nil
}

func previewBudgetedGuardedComment(t *testing.T) *JiraCommentAddResult {
	t.Helper()
	result, err := (&JiraService{tr: &budgetedJiraCommentPort{listPages: []int{1}}, baseURL: "https://jira.example.test"}).AddCommentGuarded(
		t.Context(), "PROJ-1", JiraCommentAddOpts{Body: []byte("body")})
	if err != nil || result.Status != "would_apply" {
		t.Fatalf("budgeted preview=%+v err=%v", result, err)
	}
	return result
}

type guardedCommentPlanCompatibilityTracker struct {
	*planTracker
	strictCalls int
}

func (s *guardedCommentPlanCompatibilityTracker) ReadGuardedCommentIssue(context.Context, string) (domain.JiraGuardedCommentIssue, error) {
	s.strictCalls++
	return domain.JiraGuardedCommentIssue{}, errors.New("unexpected strict issue read")
}
func (s *guardedCommentPlanCompatibilityTracker) ReadGuardedCommentActor(context.Context) (domain.JiraGuardedCommentActor, error) {
	s.strictCalls++
	return domain.JiraGuardedCommentActor{}, errors.New("unexpected strict actor read")
}
func (s *guardedCommentPlanCompatibilityTracker) ListJiraCommentsQualified(context.Context, string, domain.JiraCommentReadOptions) (domain.JiraCommentInventory, error) {
	s.strictCalls++
	return domain.JiraCommentInventory{}, errors.New("unexpected strict inventory read")
}
func (s *guardedCommentPlanCompatibilityTracker) WriteGuardedComment(context.Context, domain.JiraGuardedCommentWrite) (domain.JiraGuardedCommentAcknowledgement, error) {
	s.strictCalls++
	return domain.JiraGuardedCommentAcknowledgement{}, errors.New("unexpected strict write")
}

func (s *jiraGuardedCommentStub) ReadGuardedCommentActor(context.Context) (domain.JiraGuardedCommentActor, error) {
	s.actorCalls++
	if s.actorFn != nil {
		return s.actorFn(s.actorCalls)
	}
	return s.actor, nil
}
func (s *jiraGuardedCommentStub) ReadGuardedCommentIssue(_ context.Context, reference string) (domain.JiraGuardedCommentIssue, error) {
	s.issueCalls++
	s.issueRefs = append(s.issueRefs, reference)
	return s.issueFn(s.issueCalls)
}
func (s *jiraGuardedCommentStub) ListJiraCommentsQualified(_ context.Context, issueID string, opts domain.JiraCommentReadOptions) (domain.JiraCommentInventory, error) {
	s.inventoryCalls++
	s.inventoryIDs = append(s.inventoryIDs, issueID)
	if opts.MaxPages != 100 || opts.MaxItems != 10_000 || opts.MaxBytes != 16<<20 {
		return domain.JiraCommentInventory{}, errors.New("wrong bounds")
	}
	return s.inventoryFn(s.inventoryCalls)
}
func (s *jiraGuardedCommentStub) WriteGuardedComment(ctx context.Context, write domain.JiraGuardedCommentWrite) (domain.JiraGuardedCommentAcknowledgement, error) {
	s.writeCalls++
	s.singleAttempt = domain.SingleAttempt(ctx)
	s.written = write
	return s.ack, s.writeErr
}

func guardedCommentFixture() *jiraGuardedCommentStub {
	issue := domain.JiraGuardedCommentIssue{ID: "101", Key: "PROJ-1", Project: "PROJ", Updated: "2026-08-22T10:00:00.000+0000", Complete: true}
	comments := []domain.Comment{{
		ID: "9", AuthorName: "other", AuthorKey: "other-key", Created: "2026-08-21T09:00:00Z",
		Updated: "2026-08-21T09:00:00Z", Body: "existing",
	}}
	return &jiraGuardedCommentStub{
		actor:       domain.JiraGuardedCommentActor{Name: "writer", Key: "writer-key", Complete: true},
		issueFn:     func(int) (domain.JiraGuardedCommentIssue, error) { return issue, nil },
		inventoryFn: func(int) (domain.JiraCommentInventory, error) { return completeCommentInventory(comments), nil },
	}
}

func completeCommentInventory(comments []domain.Comment) domain.JiraCommentInventory {
	return domain.JiraCommentInventory{Comments: append([]domain.Comment(nil), comments...), Complete: true, Total: len(comments), TotalKnown: true, PageCount: 1}
}

func previewGuardedComment(t *testing.T, stub *jiraGuardedCommentStub, body string, policy string) *JiraCommentAddResult {
	t.Helper()
	result, err := (&JiraService{tr: stub, baseURL: "https://jira.example.test"}).AddCommentGuarded(t.Context(), "proj-1", JiraCommentAddOpts{Body: []byte(body), SatisfactionPolicy: policy})
	if err != nil || result.Status != "would_apply" && result.Status != "already_satisfied" || result.ProposalHash == "" || !result.Complete {
		t.Fatalf("preview=%+v err=%v", result, err)
	}
	return result
}

func TestJiraGuardedCommentPreviewIsContentMinimizedAndOrderStable(t *testing.T) {
	stub := guardedCommentFixture()
	first := stub.inventoryFn
	stub.inventoryFn = func(call int) (domain.JiraCommentInventory, error) {
		inventory, err := first(call)
		inventory.Comments = append(inventory.Comments, domain.Comment{
			ID: "2", AuthorName: "writer", AuthorKey: "writer-key", Created: "2026-08-20T08:00:00Z",
			Updated: "2026-08-20T08:00:00Z", Body: "reviewed body",
		})
		inventory.Total = len(inventory.Comments)
		return inventory, err
	}
	preview := previewGuardedComment(t, stub, "reviewed body", jiraCommentSatisfactionAppendAlways)
	if preview.SatisfactionPolicy != "append_always" || preview.ExactBodyCount != 1 || preview.CurrentCount != 2 ||
		preview.ActorSHA256 == "" || preview.BaselineSHA256 == "" || preview.BodySHA256 == "" {
		t.Fatalf("preview=%+v", preview)
	}
	wire, err := json.Marshal(preview)
	if err != nil || bytes.Contains(wire, []byte("reviewed body")) || bytes.Contains(wire, []byte("writer-key")) || bytes.Contains(wire, []byte(`"actor"`)) || bytes.Contains(wire, []byte(`"body"`)) {
		t.Fatalf("content leaked: %s err=%v", wire, err)
	}
	if strings.Contains(JiraCommentAddText(preview), "reviewed body") || strings.Contains(JiraCommentAddText(preview), "writer-key") {
		t.Fatalf("text leaked: %q", JiraCommentAddText(preview))
	}
	stub.inventoryFn = func(call int) (domain.JiraCommentInventory, error) {
		inventory, _ := first(call)
		inventory.Comments = append([]domain.Comment{{
			ID: "2", AuthorName: "writer", AuthorKey: "writer-key", Created: "2026-08-20T08:00:00Z",
			Updated: "2026-08-20T08:00:00Z", Body: "reviewed body",
		}}, inventory.Comments...)
		inventory.Total = len(inventory.Comments)
		return inventory, nil
	}
	reordered := previewGuardedComment(t, stub, "reviewed body", jiraCommentSatisfactionAppendAlways)
	if reordered.ProposalHash != preview.ProposalHash || reordered.BaselineSHA256 != preview.BaselineSHA256 {
		t.Fatalf("reordered=%+v preview=%+v", reordered, preview)
	}
}

func TestJiraGuardedCommentInitialQualificationFailureRetainsBaseAuditFacts(t *testing.T) {
	stub := guardedCommentFixture()
	stub.actorFn = func(int) (domain.JiraGuardedCommentActor, error) {
		return domain.JiraGuardedCommentActor{}, errors.New("private malformed actor response")
	}
	result, err := (&JiraService{tr: stub, baseURL: "https://jira.example.test"}).AddCommentGuarded(t.Context(), "PROJ-1", JiraCommentAddOpts{
		Body: []byte("reviewed body"), SatisfactionPolicy: jiraCommentSatisfactionAppendAlways,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.Status != "blocked" || result.Complete ||
		result.BackendSHA256 == "" || result.BodySHA256 != sha256Hex([]byte("reviewed body")) || result.BodyBytes != len("reviewed body") ||
		result.RequestedKey != "PROJ-1" || result.Operation != "jira_issue_comment_append" || strings.Contains(err.Error(), "private malformed actor response") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestJiraCommentAddTextIsExactCompleteAuditProjection(t *testing.T) {
	result := &JiraCommentAddResult{
		SchemaVersion: 1, Operation: "jira_issue_comment_append", SatisfactionPolicy: "append_always",
		BackendSHA256: "backend", RequestedKey: "PROJ-1", IssueID: "101", Key: "PROJ-1", Project: "PROJ",
		Updated: "before", ReadbackUpdated: "after", BodySHA256: "body", BodyBytes: 4, ActorSHA256: "actor",
		CurrentCount: 2, BaselineSHA256: "baseline", ExactBodyCount: 1,
		Bounds: JiraCommentBounds{MaxKeyBytes: 1, MaxBodyBytes: 2, MaxEvidenceIDBytes: 3, MaxEvidenceMetadataBytes: 4,
			MaxPages: 5, MaxItems: 6, MaxInventoryBytes: 7, PreviewMaxRequests: 8, ApplyMaxRequests: 9,
			MaxAggregateResponseBytes: 10, DeadlineMillis: 11},
		Usage: JiraCommentUsage{Requests: 12, ResponseBytes: 13}, Mode: "apply", Status: "recovered",
		ProposalHash: "proposal", CommentID: "14", WriteAttempted: true, Reconciled: true, Complete: true,
	}
	want := "schema_version: 1\noperation: jira_issue_comment_append\nsatisfaction_policy: append_always\nbackend_sha256: backend\nrequested_key: PROJ-1\nissue_id: 101\nkey: PROJ-1\nproject: PROJ\nupdated: before\nreadback_updated: after\nbody_sha256: body\nbody_bytes: 4\nactor_sha256: actor\ncurrent_count: 2\nbaseline_sha256: baseline\nexact_body_count: 1\nbounds.max_key_bytes: 1\nbounds.max_body_bytes: 2\nbounds.max_evidence_id_bytes: 3\nbounds.max_evidence_metadata_bytes: 4\nbounds.max_pages: 5\nbounds.max_items: 6\nbounds.max_inventory_bytes: 7\nbounds.preview_max_requests: 8\nbounds.apply_max_requests: 9\nbounds.max_aggregate_response_bytes: 10\nbounds.deadline_millis: 11\nusage.requests: 12\nusage.response_bytes: 13\nmode: apply\nstatus: recovered\nproposal_hash: proposal\ncomment_id: 14\nwrite_attempted: true\nreconciled: true\ncomplete: true"
	if got := JiraCommentAddText(result); got != want {
		t.Fatalf("text mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestJiraGuardedCommentApplyUsesImmutableIDAndExactReadback(t *testing.T) {
	previewStub := guardedCommentFixture()
	preview := previewGuardedComment(t, previewStub, "native *wiki*\n", jiraCommentSatisfactionAppendAlways)
	stub := guardedCommentFixture()
	baseIssue, _ := stub.issueFn(1)
	stub.issueFn = func(call int) (domain.JiraGuardedCommentIssue, error) {
		issue := baseIssue
		if call == 3 {
			issue.Updated = "2026-08-22T10:00:01.123+0000"
		}
		return issue, nil
	}
	baseInventory, _ := stub.inventoryFn(1)
	stub.inventoryFn = func(call int) (domain.JiraCommentInventory, error) {
		inventory := baseInventory
		if call == 3 {
			inventory.Comments = append(inventory.Comments,
				domain.Comment{ID: "10", AuthorName: "writer", AuthorKey: "writer-key", Created: "2026-08-22T10:00:01Z", Updated: "2026-08-22T10:00:01Z", Body: "native *wiki*\n"},
				domain.Comment{ID: "11", AuthorName: "other", AuthorKey: "other-key", Created: "2026-08-22T10:00:01Z", Updated: "2026-08-22T10:00:01Z", Body: "unrelated"})
			inventory.Total = len(inventory.Comments)
		}
		return inventory, nil
	}
	stub.ack.ID = "10"
	result, err := (&JiraService{tr: stub, baseURL: "https://jira.example.test"}).AddCommentGuarded(t.Context(), "PROJ-1", JiraCommentAddOpts{
		Body: []byte("native *wiki*\n"), Apply: true, ExpectedProposalHash: preview.ProposalHash, SatisfactionPolicy: jiraCommentSatisfactionAppendAlways,
	})
	if err != nil || result.Status != "applied" || result.CommentID != "10" || !result.WriteAttempted || !result.Reconciled || !result.Complete ||
		stub.writeCalls != 1 || stub.actorCalls != 2 || stub.issueCalls != 3 || stub.inventoryCalls != 3 || !stub.singleAttempt ||
		stub.written.ID != "101" || stub.written.Key != "PROJ-1" || stub.written.Project != "PROJ" || string(stub.written.Body) != "native *wiki*\n" ||
		!slices.Equal(stub.issueRefs, []string{"PROJ-1", "101", "101"}) || !slices.Equal(stub.inventoryIDs, []string{"101", "101", "101"}) {
		t.Fatalf("result=%+v err=%v calls=%d/%d/%d/%d write=%+v", result, err, stub.actorCalls, stub.issueCalls, stub.inventoryCalls, stub.writeCalls, stub.written)
	}
}

type guardedCommentStatusError int

func (e guardedCommentStatusError) Error() string   { return "private backend body" }
func (e guardedCommentStatusError) HTTPStatus() int { return int(e) }

type guardedCommentNoAttemptStubError struct{}

func (guardedCommentNoAttemptStubError) Error() string                  { return "private policy detail" }
func (guardedCommentNoAttemptStubError) Unwrap() error                  { return domain.ErrForbidden }
func (guardedCommentNoAttemptStubError) DiagnosticWriteAttempted() bool { return false }

func TestJiraGuardedCommentTypedPredispatchRefusalClearsAttempt(t *testing.T) {
	preview := previewGuardedComment(t, guardedCommentFixture(), "body", jiraCommentSatisfactionAppendAlways)
	stub := guardedCommentFixture()
	stub.writeErr = guardedCommentNoAttemptStubError{}
	result, err := (&JiraService{tr: stub, baseURL: "https://jira.example.test"}).AddCommentGuarded(t.Context(), "PROJ-1", JiraCommentAddOpts{
		Body: []byte("body"), Apply: true, ExpectedProposalHash: preview.ProposalHash, SatisfactionPolicy: jiraCommentSatisfactionAppendAlways,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, domain.ErrForbidden) || strings.Contains(err.Error(), "private policy detail") ||
		result.Status != "blocked" || !result.Complete || result.WriteAttempted || result.Reconciled || stub.writeCalls != 1 || stub.inventoryCalls != 2 {
		t.Fatalf("result=%+v err=%v writes=%d inventory=%d", result, err, stub.writeCalls, stub.inventoryCalls)
	}
}

func TestJiraGuardedCommentClosedOutcomesAndNoReplay(t *testing.T) {
	for _, test := range []struct {
		name       string
		writeErr   error
		ack        string
		newRows    []domain.Comment
		advance    bool
		changeBase bool
		readErr    bool
		wantStatus string
		wantErr    bool
	}{
		{name: "definitive rejection", writeErr: guardedCommentStatusError(400), wantStatus: "not_applied", wantErr: true},
		{name: "ambiguous recovered", writeErr: context.DeadlineExceeded, advance: true, wantStatus: "recovered", newRows: []domain.Comment{{ID: "10", AuthorName: "writer", AuthorKey: "writer-key", Created: "2026-08-22T10:00:01Z", Updated: "2026-08-22T10:00:01Z", Body: "body"}}},
		{name: "ambiguous with acknowledgement recovered", writeErr: context.DeadlineExceeded, ack: "10", advance: true, wantStatus: "recovered", newRows: []domain.Comment{{ID: "10", AuthorName: "writer", AuthorKey: "writer-key", Created: "2026-08-22T10:00:01Z", Updated: "2026-08-22T10:00:01Z", Body: "body"}}},
		{name: "empty acknowledgement recovered", advance: true, wantStatus: "recovered", newRows: []domain.Comment{{ID: "10", AuthorName: "writer", AuthorKey: "writer-key", Created: "2026-08-22T10:00:01Z", Updated: "2026-08-22T10:00:01Z", Body: "body"}}},
		{name: "unrelated addition allowed", writeErr: context.DeadlineExceeded, advance: true, wantStatus: "recovered", newRows: []domain.Comment{
			{ID: "10", AuthorName: "writer", AuthorKey: "writer-key", Created: "2026-08-22T10:00:01Z", Updated: "2026-08-22T10:00:01Z", Body: "body"},
			{ID: "11", AuthorName: "other", AuthorKey: "other-key", Created: "2026-08-22T10:00:01Z", Updated: "2026-08-22T10:00:01Z", Body: "other"},
		}},
		{name: "acknowledged id mismatch", ack: "99", advance: true, wantStatus: "outcome_unknown", wantErr: true, newRows: []domain.Comment{{ID: "10", AuthorName: "writer", AuthorKey: "writer-key", Created: "2026-08-22T10:00:01Z", Updated: "2026-08-22T10:00:01Z", Body: "body"}}},
		{name: "zero candidate", writeErr: context.DeadlineExceeded, advance: true, wantStatus: "outcome_unknown", wantErr: true},
		{name: "nonadvancing", writeErr: context.DeadlineExceeded, wantStatus: "outcome_unknown", wantErr: true, newRows: []domain.Comment{{ID: "10", AuthorName: "writer", AuthorKey: "writer-key", Created: "2026-08-22T10:00:01Z", Updated: "2026-08-22T10:00:01Z", Body: "body"}}},
		{name: "changed baseline", writeErr: context.DeadlineExceeded, advance: true, changeBase: true, wantStatus: "outcome_unknown", wantErr: true, newRows: []domain.Comment{{ID: "10", AuthorName: "writer", AuthorKey: "writer-key", Created: "2026-08-22T10:00:01Z", Updated: "2026-08-22T10:00:01Z", Body: "body"}}},
		{name: "readback unavailable", writeErr: context.DeadlineExceeded, readErr: true, wantStatus: "outcome_unknown", wantErr: true},
		{name: "multiple ambiguous", writeErr: context.DeadlineExceeded, advance: true, wantStatus: "outcome_unknown", wantErr: true, newRows: []domain.Comment{
			{ID: "10", AuthorName: "writer", AuthorKey: "writer-key", Created: "2026-08-22T10:00:01Z", Updated: "2026-08-22T10:00:01Z", Body: "body"},
			{ID: "11", AuthorName: "writer", AuthorKey: "writer-key", Created: "2026-08-22T10:00:01Z", Updated: "2026-08-22T10:00:01Z", Body: "body"},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			preview := previewGuardedComment(t, guardedCommentFixture(), "body", jiraCommentSatisfactionAppendAlways)
			stub := guardedCommentFixture()
			issue, _ := stub.issueFn(1)
			stub.issueFn = func(call int) (domain.JiraGuardedCommentIssue, error) {
				value := issue
				if call == 3 && test.advance {
					value.Updated = "2026-08-22T10:00:01Z"
				}
				return value, nil
			}
			base, _ := stub.inventoryFn(1)
			stub.inventoryFn = func(call int) (domain.JiraCommentInventory, error) {
				if call == 3 && test.readErr {
					return domain.JiraCommentInventory{}, errors.New("private readback")
				}
				value := base
				if call == 3 {
					if test.changeBase {
						value.Comments[0].Body = "changed"
					}
					value.Comments = append(value.Comments, test.newRows...)
					value.Total = len(value.Comments)
				}
				return value, nil
			}
			stub.writeErr, stub.ack.ID = test.writeErr, test.ack
			result, err := (&JiraService{tr: stub, baseURL: "https://jira.example.test"}).AddCommentGuarded(t.Context(), "PROJ-1", JiraCommentAddOpts{
				Body: []byte("body"), Apply: true, ExpectedProposalHash: preview.ProposalHash, SatisfactionPolicy: jiraCommentSatisfactionAppendAlways,
			})
			wantReconciled := test.name != "definitive rejection" && !test.readErr
			wantComplete := !test.readErr
			if result.Status != test.wantStatus || (err != nil) != test.wantErr || stub.writeCalls != 1 || !result.WriteAttempted ||
				result.Reconciled != wantReconciled || result.Complete != wantComplete {
				t.Fatalf("result=%+v err=%v writes=%d", result, err, stub.writeCalls)
			}
			if test.readErr && (result.Reconciled || result.Complete) {
				t.Fatalf("unavailable readback result=%+v", result)
			}
			if err != nil && strings.Contains(err.Error(), "private backend body") {
				t.Fatalf("error leaked: %v", err)
			}
		})
	}
}

func TestJiraGuardedCommentHashBeforeExactBodyNoopAndDrift(t *testing.T) {
	stub := guardedCommentFixture()
	base, _ := stub.inventoryFn(1)
	base.Comments = append(base.Comments, domain.Comment{ID: "10", AuthorName: "other", AuthorKey: "other-key", Created: "2026-08-22T09:00:00Z", Updated: "2026-08-22T09:00:00Z", Body: "present"})
	base.Total = len(base.Comments)
	stub.inventoryFn = func(int) (domain.JiraCommentInventory, error) { return base, nil }
	preview := previewGuardedComment(t, stub, "present", jiraCommentSatisfactionExactBodyPresent)
	result, err := (&JiraService{tr: stub, baseURL: "https://jira.example.test"}).AddCommentGuarded(t.Context(), "PROJ-1", JiraCommentAddOpts{
		Body: []byte("present"), Apply: true, ExpectedProposalHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SatisfactionPolicy: jiraCommentSatisfactionExactBodyPresent,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || stub.writeCalls != 0 {
		t.Fatalf("hash-before-noop result=%+v err=%v", result, err)
	}
	result, err = (&JiraService{tr: stub, baseURL: "https://jira.example.test"}).AddCommentGuarded(t.Context(), "PROJ-1", JiraCommentAddOpts{
		Body: []byte("present"), Apply: true, ExpectedProposalHash: preview.ProposalHash, SatisfactionPolicy: jiraCommentSatisfactionExactBodyPresent,
	})
	if err != nil || result.Status != "already_satisfied" || stub.writeCalls != 0 {
		t.Fatalf("noop result=%+v err=%v", result, err)
	}
}

func TestJiraGuardedCommentPrewriteDriftAndMissingStrictPortBlockWithoutPOST(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*jiraGuardedCommentStub)
	}{
		{name: "actor", mutate: func(stub *jiraGuardedCommentStub) {
			stub.actorFn = func(call int) (domain.JiraGuardedCommentActor, error) {
				actor := stub.actor
				if call == 2 {
					actor.Key = "changed-key"
				}
				return actor, nil
			}
		}},
		{name: "issue updated", mutate: func(stub *jiraGuardedCommentStub) {
			base := stub.issueFn
			stub.issueFn = func(call int) (domain.JiraGuardedCommentIssue, error) {
				issue, err := base(call)
				if call == 2 {
					issue.Updated = "2026-08-22T10:00:01Z"
				}
				return issue, err
			}
		}},
		{name: "inventory", mutate: func(stub *jiraGuardedCommentStub) {
			base := stub.inventoryFn
			stub.inventoryFn = func(call int) (domain.JiraCommentInventory, error) {
				inventory, err := base(call)
				if call == 2 {
					inventory.Comments[0].Body = "changed"
				}
				return inventory, err
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			preview := previewGuardedComment(t, guardedCommentFixture(), "body", jiraCommentSatisfactionAppendAlways)
			stub := guardedCommentFixture()
			test.mutate(stub)
			result, err := (&JiraService{tr: stub, baseURL: "https://jira.example.test"}).AddCommentGuarded(t.Context(), "PROJ-1", JiraCommentAddOpts{
				Body: []byte("body"), Apply: true, ExpectedProposalHash: preview.ProposalHash, SatisfactionPolicy: jiraCommentSatisfactionAppendAlways,
			})
			if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || !result.Complete || result.WriteAttempted || stub.writeCalls != 0 {
				t.Fatalf("result=%+v err=%v writes=%d", result, err, stub.writeCalls)
			}
		})
	}

	legacy := &planTracker{}
	result, err := (&JiraService{tr: legacy, baseURL: "https://jira.example.test"}).AddCommentGuarded(t.Context(), "PROJ-1", JiraCommentAddOpts{Body: []byte("body")})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || result.Complete || legacy.commentCalls != 0 {
		t.Fatalf("fallback result=%+v err=%v legacy_writes=%d", result, err, legacy.commentCalls)
	}
}

func TestJiraGuardedCommentPhysicalRequestBoundsAtNAndNPlusOne(t *testing.T) {
	preview := previewBudgetedGuardedComment(t)
	for _, test := range []struct {
		name       string
		apply      bool
		listPages  []int
		wantStatus string
		wantUsage  int
		wantErr    bool
	}{
		{name: "preview N 102", listPages: []int{100}, wantStatus: "would_apply", wantUsage: 102},
		{name: "preview N plus 1", listPages: []int{101}, wantStatus: "blocked", wantUsage: 102, wantErr: true},
		{name: "apply N 306", apply: true, listPages: []int{100, 100, 100}, wantStatus: "applied", wantUsage: 306},
		{name: "apply N plus 1", apply: true, listPages: []int{100, 100, 101}, wantStatus: "outcome_unknown", wantUsage: 306, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			port := &budgetedJiraCommentPort{listPages: test.listPages}
			opts := JiraCommentAddOpts{Body: []byte("body"), SatisfactionPolicy: jiraCommentSatisfactionAppendAlways}
			if test.apply {
				opts.Apply, opts.ExpectedProposalHash = true, preview.ProposalHash
			}
			result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).AddCommentGuarded(t.Context(), "PROJ-1", opts)
			if result == nil || result.Status != test.wantStatus || result.Usage.Requests != test.wantUsage || (err != nil) != test.wantErr {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if test.wantErr && !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error=%v, want terminal check failure", err)
			}
		})
	}
}

type aggregateExhaustionCommentPort struct{ domain.Tracker }

func (aggregateExhaustionCommentPort) ReadGuardedCommentActor(ctx context.Context) (domain.JiraGuardedCommentActor, error) {
	budget := domain.ReadBudgetFromContext(ctx)
	if err := budget.TakeAttempt(); err != nil {
		return domain.JiraGuardedCommentActor{}, err
	}
	remaining, finish, err := budget.BeginResponse(ctx)
	if err != nil {
		return domain.JiraGuardedCommentActor{}, err
	}
	finish(remaining)
	return domain.JiraGuardedCommentActor{Name: "writer", Key: "writer-key", Complete: true}, nil
}

func (aggregateExhaustionCommentPort) ReadGuardedCommentIssue(ctx context.Context, _ string) (domain.JiraGuardedCommentIssue, error) {
	budget := domain.ReadBudgetFromContext(ctx)
	if err := budget.TakeAttempt(); err != nil {
		return domain.JiraGuardedCommentIssue{}, err
	}
	remaining, finish, err := budget.BeginResponse(ctx)
	if err != nil {
		return domain.JiraGuardedCommentIssue{}, err
	}
	finish(0)
	if remaining == 0 {
		return domain.JiraGuardedCommentIssue{}, domain.ErrReadResponseBudgetExhausted
	}
	return domain.JiraGuardedCommentIssue{}, errors.New("aggregate fixture retained unexpected capacity")
}

func (aggregateExhaustionCommentPort) ListJiraCommentsQualified(context.Context, string, domain.JiraCommentReadOptions) (domain.JiraCommentInventory, error) {
	return domain.JiraCommentInventory{}, errors.New("inventory unexpectedly reached")
}
func (aggregateExhaustionCommentPort) WriteGuardedComment(context.Context, domain.JiraGuardedCommentWrite) (domain.JiraGuardedCommentAcknowledgement, error) {
	return domain.JiraGuardedCommentAcknowledgement{}, errors.New("write unexpectedly reached")
}

func TestJiraGuardedCommentAggregateAndInventoryByteExhaustionRemainDistinct(t *testing.T) {
	inventoryPort := guardedCommentFixture()
	inventoryPort.inventoryFn = func(int) (domain.JiraCommentInventory, error) {
		return domain.JiraCommentInventory{Complete: false, PartialReason: "byte_limit", PageCount: 1}, nil
	}
	for _, test := range []struct {
		name              string
		port              domain.Tracker
		wantResponseBytes int64
	}{
		{name: "retained inventory limit", port: inventoryPort, wantResponseBytes: 0},
		{name: "aggregate transport limit", port: aggregateExhaustionCommentPort{}, wantResponseBytes: 16 << 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := (&JiraService{tr: test.port, baseURL: "https://jira.example.test"}).AddCommentGuarded(t.Context(), "PROJ-1", JiraCommentAddOpts{Body: []byte("body")})
			if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || result.Usage.ResponseBytes != test.wantResponseBytes {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestJiraGuardedCommentAbsoluteDeadlineAndCloseoutContext(t *testing.T) {
	preview := previewBudgetedGuardedComment(t)
	t.Run("deadline expired before dispatch", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		defer cancel()
		port := &budgetedJiraCommentPort{listPages: []int{1, 1}}
		result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).AddCommentGuarded(ctx, "PROJ-1", JiraCommentAddOpts{
			Body: []byte("body"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
		})
		if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || result.WriteAttempted || port.writeCalls != 0 {
			t.Fatalf("result=%+v err=%v writes=%d", result, err, port.writeCalls)
		}
	})
	t.Run("absolute deadline expires after dispatch", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		port := &budgetedJiraCommentPort{listPages: []int{1, 1, 1}, waitForWriteDeadline: true}
		result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).AddCommentGuarded(ctx, "PROJ-1", JiraCommentAddOpts{
			Body: []byte("body"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
		})
		if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "outcome_unknown" || !result.WriteAttempted || result.Reconciled || result.Complete ||
			port.writeCalls != 1 || port.closeoutBudget != port.firstBudget || !port.closeoutSingle || !errors.Is(port.closeoutContextErr, context.DeadlineExceeded) ||
			port.firstDeadline.IsZero() || !port.closeoutDeadline.Equal(port.firstDeadline) {
			t.Fatalf("result=%+v err=%v budget=%p/%p single=%t context_err=%v deadlines=%v/%v", result, err,
				port.firstBudget, port.closeoutBudget, port.closeoutSingle, port.closeoutContextErr, port.firstDeadline, port.closeoutDeadline)
		}
	})
	t.Run("canceled after dispatch", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		port := &budgetedJiraCommentPort{listPages: []int{1, 1, 1}, cancel: cancel, cancelOnWrite: true}
		result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).AddCommentGuarded(ctx, "PROJ-1", JiraCommentAddOpts{
			Body: []byte("body"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
		})
		if err != nil || result.Status != "recovered" || !result.WriteAttempted || !result.Reconciled || port.writeCalls != 1 ||
			port.firstBudget == nil || port.closeoutBudget != port.firstBudget || !port.closeoutSingle || port.closeoutContextErr != nil ||
			port.firstDeadline.IsZero() || !port.closeoutDeadline.Equal(port.firstDeadline) || time.Until(port.closeoutDeadline) > jiraGuardedCommentDeadline {
			t.Fatalf("result=%+v err=%v budget=%p/%p single=%t context_err=%v deadlines=%v/%v", result, err,
				port.firstBudget, port.closeoutBudget, port.closeoutSingle, port.closeoutContextErr, port.firstDeadline, port.closeoutDeadline)
		}
	})
}

func TestValidateJiraCommentBodyIsBoundedUTF8NonemptyAndByteStable(t *testing.T) {
	body := []byte("  native *wiki*\n")
	got, err := ValidateJiraCommentBody(body)
	if err != nil || string(got) != string(body) {
		t.Fatalf("body=%q err=%v", got, err)
	}
	for _, invalid := range [][]byte{nil, []byte(" \n\t"), {0xff}, make([]byte, JiraCommentBodyMaxBytes+1)} {
		if _, err := ValidateJiraCommentBody(invalid); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("input length=%d err=%v", len(invalid), err)
		}
	}
	if exact, err := ValidateJiraCommentBody(bytes.Repeat([]byte("x"), JiraCommentBodyMaxBytes)); err != nil || len(exact) != JiraCommentBodyMaxBytes {
		t.Fatalf("exact body limit len=%d err=%v", len(exact), err)
	}
}

func TestGuardedCommentTimestampQualificationIsStrict(t *testing.T) {
	valid := []string{
		"2026-08-22T10:00:00Z",
		"2026-08-22T10:00:00.123456789Z",
		"2026-08-22T10:00:00+00:00",
		"2026-08-22T10:00:00.123+0000",
		"2026-08-22T10:00:00+0000",
	}
	for _, timestamp := range valid {
		inventory := completeCommentInventory([]domain.Comment{{
			ID: "1", AuthorName: "writer", AuthorKey: "writer-key", Created: timestamp, Updated: timestamp, Body: "body",
		}})
		if _, err := qualifyGuardedCommentInventory(inventory); err != nil {
			t.Fatalf("valid timestamp %q: %v", timestamp, err)
		}
	}
	for _, timestamps := range [][2]string{
		{"2026-08-22", "2026-08-22"},
		{"2026-08-22T10:00:00", "2026-08-22T10:00:00"},
		{"2026-08-22T10:00:00,123Z", "2026-08-22T10:00:00,123Z"},
		{"2026-08-22T10:00:00.Z", "2026-08-22T10:00:00.Z"},
		{"2026-08-22T10:00:00.1234567890Z", "2026-08-22T10:00:00.1234567890Z"},
		{"2026-08-22T10:00:00z", "2026-08-22T10:00:00z"},
		{"2026-08-22T10:00:01Z", "2026-08-22T10:00:00Z"},
		{" 2026-08-22T10:00:00Z", "2026-08-22T10:00:00Z"},
	} {
		inventory := completeCommentInventory([]domain.Comment{{
			ID: "1", AuthorName: "writer", AuthorKey: "writer-key", Created: timestamps[0], Updated: timestamps[1], Body: "body",
		}})
		if _, err := qualifyGuardedCommentInventory(inventory); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("invalid timestamps %q/%q err=%v", timestamps[0], timestamps[1], err)
		}
	}
}

func TestGuardedCommentPortDoesNotChangeCSVPlanBehaviorOrBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.csv")
	if err := os.WriteFile(path, []byte("version,op,source,value,expected_updated\n1,comment,PROJ-1,hello,2026-01-01\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tracker := &guardedCommentPlanCompatibilityTracker{planTracker: &planTracker{issues: map[string]domain.Issue{
		"PROJ-1": {Key: "PROJ-1", Fields: map[string]any{"updated": "2026-01-01"}},
	}}}
	result, err := (&JiraService{tr: tracker}).ApplyPlan(t.Context(), JiraPlanApplyOpts{
		CSVPath: path, Apply: true, Confirm: planApplyConfirm, AllowOps: []string{"comment"},
	})
	if err != nil || tracker.strictCalls != 0 || tracker.commentCalls != 1 || tracker.commentKey != "PROJ-1" || tracker.commentBody != "hello" {
		t.Fatalf("result=%+v err=%v strict=%d legacy=%d/%q/%q", result, err, tracker.strictCalls, tracker.commentCalls, tracker.commentKey, tracker.commentBody)
	}
	wire, err := json.Marshal(result)
	quotedPath, _ := json.Marshal(path)
	want := `{"version":1,"path":` + string(quotedPath) + `,"mode":"apply","count":1,"results":[{"row":2,"op":"comment","source":"PROJ-1","value":"hello","expected_updated":"2026-01-01","status":"applied"}]}`
	if err != nil || string(wire) != want {
		t.Fatalf("CSV result bytes=%s err=%v want=%s", wire, err, want)
	}
}
