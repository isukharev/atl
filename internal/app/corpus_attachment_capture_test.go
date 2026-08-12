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

type corpusAttachmentTestReadCloser struct {
	io.Reader
	closeErr error
}

func (reader *corpusAttachmentTestReadCloser) Close() error { return reader.closeErr }

func corpusAttachmentTestOptions(partial bool) *corpusPullEvidenceOptions {
	return newCorpusPullEvidenceOptions(CorpusBuildOptions{
		Attachments: true, MaxAttachmentPagesPerItem: 2, MaxAttachmentsPerItem: 10,
		AttachmentBodies: true, AttachmentMediaTypes: []string{"application/octet-stream"},
		MaxAttachmentBytes: 8, MaxTotalAttachmentBytes: 16, AllowPartialEvidence: partial,
	})
}

func corpusAttachmentTestInventory(size int64) domain.AttachmentInventory {
	return domain.AttachmentInventory{Complete: true, Attachments: []domain.Attachment{{
		ID: "7", Title: "fixture.bin", MediaType: "application/octet-stream", FileSize: size,
	}}}
}

func TestCaptureCorpusAttachmentsStreamsVerifiedBody(t *testing.T) {
	root := t.TempDir()
	opened := 0
	capture, err := captureCorpusAttachments(t.Context(), root, mirror.CorpusSnapshotJira, "9", "PROJ/PROJ-1",
		corpusAttachmentTestInventory(3), corpusAttachmentTestOptions(false),
		func(context.Context, domain.Attachment) (io.ReadCloser, error) {
			opened++
			return io.NopCloser(strings.NewReader("abc")), nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if opened != 1 || capture.bodiesState != mirror.AttachmentBodiesComplete || len(capture.payloads) != 1 ||
		string(capture.payloads[0].data) != "abc" || capture.records[0].Body.Size != 3 || capture.records[0].Body.SHA256 != mirror.Hash([]byte("abc")) {
		t.Fatalf("capture=%+v opened=%d", capture, opened)
	}
	if _, err := os.Stat(filepath.Join(root, ".atl", "corpus-attachment-staging", "9", "7.body")); !os.IsNotExist(err) {
		t.Fatalf("staging residue=%v", err)
	}
}

func TestCaptureCorpusAttachmentsEnforcesPolicyBeforeOpeningBody(t *testing.T) {
	for name, test := range map[string]struct {
		mutate     func(*domain.Attachment, *corpusPullEvidenceOptions)
		wantReason mirror.AttachmentBodyReason
	}{
		"media": {func(attachment *domain.Attachment, _ *corpusPullEvidenceOptions) {
			attachment.MediaType = "text/plain"
		}, mirror.AttachmentBodyReasonMediaExcluded},
		"item limit": {func(attachment *domain.Attachment, _ *corpusPullEvidenceOptions) {
			attachment.FileSize = 9
		}, mirror.AttachmentBodyReasonItemLimit},
		"aggregate limit": {func(_ *domain.Attachment, options *corpusPullEvidenceOptions) {
			options.budget.maximum = 2
		}, mirror.AttachmentBodyReasonAggregateLimit},
	} {
		t.Run(name, func(t *testing.T) {
			inventory := corpusAttachmentTestInventory(3)
			options := corpusAttachmentTestOptions(true)
			test.mutate(&inventory.Attachments[0], options)
			opened := false
			capture, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotJira, "9", "PROJ/PROJ-1", inventory, options,
				func(context.Context, domain.Attachment) (io.ReadCloser, error) {
					opened = true
					return io.NopCloser(strings.NewReader("abc")), nil
				})
			if err != nil || opened || capture.records[0].Body.Reason != test.wantReason {
				t.Fatalf("capture=%+v opened=%t error=%v", capture, opened, err)
			}
			if name == "media" && capture.bodiesState != mirror.AttachmentBodiesComplete {
				t.Fatalf("explicit media exclusion became partial: %+v", capture)
			}
			if name != "media" && capture.bodiesState != mirror.AttachmentBodiesPartial {
				t.Fatalf("limit did not become partial: %+v", capture)
			}
		})
	}
}

func TestCaptureCorpusAttachmentsStrictPolicyRejectsLimit(t *testing.T) {
	inventory := corpusAttachmentTestInventory(9)
	_, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotJira, "9", "PROJ/PROJ-1", inventory,
		corpusAttachmentTestOptions(false), func(context.Context, domain.Attachment) (io.ReadCloser, error) {
			t.Fatal("body opened after static limit")
			return nil, nil
		})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error=%v", err)
	}
}

