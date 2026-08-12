package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type jiraCorpusEvidenceTracker struct {
	*jiraCompleteTracker
	comments        domain.JiraCommentInventory
	attachments     domain.JiraAttachmentInventory
	commentErr      error
	attachmentErr   error
	body            string
	commentReads    int
	attachmentReads int
	bodyReads       int
	driftAfterRead  bool
}

func (tracker *jiraCorpusEvidenceTracker) ListJiraCommentsQualified(_ context.Context, issueID string, options domain.JiraCommentReadOptions) (domain.JiraCommentInventory, error) {
	tracker.commentReads++
	if (issueID != "9" && issueID != "10") || options.MaxPages != 2 || options.MaxItems != 10 {
		return domain.JiraCommentInventory{}, errors.New("unexpected comment qualification")
	}
	inventory := tracker.comments
	inventory.Comments = append([]domain.Comment{}, inventory.Comments...)
	if issueID == "10" && len(inventory.Comments) > 0 {
		inventory.Comments[0].ID = "6"
	}
	return inventory, tracker.commentErr
}

func (tracker *jiraCorpusEvidenceTracker) ListJiraAttachmentsQualified(_ context.Context, issueID string, options domain.JiraAttachmentReadOptions) (domain.JiraAttachmentInventory, error) {
	tracker.attachmentReads++
	if (issueID != "9" && issueID != "10") || options.MaxItems != 10 {
		return domain.JiraAttachmentInventory{}, errors.New("unexpected attachment qualification")
	}
	inventory := tracker.attachments
	inventory.Attachments = append([]domain.Attachment{}, inventory.Attachments...)
	if issueID == "10" && len(inventory.Attachments) > 0 {
		inventory.Attachments[0].ID = "8"
		inventory.Attachments[0].DownPath = "/secure/attachment/8/a.bin"
	}
	return inventory, tracker.attachmentErr
}

func (tracker *jiraCorpusEvidenceTracker) StreamAttachment(_ context.Context, path string) (io.ReadCloser, error) {
	tracker.bodyReads++
	if path != "/secure/attachment/7/a.bin" && path != "/secure/attachment/8/a.bin" {
		return nil, errors.New("unexpected attachment reference")
	}
	return io.NopCloser(strings.NewReader(tracker.body)), nil
}

func (tracker *jiraCorpusEvidenceTracker) GetIssue(ctx context.Context, key string, fields []string) (*domain.Issue, error) {
	issue, err := tracker.jiraCompleteTracker.GetIssue(ctx, key, fields)
	if err == nil && tracker.driftAfterRead && tracker.commentReads > 0 && len(fields) == 1 && fields[0] == "updated" {
		issue.Fields = maps.Clone(issue.Fields)
		issue.Fields["updated"] = "2026-01-02"
	}
	return issue, err
}

func newJiraCorpusEvidenceTracker() *jiraCorpusEvidenceTracker {
	base := newCompleteJiraTracker()
	for _, issue := range base.getIssues {
		issue.Fields["updated"] = "2026-01-01"
	}
	return &jiraCorpusEvidenceTracker{
		jiraCompleteTracker: base,
		comments: domain.JiraCommentInventory{
			Complete: true, Total: 1, TotalKnown: true, PageCount: 1,
			Comments: []domain.Comment{{ID: "5", AuthorKey: "stable", Created: "2026-01-01", Body: "comment body"}},
		},
		attachments: domain.JiraAttachmentInventory{Complete: true, Attachments: []domain.Attachment{{
			ID: "7", Title: "a.bin", MediaType: "application/octet-stream", FileSize: 3,
			Created: "2026-01-01", AuthorKey: "stable", DownPath: "/secure/attachment/7/a.bin",
		}}},
		body: "abc",
	}
}

