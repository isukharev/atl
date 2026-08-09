package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	indexerStableIDDomain   = "atl.corpus.indexer-v1.stable-id.v1"
	indexerEdgeIDDomain     = "atl.corpus.indexer-v1.edge-id.v1"
	indexerDocumentsDomain  = "atl.corpus.indexer-v1.documents.v1"
	indexerEdgesDomain      = "atl.corpus.indexer-v1.edges.v1"
	indexerMarkdownDomain   = "atl.corpus.indexer-v1.markdown.v1"
	indexerProjectionDomain = "atl.corpus.indexer-v1.projection.v1"

	maxIndexerFieldBytes = 64 << 10
	maxIndexerLabels     = 10_000
	maxIndexerEvidence   = 32
)

// StableObjectID derives a rename-stable logical identity. originDigest must
// be the mirror's tagged content-minimized backend-origin digest; providerID
// must be the immutable canonical positive decimal ID supplied by the provider.
func StableObjectID(originDigest string, service Service, kind ObjectKind, providerID string) (string, error) {
	if !validOriginDigest(originDigest) {
		return "", reject(ReasonDigest)
	}
	if !validQualificationService(service) || !validObjectKind(service, kind) {
		return "", reject(ReasonType)
	}
	if err := validateProviderID(providerID); err != nil {
		return "", err
	}
	return domainHash(indexerStableIDDomain,
		[]byte(originDigest), []byte(service), []byte(kind), []byte(providerID)), nil
}

// DeriveEdgeID derives the semantic identity of an edge. Evidence locations
// are intentionally excluded so a mirror path relocation does not rename the
// relationship.
func DeriveEdgeID(edge IndexerEdge) (string, error) {
	if !isLowerSHA256(edge.SourceID) || !validEdgeRelation(edge.Relation) ||
		!validDirection(edge.Direction) {
		return "", reject(ReasonType)
	}
	if err := validateBoundedPlain(edge.RelationName, true); err != nil {
		return "", err
	}
	target, err := edgeTargetPreimage(edge)
	if err != nil {
		return "", err
	}
	return domainHash(indexerEdgeIDDomain,
		[]byte(edge.SourceID), []byte(edge.Relation), []byte(edge.RelationName),
		[]byte(edge.Direction), target), nil
}

// CanonicalIndexerDocuments returns sorted canonical JSONL. Empty inventories
// are encoded as zero bytes; every non-empty record ends in exactly one LF.
func CanonicalIndexerDocuments(documents []IndexerDocument, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if documents == nil {
		return nil, reject(ReasonFormat)
	}
	if len(documents) > limits.MaxMembers {
		return nil, reject(ReasonBounds)
	}
	normalized := make([]IndexerDocument, len(documents))
	for i := range documents {
		normalized[i] = normalizeDocument(documents[i])
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].ID < normalized[j].ID })
	return encodeDocumentJSONL(normalized, limits)
}

// ParseIndexerDocuments accepts only exact canonical indexer-v1 JSONL.
func ParseIndexerDocuments(data []byte, limits Limits) ([]IndexerDocument, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	documents, err := decodeDocumentJSONL(data, limits)
	if err != nil {
		return nil, err
	}
	canonical, err := CanonicalIndexerDocuments(documents, limits)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(data, canonical) {
		return nil, reject(ReasonFormat)
	}
	return documents, nil
}

// CanonicalIndexerEdges returns sorted canonical JSONL.
func CanonicalIndexerEdges(edges []IndexerEdge, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if edges == nil {
		return nil, reject(ReasonFormat)
	}
	if len(edges) > limits.MaxMembers {
		return nil, reject(ReasonBounds)
	}
	normalized := append([]IndexerEdge(nil), edges...)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].ID < normalized[j].ID })
	return encodeEdgeJSONL(normalized, limits)
}

// ParseIndexerEdges accepts only exact canonical indexer-v1 JSONL.
func ParseIndexerEdges(data []byte, limits Limits) ([]IndexerEdge, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	edges, err := decodeEdgeJSONL(data, limits)
	if err != nil {
		return nil, err
	}
	canonical, err := CanonicalIndexerEdges(edges, limits)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(data, canonical) {
		return nil, reject(ReasonFormat)
	}
	return edges, nil
}

