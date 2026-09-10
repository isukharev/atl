package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func TestBrokerAttachmentStreamStartRejectsInvalidInputsBeforeIO(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*attachmentAppFixture, *BrokerAttachmentStreamStart)
	}{
		{name: "request", mutate: func(_ *attachmentAppFixture, value *BrokerAttachmentStreamStart) { value.Request.SchemaVersion = 2 }},
		{name: "backend", mutate: func(_ *attachmentAppFixture, value *BrokerAttachmentStreamStart) {
			value.Verified.Backend.WorkloadBackendID = "other"
		}},
		{name: "stream", mutate: func(_ *attachmentAppFixture, value *BrokerAttachmentStreamStart) { value.StreamID = "" }},
		{name: "correlation", mutate: func(_ *attachmentAppFixture, value *BrokerAttachmentStreamStart) { value.CorrelationID = "bad value" }},
		{name: "tail", mutate: func(_ *attachmentAppFixture, value *BrokerAttachmentStreamStart) {
			value.ScannerTailBytes = brokerAttachmentMaxTailBytes + 1
		}},
		{name: "origin mismatch", mutate: func(f *attachmentAppFixture, _ *BrokerAttachmentStreamStart) { f.jira.origin = stringsOf('2', 64) }},
		{name: "origin error", mutate: func(f *attachmentAppFixture, _ *BrokerAttachmentStreamStart) {
			f.jira.originErr = domain.ErrCheckFailed
		}},
		{name: "authentication expired", mutate: func(f *attachmentAppFixture, value *BrokerAttachmentStreamStart) {
			value.InitialAuthenticationDeadline = f.clock.Now()
		}},
		{name: "authentication too long", mutate: func(f *attachmentAppFixture, value *BrokerAttachmentStreamStart) {
			value.InitialAuthenticationDeadline = f.clock.Now().Add(6 * time.Second)
		}},
		{name: "budget reused", mutate: func(f *attachmentAppFixture, _ *BrokerAttachmentStreamStart) {
			ctx, _ := f.budgets.AuthenticationContext(f.ctx)
			_ = attachmentAppCharge(ctx, 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttachmentAppFixture(t, []byte("body"), 4, 7)
			input := BrokerAttachmentStreamStart{Request: fixture.request, Verified: fixture.verified, InitialAuthenticationDeadline: fixture.clock.Now().Add(5 * time.Second), StreamID: "stream-1", CorrelationID: "correlation-1", ScannerTailBytes: 7, Budgets: fixture.budgets}
			test.mutate(fixture, &input)
			operation, err := fixture.service.Start(fixture.ctx, input)
			if operation != nil || err == nil {
				t.Fatalf("operation=%v err=%v", operation, err)
			}
			if fixture.authorizer.calls.Load() != 0 || fixture.jira.qualifyCalls.Load() != 0 || fixture.jira.prepareCalls.Load() != 0 || fixture.jira.openCalls.Load() != 0 {
				t.Fatalf("invalid input reached I/O: authority=%d metadata=%d prepare=%d open=%d", fixture.authorizer.calls.Load(), fixture.jira.qualifyCalls.Load(), fixture.jira.prepareCalls.Load(), fixture.jira.openCalls.Load())
			}
		})
	}
}

type attachmentAppPortWithoutOrigin struct{ jira *attachmentAppJira }

func (p attachmentAppPortWithoutOrigin) QualifyBrokerJiraAttachment(ctx context.Context, issueKey, attachmentID string) (domain.BrokerJiraAttachmentSnapshotV3, error) {
	return p.jira.QualifyBrokerJiraAttachment(ctx, issueKey, attachmentID)
}

func (p attachmentAppPortWithoutOrigin) PrepareBrokerJiraAttachment(ctx context.Context, snapshot domain.BrokerJiraAttachmentSnapshotV3) (domain.BrokerJiraAttachmentOpenHandleV3, error) {
	return p.jira.PrepareBrokerJiraAttachment(ctx, snapshot)
}

func TestBrokerAttachmentStreamConstructorRequiresOriginQualifiedPort(t *testing.T) {
	fixture := newAttachmentAppFixture(t, nil, 0, 7)
	service, err := NewBrokerJiraAttachmentStreamService(fixture.authorizer, BrokerJiraAttachmentStreamReader{Backend: fixture.backend, Reader: attachmentAppPortWithoutOrigin{jira: fixture.jira}})
	if service != nil || err == nil || fixture.authorizer.calls.Load() != 0 || fixture.jira.qualifyCalls.Load() != 0 {
		t.Fatalf("service=%v err=%v authority=%d metadata=%d", service, err, fixture.authorizer.calls.Load(), fixture.jira.qualifyCalls.Load())
	}
}