func TestJiraCorpusEvidenceReaderQualificationMatrix(t *testing.T) {
	legacy := &JiraService{tr: newCompleteJiraTracker(), baseURL: jiraMirrorTestBackendURL}
	if _, err := legacy.captureJiraCorpusComments(t.Context(), "9", "revision", jiraCorpusEvidenceOptions(false)); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("strict legacy comments error=%v", err)
	}
	comments, err := legacy.captureJiraCorpusComments(t.Context(), "9", "revision", jiraCorpusEvidenceOptions(true))
	if err != nil || comments.Complete || comments.PartialReason != mirror.JiraCommentsPartialUnsupported || comments.Comments == nil {
		t.Fatalf("comments=%+v error=%v", comments, err)
	}
	if _, err := legacy.readJiraCorpusAttachments(t.Context(), "9", jiraCorpusEvidenceOptions(false)); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("strict legacy attachments error=%v", err)
	}
	attachments, err := legacy.readJiraCorpusAttachments(t.Context(), "9", jiraCorpusEvidenceOptions(true))
	if err != nil || attachments.Complete || attachments.PartialReason != mirror.AttachmentInventoryUnsupported || attachments.Attachments == nil {
		t.Fatalf("attachments=%+v error=%v", attachments, err)
	}

	tracker := newJiraCorpusEvidenceTracker()
	tracker.commentErr = domain.ErrForbidden
	tracker.attachmentErr = domain.ErrForbidden
	service := &JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}
	comments, err = service.captureJiraCorpusComments(t.Context(), "9", "revision", jiraCorpusEvidenceOptions(true))
	if err != nil || comments.PartialReason != mirror.JiraCommentsPartialForbidden {
		t.Fatalf("forbidden comments=%+v error=%v", comments, err)
	}
	attachments, err = service.readJiraCorpusAttachments(t.Context(), "9", jiraCorpusEvidenceOptions(true))
	if err != nil || attachments.PartialReason != mirror.AttachmentInventoryForbidden {
		t.Fatalf("forbidden attachments=%+v error=%v", attachments, err)
	}
	if _, err := service.captureJiraCorpusComments(t.Context(), "9", "revision", jiraCorpusEvidenceOptions(false)); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("strict forbidden comments error=%v", err)
	}
	if _, err := service.readJiraCorpusAttachments(t.Context(), "9", jiraCorpusEvidenceOptions(false)); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("strict forbidden attachments error=%v", err)
	}
}

func TestCaptureJiraCorpusCommentsNormalizesReplyRootAndRejectsIncomplete(t *testing.T) {
	tracker := newJiraCorpusEvidenceTracker()
	tracker.comments.Comments[0].ParentID = "0"
	service := &JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}
	comments, err := service.captureJiraCorpusComments(t.Context(), "9", "revision", jiraCorpusEvidenceOptions(false))
	if err != nil || !comments.Complete || len(comments.Comments) != 1 || comments.Comments[0].ParentID != "" {
		t.Fatalf("comments=%+v error=%v", comments, err)
	}
	tracker.comments.Complete = false
	tracker.comments.PartialReason = domain.JiraCommentPartialItemLimit
	if _, err := service.captureJiraCorpusComments(t.Context(), "9", "revision", jiraCorpusEvidenceOptions(false)); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("incomplete comments error=%v", err)
	}
}

func jiraCorpusEvidenceOptions(allowPartial bool) *corpusPullEvidenceOptions {
	options := CorpusBuildOptions{
		Comments: true, MaxCommentPagesPerItem: 2, MaxCommentsPerItem: 10,
		Attachments: true, MaxAttachmentPagesPerItem: 2, MaxAttachmentsPerItem: 10,
		AttachmentBodies: true, AttachmentMediaTypes: []string{"application/octet-stream"},
		MaxAttachmentBytes: 16, MaxTotalAttachmentBytes: 64, AllowPartialEvidence: allowPartial,
	}
	return newCorpusPullEvidenceOptions(options)
}

func TestJiraCompletePullPublishesQualifiedCorpusEvidence(t *testing.T) {
	root := t.TempDir()
	tracker := newJiraCorpusEvidenceTracker()
	result, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).Pull(t.Context(), JiraPullOpts{
		Complete: true, Project: "PROJ", MaxIssues: 2, Into: root,
		exactFields: []string{"summary", "description", "project", "updated", "issuelinks"},
		evidence:    jiraCorpusEvidenceOptions(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete == nil || !result.Complete.Complete || tracker.commentReads != 2 || tracker.attachmentReads != 2 || tracker.bodyReads != 2 {
		t.Fatalf("result=%+v reads=%d/%d/%d", result.Complete, tracker.commentReads, tracker.attachmentReads, tracker.bodyReads)
	}
	for _, key := range []string{"PROJ-9", "PROJ-10"} {
		stem := filepath.Join(root, "PROJ", key)
		comments, err := os.ReadFile(stem + ".comments.json")
		if err != nil {
			t.Fatal(err)
		}
		decodedComments, err := mirror.DecodeJiraCommentsSidecarV1(comments)
		if err != nil || !decodedComments.Complete || decodedComments.ParentRevision != "2026-01-01" {
			t.Fatalf("comments=%+v error=%v", decodedComments, err)
		}
		attachments, err := os.ReadFile(stem + ".attachments.json")
		if err != nil {
			t.Fatal(err)
		}
		decodedAttachments, err := mirror.DecodeAttachmentSidecarV1(attachments)
		if err != nil || !decodedAttachments.Complete || decodedAttachments.Attachments[0].Body.State != mirror.AttachmentBodyCaptured {
			t.Fatalf("attachments=%+v error=%v", decodedAttachments, err)
		}
		bodyPath := filepath.Join(root, filepath.FromSlash(decodedAttachments.Attachments[0].Body.Path))
		body, err := os.ReadFile(bodyPath)
		info, statErr := os.Stat(bodyPath)
		mode := os.FileMode(0)
		if statErr == nil {
			mode = info.Mode()
		}
		if err != nil || statErr != nil || string(body) != "abc" || mode.Perm() != 0o600 {
			t.Fatalf("body=%q read=%v stat=%v mode=%v", body, err, statErr, mode)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".atl", "corpus-attachment-staging", "9", "7.body")); !os.IsNotExist(err) {
		t.Fatalf("private staging residue: %v", err)
	}
}

func TestJiraCorpusEvidenceParentDriftPreventsPublication(t *testing.T) {
	root := t.TempDir()
	tracker := newJiraCorpusEvidenceTracker()
	tracker.driftAfterRead = true
	_, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).Pull(t.Context(), JiraPullOpts{
		Complete: true, Project: "PROJ", MaxIssues: 2, Into: root,
		exactFields: []string{"summary", "description", "project", "updated", "issuelinks"},
		evidence:    jiraCorpusEvidenceOptions(false),
	})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "PROJ", "PROJ-9.comments.json")); !os.IsNotExist(statErr) {
		t.Fatalf("drift published evidence: %v", statErr)
	}
}

