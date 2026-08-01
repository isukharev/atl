package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type confluenceFooterCommentStoreStub struct {
	domain.DocStore
	user          domain.ConfluenceUserIdentity
	userFn        func(int) (domain.ConfluenceUserIdentity, error)
	pageVersion   int
	metaFn        func(int) (*domain.PageMeta, error)
	comments      []domain.ConfluenceCommentRecord
	listFn        func(int) (domain.ConfluenceCommentInventory, error)
	addResult     *domain.Comment
	addErr        error
	commit        []domain.ConfluenceCommentRecord
	userCalls     int
	metaCalls     int
	listCalls     int
	addCalls      int
	singleAttempt bool
	postedBody    []byte
}

func (s *confluenceFooterCommentStoreStub) CurrentConfluenceUser(context.Context) (domain.ConfluenceUserIdentity, error) {
	s.userCalls++
	if s.userFn != nil {
		return s.userFn(s.userCalls)
	}
	return s.user, nil
}

func (s *confluenceFooterCommentStoreStub) GetMeta(_ context.Context, id string) (*domain.PageMeta, error) {
	s.metaCalls++
	if s.metaFn != nil {
		return s.metaFn(s.metaCalls)
	}
	return &domain.PageMeta{ID: id, Type: "page", Version: s.pageVersion}, nil
}

func (s *confluenceFooterCommentStoreStub) ListConfluenceComments(_ context.Context, _ string, opts domain.ConfluenceCommentReadOptions) (domain.ConfluenceCommentInventory, error) {
	s.listCalls++
	if opts.ParentVersion != s.pageVersion || opts.DepthAll || len(opts.Locations) != 1 || opts.Locations[0] != domain.ConfluenceCommentSelectorFooter {
		return domain.ConfluenceCommentInventory{}, errors.New("unexpected read options")
	}
	if s.listFn != nil {
		return s.listFn(s.listCalls)
	}
	return completeQualifiedComments(append([]domain.ConfluenceCommentRecord(nil), s.comments...)...), nil
}

func (s *confluenceFooterCommentStoreStub) AddComment(ctx context.Context, _ string, body []byte) (*domain.Comment, error) {
	s.addCalls++
	s.singleAttempt = domain.SingleAttempt(ctx)
	s.postedBody = append([]byte(nil), body...)
	s.comments = append(s.comments, s.commit...)
	return s.addResult, s.addErr
}

func confluenceFooterCommentRecord(id, actorID, body string) domain.ConfluenceCommentRecord {
	rootID := id
	return domain.ConfluenceCommentRecord{
		ID: id, PageID: "42", RootID: &rootID,
		Relation: domain.ConfluenceCommentRelationRoot, Location: domain.ConfluenceCommentLocationFooter,
		Resolution: domain.ConfluenceCommentResolutionUnknown, Version: 1,
		AuthorID: actorID, AuthorDisplayName: strings.ToUpper(actorID),
		CreatedAt: "2026-08-01T00:00:00.000Z", UpdatedAt: "2026-08-01T00:00:00.000Z",
		Body: body, BodyStorage: body,
	}
}

func confluenceFooterCommentFixture() *confluenceFooterCommentStoreStub {
	return &confluenceFooterCommentStoreStub{
		user:        domain.ConfluenceUserIdentity{ID: "user-1", DisplayName: "Example User"},
		pageVersion: 7,
		comments:    []domain.ConfluenceCommentRecord{confluenceFooterCommentRecord("10", "user-2", "<p>existing</p>")},
	}
}

func previewConfluenceFooterComment(t *testing.T, store *confluenceFooterCommentStoreStub, body string) *ConfluenceFooterCommentAddResult {
	t.Helper()
	result, err := (&ConfluenceService{store: store, baseURL: "https://confluence.example.test"}).AddFooterCommentGuarded(
		context.Background(), "42", ConfluenceFooterCommentAddOpts{Body: []byte(body)},
	)
	if err != nil || result == nil || result.Status != "would_apply" || result.ProposalHash == "" {
		t.Fatalf("preview=%+v err=%v", result, err)
	}
	return result
}

func applyConfluenceFooterComment(store *confluenceFooterCommentStoreStub, baseURL, body, proposalHash string) (*ConfluenceFooterCommentAddResult, error) {
	return (&ConfluenceService{store: store, baseURL: baseURL}).AddFooterCommentGuarded(
		context.Background(), "42", ConfluenceFooterCommentAddOpts{
			Body: []byte(body), Apply: true, ExpectedProposalHash: proposalHash,
		},
	)
}

