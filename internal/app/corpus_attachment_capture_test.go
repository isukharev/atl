package app

import (
	"context"
	"errors"
	"fmt"
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
		ID: "7", Title: "fixture.bin", MediaType: "application/octet-stream", FileSize: size, Version: 1,
	}}}
}

func TestCaptureCorpusAttachmentsStreamsVerifiedBody(t *testing.T) {
	root := t.TempDir()
	opened := 0
	options := corpusAttachmentTestOptions(false)
	capture, err := captureCorpusAttachments(t.Context(), root, mirror.CorpusSnapshotJira, "9", "PROJ/PROJ-1",
		corpusAttachmentTestInventory(3), options,
		func(context.Context, domain.Attachment) (io.ReadCloser, error) {
			opened++
			return io.NopCloser(strings.NewReader("abc")), nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if opened != 1 || capture.bodiesState != mirror.AttachmentBodiesComplete || len(capture.payloads) != 1 ||
		string(capture.payloads[0].data) != "abc" || capture.records[0].Body.Size != 3 || capture.records[0].Body.SHA256 != mirror.Hash([]byte("abc")) ||
		options.budget.usage() != 3 {
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

func TestCaptureCorpusAttachmentsPreflightsPerParentPublicationCount(t *testing.T) {
	inventory := domain.AttachmentInventory{Complete: true, Attachments: []domain.Attachment{
		{ID: "7", Title: "first.bin", MediaType: "application/octet-stream", FileSize: 3},
		{ID: "8", Title: "second.bin", MediaType: "application/octet-stream", FileSize: 3},
	}}

	t.Run("strict does not open a prefix", func(t *testing.T) {
		options := corpusAttachmentTestOptions(false)
		options.binding.MaxAttachmentBodiesPerItem = 1
		opened := 0
		_, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotJira, "9", "PROJ/PROJ-1", inventory, options,
			func(context.Context, domain.Attachment) (io.ReadCloser, error) {
				opened++
				return io.NopCloser(strings.NewReader("abc")), nil
			})
		if !errors.Is(err, domain.ErrCheckFailed) || opened != 0 {
			t.Fatalf("error=%v opened=%d", err, opened)
		}
	})

	t.Run("partial records the deterministic bounded subset", func(t *testing.T) {
		options := corpusAttachmentTestOptions(true)
		options.binding.MaxAttachmentBodiesPerItem = 1
		opened := 0
		capture, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotJira, "9", "PROJ/PROJ-1", inventory, options,
			func(_ context.Context, attachment domain.Attachment) (io.ReadCloser, error) {
				opened++
				if attachment.ID != "7" {
					t.Fatalf("opened out-of-plan attachment %q", attachment.ID)
				}
				return io.NopCloser(strings.NewReader("abc")), nil
			})
		if err != nil || opened != 1 || capture.bodiesState != mirror.AttachmentBodiesPartial || len(capture.records) != 2 ||
			capture.records[0].Body.State != mirror.AttachmentBodyCaptured ||
			capture.records[1].Body != (mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyExcluded, Reason: mirror.AttachmentBodyReasonCountLimit}) {
			t.Fatalf("capture=%+v error=%v opened=%d", capture, err, opened)
		}
	})

	t.Run("zero transaction slots never open a body", func(t *testing.T) {
		inventory := corpusAttachmentTestInventory(3)
		for _, partial := range []bool{false, true} {
			t.Run(map[bool]string{false: "strict", true: "partial"}[partial], func(t *testing.T) {
				options := corpusAttachmentTestOptions(partial)
				opened := 0
				capture, err := captureCorpusAttachmentsWithBodyLimit(t.Context(), t.TempDir(), mirror.CorpusSnapshotJira, "9", "PROJ/PROJ-1", inventory, options, 0, true, 0, false,
					func(context.Context, domain.Attachment) (io.ReadCloser, error) {
						opened++
						return io.NopCloser(strings.NewReader("abc")), nil
					})
				if !partial {
					if !errors.Is(err, domain.ErrCheckFailed) || opened != 0 {
						t.Fatalf("error=%v opened=%d", err, opened)
					}
					return
				}
				if err != nil || opened != 0 || capture.bodiesState != mirror.AttachmentBodiesPartial || len(capture.records) != 1 ||
					capture.records[0].Body != (mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyExcluded, Reason: mirror.AttachmentBodyReasonCountLimit}) {
					t.Fatalf("capture=%+v error=%v opened=%d", capture, err, opened)
				}
			})
		}
	})

	t.Run("per-parent byte envelope never opens an unstaged strict prefix", func(t *testing.T) {
		inventory := domain.AttachmentInventory{Complete: true, Attachments: []domain.Attachment{
			{ID: "7", Version: 1, Title: "first.bin", MediaType: "application/octet-stream", FileSize: 3},
			{ID: "8", Version: 1, Title: "second.bin", MediaType: "application/octet-stream", FileSize: 3},
		}}
		for _, partial := range []bool{false, true} {
			t.Run(map[bool]string{false: "strict", true: "partial"}[partial], func(t *testing.T) {
				options := corpusAttachmentTestOptions(partial)
				opened := 0
				capture, err := captureCorpusAttachmentsWithBodyLimit(
					t.Context(), t.TempDir(), mirror.CorpusSnapshotConfluence, "9", "items/one", inventory,
					options, 2, true, 3, true,
					func(context.Context, domain.Attachment) (io.ReadCloser, error) {
						opened++
						return io.NopCloser(strings.NewReader("abc")), nil
					},
				)
				if !partial {
					if !errors.Is(err, domain.ErrCheckFailed) || opened != 0 {
						t.Fatalf("error=%v opened=%d", err, opened)
					}
					return
				}
				if err != nil || opened != 1 || capture.bodiesState != mirror.AttachmentBodiesPartial || len(capture.records) != 2 ||
					capture.records[0].Body.State != mirror.AttachmentBodyCaptured ||
					capture.records[1].Body != (mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyExcluded, Reason: mirror.AttachmentBodyReasonAggregateLimit}) {
					t.Fatalf("capture=%+v error=%v opened=%d", capture, err, opened)
				}
			})
		}
	})

	t.Run("restored aggregate envelope never opens an unstaged strict prefix", func(t *testing.T) {
		inventory := domain.AttachmentInventory{Complete: true, Attachments: []domain.Attachment{
			{ID: "7", Version: 1, Title: "first.bin", MediaType: "application/octet-stream", FileSize: 3},
			{ID: "8", Version: 1, Title: "second.bin", MediaType: "application/octet-stream", FileSize: 3},
		}}
		for _, partial := range []bool{false, true} {
			t.Run(map[bool]string{false: "strict", true: "partial"}[partial], func(t *testing.T) {
				options := corpusAttachmentTestOptions(partial)
				// This models a resumed complete pull whose accepted durable prefix
				// already consumed 60 of its 64-byte aggregate policy.
				options.binding.MaxTotalAttachmentBytes = 64
				options.budget.maximum = 64
				options.budget.reserved = 60
				opened := 0
				capture, err := captureCorpusAttachmentsWithBodyLimit(
					t.Context(), t.TempDir(), mirror.CorpusSnapshotConfluence, "9", "items/one", inventory,
					options, 2, true, 64, true,
					func(context.Context, domain.Attachment) (io.ReadCloser, error) {
						opened++
						return io.NopCloser(strings.NewReader("abc")), nil
					},
				)
				if !partial {
					if !errors.Is(err, domain.ErrCheckFailed) || opened != 0 || options.budget.usage() != 60 {
						t.Fatalf("error=%v opened=%d usage=%d", err, opened, options.budget.usage())
					}
					return
				}
				if err != nil || opened != 1 || options.budget.usage() != 63 || capture.bodiesState != mirror.AttachmentBodiesPartial || len(capture.records) != 2 ||
					capture.records[0].Body.State != mirror.AttachmentBodyCaptured ||
					capture.records[1].Body != (mirror.AttachmentSidecarBody{State: mirror.AttachmentBodyExcluded, Reason: mirror.AttachmentBodyReasonAggregateLimit}) {
					t.Fatalf("capture=%+v error=%v opened=%d usage=%d", capture, err, opened, options.budget.usage())
				}
			})
		}
	})

	t.Run("item policy never opens an earlier strict sibling", func(t *testing.T) {
		inventory := domain.AttachmentInventory{Complete: true, Attachments: []domain.Attachment{
			{ID: "7", Version: 1, Title: "first.bin", MediaType: "application/octet-stream", FileSize: 3},
			{ID: "8", Version: 1, Title: "oversized.bin", MediaType: "application/octet-stream", FileSize: 9},
		}}
		options := corpusAttachmentTestOptions(false)
		opened := 0
		_, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotConfluence, "9", "items/one", inventory, options,
			func(context.Context, domain.Attachment) (io.ReadCloser, error) {
				opened++
				return io.NopCloser(strings.NewReader("abc")), nil
			})
		if !errors.Is(err, domain.ErrCheckFailed) || opened != 0 || options.budget.usage() != 0 {
			t.Fatalf("error=%v opened=%d usage=%d", err, opened, options.budget.usage())
		}
	})
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
			options := corpusAttachmentTestOptions(true)
			capture, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotJira, "9", "PROJ/PROJ-1",
				corpusAttachmentTestInventory(3), options,
				func(context.Context, domain.Attachment) (io.ReadCloser, error) { return test.reader, nil })
			if err != nil || capture.bodiesState != mirror.AttachmentBodiesPartial || len(capture.payloads) != 0 ||
				capture.records[0].Body.State != mirror.AttachmentBodyFailed || capture.records[0].Body.Reason != test.wantReason ||
				options.budget.usage() != 0 {
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
			if test.service == mirror.CorpusSnapshotConfluence {
				// Confluence sidecars bind every listed row to a positive
				// download-selector version, even for inventory-only evidence.
				inventory.Attachments[0].Version = 1
			}
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

func TestCaptureCorpusAttachmentsRejectsUnrepresentableConfluenceInventoryBeforeOpening(t *testing.T) {
	for name, mutate := range map[string]func(*domain.AttachmentInventory){
		"unversioned legacy row": func(inventory *domain.AttachmentInventory) {
			inventory.Complete = false
			inventory.PartialReason = domain.AttachmentPartialLegacyUnqualified
			inventory.Attachments[0].Version = 0
		},
		"opaque attachment id": func(inventory *domain.AttachmentInventory) {
			inventory.Attachments[0].ID = "att_opaque-1"
		},
		"uint64 overflow attachment id": func(inventory *domain.AttachmentInventory) {
			inventory.Attachments[0].ID = strings.Repeat("9", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			inventory := corpusAttachmentTestInventory(3)
			mutate(&inventory)
			opened := 0
			_, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotConfluence, "9", "items/one", inventory,
				corpusAttachmentTestOptions(true), func(context.Context, domain.Attachment) (io.ReadCloser, error) {
					opened++
					return io.NopCloser(strings.NewReader("abc")), nil
				})
			if !errors.Is(err, domain.ErrCheckFailed) || opened != 0 {
				t.Fatalf("error=%v opened=%d", err, opened)
			}
		})
	}
}

