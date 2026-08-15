package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type completeAttachmentResumeStore struct {
	*completePullStore
	inventories map[string]domain.AttachmentInventory
	bodies      map[string]string
	inventoryN  int
	bodyN       int
}

func (store *completeAttachmentResumeStore) GetMeta(_ context.Context, id string) (*domain.PageMeta, error) {
	page := store.pages[id]
	if page == nil {
		return nil, domain.ErrNotFound
	}
	return &domain.PageMeta{ID: page.ID, Version: page.Version}, nil
}

func (store *completeAttachmentResumeStore) ListAttachmentsQualifiedBounded(_ context.Context, id string, options domain.AttachmentReadOptions) (domain.AttachmentInventory, error) {
	store.inventoryN++
	if options.MaxPages != 2 || options.MaxItems != 10 {
		return domain.AttachmentInventory{}, errors.New("unexpected attachment bounds")
	}
	return store.inventories[id], nil
}

func (store *completeAttachmentResumeStore) RevalidateAttachmentDownload(
	ctx context.Context,
	pageID, filename string,
	version int,
) (domain.ConfluenceAttachmentDownloadEvidence, error) {
	if domain.ReadBudgetFromContext(ctx) == nil || !domain.SingleAttempt(ctx) {
		return domain.ConfluenceAttachmentDownloadEvidence{}, errors.New("missing bounded single-attempt revalidation context")
	}
	inventory, found := store.inventories[pageID]
	if !found {
		return domain.ConfluenceAttachmentDownloadEvidence{}, domain.ErrNotFound
	}
	var match *domain.Attachment
	for i := range inventory.Attachments {
		attachment := &inventory.Attachments[i]
		if attachment.Title != filename || attachment.Version != version {
			continue
		}
		if match != nil {
			return domain.ConfluenceAttachmentDownloadEvidence{}, domain.ErrCheckFailed
		}
		match = attachment
	}
	if match == nil {
		return domain.ConfluenceAttachmentDownloadEvidence{}, domain.ErrNotFound
	}
	return domain.ConfluenceAttachmentDownloadEvidence{
		AttachmentID: match.ID, PageID: pageID, Filename: match.Title, Version: match.Version, FileSize: match.FileSize,
	}, nil
}

