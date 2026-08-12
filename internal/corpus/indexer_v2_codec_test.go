package corpus

import (
	"bytes"
	"errors"
	"testing"
)

func TestIndexerV2ArtifactBundleCanonicalRoundTrip(t *testing.T) {
	documents, edges, markdown, artifact, member, qualifications := validIndexerV2Bundle(t)
	encoded, err := CanonicalIndexerArtifacts([]IndexerArtifact{artifact}, Limits{})
	if err != nil {
		t.Fatalf("CanonicalIndexerArtifacts: %v", err)
	}
	if !bytes.HasSuffix(encoded, []byte("\n")) || !bytes.Contains(encoded, []byte(`"status":"captured"`)) {
		t.Fatalf("artifact JSONL = %q", encoded)
	}
	parsed, err := ParseIndexerArtifacts(encoded, Limits{})
	if err != nil || len(parsed) != 1 || parsed[0] != artifact {
		t.Fatalf("ParseIndexerArtifacts = %#v, %v", parsed, err)
	}

	receipt, err := BuildIndexerReceiptV2(qualifications, documents, edges, markdown,
		[]IndexerArtifact{artifact}, []ArtifactMember{member}, Limits{})
	if err != nil {
		t.Fatalf("BuildIndexerReceiptV2: %v", err)
	}
	if receipt.SchemaVersion != IndexerReceiptSchemaV2 || receipt.ProjectionSchema != IndexerSchemaV2 ||
		receipt.Counts.Artifacts != 1 || receipt.Counts.ArtifactBytes != 3 || receipt.Readiness != ProjectionPartial {
		t.Fatalf("receipt = %#v", receipt)
	}
	receiptBytes, err := CanonicalIndexerReceiptV2(receipt, Limits{})
	if err != nil {
		t.Fatalf("CanonicalIndexerReceiptV2: %v", err)
	}
	parsedReceipt, err := ParseIndexerReceiptV2(receiptBytes, Limits{})
	if err != nil || parsedReceipt.ProjectionDigest != receipt.ProjectionDigest {
		t.Fatalf("ParseIndexerReceiptV2 = %#v, %v", parsedReceipt, err)
	}
	if err := VerifyIndexerBundleV2(parsedReceipt, documents, edges, markdown,
		[]IndexerArtifact{artifact}, []ArtifactMember{member}, Limits{}); err != nil {
		t.Fatalf("VerifyIndexerBundleV2: %v", err)
	}
}

