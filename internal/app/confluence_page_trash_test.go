package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

const confluenceTrashTestBackend = "https://confluence.example.test/wiki"

type confluenceTrashRead struct {
	page *domain.Resource
	err  error
}

type confluenceTrashStore struct {
	domain.DocStore
	reads       map[string][]confluenceTrashRead
	readIndex   map[string]int
	deleteErr   error
	deleteCalls int
}

type confluenceTrashNoStatusStore struct {
	domain.DocStore
}

func (s *confluenceTrashStore) GetPageByStatus(_ context.Context, _ string, status string, _ domain.PullOpts) (*domain.Resource, error) {
	if s.readIndex == nil {
		s.readIndex = map[string]int{}
	}
	index := s.readIndex[status]
	queue := s.reads[status]
	if index >= len(queue) {
		return nil, errors.New("unexpected page-status read")
	}
	s.readIndex[status] = index + 1
	read := queue[index]
	if read.page == nil || read.err != nil {
		return read.page, read.err
	}
	page := *read.page
	page.Body = append([]byte(nil), read.page.Body...)
	page.Ancestors = append([]string(nil), read.page.Ancestors...)
	page.AncestorIDs = append([]string(nil), read.page.AncestorIDs...)
	return &page, nil
}

func (s *confluenceTrashStore) DeletePage(context.Context, string) error {
	s.deleteCalls++
	return s.deleteErr
}

type confluenceTrashHTTPError struct {
	status   int
	sentinel error
}

func (e confluenceTrashHTTPError) Error() string   { return "request failed" }
func (e confluenceTrashHTTPError) HTTPStatus() int { return e.status }
func (e confluenceTrashHTTPError) Unwrap() error   { return e.sentinel }

func confluenceTrashPage(status string, version int, body string) *domain.Resource {
	return &domain.Resource{
		ID: "42", Type: "page", Status: status, Title: "Reviewed title", SpaceKey: "DOC",
		Version: version, Body: []byte(body), BodyPresent: true,
		Parent: "10", Ancestors: []string{"Home"}, AncestorIDs: []string{"10"}, AncestorsPresent: true,
	}
}

func previewConfluenceTrash(t *testing.T, page *domain.Resource) *ConfluencePageTrashResult {
	t.Helper()
	store := &confluenceTrashStore{reads: map[string][]confluenceTrashRead{
		"current": {{page: page}},
	}}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceTrashTestBackend}).TrashPageGuarded(context.Background(), "42", ConfluencePageTrashOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Status != "would_apply" || result.ProposalHash == "" || store.deleteCalls != 0 {
		t.Fatalf("preview = %+v, err=%v, deletes=%d", result, err, store.deleteCalls)
	}
	return result
}

func TestConfluencePageTrashPreviewAndProposalBinding(t *testing.T) {
	page := confluenceTrashPage("current", 7, "<p>body</p>")
	preview := previewConfluenceTrash(t, page)
	if preview.Mode != "dry-run" || preview.CurrentVersion != 7 || preview.ExpectedVersion != 7 ||
		preview.CurrentStatus != "current" || preview.TargetStatus != "trashed" || !preview.Complete || preview.WriteAttempted {
		t.Fatalf("preview = %+v", preview)
	}
	base := confluencePageTrashSnapshot{
		id: "42", contentType: "page", status: "current", version: 7,
		bodySHA256: "body", bodyBytes: 4, titleSHA256: "title", space: "DOC", parent: "10", backendSHA256: "backend",
	}
	for name, mutate := range map[string]func(*confluencePageTrashSnapshot){
		"backend": func(v *confluencePageTrashSnapshot) { v.backendSHA256 = "other" },
		"id":      func(v *confluencePageTrashSnapshot) { v.id = "43" },
		"type":    func(v *confluencePageTrashSnapshot) { v.contentType = "blogpost" },
		"status":  func(v *confluencePageTrashSnapshot) { v.status = "trashed" },
		"version": func(v *confluencePageTrashSnapshot) { v.version++ },
		"body":    func(v *confluencePageTrashSnapshot) { v.bodySHA256 = "other" },
		"bytes":   func(v *confluencePageTrashSnapshot) { v.bodyBytes++ },
		"title":   func(v *confluencePageTrashSnapshot) { v.titleSHA256 = "other" },
		"space":   func(v *confluencePageTrashSnapshot) { v.space = "OTHER" },
		"parent":  func(v *confluencePageTrashSnapshot) { v.parent = "11" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if confluencePageTrashProposalHash(changed) == confluencePageTrashProposalHash(base) {
				t.Fatalf("proposal hash did not bind %s", name)
			}
		})
	}
}

