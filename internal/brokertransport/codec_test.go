package brokertransport

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func TestAuthenticationEnvelopeIsCanonicalAndRequestBound(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	challenge := AuthenticationChallenge{Nonce: testNonce('n'), Audience: "atl-broker", BrokerID: "broker-1"}
	request, err := NewAuthenticationRequest([]byte("synthetic-workload-credential"), challenge)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := EncodeAuthenticationRequestV1(request)
	decoded, decodeErr := DecodeAuthenticationRequestV1(wire)
	credential := request.Credential()
	defer clear(credential)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decoded, request) || bytes.Contains(wire, credential) {
		t.Fatalf("decoded=%+v encode=%v decode=%v wire=%s", decoded, err, decodeErr, wire)
	}
	for _, formatted := range []string{fmt.Sprint(request), fmt.Sprintf("%+v", request), fmt.Sprintf("%#v", request)} {
		if strings.Contains(formatted, "synthetic-workload-credential") || strings.Contains(formatted, request.CredentialSHA256) {
			t.Fatalf("authentication formatting exposed request data: %q", formatted)
		}
	}
	response := AuthenticationResponse{SchemaVersion: 1, Nonce: request.Nonce, CredentialSHA256: request.CredentialSHA256, IssuerSHA256: strings.Repeat("a", 64), IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(5 * time.Second).UnixMilli(), Context: testVerifiedContext(now)}
	responseWire, err := EncodeAuthenticationResponseV1(response)
	decodedResponse, decodeErr := DecodeAuthenticationResponseV1(responseWire)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decodedResponse, response) || ValidateAuthenticationResponseForV1(decodedResponse, request, response.IssuerSHA256, now) != nil {
		t.Fatalf("decoded=%+v encode=%v decode=%v", decodedResponse, err, decodeErr)
	}

	changed := request
	changed.Nonce = testNonce('x')
	if ValidateAuthenticationResponseForV1(response, changed, response.IssuerSHA256, now) == nil {
		t.Fatal("cross-nonce authentication response accepted")
	}
	changed = request
	changed.credential = []byte("other-workload-credential")
	changed.CredentialSHA256 = credentialSHA256(changed.credential)
	if ValidateAuthenticationResponseForV1(response, changed, response.IssuerSHA256, now) == nil {
		t.Fatal("cross-credential authentication response accepted")
	}
	if ValidateAuthenticationResponseForV1(response, request, strings.Repeat("b", 64), now) == nil || ValidateAuthenticationResponseForV1(response, request, response.IssuerSHA256, now.Add(5*time.Second)) == nil {
		t.Fatal("wrong issuer or expired authentication response accepted")
	}
}

func TestTransportFailureAndProtocolAreClosed(t *testing.T) {
	for _, reason := range []domain.BrokerReason{domain.BrokerReasonMalformed, domain.BrokerReasonDenied, domain.BrokerReasonCredentialExpired, domain.BrokerReasonAuthorizationUnavailable, domain.BrokerReasonUnsupportedConsistency} {
		failure, err := NewFailure(reason)
		wire, encodeErr := EncodeFailureV1(failure)
		decoded, decodeErr := DecodeFailureV1(wire)
		if err != nil || encodeErr != nil || decodeErr != nil || !reflect.DeepEqual(decoded, failure) || !decoded.Complete || decoded.Recovery == "" {
			t.Fatalf("reason=%q decoded=%+v errors=%v/%v/%v", reason, decoded, err, encodeErr, decodeErr)
		}
	}
	protocol := StaticProtocolV1()
	wire, err := EncodeProtocolV1(protocol)
	decoded, decodeErr := DecodeProtocolV1(wire)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decoded, protocol) || len(decoded.Operations) != 2 {
		t.Fatalf("protocol=%+v errors=%v/%v", decoded, err, decodeErr)
	}
}

