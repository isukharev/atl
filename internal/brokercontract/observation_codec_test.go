package brokercontract

import (
	"bytes"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestDiscoveryKeepsClosedUniqueOperationsAndExplicitAvailability(t *testing.T) {
	available := AvailableDefinitions()
	operations := make([]domain.BrokerDiscoveryOperation, len(available))
	for index, definition := range available {
		operations[index] = domain.BrokerDiscoveryOperation{ID: definition.ID, Version: definition.Version, Availability: domain.BrokerAvailabilityAvailable, Features: wireStrings(definition.RequiredFeatures), Limits: definition.Limits}
	}
	value := domain.BrokerDiscoveryProjection{SchemaVersion: 1, ExecutionScopeSHA256: digestChar('a'), RegistrySHA256: RegistrySHA256(), IssuedAtMillis: 2000, ExpiresAtMillis: 7000, Operations: operations, Complete: true}
	wire, err := EncodeDiscoveryV1(value)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDiscoveryV1(wire)
	if err != nil || !reflect.DeepEqual(decoded, value) {
		t.Fatalf("decoded=%+v err=%v wire=%s", decoded, err, wire)
	}
	duplicate := value.Operations[0]
	value.Operations = append(value.Operations, duplicate)
	if _, err := EncodeDiscoveryV1(value); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("duplicate advertisement error=%v", err)
	}
	value.Operations = value.Operations[:len(value.Operations)-1]
	value.Operations[0].Availability = domain.BrokerAvailabilityUnsupported
	sort.Slice(value.Operations, func(i, j int) bool { return value.Operations[i].ID < value.Operations[j].ID })
	if _, err := EncodeDiscoveryV1(value); err != nil {
		t.Fatalf("explicit unsupported fact rejected: %v", err)
	}
	value.Operations[0].ID = "unknown.operation"
	if _, err := EncodeDiscoveryV1(value); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("unknown operation accepted: %v", err)
	}
}

func TestOperationTicketAndOutcomeTruthAreClosed(t *testing.T) {
	ticket := domain.BrokerOperationTicket{
		SchemaVersion: 1, OperationID: "operation-1", PrincipalSHA256: digestChar('a'), ExecutionSHA256: digestChar('b'),
		AudienceSHA256: digestChar('c'), BackendSHA256: digestChar('d'), Operation: domain.BrokerOperationJiraCommentApply,
		OperationVersion: 1, ArgumentsSHA256: digestChar('e'), ProposalSHA256: digestChar('f'), IssuedAtMillis: 2000, AcceptUntilMillis: 62000,
	}
	wire, err := EncodeOperationTicketV1(ticket)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeOperationTicketV1(wire)
	digest, digestErr := OperationTicketSHA256(decoded)
	if err != nil || digestErr != nil || !reflect.DeepEqual(decoded, ticket) || !validDigest(digest) {
		t.Fatalf("decoded=%+v err=%v digest=%q/%v", decoded, err, digest, digestErr)
	}
	changed := ticket
	changed.ArgumentsSHA256 = digestChar('0')
	changedDigest, _ := OperationTicketSHA256(changed)
	if digest == changedDigest {
		t.Fatal("changed operation arguments retained ticket identity")
	}

	valid := []domain.BrokerOperationOutcome{
		{SchemaVersion: 1, TicketSHA256: digest, Phase: domain.BrokerOperationAdmitted, ObservedAtMillis: 3000},
		{SchemaVersion: 1, TicketSHA256: digest, Phase: domain.BrokerOperationDispatching, ObservedAtMillis: 3000},
		{SchemaVersion: 1, TicketSHA256: digest, Phase: domain.BrokerOperationApplied, ObservedAtMillis: 3000, ResultSHA256: digestChar('1'), Complete: true, Reconciled: true},
		{SchemaVersion: 1, TicketSHA256: digest, Phase: domain.BrokerOperationNotApplied, ObservedAtMillis: 3000, Complete: true},
		{SchemaVersion: 1, TicketSHA256: digest, Phase: domain.BrokerOperationOutcomeUnknown, ObservedAtMillis: 3000},
		{SchemaVersion: 1, TicketSHA256: digest, Phase: domain.BrokerOperationRetiredNonReplayable, ObservedAtMillis: 3000, Complete: true},
	}
	for _, outcome := range valid {
		wire, err := EncodeOperationOutcomeV1(outcome)
		decoded, decodeErr := DecodeOperationOutcomeV1(wire)
		if err != nil || decodeErr != nil || !reflect.DeepEqual(decoded, outcome) {
			t.Fatalf("phase=%s decoded=%+v errors=%v/%v", outcome.Phase, decoded, err, decodeErr)
		}
	}
	invalid := valid[4]
	invalid.Complete = true
	if _, err := EncodeOperationOutcomeV1(invalid); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("invalid unknown error=%v", err)
	}
	missingFalse := []byte(`{"schema_version":1,"ticket_sha256":"` + digest + `","phase":"admitted","observed_at_millis":3000}`)
	if _, err := DecodeOperationOutcomeV1(missingFalse); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("missing required false fields error=%v", err)
	}
}

