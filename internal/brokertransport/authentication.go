// Package brokertransport owns the authenticated Broker HTTP boundary shared
// by the product server, authority adapter, and later remote clients.
package brokertransport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

const (
	SchemaVersion              = 1
	AuthenticationProfileV1    = "opaque_introspection_v1"
	MaxWorkloadCredentialBytes = 8 << 10
	MaxAuthorityEnvelopeBytes  = 128 << 10
	AuthenticationLeaseMillis  = int64(5_000)
)

type AuthenticationChallenge struct {
	Nonce    string
	Audience string
	BrokerID string
}

type Authentication struct {
	Context         domain.BrokerVerifiedContext
	ReleaseDeadline time.Time
}

type Authenticator interface {
	Authenticate(context.Context, []byte, AuthenticationChallenge) (Authentication, error)
}

type AuthenticationRequest struct {
	SchemaVersion    int
	Nonce            string
	Audience         string
	BrokerID         string
	CredentialSHA256 string
	credential       []byte
}

type AuthenticationResponse struct {
	SchemaVersion    int
	Nonce            string
	CredentialSHA256 string
	IssuerSHA256     string
	IssuedAtMillis   int64
	ExpiresAtMillis  int64
	Context          domain.BrokerVerifiedContext
}

type authenticationRequestWire struct {
	SchemaVersion            int    `json:"schema_version"`
	Nonce                    string `json:"nonce"`
	Audience                 string `json:"audience"`
	BrokerID                 string `json:"broker_id"`
	CredentialSHA256         string `json:"credential_sha256"`
	WorkloadCredentialBase64 string `json:"workload_credential_base64"`
}

type authenticationResponseWire struct {
	SchemaVersion    int             `json:"schema_version"`
	Nonce            string          `json:"nonce"`
	CredentialSHA256 string          `json:"credential_sha256"`
	IssuerSHA256     string          `json:"issuer_sha256"`
	IssuedAtMillis   int64           `json:"issued_at_millis"`
	ExpiresAtMillis  int64           `json:"expires_at_millis"`
	Context          json.RawMessage `json:"context"`
}

func NewAuthenticationRequest(credential []byte, challenge AuthenticationChallenge) (AuthenticationRequest, error) {
	value := AuthenticationRequest{SchemaVersion: SchemaVersion, Nonce: challenge.Nonce, Audience: challenge.Audience, BrokerID: challenge.BrokerID, CredentialSHA256: credentialSHA256(credential), credential: bytes.Clone(credential)}
	if !validAuthenticationRequest(value) {
		return AuthenticationRequest{}, malformedError()
	}
	return value, nil
}

func ValidateAuthenticationChallenge(challenge AuthenticationChallenge) error {
	if !validNonce(challenge.Nonce) || !validIdentifier(challenge.Audience) || !validIdentifier(challenge.BrokerID) {
		return malformedError()
	}
	return nil
}

func EncodeAuthenticationRequestV1(value AuthenticationRequest) ([]byte, error) {
	if !validAuthenticationRequest(value) {
		return nil, malformedError()
	}
	wire := authenticationRequestWire{SchemaVersion: value.SchemaVersion, Nonce: value.Nonce, Audience: value.Audience, BrokerID: value.BrokerID, CredentialSHA256: value.CredentialSHA256, WorkloadCredentialBase64: base64.StdEncoding.EncodeToString(value.credential)}
	return marshalBounded(wire, MaxAuthorityEnvelopeBytes)
}

func DecodeAuthenticationRequestV1(data []byte) (AuthenticationRequest, error) {
	var wire authenticationRequestWire
	if !decodeExact(data, MaxAuthorityEnvelopeBytes, &wire) {
		return AuthenticationRequest{}, malformedError()
	}
	credential, err := base64.StdEncoding.Strict().DecodeString(wire.WorkloadCredentialBase64)
	value := AuthenticationRequest{SchemaVersion: wire.SchemaVersion, Nonce: wire.Nonce, Audience: wire.Audience, BrokerID: wire.BrokerID, CredentialSHA256: wire.CredentialSHA256, credential: credential}
	if err != nil || base64.StdEncoding.EncodeToString(credential) != wire.WorkloadCredentialBase64 || !validAuthenticationRequest(value) {
		return AuthenticationRequest{}, malformedError()
	}
	return value, nil
}