func TestTransportEnvelopesRejectLossyOrExpandedJSON(t *testing.T) {
	request, _ := NewAuthenticationRequest([]byte("synthetic-workload-credential"), AuthenticationChallenge{Nonce: testNonce('n'), Audience: "atl-broker", BrokerID: "broker-1"})
	authWire, _ := EncodeAuthenticationRequestV1(request)
	failure, _ := NewFailure(domain.BrokerReasonDenied)
	failureWire, _ := EncodeFailureV1(failure)
	protocolWire, _ := EncodeProtocolV1(StaticProtocolV1())
	for name, data := range map[string][]byte{
		"auth duplicate":     bytes.Replace(authWire, []byte(`"nonce":`), []byte(`"nonce":"duplicate","nonce":`), 1),
		"auth unknown":       bytes.Replace(authWire, []byte(`"nonce":`), []byte(`"unknown":true,"nonce":`), 1),
		"auth trailing":      append(bytes.Clone(authWire), []byte(`{}`)...),
		"failure case alias": bytes.Replace(failureWire, []byte(`"status":`), []byte(`"Status":`), 1),
		"failure null bool":  bytes.Replace(failureWire, []byte(`"retry_safe":false`), []byte(`"retry_safe":null`), 1),
		"protocol child":     bytes.Replace(protocolWire, []byte(`"id":`), []byte(`"unknown":true,"id":`), 1),
		"protocol null list": bytes.Replace(protocolWire, []byte(`"features":[]`), []byte(`"features":null`), 1),
		"invalid utf8":       append(bytes.Clone(failureWire), 0xff),
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			switch {
			case strings.HasPrefix(name, "auth"):
				_, err = DecodeAuthenticationRequestV1(data)
			case strings.HasPrefix(name, "failure") || name == "invalid utf8":
				_, err = DecodeFailureV1(data)
			default:
				_, err = DecodeProtocolV1(data)
			}
			if err == nil || !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestErrorForReasonRejectsOpenVocabulary(t *testing.T) {
	if ok, err := brokercontract.ErrorForReason("private-policy-text"); ok || err != nil {
		t.Fatalf("err=%v ok=%t", err, ok)
	}
}

func FuzzAuthenticationAndTransportEnvelopes(f *testing.F) {
	request, _ := NewAuthenticationRequest([]byte("synthetic-workload-credential"), AuthenticationChallenge{Nonce: testNonce('n'), Audience: "atl-broker", BrokerID: "broker-1"})
	authWire, _ := EncodeAuthenticationRequestV1(request)
	failure, _ := NewFailure(domain.BrokerReasonDenied)
	failureWire, _ := EncodeFailureV1(failure)
	protocolWire, _ := EncodeProtocolV1(StaticProtocolV1())
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	authResponseWire, _ := EncodeAuthenticationResponseV1(AuthenticationResponse{SchemaVersion: 1, Nonce: request.Nonce, CredentialSHA256: request.CredentialSHA256, IssuerSHA256: strings.Repeat("a", 64), IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(5 * time.Second).UnixMilli(), Context: testVerifiedContext(now)})
	f.Add(uint8(0), authWire)
	f.Add(uint8(1), failureWire)
	f.Add(uint8(2), protocolWire)
	f.Add(uint8(3), authResponseWire)
	f.Add(uint8(0), []byte(`{"nonce":"duplicate","nonce":"duplicate"}`))
	f.Fuzz(func(t *testing.T, kind uint8, data []byte) {
		if len(data) > int(MaxProtocolBytes) {
			t.Skip()
		}
		switch kind % 4 {
		case 0:
			value, err := DecodeAuthenticationRequestV1(data)
			if err == nil {
				encoded, encodeErr := EncodeAuthenticationRequestV1(value)
				roundTrip, roundTripErr := DecodeAuthenticationRequestV1(encoded)
				if encodeErr != nil || roundTripErr != nil || !reflect.DeepEqual(roundTrip, value) {
					t.Fatalf("authentication round trip failed: encode=%v decode=%v", encodeErr, roundTripErr)
				}
			}
		case 1:
			value, err := DecodeFailureV1(data)
			if err == nil {
				encoded, encodeErr := EncodeFailureV1(value)
				roundTrip, roundTripErr := DecodeFailureV1(encoded)
				if encodeErr != nil || roundTripErr != nil || !reflect.DeepEqual(roundTrip, value) {
					t.Fatalf("failure round trip failed: encode=%v decode=%v", encodeErr, roundTripErr)
				}
			}
		case 2:
			value, err := DecodeProtocolV1(data)
			if err == nil {
				encoded, encodeErr := EncodeProtocolV1(value)
				roundTrip, roundTripErr := DecodeProtocolV1(encoded)
				if encodeErr != nil || roundTripErr != nil || !reflect.DeepEqual(roundTrip, value) {
					t.Fatalf("protocol round trip failed: encode=%v decode=%v", encodeErr, roundTripErr)
				}
			}
		case 3:
			value, err := DecodeAuthenticationResponseV1(data)
			if err == nil {
				encoded, encodeErr := EncodeAuthenticationResponseV1(value)
				roundTrip, roundTripErr := DecodeAuthenticationResponseV1(encoded)
				if encodeErr != nil || roundTripErr != nil || !reflect.DeepEqual(roundTrip, value) {
					t.Fatalf("authentication response round trip failed: encode=%v decode=%v", encodeErr, roundTripErr)
				}
			}
		}
	})
}

func testVerifiedContext(now time.Time) domain.BrokerVerifiedContext {
	return domain.BrokerVerifiedContext{
		PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "revision-1",
		ExecutionNotBeforeMillis: now.Add(-time.Second).UnixMilli(), ExecutionExpiresMillis: now.Add(time.Minute).UnixMilli(), GrantExpiresMillis: now.Add(time.Minute).UnixMilli(), CredentialExpiresMillis: now.Add(time.Minute).UnixMilli(),
		Backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: strings.Repeat("b", 64), WorkloadBackendID: "jira-primary"},
	}
}

func testNonce(value byte) string {
	return strings.Repeat(string(value), 32)
}