func TestBrokerAttachmentStreamSetupOrderBudgetsAndDrift(t *testing.T) {
	fixture := newAttachmentAppFixture(t, []byte("body"), 4, 7)
	operation := fixture.start(t, 7)
	if want := []string{"admission", "qualification_initial", "metadata", "operation_initial", "qualification_pre_open", "prepare", "operation_body_dispatch", "open"}; !reflect.DeepEqual(fixture.order, want) {
		t.Fatalf("order=%v want=%v", fixture.order, want)
	}
	if !fixture.budgets.validUsage(1, 5, 2, 1) {
		t.Fatalf("setup usage auth=%+v decision=%+v metadata=%+v body=%+v", fixture.budgets.authentication.Usage(), fixture.budgets.decision.Usage(), fixture.budgets.metadata.Usage(), fixture.budgets.body.Usage())
	}
	if err := operation.Close(); err != nil || fixture.jira.bodyCloses.Load() != 1 || fixture.jira.handleCloses.Load() != 1 {
		t.Fatalf("close=%v body=%d handle=%d", err, fixture.jira.bodyCloses.Load(), fixture.jira.handleCloses.Load())
	}

	for _, test := range []struct {
		name   string
		mutate func(*attachmentAppFixture)
	}{
		{name: "metadata drift", mutate: func(f *attachmentAppFixture) { f.jira.driftAt = 1 }},
		{name: "prepared snapshot drift", mutate: func(f *attachmentAppFixture) { f.jira.prepareDrift = true }},
		{name: "admission expires during qualification", mutate: func(f *attachmentAppFixture) {
			f.authorizer.advancePhase, f.authorizer.advanceAmount = "qualification_initial", 6*time.Second
		}},
		{name: "body dispatch denied", mutate: func(f *attachmentAppFixture) {
			f.authorizer.failPhase, f.authorizer.err = "operation_body_dispatch", domain.ErrForbidden
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newAttachmentAppFixture(t, []byte("body"), 4, 7)
			test.mutate(f)
			operation, err := f.service.Start(f.ctx, BrokerAttachmentStreamStart{Request: f.request, Verified: f.verified, InitialAuthenticationDeadline: f.clock.Now().Add(5 * time.Second), StreamID: "stream-1", CorrelationID: "correlation-1", ScannerTailBytes: 7, Budgets: f.budgets})
			if operation != nil || err == nil {
				t.Fatalf("operation=%v err=%v", operation, err)
			}
			if test.name == "body dispatch denied" && f.jira.openCalls.Load() != 0 {
				t.Fatal("body opened after dispatch denial")
			}
			if f.jira.prepareCalls.Load() > 0 && f.jira.handleCloses.Load() != 1 {
				t.Fatalf("prepared handle closes=%d", f.jira.handleCloses.Load())
			}
		})
	}
}

func TestBrokerAttachmentStreamSourceFailuresCloseWithoutDisclosure(t *testing.T) {
	for _, test := range []struct {
		name     string
		bodySize int
		declared int64
		mode     string
	}{
		{name: "short", bodySize: 7, declared: 8},
		{name: "native error", bodySize: 7, declared: 7, mode: "error"},
		{name: "no progress", bodySize: 7, declared: 7, mode: "no-progress"},
		{name: "sixteen MiB overflow", bodySize: 16<<20 + 1, declared: 16 << 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttachmentAppFixture(t, bytes.Repeat([]byte{'x'}, test.bodySize), test.declared, 31)
			fixture.jira.readMode = test.mode
			operation := fixture.start(t, 31)
			var tail []byte
			for {
				candidate, err := operation.ReadCandidate(tail)
				if err != nil {
					break
				}
				payloadBytes := min(len(candidate.Bytes), int(brokercontract.MaxAttachmentDecodedFrameBytesV3))
				selection := BrokerAttachmentReleaseSelection{CandidateID: candidate.ID, Payload: candidate.Bytes[:payloadBytes], RetainedTail: candidate.Bytes[payloadBytes:]}
				authorized, err := operation.AuthorizeRelease(fixture.fresh(t), selection)
				if err != nil {
					t.Fatalf("authorization before source failure: %v", err)
				}
				complete, err := operation.CommitRelease(candidate.ID, attachmentAppLineHashes(t, authorized.Lines))
				if err != nil || complete {
					t.Fatalf("premature terminal complete=%t err=%v", complete, err)
				}
				tail = selection.RetainedTail
			}
			if operation.state != brokerAttachmentStreamClosed || fixture.jira.bodyCloses.Load() != 1 || fixture.jira.handleCloses.Load() != 1 {
				t.Fatalf("state=%d body_close=%d handle_close=%d", operation.state, fixture.jira.bodyCloses.Load(), fixture.jira.handleCloses.Load())
			}
		})
	}
}

