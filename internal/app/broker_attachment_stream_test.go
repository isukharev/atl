package app

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type attachmentAppClock struct {
	mu      sync.Mutex
	now     time.Time
	started time.Time
}

func TestAttachmentAppClockIncludesElapsedWallTime(t *testing.T) {
	fixture := newAttachmentAppFixture(t, nil, 0, 0)
	before := fixture.clock.Now()
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal("test context ended before the clock observation")
	}
	if !fixture.clock.Now().After(before) {
		t.Fatal("fixture clock stayed frozen while real context time elapsed")
	}
}

func (c *attachmentAppClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Real context deadlines keep advancing during instrumented tests. Explicit
	// Advance calls still adjust the logical offset for rollback/expiry cases.
	return c.now.Add(time.Since(c.started))
}

func (c *attachmentAppClock) Advance(value time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(value)
	c.mu.Unlock()
}

type attachmentAppAuthorizer struct {
	clock           *attachmentAppClock
	order           *[]string
	calls           atomic.Int32
	block           <-chan struct{}
	entered         chan<- struct{}
	failPhase       string
	err             error
	advancePhase    string
	advanceAmount   time.Duration
	discoveryErr    error
	discoveryLife   time.Duration
	mutateDiscovery func(*domain.BrokerFamilyDiscoveryProjectionV4)
}

func (a *attachmentAppAuthorizer) DiscoverFamilyV4(ctx context.Context, request domain.BrokerFamilyDiscoveryAuthorizationRequestV4) (domain.BrokerFamilyDiscoveryProjectionV4, error) {
	if err := attachmentAppCharge(ctx, 17); err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, err
	}
	a.calls.Add(1)
	if a.discoveryErr != nil {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, a.discoveryErr
	}
	now := a.clock.Now()
	lifetime := a.discoveryLife
	if lifetime == 0 {
		lifetime = 5 * time.Second
	}
	expires := min(now.Add(lifetime).UnixMilli(), request.Request.NotAfterMillis)
	if deadline, ok := ctx.Deadline(); ok {
		expires = min(expires, deadline.UnixMilli())
	}
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(request.Context)
	projection := domain.BrokerFamilyDiscoveryProjectionV4{
		SchemaVersion: domain.BrokerDiscoverySchemaVersionV4, RequestID: request.Request.RequestID, RequestSHA256: request.RequestSHA256, ContextSHA256: contextSHA256,
		ExecutionID: request.Context.ExecutionID, ExecutionEpoch: request.Context.ExecutionEpoch, Audience: request.Context.Audience, BrokerID: request.Context.BrokerID,
		AuthorityRevision: request.Context.AuthorityRevision, ContractFamily: request.Request.ContractFamily, Service: request.Request.Service,
		RegistrySHA256: brokercontract.RegistrySHA256V3(), ContractSchemaSHA256: brokercontract.ExecutionSchemaSHA256V3(), DiscoverySchemaSHA256: brokercontract.DiscoverySchemaSHA256V4(),
		IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: expires, Operations: []domain.BrokerFamilyDiscoveryOperationV4{}, Complete: true,
	}
	value := brokercontract.RegistryV3()[0]
	definition := value.Definition
	projection.Operations = []domain.BrokerFamilyDiscoveryOperationV4{{
		ID: definition.ID, Version: definition.Version, Supported: true, Access: domain.BrokerDiscoveryAccessAllowed,
		Features: definition.RequiredFeatures, Limits: definition.Limits, Effects: definition.Effects,
		MaxMetadataItems: value.MaxMetadataItems, MaxJiraAttempts: value.MaxJiraAttempts, MaxAuthenticationAttempts: value.MaxAuthenticationAttempts,
		MaxDecisionAttempts: value.MaxDecisionAttempts, MaxTotalHostOutboundAttempts: value.MaxTotalHostOutboundAttempts,
		MaxCommandHostOutboundAttempts: value.MaxCommandHostOutboundAttempts, MaxJiraResponseBytes: value.MaxJiraResponseBytes,
		MaxAuthorityResponseBytes: value.MaxAuthorityResponseBytes, MaxTotalHostResponseBytes: value.MaxTotalHostResponseBytes,
		MaxManifestLineBytes: value.MaxManifestLineBytes, MaxDataLineBytes: value.MaxDataLineBytes,
		MaxTerminalLineBytes: value.MaxTerminalLineBytes, MaxFramedResponseBytes: value.MaxFramedResponseBytes,
	}}
	if a.mutateDiscovery != nil {
		a.mutateDiscovery(&projection)
	}
	return projection, nil
}

