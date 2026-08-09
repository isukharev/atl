package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestIndexerV1StableObjectIDExcludesPresentationFields(t *testing.T) {
	origin := "sha256:" + digestByte('a')
	wantID := "85d43b7ea87372b8aad6519eeac888511b21693152a2538212a9b152116fdeee"
	first, err := StableObjectID(origin, ServiceJira, ObjectIssue, "10001")
	if err != nil {
		t.Fatalf("StableObjectID: %v", err)
	}
	// A renamed key/title/path never enters the helper's preimage.
	second, err := StableObjectID(origin, ServiceJira, ObjectIssue, "10001")
	if err != nil || second != first {
		t.Fatalf("rename-stable identity = %q, %v; want %q", second, err, first)
	}
	if first != wantID {
		t.Fatalf("stable ID = %q, want %q", first, wantID)
	}
	otherOrigin, err := StableObjectID("sha256:"+digestByte('b'), ServiceJira, ObjectIssue, "10001")
	if err != nil || otherOrigin == first {
		t.Fatalf("different origin identity = %q, %v", otherOrigin, err)
	}
	otherKind, err := StableObjectID(origin, ServiceJira, ObjectComment, "10001")
	if err != nil || otherKind == first {
		t.Fatalf("different kind identity = %q, %v", otherKind, err)
	}

	for name, call := range map[string]func() error{
		"bad origin": func() error { _, err := StableObjectID("x", ServiceJira, ObjectIssue, "1"); return err },
		"untagged":   func() error { _, err := StableObjectID(digestByte('a'), ServiceJira, ObjectIssue, "1"); return err },
		"aggregate":  func() error { _, err := StableObjectID(origin, ServiceAggregate, ObjectIssue, "1"); return err },
		"cross kind": func() error { _, err := StableObjectID(origin, ServiceConfluence, ObjectIssue, "1"); return err },
		"zero":       func() error { _, err := StableObjectID(origin, ServiceJira, ObjectIssue, "0"); return err },
		"leading 0":  func() error { _, err := StableObjectID(origin, ServiceJira, ObjectIssue, "01"); return err },
		"nondecimal": func() error { _, err := StableObjectID(origin, ServiceJira, ObjectIssue, "ABC-1"); return err },
	} {
		t.Run(name, func(t *testing.T) { assertRejected(t, call()) })
	}
}

