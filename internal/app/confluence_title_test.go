package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type titleStore struct {
	domain.DocStore
	reads         []*domain.Resource
	readErrs      []error
	getCalls      int
	updateCalls   int
	updatedTitle  string
	updatedBody   []byte
	updatedExpect int
	updateErr     error
}

type titleHTTPError struct{ status int }

func (e titleHTTPError) Error() string   { return "http rejected" }
func (e titleHTTPError) HTTPStatus() int { return e.status }

func (s *titleStore) GetPage(context.Context, string, domain.PullOpts) (*domain.Resource, error) {
	i := s.getCalls
	s.getCalls++
	if i < len(s.readErrs) && s.readErrs[i] != nil {
		return nil, s.readErrs[i]
	}
	if i >= len(s.reads) {
		return nil, errors.New("unexpected read")
	}
	return s.reads[i], nil
}

func (s *titleStore) UpdatePage(_ context.Context, _ string, expect int, title string, body []byte, _ bool) (int, error) {
	s.updateCalls++
	s.updatedExpect, s.updatedTitle, s.updatedBody = expect, title, append([]byte(nil), body...)
	if s.updateErr != nil {
		return 0, s.updateErr
	}
	return expect + 1, nil
}

func titlePage(title string, version int, body string) *domain.Resource {
	return &domain.Resource{ID: "42", Title: title, Version: version, Body: []byte(body), BodyPresent: true}
}

func TestConfluenceTitleMissingNativeBodyFailsBeforeWrite(t *testing.T) {
	store := &titleStore{reads: []*domain.Resource{{ID: "42", Title: "Old", Version: 7}}}
	_, err := (&ConfluenceService{store: store}).SetTitleGuarded(context.Background(), "42", ConfluenceTitleSetOpts{Title: []byte("New")})
	if !errors.Is(err, domain.ErrCheckFailed) || store.updateCalls != 0 {
		t.Fatalf("err=%v updates=%d", err, store.updateCalls)
	}
}

