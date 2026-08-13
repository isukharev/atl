package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
)

type corpusBuildJiraTracker struct {
	*jiraCompleteTracker
	user        *domain.User
	currentErr  error
	budgeted    bool
	comments    domain.JiraCommentInventory
	attachments domain.JiraAttachmentInventory
	body        string
	bodyReads   int
}

func (tracker *corpusBuildJiraTracker) ListJiraCommentsQualified(ctx context.Context, issueID string, options domain.JiraCommentReadOptions) (domain.JiraCommentInventory, error) {
	if tracker.budgeted {
		if err := consumeCorpusBuildRead(ctx, 17); err != nil {
			return domain.JiraCommentInventory{}, err
		}
	}
	if (issueID != "9" && issueID != "10") || options.MaxPages != 2 || options.MaxItems != 10 {
		return domain.JiraCommentInventory{}, errors.New("unexpected configured fixture comment qualification")
	}
	inventory := tracker.comments
	inventory.Comments = append([]domain.Comment{}, inventory.Comments...)
	if issueID == "10" && len(inventory.Comments) != 0 {
		inventory.Comments[0].ID = "6"
	}
	return inventory, nil
}

func (tracker *corpusBuildJiraTracker) ListJiraAttachmentsQualified(ctx context.Context, issueID string, options domain.JiraAttachmentReadOptions) (domain.JiraAttachmentInventory, error) {
	if tracker.budgeted {
		if err := consumeCorpusBuildRead(ctx, 19); err != nil {
			return domain.JiraAttachmentInventory{}, err
		}
	}
	if (issueID != "9" && issueID != "10") || options.MaxItems != 10 {
		return domain.JiraAttachmentInventory{}, errors.New("unexpected configured fixture attachment qualification")
	}
	inventory := tracker.attachments
	inventory.Attachments = append([]domain.Attachment{}, inventory.Attachments...)
	if issueID == "10" && len(inventory.Attachments) != 0 {
		inventory.Attachments[0].ID = "8"
		inventory.Attachments[0].DownPath = "/secure/attachment/8/a.bin"
	}
	return inventory, nil
}

func (tracker *corpusBuildJiraTracker) StreamAttachment(ctx context.Context, path string) (io.ReadCloser, error) {
	if path != "/secure/attachment/7/a.bin" && path != "/secure/attachment/8/a.bin" {
		return nil, errors.New("unexpected configured fixture attachment reference")
	}
	if tracker.budgeted {
		if err := consumeCorpusBuildRead(ctx, int64(len(tracker.body))); err != nil {
			return nil, err
		}
	}
	tracker.bodyReads++
	return io.NopCloser(strings.NewReader(tracker.body)), nil
}

func (tracker *corpusBuildJiraTracker) CurrentUser(ctx context.Context) (*domain.User, error) {
	if tracker.budgeted {
		if err := consumeCorpusBuildRead(ctx, 7); err != nil {
			return nil, err
		}
	}
	if tracker.currentErr != nil {
		return nil, tracker.currentErr
	}
	if tracker.user == nil {
		return nil, errors.New("missing configured fixture principal")
	}
	copy := *tracker.user
	return &copy, nil
}

func (tracker *corpusBuildJiraTracker) SearchQualified(ctx context.Context, query string, fields []string, limit int, cursor string) (domain.IssueSearchPage, error) {
	if tracker.budgeted {
		if err := consumeCorpusBuildRead(ctx, 11); err != nil {
			return domain.IssueSearchPage{}, err
		}
	}
	return tracker.jiraCompleteTracker.SearchQualified(ctx, query, fields, limit, cursor)
}

func (tracker *corpusBuildJiraTracker) GetIssue(ctx context.Context, key string, fields []string) (*domain.Issue, error) {
	if tracker.budgeted {
		if err := consumeCorpusBuildRead(ctx, 13); err != nil {
			return nil, err
		}
	}
	return tracker.jiraCompleteTracker.GetIssue(ctx, key, fields)
}

type corpusBuildConfluenceStore struct {
	*completePullStore
	identity   domain.ConfluenceUserIdentity
	currentErr error
	budgeted   bool
	pageMu     sync.Mutex
}

func (store *corpusBuildConfluenceStore) CurrentConfluenceUser(ctx context.Context) (domain.ConfluenceUserIdentity, error) {
	if store.budgeted {
		if err := consumeCorpusBuildRead(ctx, 5); err != nil {
			return domain.ConfluenceUserIdentity{}, err
		}
	}
	if store.currentErr != nil {
		return domain.ConfluenceUserIdentity{}, store.currentErr
	}
	return store.identity, nil
}

func (store *corpusBuildConfluenceStore) SearchComplete(ctx context.Context, query string, limit int, cursor string) (domain.PageSearchPage, error) {
	if store.budgeted {
		if err := consumeCorpusBuildRead(ctx, 10); err != nil {
			return domain.PageSearchPage{}, err
		}
	}
	return store.completePullStore.SearchComplete(ctx, query, limit, cursor)
}

func (store *corpusBuildConfluenceStore) GetPage(ctx context.Context, id string, options domain.PullOpts) (*domain.Resource, error) {
	if store.budgeted {
		if err := consumeCorpusBuildRead(ctx, 20); err != nil {
			return nil, err
		}
	}
	store.pageMu.Lock()
	defer store.pageMu.Unlock()
	page, err := store.completePullStore.GetPage(ctx, id, options)
	return page, err
}