func TestCacheQualificationBindsScopeAndContent(t *testing.T) {
	request := domain.BrokerCacheQualificationRequest{
		SchemaVersion: 1, Context: fixtureContext(), SourcePrincipalSHA256: digestChar('a'), SourceReadScopeSHA256: digestChar('b'),
		Operation: domain.BrokerOperationJiraIssueRead, SelectorSHA256: digestChar('c'), ProjectionSHA256: digestChar('d'),
		EvidenceSchemaSHA256: digestChar('e'), GenerationSHA256: digestChar('f'), ContentSHA256: digestChar('1'), ExpiresAtMillis: 30000,
	}
	requestWire, encodeErr := EncodeCacheQualificationRequestV1(request)
	decodedRequest, decodeRequestErr := DecodeCacheQualificationRequestV1(requestWire)
	if encodeErr != nil || decodeRequestErr != nil || !reflect.DeepEqual(decodedRequest, request) {
		t.Fatalf("request=%+v errors=%v/%v", decodedRequest, encodeErr, decodeRequestErr)
	}
	digest, err := CacheQualificationRequestSHA256(request)
	if err != nil || !validDigest(digest) {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	changedScope, changedContent := request, request
	changedScope.SourceReadScopeSHA256 = digestChar('2')
	changedContent.ContentSHA256 = digestChar('3')
	scopeDigest, _ := CacheQualificationRequestSHA256(changedScope)
	contentDigest, _ := CacheQualificationRequestSHA256(changedContent)
	if digest == scopeDigest || digest == contentDigest || scopeDigest == contentDigest {
		t.Fatal("scope/content changes did not alter cache qualification identity")
	}
	qualification := domain.BrokerCacheQualification{
		SchemaVersion: 1, Status: domain.BrokerDecisionAllowed, IssuerSHA256: digestChar('a'), TargetExecutionSHA256: digestChar('b'),
		AuthorityRevisionSHA256: digestChar('c'), RequestSHA256: digest, IssuedAtMillis: 2000, ExpiresAtMillis: 7000,
	}
	wire, err := EncodeCacheQualificationV1(qualification)
	decoded, decodeErr := DecodeCacheQualificationV1(wire)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decoded, qualification) {
		t.Fatalf("decoded=%+v errors=%v/%v", decoded, err, decodeErr)
	}
	if _, err := DecodeCacheQualificationV1(bytes.Replace(wire, []byte(`"status":"allowed"`), []byte(`"status":"allowed","policy":"private"`), 1)); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("unknown policy member error=%v", err)
	}
}

func TestExecutionProjectionIsCurrentCallerScoped(t *testing.T) {
	value := domain.BrokerExecutionProjection{SchemaVersion: 1, ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", Audience: "atl-broker", AuthorityRevision: "revision-1", ExpiresAtMillis: 7000, ScopeSHA256: digestChar('a')}
	wire, err := EncodeExecutionProjectionV1(value)
	decoded, decodeErr := DecodeExecutionProjectionV1(wire)
	if err != nil || decodeErr != nil || decoded != value {
		t.Fatalf("projection=%+v errors=%v/%v", decoded, err, decodeErr)
	}
	if _, err := DecodeExecutionProjectionV1(bytes.Replace(wire, []byte(`"execution_id":"execution-1"`), []byte(`"execution_id":"execution-1","principal_id":"private"`), 1)); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("expanded projection error=%v", err)
	}
}

func FuzzDecodeOperationOutcomeV1(f *testing.F) {
	seed, _ := EncodeOperationOutcomeV1(domain.BrokerOperationOutcome{SchemaVersion: 1, TicketSHA256: digestChar('a'), Phase: domain.BrokerOperationOutcomeUnknown, ObservedAtMillis: 2000})
	f.Add(seed)
	f.Add([]byte(`{"schema_version":1,"phase":"applied"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		value, err := DecodeOperationOutcomeV1(data)
		if err == nil {
			if encoded, encodeErr := EncodeOperationOutcomeV1(value); encodeErr != nil || len(encoded) == 0 {
				t.Fatalf("accepted outcome did not re-encode: %v", encodeErr)
			}
		}
	})
}