func TestBrokerAttachmentStreamRejectsCandidateAndTailMutationBeforeReleaseIO(t *testing.T) {
	fixture := newAttachmentAppFixture(t, bytes.Repeat([]byte{'x'}, 2<<20), 2<<20, 31)
	operation := fixture.start(t, 31)
	candidate, err := operation.ReadCandidate(nil)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Bytes[0] ^= 1
	selection := BrokerAttachmentReleaseSelection{CandidateID: candidate.ID, Payload: candidate.Bytes[:1<<20], RetainedTail: candidate.Bytes[1<<20:]}
	beforeAuthority, beforeMetadata := fixture.authorizer.calls.Load(), fixture.jira.qualifyCalls.Load()
	if _, err := operation.AuthorizeRelease(BrokerAttachmentFreshAuthorization{}, selection); err == nil {
		t.Fatal("mutated candidate accepted")
	}
	if fixture.authorizer.calls.Load() != beforeAuthority || fixture.jira.qualifyCalls.Load() != beforeMetadata {
		t.Fatal("mutated candidate reached release I/O")
	}

	fixture = newAttachmentAppFixture(t, bytes.Repeat([]byte{'y'}, 2<<20), 2<<20, 31)
	operation = fixture.start(t, 31)
	candidate, _ = operation.ReadCandidate(nil)
	selection = BrokerAttachmentReleaseSelection{CandidateID: candidate.ID, Payload: candidate.Bytes[:1<<20], RetainedTail: candidate.Bytes[1<<20:]}
	authorized, err := operation.AuthorizeRelease(fixture.fresh(t), selection)
	if err != nil {
		t.Fatal(err)
	}
	if complete, err := operation.CommitRelease(candidate.ID, attachmentAppLineHashes(t, authorized.Lines)); err != nil || complete {
		t.Fatalf("first commit complete=%t err=%v", complete, err)
	}
	badTail := bytes.Clone(selection.RetainedTail)
	badTail[0] ^= 1
	beforeAuthority, beforeMetadata = fixture.authorizer.calls.Load(), fixture.jira.qualifyCalls.Load()
	if _, err := operation.ReadCandidate(badTail); err == nil {
		t.Fatal("mutated retained tail accepted")
	}
	if fixture.authorizer.calls.Load() != beforeAuthority || fixture.jira.qualifyCalls.Load() != beforeMetadata {
		t.Fatal("mutated tail reached release I/O")
	}
}

func TestBrokerAttachmentStreamCommitIsOnlyStateAdvance(t *testing.T) {
	fixture := newAttachmentAppFixture(t, []byte("native attachment bytes"), 23, 7)
	operation := fixture.start(t, 7)
	candidate, err := operation.ReadCandidate(nil)
	if err != nil {
		t.Fatal(err)
	}
	selection := BrokerAttachmentReleaseSelection{CandidateID: candidate.ID, Payload: candidate.Bytes}
	prior, count, cumulative := operation.priorReleaseSHA256, operation.committedReleaseCount, operation.committedBytes
	authorized, err := operation.AuthorizeRelease(fixture.fresh(t), selection)
	if err != nil {
		t.Fatal(err)
	}
	if operation.priorReleaseSHA256 != prior || operation.committedReleaseCount != count || operation.committedBytes != cumulative || len(authorized.Lines) != 3 {
		t.Fatal("authorization installed tentative state before flush")
	}
	wrong := attachmentAppLineHashes(t, authorized.Lines)
	wrong[0] = stringsOf('f', 64)
	if complete, err := operation.CommitRelease(candidate.ID, wrong); err == nil || complete {
		t.Fatalf("wrong commit complete=%t err=%v", complete, err)
	}
	if operation.priorReleaseSHA256 != prior || operation.committedReleaseCount != count || operation.committedBytes != cumulative {
		t.Fatal("failed commit advanced state")
	}
}

