package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/fragment"
	"github.com/isukharev/atl/internal/mirror"
)

func corpusConfluenceVisibility(restricted *bool) (corpus.Visibility, corpus.Evidence) {
	if restricted == nil {
		return corpus.VisibilityUnknown, corpusNotRequested(corpus.EvidenceVisibility)
	}
	if *restricted {
		return corpus.VisibilityRestricted, corpusComplete(corpus.EvidenceVisibility, 1)
	}
	return corpus.VisibilityUnrestricted, corpusComplete(corpus.EvidenceVisibility, 1)
}

func corpusJiraVisibility(fields map[string]any) (corpus.Visibility, corpus.Evidence) {
	security, present := fields["security"]
	if !present {
		return corpus.VisibilityUnknown, corpusNotRequested(corpus.EvidenceVisibility)
	}
	if security == nil {
		// No issue-security level does not prove visibility outside the source
		// authorization boundary.
		return corpus.VisibilityUnknown, corpusUnavailable(corpus.EvidenceVisibility, corpus.EvidenceLegacyUnqualified)
	}
	return corpus.VisibilityRestricted, corpusComplete(corpus.EvidenceVisibility, 1)
}

func (builder *corpusProjectionBuilder) projectConfluenceComments(
	source corpusExportSource,
	owner corpusIndexedItem,
	item mirror.CorpusSnapshotItem,
	_ corpus.SourceLineage,
	visibility corpus.Visibility,
	visibilityEvidence corpus.Evidence,
) (corpus.Evidence, error) {
	if corpusCaptureDimensionNotRequested(source, corpus.CaptureComments) {
		return corpusNotRequested(corpus.EvidenceComments), nil
	}
	var metadata mirror.Meta
	if err := json.Unmarshal(item.Metadata.Data, &metadata); err != nil {
		return corpus.Evidence{}, err
	}
	sidecar, present := corpusAuxiliaryWithSuffix(item.Auxiliaries, ".comments.json")
	if !present {
		if metadata.CommentsPulled {
			return corpusUnavailable(corpus.EvidenceComments, corpus.EvidenceMissing), nil
		}
		return corpusNotRequested(corpus.EvidenceComments), nil
	}
	if !metadata.CommentsPulled {
		return corpus.Evidence{}, fmt.Errorf("confluence comment sidecar is not bound by metadata")
	}
	decoded, err := mirror.DecodeConfluenceCommentsSidecar(sidecar.Data)
	if err != nil {
		return corpus.Evidence{}, err
	}
	commentLineage := corpus.SourceLineage{Path: sidecar.Path, NativeSHA256: sidecar.SHA256, MetadataSHA256: item.Metadata.SHA256}
	count := 0
	switch decoded.Format {
	case mirror.ConfluenceCommentsSidecarFormatV2:
		if decoded.V2 == nil || decoded.V2.PageID != owner.providerID || decoded.V2.PageVersion != item.Version {
			return corpus.Evidence{}, fmt.Errorf("confluence comment sidecar is misbound")
		}
		commentTargets := make(map[string]string, len(decoded.V2.Comments))
		for _, comment := range decoded.V2.Comments {
			stableID, idErr := corpus.StableObjectID(source.snapshot.OriginSHA256(), corpus.ServiceConfluence, corpus.ObjectComment, comment.ID)
			if idErr != nil {
				return corpus.Evidence{}, idErr
			}
			commentTargets[comment.ID] = stableID
		}
		for _, comment := range decoded.V2.Comments {
			if err := builder.addConfluenceComment(source, owner, sidecar.Path, commentLineage, visibility, visibilityEvidence,
				commentTargets, comment.ID, comment.ParentID, comment.Version, comment.UpdatedAt, comment.BodyStorage, comment.Body); err != nil {
				return corpus.Evidence{}, err
			}
			count++
		}
		if decoded.V2.Complete {
			return corpusComplete(corpus.EvidenceComments, count), nil
		}
		for _, reason := range decoded.V2.PartialReasons {
			if reason == domain.ConfluenceCommentPartialForbidden {
				return corpusForbidden(corpus.EvidenceComments), nil
			}
		}
		reasons := []corpus.EvidenceReason{corpus.EvidenceUnresolved}
		for _, reason := range decoded.V2.PartialReasons {
			if strings.Contains(strings.ToLower(reason), "trunc") || strings.Contains(strings.ToLower(reason), "limit") {
				reasons = append(reasons, corpus.EvidenceTruncated)
				break
			}
		}
		return corpusPartial(corpus.EvidenceComments, count, reasons...), nil
	case mirror.ConfluenceCommentsSidecarFormatLegacy:
		for _, comment := range decoded.Legacy {
			if err := builder.addConfluenceComment(source, owner, sidecar.Path, commentLineage, visibility, visibilityEvidence,
				nil, comment.ID, nil, 0, comment.Created, comment.BodyStorage, comment.Body); err != nil {
				return corpus.Evidence{}, err
			}
			count++
		}
		return corpusPartial(corpus.EvidenceComments, count, corpus.EvidenceLegacyUnqualified), nil
	default:
		return corpus.Evidence{}, fmt.Errorf("unsupported Confluence comment sidecar")
	}
}