func TestCaptureCorpusAttachmentsRecordsStreamFailuresOnlyInPartialMode(t *testing.T) {
	for name, test := range map[string]struct {
		reader     io.ReadCloser
		wantReason mirror.AttachmentBodyReason
	}{
		"short": {io.NopCloser(strings.NewReader("ab")), mirror.AttachmentBodyReasonSizeMismatch},
		"long":  {io.NopCloser(strings.NewReader("abcd")), mirror.AttachmentBodyReasonSizeMismatch},
		"close": {&corpusAttachmentTestReadCloser{Reader: strings.NewReader("abc"), closeErr: errors.New("close failed")}, mirror.AttachmentBodyReasonFailed},
	} {
		t.Run(name, func(t *testing.T) {
			capture, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotJira, "9", "PROJ/PROJ-1",
				corpusAttachmentTestInventory(3), corpusAttachmentTestOptions(true),
				func(context.Context, domain.Attachment) (io.ReadCloser, error) { return test.reader, nil })
			if err != nil || capture.bodiesState != mirror.AttachmentBodiesPartial || len(capture.payloads) != 0 ||
				capture.records[0].Body.State != mirror.AttachmentBodyFailed || capture.records[0].Body.Reason != test.wantReason {
				t.Fatalf("capture=%+v error=%v", capture, err)
			}
		})
	}
}

func TestStreamCorpusAttachmentRejectsSymlinkedStaging(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".atl", "corpus-attachment-staging")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, _, err := streamCorpusAttachment(t.Context(), root, "9", "7", 3, strings.NewReader("abc"))
	if err == nil {
		t.Fatal("symlinked staging was accepted")
	}
	if entries, readErr := os.ReadDir(outside); readErr != nil || len(entries) != 0 {
		t.Fatalf("outside entries=%v error=%v", entries, readErr)
	}
}

func TestCaptureCorpusAttachmentsQualifiesEveryInventoryOutcome(t *testing.T) {
	for name, test := range map[string]struct {
		service string
		reason  string
		want    mirror.AttachmentPartialReason
	}{
		"page limit":         {mirror.CorpusSnapshotConfluence, domain.AttachmentPartialPageLimit, mirror.AttachmentReasonInventoryPageLimit},
		"item limit":         {mirror.CorpusSnapshotConfluence, domain.AttachmentPartialItemLimit, mirror.AttachmentReasonInventoryItemLimit},
		"pagination stalled": {mirror.CorpusSnapshotConfluence, domain.AttachmentPartialPaginationStalled, mirror.AttachmentReasonInventoryStalled},
		"legacy":             {mirror.CorpusSnapshotConfluence, domain.AttachmentPartialLegacyUnqualified, mirror.AttachmentReasonInventoryLegacy},
		"jira field":         {mirror.CorpusSnapshotJira, domain.JiraAttachmentPartialFieldUnavailable, mirror.AttachmentReasonInventoryField},
		"forbidden":          {mirror.CorpusSnapshotJira, mirror.AttachmentInventoryForbidden, mirror.AttachmentReasonInventoryForbidden},
		"unsupported":        {mirror.CorpusSnapshotJira, mirror.AttachmentInventoryUnsupported, mirror.AttachmentReasonInventoryUnsupported},
	} {
		t.Run(name, func(t *testing.T) {
			options := corpusAttachmentTestOptions(true)
			options.binding.AttachmentBodies = false
			inventory := corpusAttachmentTestInventory(3)
			inventory.Complete = false
			inventory.PartialReason = test.reason
			capture, err := captureCorpusAttachments(t.Context(), t.TempDir(), test.service, "9", "items/one", inventory, options,
				func(context.Context, domain.Attachment) (io.ReadCloser, error) {
					t.Fatal("body opened when body capture was not requested")
					return nil, nil
				})
			if err != nil || capture.bodiesState != mirror.AttachmentBodiesNotRequested ||
				len(capture.partialReasons) != 1 || capture.partialReasons[0] != test.want ||
				capture.records[0].Body.State != mirror.AttachmentBodyNotRequested {
				t.Fatalf("capture=%+v error=%v", capture, err)
			}
		})
	}

	options := corpusAttachmentTestOptions(true)
	inventory := corpusAttachmentTestInventory(3)
	inventory.Complete = false
	inventory.PartialReason = "future-reason"
	if _, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotJira, "9", "items/one", inventory, options, nil); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unknown inventory reason error=%v", err)
	}
	options = corpusAttachmentTestOptions(false)
	inventory.PartialReason = domain.AttachmentPartialItemLimit
	if _, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotJira, "9", "items/one", inventory, options, nil); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("strict incomplete inventory error=%v", err)
	}
}

