package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

func TestExportCorpusPublishesReadyQualifiedCapture(t *testing.T) {
	mirrorRoot := t.TempDir()
	seedCorpusExportJira(t, mirrorRoot, "EX-1", "10001", "EX/EX-1.wiki", []byte("body"), map[string]any{
		"summary": "Issue", "project": map[string]any{"key": "EX"},
		"comment": map[string]any{"startAt": 0, "total": 1, "comments": []any{map[string]any{
			"id": "20001", "body": "unrequested comment",
		}}},
		"attachment": []any{map[string]any{"id": "30001", "filename": "unrequested.txt"}},
	})
	capture := corpusExportCaptureReceipt(t, corpus.ServiceJira, mirrorRoot)
	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := ExportCorpus(context.Background(), CorpusExportOptions{
		JiraRoot: mirrorRoot, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		CaptureReceipts: []corpus.CaptureReceipt{capture},
	})
	if err != nil {
		t.Fatalf("ExportCorpus: %v", err)
	}
	if result.Projection.Readiness != corpus.ProjectionReady || len(result.Projection.Qualifications) != 1 ||
		result.Projection.Qualifications[0].State != corpus.QualificationReady ||
		result.Projection.Qualifications[0].SourceReceiptDigest != capture.ReceiptDigest ||
		result.Projection.Counts.Documents != 1 ||
		result.Generation.Totals.Members != 7 {
		t.Fatalf("qualified result = %#v", result)
	}

	store, err := corpus.Open(storeRoot, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manifest := selected.Manifest()
	if len(manifest.Qualifications) != 1 || manifest.Qualifications[0].ReceiptSchema != corpus.CaptureReceiptSchemaV1 ||
		manifest.Qualifications[0].SelectorDigest != capture.SelectorDigest ||
		manifest.Qualifications[0].ReceiptDigest != capture.ReceiptDigest {
		t.Fatalf("generation qualifications = %#v", manifest.Qualifications)
	}
	var documentBytes bytes.Buffer
	if _, err := selected.CopyMember(context.Background(), corpus.ServiceJira, corpusDocumentsStableID, corpus.RoleDocument, &documentBytes); err != nil {
		t.Fatal(err)
	}
	documents, err := corpus.ParseIndexerDocuments(documentBytes.Bytes(), corpus.Limits{})
	if err != nil || len(documents) != 1 || documents[0].Evidence[0].Status != corpus.EvidenceNotRequested ||
		documents[0].Evidence[2].Status != corpus.EvidenceNotRequested {
		t.Fatalf("qualified documents=%#v error=%v", documents, err)
	}
	var receiptBytes bytes.Buffer
	if _, err := selected.CopyMember(context.Background(), corpus.ServiceJira, corpusCaptureStableID, corpus.RoleMetadata, &receiptBytes); err != nil {
		t.Fatal(err)
	}
	parsed, err := corpus.ParseCaptureReceipt(receiptBytes.Bytes(), corpus.Limits{})
	if err != nil || parsed.ReceiptDigest != capture.ReceiptDigest {
		t.Fatalf("sealed capture receipt = %#v, %v", parsed, err)
	}
	_ = selected.Close()
	_ = store.Close()

	again, err := ExportCorpus(context.Background(), CorpusExportOptions{
		JiraRoot: mirrorRoot, StoreRoot: storeRoot,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		CaptureReceipts: []corpus.CaptureReceipt{capture},
	})
	if err != nil || !again.Reused || again.Generation.GenerationDigest != result.Generation.GenerationDigest {
		t.Fatalf("qualified reuse = %#v, %v", again, err)
	}
}

func TestExportCorpusRejectsCaptureMismatchAndMixedQualificationBeforeStoreWrites(t *testing.T) {
	jiraRoot := t.TempDir()
	seedCorpusExportJira(t, jiraRoot, "EX-1", "10001", "EX/EX-1.wiki", []byte("body"), map[string]any{"summary": "Issue"})
	confluenceRoot := t.TempDir()
	seedCorpusExportConfluence(t, confluenceRoot, "20001", "DOC/page.csf", []byte("<p>body</p>"), mirror.Meta{
		ID: "20001", Title: "Page", Space: "DOC", Version: 1,
	})
	capture := corpusExportCaptureReceipt(t, corpus.ServiceJira, jiraRoot)
	unsupportedEvidence := corpusExportCaptureReceiptWithDimensions(t, corpus.ServiceJira, jiraRoot, []corpus.CaptureDimensionEvidence{
		{Dimension: corpus.CaptureNative, State: corpus.CaptureComplete},
		{Dimension: corpus.CaptureMetadata, State: corpus.CaptureComplete},
		{Dimension: corpus.CaptureComments, State: corpus.CaptureComplete},
		{Dimension: corpus.CaptureAttachments, State: corpus.CaptureNotRequested},
	})

	for name, options := range map[string]CorpusExportOptions{
		"selection mismatch": {
			JiraRoot: jiraRoot, CaptureReceipts: []corpus.CaptureReceipt{func() corpus.CaptureReceipt {
				changed := capture
				changed.SelectionDigest = strings.Repeat("f", 64)
				return changed
			}()},
		},
		"mixed source set": {
			JiraRoot: jiraRoot, ConfluenceRoot: confluenceRoot,
			CaptureReceipts: []corpus.CaptureReceipt{capture},
		},
		"unsupported optional evidence": {
			JiraRoot: jiraRoot, CaptureReceipts: []corpus.CaptureReceipt{unsupportedEvidence},
		},
	} {
		t.Run(name, func(t *testing.T) {
			storeRoot := t.TempDir()
			if err := os.Chmod(storeRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			options.StoreRoot = storeRoot
			options.InitializeStore = true
			options.GeneratorVersion = "test-v1"
			options.BuildState = corpus.BuildStateClean
			if _, err := ExportCorpus(context.Background(), options); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error = %v", err)
			}
			entries, err := os.ReadDir(storeRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("store entries=%d err=%v", len(entries), err)
			}
		})
	}
}

func TestExportCorpusPublishesAndReusesExactJiraProjection(t *testing.T) {
	mirrorRoot := t.TempDir()
	body := []byte("h1. Pristine body\n\n[related|EX-2]")
	fields := map[string]any{
		"summary": "Synthetic\vsummary", "project": map[string]any{"key": "EX"},
		"labels": []any{"two", "one"}, "updated": "2026-08-09T12:00:00.000+0000", "security": nil,
		"parent": nil, "issuelinks": []any{},
		"comment": map[string]any{"startAt": 0, "total": 1, "comments": []any{map[string]any{
			"id": "20001", "created": "2026-08-09T11:00:00.000+0000", "body": "comment body",
		}}},
		"attachment": []any{map[string]any{"id": "30001", "filename": "notes.txt"}},
	}
	seedCorpusExportJira(t, mirrorRoot, "EX-1", "10001", "EX/EX-1.wiki", body, fields)
	metadataBytes, err := os.ReadFile(filepath.Join(mirrorRoot, "EX", "EX-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	metadataSHA256 := mirror.Hash(metadataBytes)
	writeCorpusExportFile(t, mirrorRoot, "EX/EX-1.wiki", []byte("ambient native edit"))
	writeCorpusExportFile(t, mirrorRoot, "EX/EX-1.md", []byte("ambient Markdown edit"))

	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	options := CorpusExportOptions{
		JiraRoot: mirrorRoot, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
	}
	result, err := ExportCorpus(context.Background(), options)
	if err != nil {
		t.Fatalf("ExportCorpus: %v", err)
	}
	if result.Reused || result.Projection.Readiness != corpus.ProjectionPartial ||
		result.Projection.Counts.Documents != 3 || result.Generation.Totals.Members != 7 {
		t.Fatalf("first export = %#v", result)
	}

	store, err := corpus.Open(storeRoot, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var documentBytes bytes.Buffer
	if _, err := selected.CopyMember(context.Background(), corpus.ServiceJira, corpusDocumentsStableID, corpus.RoleDocument, &documentBytes); err != nil {
		t.Fatal(err)
	}
	documents, err := corpus.ParseIndexerDocuments(documentBytes.Bytes(), corpus.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var issue, attachment corpus.IndexerDocument
	for _, document := range documents {
		switch document.Kind {
		case corpus.ObjectIssue:
			issue = document
		case corpus.ObjectAttachment:
			attachment = document
		}
	}
	if issue.ID == "" || issue.Title != "Synthetic\ufffdsummary" || issue.Text != "# Pristine body\n\n[related](EX-2)" ||
		strings.Contains(issue.Text, "Synthetic summary") || issue.Visibility != corpus.VisibilityUnknown {
		t.Fatalf("issue projection = %#v", issue)
	}
	wantAttachmentLineage := corpus.SourceLineage{
		Path: "EX/EX-1.json", NativeSHA256: metadataSHA256, MetadataSHA256: metadataSHA256,
	}
	if attachment.ID == "" || attachment.Source != wantAttachmentLineage {
		t.Fatalf("legacy attachment lineage = %#v, want %#v", attachment.Source, wantAttachmentLineage)
	}
	var artifactBytes bytes.Buffer
	if _, err := selected.CopyMember(context.Background(), corpus.ServiceJira, corpusArtifactsStableID, corpus.RoleMetadata, &artifactBytes); err != nil {
		t.Fatal(err)
	}
	artifacts, err := corpus.ParseIndexerArtifacts(artifactBytes.Bytes(), corpus.Limits{})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("legacy artifacts=%#v error=%v", artifacts, err)
	}
	wantArtifactLineage := corpus.ArtifactSourceLineage{
		InventoryPath: "EX/EX-1.json", InventorySHA256: metadataSHA256,
		ParentNativeSHA256: mirror.Hash(body), ParentMetadataSHA256: metadataSHA256,
	}
	if artifacts[0].Source != wantArtifactLineage {
		t.Fatalf("legacy artifact lineage = %#v, want %#v", artifacts[0].Source, wantArtifactLineage)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	options.InitializeStore = false
	again, err := ExportCorpus(context.Background(), options)
	if err != nil {
		t.Fatalf("idempotent ExportCorpus: %v", err)
	}
	if !again.Reused || again.Generation.GenerationDigest != result.Generation.GenerationDigest ||
		again.Projection.ProjectionDigest != result.Projection.ProjectionDigest {
		t.Fatalf("idempotent result = %#v, first %#v", again, result)
	}
	freshStore := t.TempDir()
	if err := os.Chmod(freshStore, 0o700); err != nil {
		t.Fatal(err)
	}
	fresh, err := ExportCorpus(context.Background(), CorpusExportOptions{
		JiraRoot: mirrorRoot, StoreRoot: freshStore, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
	})
	if err != nil || fresh.Projection.ProjectionDigest != result.Projection.ProjectionDigest ||
		fresh.Generation.GenerationDigest != result.Generation.GenerationDigest {
		t.Fatalf("fresh deterministic export = %#v, %v; first %#v", fresh, err, result)
	}
	relocatedMirror := t.TempDir()
	seedCorpusExportJira(t, relocatedMirror, "EX-1", "10001", "EX/EX-1.wiki", body, fields)
	relocatedStore := t.TempDir()
	if err := os.Chmod(relocatedStore, 0o700); err != nil {
		t.Fatal(err)
	}
	relocated, err := ExportCorpus(context.Background(), CorpusExportOptions{
		JiraRoot: relocatedMirror, StoreRoot: relocatedStore, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
	})
	if err != nil || relocated.Projection.ProjectionDigest != result.Projection.ProjectionDigest ||
		relocated.Generation.GenerationDigest != result.Generation.GenerationDigest {
		t.Fatalf("relocated deterministic export = %#v, %v; first %#v", relocated, err, result)
	}
}

func TestExportCorpusDoesNotCertifyMalformedJiraIssueLinks(t *testing.T) {
	for name, raw := range map[string]any{
		"malformed row": []any{map[string]any{}},
		"null field":    nil,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			seedCorpusExportJira(t, root, "EX-1", "10001", "EX/EX-1.wiki", []byte("body"), map[string]any{
				"summary": "Issue", "project": map[string]any{"key": "EX"}, "issuelinks": raw,
			})
			document := exportSingleCorpusDocument(t, corpus.ServiceJira, root, corpus.ObjectIssue)
			relations := document.Evidence[5]
			if relations.Kind != corpus.EvidenceRelations || relations.Status != corpus.EvidenceUnavailable ||
				relations.CountExact || relations.ObservedCount != 0 || len(relations.Reasons) != 1 || relations.Reasons[0] != corpus.EvidenceCorrupt {
				t.Fatalf("malformed issue-link evidence = %#v", relations)
			}
		})
	}
}

func TestCorpusJiraIssueLinksCompleteRejectsMalformedRows(t *testing.T) {
	mapped := []domain.IssueLink{{Direction: "outward", Key: "EX-2"}}
	valid := []any{map[string]any{
		"type": map[string]any{"name": "Relates"}, "outwardIssue": map[string]any{"key": "EX-2"},
	}}
	if !corpusJiraIssueLinksComplete(valid, mapped) {
		t.Fatal("canonical issue link was rejected")
	}
	for name, raw := range map[string][]any{
		"non-object row": {"bad"},
		"both directions": {map[string]any{
			"type":        map[string]any{"name": "Relates"},
			"inwardIssue": map[string]any{"key": "EX-2"}, "outwardIssue": map[string]any{"key": "EX-2"},
		}},
		"invalid extra direction": {map[string]any{
			"type": map[string]any{"name": "Relates"}, "inwardIssue": "bad", "outwardIssue": map[string]any{"key": "EX-2"},
		}},
		"missing relation type": {map[string]any{"outwardIssue": map[string]any{"key": "EX-2"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if corpusJiraIssueLinksComplete(raw, mapped) {
				t.Fatal("malformed issue link was accepted")
			}
		})
	}
}

func TestExportCorpusPreflightsActualStoreMembersBeforeInitialization(t *testing.T) {
	mirrorRoot := t.TempDir()
	seedCorpusExportJira(t, mirrorRoot, "EX-1", "10001", "EX/EX-1.wiki", []byte("body"), map[string]any{
		"summary": "Issue", "project": map[string]any{"key": "EX"},
	})
	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := ExportCorpus(context.Background(), CorpusExportOptions{
		JiraRoot: mirrorRoot, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		Limits: corpus.Limits{MaxMembers: 3},
	})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("member-bound error = %v", err)
	}
	entries, readErr := os.ReadDir(storeRoot)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("bounded export initialized or staged store state: entries=%d err=%v", len(entries), readErr)
	}
}

func TestCorpusExportMemberPreflightUsesActualMemberBytes(t *testing.T) {
	members := []corpusExportMember{{data: []byte("abc")}, {data: []byte("def")}}
	for name, limits := range map[string]corpus.Limits{
		"members":      {MaxMembers: 1},
		"member bytes": {MaxMemberBytes: 2},
		"total bytes":  {MaxTotalBytes: 5},
	} {
		t.Run(name, func(t *testing.T) {
			if err := preflightCorpusExportMembers(members, limits); !errors.Is(err, corpus.ErrIntegrity) {
				t.Fatalf("preflight error = %v", err)
			}
		})
	}
	if err := preflightCorpusExportMembers(members, corpus.Limits{MaxMembers: 2, MaxMemberBytes: 3, MaxTotalBytes: 6}); err != nil {
		t.Fatalf("exact bounds rejected: %v", err)
	}
}

func TestExportCorpusUsesStableIdentityAndRelativeCrossServiceLinks(t *testing.T) {
	jiraRoot := t.TempDir()
	seedCorpusExportJira(t, jiraRoot, "OLD-2", "10002", "OLD/OLD-2.wiki", []byte("jira body"), map[string]any{
		"summary": "Target", "project": map[string]any{"key": "OLD"}, "security": map[string]any{"id": "1"},
	})
	confluenceRoot := t.TempDir()
	csfBody := []byte(`<p><ac:structured-macro ac:name="jira"><ac:parameter ac:name="key">OLD-2</ac:parameter></ac:structured-macro></p>`)
	seedCorpusExportConfluence(t, confluenceRoot, "20002", "DOC/page/page.csf", csfBody, mirror.Meta{
		ID: "20002", Title: "Page", Space: "DOC", Version: 1,
	})

	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := ExportCorpus(context.Background(), CorpusExportOptions{
		JiraRoot: jiraRoot, ConfluenceRoot: confluenceRoot, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateUnknown,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projection.Qualifications) != 2 || result.Generation.Services[0] != corpus.ServiceConfluence || result.Generation.Services[1] != corpus.ServiceJira {
		t.Fatalf("mixed result = %#v", result)
	}

	store, _ := corpus.Open(storeRoot, corpus.Options{})
	selected, _ := store.SelectCurrent(context.Background())
	var documentsBytes bytes.Buffer
	if _, err := selected.CopyMember(context.Background(), corpus.ServiceAggregate, corpusDocumentsStableID, corpus.RoleDocument, &documentsBytes); err != nil {
		t.Fatal(err)
	}
	documents, err := corpus.ParseIndexerDocuments(documentsBytes.Bytes(), corpus.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var page, issue corpus.IndexerDocument
	for _, document := range documents {
		switch document.Kind {
		case corpus.ObjectPage:
			page = document
		case corpus.ObjectIssue:
			issue = document
		}
	}
	if page.ID == "" || issue.ID == "" || !strings.Contains(page.Text, "../jira/"+issue.ID+".md") {
		t.Fatalf("cross-service documents = %#v", documents)
	}
	_ = selected.Close()
	_ = store.Close()
}

func TestExportCorpusStableIdentitySurvivesKeyTitleAndPathChanges(t *testing.T) {
	jiraBefore := t.TempDir()
	seedCorpusExportJira(t, jiraBefore, "OLD-1", "10001", "OLD/OLD-1.wiki", []byte("body"), map[string]any{
		"summary": "Issue", "project": map[string]any{"key": "OLD"},
	})
	jiraAfter := t.TempDir()
	seedCorpusExportJira(t, jiraAfter, "NEW-1", "10001", "NEW/moved/NEW-1.wiki", []byte("body"), map[string]any{
		"summary": "Issue", "project": map[string]any{"key": "NEW"},
	})
	beforeIssue := exportSingleCorpusDocument(t, corpus.ServiceJira, jiraBefore, corpus.ObjectIssue)
	afterIssue := exportSingleCorpusDocument(t, corpus.ServiceJira, jiraAfter, corpus.ObjectIssue)
	if beforeIssue.ID != afterIssue.ID || beforeIssue.Key == afterIssue.Key || beforeIssue.Source.Path == afterIssue.Source.Path {
		t.Fatalf("Jira rename identity before=%#v after=%#v", beforeIssue, afterIssue)
	}

	confluenceBefore := t.TempDir()
	seedCorpusExportConfluence(t, confluenceBefore, "20001", "DOC/old/old.csf", []byte("<p>body</p>"), mirror.Meta{
		ID: "20001", Title: "Old title", Space: "DOC", Version: 1,
	})
	confluenceAfter := t.TempDir()
	seedCorpusExportConfluence(t, confluenceAfter, "20001", "DOC/new/moved.csf", []byte("<p>body</p>"), mirror.Meta{
		ID: "20001", Title: "New title", Space: "DOC", Version: 2,
	})
	beforePage := exportSingleCorpusDocument(t, corpus.ServiceConfluence, confluenceBefore, corpus.ObjectPage)
	afterPage := exportSingleCorpusDocument(t, corpus.ServiceConfluence, confluenceAfter, corpus.ObjectPage)
	if beforePage.ID != afterPage.ID || beforePage.Title == afterPage.Title || beforePage.Source.Path == afterPage.Source.Path {
		t.Fatalf("Confluence rename identity before=%#v after=%#v", beforePage, afterPage)
	}
}

func TestExportCorpusRefusesUnreconciledByDefault(t *testing.T) {
	root := t.TempDir()
	state := seedCorpusExportJira(t, root, "EX-1", "10001", "EX/EX-1.wiki", []byte("base"), map[string]any{"summary": "one"})
	if err := mirror.New(root).RecordStaged([]mirror.StagedContent{{ID: state.ID, Path: state.Path, Body: []byte("edit"), BaseHash: state.Hash}}); err != nil {
		t.Fatal(err)
	}
	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := ExportCorpus(context.Background(), CorpusExportOptions{
		JiraRoot: root, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateUnknown,
	})
	if err == nil {
		t.Fatal("unreconciled export unexpectedly succeeded")
	}
	result, err := ExportCorpus(context.Background(), CorpusExportOptions{
		JiraRoot: root, StoreRoot: storeRoot, InitializeStore: true,
		AllowUnreconciled: true, GeneratorVersion: "test-v1", BuildState: corpus.BuildStateUnknown,
	})
	if err != nil {
		t.Fatalf("diagnostic unreconciled export: %v", err)
	}
	if result.Projection.Readiness != corpus.ProjectionPartial || len(result.Projection.Qualifications) != 1 ||
		len(result.Projection.Qualifications[0].Reasons) != 2 ||
		result.Projection.Qualifications[0].Reasons[1] != corpus.QualificationUnreconciled {
		t.Fatalf("diagnostic qualification = %#v", result.Projection)
	}
}

func TestExportCorpusProjectsQualifiedConfluenceCommentThreadWithoutDuplicatingPageBody(t *testing.T) {
	confluenceRoot := t.TempDir()
	metadata := mirror.Meta{
		ID: "20002", Title: "Page", Space: "DOC", Version: 3, CommentsPulled: true,
		CommentCount: 2, CommentSidecarVersion: mirror.ConfluenceCommentsSidecarSchemaVersion,
	}
	seedCorpusExportConfluence(t, confluenceRoot, "20002", "DOC/page/page.csf", []byte("<p>page only</p>"), metadata)
	rootID := "21001"
	replyID := "21002"
	sidecar := mirror.ConfluenceCommentsSidecarV2{
		SchemaVersion: mirror.ConfluenceCommentsSidecarSchemaVersion, PageID: "20002", PageVersion: 3,
		Complete: true, CommentsComplete: true, ThreadsComplete: true, AnchorsComplete: true,
		Count: 2, RootCount: 1, PartialReasons: []string{}, Capabilities: completeCommentCapabilities(),
		Comments: []mirror.ConfluenceCommentsSidecarComment{
			{
				ID: rootID, PageID: "20002", RootID: &rootID, Relation: domain.ConfluenceCommentRelationRoot,
				Location: domain.ConfluenceCommentLocationFooter, Resolution: domain.ConfluenceCommentResolutionOpen,
				Version: 1, UpdatedAt: "2026-08-09T10:00:00Z", Body: "root fallback", BodyStorage: "<p>root body</p>",
			},
			{
				ID: replyID, PageID: "20002", ParentID: &rootID, RootID: &rootID, Relation: domain.ConfluenceCommentRelationReply,
				Location: domain.ConfluenceCommentLocationFooter, Resolution: domain.ConfluenceCommentResolutionOpen,
				Version: 2, UpdatedAt: "2026-08-09T11:00:00Z", Body: "reply fallback", BodyStorage: "<p>reply body</p>",
			},
		},
		Diagnostics: []mirror.ConfluenceCommentsSidecarDiagnostic{},
	}
	encoded, err := mirror.EncodeConfluenceCommentsSidecarV2(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	writeCorpusExportFile(t, confluenceRoot, "DOC/page/page.comments.json", encoded)

	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := ExportCorpus(context.Background(), CorpusExportOptions{
		ConfluenceRoot: confluenceRoot, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Projection.Counts.Documents != 3 || result.Projection.Counts.MarkdownFiles != 3 {
		t.Fatalf("projection counts = %#v", result.Projection.Counts)
	}
	documents, edges := readCorpusExportProjection(t, storeRoot, corpus.ServiceConfluence)
	owners, replies := 0, 0
	for _, edge := range edges {
		switch edge.Relation {
		case corpus.EdgeCommentOwner:
			owners++
		case corpus.EdgeCommentReply:
			replies++
		}
	}
	for _, document := range documents {
		if document.Kind == corpus.ObjectPage && strings.Contains(document.Text, "root body") {
			t.Fatalf("page body duplicates comment content: %#v", document)
		}
		if document.Kind == corpus.ObjectComment && !strings.HasSuffix(document.Source.Path, ".comments.json") {
			t.Fatalf("comment lineage does not name its authoritative sidecar: %#v", document.Source)
		}
	}
	if owners != 2 || replies != 1 {
		t.Fatalf("comment edges owners=%d replies=%d: %#v", owners, replies, edges)
	}
}

func TestExportCorpusKeepsConfluenceRenderFailureExplicit(t *testing.T) {
	root := t.TempDir()
	seedCorpusExportConfluence(t, root, "20002", "DOC/page/page.csf", []byte("<p>broken"), mirror.Meta{
		ID: "20002", Title: "Page", Space: "DOC", Version: 1,
	})
	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := ExportCorpus(context.Background(), CorpusExportOptions{
		ConfluenceRoot: root, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Projection.Counts.Documents != 1 || result.Projection.Counts.MarkdownFiles != 0 || result.Generation.Totals.Members != 5 {
		t.Fatalf("render-failed counts = %#v / %#v", result.Projection.Counts, result.Generation.Totals)
	}
	documents, _ := readCorpusExportProjection(t, storeRoot, corpus.ServiceConfluence)
	if documents[0].RenderStatus != corpus.RenderFailed || documents[0].Evidence[1].Status != corpus.EvidenceUnavailable ||
		documents[0].Evidence[5].Status != corpus.EvidenceUnavailable {
		t.Fatalf("render-failed document = %#v", documents[0])
	}
}

func TestCorpusExportFailureIsContentFree(t *testing.T) {
	const private = "PRIVATE-CANARY /private/path"
	err := corpusExportFailure("project pristine mirror evidence", errors.New(private))
	if err == nil || strings.Contains(err.Error(), private) || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("content-free failure = %v", err)
	}
	if err := corpusExportFailure("project pristine mirror evidence", context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation classification = %v", err)
	}
	ambiguous := corpusExportFailure("seal generation", errors.Join(corpus.ErrOutcomeUnknown, errors.New(private)))
	if !errors.Is(ambiguous, corpus.ErrOutcomeUnknown) || !errors.Is(ambiguous, domain.ErrCheckFailed) || strings.Contains(ambiguous.Error(), private) {
		t.Fatalf("content-free ambiguous failure = %v", ambiguous)
	}
	ambiguousCanceled := corpusExportFailure("seal generation", errors.Join(corpus.ErrOutcomeUnknown, context.Canceled, errors.New(private)))
	if !errors.Is(ambiguousCanceled, corpus.ErrOutcomeUnknown) || !errors.Is(ambiguousCanceled, context.Canceled) ||
		!errors.Is(ambiguousCanceled, domain.ErrCheckFailed) || strings.Contains(ambiguousCanceled.Error(), private) {
		t.Fatalf("content-free canceled ambiguous failure = %v", ambiguousCanceled)
	}
}

func readCorpusExportProjection(t *testing.T, storeRoot string, service corpus.Service) ([]corpus.IndexerDocument, []corpus.IndexerEdge) {
	t.Helper()
	store, err := corpus.Open(storeRoot, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	selected, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = selected.Close() }()
	var documentBytes, edgeBytes bytes.Buffer
	if _, err := selected.CopyMember(context.Background(), service, corpusDocumentsStableID, corpus.RoleDocument, &documentBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.CopyMember(context.Background(), service, corpusEdgesStableID, corpus.RoleEdges, &edgeBytes); err != nil {
		t.Fatal(err)
	}
	documents, err := corpus.ParseIndexerDocuments(documentBytes.Bytes(), corpus.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	edges, err := corpus.ParseIndexerEdges(edgeBytes.Bytes(), corpus.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return documents, edges
}

func exportSingleCorpusDocument(t *testing.T, service corpus.Service, mirrorRoot string, kind corpus.ObjectKind) corpus.IndexerDocument {
	t.Helper()
	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	options := CorpusExportOptions{
		StoreRoot: storeRoot, InitializeStore: true, GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
	}
	if service == corpus.ServiceJira {
		options.JiraRoot = mirrorRoot
	} else {
		options.ConfluenceRoot = mirrorRoot
	}
	if _, err := ExportCorpus(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	documents, _ := readCorpusExportProjection(t, storeRoot, service)
	for _, document := range documents {
		if document.Kind == kind {
			return document
		}
	}
	t.Fatalf("no %s document in %#v", kind, documents)
	return corpus.IndexerDocument{}
}

func seedCorpusExportJira(t *testing.T, root, key, providerID, relative string, body []byte, fields map[string]any) mirror.SyncState {
	t.Helper()
	m := mirror.New(root)
	bindCorpusExportBackend(t, m, mirror.CorpusSnapshotJira)
	state := mirror.SyncState{ID: key, Version: 0, Hash: mirror.Hash(body), Path: relative}
	if err := m.SaveBaseExt(key, body, ".wiki"); err != nil {
		t.Fatal(err)
	}
	writeCorpusExportJSON(t, root, strings.TrimSuffix(relative, ".wiki")+".json", map[string]any{"key": key, "id": providerID, "fields": fields})
	recordCorpusExportState(t, m, state)
	return state
}

func seedCorpusExportConfluence(t *testing.T, root, providerID, relative string, body []byte, metadata mirror.Meta) mirror.SyncState {
	t.Helper()
	m := mirror.New(root)
	bindCorpusExportBackend(t, m, mirror.CorpusSnapshotConfluence)
	state := mirror.SyncState{ID: providerID, Version: metadata.Version, Hash: mirror.Hash(body), Path: relative}
	metadata.Hash = state.Hash
	if err := m.SaveBaseExt(providerID, body, ".csf"); err != nil {
		t.Fatal(err)
	}
	writeCorpusExportJSON(t, root, strings.TrimSuffix(relative, ".csf")+".meta.json", metadata)
	recordCorpusExportState(t, m, state)
	return state
}

func bindCorpusExportBackend(t *testing.T, m *mirror.Mirror, service string) {
	t.Helper()
	origin, err := backendid.OriginSHA256("https://backend.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.BindBackend(mirror.BackendBinding{Service: service, OriginSHA256: origin}); err != nil {
		t.Fatal(err)
	}
}

func recordCorpusExportState(t *testing.T, m *mirror.Mirror, state mirror.SyncState) {
	t.Helper()
	batch, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	batch.Record(state)
	if err := batch.Flush(); err != nil {
		t.Fatal(err)
	}
}

func corpusExportCaptureReceipt(t *testing.T, service corpus.Service, root string) corpus.CaptureReceipt {
	return corpusExportCaptureReceiptWithDimensions(t, service, root, []corpus.CaptureDimensionEvidence{
		{Dimension: corpus.CaptureNative, State: corpus.CaptureComplete},
		{Dimension: corpus.CaptureMetadata, State: corpus.CaptureComplete},
		{Dimension: corpus.CaptureComments, State: corpus.CaptureNotRequested},
		{Dimension: corpus.CaptureAttachments, State: corpus.CaptureNotRequested},
	})
}

func corpusExportCaptureReceiptWithDimensions(t *testing.T, service corpus.Service, root string, dimensions []corpus.CaptureDimensionEvidence) corpus.CaptureReceipt {
	t.Helper()
	snapshot, err := mirror.New(root).BeginCorpusSnapshot(string(service), mirror.CorpusSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	providerIDs := make([]string, 0, snapshot.Len())
	for _, item := range snapshot.Inventory() {
		providerIDs = append(providerIDs, item.ProviderID)
	}
	selection, err := corpus.CaptureSelectionDigest(service, providerIDs)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := corpus.PrincipalScopeDigest(service, snapshot.OriginSHA256(), "synthetic-user")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	receipt, err := corpus.BuildCaptureReceipt(corpus.CaptureReceiptInput{
		Service: service, ScopeDigest: scope,
		SelectorDigest: strings.Repeat("a", 64), OptionsDigest: strings.Repeat("b", 64),
		SelectionDigest: selection, SnapshotDigest: snapshot.Fingerprint(),
		StartedAt: started, CompletedAt: started.Add(time.Minute),
		Total: snapshot.Len(), Completed: snapshot.Len(), Usage: corpus.CaptureUsage{Attempts: 3, ResponseBytes: 1024},
		Dimensions: dimensions,
	}, corpus.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func writeCorpusExportJSON(t *testing.T, root, relative string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeCorpusExportFile(t, root, relative, append(data, '\n'))
}

func writeCorpusExportFile(t *testing.T, root, relative string, data []byte) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
