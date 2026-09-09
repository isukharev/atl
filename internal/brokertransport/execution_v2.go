package brokertransport

import "github.com/isukharev/atl/internal/domain"

const ExecutePathV2 = "/v2/execute"

// EncodeExecutionFailureV2 uses the existing closed, read-only v2 failure shape
// while keeping execution consumers independent of discovery route selection.
func EncodeExecutionFailureV2(reason domain.BrokerReason) ([]byte, error) {
	return EncodeDiscoveryFailureV2(reason)
}

func DecodeExecutionFailureV2(body []byte) (Failure, error) {
	return DecodeDiscoveryFailureV2(body)
}