func TestBrokerAttachmentStreamReleaseLeaseAndMetadataRemainCurrent(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*attachmentAppFixture)
	}{
		{name: "metadata drift", mutate: func(f *attachmentAppFixture) { f.jira.driftAt = 2 }},
		{name: "release decision expires", mutate: func(f *attachmentAppFixture) {
			f.authorizer.advancePhase, f.authorizer.advanceAmount = "operation_release", 6*time.Second
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttachmentAppFixture(t, []byte("body"), 4, 7)
			operation := fixture.start(t, 7)
			candidate, err := operation.ReadCandidate(nil)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(fixture)
			beforePrior := operation.priorReleaseSHA256
			if _, err := operation.AuthorizeRelease(fixture.fresh(t), BrokerAttachmentReleaseSelection{CandidateID: candidate.ID, Payload: candidate.Bytes}); err == nil {
				t.Fatal("stale release accepted")
			}
			if operation.priorReleaseSHA256 != beforePrior || operation.committedReleaseCount != 0 || operation.state != brokerAttachmentStreamClosed {
				t.Fatalf("prior=%q releases=%d state=%d", operation.priorReleaseSHA256, operation.committedReleaseCount, operation.state)
			}
		})
	}

	fixture := newAttachmentAppFixture(t, []byte("body"), 4, 7)
	operation := fixture.start(t, 7)
	candidate, _ := operation.ReadCandidate(nil)
	fresh := fixture.fresh(t)
	authorized, err := operation.AuthorizeRelease(fresh, BrokerAttachmentReleaseSelection{CandidateID: candidate.ID, Payload: candidate.Bytes})
	if err != nil {
		t.Fatal(err)
	}
	if authorized.FlushNotAfter.After(fresh.ReleaseDeadline) {
		t.Fatalf("flush=%v authentication=%v", authorized.FlushNotAfter, fresh.ReleaseDeadline)
	}
	if complete, err := operation.CommitRelease(candidate.ID, attachmentAppLineHashes(t, authorized.Lines)); err != nil || !complete {
		t.Fatalf("complete=%t err=%v", complete, err)
	}
}

func TestBrokerAttachmentStreamInvalidTransitionsAndSplitsDoNoReleaseIO(t *testing.T) {
	for _, test := range []struct {
		name   string
		invoke func(*testing.T, *attachmentAppFixture, *BrokerJiraAttachmentStreamOperation, BrokerAttachmentReadCandidate)
	}{
		{name: "second candidate", invoke: func(t *testing.T, _ *attachmentAppFixture, operation *BrokerJiraAttachmentStreamOperation, _ BrokerAttachmentReadCandidate) {
			if _, err := operation.ReadCandidate(nil); err == nil {
				t.Fatal("second candidate accepted")
			}
		}},
		{name: "stale id", invoke: func(t *testing.T, fixture *attachmentAppFixture, operation *BrokerJiraAttachmentStreamOperation, candidate BrokerAttachmentReadCandidate) {
			selection := BrokerAttachmentReleaseSelection{CandidateID: candidate.ID + 1, Payload: candidate.Bytes}
			if _, err := operation.AuthorizeRelease(fixture.fresh(t), selection); err == nil {
				t.Fatal("stale candidate id accepted")
			}
		}},
		{name: "bad split", invoke: func(t *testing.T, fixture *attachmentAppFixture, operation *BrokerJiraAttachmentStreamOperation, candidate BrokerAttachmentReadCandidate) {
			selection := BrokerAttachmentReleaseSelection{CandidateID: candidate.ID, Payload: candidate.Bytes[:len(candidate.Bytes)-1], RetainedTail: candidate.Bytes[len(candidate.Bytes)-1:]}
			if _, err := operation.AuthorizeRelease(fixture.fresh(t), selection); err == nil {
				t.Fatal("nondeterministic terminal split accepted")
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttachmentAppFixture(t, []byte("body"), 4, 7)
			operation := fixture.start(t, 7)
			candidate, err := operation.ReadCandidate(nil)
			if err != nil {
				t.Fatal(err)
			}
			beforeAuthority, beforeMetadata := fixture.authorizer.calls.Load(), fixture.jira.qualifyCalls.Load()
			test.invoke(t, fixture, operation, candidate)
			if fixture.authorizer.calls.Load() != beforeAuthority || fixture.jira.qualifyCalls.Load() != beforeMetadata {
				t.Fatal("invalid transition reached release PDP or metadata")
			}
		})
	}
}

func TestBrokerAttachmentStreamCommitRejectsMissingReorderedAndStaleHashes(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]string) (uint64, []string)
	}{
		{name: "missing", mutate: func(values []string) (uint64, []string) { return 0, values[:len(values)-1] }},
		{name: "reordered", mutate: func(values []string) (uint64, []string) {
			values[0], values[1] = values[1], values[0]
			return 0, values
		}},
		{name: "changed", mutate: func(values []string) (uint64, []string) { values[0] = stringsOf('f', 64); return 0, values }},
		{name: "stale id", mutate: func(values []string) (uint64, []string) { return 1, values }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttachmentAppFixture(t, []byte("body"), 4, 7)
			operation := fixture.start(t, 7)
			candidate, _ := operation.ReadCandidate(nil)
			authorized, err := operation.AuthorizeRelease(fixture.fresh(t), BrokerAttachmentReleaseSelection{CandidateID: candidate.ID, Payload: candidate.Bytes})
			if err != nil {
				t.Fatal(err)
			}
			prior := operation.priorReleaseSHA256
			idDelta, hashes := test.mutate(attachmentAppLineHashes(t, authorized.Lines))
			if complete, err := operation.CommitRelease(candidate.ID+idDelta, hashes); err == nil || complete {
				t.Fatalf("complete=%t err=%v", complete, err)
			}
			if operation.priorReleaseSHA256 != prior || operation.committedReleaseCount != 0 || operation.state != brokerAttachmentStreamClosed {
				t.Fatal("failed commit advanced or retained a usable operation")
			}
		})
	}
}

