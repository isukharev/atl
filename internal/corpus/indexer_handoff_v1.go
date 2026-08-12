package corpus

import (
	"bytes"
)

const IndexerHandoffSchemaV1 = 1

// IndexerHandoff is an explicit owner-private route to the one canonical
// document inventory inside a verified sealed generation. The generation ID,
// member path, and stable identity remain out of ordinary command output.
type IndexerHandoff struct {
	SchemaVersion    int    `json:"schema_version"`
	GenerationID     string `json:"generation_id"`
	GenerationDigest string `json:"generation_digest"`
	ProjectionSchema int    `json:"projection_schema"`
	Documents        Member `json:"documents"`
}

func BuildIndexerHandoff(generationID, generationDigest string, projectionSchema int, documents Member, limits Limits) (IndexerHandoff, error) {
	handoff := IndexerHandoff{
		SchemaVersion: IndexerHandoffSchemaV1, GenerationID: generationID,
		GenerationDigest: generationDigest, ProjectionSchema: projectionSchema, Documents: documents,
	}
	if err := validateIndexerHandoff(handoff, limits); err != nil {
		return IndexerHandoff{}, err
	}
	return handoff, nil
}

func CanonicalIndexerHandoff(handoff IndexerHandoff, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if err := validateIndexerHandoff(handoff, limits); err != nil {
		return nil, err
	}
	data, err := marshalCanonical(handoff)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limits.MaxManifestBytes {
		return nil, reject(ReasonBounds)
	}
	return data, nil
}

func ParseIndexerHandoff(data []byte, limits Limits) (IndexerHandoff, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return IndexerHandoff{}, err
	}
	if len(data) == 0 || int64(len(data)) > limits.MaxManifestBytes {
		return IndexerHandoff{}, reject(ReasonBounds)
	}
	var handoff IndexerHandoff
	if err := decodeStrictObject(data, &handoff); err != nil {
		return IndexerHandoff{}, err
	}
	canonical, err := CanonicalIndexerHandoff(handoff, limits)
	if err != nil {
		return IndexerHandoff{}, err
	}
	if !bytes.Equal(data, canonical) {
		return IndexerHandoff{}, reject(ReasonFormat)
	}
	return handoff, nil
}

func validateIndexerHandoff(handoff IndexerHandoff, limits Limits) error {
	if handoff.SchemaVersion != IndexerHandoffSchemaV1 || handoff.ProjectionSchema != IndexerSchemaV2 {
		return reject(ReasonSchema)
	}
	if err := validateGenerationID(handoff.GenerationID); err != nil {
		return err
	}
	if !isLowerSHA256(handoff.GenerationDigest) {
		return reject(ReasonDigest)
	}
	document := handoff.Documents
	if document.Role != RoleDocument || document.StableID != IndexerDocumentsStableID {
		return reject(ReasonMembership)
	}
	wantPath := "projection/" + string(document.Service) + "/documents.indexer-v1.jsonl"
	if document.Path != wantPath {
		return reject(ReasonPath)
	}
	_, _, err := validateMembers([]Member{document}, limits)
	return err
}