func consumeCorpusBuildRead(ctx context.Context, size int64) error {
	budget := domain.ReadBudgetFromContext(ctx)
	if budget == nil {
		return errors.New("missing corpus build read budget")
	}
	if err := budget.TakeAttempt(); err != nil {
		return err
	}
	remaining, finish, err := budget.BeginResponse(ctx)
	if err != nil {
		return err
	}
	if size > remaining {
		finish(remaining)
		return domain.ErrReadResponseBudgetExhausted
	}
	finish(size)
	return nil
}

func TestCorpusBuildPublishesQualifiedCrossServiceGeneration(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	jira := newCorpusBuildJiraFixture(true)
	confluence := newCorpusBuildConfluenceFixture(true)
	service := newCorpusBuildTestService(jira, confluence)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	options.ConfluenceSpace = "DOC"
	options.MaxConfluencePages = 2

	result, err := service.Build(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "new" || result.Reused || result.Projection.Readiness != corpus.ProjectionReady ||
		len(result.Services) != 2 || len(result.Generation.Services) != 2 {
		t.Fatalf("result=%#v", result)
	}
	if result.Usage != (corpus.CaptureUsage{Attempts: 12, ResponseBytes: 142}) {
		t.Fatalf("aggregate usage=%#v", result.Usage)
	}
	if result.Services[0].Service != corpus.ServiceConfluence || result.Services[0].Count != 2 ||
		result.Services[0].Usage != (corpus.CaptureUsage{Attempts: 4, ResponseBytes: 60}) ||
		result.Services[1].Service != corpus.ServiceJira || result.Services[1].Count != 2 ||
		result.Services[1].Usage != (corpus.CaptureUsage{Attempts: 6, ResponseBytes: 70}) {
		t.Fatalf("service results=%#v", result.Services)
	}

	store, err := corpus.Open(root, corpus.Options{Limits: corpusBuildLimits(options)})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := store.SelectCurrent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if selected.Receipt().GenerationDigest != result.Generation.GenerationDigest ||
		len(selected.Manifest().Qualifications) != 2 {
		t.Fatalf("selected=%#v result=%#v", selected.Summary(), result.Generation)
	}
	_ = selected.Close()
	_ = store.Close()

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{root, "PROJ", "DOC", "fixture-jira-principal", "fixture-confluence-principal", "private fixture title"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("content-free result leaked %q: %s", private, encoded)
		}
	}
}