func (store *completeAttachmentResumeStore) DownloadAttachment(_ context.Context, pageID, filename string, _ int) (io.ReadCloser, error) {
	store.bodyN++
	body, found := store.bodies[pageID+"/"+filename]
	if !found {
		return nil, domain.ErrNotFound
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

func publicConfluenceAttachmentPullOpts(root string) PullOpts {
	return PullOpts{
		Complete: true, Space: "DOC", MaxPages: 1, Into: root,
		Attachments: true, MaxAttachmentPagesPerItem: 2, MaxAttachmentsPerItem: 10,
	}
}

func TestConfluenceCompletePullPartialCommentsRequireExplicitOptIn(t *testing.T) {
	newPartialStore := func() *confluenceCorpusEvidenceStore {
		store := newConfluenceCorpusEvidenceStore()
		store.comments = completeQualifiedComments()
		store.comments.CommentsComplete = false
		store.comments.PartialReasons = []string{domain.ConfluenceCommentPartialPageLimit}
		return store
	}

	t.Run("strict default leaves checkpoint before partial sidecar", func(t *testing.T) {
		root := t.TempDir()
		result, err := (&ConfluenceService{store: newPartialStore(), baseURL: confluenceTestBackendURL}).Pull(t.Context(), PullOpts{
			Complete: true, Space: "DOC", MaxPages: 1, Into: root, Comments: true,
		})
		if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.Complete == nil || result.Complete.Complete {
			t.Fatalf("result=%#v error=%v", result, err)
		}
		comments := confluencePullInclude(t, result, ConfluencePullIncludeComments)
		if comments.Qualification != ConfluencePullIncludePartial || comments.Complete == nil || *comments.Complete ||
			comments.Reason != ConfluencePullIncludeReasonInventoryIncomplete {
			t.Fatalf("comments=%+v", comments)
		}
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() && strings.HasSuffix(path, ".comments.json") {
				t.Fatalf("strict pull published %s", path)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("explicit opt-in publishes qualified partial sidecar", func(t *testing.T) {
		root := t.TempDir()
		result, err := (&ConfluenceService{store: newPartialStore(), baseURL: confluenceTestBackendURL}).Pull(t.Context(), PullOpts{
			Complete: true, Space: "DOC", MaxPages: 1, Into: root, Comments: true, AllowPartialArtifacts: true,
		})
		if err != nil || result == nil || result.Complete == nil || !result.Complete.Complete || len(result.Pages) != 1 {
			t.Fatalf("result=%#v error=%v", result, err)
		}
		comments := confluencePullInclude(t, result, ConfluencePullIncludeComments)
		if comments.Qualification != ConfluencePullIncludePartial || comments.Complete == nil || *comments.Complete ||
			comments.Reason != ConfluencePullIncludeReasonInventoryIncomplete {
			t.Fatalf("comments=%+v", comments)
		}
		stem := strings.TrimSuffix(filepath.Join(root, filepath.FromSlash(result.Pages[0].Path)), ".csf")
		data, readErr := os.ReadFile(stem + ".comments.json")
		decoded, decodeErr := mirror.DecodeConfluenceCommentsSidecar(data)
		if readErr != nil || decodeErr != nil || decoded.V2 == nil || decoded.V2.Complete || decoded.V2.CommentsComplete {
			t.Fatalf("sidecar=%#v read=%v decode=%v", decoded, readErr, decodeErr)
		}
	})
}

func TestConfluenceCompletePullCapturesPublicAttachmentInventoryAndBodies(t *testing.T) {
	root := t.TempDir()
	store := newConfluenceCorpusEvidenceStore()
	opts := publicConfluenceAttachmentPullOpts(root)
	opts.AttachmentBodies = true
	opts.AttachmentMediaTypes = []string{"application/octet-stream"}
	opts.MaxAttachmentBytes = 16
	opts.MaxTotalAttachmentBytes = 64
	result, err := (&ConfluenceService{store: store, baseURL: confluenceTestBackendURL}).Pull(t.Context(), opts)
	if err != nil || result == nil || result.Complete == nil || !result.Complete.Complete || len(result.Pages) != 1 ||
		result.Pages[0].Attachments == nil || *result.Pages[0].Attachments != 1 || store.inventoryReads != 1 || store.bodyReads != 1 {
		t.Fatalf("result=%#v reads=%d/%d error=%v", result, store.inventoryReads, store.bodyReads, err)
	}
	attachments := confluencePullInclude(t, result, ConfluencePullIncludeAttachments)
	if attachments.Qualification != ConfluencePullIncludeQualified || attachments.Complete == nil || !*attachments.Complete {
		t.Fatalf("attachments=%+v", attachments)
	}
	stem := strings.TrimSuffix(filepath.Join(root, filepath.FromSlash(result.Pages[0].Path)), ".csf")
	data, readErr := os.ReadFile(stem + ".attachments.json")
	decoded, decodeErr := mirror.DecodeAttachmentSidecarV1(data)
	if readErr != nil || decodeErr != nil || !decoded.Complete || decoded.BodiesState != mirror.AttachmentBodiesComplete ||
		len(decoded.Attachments) != 1 || decoded.Attachments[0].Body.State != mirror.AttachmentBodyCaptured {
		t.Fatalf("sidecar=%#v read=%v decode=%v", decoded, readErr, decodeErr)
	}
	body, bodyErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(decoded.Attachments[0].Body.Path)))
	if bodyErr != nil || string(body) != "abc" {
		t.Fatalf("body=%q error=%v", body, bodyErr)
	}
}

func TestConfluenceCompletePullPartialAttachmentCaptureRemainsExplicit(t *testing.T) {
	newPartialStore := func() *confluenceCorpusEvidenceStore {
		store := newConfluenceCorpusEvidenceStore()
		store.inventory.Complete = false
		store.inventory.PartialReason = domain.AttachmentPartialItemLimit
		return store
	}

	t.Run("strict inventory policy fails closed", func(t *testing.T) {
		root := t.TempDir()
		result, err := (&ConfluenceService{store: newPartialStore(), baseURL: confluenceTestBackendURL}).Pull(t.Context(), publicConfluenceAttachmentPullOpts(root))
		if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.Complete == nil || result.Complete.Complete {
			t.Fatalf("result=%#v error=%v", result, err)
		}
		attachments := confluencePullInclude(t, result, ConfluencePullIncludeAttachments)
		if attachments.Qualification != ConfluencePullIncludePartial || attachments.Complete == nil || *attachments.Complete ||
			attachments.Reason != ConfluencePullIncludeReasonInventoryIncomplete || result.HasFailedInclude() {
			t.Fatalf("attachments=%+v", attachments)
		}
	})

	t.Run("opt-in preserves partial inventory sidecar", func(t *testing.T) {
		root := t.TempDir()
		opts := publicConfluenceAttachmentPullOpts(root)
		opts.AllowPartialArtifacts = true
		result, err := (&ConfluenceService{store: newPartialStore(), baseURL: confluenceTestBackendURL}).Pull(t.Context(), opts)
		if err != nil || result == nil || result.Complete == nil || !result.Complete.Complete || len(result.Pages) != 1 {
			t.Fatalf("result=%#v error=%v", result, err)
		}
		attachments := confluencePullInclude(t, result, ConfluencePullIncludeAttachments)
		if attachments.Qualification != ConfluencePullIncludePartial || attachments.Complete == nil || *attachments.Complete ||
			attachments.Reason != ConfluencePullIncludeReasonInventoryIncomplete {
			t.Fatalf("attachments=%+v", attachments)
		}
		stem := strings.TrimSuffix(filepath.Join(root, filepath.FromSlash(result.Pages[0].Path)), ".csf")
		data, readErr := os.ReadFile(stem + ".attachments.json")
		decoded, decodeErr := mirror.DecodeAttachmentSidecarV1(data)
		if readErr != nil || decodeErr != nil || decoded.Complete || decoded.InventoryComplete ||
			decoded.InventoryPartialReason != domain.AttachmentPartialItemLimit {
			t.Fatalf("sidecar=%#v read=%v decode=%v", decoded, readErr, decodeErr)
		}
	})

	t.Run("opt-in rejects an unversioned legacy inventory before publication", func(t *testing.T) {
		root := t.TempDir()
		store := newPartialStore()
		store.inventory.PartialReason = domain.AttachmentPartialLegacyUnqualified
		store.inventory.Attachments[0].Version = 0
		opts := publicConfluenceAttachmentPullOpts(root)
		opts.AllowPartialArtifacts = true
		result, err := (&ConfluenceService{store: store, baseURL: confluenceTestBackendURL}).Pull(t.Context(), opts)
		if !errors.Is(err, domain.ErrCheckFailed) || store.revalidateReads != 0 || store.bodyReads != 0 {
			t.Fatalf("result=%#v revalidations=%d body_reads=%d error=%v", result, store.revalidateReads, store.bodyReads, err)
		}
		if walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.HasSuffix(path, ".attachments.json") {
				t.Fatalf("unversioned inventory published %s", path)
			}
			return nil
		}); walkErr != nil {
			t.Fatal(walkErr)
		}
	})

	for name, mutate := range map[string]func(*domain.Attachment){
		"opaque attachment identity":          func(attachment *domain.Attachment) { attachment.ID = "att_opaque-1" },
		"uint64-overflow attachment identity": func(attachment *domain.Attachment) { attachment.ID = strings.Repeat("9", 64) },
		"sidecar-invalid metadata":            func(attachment *domain.Attachment) { attachment.Created = strings.Repeat("x", (64<<10)+1) },
		"overlong filename selector":          func(attachment *domain.Attachment) { attachment.Title = strings.Repeat("x", 256) },
	} {
		t.Run("opt-in rejects "+name+" before revalidation or publication", func(t *testing.T) {
			root := t.TempDir()
			store := newConfluenceCorpusEvidenceStore()
			mutate(&store.inventory.Attachments[0])
			opts := publicConfluenceAttachmentPullOpts(root)
			opts.AllowPartialArtifacts = true
			opts.AttachmentBodies = true
			opts.AttachmentMediaTypes = []string{"application/octet-stream"}
			opts.MaxAttachmentBytes = 16
			opts.MaxTotalAttachmentBytes = 64
			result, err := (&ConfluenceService{store: store, baseURL: confluenceTestBackendURL}).Pull(t.Context(), opts)
			if !errors.Is(err, domain.ErrCheckFailed) || store.revalidateReads != 0 || store.bodyReads != 0 {
				t.Fatalf("result=%#v revalidations=%d body_reads=%d error=%v", result, store.revalidateReads, store.bodyReads, err)
			}
			if result == nil || result.Complete == nil {
				t.Fatalf("result=%#v, want bounded unadvanced complete-pull checkpoint", result)
			}
			checkpoint, found, checkpointErr := mirror.New(root).CompletePullCheckpoint(result.Complete.SelectorSHA256)
			if checkpointErr != nil || !found || checkpoint.NextIndex != 0 || checkpoint.Includes.Attachments.Published != 0 || checkpoint.Includes.Attachments.BodyBytes != 0 {
				t.Fatalf("checkpoint=%+v found=%t error=%v, want no accepted attachment prefix", checkpoint, found, checkpointErr)
			}
			if walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if !entry.IsDir() && strings.HasSuffix(path, ".attachments.json") {
					t.Fatalf("invalid attachment inventory published %s", path)
				}
				return nil
			}); walkErr != nil {
				t.Fatal(walkErr)
			}
		})
	}
}

