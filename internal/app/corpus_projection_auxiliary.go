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

func (builder *corpusProjectionBuilder) projectJiraAttachments(
	source corpusExportSource,
	owner corpusIndexedItem,
	item mirror.CorpusSnapshotItem,
	fields map[string]any,
	visibility corpus.Visibility,
	visibilityEvidence corpus.Evidence,
) (corpus.Evidence, error) {
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
	}
	return corpusComplete(corpus.EvidenceAttachments, len(attachments)), nil
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
