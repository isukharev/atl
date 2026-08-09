package corpus

import (
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/backendid"
)

func validOriginDigest(value string) bool {
	return len(value) == len(backendid.Prefix)+64 && strings.HasPrefix(value, backendid.Prefix) && isLowerSHA256(value[len(backendid.Prefix):])
}

func validateIndexerReceipt(receipt IndexerReceipt, limits Limits) error {
	if receipt.SchemaVersion != IndexerReceiptSchemaV1 || receipt.ProjectionSchema != IndexerSchemaV1 {
		return reject(ReasonSchema)
	}
	readiness, _, err := validateIndexerQualifications(receipt.Qualifications)
	if err != nil {
		return err
	}
	if receipt.Readiness != readiness {
		return reject(ReasonMembership)
	}
	if receipt.Counts.Documents < 0 || receipt.Counts.Documents > limits.MaxMembers ||
		receipt.Counts.Edges < 0 || receipt.Counts.Edges > limits.MaxMembers ||
		receipt.Counts.MarkdownFiles < 0 || receipt.Counts.MarkdownFiles > limits.MaxMembers ||
		receipt.Counts.MarkdownBytes < 0 || receipt.Counts.MarkdownBytes > limits.MaxTotalBytes {
		return reject(ReasonBounds)
	}
	if !isLowerSHA256(receipt.DocumentsDigest) || !isLowerSHA256(receipt.EdgesDigest) ||
		!isLowerSHA256(receipt.MarkdownDigest) || !isLowerSHA256(receipt.ProjectionDigest) {
		return reject(ReasonDigest)
	}
	want, err := indexerProjectionDigest(receipt, limits)
	if err != nil {
		return err
	}
	if receipt.ProjectionDigest != want {
		return reject(ReasonDigest)
	}
	return nil
}

func indexerProjectionDigest(receipt IndexerReceipt, limits Limits) (string, error) {
	preimage := struct {
		SchemaVersion    int                    `json:"schema_version"`
		ProjectionSchema int                    `json:"projection_schema"`
		Readiness        ProjectionReadiness    `json:"readiness"`
		Qualifications   []IndexerQualification `json:"qualifications"`
		Counts           ProjectionCounts       `json:"counts"`
		DocumentsDigest  string                 `json:"documents_digest"`
		EdgesDigest      string                 `json:"edges_digest"`
		MarkdownDigest   string                 `json:"markdown_digest"`
	}{receipt.SchemaVersion, receipt.ProjectionSchema, receipt.Readiness, receipt.Qualifications,
		receipt.Counts, receipt.DocumentsDigest, receipt.EdgesDigest, receipt.MarkdownDigest}
	data, err := marshalCanonical(preimage)
	if err != nil {
		return "", err
	}
	if int64(len(data)) > limits.MaxManifestBytes {
		return "", reject(ReasonBounds)
	}
	return domainHash(indexerProjectionDomain, data), nil
}

func validateProviderID(value string) error {
	if len(value) == 0 || len(value) > 64 || value[0] == '0' {
		return reject(ReasonType)
	}
	for i := range value {
		if value[i] < '0' || value[i] > '9' {
			return reject(ReasonType)
		}
	}
	return nil
}

func validateBoundedPlain(value string, empty bool) error {
	if value == "" && empty {
		return nil
	}
	if value == "" || len(value) > maxIndexerFieldBytes {
		return reject(ReasonBounds)
	}
	if !utf8.ValidString(value) || containsControl(value) {
		return reject(ReasonType)
	}
	return nil
}

func validateProviderReference(value string) error {
	if err := validateBoundedPlain(value, false); err != nil {
		return err
	}
	lower := strings.ToLower(value)
	if strings.TrimSpace(value) != value || strings.Contains(value, "://") || strings.HasPrefix(value, "//") ||
		strings.HasPrefix(lower, "http:") || strings.HasPrefix(lower, "https:") ||
		strings.HasPrefix(lower, "mailto:") || strings.HasPrefix(lower, "file:") ||
		strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "javascript:") ||
		strings.HasPrefix(lower, "vbscript:") {
		return reject(ReasonType)
	}
	return nil
}

func hasEvidenceReason(reasons []EvidenceReason, wanted EvidenceReason) bool {
	for _, reason := range reasons {
		if reason == wanted {
			return true
		}
	}
	return false
}

func validObjectKind(service Service, kind ObjectKind) bool {
	switch service {
	case ServiceConfluence:
		return kind == ObjectPage || kind == ObjectComment || kind == ObjectAttachment
	case ServiceJira:
		return kind == ObjectIssue || kind == ObjectComment || kind == ObjectAttachment
	default:
		return false
	}
}

func validVisibility(value Visibility) bool {
	return value == VisibilityRestricted || value == VisibilityUnrestricted || value == VisibilityUnknown
}

func validRenderStatus(value RenderStatus) bool {
	return value == RenderRendered || value == RenderEmpty || value == RenderFailed || value == RenderUnsupported
}

func validEvidenceKind(value EvidenceKind) bool {
	switch value {
	case EvidenceMetadata, EvidenceBody, EvidenceHierarchy, EvidenceRelations,
		EvidenceComments, EvidenceAttachments, EvidenceVisibility:
		return true
	default:
		return false
	}
}

func validEvidenceStatus(value EvidenceStatus) bool {
	switch value {
	case EvidenceComplete, EvidencePartial, EvidenceForbidden, EvidenceUnavailable,
		EvidenceNotRequested, EvidenceUnsupported:
		return true
	default:
		return false
	}
}

func validEvidenceReason(value EvidenceReason) bool {
	switch value {
	case EvidenceTruncated, EvidenceMissing, EvidenceCorrupt, EvidenceRestrictedReason,
		EvidenceLegacyUnqualified, EvidenceRenderFailed, EvidenceUnresolved,
		EvidenceUnsupportedReason:
		return true
	default:
		return false
	}
}

func validEdgeRelation(value EdgeRelation) bool {
	switch value {
	case EdgeParent, EdgeContains, EdgeReferences, EdgeJiraIssueLink,
		EdgeCommentOwner, EdgeCommentReply, EdgeAttachmentOwner:
		return true
	default:
		return false
	}
}

func validDirection(value EdgeDirection) bool {
	return value == DirectionOutbound || value == DirectionInbound || value == DirectionUndirected || value == DirectionUnknown
}

func validConfidence(value EdgeConfidence) bool {
	return value == ConfidenceExact || value == ConfidenceReported || value == ConfidenceStructural
}

func validQualificationState(value QualificationState) bool {
	return value == QualificationReady || value == QualificationPartial || value == QualificationUnavailable
}

func validQualificationBasis(value QualificationBasis) bool {
	return value == QualificationReceipt || value == QualificationStructural
}

func validQualificationReason(value QualificationReason) bool {
	switch value {
	case QualificationLegacyMirror, QualificationIncompletePull, QualificationUnreconciled,
		QualificationMissingBinding, QualificationCorruptEvidence, QualificationUnsupportedSource:
		return true
	default:
		return false
	}
}

func bytesToLowerHex(value []byte) string {
	const alphabet = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for i, current := range value {
		encoded[i*2] = alphabet[current>>4]
		encoded[i*2+1] = alphabet[current&0x0f]
	}
	return string(encoded)
}
