package brokertransport

import (
	"reflect"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
)

const (
	ExecutePathV3                          = "/v3/execute"
	ExecutionStreamMediaTypeV3             = "application/x-ndjson"
	AuthorizeAttachmentAdmissionPathV3     = "/v3/authorize/attachment/admission"
	AuthorizeAttachmentQualificationPathV3 = "/v3/authorize/attachment/qualification"
	AuthorizeAttachmentOperationPathV3     = "/v3/authorize/attachment/operation"
)

func EncodeExecutionFailureV3(reason domain.BrokerReason) ([]byte, error) {
	value, err := newExecutionFailureV3(reason)
	if err != nil {
		return nil, err
	}
	return marshalBounded(failureWire{domain.BrokerExecutionSchemaVersionV3, value.Status, string(value.Reason), value.Recovery, value.RetrySafe, value.Complete}, MaxTransportFailureBytes)
}

func DecodeExecutionFailureV3(body []byte) (Failure, error) {
	var wire failureWire
	if !decodeExact(body, MaxTransportFailureBytes, &wire) || wire.SchemaVersion != domain.BrokerExecutionSchemaVersionV3 {
		return Failure{}, malformedError()
	}
	value := Failure{wire.SchemaVersion, wire.Status, domain.BrokerReason(wire.Reason), wire.Recovery, wire.RetrySafe, wire.Complete}
	want, err := newExecutionFailureV3(value.Reason)
	if err != nil || !reflect.DeepEqual(value, want) {
		return Failure{}, malformedError()
	}
	return value, nil
}

func newExecutionFailureV3(reason domain.BrokerReason) (Failure, error) {
	ok, safe := brokercontract.ErrorForReason(reason)
	if !ok {
		return Failure{}, malformedError()
	}
	recovery := diagnostic.Recover(safe, diagnostic.OperationRead)
	return Failure{domain.BrokerExecutionSchemaVersionV3, TransportStatusRejected, reason, string(recovery.Action), recovery.RetrySafe, true}, nil
}
