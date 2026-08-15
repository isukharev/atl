package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type confluenceCorpusEvidenceStore struct {
	*completePullStore
	inventory       domain.AttachmentInventory
	inventoryErr    error
	bodyErr         error
	revalidateErr   error
	comments        domain.ConfluenceCommentInventory
	commentErr      error
	body            string
	identity        domain.ConfluenceUserIdentity
	metaVersion     int
	driftAfterRead  bool
	inventoryReads  int
	revalidateReads int
	bodyReads       int
	commentReads    int
}

// duplicateConfluenceAttachmentSelectorStore models the filename/version
// ambiguity that the Confluence binary route cannot express directly. Its
// selector revalidation deliberately resolves both inventory rows to the
// first attachment; the capture path must refuse to publish that second body
// under a different inventory ID.
type duplicateConfluenceAttachmentSelectorStore struct {
	*confluenceCorpusEvidenceStore
}

func (store *duplicateConfluenceAttachmentSelectorStore) RevalidateAttachmentDownload(
	ctx context.Context,
	pageID, filename string,
	version int,
) (domain.ConfluenceAttachmentDownloadEvidence, error) {
	store.revalidateReads++
	if domain.ReadBudgetFromContext(ctx) == nil || !domain.SingleAttempt(ctx) || pageID != "10" || filename != "a.bin" || version != 2 {
		return domain.ConfluenceAttachmentDownloadEvidence{}, errors.New("unexpected attachment download revalidation")
	}
	attachment := store.inventory.Attachments[0]
	return domain.ConfluenceAttachmentDownloadEvidence{
		AttachmentID: attachment.ID, PageID: pageID, Filename: filename,
		Version: attachment.Version, FileSize: attachment.FileSize,
	}, nil
}

func (store *confluenceCorpusEvidenceStore) CurrentConfluenceUser(context.Context) (domain.ConfluenceUserIdentity, error) {
	return store.identity, nil
}

func (store *confluenceCorpusEvidenceStore) ListConfluenceComments(_ context.Context, id string, options domain.ConfluenceCommentReadOptions) (domain.ConfluenceCommentInventory, error) {
	store.commentReads++
	validLimits := options.MaxPages == 2 && options.MaxItems == 10 || options.MaxPages == 0 && options.MaxItems == 0
	if id != "10" || options.ParentVersion != 3 || !validLimits {
		return domain.ConfluenceCommentInventory{}, errors.New("unexpected comment qualification")
	}
	return store.comments, store.commentErr
}

func (store *confluenceCorpusEvidenceStore) GetMeta(_ context.Context, id string) (*domain.PageMeta, error) {
	version := store.metaVersion
	if store.driftAfterRead && store.inventoryReads > 0 {
		version++
	}
	return &domain.PageMeta{ID: id, Version: version}, nil
}

func (store *confluenceCorpusEvidenceStore) ListAttachmentsQualifiedBounded(_ context.Context, id string, options domain.AttachmentReadOptions) (domain.AttachmentInventory, error) {
	store.inventoryReads++
	if id != "10" || options.MaxPages != 2 || options.MaxItems != 10 {
		return domain.AttachmentInventory{}, errors.New("unexpected attachment qualification")
	}
	return store.inventory, store.inventoryErr
}

func (store *confluenceCorpusEvidenceStore) RevalidateAttachmentDownload(ctx context.Context, pageID, filename string, version int) (domain.ConfluenceAttachmentDownloadEvidence, error) {
	store.revalidateReads++
	if domain.ReadBudgetFromContext(ctx) == nil || !domain.SingleAttempt(ctx) || pageID != "10" || filename != "a.bin" || version != 2 {
		return domain.ConfluenceAttachmentDownloadEvidence{}, errors.New("unexpected attachment download revalidation")
	}
	if store.revalidateErr != nil {
		return domain.ConfluenceAttachmentDownloadEvidence{}, store.revalidateErr
	}
	if len(store.inventory.Attachments) == 0 {
		return domain.ConfluenceAttachmentDownloadEvidence{}, domain.ErrNotFound
	}
	attachment := store.inventory.Attachments[0]
	return domain.ConfluenceAttachmentDownloadEvidence{
		AttachmentID: attachment.ID, PageID: pageID, Filename: filename,
		Version: attachment.Version, FileSize: attachment.FileSize,
	}, nil
}

