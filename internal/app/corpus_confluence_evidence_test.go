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

type confluenceCorpusEvidenceStore struct {
	*completePullStore
	inventory      domain.AttachmentInventory
	inventoryErr   error
	body           string
	metaVersion    int
	driftAfterRead bool
	inventoryReads int
	bodyReads      int
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

func (store *confluenceCorpusEvidenceStore) DownloadAttachment(_ context.Context, pageID, filename string, version int) (io.ReadCloser, error) {
	store.bodyReads++
	if pageID != "10" || filename != "a.bin" || version != 2 {
		return nil, errors.New("unexpected attachment download")
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
		body: "abc", metaVersion: 3,
	}
}

func confluenceCorpusEvidenceOptions(partial bool) *corpusPullEvidenceOptions {
	return newCorpusPullEvidenceOptions(CorpusBuildOptions{
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
	if result.Complete == nil || !result.Complete.Complete || store.inventoryReads != 1 || store.bodyReads != 1 {
		t.Fatalf("result=%+v reads=%d/%d", result.Complete, store.inventoryReads, store.bodyReads)
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

func TestConfluenceAttachmentParentDriftPreventsPublication(t *testing.T) {
	root := t.TempDir()
	store := newConfluenceCorpusEvidenceStore()
	store.driftAfterRead = true
	result, err := (&ConfluenceService{store: store, baseURL: confluenceTestBackendURL}).Pull(t.Context(), PullOpts{
		Complete: true, Space: "DOC", MaxPages: 1, Into: root,
		evidence: confluenceCorpusEvidenceOptions(false),
	})
	if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.Complete == nil || result.Complete.Complete {
		t.Fatalf("result=%+v error=%v", result, err)
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
