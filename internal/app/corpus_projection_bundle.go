package app

import (
	"fmt"
	"sort"

	"github.com/isukharev/atl/internal/corpus"
)

func assembleCorpusProjectionBundle(sources []corpusExportSource, indexed []corpusIndexedSource, builder *corpusProjectionBuilder, limits corpus.Limits) (corpusProjectionBundle, error) {
	qualifications := make([]corpus.IndexerQualification, 0, len(indexed))
	for _, source := range indexed {
		if source.source.capture != nil {
			qualifications = append(qualifications, corpus.IndexerQualification{
				Service: source.source.service, State: corpus.QualificationReady,
				Basis: corpus.QualificationReceipt, ScopeDigest: source.source.capture.ScopeDigest,
				SourceReceiptDigest: source.source.capture.ReceiptDigest,
				Reasons:             []corpus.QualificationReason{},
			})
		} else {
			reasons := []corpus.QualificationReason{corpus.QualificationLegacyMirror}
			if !source.source.snapshot.Reconciled() {
				reasons = append(reasons, corpus.QualificationUnreconciled)
			}
			qualifications = append(qualifications, corpus.IndexerQualification{
				Service: source.source.service, State: corpus.QualificationPartial,
				Basis: corpus.QualificationStructural, ScopeDigest: source.source.snapshot.Fingerprint(),
				Reasons: reasons,
			})
		}
	}
	sort.Slice(qualifications, func(i, j int) bool { return qualifications[i].Service < qualifications[j].Service })
	for _, document := range builder.documents {
		if _, err := corpus.CanonicalIndexerDocuments([]corpus.IndexerDocument{document}, limits); err != nil {
			return corpusProjectionBundle{}, fmt.Errorf("validate %s corpus document: %w", document.Kind, err)
		}
	}
	documentsBytes, err := corpus.CanonicalIndexerDocuments(builder.documents, limits)
	if err != nil {
		return corpusProjectionBundle{}, fmt.Errorf("encode corpus documents: %w", err)
	}
	edgesBytes, err := corpus.CanonicalIndexerEdges(builder.edges, limits)
	if err != nil {
		return corpusProjectionBundle{}, fmt.Errorf("encode corpus edges: %w", err)
	}
	receipt, err := corpus.BuildIndexerReceipt(qualifications, builder.documents, builder.edges, builder.markdown, limits)
	if err != nil {
		return corpusProjectionBundle{}, fmt.Errorf("build corpus projection receipt: %w", err)
	}
	receiptBytes, err := corpus.CanonicalIndexerReceipt(receipt, limits)
	if err != nil {
		return corpusProjectionBundle{}, fmt.Errorf("encode corpus projection receipt: %w", err)
	}
	if err := corpus.VerifyIndexerBundle(receipt, builder.documents, builder.edges, builder.markdown, limits); err != nil {
		return corpusProjectionBundle{}, err
	}

	inventoryService := sources[0].service
	if len(sources) == 2 {
		inventoryService = corpus.ServiceAggregate
	}
	prefix := "projection/" + string(inventoryService) + "/"
	members := []corpusExportMember{
		{spec: corpus.MemberSpec{Service: inventoryService, StableID: corpusDocumentsStableID, Role: corpus.RoleDocument, Path: prefix + "documents.indexer-v1.jsonl"}, data: documentsBytes},
		{spec: corpus.MemberSpec{Service: inventoryService, StableID: corpusEdgesStableID, Role: corpus.RoleEdges, Path: prefix + "edges.indexer-v1.jsonl"}, data: edgesBytes},
		{spec: corpus.MemberSpec{Service: inventoryService, StableID: corpusReceiptStableID, Role: corpus.RoleMetadata, Path: prefix + "receipt.indexer-v1.json"}, data: receiptBytes},
	}
	for _, source := range sources {
		if source.capture == nil {
			continue
		}
		members = append(members, corpusExportMember{
			spec: corpus.MemberSpec{
				Service: source.service, StableID: corpusCaptureStableID, Role: corpus.RoleMetadata,
				Path: "capture/" + string(source.service) + "/receipt.capture-v1.json",
			},
			data: append([]byte(nil), source.captureBytes...),
		})
	}
	for _, document := range builder.documents {
		data, ok := builder.files[document.MarkdownPath]
		if !ok {
			continue
		}
		members = append(members, corpusExportMember{
			spec: corpus.MemberSpec{Service: document.Service, StableID: document.ID, Role: corpus.RoleDocument, Path: document.MarkdownPath},
			data: data,
		})
	}
	sortCorpusExportMembers(members)

	receiptDigest := corpusBytesSHA256(receiptBytes)
	storeQualifications := make([]corpus.Qualification, 0, len(receipt.Qualifications))
	for _, qualification := range receipt.Qualifications {
		var source *corpusExportSource
		for index := range sources {
			if sources[index].service == qualification.Service {
				source = &sources[index]
				break
			}
		}
		if source != nil && source.capture != nil {
			storeQualifications = append(storeQualifications, corpus.Qualification{
				Service: qualification.Service, ReceiptSchema: corpus.CaptureReceiptSchemaV1,
				ScopeDigest: source.capture.ScopeDigest, SelectorDigest: source.capture.SelectorDigest,
				ProjectionDigest: receipt.ProjectionDigest, ReceiptDigest: source.capture.ReceiptDigest,
			})
			continue
		}
		storeQualifications = append(storeQualifications, corpus.Qualification{
			Service:       qualification.Service,
			ReceiptSchema: corpus.IndexerReceiptSchemaV1,
			ScopeDigest:   qualification.ScopeDigest,
			// A structural legacy snapshot has no independent selector receipt.
			// Reusing the exact snapshot scope here is explicit and remains
			// distinguishable through the partial/structural indexer receipt.
			SelectorDigest:   qualification.ScopeDigest,
			ProjectionDigest: receipt.ProjectionDigest,
			ReceiptDigest:    receiptDigest,
		})
	}
	return corpusProjectionBundle{
		receipt: receipt, members: members,
		qualifications: storeQualifications,
	}, nil
}