// BuildIndexerReceipt validates and binds an exact projection bundle.
func BuildIndexerReceipt(qualifications []IndexerQualification, documents []IndexerDocument, edges []IndexerEdge, markdown []MarkdownMember, limits Limits) (IndexerReceipt, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return IndexerReceipt{}, err
	}
	documentBytes, err := CanonicalIndexerDocuments(documents, limits)
	if err != nil {
		return IndexerReceipt{}, err
	}
	edgeBytes, err := CanonicalIndexerEdges(edges, limits)
	if err != nil {
		return IndexerReceipt{}, err
	}
	markdownDigest, markdownBytes, err := indexerMarkdownDigest(markdown, limits)
	if err != nil {
		return IndexerReceipt{}, err
	}
	qualifications = normalizeQualifications(qualifications)
	readiness, err := validateIndexerBundle(qualifications, documents, edges, markdown, limits)
	if err != nil {
		return IndexerReceipt{}, err
	}
	receipt := IndexerReceipt{
		SchemaVersion:    IndexerReceiptSchemaV1,
		ProjectionSchema: IndexerSchemaV1,
		Readiness:        readiness,
		Qualifications:   qualifications,
		Counts:           ProjectionCounts{Documents: len(documents), Edges: len(edges), MarkdownFiles: len(markdown), MarkdownBytes: markdownBytes},
		DocumentsDigest:  domainHash(indexerDocumentsDomain, documentBytes),
		EdgesDigest:      domainHash(indexerEdgesDomain, edgeBytes),
		MarkdownDigest:   markdownDigest,
	}
	receipt.ProjectionDigest, err = indexerProjectionDigest(receipt, limits)
	if err != nil {
		return IndexerReceipt{}, err
	}
	return receipt, nil
}

// CanonicalIndexerReceipt returns the exact content-free receipt bytes.
func CanonicalIndexerReceipt(receipt IndexerReceipt, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if err := validateIndexerReceipt(receipt, limits); err != nil {
		return nil, err
	}
	data, err := marshalCanonical(receipt)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limits.MaxManifestBytes {
		return nil, reject(ReasonBounds)
	}
	return data, nil
}

// ParseIndexerReceipt accepts only the exact canonical receipt encoding.
func ParseIndexerReceipt(data []byte, limits Limits) (IndexerReceipt, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return IndexerReceipt{}, err
	}
	if len(data) == 0 || int64(len(data)) > limits.MaxManifestBytes {
		return IndexerReceipt{}, reject(ReasonBounds)
	}
	var receipt IndexerReceipt
	if err := decodeStrictObject(data, &receipt); err != nil {
		return IndexerReceipt{}, err
	}
	canonical, err := CanonicalIndexerReceipt(receipt, limits)
	if err != nil {
		return IndexerReceipt{}, err
	}
	if !bytes.Equal(data, canonical) {
		return IndexerReceipt{}, reject(ReasonFormat)
	}
	return receipt, nil
}

// VerifyIndexerBundle proves that a receipt names these exact documents,
// edges, Markdown members, and service qualifications.
func VerifyIndexerBundle(receipt IndexerReceipt, documents []IndexerDocument, edges []IndexerEdge, markdown []MarkdownMember, limits Limits) error {
	want, err := BuildIndexerReceipt(receipt.Qualifications, documents, edges, markdown, limits)
	if err != nil {
		return err
	}
	got, err := CanonicalIndexerReceipt(receipt, limits)
	if err != nil {
		return err
	}
	wantBytes, err := CanonicalIndexerReceipt(want, limits)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, wantBytes) {
		return reject(ReasonDigest)
	}
	return nil
}

func encodeDocumentJSONL(documents []IndexerDocument, limits Limits) ([]byte, error) {
	var output bytes.Buffer
	for i := range documents {
		if err := validateIndexerDocument(documents[i], limits); err != nil {
			return nil, err
		}
		if i > 0 && documents[i-1].ID >= documents[i].ID {
			return nil, reject(ReasonMembership)
		}
		line, err := json.Marshal(documents[i])
		if err != nil {
			return nil, reject(ReasonFormat)
		}
		if int64(len(line)+1) > limits.MaxMemberBytes || int64(output.Len()+len(line)+1) > limits.MaxTotalBytes {
			return nil, reject(ReasonBounds)
		}
		output.Write(line)
		output.WriteByte('\n')
	}
	return output.Bytes(), nil
}