func TestConfluenceFooterCommentPreviewBindsTargetActorCapabilityAndSortedBaseline(t *testing.T) {
	store := confluenceFooterCommentFixture()
	store.comments = append(store.comments, confluenceFooterCommentRecord("2", "user-3", "<p>other</p>"))
	preview := previewConfluenceFooterComment(t, store, "<p> reviewed </p>")
	if preview.PageID != "42" || preview.PageVersion != 7 || preview.BodyBytes != len("<p> reviewed </p>") ||
		preview.Actor.ID != "user-1" || preview.Capability.Provider != "public_rest" || preview.Capability.Depth != "root" ||
		preview.CurrentCount != 2 || preview.BaselineSHA256 == "" || preview.BackendSHA256 == "" || !preview.Complete || store.addCalls != 0 {
		t.Fatalf("preview=%+v add=%d", preview, store.addCalls)
	}
	store.comments[0], store.comments[1] = store.comments[1], store.comments[0]
	reordered := previewConfluenceFooterComment(t, store, "<p> reviewed </p>")
	if reordered.ProposalHash != preview.ProposalHash || reordered.BaselineSHA256 != preview.BaselineSHA256 {
		t.Fatalf("reordered=%+v preview=%+v", reordered, preview)
	}
	if strings.Contains(ConfluenceFooterCommentAddText(preview), "reviewed") || strings.Contains(ConfluenceFooterCommentAddText(preview), preview.Actor.DisplayName) {
		t.Fatalf("text leaked body or display name: %q", ConfluenceFooterCommentAddText(preview))
	}
}

func TestConfluenceFooterCommentApplyUsesOnePOSTAndExactReadback(t *testing.T) {
	store := confluenceFooterCommentFixture()
	preview := previewConfluenceFooterComment(t, store, "<p>reviewed</p>")
	store.commit = []domain.ConfluenceCommentRecord{
		confluenceFooterCommentRecord("20", "user-1", "<p>reviewed</p>"),
		confluenceFooterCommentRecord("21", "user-3", "<p>concurrent distinct</p>"),
	}
	store.addResult = &domain.Comment{ID: "20"}
	result, err := (&ConfluenceService{store: store, baseURL: "https://confluence.example.test"}).AddFooterCommentGuarded(
		context.Background(), "42", ConfluenceFooterCommentAddOpts{
			Body: []byte("<p>reviewed</p>"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
		},
	)
	if err != nil || result.Status != "applied" || !result.Reconciled || result.Created == nil || result.Created.ID != "20" ||
		store.addCalls != 1 || store.listCalls != 4 || store.metaCalls != 4 || store.userCalls != 3 ||
		!store.singleAttempt || string(store.postedBody) != "<p>reviewed</p>" {
		t.Fatalf("result=%+v err=%v calls=user:%d meta:%d list:%d add:%d single=%t body=%q",
			result, err, store.userCalls, store.metaCalls, store.listCalls, store.addCalls, store.singleAttempt, store.postedBody)
	}
}

type confluenceFooterCommentStatusError int

func (e confluenceFooterCommentStatusError) Error() string   { return "sensitive backend detail" }
func (e confluenceFooterCommentStatusError) HTTPStatus() int { return int(e) }

func TestConfluenceFooterCommentOutcomesNeverReplay(t *testing.T) {
	tests := []struct {
		name          string
		writeErr      error
		commits       []domain.ConfluenceCommentRecord
		addResult     *domain.Comment
		wantStatus    string
		wantErr       bool
		wantAmbiguous bool
		wantLists     int
	}{
		{name: "definitive rejection", writeErr: confluenceFooterCommentStatusError(400), wantStatus: "not_applied", wantErr: true, wantLists: 3},
		{name: "timeout recovered", writeErr: context.DeadlineExceeded, commits: []domain.ConfluenceCommentRecord{confluenceFooterCommentRecord("20", "user-1", "<p>x</p>")}, wantStatus: "recovered", wantLists: 4},
		{name: "success returned id", commits: []domain.ConfluenceCommentRecord{confluenceFooterCommentRecord("20", "user-1", "<p>x</p>")}, addResult: &domain.Comment{ID: "20"}, wantStatus: "applied", wantLists: 4},
		{name: "server error no candidate", writeErr: confluenceFooterCommentStatusError(500), wantStatus: "outcome_unknown", wantErr: true, wantAmbiguous: true, wantLists: 4},
		{name: "duplicate candidates", writeErr: context.DeadlineExceeded, commits: []domain.ConfluenceCommentRecord{confluenceFooterCommentRecord("20", "user-1", "<p>x</p>"), confluenceFooterCommentRecord("21", "user-1", "<p>x</p>")}, wantStatus: "outcome_unknown", wantErr: true, wantAmbiguous: true, wantLists: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := confluenceFooterCommentFixture()
			preview := previewConfluenceFooterComment(t, store, "<p>x</p>")
			store.addErr, store.addResult, store.commit = test.writeErr, test.addResult, test.commits
			result, err := (&ConfluenceService{store: store, baseURL: "https://confluence.example.test"}).AddFooterCommentGuarded(
				context.Background(), "42", ConfluenceFooterCommentAddOpts{Body: []byte("<p>x</p>"), Apply: true, ExpectedProposalHash: preview.ProposalHash},
			)
			if result == nil || result.Status != test.wantStatus || (err != nil) != test.wantErr || store.addCalls != 1 || store.listCalls != test.wantLists || !store.singleAttempt {
				t.Fatalf("result=%+v err=%v add=%d lists=%d single=%t", result, err, store.addCalls, store.listCalls, store.singleAttempt)
			}
			var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
			gotAmbiguous := errors.As(err, &ambiguous) && ambiguous.DiagnosticAmbiguousWrite()
			if gotAmbiguous != test.wantAmbiguous {
				t.Fatalf("ambiguous=%t want=%t err=%v", gotAmbiguous, test.wantAmbiguous, err)
			}
			if err != nil && strings.Contains(err.Error(), "sensitive backend detail") {
				t.Fatalf("backend detail leaked: %v", err)
			}
		})
	}
}