func TestIndexerV1CanonicalizesEmptyLabelsAsQualifiedInventory(t *testing.T) {
	document := validIndexerDocument(t)
	document.Labels = nil

	encoded, err := CanonicalIndexerDocuments([]IndexerDocument{document}, Limits{})
	if err != nil {
		t.Fatalf("CanonicalIndexerDocuments: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"labels":[]`)) || bytes.Contains(encoded, []byte(`"labels":null`)) {
		t.Fatalf("empty label inventory is not canonical: %s", encoded)
	}
	parsed, err := ParseIndexerDocuments(encoded, Limits{})
	if err != nil || len(parsed) != 1 || parsed[0].Labels == nil || len(parsed[0].Labels) != 0 {
		t.Fatalf("ParseIndexerDocuments = %#v, %v", parsed, err)
	}
}

func TestIndexerV1CanonicalBundleExactBytesAndRoots(t *testing.T) {
	document := validIndexerDocument(t)
	document.Labels = []string{"zeta", "alpha"}
	document.Evidence[0], document.Evidence[6] = document.Evidence[6], document.Evidence[0]
	documentBytes, err := CanonicalIndexerDocuments([]IndexerDocument{document}, Limits{})
	if err != nil {
		t.Fatalf("CanonicalIndexerDocuments: %v", err)
	}
	wantDocument := `{"schema_version":1,"id":"85d43b7ea87372b8aad6519eeac888511b21693152a2538212a9b152116fdeee","service":"jira","kind":"issue","key":"EX-1","title":"Synthetic issue","container":"EX","version":"7","updated":"2026-08-09T12:34:56Z","labels":["alpha","zeta"],"source":{"path":"jira/EX-1.wiki","native_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","metadata_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"text":"Body\n","body_sha256":"421dc617d921c24f41441973d8476605718a14a5c2228b8344cc1d6d816e8d39","render_status":"rendered","markdown_path":"jira/EX-1.md","markdown_sha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","visibility":"unknown","evidence":[{"kind":"attachments","status":"complete","reasons":[],"observed_count":0,"count_exact":true},{"kind":"body","status":"complete","reasons":[],"observed_count":1,"count_exact":true},{"kind":"comments","status":"not_requested","reasons":[],"observed_count":0,"count_exact":false},{"kind":"hierarchy","status":"partial","reasons":["unresolved"],"observed_count":0,"count_exact":false},{"kind":"metadata","status":"complete","reasons":[],"observed_count":1,"count_exact":true},{"kind":"relations","status":"unsupported","reasons":["unsupported"],"observed_count":0,"count_exact":false},{"kind":"visibility","status":"unavailable","reasons":["legacy_unqualified"],"observed_count":0,"count_exact":false}]}` + "\n"
	if diff := firstByteDifference(documentBytes, []byte(wantDocument)); diff != "" {
		t.Fatalf("document JSONL differs: %s\ngot:  %s\nwant: %s", diff, documentBytes, wantDocument)
	}

	edge := validIndexerEdge(t, document.ID)
	edgeBytes, err := CanonicalIndexerEdges([]IndexerEdge{edge}, Limits{})
	if err != nil {
		t.Fatalf("CanonicalIndexerEdges: %v", err)
	}
	wantEdge := `{"schema_version":1,"id":"61fe2c2da3eff63986738870f8dc71e283946511d8384e23c1719d229fe58e3c","source_id":"85d43b7ea87372b8aad6519eeac888511b21693152a2538212a9b152116fdeee","relation":"references","direction":"outbound","unresolved":{"service":"confluence","kind":"page","value":"12345"},"confidence":"reported","evidence":{"kind":"relations","path":"jira/EX-1.json","fragment":"fields.links[0]"}}` + "\n"
	if diff := firstByteDifference(edgeBytes, []byte(wantEdge)); diff != "" {
		t.Fatalf("edge JSONL differs: %s\ngot:  %s\nwant: %s", diff, edgeBytes, wantEdge)
	}

	normalizedDocument := normalizeDocument(document)
	markdown := []MarkdownMember{{
		DocumentID: normalizedDocument.ID,
		Path:       normalizedDocument.MarkdownPath,
		Size:       12,
		SHA256:     normalizedDocument.MarkdownSHA256,
	}}
	qualifications := []IndexerQualification{{
		Service:     ServiceJira,
		State:       QualificationPartial,
		Basis:       QualificationStructural,
		ScopeDigest: digestByte('d'),
		Reasons:     []QualificationReason{QualificationLegacyMirror},
	}}
	receipt, err := BuildIndexerReceipt(qualifications, []IndexerDocument{document}, []IndexerEdge{edge}, markdown, Limits{})
	if err != nil {
		t.Fatalf("BuildIndexerReceipt: %v", err)
	}
	receiptBytes, err := CanonicalIndexerReceipt(receipt, Limits{})
	if err != nil {
		t.Fatalf("CanonicalIndexerReceipt: %v", err)
	}
	wantReceipt := `{"schema_version":1,"projection_schema":1,"readiness":"partial","qualifications":[{"service":"jira","state":"partial","basis":"structural","scope_digest":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","reasons":["legacy_mirror"]}],"counts":{"documents":1,"edges":1,"markdown_files":1,"markdown_bytes":12},"documents_digest":"4ccc57d5fa6bf2e9b8f102a5af25dbf53264aef0715a8e70598bb956836066aa","edges_digest":"6f723e530196896a167a6ef83d427ca4b77f1a4c530711dcd8f123718d808919","markdown_digest":"6a7c97bfc93ed54b9fde4aad0cb01423cdfbb1dee47b251094503d9b17927543","projection_digest":"5b691af407566cfea8cb5de2295724c23ccd04963620050d54de016b70e73a84"}` + "\n"
	if diff := firstByteDifference(receiptBytes, []byte(wantReceipt)); diff != "" {
		t.Fatalf("receipt differs: %s\ngot:  %s\nwant: %s", diff, receiptBytes, wantReceipt)
	}

	parsedDocuments, err := ParseIndexerDocuments(documentBytes, Limits{})
	if err != nil || len(parsedDocuments) != 1 || parsedDocuments[0].ID != document.ID {
		t.Fatalf("ParseIndexerDocuments = %#v, %v", parsedDocuments, err)
	}
	parsedEdges, err := ParseIndexerEdges(edgeBytes, Limits{})
	if err != nil || len(parsedEdges) != 1 || parsedEdges[0].ID != edge.ID {
		t.Fatalf("ParseIndexerEdges = %#v, %v", parsedEdges, err)
	}
	parsedReceipt, err := ParseIndexerReceipt(receiptBytes, Limits{})
	if err != nil || parsedReceipt.ProjectionDigest != receipt.ProjectionDigest {
		t.Fatalf("ParseIndexerReceipt = %#v, %v", parsedReceipt, err)
	}
	if err := VerifyIndexerBundle(parsedReceipt, parsedDocuments, parsedEdges, markdown, Limits{}); err != nil {
		t.Fatalf("VerifyIndexerBundle: %v", err)
	}
	if receipt.Readiness != ProjectionPartial {
		t.Fatalf("readiness = %q, want partial", receipt.Readiness)
	}
	if testing.Verbose() {
		t.Logf("stable=%s", document.ID)
		t.Logf("edge-id=%s", edge.ID)
		t.Logf("document=%s", documentBytes)
		t.Logf("edge=%s", edgeBytes)
		t.Logf("receipt=%s", receiptBytes)
	}
}

func TestIndexerV1CanonicalJSONLSortsAndRejectsNonCanonicalInput(t *testing.T) {
	first := validIndexerDocument(t)
	second := first
	second.ID = digestByte('1')
	second.Source.Path = "jira/EX-2.wiki"
	second.MarkdownPath = "jira/EX-2.md"
	second.MarkdownSHA256 = digestByte('2')
	if first.ID < second.ID {
		first, second = second, first
	}
	canonical, err := CanonicalIndexerDocuments([]IndexerDocument{first, second}, Limits{})
	if err != nil {
		t.Fatalf("canonical sorted documents: %v", err)
	}
	parsed, err := ParseIndexerDocuments(canonical, Limits{})
	if err != nil || len(parsed) != 2 || parsed[0].ID >= parsed[1].ID {
		t.Fatalf("sorted parse = %#v, %v", parsed, err)
	}
	lines := bytes.Split(bytes.TrimSuffix(canonical, []byte("\n")), []byte("\n"))
	unsorted := append(append(append([]byte{}, lines[1]...), '\n'), lines[0]...)
	unsorted = append(unsorted, '\n')
	_, err = ParseIndexerDocuments(unsorted, Limits{})
	assertReason(t, err, ReasonFormat)

	for name, data := range map[string][]byte{
		"missing newline": bytes.TrimSuffix(canonical, []byte("\n")),
		"extra newline":   append(append([]byte{}, canonical...), '\n'),
		"blank line":      append([]byte("\n"), canonical...),
		"duplicate key":   []byte(strings.Replace(string(lines[0])+"\n", `{"schema_version":1,`, `{"schema_version":1,"schema_version":1,`, 1)),
		"unknown key":     []byte(strings.Replace(string(lines[0])+"\n", `{"schema_version":1,`, `{"unknown":1,"schema_version":1,`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseIndexerDocuments(data, Limits{})
			assertRejected(t, err)
		})
	}

	empty, err := CanonicalIndexerDocuments([]IndexerDocument{}, Limits{})
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty canonical documents = %q, %v", empty, err)
	}
	parsed, err = ParseIndexerDocuments(empty, Limits{})
	if err != nil || parsed == nil || len(parsed) != 0 {
		t.Fatalf("empty parsed documents = %#v, %v", parsed, err)
	}
	if _, err := CanonicalIndexerDocuments(nil, Limits{}); err == nil {
		t.Fatal("nil document inventory accepted")
	}
}