func encodeEdgeJSONL(edges []IndexerEdge, limits Limits) ([]byte, error) {
	var output bytes.Buffer
	for i := range edges {
		if err := validateIndexerEdge(edges[i], limits); err != nil {
			return nil, err
		}
		if i > 0 && edges[i-1].ID >= edges[i].ID {
			return nil, reject(ReasonMembership)
		}
		line, err := json.Marshal(edges[i])
		if err != nil {
			return nil, reject(ReasonFormat)
		}
		if int64(len(line)+1) > limits.MaxMemberBytes || int64(output.Len()+len(line)+1) > limits.MaxTotalBytes {
			return nil, reject(ReasonBounds)
		}
		output.Write(line)
		output.WriteByte('\n')
	}
	return output.Bytes(), nil
}

func decodeDocumentJSONL(data []byte, limits Limits) ([]IndexerDocument, error) {
	if int64(len(data)) > limits.MaxTotalBytes {
		return nil, reject(ReasonBounds)
	}
	var documents []IndexerDocument
	err := forEachJSONLine(data, limits, func(line []byte) error {
		var document IndexerDocument
		if err := decodeStrictObject(line, &document); err != nil {
			return err
		}
		documents = append(documents, document)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if documents == nil {
		documents = []IndexerDocument{}
	}
	return documents, nil
}

func decodeEdgeJSONL(data []byte, limits Limits) ([]IndexerEdge, error) {
	if int64(len(data)) > limits.MaxTotalBytes {
		return nil, reject(ReasonBounds)
	}
	var edges []IndexerEdge
	err := forEachJSONLine(data, limits, func(line []byte) error {
		var edge IndexerEdge
		if err := decodeStrictObject(line, &edge); err != nil {
			return err
		}
		edges = append(edges, edge)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if edges == nil {
		edges = []IndexerEdge{}
	}
	return edges, nil
}

func forEachJSONLine(data []byte, limits Limits, visit func([]byte) error) error {
	if len(data) == 0 {
		return nil
	}
	if data[len(data)-1] != '\n' {
		return reject(ReasonFormat)
	}
	count := 0
	for start := 0; start < len(data); {
		relativeEnd := bytes.IndexByte(data[start:], '\n')
		if relativeEnd < 0 {
			return reject(ReasonFormat)
		}
		end := start + relativeEnd
		if end == start {
			return reject(ReasonFormat)
		}
		if int64(end-start+1) > limits.MaxMemberBytes {
			return reject(ReasonBounds)
		}
		count++
		if count > limits.MaxMembers {
			return reject(ReasonBounds)
		}
		if err := visit(data[start:end]); err != nil {
			return err
		}
		start = end + 1
	}
	return nil
}

func normalizeDocument(document IndexerDocument) IndexerDocument {
	document.Labels = append([]string(nil), document.Labels...)
	sort.Strings(document.Labels)
	document.Evidence = append([]Evidence(nil), document.Evidence...)
	for i := range document.Evidence {
		reasons := document.Evidence[i].Reasons
		document.Evidence[i].Reasons = make([]EvidenceReason, len(document.Evidence[i].Reasons))
		copy(document.Evidence[i].Reasons, reasons)
		sort.Slice(document.Evidence[i].Reasons, func(left, right int) bool {
			return document.Evidence[i].Reasons[left] < document.Evidence[i].Reasons[right]
		})
	}
	sort.Slice(document.Evidence, func(i, j int) bool { return document.Evidence[i].Kind < document.Evidence[j].Kind })
	return document
}

func normalizeQualifications(qualifications []IndexerQualification) []IndexerQualification {
	normalized := make([]IndexerQualification, len(qualifications))
	for i := range qualifications {
		normalized[i] = qualifications[i]
		normalized[i].Reasons = make([]QualificationReason, len(qualifications[i].Reasons))
		copy(normalized[i].Reasons, qualifications[i].Reasons)
		sort.Slice(normalized[i].Reasons, func(left, right int) bool {
			return normalized[i].Reasons[left] < normalized[i].Reasons[right]
		})
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Service < normalized[j].Service })
	return normalized
}

func validateIndexerDocument(document IndexerDocument, limits Limits) error {
	if document.SchemaVersion != IndexerSchemaV1 {
		return reject(ReasonSchema)
	}
	if !isLowerSHA256(document.ID) || !isLowerSHA256(document.BodySHA256) ||
		!isLowerSHA256(document.Source.NativeSHA256) || !isLowerSHA256(document.Source.MetadataSHA256) {
		return reject(ReasonDigest)
	}
	if !validQualificationService(document.Service) || !validObjectKind(document.Service, document.Kind) ||
		!validVisibility(document.Visibility) || !validRenderStatus(document.RenderStatus) {
		return reject(ReasonType)
	}
	for _, value := range []string{document.Key, document.Title, document.Container, document.Version} {
		if err := validateBoundedPlain(value, true); err != nil {
			return err
		}
	}
	if document.Updated != "" {
		parsed, err := time.Parse(time.RFC3339Nano, document.Updated)
		if err != nil || parsed.UTC().Format(time.RFC3339Nano) != document.Updated {
			return reject(ReasonFormat)
		}
	}
	if document.Labels == nil || len(document.Labels) > maxIndexerLabels {
		return reject(ReasonBounds)
	}
	for i, label := range document.Labels {
		if err := validateBoundedPlain(label, false); err != nil {
			return err
		}
		if i > 0 && document.Labels[i-1] >= label {
			return reject(ReasonMembership)
		}
	}
	if err := validateMemberPath(document.Source.Path, limits); err != nil {
		return err
	}
	if !utf8.ValidString(document.Text) || strings.IndexByte(document.Text, 0) >= 0 || int64(len(document.Text)) > limits.MaxMemberBytes {
		return reject(ReasonBounds)
	}
	wantBody := sha256.Sum256([]byte(document.Text))
	if document.BodySHA256 != bytesToLowerHex(wantBody[:]) {
		return reject(ReasonDigest)
	}
	switch document.RenderStatus {
	case RenderRendered:
		if document.Text == "" || document.MarkdownPath == "" || !isLowerSHA256(document.MarkdownSHA256) {
			return reject(ReasonMembership)
		}
	case RenderEmpty:
		if document.Text != "" || document.MarkdownPath == "" || !isLowerSHA256(document.MarkdownSHA256) {
			return reject(ReasonMembership)
		}
	case RenderFailed, RenderUnsupported:
		if document.Text != "" || document.MarkdownPath != "" || document.MarkdownSHA256 != "" {
			return reject(ReasonMembership)
		}
	}
	if document.MarkdownPath != "" {
		if err := validateMemberPath(document.MarkdownPath, limits); err != nil {
			return err
		}
	}
	if err := validateDocumentEvidence(document.Evidence); err != nil {
		return err
	}
	bodyEvidence := document.Evidence[1]
	visibilityEvidence := document.Evidence[6]
	switch document.RenderStatus {
	case RenderRendered, RenderEmpty:
		if bodyEvidence.Status != EvidenceComplete {
			return reject(ReasonMembership)
		}
	case RenderFailed:
		if bodyEvidence.Status != EvidenceUnavailable || !hasEvidenceReason(bodyEvidence.Reasons, EvidenceRenderFailed) {
			return reject(ReasonMembership)
		}
	case RenderUnsupported:
		if bodyEvidence.Status != EvidenceUnsupported || !hasEvidenceReason(bodyEvidence.Reasons, EvidenceUnsupportedReason) {
			return reject(ReasonMembership)
		}
	}
	if document.Visibility == VisibilityUnknown {
		if visibilityEvidence.Status == EvidenceComplete {
			return reject(ReasonMembership)
		}
	} else if visibilityEvidence.Status != EvidenceComplete {
		return reject(ReasonMembership)
	}
	return nil
}

func validateDocumentEvidence(evidence []Evidence) error {
	all := []EvidenceKind{EvidenceAttachments, EvidenceBody, EvidenceComments, EvidenceHierarchy, EvidenceMetadata, EvidenceRelations, EvidenceVisibility}
	if len(evidence) != len(all) || len(evidence) > maxIndexerEvidence {
		return reject(ReasonMembership)
	}
	for i, item := range evidence {
		if item.Kind != all[i] || !validEvidenceStatus(item.Status) || item.Reasons == nil || item.ObservedCount < 0 {
			return reject(ReasonType)
		}
		if item.CountExact != (item.Status == EvidenceComplete) {
			return reject(ReasonMembership)
		}
		if item.Status != EvidenceComplete && item.Status != EvidencePartial && item.ObservedCount != 0 {
			return reject(ReasonMembership)
		}
		if item.Status == EvidenceComplete || item.Status == EvidenceNotRequested {
			if len(item.Reasons) != 0 {
				return reject(ReasonMembership)
			}
		} else if len(item.Reasons) == 0 {
			return reject(ReasonMembership)
		}
		if item.Status == EvidenceForbidden && !hasEvidenceReason(item.Reasons, EvidenceRestrictedReason) {
			return reject(ReasonMembership)
		}
		if item.Status == EvidenceUnsupported && !hasEvidenceReason(item.Reasons, EvidenceUnsupportedReason) {
			return reject(ReasonMembership)
		}
		for reasonIndex, reason := range item.Reasons {
			if !validEvidenceReason(reason) {
				return reject(ReasonType)
			}
			if reasonIndex > 0 && item.Reasons[reasonIndex-1] >= reason {
				return reject(ReasonMembership)
			}
		}
	}
	return nil
}

func validateIndexerEdge(edge IndexerEdge, limits Limits) error {
	if edge.SchemaVersion != IndexerSchemaV1 {
		return reject(ReasonSchema)
	}
	if !isLowerSHA256(edge.ID) || !isLowerSHA256(edge.SourceID) ||
		!validEdgeRelation(edge.Relation) || !validDirection(edge.Direction) || !validConfidence(edge.Confidence) {
		return reject(ReasonType)
	}
	if err := validateBoundedPlain(edge.RelationName, true); err != nil {
		return err
	}
	if (edge.Relation == EdgeJiraIssueLink) != (edge.RelationName != "") {
		return reject(ReasonMembership)
	}
	wantID, err := DeriveEdgeID(edge)
	if err != nil {
		return err
	}
	if edge.ID != wantID {
		return reject(ReasonDigest)
	}
	if !validEvidenceKind(edge.Evidence.Kind) {
		return reject(ReasonType)
	}
	if err := validateMemberPath(edge.Evidence.Path, limits); err != nil {
		return err
	}
	return validateBoundedPlain(edge.Evidence.Fragment, true)
}

func edgeTargetPreimage(edge IndexerEdge) ([]byte, error) {
	if (edge.TargetID == "") == (edge.Unresolved == nil) {
		return nil, reject(ReasonMembership)
	}
	if edge.TargetID != "" {
		if !isLowerSHA256(edge.TargetID) {
			return nil, reject(ReasonDigest)
		}
		return []byte("id:" + edge.TargetID), nil
	}
	if !validQualificationService(edge.Unresolved.Service) || !validObjectKind(edge.Unresolved.Service, edge.Unresolved.Kind) {
		return nil, reject(ReasonType)
	}
	if err := validateProviderReference(edge.Unresolved.Value); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(edge.Unresolved)
	if err != nil {
		return nil, reject(ReasonFormat)
	}
	return append([]byte("ref:"), encoded...), nil
}

func indexerMarkdownDigest(markdown []MarkdownMember, limits Limits) (string, int64, error) {
	if markdown == nil {
		return "", 0, reject(ReasonFormat)
	}
	if len(markdown) > limits.MaxMembers {
		return "", 0, reject(ReasonBounds)
	}
	normalized := append([]MarkdownMember(nil), markdown...)
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].DocumentID != normalized[j].DocumentID {
			return normalized[i].DocumentID < normalized[j].DocumentID
		}
		return normalized[i].Path < normalized[j].Path
	})
	hash := sha256.New()
	writeHashPart(hash, []byte(indexerMarkdownDomain))
	var total int64
	seenDocuments := make(map[string]struct{}, len(normalized))
	seenPaths := make(map[string]struct{}, len(normalized))
	for _, member := range normalized {
		if !isLowerSHA256(member.DocumentID) || !isLowerSHA256(member.SHA256) {
			return "", 0, reject(ReasonDigest)
		}
		if err := validateMemberPath(member.Path, limits); err != nil {
			return "", 0, err
		}
		if member.Size < 0 || member.Size > limits.MaxMemberBytes || member.Size > limits.MaxTotalBytes-total {
			return "", 0, reject(ReasonBounds)
		}
		if _, duplicate := seenDocuments[member.DocumentID]; duplicate {
			return "", 0, reject(ReasonMembership)
		}
		if _, duplicate := seenPaths[member.Path]; duplicate {
			return "", 0, reject(ReasonMembership)
		}
		seenDocuments[member.DocumentID] = struct{}{}
		seenPaths[member.Path] = struct{}{}
		total += member.Size
		entry := struct {
			DocumentID string `json:"document_id"`
			Path       string `json:"path"`
			Size       int64  `json:"size"`
			SHA256     string `json:"sha256"`
		}{member.DocumentID, member.Path, member.Size, member.SHA256}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return "", 0, reject(ReasonFormat)
		}
		writeHashPart(hash, encoded)
	}
	return bytesToLowerHex(hash.Sum(nil)), total, nil
}