func (builder *corpusProjectionBuilder) addConfluenceComment(
	source corpusExportSource,
	owner corpusIndexedItem,
	evidencePath string,
	lineage corpus.SourceLineage,
	visibility corpus.Visibility,
	visibilityEvidence corpus.Evidence,
	commentTargets map[string]string,
	providerID string,
	parentID *string,
	version int,
	updated, storage, fallback string,
) error {
	stableID, err := corpus.StableObjectID(source.snapshot.OriginSHA256(), corpus.ServiceConfluence, corpus.ObjectComment, providerID)
	if err != nil {
		return err
	}
	markdownPath := corpusMarkdownPath(corpus.ServiceConfluence, stableID)
	text, status, bodyEvidence := corpusRenderConfluenceComment(storage, fallback, builder.linkResolver(corpusIndexedItem{
		stableID: stableID, markdownPath: markdownPath, container: owner.container,
	}))
	relationCount := 1
	if err := builder.addEdge(corpus.IndexerEdge{
		SchemaVersion: corpus.IndexerSchemaV1, SourceID: stableID, Relation: corpus.EdgeCommentOwner,
		Direction: corpus.DirectionOutbound, TargetID: owner.stableID,
		Confidence: corpus.ConfidenceExact, Evidence: corpus.EdgeEvidence{Kind: corpus.EvidenceComments, Path: evidencePath, Fragment: "comment-owner"},
	}); err != nil {
		return err
	}
	if parentID != nil && strings.TrimSpace(*parentID) != "" {
		parentProviderID := strings.TrimSpace(*parentID)
		parentStableID := commentTargets[parentProviderID]
		if err := builder.addEdge(corpus.IndexerEdge{
			SchemaVersion: corpus.IndexerSchemaV1, SourceID: stableID, Relation: corpus.EdgeCommentReply,
			Direction: corpus.DirectionOutbound, TargetID: parentStableID,
			Unresolved: corpusReferenceIfEmpty(parentStableID, corpus.ServiceConfluence, corpus.ObjectComment, parentProviderID),
			Confidence: corpus.ConfidenceExact, Evidence: corpus.EdgeEvidence{Kind: corpus.EvidenceComments, Path: evidencePath, Fragment: "comment-reply"},
		}); err != nil {
			return err
		}
		relationCount++
	}
	versionText := ""
	if version > 0 {
		versionText = fmt.Sprint(version)
	}
	return builder.addDocument(corpus.IndexerDocument{
		SchemaVersion: corpus.IndexerSchemaV1, ID: stableID, Service: corpus.ServiceConfluence, Kind: corpus.ObjectComment,
		Container: owner.container, Version: versionText, Updated: corpusCanonicalTimestamp(updated), Labels: []string{}, Source: lineage,
		Text: text, RenderStatus: status, MarkdownPath: markdownPath, Visibility: visibility,
		Evidence: corpusEvidenceSet(corpusUnsupported(corpus.EvidenceAttachments), bodyEvidence, corpusUnsupported(corpus.EvidenceComments),
			corpusComplete(corpus.EvidenceHierarchy, 1), corpusComplete(corpus.EvidenceMetadata, 1),
			corpusComplete(corpus.EvidenceRelations, relationCount), visibilityEvidence),
	})
}