func TestConfluenceTitleDryRunAndApply(t *testing.T) {
	previewStore := &titleStore{reads: []*domain.Resource{titlePage("Old", 7, "<p>body</p>")}}
	preview, err := (&ConfluenceService{store: previewStore}).SetTitleGuarded(context.Background(), "42", ConfluenceTitleSetOpts{Title: []byte("  New title\n")})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Status != "would_apply" || preview.Title != "New title" || preview.TitleBytes != 9 || preview.ProposalHash == "" || preview.TitleSHA256 == "" || previewStore.updateCalls != 0 {
		t.Fatalf("preview=%+v updates=%d", preview, previewStore.updateCalls)
	}

	applyStore := &titleStore{reads: []*domain.Resource{
		titlePage("Old", 7, "<p>body</p>"), titlePage("New title", 8, "<p>body</p>"),
	}}
	result, err := (&ConfluenceService{store: applyStore}).SetTitleGuarded(context.Background(), "42", ConfluenceTitleSetOpts{
		Title: []byte("New title"), ExpectedVersion: 7, ExpectedProposalHash: preview.ProposalHash, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "applied" || result.FinalVersion != 8 || applyStore.updateCalls != 1 || applyStore.updatedExpect != 7 || applyStore.updatedTitle != "New title" || string(applyStore.updatedBody) != "<p>body</p>" {
		t.Fatalf("result=%+v store=%+v", result, applyStore)
	}
}

func TestConfluenceTitleApplyGatesAndIdempotency(t *testing.T) {
	t.Run("already satisfied", func(t *testing.T) {
		store := &titleStore{reads: []*domain.Resource{titlePage("New", 9, "x")}}
		res, err := (&ConfluenceService{store: store}).SetTitleGuarded(context.Background(), "42", ConfluenceTitleSetOpts{
			Title: []byte("New"), ExpectedVersion: 7, ExpectedProposalHash: "stale", Apply: true,
		})
		if err != nil || res.Status != "already_satisfied" || store.updateCalls != 0 {
			t.Fatalf("res=%+v err=%v updates=%d", res, err, store.updateCalls)
		}
	})
	t.Run("stale version", func(t *testing.T) {
		store := &titleStore{reads: []*domain.Resource{titlePage("Old", 8, "x")}}
		res, err := (&ConfluenceService{store: store}).SetTitleGuarded(context.Background(), "42", ConfluenceTitleSetOpts{
			Title: []byte("New"), ExpectedVersion: 7, ExpectedProposalHash: "x", Apply: true,
		})
		if !errors.Is(err, domain.ErrCheckFailed) || res.Status != "blocked" || store.updateCalls != 0 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
	t.Run("proposal changed", func(t *testing.T) {
		store := &titleStore{reads: []*domain.Resource{titlePage("Old", 7, "x")}}
		res, err := (&ConfluenceService{store: store}).SetTitleGuarded(context.Background(), "42", ConfluenceTitleSetOpts{
			Title: []byte("Changed"), ExpectedVersion: 7, ExpectedProposalHash: "reviewed-other", Apply: true,
		})
		if !errors.Is(err, domain.ErrCheckFailed) || res.Status != "blocked" || store.updateCalls != 0 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
}

func TestConfluenceTitleAmbiguousWriteIsReconciledWithoutReplay(t *testing.T) {
	previewHash, _ := confluenceTitleProposalHash("42", 7, "New")
	store := &titleStore{
		reads:     []*domain.Resource{titlePage("Old", 7, "body"), titlePage("New", 8, "body")},
		updateErr: errors.New("connection reset after write"),
	}
	res, err := (&ConfluenceService{store: store}).SetTitleGuarded(context.Background(), "42", ConfluenceTitleSetOpts{
		Title: []byte("New"), ExpectedVersion: 7, ExpectedProposalHash: previewHash, Apply: true,
	})
	if err != nil || res.Status != "applied" || !res.Reconciled || store.updateCalls != 1 || store.getCalls != 2 {
		t.Fatalf("res=%+v err=%v store=%+v", res, err, store)
	}
}

func TestConfluenceTitleUnknownOutcomeFailsClosed(t *testing.T) {
	previewHash, _ := confluenceTitleProposalHash("42", 7, "New")
	store := &titleStore{
		reads:     []*domain.Resource{titlePage("Old", 7, "body")},
		readErrs:  []error{nil, errors.New("verification unavailable")},
		updateErr: errors.New("connection reset after write"),
	}
	res, err := (&ConfluenceService{store: store}).SetTitleGuarded(context.Background(), "42", ConfluenceTitleSetOpts{
		Title: []byte("New"), ExpectedVersion: 7, ExpectedProposalHash: previewHash, Apply: true,
	})
	if err == nil || res.Status != "unknown" || store.updateCalls != 1 || store.getCalls != 2 {
		t.Fatalf("res=%+v err=%v store=%+v", res, err, store)
	}
}

func TestConfluenceTitleDefinitiveRejectionIsNotReconciledOrReplayed(t *testing.T) {
	previewHash, _ := confluenceTitleProposalHash("42", 7, "New")
	store := &titleStore{
		reads:     []*domain.Resource{titlePage("Old", 7, "body")},
		updateErr: titleHTTPError{status: 409},
	}
	res, err := (&ConfluenceService{store: store}).SetTitleGuarded(context.Background(), "42", ConfluenceTitleSetOpts{
		Title: []byte("New"), ExpectedVersion: 7, ExpectedProposalHash: previewHash, Apply: true,
	})
	if err == nil || res.Status != "failed" || store.updateCalls != 1 || store.getCalls != 1 {
		t.Fatalf("res=%+v err=%v store=%+v", res, err, store)
	}
}

func TestConfluenceTitleVerificationMismatchIsUnknown(t *testing.T) {
	previewHash, _ := confluenceTitleProposalHash("42", 7, "New")
	store := &titleStore{reads: []*domain.Resource{
		titlePage("Old", 7, "body"), titlePage("New", 8, "changed body"),
	}}
	res, err := (&ConfluenceService{store: store}).SetTitleGuarded(context.Background(), "42", ConfluenceTitleSetOpts{
		Title: []byte("New"), ExpectedVersion: 7, ExpectedProposalHash: previewHash, Apply: true,
	})
	if err == nil || res.Status != "unknown" || store.updateCalls != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestNormalizeConfluenceTitle(t *testing.T) {
	for _, input := range [][]byte{
		nil, []byte(" \n "), []byte("a\nb"), {'x', 0}, {0xff},
		[]byte("safe\u202Espoof"), []byte("safe\u200Fspoof"), []byte("safe\uFEFFspoof"),
	} {
		if _, err := normalizeConfluenceTitle(input); !errors.Is(err, domain.ErrUsage) {
			t.Errorf("input %q error=%v", input, err)
		}
	}
	if _, err := normalizeConfluenceTitle(make([]byte, ConfluenceTitleInputCap+1)); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("oversized error=%v", err)
	}
}
