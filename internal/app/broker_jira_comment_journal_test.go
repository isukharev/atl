//go:build linux || darwin

package app

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/adapter/brokerjournal"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type countingBrokerCommentJournal struct {
	domain.BrokerJournal
	lookups int
}

type barrierBrokerCommentJournal struct {
	domain.BrokerJournal
	mu      sync.Mutex
	lookups int
	ready   chan struct{}
}

func (j *barrierBrokerCommentJournal) Lookup(ctx context.Context, owner domain.BrokerJournalOwner, id string) (domain.BrokerJournalRecord, error) {
	j.mu.Lock()
	j.lookups++
	if j.lookups == 2 {
		close(j.ready)
	}
	j.mu.Unlock()
	select {
	case <-ctx.Done():
		return domain.BrokerJournalRecord{}, ctx.Err()
	case <-j.ready:
		return j.BrokerJournal.Lookup(ctx, owner, id)
	}
}

func (j *countingBrokerCommentJournal) Lookup(ctx context.Context, owner domain.BrokerJournalOwner, id string) (domain.BrokerJournalRecord, error) {
	j.lookups++
	return j.BrokerJournal.Lookup(ctx, owner, id)
}

func TestBrokerJiraCommentRealJournalReopenRejectsExactDuplicate(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "applied"
		if unknown {
			name = "outcome_unknown"
		}
		t.Run(name, func(t *testing.T) {
			service, authorizer, port, _, _, events := brokerJiraCommentFixture(t, 1)
			now := time.Now()
			verified := brokerJiraCommentContext()
			verified.ExecutionNotBeforeMillis = now.Add(-time.Second).UnixMilli()
			verified.ExecutionExpiresMillis = now.Add(time.Minute).UnixMilli()
			verified.GrantExpiresMillis = verified.ExecutionExpiresMillis
			verified.CredentialExpiresMillis = verified.ExecutionExpiresMillis
			authorizer.nowMillis = now.UnixMilli()
			service.now = time.Now
			owner, err := brokercontract.BrokerJournalOwnerV1(verified)
			if err != nil {
				t.Fatal(err)
			}
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "journal")
			identity := brokerjournal.Identity{BrokerSHA256: owner.BrokerSHA256, BackendSHA256: owner.BackendSHA256}
			limits := brokerjournal.Limits{Records: 1, ReservedBytes: 8 << 20}
			journal, err := brokerjournal.Create(path, identity, limits)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if journal != nil {
					_ = journal.Close()
				}
			})
			service.journal = journal
			preview, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), verified)
			if err != nil {
				t.Fatal(err)
			}
			port.unknownReadback = unknown
			request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
			result, err := service.Execute(t.Context(), request, verified)
			if (err != nil) != unknown || result.Comment.Status != name || port.writeCalls != 1 {
				t.Fatalf("result=%+v error=%v writes=%d", result, err, port.writeCalls)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			journal, err = brokerjournal.Open(path, identity, limits)
			if err != nil {
				t.Fatal(err)
			}
			service.journal = journal
			record, err := journal.Lookup(t.Context(), owner, preview.Comment.OperationTicket)
			if err != nil || string(record.Phase) != name || !record.DispatchClaimed || record.ArtifactBytes == 0 {
				t.Fatalf("record=%+v error=%v", record, err)
			}
			fenced, err := journal.Fenced(t.Context(), record.Intent.TargetSHA256)
			if err != nil || fenced != unknown {
				t.Fatalf("fenced=%t error=%v", fenced, err)
			}
			// Keep the exact original proposal visible: rejection must reach the
			// reopened ticket phase, not succeed accidentally through hash drift.
			port.hideWritten = true
			*events = nil
			observed := &countingBrokerCommentJournal{BrokerJournal: journal}
			service.journal = observed
			_, err = service.Execute(t.Context(), request, verified)
			if err == nil || port.writeCalls != 1 || observed.lookups != 1 || containsOrdered(*events, []string{"proposal_authorization"}) {
				t.Fatalf("duplicate error=%v writes=%d lookups=%d events=%v", err, port.writeCalls, observed.lookups, *events)
			}
		})
	}
}

