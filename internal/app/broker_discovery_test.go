package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type appDiscoveryAuthority struct {
	*brokerReadAuthorizerStub
	discover func(context.Context, domain.BrokerDiscoveryAuthorizationRequestV2) (domain.BrokerDiscoveryProjectionV2, error)
}

func (a *appDiscoveryAuthority) DiscoverV2(ctx context.Context, request domain.BrokerDiscoveryAuthorizationRequestV2) (domain.BrokerDiscoveryProjectionV2, error) {
	return a.discover(ctx, request)
}

func TestBrokerDiscoveryRejectsDriftExpiryCancellationAndAuthorityOutage(t *testing.T) {
	for _, name := range []string{"stale context", "session ceiling", "expired", "late authority", "canceled", "outage", "current"} {
		t.Run(name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Millisecond)
			_, verified, jira, confluence := brokerReadFixture(now.UnixMilli())
			digest, _ := brokercontract.VerifiedContextSHA256(verified)
			request := domain.BrokerDiscoveryRequestV2{SchemaVersion: 2, RequestID: "discovery-1", Service: "jira", BrokerID: verified.BrokerID, Audience: verified.Audience, ContextSHA256: digest, NotAfterMillis: now.Add(5 * time.Second).UnixMilli(), Expect: domain.BrokerRequestExpectations{ExecutionID: verified.ExecutionID, ExecutionEpoch: verified.ExecutionEpoch, AuthorityRevision: verified.AuthorityRevision}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			authority := &appDiscoveryAuthority{brokerReadAuthorizerStub: &brokerReadAuthorizerStub{nowMillis: now.UnixMilli()}}
			authority.discover = func(_ context.Context, bound domain.BrokerDiscoveryAuthorizationRequestV2) (domain.BrokerDiscoveryProjectionV2, error) {
				calls++
				if name == "outage" {
					return domain.BrokerDiscoveryProjectionV2{}, errors.New("private-authority-canary")
				}
				definition, _ := brokercontract.Definition(domain.BrokerOperationJiraIssueRead, 1)
				p := domain.BrokerDiscoveryProjectionV2{SchemaVersion: 2, RequestID: request.RequestID, RequestSHA256: bound.RequestSHA256, ContextSHA256: request.ContextSHA256, ExecutionID: verified.ExecutionID, ExecutionEpoch: verified.ExecutionEpoch, Audience: verified.Audience, BrokerID: verified.BrokerID, AuthorityRevision: verified.AuthorityRevision, Service: "jira", RegistrySHA256: brokercontract.RegistrySHA256(), ContractSchemaSHA256: brokercontract.SchemaSHA256(), DiscoverySchemaSHA256: brokercontract.DiscoverySchemaSHA256V2(), IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: request.NotAfterMillis, Complete: true, Operations: []domain.BrokerDiscoveryOperationV2{{ID: definition.ID, Version: definition.Version, Supported: true, Access: domain.BrokerDiscoveryAccessAllowed, Features: definition.RequiredFeatures, Limits: definition.Limits, Effects: definition.Effects}}}
				if name == "late authority" {
					now = now.Add(6 * time.Second)
				}
				if name == "canceled" {
					cancel()
				}
				return p, nil
			}
			service := brokerReadTestService(authority, jira, confluence)
			service.now = func() time.Time { return now }
			switch name {
			case "stale context":
				verified.AuthorityRevision = "revision-other"
			case "session ceiling":
				verified.GrantExpiresMillis = now.Add(time.Second).UnixMilli()
			case "expired":
				request.NotAfterMillis = now.UnixMilli()
			}
			p, deadline, err := service.Discover(ctx, request, verified)
			if name == "current" {
				if err != nil || !p.Complete || deadline.IsZero() {
					t.Fatalf("projection=%+v deadline=%v err=%v", p, deadline, err)
				}
			} else if err == nil || !deadline.IsZero() || p.Complete {
				t.Fatalf("accepted %s: projection=%+v err=%v", name, p, err)
			}
			if jira.qualification != 0 || jira.business != 0 || confluence.qualification != 0 || confluence.business != 0 || len(authority.calls) != 0 {
				t.Fatal("discovery entered operation/qualification path")
			}
			if (name == "stale context" || name == "session ceiling" || name == "expired") && calls != 0 {
				t.Fatal("invalid request reached authority")
			}
		})
	}
}