func (store *confluenceCorpusEvidenceStore) DownloadAttachment(_ context.Context, pageID, filename string, version int) (io.ReadCloser, error) {
	store.bodyReads++
	if pageID != "10" || filename != "a.bin" || version != 2 {
		return nil, errors.New("unexpected attachment download")
	}
	if store.bodyErr != nil {
		return nil, store.bodyErr
	}
	return io.NopCloser(strings.NewReader(store.body)), nil
}

func newConfluenceCorpusEvidenceStore() *confluenceCorpusEvidenceStore {
	page := completeTestPage("10")
	page.Version = 3
	return &confluenceCorpusEvidenceStore{
		completePullStore: &completePullStore{
			pullStore:      &pullStore{pages: map[string]*domain.Resource{"10": page}},
			searchSequence: []domain.PageSearchPage{completeSearchPage("10"), completeSearchPage("10")},
		},
		inventory: domain.AttachmentInventory{Complete: true, Attachments: []domain.Attachment{{
			ID: "7", Title: "a.bin", MediaType: "application/octet-stream", FileSize: 3, Version: 2,
			Created: "2026-01-01", AuthorKey: "stable",
		}}},
		body: "abc", identity: domain.ConfluenceUserIdentity{ID: "fixture-confluence-principal", DisplayName: "Fixture User"}, metaVersion: 3,
	}
}

func confluenceCorpusEvidenceOptions(partial bool) *corpusPullEvidenceOptions {
	return newCorpusPullEvidenceOptions(CorpusBuildOptions{
		Attachments: true, MaxAttachmentPagesPerItem: 2, MaxAttachmentsPerItem: 10,
		AttachmentBodies: true, AttachmentMediaTypes: []string{"application/octet-stream"},
		MaxAttachmentBytes: 16, MaxTotalAttachmentBytes: 64, AllowPartialEvidence: partial,
	})
}

func confluenceCombinedCorpusEvidenceOptions(partial bool) *corpusPullEvidenceOptions {
	return newCorpusPullEvidenceOptions(CorpusBuildOptions{
		Comments: true, MaxCommentPagesPerItem: 2, MaxCommentsPerItem: 10,
		Attachments: true, MaxAttachmentPagesPerItem: 2, MaxAttachmentsPerItem: 10,
		AttachmentBodies: true, AttachmentMediaTypes: []string{"application/octet-stream"},
		MaxAttachmentBytes: 16, MaxTotalAttachmentBytes: 64, AllowPartialEvidence: partial,
	})
}

