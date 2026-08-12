package corpus

import (
	"bytes"
	"encoding/json"
	"sort"
)

const (
	indexerArtifactsDomain    = "atl.corpus.indexer-v2.artifacts.v1"
	indexerProjectionV2Domain = "atl.corpus.indexer-v2.projection.v1"
)

// CanonicalIndexerArtifacts returns stable document-id-ordered JSONL. Empty
// inventories are exactly zero bytes.
func CanonicalIndexerArtifacts(artifacts []IndexerArtifact, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if artifacts == nil {
		return nil, reject(ReasonFormat)
	}
	if len(artifacts) > limits.MaxMembers {
		return nil, reject(ReasonBounds)
	}
	normalized := append([]IndexerArtifact(nil), artifacts...)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].DocumentID < normalized[j].DocumentID })
	var output bytes.Buffer
	for index, artifact := range normalized {
		if err := validateIndexerArtifact(artifact, limits); err != nil {
			return nil, err
		}
		if index > 0 && normalized[index-1].DocumentID >= artifact.DocumentID {
			return nil, reject(ReasonMembership)
		}
		line, err := json.Marshal(artifact)
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

// ParseIndexerArtifacts accepts only the exact canonical indexer-v2 JSONL.
func ParseIndexerArtifacts(data []byte, limits Limits) ([]IndexerArtifact, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limits.MaxTotalBytes {
		return nil, reject(ReasonBounds)
	}
	artifacts := []IndexerArtifact{}
	if err := forEachJSONLine(data, limits, func(line []byte) error {
		var artifact IndexerArtifact
		if err := decodeStrictObject(line, &artifact); err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
		return nil
	}); err != nil {
		return nil, err
	}
	canonical, err := CanonicalIndexerArtifacts(artifacts, limits)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(data, canonical) {
		return nil, reject(ReasonFormat)
	}
	return artifacts, nil
}

// BuildIndexerReceiptV2 validates and binds the unchanged v1 document, edge,
// and Markdown inventories plus the additive artifact inventory and members.
func BuildIndexerReceiptV2(
	qualifications []IndexerQualification,
	documents []IndexerDocument,
	edges []IndexerEdge,
	markdown []MarkdownMember,
	artifacts []IndexerArtifact,
	artifactMembers []ArtifactMember,
	limits Limits,
) (IndexerReceiptV2, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return IndexerReceiptV2{}, err
	}
	documentBytes, err := CanonicalIndexerDocuments(documents, limits)
	if err != nil {
		return IndexerReceiptV2{}, err
	}
	edgeBytes, err := CanonicalIndexerEdges(edges, limits)
	if err != nil {
		return IndexerReceiptV2{}, err
	}
	artifactBytes, err := CanonicalIndexerArtifacts(artifacts, limits)
	if err != nil {
		return IndexerReceiptV2{}, err
	}
	markdownDigest, markdownSize, err := indexerMarkdownDigest(markdown, limits)
	if err != nil {
		return IndexerReceiptV2{}, err
	}
	qualifications = normalizeQualifications(qualifications)
	readiness, capturedBytes, err := validateIndexerBundleV2(qualifications, documents, edges, markdown, artifacts, artifactMembers, limits)
	if err != nil {
		return IndexerReceiptV2{}, err
	}
	receipt := IndexerReceiptV2{
		SchemaVersion: IndexerReceiptSchemaV2, ProjectionSchema: IndexerSchemaV2,
		Readiness: readiness, Qualifications: qualifications,
		Counts: ProjectionCountsV2{
			Documents: len(documents), Edges: len(edges), MarkdownFiles: len(markdown), MarkdownBytes: markdownSize,
			Artifacts: len(artifacts), ArtifactBytes: capturedBytes,
		},
		DocumentsDigest: domainHash(indexerDocumentsDomain, documentBytes),
		EdgesDigest:     domainHash(indexerEdgesDomain, edgeBytes),
		MarkdownDigest:  markdownDigest,
		ArtifactsDigest: domainHash(indexerArtifactsDomain, artifactBytes),
	}
	receipt.ProjectionDigest, err = indexerProjectionDigestV2(receipt, limits)
	if err != nil {
		return IndexerReceiptV2{}, err
	}
	return receipt, nil
}