func (a *attachmentAppAuthorizer) AdmitAttachment(ctx context.Context, request domain.BrokerAttachmentAdmissionRequestV3) (domain.BrokerAttachmentAdmissionDecisionV3, error) {
	issued, err := a.call(ctx, "admission")
	if err != nil {
		return domain.BrokerAttachmentAdmissionDecisionV3{}, err
	}
	requestSHA256, _ := brokercontract.AttachmentAdmissionRequestSHA256V3(request)
	core := attachmentAppDecisionCore(ctx, request.Context, requestSHA256, "admission", issued)
	return attachmentAppRoundtrip(domain.BrokerAttachmentAdmissionDecisionV3{BrokerDecisionCore: core}, brokercontract.EncodeAttachmentAdmissionDecisionV3, brokercontract.DecodeAttachmentAdmissionDecisionV3), nil
}

func (a *attachmentAppAuthorizer) AuthorizeAttachmentQualification(ctx context.Context, request domain.BrokerAttachmentQualificationRequestV3) (domain.BrokerAttachmentQualificationDecisionV3, error) {
	issued, err := a.call(ctx, "qualification_"+string(request.Phase))
	if err != nil {
		return domain.BrokerAttachmentQualificationDecisionV3{}, err
	}
	requestSHA256, _ := brokercontract.AttachmentQualificationRequestSHA256V3(request)
	plan, verified := attachmentAppQualificationPlanContext(request)
	planSHA256, _ := brokercontract.AttachmentMetadataPlanSHA256V3(plan)
	value := domain.BrokerAttachmentQualificationDecisionV3{Phase: request.Phase, BrokerDecisionCore: attachmentAppDecisionCore(ctx, verified, requestSHA256, "qualification-"+string(request.Phase), issued), PlanSHA256: planSHA256}
	return attachmentAppRoundtrip(value, brokercontract.EncodeAttachmentQualificationDecisionV3, brokercontract.DecodeAttachmentQualificationDecisionV3), nil
}

func (a *attachmentAppAuthorizer) AuthorizeAttachmentOperation(ctx context.Context, request domain.BrokerAttachmentOperationAuthorizationRequestV3) (domain.BrokerAttachmentOperationDecisionV3, error) {
	issued, err := a.call(ctx, "operation_"+string(request.Phase))
	if err != nil {
		return domain.BrokerAttachmentOperationDecisionV3{}, err
	}
	requestSHA256, _ := brokercontract.AttachmentOperationAuthorizationRequestSHA256V3(request)
	verified := attachmentAppOperationContext(request)
	value := domain.BrokerAttachmentOperationDecisionV3{
		Phase: request.Phase, BrokerDecisionCore: attachmentAppDecisionCore(ctx, verified, requestSHA256, "operation-"+string(request.Phase), issued),
		QualificationDecisionSHA256: attachmentAppOperationQualificationDecision(request), Operation: domain.BrokerOperationJiraAttachmentDownload, OperationVersion: 1,
	}
	if request.Release != nil {
		value.AnchorSHA256, value.PriorReleaseSHA256, value.ManifestCoreSHA256 = request.Release.AnchorSHA256, request.Release.PriorReleaseSHA256, request.Release.ManifestCoreSHA256
		value.ReleaseFactsSHA256, _ = brokercontract.AttachmentReleaseFactsSHA256V3(request.Release.Facts)
	} else {
		arguments := attachmentAppQualificationArguments(request.Qualified.QualificationRequest)
		value.ArgumentsSHA256, _ = brokercontract.AttachmentArgumentsSHA256V3(arguments)
		value.ResourcesSHA256, _ = brokercontract.AttachmentResourcesSHA256V3(request.Qualified.Snapshot)
		value.EffectsSHA256, _ = brokercontract.AttachmentEffectsSHA256V3(request.Qualified.Effects)
	}
	return attachmentAppRoundtrip(value, brokercontract.EncodeAttachmentOperationDecisionV3, brokercontract.DecodeAttachmentOperationDecisionV3), nil
}