func TestConfluenceCompletePullPublishesQualifiedAttachmentEvidence(t *testing.T) {
	root := t.TempDir()
	store := newConfluenceCorpusEvidenceStore()
	result, err := (&ConfluenceService{store: store, baseURL: confluenceTestBackendURL}).Pull(t.Context(), PullOpts{
		Complete: true, Space: "DOC", MaxPages: 1, Into: root,
		evidence: confluenceCorpusEvidenceOptions(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete == nil || !result.Complete.Complete || store.inventoryReads != 1 || store.revalidateReads != 1 || store.bodyReads != 1 {
		t.Fatalf("result=%+v reads=%d/%d/%d", result.Complete, store.inventoryReads, store.revalidateReads, store.bodyReads)
	}
	if len(result.Pages) != 1 {
		t.Fatalf("pages=%+v", result.Pages)
	}
	stem := strings.TrimSuffix(filepath.Join(root, filepath.FromSlash(result.Pages[0].Path)), ".csf")
	data, err := os.ReadFile(stem + ".attachments.json")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := mirror.DecodeAttachmentSidecarV1(data)
	if err != nil || !decoded.Complete || decoded.ParentID != "10" || decoded.ParentVersion != 3 ||
		decoded.Attachments[0].Body.State != mirror.AttachmentBodyCaptured {
		t.Fatalf("decoded=%+v error=%v", decoded, err)
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(decoded.Attachments[0].Body.Path)))
	if err != nil || string(body) != "abc" {
		t.Fatalf("body=%q error=%v", body, err)
	}
}

func TestConfluenceCompletePullRetiresStaleCapturedAttachmentBodies(t *testing.T) {
	root := t.TempDir()
	store := newConfluenceCorpusEvidenceStore()
	svc := &ConfluenceService{store: store, baseURL: confluenceTestBackendURL}
	first, err := svc.Pull(t.Context(), PullOpts{
		Complete: true, Space: "DOC", MaxPages: 1, Into: root,
		evidence: confluenceCorpusEvidenceOptions(false),
	})
	if err != nil || len(first.Pages) != 1 {
		t.Fatalf("first=%+v error=%v", first, err)
	}
	stem := strings.TrimSuffix(filepath.Join(root, filepath.FromSlash(first.Pages[0].Path)), ".csf")
	firstSidecar, decodeErr := func() (mirror.AttachmentSidecarV1, error) {
		data, readErr := os.ReadFile(stem + ".attachments.json")
		if readErr != nil {
			return mirror.AttachmentSidecarV1{}, readErr
		}
		return mirror.DecodeAttachmentSidecarV1(data)
	}()
	if decodeErr != nil || len(firstSidecar.Attachments) != 1 || firstSidecar.Attachments[0].Body.State != mirror.AttachmentBodyCaptured {
		t.Fatalf("first sidecar=%+v error=%v", firstSidecar, decodeErr)
	}
	oldBody := filepath.Join(root, filepath.FromSlash(firstSidecar.Attachments[0].Body.Path))
	if _, statErr := os.Stat(oldBody); statErr != nil {
		t.Fatal(statErr)
	}

	// A later recrawl sees the same attachment inventory but no longer accepts
	// its MIME type. The new sidecar is qualified evidence of that exclusion;
	// the older captured body must not survive beside it.
	store.completePullStore.pullStore.pages["10"].Version = 4
	store.completePullStore.pullStore.pages["10"].Body = []byte("<p>updated</p>")
	store.metaVersion = 4
	store.inventory.Attachments[0].MediaType = "application/x-not-captured"
	store.searchSequence = []domain.PageSearchPage{completeSearchPage("10"), completeSearchPage("10")}
	second, err := svc.Pull(t.Context(), PullOpts{
		Complete: true, Space: "DOC", MaxPages: 1, Into: root,
		evidence: confluenceCorpusEvidenceOptions(false),
	})
	if err != nil || second == nil || second.Complete == nil || !second.Complete.Complete {
		t.Fatalf("second=%+v error=%v", second, err)
	}
	if _, statErr := os.Stat(oldBody); !os.IsNotExist(statErr) {
		t.Fatalf("stale attachment body stat=%v", statErr)
	}
	data, readErr := os.ReadFile(stem + ".attachments.json")
	secondSidecar, decodeErr := mirror.DecodeAttachmentSidecarV1(data)
	if readErr != nil || decodeErr != nil || secondSidecar.ParentVersion != 4 || len(secondSidecar.Attachments) != 1 ||
		secondSidecar.Attachments[0].Body != (mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyExcluded, Reason: mirror.AttachmentBodyReasonMediaExcluded}) ||
		store.bodyReads != 1 {
		t.Fatalf("sidecar=%+v read=%v decode=%v body_reads=%d", secondSidecar, readErr, decodeErr, store.bodyReads)
	}
}

func TestConfluenceNonCompletePullRefusesToInvalidatePriorAttachmentCapture(t *testing.T) {
	root := t.TempDir()
	store := newConfluenceCorpusEvidenceStore()
	svc := &ConfluenceService{store: store, baseURL: confluenceTestBackendURL}
	first, err := svc.Pull(t.Context(), PullOpts{
		Complete: true, Space: "DOC", MaxPages: 1, Into: root,
		evidence: confluenceCorpusEvidenceOptions(false),
	})
	if err != nil || len(first.Pages) != 1 {
		t.Fatalf("first=%+v error=%v", first, err)
	}
	stem := strings.TrimSuffix(filepath.Join(root, filepath.FromSlash(first.Pages[0].Path)), ".csf")
	sidecarPath := stem + ".attachments.json"
	sidecarBytes, readErr := os.ReadFile(sidecarPath)
	sidecar, decodeErr := mirror.DecodeAttachmentSidecarV1(sidecarBytes)
	if readErr != nil || decodeErr != nil || len(sidecar.Attachments) != 1 || sidecar.Attachments[0].Body.State != mirror.AttachmentBodyCaptured {
		t.Fatalf("sidecar=%+v read=%v decode=%v", sidecar, readErr, decodeErr)
	}
	bodyPath := filepath.Join(root, filepath.FromSlash(sidecar.Attachments[0].Body.Path))
	oldNative, nativeErr := os.ReadFile(stem + ".csf")
	if nativeErr != nil {
		t.Fatal(nativeErr)
	}

	// A normal incremental-style refresh has no atomic artifact journal. It
	// must not make this now-stale capture impossible to retire on a later
	// complete pull.
	store.completePullStore.pullStore.pages["10"].Version = 4
	store.completePullStore.pullStore.pages["10"].Body = []byte("<p>updated</p>")
	if result, pullErr := svc.Pull(t.Context(), PullOpts{ID: "10", Into: root}); !errors.Is(pullErr, domain.ErrCheckFailed) || result == nil || len(result.Pages) != 0 {
		t.Fatalf("ordinary result=%+v error=%v", result, pullErr)
	}
	if got, statErr := os.ReadFile(stem + ".csf"); statErr != nil || string(got) != string(oldNative) {
		t.Fatalf("native after refused ordinary refresh=%q error=%v", got, statErr)
	}
	if got, statErr := os.ReadFile(bodyPath); statErr != nil || string(got) != "abc" {
		t.Fatalf("body after refused ordinary refresh=%q error=%v", got, statErr)
	}
	if got, statErr := os.ReadFile(sidecarPath); statErr != nil || string(got) != string(sidecarBytes) {
		t.Fatalf("sidecar after refused ordinary refresh=%q error=%v", got, statErr)
	}

	// An attachment-free complete pull owns the required atomic retirement.
	store.metaVersion = 4
	store.searchSequence = []domain.PageSearchPage{completeSearchPage("10"), completeSearchPage("10")}
	second, secondErr := svc.Pull(t.Context(), PullOpts{Complete: true, Space: "DOC", MaxPages: 1, Into: root})
	if secondErr != nil || second == nil || second.Complete == nil || !second.Complete.Complete {
		t.Fatalf("second=%+v error=%v", second, secondErr)
	}
	if _, statErr := os.Stat(bodyPath); !os.IsNotExist(statErr) {
		t.Fatalf("stale body stat=%v", statErr)
	}
	if _, statErr := os.Stat(sidecarPath); !os.IsNotExist(statErr) {
		t.Fatalf("stale sidecar stat=%v", statErr)
	}
	if got, statErr := os.ReadFile(stem + ".csf"); statErr != nil || string(got) != "<p>updated</p>" {
		t.Fatalf("native after complete retirement=%q error=%v", got, statErr)
	}
}

func TestConfluenceNonCompletePullRefusesAttachmentCaptureRelocationBeforeWrite(t *testing.T) {
	root := t.TempDir()
	store := newConfluenceCorpusEvidenceStore()
	svc := &ConfluenceService{store: store, baseURL: confluenceTestBackendURL}
	first, err := svc.Pull(t.Context(), PullOpts{
		Complete: true, Space: "DOC", MaxPages: 1, Into: root,
		evidence: confluenceCorpusEvidenceOptions(false),
	})
	if err != nil || len(first.Pages) != 1 {
		t.Fatalf("first=%+v error=%v", first, err)
	}
	m := mirror.New(root)
	before, found, stateErr := m.SyncStateOf("10")
	if stateErr != nil || !found {
		t.Fatalf("initial state=%+v found=%t error=%v", before, found, stateErr)
	}
	stem := strings.TrimSuffix(filepath.Join(root, filepath.FromSlash(before.Path)), ".csf")
	sidecarPath := stem + ".attachments.json"
	sidecarBytes, sidecarErr := os.ReadFile(sidecarPath)
	sidecar, decodeErr := mirror.DecodeAttachmentSidecarV1(sidecarBytes)
	if sidecarErr != nil || decodeErr != nil || len(sidecar.Attachments) != 1 || sidecar.Attachments[0].Body.State != mirror.AttachmentBodyCaptured {
		t.Fatalf("sidecar=%+v read=%v decode=%v", sidecar, sidecarErr, decodeErr)
	}
	bodyPath := filepath.Join(root, filepath.FromSlash(sidecar.Attachments[0].Body.Path))

	// A title move changes the canonical page path. The ordinary pull has no
	// journal that could bind both the new state and old capture retirement, so
	// it must stop before either path can be written.
	page := store.pages["10"]
	page.Title = "Moved attachment page"
	page.Version = 4
	page.Body = []byte("<p>moved</p>")
	result, pullErr := svc.Pull(t.Context(), PullOpts{ID: "10", Into: root})
	if !errors.Is(pullErr, domain.ErrCheckFailed) || result == nil || len(result.Pages) != 0 {
		t.Fatalf("ordinary result=%+v error=%v", result, pullErr)
	}
	after, found, stateErr := m.SyncStateOf("10")
	if stateErr != nil || !found || after != before {
		t.Fatalf("state after refused relocation=%+v found=%t error=%v want=%+v", after, found, stateErr, before)
	}
	if got, readErr := os.ReadFile(stem + ".csf"); readErr != nil || string(got) != "<p>10</p>" {
		t.Fatalf("native after refused relocation=%q error=%v", got, readErr)
	}
	if got, readErr := os.ReadFile(sidecarPath); readErr != nil || string(got) != string(sidecarBytes) {
		t.Fatalf("sidecar after refused relocation=%q error=%v", got, readErr)
	}
	if got, readErr := os.ReadFile(bodyPath); readErr != nil || string(got) != "abc" {
		t.Fatalf("body after refused relocation=%q error=%v", got, readErr)
	}
}

func TestConfluenceCompletePullRefusesFilenameSelectorIdentityMismatch(t *testing.T) {
	root := t.TempDir()
	base := newConfluenceCorpusEvidenceStore()
	base.inventory.Attachments = []domain.Attachment{
		{ID: "7", Title: "a.bin", MediaType: "application/octet-stream", FileSize: 3, Version: 2, Created: "2026-01-01", AuthorKey: "stable"},
		{ID: "8", Title: "a.bin", MediaType: "application/octet-stream", FileSize: 3, Version: 2, Created: "2026-01-01", AuthorKey: "stable"},
	}
	store := &duplicateConfluenceAttachmentSelectorStore{confluenceCorpusEvidenceStore: base}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceTestBackendURL}).Pull(t.Context(), PullOpts{
		Complete: true, Space: "DOC", MaxPages: 1, Into: root,
		evidence: confluenceCorpusEvidenceOptions(true),
	})
	if err != nil || result == nil || result.Complete == nil || !result.Complete.Complete || store.revalidateReads != 2 || store.bodyReads != 1 {
		t.Fatalf("result=%+v error=%v revalidations=%d body_reads=%d", result, err, store.revalidateReads, store.bodyReads)
	}
	attachments := confluencePullInclude(t, result, ConfluencePullIncludeAttachments)
	if attachments.Qualification != ConfluencePullIncludePartial || attachments.Reason != ConfluencePullIncludeReasonBodyIncomplete {
		t.Fatalf("attachments=%+v", attachments)
	}
	stem := strings.TrimSuffix(filepath.Join(root, filepath.FromSlash(result.Pages[0].Path)), ".csf")
	data, readErr := os.ReadFile(stem + ".attachments.json")
	sidecar, decodeErr := mirror.DecodeAttachmentSidecarV1(data)
	if readErr != nil || decodeErr != nil || len(sidecar.Attachments) != 2 ||
		sidecar.Attachments[0].ID != "7" || sidecar.Attachments[0].Body.State != mirror.AttachmentBodyCaptured ||
		sidecar.Attachments[1].ID != "8" || sidecar.Attachments[1].Body != (mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyFailed, Reason: mirror.AttachmentBodyReasonFailed}) {
		t.Fatalf("sidecar=%+v read=%v decode=%v", sidecar, readErr, decodeErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(stem+".attachments/8.body"))); !os.IsNotExist(statErr) {
		t.Fatalf("misbound attachment body stat=%v", statErr)
	}
}