func TestCaptureCorpusAttachmentsRejectsConfluenceSidecarMetadataBeforeOpening(t *testing.T) {
	for name, mutate := range map[string]func(*domain.Attachment){
		"whitespace filename": func(attachment *domain.Attachment) { attachment.Title = " \t" },
		"overlong binary selector filename": func(attachment *domain.Attachment) {
			attachment.Title = strings.Repeat("x", 256)
		},
		"invalid UTF-8 author":       func(attachment *domain.Attachment) { attachment.Author = string([]byte{0xff}) },
		"overlong created timestamp": func(attachment *domain.Attachment) { attachment.Created = strings.Repeat("x", (64<<10)+1) },
	} {
		t.Run(name, func(t *testing.T) {
			inventory := corpusAttachmentTestInventory(3)
			inventory.Attachments[0].Version = 1
			mutate(&inventory.Attachments[0])
			opened := 0
			_, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotConfluence, "9", "items/one", inventory,
				corpusAttachmentTestOptions(true), func(context.Context, domain.Attachment) (io.ReadCloser, error) {
					opened++
					return io.NopCloser(strings.NewReader("abc")), nil
				})
			if !errors.Is(err, domain.ErrCheckFailed) || opened != 0 {
				t.Fatalf("error=%v opened=%d", err, opened)
			}
		})
	}
}

