package brokertransport

import (
	"testing"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func TestProtocolV1RetainsExactReadProfileWithGuardedSemanticRegistry(t *testing.T) {
	protocol := StaticProtocolV1()
	if len(brokercontract.AvailableDefinitions()) != 5 || len(protocol.Operations) != 2 || protocol.ExecutionProfile != "exact_reads_v1" {
		t.Fatal("HTTP read profile and broader semantic registry were conflated")
	}
	for index, id := range []domain.BrokerOperationID{domain.BrokerOperationConfluencePageRead, domain.BrokerOperationJiraIssueRead} {
		operation := protocol.Operations[index]
		if operation.ID != id || operation.Version != 1 || len(operation.Features) != 0 || operation.MaxRequestBytes != 64<<10 {
			t.Fatalf("HTTP exact-read profile drift: %+v", operation)
		}
	}
	for _, id := range []domain.BrokerOperationID{domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationJiraCommentApply, domain.BrokerOperationOutcomeLookup} {
		changed := protocol
		changed.Operations = append([]ProtocolOperation(nil), protocol.Operations...)
		// Change only the ID, retaining valid read-profile bounds and features,
		// so refusal proves the operation boundary rather than another limit.
		changed.Operations[0].ID = id
		if _, err := EncodeProtocolV1(changed); err == nil {
			t.Fatalf("HTTP read descriptor admitted %s", id)
		}
	}
}