func TestBrokerJiraCommentRealJournalConcurrentDuplicateDispatchesOnce(t *testing.T) {
	now := time.Now()
	verified := brokerJiraCommentContext()
	verified.ExecutionNotBeforeMillis = now.Add(-time.Second).UnixMilli()
	verified.ExecutionExpiresMillis = now.Add(time.Minute).UnixMilli()
	verified.GrantExpiresMillis = verified.ExecutionExpiresMillis
	verified.CredentialExpiresMillis = verified.ExecutionExpiresMillis
	owner, err := brokercontract.BrokerJournalOwnerV1(verified)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := brokerjournal.Create(filepath.Join(parent, "journal"), brokerjournal.Identity{BrokerSHA256: owner.BrokerSHA256, BackendSHA256: owner.BackendSHA256}, brokerjournal.Limits{Records: 1, ReservedBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })

	first, firstAuthorizer, firstPort, _, firstPreflight, _ := brokerJiraCommentFixture(t, 1)
	firstAuthorizer.nowMillis = now.UnixMilli()
	first.now = time.Now
	first.journal = journal
	preview, err := first.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), verified)
	if err != nil {
		t.Fatal(err)
	}
	second, secondAuthorizer, secondPort, _, secondPreflight, _ := brokerJiraCommentFixture(t, 1)
	secondAuthorizer.nowMillis = now.UnixMilli()
	second.now = time.Now
	barrier := &barrierBrokerCommentJournal{BrokerJournal: journal, ready: make(chan struct{})}
	first.journal, second.journal = barrier, barrier
	first.preflight, second.preflight = firstPreflight, secondPreflight
	request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)

	results := make(chan error, 2)
	ctx := t.Context()
	go func() {
		_, applyErr := first.Execute(ctx, request, verified)
		results <- applyErr
	}()
	go func() {
		_, applyErr := second.Execute(ctx, request, verified)
		results <- applyErr
	}()
	firstErr, secondErr := <-results, <-results
	if (firstErr == nil) == (secondErr == nil) || firstPort.writeCalls+secondPort.writeCalls != 1 {
		t.Fatalf("errors=%v/%v writes=%d/%d", firstErr, secondErr, firstPort.writeCalls, secondPort.writeCalls)
	}
	record, err := journal.Lookup(t.Context(), owner, preview.Comment.OperationTicket)
	if err != nil || record.Phase != domain.BrokerOperationApplied || !record.DispatchClaimed {
		t.Fatalf("record=%+v error=%v", record, err)
	}
}

func TestBrokerJiraCommentRealJournalActiveTargetFenceDeniesSecondTicket(t *testing.T) {
	service, authorizer, port, _, _, _ := brokerJiraCommentFixture(t, 1)
	now := time.Now()
	verified := brokerJiraCommentContext()
	verified.ExecutionNotBeforeMillis = now.Add(-time.Second).UnixMilli()
	verified.ExecutionExpiresMillis = now.Add(time.Minute).UnixMilli()
	verified.GrantExpiresMillis = verified.ExecutionExpiresMillis
	verified.CredentialExpiresMillis = verified.ExecutionExpiresMillis
	authorizer.nowMillis = now.UnixMilli()
	service.now = time.Now
	owner, err := brokercontract.BrokerJournalOwnerV1(verified)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := brokerjournal.Create(filepath.Join(parent, "journal"), brokerjournal.Identity{BrokerSHA256: owner.BrokerSHA256, BackendSHA256: owner.BackendSHA256}, brokerjournal.Limits{Records: 2, ReservedBytes: 12 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	service.journal = journal
	first, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), verified)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), verified)
	if err != nil {
		t.Fatal(err)
	}
	port.unknownReadback, port.hideWritten = true, true
	firstRequest := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, first.Comment.ProposalHash, first.Comment.OperationTicket)
	if _, err := service.Execute(t.Context(), firstRequest, verified); err == nil {
		t.Fatal("ambiguous first ticket unexpectedly succeeded")
	}
	secondRequest := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, second.Comment.ProposalHash, second.Comment.OperationTicket)
	if _, err := service.Execute(t.Context(), secondRequest, verified); err == nil || port.writeCalls != 1 {
		t.Fatalf("second error=%v writes=%d", err, port.writeCalls)
	}
	firstRecord, err := journal.Lookup(t.Context(), owner, first.Comment.OperationTicket)
	if err != nil || firstRecord.Phase != domain.BrokerOperationOutcomeUnknown {
		t.Fatalf("first record=%+v error=%v", firstRecord, err)
	}
	secondRecord, err := journal.Lookup(t.Context(), owner, second.Comment.OperationTicket)
	if err != nil || secondRecord.Phase != "" || secondRecord.DispatchClaimed {
		t.Fatalf("second record=%+v error=%v", secondRecord, err)
	}
}