func TestCorpusBuildOneServiceDoesNotFabricateAbsentBackend(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	service := newCorpusBuildTestService(newCorpusBuildJiraFixture(false), nil)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2

	result, err := service.Build(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Services) != 1 || result.Services[0].Service != corpus.ServiceJira ||
		len(result.Generation.Services) != 1 || result.Generation.Services[0] != corpus.ServiceJira ||
		len(result.Projection.Qualifications) != 1 || result.Projection.Qualifications[0].Service != corpus.ServiceJira {
		t.Fatalf("result=%#v", result)
	}
	workspace, err := corpus.OpenBuildWorkspace(t.Context(), root, corpus.Options{Limits: corpusBuildLimits(options)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = workspace.Close() }()
	active, found, err := workspace.LoadActive()
	if err != nil || !found {
		t.Fatalf("active found=%t err=%v", found, err)
	}
	attemptRoot, err := workspace.AttemptRoot(active.AttemptID, corpus.ServiceJira)
	if err != nil {
		t.Fatal(err)
	}
	receipt, found, err := workspace.LoadCaptureReceipt(active.AttemptID, corpus.ServiceJira)
	if err != nil || !found {
		t.Fatalf("receipt found=%t err=%v", found, err)
	}
	expectedOptions, err := service.captureOptionsDigest(corpus.ServiceJira, options)
	validationErr := validateAdoptedCorpusCapture(attemptRoot, active.Services[0], receipt, expectedOptions, active.Deadline, options, corpusBuildLimits(options))
	if err != nil || validationErr != nil {
		t.Fatalf("valid adoption options=%q receipt_options=%q state=%#v receipt=%#v digest_error=%v validation_error=%v", expectedOptions, receipt.OptionsDigest, active.Services[0], receipt, err, validationErr)
	}
	if err := validateAdoptedCorpusCapture(attemptRoot, active.Services[0], receipt, strings.Repeat("f", 64), active.Deadline, options, corpusBuildLimits(options)); !errors.Is(err, corpus.ErrIntegrity) {
		t.Fatalf("mismatched options adoption error=%v", err)
	}
	changedDimensions := receipt
	changedDimensions.Dimensions = append([]corpus.CaptureDimensionEvidence(nil), receipt.Dimensions...)
	for index := range changedDimensions.Dimensions {
		if changedDimensions.Dimensions[index].Dimension == corpus.CaptureComments {
			changedDimensions.Dimensions[index].State = corpus.CaptureComplete
		}
	}
	if err := validateAdoptedCorpusCapture(attemptRoot, active.Services[0], changedDimensions, expectedOptions, active.Deadline, options, corpusBuildLimits(options)); !errors.Is(err, corpus.ErrIntegrity) {
		t.Fatalf("widened evidence adoption error=%v", err)
	}
	deadline, err := time.Parse(time.RFC3339Nano, active.Deadline)
	if err != nil {
		t.Fatal(err)
	}
	started, err := time.Parse(time.RFC3339Nano, receipt.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	late, err := corpus.BuildCaptureReceipt(corpus.CaptureReceiptInput{
		Service: receipt.Service, ScopeDigest: receipt.ScopeDigest, SelectorDigest: receipt.SelectorDigest,
		OptionsDigest: receipt.OptionsDigest, SelectionDigest: receipt.SelectionDigest, SnapshotDigest: receipt.SnapshotDigest,
		StartedAt: started, CompletedAt: deadline.Add(time.Nanosecond), Total: receipt.Total, Completed: receipt.Completed,
		Usage: receipt.Usage, Dimensions: receipt.Dimensions,
	}, corpusBuildLimits(options))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAdoptedCorpusCapture(attemptRoot, active.Services[0], late, expectedOptions, active.Deadline, options, corpusBuildLimits(options)); !errors.Is(err, corpus.ErrIntegrity) {
		t.Fatalf("late receipt adoption error=%v", err)
	}
}

func TestCorpusBuildPublishesQualifiedJiraEvidenceAndArtifacts(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	tracker := newCorpusBuildJiraFixture(true)
	for _, issue := range tracker.getIssues {
		issue.Fields["updated"] = "2026-01-01"
	}
	tracker.comments = domain.JiraCommentInventory{
		Complete: true, Total: 1, TotalKnown: true, PageCount: 1,
		Comments: []domain.Comment{{ID: "5", Body: "configured fixture comment"}},
	}
	tracker.attachments = domain.JiraAttachmentInventory{Complete: true, Attachments: []domain.Attachment{{
		ID: "7", Title: "a.bin", MediaType: "application/octet-stream", FileSize: 3,
		DownPath: "/secure/attachment/7/a.bin",
	}}}
	tracker.body = "abc"
	service := newCorpusBuildTestService(tracker, nil)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject, options.MaxJiraIssues = "PROJ", 2
	options.Comments, options.MaxCommentPagesPerItem, options.MaxCommentsPerItem = true, 2, 10
	options.Attachments, options.MaxAttachmentPagesPerItem, options.MaxAttachmentsPerItem = true, 2, 10
	options.AttachmentBodies = true
	options.AttachmentMediaTypes = []string{"application/octet-stream"}
	options.MaxAttachmentBytes, options.MaxTotalAttachmentBytes = 16, 64

	result, err := service.Build(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Projection.Readiness != corpus.ProjectionReady || result.Projection.Counts.Artifacts != 2 ||
		result.Projection.Counts.ArtifactBytes != 6 || len(result.Services) != 1 ||
		result.Services[0].Dimensions[0] != (corpus.CaptureDimensionEvidence{Dimension: corpus.CaptureAttachments, State: corpus.CaptureComplete}) ||
		result.Services[0].Dimensions[1] != (corpus.CaptureDimensionEvidence{Dimension: corpus.CaptureComments, State: corpus.CaptureComplete}) {
		t.Fatalf("result=%#v", result)
	}
	store, err := corpus.Open(root, corpus.Options{Limits: corpusBuildLimits(options)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	selected, err := store.SelectCurrent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = selected.Close() }()
	assetMembers := 0
	for _, member := range selected.Manifest().Members {
		if member.Role == corpus.RoleAsset {
			assetMembers++
		}
	}
	if assetMembers != 2 {
		t.Fatalf("manifest=%#v", selected.Manifest())
	}
}

func TestCorpusBuildExplicitPartialEvidencePublishesPartialGeneration(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	tracker := newCorpusBuildJiraFixture(true)
	for _, issue := range tracker.getIssues {
		issue.Fields["updated"] = "2026-01-01"
	}
	tracker.comments = domain.JiraCommentInventory{
		Comments: []domain.Comment{{ID: "5", Body: "bounded prefix"}},
		Total:    2, TotalKnown: true, PageCount: 1, PartialReason: domain.JiraCommentPartialPageLimit,
	}
	service := newCorpusBuildTestService(tracker, nil)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject, options.MaxJiraIssues = "PROJ", 2
	options.Comments, options.MaxCommentPagesPerItem, options.MaxCommentsPerItem = true, 2, 10
	options.AllowPartialEvidence = true

	result, err := service.Build(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Projection.Readiness != corpus.ProjectionPartial || len(result.Projection.Qualifications) != 1 ||
		result.Projection.Qualifications[0].State != corpus.QualificationPartial ||
		len(result.Services) != 1 || result.Services[0].Dimensions[1].State != corpus.CapturePartial {
		t.Fatalf("result=%#v", result)
	}
}

func TestCorpusBuildJiraUsesExactMinimalFieldProjection(t *testing.T) {
	options := corpusBuildTestOptions("/synthetic/private-root")
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	pull := corpusBuildJiraPullOptions("/synthetic/attempt", options, newCorpusPullEvidenceOptions(options))
	fields := jiraCompletePullFields(pull, []string{"comment"}, *pull.exactRender)
	if got, want := strings.Join(fields, ","), "summary,description,project,issuelinks"; got != want {
		t.Fatalf("fields=%q want=%q", got, want)
	}
}

func TestCorpusBuildResumesExactAttemptWithoutResettingGuards(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	tracker := newCorpusBuildJiraFixture(true)
	tracker.getErrorAt = "10"
	service := newCorpusBuildTestService(tracker, nil)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2

	if _, err := service.Build(t.Context(), options); err == nil {
		t.Fatal("interrupted capture unexpectedly succeeded")
	}
	tracker.getErrorAt = ""
	options.Initialize = false
	result, err := service.Build(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "resumed" || result.Usage != (corpus.CaptureUsage{Attempts: 9, ResponseBytes: 97}) ||
		result.Services[0].Usage != (corpus.CaptureUsage{Attempts: 7, ResponseBytes: 83}) {
		t.Fatalf("resumed result=%#v", result)
	}
}

func TestCorpusBuildResumePreservesGenerationAttachmentBodyBudget(t *testing.T) {
	setup := func(t *testing.T, partial bool) (string, *corpusBuildJiraTracker, *confluenceCorpusEvidenceStore, *CorpusBuildService, CorpusBuildOptions) {
		t.Helper()
		root := corpusBuildPrivateRoot(t)
		jira := newCorpusBuildJiraFixture(false)
		for _, issue := range jira.getIssues {
			issue.Fields["updated"] = "2026-01-01"
		}
		jira.attachments = domain.JiraAttachmentInventory{Complete: true, Attachments: []domain.Attachment{{
			ID: "7", Title: "a.bin", MediaType: "application/octet-stream", FileSize: 3,
			DownPath: "/secure/attachment/7/a.bin",
		}}}
		jira.body, jira.getErrorAt = "abc", "9"
		confluence := newConfluenceCorpusEvidenceStore()
		service := newCorpusBuildTestService(jira, confluence)
		options := corpusBuildTestOptions(root)
		options.Initialize = true
		options.ConfluenceSpace, options.MaxConfluencePages = "DOC", 1
		options.JiraProject, options.MaxJiraIssues = "PROJ", 2
		options.Attachments, options.MaxAttachmentPagesPerItem, options.MaxAttachmentsPerItem = true, 2, 10
		options.AttachmentBodies = true
		options.AttachmentMediaTypes = []string{"application/octet-stream"}
		options.MaxAttachmentBytes, options.MaxTotalAttachmentBytes = 4, 4
		options.AllowPartialEvidence = partial
		return root, jira, confluence, service, options
	}

	for _, partial := range []bool{false, true} {
		name := "strict"
		if partial {
			name = "explicit-partial"
		}
		t.Run(name, func(t *testing.T) {
			root, jira, confluence, service, options := setup(t, partial)

			if result, err := service.Build(t.Context(), options); result != nil || err == nil {
				t.Fatalf("interrupted result=%#v error=%v", result, err)
			}
			active := loadCorpusBuildActiveForTest(t, root, options)
			if active.SchemaVersion != corpus.BuildActiveSchemaV2 || active.AttachmentBodyBytes != 3 ||
				active.Services[0].Service != corpus.ServiceConfluence || active.Services[0].AttachmentBodyBytes != 3 ||
				active.Services[1].AttachmentBodyBytes != 0 || confluence.bodyReads != 1 || jira.bodyReads != 0 {
				t.Fatalf("interrupted active=%#v body_reads=%d/%d", active, confluence.bodyReads, jira.bodyReads)
			}
			if !partial {
				writeLegacyCorpusBuildActiveForTest(t, root, active)
			}

			jira.getErrorAt = ""
			options.Initialize = false
			result, err := service.Build(t.Context(), options)
			if partial {
				if err != nil || result == nil || result.Source != "resumed" || result.Projection.Readiness != corpus.ProjectionPartial ||
					result.Projection.Counts.ArtifactBytes != 3 {
					t.Fatalf("partial result=%#v error=%v", result, err)
				}
			} else if result != nil || !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("strict result=%#v error=%v", result, err)
			}
			active = loadCorpusBuildActiveForTest(t, root, options)
			if active.SchemaVersion != corpus.BuildActiveSchemaV2 || active.AttachmentBodyBytes != 3 ||
				active.Services[0].AttachmentBodyBytes != 3 || active.Services[1].AttachmentBodyBytes != 0 ||
				confluence.bodyReads != 1 || jira.bodyReads != 0 {
				t.Fatalf("resumed active=%#v body_reads=%d/%d", active, confluence.bodyReads, jira.bodyReads)
			}
			if _, err := os.Stat(filepath.Join(root, "active.v1.json")); !os.IsNotExist(err) {
				t.Fatalf("legacy active path survived migration: %v", err)
			}
		})
	}

	t.Run("repeated-restart", func(t *testing.T) {
		root, jira, confluence, service, options := setup(t, true)
		if result, err := service.Build(t.Context(), options); result != nil || err == nil {
			t.Fatalf("initial result=%#v error=%v", result, err)
		}
		options.Initialize, options.Restart = false, true
		confluence.searchSequence = []domain.PageSearchPage{completeSearchPage("10"), completeSearchPage("10")}
		if result, err := service.Build(t.Context(), options); result != nil || err == nil {
			t.Fatalf("first restart result=%#v error=%v", result, err)
		}
		active := loadCorpusBuildActiveForTest(t, root, options)
		if active.AttachmentBodyBytes != 3 || active.Services[0].AttachmentBodyBytes != 0 ||
			active.Services[1].AttachmentBodyBytes != 0 || confluence.bodyReads != 1 || jira.bodyReads != 0 {
			t.Fatalf("first restart active=%#v body_reads=%d/%d", active, confluence.bodyReads, jira.bodyReads)
		}

		jira.getErrorAt = ""
		confluence.searchSequence = []domain.PageSearchPage{completeSearchPage("10"), completeSearchPage("10")}
		result, err := service.Build(t.Context(), options)
		if err != nil || result == nil || result.Source != "restarted" || result.Projection.Readiness != corpus.ProjectionPartial ||
			result.Projection.Counts.ArtifactBytes != 0 {
			t.Fatalf("second restart result=%#v error=%v", result, err)
		}
		active = loadCorpusBuildActiveForTest(t, root, options)
		if active.AttachmentBodyBytes != 3 || active.Services[0].AttachmentBodyBytes != 0 ||
			active.Services[1].AttachmentBodyBytes != 0 || confluence.bodyReads != 1 || jira.bodyReads != 0 {
			t.Fatalf("second restart active=%#v body_reads=%d/%d", active, confluence.bodyReads, jira.bodyReads)
		}
	})
}

func TestCorpusBuildChargesSuccessfulBodyBeforePublicationFailure(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	confluence := newConfluenceCorpusEvidenceStore()
	confluence.driftAfterRead = true
	service := newCorpusBuildTestService(nil, confluence)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.ConfluenceSpace, options.MaxConfluencePages = "DOC", 1
	options.Attachments, options.MaxAttachmentPagesPerItem, options.MaxAttachmentsPerItem = true, 2, 10
	options.AttachmentBodies = true
	options.AttachmentMediaTypes = []string{"application/octet-stream"}
	options.MaxAttachmentBytes, options.MaxTotalAttachmentBytes = 3, 3

	if result, err := service.Build(t.Context(), options); result != nil || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("initial result=%#v error=%v", result, err)
	}
	active := loadCorpusBuildActiveForTest(t, root, options)
	if active.AttachmentBodyBytes != 3 || active.Services[0].AttachmentBodyBytes != 3 || confluence.bodyReads != 1 {
		t.Fatalf("initial active=%#v body_reads=%d", active, confluence.bodyReads)
	}

	options.Initialize = false
	confluence.searchSequence = []domain.PageSearchPage{completeSearchPage("10"), completeSearchPage("10")}
	if result, err := service.Build(t.Context(), options); result != nil || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("resume result=%#v error=%v", result, err)
	}
	active = loadCorpusBuildActiveForTest(t, root, options)
	if active.AttachmentBodyBytes != 3 || active.Services[0].AttachmentBodyBytes != 3 || confluence.bodyReads != 1 {
		t.Fatalf("resumed active=%#v body_reads=%d", active, confluence.bodyReads)
	}

	options.Restart = true
	confluence.searchSequence = []domain.PageSearchPage{completeSearchPage("10"), completeSearchPage("10")}
	if result, err := service.Build(t.Context(), options); result != nil || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("restart result=%#v error=%v", result, err)
	}
	active = loadCorpusBuildActiveForTest(t, root, options)
	if active.AttachmentBodyBytes != 3 || active.Services[0].AttachmentBodyBytes != 0 || confluence.bodyReads != 1 {
		t.Fatalf("restarted active=%#v body_reads=%d", active, confluence.bodyReads)
	}
}

func TestCorpusBuildResumeRejectsPrincipalScopeDrift(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	tracker := newCorpusBuildJiraFixture(false)
	tracker.getErrorAt = "10"
	confluence := newCorpusBuildConfluenceFixture(false)
	service := newCorpusBuildTestService(tracker, confluence)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.ConfluenceSpace = "DOC"
	options.MaxConfluencePages = 2
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2

	if _, err := service.Build(t.Context(), options); err == nil {
		t.Fatal("interrupted cross-service capture unexpectedly succeeded")
	}
	confluence.identity.ID = "rotated-fixture-principal"
	tracker.getErrorAt = ""
	options.Initialize = false
	result, err := service.Build(t.Context(), options)
	var closed *CorpusBuildError
	if result != nil || !errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &closed) ||
		closed.Phase != CorpusBuildPhasePrincipal || closed.Reason != CorpusBuildReasonDrift {
		t.Fatalf("result=%#v error=%#v", result, err)
	}
}

func TestCorpusBuildRequiresExplicitRestartAfterAmbiguousRemotePhase(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	tracker := newCorpusBuildJiraFixture(false)
	tracker.getErrorAt = "10"
	service := newCorpusBuildTestService(tracker, nil)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	if _, err := service.Build(t.Context(), options); err == nil {
		t.Fatal("interrupted capture unexpectedly succeeded")
	}

	workspace, err := corpus.OpenBuildWorkspace(t.Context(), root, corpus.Options{Limits: corpusBuildLimits(options)})
	if err != nil {
		t.Fatal(err)
	}
	active, found, err := workspace.LoadActive()
	if err != nil || !found {
		t.Fatalf("active found=%t err=%v", found, err)
	}
	active.RemoteInFlight = true
	active.RemoteService = corpus.ServiceJira
	if err := workspace.SaveActive(active); err != nil {
		t.Fatal(err)
	}
	_ = workspace.Close()

	options.Initialize = false
	_, err = service.Build(t.Context(), options)
	var closed *CorpusBuildError
	if !errors.Is(err, corpus.ErrOutcomeUnknown) || !errors.As(err, &closed) ||
		closed.Phase != CorpusBuildPhaseRecover || closed.Reason != CorpusBuildReasonOutcomeUnknown {
		t.Fatalf("ambiguous error=%#v", err)
	}
	tracker.getErrorAt = ""
	options.Restart = true
	result, err := service.Build(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "restarted" || result.Projection.Readiness != corpus.ProjectionReady {
		t.Fatalf("restart result=%#v", result)
	}
}

func TestCorpusBuildRestartBeforeMirrorInitializationAndCompletedRefusal(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	tracker := newCorpusBuildJiraFixture(false)
	tracker.currentErr = errors.New("configured fixture principal unavailable")
	service := newCorpusBuildTestService(tracker, nil)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	if result, err := service.Build(t.Context(), options); result != nil || err == nil {
		t.Fatalf("result=%#v error=%v", result, err)
	}

	tracker.currentErr = nil
	options.Initialize = false
	options.Restart = true
	result, err := service.Build(t.Context(), options)
	if err != nil || result.Source != "restarted" || result.Projection.Readiness != corpus.ProjectionReady {
		t.Fatalf("restart result=%#v error=%v", result, err)
	}
	if result, err := service.Build(t.Context(), options); result != nil || !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("completed restart result=%#v error=%v", result, err)
	}
}

func TestCorpusBuildFailurePreservesPreviousGenerationAndDiagnosticsAreClosed(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	tracker := newCorpusBuildJiraFixture(false)
	service := newCorpusBuildTestService(tracker, nil)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	first, err := service.Build(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}

	tracker.getErrorAt = "10"
	options.Initialize = false
	_, err = service.Build(t.Context(), options)
	if err == nil || strings.Contains(err.Error(), "injected read interruption") {
		t.Fatalf("failure=%v", err)
	}
	store, openErr := corpus.Open(root, corpus.Options{Limits: corpusBuildLimits(options)})
	if openErr != nil {
		t.Fatal(openErr)
	}
	selected, selectErr := store.SelectCurrent(t.Context())
	if selectErr != nil {
		t.Fatal(selectErr)
	}
	if selected.Receipt().GenerationDigest != first.Generation.GenerationDigest {
		t.Fatalf("current=%s first=%s", selected.Receipt().GenerationDigest, first.Generation.GenerationDigest)
	}
	_ = selected.Close()
	_ = store.Close()

	privateFailure := errors.New("private selector title /private/path")
	principalTracker := newCorpusBuildJiraFixture(false)
	principalTracker.currentErr = privateFailure
	privateRoot := corpusBuildPrivateRoot(t)
	privateOptions := corpusBuildTestOptions(privateRoot)
	privateOptions.Initialize = true
	privateOptions.JiraProject = "PROJ"
	privateOptions.MaxJiraIssues = 2
	_, err = newCorpusBuildTestService(principalTracker, nil).Build(t.Context(), privateOptions)
	var closed *CorpusBuildError
	if !errors.As(err, &closed) || closed.Phase != CorpusBuildPhasePrincipal ||
		strings.Contains(err.Error(), privateFailure.Error()) || !errors.Is(err, privateFailure) {
		t.Fatalf("principal failure=%#v", err)
	}
}

func TestCorpusBuildPublicationRecoveryBarrierIsOutcomeUnknownAndContentFree(t *testing.T) {
	privateFailure := errors.New("private completed-record path")
	err := CorpusBuildFailure(CorpusBuildPhasePublish, errors.Join(corpus.ErrOutcomeUnknown, privateFailure))
	var closed *CorpusBuildError
	if !errors.As(err, &closed) || closed.Phase != CorpusBuildPhasePublish ||
		closed.Reason != CorpusBuildReasonOutcomeUnknown || !errors.Is(err, corpus.ErrOutcomeUnknown) ||
		!errors.Is(err, privateFailure) || strings.Contains(err.Error(), privateFailure.Error()) {
		t.Fatalf("publication recovery error=%#v", err)
	}
}

func TestCorpusBuildCanceledDeadlineNeverPublishes(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	tracker := newCorpusBuildJiraFixture(false)
	tracker.currentErr = context.Canceled
	service := newCorpusBuildTestService(tracker, nil)
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := service.Build(ctx, options)
	var closed *CorpusBuildError
	if result != nil || !errors.Is(err, context.Canceled) || !errors.As(err, &closed) ||
		closed.Phase != CorpusBuildPhasePrincipal || closed.Reason != CorpusBuildReasonDeadline {
		t.Fatalf("result=%#v error=%#v", result, err)
	}
	store, openErr := corpus.Open(root, corpus.Options{Limits: corpusBuildLimits(options)})
	if openErr != nil {
		t.Fatal(openErr)
	}
	if generation, selectErr := store.SelectCurrent(context.Background()); generation != nil || !errors.Is(selectErr, corpus.ErrNoCurrent) {
		t.Fatalf("generation=%#v select_error=%v", generation, selectErr)
	}
	_ = store.Close()
}

func TestCorpusBuildExpiredAttemptDeadlineNeverPublishes(t *testing.T) {
	root := corpusBuildPrivateRoot(t)
	service := newCorpusBuildTestService(newCorpusBuildJiraFixture(true), nil)
	service.now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	options := corpusBuildTestOptions(root)
	options.Initialize = true
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	options.Deadline = time.Hour

	result, err := service.Build(t.Context(), options)
	var closed *CorpusBuildError
	if result != nil || !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &closed) ||
		closed.Reason != CorpusBuildReasonDeadline {
		t.Fatalf("result=%#v error=%#v", result, err)
	}
	store, openErr := corpus.Open(root, corpus.Options{Limits: corpusBuildLimits(options)})
	if openErr != nil {
		t.Fatal(openErr)
	}
	if generation, selectErr := store.SelectCurrent(context.Background()); generation != nil || !errors.Is(selectErr, corpus.ErrNoCurrent) {
		t.Fatalf("generation=%#v select_error=%v", generation, selectErr)
	}
	_ = store.Close()
}

