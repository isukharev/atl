package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

type attachmentDiscoveryStore struct {
	domain.DocStore
	page     domain.ConfluenceAttachmentDiscoveryPage
	err      error
	requests []domain.ConfluenceAttachmentDiscoveryRequest
	checkCtx bool
}

func (s *attachmentDiscoveryStore) DiscoverAttachmentsQualified(ctx context.Context, request domain.ConfluenceAttachmentDiscoveryRequest) (domain.ConfluenceAttachmentDiscoveryPage, error) {
	s.requests = append(s.requests, request)
	if s.checkCtx {
		budget := domain.ReadBudgetFromContext(ctx)
		if budget == nil || !domain.SingleAttempt(ctx) {
			return domain.ConfluenceAttachmentDiscoveryPage{}, errors.New("missing bounded single-attempt context")
		}
		if err := budget.TakeAttempt(); err != nil {
			return domain.ConfluenceAttachmentDiscoveryPage{}, err
		}
		_, finish, err := budget.BeginResponse(ctx)
		if err != nil {
			return domain.ConfluenceAttachmentDiscoveryPage{}, err
		}
		finish(7)
	}
	return s.page, s.err
}

func attachmentDiscoveryOpts() ConfluenceAttachmentDiscoveryOpts {
	return ConfluenceAttachmentDiscoveryOpts{
		Space: "DOC", MaxItems: 2, MaxRequests: 3, MaxResponseBytes: 1024, Deadline: time.Second,
	}
}

func discoveredAttachment() domain.ConfluenceAttachmentMetadata {
	return domain.ConfluenceAttachmentMetadata{
		ID: "21", Title: "diagram.png", Type: "attachment", Version: 3,
		ContainerID: "10", ContainerType: "page", ContainerVersion: 7,
		Space: "DOC", MediaType: "image/png", FileSize: 42,
	}
}

func TestDiscoverAttachmentsInstallsMandatoryPhysicalBounds(t *testing.T) {
	total := 1
	store := &attachmentDiscoveryStore{checkCtx: true, page: domain.ConfluenceAttachmentDiscoveryPage{
		Attachments: []domain.ConfluenceAttachmentMetadata{discoveredAttachment()}, Start: 0, TotalSize: &total,
		Complete: true, Consistency: domain.ConfluenceAttachmentDiscoveryConsistencyLiveUnproven,
	}}
	result, err := (&ConfluenceService{store: store}).DiscoverAttachments(t.Context(), attachmentDiscoveryOpts())
	if err != nil {
		t.Fatalf("DiscoverAttachments: %v", err)
	}
	if result.Qualification != ConfluenceAttachmentDiscoveryComplete || !result.Complete || result.Count != 1 || result.NextCursor != "" ||
		result.Bounds.RequestsUsed != 1 || result.Bounds.ResponseBytesUsed != 7 {
		t.Fatalf("result=%+v", result)
	}
}

func TestDiscoverAttachmentsCursorIsQueryAndScopeBound(t *testing.T) {
	total := 2
	next := 1
	store := &attachmentDiscoveryStore{page: domain.ConfluenceAttachmentDiscoveryPage{
		Attachments: []domain.ConfluenceAttachmentMetadata{discoveredAttachment()}, Start: 0, NextStart: &next, TotalSize: &total,
		PartialReason: domain.ConfluenceAttachmentDiscoveryPartialItemLimit,
		Consistency:   domain.ConfluenceAttachmentDiscoveryConsistencyLiveUnproven,
	}}
	opts := attachmentDiscoveryOpts()
	opts.MaxItems = 1
	first, err := (&ConfluenceService{store: store}).DiscoverAttachments(t.Context(), opts)
	if err != nil || first.NextCursor == "" || first.Qualification != ConfluenceAttachmentDiscoveryPartial {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	store.page = domain.ConfluenceAttachmentDiscoveryPage{
		Attachments: []domain.ConfluenceAttachmentMetadata{}, Start: 1, NextStart: &next,
		PartialReason: domain.ConfluenceAttachmentDiscoveryPartialPaginationStalled,
		Consistency:   domain.ConfluenceAttachmentDiscoveryConsistencyLiveUnproven,
	}
	opts.Cursor = first.NextCursor
	if _, err := (&ConfluenceService{store: store}).DiscoverAttachments(t.Context(), opts); err != nil {
		t.Fatalf("continue: %v", err)
	}
	if len(store.requests) != 2 || store.requests[1].Start != 1 {
		t.Fatalf("requests=%+v", store.requests)
	}
	opts.CQL = "creator = currentUser()"
	if _, err := (&ConfluenceService{store: store}).DiscoverAttachments(t.Context(), opts); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("scope mismatch err=%v", err)
	}
	if len(store.requests) != 2 {
		t.Fatal("scope-mismatched cursor reached the backend")
	}
	opts.CQL = ""
	if _, err := (&ConfluenceService{baseURL: "https://other.example.test", store: store}).DiscoverAttachments(t.Context(), opts); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("backend mismatch err=%v", err)
	}
	if len(store.requests) != 2 {
		t.Fatal("backend-mismatched cursor reached the backend")
	}
}

