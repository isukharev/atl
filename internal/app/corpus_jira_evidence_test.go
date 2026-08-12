package app

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		})
	}
}