func corpusRenderConfluenceComment(storage, fallback string, resolver mirror.MarkdownLinkResolver) (string, corpus.RenderStatus, corpus.Evidence) {
	if storage == "" {
		if !utf8.ValidString(fallback) {
			return "", corpus.RenderFailed, corpusUnavailable(corpus.EvidenceBody, corpus.EvidenceRenderFailed)
		}
		status := corpus.RenderRendered
		if fallback == "" {
			status = corpus.RenderEmpty
		}
		return fallback, status, corpusComplete(corpus.EvidenceBody, 1)
	}
	root, err := csf.Parse([]byte(storage))
	if err != nil {
		return "", corpus.RenderFailed, corpusUnavailable(corpus.EvidenceBody, corpus.EvidenceRenderFailed)
	}
	text := string(mirror.RenderMarkdownResolved(root, fragment.Extract(root), resolver))
	status := corpus.RenderRendered
	if text == "" {
		status = corpus.RenderEmpty
	}
	return text, status, corpusComplete(corpus.EvidenceBody, 1)
}

func (builder *corpusProjectionBuilder) projectJiraComments(
	source corpusExportSource,
	owner corpusIndexedItem,
	item mirror.CorpusSnapshotItem,
	fields map[string]any,
	visibility corpus.Visibility,
	visibilityEvidence corpus.Evidence,
) (corpus.Evidence, error) {
	if corpusCaptureDimensionNotRequested(source, corpus.CaptureComments) {
		return corpusNotRequested(corpus.EvidenceComments), nil
	}
	if sidecar, present := corpusAuxiliaryWithSuffix(item.Auxiliaries, ".comments.json"); present {
		return builder.projectQualifiedJiraComments(source, owner, item, sidecar, visibility, visibilityEvidence)
	}
	raw, present := fields["comment"]
	if !present {
		return corpusNotRequested(corpus.EvidenceComments), nil
	}
	container, ok := raw.(map[string]any)
	if !ok {
		return corpusUnavailable(corpus.EvidenceComments, corpus.EvidenceCorrupt), nil
	}
	rawComments, ok := container["comments"].([]any)
	if !ok {
		return corpusUnavailable(corpus.EvidenceComments, corpus.EvidenceCorrupt), nil
	}
	commentTargets := make(map[string]string, len(rawComments))
	lineage := corpus.SourceLineage{Path: item.Metadata.Path, NativeSHA256: item.Metadata.SHA256, MetadataSHA256: item.Metadata.SHA256}
	for _, rawComment := range rawComments {
		comment, ok := rawComment.(map[string]any)
		if !ok {
			return corpus.Evidence{}, fmt.Errorf("jira comment evidence is malformed")
		}
		providerID := corpusStringValue(comment["id"])
		stableID, err := corpus.StableObjectID(source.snapshot.OriginSHA256(), corpus.ServiceJira, corpus.ObjectComment, providerID)
		if err != nil {
			return corpus.Evidence{}, err
		}
		commentTargets[providerID] = stableID
	}
	for _, rawComment := range rawComments {
		comment, ok := rawComment.(map[string]any)
		if !ok {
			return corpus.Evidence{}, fmt.Errorf("jira comment evidence is malformed")
		}
		providerID := corpusStringValue(comment["id"])
		stableID, err := corpus.StableObjectID(source.snapshot.OriginSHA256(), corpus.ServiceJira, corpus.ObjectComment, providerID)
		if err != nil {
			return corpus.Evidence{}, err
		}
		markdownPath := corpusMarkdownPath(corpus.ServiceJira, stableID)
		text, status, bodyEvidence := corpusRenderJiraWiki([]byte(corpusStringValue(comment["body"])), builder.jiraLinkResolver(corpusIndexedItem{
			stableID: stableID, markdownPath: markdownPath, container: owner.container,
		}))
		relationCount := 1
		if err := builder.addEdge(corpus.IndexerEdge{
			SchemaVersion: corpus.IndexerSchemaV1, SourceID: stableID, Relation: corpus.EdgeCommentOwner,
			Direction: corpus.DirectionOutbound, TargetID: owner.stableID,
			Confidence: corpus.ConfidenceReported, Evidence: corpus.EdgeEvidence{Kind: corpus.EvidenceComments, Path: item.Metadata.Path, Fragment: "fields.comment"},
		}); err != nil {
			return corpus.Evidence{}, err
		}
		if parent := corpusStringValue(comment["parentId"]); parent != "" {
			parentStableID := commentTargets[parent]
			if err := builder.addEdge(corpus.IndexerEdge{
				SchemaVersion: corpus.IndexerSchemaV1, SourceID: stableID, Relation: corpus.EdgeCommentReply,
				Direction: corpus.DirectionOutbound, TargetID: parentStableID,
				Unresolved: corpusReferenceIfEmpty(parentStableID, corpus.ServiceJira, corpus.ObjectComment, parent),
				Confidence: corpus.ConfidenceReported, Evidence: corpus.EdgeEvidence{Kind: corpus.EvidenceComments, Path: item.Metadata.Path, Fragment: "fields.comment"},
			}); err != nil {
				return corpus.Evidence{}, err
			}
			relationCount++
		}
		updated := corpusStringValue(comment["updated"])
		if updated == "" {
			updated = corpusStringValue(comment["created"])
		}
		if err := builder.addDocument(corpus.IndexerDocument{
			SchemaVersion: corpus.IndexerSchemaV1, ID: stableID, Service: corpus.ServiceJira, Kind: corpus.ObjectComment,
			Container: owner.container, Updated: corpusCanonicalTimestamp(updated), Labels: []string{}, Source: lineage,
			Text: text, RenderStatus: status, MarkdownPath: markdownPath, Visibility: visibility,
			Evidence: corpusEvidenceSet(corpusUnsupported(corpus.EvidenceAttachments), bodyEvidence, corpusUnsupported(corpus.EvidenceComments),
				corpusComplete(corpus.EvidenceHierarchy, 1), corpusComplete(corpus.EvidenceMetadata, 1),
				corpusComplete(corpus.EvidenceRelations, relationCount), visibilityEvidence),
		}); err != nil {
			return corpus.Evidence{}, err
		}
	}
	total, totalOK := corpusIntegerValue(container["total"])
	start, startOK := corpusIntegerValue(container["startAt"])
	if totalOK && startOK && start == 0 && total == len(rawComments) {
		return corpusComplete(corpus.EvidenceComments, total), nil
	}
	if totalOK && total < len(rawComments) {
		return corpus.Evidence{}, fmt.Errorf("jira comment total is inconsistent")
	}
	reasons := []corpus.EvidenceReason{corpus.EvidenceLegacyUnqualified}
	if totalOK && total > len(rawComments) {
		reasons = append(reasons, corpus.EvidenceTruncated)
	}
	return corpusPartial(corpus.EvidenceComments, len(rawComments), reasons...), nil
}

