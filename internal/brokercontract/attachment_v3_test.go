package brokercontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/isukharev/atl/internal/domain"
)

const attachmentTestDeadline = int64(62_000)

type attachmentV3Fixture struct {
	request           domain.BrokerAttachmentRequestV3
	admissionRequest  domain.BrokerAttachmentAdmissionRequestV3
	admissionDecision domain.BrokerAttachmentAdmissionDecisionV3
	initialQ          domain.BrokerAttachmentQualificationRequestV3
	initialQD         domain.BrokerAttachmentQualificationDecisionV3
	initialO          domain.BrokerAttachmentOperationAuthorizationRequestV3
	initialOD         domain.BrokerAttachmentOperationDecisionV3
	preOpenQ          domain.BrokerAttachmentQualificationRequestV3
	preOpenQD         domain.BrokerAttachmentQualificationDecisionV3
	bodyO             domain.BrokerAttachmentOperationAuthorizationRequestV3
	bodyOD            domain.BrokerAttachmentOperationDecisionV3
	anchor            domain.BrokerAttachmentStreamAnchorV3
	manifestCore      domain.BrokerAttachmentManifestCoreV3
}

type attachmentTestTB interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

func TestAttachmentV3RegistryIsUnavailableBoundedAndSeparate(t *testing.T) {
	definitions := RegistryV3()
	if RegistrySHA256V3() != "437d6b5a3fe5b10a7f771b1cc2d8d15930cba7c902b1e19264c9e28fd5d21cd7" ||
		ExecutionSchemaSHA256V3() != "5da402d39bb41fe7c9dece70f01413f5ac378b67208715f6f7efdf30ee04c626" ||
		DiscoverySchemaSHA256V4() != "12a706d61423bcd74898d80ef87529c855f0ea14e387addb7f3f108333c1caf9" {
		t.Fatalf("execution-v3 contract bytes changed: %s/%s/%s", RegistrySHA256V3(), ExecutionSchemaSHA256V3(), DiscoverySchemaSHA256V4())
	}
	if len(definitions) != 1 || len(AvailableDefinitionsV3()) != 0 || !validDigest(RegistrySHA256V3()) || !validDigest(ExecutionSchemaSHA256V3()) {
		t.Fatalf("definitions=%+v available=%d", definitions, len(AvailableDefinitionsV3()))
	}
	definition := definitions[0]
	if definition.Definition.Available || !definition.Definition.Streaming || definition.Definition.ID != domain.BrokerOperationJiraAttachmentDownload || definition.MaxMetadataItems != 10_000 ||
		definition.Definition.Limits.MaxResources != 2 || definition.Definition.Limits.MaxFields != 0 || definition.Definition.Limits.MaxStreamChunks != 16 ||
		definition.MaxJiraAttempts != 19 || definition.MaxAuthenticationAttempts != 17 || definition.MaxDecisionAttempts != 37 || definition.MaxTotalHostOutboundAttempts != 73 ||
		definition.MaxCommandHostOutboundAttempts != 76 ||
		definition.MaxJiraAttempts+definition.MaxAuthenticationAttempts+definition.MaxDecisionAttempts != definition.MaxTotalHostOutboundAttempts ||
		definition.MaxJiraResponseBytes != 34<<20 || definition.MaxAuthorityResponseBytes != 27<<18 || definition.MaxTotalHostResponseBytes != 163<<18 ||
		definition.MaxJiraResponseBytes+definition.MaxAuthorityResponseBytes != definition.MaxTotalHostResponseBytes || definition.MaxFramedResponseBytes != 24<<20 || MaxAttachmentContentURIBytesV3 != 64<<10 {
		t.Fatalf("invalid attachment definition: %+v", definition)
	}
	if RegistrySHA256() != "a712329120114874b6d1c2f62884ca28a45cdef8616fd78073bc3a6a72fae72c" ||
		SchemaSHA256() != "fdf82ad96e59a6c32f639f4602da15632dfcb72ab0fb734df00782bf29967372" ||
		RegistrySHA256V2() != "a48e8594281ddd5ae5d2fb25ef096a677a9ba488320a9d3661504bf307846c02" {
		t.Fatalf("frozen prior contract changed: %s/%s/%s", RegistrySHA256(), SchemaSHA256(), RegistrySHA256V2())
	}
}