func TestBrokerAttachmentStreamFreshAuthorizationAndTransitionsAreClosed(t *testing.T) {
	fixture := newAttachmentAppFixture(t, []byte("body"), 4, 7)
	operation := fixture.start(t, 7)
	candidate, err := operation.ReadCandidate(nil)
	if err != nil {
		t.Fatal(err)
	}
	selection := BrokerAttachmentReleaseSelection{CandidateID: candidate.ID, Payload: candidate.Bytes}
	before := fixture.authorizer.calls.Load()
	if _, err := operation.AuthorizeRelease(BrokerAttachmentFreshAuthorization{Context: fixture.verified, ReleaseDeadline: fixture.clock.Now().Add(5 * time.Second)}, selection); err == nil {
		t.Fatal("release accepted without a fresh authentication attempt")
	}
	if fixture.authorizer.calls.Load() != before {
		t.Fatal("missing authentication reached PDP")
	}
	if _, err := operation.ReadCandidate(nil); err == nil {
		t.Fatal("closed operation accepted another candidate")
	}

	fixture = newAttachmentAppFixture(t, []byte("body"), 4, 7)
	operation = fixture.start(t, 7)
	candidate, _ = operation.ReadCandidate(nil)
	fresh := fixture.fresh(t)
	fresh.Context.AuthorityRevision = "other-revision"
	before = fixture.authorizer.calls.Load()
	if _, err := operation.AuthorizeRelease(fresh, BrokerAttachmentReleaseSelection{CandidateID: candidate.ID, Payload: candidate.Bytes}); err == nil {
		t.Fatal("drifted authentication context accepted")
	}
	if fixture.authorizer.calls.Load() != before {
		t.Fatal("drifted context reached PDP")
	}
}

func TestBrokerAttachmentStreamConcurrentCloseCancelsBlockedRead(t *testing.T) {
	fixture := newAttachmentAppFixture(t, []byte("body"), 4, 7)
	fixture.jira.blockRead = true
	entered := make(chan struct{}, 1)
	fixture.jira.readEntered = entered
	operation := fixture.start(t, 7)
	readDone := make(chan error, 1)
	go func() {
		_, err := operation.ReadCandidate(nil)
		readDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("read did not reach the blocked body")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- operation.Close() }()
	select {
	case err := <-readDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("read err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel blocked read")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not join blocked state method")
	}
	if fixture.jira.bodyCloses.Load() != 1 || fixture.jira.handleCloses.Load() != 1 {
		t.Fatalf("body_close=%d handle_close=%d", fixture.jira.bodyCloses.Load(), fixture.jira.handleCloses.Load())
	}
}

