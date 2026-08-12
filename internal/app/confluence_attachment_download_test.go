package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

type boundedAttachmentDownloadStore struct {
	domain.DocStore
	evidence        domain.ConfluenceAttachmentDownloadEvidence
	revalidateErr   error
	revalidateCalls int
	downloadCalls   int
	downloadVersion int
	checkBounds     bool
	exhaustAttempts bool
	exhaustBytes    bool
	waitForDeadline bool
}

func (s *boundedAttachmentDownloadStore) RevalidateAttachmentDownload(ctx context.Context, pageID, filename string, version int) (domain.ConfluenceAttachmentDownloadEvidence, error) {
	s.revalidateCalls++
	if pageID != "10" || filename != "diagram.png" && filename != "missing.png" || version < 0 {
		return domain.ConfluenceAttachmentDownloadEvidence{}, errors.New("unexpected selector")
	}
	if s.checkBounds {
		if !domain.SingleAttempt(ctx) || domain.ReadBudgetFromContext(ctx) == nil {
			return domain.ConfluenceAttachmentDownloadEvidence{}, errors.New("missing bounded single-attempt context")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > confluenceAttachmentDownloadMetadataDeadline {
			return domain.ConfluenceAttachmentDownloadEvidence{}, errors.New("missing metadata deadline")
		}
		budget := domain.ReadBudgetFromContext(ctx)
		if s.exhaustAttempts {
			for range confluenceAttachmentDownloadMaxRequests {
				if err := budget.TakeAttempt(); err != nil {
					return domain.ConfluenceAttachmentDownloadEvidence{}, errors.New("request budget rejected too early")
				}
			}
			return domain.ConfluenceAttachmentDownloadEvidence{}, budget.TakeAttempt()
		}
		if err := budget.TakeAttempt(); err != nil {
			return domain.ConfluenceAttachmentDownloadEvidence{}, err
		}
		remaining, finish, err := budget.BeginResponse(ctx)
		if err != nil {
			return domain.ConfluenceAttachmentDownloadEvidence{}, err
		}
		if remaining != confluenceAttachmentDownloadMaxResponseBytes {
			finish(0)
			return domain.ConfluenceAttachmentDownloadEvidence{}, errors.New("unexpected response-byte budget")
		}
		if s.exhaustBytes {
			finish(remaining)
			return domain.ConfluenceAttachmentDownloadEvidence{}, domain.ErrReadResponseBudgetExhausted
		}
		finish(7)
	}
	if s.revalidateErr != nil {
		return domain.ConfluenceAttachmentDownloadEvidence{}, s.revalidateErr
	}
	if s.waitForDeadline {
		<-ctx.Done()
		return domain.ConfluenceAttachmentDownloadEvidence{}, ctx.Err()
	}
	return s.evidence, nil
}

func (s *boundedAttachmentDownloadStore) DownloadAttachment(ctx context.Context, _ string, _ string, version int) (io.ReadCloser, error) {
	s.downloadCalls++
	s.downloadVersion = version
	if s.checkBounds {
		if !domain.SingleAttempt(ctx) || domain.ReadBudgetFromContext(ctx) == nil {
			return nil, errors.New("download escaped request budget")
		}
		if err := domain.ReadBudgetFromContext(ctx).TakeAttempt(); err != nil {
			return nil, err
		}
	}
	return io.NopCloser(bytes.NewReader([]byte("native attachment bytes"))), nil
}

func TestDownloadAttachmentRevalidatesAndPinsLatestPositiveVersion(t *testing.T) {
	store := &boundedAttachmentDownloadStore{checkBounds: true, evidence: domain.ConfluenceAttachmentDownloadEvidence{
		AttachmentID: "21", PageID: "10", Filename: "diagram.png", Version: 3,
	}}
	root := t.TempDir()
	result, err := (&ConfluenceService{store: store}).DownloadAttachmentKnownPage(t.Context(), "10", "diagram.png", 0, root)
	if err != nil {
		t.Fatalf("DownloadAttachmentKnownPage: %v", err)
	}
	if store.revalidateCalls != 1 || store.downloadCalls != 1 || store.downloadVersion != 3 {
		t.Fatalf("revalidation/download=%d/%d version=%d", store.revalidateCalls, store.downloadCalls, store.downloadVersion)
	}
	if result.RequestedAttachmentVersion != 0 || result.ObservedAttachmentVersion != 3 || result.ObservedAttachmentID != "21" ||
		result.Selector != ConfluenceAttachmentSelectorLatest || !result.IdentityRevalidated || result.AttachmentIDBound || result.PageVersionGated {
		t.Fatalf("result=%+v", result)
	}
	if got, readErr := os.ReadFile(filepath.Join(root, "diagram.png")); readErr != nil || string(got) != "native attachment bytes" {
		t.Fatalf("written=%q err=%v", got, readErr)
	}
}

func TestDownloadAttachmentPositiveVersionMustMatchEvidence(t *testing.T) {
	store := &boundedAttachmentDownloadStore{evidence: domain.ConfluenceAttachmentDownloadEvidence{
		AttachmentID: "21", PageID: "10", Filename: "diagram.png", Version: 3,
	}}
	root := t.TempDir()
	result, err := (&ConfluenceService{store: store}).DownloadAttachmentKnownPage(t.Context(), "10", "diagram.png", 2, root)
	if !errors.Is(err, domain.ErrCheckFailed) || result != nil || store.downloadCalls != 0 {
		t.Fatalf("result=%+v err=%v download_calls=%d", result, err, store.downloadCalls)
	}
	if entries, readErr := os.ReadDir(root); readErr != nil || len(entries) != 0 {
		t.Fatalf("failed qualification wrote files=%v err=%v", entries, readErr)
	}
}

func TestDownloadAttachmentQualificationFailureMakesNoBinaryRequestOrWrite(t *testing.T) {
	store := &boundedAttachmentDownloadStore{revalidateErr: domain.ErrNotFound}
	root := t.TempDir()
	result, err := (&ConfluenceService{store: store}).DownloadAttachmentKnownPage(t.Context(), "10", "missing.png", 0, root)
	if !errors.Is(err, domain.ErrNotFound) || result != nil || store.downloadCalls != 0 {
		t.Fatalf("result=%+v err=%v download_calls=%d", result, err, store.downloadCalls)
	}
	if entries, readErr := os.ReadDir(root); readErr != nil || len(entries) != 0 {
		t.Fatalf("failed qualification wrote files=%v err=%v", entries, readErr)
	}
}

func TestDownloadAttachmentMetadataDeadlineHonorsEarlierCallerDeadline(t *testing.T) {
	store := &boundedAttachmentDownloadStore{waitForDeadline: true}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := (&ConfluenceService{store: store}).DownloadAttachmentKnownPage(ctx, "10", "diagram.png", 0, t.TempDir())
	if !errors.Is(err, context.DeadlineExceeded) || store.downloadCalls != 0 {
		t.Fatalf("err=%v download_calls=%d", err, store.downloadCalls)
	}
}

func TestDownloadAttachmentMetadataBudgetsFailBeforeBinaryRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*boundedAttachmentDownloadStore)
		want error
	}{
		{name: "physical requests", set: func(s *boundedAttachmentDownloadStore) { s.exhaustAttempts = true }, want: domain.ErrReadAttemptBudgetExhausted},
		{name: "response bytes", set: func(s *boundedAttachmentDownloadStore) { s.exhaustBytes = true }, want: domain.ErrReadResponseBudgetExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &boundedAttachmentDownloadStore{checkBounds: true}
			tc.set(store)
			_, err := (&ConfluenceService{store: store}).DownloadAttachmentKnownPage(t.Context(), "10", "diagram.png", 0, t.TempDir())
			if !errors.Is(err, tc.want) || store.downloadCalls != 0 {
				t.Fatalf("err=%v download_calls=%d", err, store.downloadCalls)
			}
		})
	}
}
