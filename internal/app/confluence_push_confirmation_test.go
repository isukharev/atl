package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

func TestConfluencePushPreservesCandidateOnUnqualifiedRefresh(t *testing.T) {
	for _, tc := range []struct {
		name, id, body string
		version        int
	}{
		{"old version", "123", "<p>x</p>", 3},
		{"wrong body", "123", "<p>x</p>", 4},
		{"newer version", "123", "<p>candidate</p>", 5},
		{"wrong identity", "999", "<p>candidate</p>", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, path := syncedMirror(t, 3)
			candidate := []byte("<p>candidate</p>")
			if err := os.WriteFile(path, candidate, 0o644); err != nil {
				t.Fatal(err)
			}
			store := &stubStore{newVer: 4, page: &domain.Resource{ID: tc.id, Title: "T", SpaceKey: "SP", Version: tc.version, Body: []byte(tc.body)}}
			result, err := (&ConfluenceService{store: store, baseURL: confluenceTestBackendURL}).Push(t.Context(), path, PushOpts{Into: root})
			if err != nil || !result.Items[0].Pushed || result.Items[0].Warning == "" || !store.updateCalled {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			local, body, err := mirror.New(root).LoadCSF(path)
			if err != nil || !bytes.Equal(body, candidate) || local.Synced.Version != 3 || !local.Dirty {
				t.Fatalf("local=%+v body=%q err=%v", local, body, err)
			}
			base, ok := mirror.New(root).BaseBody("123")
			if !ok || string(base) != "<p>x</p>" {
				t.Fatalf("base=%q found=%v", base, ok)
			}
		})
	}
}

type pushConfirmationStore struct {
	domain.DocStore
	page          *domain.Resource
	readErr       error
	writes, reads int
	t             *testing.T
}

func (s *pushConfirmationStore) UpdatePage(context.Context, string, int, string, []byte, bool) (int, error) {
	s.writes++
	return 0, &domain.PageUpdateUnconfirmedError{ExpectedVersion: 4}
}
func (s *pushConfirmationStore) GetPage(ctx context.Context, _ string, _ domain.PullOpts) (*domain.Resource, error) {
	s.reads++
	if _, ok := ctx.Deadline(); !ok || !domain.SingleAttempt(ctx) || domain.ReadBudgetFromContext(ctx) == nil {
		s.t.Error("reconciliation read is not bounded")
	}
	return s.page, s.readErr
}

func TestConfluencePushReconcilesUnconfirmedAcknowledgementWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		readErr error
	}{
		{"exact match", 4, nil}, {"old version", 3, nil}, {"newer version", 5, nil}, {"unavailable", 0, errors.New("unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &pushConfirmationStore{t: t, readErr: tc.readErr, page: &domain.Resource{ID: "123", Title: "T", Version: tc.version, BodyPresent: true, Body: []byte("<p>candidate</p>")}}
			version, err := (&ConfluenceService{store: store}).updateConfluencePush(t.Context(), "123", 3, "T", []byte("<p>candidate</p>"), false)
			if store.writes != 1 || store.reads != 1 {
				t.Fatalf("writes=%d reads=%d", store.writes, store.reads)
			}
			if tc.version == 4 {
				if err != nil || version != 4 {
					t.Fatalf("version=%d err=%v", version, err)
				}
				return
			}
			var unconfirmed *domain.PageUpdateUnconfirmedError
			if version != 0 || !errors.As(err, &unconfirmed) {
				t.Fatalf("version=%d err=%v", version, err)
			}
		})
	}
}

func TestConfluencePushAggregatePreservesUnconfirmedOutcomeInEitherOrder(t *testing.T) {
	unconfirmed := &domain.PageUpdateUnconfirmedError{ExpectedVersion: 4}
	for _, pair := range [][2]error{{domain.ErrCheckFailed, unconfirmed}, {unconfirmed, domain.ErrCheckFailed}, {domain.ErrVersionConflict, unconfirmed}} {
		var got *domain.PageUpdateUnconfirmedError
		if !errors.As(moreSevereErr(pair[0], pair[1]), &got) {
			t.Fatal("aggregate lost ambiguous write evidence")
		}
	}
}
