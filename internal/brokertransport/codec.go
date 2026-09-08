package brokertransport

import (
	"encoding/json"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

const (
	MaxTransportFailureBytes = int64(4 << 10)
	MaxProtocolBytes         = int64(64 << 10)
	MaxAdminStatusBytes      = int64(4 << 10)
	TransportStatusRejected  = "rejected"
)

const (
	AdminKindHealth        = "health"
	AdminKindReadiness     = "readiness"
	AdminStatusHealthy     = "healthy"
	AdminStatusReady       = "ready"
	AdminStatusDraining    = "draining"
	AdminStatusUnavailable = "unavailable"
)

type Failure struct {
	SchemaVersion int
	Status        string
	Reason        domain.BrokerReason
	Recovery      string
	RetrySafe     bool
	Complete      bool
}

type ProtocolOperation struct {
	ID               domain.BrokerOperationID
	Version          int
	Features         []string
	MaxRequestBytes  int64
	MaxResponseBytes int64
}

type Protocol struct {
	SchemaVersion         int
	ContractSchemaSHA256  string
	TransportSchemaSHA256 string
	RegistrySHA256        string
	AuthenticationProfile string
	ExecutionProfile      string
	ConsistencyProfile    string
	Operations            []ProtocolOperation
	Complete              bool
}

type AdminStatus struct {
	SchemaVersion int
	Kind          string
	Status        string
	Complete      bool
}

type failureWire struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	Reason        string `json:"reason"`
	Recovery      string `json:"recovery"`
	RetrySafe     bool   `json:"retry_safe"`
	Complete      bool   `json:"complete"`
}

type protocolOperationWire struct {
	ID               string   `json:"id"`
	Version          int      `json:"version"`
	Features         []string `json:"features"`
	MaxRequestBytes  int64    `json:"max_request_bytes"`
	MaxResponseBytes int64    `json:"max_response_bytes"`
}

type protocolWire struct {
	SchemaVersion         int                     `json:"schema_version"`
	ContractSchemaSHA256  string                  `json:"contract_schema_sha256"`
	TransportSchemaSHA256 string                  `json:"transport_schema_sha256"`
	RegistrySHA256        string                  `json:"registry_sha256"`
	AuthenticationProfile string                  `json:"authentication_profile"`
	ExecutionProfile      string                  `json:"execution_profile"`
	ConsistencyProfile    string                  `json:"consistency_profile"`
	Operations            []protocolOperationWire `json:"operations"`
	Complete              bool                    `json:"complete"`
}

type adminStatusWire struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	Complete      bool   `json:"complete"`
}

func NewFailure(reason domain.BrokerReason) (Failure, error) {
	ok, safe := brokercontract.ErrorForReason(reason)
	if !ok {
		return Failure{}, malformedError()
	}
	recovery := diagnostic.Recover(safe, diagnostic.OperationRead)
	return Failure{SchemaVersion: SchemaVersion, Status: TransportStatusRejected, Reason: reason, Recovery: string(recovery.Action), RetrySafe: recovery.RetrySafe, Complete: true}, nil
}

func EncodeFailureV1(value Failure) ([]byte, error) {
	if !validFailure(value) {
		return nil, malformedError()
	}
	wire := failureWire{value.SchemaVersion, value.Status, string(value.Reason), value.Recovery, value.RetrySafe, value.Complete}
	return marshalBounded(wire, MaxTransportFailureBytes)
}

func DecodeFailureV1(data []byte) (Failure, error) {
	var wire failureWire
	if !decodeExact(data, MaxTransportFailureBytes, &wire) {
		return Failure{}, malformedError()
	}
	value := Failure{wire.SchemaVersion, wire.Status, domain.BrokerReason(wire.Reason), wire.Recovery, wire.RetrySafe, wire.Complete}
	if !validFailure(value) {
		return Failure{}, malformedError()
	}
	return value, nil
}

