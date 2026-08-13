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

func TestIndexerV2PreservesEstablishedV1AttachmentDocumentAndReceiptBytes(t *testing.T) {
	for _, test := range []struct {
		name         string
		legacy       bool
		documentHash string
		receiptHash  string
	}{
		{
			name:         "qualified",
			documentHash: "8be3aaad7a2cbc67283d6ee8d41179562e2ce947a502978a3305005e0181411d",
			receiptHash:  "6be43c2813346993e73c1b818b357e0961dbb4c5e8a534b3fa447d8c7ede8595",
		},
		{
			name:         "legacy",
			legacy:       true,
			documentHash: "64355ec13075622054ea467dd36624f34bdb0f7a798dd7c73715a18351d14878",
			receiptHash:  "c312545c1b67f240cc27ac09235c3bb6da7a803d1d0412da5a14c8cef5529bad",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			documents, edges, markdown, _, _, qualifications := validIndexerV2Bundle(t)
			if test.legacy {
				documents, edges, markdown, _, qualifications = validLegacyIndexerV2Bundle(t)
			}
			documentBytes, err := CanonicalIndexerDocuments([]IndexerDocument{documents[1]}, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := BuildIndexerReceipt(qualifications, documents, edges, markdown, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			receiptBytes, err := CanonicalIndexerReceipt(receipt, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if got := rawSHA256(string(documentBytes)); got != test.documentHash {
				t.Fatalf("document bytes hash = %s, want %s", got, test.documentHash)
			}
			if got := rawSHA256(string(receiptBytes)); got != test.receiptHash {
				t.Fatalf("receipt bytes hash = %s, want %s", got, test.receiptHash)
			}
		})
	}
}

func TestIndexerV2AcceptsClosedAttachmentOwnerShapes(t *testing.T) {
	for _, test := range []struct {
		name   string
		legacy bool
	}{
		{name: "qualified"},
		{name: "legacy", legacy: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			documents, edges, markdown, artifact, member, qualifications := validIndexerV2Bundle(t)
			members := []ArtifactMember{member}
			if test.legacy {
				documents, edges, markdown, artifact, qualifications = validLegacyIndexerV2Bundle(t)
				members = []ArtifactMember{}
			}
			if _, err := BuildIndexerReceiptV2(qualifications, documents, edges, markdown,
				[]IndexerArtifact{artifact}, members, Limits{}); err != nil {
				t.Fatalf("BuildIndexerReceiptV2: %v", err)
			}
		})
	}
}

func TestVerifyIndexerDocumentsV2BindsDocumentServicesToQualifications(t *testing.T) {
	documents, edges, markdown, artifact, member, qualifications := validIndexerV2Bundle(t)
	receipt, err := BuildIndexerReceiptV2(qualifications, documents, edges, markdown,
		[]IndexerArtifact{artifact}, []ArtifactMember{member}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Qualifications = append([]IndexerQualification(nil), receipt.Qualifications...)
	receipt.Qualifications[0].Service = ServiceConfluence
	limits, err := normalizeLimits(Limits{})
	if err != nil {
		t.Fatal(err)
	}
	receipt.ProjectionDigest, err = indexerProjectionDigestV2(receipt, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyIndexerDocumentsV2(receipt, documents, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("cross-service document verification error=%v", err)
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

func TestIndexerV2RejectsQualifiedArtifactSourceLineageDrift(t *testing.T) {
	for name, mutate := range map[string]func([]IndexerDocument, []IndexerEdge, *IndexerArtifact) error{
		"artifact inventory path": func(_ []IndexerDocument, _ []IndexerEdge, artifact *IndexerArtifact) error {
			artifact.Source.InventoryPath = "EX/other.attachments.json"
			return nil
		},
		"attachment inventory path": func(documents []IndexerDocument, _ []IndexerEdge, _ *IndexerArtifact) error {
			documents[1].Source.Path = "EX/other.attachments.json"
			return nil
		},
		"attachment parent metadata": func(documents []IndexerDocument, _ []IndexerEdge, _ *IndexerArtifact) error {
			documents[1].Source.MetadataSHA256 = digestByte('f')
			return nil
		},
		"attachment parent native": func(documents []IndexerDocument, _ []IndexerEdge, _ *IndexerArtifact) error {
			documents[1].Source.NativeSHA256 = digestByte('f')
			return nil
		},
		"artifact parent native": func(_ []IndexerDocument, _ []IndexerEdge, artifact *IndexerArtifact) error {
			artifact.Source.ParentNativeSHA256 = digestByte('f')
			return nil
		},
		"artifact parent metadata": func(_ []IndexerDocument, _ []IndexerEdge, artifact *IndexerArtifact) error {
			artifact.Source.ParentMetadataSHA256 = digestByte('f')
			return nil
		},
		"parent native": func(documents []IndexerDocument, _ []IndexerEdge, _ *IndexerArtifact) error {
			documents[0].Source.NativeSHA256 = digestByte('f')
			return nil
		},
		"parent metadata": func(documents []IndexerDocument, _ []IndexerEdge, _ *IndexerArtifact) error {
			documents[0].Source.MetadataSHA256 = digestByte('f')
			return nil
		},
		"owner evidence path": func(_ []IndexerDocument, edges []IndexerEdge, _ *IndexerArtifact) error {
			edges[0].Evidence.Path = "EX/other.attachments.json"
			id, err := DeriveEdgeID(edges[0])
			edges[0].ID = id
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			documents, edges, markdown, artifact, member, qualifications := validIndexerV2Bundle(t)
			if err := mutate(documents, edges, &artifact); err != nil {
				t.Fatal(err)
			}
			_, err := BuildIndexerReceiptV2(qualifications, documents, edges, markdown,
				[]IndexerArtifact{artifact}, []ArtifactMember{member}, Limits{})
			assertReason(t, err, ReasonLineage)
		})
	}
}

func TestIndexerV2BindsQualifiedInventoryDigestInV2Receipt(t *testing.T) {
	documents, edges, markdown, artifact, member, qualifications := validIndexerV2Bundle(t)
	receipt, err := BuildIndexerReceiptV2(qualifications, documents, edges, markdown,
		[]IndexerArtifact{artifact}, []ArtifactMember{member}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	artifact.Source.InventorySHA256 = digestByte('f')
	if _, err := BuildIndexerReceiptV2(qualifications, documents, edges, markdown,
		[]IndexerArtifact{artifact}, []ArtifactMember{member}, Limits{}); err != nil {
		t.Fatalf("independently valid inventory digest: %v", err)
	}
	assertReason(t, VerifyIndexerBundleV2(receipt, documents, edges, markdown,
		[]IndexerArtifact{artifact}, []ArtifactMember{member}, Limits{}), ReasonDigest)
}

func TestIndexerV2RejectsLegacyArtifactSourceLineageDrift(t *testing.T) {
	for name, mutate := range map[string]func([]IndexerDocument, *IndexerArtifact){
		"inventory digest": func(_ []IndexerDocument, artifact *IndexerArtifact) {
			artifact.Source.InventorySHA256 = digestByte('f')
		},
		"attachment native": func(documents []IndexerDocument, _ *IndexerArtifact) {
			documents[1].Source.NativeSHA256 = digestByte('f')
		},
		"attachment metadata": func(documents []IndexerDocument, _ *IndexerArtifact) {
			documents[1].Source.MetadataSHA256 = digestByte('f')
		},
	} {
		t.Run(name, func(t *testing.T) {
			documents, edges, markdown, artifact, qualifications := validLegacyIndexerV2Bundle(t)
			mutate(documents, &artifact)
			_, err := BuildIndexerReceiptV2(qualifications, documents, edges, markdown,
				[]IndexerArtifact{artifact}, []ArtifactMember{}, Limits{})
			assertReason(t, err, ReasonLineage)
		})
	}
}

func TestIndexerV2RejectsAttachmentOwnerShape(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*IndexerEdge)
		reason Reason
	}{
		{name: "inbound direction", mutate: func(edge *IndexerEdge) { edge.Direction = DirectionInbound }, reason: ReasonMembership},
		{name: "unknown direction", mutate: func(edge *IndexerEdge) { edge.Direction = DirectionUnknown }, reason: ReasonMembership},
		{name: "structural confidence", mutate: func(edge *IndexerEdge) { edge.Confidence = ConfidenceStructural }, reason: ReasonMembership},
		{name: "wrong confidence", mutate: func(edge *IndexerEdge) { edge.Confidence = ConfidenceReported }, reason: ReasonMembership},
		{name: "comments evidence", mutate: func(edge *IndexerEdge) { edge.Evidence.Kind = EvidenceComments }, reason: ReasonMembership},
		{name: "wrong fragment", mutate: func(edge *IndexerEdge) { edge.Evidence.Fragment = "fields.attachment" }, reason: ReasonMembership},
		{name: "empty fragment", mutate: func(edge *IndexerEdge) { edge.Evidence.Fragment = "" }, reason: ReasonMembership},
		{name: "mismatched path", mutate: func(edge *IndexerEdge) { edge.Evidence.Path = "EX/other.attachments.json" }, reason: ReasonLineage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			documents, edges, markdown, artifact, member, qualifications := validIndexerV2Bundle(t)
			test.mutate(&edges[0])
			var err error
			edges[0].ID, err = DeriveEdgeID(edges[0])
			if err != nil {
				t.Fatal(err)
			}
			_, err = BuildIndexerReceiptV2(qualifications, documents, edges, markdown,
				[]IndexerArtifact{artifact}, []ArtifactMember{member}, Limits{})
			assertReason(t, err, test.reason)
		})
	}
}

func TestIndexerV2RequiresExactlyOneMatchingAttachmentOwner(t *testing.T) {
	documents, edges, markdown, artifact, member, qualifications := validIndexerV2Bundle(t)
	_, err := BuildIndexerReceiptV2(qualifications, documents, []IndexerEdge{}, markdown,
		[]IndexerArtifact{artifact}, []ArtifactMember{member}, Limits{})
	assertReason(t, err, ReasonMembership)

	secondOwner := documents[0]
	secondOwner.ID = digestByte('f')
	secondOwner.Key = "EX-2"
	secondOwner.Source.Path = "jira/EX-2.wiki"
	secondOwner.MarkdownPath = "jira/EX-2.md"
	documents = append(documents, secondOwner)
	markdown = append(markdown, MarkdownMember{
		DocumentID: secondOwner.ID, Path: secondOwner.MarkdownPath,
		Size: int64(len(secondOwner.Text)), SHA256: secondOwner.MarkdownSHA256,
	})
	differentOwner := edges[0]
	differentOwner.TargetID = secondOwner.ID
	differentOwner.ID, err = DeriveEdgeID(differentOwner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildIndexerReceipt(qualifications, documents, append(edges, differentOwner), markdown, Limits{}); err != nil {
		t.Fatalf("v1 compatibility rejected two owner candidates: %v", err)
	}
	if _, err := BuildIndexerReceiptV2(qualifications, documents, append(edges, differentOwner), markdown,
		[]IndexerArtifact{artifact}, []ArtifactMember{member}, Limits{}); err == nil {
		t.Fatal("multiple owner edges accepted")
	} else {
		assertReason(t, err, ReasonMembership)
	}
}

func TestIndexerV2ArtifactBodyReasonMatrixIsClosed(t *testing.T) {
	valid := []struct {
		status ArtifactBodyStatus
		reason ArtifactBodyReason
	}{
		{ArtifactBodyCaptured, ""},
		{ArtifactBodyNotRequested, ""},
		{ArtifactBodyExcluded, ArtifactReasonMediaTypeExcluded},
		{ArtifactBodyExcluded, ArtifactReasonCountLimit},
		{ArtifactBodyExcluded, ArtifactReasonItemLimit},
		{ArtifactBodyExcluded, ArtifactReasonAggregateLimit},
		{ArtifactBodyForbidden, ArtifactReasonForbidden},
		{ArtifactBodyFailed, ArtifactReasonFailed},
		{ArtifactBodyFailed, ArtifactReasonSizeMismatch},
	}
	for _, value := range valid {
		if !validArtifactBodyStatus(value.status) || !validArtifactBodyReason(value.reason, value.status) {
			t.Fatalf("rejected status=%q reason=%q", value.status, value.reason)
		}
	}
	if validArtifactBodyStatus("future") {
		t.Fatal("future artifact body status was accepted")
	}
	for _, value := range []struct {
		status ArtifactBodyStatus
		reason ArtifactBodyReason
	}{
		{ArtifactBodyCaptured, ArtifactReasonFailed},
		{ArtifactBodyExcluded, ArtifactReasonForbidden},
		{ArtifactBodyForbidden, ""},
		{ArtifactBodyFailed, ArtifactReasonCountLimit},
		{"future", ""},
	} {
		if validArtifactBodyReason(value.reason, value.status) {
			t.Fatalf("accepted status=%q reason=%q", value.status, value.reason)
		}
	}
}

func TestVerifyIndexerDocumentsV2RejectsInventorySwap(t *testing.T) {
	documents, edges, markdown, artifact, member, qualifications := validIndexerV2Bundle(t)
	receipt, err := BuildIndexerReceiptV2(qualifications, documents, edges, markdown,
		[]IndexerArtifact{artifact}, []ArtifactMember{member}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyIndexerDocumentsV2(receipt, documents, Limits{}); err != nil {
		t.Fatal(err)
	}
	swapped := append([]IndexerDocument(nil), documents...)
	swapped[0].Title += " changed"
	if err := VerifyIndexerDocumentsV2(receipt, swapped, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("swapped error=%v", err)
	}
	if err := VerifyIndexerDocumentsV2(receipt, documents[:1], Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("truncated error=%v", err)
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

func validLegacyIndexerV2Bundle(t testing.TB) ([]IndexerDocument, []IndexerEdge, []MarkdownMember, IndexerArtifact, []IndexerQualification) {
	t.Helper()
	documents, edges, markdown, artifact, _, qualifications := validIndexerV2Bundle(t)
	documents[1].Source = SourceLineage{
		Path: "EX/EX-1.json", NativeSHA256: digestByte('c'), MetadataSHA256: digestByte('c'),
	}
	edges[0].Confidence = ConfidenceReported
	edges[0].Evidence = EdgeEvidence{Kind: EvidenceAttachments, Path: "EX/EX-1.json", Fragment: "fields.attachment"}
	var err error
	edges[0].ID, err = DeriveEdgeID(edges[0])
	if err != nil {
		t.Fatal(err)
	}
	artifact.Status = ArtifactBodyNotRequested
	artifact.Path = ""
	artifact.Size = 0
	artifact.SHA256 = ""
	artifact.Source.InventoryPath = "EX/EX-1.json"
	artifact.Source.InventorySHA256 = digestByte('c')
	return documents, edges, markdown, artifact, qualifications
}