func TestConfluenceCompletePullPartialAttachmentBodiesUseBodyQualification(t *testing.T) {
	t.Run("strict policy stops before the checkpoint with partial evidence", func(t *testing.T) {
		root := t.TempDir()
		store := newConfluenceCorpusEvidenceStore()
		opts := publicConfluenceAttachmentPullOpts(root)
		opts.AttachmentBodies = true
		opts.AttachmentMediaTypes = []string{"application/octet-stream"}
		opts.MaxAttachmentBytes = 2
		opts.MaxTotalAttachmentBytes = 16
		result, err := (&ConfluenceService{store: store, baseURL: confluenceTestBackendURL}).Pull(t.Context(), opts)
		if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.Complete == nil || result.Complete.Complete {
			t.Fatalf("result=%#v error=%v", result, err)
		}
		attachments := confluencePullInclude(t, result, ConfluencePullIncludeAttachments)
		if attachments.Qualification != ConfluencePullIncludePartial || attachments.Complete == nil || *attachments.Complete ||
			attachments.Reason != ConfluencePullIncludeReasonBodyIncomplete || store.bodyReads != 0 {
			t.Fatalf("attachments=%+v body_reads=%d", attachments, store.bodyReads)
		}
	})

	t.Run("explicit opt-in preserves partial body qualification", func(t *testing.T) {
		root := t.TempDir()
		store := newConfluenceCorpusEvidenceStore()
		opts := publicConfluenceAttachmentPullOpts(root)
		opts.AllowPartialArtifacts = true
		opts.AttachmentBodies = true
		opts.AttachmentMediaTypes = []string{"application/octet-stream"}
		opts.MaxAttachmentBytes = 2
		opts.MaxTotalAttachmentBytes = 16
		result, err := (&ConfluenceService{store: store, baseURL: confluenceTestBackendURL}).Pull(t.Context(), opts)
		if err != nil || result == nil || result.Complete == nil || !result.Complete.Complete {
			t.Fatalf("result=%#v error=%v", result, err)
		}
		attachments := confluencePullInclude(t, result, ConfluencePullIncludeAttachments)
		if attachments.Qualification != ConfluencePullIncludePartial || attachments.Complete == nil || *attachments.Complete ||
			attachments.Reason != ConfluencePullIncludeReasonBodyIncomplete || store.bodyReads != 0 {
			t.Fatalf("attachments=%+v body_reads=%d", attachments, store.bodyReads)
		}
	})
}