func (a *attachmentAppAuthorizer) call(ctx context.Context, phase string) (time.Time, error) {
	issued := a.clock.Now()
	a.calls.Add(1)
	if a.order != nil {
		*a.order = append(*a.order, phase)
	}
	if err := attachmentAppCharge(ctx, 7); err != nil {
		return issued, err
	}
	if a.block != nil {
		if a.entered != nil {
			a.entered <- struct{}{}
		}
		select {
		case <-a.block:
		case <-ctx.Done():
			return issued, ctx.Err()
		}
	}
	if phase == a.advancePhase {
		a.clock.Advance(a.advanceAmount)
	}
	if phase == a.failPhase {
		return issued, a.err
	}
	return issued, nil
}

type attachmentAppJira struct {
	snapshot     domain.BrokerJiraAttachmentSnapshotV3
	body         []byte
	chunks       []int
	order        *[]string
	qualifyCalls atomic.Int32
	prepareCalls atomic.Int32
	openCalls    atomic.Int32
	handleCloses atomic.Int32
	bodyCloses   atomic.Int32
	readMode     string
	driftAt      int32
	blockRead    bool
	readEntered  chan<- struct{}
	prepareDrift bool
	origin       string
	originErr    error
	originCalls  atomic.Int32
}

func (j *attachmentAppJira) BrokerOriginSHA256() (string, error) {
	j.originCalls.Add(1)
	return j.origin, j.originErr
}

func (j *attachmentAppJira) QualifyBrokerJiraAttachment(ctx context.Context, _, _ string) (domain.BrokerJiraAttachmentSnapshotV3, error) {
	call := j.qualifyCalls.Add(1)
	if j.order != nil {
		*j.order = append(*j.order, "metadata")
	}
	if err := attachmentAppCharge(ctx, 11); err != nil {
		return domain.BrokerJiraAttachmentSnapshotV3{}, err
	}
	value := j.snapshot
	if j.driftAt == call {
		value.Updated += "-drift"
	}
	return value, nil
}

func (j *attachmentAppJira) PrepareBrokerJiraAttachment(ctx context.Context, snapshot domain.BrokerJiraAttachmentSnapshotV3) (domain.BrokerJiraAttachmentOpenHandleV3, error) {
	j.prepareCalls.Add(1)
	if j.order != nil {
		*j.order = append(*j.order, "prepare")
	}
	if err := attachmentAppCharge(ctx, 13); err != nil {
		return nil, err
	}
	if j.prepareDrift {
		snapshot.Updated += "-drift"
	}
	return &attachmentAppHandle{jira: j, snapshot: snapshot}, nil
}

type attachmentAppHandle struct {
	jira     *attachmentAppJira
	snapshot domain.BrokerJiraAttachmentSnapshotV3
	body     *attachmentAppBody
	closed   atomic.Bool
}

func (h *attachmentAppHandle) Snapshot() domain.BrokerJiraAttachmentSnapshotV3 { return h.snapshot }

