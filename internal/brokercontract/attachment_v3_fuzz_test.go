package brokercontract

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func FuzzDecodeAttachmentV3Request(f *testing.F) {
	f.Add([]byte(`{"schema_version":3,"operation":"jira.issue.attachment.download"}`))
	fixture := newAttachmentV3Fixture(f)
	f.Add(mustAttachmentEncode(f, fixture.request, EncodeAttachmentRequestV3))
	f.Fuzz(func(_ *testing.T, body []byte) { _, _ = DecodeAttachmentRequestV3(body) })
}

func FuzzDecodeAttachmentV3Qualification(f *testing.F) {
	f.Add([]byte(`{"schema_version":3,"phase":"release","request":null}`))
	fixture := newAttachmentV3Fixture(f)
	release := attachmentReleaseQualificationFixture(f, fixture, 10_000)
	for _, value := range []domain.BrokerAttachmentQualificationRequestV3{fixture.initialQ, fixture.preOpenQ, release} {
		f.Add(mustAttachmentEncode(f, value, EncodeAttachmentQualificationRequestV3))
	}
	f.Fuzz(func(_ *testing.T, body []byte) { _, _ = DecodeAttachmentQualificationRequestV3(body) })
}

func FuzzDecodeAttachmentV3StreamLines(f *testing.F) {
	f.Add([]byte(`{"schema_version":3,"frame_version":1,"kind":"data"}`))
	fixture := newAttachmentV3Fixture(f)
	releaseQ := attachmentReleaseQualificationFixture(f, fixture, 10_000)
	releaseQD := attachmentQualificationDecisionFixture(f, releaseQ, 10_000)
	releaseO := attachmentReleaseOperationFixture(f, fixture, releaseQ, releaseQD)
	releaseOD := attachmentOperationDecisionFixture(f, releaseO, 10_000)
	coreDigest, _ := AttachmentManifestCoreSHA256V3(fixture.manifestCore)
	manifest := domain.BrokerAttachmentManifestLineV3{Core: fixture.manifestCore, ManifestCoreSHA256: coreDigest, ReleaseDecisionSHA256: releaseOD.DecisionSHA256}
	payload := []byte("native attachment bytes")
	payloadDigest := sha256.Sum256(payload)
	payloadSHA256 := hex.EncodeToString(payloadDigest[:])
	data := domain.BrokerAttachmentDataLineV3{SchemaVersion: 3, FrameVersion: 1, Kind: domain.BrokerAttachmentReleaseData, StreamID: fixture.anchor.StreamID, Index: 0, Offset: 0, DecodedBytes: int64(len(payload)), PayloadBase64: base64.StdEncoding.EncodeToString(payload), PayloadSHA256: payloadSHA256, CumulativeBytes: int64(len(payload)), CumulativeSHA256: payloadSHA256, ResourceSHA256: fixture.anchor.ResourcesSHA256, PriorReleaseSHA256: releaseO.Release.PriorReleaseSHA256, ReleaseDecisionSHA256: releaseOD.DecisionSHA256}
	terminal := domain.BrokerAttachmentTerminalLineV3{SchemaVersion: 3, FrameVersion: 1, Kind: domain.BrokerAttachmentReleaseTerminal, StreamID: fixture.anchor.StreamID, ChunkCount: 1, TotalBytes: int64(len(payload)), DeclaredSize: int64(len(payload)), WholeSHA256: payloadSHA256, PriorReleaseSHA256: releaseO.Release.PriorReleaseSHA256, ReleaseDecisionSHA256: releaseOD.DecisionSHA256, EOFProven: true, Complete: true}
	for _, seed := range [][]byte{mustAttachmentEncode(f, manifest, EncodeAttachmentManifestLineV3), mustAttachmentEncode(f, data, EncodeAttachmentDataLineV3), mustAttachmentEncode(f, terminal, EncodeAttachmentTerminalLineV3)} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, body []byte) {
		_, _ = DecodeAttachmentManifestLineV3(body)
		_, _ = DecodeAttachmentDataLineV3(body)
		_, _ = DecodeAttachmentTerminalLineV3(body)
	})
}
