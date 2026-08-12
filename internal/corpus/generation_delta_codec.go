package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

const generationDeltaDocumentDomain = "atl.corpus.generation-delta.document.v1"

// BuildGenerationDelta compares canonical document identities and constructs
// a deterministic private delta for one compatible generation transition.
func BuildGenerationDelta(
	predecessorID, predecessorGenerationDigest, predecessorProjectionDigest, successorProjectionDigest string,
	bindings []GenerationDeltaBinding,
	predecessorDocuments, successorDocuments []IndexerDocument,
	limits Limits,
) (GenerationDelta, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return GenerationDelta{}, err
	}
	if err := validateGenerationID(predecessorID); err != nil ||
		!isLowerSHA256(predecessorGenerationDigest) || !isLowerSHA256(predecessorProjectionDigest) ||
		!isLowerSHA256(successorProjectionDigest) {
		return GenerationDelta{}, reject(ReasonDigest)
	}
	bindings = normalizeGenerationDeltaBindings(bindings)
	if err := validateGenerationDeltaBindings(bindings); err != nil {
		return GenerationDelta{}, err
	}
	predecessor, err := generationDeltaDocuments(predecessorDocuments, limits)
	if err != nil {
		return GenerationDelta{}, err
	}
	successor, err := generationDeltaDocuments(successorDocuments, limits)
	if err != nil {
		return GenerationDelta{}, err
	}

	ids := make([]string, 0, len(predecessor)+len(successor))
	seen := make(map[string]struct{}, len(predecessor)+len(successor))
	for id := range predecessor {
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for id := range successor {
		if _, present := seen[id]; !present {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	records := make([]GenerationDeltaRecord, 0, len(ids))
	counts := GenerationDeltaCounts{}
	for _, id := range ids {
		before, beforePresent := predecessor[id]
		after, afterPresent := successor[id]
		record := GenerationDeltaRecord{ID: id}
		switch {
		case !beforePresent && afterPresent:
			record.Service, record.Kind = after.service, after.kind
			record.State, record.SuccessorDigest = GenerationDeltaAdded, after.digest
			counts.Added++
		case beforePresent && !afterPresent:
			record.Service, record.Kind = before.service, before.kind
			record.State, record.PredecessorDigest = GenerationDeltaTombstoned, before.digest
			record.Reason = GenerationDeltaAbsentQualified
			counts.Tombstoned++
		case beforePresent && afterPresent:
			if before.service != after.service || before.kind != after.kind {
				return GenerationDelta{}, reject(ReasonLineage)
			}
			record.Service, record.Kind = before.service, before.kind
			record.PredecessorDigest, record.SuccessorDigest = before.digest, after.digest
			if before.digest == after.digest {
				record.State = GenerationDeltaRetained
				counts.Retained++
			} else {
				record.State = GenerationDeltaChanged
				counts.Changed++
			}
		default:
			return GenerationDelta{}, reject(ReasonMembership)
		}
		records = append(records, record)
	}
	delta := GenerationDelta{
		SchemaVersion:           GenerationDeltaSchemaV1,
		PredecessorGenerationID: predecessorID, PredecessorGenerationDigest: predecessorGenerationDigest,
		PredecessorProjectionDigest: predecessorProjectionDigest, SuccessorProjectionDigest: successorProjectionDigest,
		Bindings: bindings, Records: records, Counts: counts,
	}
	if err := validateGenerationDelta(delta, limits); err != nil {
		return GenerationDelta{}, err
	}
	return delta, nil
}

// CanonicalGenerationDelta returns the exact sealed member bytes.
func CanonicalGenerationDelta(delta GenerationDelta, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if err := validateGenerationDelta(delta, limits); err != nil {
		return nil, err
	}
	data, err := marshalCanonical(delta)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limits.MaxMemberBytes || int64(len(data)) > limits.MaxManifestBytes {
		return nil, reject(ReasonBounds)
	}
	return data, nil
}

// ParseGenerationDelta accepts only exact canonical schema-v1 bytes.
func ParseGenerationDelta(data []byte, limits Limits) (GenerationDelta, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return GenerationDelta{}, err
	}
	if len(data) == 0 || int64(len(data)) > limits.MaxMemberBytes || int64(len(data)) > limits.MaxManifestBytes {
		return GenerationDelta{}, reject(ReasonBounds)
	}
	var delta GenerationDelta
	if err := decodeStrictObject(data, &delta); err != nil {
		return GenerationDelta{}, err
	}
	canonical, err := CanonicalGenerationDelta(delta, limits)
	if err != nil {
		return GenerationDelta{}, err
	}
	if !bytes.Equal(data, canonical) {
		return GenerationDelta{}, reject(ReasonFormat)
	}
	return delta, nil
}

// BuildGenerationDiffArtifact binds the verified final successor digest to the
// exact sealed delta digest without changing sealed generation bytes.
func BuildGenerationDiffArtifact(delta GenerationDelta, successorGenerationDigest, tombstoneDigest string, limits Limits) (GenerationDiffArtifact, error) {
	canonicalDelta, err := CanonicalGenerationDelta(delta, limits)
	if err != nil {
		return GenerationDiffArtifact{}, err
	}
	if !isLowerSHA256(successorGenerationDigest) || !isLowerSHA256(tombstoneDigest) {
		return GenerationDiffArtifact{}, reject(ReasonDigest)
	}
	deltaSum := sha256.Sum256(canonicalDelta)
	if tombstoneDigest != hex.EncodeToString(deltaSum[:]) {
		return GenerationDiffArtifact{}, reject(ReasonDigest)
	}
	artifact := GenerationDiffArtifact{
		SchemaVersion:               GenerationDiffArtifactSchemaV1,
		PredecessorGenerationDigest: delta.PredecessorGenerationDigest,
		SuccessorGenerationDigest:   successorGenerationDigest, TombstoneDigest: tombstoneDigest,
		Reason:   GenerationDeltaAbsentQualified,
		Bindings: append([]GenerationDeltaBinding(nil), delta.Bindings...),
		Records:  append([]GenerationDeltaRecord(nil), delta.Records...), Counts: delta.Counts,
	}
	if _, err := CanonicalGenerationDiffArtifact(artifact, limits); err != nil {
		return GenerationDiffArtifact{}, err
	}
	return artifact, nil
}

func CanonicalGenerationDiffArtifact(artifact GenerationDiffArtifact, limits Limits) ([]byte, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if err := validateGenerationDiffArtifact(artifact, limits); err != nil {
		return nil, err
	}
	data, err := marshalCanonical(artifact)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limits.MaxMemberBytes || int64(len(data)) > limits.MaxManifestBytes {
		return nil, reject(ReasonBounds)
	}
	return data, nil
}

func ParseGenerationDiffArtifact(data []byte, limits Limits) (GenerationDiffArtifact, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return GenerationDiffArtifact{}, err
	}
	if len(data) == 0 || int64(len(data)) > limits.MaxMemberBytes || int64(len(data)) > limits.MaxManifestBytes {
		return GenerationDiffArtifact{}, reject(ReasonBounds)
	}
	var artifact GenerationDiffArtifact
	if err := decodeStrictObject(data, &artifact); err != nil {
		return GenerationDiffArtifact{}, err
	}
	canonical, err := CanonicalGenerationDiffArtifact(artifact, limits)
	if err != nil {
		return GenerationDiffArtifact{}, err
	}
	if !bytes.Equal(data, canonical) {
		return GenerationDiffArtifact{}, reject(ReasonFormat)
	}
	return artifact, nil
}