func TestIndexerV1DocumentValidationPreservesEvidenceDistinctions(t *testing.T) {
	base := validIndexerDocument(t)
	tests := []struct {
		name   string
		mutate func(*IndexerDocument)
		reason Reason
	}{
		{name: "future schema", mutate: func(d *IndexerDocument) { d.SchemaVersion++ }, reason: ReasonSchema},
		{name: "bad id", mutate: func(d *IndexerDocument) { d.ID = "short" }, reason: ReasonDigest},
		{name: "body mismatch", mutate: func(d *IndexerDocument) { d.Text += "changed" }, reason: ReasonDigest},
		{name: "non UTC time", mutate: func(d *IndexerDocument) { d.Updated = "2026-08-09T14:34:56+02:00" }, reason: ReasonFormat},
		{name: "unknown visibility", mutate: func(d *IndexerDocument) { d.Visibility = "public" }, reason: ReasonType},
		{name: "missing evidence", mutate: func(d *IndexerDocument) { d.Evidence = d.Evidence[:6] }, reason: ReasonMembership},
		{name: "complete without exact count", mutate: func(d *IndexerDocument) { d.Evidence[0].CountExact = false }, reason: ReasonMembership},
		{name: "partial without reason", mutate: func(d *IndexerDocument) { d.Evidence[1].Status = EvidencePartial }, reason: ReasonMembership},
		{name: "not requested claims exact", mutate: func(d *IndexerDocument) { d.Evidence[2].CountExact = true }, reason: ReasonMembership},
		{name: "not requested claims observations", mutate: func(d *IndexerDocument) { d.Evidence[2].ObservedCount = 1 }, reason: ReasonMembership},
		{name: "unsupported lacks reason", mutate: func(d *IndexerDocument) { d.Evidence[5].Reasons = []EvidenceReason{EvidenceMissing} }, reason: ReasonMembership},
		{name: "unknown visibility claims complete", mutate: func(d *IndexerDocument) {
			d.Evidence[6] = Evidence{Kind: EvidenceVisibility, Status: EvidenceComplete, Reasons: []EvidenceReason{}, CountExact: true}
		}, reason: ReasonMembership},
		{name: "failed with text", mutate: func(d *IndexerDocument) { d.RenderStatus = RenderFailed }, reason: ReasonMembership},
		{name: "failed without render evidence", mutate: func(d *IndexerDocument) {
			d.Text = ""
			d.BodySHA256 = rawSHA256("")
			d.RenderStatus = RenderFailed
			d.MarkdownPath = ""
			d.MarkdownSHA256 = ""
		}, reason: ReasonMembership},
		{name: "path escape", mutate: func(d *IndexerDocument) { d.Source.Path = "../outside" }, reason: ReasonPath},
		{name: "duplicate label", mutate: func(d *IndexerDocument) { d.Labels = []string{"same", "same"} }, reason: ReasonMembership},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := normalizeDocument(base)
			test.mutate(&document)
			_, err := CanonicalIndexerDocuments([]IndexerDocument{document}, Limits{})
			assertReason(t, err, test.reason)
		})
	}

	empty := normalizeDocument(base)
	empty.Text = ""
	empty.BodySHA256 = rawSHA256("")
	empty.RenderStatus = RenderEmpty
	if _, err := CanonicalIndexerDocuments([]IndexerDocument{empty}, Limits{}); err != nil {
		t.Fatalf("qualified empty body rejected: %v", err)
	}
	failed := empty
	failed.RenderStatus = RenderFailed
	failed.MarkdownPath, failed.MarkdownSHA256 = "", ""
	failed.Evidence[1] = Evidence{Kind: EvidenceBody, Status: EvidenceUnavailable, Reasons: []EvidenceReason{EvidenceRenderFailed}}
	if _, err := CanonicalIndexerDocuments([]IndexerDocument{failed}, Limits{}); err != nil {
		t.Fatalf("explicit render failure rejected: %v", err)
	}
}

