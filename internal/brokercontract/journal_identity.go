package brokercontract

import "github.com/isukharev/atl/internal/domain"

const journalDigestNamespace = "atl.broker.journal.v1/"

// BrokerJournalOwnerV1 derives stable ownership separately from the original
// execution and authority revision. A later authorized execution with the same
// owner can therefore observe an old writer without reviving it.
func BrokerJournalOwnerV1(value domain.BrokerVerifiedContext) (domain.BrokerJournalOwner, error) {
	if err := validateContext(value); err != nil {
		return domain.BrokerJournalOwner{}, err
	}
	broker, err := BrokerJournalBrokerSHA256V1(value.BrokerID)
	if err != nil {
		return domain.BrokerJournalOwner{}, err
	}
	backend, err := BrokerJournalBackendSHA256V1(value.Backend)
	if err != nil {
		return domain.BrokerJournalOwner{}, err
	}
	principal, err := journalDigest("owner/principal", struct {
		PrincipalID string `json:"principal_id"`
	}{value.PrincipalID})
	if err != nil {
		return domain.BrokerJournalOwner{}, err
	}
	workload, err := journalDigest("owner/workload", struct {
		WorkloadID string `json:"workload_id"`
	}{value.WorkloadID})
	if err != nil {
		return domain.BrokerJournalOwner{}, err
	}
	audience, err := journalDigest("owner/audience", struct {
		Audience string `json:"audience"`
	}{value.Audience})
	if err != nil {
		return domain.BrokerJournalOwner{}, err
	}
	return domain.BrokerJournalOwner{
		BrokerSHA256: broker, BackendSHA256: backend, PrincipalSHA256: principal,
		WorkloadSHA256: workload, AudienceSHA256: audience,
	}, nil
}

// BrokerJournalBrokerSHA256V1 binds operator-owned journal storage to a Broker
// without requiring or inventing an authenticated workload context.
func BrokerJournalBrokerSHA256V1(brokerID string) (string, error) {
	if !validIdentifier(brokerID) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return journalDigest("owner/broker", struct {
		BrokerID string `json:"broker_id"`
	}{brokerID})
}

// BrokerJournalBackendSHA256V1 binds the complete configured destination, not
// merely its origin digest.
func BrokerJournalBackendSHA256V1(value domain.BrokerBackendBinding) (string, error) {
	if !validService(value.Service) || !validDigest(value.OriginSHA256) || !validIdentifier(value.WorkloadBackendID) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return journalDigest("owner/backend", struct {
		Service           string `json:"service"`
		OriginSHA256      string `json:"origin_sha256"`
		WorkloadBackendID string `json:"workload_backend_id"`
	}{value.Service, value.OriginSHA256, value.WorkloadBackendID})
}

// BrokerJournalWriterSHA256V1 derives the original writer fields kept outside
// stable ownership. Observers must not compare these with their current values.
func BrokerJournalWriterSHA256V1(value domain.BrokerVerifiedContext) (executionSHA256, authorityRevisionSHA256 string, err error) {
	if err := validateContext(value); err != nil {
		return "", "", err
	}
	executionSHA256, err = journalDigest("writer/execution", struct {
		ExecutionID    string `json:"execution_id"`
		ExecutionEpoch string `json:"execution_epoch"`
	}{value.ExecutionID, value.ExecutionEpoch})
	if err != nil {
		return "", "", err
	}
	authorityRevisionSHA256, err = journalDigest("writer/authority-revision", struct {
		AuthorityRevision string `json:"authority_revision"`
	}{value.AuthorityRevision})
	return executionSHA256, authorityRevisionSHA256, err
}

// BrokerOperationObservationResourceV1 constructs the final authorization
// resource entirely from the normalized input ticket. Its digests prove the
// selector and closed projection, not journal existence, ownership, or phase.
func BrokerOperationObservationResourceV1(operationTicket string) (domain.BrokerQualifiedResource, error) {
	if !validIdentifier(operationTicket) {
		return domain.BrokerQualifiedResource{}, reject(domain.BrokerReasonMalformed)
	}
	selector := struct {
		OperationTicket string `json:"operation_ticket"`
	}{operationTicket}
	versionEvidence, err := digestValue("operation-resource-version-evidence", selector)
	if err != nil {
		return domain.BrokerQualifiedResource{}, err
	}
	projection, err := digestValue("operation-resource-identity-projection", struct {
		OperationTicket string   `json:"operation_ticket"`
		MetadataFields  []string `json:"metadata_fields"`
	}{operationTicket, []string{"operation_id"}})
	if err != nil {
		return domain.BrokerQualifiedResource{}, err
	}
	return domain.BrokerQualifiedResource{
		Kind: domain.BrokerResourceOperation, ImmutableID: operationTicket,
		AncestorIDs: []string{}, VersionEvidence: versionEvidence, ProjectionSHA256: projection,
	}, nil
}

func journalDigest(kind string, value any) (string, error) {
	return digestValueInNamespace(journalDigestNamespace, kind, value)
}