type generationDeltaDocument struct {
	service Service
	kind    ObjectKind
	digest  string
}

func generationDeltaDocuments(documents []IndexerDocument, limits Limits) (map[string]generationDeltaDocument, error) {
	if documents == nil || len(documents) > limits.MaxMembers {
		return nil, reject(ReasonBounds)
	}
	if _, err := CanonicalIndexerDocuments(documents, limits); err != nil {
		return nil, err
	}
	result := make(map[string]generationDeltaDocument, len(documents))
	for _, document := range documents {
		canonical, err := CanonicalIndexerDocuments([]IndexerDocument{document}, limits)
		if err != nil {
			return nil, err
		}
		if _, duplicate := result[document.ID]; duplicate {
			return nil, reject(ReasonMembership)
		}
		result[document.ID] = generationDeltaDocument{
			service: document.Service, kind: document.Kind,
			digest: domainHash(generationDeltaDocumentDomain, canonical),
		}
	}
	return result, nil
}

func normalizeGenerationDeltaBindings(bindings []GenerationDeltaBinding) []GenerationDeltaBinding {
	result := append([]GenerationDeltaBinding(nil), bindings...)
	sort.Slice(result, func(i, j int) bool { return result[i].Service < result[j].Service })
	return result
}

func validateGenerationDeltaBindings(bindings []GenerationDeltaBinding) error {
	if len(bindings) == 0 || len(bindings) > 2 {
		return reject(ReasonMembership)
	}
	var previous Service
	for index, binding := range bindings {
		if !validQualificationService(binding.Service) || binding.ReceiptSchema != CaptureReceiptSchemaV1 {
			return reject(ReasonSchema)
		}
		if index > 0 && previous >= binding.Service {
			return reject(ReasonMembership)
		}
		previous = binding.Service
		if !isLowerSHA256(binding.ScopeDigest) || !isLowerSHA256(binding.SelectorDigest) || !isLowerSHA256(binding.OptionsDigest) {
			return reject(ReasonDigest)
		}
	}
	return nil
}

