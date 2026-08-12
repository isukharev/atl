package app

import (
	"bytes"
	"context"

	"github.com/isukharev/atl/internal/corpus"
)

const corpusDeltaStableID = "generation-delta-v1"

type corpusDeltaProjection struct {
	receipt   corpus.IndexerReceiptV2
	documents []corpus.IndexerDocument
	captures  map[corpus.Service]corpus.CaptureReceipt
	bindings  []corpus.GenerationDeltaBinding
}

func deriveCorpusGenerationDelta(ctx context.Context, predecessor *corpus.Generation, bundle corpusProjectionBundle, limits corpus.Limits) (*corpusExportMember, error) {
	if predecessor == nil {
		return nil, nil
	}
	before, qualified, err := loadCorpusDeltaGeneration(ctx, predecessor, limits)
	if err != nil {
		return nil, err
	}
	after, successorQualified, err := loadCorpusDeltaBundle(bundle, limits)
	if err != nil {
		return nil, err
	}
	if !qualified || !successorQualified || !compatibleCorpusDeltaProjections(before, after) {
		return nil, nil
	}
	delta, err := corpus.BuildGenerationDelta(
		predecessor.ID(), predecessor.Receipt().GenerationDigest,
		before.receipt.ProjectionDigest, after.receipt.ProjectionDigest,
		after.bindings, before.documents, after.documents, limits,
	)
	if err != nil {
		return nil, err
	}
	data, err := corpus.CanonicalGenerationDelta(delta, limits)
	if err != nil {
		return nil, err
	}
	inventoryService, err := corpusDeltaInventoryService(after.bindings)
	if err != nil {
		return nil, err
	}
	return &corpusExportMember{
		spec: corpus.MemberSpec{
			Service: inventoryService, StableID: corpusDeltaStableID, Role: corpus.RoleTombstone,
			Path: corpusDeltaMemberPath(inventoryService),
		},
		data: data,
	}, nil
}

func loadCorpusDeltaGeneration(ctx context.Context, generation *corpus.Generation, limits corpus.Limits) (corpusDeltaProjection, bool, error) {
	if generation == nil || generation.Manifest().ProjectionSchema != corpus.IndexerSchemaV2 {
		return corpusDeltaProjection{}, false, nil
	}
	manifest := generation.Manifest()
	inventoryService, qualifiedShape := corpusDeltaQualificationService(manifest.Qualifications)
	if inventoryService == "" {
		return corpusDeltaProjection{}, false, nil
	}
	documentsMember := corpus.Member{
		Service: inventoryService, StableID: corpusDocumentsStableID, Role: corpus.RoleDocument,
		Path: corpusProjectionPrefix(inventoryService) + "documents.indexer-v1.jsonl",
	}
	receiptMember := corpus.Member{
		Service: inventoryService, StableID: corpusReceiptV2StableID, Role: corpus.RoleMetadata,
		Path: corpusProjectionPrefix(inventoryService) + "receipt.indexer-v2.json",
	}
	documentsBytes, err := copyExactCorpusGenerationMember(ctx, generation, documentsMember)
	if err != nil {
		return corpusDeltaProjection{}, false, err
	}
	receiptBytes, err := copyExactCorpusGenerationMember(ctx, generation, receiptMember)
	if err != nil {
		return corpusDeltaProjection{}, false, err
	}
	documents, err := corpus.ParseIndexerDocuments(documentsBytes, limits)
	if err != nil {
		return corpusDeltaProjection{}, false, err
	}
	receipt, err := corpus.ParseIndexerReceiptV2(receiptBytes, limits)
	if err != nil || corpus.VerifyIndexerDocumentsV2(receipt, documents, limits) != nil {
		return corpusDeltaProjection{}, false, corpus.ErrIntegrity
	}
	projection := corpusDeltaProjection{receipt: receipt, documents: documents, captures: map[corpus.Service]corpus.CaptureReceipt{}}
	if !qualifiedShape || receipt.Readiness != corpus.ProjectionReady {
		return projection, false, nil
	}
	bindings := make([]corpus.GenerationDeltaBinding, 0, len(manifest.Qualifications))
	for _, qualification := range manifest.Qualifications {
		captureMember := corpus.Member{
			Service: qualification.Service, StableID: corpusCaptureStableID, Role: corpus.RoleMetadata,
			Path: "capture/" + string(qualification.Service) + "/receipt.capture-v1.json",
		}
		captureBytes, readErr := copyExactCorpusGenerationMember(ctx, generation, captureMember)
		if readErr != nil {
			return corpusDeltaProjection{}, false, readErr
		}
		capture, parseErr := corpus.ParseCaptureReceipt(captureBytes, limits)
		if parseErr != nil || capture.Service != qualification.Service ||
			capture.ScopeDigest != qualification.ScopeDigest || capture.SelectorDigest != qualification.SelectorDigest ||
			capture.ReceiptDigest != qualification.ReceiptDigest || receipt.ProjectionDigest != qualification.ProjectionDigest {
			return corpusDeltaProjection{}, false, corpus.ErrIntegrity
		}
		if !completeCorpusDeltaCapture(capture, limits) || !readyCorpusDeltaIndexerQualification(receipt.Qualifications, capture) {
			return projection, false, nil
		}
		projection.captures[capture.Service] = capture
		bindings = append(bindings, corpus.GenerationDeltaBinding{
			Service: capture.Service, ReceiptSchema: capture.SchemaVersion,
			ScopeDigest: capture.ScopeDigest, SelectorDigest: capture.SelectorDigest, OptionsDigest: capture.OptionsDigest,
		})
	}
	projection.bindings = bindings
	return projection, true, nil
}