func TestAttachmentV3CurrentSetupAndHistoricalReleaseRoundTrip(t *testing.T) {
	fixture := newAttachmentV3Fixture(t)
	assertRoundTrip(t, fixture.request, EncodeAttachmentRequestV3, DecodeAttachmentRequestV3)
	assertRoundTrip(t, fixture.admissionRequest, EncodeAttachmentAdmissionRequestV3, DecodeAttachmentAdmissionRequestV3)
	assertRoundTrip(t, fixture.admissionDecision, EncodeAttachmentAdmissionDecisionV3, DecodeAttachmentAdmissionDecisionV3)
	assertRoundTrip(t, fixture.initialQ, EncodeAttachmentQualificationRequestV3, DecodeAttachmentQualificationRequestV3)
	assertRoundTrip(t, fixture.initialQD, EncodeAttachmentQualificationDecisionV3, DecodeAttachmentQualificationDecisionV3)
	assertRoundTrip(t, fixture.initialO, EncodeAttachmentOperationAuthorizationRequestV3, DecodeAttachmentOperationAuthorizationRequestV3)
	assertRoundTrip(t, fixture.initialOD, EncodeAttachmentOperationDecisionV3, DecodeAttachmentOperationDecisionV3)
	assertRoundTrip(t, fixture.preOpenQ, EncodeAttachmentQualificationRequestV3, DecodeAttachmentQualificationRequestV3)
	assertRoundTrip(t, fixture.preOpenQD, EncodeAttachmentQualificationDecisionV3, DecodeAttachmentQualificationDecisionV3)
	assertRoundTrip(t, fixture.bodyO, EncodeAttachmentOperationAuthorizationRequestV3, DecodeAttachmentOperationAuthorizationRequestV3)
	assertRoundTrip(t, fixture.bodyOD, EncodeAttachmentOperationDecisionV3, DecodeAttachmentOperationDecisionV3)
	if !validAttachmentAnchorV3(fixture.anchor) {
		t.Fatalf("fixture anchor is invalid: %+v", fixture.anchor)
	}
	assertRoundTrip(t, fixture.anchor, EncodeAttachmentStreamAnchorV3, DecodeAttachmentStreamAnchorV3)

	if err := ValidateAttachmentOperationDecisionV3(fixture.initialOD, fixture.initialO, 7_050, attachmentTestDeadline); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("expired nested admission remained current: %v", err)
	}
	if err := ValidateAttachmentOperationDecisionV3(fixture.bodyOD, fixture.bodyO, 7_350, attachmentTestDeadline); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("expired nested pre-open decision remained current: %v", err)
	}
	if ValidateAttachmentOperationDecisionV3(fixture.initialOD, fixture.initialO, 8_000, attachmentTestDeadline) == nil {
		t.Fatal("expired setup operation remained current")
	}
	if err := MatchAttachmentReleaseContextV3(fixture.anchor, fixture.anchor.Context, 10_000, 15_000); err != nil {
		t.Fatalf("fresh authentication was coupled to expired setup decisions: %v", err)
	}
	changedContext := fixture.anchor.Context
	changedContext.PrincipalID = "other-principal"
	if err := MatchAttachmentReleaseContextV3(fixture.anchor, changedContext, 10_000, 15_000); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("changed release identity err=%v", err)
	}
	releaseQ := attachmentReleaseQualificationFixture(t, fixture, 10_000)
	if err := ValidateAttachmentReleaseQualificationV3(releaseQ, fixture.anchor, releaseQ.Release.PriorReleaseSHA256, 10_000, 15_000); err != nil {
		t.Fatalf("fresh release qualification binding: %v", err)
	}
	detachedRelease := releaseQ
	detachedPayload := *detachedRelease.Release
	detachedPayload.Plan.SelectorSHA256 = digestTest('f')
	detachedRelease.Release = &detachedPayload
	if err := ValidateAttachmentReleaseQualificationV3(detachedRelease, fixture.anchor, releaseQ.Release.PriorReleaseSHA256, 10_000, 15_000); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("detached release selector err=%v", err)
	}
	releaseQD := attachmentQualificationDecisionFixture(t, releaseQ, 10_000)
	releaseO := attachmentReleaseOperationFixture(t, fixture, releaseQ, releaseQD)
	releaseOD := attachmentOperationDecisionFixture(t, releaseO, 10_000)
	if err := ValidateAttachmentOperationDecisionV3(releaseOD, releaseO, 10_000, attachmentTestDeadline); err != nil {
		t.Fatalf("fresh release rejected because historical setup expired: %v", err)
	}
	assertRoundTrip(t, releaseQ, EncodeAttachmentQualificationRequestV3, DecodeAttachmentQualificationRequestV3)
	assertRoundTrip(t, releaseO, EncodeAttachmentOperationAuthorizationRequestV3, DecodeAttachmentOperationAuthorizationRequestV3)

	for name, encode := range map[string]func() ([]byte, error){
		"initial as release qualification": func() ([]byte, error) {
			changed := releaseQ
			changed.Phase = domain.BrokerAttachmentQualificationInitial
			return EncodeAttachmentQualificationRequestV3(changed)
		},
		"pre-open in initial operation": func() ([]byte, error) {
			changed := fixture.initialO
			qualified := *changed.Qualified
			qualified.QualificationRequest = fixture.preOpenQ
			changed.Qualified = &qualified
			return EncodeAttachmentOperationAuthorizationRequestV3(changed)
		},
		"release payload in body dispatch": func() ([]byte, error) {
			changed := releaseO
			changed.Phase = domain.BrokerAttachmentOperationBodyDispatch
			return EncodeAttachmentOperationAuthorizationRequestV3(changed)
		},
		"selector detached from arguments": func() ([]byte, error) {
			changed := fixture.initialQ
			initial := *changed.Initial
			initial.Plan.SelectorSHA256 = digestTest('f')
			changed.Initial = &initial
			return EncodeAttachmentQualificationRequestV3(changed)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := encode(); !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("cross-phase value err=%v", err)
			}
		})
	}

	for name, body := range map[string][]byte{
		"null arguments": bytes.Replace(mustAttachmentEncode(t, fixture.request, EncodeAttachmentRequestV3), []byte(`"arguments":{"issue_key":"PROJ-1","attachment_id":"200"}`), []byte(`"arguments":null`), 1),
		"duplicate":      bytes.Replace(mustAttachmentEncode(t, fixture.request, EncodeAttachmentRequestV3), []byte(`"schema_version":3`), []byte(`"schema_version":3,"schema_version":3`), 1),
		"unknown":        bytes.Replace(mustAttachmentEncode(t, releaseQ, EncodeAttachmentQualificationRequestV3), []byte(`"anchor_sha256"`), []byte(`"private_url":"https://example.invalid","anchor_sha256"`), 1),
		"unknown phase":  bytes.Replace(mustAttachmentEncode(t, releaseQ, EncodeAttachmentQualificationRequestV3), []byte(`"phase":"release"`), []byte(`"phase":"future"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			if strings.Contains(name, "arguments") || name == "duplicate" {
				_, err = DecodeAttachmentRequestV3(body)
			} else {
				_, err = DecodeAttachmentQualificationRequestV3(body)
			}
			if !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("err=%v body=%s", err, body)
			}
		})
	}
	recursiveQualification := bytes.Replace(mustAttachmentEncode(t, fixture.preOpenQ, EncodeAttachmentQualificationRequestV3), []byte(`"phase":"initial"`), []byte(`"phase":"body_dispatch"`), 1)
	if _, err := DecodeAttachmentQualificationRequestV3(recursiveQualification); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("recursive pre-open phase chain err=%v", err)
	}
	recursiveOperation := bytes.Replace(mustAttachmentEncode(t, fixture.bodyO, EncodeAttachmentOperationAuthorizationRequestV3), []byte(`"phase":"initial"`), []byte(`"phase":"body_dispatch"`), 1)
	if _, err := DecodeAttachmentOperationAuthorizationRequestV3(recursiveOperation); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("recursive body-dispatch phase chain err=%v", err)
	}

	for _, value := range []domain.BrokerAttachmentOperationAuthorizationRequestV3{fixture.initialO, fixture.bodyO, releaseO} {
		body := mustAttachmentEncode(t, value, EncodeAttachmentOperationAuthorizationRequestV3)
		if len(body) > 64<<10 {
			t.Fatalf("setup envelope=%d", len(body))
		}
	}
}

func TestAttachmentV3AcyclicLinesAndClientVisibleReceipt(t *testing.T) {
	fixture := newAttachmentV3Fixture(t)
	coreDigest, _ := AttachmentManifestCoreSHA256V3(fixture.manifestCore)
	manifest := domain.BrokerAttachmentManifestLineV3{Core: fixture.manifestCore, ManifestCoreSHA256: coreDigest, ReleaseDecisionSHA256: fixture.bodyOD.DecisionSHA256}
	manifestBody := mustAttachmentEncode(t, manifest, EncodeAttachmentManifestLineV3)
	if decoded, err := DecodeAttachmentManifestLineV3(manifestBody); err != nil || !reflect.DeepEqual(decoded, manifest) || bytes.Contains(manifestBody, []byte("release_receipt")) {
		t.Fatalf("manifest roundtrip err=%v body=%s", err, manifestBody)
	}

	payload := []byte("native attachment bytes")
	payloadHash := sha256.Sum256(payload)
	data := domain.BrokerAttachmentDataLineV3{SchemaVersion: 3, FrameVersion: 1, Kind: domain.BrokerAttachmentReleaseData, StreamID: "stream-1", Index: 0, Offset: 0, DecodedBytes: int64(len(payload)), PayloadBase64: base64.StdEncoding.EncodeToString(payload), PayloadSHA256: hex.EncodeToString(payloadHash[:]), CumulativeBytes: int64(len(payload)), CumulativeSHA256: hex.EncodeToString(payloadHash[:]), ResourceSHA256: fixture.anchor.ResourcesSHA256, PriorReleaseSHA256: digestTest('8'), ReleaseDecisionSHA256: digestTest('9')}
	dataBody := mustAttachmentEncode(t, data, EncodeAttachmentDataLineV3)
	if decoded, err := DecodeAttachmentDataLineV3(dataBody); err != nil || !reflect.DeepEqual(decoded, data) {
		t.Fatalf("data roundtrip err=%v", err)
	}
	if _, err := DecodeAttachmentDataLineV3(append(bytes.Clone(dataBody), ' ')); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("non-canonical data line err=%v", err)
	}
	terminal := domain.BrokerAttachmentTerminalLineV3{SchemaVersion: 3, FrameVersion: 1, Kind: domain.BrokerAttachmentReleaseTerminal, StreamID: "stream-1", ChunkCount: 1, TotalBytes: int64(len(payload)), DeclaredSize: int64(len(payload)), WholeSHA256: hex.EncodeToString(payloadHash[:]), PriorReleaseSHA256: digestTest('a'), ReleaseDecisionSHA256: digestTest('b'), EOFProven: true, Complete: true}
	terminalBody := mustAttachmentEncode(t, terminal, EncodeAttachmentTerminalLineV3)
	if decoded, err := DecodeAttachmentTerminalLineV3(terminalBody); err != nil || !reflect.DeepEqual(decoded, terminal) {
		t.Fatalf("terminal roundtrip err=%v", err)
	}

	manifestLine, _ := AttachmentExactLineSHA256V3(manifestBody)
	dataLine, _ := AttachmentExactLineSHA256V3(dataBody)
	receipt, err := AttachmentReleaseReceiptSHA256V3(digestTest('c'), digestTest('d'), []string{manifestLine, dataLine})
	reordered, _ := AttachmentReleaseReceiptSHA256V3(digestTest('c'), digestTest('d'), []string{dataLine, manifestLine})
	if err != nil || !validDigest(receipt) || receipt == reordered {
		t.Fatalf("receipt=%s reordered=%s err=%v", receipt, reordered, err)
	}
	anchorDigest, _ := AttachmentStreamAnchorSHA256V3(fixture.anchor)
	if anchorDigest == coreDigest || bytes.Contains(mustAttachmentEncode(t, fixture.anchor, EncodeAttachmentStreamAnchorV3), []byte("manifest_core")) {
		t.Fatal("anchor and manifest core are cyclic or share a digest domain")
	}
	htmlCore := fixture.manifestCore
	htmlSnapshot := htmlCore.Snapshot
	htmlSnapshot.IssueEvidenceSHA256, htmlSnapshot.AttachmentEvidenceSHA256, htmlSnapshot.ProjectionSHA256 = "", "", ""
	htmlSnapshot.Filename = "a<b>.bin"
	htmlSnapshot, err = NewAttachmentSnapshotEvidenceV3(htmlSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	htmlCore.Snapshot = htmlSnapshot
	htmlDigest, _ := AttachmentManifestCoreSHA256V3(htmlCore)
	htmlLine := mustAttachmentEncode(t, domain.BrokerAttachmentManifestLineV3{Core: htmlCore, ManifestCoreSHA256: htmlDigest, ReleaseDecisionSHA256: fixture.bodyOD.DecisionSHA256}, EncodeAttachmentManifestLineV3)
	if !bytes.Contains(htmlLine, []byte("a<b>.bin")) || bytes.Contains(htmlLine, []byte(`\u003c`)) {
		t.Fatalf("stream line unexpectedly HTML-escaped: %s", htmlLine)
	}
}

func TestAttachmentV3PhysicalAndFramedBoundsAreExact(t *testing.T) {
	framedMaximum := MaxAttachmentManifestLineBytesV3 + 1 + MaxAttachmentDataFramesV3*(MaxAttachmentDataLineBytesV3+1) + MaxAttachmentTerminalLineBytesV3 + 1
	if framedMaximum != 23_150_610 || framedMaximum > MaxAttachmentFramedResponseBytesV3 {
		t.Fatalf("framed maximum=%d contract=%d", framedMaximum, MaxAttachmentFramedResponseBytesV3)
	}
	if MaxAttachmentAuthenticationAttemptsV3+MaxAttachmentDecisionAttemptsV3 != 54 ||
		MaxAttachmentCommandHostOutboundAttemptsV3 != MaxAttachmentHostOutboundAttemptsV3+3 ||
		int64(MaxAttachmentAuthenticationAttemptsV3+MaxAttachmentDecisionAttemptsV3)*MaxAttachmentAuthorityCallBytesV3 != MaxAttachmentAuthorityResponseBytesV3 ||
		18*MaxAttachmentMetadataResponseBytesV3+MaxAttachmentNativeBodyBytesV3 != MaxAttachmentJiraResponseBytesV3 ||
		MaxAttachmentAuthorityResponseBytesV3+MaxAttachmentJiraResponseBytesV3 != MaxAttachmentHostResponseBytesV3 {
		t.Fatal("categorical response budgets no longer prove their aggregate")
	}
	payload := bytes.Repeat([]byte{'x'}, int(MaxAttachmentDecodedFrameBytesV3))
	payloadDigest := sha256.Sum256(payload)
	line := domain.BrokerAttachmentDataLineV3{SchemaVersion: 3, FrameVersion: 1, Kind: domain.BrokerAttachmentReleaseData, StreamID: "stream-1", Index: 15, Offset: 15 << 20, DecodedBytes: int64(len(payload)), PayloadBase64: base64.StdEncoding.EncodeToString(payload), PayloadSHA256: hex.EncodeToString(payloadDigest[:]), CumulativeBytes: 16 << 20, CumulativeSHA256: digestTest('a'), ResourceSHA256: digestTest('b'), PriorReleaseSHA256: digestTest('c'), ReleaseDecisionSHA256: digestTest('d')}
	body := mustAttachmentEncode(t, line, EncodeAttachmentDataLineV3)
	if int64(len(body)) > MaxAttachmentDataLineBytesV3 {
		t.Fatalf("maximum data line=%d", len(body))
	}
	line.DecodedBytes++
	if _, err := EncodeAttachmentDataLineV3(line); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("oversized decoded frame err=%v", err)
	}
}

func TestAttachmentV3CanonicalStringEncodingIsValidJSON(t *testing.T) {
	for name, value := range map[string]string{"DEL": "\x7f", "C1": "\u0085", "unicode": "Пример—東京", "HTML": "<>&", "line separator": "\u2028", "paragraph separator": "\u2029", "quoted controls": "\t\n\r\\\""} {
		t.Run(name, func(t *testing.T) {
			var canonical bytes.Buffer
			if err := writeCanonicalExecutionV3(&canonical, value, 0); err != nil {
				t.Fatal(err)
			}
			var decoded string
			if err := json.Unmarshal(canonical.Bytes(), &decoded); err != nil || decoded != value {
				t.Fatalf("canonical=%q decoded=%q err=%v", canonical.Bytes(), decoded, err)
			}
			if bytes.Contains(canonical.Bytes(), []byte(`\x`)) || name == "HTML" && !bytes.Contains(canonical.Bytes(), []byte("<>&")) {
				t.Fatalf("non-JSON or HTML-escaped canonical string: %q", canonical.Bytes())
			}
			if digest, err := digestExecutionV3("string-regression", struct {
				Value string `json:"value"`
			}{value}); err != nil || !validDigest(digest) {
				t.Fatalf("digest=%s err=%v", digest, err)
			}
		})
	}
}

func TestAttachmentV3TerminalFactsAreCompleteAndConsistent(t *testing.T) {
	fixture := newAttachmentV3Fixture(t)
	releaseQ := attachmentReleaseQualificationFixture(t, fixture, 10_000)
	releaseQD := attachmentQualificationDecisionFixture(t, releaseQ, 10_000)
	release := attachmentReleaseOperationFixture(t, fixture, releaseQ, releaseQD).Release.Facts
	data := release.Data
	terminal := domain.BrokerAttachmentTerminalLineV3{
		SchemaVersion: 3, FrameVersion: 1, Kind: domain.BrokerAttachmentReleaseTerminal, StreamID: fixture.anchor.StreamID,
		ChunkCount: data.Terminal.ChunkCount, TotalBytes: data.Terminal.TotalBytes, DeclaredSize: data.Terminal.TotalBytes,
		WholeSHA256: data.Terminal.WholeSHA256, PriorReleaseSHA256: releaseQ.Release.PriorReleaseSHA256,
		ReleaseDecisionSHA256: digestTest('d'), EOFProven: true, Complete: true,
	}
	if err := ValidateAttachmentTerminalLineForReleaseV3(terminal, release, terminal.DeclaredSize, terminal.StreamID, terminal.PriorReleaseSHA256, terminal.ReleaseDecisionSHA256); err != nil {
		t.Fatalf("terminal release binding: %v", err)
	}

	empty := domain.BrokerAttachmentReleaseFactsV3{Kind: domain.BrokerAttachmentReleaseTerminal, Terminal: &domain.BrokerAttachmentReleaseTerminalFactsV3{ChunkCount: 0, TotalBytes: 0, WholeSHA256: AttachmentEmptyBodySHA256V3, EOFProven: true, Complete: true}}
	if _, err := AttachmentReleaseFactsSHA256V3(empty); err != nil {
		t.Fatalf("zero-body terminal: %v", err)
	}
	badEmpty := empty
	badEmptyTerminal := *empty.Terminal
	badEmptyTerminal.WholeSHA256 = digestTest('1')
	badEmpty.Terminal = &badEmptyTerminal

	shortWithoutTerminal := release
	shortData := *release.Data
	shortData.Terminal = nil
	shortWithoutTerminal.Data = &shortData
	lastWithoutTerminal := release
	lastData := *release.Data
	lastData.Index, lastData.Offset, lastData.DecodedBytes, lastData.CumulativeBytes, lastData.Terminal = 15, 15<<20, 1<<20, 16<<20, nil
	lastWithoutTerminal.Data = &lastData
	wrongWhole := release
	wrongWholeData := *release.Data
	wrongWholeTerminal := *release.Data.Terminal
	wrongWholeTerminal.WholeSHA256 = digestTest('4')
	wrongWholeData.Terminal = &wrongWholeTerminal
	wrongWhole.Data = &wrongWholeData
	wrongCount := release
	wrongCountData := *release.Data
	wrongCountTerminal := *release.Data.Terminal
	wrongCountTerminal.ChunkCount = 2
	wrongCountData.Terminal = &wrongCountTerminal
	wrongCount.Data = &wrongCountData

	for name, value := range map[string]domain.BrokerAttachmentReleaseFactsV3{
		"zero hash": badEmpty, "short without terminal": shortWithoutTerminal, "index fifteen without terminal": lastWithoutTerminal,
		"whole versus cumulative": wrongWhole, "chunk count arithmetic": wrongCount,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := AttachmentReleaseFactsSHA256V3(value); !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("err=%v value=%+v", err, value)
			}
		})
	}

	zeroLine := domain.BrokerAttachmentTerminalLineV3{SchemaVersion: 3, FrameVersion: 1, Kind: domain.BrokerAttachmentReleaseTerminal, StreamID: "stream-1", ChunkCount: 0, TotalBytes: 0, DeclaredSize: 0, WholeSHA256: AttachmentEmptyBodySHA256V3, PriorReleaseSHA256: digestTest('a'), ReleaseDecisionSHA256: digestTest('b'), EOFProven: true, Complete: true}
	if _, err := EncodeAttachmentTerminalLineV3(zeroLine); err != nil {
		t.Fatal(err)
	}
	zeroLine.WholeSHA256 = digestTest('1')
	if _, err := EncodeAttachmentTerminalLineV3(zeroLine); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("zero terminal line hash err=%v", err)
	}
	terminal.TotalBytes++
	terminal.DeclaredSize++
	if err := ValidateAttachmentTerminalLineForReleaseV3(terminal, release, terminal.DeclaredSize, terminal.StreamID, terminal.PriorReleaseSHA256, terminal.ReleaseDecisionSHA256); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("terminal line detached from release facts err=%v", err)
	}
}

func TestAttachmentV3WorstCaseSetupEnvelopeFitsSixtyFourKiB(t *testing.T) {
	const highMillis = int64(9_000_000_000_000_000_000)
	escapedIdentifier := strings.Repeat(`\`, domain.BrokerMaxIdentifierBytes)
	context := attachmentContextFixture()
	context.PrincipalID = escapedIdentifier
	context.WorkloadID = escapedIdentifier
	context.ExecutionID = escapedIdentifier
	context.ExecutionEpoch = escapedIdentifier
	context.Audience = escapedIdentifier
	context.BrokerID = escapedIdentifier
	context.AuthorityRevision = escapedIdentifier
	context.Backend.WorkloadBackendID = escapedIdentifier
	context.ExecutionNotBeforeMillis = highMillis
	context.ExecutionExpiresMillis = highMillis + 63_000
	context.GrantExpiresMillis = highMillis + 63_000
	context.CredentialExpiresMillis = highMillis + 63_000
	project := strings.Repeat("P", 32)
	arguments := domain.BrokerAttachmentArgumentsV3{IssueKey: project + "-" + strings.Repeat("9", 31), AttachmentID: strings.Repeat("9", 64)}
	argumentsDigest, _ := AttachmentArgumentsSHA256V3(arguments)
	admission := domain.BrokerAttachmentAdmissionRequestV3{Context: context, Operation: domain.BrokerOperationJiraAttachmentDownload, OperationVersion: 1, RequestID: escapedIdentifier, Features: append([]string{}, attachmentFeaturesV3...), Arguments: arguments, ArgumentsSHA256: argumentsDigest, DeadlineMillis: highMillis + 62_000}
	admissionDecision := attachmentAdmissionDecisionFixture(t, admission, highMillis+1_000)
	admissionDecision.DecisionID, admissionDecision.DecisionSHA256 = escapedIdentifier, ""
	admissionDecision = mustAttachmentDecode(t, admissionDecision, EncodeAttachmentAdmissionDecisionV3, DecodeAttachmentAdmissionDecisionV3)
	plan := domain.BrokerAttachmentMetadataPlanV3{SelectorSHA256: argumentsDigest, MetadataFields: append([]string{}, attachmentMetadataFieldsV3...), Limits: domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: 1 << 20}}
	initialQ := domain.BrokerAttachmentQualificationRequestV3{Phase: domain.BrokerAttachmentQualificationInitial, Initial: &domain.BrokerAttachmentInitialQualificationV3{Admission: admission, AdmissionDecision: admissionDecision, Plan: plan}}
	initialQD := attachmentQualificationDecisionFixture(t, initialQ, highMillis+2_100)
	initialQD.DecisionID, initialQD.DecisionSHA256 = escapedIdentifier, ""
	initialQD = mustAttachmentDecode(t, initialQD, EncodeAttachmentQualificationDecisionV3, DecodeAttachmentQualificationDecisionV3)
	effects := []domain.BrokerAttachmentEffectV3{{Kind: domain.BrokerEffectRead, ResourceKind: domain.BrokerResourceJiraIssue, Fields: []string{"id", "key", "project", "updated"}}, {Kind: domain.BrokerEffectRead, ResourceKind: domain.BrokerResourceJiraAttachment, Fields: []string{"body", "created", "filename", "id", "media_type", "parent_id", "size"}}}
	for name, fill := range map[string]string{"html": "<", "quote": `"`, "backslash": `\`, "tab": "\t", "newline": "\n", "carriage return": "\r", "line separator": "\u2028", "paragraph separator": "\u2029"} {
		t.Run(name, func(t *testing.T) {
			text := repeatToUTF8Bytes(fill, 4<<10)
			if len(text) != 4<<10 {
				t.Fatalf("fixture bytes=%d", len(text))
			}
			snapshot, err := NewAttachmentSnapshotEvidenceV3(domain.BrokerJiraAttachmentSnapshotV3{IssueID: strings.Repeat("8", 64), IssueKey: arguments.IssueKey, Project: project, Updated: text, AttachmentID: arguments.AttachmentID, ParentID: strings.Repeat("8", 64), Filename: strings.Repeat(`"`, 255), MediaType: strings.Repeat(`\`, 255), Created: text, DeclaredSize: 16 << 20})
			if err != nil {
				t.Fatal(err)
			}
			initialO := domain.BrokerAttachmentOperationAuthorizationRequestV3{Phase: domain.BrokerAttachmentOperationInitial, Qualified: &domain.BrokerAttachmentQualifiedOperationV3{QualificationRequest: initialQ, QualificationDecision: initialQD, Snapshot: snapshot, Effects: effects}}
			initialOD := attachmentOperationDecisionFixture(t, initialO, highMillis+3_200)
			initialOD.DecisionID, initialOD.DecisionSHA256 = escapedIdentifier, ""
			initialOD = mustAttachmentDecode(t, initialOD, EncodeAttachmentOperationDecisionV3, DecodeAttachmentOperationDecisionV3)
			preOpenQ := domain.BrokerAttachmentQualificationRequestV3{Phase: domain.BrokerAttachmentQualificationPreOpen, PreOpen: &domain.BrokerAttachmentPreOpenQualificationV3{InitialOperation: initialO, InitialOperationDecision: initialOD, Plan: plan}}
			preOpenQD := attachmentQualificationDecisionFixture(t, preOpenQ, highMillis+4_300)
			preOpenQD.DecisionID, preOpenQD.DecisionSHA256 = escapedIdentifier, ""
			preOpenQD = mustAttachmentDecode(t, preOpenQD, EncodeAttachmentQualificationDecisionV3, DecodeAttachmentQualificationDecisionV3)
			bodyDispatch := domain.BrokerAttachmentOperationAuthorizationRequestV3{Phase: domain.BrokerAttachmentOperationBodyDispatch, Qualified: &domain.BrokerAttachmentQualifiedOperationV3{QualificationRequest: preOpenQ, QualificationDecision: preOpenQD, Snapshot: snapshot, Effects: effects}}
			body := mustAttachmentEncode(t, bodyDispatch, EncodeAttachmentOperationAuthorizationRequestV3)
			if len(body) > 64<<10 {
				t.Fatalf("escaping-aware body-dispatch envelope=%d", len(body))
			}
			if name == "html" {
				legacySnapshot, legacyErr := json.Marshal(attachmentSnapshotToWireV3(snapshot))
				if legacyErr != nil || 2*len(legacySnapshot) <= 64<<10 || bytes.Contains(body, []byte(`\u003c`)) {
					t.Fatalf("HTML-escaping regression proof legacy_snapshot=%d body=%d err=%v", len(legacySnapshot), len(body), legacyErr)
				}
			}
			t.Logf("escaping-aware body-dispatch envelope=%d bytes, remaining=%d", len(body), (64<<10)-len(body))
		})
	}
}

func repeatToUTF8Bytes(value string, maximum int) string {
	var out strings.Builder
	for out.Len()+len(value) <= maximum {
		out.WriteString(value)
	}
	for out.Len() < maximum {
		out.WriteByte('x')
	}
	return out.String()
}

func TestPublishedAttachmentV3SchemasMatchAndValidateVectors(t *testing.T) {
	for _, pair := range []struct {
		path string
		body []byte
	}{{"../../docs/schemas/broker-execution-v3.schema.json", ExecutionSchemaV3()}, {"../../docs/schemas/broker-discovery-v4.schema.json", DiscoverySchemaV4()}} {
		published, err := os.ReadFile(pair.path)
		var strict map[string]any
		if err != nil || !bytes.Equal(published, pair.body) || !decodeAttachmentV3(pair.body, 1<<20, &strict) {
			t.Fatalf("published schema mismatch path=%s err=%v", pair.path, err)
		}
	}
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
	fixture := newAttachmentV3Fixture(t)
	releaseQ := attachmentReleaseQualificationFixture(t, fixture, 10_000)
	releaseQD := attachmentQualificationDecisionFixture(t, releaseQ, 10_000)
	releaseO := attachmentReleaseOperationFixture(t, fixture, releaseQ, releaseQD)
	releaseOD := attachmentOperationDecisionFixture(t, releaseO, 10_000)
	coreDigest, _ := AttachmentManifestCoreSHA256V3(fixture.manifestCore)
	manifest := domain.BrokerAttachmentManifestLineV3{Core: fixture.manifestCore, ManifestCoreSHA256: coreDigest, ReleaseDecisionSHA256: releaseOD.DecisionSHA256}
	payload := []byte("native attachment bytes")
	payloadDigest := sha256.Sum256(payload)
	data := domain.BrokerAttachmentDataLineV3{SchemaVersion: 3, FrameVersion: 1, Kind: domain.BrokerAttachmentReleaseData, StreamID: fixture.anchor.StreamID, Index: 0, Offset: 0, DecodedBytes: int64(len(payload)), PayloadBase64: base64.StdEncoding.EncodeToString(payload), PayloadSHA256: hex.EncodeToString(payloadDigest[:]), CumulativeBytes: int64(len(payload)), CumulativeSHA256: hex.EncodeToString(payloadDigest[:]), ResourceSHA256: fixture.anchor.ResourcesSHA256, PriorReleaseSHA256: releaseO.Release.PriorReleaseSHA256, ReleaseDecisionSHA256: releaseOD.DecisionSHA256}
	terminal := domain.BrokerAttachmentTerminalLineV3{SchemaVersion: 3, FrameVersion: 1, Kind: domain.BrokerAttachmentReleaseTerminal, StreamID: fixture.anchor.StreamID, ChunkCount: 1, TotalBytes: int64(len(payload)), DeclaredSize: int64(len(payload)), WholeSHA256: hex.EncodeToString(payloadDigest[:]), PriorReleaseSHA256: digestTest('a'), ReleaseDecisionSHA256: releaseOD.DecisionSHA256, EOFProven: true, Complete: true}
	vectors := [][]byte{
		mustAttachmentEncode(t, fixture.request, EncodeAttachmentRequestV3), mustAttachmentEncode(t, fixture.admissionRequest, EncodeAttachmentAdmissionRequestV3),
		mustAttachmentEncode(t, fixture.bodyO, EncodeAttachmentOperationAuthorizationRequestV3), mustAttachmentEncode(t, fixture.bodyOD, EncodeAttachmentOperationDecisionV3),
		mustAttachmentEncode(t, releaseO, EncodeAttachmentOperationAuthorizationRequestV3), mustAttachmentEncode(t, releaseOD, EncodeAttachmentOperationDecisionV3),
		mustAttachmentEncode(t, fixture.anchor, EncodeAttachmentStreamAnchorV3), mustAttachmentEncode(t, manifest, EncodeAttachmentManifestLineV3),
		mustAttachmentEncode(t, data, EncodeAttachmentDataLineV3), mustAttachmentEncode(t, terminal, EncodeAttachmentTerminalLineV3),
	}
	for index, vector := range vectors {
		var value any
		if json.Unmarshal(vector, &value) != nil || resolved.Validate(value) != nil {
			t.Fatalf("schema rejected vector %d: %s", index, vector)
		}
	}
}

func newAttachmentV3Fixture(t attachmentTestTB) attachmentV3Fixture {
	t.Helper()
	context := attachmentContextFixture()
	request := domain.BrokerAttachmentRequestV3{SchemaVersion: 3, Operation: domain.BrokerOperationJiraAttachmentDownload, OperationVersion: 1, RequestID: "request-1", Features: append([]string{}, attachmentFeaturesV3...), Expect: domain.BrokerRequestExpectations{ExecutionID: context.ExecutionID, ExecutionEpoch: context.ExecutionEpoch, AuthorityRevision: context.AuthorityRevision}, Arguments: domain.BrokerAttachmentArgumentsV3{IssueKey: "PROJ-1", AttachmentID: "200"}}
	argumentsDigest, _ := AttachmentArgumentsSHA256V3(request.Arguments)
	admissionRequest := domain.BrokerAttachmentAdmissionRequestV3{Context: context, Operation: request.Operation, OperationVersion: 1, RequestID: request.RequestID, Features: append([]string{}, request.Features...), Arguments: request.Arguments, ArgumentsSHA256: argumentsDigest, DeadlineMillis: attachmentTestDeadline}
	admissionDecision := attachmentAdmissionDecisionFixture(t, admissionRequest, 2_000)
	plan := domain.BrokerAttachmentMetadataPlanV3{SelectorSHA256: argumentsDigest, MetadataFields: append([]string{}, attachmentMetadataFieldsV3...), Limits: domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: 1 << 20}}
	initialQ := domain.BrokerAttachmentQualificationRequestV3{Phase: domain.BrokerAttachmentQualificationInitial, Initial: &domain.BrokerAttachmentInitialQualificationV3{Admission: admissionRequest, AdmissionDecision: admissionDecision, Plan: plan}}
	initialQD := attachmentQualificationDecisionFixture(t, initialQ, 2_100)
	snapshot, err := NewAttachmentSnapshotEvidenceV3(domain.BrokerJiraAttachmentSnapshotV3{IssueID: "100", IssueKey: "PROJ-1", Project: "PROJ", Updated: "2026-09-09T00:00:00Z", AttachmentID: "200", ParentID: "100", Filename: "example.bin", MediaType: "application/octet-stream", Created: "2026-09-09T00:00:00Z", DeclaredSize: 23})
	if err != nil {
		t.Fatal(err)
	}
	effects := []domain.BrokerAttachmentEffectV3{{Kind: domain.BrokerEffectRead, ResourceKind: domain.BrokerResourceJiraIssue, Fields: []string{"id", "key", "project", "updated"}}, {Kind: domain.BrokerEffectRead, ResourceKind: domain.BrokerResourceJiraAttachment, Fields: []string{"body", "created", "filename", "id", "media_type", "parent_id", "size"}}}
	initialO := domain.BrokerAttachmentOperationAuthorizationRequestV3{Phase: domain.BrokerAttachmentOperationInitial, Qualified: &domain.BrokerAttachmentQualifiedOperationV3{QualificationRequest: initialQ, QualificationDecision: initialQD, Snapshot: snapshot, Effects: effects}}
	initialOD := attachmentOperationDecisionFixture(t, initialO, 2_200)
	preOpenQ := domain.BrokerAttachmentQualificationRequestV3{Phase: domain.BrokerAttachmentQualificationPreOpen, PreOpen: &domain.BrokerAttachmentPreOpenQualificationV3{InitialOperation: initialO, InitialOperationDecision: initialOD, Plan: plan}}
	preOpenQD := attachmentQualificationDecisionFixture(t, preOpenQ, 2_300)
	bodyO := domain.BrokerAttachmentOperationAuthorizationRequestV3{Phase: domain.BrokerAttachmentOperationBodyDispatch, Qualified: &domain.BrokerAttachmentQualifiedOperationV3{QualificationRequest: preOpenQ, QualificationDecision: preOpenQD, Snapshot: snapshot, Effects: effects}}
	bodyOD := attachmentOperationDecisionFixture(t, bodyO, 2_400)
	requestDigest, _ := AttachmentRequestSHA256V3(request)
	resourcesDigest, _ := AttachmentResourcesSHA256V3(snapshot)
	effectsDigest, _ := AttachmentEffectsSHA256V3(effects)
	snapshotDigest, err := AttachmentSnapshotSHA256V3(snapshot)
	if err != nil {
		t.Fatalf("snapshot digest: %v", err)
	}
	anchor := domain.BrokerAttachmentStreamAnchorV3{
		SchemaVersion: 1, ContractFamily: domain.BrokerContractFamilyExecutionV3, Operation: request.Operation, OperationVersion: 1, Features: append([]string{}, request.Features...), Context: context,
		RequestSHA256: requestDigest, ArgumentsSHA256: argumentsDigest, ResourcesSHA256: resourcesDigest, EffectsSHA256: effectsDigest, SnapshotSHA256: snapshotDigest,
		StreamID: "stream-1", CorrelationID: "correlation-1", OverallDeadlineMillis: attachmentTestDeadline,
		AdmissionDecisionSHA256: admissionDecision.DecisionSHA256, InitialQualificationDecisionSHA256: initialQD.DecisionSHA256,
		InitialOperationDecisionSHA256: initialOD.DecisionSHA256, PreOpenQualificationDecisionSHA256: preOpenQD.DecisionSHA256,
		BodyDispatchOperationDecisionSHA256: bodyOD.DecisionSHA256,
	}
	anchorDigest, _ := AttachmentStreamAnchorSHA256V3(anchor)
	manifestCore := domain.BrokerAttachmentManifestCoreV3{
		SchemaVersion: 3, FrameVersion: 1, StreamID: anchor.StreamID, CorrelationID: anchor.CorrelationID, ArgumentsSHA256: argumentsDigest, AnchorSHA256: anchorDigest,
		Snapshot: snapshot, ConsistencyProfile: domain.BrokerAttachmentConsistencyStepSnapshotV1,
		MaxDataFrames: 16, MaxDecodedFrameBytes: 1 << 20, MaxNativeBodyBytes: 16 << 20, MaxMetadataItems: 10_000,
		MaxManifestLineBytes: 64 << 10, MaxDataLineBytes: 1_441_792, MaxTerminalLineBytes: 16 << 10, MaxFramedBytes: 24 << 20,
		MaxJiraAttempts: 19, MaxAuthenticationAttempts: 17, MaxDecisionAttempts: 37, MaxTotalHostOutboundAttempts: 73, MaxCommandHostOutboundAttempts: 76,
		MaxJiraResponseBytes: 34 << 20, MaxAuthorityResponseBytes: 27 << 18, MaxTotalHostResponseBytes: 163 << 18,
		MaxOperationMillis: 60_000, MaxDecisionLeaseMillis: 5_000,
	}
	return attachmentV3Fixture{request: request, admissionRequest: admissionRequest, admissionDecision: admissionDecision, initialQ: initialQ, initialQD: initialQD, initialO: initialO, initialOD: initialOD, preOpenQ: preOpenQ, preOpenQD: preOpenQD, bodyO: bodyO, bodyOD: bodyOD, anchor: anchor, manifestCore: manifestCore}
}

func attachmentContextFixture() domain.BrokerVerifiedContext {
	return domain.BrokerVerifiedContext{PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "revision-1", ExecutionNotBeforeMillis: 1_000, ExecutionExpiresMillis: 63_000, GrantExpiresMillis: 63_000, CredentialExpiresMillis: 63_000, Backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: digestTest('1'), WorkloadBackendID: "jira-primary"}}
}

func attachmentAdmissionDecisionFixture(t attachmentTestTB, request domain.BrokerAttachmentAdmissionRequestV3, issued int64) domain.BrokerAttachmentAdmissionDecisionV3 {
	t.Helper()
	requestDigest, _ := AttachmentAdmissionRequestSHA256V3(request)
	contextDigest, _ := VerifiedContextSHA256(request.Context)
	value := domain.BrokerAttachmentAdmissionDecisionV3{BrokerDecisionCore: domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: "admission-1", AuthorityRevision: request.Context.AuthorityRevision, ContextSHA256: contextDigest, RequestSHA256: requestDigest, IssuedAtMillis: issued, ExpiresAtMillis: issued + 5_000}}
	return mustAttachmentDecode(t, value, EncodeAttachmentAdmissionDecisionV3, DecodeAttachmentAdmissionDecisionV3)
}

func attachmentQualificationDecisionFixture(t attachmentTestTB, request domain.BrokerAttachmentQualificationRequestV3, issued int64) domain.BrokerAttachmentQualificationDecisionV3 {
	t.Helper()
	requestDigest, _ := AttachmentQualificationRequestSHA256V3(request)
	plan, context, ok := attachmentQualificationPlanContextV3(request, issued)
	if !ok {
		t.Fatal("qualification lineage invalid")
	}
	planDigest, _ := AttachmentMetadataPlanSHA256V3(plan)
	contextDigest, _ := VerifiedContextSHA256(context)
	value := domain.BrokerAttachmentQualificationDecisionV3{Phase: request.Phase, BrokerDecisionCore: domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: "qualification-" + string(request.Phase), AuthorityRevision: context.AuthorityRevision, ContextSHA256: contextDigest, RequestSHA256: requestDigest, IssuedAtMillis: issued, ExpiresAtMillis: issued + 5_000}, PlanSHA256: planDigest}
	return mustAttachmentDecode(t, value, EncodeAttachmentQualificationDecisionV3, DecodeAttachmentQualificationDecisionV3)
}

func attachmentOperationDecisionFixture(t attachmentTestTB, request domain.BrokerAttachmentOperationAuthorizationRequestV3, issued int64) domain.BrokerAttachmentOperationDecisionV3 {
	t.Helper()
	binding, context, err := attachmentOperationDecisionBindingV3(request)
	if err != nil {
		t.Fatal(err)
	}
	requestDigest, _ := AttachmentOperationAuthorizationRequestSHA256V3(request)
	contextDigest, _ := VerifiedContextSHA256(context)
	binding.BrokerDecisionCore = domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: "operation-" + string(request.Phase), AuthorityRevision: context.AuthorityRevision, ContextSHA256: contextDigest, RequestSHA256: requestDigest, IssuedAtMillis: issued, ExpiresAtMillis: issued + 5_000}
	return mustAttachmentDecode(t, binding, EncodeAttachmentOperationDecisionV3, DecodeAttachmentOperationDecisionV3)
}

func attachmentReleaseQualificationFixture(t attachmentTestTB, fixture attachmentV3Fixture, _ int64) domain.BrokerAttachmentQualificationRequestV3 {
	t.Helper()
	anchorDigest, _ := AttachmentStreamAnchorSHA256V3(fixture.anchor)
	coreDigest, _ := AttachmentManifestCoreSHA256V3(fixture.manifestCore)
	prior, _ := AttachmentReleaseRootSHA256V3(anchorDigest, coreDigest)
	context := fixture.anchor.Context
	return domain.BrokerAttachmentQualificationRequestV3{Phase: domain.BrokerAttachmentQualificationRelease, Release: &domain.BrokerAttachmentReleaseQualificationV3{Context: context, AnchorSHA256: anchorDigest, PriorReleaseSHA256: prior, Coordinate: domain.BrokerAttachmentReleaseCoordinateV3{Kind: domain.BrokerAttachmentReleaseData, Index: 0, Offset: 0}, Plan: domain.BrokerAttachmentMetadataPlanV3{SelectorSHA256: fixture.anchor.ArgumentsSHA256, MetadataFields: append([]string{}, attachmentMetadataFieldsV3...), Limits: domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: 1 << 20}}}}
}

func attachmentReleaseOperationFixture(t attachmentTestTB, fixture attachmentV3Fixture, qualification domain.BrokerAttachmentQualificationRequestV3, decision domain.BrokerAttachmentQualificationDecisionV3) domain.BrokerAttachmentOperationAuthorizationRequestV3 {
	t.Helper()
	coreDigest, _ := AttachmentManifestCoreSHA256V3(fixture.manifestCore)
	facts := domain.BrokerAttachmentReleaseFactsV3{Kind: domain.BrokerAttachmentReleaseData, Data: &domain.BrokerAttachmentDataReleaseV3{Index: 0, Offset: 0, DecodedBytes: 23, PayloadSHA256: digestTest('2'), CumulativeBytes: 23, CumulativeSHA256: digestTest('3'), ResourceSHA256: fixture.anchor.ResourcesSHA256, Terminal: &domain.BrokerAttachmentReleaseTerminalFactsV3{ChunkCount: 1, TotalBytes: 23, WholeSHA256: digestTest('3'), EOFProven: true, Complete: true}}}
	return domain.BrokerAttachmentOperationAuthorizationRequestV3{Phase: domain.BrokerAttachmentOperationRelease, Release: &domain.BrokerAttachmentReleaseOperationV3{QualificationRequest: qualification, QualificationDecision: decision, AnchorSHA256: qualification.Release.AnchorSHA256, PriorReleaseSHA256: qualification.Release.PriorReleaseSHA256, SnapshotSHA256: fixture.anchor.SnapshotSHA256, ManifestCoreSHA256: coreDigest, Facts: facts}}
}

func digestTest(char byte) string { return strings.Repeat(string(char), 64) }

func mustAttachment(t attachmentTestTB, body []byte, err error) []byte {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func mustAttachmentCall(t attachmentTestTB, call func() ([]byte, error)) []byte {
	t.Helper()
	body, err := call()
	return mustAttachment(t, body, err)
}

func mustAttachmentEncode[T any](t attachmentTestTB, value T, encode func(T) ([]byte, error)) []byte {
	t.Helper()
	body, err := encode(value)
	return mustAttachment(t, body, err)
}

func mustAttachmentDecode[T any](t attachmentTestTB, value T, encode func(T) ([]byte, error), decode func([]byte) (T, error)) T {
	t.Helper()
	body, encodeErr := encode(value)
	body = mustAttachment(t, body, encodeErr)
	decoded, err := decode(body)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func assertRoundTrip[T any](t *testing.T, value T, encode func(T) ([]byte, error), decode func([]byte) (T, error)) {
	t.Helper()
	decoded := mustAttachmentDecode(t, value, encode, decode)
	if !reflect.DeepEqual(value, decoded) {
		t.Fatalf("round trip changed value\nwant=%+v\ngot=%+v", value, decoded)
	}
}