func TestJiraCorpusEvidencePartialPolicyIsExplicit(t *testing.T) {
	for _, allowPartial := range []bool{false, true} {
		t.Run(map[bool]string{false: "strict", true: "partial"}[allowPartial], func(t *testing.T) {
			root := t.TempDir()
			tracker := newJiraCorpusEvidenceTracker()
			tracker.comments = domain.JiraCommentInventory{
				Comments: []domain.Comment{{ID: "5", Body: "prefix"}}, Total: 2, TotalKnown: true,
				PageCount: 1, PartialReason: domain.JiraCommentPartialPageLimit,
			}
			result, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).Pull(t.Context(), JiraPullOpts{
				Complete: true, Project: "PROJ", MaxIssues: 2, Into: root,
				exactFields: []string{"summary", "description", "project", "updated", "issuelinks"},
				evidence:    jiraCorpusEvidenceOptions(allowPartial),
			})
			if !allowPartial {
				if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.Complete == nil || result.Complete.Complete {
					t.Fatalf("strict result=%+v error=%v", result, err)
				}
				return
			}
			if err != nil || result.Complete == nil || !result.Complete.Complete {
				t.Fatalf("partial result=%+v error=%v", result, err)
			}
			data, readErr := os.ReadFile(filepath.Join(root, "PROJ", "PROJ-9.comments.json"))
			decoded, decodeErr := mirror.DecodeJiraCommentsSidecarV1(data)
			if readErr != nil || decodeErr != nil || decoded.Complete || decoded.PartialReason != domain.JiraCommentPartialPageLimit {
				t.Fatalf("decoded=%+v read=%v decode=%v", decoded, readErr, decodeErr)
			}
			capture := corpusExportCaptureReceiptWithDimensions(t, corpus.ServiceJira, root, []corpus.CaptureDimensionEvidence{
				{Dimension: corpus.CaptureNative, State: corpus.CaptureComplete},
				{Dimension: corpus.CaptureMetadata, State: corpus.CaptureComplete},
				{Dimension: corpus.CaptureComments, State: corpus.CapturePartial},
				{Dimension: corpus.CaptureAttachments, State: corpus.CaptureComplete},
			})
			storeRoot := t.TempDir()
			if err := os.Chmod(storeRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			exported, err := ExportCorpus(t.Context(), CorpusExportOptions{
				JiraRoot: root, StoreRoot: storeRoot, InitializeStore: true,
				GeneratorVersion: "test-v2", BuildState: corpus.BuildStateClean,
				CaptureReceipts: []corpus.CaptureReceipt{capture},
			})
			if err != nil || exported.Projection.Readiness != corpus.ProjectionPartial ||
				len(exported.Projection.Qualifications) != 1 || exported.Projection.Qualifications[0].State != corpus.QualificationPartial ||
				len(exported.Projection.Qualifications[0].Reasons) != 1 || exported.Projection.Qualifications[0].Reasons[0] != corpus.QualificationIncompletePull {
				t.Fatalf("partial projection=%#v error=%v", exported, err)
			}
		})
	}
}