func validateIndexerBundle(qualifications []IndexerQualification, documents []IndexerDocument, edges []IndexerEdge, markdown []MarkdownMember, limits Limits) (ProjectionReadiness, error) {
	readiness, qualified, err := validateIndexerQualifications(qualifications)
	if err != nil {
		return "", err
	}
	documentByID := make(map[string]IndexerDocument, len(documents))
	for _, document := range documents {
		document = normalizeDocument(document)
		if err := validateIndexerDocument(document, limits); err != nil {
			return "", err
		}
		if _, ok := qualified[document.Service]; !ok {
			return "", reject(ReasonMembership)
		}
		if _, duplicate := documentByID[document.ID]; duplicate {
			return "", reject(ReasonMembership)
		}
		documentByID[document.ID] = document
	}
	for _, edge := range edges {
		if _, ok := documentByID[edge.SourceID]; !ok {
			return "", reject(ReasonMembership)
		}
		if edge.TargetID != "" {
			if _, ok := documentByID[edge.TargetID]; !ok {
				return "", reject(ReasonMembership)
			}
		}
	}
	markdownByDocument := make(map[string]MarkdownMember, len(markdown))
	for _, member := range markdown {
		if _, ok := documentByID[member.DocumentID]; !ok {
			return "", reject(ReasonMembership)
		}
		if _, duplicate := markdownByDocument[member.DocumentID]; duplicate {
			return "", reject(ReasonMembership)
		}
		markdownByDocument[member.DocumentID] = member
	}
	for id, document := range documentByID {
		member, present := markdownByDocument[id]
		want := document.RenderStatus == RenderRendered || document.RenderStatus == RenderEmpty
		if present != want {
			return "", reject(ReasonMembership)
		}
		if present && (member.Path != document.MarkdownPath || member.SHA256 != document.MarkdownSHA256) {
			return "", reject(ReasonDigest)
		}
	}
	return readiness, nil
}