func TestConfluenceCompletePullAttachmentFlagsRequireBoundedPolicy(t *testing.T) {
	store := newConfluenceCorpusEvidenceStore()
	_, err := (&ConfluenceService{store: store, baseURL: confluenceTestBackendURL}).Pull(t.Context(), PullOpts{
		Complete: true, Space: "DOC", Into: t.TempDir(), Attachments: true,
	})
	if !errors.Is(err, domain.ErrUsage) || store.inventoryReads != 0 || store.bodyReads != 0 {
		t.Fatalf("error=%v reads=%d/%d", err, store.inventoryReads, store.bodyReads)
	}
}

func TestConfluenceCompletePullAttachmentBodyPublicationBudgetIsAdmittedBeforeReads(t *testing.T) {
	store := newConfluenceCorpusEvidenceStore()
	opts := publicConfluenceAttachmentPullOpts(t.TempDir())
	opts.AttachmentBodies = true
	opts.AttachmentMediaTypes = []string{"application/octet-stream"}
	opts.MaxAttachmentBytes = 1
	opts.MaxTotalAttachmentBytes = confluenceCompletePullMaxAttachmentBodyBytes + 1
	if err := ValidateConfluencePullOptionalArtifacts(opts); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("validation error=%v", err)
	}
	if _, err := (&ConfluenceService{store: store, baseURL: confluenceTestBackendURL}).Pull(t.Context(), opts); !errors.Is(err, domain.ErrUsage) ||
		store.inventoryReads != 0 || store.bodyReads != 0 {
		t.Fatalf("pull error=%v inventory=%d bodies=%d", err, store.inventoryReads, store.bodyReads)
	}
}

