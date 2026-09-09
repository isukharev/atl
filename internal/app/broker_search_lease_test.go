package app

import (
	"context"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

type skewProjectPageAuthorizer struct {
	*brokerProjectPageAuthorizerStub
	current    *time.Time
	delayPhase domain.BrokerAuthorizationPhase
}

func (a *skewProjectPageAuthorizer) start() {
	a.nowMillis = a.current.Add(800 * time.Millisecond).UnixMilli()
}

func (a *skewProjectPageAuthorizer) finish(phase domain.BrokerAuthorizationPhase) {
	if a.delayPhase == phase {
		*a.current = a.current.Add(151 * time.Millisecond)
	}
}

func (a *skewProjectPageAuthorizer) AdmitProjectPage(ctx context.Context, req domain.BrokerProjectPageAdmissionRequestV2) (domain.BrokerProjectPageAdmissionDecisionV2, error) {
	a.start()
	decision, err := a.brokerProjectPageAuthorizerStub.AdmitProjectPage(ctx, req)
	a.finish(domain.BrokerPhaseAdmission)
	return decision, err
}

func (a *skewProjectPageAuthorizer) AuthorizeProjectPageQualification(ctx context.Context, req domain.BrokerProjectPageQualificationRequestV2) (domain.BrokerProjectPageQualificationDecisionV2, error) {
	a.start()
	decision, err := a.brokerProjectPageAuthorizerStub.AuthorizeProjectPageQualification(ctx, req)
	a.finish(domain.BrokerPhaseQualificationAuthorization)
	return decision, err
}

func (a *skewProjectPageAuthorizer) AuthorizeProjectPage(ctx context.Context, req domain.BrokerProjectPageOperationAuthorizationRequestV2) (domain.BrokerProjectPageOperationDecisionV2, error) {
	a.start()
	decision, err := a.brokerProjectPageAuthorizerStub.AuthorizeProjectPage(ctx, req)
	a.finish(domain.BrokerPhaseFinalAuthorization)
	return decision, err
}

func TestBrokerProjectPageClockSkewDoesNotExtendDecisionLease(t *testing.T) {
	for _, test := range []struct {
		name         string
		delayPhase   domain.BrokerAuthorizationPhase
		after        string
		wantAttempts int
		wantAllowed  bool
	}{
		{name: "positive skew control", wantAttempts: 3, wantAllowed: true},
		{name: "admission transport elapsed", delayPhase: domain.BrokerPhaseAdmission},
		{name: "qualification transport elapsed", delayPhase: domain.BrokerPhaseQualificationAuthorization},
		{name: "first metadata elapsed", after: "project", wantAttempts: 1},
		{name: "second metadata elapsed", after: "identity", wantAttempts: 2},
		{name: "operation transport elapsed", delayPhase: domain.BrokerPhaseFinalAuthorization, wantAttempts: 2},
		{name: "business response elapsed", after: "business", wantAttempts: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := time.Now().Truncate(time.Millisecond)
			current := started
			request, verified, reader := brokerProjectPageFixture(started.UnixMilli())
			authorizer := &skewProjectPageAuthorizer{brokerProjectPageAuthorizerStub: &brokerProjectPageAuthorizerStub{lease: 150 * time.Millisecond}, current: &current, delayPhase: test.delayPhase}
			advance := func() { current = current.Add(151 * time.Millisecond) }
			switch test.after {
			case "project":
				reader.afterProject = advance
			case "identity":
				reader.afterIdentity = advance
			case "business":
				reader.afterBusiness = advance
			}
			service := brokerProjectPageTestService(authorizer, reader)
			service.now = func() time.Time { return current }
			result, err := service.Execute(t.Context(), request, verified)
			if reader.backendAttempts != test.wantAttempts {
				t.Fatalf("backend attempts=%d want%d err=%v", reader.backendAttempts, test.wantAttempts, err)
			}
			if test.wantAllowed {
				if err != nil || result.Page == nil || result.ReleaseDeadline.After(started.Add(150*time.Millisecond)) {
					t.Fatalf("positive result=%+v err=%v", result, err)
				}
			} else if err == nil || result != (BrokerProjectPageResult{}) {
				t.Fatalf("elapsed lease released result=%+v err=%v", result, err)
			}
		})
	}
}

type queuedProjectPageReader struct {
	*brokerProjectPageReaderStub
	wait time.Duration
}

func (r *queuedProjectPageReader) QualifyBrokerProject(ctx context.Context, key string) (domain.BrokerJiraProjectIdentityV2, error) {
	timer := time.NewTimer(r.wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return domain.BrokerJiraProjectIdentityV2{}, ctx.Err()
	case <-timer.C:
		return r.brokerProjectPageReaderStub.QualifyBrokerProject(ctx, key)
	}
}

func TestBrokerProjectPageSkewedLeaseCancelsQueuedRead(t *testing.T) {
	for _, wait := range []time.Duration{20 * time.Millisecond, 250 * time.Millisecond} {
		t.Run(wait.String(), func(t *testing.T) {
			started := time.Now()
			request, verified, reader := brokerProjectPageFixture(started.UnixMilli())
			authorizer := &brokerProjectPageAuthorizerStub{nowMillis: started.Add(500 * time.Millisecond).UnixMilli(), lease: 150 * time.Millisecond}
			service, err := NewBrokerProjectPageService(authorizer, BrokerJiraProjectPageReader{Backend: verified.Backend, Reader: &queuedProjectPageReader{brokerProjectPageReaderStub: reader, wait: wait}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			result, err := service.Execute(ctx, request, verified)
			if wait < 150*time.Millisecond {
				if err != nil || result.Page == nil || reader.backendAttempts != 3 {
					t.Fatalf("positive queued read result=%+v err=%v attempts=%d", result, err, reader.backendAttempts)
				}
			} else if err == nil || result != (BrokerProjectPageResult{}) || reader.backendAttempts != 0 {
				t.Fatalf("expired queued read result=%+v err=%v attempts=%d", result, err, reader.backendAttempts)
			}
		})
	}
}