// CanonicalIndexerReceiptV2 returns the exact content-free receipt bytes.
func CanonicalIndexerReceiptV2(receipt IndexerReceiptV2, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if err := validateIndexerReceiptV2(receipt, limits); err != nil {
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

// ParseIndexerReceiptV2 accepts only exact canonical schema-v2 bytes.
func ParseIndexerReceiptV2(data []byte, limits Limits) (IndexerReceiptV2, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return IndexerReceiptV2{}, err
	}
	if len(data) == 0 || int64(len(data)) > limits.MaxManifestBytes {
		return IndexerReceiptV2{}, reject(ReasonBounds)
	}
	var receipt IndexerReceiptV2
	if err := decodeStrictObject(data, &receipt); err != nil {
		return IndexerReceiptV2{}, err
	}
	canonical, err := CanonicalIndexerReceiptV2(receipt, limits)
	if err != nil {
		return IndexerReceiptV2{}, err
	}
	if !bytes.Equal(data, canonical) {
		return IndexerReceiptV2{}, reject(ReasonFormat)
	}
	return receipt, nil
}

// VerifyIndexerBundleV2 proves that a receipt names these exact inventories.
func VerifyIndexerBundleV2(
	receipt IndexerReceiptV2,
	documents []IndexerDocument,
	edges []IndexerEdge,
	markdown []MarkdownMember,
	artifacts []IndexerArtifact,
	artifactMembers []ArtifactMember,
	limits Limits,
) error {
	want, err := BuildIndexerReceiptV2(receipt.Qualifications, documents, edges, markdown, artifacts, artifactMembers, limits)
	if err != nil {
		return err
	}
	gotBytes, err := CanonicalIndexerReceiptV2(receipt, limits)
	if err != nil {
		return err
	}
	wantBytes, err := CanonicalIndexerReceiptV2(want, limits)
	if err != nil {
		return err
	}
	if !bytes.Equal(gotBytes, wantBytes) {
		return reject(ReasonDigest)
	}
	return nil
}

func validateIndexerArtifact(artifact IndexerArtifact, limits Limits) error {
	if artifact.SchemaVersion != IndexerSchemaV2 {
		return reject(ReasonSchema)
	}
	if !isLowerSHA256(artifact.DocumentID) || !isLowerSHA256(artifact.ParentID) ||
		!validQualificationService(artifact.Service) {
		return reject(ReasonType)
	}
	if err := validateBoundedPlain(artifact.MediaType, true); err != nil {
		return err
	}
	if artifact.DeclaredSize < 0 || artifact.Size < 0 {
		return reject(ReasonBounds)
	}
	if err := validateMemberPath(artifact.Source.InventoryPath, limits); err != nil {
		return err
	}
	if !isLowerSHA256(artifact.Source.InventorySHA256) || !isLowerSHA256(artifact.Source.ParentNativeSHA256) ||
		!isLowerSHA256(artifact.Source.ParentMetadataSHA256) {
		return reject(ReasonDigest)
	}
	if !validArtifactBodyStatus(artifact.Status) || !validArtifactBodyReason(artifact.Reason, artifact.Status) {
		return reject(ReasonType)
	}
	if artifact.Status == ArtifactBodyCaptured {
		wantPath := "artifacts/" + string(artifact.Service) + "/" + artifact.DocumentID + ".body"
		if artifact.Path != wantPath || artifact.Reason != "" || artifact.Size != artifact.DeclaredSize ||
			artifact.Size > limits.MaxMemberBytes || !isLowerSHA256(artifact.SHA256) {
			return reject(ReasonMembership)
		}
		return validateMemberPath(artifact.Path, limits)
	}
	if artifact.Path != "" || artifact.Size != 0 || artifact.SHA256 != "" {
		return reject(ReasonMembership)
	}
	return nil
}

func validArtifactBodyStatus(status ArtifactBodyStatus) bool {
	switch status {
	case ArtifactBodyCaptured, ArtifactBodyExcluded, ArtifactBodyForbidden, ArtifactBodyFailed, ArtifactBodyNotRequested:
		return true
	default:
		return false
	}
}

func validArtifactBodyReason(reason ArtifactBodyReason, status ArtifactBodyStatus) bool {
	switch status {
	case ArtifactBodyCaptured, ArtifactBodyNotRequested:
		return reason == ""
	case ArtifactBodyExcluded:
		return reason == ArtifactReasonMediaTypeExcluded || reason == ArtifactReasonCountLimit ||
			reason == ArtifactReasonItemLimit || reason == ArtifactReasonAggregateLimit
	case ArtifactBodyForbidden:
		return reason == ArtifactReasonForbidden
	case ArtifactBodyFailed:
		return reason == ArtifactReasonFailed || reason == ArtifactReasonSizeMismatch
	default:
		return false
	}
}

func validateIndexerReceiptV2(receipt IndexerReceiptV2, limits Limits) error {
	if receipt.SchemaVersion != IndexerReceiptSchemaV2 || receipt.ProjectionSchema != IndexerSchemaV2 {
		return reject(ReasonSchema)
	}
	readiness, _, err := validateIndexerQualifications(receipt.Qualifications)
	if err != nil {
		return err
	}
	if receipt.Readiness != readiness {
		return reject(ReasonMembership)
	}
	counts := receipt.Counts
	if counts.Documents < 0 || counts.Documents > limits.MaxMembers || counts.Edges < 0 || counts.Edges > limits.MaxMembers ||
		counts.MarkdownFiles < 0 || counts.MarkdownFiles > limits.MaxMembers || counts.MarkdownBytes < 0 || counts.MarkdownBytes > limits.MaxTotalBytes ||
		counts.Artifacts < 0 || counts.Artifacts > limits.MaxMembers || counts.ArtifactBytes < 0 || counts.ArtifactBytes > limits.MaxTotalBytes {
		return reject(ReasonBounds)
	}
	if !isLowerSHA256(receipt.DocumentsDigest) || !isLowerSHA256(receipt.EdgesDigest) || !isLowerSHA256(receipt.MarkdownDigest) ||
		!isLowerSHA256(receipt.ArtifactsDigest) || !isLowerSHA256(receipt.ProjectionDigest) {
		return reject(ReasonDigest)
	}
	want, err := indexerProjectionDigestV2(receipt, limits)
	if err != nil {
		return err
	}
	if receipt.ProjectionDigest != want {
		return reject(ReasonDigest)
	}
	return nil
}

func indexerProjectionDigestV2(receipt IndexerReceiptV2, limits Limits) (string, error) {
	preimage := struct {
		SchemaVersion    int                    `json:"schema_version"`
		ProjectionSchema int                    `json:"projection_schema"`
		Readiness        ProjectionReadiness    `json:"readiness"`
		Qualifications   []IndexerQualification `json:"qualifications"`
		Counts           ProjectionCountsV2     `json:"counts"`
		DocumentsDigest  string                 `json:"documents_digest"`
		EdgesDigest      string                 `json:"edges_digest"`
		MarkdownDigest   string                 `json:"markdown_digest"`
		ArtifactsDigest  string                 `json:"artifacts_digest"`
	}{
		receipt.SchemaVersion, receipt.ProjectionSchema, receipt.Readiness, receipt.Qualifications, receipt.Counts,
		receipt.DocumentsDigest, receipt.EdgesDigest, receipt.MarkdownDigest, receipt.ArtifactsDigest,
	}
	data, err := marshalCanonical(preimage)
	if err != nil {
		return "", err
	}
	if int64(len(data)) > limits.MaxManifestBytes {
		return "", reject(ReasonBounds)
	}
	return domainHash(indexerProjectionV2Domain, data), nil
}

func validateIndexerBundleV2(
	qualifications []IndexerQualification,
	documents []IndexerDocument,
	edges []IndexerEdge,
	markdown []MarkdownMember,
	artifacts []IndexerArtifact,
	artifactMembers []ArtifactMember,
	limits Limits,
) (ProjectionReadiness, int64, error) {
	readiness, err := validateIndexerBundle(qualifications, documents, edges, markdown, limits)
	if err != nil {
		return "", 0, err
	}
	documentByID := make(map[string]IndexerDocument, len(documents))
	for _, document := range documents {
		documentByID[document.ID] = document
	}
	type attachmentOwner struct {
		parentID      string
		inventoryPath string
		qualified     bool
	}
	ownerEdges := make(map[string]attachmentOwner)
	for _, edge := range edges {
		if edge.Relation != EdgeAttachmentOwner {
			continue
		}
		attachment, attachmentPresent := documentByID[edge.SourceID]
		parent, parentPresent := documentByID[edge.TargetID]
		if edge.TargetID == "" || !attachmentPresent || attachment.Kind != ObjectAttachment ||
			!parentPresent || parent.Service != attachment.Service || (parent.Kind != ObjectIssue && parent.Kind != ObjectPage) {
			return "", 0, reject(ReasonMembership)
		}
		qualified := edge.Confidence == ConfidenceExact && edge.Evidence.Fragment == "attachment-owner"
		legacy := edge.Confidence == ConfidenceReported && edge.Evidence.Fragment == "fields.attachment" &&
			attachment.Service == ServiceJira && parent.Kind == ObjectIssue
		if edge.Direction != DirectionOutbound || edge.Evidence.Kind != EvidenceAttachments || (!qualified && !legacy) {
			return "", 0, reject(ReasonMembership)
		}
		if _, duplicate := ownerEdges[edge.SourceID]; duplicate {
			return "", 0, reject(ReasonMembership)
		}
		ownerEdges[edge.SourceID] = attachmentOwner{
			parentID:      edge.TargetID,
			inventoryPath: edge.Evidence.Path,
			qualified:     qualified,
		}
	}
	memberByID, capturedBytes, err := validateArtifactMembers(artifactMembers, limits)
	if err != nil {
		return "", 0, err
	}
	artifactByID := make(map[string]IndexerArtifact, len(artifacts))
	for _, artifact := range artifacts {
		if err := validateIndexerArtifact(artifact, limits); err != nil {
			return "", 0, err
		}
		attachment, attachmentPresent := documentByID[artifact.DocumentID]
		parent, parentPresent := documentByID[artifact.ParentID]
		owner := ownerEdges[artifact.DocumentID]
		if !attachmentPresent || attachment.Kind != ObjectAttachment || attachment.Service != artifact.Service ||
			!parentPresent || parent.Service != artifact.Service || (parent.Kind != ObjectIssue && parent.Kind != ObjectPage) ||
			owner.parentID != artifact.ParentID {
			return "", 0, reject(ReasonMembership)
		}
		if owner.inventoryPath != artifact.Source.InventoryPath ||
			attachment.Source.Path != artifact.Source.InventoryPath ||
			parent.Source.NativeSHA256 != artifact.Source.ParentNativeSHA256 ||
			parent.Source.MetadataSHA256 != artifact.Source.ParentMetadataSHA256 {
			return "", 0, reject(ReasonLineage)
		}
		// The established v1 attachment lineage uses parent digests for
		// qualified sidecars and the Jira metadata inventory digest for both
		// legacy digest fields. ArtifactSourceLineage carries the missing half.
		if owner.qualified {
			if attachment.Source.NativeSHA256 != artifact.Source.ParentNativeSHA256 ||
				attachment.Source.MetadataSHA256 != artifact.Source.ParentMetadataSHA256 {
				return "", 0, reject(ReasonLineage)
			}
		} else if attachment.Source.NativeSHA256 != artifact.Source.InventorySHA256 ||
			attachment.Source.MetadataSHA256 != artifact.Source.InventorySHA256 ||
			attachment.Source.MetadataSHA256 != artifact.Source.ParentMetadataSHA256 {
			return "", 0, reject(ReasonLineage)
		}
		if _, duplicate := artifactByID[artifact.DocumentID]; duplicate {
			return "", 0, reject(ReasonMembership)
		}
		artifactByID[artifact.DocumentID] = artifact
		member, memberPresent := memberByID[artifact.DocumentID]
		if artifact.Status == ArtifactBodyCaptured {
			if !memberPresent || member.Path != artifact.Path || member.Size != artifact.Size || member.SHA256 != artifact.SHA256 {
				return "", 0, reject(ReasonDigest)
			}
		} else if memberPresent {
			return "", 0, reject(ReasonMembership)
		}
	}
	for id, document := range documentByID {
		if document.Kind == ObjectAttachment {
			if _, present := artifactByID[id]; !present {
				return "", 0, reject(ReasonMembership)
			}
		}
	}
	for id := range ownerEdges {
		if _, present := artifactByID[id]; !present {
			return "", 0, reject(ReasonMembership)
		}
	}
	if len(memberByID) != 0 {
		for id := range memberByID {
			if _, present := artifactByID[id]; !present {
				return "", 0, reject(ReasonMembership)
			}
		}
	}
	return readiness, capturedBytes, nil
}

func validateArtifactMembers(members []ArtifactMember, limits Limits) (map[string]ArtifactMember, int64, error) {
	if members == nil {
		return nil, 0, reject(ReasonFormat)
	}
	if len(members) > limits.MaxMembers {
		return nil, 0, reject(ReasonBounds)
	}
	out := make(map[string]ArtifactMember, len(members))
	paths := make(map[string]struct{}, len(members))
	var total int64
	for _, member := range members {
		if !isLowerSHA256(member.DocumentID) || !isLowerSHA256(member.SHA256) {
			return nil, 0, reject(ReasonDigest)
		}
		if err := validateMemberPath(member.Path, limits); err != nil {
			return nil, 0, err
		}
		if member.Size < 0 || member.Size > limits.MaxMemberBytes || member.Size > limits.MaxTotalBytes-total {
			return nil, 0, reject(ReasonBounds)
		}
		if _, duplicate := out[member.DocumentID]; duplicate {
			return nil, 0, reject(ReasonMembership)
		}
		if _, duplicate := paths[member.Path]; duplicate {
			return nil, 0, reject(ReasonMembership)
		}
		out[member.DocumentID] = member
		paths[member.Path] = struct{}{}
		total += member.Size
	}
	return out, total, nil
}
