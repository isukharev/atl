package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const attachmentDownloadBody = "native attachment bytes"

type boundedAttachmentDownloadStore struct {
	domain.DocStore
	evidence         domain.ConfluenceAttachmentDownloadEvidence
	revalidateErr    error
	revalidateCalls  int
	downloadCalls    int
	downloadVersion  int
	metadataBudget   *domain.ReadBudget
	metadataContext  context.Context
	checkBounds      bool
	exhaustAttempts  bool
	exhaustBytes     bool
	waitForDeadline  bool
	downloadBody     io.ReadCloser
	downloadErr      error
	downloadCheckErr error
}

func downloadEvidence(size int64) domain.ConfluenceAttachmentDownloadEvidence {
	return domain.ConfluenceAttachmentDownloadEvidence{
		AttachmentID: "21", PageID: "10", Filename: "diagram.png", Version: 3, FileSize: size,
	}
}

func (s *boundedAttachmentDownloadStore) RevalidateAttachmentDownload(ctx context.Context, pageID, filename string, version int) (domain.ConfluenceAttachmentDownloadEvidence, error) {
	s.revalidateCalls++
	s.metadataContext = ctx
	if pageID != s.evidence.PageID && pageID != "10" || filename != "diagram.png" && filename != "missing.png" || version < 0 {
		return domain.ConfluenceAttachmentDownloadEvidence{}, errors.New("unexpected selector")
	}
	if s.checkBounds {
		s.metadataBudget = domain.ReadBudgetFromContext(ctx)
		if !domain.SingleAttempt(ctx) || s.metadataBudget == nil {
			return domain.ConfluenceAttachmentDownloadEvidence{}, errors.New("missing bounded single-attempt metadata context")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > confluenceAttachmentDownloadMetadataDeadline {
			return domain.ConfluenceAttachmentDownloadEvidence{}, errors.New("missing metadata deadline")
		}
		if s.exhaustAttempts {
			for range confluenceAttachmentDownloadMaxRequests {
				if err := s.metadataBudget.TakeAttempt(); err != nil {
					return domain.ConfluenceAttachmentDownloadEvidence{}, errors.New("request budget rejected too early")
				}
			}
			return domain.ConfluenceAttachmentDownloadEvidence{}, s.metadataBudget.TakeAttempt()
		}
		if err := s.metadataBudget.TakeAttempt(); err != nil {
			return domain.ConfluenceAttachmentDownloadEvidence{}, err
		}
		remaining, finish, err := s.metadataBudget.BeginResponse(ctx)
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
		budget := domain.ReadBudgetFromContext(ctx)
		if domain.SingleAttempt(ctx) || !domain.NoReplayRetries(ctx) || budget == nil || budget == s.metadataBudget {
			s.downloadCheckErr = errors.New("binary did not receive its separate bounded no-replay context")
			return nil, s.downloadCheckErr
		}
		if s.metadataContext == nil || !errors.Is(s.metadataContext.Err(), context.Canceled) {
			s.downloadCheckErr = errors.New("metadata context was not canceled before binary read")
			return nil, s.downloadCheckErr
		}
		if err := budget.TakeAttempt(); err != nil {
			return nil, err
		}
	}
	if s.downloadErr != nil {
		return nil, s.downloadErr
	}
	if s.downloadBody != nil {
		return s.downloadBody, nil
	}
	return io.NopCloser(strings.NewReader(attachmentDownloadBody)), nil
}

func TestDownloadAttachmentRevalidatesPinsSizeAndSeparatesBinaryBudget(t *testing.T) {
	store := &boundedAttachmentDownloadStore{checkBounds: true, evidence: downloadEvidence(int64(len(attachmentDownloadBody)))}
	root := t.TempDir()
	result, err := (&ConfluenceService{store: store}).DownloadAttachmentKnownPage(t.Context(), "10", "diagram.png", 0, root)
	if err != nil {
		t.Fatalf("DownloadAttachmentKnownPage: %v", err)
	}
	if store.revalidateCalls != 1 || store.downloadCalls != 1 || store.downloadVersion != 3 || store.downloadCheckErr != nil {
		t.Fatalf("revalidation/download=%d/%d version=%d check=%v", store.revalidateCalls, store.downloadCalls, store.downloadVersion, store.downloadCheckErr)
	}
	if result.RequestedAttachmentVersion != 0 || result.ObservedAttachmentVersion != 3 || result.ObservedAttachmentID != "21" ||
		result.ObservedFileSize != int64(len(attachmentDownloadBody)) || result.MaxBytes != ConfluenceAttachmentDownloadDefaultMaxBytes ||
		result.Selector != ConfluenceAttachmentSelectorLatest || !result.IdentityRevalidated || result.AttachmentIDBound || result.PageVersionGated {
		t.Fatalf("result=%+v", result)
	}
	if got, readErr := os.ReadFile(filepath.Join(root, "diagram.png")); readErr != nil || string(got) != attachmentDownloadBody {
		t.Fatalf("written=%q err=%v", got, readErr)
	}
}

func TestDownloadAttachmentPositiveVersionMustMatchEvidence(t *testing.T) {
	store := &boundedAttachmentDownloadStore{evidence: downloadEvidence(0)}
	root := t.TempDir()
	result, err := (&ConfluenceService{store: store}).DownloadAttachmentKnownPage(t.Context(), "10", "diagram.png", 2, root)
	if !errors.Is(err, domain.ErrCheckFailed) || result != nil || store.downloadCalls != 0 {
		t.Fatalf("result=%+v err=%v download_calls=%d", result, err, store.downloadCalls)
	}
}

func TestDownloadAttachmentQualificationAndSizeFailuresPrecedeBinaryAndDirectory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		store   *boundedAttachmentDownloadStore
		options ConfluenceAttachmentDownloadOptions
		want    error
	}{
		{name: "qualification", store: &boundedAttachmentDownloadStore{evidence: downloadEvidence(0), revalidateErr: domain.ErrNotFound}, want: domain.ErrNotFound},
		{name: "missing size sentinel", store: &boundedAttachmentDownloadStore{evidence: domain.ConfluenceAttachmentDownloadEvidence{AttachmentID: "21", PageID: "10", Filename: "diagram.png", Version: 3, FileSize: -1}}, want: domain.ErrCheckFailed},
		{name: "attachment equals page", store: &boundedAttachmentDownloadStore{evidence: domain.ConfluenceAttachmentDownloadEvidence{AttachmentID: "10", PageID: "10", Filename: "diagram.png", Version: 3, FileSize: 0}}, want: domain.ErrCheckFailed},
		{name: "above selected max", store: &boundedAttachmentDownloadStore{evidence: downloadEvidence(9)}, options: ConfluenceAttachmentDownloadOptions{MaxBytes: 8}, want: domain.ErrOutputLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "not-created")
			result, err := (&ConfluenceService{store: tc.store}).DownloadAttachmentKnownPageWithOptions(t.Context(), "10", "diagram.png", 0, root, tc.options)
			if !errors.Is(err, tc.want) || result != nil || tc.store.downloadCalls != 0 {
				t.Fatalf("result=%+v err=%v download_calls=%d", result, err, tc.store.downloadCalls)
			}
			if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
				t.Fatalf("failure created output root: %v", statErr)
			}
		})
	}
}