func (builder *corpusProjectionBuilder) projectQualifiedJiraComments(
	source corpusExportSource,
	owner corpusIndexedItem,
	item mirror.CorpusSnapshotItem,
	sidecar mirror.CorpusSnapshotFile,
	visibility corpus.Visibility,
	visibilityEvidence corpus.Evidence,
) (corpus.Evidence, error) {
	decoded, err := mirror.DecodeJiraCommentsSidecarV1(sidecar.Data)
	if err != nil || decoded.OriginSHA256 != source.snapshot.OriginSHA256() || decoded.ParentID != item.ProviderID ||
		decoded.ParentRevision != corpusJiraSnapshotRevision(item.Metadata.Data) || decoded.NativeSHA256 != item.Native.SHA256 ||
		decoded.MetadataSHA256 != item.Metadata.SHA256 {
		return corpus.Evidence{}, fmt.Errorf("qualified Jira comment sidecar is misbound")
	}
	lineage := corpus.SourceLineage{Path: sidecar.Path, NativeSHA256: item.Native.SHA256, MetadataSHA256: item.Metadata.SHA256}
	targets := make(map[string]string, len(decoded.Comments))
	for _, comment := range decoded.Comments {
		stableID, idErr := corpus.StableObjectID(source.snapshot.OriginSHA256(), corpus.ServiceJira, corpus.ObjectComment, comment.ID)
		if idErr != nil {
			return corpus.Evidence{}, idErr
		}
		targets[comment.ID] = stableID
	}
	for _, comment := range decoded.Comments {
		stableID := targets[comment.ID]
		markdownPath := corpusMarkdownPath(corpus.ServiceJira, stableID)
		text, status, bodyEvidence := corpusRenderJiraWiki([]byte(comment.Body), builder.jiraLinkResolver(corpusIndexedItem{
			stableID: stableID, markdownPath: markdownPath, container: owner.container,
		}))
		relationCount := 1
		if err := builder.addEdge(corpus.IndexerEdge{
			SchemaVersion: corpus.IndexerSchemaV1, SourceID: stableID, Relation: corpus.EdgeCommentOwner,
			Direction: corpus.DirectionOutbound, TargetID: owner.stableID,
			Confidence: corpus.ConfidenceExact, Evidence: corpus.EdgeEvidence{Kind: corpus.EvidenceComments, Path: sidecar.Path, Fragment: "comment-owner"},
		}); err != nil {
			return corpus.Evidence{}, err
		}
		if comment.ParentID != "" {
			parentStableID := targets[comment.ParentID]
			if err := builder.addEdge(corpus.IndexerEdge{
				SchemaVersion: corpus.IndexerSchemaV1, SourceID: stableID, Relation: corpus.EdgeCommentReply,
				Direction: corpus.DirectionOutbound, TargetID: parentStableID,
				Unresolved: corpusReferenceIfEmpty(parentStableID, corpus.ServiceJira, corpus.ObjectComment, comment.ParentID),
				Confidence: corpus.ConfidenceExact, Evidence: corpus.EdgeEvidence{Kind: corpus.EvidenceComments, Path: sidecar.Path, Fragment: "comment-reply"},
			}); err != nil {
				return corpus.Evidence{}, err
			}
			relationCount++
		}
		updated := comment.UpdatedAt
		if updated == "" {
			updated = comment.CreatedAt
		}
		if err := builder.addDocument(corpus.IndexerDocument{
			SchemaVersion: corpus.IndexerSchemaV1, ID: stableID, Service: corpus.ServiceJira, Kind: corpus.ObjectComment,
			Container: owner.container, Updated: corpusCanonicalTimestamp(updated), Labels: []string{}, Source: lineage,
			Text: text, RenderStatus: status, MarkdownPath: markdownPath, Visibility: visibility,
			Evidence: corpusEvidenceSet(corpusUnsupported(corpus.EvidenceAttachments), bodyEvidence, corpusUnsupported(corpus.EvidenceComments),
				corpusComplete(corpus.EvidenceHierarchy, 1), corpusComplete(corpus.EvidenceMetadata, 1),
				corpusComplete(corpus.EvidenceRelations, relationCount), visibilityEvidence),
		}); err != nil {
			return corpus.Evidence{}, err
		}
	}
	if decoded.Complete {
		return corpusComplete(corpus.EvidenceComments, decoded.Count), nil
	}
	switch decoded.PartialReason {
	case mirror.JiraCommentsPartialForbidden:
		return corpusForbidden(corpus.EvidenceComments), nil
	case mirror.JiraCommentsPartialUnsupported:
		return corpusUnsupported(corpus.EvidenceComments), nil
	default:
		return corpusPartial(corpus.EvidenceComments, decoded.Count, corpus.EvidenceTruncated), nil
	}
}