func (h *attachmentAppHandle) Open(ctx context.Context, _ time.Time) (io.ReadCloser, error) {
	h.jira.openCalls.Add(1)
	if h.jira.order != nil {
		*h.jira.order = append(*h.jira.order, "open")
	}
	budget := domain.ReadBudgetFromContext(ctx)
	if budget == nil || budget.TakeAttempt() != nil {
		return nil, domain.ErrReadAttemptBudgetExhausted
	}
	h.body = &attachmentAppBody{ctx: ctx, data: bytes.Clone(h.jira.body), chunks: append([]int(nil), h.jira.chunks...), mode: h.jira.readMode, block: h.jira.blockRead, entered: h.jira.readEntered, budget: budget, closes: &h.jira.bodyCloses}
	return h.body, nil
}

func (h *attachmentAppHandle) Close() error {
	if h.closed.CompareAndSwap(false, true) {
		h.jira.handleCloses.Add(1)
		if h.body != nil {
			return h.body.Close()
		}
	}
	return nil
}

type attachmentAppBody struct {
	mu        sync.Mutex
	ctx       context.Context
	data      []byte
	offset    int
	chunks    []int
	chunk     int
	mode      string
	block     bool
	entered   chan<- struct{}
	budget    *domain.ReadBudget
	finish    func(int64)
	remaining int64
	consumed  int64
	closed    bool
	closes    *atomic.Int32
}

func (b *attachmentAppBody) Read(buffer []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, context.Canceled
	}
	if b.block {
		b.mu.Unlock()
		if b.entered != nil {
			b.entered <- struct{}{}
		}
		<-b.ctx.Done()
		b.mu.Lock()
		return 0, b.ctx.Err()
	}
	if b.mode == "no-progress" {
		return 0, nil
	}
	if b.finish == nil {
		remaining, finish, err := b.budget.BeginResponse(b.ctx)
		if err != nil {
			return 0, err
		}
		b.remaining, b.finish = remaining, finish
	}
	if b.offset >= len(b.data) {
		b.finishOnce()
		if b.mode == "error" {
			return 0, io.ErrUnexpectedEOF
		}
		return 0, io.EOF
	}
	if b.consumed >= b.remaining {
		b.finishOnce()
		return 0, domain.ErrReadResponseBudgetExhausted
	}
	limit := len(buffer)
	if b.chunk < len(b.chunks) && b.chunks[b.chunk] < limit {
		limit = b.chunks[b.chunk]
	}
	b.chunk++
	if limit > len(b.data)-b.offset {
		limit = len(b.data) - b.offset
	}
	if int64(limit) > b.remaining-b.consumed {
		limit = int(b.remaining - b.consumed)
	}
	n := copy(buffer, b.data[b.offset:b.offset+limit])
	b.offset += n
	b.consumed += int64(n)
	return n, nil
}

func (b *attachmentAppBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		b.finishOnce()
		b.closes.Add(1)
	}
	return nil
}

func (b *attachmentAppBody) finishOnce() {
	if b.finish != nil {
		b.finish(b.consumed)
		b.finish = nil
	}
}

type attachmentAppFixture struct {
	clock      *attachmentAppClock
	verified   domain.BrokerVerifiedContext
	request    domain.BrokerAttachmentRequestV3
	backend    domain.BrokerBackendBinding
	budgets    *BrokerAttachmentExecutionBudgets
	authorizer *attachmentAppAuthorizer
	jira       *attachmentAppJira
	service    *BrokerJiraAttachmentStreamService
	ctx        context.Context
	cancel     context.CancelFunc
	order      []string
}