func TestValidateCorpusBuildOptionsRejectsInvalidBoundsAndSelectorsWithoutEffects(t *testing.T) {
	base := corpusBuildTestOptions("/synthetic/private-root")
	base.JiraProject = "PROJ"
	base.MaxJiraIssues = 2
	tests := map[string]func(*CorpusBuildOptions){
		"missing root":         func(options *CorpusBuildOptions) { options.Root = "" },
		"missing service":      func(options *CorpusBuildOptions) { options.JiraProject, options.MaxJiraIssues = "", 0 },
		"noncanonical project": func(options *CorpusBuildOptions) { options.JiraProject = "proj" },
		"cap without service":  func(options *CorpusBuildOptions) { options.JiraProject = "" },
		"control in space": func(options *CorpusBuildOptions) {
			options.JiraProject, options.MaxJiraIssues = "", 0
			options.ConfluenceSpace, options.MaxConfluencePages = "private\nspace", 1
		},
		"initialize and restart": func(options *CorpusBuildOptions) { options.Initialize, options.Restart = true, true },
		"request bound":          func(options *CorpusBuildOptions) { options.MaxRequests = corpusBuildMaxRequests + 1 },
		"response bound":         func(options *CorpusBuildOptions) { options.MaxResponseBytes = corpusBuildMaxResponse + 1 },
		"deadline bound":         func(options *CorpusBuildOptions) { options.Deadline = corpusBuildMaxDeadline + time.Second },
		"schedule bound":         func(options *CorpusBuildOptions) { options.MaxInFlight = corpusBuildMaxInFlight + 1 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			options := base
			mutate(&options)
			err := ValidateCorpusBuildOptions(options)
			if !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), "private\nspace") || (options.Root != "" && strings.Contains(err.Error(), options.Root)) {
				t.Fatalf("validation leaked caller value: %v", err)
			}
		})
	}
}

