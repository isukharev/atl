package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

func jiraAttachmentBodyMaterializationTestRoot(t *testing.T) (*JiraService, *jiraCorpusEvidenceTracker, string) {
	t.Helper()
	root := t.TempDir()
	tracker := newJiraCorpusEvidenceTracker()
	service := &JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}
	if result, err := service.Pull(t.Context(), JiraPullOpts{
		Complete: true, Project: "PROJ", MaxIssues: 2, Into: root,
		Attachments: true, MaxAttachmentsPerItem: 10,
	}); err != nil || result.Complete == nil || !result.Complete.Complete {
		t.Fatalf("inventory pull result=%+v err=%v", result, err)
	}
	return service, tracker, root
}

func TestJiraAttachmentBodyMaterializerResumesBoundedOneBodyTransactions(t *testing.T) {
	service, tracker, root := jiraAttachmentBodyMaterializationTestRoot(t)
	opts := JiraAttachmentBodyMaterializeOpts{
		Into: root, AttachmentMediaTypes: []string{"application/octet-stream"},
		MaxAttachmentBytes: 16, MaxTransactions: 1,
	}
	first, err := service.MaterializeAttachmentBodies(t.Context(), opts)
	if err != nil || first.Captured != 1 || first.Remaining != 1 || first.Complete || tracker.bodyReads != 1 {
		t.Fatalf("first=%+v err=%v body_reads=%d", first, err, tracker.bodyReads)
	}
	opts.MaxTransactions = 10
	second, err := service.MaterializeAttachmentBodies(t.Context(), opts)
	if err != nil || second.Captured != 1 || second.Remaining != 0 || !second.Complete || tracker.bodyReads != 2 {
		t.Fatalf("second=%+v err=%v body_reads=%d", second, err, tracker.bodyReads)
	}
	for _, path := range []string{
		filepath.Join(root, "PROJ", "PROJ-9.attachments", "7.body"),
		filepath.Join(root, "PROJ", "PROJ-10.attachments", "8.body"),
	} {
		if info, statErr := os.Stat(path); statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("body %s info=%v err=%v", path, info, statErr)
		}
	}
	if _, verifyErr := mirror.New(root).JiraAttachmentBodyInventories(); verifyErr != nil {
		t.Fatalf("final materialization verification: %v", verifyErr)
	}
}

func TestJiraAttachmentBodyMaterializerRejectsUncoveredInventoryBeforeBodyRead(t *testing.T) {
	service, tracker, root := jiraAttachmentBodyMaterializationTestRoot(t)
	_, err := service.MaterializeAttachmentBodies(t.Context(), JiraAttachmentBodyMaterializeOpts{
		Into: root, AttachmentMediaTypes: []string{"text/plain"}, MaxAttachmentBytes: 16, MaxTransactions: 1,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || tracker.bodyReads != 0 {
		t.Fatalf("error=%v body_reads=%d", err, tracker.bodyReads)
	}
}

func TestValidateJiraAttachmentBodyMaterializeOptsRequiresStrictBounds(t *testing.T) {
	valid := JiraAttachmentBodyMaterializeOpts{Into: "mirror", AttachmentMediaTypes: []string{"application/pdf"}, MaxAttachmentBytes: 16, MaxTransactions: 1}
	if err := ValidateJiraAttachmentBodyMaterializeOpts(valid); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []JiraAttachmentBodyMaterializeOpts{
		{Into: "mirror", AttachmentMediaTypes: []string{"application/*"}, MaxAttachmentBytes: 16, MaxTransactions: 1},
		{Into: "mirror", AttachmentMediaTypes: []string{"Application/pdf"}, MaxAttachmentBytes: 16, MaxTransactions: 1},
		{Into: "mirror", AttachmentMediaTypes: []string{"application/pdf; charset=utf-8"}, MaxAttachmentBytes: 16, MaxTransactions: 1},
		{Into: "mirror", AttachmentMediaTypes: []string{"application/pdf", "application/pdf"}, MaxAttachmentBytes: 16, MaxTransactions: 1},
		{Into: "mirror", AttachmentMediaTypes: []string{"application/pdf"}, MaxAttachmentBytes: mirror.MaxJiraAttachmentBodyMaterializationBytes + 1, MaxTransactions: 1},
		{Into: "mirror", AttachmentMediaTypes: []string{"application/pdf"}, MaxAttachmentBytes: 16, MaxTransactions: mirror.MaxJiraAttachmentBodyMaterializationTransactions + 1},
	} {
		if err := ValidateJiraAttachmentBodyMaterializeOpts(invalid); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("invalid options=%+v error=%v", invalid, err)
		}
	}
}

func TestJiraAttachmentBodyMaterializerRejectsUnavailableLocalContextBeforeBackendRead(t *testing.T) {
	opts := JiraAttachmentBodyMaterializeOpts{
		Into: t.TempDir(), AttachmentMediaTypes: []string{"application/pdf"}, MaxAttachmentBytes: 16, MaxTransactions: 1,
	}
	var unavailable *JiraService
	if _, err := unavailable.MaterializeAttachmentBodies(t.Context(), opts); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("nil service error=%v", err)
	}
	service := &JiraService{}
	//nolint:staticcheck // The public API explicitly rejects a nil context.
	if _, err := service.MaterializeAttachmentBodies(nil, opts); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("nil context error=%v", err)
	}
	if _, err := service.MaterializeAttachmentBodies(context.Background(), JiraAttachmentBodyMaterializeOpts{}); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("invalid options error=%v", err)
	}
	if _, err := service.MaterializeAttachmentBodies(t.Context(), opts); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("uninitialized mirror error=%v", err)
	}
}