func validateGenerationDelta(delta GenerationDelta, limits Limits) error {
	if delta.SchemaVersion != GenerationDeltaSchemaV1 {
		return reject(ReasonSchema)
	}
	if err := validateGenerationID(delta.PredecessorGenerationID); err != nil {
		return err
	}
	for _, digest := range []string{delta.PredecessorGenerationDigest, delta.PredecessorProjectionDigest, delta.SuccessorProjectionDigest} {
		if !isLowerSHA256(digest) {
			return reject(ReasonDigest)
		}
	}
	if err := validateGenerationDeltaBindings(delta.Bindings); err != nil {
		return err
	}
	boundServices := make(map[Service]struct{}, len(delta.Bindings))
	for _, binding := range delta.Bindings {
		boundServices[binding.Service] = struct{}{}
	}
	return validateGenerationDeltaRecords(delta.Records, delta.Counts, boundServices, limits)
}

func validateGenerationDeltaRecords(records []GenerationDeltaRecord, counts GenerationDeltaCounts, boundServices map[Service]struct{}, limits Limits) error {
	if records == nil || len(records) > limits.MaxMembers || counts.Added < 0 || counts.Retained < 0 || counts.Changed < 0 || counts.Tombstoned < 0 ||
		counts.Added+counts.Retained+counts.Changed+counts.Tombstoned != len(records) {
		return reject(ReasonMembership)
	}
	actual := GenerationDeltaCounts{}
	for index, record := range records {
		if !isLowerSHA256(record.ID) || !validQualificationService(record.Service) || !validObjectKind(record.Service, record.Kind) {
			return reject(ReasonType)
		}
		if _, present := boundServices[record.Service]; !present {
			return reject(ReasonMembership)
		}
		if index > 0 && records[index-1].ID >= record.ID {
			return reject(ReasonMembership)
		}
		switch record.State {
		case GenerationDeltaAdded:
			if record.PredecessorDigest != "" || !isLowerSHA256(record.SuccessorDigest) || record.Reason != "" {
				return reject(ReasonLineage)
			}
			actual.Added++
		case GenerationDeltaRetained:
			if !isLowerSHA256(record.PredecessorDigest) || record.PredecessorDigest != record.SuccessorDigest || record.Reason != "" {
				return reject(ReasonLineage)
			}
			actual.Retained++
		case GenerationDeltaChanged:
			if !isLowerSHA256(record.PredecessorDigest) || !isLowerSHA256(record.SuccessorDigest) || record.PredecessorDigest == record.SuccessorDigest || record.Reason != "" {
				return reject(ReasonLineage)
			}
			actual.Changed++
		case GenerationDeltaTombstoned:
			if !isLowerSHA256(record.PredecessorDigest) || record.SuccessorDigest != "" || record.Reason != GenerationDeltaAbsentQualified {
				return reject(ReasonLineage)
			}
			actual.Tombstoned++
		default:
			return reject(ReasonType)
		}
	}
	if actual != counts {
		return reject(ReasonMembership)
	}
	return nil
}

func validateGenerationDiffArtifact(artifact GenerationDiffArtifact, limits Limits) error {
	if artifact.SchemaVersion != GenerationDiffArtifactSchemaV1 || artifact.Reason != GenerationDeltaAbsentQualified {
		return reject(ReasonSchema)
	}
	for _, digest := range []string{artifact.PredecessorGenerationDigest, artifact.SuccessorGenerationDigest, artifact.TombstoneDigest} {
		if !isLowerSHA256(digest) {
			return reject(ReasonDigest)
		}
	}
	if err := validateGenerationDeltaBindings(artifact.Bindings); err != nil {
		return err
	}
	boundServices := make(map[Service]struct{}, len(artifact.Bindings))
	for _, binding := range artifact.Bindings {
		boundServices[binding.Service] = struct{}{}
	}
	return validateGenerationDeltaRecords(artifact.Records, artifact.Counts, boundServices, limits)
}
