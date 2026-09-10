package brokerserver

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

func TestAttachmentDiscoveryRoutesKeepTheirClosedVersionAndAuthBoundary(t *testing.T) {
	for _, path := range []string{brokertransport.DiscoveryNegotiatePathV4, brokertransport.DiscoveryPathV4} {
		for _, malformed := range []bool{false, true} {
			fixture := newBrokerServerFixture(t, "unused", "", nil)
			verified := fixture.authenticator.authentication.Context
			hello := brokertransport.DiscoveryNegotiationV4{
				SchemaVersion: 4, RequestID: "request-1", ContractFamily: domain.BrokerContractFamilyExecutionV3,
				Service: "jira", BrokerID: verified.BrokerID, Audience: verified.Audience, ExecutionID: verified.ExecutionID,
				ExecutionEpoch: verified.ExecutionEpoch, AuthorityRevision: verified.AuthorityRevision,
				NotAfterMillis: fixture.baseTime.Add(5 * time.Second).UnixMilli(),
			}
			body, err := brokertransport.EncodeDiscoveryNegotiationV4(hello)
			if err != nil {
				t.Fatal(err)
			}
			if path == brokertransport.DiscoveryPathV4 {
				discovery, bindErr := brokertransport.BindDiscoveryNegotiationV4(hello, verified, fixture.baseTime)
				if bindErr != nil {
					t.Fatal(bindErr)
				}
				body, err = brokercontract.EncodeFamilyDiscoveryRequestV4(discovery)
				if err != nil {
					t.Fatal(err)
				}
			}
			wantReason, wantAuth := domain.BrokerReasonUnsupported, 1
			if malformed {
				body = []byte(`{}`)
				wantReason, wantAuth = domain.BrokerReasonMalformed, 0
			}
			request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer synthetic-workload-credential")
			writer := &deadlineResponseWriter{}
			fixture.handler.ServeHTTP(writer, request)
			failure, err := brokertransport.DecodeDiscoveryFailureV4(writer.body.Bytes())
			if err != nil || failure.Reason != wantReason || fixture.authenticator.calls != wantAuth || fixture.backendCalls.Load() != 0 || fixture.authorizer.admissionCalls != 0 || len(fixture.handler.permits) != 0 {
				t.Fatalf("route=%s malformed=%t reason=%s decode=%v auth=%d backend=%d", path, malformed, failure.Reason, err, fixture.authenticator.calls, fixture.backendCalls.Load())
			}
		}
	}
}