func (builder *corpusProjectionBuilder) projectJiraAttachments(
	source corpusExportSource,
	owner corpusIndexedItem,
	item mirror.CorpusSnapshotItem,
	fields map[string]any,
	visibility corpus.Visibility,
	visibilityEvidence corpus.Evidence,
) (corpus.Evidence, error) {
	if corpusCaptureDimensionNotRequested(source, corpus.CaptureAttachments) {
		return corpusNotRequested(corpus.EvidenceAttachments), nil
	}
	if sidecar, present := corpusAuxiliaryWithSuffix(item.Auxiliaries, ".attachments.json"); present {
		return builder.projectQualifiedAttachments(source, owner, item, sidecar, visibility, visibilityEvidence)
	}
	raw, present := fields["attachment"]
	if !present {
		return corpusNotRequested(corpus.EvidenceAttachments), nil
	}
	attachments, ok := raw.([]any)
	if !ok {
		return corpusUnavailable(corpus.EvidenceAttachments, corpus.EvidenceCorrupt), nil
	}
	lineage := corpus.SourceLineage{Path: item.Metadata.Path, NativeSHA256: item.Metadata.SHA256, MetadataSHA256: item.Metadata.SHA256}
	for _, rawAttachment := range attachments {
		attachment, ok := rawAttachment.(map[string]any)
		if !ok {
			return corpus.Evidence{}, fmt.Errorf("jira attachment evidence is malformed")
		}
		providerID := corpusStringValue(attachment["id"])
		stableID, err := corpus.StableObjectID(source.snapshot.OriginSHA256(), corpus.ServiceJira, corpus.ObjectAttachment, providerID)
		if err != nil {
			return corpus.Evidence{}, err
		}
		if err := builder.addEdge(corpus.IndexerEdge{
			SchemaVersion: corpus.IndexerSchemaV1, SourceID: stableID, Relation: corpus.EdgeAttachmentOwner,
			Direction: corpus.DirectionOutbound, TargetID: owner.stableID,
			Confidence: corpus.ConfidenceReported, Evidence: corpus.EdgeEvidence{Kind: corpus.EvidenceAttachments, Path: item.Metadata.Path, Fragment: "fields.attachment"},
		}); err != nil {
			return corpus.Evidence{}, err
		}
		if err := builder.addDocument(corpus.IndexerDocument{
			SchemaVersion: corpus.IndexerSchemaV1, ID: stableID, Service: corpus.ServiceJira, Kind: corpus.ObjectAttachment,
			Title: corpusPresentation(corpusStringValue(attachment["filename"])), Container: owner.container, Labels: []string{}, Source: lineage,
			RenderStatus: corpus.RenderUnsupported, Visibility: visibility,
			Evidence: corpusEvidenceSet(corpusUnsupported(corpus.EvidenceAttachments), corpusUnsupported(corpus.EvidenceBody),
				corpusUnsupported(corpus.EvidenceComments), corpusComplete(corpus.EvidenceHierarchy, 1), corpusComplete(corpus.EvidenceMetadata, 1),
				corpusComplete(corpus.EvidenceRelations, 1), visibilityEvidence),
		}); err != nil {
			return corpus.Evidence{}, err
		}
		if err := builder.addArtifact(corpus.IndexerArtifact{
			SchemaVersion: corpus.IndexerSchemaV2, DocumentID: stableID, Service: corpus.ServiceJira, ParentID: owner.stableID,
			MediaType:    corpusPresentation(corpusStringValue(attachment["mimeType"])),
			DeclaredSize: corpusInt64Value(attachment["size"]), Status: corpus.ArtifactBodyNotRequested,
			Source: corpus.ArtifactSourceLineage{
				InventoryPath: item.Metadata.Path, InventorySHA256: item.Metadata.SHA256,
				ParentNativeSHA256: item.Native.SHA256, ParentMetadataSHA256: item.Metadata.SHA256,
			},
		}, nil); err != nil {
			return corpus.Evidence{}, err
		}
	}
	return corpusComplete(corpus.EvidenceAttachments, len(attachments)), nil
}