func loadCorpusDeltaBundle(bundle corpusProjectionBundle, limits corpus.Limits) (corpusDeltaProjection, bool, error) {
	inventoryService, qualifiedShape := corpusDeltaQualificationService(bundle.qualifications)
	if inventoryService == "" {
		return corpusDeltaProjection{}, false, corpus.ErrIntegrity
	}
	documentsBytes, found := corpusBundleMemberBytes(bundle.members, corpus.MemberSpec{
		Service: inventoryService, StableID: corpusDocumentsStableID, Role: corpus.RoleDocument,
		Path: corpusProjectionPrefix(inventoryService) + "documents.indexer-v1.jsonl",
	})
	if !found {
		return corpusDeltaProjection{}, false, corpus.ErrIntegrity
	}
	receiptBytes, found := corpusBundleMemberBytes(bundle.members, corpus.MemberSpec{
		Service: inventoryService, StableID: corpusReceiptV2StableID, Role: corpus.RoleMetadata,
		Path: corpusProjectionPrefix(inventoryService) + "receipt.indexer-v2.json",
	})
	if !found {
		return corpusDeltaProjection{}, false, corpus.ErrIntegrity
	}
	documents, err := corpus.ParseIndexerDocuments(documentsBytes, limits)
	if err != nil {
		return corpusDeltaProjection{}, false, err
	}
	receipt, err := corpus.ParseIndexerReceiptV2(receiptBytes, limits)
	if err != nil || corpus.VerifyIndexerDocumentsV2(receipt, documents, limits) != nil || receipt.ProjectionDigest != bundle.receipt.ProjectionDigest {
		return corpusDeltaProjection{}, false, corpus.ErrIntegrity
	}
	projection := corpusDeltaProjection{receipt: receipt, documents: documents, captures: map[corpus.Service]corpus.CaptureReceipt{}}
	if !qualifiedShape || receipt.Readiness != corpus.ProjectionReady {
		return projection, false, nil
	}
	bindings := make([]corpus.GenerationDeltaBinding, 0, len(bundle.qualifications))
	for _, qualification := range bundle.qualifications {
		captureBytes, present := corpusBundleMemberBytes(bundle.members, corpus.MemberSpec{
			Service: qualification.Service, StableID: corpusCaptureStableID, Role: corpus.RoleMetadata,
			Path: "capture/" + string(qualification.Service) + "/receipt.capture-v1.json",
		})
		if !present {
			return corpusDeltaProjection{}, false, corpus.ErrIntegrity
		}
		capture, parseErr := corpus.ParseCaptureReceipt(captureBytes, limits)
		if parseErr != nil || capture.Service != qualification.Service ||
			capture.ScopeDigest != qualification.ScopeDigest || capture.SelectorDigest != qualification.SelectorDigest ||
			capture.ReceiptDigest != qualification.ReceiptDigest || receipt.ProjectionDigest != qualification.ProjectionDigest {
			return corpusDeltaProjection{}, false, corpus.ErrIntegrity
		}
		if !completeCorpusDeltaCapture(capture, limits) || !readyCorpusDeltaIndexerQualification(receipt.Qualifications, capture) {
			return projection, false, nil
		}
		projection.captures[capture.Service] = capture
		bindings = append(bindings, corpus.GenerationDeltaBinding{
			Service: capture.Service, ReceiptSchema: capture.SchemaVersion,
			ScopeDigest: capture.ScopeDigest, SelectorDigest: capture.SelectorDigest, OptionsDigest: capture.OptionsDigest,
		})
	}
	projection.bindings = bindings
	return projection, true, nil
}