func TestDownloadAttachmentRejectsInvalidFilenameBeforeReferenceOrDependencies(t *testing.T) {
	for _, filename := range []string{" \t ", string([]byte{0xff}), strings.Repeat("x", ConfluenceAttachmentDownloadMaxFilenameBytes+1)} {
		store := &boundedAttachmentDownloadStore{evidence: downloadEvidence(0)}
		root := filepath.Join(t.TempDir(), "not-created")
		result, err := (&ConfluenceService{store: store, baseURL: "https://example.test"}).DownloadAttachmentKnownPage(
			t.Context(), "/x/AwAG", filename, 0, root)
		if !errors.Is(err, domain.ErrUsage) || result != nil || store.revalidateCalls != 0 || store.downloadCalls != 0 {
			t.Fatalf("filename=%q result=%+v err=%v calls=%d/%d", filename, result, err, store.revalidateCalls, store.downloadCalls)
		}
		if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
			t.Fatalf("filename=%q created output root: %v", filename, statErr)
		}
	}
}

func TestDownloadAttachmentMetadataDeadlineHonorsEarlierCallerDeadline(t *testing.T) {
	store := &boundedAttachmentDownloadStore{evidence: downloadEvidence(0), waitForDeadline: true}
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
			store := &boundedAttachmentDownloadStore{checkBounds: true, evidence: downloadEvidence(0)}
			tc.set(store)
			_, err := (&ConfluenceService{store: store}).DownloadAttachmentKnownPage(t.Context(), "10", "diagram.png", 0, t.TempDir())
			if !errors.Is(err, tc.want) || store.downloadCalls != 0 {
				t.Fatalf("err=%v download_calls=%d", err, store.downloadCalls)
			}
		})
	}
}

