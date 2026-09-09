package app

import (
	"context"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

type skewConfluenceRead struct {
	*brokerReadConfluenceStub
	afterQualification func()
	afterBusiness      func()
}

func (r *skewConfluenceRead) QualifyBrokerPage(ctx context.Context, id string) (domain.BrokerConfluencePageIdentity, error) {
	value, err := r.brokerReadConfluenceStub.QualifyBrokerPage(ctx, id)
	if r.afterQualification != nil {
		r.afterQualification()
	}
	return value, err
}

func (r *skewConfluenceRead) ReadBrokerPage(ctx context.Context, id string, projection domain.BrokerConfluenceProjection) (domain.BrokerConfluencePageSnapshot, error) {
	value, err := r.brokerReadConfluenceStub.ReadBrokerPage(ctx, id, projection)
	if r.afterBusiness != nil {
		r.afterBusiness()
	}
	return value, err
}

func TestBrokerExactReadClockSkewCannotExtendMetadataOrReleaseLease(t *testing.T) {
	for _, backend := range []string{"jira", "confluence"} {
		for _, after := range []string{"positive", "qualification", "business"} {
			t.Run(backend+"/"+after, func(t *testing.T) {
				started := time.Now().Truncate(time.Millisecond)
				current := started
				request, verified, jira, confluence := brokerReadFixture(started.UnixMilli())
				authorizer := &brokerReadAuthorizerStub{nowMillis: started.Add(800 * time.Millisecond).UnixMilli(), lease: 150 * time.Millisecond}
				wrapped := &skewConfluenceRead{brokerReadConfluenceStub: confluence}
				advance := func() { current = current.Add(151 * time.Millisecond) }
				switch after {
				case "qualification":
					jira.afterQualification, wrapped.afterQualification = advance, advance
				case "business":
					jira.afterBusiness, wrapped.afterBusiness = advance, advance
				}
				if backend == "confluence" {
					request.Operation = domain.BrokerOperationConfluencePageRead
					request.Arguments = domain.BrokerOperationArguments{ConfluencePageRead: &domain.BrokerConfluencePageReadArguments{PageID: "42", Projection: domain.BrokerConfluenceProjectionStorage}}
					verified.Backend = brokerConfluenceTestBackend()
				}
				service, err := NewBrokerReadService(authorizer, BrokerJiraIssueReader{Backend: brokerJiraTestBackend(), Reader: jira}, BrokerConfluencePageReader{Backend: brokerConfluenceTestBackend(), Reader: wrapped})
				if err != nil {
					t.Fatal(err)
				}
				service.now = func() time.Time { return current }
				result, err := service.Execute(t.Context(), request, verified)
				attempts := jira.backendAttempts + confluence.backendAttempts
				wantAttempts := 2
				if after == "qualification" {
					wantAttempts = 1
				}
				if attempts != wantAttempts {
					t.Fatalf("attempts=%d want%d err=%v", attempts, wantAttempts, err)
				}
				if after == "positive" {
					if err != nil || result.ReleaseDeadline.IsZero() || result.ReleaseDeadline.After(started.Add(150*time.Millisecond)) {
						t.Fatalf("positive result=%+v err=%v", result, err)
					}
				} else if err == nil || result != (BrokerExactReadResult{}) {
					t.Fatalf("elapsed lease result=%+v err=%v", result, err)
				}
			})
		}
	}
}