func TestConfluenceFooterCommentRejectsDriftAndPartialEvidenceBeforePOST(t *testing.T) {
	t.Run("stale body hash", func(t *testing.T) {
		store := confluenceFooterCommentFixture()
		preview := previewConfluenceFooterComment(t, store, "<p>one</p>")
		result, err := (&ConfluenceService{store: store, baseURL: "https://confluence.example.test"}).AddFooterCommentGuarded(
			context.Background(), "42", ConfluenceFooterCommentAddOpts{Body: []byte("<p>two</p>"), Apply: true, ExpectedProposalHash: preview.ProposalHash},
		)
		if result == nil || result.Status != "conflict" || !errors.Is(err, domain.ErrCheckFailed) || store.addCalls != 0 {
			t.Fatalf("result=%+v err=%v add=%d", result, err, store.addCalls)
		}
	})

	t.Run("prewrite baseline drift", func(t *testing.T) {
		store := confluenceFooterCommentFixture()
		preview := previewConfluenceFooterComment(t, store, "<p>x</p>")
		store.listFn = func(call int) (domain.ConfluenceCommentInventory, error) {
			comments := append([]domain.ConfluenceCommentRecord(nil), store.comments...)
			if call == 3 {
				comments = append(comments, confluenceFooterCommentRecord("11", "user-3", "<p>new</p>"))
			}
			return completeQualifiedComments(comments...), nil
		}
		result, err := (&ConfluenceService{store: store, baseURL: "https://confluence.example.test"}).AddFooterCommentGuarded(
			context.Background(), "42", ConfluenceFooterCommentAddOpts{Body: []byte("<p>x</p>"), Apply: true, ExpectedProposalHash: preview.ProposalHash},
		)
		if result == nil || result.Status != "conflict" || !errors.Is(err, domain.ErrCheckFailed) || store.addCalls != 0 || store.listCalls != 3 {
			t.Fatalf("result=%+v err=%v add=%d lists=%d", result, err, store.addCalls, store.listCalls)
		}
	})

	t.Run("prewrite member edit", func(t *testing.T) {
		store := confluenceFooterCommentFixture()
		preview := previewConfluenceFooterComment(t, store, "<p>x</p>")
		store.listFn = func(call int) (domain.ConfluenceCommentInventory, error) {
			comments := append([]domain.ConfluenceCommentRecord(nil), store.comments...)
			if call == 3 {
				comments[0].Body, comments[0].BodyStorage, comments[0].Version = "edited", "<p>edited</p>", 2
			}
			return completeQualifiedComments(comments...), nil
		}
		result, err := (&ConfluenceService{store: store, baseURL: "https://confluence.example.test"}).AddFooterCommentGuarded(
			context.Background(), "42", ConfluenceFooterCommentAddOpts{Body: []byte("<p>x</p>"), Apply: true, ExpectedProposalHash: preview.ProposalHash},
		)
		if result == nil || result.Status != "conflict" || !errors.Is(err, domain.ErrCheckFailed) || store.addCalls != 0 {
			t.Fatalf("result=%+v err=%v add=%d", result, err, store.addCalls)
		}
	})

	t.Run("partial inventory", func(t *testing.T) {
		store := confluenceFooterCommentFixture()
		store.listFn = func(int) (domain.ConfluenceCommentInventory, error) {
			inventory := completeQualifiedComments()
			inventory.CommentsComplete = false
			inventory.PartialReasons = []string{domain.ConfluenceCommentPartialPageLimit}
			return inventory, nil
		}
		if _, err := (&ConfluenceService{store: store, baseURL: "https://confluence.example.test"}).AddFooterCommentGuarded(
			context.Background(), "42", ConfluenceFooterCommentAddOpts{Body: []byte("<p>x</p>")},
		); !errors.Is(err, domain.ErrCheckFailed) || store.addCalls != 0 {
			t.Fatalf("err=%v add=%d", err, store.addCalls)
		}
	})
}