func TestIndexerV1EdgeRequiresQualifiedOrUnresolvedTarget(t *testing.T) {
	document := validIndexerDocument(t)
	base := validIndexerEdge(t, document.ID)
	tests := []struct {
		name   string
		mutate func(*IndexerEdge)
		reason Reason
	}{
		{name: "both targets", mutate: func(e *IndexerEdge) { e.TargetID = digestByte('1') }, reason: ReasonMembership},
		{name: "no target", mutate: func(e *IndexerEdge) { e.Unresolved = nil }, reason: ReasonMembership},
		{name: "unsafe evidence", mutate: func(e *IndexerEdge) { e.Evidence.Path = "../raw" }, reason: ReasonPath},
		{name: "URL reference", mutate: func(e *IndexerEdge) { e.Unresolved.Value = "https://backend.invalid/1" }, reason: ReasonType},
		{name: "opaque HTTP URL reference", mutate: func(e *IndexerEdge) { e.Unresolved.Value = "HTTPS:backend.invalid/1" }, reason: ReasonType},
		{name: "scheme relative URL reference", mutate: func(e *IndexerEdge) { e.Unresolved.Value = "//backend.invalid/1" }, reason: ReasonType},
		{name: "active URL reference", mutate: func(e *IndexerEdge) { e.Unresolved.Value = "javascript:alert(1)" }, reason: ReasonType},
		{name: "name on generic relation", mutate: func(e *IndexerEdge) { e.RelationName = "custom" }, reason: ReasonMembership},
		{name: "invented relation", mutate: func(e *IndexerEdge) { e.Relation = "blocks" }, reason: ReasonType},
		{name: "wrong id", mutate: func(e *IndexerEdge) { e.ID = digestByte('f') }, reason: ReasonDigest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			edge := base
			copy := *base.Unresolved
			edge.Unresolved = &copy
			test.mutate(&edge)
			_, err := CanonicalIndexerEdges([]IndexerEdge{edge}, Limits{})
			assertReason(t, err, test.reason)
		})
	}
	titled := base
	titled.Unresolved = &Reference{Service: ServiceConfluence, Kind: ObjectPage, Value: "Synthetic page title"}
	titled.ID, _ = DeriveEdgeID(titled)
	if _, err := CanonicalIndexerEdges([]IndexerEdge{titled}, Limits{}); err != nil {
		t.Fatalf("bounded unresolved title rejected: %v", err)
	}
}