func TestBrokerAttachmentStreamConcurrentCloseCancelsBlockedDecision(t *testing.T) {
	fixture := newAttachmentAppFixture(t, []byte("body"), 4, 7)
	operation := fixture.start(t, 7)
	candidate, err := operation.ReadCandidate(nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh := fixture.fresh(t)
	entered := make(chan struct{}, 1)
	fixture.authorizer.block = make(chan struct{})
	fixture.authorizer.entered = entered
	beforeMetadata := fixture.jira.qualifyCalls.Load()
	done := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		_, err := operation.AuthorizeRelease(fresh, BrokerAttachmentReleaseSelection{CandidateID: candidate.ID, Payload: candidate.Bytes})
		done <- err
	}()
	t.Cleanup(func() {
		fixture.cancel()
		select {
		case <-joined:
		case <-time.After(time.Second):
			t.Error("authorization did not stop after cancellation")
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("authorization did not reach the blocked decision")
	}
	closeDone := make(chan error, 1)
	closeJoined := make(chan struct{})
	go func() {
		defer close(closeJoined)
		closeDone <- operation.Close()
	}()
	t.Cleanup(func() {
		fixture.cancel()
		select {
		case <-closeJoined:
		case <-time.After(time.Second):
			t.Error("Close did not stop after cancellation")
		}
	})
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not join blocked authorization")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("authorization error=%v", err)
	}
	if fixture.jira.qualifyCalls.Load() != beforeMetadata || fixture.jira.bodyCloses.Load() != 1 || fixture.jira.handleCloses.Load() != 1 || operation.committedReleaseCount != 0 {
		t.Fatal("canceled authorization advanced release state or leaked the body")
	}
}

func TestBrokerAttachmentStreamOpaqueFormatting(t *testing.T) {
	fixture := newAttachmentAppFixture(t, []byte("PRIVATE-BODY-CANARY"), 19, 7)
	operation := fixture.start(t, 7)
	defer operation.Close()
	candidate, err := operation.ReadCandidate(nil)
	if err != nil {
		t.Fatal(err)
	}
	selection := BrokerAttachmentReleaseSelection{CandidateID: candidate.ID, Payload: candidate.Bytes}
	authorized, err := operation.AuthorizeRelease(fixture.fresh(t), selection)
	if err != nil {
		t.Fatal(err)
	}
	values := []any{fixture.budgets, operation, candidate, selection, authorized, BrokerAttachmentFreshAuthorization{Context: fixture.verified}, BrokerAttachmentStreamStart{Request: fixture.request}}
	for _, value := range values {
		formatted := fmt.Sprintf("%v|%+v|%#v|%s|%q|%x|%d", value, value, value, value, value, value, value)
		if bytes.Contains([]byte(formatted), []byte("PRIVATE-BODY-CANARY")) || bytes.Contains([]byte(formatted), authorized.Lines[1]) {
			t.Fatalf("format exposed stream data: %q", formatted)
		}
	}
}

func TestBrokerAttachmentExecutionBudgetCeilings(t *testing.T) {
	budgets, err := NewBrokerAttachmentExecutionBudgets()
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for attempt := 0; attempt < brokercontract.MaxAttachmentAuthenticationAttemptsV3; attempt++ {
		authCtx, err := budgets.AuthenticationContext(ctx)
		if err != nil || attachmentAppCharge(authCtx, brokercontract.MaxAttachmentAuthorityCallBytesV3) != nil {
			t.Fatalf("authentication attempt %d err=%v", attempt, err)
		}
	}
	authCtx, _ := budgets.AuthenticationContext(ctx)
	if err := attachmentAppCharge(authCtx, 0); !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) {
		t.Fatalf("authentication overflow err=%v", err)
	}
	if got := budgets.authentication.Usage(); got != (domain.ReadBudgetUsage{Attempts: 17, ResponseBytes: 17 * brokercontract.MaxAttachmentAuthorityCallBytesV3}) {
		t.Fatalf("authentication usage=%+v", got)
	}
	formatted := fmt.Sprintf("%#v|%d", budgets, budgets)
	if !bytes.Contains([]byte(formatted), []byte(brokerAttachmentBudgetsLabel)) {
		t.Fatalf("budget format=%q", formatted)
	}
}
