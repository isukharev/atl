package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/fragment"
	"github.com/isukharev/atl/internal/jiramap"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/wikimd"
)

type corpusIndexedSource struct {
	source corpusExportSource
	items  []corpusIndexedItem
}

type corpusIndexedItem struct {
	index        int
	stableID     string
	markdownPath string
	providerID   string
	stateID      string
	key          string
	title        string
	container    string
}

type corpusProjectionBuilder struct {
	documents     []corpus.IndexerDocument
	edges         []corpus.IndexerEdge
	markdown      []corpus.MarkdownMember
	files         map[string][]byte
	documentsSeen map[string]struct{}
	edgesSeen     map[string]struct{}

	jiraByKey           map[string]string
	confluenceByID      map[string]string
	confluenceByTitle   map[string]string
	confluenceAmbiguous map[string]bool
}

func projectCorpusSnapshots(ctx context.Context, sources []corpusExportSource, limits corpus.Limits) (corpusProjectionBundle, error) {
	indexed, builder, err := indexCorpusSnapshotSources(ctx, sources)
	if err != nil {
		return corpusProjectionBundle{}, err
	}
	for _, source := range indexed {
		for _, descriptor := range source.items {
			if err := ctx.Err(); err != nil {
				return corpusProjectionBundle{}, err
			}
			item, err := source.source.snapshot.ReadItem(descriptor.index)
			if err != nil {
				return corpusProjectionBundle{}, err
			}
			switch source.source.service {
			case corpus.ServiceConfluence:
				err = builder.projectConfluenceItem(source.source, descriptor, item)
			case corpus.ServiceJira:
				err = builder.projectJiraItem(source.source, descriptor, item)
			default:
				err = fmt.Errorf("unsupported corpus source service")
			}
			if err != nil {
				return corpusProjectionBundle{}, err
			}
		}
	}

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

func indexCorpusSnapshotSources(ctx context.Context, sources []corpusExportSource) ([]corpusIndexedSource, *corpusProjectionBuilder, error) {
	builder := &corpusProjectionBuilder{
		documents: []corpus.IndexerDocument{}, edges: []corpus.IndexerEdge{}, markdown: []corpus.MarkdownMember{},
		files: make(map[string][]byte), documentsSeen: make(map[string]struct{}), edgesSeen: make(map[string]struct{}),
		jiraByKey: make(map[string]string), confluenceByID: make(map[string]string),
		confluenceByTitle: make(map[string]string), confluenceAmbiguous: make(map[string]bool),
	}
	indexed := make([]corpusIndexedSource, 0, len(sources))
	for _, source := range sources {
		current := corpusIndexedSource{source: source, items: make([]corpusIndexedItem, 0, source.snapshot.Len())}
		for index := 0; index < source.snapshot.Len(); index++ {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			item, err := source.snapshot.ReadItem(index)
			if err != nil {
				return nil, nil, err
			}
			stableID, err := corpus.StableObjectID(source.snapshot.OriginSHA256(), source.service, corpusObjectKind(source.service), item.ProviderID)
			if err != nil {
				return nil, nil, err
			}
			descriptor := corpusIndexedItem{
				index: index, stableID: stableID, providerID: item.ProviderID, stateID: item.StateID,
				markdownPath: "markdown/" + string(source.service) + "/" + stableID + ".md",
			}
			switch source.service {
			case corpus.ServiceConfluence:
				var metadata mirror.Meta
				if err := json.Unmarshal(item.Metadata.Data, &metadata); err != nil {
					return nil, nil, err
				}
				descriptor.key = metadata.ID
				descriptor.title = corpusPresentation(metadata.Title)
				descriptor.container = corpusPresentation(metadata.Space)
				builder.confluenceByID[metadata.ID] = stableID
				titleKey := corpusConfluenceTitleKey(metadata.Space, metadata.Title)
				if previous, present := builder.confluenceByTitle[titleKey]; present && previous != stableID {
					builder.confluenceAmbiguous[titleKey] = true
				} else {
					builder.confluenceByTitle[titleKey] = stableID
				}
			case corpus.ServiceJira:
				var snapshot JiraIssueSnapshot
				if err := json.Unmarshal(item.Metadata.Data, &snapshot); err != nil {
					return nil, nil, err
				}
				descriptor.key = strings.ToUpper(snapshot.Key)
				descriptor.title = corpusPresentation(corpusStringValue(snapshot.Fields["summary"]))
				if project, ok := snapshot.Fields["project"].(map[string]any); ok {
					descriptor.container = corpusPresentation(corpusStringValue(project["key"]))
				}
				if previous, present := builder.jiraByKey[descriptor.key]; present && previous != stableID {
					return nil, nil, fmt.Errorf("duplicate Jira key in corpus snapshot")
				}
				builder.jiraByKey[descriptor.key] = stableID
			}
			current.items = append(current.items, descriptor)
		}
		indexed = append(indexed, current)
	}
	return indexed, builder, nil
}

func corpusObjectKind(service corpus.Service) corpus.ObjectKind {
	if service == corpus.ServiceConfluence {
		return corpus.ObjectPage
	}
	return corpus.ObjectIssue
}

func (builder *corpusProjectionBuilder) projectConfluenceItem(source corpusExportSource, indexed corpusIndexedItem, item mirror.CorpusSnapshotItem) error {
	var metadata mirror.Meta
	if err := json.Unmarshal(item.Metadata.Data, &metadata); err != nil {
		return err
	}
	lineage := corpus.SourceLineage{Path: item.Native.Path, NativeSHA256: item.Native.SHA256, MetadataSHA256: item.Metadata.SHA256}
	visibility, visibilityEvidence := corpusConfluenceVisibility(metadata.Restricted)

	root, parseErr := csf.Parse(item.Native.Data)
	text := ""
	renderStatus := corpus.RenderFailed
	bodyEvidence := corpusUnavailable(corpus.EvidenceBody, corpus.EvidenceRenderFailed)
	relationsEvidence := corpusUnavailable(corpus.EvidenceRelations, corpus.EvidenceCorrupt)
	pageReferences := []corpusConfluenceReference{}
	if parseErr == nil {
		refs := corpusConfluenceRefs(root, metadata.Refs)
		pageReferences = corpusExtractConfluenceReferences(root)
		rendered := mirror.RenderMarkdownResolved(root, refs, builder.linkResolver(indexed))
		if utf8.Valid(rendered) {
			text = string(rendered)
			renderStatus = corpus.RenderRendered
			if text == "" {
				renderStatus = corpus.RenderEmpty
			}
			bodyEvidence = corpusComplete(corpus.EvidenceBody, 1)
		}
		relationsEvidence = corpusComplete(corpus.EvidenceRelations, 0)
	}

	relationCount := 0
	if metadata.Parent != "" {
		targetID := builder.confluenceByID[metadata.Parent]
		if err := builder.addEdge(corpus.IndexerEdge{
			SchemaVersion: corpus.IndexerSchemaV1, SourceID: indexed.stableID, Relation: corpus.EdgeParent,
			Direction: corpus.DirectionOutbound, TargetID: targetID,
			Unresolved: corpusReferenceIfEmpty(targetID, corpus.ServiceConfluence, corpus.ObjectPage, metadata.Parent),
			Confidence: corpus.ConfidenceExact, Evidence: corpus.EdgeEvidence{Kind: corpus.EvidenceHierarchy, Path: item.Metadata.Path, Fragment: "parent"},
		}); err != nil {
			return err
		}
		if targetID != "" {
			if err := builder.addEdge(corpus.IndexerEdge{
				SchemaVersion: corpus.IndexerSchemaV1, SourceID: targetID, Relation: corpus.EdgeContains,
				Direction: corpus.DirectionOutbound, TargetID: indexed.stableID,
				Confidence: corpus.ConfidenceExact, Evidence: corpus.EdgeEvidence{Kind: corpus.EvidenceHierarchy, Path: item.Metadata.Path, Fragment: "parent"},
			}); err != nil {
				return err
			}
		}
	}
	for _, reference := range pageReferences {
		var targetID string
		service := corpus.ServiceConfluence
		value := reference.Title
		switch reference.Kind {
		case corpus.ObjectPage:
			targetID = builder.confluenceTitleTarget(reference.Space, reference.Title, metadata.Space)
			if reference.Space != "" {
				value = reference.Space + "/" + reference.Title
			}
		case corpus.ObjectIssue:
			service = corpus.ServiceJira
			targetID = builder.jiraByKey[strings.ToUpper(reference.Title)]
			value = strings.ToUpper(reference.Title)
		case corpus.ObjectAttachment:
			// Confluence metadata does not expose a stable attachment ID in
			// this mirror shape, so preserve the filename as unresolved.
		default:
			continue
		}
		if err := builder.addEdge(corpus.IndexerEdge{
			SchemaVersion: corpus.IndexerSchemaV1, SourceID: indexed.stableID, Relation: corpus.EdgeReferences,
			Direction: corpus.DirectionOutbound, TargetID: targetID,
			Unresolved: corpusReferenceIfEmpty(targetID, service, reference.Kind, value),
			Confidence: corpus.ConfidenceStructural, Evidence: corpus.EdgeEvidence{Kind: corpus.EvidenceRelations, Path: item.Native.Path, Fragment: reference.Fragment},
		}); err != nil {
			return err
		}
		relationCount++
	}
	if relationsEvidence.Status == corpus.EvidenceComplete {
		relationsEvidence.ObservedCount = relationCount
	}
	commentsEvidence, err := builder.projectConfluenceComments(source, indexed, item, lineage, visibility, visibilityEvidence)
	if err != nil {
		return err
	}

	document := corpus.IndexerDocument{
		SchemaVersion: corpus.IndexerSchemaV1, ID: indexed.stableID, Service: corpus.ServiceConfluence, Kind: corpus.ObjectPage,
		Key: metadata.ID, Title: indexed.title, Container: indexed.container, Version: strconv.Itoa(metadata.Version),
		Updated: corpusCanonicalTimestamp(metadata.Updated), Labels: corpusSortedUnique(metadata.Labels), Source: lineage,
		Text: text, RenderStatus: renderStatus, MarkdownPath: indexed.markdownPath, Visibility: visibility,
		Evidence: corpusEvidenceSet(
			corpusUnsupported(corpus.EvidenceAttachments), bodyEvidence, commentsEvidence,
			corpusComplete(corpus.EvidenceHierarchy, corpusBoolCount(metadata.Parent != "")),
			corpusComplete(corpus.EvidenceMetadata, 1), relationsEvidence, visibilityEvidence,
		),
	}
	return builder.addDocument(document)
}

func (builder *corpusProjectionBuilder) projectJiraItem(source corpusExportSource, indexed corpusIndexedItem, item mirror.CorpusSnapshotItem) error {
	var snapshot JiraIssueSnapshot
	if err := json.Unmarshal(item.Metadata.Data, &snapshot); err != nil {
		return err
	}
	issue := jiramap.Issue(snapshot.ID, snapshot.Key, snapshot.Fields)
	lineage := corpus.SourceLineage{Path: item.Native.Path, NativeSHA256: item.Native.SHA256, MetadataSHA256: item.Metadata.SHA256}
	visibility, visibilityEvidence := corpusJiraVisibility(snapshot.Fields)

	text, renderStatus, bodyEvidence := corpusRenderJiraWiki(item.Native.Data, builder.jiraLinkResolver(indexed))
	relationCount := 0
	parentEvidence := corpusNotRequested(corpus.EvidenceHierarchy)
	if rawParent, present := snapshot.Fields["parent"]; present {
		switch parent := rawParent.(type) {
		case nil:
			parentEvidence = corpusComplete(corpus.EvidenceHierarchy, 0)
		case map[string]any:
			key := strings.ToUpper(corpusStringValue(parent["key"]))
			if key == "" {
				parentEvidence = corpusUnavailable(corpus.EvidenceHierarchy, corpus.EvidenceCorrupt)
			} else {
				targetID := builder.jiraByKey[key]
				if err := builder.addEdge(corpus.IndexerEdge{
					SchemaVersion: corpus.IndexerSchemaV1, SourceID: indexed.stableID, Relation: corpus.EdgeParent,
					Direction: corpus.DirectionOutbound, TargetID: targetID,
					Unresolved: corpusReferenceIfEmpty(targetID, corpus.ServiceJira, corpus.ObjectIssue, key),
					Confidence: corpus.ConfidenceReported, Evidence: corpus.EdgeEvidence{Kind: corpus.EvidenceHierarchy, Path: item.Metadata.Path, Fragment: "fields.parent"},
				}); err != nil {
					return err
				}
				parentEvidence = corpusComplete(corpus.EvidenceHierarchy, 1)
				if targetID != "" {
					if err := builder.addEdge(corpus.IndexerEdge{
						SchemaVersion: corpus.IndexerSchemaV1, SourceID: targetID, Relation: corpus.EdgeContains,
						Direction: corpus.DirectionOutbound, TargetID: indexed.stableID,
						Confidence: corpus.ConfidenceReported, Evidence: corpus.EdgeEvidence{Kind: corpus.EvidenceHierarchy, Path: item.Metadata.Path, Fragment: "fields.parent"},
					}); err != nil {
						return err
					}
				}
			}
		default:
			parentEvidence = corpusUnavailable(corpus.EvidenceHierarchy, corpus.EvidenceCorrupt)
		}
	}

	relationsEvidence := corpusNotRequested(corpus.EvidenceRelations)
	if rawLinks, present := snapshot.Fields["issuelinks"]; present {
		links, valid := rawLinks.([]any)
		if !valid || !corpusJiraIssueLinksComplete(links, issue.Links) {
			relationsEvidence = corpusUnavailable(corpus.EvidenceRelations, corpus.EvidenceCorrupt)
		} else {
			for _, link := range issue.Links {
				key := strings.ToUpper(link.Key)
				targetID := builder.jiraByKey[key]
				direction := corpus.DirectionUnknown
				switch link.Direction {
				case "outward":
					direction = corpus.DirectionOutbound
				case "inward":
					direction = corpus.DirectionInbound
				}
				name := link.TypeName
				if name == "" {
					name = link.Type
				}
				if name == "" {
					name = "related"
				}
				name = corpusPresentation(name)
				if err := builder.addEdge(corpus.IndexerEdge{
					SchemaVersion: corpus.IndexerSchemaV1, SourceID: indexed.stableID, Relation: corpus.EdgeJiraIssueLink,
					RelationName: name, Direction: direction, TargetID: targetID,
					Unresolved: corpusReferenceIfEmpty(targetID, corpus.ServiceJira, corpus.ObjectIssue, key),
					Confidence: corpus.ConfidenceReported, Evidence: corpus.EdgeEvidence{Kind: corpus.EvidenceRelations, Path: item.Metadata.Path, Fragment: "fields.issuelinks"},
				}); err != nil {
					return err
				}
				relationCount++
			}
			relationsEvidence = corpusComplete(corpus.EvidenceRelations, len(links))
		}
	}
	for _, reference := range extractGraphReferences(string(item.Native.Data), "", "", true) {
		var service corpus.Service
		var kind corpus.ObjectKind
		var value, targetID string
		switch {
		case strings.HasPrefix(reference.Node.ID, "jira:issue:"):
			service, kind = corpus.ServiceJira, corpus.ObjectIssue
			value = strings.TrimPrefix(reference.Node.ID, "jira:issue:")
			targetID = builder.jiraByKey[value]
		case strings.HasPrefix(reference.Node.ID, "confluence:page:"):
			service, kind = corpus.ServiceConfluence, corpus.ObjectPage
			value = strings.TrimPrefix(reference.Node.ID, "confluence:page:")
			targetID = builder.confluenceByID[value]
		default:
			continue
		}
		if targetID == indexed.stableID {
			continue
		}
		if err := builder.addEdge(corpus.IndexerEdge{
			SchemaVersion: corpus.IndexerSchemaV1, SourceID: indexed.stableID, Relation: corpus.EdgeReferences,
			Direction: corpus.DirectionOutbound, TargetID: targetID, Unresolved: corpusReferenceIfEmpty(targetID, service, kind, value),
			Confidence: corpus.ConfidenceStructural, Evidence: corpus.EdgeEvidence{Kind: corpus.EvidenceRelations, Path: item.Native.Path, Fragment: "body"},
		}); err != nil {
			return err
		}
		relationCount++
	}
	switch {
	case relationsEvidence.Status == corpus.EvidenceComplete:
		relationsEvidence.ObservedCount = relationCount
	case relationCount > 0 && relationsEvidence.Status == corpus.EvidenceUnavailable:
		relationsEvidence = corpusPartial(corpus.EvidenceRelations, relationCount, corpus.EvidenceCorrupt)
	case relationCount > 0 && relationsEvidence.Status == corpus.EvidenceNotRequested:
		relationsEvidence = corpusPartial(corpus.EvidenceRelations, relationCount, corpus.EvidenceLegacyUnqualified)
	}

	commentsEvidence, err := builder.projectJiraComments(source, indexed, item, snapshot.Fields, visibility, visibilityEvidence)
	if err != nil {
		return err
	}
	attachmentsEvidence, err := builder.projectJiraAttachments(source, indexed, item, snapshot.Fields, visibility, visibilityEvidence)
	if err != nil {
		return err
	}
	document := corpus.IndexerDocument{
		SchemaVersion: corpus.IndexerSchemaV1, ID: indexed.stableID, Service: corpus.ServiceJira, Kind: corpus.ObjectIssue,
		Key: indexed.key, Title: indexed.title, Container: indexed.container,
		Updated: corpusCanonicalTimestamp(corpusStringValue(snapshot.Fields["updated"])), Labels: corpusSortedUnique(issue.Labels), Source: lineage,
		Text: text, RenderStatus: renderStatus, MarkdownPath: indexed.markdownPath, Visibility: visibility,
		Evidence: corpusEvidenceSet(attachmentsEvidence, bodyEvidence, commentsEvidence, parentEvidence,
			corpusComplete(corpus.EvidenceMetadata, 1), relationsEvidence, visibilityEvidence),
	}
	return builder.addDocument(document)
}

func (builder *corpusProjectionBuilder) addDocument(document corpus.IndexerDocument) error {
	if _, duplicate := builder.documentsSeen[document.ID]; duplicate {
		return fmt.Errorf("duplicate corpus document identity")
	}
	document.Labels = corpusSortedUnique(document.Labels)
	document.BodySHA256 = corpusBytesSHA256([]byte(document.Text))
	if document.RenderStatus == corpus.RenderRendered || document.RenderStatus == corpus.RenderEmpty {
		data := []byte(document.Text)
		document.MarkdownSHA256 = corpusBytesSHA256(data)
		builder.files[document.MarkdownPath] = append([]byte(nil), data...)
		builder.markdown = append(builder.markdown, corpus.MarkdownMember{
			DocumentID: document.ID, Path: document.MarkdownPath, Size: int64(len(data)), SHA256: document.MarkdownSHA256,
		})
	} else {
		document.MarkdownPath = ""
	}
	builder.documentsSeen[document.ID] = struct{}{}
	builder.documents = append(builder.documents, document)
	return nil
}

func (builder *corpusProjectionBuilder) addEdge(edge corpus.IndexerEdge) error {
	if edge.TargetID != "" {
		edge.Unresolved = nil
	}
	id, err := corpus.DeriveEdgeID(edge)
	if err != nil {
		return err
	}
	edge.ID = id
	if _, duplicate := builder.edgesSeen[id]; duplicate {
		return nil
	}
	builder.edgesSeen[id] = struct{}{}
	builder.edges = append(builder.edges, edge)
	return nil
}

func (builder *corpusProjectionBuilder) linkResolver(source corpusIndexedItem) mirror.MarkdownLinkResolver {
	return func(target string) (string, bool) {
		switch {
		case strings.HasPrefix(target, "jira:"):
			stableID := builder.jiraByKey[strings.ToUpper(strings.TrimPrefix(target, "jira:"))]
			return corpusRelativeMarkdownLink(source.markdownPath, corpusMarkdownPath(corpus.ServiceJira, stableID))
		case strings.HasPrefix(target, "confluence-page:"):
			identity := strings.TrimPrefix(target, "confluence-page:")
			decoded, err := url.PathUnescape(identity)
			if err != nil {
				return "", false
			}
			space, title := "", decoded
			if slash := strings.Index(decoded, "/"); slash >= 0 {
				space, title = decoded[:slash], decoded[slash+1:]
			}
			stableID := builder.confluenceTitleTarget(space, title, source.container)
			return corpusRelativeMarkdownLink(source.markdownPath, corpusMarkdownPath(corpus.ServiceConfluence, stableID))
		default:
			return "", false
		}
	}
}

func (builder *corpusProjectionBuilder) jiraLinkResolver(source corpusIndexedItem) func(string) (string, bool) {
	return func(target string) (string, bool) {
		key := strings.ToUpper(strings.TrimPrefix(target, "jira:"))
		stableID := builder.jiraByKey[key]
		return corpusRelativeMarkdownLink(source.markdownPath, corpusMarkdownPath(corpus.ServiceJira, stableID))
	}
}

func corpusMarkdownPath(service corpus.Service, stableID string) string {
	if stableID == "" {
		return ""
	}
	return "markdown/" + string(service) + "/" + stableID + ".md"
}

func corpusRelativeMarkdownLink(from, to string) (string, bool) {
	if from == "" || to == "" {
		return "", false
	}
	relative, err := filepath.Rel(filepath.FromSlash(filepath.Dir(from)), filepath.FromSlash(to))
	if err != nil || relative == "." || strings.HasPrefix(relative, "/") {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

func (builder *corpusProjectionBuilder) confluenceTitleTarget(space, title, fallbackSpace string) string {
	if space == "" {
		space = fallbackSpace
	}
	key := corpusConfluenceTitleKey(space, title)
	if builder.confluenceAmbiguous[key] {
		return ""
	}
	return builder.confluenceByTitle[key]
}

func corpusConfluenceTitleKey(space, title string) string {
	return strings.ToUpper(space) + "\x00" + title
}

type corpusConfluenceReference struct {
	Kind                   corpus.ObjectKind
	Space, Title, Fragment string
}

func corpusExtractConfluenceReferences(root *csf.Node) []corpusConfluenceReference {
	seen := map[string]bool{}
	out := []corpusConfluenceReference{}
	csf.Walk(root, func(node *csf.Node) bool {
		var reference corpusConfluenceReference
		switch {
		case node.Name.Space == "ri" && node.Name.Local == "page":
			reference = corpusConfluenceReference{Kind: corpus.ObjectPage, Space: node.Attrv("ri", "space-key"), Title: node.Attrv("ri", "content-title"), Fragment: "page-link"}
		case node.Name.Space == "ri" && node.Name.Local == "attachment":
			reference = corpusConfluenceReference{Kind: corpus.ObjectAttachment, Title: node.Attrv("ri", "filename"), Fragment: "attachment-link"}
		case node.MacroName() == "jira":
			key := corpusCSFMacroParameter(node, "key")
			if key != "" {
				reference = corpusConfluenceReference{Kind: corpus.ObjectIssue, Title: strings.ToUpper(key), Fragment: "jira-macro"}
			}
		}
		if reference.Title == "" {
			return true
		}
		key := string(reference.Kind) + "\x00" + reference.Space + "\x00" + reference.Title
		if !seen[key] {
			seen[key] = true
			out = append(out, reference)
		}
		return true
	})
	return out
}

func corpusCSFMacroParameter(node *csf.Node, name string) string {
	for _, child := range node.Children {
		if child.Type == csf.Element && child.Name.Space == "ac" && child.Name.Local == "parameter" && child.Attrv("ac", "name") == name {
			return strings.TrimSpace(csf.TextContent(child))
		}
	}
	return ""
}

func corpusConfluenceRefs(root *csf.Node, recorded []domain.Ref) []domain.Ref {
	resolved := make(map[string]domain.Ref, len(recorded))
	for _, reference := range recorded {
		reference.Asset = ""
		resolved[string(reference.Kind)+"\x00"+reference.Key] = reference
	}
	refs := fragment.Extract(root)
	for index := range refs {
		if recorded, present := resolved[string(refs[index].Kind)+"\x00"+refs[index].Key]; present {
			refs[index].Display = recorded.Display
			refs[index].Asset = ""
		}
	}
	return refs
}

func corpusReferenceIfEmpty(targetID string, service corpus.Service, kind corpus.ObjectKind, value string) *corpus.Reference {
	if targetID != "" {
		return nil
	}
	return &corpus.Reference{Service: service, Kind: kind, Value: strings.TrimSpace(corpusPresentation(value))}
}

func corpusRenderJiraWiki(native []byte, resolver func(string) (string, bool)) (text string, status corpus.RenderStatus, evidence corpus.Evidence) {
	if !utf8.Valid(native) {
		return "", corpus.RenderFailed, corpusUnavailable(corpus.EvidenceBody, corpus.EvidenceRenderFailed)
	}
	defer func() {
		if recover() != nil {
			text = ""
			status = corpus.RenderFailed
			evidence = corpusUnavailable(corpus.EvidenceBody, corpus.EvidenceRenderFailed)
		}
	}()
	text = wikimd.Render(string(native), wikimd.Options{LinkResolver: resolver})
	status = corpus.RenderRendered
	if text == "" {
		status = corpus.RenderEmpty
	}
	evidence = corpusComplete(corpus.EvidenceBody, 1)
	return
}

func corpusEvidenceSet(attachments, body, comments, hierarchy, metadata, relations, visibility corpus.Evidence) []corpus.Evidence {
	return []corpus.Evidence{attachments, body, comments, hierarchy, metadata, relations, visibility}
}

func corpusComplete(kind corpus.EvidenceKind, count int) corpus.Evidence {
	return corpus.Evidence{Kind: kind, Status: corpus.EvidenceComplete, Reasons: []corpus.EvidenceReason{}, ObservedCount: count, CountExact: true}
}

func corpusPartial(kind corpus.EvidenceKind, count int, reasons ...corpus.EvidenceReason) corpus.Evidence {
	return corpus.Evidence{Kind: kind, Status: corpus.EvidencePartial, Reasons: corpusSortedReasons(reasons), ObservedCount: count}
}

func corpusUnavailable(kind corpus.EvidenceKind, reasons ...corpus.EvidenceReason) corpus.Evidence {
	return corpus.Evidence{Kind: kind, Status: corpus.EvidenceUnavailable, Reasons: corpusSortedReasons(reasons)}
}

func corpusNotRequested(kind corpus.EvidenceKind) corpus.Evidence {
	return corpus.Evidence{Kind: kind, Status: corpus.EvidenceNotRequested, Reasons: []corpus.EvidenceReason{}}
}

func corpusUnsupported(kind corpus.EvidenceKind) corpus.Evidence {
	return corpus.Evidence{Kind: kind, Status: corpus.EvidenceUnsupported, Reasons: []corpus.EvidenceReason{corpus.EvidenceUnsupportedReason}}
}

func corpusSortedReasons(reasons []corpus.EvidenceReason) []corpus.EvidenceReason {
	out := append([]corpus.EvidenceReason(nil), reasons...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if out == nil {
		out = []corpus.EvidenceReason{}
	}
	return out
}

func corpusCanonicalTimestamp(value string) string {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700", "2006-01-02T15:04:05-0700"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC().Format(time.RFC3339Nano)
		}
	}
	return ""
}

func corpusStringValue(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return ""
	}
}

func corpusIntegerValue(value any) (int, bool) {
	raw := corpusStringValue(value)
	if raw == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(raw)
	return parsed, err == nil && parsed >= 0
}

func corpusBoolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}