func TestIndexerV2PreservesStrictV1ReceiptBoundary(t *testing.T) {
	documents, edges, markdown, artifact, member, qualifications := validIndexerV2Bundle(t)
	v1, err := BuildIndexerReceipt(qualifications, documents, edges, markdown, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	v1Bytes, err := CanonicalIndexerReceipt(v1, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := BuildIndexerReceiptV2(qualifications, documents, edges, markdown,
		[]IndexerArtifact{artifact}, []ArtifactMember{member}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	v2Bytes, err := CanonicalIndexerReceiptV2(v2, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseIndexerReceipt(v2Bytes, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("v1 accepted v2 receipt: %v", err)
	}
	if _, err := ParseIndexerReceiptV2(v1Bytes, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("v2 accepted v1 receipt: %v", err)
	}
}

func TestIndexerV2RejectsArtifactMembershipAndStatusDrift(t *testing.T) {
	documents, edges, markdown, artifact, member, qualifications := validIndexerV2Bundle(t)
	for name, mutate := range map[string]func(*IndexerArtifact, *ArtifactMember){
		"unsafe path":    func(value *IndexerArtifact, _ *ArtifactMember) { value.Path = "../escape" },
		"unknown status": func(value *IndexerArtifact, _ *ArtifactMember) { value.Status = "unknown" },
		"member digest":  func(_ *IndexerArtifact, value *ArtifactMember) { value.SHA256 = digestByte('f') },
		"wrong parent":   func(value *IndexerArtifact, _ *ArtifactMember) { value.ParentID = digestByte('f') },
	} {
		t.Run(name, func(t *testing.T) {
			changedArtifact, changedMember := artifact, member
			mutate(&changedArtifact, &changedMember)
			if _, err := BuildIndexerReceiptV2(qualifications, documents, edges, markdown,
				[]IndexerArtifact{changedArtifact}, []ArtifactMember{changedMember}, Limits{}); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if _, err := BuildIndexerReceiptV2(qualifications, documents, edges, markdown,
		[]IndexerArtifact{}, []ArtifactMember{}, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("missing artifact record error = %v", err)
	}
	if _, err := BuildIndexerReceiptV2(qualifications, documents, edges, markdown,
		[]IndexerArtifact{artifact}, []ArtifactMember{}, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("missing artifact member error = %v", err)
	}
}

func validIndexerV2Bundle(t testing.TB) ([]IndexerDocument, []IndexerEdge, []MarkdownMember, IndexerArtifact, ArtifactMember, []IndexerQualification) {
	t.Helper()
	owner := normalizeDocument(validIndexerDocument(t))
	owner.Evidence[0] = Evidence{Kind: EvidenceAttachments, Status: EvidenceComplete, Reasons: []EvidenceReason{}, ObservedCount: 1, CountExact: true}
	attachmentID, err := StableObjectID("sha256:"+digestByte('a'), ServiceJira, ObjectAttachment, "30001")
	if err != nil {
		t.Fatal(err)
	}
	attachment := IndexerDocument{
		SchemaVersion: IndexerSchemaV1, ID: attachmentID, Service: ServiceJira, Kind: ObjectAttachment,
		Title: "synthetic.bin", Container: "EX", Labels: []string{},
		Source:     SourceLineage{Path: "EX/EX-1.attachments.json", NativeSHA256: digestByte('b'), MetadataSHA256: digestByte('c')},
		BodySHA256: rawSHA256(""), RenderStatus: RenderUnsupported, Visibility: VisibilityUnknown,
		Evidence: []Evidence{
			{Kind: EvidenceAttachments, Status: EvidenceUnsupported, Reasons: []EvidenceReason{EvidenceUnsupportedReason}},
			{Kind: EvidenceBody, Status: EvidenceUnsupported, Reasons: []EvidenceReason{EvidenceUnsupportedReason}},
			{Kind: EvidenceComments, Status: EvidenceUnsupported, Reasons: []EvidenceReason{EvidenceUnsupportedReason}},
			{Kind: EvidenceHierarchy, Status: EvidenceComplete, Reasons: []EvidenceReason{}, ObservedCount: 1, CountExact: true},
			{Kind: EvidenceMetadata, Status: EvidenceComplete, Reasons: []EvidenceReason{}, ObservedCount: 1, CountExact: true},
			{Kind: EvidenceRelations, Status: EvidenceComplete, Reasons: []EvidenceReason{}, ObservedCount: 1, CountExact: true},
			{Kind: EvidenceVisibility, Status: EvidenceUnavailable, Reasons: []EvidenceReason{EvidenceLegacyUnqualified}},
		},
	}
	edge := IndexerEdge{
		SchemaVersion: IndexerSchemaV1, SourceID: attachmentID, Relation: EdgeAttachmentOwner,
		Direction: DirectionOutbound, TargetID: owner.ID, Confidence: ConfidenceExact,
		Evidence: EdgeEvidence{Kind: EvidenceAttachments, Path: "EX/EX-1.attachments.json", Fragment: "attachment-owner"},
	}
	edge.ID, err = DeriveEdgeID(edge)
	if err != nil {
		t.Fatal(err)
	}
	artifact := IndexerArtifact{
		SchemaVersion: IndexerSchemaV2, DocumentID: attachmentID, Service: ServiceJira, ParentID: owner.ID,
		MediaType: "application/octet-stream", DeclaredSize: 3, Status: ArtifactBodyCaptured,
		Path: "artifacts/jira/" + attachmentID + ".body", Size: 3, SHA256: digestByte('e'),
		Source: ArtifactSourceLineage{
			InventoryPath: "EX/EX-1.attachments.json", InventorySHA256: digestByte('d'),
			ParentNativeSHA256: digestByte('b'), ParentMetadataSHA256: digestByte('c'),
		},
	}
	member := ArtifactMember{DocumentID: attachmentID, Path: artifact.Path, Size: artifact.Size, SHA256: artifact.SHA256}
	markdown := []MarkdownMember{{
		DocumentID: owner.ID, Path: owner.MarkdownPath, Size: int64(len(owner.Text)), SHA256: owner.MarkdownSHA256,
	}}
	qualifications := []IndexerQualification{{
		Service: ServiceJira, State: QualificationPartial, Basis: QualificationStructural,
		ScopeDigest: digestByte('a'), Reasons: []QualificationReason{QualificationLegacyMirror},
	}}
	return []IndexerDocument{owner, attachment}, []IndexerEdge{edge}, markdown, artifact, member, qualifications
}