func verifyCorpusGenerationDelta(ctx context.Context, store *corpus.Store, successor *corpus.Generation, limits corpus.Limits) (corpus.GenerationDelta, error) {
	if store == nil || successor == nil {
		return corpus.GenerationDelta{}, corpus.ErrIntegrity
	}
	manifest := successor.Manifest()
	if manifest.TombstoneDigest == "" || manifest.PredecessorDigest == "" {
		return corpus.GenerationDelta{}, corpus.ErrIntegrity
	}
	inventoryService, _ := corpusDeltaQualificationService(manifest.Qualifications)
	if inventoryService == "" {
		return corpus.GenerationDelta{}, corpus.ErrIntegrity
	}
	deltaBytes, err := copyExactCorpusGenerationMember(ctx, successor, corpus.Member{
		Service: inventoryService, StableID: corpusDeltaStableID, Role: corpus.RoleTombstone,
		Path: corpusDeltaMemberPath(inventoryService),
	})
	if err != nil || corpusBytesSHA256(deltaBytes) != manifest.TombstoneDigest {
		return corpus.GenerationDelta{}, corpus.ErrIntegrity
	}
	delta, err := corpus.ParseGenerationDelta(deltaBytes, limits)
	if err != nil || delta.PredecessorGenerationDigest != manifest.PredecessorDigest {
		return corpus.GenerationDelta{}, corpus.ErrIntegrity
	}
	predecessor, err := store.Verify(ctx, delta.PredecessorGenerationID)
	if err != nil {
		return corpus.GenerationDelta{}, err
	}
	defer func() { _ = predecessor.Close() }()
	if predecessor.Receipt().GenerationDigest != delta.PredecessorGenerationDigest {
		return corpus.GenerationDelta{}, corpus.ErrIntegrity
	}
	before, beforeQualified, err := loadCorpusDeltaGeneration(ctx, predecessor, limits)
	if err != nil {
		return corpus.GenerationDelta{}, err
	}
	after, afterQualified, err := loadCorpusDeltaGeneration(ctx, successor, limits)
	if err != nil {
		return corpus.GenerationDelta{}, err
	}
	if !beforeQualified || !afterQualified || !compatibleCorpusDeltaProjections(before, after) ||
		before.receipt.ProjectionDigest != delta.PredecessorProjectionDigest || after.receipt.ProjectionDigest != delta.SuccessorProjectionDigest {
		return corpus.GenerationDelta{}, corpus.ErrIntegrity
	}
	want, err := corpus.BuildGenerationDelta(
		predecessor.ID(), predecessor.Receipt().GenerationDigest,
		before.receipt.ProjectionDigest, after.receipt.ProjectionDigest,
		after.bindings, before.documents, after.documents, limits,
	)
	if err != nil {
		return corpus.GenerationDelta{}, err
	}
	wantBytes, err := corpus.CanonicalGenerationDelta(want, limits)
	if err != nil || !bytes.Equal(wantBytes, deltaBytes) {
		return corpus.GenerationDelta{}, corpus.ErrIntegrity
	}
	return delta, nil
}

func verifyCorpusGenerationTombstoneState(ctx context.Context, store *corpus.Store, generation *corpus.Generation, limits corpus.Limits) error {
	if store == nil || generation == nil {
		return corpus.ErrIntegrity
	}
	manifest := generation.Manifest()
	tombstones := 0
	for _, member := range manifest.Members {
		if member.Role == corpus.RoleTombstone {
			tombstones++
		}
	}
	if manifest.TombstoneDigest == "" {
		if tombstones == 0 {
			return nil
		}
		return corpus.ErrIntegrity
	}
	if tombstones != 1 {
		return corpus.ErrIntegrity
	}
	_, err := verifyCorpusGenerationDelta(ctx, store, generation, limits)
	return err
}