type testAttachmentReadCloser struct {
	reader   io.Reader
	closeErr error
	closes   atomic.Int32
}

func (r *testAttachmentReadCloser) Read(p []byte) (int, error) { return r.reader.Read(p) }
func (r *testAttachmentReadCloser) Close() error {
	r.closes.Add(1)
	return r.closeErr
}

type exactEOFReader struct {
	data []byte
	done bool
}

type zeroThenExtraReader struct {
	step int
}

func (r *zeroThenExtraReader) Read(p []byte) (int, error) {
	r.step++
	switch r.step {
	case 1:
		return copy(p, "abc"), nil
	case 2:
		return 0, nil
	default:
		return copy(p, "x"), nil
	}
}

func (r *exactEOFReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	n := copy(p, r.data)
	return n, io.EOF
}

func TestDownloadAttachmentExactLengthNMinusOneNPlusOneAndZero(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared int64
		body     []byte
		exactEOF bool
		wantOK   bool
	}{
		{name: "N-1", declared: 3, body: []byte("ab")},
		{name: "N", declared: 3, body: []byte("abc"), wantOK: true},
		{name: "N with same-read EOF", declared: 3, body: []byte("abc"), exactEOF: true, wantOK: true},
		{name: "N+1", declared: 3, body: []byte("abcd")},
		{name: "zero", declared: 0, body: []byte{}, wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reader io.Reader = bytes.NewReader(tc.body)
			if tc.exactEOF {
				reader = &exactEOFReader{data: tc.body}
			}
			body := &testAttachmentReadCloser{reader: reader}
			store := &boundedAttachmentDownloadStore{evidence: downloadEvidence(tc.declared), downloadBody: body}
			root := t.TempDir()
			target := filepath.Join(root, "diagram.png")
			if err := os.WriteFile(target, []byte("sentinel"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := (&ConfluenceService{store: store}).DownloadAttachmentKnownPage(t.Context(), "10", "diagram.png", 0, root)
			got, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.wantOK {
				if err != nil || !bytes.Equal(got, tc.body) {
					t.Fatalf("err=%v body=%q", err, got)
				}
			} else if !errors.Is(err, domain.ErrCheckFailed) || string(got) != "sentinel" {
				t.Fatalf("err=%v preserved=%q", err, got)
			}
			if body.closes.Load() != 1 {
				t.Fatalf("close count=%d", body.closes.Load())
			}
			assertNoAttachmentTemps(t, root)
		})
	}
}