func TestCaptureCorpusAttachmentsRejectsOversizedConfluenceSidecarBeforeOpening(t *testing.T) {
	const count = 257 // 257 × 64 KiB valid fields exceed the 16 MiB sidecar cap.
	inventory := domain.AttachmentInventory{Complete: true, Attachments: make([]domain.Attachment, 0, count)}
	for index := 0; index < count; index++ {
		inventory.Attachments = append(inventory.Attachments, domain.Attachment{
			ID: fmt.Sprintf("%d", index+1), Version: 1, Title: "fixture.bin", FileSize: 0,
			Created: strings.Repeat("x", 64<<10),
		})
	}
	options := corpusAttachmentTestOptions(true)
	options.binding.AttachmentBodies = false
	opened := 0
	_, err := captureCorpusAttachments(t.Context(), t.TempDir(), mirror.CorpusSnapshotConfluence, "9", "items/one", inventory, options,
		func(context.Context, domain.Attachment) (io.ReadCloser, error) {
			opened++
			return io.NopCloser(strings.NewReader("")), nil
		})
	if !errors.Is(err, domain.ErrCheckFailed) || opened != 0 {
		t.Fatalf("error=%v opened=%d", err, opened)
	}
}

func TestCorpusAttachmentPartialReasonsRemainUnique(t *testing.T) {
	capture := corpusAttachmentCapture{partialReasons: []mirror.AttachmentPartialReason{mirror.AttachmentReasonBodyFailed}}
	if err := capture.notePartial(corpusAttachmentTestOptions(true), mirror.AttachmentReasonBodyFailed); err != nil ||
		len(capture.partialReasons) != 1 || capture.bodiesState != mirror.AttachmentBodiesPartial {
		t.Fatalf("capture=%+v error=%v", capture, err)
	}
}
