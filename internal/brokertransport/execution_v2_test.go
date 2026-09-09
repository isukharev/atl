package brokertransport

import (
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestExecutionFailureV2AliasesRoundTripWithoutCrossVersionAcceptance(t *testing.T) {
	for _, reason := range []domain.BrokerReason{
		domain.BrokerReasonMalformed,
		domain.BrokerReasonCredentialExpired,
		domain.BrokerReasonStaleExecution,
		domain.BrokerReasonStaleAuthority,
		domain.BrokerReasonDecisionExpired,
		domain.BrokerReasonDenied,
		domain.BrokerReasonRevoked,
		domain.BrokerReasonUnsupported,
		domain.BrokerReasonAuthorizationUnavailable,
		domain.BrokerReasonUnsupportedConsistency,
	} {
		t.Run(string(reason), func(t *testing.T) {
			wire, err := EncodeExecutionFailureV2(reason)
			got, decodeErr := DecodeExecutionFailureV2(wire)
			wantWire, wantErr := EncodeDiscoveryFailureV2(reason)
			want, wantDecodeErr := DecodeDiscoveryFailureV2(wantWire)
			if err != nil || decodeErr != nil || wantErr != nil || wantDecodeErr != nil || !reflect.DeepEqual(got, want) || string(wire) != string(wantWire) {
				t.Fatalf("execution=%+v wire=%s errors=(%v, %v) discovery=%+v wire=%s errors=(%v, %v)", got, wire, err, decodeErr, want, wantWire, wantErr, wantDecodeErr)
			}
			if _, err := DecodeFailureV1(wire); err == nil {
				t.Fatal("execution-v2 failure accepted as execution-v1")
			}
			legacy, err := NewFailure(reason)
			if err != nil {
				t.Fatal(err)
			}
			legacyWire, err := EncodeFailureV1(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeExecutionFailureV2(legacyWire); err == nil {
				t.Fatal("execution-v1 failure accepted as execution-v2")
			}
		})
	}
}