func TestConfluenceCompletePullPublishesCombinedCommentsAndAttachments(t *testing.T) {
	root := t.TempDir()
	store := newConfluenceCorpusEvidenceStore()
	store.comments = completeQualifiedComments()
	result, err := (&ConfluenceService{store: store, baseURL: confluenceTestBackendURL}).Pull(t.Context(), PullOpts{
		Complete: true, Space: "DOC", MaxPages: 1, Into: root, Comments: true,
		evidence: confluenceCombinedCorpusEvidenceOptions(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete == nil || !result.Complete.Complete || store.commentReads != 1 || store.inventoryReads != 1 || store.revalidateReads != 1 || store.bodyReads != 1 {
		t.Fatalf("result=%+v reads=%d/%d/%d/%d", result.Complete, store.commentReads, store.inventoryReads, store.revalidateReads, store.bodyReads)
	}
	comments := confluencePullInclude(t, result, ConfluencePullIncludeComments)
	if comments.Qualification != ConfluencePullIncludeQualified || comments.Complete == nil || !*comments.Complete {
		t.Fatalf("comments include=%+v", comments)
	}
	stem := strings.TrimSuffix(filepath.Join(root, filepath.FromSlash(result.Pages[0].Path)), ".csf")
	for _, suffix := range []string{".comments.json", ".attachments.json"} {
		if info, statErr := os.Stat(stem + suffix); statErr != nil || !info.Mode().IsRegular() {
			t.Fatalf("artifact %s info=%v error=%v", suffix, info, statErr)
		}
	}
}

func TestConfluenceAttachmentParentDriftPreventsPublication(t *testing.T) {
	root := t.TempDir()
	store := newConfluenceCorpusEvidenceStore()
	store.comments = completeQualifiedComments()
	store.driftAfterRead = true
	result, err := (&ConfluenceService{store: store, baseURL: confluenceTestBackendURL}).Pull(t.Context(), PullOpts{
		Complete: true, Space: "DOC", MaxPages: 1, Into: root, Comments: true,
		evidence: confluenceCombinedCorpusEvidenceOptions(false),
	})
	if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.Complete == nil || result.Complete.Complete {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	comments := confluencePullInclude(t, result, ConfluencePullIncludeComments)
	if comments.Qualification != ConfluencePullIncludeFailed || comments.Reason != ConfluencePullIncludeReasonReadFailed || !result.HasFailedInclude() {
		t.Fatalf("comments include=%+v", comments)
	}
	var published []string
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".attachments.json") {
			published = append(published, path)
		}
		return nil
	})
	if walkErr != nil || len(published) != 0 {
		t.Fatalf("published=%v error=%v", published, walkErr)
	}
}

func TestConfluenceAttachmentReadFailuresCloseStagedCommentEvidence(t *testing.T) {
	for _, failure := range []string{"inventory", "body"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			store := newConfluenceCorpusEvidenceStore()
			store.comments = completeQualifiedComments()
			if failure == "inventory" {
				store.inventoryErr = errors.New("synthetic attachment inventory failure")
			} else {
				store.bodyErr = errors.New("synthetic attachment body failure")
			}
			result, err := (&ConfluenceService{store: store, baseURL: confluenceTestBackendURL}).Pull(t.Context(), PullOpts{
				Complete: true, Space: "DOC", MaxPages: 1, Into: root, Comments: true,
				evidence: confluenceCombinedCorpusEvidenceOptions(false),
			})
			if err == nil || result == nil || result.Complete == nil || result.Complete.Complete {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			comments := confluencePullInclude(t, result, ConfluencePullIncludeComments)
			if comments.Qualification != ConfluencePullIncludeFailed || comments.Complete == nil || *comments.Complete ||
				comments.Reason != ConfluencePullIncludeReasonStagingFailed || !result.HasFailedInclude() {
				t.Fatalf("comments include=%+v", comments)
			}
			if store.commentReads != 1 || store.inventoryReads != 1 {
				t.Fatalf("reads comments=%d inventory=%d bodies=%d", store.commentReads, store.inventoryReads, store.bodyReads)
			}
			if failure == "inventory" && store.bodyReads != 0 || failure == "body" && store.bodyReads != 1 {
				t.Fatalf("failure=%s body reads=%d", failure, store.bodyReads)
			}
		})
	}
}

