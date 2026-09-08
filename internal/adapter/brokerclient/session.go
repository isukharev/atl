package brokerclient

import (
	"bytes"
	"fmt"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
	"github.com/isukharev/atl/internal/strictjson"
)

const MaxSessionBytes = int64(16 << 10)

type Session struct {
	Credential        []byte
	ExecutionID       string
	ExecutionEpoch    string
	AuthorityRevision string
}

type sessionWire struct {
	SchemaVersion     int    `json:"schema_version"`
	Credential        string `json:"credential"`
	ExecutionID       string `json:"execution_id"`
	ExecutionEpoch    string `json:"execution_epoch"`
	AuthorityRevision string `json:"authority_revision"`
}

type SessionLoader interface {
	Load() (Session, error)
}

type FileSessionLoader struct{ Path string }

func (l FileSessionLoader) Load() (Session, error) {
	body, err := safepath.ReadFilePrivate(l.Path, MaxSessionBytes)
	if err != nil {
		return Session{}, clientError(domain.ErrConfig)
	}
	var wire sessionWire
	if strictjson.DecodeExact(body, brokercontract.MaxCanonicalDepth, &wire) != nil {
		return Session{}, clientError(domain.ErrConfig)
	}
	session := Session{
		Credential: []byte(wire.Credential), ExecutionID: wire.ExecutionID,
		ExecutionEpoch: wire.ExecutionEpoch, AuthorityRevision: wire.AuthorityRevision,
	}
	if wire.SchemaVersion != 1 || !validCredential(session.Credential) ||
		!validIdentifier(session.ExecutionID, domain.BrokerMaxIdentifierBytes) ||
		!validIdentifier(session.ExecutionEpoch, domain.BrokerMaxIdentifierBytes) ||
		!validIdentifier(session.AuthorityRevision, domain.BrokerMaxAuthorityRevisionBytes) {
		session.Clear()
		return Session{}, clientError(domain.ErrConfig)
	}
	return session, nil
}

func (s *Session) Clear() {
	if s == nil {
		return
	}
	for index := range s.Credential {
		s.Credential[index] = 0
	}
	s.Credential = nil
	*s = Session{}
}

func (s Session) expectations() domain.BrokerRequestExpectations {
	return domain.BrokerRequestExpectations{ExecutionID: s.ExecutionID, ExecutionEpoch: s.ExecutionEpoch, AuthorityRevision: s.AuthorityRevision}
}

func validCredential(value []byte) bool {
	if len(value) < brokertransport.MinWorkloadCredentialBytes || len(value) > brokertransport.MaxWorkloadCredentialBytes || !utf8.Valid(value) || !bytes.Equal(bytes.TrimSpace(value), value) {
		return false
	}
	for _, current := range value {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	return true
}

func validIdentifier(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || value != string(bytes.TrimSpace([]byte(value))) {
		return false
	}
	for _, current := range value {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	return true
}

type closedError struct{ cause error }

func (*closedError) Error() string   { return "Broker client request failed" }
func (e *closedError) Unwrap() error { return e.cause }

func clientError(cause error) error {
	if ok, safe := brokercontract.ContentFreeError(cause); ok {
		return safe
	}
	return &closedError{cause: cause}
}

func unsupported() error {
	_, err := brokercontract.ErrorForReason(domain.BrokerReasonUnsupported)
	if err == nil {
		panic(fmt.Sprintf("missing Broker reason %q", domain.BrokerReasonUnsupported))
	}
	return err
}