func newAttachmentAppFixture(t *testing.T, body []byte, declared int64, tail int) *attachmentAppFixture {
	t.Helper()
	started := time.Now()
	now := started.UTC().Truncate(time.Millisecond)
	backend := domain.BrokerBackendBinding{Service: "jira", OriginSHA256: stringsOf('1', 64), WorkloadBackendID: "jira-primary"}
	verified := domain.BrokerVerifiedContext{
		PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "revision-1",
		ExecutionNotBeforeMillis: now.Add(-time.Minute).UnixMilli(), ExecutionExpiresMillis: now.Add(2 * time.Minute).UnixMilli(), GrantExpiresMillis: now.Add(2 * time.Minute).UnixMilli(), CredentialExpiresMillis: now.Add(2 * time.Minute).UnixMilli(), Backend: backend,
	}
	request := domain.BrokerAttachmentRequestV3{SchemaVersion: 3, Operation: domain.BrokerOperationJiraAttachmentDownload, OperationVersion: 1, RequestID: "request-1", Features: []string{"atomic_local_publish_v1", "attachment_id_v1", "step_snapshot_v1"}, Expect: domain.BrokerRequestExpectations{ExecutionID: verified.ExecutionID, ExecutionEpoch: verified.ExecutionEpoch, AuthorityRevision: verified.AuthorityRevision}, Arguments: domain.BrokerAttachmentArgumentsV3{IssueKey: "PROJ-1", AttachmentID: "200"}}
	snapshot, err := brokercontract.NewAttachmentSnapshotEvidenceV3(domain.BrokerJiraAttachmentSnapshotV3{IssueID: "100", IssueKey: "PROJ-1", Project: "PROJ", Updated: "2026-09-09T00:00:00Z", AttachmentID: "200", ParentID: "100", Filename: "example.bin", MediaType: "application/octet-stream", Created: "2026-09-09T00:00:00Z", DeclaredSize: declared})
	if err != nil {
		t.Fatal(err)
	}
	clock := &attachmentAppClock{now: now, started: started}
	fixture := &attachmentAppFixture{clock: clock, verified: verified, request: request, backend: backend}
	fixture.authorizer = &attachmentAppAuthorizer{clock: clock, order: &fixture.order}
	fixture.jira = &attachmentAppJira{snapshot: snapshot, body: bytes.Clone(body), order: &fixture.order, origin: backend.OriginSHA256}
	fixture.service, err = NewBrokerJiraAttachmentStreamService(fixture.authorizer, BrokerJiraAttachmentStreamReader{Backend: backend, Reader: fixture.jira})
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.now = clock.Now
	fixture.budgets, err = NewBrokerAttachmentExecutionBudgets()
	if err != nil {
		t.Fatal(err)
	}
	fixture.ctx, fixture.cancel = context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(fixture.cancel)
	authCtx, err := fixture.budgets.AuthenticationContext(fixture.ctx)
	if err != nil || attachmentAppCharge(authCtx, 5) != nil {
		t.Fatalf("initial authentication budget: %v", err)
	}
	_ = tail
	return fixture
}

func (f *attachmentAppFixture) start(t *testing.T, tail int) *BrokerJiraAttachmentStreamOperation {
	t.Helper()
	operation, err := f.service.Start(f.ctx, BrokerAttachmentStreamStart{Request: f.request, Verified: f.verified, InitialAuthenticationDeadline: f.clock.Now().Add(5 * time.Second), StreamID: "stream-1", CorrelationID: "correlation-1", ScannerTailBytes: tail, Budgets: f.budgets})
	if err != nil {
		reason, _ := brokercontract.Reason(err)
		t.Fatalf("start failed: %v reason=%s phases=%v", err, reason, f.order)
	}
	return operation
}

func (f *attachmentAppFixture) fresh(t *testing.T) BrokerAttachmentFreshAuthorization {
	t.Helper()
	ctx, err := f.budgets.AuthenticationContext(f.ctx)
	if err != nil || attachmentAppCharge(ctx, 5) != nil {
		t.Fatalf("fresh authentication: %v", err)
	}
	return BrokerAttachmentFreshAuthorization{Context: f.verified, ReleaseDeadline: f.clock.Now().Add(5 * time.Second)}
}

