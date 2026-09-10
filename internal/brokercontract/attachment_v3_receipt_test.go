package brokercontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestAttachmentV3ExactLineHashIncludesDelimiterBeyondJSONCap(t *testing.T) {
	line := bytes.Repeat([]byte{'x'}, int(MaxAttachmentDataLineBytesV3))
	line = append(line, '\n')
	want := sha256.Sum256(line)
	if got, err := AttachmentExactLineSHA256V3(line); err != nil || got != hex.EncodeToString(want[:]) {
		t.Fatalf("maximum emitted line hash=%q err=%v", got, err)
	}
	if _, err := AttachmentExactLineSHA256V3(append(line, 'x')); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("oversized emitted line err=%v", err)
	}
}

func TestAttachmentV3FirstAndLastReleaseBindsAllThreeLines(t *testing.T) {
	fixture := newAttachmentV3Fixture(t)
	qualification := attachmentReleaseQualificationFixture(t, fixture, 10_000)
	qualificationDecision := attachmentQualificationDecisionFixture(t, qualification, 10_000)
	operation := attachmentReleaseOperationFixture(t, fixture, qualification, qualificationDecision)
	payload := []byte("native attachment bytes")
	digest := sha256.Sum256(payload)
	payloadSHA256 := hex.EncodeToString(digest[:])
	facts := operation.Release.Facts.Data
	facts.PayloadSHA256, facts.CumulativeSHA256, facts.Terminal.WholeSHA256 = payloadSHA256, payloadSHA256, payloadSHA256
	decision := attachmentOperationDecisionFixture(t, operation, 10_000)
	prior := operation.Release.PriorReleaseSHA256
	if err := ValidateAttachmentReleaseQualificationV3(qualification, fixture.anchor, prior, 10_000, 15_000); err != nil {
		t.Fatalf("current release qualification: %v", err)
	}
	if err := ValidateAttachmentOperationDecisionV3(decision, operation, 10_000, attachmentTestDeadline); err != nil {
		t.Fatalf("current release decision: %v", err)
	}
	manifest := domain.BrokerAttachmentManifestLineV3{
		Core: fixture.manifestCore, ManifestCoreSHA256: operation.Release.ManifestCoreSHA256,
		ReleaseDecisionSHA256: decision.DecisionSHA256,
	}
	data := domain.BrokerAttachmentDataLineV3{
		SchemaVersion: 3, FrameVersion: 1, Kind: domain.BrokerAttachmentReleaseData,
		StreamID: fixture.anchor.StreamID, Index: 0, Offset: 0,
		DecodedBytes: int64(len(payload)), PayloadBase64: base64.StdEncoding.EncodeToString(payload),
		PayloadSHA256: payloadSHA256, CumulativeBytes: int64(len(payload)), CumulativeSHA256: payloadSHA256,
		ResourceSHA256: fixture.anchor.ResourcesSHA256, PriorReleaseSHA256: prior, ReleaseDecisionSHA256: decision.DecisionSHA256,
	}
	terminal := domain.BrokerAttachmentTerminalLineV3{
		SchemaVersion: 3, FrameVersion: 1, Kind: domain.BrokerAttachmentReleaseTerminal,
		StreamID: fixture.anchor.StreamID, ChunkCount: 1, TotalBytes: int64(len(payload)),
		DeclaredSize: fixture.manifestCore.Snapshot.DeclaredSize, WholeSHA256: payloadSHA256,
		PriorReleaseSHA256: prior, ReleaseDecisionSHA256: decision.DecisionSHA256, EOFProven: true, Complete: true,
	}
	if err := ValidateAttachmentTerminalLineForReleaseV3(terminal, operation.Release.Facts, terminal.DeclaredSize, terminal.StreamID, prior, decision.DecisionSHA256); err != nil {
		t.Fatalf("terminal facts binding: %v", err)
	}
	lines := [][]byte{
		mustAttachmentEncode(t, manifest, EncodeAttachmentManifestLineV3),
		mustAttachmentEncode(t, data, EncodeAttachmentDataLineV3),
		mustAttachmentEncode(t, terminal, EncodeAttachmentTerminalLineV3),
	}
	lineDigests := make([]string, len(lines))
	for index, line := range lines {
		// The receipt includes the exact emitted newline, not just JSON bytes.
		var err error
		lineDigests[index], err = AttachmentExactLineSHA256V3(append(line, '\n'))
		if err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := AttachmentReleaseReceiptSHA256V3(prior, decision.DecisionSHA256, lineDigests)
	if err != nil || !validDigest(receipt) {
		t.Fatalf("first-and-last three-line receipt: %q, %v", receipt, err)
	}
	for name, changed := range map[string][]string{
		"missing manifest": lineDigests[1:],
		"missing data":     {lineDigests[0], lineDigests[2]},
		"missing terminal": lineDigests[:2],
		"reordered":        {lineDigests[0], lineDigests[2], lineDigests[1]},
	} {
		t.Run(name, func(t *testing.T) {
			changedReceipt, err := AttachmentReleaseReceiptSHA256V3(prior, decision.DecisionSHA256, changed)
			if err != nil || changedReceipt == receipt {
				t.Fatalf("changed receipt = %q, %v", changedReceipt, err)
			}
		})
	}
}

func TestAttachmentV3ReleaseReceiptBoundsAndExistingVectors(t *testing.T) {
	prior, decision := digestTest('a'), digestTest('b')
	lines := []string{digestTest('c'), digestTest('d'), digestTest('e')}
	// Independent SHA-256 vectors over the unchanged canonical receipt envelope.
	for count, expected := range map[int]string{
		1: "57334f053ea7d189ae8d42a826906b1b638c9bfacc0677f044c2a8f7791c41ac",
		2: "7b892540115cd6ada913d35d5d74b161b0a62f7285219d39c27b44fa0e116bde",
	} {
		if actual, err := AttachmentReleaseReceiptSHA256V3(prior, decision, lines[:count]); err != nil || actual != expected {
			t.Fatalf("%d-line legacy vector: %q, %v", count, actual, err)
		}
	}
	for name, rejected := range map[string][]string{
		"empty":                nil,
		"four lines":           {lines[0], lines[1], lines[2], digestTest('f')},
		"invalid third digest": {lines[0], lines[1], "invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := AttachmentReleaseReceiptSHA256V3(prior, decision, rejected); !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("invalid receipt err=%v", err)
			}
		})
	}
}