func TestConfluenceCompleteAttachmentBodyPublicationLimitReservesStagedAssets(t *testing.T) {
	m := mirror.New(t.TempDir())
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	run := &confluencePullRun{
		complete: &confluenceCompleteSelection{}, mirror: m,
		opts: PullOpts{evidence: &corpusPullEvidenceOptions{binding: corpusEvidenceBinding{
			Attachments: true, AttachmentBodies: true, MaxAttachmentBodiesPerItem: confluenceCompletePullMaxAttachmentBodiesPerPage,
		}}},
	}
	assets := &stagedConfluenceAssetSink{assets: make([]stagedConfluenceAsset, 1_532)}
	limit, enforced, byteLimit, byteEnforced, err := run.completeAttachmentBodyPublicationLimit(&confluencePullFetchedPage{
		id: "10", rel: "DOC/page/page.csf",
	}, assets, 4, 0, 0, 0)
	// The prepared native/view/meta/base core contributes four entries; macro,
	// attachment sidecar and 1,532 assets leave 510 transaction slots.
	if err != nil || !enforced || limit != 510 || !byteEnforced || byteLimit != confluenceCompletePullMaxAttachmentBodyBytes {
		t.Fatalf("limit=%d enforced=%t byte_limit=%d byte_enforced=%t error=%v", limit, enforced, byteLimit, byteEnforced, err)
	}
}