func TestConfluencePageTrashApplySuccessAndRecovery(t *testing.T) {
	current := confluenceTrashPage("current", 7, "<p>body</p>")
	trashed := confluenceTrashPage("trashed", 7, "<p>body</p>")
	preview := previewConfluenceTrash(t, current)
	for name, writeErr := range map[string]error{
		"success":             nil,
		"recovered":           errors.New("connection closed"),
		"not-found recovered": confluenceTrashHTTPError{status: 404, sentinel: domain.ErrNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			store := &confluenceTrashStore{
				reads: map[string][]confluenceTrashRead{
					"current": {{page: current}, {page: current}, {err: domain.ErrNotFound}},
					"trashed": {{page: trashed}},
				},
				deleteErr: writeErr,
			}
			result, err := (&ConfluenceService{store: store, baseURL: confluenceTrashTestBackend}).TrashPageGuarded(context.Background(), "42", ConfluencePageTrashOpts{
				Apply: true, Confirm: "TRASH", ExpectedVersion: 7, ExpectedProposalHash: preview.ProposalHash,
			})
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := "applied"
			if writeErr != nil {
				wantStatus = "recovered"
			}
			if result.Status != wantStatus || !result.WriteAttempted || !result.Reconciled || result.ObservedState != "trashed" || store.deleteCalls != 1 {
				t.Fatalf("result=%+v deletes=%d", result, store.deleteCalls)
			}
		})
	}
}

func TestConfluencePageTrashBlocksDriftBeforeDelete(t *testing.T) {
	current := confluenceTrashPage("current", 7, "<p>body</p>")
	changed := confluenceTrashPage("current", 8, "<p>changed</p>")
	preview := previewConfluenceTrash(t, current)
	store := &confluenceTrashStore{reads: map[string][]confluenceTrashRead{
		"current": {{page: current}, {page: changed}},
	}}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceTrashTestBackend}).TrashPageGuarded(context.Background(), "42", ConfluencePageTrashOpts{
		Apply: true, Confirm: "TRASH", ExpectedVersion: 7, ExpectedProposalHash: preview.ProposalHash,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.Status != "blocked" || result.WriteAttempted || store.deleteCalls != 0 {
		t.Fatalf("result=%+v err=%v deletes=%d", result, err, store.deleteCalls)
	}
}

func TestConfluencePageTrashApplyGatesPrecedeReads(t *testing.T) {
	for name, opts := range map[string]ConfluencePageTrashOpts{
		"confirmation": {Apply: true, Confirm: "wrong", ExpectedVersion: 7, ExpectedProposalHash: "hash"},
		"version":      {Apply: true, Confirm: "TRASH", ExpectedProposalHash: "hash"},
		"hash":         {Apply: true, Confirm: "TRASH", ExpectedVersion: 7},
	} {
		t.Run(name, func(t *testing.T) {
			store := &confluenceTrashStore{}
			result, err := (&ConfluenceService{store: store, baseURL: confluenceTrashTestBackend}).TrashPageGuarded(context.Background(), "42", opts)
			if result != nil || !errors.Is(err, domain.ErrUsage) || len(store.readIndex) != 0 || store.deleteCalls != 0 {
				t.Fatalf("result=%+v err=%v reads=%v deletes=%d", result, err, store.readIndex, store.deleteCalls)
			}
		})
	}
}

func TestConfluencePageTrashRequiresExactStatusReader(t *testing.T) {
	store := &confluenceTrashNoStatusStore{}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceTrashTestBackend}).TrashPageGuarded(context.Background(), "42", ConfluencePageTrashOpts{})
	if result != nil || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestValidateConfluencePageTrashReadFailsClosed(t *testing.T) {
	valid := confluenceTrashPage("current", 7, "<p>body</p>")
	tests := map[string]func(*domain.Resource) *domain.Resource{
		"nil":               func(*domain.Resource) *domain.Resource { return nil },
		"id":                func(v *domain.Resource) *domain.Resource { v.ID = "43"; return v },
		"non-numeric id":    func(v *domain.Resource) *domain.Resource { v.ID = "page"; return v },
		"type":              func(v *domain.Resource) *domain.Resource { v.Type = "blogpost"; return v },
		"status":            func(v *domain.Resource) *domain.Resource { v.Status = "trashed"; return v },
		"version":           func(v *domain.Resource) *domain.Resource { v.Version = 0; return v },
		"body projection":   func(v *domain.Resource) *domain.Resource { v.BodyPresent = false; return v },
		"title":             func(v *domain.Resource) *domain.Resource { v.Title = " "; return v },
		"space":             func(v *domain.Resource) *domain.Resource { v.SpaceKey = ""; return v },
		"ancestors omitted": func(v *domain.Resource) *domain.Resource { v.AncestorsPresent = false; return v },
		"ancestor mismatch": func(v *domain.Resource) *domain.Resource { v.AncestorIDs = nil; return v },
		"empty ancestor id": func(v *domain.Resource) *domain.Resource { v.AncestorIDs[0] = ""; return v },
		"invalid ancestor":  func(v *domain.Resource) *domain.Resource { v.AncestorIDs[0] = "home"; return v },
		"parent mismatch":   func(v *domain.Resource) *domain.Resource { v.Parent = "11"; return v },
		"top level parent": func(v *domain.Resource) *domain.Resource {
			v.Ancestors = nil
			v.AncestorIDs = nil
			v.Parent = "10"
			return v
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			page := *valid
			page.Ancestors = append([]string(nil), valid.Ancestors...)
			page.AncestorIDs = append([]string(nil), valid.AncestorIDs...)
			if err := validateExactConfluencePageRead(mutate(&page), "42", "current"); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error = %v, want ErrCheckFailed", err)
			}
		})
	}
}