func (builder *corpusProjectionBuilder) projectConfluenceAttachments(
	source corpusExportSource,
	owner corpusIndexedItem,
	item mirror.CorpusSnapshotItem,
	visibility corpus.Visibility,
	visibilityEvidence corpus.Evidence,
) (corpus.Evidence, error) {
	if corpusCaptureDimensionNotRequested(source, corpus.CaptureAttachments) {
		return corpusNotRequested(corpus.EvidenceAttachments), nil
	}
	sidecar, present := corpusAuxiliaryWithSuffix(item.Auxiliaries, ".attachments.json")
	if !present {
		return corpusUnsupported(corpus.EvidenceAttachments), nil
	}
	return builder.projectQualifiedAttachments(source, owner, item, sidecar, visibility, visibilityEvidence)
}

func corpusConfluenceAttachmentTargets(source corpusExportSource, item mirror.CorpusSnapshotItem) (map[string]string, error) {
	targets := map[string]string{}
	if source.service != corpus.ServiceConfluence || corpusCaptureDimensionNotRequested(source, corpus.CaptureAttachments) {
		return targets, nil
	}
	sidecar, present := corpusAuxiliaryWithSuffix(item.Auxiliaries, ".attachments.json")
	if !present {
		return targets, nil
	}
	decoded, err := mirror.DecodeAttachmentSidecarV1(sidecar.Data)
	if err != nil || decoded.Service != mirror.CorpusSnapshotConfluence || decoded.OriginSHA256 != source.snapshot.OriginSHA256() ||
		decoded.ParentID != item.ProviderID || decoded.ParentVersion != item.Version || decoded.ParentRevision != "" ||
		decoded.NativeSHA256 != item.Native.SHA256 || decoded.MetadataSHA256 != item.Metadata.SHA256 {
		return nil, fmt.Errorf("qualified Confluence attachment sidecar is misbound")
	}
	ambiguous := map[string]bool{}
	for _, attachment := range decoded.Attachments {
		stableID, idErr := corpus.StableObjectID(source.snapshot.OriginSHA256(), source.service, corpus.ObjectAttachment, attachment.ID)
		if idErr != nil {
			return nil, idErr
		}
		if previous, exists := targets[attachment.Filename]; exists && previous != stableID {
			ambiguous[attachment.Filename] = true
		} else {
			targets[attachment.Filename] = stableID
		}
	}
	for filename := range ambiguous {
		delete(targets, filename)
	}
	return targets, nil
}