func TestConfluenceFooterCommentProposalBindingRejectsEveryChangedInputBeforePOST(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*confluenceFooterCommentStoreStub) string
	}{
		{name: "backend", mutate: func(*confluenceFooterCommentStoreStub) string { return "https://other.example.test" }},
		{name: "page version", mutate: func(store *confluenceFooterCommentStoreStub) string {
			store.pageVersion = 8
			return "https://confluence.example.test"
		}},
		{name: "actor", mutate: func(store *confluenceFooterCommentStoreStub) string {
			store.user.ID = "user-9"
			return "https://confluence.example.test"
		}},
		{name: "capability", mutate: func(store *confluenceFooterCommentStoreStub) string {
			store.listFn = func(int) (domain.ConfluenceCommentInventory, error) {
				inventory := completeQualifiedComments(store.comments...)
				inventory.Capabilities.Footer = domain.ConfluenceCapabilityDocumented
				return inventory, nil
			}
			return "https://confluence.example.test"
		}},
		{name: "baseline ids", mutate: func(store *confluenceFooterCommentStoreStub) string {
			store.comments = append(store.comments, confluenceFooterCommentRecord("11", "user-3", "<p>new</p>"))
			return "https://confluence.example.test"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := confluenceFooterCommentFixture()
			preview := previewConfluenceFooterComment(t, store, "<p>x</p>")
			baseURL := test.mutate(store)
			result, err := applyConfluenceFooterComment(store, baseURL, "<p>x</p>", preview.ProposalHash)
			if result == nil || result.Status != "conflict" || !errors.Is(err, domain.ErrCheckFailed) || store.addCalls != 0 {
				t.Fatalf("result=%+v err=%v add=%d", result, err, store.addCalls)
			}
		})
	}

	t.Run("target identity and type", func(t *testing.T) {
		for name, meta := range map[string]*domain.PageMeta{
			"wrong id":   {ID: "43", Type: "page", Version: 7},
			"wrong type": {ID: "42", Type: "comment", Version: 7},
			"no version": {ID: "42", Type: "page"},
		} {
			t.Run(name, func(t *testing.T) {
				store := confluenceFooterCommentFixture()
				store.metaFn = func(int) (*domain.PageMeta, error) { return meta, nil }
				if _, err := (&ConfluenceService{store: store, baseURL: "https://confluence.example.test"}).AddFooterCommentGuarded(
					context.Background(), "42", ConfluenceFooterCommentAddOpts{Body: []byte("<p>x</p>")},
				); !errors.Is(err, domain.ErrCheckFailed) || store.addCalls != 0 || store.listCalls != 0 {
					t.Fatalf("err=%v add=%d lists=%d", err, store.addCalls, store.listCalls)
				}
			})
		}
	})
}