func TestConfluencePageTrashPrewriteFailureIsContentFree(t *testing.T) {
	current := confluenceTrashPage("current", 7, "<p>body</p>")
	preview := previewConfluenceTrash(t, current)
	store := &confluenceTrashStore{reads: map[string][]confluenceTrashRead{
		"current": {{page: current}, {err: errors.New("private server detail")}},
	}}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceTrashTestBackend}).TrashPageGuarded(context.Background(), "42", ConfluencePageTrashOpts{
		Apply: true, Confirm: "TRASH", ExpectedVersion: 7, ExpectedProposalHash: preview.ProposalHash,
	})
	if result == nil || result.Status != "blocked" || result.WriteAttempted || !errors.Is(err, domain.ErrCheckFailed) || strings.Contains(err.Error(), "private server detail") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestConfluencePageTrashOutcomeMatrices(t *testing.T) {
	current := confluenceTrashPage("current", 7, "<p>body</p>")
	preview := previewConfluenceTrash(t, current)
	tests := []struct {
		name      string
		writeErr  error
		readback  map[string][]confluenceTrashRead
		wantState string
		complete  bool
	}{
		{
			name: "current remains after success", readback: map[string][]confluenceTrashRead{"current": {{page: current}}},
			wantState: "current", complete: true,
		},
		{
			name: "current remains after ambiguous error", writeErr: errors.New("timeout"),
			readback: map[string][]confluenceTrashRead{"current": {{page: current}}}, wantState: "current", complete: true,
		},
		{
			name: "both states absent", writeErr: errors.New("timeout"),
			readback: map[string][]confluenceTrashRead{
				"current": {{err: domain.ErrNotFound}}, "trashed": {{err: domain.ErrNotFound}},
			}, wantState: "not_found", complete: false,
		},
		{
			name: "higher version trashed projection",
			readback: map[string][]confluenceTrashRead{
				"current": {{err: domain.ErrNotFound}},
				"trashed": {{page: confluenceTrashPage("trashed", 8, "<p>body</p>")}},
			}, wantState: "trashed", complete: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reads := map[string][]confluenceTrashRead{
				"current": {{page: current}, {page: current}},
			}
			for status, queue := range test.readback {
				reads[status] = append(reads[status], queue...)
			}
			store := &confluenceTrashStore{reads: reads, deleteErr: test.writeErr}
			result, err := (&ConfluenceService{store: store, baseURL: confluenceTrashTestBackend}).TrashPageGuarded(context.Background(), "42", ConfluencePageTrashOpts{
				Apply: true, Confirm: "TRASH", ExpectedVersion: 7, ExpectedProposalHash: preview.ProposalHash,
			})
			var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
			if result == nil || result.Status != "outcome_unknown" || result.ObservedState != test.wantState || result.Complete != test.complete ||
				!errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() || store.deleteCalls != 1 {
				t.Fatalf("result=%+v err=%v deletes=%d", result, err, store.deleteCalls)
			}
		})
	}

	t.Run("definitive rejection", func(t *testing.T) {
		store := &confluenceTrashStore{
			reads:     map[string][]confluenceTrashRead{"current": {{page: current}, {page: current}}},
			deleteErr: confluenceTrashHTTPError{status: 403, sentinel: domain.ErrForbidden},
		}
		result, err := (&ConfluenceService{store: store, baseURL: confluenceTrashTestBackend}).TrashPageGuarded(context.Background(), "42", ConfluencePageTrashOpts{
			Apply: true, Confirm: "TRASH", ExpectedVersion: 7, ExpectedProposalHash: preview.ProposalHash,
		})
		if result == nil || result.Status != "not_applied" || !errors.Is(err, domain.ErrForbidden) || store.deleteCalls != 1 {
			t.Fatalf("result=%+v err=%v deletes=%d", result, err, store.deleteCalls)
		}
	})
}

func TestConfluencePageTrashAlreadySatisfied(t *testing.T) {
	trashed := confluenceTrashPage("trashed", 7, "<p>body</p>")
	store := &confluenceTrashStore{reads: map[string][]confluenceTrashRead{
		"current": {{err: domain.ErrNotFound}}, "trashed": {{page: trashed}},
	}}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceTrashTestBackend}).TrashPageGuarded(context.Background(), "42", ConfluencePageTrashOpts{})
	if err != nil || result == nil || result.Status != "already_satisfied" || result.CurrentStatus != "trashed" || result.WriteAttempted || store.deleteCalls != 0 {
		t.Fatalf("result=%+v err=%v deletes=%d", result, err, store.deleteCalls)
	}
}
