package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type appFamilyDiscoveryAuthority struct {
	*brokerProjectPageAuthorizerStub
	discover func(context.Context, domain.BrokerFamilyDiscoveryAuthorizationRequestV3) (domain.BrokerFamilyDiscoveryProjectionV3, error)
}

func (a *appFamilyDiscoveryAuthority) DiscoverFamilyV3(ctx context.Context, request domain.BrokerFamilyDiscoveryAuthorizationRequestV3) (domain.BrokerFamilyDiscoveryProjectionV3, error) {
	return a.discover(ctx, request)
}

func TestBrokerFamilyDiscoveryCurrentAndClosedFailures(t *testing.T) {
	for _, name := range []string{"current", "wrong origin", "wrong binding", "wrong family", "stale context", "session ceiling", "expired", "pre-canceled", "canceled", "outage", "late authority", "short elapsed lease", "stale projection"} {
		t.Run(name, func(t *testing.T) {
			started := time.Now()
			observed := started
			_, verified, reader := brokerProjectPageFixture(started.UnixMilli())
			digest, err := brokercontract.VerifiedContextSHA256(verified)
			if err != nil {
				t.Fatal(err)
			}
			request := domain.BrokerFamilyDiscoveryRequestV3{
				SchemaVersion: 3, RequestID: "family-request", ContractFamily: domain.BrokerContractFamilyExecutionV2,
				Service: "jira", BrokerID: verified.BrokerID, Audience: verified.Audience, ContextSHA256: digest,
				NotAfterMillis: started.Add(5 * time.Second).UnixMilli(),
				Expect:         domain.BrokerRequestExpectations{ExecutionID: verified.ExecutionID, ExecutionEpoch: verified.ExecutionEpoch, AuthorityRevision: verified.AuthorityRevision},
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			authority := &appFamilyDiscoveryAuthority{brokerProjectPageAuthorizerStub: &brokerProjectPageAuthorizerStub{nowMillis: started.UnixMilli()}}
			authority.discover = func(ctx context.Context, bound domain.BrokerFamilyDiscoveryAuthorizationRequestV3) (domain.BrokerFamilyDiscoveryProjectionV3, error) {
				calls++
				if deadline, ok := ctx.Deadline(); !ok || deadline.After(started.Add(5*time.Second)) {
					t.Fatal("missing bounded authority context")
				}
				p := domain.BrokerFamilyDiscoveryProjectionV3{
					SchemaVersion: 3, RequestID: request.RequestID, RequestSHA256: bound.RequestSHA256, ContextSHA256: request.ContextSHA256,
					ExecutionID: verified.ExecutionID, ExecutionEpoch: verified.ExecutionEpoch, Audience: verified.Audience,
					BrokerID: verified.BrokerID, AuthorityRevision: verified.AuthorityRevision, ContractFamily: request.ContractFamily, Service: "jira",
					RegistrySHA256: brokercontract.RegistrySHA256V2(), ContractSchemaSHA256: brokercontract.ExecutionSchemaSHA256V2(),
					DiscoverySchemaSHA256: brokercontract.DiscoverySchemaSHA256V3(), IssuedAtMillis: started.UnixMilli(),
					ExpiresAtMillis: request.NotAfterMillis, Complete: true, Operations: []domain.BrokerFamilyDiscoveryOperationV3{},
				}
				for _, row := range brokercontract.AvailableDefinitionsV2() {
					d := row.Definition
					p.Operations = append(p.Operations, domain.BrokerFamilyDiscoveryOperationV3{ID: d.ID, Version: d.Version, Supported: true, Access: domain.BrokerDiscoveryAccessAllowed, Features: d.RequiredFeatures, Limits: d.Limits, Effects: d.Effects})
				}
				switch name {
				case "outage":
					return domain.BrokerFamilyDiscoveryProjectionV3{}, errors.New("private-authority-canary")
				case "canceled":
					cancel()
				case "late authority":
					observed = started.Add(6 * time.Second)
				case "short elapsed lease":
					// Still wall-current, but the one-second lease cannot cover
					// two seconds already spent in the authority call.
					observed = started.Add(2 * time.Second)
					p.IssuedAtMillis = observed.UnixMilli()
					p.ExpiresAtMillis = observed.Add(time.Second).UnixMilli()
				case "stale projection":
					p.AuthorityRevision = "other-revision"
				}
				return p, nil
			}
			service, err := NewBrokerProjectPageService(authority, BrokerJiraProjectPageReader{Backend: verified.Backend, Reader: reader})
			if err != nil {
				t.Fatal(err)
			}
			service.now = func() time.Time { return observed }
			wantCalls := 0
			switch name {
			case "wrong origin":
				reader.origin = strings.Repeat("b", 64)
			case "wrong binding":
				verified.Backend.WorkloadBackendID = "other-backend"
			case "wrong family":
				request.ContractFamily = "other-family"
			case "stale context":
				verified.AuthorityRevision = "other-revision"
			case "session ceiling":
				verified.GrantExpiresMillis = started.Add(time.Second).UnixMilli()
			case "expired":
				request.NotAfterMillis = started.UnixMilli()
			case "pre-canceled":
				cancel()
			default:
				wantCalls = 1
			}
			projection, deadline, err := service.DiscoverExecutionV2(ctx, request, verified)
			if name == "current" {
				if err != nil || !projection.Complete || !deadline.After(started) || deadline.After(started.Add(5*time.Second)) {
					t.Fatalf("projection=%+v deadline=%v error=%v", projection, deadline, err)
				}
			} else if err == nil || !deadline.IsZero() || projection.Complete || strings.Contains(err.Error(), "private-authority-canary") {
				t.Fatalf("accepted or disclosed closed failure: projection=%+v error=%v", projection, err)
			}
			if calls != wantCalls || reader.backendAttempts != 0 || reader.projectCalls != 0 || reader.identityCalls != 0 || reader.businessCalls != 0 || len(authority.calls) != 0 {
				t.Fatalf("unexpected phase calls: discovery=%d backend=%d authorization=%v", calls, reader.backendAttempts, authority.calls)
			}
		})
	}
}