func (builder *corpusProjectionBuilder) projectQualifiedAttachments(
	source corpusExportSource,
	owner corpusIndexedItem,
	item mirror.CorpusSnapshotItem,
	sidecar mirror.CorpusSnapshotFile,
	visibility corpus.Visibility,
	visibilityEvidence corpus.Evidence,
) (corpus.Evidence, error) {
	decoded, err := mirror.DecodeAttachmentSidecarV1(sidecar.Data)
	if err != nil || decoded.Service != string(source.service) || decoded.OriginSHA256 != source.snapshot.OriginSHA256() ||
		decoded.ParentID != item.ProviderID || decoded.NativeSHA256 != item.Native.SHA256 || decoded.MetadataSHA256 != item.Metadata.SHA256 {
		return corpus.Evidence{}, fmt.Errorf("qualified attachment sidecar is misbound")
	}
	switch source.service {
	case corpus.ServiceConfluence:
		if decoded.ParentVersion != item.Version || decoded.ParentRevision != "" {
			return corpus.Evidence{}, fmt.Errorf("qualified Confluence attachment sidecar is misbound")
		}
	case corpus.ServiceJira:
		if decoded.ParentVersion != 0 || decoded.ParentRevision != corpusJiraSnapshotRevision(item.Metadata.Data) {
			return corpus.Evidence{}, fmt.Errorf("qualified Jira attachment sidecar is misbound")
		}
	default:
		return corpus.Evidence{}, fmt.Errorf("qualified attachment service is unsupported")
	}
	lineage := corpus.SourceLineage{Path: sidecar.Path, NativeSHA256: item.Native.SHA256, MetadataSHA256: item.Metadata.SHA256}
	for _, attachment := range decoded.Attachments {
		stableID, idErr := corpus.StableObjectID(source.snapshot.OriginSHA256(), source.service, corpus.ObjectAttachment, attachment.ID)
		if idErr != nil {
			return corpus.Evidence{}, idErr
		}
		if err := builder.addEdge(corpus.IndexerEdge{
			SchemaVersion: corpus.IndexerSchemaV1, SourceID: stableID, Relation: corpus.EdgeAttachmentOwner,
			Direction: corpus.DirectionOutbound, TargetID: owner.stableID,
			Confidence: corpus.ConfidenceExact, Evidence: corpus.EdgeEvidence{Kind: corpus.EvidenceAttachments, Path: sidecar.Path, Fragment: "attachment-owner"},
		}); err != nil {
			return corpus.Evidence{}, err
		}
		version := ""
		if attachment.Version > 0 {
			version = fmt.Sprint(attachment.Version)
		}
		if err := builder.addDocument(corpus.IndexerDocument{
			SchemaVersion: corpus.IndexerSchemaV1, ID: stableID, Service: source.service, Kind: corpus.ObjectAttachment,
			Title: corpusPresentation(attachment.Filename), Container: owner.container, Version: version,
			Updated: corpusCanonicalTimestamp(attachment.CreatedAt), Labels: []string{}, Source: lineage,
			RenderStatus: corpus.RenderUnsupported, Visibility: visibility,
			Evidence: corpusEvidenceSet(corpusUnsupported(corpus.EvidenceAttachments), corpusUnsupported(corpus.EvidenceBody),
				corpusUnsupported(corpus.EvidenceComments), corpusComplete(corpus.EvidenceHierarchy, 1), corpusComplete(corpus.EvidenceMetadata, 1),
				corpusComplete(corpus.EvidenceRelations, 1), visibilityEvidence),
		}); err != nil {
			return corpus.Evidence{}, err
		}
		status, reason, mapErr := corpusArtifactBodyState(attachment.Body)
		if mapErr != nil {
			return corpus.Evidence{}, mapErr
		}
		artifact := corpus.IndexerArtifact{
			SchemaVersion: corpus.IndexerSchemaV2, DocumentID: stableID, Service: source.service, ParentID: owner.stableID,
			MediaType: corpusPresentation(attachment.MediaType), DeclaredSize: attachment.DeclaredSize,
			Status: status, Reason: reason,
			Source: corpus.ArtifactSourceLineage{
				InventoryPath: sidecar.Path, InventorySHA256: sidecar.SHA256,
				ParentNativeSHA256: item.Native.SHA256, ParentMetadataSHA256: item.Metadata.SHA256,
			},
		}
		var body []byte
		if attachment.Body.State == mirror.AttachmentBodyCaptured {
			captured, present := corpusAuxiliaryAtPath(item.Auxiliaries, attachment.Body.Path)
			if !present || captured.SHA256 != attachment.Body.SHA256 || int64(len(captured.Data)) != attachment.Body.Size {
				return corpus.Evidence{}, fmt.Errorf("qualified attachment body is missing or mismatched")
			}
			artifact.Path = "artifacts/" + string(source.service) + "/" + stableID + ".body"
			artifact.Size = attachment.Body.Size
			artifact.SHA256 = attachment.Body.SHA256
			body = captured.Data
		}
		if err := builder.addArtifact(artifact, body); err != nil {
			return corpus.Evidence{}, err
		}
	}
	if decoded.Complete {
		return corpusComplete(corpus.EvidenceAttachments, decoded.Count), nil
	}
	if !decoded.InventoryComplete {
		switch decoded.InventoryPartialReason {
		case mirror.AttachmentInventoryForbidden:
			return corpusForbidden(corpus.EvidenceAttachments), nil
		case mirror.AttachmentInventoryUnsupported:
			return corpusUnsupported(corpus.EvidenceAttachments), nil
		}
	}
	reason := corpus.EvidenceUnresolved
	for _, partial := range decoded.PartialReasons {
		switch partial {
		case mirror.AttachmentReasonInventoryPageLimit, mirror.AttachmentReasonInventoryItemLimit,
			mirror.AttachmentReasonBodyCountLimit, mirror.AttachmentReasonBodyItemLimit, mirror.AttachmentReasonBodyAggregateLimit:
			reason = corpus.EvidenceTruncated
		}
	}
	return corpusPartial(corpus.EvidenceAttachments, decoded.Count, reason), nil
}