func TestCaptureCorpusAttachmentsRecordsBodyOpenOutcomes(t *testing.T) {
	for name, test := range map[string]struct {
		openErr    error
		partial    bool
		wantState  mirror.AttachmentBodyState
		wantReason mirror.AttachmentBodyReason
		wantErr    bool
	}{
		"forbidden partial": {domain.ErrForbidden, true, mirror.AttachmentBodyForbidden, mirror.AttachmentBodyReasonForbidden, false},
		"failed partial":    {errors.New("synthetic read failure"), true, mirror.AttachmentBodyFailed, mirror.AttachmentBodyReasonFailed, false},
		"failed strict":     {errors.New("synthetic read failure"), false, "", "", true},
	} {
		t.Run(name, func(t *testing.T) {
			capture, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotJira, "9", "items/one",
				corpusAttachmentTestInventory(3), corpusAttachmentTestOptions(test.partial),
				func(context.Context, domain.Attachment) (io.ReadCloser, error) { return nil, test.openErr })
			if test.wantErr {
				if !errors.Is(err, domain.ErrCheckFailed) {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil || capture.bodiesState != mirror.AttachmentBodiesPartial || len(capture.records) != 1 ||
				capture.records[0].Body.State != test.wantState || capture.records[0].Body.Reason != test.wantReason {
				t.Fatalf("capture=%+v error=%v", capture, err)
			}
		})
	}
}

func TestCaptureCorpusAttachmentsRejectsUnavailableInputsAndUnsafeIdentity(t *testing.T) {
	inventory := corpusAttachmentTestInventory(3)
	if _, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotJira, "9", "items/one", inventory, nil, nil); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("nil policy error=%v", err)
	}
	options := corpusAttachmentTestOptions(true)
	options.budget = nil
	if _, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotJira, "9", "items/one", inventory, options, nil); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("missing budget error=%v", err)
	}
	options = corpusAttachmentTestOptions(true)
	if _, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotJira, "9", "items/one", domain.AttachmentInventory{}, options, nil); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("nil inventory error=%v", err)
	}
	inventory = corpusAttachmentTestInventory(3)
	inventory.Attachments[0].ID = "../outside"
	if _, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotJira, "9", "items/one", inventory, options,
		func(context.Context, domain.Attachment) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("abc")), nil
		}); err == nil {
		t.Fatal("unsafe attachment identity was accepted")
	}
}

func TestCorpusAttachmentPartialReasonsRemainUnique(t *testing.T) {
	capture := corpusAttachmentCapture{partialReasons: []mirror.AttachmentPartialReason{mirror.AttachmentReasonBodyFailed}}
	if err := capture.notePartial(corpusAttachmentTestOptions(true), mirror.AttachmentReasonBodyFailed); err != nil ||
		len(capture.partialReasons) != 1 || capture.bodiesState != mirror.AttachmentBodiesPartial {
		t.Fatalf("capture=%+v error=%v", capture, err)
	}
}