func TestIndexerV1ReceiptQualificationAndBundleIntegrity(t *testing.T) {
	document := normalizeDocument(validIndexerDocument(t))
	markdown := []MarkdownMember{{DocumentID: document.ID, Path: document.MarkdownPath, Size: 1, SHA256: document.MarkdownSHA256}}
	partial := []IndexerQualification{{
		Service: ServiceJira, State: QualificationPartial, Basis: QualificationStructural,
		ScopeDigest: digestByte('b'), Reasons: []QualificationReason{QualificationLegacyMirror},
	}}
	receipt, err := BuildIndexerReceipt(partial, []IndexerDocument{document}, []IndexerEdge{}, markdown, Limits{})
	if err != nil {
		t.Fatalf("partial structural receipt: %v", err)
	}
	if receipt.Readiness != ProjectionPartial {
		t.Fatalf("partial readiness = %q", receipt.Readiness)
	}

	ready := append([]IndexerQualification(nil), partial...)
	ready[0].State = QualificationReady
	ready[0].Basis = QualificationReceipt
	ready[0].SourceReceiptDigest = digestByte('c')
	ready[0].Reasons = []QualificationReason{}
	receipt, err = BuildIndexerReceipt(ready, []IndexerDocument{document}, []IndexerEdge{}, markdown, Limits{})
	if err != nil || receipt.Readiness != ProjectionReady {
		t.Fatalf("ready receipt = %#v, %v", receipt, err)
	}

	bad := append([]IndexerQualification(nil), ready...)
	bad[0].Basis = QualificationStructural
	bad[0].SourceReceiptDigest = ""
	_, err = BuildIndexerReceipt(bad, []IndexerDocument{document}, []IndexerEdge{}, markdown, Limits{})
	assertReason(t, err, ReasonMembership)

	wrongMarkdown := append([]MarkdownMember(nil), markdown...)
	wrongMarkdown[0].SHA256 = digestByte('e')
	_, err = BuildIndexerReceipt(ready, []IndexerDocument{document}, []IndexerEdge{}, wrongMarkdown, Limits{})
	assertReason(t, err, ReasonDigest)

	otherService := append([]IndexerQualification(nil), ready...)
	otherService[0].Service = ServiceConfluence
	_, err = BuildIndexerReceipt(otherService, []IndexerDocument{document}, []IndexerEdge{}, markdown, Limits{})
	assertReason(t, err, ReasonMembership)

	receipt, err = BuildIndexerReceipt(ready, []IndexerDocument{document}, []IndexerEdge{}, markdown, Limits{})
	if err != nil {
		t.Fatalf("ready receipt rebuild: %v", err)
	}
	receipt.DocumentsDigest = digestByte('e')
	if _, err := CanonicalIndexerReceipt(receipt, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("mutated receipt error = %v", err)
	}

	duplicatePath := []MarkdownMember{
		markdown[0],
		{DocumentID: digestByte('1'), Path: markdown[0].Path, Size: 1, SHA256: digestByte('2')},
	}
	if _, _, err := indexerMarkdownDigest(duplicatePath, DefaultLimits()); err == nil {
		t.Fatal("duplicate Markdown path accepted")
	}
}