func TestDownloadAttachmentRejectsNoProgressEOFProbe(t *testing.T) {
	body := &testAttachmentReadCloser{reader: &zeroThenExtraReader{}}
	store := &boundedAttachmentDownloadStore{evidence: downloadEvidence(3), downloadBody: body}
	root := t.TempDir()
	target := filepath.Join(root, "diagram.png")
	if err := os.WriteFile(target, []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := (&ConfluenceService{store: store}).DownloadAttachmentKnownPage(t.Context(), "10", "diagram.png", 0, root)
	got, readErr := os.ReadFile(target)
	if !errors.Is(err, domain.ErrCheckFailed) || readErr != nil || string(got) != "sentinel" || body.closes.Load() != 1 {
		t.Fatalf("err=%v read=%v preserved=%q closes=%d", err, readErr, got, body.closes.Load())
	}
	assertNoAttachmentTemps(t, root)
}

func TestDownloadAttachmentCloseCancellationAndPreReadFailurePreserveDestination(t *testing.T) {
	for _, tc := range []struct {
		name       string
		makeBody   func(context.CancelFunc) *testAttachmentReadCloser
		makeRoot   func(*testing.T) string
		wantCancel bool
	}{
		{name: "close error", makeBody: func(context.CancelFunc) *testAttachmentReadCloser {
			return &testAttachmentReadCloser{reader: strings.NewReader("abc"), closeErr: errors.New("private close detail")}
		}, makeRoot: func(t *testing.T) string { return t.TempDir() }},
		{name: "read error", makeBody: func(context.CancelFunc) *testAttachmentReadCloser {
			return &testAttachmentReadCloser{reader: failingAttachmentReader{}}
		}, makeRoot: func(t *testing.T) string { return t.TempDir() }},
		{name: "caller cancellation", makeBody: func(cancel context.CancelFunc) *testAttachmentReadCloser {
			return &testAttachmentReadCloser{reader: &cancelAtEOFReader{reader: strings.NewReader("abc"), cancel: cancel}}
		}, makeRoot: func(t *testing.T) string { return t.TempDir() }, wantCancel: true},
		{name: "filesystem before read", makeBody: func(context.CancelFunc) *testAttachmentReadCloser {
			return &testAttachmentReadCloser{reader: strings.NewReader("abc")}
		}, makeRoot: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "not-a-directory")
			if err := os.WriteFile(path, []byte("sentinel"), 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			body := tc.makeBody(cancel)
			root := tc.makeRoot(t)
			if info, err := os.Stat(root); err == nil && info.IsDir() {
				if err := os.WriteFile(filepath.Join(root, "diagram.png"), []byte("sentinel"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			store := &boundedAttachmentDownloadStore{evidence: downloadEvidence(3), downloadBody: body}
			_, err := (&ConfluenceService{store: store}).DownloadAttachmentKnownPage(ctx, "10", "diagram.png", 0, root)
			if err == nil || tc.wantCancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v", err)
			}
			if body.closes.Load() != 1 {
				t.Fatalf("close count=%d", body.closes.Load())
			}
			if info, statErr := os.Stat(root); statErr == nil && info.IsDir() {
				got, readErr := os.ReadFile(filepath.Join(root, "diagram.png"))
				if readErr != nil || string(got) != "sentinel" {
					t.Fatalf("preserved=%q err=%v", got, readErr)
				}
				assertNoAttachmentTemps(t, root)
			}
		})
	}
}

type cancelAtEOFReader struct {
	reader io.Reader
	cancel context.CancelFunc
}

type failingAttachmentReader struct{}

func (failingAttachmentReader) Read([]byte) (int, error) {
	return 0, errors.New("private read detail")
}

func (r *cancelAtEOFReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if errors.Is(err, io.EOF) {
		r.cancel()
	}
	return n, err
}

func assertNoAttachmentTemps(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}

func TestNormalizeConfluenceAttachmentDownloadOptions(t *testing.T) {
	got, err := NormalizeConfluenceAttachmentDownloadOptions(ConfluenceAttachmentDownloadOptions{})
	if err != nil || got.MaxBytes != ConfluenceAttachmentDownloadDefaultMaxBytes {
		t.Fatalf("defaults=%+v err=%v", got, err)
	}
	for _, value := range []int64{-1, ConfluenceAttachmentDownloadMaxBytes + 1} {
		if _, err := NormalizeConfluenceAttachmentDownloadOptions(ConfluenceAttachmentDownloadOptions{MaxBytes: value}); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("max_bytes=%d err=%v", value, err)
		}
	}
}