func TestConfluenceQualifiedAttachmentProjectsResolvedIndexerV2Artifact(t *testing.T) {
	mirrorRoot := t.TempDir()
	backend := newConfluenceCorpusEvidenceStore()
	backend.completePullStore.pullStore.pages["10"].Body = []byte(`<p>download <ac:link><ri:attachment ri:filename="a.bin"/></ac:link></p>`)
	_, err := (&ConfluenceService{store: backend, baseURL: confluenceTestBackendURL}).Pull(t.Context(), PullOpts{
		Complete: true, Space: "DOC", MaxPages: 1, Into: mirrorRoot,
		evidence: confluenceCorpusEvidenceOptions(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	capture := corpusExportCaptureReceiptWithDimensions(t, corpus.ServiceConfluence, mirrorRoot, []corpus.CaptureDimensionEvidence{
		{Dimension: corpus.CaptureNative, State: corpus.CaptureComplete},
		{Dimension: corpus.CaptureMetadata, State: corpus.CaptureComplete},
		{Dimension: corpus.CaptureComments, State: corpus.CaptureNotRequested},
		{Dimension: corpus.CaptureAttachments, State: corpus.CaptureComplete},
	})
	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := ExportCorpus(t.Context(), CorpusExportOptions{
		ConfluenceRoot: mirrorRoot, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v2", BuildState: corpus.BuildStateClean,
		CaptureReceipts: []corpus.CaptureReceipt{capture},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Projection.Readiness != corpus.ProjectionReady || result.Projection.Counts.Documents != 2 ||
		result.Projection.Counts.Artifacts != 1 || result.Projection.Counts.ArtifactBytes != 3 {
		t.Fatalf("projection=%#v", result)
	}
	documents, edges := readCorpusExportProjection(t, storeRoot, corpus.ServiceConfluence)
	var page, attachment corpus.IndexerDocument
	for _, document := range documents {
		switch document.Kind {
		case corpus.ObjectPage:
			page = document
		case corpus.ObjectAttachment:
			attachment = document
		}
	}
	if page.ID == "" || attachment.ID == "" || attachment.Version != "2" ||
		!strings.HasSuffix(attachment.Source.Path, ".attachments.json") || page.Evidence[0].Status != corpus.EvidenceComplete {
		t.Fatalf("documents=%#v", documents)
	}
	var ownerEdge, referenceEdge bool
	for _, edge := range edges {
		ownerEdge = ownerEdge || edge.Relation == corpus.EdgeAttachmentOwner && edge.SourceID == attachment.ID && edge.TargetID == page.ID
		referenceEdge = referenceEdge || edge.Relation == corpus.EdgeReferences && edge.SourceID == page.ID && edge.TargetID == attachment.ID && edge.Evidence.Fragment == "attachment-link"
	}
	if !ownerEdge || !referenceEdge {
		t.Fatalf("edges=%#v", edges)
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
	var body bytes.Buffer
	if _, err := selected.CopyMember(t.Context(), corpus.ServiceConfluence, attachment.ID, corpus.RoleAsset, &body); err != nil || body.String() != "abc" {
		t.Fatalf("body=%q error=%v", body.String(), err)
	}
}

func TestConfluenceForbiddenCommentsRemainExplicitInPartialProjection(t *testing.T) {
	for _, allowPartial := range []bool{false, true} {
		t.Run(map[bool]string{false: "strict", true: "partial"}[allowPartial], func(t *testing.T) {
			mirrorRoot := t.TempDir()
			backend := newConfluenceCorpusEvidenceStore()
			backend.commentErr = domain.ErrForbidden
			options := newCorpusPullEvidenceOptions(CorpusBuildOptions{
				Comments: true, MaxCommentPagesPerItem: 2, MaxCommentsPerItem: 10,
				AllowPartialEvidence: allowPartial,
			})
			result, err := (&ConfluenceService{store: backend, baseURL: confluenceTestBackendURL}).Pull(t.Context(), PullOpts{
				Complete: true, Space: "DOC", MaxPages: 1, Into: mirrorRoot, Comments: true, evidence: options,
			})
			if !allowPartial {
				if !errors.Is(err, domain.ErrForbidden) || result == nil || result.Complete == nil || result.Complete.Complete {
					t.Fatalf("result=%#v error=%v", result, err)
				}
				comments := confluencePullInclude(t, result, ConfluencePullIncludeComments)
				if comments.Qualification != ConfluencePullIncludeFailed || comments.Reason != ConfluencePullIncludeReasonReadFailed || !result.HasFailedInclude() {
					t.Fatalf("comments include=%+v", comments)
				}
				return
			}
			if err != nil || result.Complete == nil || !result.Complete.Complete || backend.commentReads != 1 {
				t.Fatalf("result=%#v reads=%d error=%v", result, backend.commentReads, err)
			}
			comments := confluencePullInclude(t, result, ConfluencePullIncludeComments)
			if comments.Qualification != ConfluencePullIncludePartial || comments.Complete == nil || *comments.Complete || comments.Reason != ConfluencePullIncludeReasonInventoryIncomplete {
				t.Fatalf("comments include=%+v", comments)
			}
			var sidecarPath string
			walkErr := filepath.WalkDir(mirrorRoot, func(path string, entry os.DirEntry, err error) error {
				if err == nil && !entry.IsDir() && strings.HasSuffix(path, ".comments.json") {
					sidecarPath = path
				}
				return err
			})
			data, readErr := os.ReadFile(sidecarPath)
			decoded, decodeErr := mirror.DecodeConfluenceCommentsSidecar(data)
			if walkErr != nil || readErr != nil || decodeErr != nil || decoded.V2 == nil || decoded.V2.Complete ||
				!slices.Contains(decoded.V2.PartialReasons, domain.ConfluenceCommentPartialForbidden) {
				t.Fatalf("sidecar=%#v walk=%v read=%v decode=%v", decoded, walkErr, readErr, decodeErr)
			}
			capture := corpusExportCaptureReceiptWithDimensions(t, corpus.ServiceConfluence, mirrorRoot, []corpus.CaptureDimensionEvidence{
				{Dimension: corpus.CaptureNative, State: corpus.CaptureComplete},
				{Dimension: corpus.CaptureMetadata, State: corpus.CaptureComplete},
				{Dimension: corpus.CaptureComments, State: corpus.CapturePartial},
				{Dimension: corpus.CaptureAttachments, State: corpus.CaptureNotRequested},
			})
			storeRoot := t.TempDir()
			if err := os.Chmod(storeRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			exported, err := ExportCorpus(t.Context(), CorpusExportOptions{
				ConfluenceRoot: mirrorRoot, StoreRoot: storeRoot, InitializeStore: true,
				GeneratorVersion: "test-v2", BuildState: corpus.BuildStateClean,
				CaptureReceipts: []corpus.CaptureReceipt{capture},
			})
			if err != nil || exported.Projection.Readiness != corpus.ProjectionPartial {
				t.Fatalf("projection=%#v error=%v", exported, err)
			}
			documents, _ := readCorpusExportProjection(t, storeRoot, corpus.ServiceConfluence)
			if len(documents) != 1 || documents[0].Evidence[2].Status != corpus.EvidenceForbidden {
				t.Fatalf("documents=%#v", documents)
			}
		})
	}
}

func TestConfluenceStrictIncompleteCommentsReturnClosedPartialEvidence(t *testing.T) {
	mirrorRoot := t.TempDir()
	backend := newConfluenceCorpusEvidenceStore()
	backend.comments = completeQualifiedComments()
	backend.comments.CommentsComplete = false
	backend.comments.PartialReasons = []string{domain.ConfluenceCommentPartialPageLimit}
	options := newCorpusPullEvidenceOptions(CorpusBuildOptions{
		Comments: true, MaxCommentPagesPerItem: 2, MaxCommentsPerItem: 10,
	})

	result, err := (&ConfluenceService{store: backend, baseURL: confluenceTestBackendURL}).Pull(t.Context(), PullOpts{
		Complete: true, Space: "DOC", MaxPages: 1, Into: mirrorRoot, Comments: true, evidence: options,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.Complete == nil || result.Complete.Complete {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	comments := confluencePullInclude(t, result, ConfluencePullIncludeComments)
	if comments.Qualification != ConfluencePullIncludePartial || comments.Complete == nil || *comments.Complete ||
		comments.Reason != ConfluencePullIncludeReasonInventoryIncomplete {
		t.Fatalf("comments include=%+v", comments)
	}
	if backend.commentReads != 1 {
		t.Fatalf("comment reads=%d", backend.commentReads)
	}
	var sidecars []string
	walkErr := filepath.WalkDir(mirrorRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".comments.json") {
			sidecars = append(sidecars, path)
		}
		return nil
	})
	if walkErr != nil || len(sidecars) != 0 {
		t.Fatalf("sidecars=%v error=%v", sidecars, walkErr)
	}
}