func TestConfluenceCompleteAttachmentBodyPublicationLimitReservesKnownPayloadBytes(t *testing.T) {
	m := mirror.New(t.TempDir())
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	run := &confluencePullRun{
		complete: &confluenceCompleteSelection{}, mirror: m,
		opts: PullOpts{evidence: &corpusPullEvidenceOptions{binding: corpusEvidenceBinding{
			Attachments: true, AttachmentBodies: true, MaxAttachmentBodiesPerItem: confluenceCompletePullMaxAttachmentBodiesPerPage,
		}}},
	}
	assets := &stagedConfluenceAssetSink{bytes: 64 << 20}
	// 128 MiB of prepared native/view/meta/base/comment artifacts, 64 MiB of
	// assets, an exact 4 KiB attachment-sidecar reservation, and the 256 MiB
	// publisher cap leave 64 MiB. The public attachment cap independently keeps
	// the result at 64 MiB; increase the known core by one byte to prove the
	// transaction rather than only public-policy clipping supplies the bound.
	_, _, byteLimit, byteEnforced, err := run.completeAttachmentBodyPublicationLimit(&confluencePullFetchedPage{
		id: "10", rel: "DOC/page/page.csf",
	}, assets, 4, (128<<20)+1, 0, 4<<10)
	if err != nil || !byteEnforced || byteLimit != (64<<20)-1-(4<<10) {
		t.Fatalf("byte_limit=%d enforced=%t error=%v", byteLimit, byteEnforced, err)
	}
}