func validateIndexerQualifications(qualifications []IndexerQualification) (ProjectionReadiness, map[Service]struct{}, error) {
	if qualifications == nil {
		return "", nil, reject(ReasonFormat)
	}
	if len(qualifications) == 0 || len(qualifications) > 2 {
		return "", nil, reject(ReasonMembership)
	}
	qualified := make(map[Service]struct{}, len(qualifications))
	readiness := ProjectionReady
	for i, qualification := range qualifications {
		if !validQualificationService(qualification.Service) || !validQualificationState(qualification.State) ||
			!validQualificationBasis(qualification.Basis) {
			return "", nil, reject(ReasonType)
		}
		if i > 0 && qualifications[i-1].Service >= qualification.Service {
			return "", nil, reject(ReasonMembership)
		}
		if !isLowerSHA256(qualification.ScopeDigest) || qualification.Reasons == nil {
			return "", nil, reject(ReasonDigest)
		}
		if qualification.Basis == QualificationReceipt {
			if !isLowerSHA256(qualification.SourceReceiptDigest) {
				return "", nil, reject(ReasonDigest)
			}
		} else if qualification.SourceReceiptDigest != "" || qualification.State == QualificationReady {
			return "", nil, reject(ReasonMembership)
		}
		if qualification.State == QualificationReady {
			if len(qualification.Reasons) != 0 {
				return "", nil, reject(ReasonMembership)
			}
		} else if len(qualification.Reasons) == 0 {
			return "", nil, reject(ReasonMembership)
		}
		for reasonIndex, reason := range qualification.Reasons {
			if !validQualificationReason(reason) {
				return "", nil, reject(ReasonType)
			}
			if reasonIndex > 0 && qualification.Reasons[reasonIndex-1] >= reason {
				return "", nil, reject(ReasonMembership)
			}
		}
		qualified[qualification.Service] = struct{}{}
		if qualification.State == QualificationUnavailable {
			readiness = ProjectionUnavailable
		} else if qualification.State == QualificationPartial && readiness == ProjectionReady {
			readiness = ProjectionPartial
		}
	}
	return readiness, qualified, nil
}