func corpusArtifactBodyState(body mirror.AttachmentSidecarBody) (corpus.ArtifactBodyStatus, corpus.ArtifactBodyReason, error) {
	status := corpus.ArtifactBodyStatus(body.State)
	reason := corpus.ArtifactBodyReason(body.Reason)
	switch status {
	case corpus.ArtifactBodyCaptured, corpus.ArtifactBodyExcluded, corpus.ArtifactBodyForbidden,
		corpus.ArtifactBodyFailed, corpus.ArtifactBodyNotRequested:
		return status, reason, nil
	default:
		return "", "", fmt.Errorf("qualified attachment body state is unsupported")
	}
}

func corpusAuxiliaryAtPath(values []mirror.CorpusSnapshotFile, path string) (mirror.CorpusSnapshotFile, bool) {
	for _, value := range values {
		if value.Path == path {
			return value, true
		}
	}
	return mirror.CorpusSnapshotFile{}, false
}

func corpusInt64Value(value any) int64 {
	switch current := value.(type) {
	case float64:
		if current >= 0 && current < 9223372036854775808.0 && current == float64(int64(current)) {
			return int64(current)
		}
	case int:
		if current >= 0 {
			return int64(current)
		}
	case int64:
		if current >= 0 {
			return current
		}
	case json.Number:
		parsed, err := current.Int64()
		if err == nil && parsed >= 0 {
			return parsed
		}
	}
	return 0
}

func corpusCaptureDimensionNotRequested(source corpusExportSource, dimension corpus.CaptureDimension) bool {
	if source.capture == nil {
		return false
	}
	for _, evidence := range source.capture.Dimensions {
		if evidence.Dimension == dimension {
			return evidence.State == corpus.CaptureNotRequested
		}
	}
	return false
}

func corpusAuxiliaryWithSuffix(values []mirror.CorpusSnapshotFile, suffix string) (mirror.CorpusSnapshotFile, bool) {
	for _, value := range values {
		if strings.HasSuffix(value.Path, suffix) {
			return value, true
		}
	}
	return mirror.CorpusSnapshotFile{}, false
}

func corpusSortedUnique(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, corpusPresentation(value))
	}
	sort.Strings(out)
	write := 0
	for _, value := range out {
		if value == "" || write > 0 && out[write-1] == value {
			continue
		}
		out[write] = value
		write++
	}
	return out[:write]
}

func corpusPresentation(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return unicode.ReplacementChar
		}
		return r
	}, value)
}