func runAttachmentAppStream(t *testing.T, fixture *attachmentAppFixture, tailBytes int) ([]byte, int, []int) {
	t.Helper()
	operation := fixture.start(t, tailBytes)
	defer operation.Close()
	var output, tail []byte
	releases := 0
	var lineCounts []int
	for {
		candidate, err := operation.ReadCandidate(tail)
		if err != nil {
			t.Fatal(err)
		}
		payloadBytes := len(candidate.Bytes)
		if payloadBytes > int(brokercontract.MaxAttachmentDecodedFrameBytesV3) {
			payloadBytes = int(brokercontract.MaxAttachmentDecodedFrameBytesV3)
		}
		selection := BrokerAttachmentReleaseSelection{CandidateID: candidate.ID, Payload: candidate.Bytes[:payloadBytes], RetainedTail: candidate.Bytes[payloadBytes:]}
		authorized, err := operation.AuthorizeRelease(fixture.fresh(t), selection)
		if err != nil {
			reason, _ := brokercontract.Reason(err)
			t.Fatalf("release %d failed: %v reason=%s phases=%v", releases, err, reason, fixture.order)
		}
		beforeCandidate := bytes.Clone(candidate.Bytes)
		beforeLines := cloneAttachmentLines(authorized.Lines)
		lineCounts = append(lineCounts, len(authorized.Lines))
		hashes := attachmentAppLineHashes(t, authorized.Lines)
		complete, err := operation.CommitRelease(candidate.ID, hashes)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(candidate.Bytes, beforeCandidate) || !reflect.DeepEqual(authorized.Lines, beforeLines) {
			t.Fatal("commit cleared server-owned candidate or line bytes")
		}
		output = append(output, selection.Payload...)
		tail = selection.RetainedTail
		releases++
		if complete {
			return output, releases, lineCounts
		}
	}
}

func TestBrokerAttachmentStreamPositiveBoundsAndDeterministicFrames(t *testing.T) {
	for _, test := range []struct {
		name   string
		size   int
		tail   int
		chunks []int
	}{
		{name: "zero", size: 0, tail: 7},
		{name: "short", size: 23, tail: 7, chunks: []int{1, 2, 3, 5}},
		{name: "one frame", size: 1 << 20, tail: 31, chunks: []int{17, 4093, 65537}},
		{name: "exact frame without lookahead", size: 1 << 20, tail: 0},
		{name: "exact window boundary", size: 1<<20 + 31, tail: 31},
		{name: "two frames", size: 1<<20 + 113, tail: 127, chunks: []int{3, 70001, 11}},
		{name: "three frames", size: 2<<20 + 113, tail: 127, chunks: []int{3, 70001, 11}},
		{name: "sixteen frames", size: 16 << 20, tail: 63, chunks: []int{65537, 9, 131071}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := bytes.Repeat([]byte{'x'}, test.size)
			fixture := newAttachmentAppFixture(t, body, int64(test.size), test.tail)
			fixture.jira.chunks = test.chunks
			got, releases, lineCounts := runAttachmentAppStream(t, fixture, test.tail)
			if !bytes.Equal(got, body) {
				t.Fatalf("body mismatch: got=%d want=%d", len(got), len(body))
			}
			wantReleases := (test.size + (1 << 20) - 1) / (1 << 20)
			if wantReleases == 0 {
				wantReleases = 1
			}
			if releases != wantReleases || fixture.jira.openCalls.Load() != 1 || fixture.jira.bodyCloses.Load() != 1 || fixture.jira.handleCloses.Load() != 1 {
				t.Fatalf("releases=%d open=%d body_close=%d handle_close=%d", releases, fixture.jira.openCalls.Load(), fixture.jira.bodyCloses.Load(), fixture.jira.handleCloses.Load())
			}
			if releases == 1 {
				wantLines := 3
				if test.size == 0 {
					wantLines = 2
				}
				if lineCounts[0] != wantLines {
					t.Fatalf("single-release lines=%v", lineCounts)
				}
			} else {
				if lineCounts[0] != 2 || lineCounts[len(lineCounts)-1] != 2 {
					t.Fatalf("first/last line shapes=%v", lineCounts)
				}
				for _, count := range lineCounts[1 : len(lineCounts)-1] {
					if count != 1 {
						t.Fatalf("middle line shapes=%v", lineCounts)
					}
				}
			}
			if !fixture.budgets.validUsage(1+releases, 5+2*releases, 2+releases, 1) {
				t.Fatalf("unexpected final budget usage: auth=%+v decision=%+v metadata=%+v body=%+v", fixture.budgets.authentication.Usage(), fixture.budgets.decision.Usage(), fixture.budgets.metadata.Usage(), fixture.budgets.body.Usage())
			}
		})
	}
}

