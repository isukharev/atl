package app

import (
	"context"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func attachmentAppDiscoveryRequest(t *testing.T, fixture *attachmentAppFixture, notAfter time.Time) domain.BrokerFamilyDiscoveryRequestV4 {
	t.Helper()
	contextSHA256, err := brokercontract.VerifiedContextSHA256(fixture.verified)
	if err != nil {
		t.Fatal(err)
	}
	return domain.BrokerFamilyDiscoveryRequestV4{
		SchemaVersion: domain.BrokerDiscoverySchemaVersionV4, RequestID: "discovery-1", ContractFamily: domain.BrokerContractFamilyExecutionV3,
		Service: "jira", BrokerID: fixture.verified.BrokerID, Audience: fixture.verified.Audience, ContextSHA256: contextSHA256, NotAfterMillis: notAfter.UnixMilli(),
		Expect: domain.BrokerRequestExpectations{ExecutionID: fixture.verified.ExecutionID, ExecutionEpoch: fixture.verified.ExecutionEpoch, AuthorityRevision: fixture.verified.AuthorityRevision},
	}
}

func TestBrokerAttachmentDiscoveryV4IsCurrentBoundedAndAdvisory(t *testing.T) {
	fixture := newAttachmentAppFixture(t, []byte("unused"), 6, 7)
	fixture.authorizer.discoveryLife = 2 * time.Second
	request := attachmentAppDiscoveryRequest(t, fixture, fixture.clock.Now().Add(5*time.Second))
	budget, err := domain.NewReadBudget(1, brokercontract.MaxDiscoveryV4Bytes)
	if err != nil {
		t.Fatal(err)
	}
	ctx := domain.WithReadBudget(fixture.ctx, budget)
	started := fixture.clock.Now()
	projection, deadline, err := fixture.service.DiscoverExecutionV3(ctx, request, fixture.verified)
	if err != nil {
		t.Fatal(err)
	}
	if projection.RequestID != request.RequestID || !projection.Complete || deadline != started.Add(2*time.Second) {
		t.Fatalf("projection=%+v deadline=%v", projection, deadline)
	}
	if budget.Usage().Attempts != 1 || fixture.jira.qualifyCalls.Load() != 0 || fixture.jira.prepareCalls.Load() != 0 || fixture.jira.openCalls.Load() != 0 {
		t.Fatalf("usage=%+v Jira=%d/%d/%d", budget.Usage(), fixture.jira.qualifyCalls.Load(), fixture.jira.prepareCalls.Load(), fixture.jira.openCalls.Load())
	}
}

func TestBrokerAttachmentDiscoveryV4RejectsContextProjectionAndExpiry(t *testing.T) {
	for _, test := range []struct {
		name      string
		mutate    func(*attachmentAppFixture, *domain.BrokerFamilyDiscoveryRequestV4, *domain.BrokerVerifiedContext)
		wantCalls int32
	}{
		{name: "backend", mutate: func(_ *attachmentAppFixture, _ *domain.BrokerFamilyDiscoveryRequestV4, verified *domain.BrokerVerifiedContext) {
			verified.Backend.WorkloadBackendID = "other"
		}},
		{name: "malformed request", mutate: func(_ *attachmentAppFixture, request *domain.BrokerFamilyDiscoveryRequestV4, _ *domain.BrokerVerifiedContext) {
			request.ContextSHA256 = "invalid"
		}},
		{name: "expired request", mutate: func(f *attachmentAppFixture, request *domain.BrokerFamilyDiscoveryRequestV4, _ *domain.BrokerVerifiedContext) {
			request.NotAfterMillis = f.clock.Now().UnixMilli()
		}},
		{name: "authority outage", wantCalls: 1, mutate: func(f *attachmentAppFixture, _ *domain.BrokerFamilyDiscoveryRequestV4, _ *domain.BrokerVerifiedContext) {
			f.authorizer.discoveryErr = domain.ErrCheckFailed
		}},
		{name: "projection drift", wantCalls: 1, mutate: func(f *attachmentAppFixture, _ *domain.BrokerFamilyDiscoveryRequestV4, _ *domain.BrokerVerifiedContext) {
			f.authorizer.mutateDiscovery = func(value *domain.BrokerFamilyDiscoveryProjectionV4) { value.AuthorityRevision = "other" }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttachmentAppFixture(t, nil, 0, 7)
			request := attachmentAppDiscoveryRequest(t, fixture, fixture.clock.Now().Add(5*time.Second))
			verified := fixture.verified
			test.mutate(fixture, &request, &verified)
			budget, _ := domain.NewReadBudget(1, brokercontract.MaxDiscoveryV4Bytes)
			_, _, err := fixture.service.DiscoverExecutionV3(domain.WithReadBudget(fixture.ctx, budget), request, verified)
			if err == nil || fixture.authorizer.calls.Load() != test.wantCalls || fixture.jira.qualifyCalls.Load() != 0 || fixture.jira.prepareCalls.Load() != 0 {
				t.Fatalf("err=%v calls=%d Jira=%d/%d", err, fixture.authorizer.calls.Load(), fixture.jira.qualifyCalls.Load(), fixture.jira.prepareCalls.Load())
			}
		})
	}
}

func TestBrokerAttachmentDiscoveryV4ParentDeadlineAndCancellation(t *testing.T) {
	fixture := newAttachmentAppFixture(t, nil, 0, 7)
	request := attachmentAppDiscoveryRequest(t, fixture, fixture.clock.Now().Add(5*time.Second))
	ctx, cancel := context.WithCancel(fixture.ctx)
	cancel()
	if _, _, err := fixture.service.DiscoverExecutionV3(ctx, request, fixture.verified); err == nil || fixture.authorizer.calls.Load() != 0 {
		t.Fatalf("canceled discovery err=%v calls=%d", err, fixture.authorizer.calls.Load())
	}

	fixture = newAttachmentAppFixture(t, nil, 0, 7)
	request = attachmentAppDiscoveryRequest(t, fixture, fixture.clock.Now().Add(5*time.Second))
	parent, parentCancel := context.WithTimeout(fixture.ctx, 100*time.Millisecond)
	defer parentCancel()
	budget, _ := domain.NewReadBudget(1, brokercontract.MaxDiscoveryV4Bytes)
	_, deadline, err := fixture.service.DiscoverExecutionV3(domain.WithReadBudget(parent, budget), request, fixture.verified)
	if err != nil {
		t.Fatal(err)
	}
	if parentDeadline, _ := parent.Deadline(); deadline.After(parentDeadline.Add(time.Millisecond)) {
		t.Fatalf("deadline=%v parent=%v", deadline, parentDeadline)
	}

	fixture.clock.Advance(6 * time.Second)
	if _, _, err := fixture.service.DiscoverExecutionV3(domain.WithReadBudget(fixture.ctx, budget), request, fixture.verified); err == nil || fixture.authorizer.calls.Load() != 1 {
		t.Fatalf("stale discovery err=%v calls=%d", err, fixture.authorizer.calls.Load())
	}
}