func validIndexerDocument(t testing.TB) IndexerDocument {
	t.Helper()
	id, err := StableObjectID("sha256:"+digestByte('a'), ServiceJira, ObjectIssue, "10001")
	if err != nil {
		t.Fatal(err)
	}
	return IndexerDocument{
		SchemaVersion: IndexerSchemaV1,
		ID:            id,
		Service:       ServiceJira,
		Kind:          ObjectIssue,
		Key:           "EX-1",
		Title:         "Synthetic issue",
		Container:     "EX",
		Version:       "7",
		Updated:       "2026-08-09T12:34:56Z",
		Labels:        []string{"alpha", "zeta"},
		Source: SourceLineage{
			Path:           "jira/EX-1.wiki",
			NativeSHA256:   digestByte('b'),
			MetadataSHA256: digestByte('c'),
		},
		Text:           "Body\n",
		BodySHA256:     rawSHA256("Body\n"),
		RenderStatus:   RenderRendered,
		MarkdownPath:   "jira/EX-1.md",
		MarkdownSHA256: digestByte('d'),
		Visibility:     VisibilityUnknown,
		Evidence: []Evidence{
			{Kind: EvidenceAttachments, Status: EvidenceComplete, Reasons: []EvidenceReason{}, CountExact: true},
			{Kind: EvidenceBody, Status: EvidenceComplete, Reasons: []EvidenceReason{}, ObservedCount: 1, CountExact: true},
			{Kind: EvidenceComments, Status: EvidenceNotRequested, Reasons: []EvidenceReason{}},
			{Kind: EvidenceHierarchy, Status: EvidencePartial, Reasons: []EvidenceReason{EvidenceUnresolved}},
			{Kind: EvidenceMetadata, Status: EvidenceComplete, Reasons: []EvidenceReason{}, ObservedCount: 1, CountExact: true},
			{Kind: EvidenceRelations, Status: EvidenceUnsupported, Reasons: []EvidenceReason{EvidenceUnsupportedReason}},
			{Kind: EvidenceVisibility, Status: EvidenceUnavailable, Reasons: []EvidenceReason{EvidenceLegacyUnqualified}},
		},
	}
}

func validIndexerEdge(t testing.TB, sourceID string) IndexerEdge {
	t.Helper()
	edge := IndexerEdge{
		SchemaVersion: IndexerSchemaV1,
		SourceID:      sourceID,
		Relation:      EdgeReferences,
		Direction:     DirectionOutbound,
		Unresolved:    &Reference{Service: ServiceConfluence, Kind: ObjectPage, Value: "12345"},
		Confidence:    ConfidenceReported,
		Evidence:      EdgeEvidence{Kind: EvidenceRelations, Path: "jira/EX-1.json", Fragment: "fields.links[0]"},
	}
	var err error
	edge.ID, err = DeriveEdgeID(edge)
	if err != nil {
		t.Fatal(err)
	}
	return edge
}

func rawSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