func TestJiraAttachmentBodyMaterializerRefusesMisboundOrActiveMirrorBeforeBackendRead(t *testing.T) {
	service, tracker, root := jiraAttachmentBodyMaterializationTestRoot(t)
	opts := JiraAttachmentBodyMaterializeOpts{
		Into: root, AttachmentMediaTypes: []string{"application/octet-stream"}, MaxAttachmentBytes: 16, MaxTransactions: 1,
	}
	wrongBackend := &JiraService{baseURL: "https://wrong.example"}
	if _, err := wrongBackend.MaterializeAttachmentBodies(t.Context(), opts); !errors.Is(err, domain.ErrCheckFailed) || tracker.bodyReads != 0 {
		t.Fatalf("misbound mirror error=%v body_reads=%d", err, tracker.bodyReads)
	}
	active := filepath.Join(root, ".atl", "complete-pulls")
	if err := os.MkdirAll(active, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "active.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.MaterializeAttachmentBodies(t.Context(), opts); !errors.Is(err, domain.ErrCheckFailed) || tracker.bodyReads != 0 {
		t.Fatalf("active complete-pull state error=%v body_reads=%d", err, tracker.bodyReads)
	}
}

func TestJiraAttachmentBodyMaterializationQueueOrdersPendingNumericTargets(t *testing.T) {
	opts := JiraAttachmentBodyMaterializeOpts{
		Into: "mirror", AttachmentMediaTypes: []string{"application/octet-stream"}, MaxAttachmentBytes: 16, MaxTransactions: 1,
	}
	inventories := []mirror.JiraAttachmentBodyInventory{
		{
			Identity: "10", ParentRevision: "2026-01-01", BodiesState: mirror.AttachmentBodiesPartial,
			Attachments: []mirror.AttachmentSidecarRecord{
				{ID: "11", MediaType: "application/octet-stream", DeclaredSize: 3, Body: mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyExcluded, Reason: mirror.AttachmentBodyReasonAggregateLimit}},
				{ID: "3", MediaType: "application/octet-stream", DeclaredSize: 3, Body: mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyExcluded, Reason: mirror.AttachmentBodyReasonAggregateLimit}},
			},
		},
		{
			Identity: "2", ParentRevision: "2026-01-01", BodiesState: mirror.AttachmentBodiesNotRequested,
			Attachments: []mirror.AttachmentSidecarRecord{
				{ID: "7", MediaType: "application/octet-stream", DeclaredSize: 3, Body: mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyNotRequested}},
			},
		},
	}
	targets, err := jiraAttachmentBodyMaterializationQueue(inventories, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 3 {
		t.Fatalf("target count=%d", len(targets))
	}
	if got, want := []jiraAttachmentBodyMaterializationTarget{targets[0], targets[1], targets[2]}, []jiraAttachmentBodyMaterializationTarget{
		{identity: "2", attachmentID: "7"}, {identity: "10", attachmentID: "3"}, {identity: "10", attachmentID: "11"},
	}; got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("targets=%+v want=%+v", targets, want)
	}
	inventories[0].Identity = "not-numeric"
	if _, err := jiraAttachmentBodyMaterializationQueue(inventories, opts); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("invalid inventory error=%v", err)
	}
	inventories[0].Identity = "10"
	inventories[0].ParentRevision = ""
	if _, err := jiraAttachmentBodyMaterializationQueue(inventories, opts); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("invalid parent revision error=%v", err)
	}
}