func TestJiraQualifiedEvidenceProjectsIndexerV2Artifacts(t *testing.T) {
	mirrorRoot := t.TempDir()
	tracker := newJiraCorpusEvidenceTracker()
	_, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).Pull(t.Context(), JiraPullOpts{
		Complete: true, Project: "PROJ", MaxIssues: 2, Into: mirrorRoot,
		exactFields: []string{"summary", "description", "project", "updated", "issuelinks"},
		evidence:    jiraCorpusEvidenceOptions(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	capture := corpusExportCaptureReceiptWithDimensions(t, corpus.ServiceJira, mirrorRoot, []corpus.CaptureDimensionEvidence{
		{Dimension: corpus.CaptureNative, State: corpus.CaptureComplete},
		{Dimension: corpus.CaptureMetadata, State: corpus.CaptureComplete},
		{Dimension: corpus.CaptureComments, State: corpus.CaptureComplete},
		{Dimension: corpus.CaptureAttachments, State: corpus.CaptureComplete},
	})
	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := ExportCorpus(t.Context(), CorpusExportOptions{
		JiraRoot: mirrorRoot, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v2", BuildState: corpus.BuildStateClean,
		CaptureReceipts: []corpus.CaptureReceipt{capture},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != corpus.IndexerSchemaV2 || result.Projection.Readiness != corpus.ProjectionReady ||
		result.Projection.Counts.Documents != 6 || result.Projection.Counts.Artifacts != 2 || result.Projection.Counts.ArtifactBytes != 6 {
		t.Fatalf("projection=%#v", result)
	}
	documents, edges := readCorpusExportProjection(t, storeRoot, corpus.ServiceJira)
	comments, attachments, owners := 0, 0, 0
	for _, document := range documents {
		switch document.Kind {
		case corpus.ObjectIssue:
			if document.Evidence[0].Status != corpus.EvidenceComplete || document.Evidence[2].Status != corpus.EvidenceComplete {
				t.Fatalf("issue evidence=%#v", document.Evidence)
			}
		case corpus.ObjectComment:
			comments++
			if !strings.HasSuffix(document.Source.Path, ".comments.json") {
				t.Fatalf("comment lineage=%#v", document.Source)
			}
		case corpus.ObjectAttachment:
			attachments++
			if !strings.HasSuffix(document.Source.Path, ".attachments.json") {
				t.Fatalf("attachment lineage=%#v", document.Source)
			}
		}
	}
	for _, edge := range edges {
		if (edge.Relation == corpus.EdgeCommentOwner || edge.Relation == corpus.EdgeAttachmentOwner) && edge.Confidence == corpus.ConfidenceExact {
			owners++
		}
	}
	if comments != 2 || attachments != 2 || owners != 4 {
		t.Fatalf("documents=%#v edges=%#v", documents, edges)
	}
	store, err := corpus.Open(storeRoot, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	selected, err := store.SelectCurrent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = selected.Close() }()
	if selected.Manifest().ProjectionSchema != corpus.IndexerSchemaV2 {
		t.Fatalf("manifest=%#v", selected.Manifest())
	}
	var artifactBytes bytes.Buffer
	if _, err := selected.CopyMember(t.Context(), corpus.ServiceJira, corpusArtifactsStableID, corpus.RoleMetadata, &artifactBytes); err != nil {
		t.Fatal(err)
	}
	artifacts, err := corpus.ParseIndexerArtifacts(artifactBytes.Bytes(), corpus.Limits{})
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("artifacts=%#v error=%v", artifacts, err)
	}
	for _, artifact := range artifacts {
		if artifact.Status != corpus.ArtifactBodyCaptured || artifact.Size != 3 || artifact.DeclaredSize != 3 ||
			!strings.HasPrefix(artifact.Path, "artifacts/jira/") || !strings.HasSuffix(artifact.Source.InventoryPath, ".attachments.json") {
			t.Fatalf("artifact=%#v", artifact)
		}
		var body bytes.Buffer
		if _, err := selected.CopyMember(t.Context(), corpus.ServiceJira, artifact.DocumentID, corpus.RoleAsset, &body); err != nil || body.String() != "abc" {
			t.Fatalf("body=%q error=%v", body.String(), err)
		}
	}
	var receiptBytes bytes.Buffer
	if _, err := selected.CopyMember(t.Context(), corpus.ServiceJira, corpusReceiptV2StableID, corpus.RoleMetadata, &receiptBytes); err != nil {
		t.Fatal(err)
	}
	receipt, err := corpus.ParseIndexerReceiptV2(receiptBytes.Bytes(), corpus.Limits{})
	if err != nil || receipt.ArtifactsDigest != result.Projection.ArtifactsDigest {
		t.Fatalf("receipt=%#v error=%v", receipt, err)
	}
	var legacyReceiptBytes bytes.Buffer
	if _, err := selected.CopyMember(t.Context(), corpus.ServiceJira, corpusReceiptStableID, corpus.RoleMetadata, &legacyReceiptBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.ParseIndexerReceipt(legacyReceiptBytes.Bytes(), corpus.Limits{}); err != nil {
		t.Fatalf("legacy receipt: %v", err)
	}
}