func StaticProtocolV1() Protocol {
	definitions := brokercontract.AvailableDefinitions()
	operations := make([]ProtocolOperation, len(definitions))
	for index, definition := range definitions {
		operations[index] = ProtocolOperation{definition.ID, definition.Version, append([]string{}, definition.RequiredFeatures...), definition.Limits.MaxRequestBytes, definition.Limits.MaxResponseBytes}
	}
	return Protocol{SchemaVersion: SchemaVersion, ContractSchemaSHA256: brokercontract.SchemaSHA256(), TransportSchemaSHA256: SchemaSHA256(), RegistrySHA256: brokercontract.RegistrySHA256(), AuthenticationProfile: AuthenticationProfileV1, ExecutionProfile: "exact_reads_v1", ConsistencyProfile: domain.BrokerReadConsistencyIdentitySnapshotV1, Operations: operations, Complete: true}
}

func EncodeProtocolV1(value Protocol) ([]byte, error) {
	if !validProtocol(value) {
		return nil, malformedError()
	}
	operations := make([]protocolOperationWire, len(value.Operations))
	for index, operation := range value.Operations {
		operations[index] = protocolOperationWire{string(operation.ID), operation.Version, append([]string{}, operation.Features...), operation.MaxRequestBytes, operation.MaxResponseBytes}
	}
	wire := protocolWire{value.SchemaVersion, value.ContractSchemaSHA256, value.TransportSchemaSHA256, value.RegistrySHA256, value.AuthenticationProfile, value.ExecutionProfile, value.ConsistencyProfile, operations, value.Complete}
	return marshalBounded(wire, MaxProtocolBytes)
}

func DecodeProtocolV1(data []byte) (Protocol, error) {
	var wire protocolWire
	if !decodeExact(data, MaxProtocolBytes, &wire) {
		return Protocol{}, malformedError()
	}
	operations := make([]ProtocolOperation, len(wire.Operations))
	for index, operation := range wire.Operations {
		operations[index] = ProtocolOperation{domain.BrokerOperationID(operation.ID), operation.Version, append([]string{}, operation.Features...), operation.MaxRequestBytes, operation.MaxResponseBytes}
	}
	value := Protocol{wire.SchemaVersion, wire.ContractSchemaSHA256, wire.TransportSchemaSHA256, wire.RegistrySHA256, wire.AuthenticationProfile, wire.ExecutionProfile, wire.ConsistencyProfile, operations, wire.Complete}
	if !validProtocol(value) {
		return Protocol{}, malformedError()
	}
	return value, nil
}

func EncodeAdminStatusV1(value AdminStatus) ([]byte, error) {
	if !validAdminStatus(value) {
		return nil, malformedError()
	}
	return marshalBounded(adminStatusWire(value), MaxAdminStatusBytes)
}

func DecodeAdminStatusV1(data []byte) (AdminStatus, error) {
	var wire adminStatusWire
	if !decodeExact(data, MaxAdminStatusBytes, &wire) {
		return AdminStatus{}, malformedError()
	}
	value := AdminStatus(wire)
	if !validAdminStatus(value) {
		return AdminStatus{}, malformedError()
	}
	return value, nil
}

func validFailure(value Failure) bool {
	want, err := NewFailure(value.Reason)
	return err == nil && reflect.DeepEqual(value, want)
}

func validProtocol(value Protocol) bool {
	return reflect.DeepEqual(value, StaticProtocolV1())
}

func validAdminStatus(value AdminStatus) bool {
	if value.SchemaVersion != SchemaVersion || !value.Complete {
		return false
	}
	switch value.Kind {
	case AdminKindHealth:
		return value.Status == AdminStatusHealthy
	case AdminKindReadiness:
		return value.Status == AdminStatusReady || value.Status == AdminStatusDraining || value.Status == AdminStatusUnavailable
	default:
		return false
	}
}

func decodeExact(data []byte, maximum int64, target any) bool {
	return len(data) > 0 && int64(len(data)) <= maximum && strictjson.DecodeExact(data, brokercontract.MaxCanonicalDepth, target) == nil
}

func marshalBounded(value any, maximum int64) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil || int64(len(data)) > maximum {
		return nil, malformedError()
	}
	return data, nil
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > domain.BrokerMaxIdentifierBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, current := range value {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, current := range value {
		if current < '0' || current > '9' && current < 'a' || current > 'f' {
			return false
		}
	}
	return true
}

func malformedError() error {
	_, err := brokercontract.ErrorForReason(domain.BrokerReasonMalformed)
	return err
}