func TestConfluenceCompleteAttachmentBodyPublicationLimitReservesRelocationTombstonePayloadBeforeBodyOpen(t *testing.T) {
	m := mirror.New(t.TempDir())
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	old := completeTestPage("10")
	oldDir, oldSlug, err := m.ClaimPageDir(old.SpaceKey, nil, old.Title, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Write(oldDir, oldSlug, old, nil); err != nil {
		t.Fatal(err)
	}
	oldMD, err := os.ReadFile(filepath.Join(oldDir, oldSlug+".md"))
	if err != nil {
		t.Fatal(err)
	}
	moved := *old
	moved.Title = "Moved page"
	moved.Version++
	newDir, newSlug, err := m.ClaimPageDir(moved.SpaceKey, nil, moved.Title, moved.ID)
	if err != nil {
		t.Fatal(err)
	}
	newRel, err := filepath.Rel(m.Root, filepath.Join(newDir, newSlug+".csf"))
	if err != nil {
		t.Fatal(err)
	}
	relocation, err := m.PlanPageRelocation(old.ID, newRel, oldMD)
	if err != nil || relocation == nil {
		t.Fatalf("relocation=%#v error=%v", relocation, err)
	}
	_, relocationBytes, err := m.PageRelocationPublicationArtifactFootprint(relocation)
	if err != nil || relocationBytes == 0 {
		t.Fatalf("relocation_bytes=%d error=%v", relocationBytes, err)
	}

	options := corpusAttachmentTestOptions(false)
	run := &confluencePullRun{
		complete: &confluenceCompleteSelection{}, mirror: m,
		opts: PullOpts{evidence: options},
	}
	// Leave only two bytes for a three-byte attachment after the relocation's
	// nonempty tombstone and the reserved final sidecar. The page planner must
	// reject before the binary opener is reached.
	coreBytes := int64(256<<20) - relocationBytes - confluenceAttachmentSidecarPreflightReserve - 2
	_, _, bodyByteLimit, byteEnforced, err := run.completeAttachmentBodyPublicationLimit(&confluencePullFetchedPage{
		id: old.ID, rel: newRel, relocation: relocation,
	}, &stagedConfluenceAssetSink{}, 4, coreBytes, 0, confluenceAttachmentSidecarPreflightReserve)
	if err != nil || !byteEnforced || bodyByteLimit != 2 {
		t.Fatalf("body_byte_limit=%d enforced=%t error=%v", bodyByteLimit, byteEnforced, err)
	}
	opened := 0
	_, err = captureCorpusAttachmentsWithBodyLimit(
		t.Context(), t.TempDir(), mirror.CorpusSnapshotConfluence, old.ID, strings.TrimSuffix(newRel, ".csf"),
		domain.AttachmentInventory{Complete: true, Attachments: []domain.Attachment{{
			ID: "7", Title: "fixture.bin", MediaType: "application/octet-stream", FileSize: 3, Version: 1,
		}}}, options, 1, true, bodyByteLimit, true,
		func(context.Context, domain.Attachment) (io.ReadCloser, error) {
			opened++
			return io.NopCloser(strings.NewReader("abc")), nil
		},
	)
	if !errors.Is(err, domain.ErrCheckFailed) || opened != 0 {
		t.Fatalf("capture error=%v opened=%d", err, opened)
	}
}

func TestConfluenceCompletePullResumesAttachmentAggregateBodyBudget(t *testing.T) {
	root := t.TempDir()
	first, second := completeTestPage("10"), completeTestPage("20")
	store := &completeAttachmentResumeStore{
		completePullStore: &completePullStore{
			pullStore: &pullStore{
				pages:   map[string]*domain.Resource{"10": first, "20": second},
				getErrs: map[string]error{"20": domain.ErrForbidden},
			},
			searchSequence: []domain.PageSearchPage{completeSearchPage("10", "20"), completeSearchPage("10", "20")},
		},
		inventories: map[string]domain.AttachmentInventory{
			"10": {Complete: true, Attachments: []domain.Attachment{{ID: "7", Title: "one.bin", MediaType: "application/octet-stream", FileSize: 3, Version: 1}}},
			"20": {Complete: true, Attachments: []domain.Attachment{{ID: "8", Title: "two.bin", MediaType: "application/octet-stream", FileSize: 3, Version: 1}}},
		},
		bodies: map[string]string{"10/one.bin": "one", "20/two.bin": "two"},
	}
	opts := PullOpts{
		Complete: true, Space: "DOC", Into: root, Attachments: true,
		MaxAttachmentPagesPerItem: 2, MaxAttachmentsPerItem: 10,
		AttachmentBodies: true, AttachmentMediaTypes: []string{"application/octet-stream"},
		MaxAttachmentBytes: 3, MaxTotalAttachmentBytes: 3, AllowPartialArtifacts: true,
	}
	svc := &ConfluenceService{store: store, baseURL: confluenceTestBackendURL}
	firstResult, firstErr := svc.Pull(t.Context(), opts)
	if !errors.Is(firstErr, domain.ErrForbidden) || firstResult == nil || firstResult.Complete == nil || firstResult.Complete.Completed != 1 {
		t.Fatalf("interrupted result=%#v error=%v", firstResult, firstErr)
	}
	if checkpoint, found, checkpointErr := mirror.New(root).CompletePullCheckpoint(firstResult.Complete.SelectorSHA256); checkpointErr != nil || !found ||
		checkpoint.Includes.Attachments.BodyBytes != 3 || checkpoint.Includes.Attachments.Published != 1 {
		t.Fatalf("checkpoint=%+v found=%t error=%v", checkpoint, found, checkpointErr)
	}
	// The body budget is durable progress, not caller input. A schema-valid
	// lower number must be rejected before resume can re-open an attachment
	// endpoint and extend the aggregate beyond the policy cap.
	progressPath := filepath.Join(root, ".atl", "complete-pulls", firstResult.Complete.SelectorSHA256+".progress.json")
	progress, readErr := os.ReadFile(progressPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	tampered := strings.Replace(string(progress), `"body_bytes": 3`, `"body_bytes": 0`, 1)
	if tampered == string(progress) {
		t.Fatalf("progress omitted attachment budget: %s", progress)
	}
	if err := os.WriteFile(progressPath, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeInventory, beforeBodies := store.inventoryN, store.bodyN
	if resumed, resumeErr := svc.Pull(t.Context(), opts); !errors.Is(resumeErr, domain.ErrCheckFailed) || resumed != nil ||
		store.inventoryN != beforeInventory || store.bodyN != beforeBodies {
		t.Fatalf("tampered resume=%#v inventory=%d bodies=%d error=%v", resumed, store.inventoryN, store.bodyN, resumeErr)
	}
	if err := os.WriteFile(progressPath, progress, 0o600); err != nil {
		t.Fatal(err)
	}
	delete(store.getErrs, "20")
	result, err := svc.Pull(t.Context(), opts)
	if err != nil || result == nil || result.Complete == nil || !result.Complete.Complete || store.bodyN != 1 {
		t.Fatalf("resumed result=%#v body_reads=%d error=%v", result, store.bodyN, err)
	}
	attachments := confluencePullInclude(t, result, ConfluencePullIncludeAttachments)
	if attachments.Qualification != ConfluencePullIncludePartial || attachments.Complete == nil || *attachments.Complete ||
		attachments.Reason != ConfluencePullIncludeReasonBodyIncomplete {
		t.Fatalf("attachments=%+v", attachments)
	}
	stem := strings.TrimSuffix(filepath.Join(root, filepath.FromSlash(result.Pages[0].Path)), ".csf")
	data, readErr := os.ReadFile(stem + ".attachments.json")
	sidecar, decodeErr := mirror.DecodeAttachmentSidecarV1(data)
	if readErr != nil || decodeErr != nil || len(sidecar.Attachments) != 1 ||
		sidecar.Attachments[0].Body.Reason != mirror.AttachmentBodyReasonAggregateLimit {
		t.Fatalf("sidecar=%#v read=%v decode=%v", sidecar, readErr, decodeErr)
	}
}

func TestCorpusEvidenceConfluenceResumeRestoresAttachmentAggregateBudget(t *testing.T) {
	root := t.TempDir()
	first, second := completeTestPage("10"), completeTestPage("20")
	store := &completeAttachmentResumeStore{
		completePullStore: &completePullStore{
			pullStore: &pullStore{
				pages:   map[string]*domain.Resource{"10": first, "20": second},
				getErrs: map[string]error{"20": domain.ErrForbidden},
			},
			searchSequence: []domain.PageSearchPage{completeSearchPage("10", "20"), completeSearchPage("10", "20")},
		},
		inventories: map[string]domain.AttachmentInventory{
			"10": {Complete: true, Attachments: []domain.Attachment{{ID: "7", Title: "one.bin", MediaType: "application/octet-stream", FileSize: 3, Version: 1}}},
			"20": {Complete: true, Attachments: []domain.Attachment{{ID: "8", Title: "two.bin", MediaType: "application/octet-stream", FileSize: 3, Version: 1}}},
		},
		bodies: map[string]string{"10/one.bin": "one", "20/two.bin": "two"},
	}
	newOptions := func() PullOpts {
		evidence := newCorpusPullEvidenceOptions(CorpusBuildOptions{
			Attachments: true, MaxAttachmentPagesPerItem: 2, MaxAttachmentsPerItem: 10,
			AttachmentBodies: true, AttachmentMediaTypes: []string{"application/octet-stream"},
			MaxAttachmentBytes: 3, MaxTotalAttachmentBytes: 3, AllowPartialEvidence: true,
		})
		if evidence == nil {
			t.Fatal("corpus evidence policy is unavailable")
		}
		return PullOpts{Complete: true, Space: "DOC", Into: root, evidence: evidence}
	}
	svc := &ConfluenceService{store: store, baseURL: confluenceTestBackendURL}
	if result, err := svc.Pull(t.Context(), newOptions()); !errors.Is(err, domain.ErrForbidden) || result == nil || result.Complete == nil || result.Complete.Completed != 1 || store.bodyN != 1 {
		t.Fatalf("interrupted corpus-style pull=%#v body_reads=%d error=%v", result, store.bodyN, err)
	}
	delete(store.getErrs, "20")
	result, err := svc.Pull(t.Context(), newOptions())
	if err != nil || result == nil || result.Complete == nil || !result.Complete.Complete || store.bodyN != 1 {
		t.Fatalf("resumed corpus-style pull=%#v body_reads=%d error=%v", result, store.bodyN, err)
	}
	attachments := confluencePullInclude(t, result, ConfluencePullIncludeAttachments)
	if attachments.Qualification != ConfluencePullIncludePartial || attachments.Complete == nil || *attachments.Complete ||
		attachments.Reason != ConfluencePullIncludeReasonBodyIncomplete {
		t.Fatalf("attachments=%+v", attachments)
	}
}