func attachmentAppCharge(ctx context.Context, responseBytes int64) error {
	budget := domain.ReadBudgetFromContext(ctx)
	if budget == nil {
		return domain.ErrReadAttemptBudgetExhausted
	}
	if err := budget.TakeAttempt(); err != nil {
		return err
	}
	remaining, finish, err := budget.BeginResponse(ctx)
	if err != nil {
		return err
	}
	if responseBytes > remaining {
		finish(remaining)
		return domain.ErrReadResponseBudgetExhausted
	}
	finish(responseBytes)
	return nil
}

func attachmentAppDecisionCore(ctx context.Context, verified domain.BrokerVerifiedContext, requestSHA256, id string, now time.Time) domain.BrokerDecisionCore {
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(verified)
	expires := now.Add(5 * time.Second).UnixMilli()
	if deadline, ok := ctx.Deadline(); ok {
		expires = min(expires, deadline.UnixMilli())
	}
	return domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: id, AuthorityRevision: verified.AuthorityRevision, ContextSHA256: contextSHA256, RequestSHA256: requestSHA256, IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: expires}
}

func attachmentAppQualificationPlanContext(value domain.BrokerAttachmentQualificationRequestV3) (domain.BrokerAttachmentMetadataPlanV3, domain.BrokerVerifiedContext) {
	switch value.Phase {
	case domain.BrokerAttachmentQualificationInitial:
		return value.Initial.Plan, value.Initial.Admission.Context
	case domain.BrokerAttachmentQualificationPreOpen:
		return value.PreOpen.Plan, attachmentAppOperationContext(value.PreOpen.InitialOperation)
	default:
		return value.Release.Plan, value.Release.Context
	}
}

func attachmentAppOperationContext(value domain.BrokerAttachmentOperationAuthorizationRequestV3) domain.BrokerVerifiedContext {
	if value.Release != nil {
		return value.Release.QualificationRequest.Release.Context
	}
	_, verified := attachmentAppQualificationPlanContext(value.Qualified.QualificationRequest)
	return verified
}

func attachmentAppQualificationArguments(value domain.BrokerAttachmentQualificationRequestV3) domain.BrokerAttachmentArgumentsV3 {
	if value.Initial != nil {
		return value.Initial.Admission.Arguments
	}
	return attachmentAppQualificationArguments(value.PreOpen.InitialOperation.Qualified.QualificationRequest)
}

func attachmentAppOperationQualificationDecision(value domain.BrokerAttachmentOperationAuthorizationRequestV3) string {
	if value.Release != nil {
		return value.Release.QualificationDecision.DecisionSHA256
	}
	return value.Qualified.QualificationDecision.DecisionSHA256
}

func attachmentAppRoundtrip[T any](value T, encode func(T) ([]byte, error), decode func([]byte) (T, error)) T {
	body, err := encode(value)
	if err != nil {
		panic(err)
	}
	decoded, err := decode(body)
	if err != nil {
		panic(err)
	}
	return decoded
}

func attachmentAppLineHashes(t *testing.T, lines [][]byte) []string {
	t.Helper()
	result := make([]string, len(lines))
	for index, line := range lines {
		exact := append(bytes.Clone(line), '\n')
		var err error
		result[index], err = brokercontract.AttachmentExactLineSHA256V3(exact)
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func cloneAttachmentLines(lines [][]byte) [][]byte {
	result := make([][]byte, len(lines))
	for index := range lines {
		result[index] = bytes.Clone(lines[index])
	}
	return result
}

func stringsOf(value byte, count int) string { return string(bytes.Repeat([]byte{value}, count)) }