func EncodeAuthenticationResponseV1(value AuthenticationResponse) ([]byte, error) {
	if !validAuthenticationResponse(value) {
		return nil, malformedError()
	}
	contextWire, err := brokercontract.EncodeVerifiedContextV1(value.Context)
	if err != nil {
		return nil, malformedError()
	}
	wire := authenticationResponseWire{SchemaVersion: value.SchemaVersion, Nonce: value.Nonce, CredentialSHA256: value.CredentialSHA256, IssuerSHA256: value.IssuerSHA256, IssuedAtMillis: value.IssuedAtMillis, ExpiresAtMillis: value.ExpiresAtMillis, Context: contextWire}
	return marshalBounded(wire, MaxAuthorityEnvelopeBytes)
}

func DecodeAuthenticationResponseV1(data []byte) (AuthenticationResponse, error) {
	var wire authenticationResponseWire
	if !decodeExact(data, MaxAuthorityEnvelopeBytes, &wire) {
		return AuthenticationResponse{}, malformedError()
	}
	verified, err := brokercontract.DecodeVerifiedContextV1(wire.Context)
	value := AuthenticationResponse{SchemaVersion: wire.SchemaVersion, Nonce: wire.Nonce, CredentialSHA256: wire.CredentialSHA256, IssuerSHA256: wire.IssuerSHA256, IssuedAtMillis: wire.IssuedAtMillis, ExpiresAtMillis: wire.ExpiresAtMillis, Context: verified}
	if err != nil || !validAuthenticationResponse(value) {
		return AuthenticationResponse{}, malformedError()
	}
	return value, nil
}

func ValidateAuthenticationResponseForV1(value AuthenticationResponse, request AuthenticationRequest, issuerSHA256 string, now time.Time) error {
	if !validAuthenticationResponse(value) || !validAuthenticationRequest(request) || value.Nonce != request.Nonce || value.CredentialSHA256 != request.CredentialSHA256 || value.IssuerSHA256 != issuerSHA256 ||
		value.Context.Audience != request.Audience || value.Context.BrokerID != request.BrokerID || now.UnixMilli() < value.IssuedAtMillis-domain.BrokerClockAllowanceMillis || now.UnixMilli() >= value.ExpiresAtMillis ||
		now.UnixMilli() < value.Context.ExecutionNotBeforeMillis-domain.BrokerClockAllowanceMillis || now.UnixMilli() >= value.Context.ExecutionExpiresMillis || now.UnixMilli() >= value.Context.GrantExpiresMillis || now.UnixMilli() >= value.Context.CredentialExpiresMillis {
		return malformedError()
	}
	return nil
}

func validAuthenticationRequest(value AuthenticationRequest) bool {
	return value.SchemaVersion == SchemaVersion && ValidateAuthenticationChallenge(AuthenticationChallenge{Nonce: value.Nonce, Audience: value.Audience, BrokerID: value.BrokerID}) == nil &&
		len(value.credential) > 0 && len(value.credential) <= MaxWorkloadCredentialBytes && value.CredentialSHA256 == credentialSHA256(value.credential)
}

func (value AuthenticationRequest) Credential() []byte { return bytes.Clone(value.credential) }

func (value *AuthenticationRequest) Clear() {
	if value == nil {
		return
	}
	clear(value.credential)
	value.credential = nil
}

func (AuthenticationRequest) String() string   { return "Broker authentication request" }
func (AuthenticationRequest) GoString() string { return "Broker authentication request" }
func (AuthenticationResponse) String() string  { return "Broker authentication response" }
func (AuthenticationResponse) GoString() string {
	return "Broker authentication response"
}
func (Authentication) String() string   { return "Broker authentication result" }
func (Authentication) GoString() string { return "Broker authentication result" }

func validAuthenticationResponse(value AuthenticationResponse) bool {
	_, contextErr := brokercontract.EncodeVerifiedContextV1(value.Context)
	return value.SchemaVersion == SchemaVersion && validNonce(value.Nonce) && validDigest(value.CredentialSHA256) && validDigest(value.IssuerSHA256) && contextErr == nil &&
		value.IssuedAtMillis > 0 && value.ExpiresAtMillis > value.IssuedAtMillis && value.ExpiresAtMillis-value.IssuedAtMillis <= AuthenticationLeaseMillis &&
		value.ExpiresAtMillis <= value.Context.ExecutionExpiresMillis && value.ExpiresAtMillis <= value.Context.GrantExpiresMillis && value.ExpiresAtMillis <= value.Context.CredentialExpiresMillis
}

func validNonce(value string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 24 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func credentialSHA256(value []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("atl-broker-workload-credential-v1\x00"))
	_, _ = hash.Write(value)
	return hex.EncodeToString(hash.Sum(nil))
}
