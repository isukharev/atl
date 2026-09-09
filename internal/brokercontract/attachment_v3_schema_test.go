package brokercontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/isukharev/atl/internal/domain"
)

func TestAttachmentV3SchemaAndCodecNegativeBoundary(t *testing.T) {
	var schema, legacy jsonschema.Schema
	if json.Unmarshal(ExecutionSchemaV3(), &schema) != nil || json.Unmarshal(SchemaV1(), &legacy) != nil {
		t.Fatal("schema JSON is invalid")
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
		if strings.HasSuffix(uri.Path, "/broker-v1.schema.json") {
			return &legacy, nil
		}
		return nil, errors.New("unknown schema")
	}})
	if err != nil {
		t.Fatal(err)
	}
	schemaAccepts := func(body []byte) bool {
		var value any
		return json.Unmarshal(body, &value) == nil && resolved.Validate(value) == nil
	}
	fixture := newAttachmentV3Fixture(t)
	releaseQ := attachmentReleaseQualificationFixture(t, fixture, 10_000)
	releaseQD := attachmentQualificationDecisionFixture(t, releaseQ, 10_000)
	releaseO := attachmentReleaseOperationFixture(t, fixture, releaseQ, releaseQD)

	coordinate := bytes.Replace(mustAttachmentEncode(t, releaseQ, EncodeAttachmentQualificationRequestV3), []byte(`"offset":0`), []byte(`"offset":1`), 1)
	if _, err := DecodeAttachmentQualificationRequestV3(coordinate); !errors.Is(err, domain.ErrUsage) || schemaAccepts(coordinate) {
		t.Fatalf("index-zero/offset-one parity codec=%v schema_accepts=%t", err, schemaAccepts(coordinate))
	}

	manifestWithFilename := func(filename string) []byte {
		core := fixture.manifestCore
		core.Snapshot.Filename = filename
		core.Snapshot.IssueEvidenceSHA256, core.Snapshot.AttachmentEvidenceSHA256, core.Snapshot.ProjectionSHA256 = "", "", ""
		core.Snapshot.IssueEvidenceSHA256, core.Snapshot.AttachmentEvidenceSHA256 = attachmentEvidenceDigestsV3(core.Snapshot)
		core.Snapshot.ProjectionSHA256, _ = attachmentProjectionDigestV3(core.Snapshot, core.Snapshot.IssueEvidenceSHA256, core.Snapshot.AttachmentEvidenceSHA256)
		coreWire := attachmentManifestCoreToWireV3(core)
		coreDigest, _ := digestExecutionV3("manifest-core-v1", coreWire)
		wire := attachmentManifestLineToWireV3(domain.BrokerAttachmentManifestLineV3{Core: core, ManifestCoreSHA256: coreDigest, ReleaseDecisionSHA256: fixture.bodyOD.DecisionSHA256})
		return mustAttachmentCall(t, func() ([]byte, error) { return marshalAttachmentV3(wire, MaxAttachmentManifestLineBytesV3) })
	}
	slashFilename := manifestWithFilename("bad/name.bin")
	if _, err := DecodeAttachmentManifestLineV3(slashFilename); !errors.Is(err, domain.ErrUsage) || schemaAccepts(slashFilename) {
		t.Fatalf("slash filename parity codec=%v schema_accepts=%t", err, schemaAccepts(slashFilename))
	}

	multibyteFilename := manifestWithFilename(strings.Repeat("é", 128))
	if _, err := DecodeAttachmentManifestLineV3(multibyteFilename); !errors.Is(err, domain.ErrUsage) || !schemaAccepts(multibyteFilename) || !bytes.Contains(ExecutionSchemaV3(), []byte(`"x-atl-max-utf8-bytes": 255`)) {
		t.Fatalf("UTF-8 byte boundary codec=%v schema_accepts=%t", err, schemaAccepts(multibyteFilename))
	}

	operationWire, _ := attachmentOperationRequestToWireV3(fixture.initialO)
	var operationPayload attachmentQualifiedOperationWireV3
	if !decodeAttachmentV3(operationWire.Request, MaxAttachmentAuthorityEnvelopeBytesV3, &operationPayload) {
		t.Fatal("valid operation payload did not decode")
	}
	operationPayload.QualificationDecision = json.RawMessage("null")
	operationWire.Request, _ = marshalAttachmentPayloadV3(operationPayload)
	nestedNull := mustAttachmentCall(t, func() ([]byte, error) {
		return marshalAttachmentV3(operationWire, MaxAttachmentAuthorityEnvelopeBytesV3)
	})
	if _, err := DecodeAttachmentOperationAuthorizationRequestV3(nestedNull); !errors.Is(err, domain.ErrUsage) || schemaAccepts(nestedNull) {
		t.Fatalf("nested null parity codec=%v schema_accepts=%t", err, schemaAccepts(nestedNull))
	}

	invalidUTF8 := bytes.Clone(manifestWithFilename("example.bin"))
	filenameIndex := bytes.Index(invalidUTF8, []byte("example.bin"))
	if filenameIndex < 0 {
		t.Fatal("manifest filename fixture missing")
	}
	invalidUTF8[filenameIndex] = 0xff
	if _, err := DecodeAttachmentManifestLineV3(invalidUTF8); !errors.Is(err, domain.ErrUsage) || utf8.Valid(invalidUTF8) || !schemaAccepts(invalidUTF8) {
		t.Fatalf("invalid UTF-8 boundary codec=%v utf8_valid=%t schema_accepts=%t", err, utf8.Valid(invalidUTF8), schemaAccepts(invalidUTF8))
	}
	requestBody := mustAttachmentEncode(t, fixture.request, EncodeAttachmentRequestV3)
	overflow := append(bytes.Clone(requestBody), bytes.Repeat([]byte{' '}, int(MaxAttachmentRequestBytesV3)+1-len(requestBody))...)
	if _, err := DecodeAttachmentRequestV3(overflow); !errors.Is(err, domain.ErrUsage) || !schemaAccepts(overflow) {
		t.Fatalf("wire byte boundary codec=%v schema_accepts=%t bytes=%d", err, schemaAccepts(overflow), len(overflow))
	}

	releaseWire, _ := attachmentOperationRequestToWireV3(releaseO)
	var releasePayload attachmentReleaseOperationWireV3
	if !decodeAttachmentV3(releaseWire.Request, MaxAttachmentAuthorityEnvelopeBytesV3, &releasePayload) {
		t.Fatal("valid release payload did not decode")
	}
	var dataFacts attachmentDataReleaseWireV3
	if !decodeAttachmentV3(releasePayload.Facts.Facts, MaxAttachmentAuthorityEnvelopeBytesV3, &dataFacts) {
		t.Fatal("valid release facts did not decode")
	}
	dataFacts.Terminal = nil
	releasePayload.Facts.Facts, _ = marshalAttachmentPayloadV3(dataFacts)
	releaseWire.Request, _ = marshalAttachmentPayloadV3(releasePayload)
	shortWithoutTerminal := mustAttachmentCall(t, func() ([]byte, error) { return marshalAttachmentV3(releaseWire, MaxAttachmentAuthorityEnvelopeBytesV3) })
	if _, err := DecodeAttachmentOperationAuthorizationRequestV3(shortWithoutTerminal); !errors.Is(err, domain.ErrUsage) || schemaAccepts(shortWithoutTerminal) {
		t.Fatalf("short frame parity codec=%v schema_accepts=%t", err, schemaAccepts(shortWithoutTerminal))
	}
	lastQ := releaseQ
	lastQualification := *lastQ.Release
	lastQualification.Coordinate = domain.BrokerAttachmentReleaseCoordinateV3{Kind: domain.BrokerAttachmentReleaseData, Index: 15, Offset: 15 << 20}
	lastQ.Release = &lastQualification
	lastQD := attachmentQualificationDecisionFixture(t, lastQ, 10_000)
	lastO := attachmentReleaseOperationFixture(t, fixture, lastQ, lastQD)
	lastO.Release.Facts = domain.BrokerAttachmentReleaseFactsV3{Kind: domain.BrokerAttachmentReleaseData, Data: &domain.BrokerAttachmentDataReleaseV3{
		Index: 15, Offset: 15 << 20, DecodedBytes: 1 << 20, PayloadSHA256: digestTest('2'),
		CumulativeBytes: 16 << 20, CumulativeSHA256: digestTest('3'), ResourceSHA256: fixture.anchor.ResourcesSHA256,
		Terminal: &domain.BrokerAttachmentReleaseTerminalFactsV3{ChunkCount: 16, TotalBytes: 16 << 20, WholeSHA256: digestTest('3'), EOFProven: true, Complete: true},
	}}
	lastWire := mustAttachmentEncode(t, lastO, EncodeAttachmentOperationAuthorizationRequestV3)
	if _, err := DecodeAttachmentOperationAuthorizationRequestV3(lastWire); err != nil || !schemaAccepts(lastWire) {
		t.Fatalf("valid index-fifteen full-frame baseline codec=%v schema_accepts=%t", err, schemaAccepts(lastWire))
	}
	lastEnvelope, _ := attachmentOperationRequestToWireV3(lastO)
	var lastPayload attachmentReleaseOperationWireV3
	if !decodeAttachmentV3(lastEnvelope.Request, MaxAttachmentAuthorityEnvelopeBytesV3, &lastPayload) {
		t.Fatal("valid index-fifteen payload did not decode")
	}
	var lastFacts attachmentDataReleaseWireV3
	if !decodeAttachmentV3(lastPayload.Facts.Facts, MaxAttachmentAuthorityEnvelopeBytesV3, &lastFacts) {
		t.Fatal("valid index-fifteen facts did not decode")
	}
	lastFacts.Terminal = nil
	lastPayload.Facts.Facts, _ = marshalAttachmentPayloadV3(lastFacts)
	lastEnvelope.Request, _ = marshalAttachmentPayloadV3(lastPayload)
	lastWithoutTerminal := mustAttachmentCall(t, func() ([]byte, error) {
		return marshalAttachmentV3(lastEnvelope, MaxAttachmentAuthorityEnvelopeBytesV3)
	})
	if _, err := DecodeAttachmentOperationAuthorizationRequestV3(lastWithoutTerminal); !errors.Is(err, domain.ErrUsage) || schemaAccepts(lastWithoutTerminal) {
		t.Fatalf("index-fifteen terminal guard codec=%v schema_accepts=%t", err, schemaAccepts(lastWithoutTerminal))
	}

	zeroLine := domain.BrokerAttachmentTerminalLineV3{SchemaVersion: 3, FrameVersion: 1, Kind: domain.BrokerAttachmentReleaseTerminal, StreamID: "stream-1", ChunkCount: 0, TotalBytes: 0, DeclaredSize: 0, WholeSHA256: AttachmentEmptyBodySHA256V3, PriorReleaseSHA256: digestTest('a'), ReleaseDecisionSHA256: digestTest('b'), EOFProven: true, Complete: true}
	zeroWire := mustAttachmentEncode(t, zeroLine, EncodeAttachmentTerminalLineV3)
	if decoded, err := DecodeAttachmentTerminalLineV3(zeroWire); err != nil || decoded != zeroLine || !schemaAccepts(zeroWire) {
		t.Fatalf("valid zero terminal baseline codec=%v schema_accepts=%t", err, schemaAccepts(zeroWire))
	}
	wrongEmptyHash := bytes.Replace(zeroWire, []byte(AttachmentEmptyBodySHA256V3), []byte(digestTest('1')), 1)
	if _, err := DecodeAttachmentTerminalLineV3(wrongEmptyHash); !errors.Is(err, domain.ErrUsage) || schemaAccepts(wrongEmptyHash) {
		t.Fatalf("zero hash parity codec=%v schema_accepts=%t", err, schemaAccepts(wrongEmptyHash))
	}
	wrongChunkCount := bytes.Replace(zeroWire, []byte(`"chunk_count":0`), []byte(`"chunk_count":1`), 1)
	if _, err := DecodeAttachmentTerminalLineV3(wrongChunkCount); !errors.Is(err, domain.ErrUsage) || schemaAccepts(wrongChunkCount) {
		t.Fatalf("terminal count/total parity codec=%v schema_accepts=%t", err, schemaAccepts(wrongChunkCount))
	}
	nonzeroStandalone := domain.BrokerAttachmentReleaseFactsV3{Kind: domain.BrokerAttachmentReleaseTerminal, Terminal: releaseO.Release.Facts.Data.Terminal}
	nonzeroStandaloneFacts, _ := attachmentReleaseFactsToUncheckedWireV3(nonzeroStandalone)
	nonzeroStandaloneOperation, _ := attachmentOperationRequestToWireV3(releaseO)
	var nonzeroStandalonePayload attachmentReleaseOperationWireV3
	if !decodeAttachmentV3(nonzeroStandaloneOperation.Request, MaxAttachmentAuthorityEnvelopeBytesV3, &nonzeroStandalonePayload) || !decodeAttachmentV3(nonzeroStandaloneFacts, MaxAttachmentAuthorityEnvelopeBytesV3, &nonzeroStandalonePayload.Facts) {
		t.Fatal("nonzero standalone terminal wire did not decode structurally")
	}
	nonzeroStandaloneOperation.Request, _ = marshalAttachmentPayloadV3(nonzeroStandalonePayload)
	nonzeroStandaloneWire := mustAttachmentCall(t, func() ([]byte, error) {
		return marshalAttachmentV3(nonzeroStandaloneOperation, MaxAttachmentAuthorityEnvelopeBytesV3)
	})
	if _, err := AttachmentReleaseFactsSHA256V3(nonzeroStandalone); !errors.Is(err, domain.ErrUsage) || schemaAccepts(nonzeroStandaloneWire) {
		t.Fatalf("standalone terminal parity codec=%v schema_accepts=%t", err, schemaAccepts(nonzeroStandaloneWire))
	}

	wrongWhole := releaseO.Release.Facts
	wrongWholeData := *wrongWhole.Data
	wrongWholeTerminal := *wrongWholeData.Terminal
	wrongWholeTerminal.WholeSHA256 = digestTest('4')
	wrongWholeData.Terminal = &wrongWholeTerminal
	wrongWhole.Data = &wrongWholeData
	wrongWholeFacts, _ := attachmentReleaseFactsToUncheckedWireV3(wrongWhole)
	wrongWholeOperation, _ := attachmentOperationRequestToWireV3(releaseO)
	var wrongWholePayload attachmentReleaseOperationWireV3
	if !decodeAttachmentV3(wrongWholeOperation.Request, MaxAttachmentAuthorityEnvelopeBytesV3, &wrongWholePayload) || !decodeAttachmentV3(wrongWholeFacts, MaxAttachmentAuthorityEnvelopeBytesV3, &wrongWholePayload.Facts) {
		t.Fatal("valid release wire did not decode")
	}
	wrongWholeOperation.Request, _ = marshalAttachmentPayloadV3(wrongWholePayload)
	wrongWholeWire := mustAttachmentCall(t, func() ([]byte, error) {
		return marshalAttachmentV3(wrongWholeOperation, MaxAttachmentAuthorityEnvelopeBytesV3)
	})
	if _, err := AttachmentReleaseFactsSHA256V3(wrongWhole); !errors.Is(err, domain.ErrUsage) || !schemaAccepts(wrongWholeWire) {
		t.Fatalf("cross-field annotation boundary codec=%v schema_accepts=%t", err, schemaAccepts(wrongWholeWire))
	}
}

func attachmentReleaseFactsToUncheckedWireV3(value domain.BrokerAttachmentReleaseFactsV3) ([]byte, error) {
	var payload any
	if value.Kind == domain.BrokerAttachmentReleaseData {
		payload = attachmentDataReleaseToWireV3(*value.Data)
	} else {
		payload = attachmentTerminalFactsToWireV3(*value.Terminal)
	}
	facts, err := marshalAttachmentPayloadV3(payload)
	if err != nil {
		return nil, err
	}
	return marshalAttachmentV3(attachmentReleaseFactsEnvelopeWireV3{Kind: string(value.Kind), Facts: facts}, MaxAttachmentAuthorityEnvelopeBytesV3)
}