func copyExactCorpusGenerationMember(ctx context.Context, generation *corpus.Generation, expected corpus.Member) ([]byte, error) {
	manifest := generation.Manifest()
	matches := 0
	for _, member := range manifest.Members {
		if member.Service == expected.Service && member.StableID == expected.StableID && member.Role == expected.Role {
			matches++
			if member.Path != expected.Path {
				return nil, corpus.ErrIntegrity
			}
		}
	}
	if matches != 1 {
		return nil, corpus.ErrIntegrity
	}
	var output bytes.Buffer
	if _, err := generation.CopyMember(ctx, expected.Service, expected.StableID, expected.Role, &output); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func corpusBundleMemberBytes(members []corpusExportMember, expected corpus.MemberSpec) ([]byte, bool) {
	var result []byte
	matches := 0
	for _, member := range members {
		if member.spec.Service == expected.Service && member.spec.StableID == expected.StableID && member.spec.Role == expected.Role {
			matches++
			if member.spec.Path != expected.Path {
				return nil, false
			}
			result = member.data
		}
	}
	return result, matches == 1
}

func corpusDeltaQualificationService(qualifications []corpus.Qualification) (corpus.Service, bool) {
	if len(qualifications) == 0 || len(qualifications) > 2 {
		return "", false
	}
	inventoryService := qualifications[0].Service
	if len(qualifications) == 2 {
		if qualifications[0].Service != corpus.ServiceConfluence || qualifications[1].Service != corpus.ServiceJira {
			return "", false
		}
		inventoryService = corpus.ServiceAggregate
	}
	qualified := true
	for _, qualification := range qualifications {
		if qualification.ReceiptSchema != corpus.CaptureReceiptSchemaV1 {
			qualified = false
		}
	}
	return inventoryService, qualified
}

func corpusDeltaInventoryService(bindings []corpus.GenerationDeltaBinding) (corpus.Service, error) {
	if len(bindings) == 1 {
		return bindings[0].Service, nil
	}
	if len(bindings) == 2 && bindings[0].Service == corpus.ServiceConfluence && bindings[1].Service == corpus.ServiceJira {
		return corpus.ServiceAggregate, nil
	}
	return "", corpus.ErrIntegrity
}

func corpusProjectionPrefix(service corpus.Service) string {
	return "projection/" + string(service) + "/"
}

func corpusDeltaMemberPath(service corpus.Service) string {
	return corpusProjectionPrefix(service) + "generation-delta.v1.json"
}

func completeCorpusDeltaCapture(receipt corpus.CaptureReceipt, limits corpus.Limits) bool {
	if corpus.VerifyCaptureReceipt(receipt, limits) != nil || receipt.Completed != receipt.Total {
		return false
	}
	for _, dimension := range receipt.Dimensions {
		if dimension.State == corpus.CapturePartial {
			return false
		}
	}
	return true
}

func readyCorpusDeltaIndexerQualification(qualifications []corpus.IndexerQualification, capture corpus.CaptureReceipt) bool {
	for _, qualification := range qualifications {
		if qualification.Service != capture.Service {
			continue
		}
		return qualification.State == corpus.QualificationReady && qualification.Basis == corpus.QualificationReceipt &&
			qualification.ScopeDigest == capture.ScopeDigest && qualification.SourceReceiptDigest == capture.ReceiptDigest &&
			len(qualification.Reasons) == 0
	}
	return false
}

func compatibleCorpusDeltaProjections(before, after corpusDeltaProjection) bool {
	if len(before.bindings) == 0 || len(before.bindings) != len(after.bindings) || len(before.captures) != len(after.captures) {
		return false
	}
	for index := range before.bindings {
		if before.bindings[index] != after.bindings[index] {
			return false
		}
		left, leftOK := before.captures[before.bindings[index].Service]
		right, rightOK := after.captures[after.bindings[index].Service]
		if !leftOK || !rightOK || len(left.Dimensions) != len(right.Dimensions) {
			return false
		}
		for dimension := range left.Dimensions {
			if left.Dimensions[dimension] != right.Dimensions[dimension] {
				return false
			}
		}
	}
	return true
}