func TestValidateCorpusBuildEvidencePolicy(t *testing.T) {
	base := corpusBuildTestOptions("/synthetic/private-root")
	base.JiraProject, base.MaxJiraIssues = "PROJ", 2
	valid := base
	valid.Comments, valid.MaxCommentPagesPerItem, valid.MaxCommentsPerItem = true, 2, 10
	valid.Attachments, valid.MaxAttachmentPagesPerItem, valid.MaxAttachmentsPerItem = true, 2, 10
	valid.AttachmentBodies = true
	valid.AttachmentMediaTypes = []string{"application/octet-stream", "text/plain"}
	valid.MaxAttachmentBytes, valid.MaxTotalAttachmentBytes = 1024, 4096
	if err := ValidateCorpusBuildOptions(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*CorpusBuildOptions){
		"comment bound without selection":    func(options *CorpusBuildOptions) { options.Comments = false },
		"attachment bound without selection": func(options *CorpusBuildOptions) { options.Attachments = false },
		"body without attachment": func(options *CorpusBuildOptions) {
			options.Attachments = false
			options.MaxAttachmentPagesPerItem = 0
			options.MaxAttachmentsPerItem = 0
		},
		"wildcard MIME": func(options *CorpusBuildOptions) { options.AttachmentMediaTypes = []string{"application/*"} },
		"parameterized MIME": func(options *CorpusBuildOptions) {
			options.AttachmentMediaTypes = []string{"text/plain; charset=utf-8"}
		},
		"duplicate MIME":              func(options *CorpusBuildOptions) { options.AttachmentMediaTypes = []string{"text/plain", "text/plain"} },
		"aggregate smaller than item": func(options *CorpusBuildOptions) { options.MaxTotalAttachmentBytes = options.MaxAttachmentBytes - 1 },
		"partial without evidence": func(options *CorpusBuildOptions) {
			*options = base
			options.AllowPartialEvidence = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			options := valid
			options.AttachmentMediaTypes = append([]string{}, valid.AttachmentMediaTypes...)
			mutate(&options)
			if err := ValidateCorpusBuildOptions(options); !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCorpusBuildActiveBindingRejectsGuardAndSelectorDrift(t *testing.T) {
	options := corpusBuildTestOptions("/synthetic/private-root")
	options.JiraProject = "PROJ"
	options.MaxJiraIssues = 2
	_, services, err := corpusBuildServices(options)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	base := corpus.BuildActive{
		SchemaVersion: corpus.BuildActiveSchemaV2, AttemptID: strings.Repeat("1", 32),
		Status: corpus.BuildAttemptActive, OptionsDigest: strings.Repeat("a", 64), Services: services,
		StartedAt: corpus.NewBuildActiveTime(started), Deadline: corpus.NewBuildActiveTime(started.Add(options.Deadline)),
		MaxAttempts: options.MaxRequests, MaxResponseBytes: options.MaxResponseBytes,
	}
	if err := validateCorpusBuildActiveBinding(base, options); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*corpus.BuildActive){
		"attempt budget": func(active *corpus.BuildActive) { active.MaxAttempts++ },
		"byte budget":    func(active *corpus.BuildActive) { active.MaxResponseBytes++ },
		"deadline": func(active *corpus.BuildActive) {
			active.Deadline = corpus.NewBuildActiveTime(started.Add(options.Deadline + time.Second))
		},
		"selector": func(active *corpus.BuildActive) { active.Services[0].SelectorDigest = strings.Repeat("b", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			active := base
			active.Services = append([]corpus.BuildServiceState(nil), base.Services...)
			mutate(&active)
			if err := validateCorpusBuildActiveBinding(active, options); !errors.Is(err, corpus.ErrIntegrity) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func newCorpusBuildJiraFixture(budgeted bool) *corpusBuildJiraTracker {
	return &corpusBuildJiraTracker{
		jiraCompleteTracker: newCompleteJiraTracker(), budgeted: budgeted,
		user: &domain.User{Key: "fixture-jira-principal", DisplayName: "Configured Fixture", Active: true},
	}
}

func newCorpusBuildConfluenceFixture(budgeted bool) *corpusBuildConfluenceStore {
	first := completeTestPage("10")
	first.Title = "private fixture title"
	second := completeTestPage("20")
	return &corpusBuildConfluenceStore{
		completePullStore: &completePullStore{
			pullStore:      &pullStore{pages: map[string]*domain.Resource{"10": first, "20": second}},
			searchSequence: []domain.PageSearchPage{completeSearchPage("20", "10"), completeSearchPage("10", "20")},
		},
		identity: domain.ConfluenceUserIdentity{ID: "fixture-confluence-principal", DisplayName: "Configured Fixture"},
		budgeted: budgeted,
	}
}

func newCorpusBuildTestService(jira *corpusBuildJiraTracker, confluence domain.DocStore) *CorpusBuildService {
	dependencies := CorpusBuildDependencies{
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		Now: time.Now,
	}
	if jira != nil {
		dependencies.Jira = &JiraService{tr: jira, baseURL: jiraMirrorTestBackendURL}
	}
	if confluence != nil {
		dependencies.Confluence = &ConfluenceService{
			store: confluence, baseURL: confluenceTestBackendURL,
			requestMaxInFlight: 2, requestsPerSecond: 100,
		}
	}
	return NewCorpusBuildService(dependencies)
}

func corpusBuildTestOptions(root string) CorpusBuildOptions {
	return CorpusBuildOptions{
		Root: root, MaxRequests: 1000, MaxResponseBytes: 1 << 20,
		MaxMembers: 100, MaxGenerationBytes: 4 << 20,
		Deadline: time.Hour, MaxInFlight: 2, RequestsPerSecond: 100,
	}
}

func corpusBuildPrivateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func loadCorpusBuildActiveForTest(t *testing.T, root string, options CorpusBuildOptions) corpus.BuildActive {
	t.Helper()
	workspace, err := corpus.OpenBuildWorkspace(t.Context(), root, corpus.Options{Limits: corpusBuildLimits(options)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = workspace.Close() }()
	active, found, err := workspace.LoadActive()
	if err != nil || !found {
		t.Fatalf("active found=%t error=%v", found, err)
	}
	return active
}

func writeLegacyCorpusBuildActiveForTest(t *testing.T, root string, active corpus.BuildActive) {
	t.Helper()
	type legacyService struct {
		Service        corpus.Service      `json:"service"`
		SelectorDigest string              `json:"selector_digest"`
		ScopeDigest    string              `json:"scope_digest,omitempty"`
		StartedAt      string              `json:"started_at,omitempty"`
		Usage          corpus.CaptureUsage `json:"usage"`
		ReceiptDigest  string              `json:"receipt_digest,omitempty"`
	}
	type legacyActive struct {
		SchemaVersion    int                       `json:"schema_version"`
		AttemptID        string                    `json:"attempt_id"`
		Status           corpus.BuildAttemptStatus `json:"status"`
		OptionsDigest    string                    `json:"options_digest"`
		Services         []legacyService           `json:"services"`
		StartedAt        string                    `json:"started_at"`
		Deadline         string                    `json:"deadline"`
		MaxAttempts      int                       `json:"max_attempts"`
		MaxResponseBytes int64                     `json:"max_response_bytes"`
		Usage            corpus.CaptureUsage       `json:"usage"`
		RemoteInFlight   bool                      `json:"remote_in_flight"`
		RemoteService    corpus.Service            `json:"remote_service,omitempty"`
		GenerationDigest string                    `json:"generation_digest,omitempty"`
	}
	services := make([]legacyService, len(active.Services))
	for index, state := range active.Services {
		services[index] = legacyService{
			Service: state.Service, SelectorDigest: state.SelectorDigest, ScopeDigest: state.ScopeDigest,
			StartedAt: state.StartedAt, Usage: state.Usage, ReceiptDigest: state.ReceiptDigest,
		}
	}
	legacy := legacyActive{
		SchemaVersion: corpus.BuildActiveSchemaV1, AttemptID: active.AttemptID, Status: active.Status,
		OptionsDigest: active.OptionsDigest, Services: services, StartedAt: active.StartedAt, Deadline: active.Deadline,
		MaxAttempts: active.MaxAttempts, MaxResponseBytes: active.MaxResponseBytes, Usage: active.Usage,
		RemoteInFlight: active.RemoteInFlight, RemoteService: active.RemoteService, GenerationDigest: active.GenerationDigest,
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "active.v2.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "active.v1.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