func TestDiscoverAttachmentsOrdinaryReadFailureReturnsQualifiedSnapshot(t *testing.T) {
	synthetic := errors.New("synthetic read failure")
	store := &attachmentDiscoveryStore{page: domain.ConfluenceAttachmentDiscoveryPage{
		Attachments: []domain.ConfluenceAttachmentMetadata{}, Start: 0,
		Consistency: domain.ConfluenceAttachmentDiscoveryConsistencyLiveUnproven,
	}, err: synthetic}
	result, err := (&ConfluenceService{store: store}).DiscoverAttachments(t.Context(), attachmentDiscoveryOpts())
	if !errors.Is(err, synthetic) {
		t.Fatalf("err=%v", err)
	}
	if result == nil || result.Qualification != ConfluenceAttachmentDiscoveryFailed || result.Complete ||
		result.Reason != ConfluenceAttachmentDiscoveryReadFailed || result.NextCursor != "" || result.Count != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestDiscoverAttachmentsInvalidPortPageReturnsContentFreeFailedSnapshot(t *testing.T) {
	store := &attachmentDiscoveryStore{page: domain.ConfluenceAttachmentDiscoveryPage{
		Attachments: []domain.ConfluenceAttachmentMetadata{{ID: "not-canonical", Title: "private title"}},
		Start:       7, Complete: true, Consistency: "claimed",
	}}
	result, err := (&ConfluenceService{store: store}).DiscoverAttachments(t.Context(), attachmentDiscoveryOpts())
	if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.Qualification != ConfluenceAttachmentDiscoveryFailed ||
		result.Reason != ConfluenceAttachmentDiscoveryValidationFailed || result.NextCursor != "" || result.Count != 0 || len(result.Attachments) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestNormalizeConfluenceAttachmentDiscoveryRequiresEveryBound(t *testing.T) {
	base := attachmentDiscoveryOpts()
	for name, mutate := range map[string]func(*ConfluenceAttachmentDiscoveryOpts){
		"items":       func(o *ConfluenceAttachmentDiscoveryOpts) { o.MaxItems = 0 },
		"requests":    func(o *ConfluenceAttachmentDiscoveryOpts) { o.MaxRequests = 0 },
		"bytes":       func(o *ConfluenceAttachmentDiscoveryOpts) { o.MaxResponseBytes = 0 },
		"deadline":    func(o *ConfluenceAttachmentDiscoveryOpts) { o.Deadline = 0 },
		"ordered cql": func(o *ConfluenceAttachmentDiscoveryOpts) { o.CQL = "type=attachment ORDER BY created" },
	} {
		t.Run(name, func(t *testing.T) {
			opts := base
			mutate(&opts)
			if _, err := NormalizeConfluenceAttachmentDiscoveryOpts(opts); !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestValidateConfluenceAttachmentDiscoveryResultFailsClosed(t *testing.T) {
	opts := attachmentDiscoveryOpts()
	hash, err := confluenceAttachmentDiscoveryScopeHash("", opts.Space, opts.CQL)
	if err != nil {
		t.Fatal(err)
	}
	total := 1
	result := &ConfluenceAttachmentDiscoveryResult{
		SchemaVersion: ConfluenceAttachmentDiscoverySchemaVersion,
		Qualification: ConfluenceAttachmentDiscoveryComplete, Complete: true,
		Consistency: domain.ConfluenceAttachmentDiscoveryConsistencyLiveUnproven,
		ScopeSHA256: hash, Count: 1, TotalSize: &total,
		Bounds: ConfluenceAttachmentDiscoveryBounds{
			MaxItems: 1, MaxRequests: 1, MaxResponseBytes: 1, DeadlineMillis: 1,
		},
		Attachments: []domain.ConfluenceAttachmentMetadata{discoveredAttachment()},
	}
	for name, mutate := range map[string]func(*ConfluenceAttachmentDiscoveryResult){
		"nil attachments": func(r *ConfluenceAttachmentDiscoveryResult) { r.Attachments = nil },
		"open reason": func(r *ConfluenceAttachmentDiscoveryResult) {
			r.Qualification, r.Complete, r.Reason = ConfluenceAttachmentDiscoveryPartial, false, "backend text"
		},
		"failed cursor": func(r *ConfluenceAttachmentDiscoveryResult) {
			r.Qualification, r.Complete, r.Reason, r.NextCursor = ConfluenceAttachmentDiscoveryFailed, false, ConfluenceAttachmentDiscoveryReadFailed, "not-resumable"
		},
		"negative size":               func(r *ConfluenceAttachmentDiscoveryResult) { r.Attachments[0].FileSize = -1 },
		"attachment equals container": func(r *ConfluenceAttachmentDiscoveryResult) { r.Attachments[0].ID = r.Attachments[0].ContainerID },
		"complete missing total":      func(r *ConfluenceAttachmentDiscoveryResult) { r.TotalSize = nil },
		"failed retained content": func(r *ConfluenceAttachmentDiscoveryResult) {
			r.Qualification, r.Complete, r.Reason = ConfluenceAttachmentDiscoveryFailed, false, ConfluenceAttachmentDiscoveryReadFailed
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := *result
			candidate.Attachments = append([]domain.ConfluenceAttachmentMetadata(nil), result.Attachments...)
			mutate(&candidate)
			if err := ValidateConfluenceAttachmentDiscoveryResult(&candidate); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