func TestConfluenceFooterCommentReadbackFailuresRemainUnknownWithoutReplay(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*confluenceFooterCommentStoreStub)
	}{
		{name: "readback error", mutate: func(store *confluenceFooterCommentStoreStub) {
			store.listFn = func(call int) (domain.ConfluenceCommentInventory, error) {
				if call == 4 {
					return domain.ConfluenceCommentInventory{}, errors.New("sensitive readback failure")
				}
				return completeQualifiedComments(store.comments...), nil
			}
		}},
		{name: "readback partial", mutate: func(store *confluenceFooterCommentStoreStub) {
			store.listFn = func(call int) (domain.ConfluenceCommentInventory, error) {
				inventory := completeQualifiedComments(store.comments...)
				if call == 4 {
					inventory.CommentsComplete = false
					inventory.PartialReasons = []string{domain.ConfluenceCommentPartialPageLimit}
				}
				return inventory, nil
			}
		}},
		{name: "baseline edited after POST", mutate: func(store *confluenceFooterCommentStoreStub) {
			store.listFn = func(call int) (domain.ConfluenceCommentInventory, error) {
				comments := append([]domain.ConfluenceCommentRecord(nil), store.comments...)
				if call == 4 {
					comments[0].Body, comments[0].BodyStorage, comments[0].Version = "edited", "<p>edited</p>", 2
				}
				return completeQualifiedComments(comments...), nil
			}
		}},
		{name: "baseline deleted after POST", mutate: func(store *confluenceFooterCommentStoreStub) {
			store.listFn = func(call int) (domain.ConfluenceCommentInventory, error) {
				comments := append([]domain.ConfluenceCommentRecord(nil), store.comments...)
				if call == 4 {
					comments = comments[1:]
				}
				return completeQualifiedComments(comments...), nil
			}
		}},
		{name: "page version changed after POST", mutate: func(store *confluenceFooterCommentStoreStub) {
			store.metaFn = func(call int) (*domain.PageMeta, error) {
				version := 7
				if call == 4 {
					version = 8
				}
				return &domain.PageMeta{ID: "42", Type: "page", Version: version}, nil
			}
		}},
		{name: "returned id mismatch", mutate: func(store *confluenceFooterCommentStoreStub) {
			store.addResult = &domain.Comment{ID: "21"}
		}},
		{name: "returned record wrong actor", mutate: func(store *confluenceFooterCommentStoreStub) {
			store.commit = []domain.ConfluenceCommentRecord{confluenceFooterCommentRecord("20", "user-9", "<p>x</p>")}
		}},
		{name: "returned record wrong body", mutate: func(store *confluenceFooterCommentStoreStub) {
			store.commit = []domain.ConfluenceCommentRecord{confluenceFooterCommentRecord("20", "user-1", "<p>other</p>")}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := confluenceFooterCommentFixture()
			preview := previewConfluenceFooterComment(t, store, "<p>x</p>")
			store.commit = []domain.ConfluenceCommentRecord{confluenceFooterCommentRecord("20", "user-1", "<p>x</p>")}
			store.addResult = &domain.Comment{ID: "20"}
			test.mutate(store)
			result, err := applyConfluenceFooterComment(store, "https://confluence.example.test", "<p>x</p>", preview.ProposalHash)
			var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
			if result == nil || result.Status != "outcome_unknown" || !errors.Is(err, domain.ErrCheckFailed) ||
				!errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() || store.addCalls != 1 || !store.singleAttempt {
				t.Fatalf("result=%+v err=%v ambiguous=%T add=%d single=%t", result, err, ambiguous, store.addCalls, store.singleAttempt)
			}
		})
	}
}

func TestValidateConfluenceFooterCommentBodyIsBoundedValidAndByteStable(t *testing.T) {
	body := []byte("<p>  reviewed &amp; exact  </p>\n")
	got, err := ValidateConfluenceFooterCommentBody(body)
	if err != nil || string(got) != string(body) {
		t.Fatalf("body=%q err=%v", got, err)
	}
	for _, invalid := range [][]byte{nil, []byte(" \n\t"), {0xff}, make([]byte, ConfluenceFooterCommentBodyMaxBytes+1)} {
		if _, err := ValidateConfluenceFooterCommentBody(invalid); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("input length=%d err=%v", len(invalid), err)
		}
	}
	if _, err := ValidateConfluenceFooterCommentBody([]byte("<p>unterminated")); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("malformed body err=%v", err)
	}
}
