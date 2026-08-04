package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type confluenceLabelStoreStub struct {
	*stubStore
	labels                []domain.ContentLabel
	truncated             bool
	listErr               error
	verificationErr       error
	verificationTruncated bool
	writeErr              error
	removeErrors          []error
	skipMutation          bool
	noCommitOnError       bool
	listCalls             int
	addCalls              int
	removeCalls           int
	addSingleAttempt      bool
	removeSingleAttempts  []bool
}

func (s *confluenceLabelStoreStub) ListContentLabels(context.Context, string) ([]domain.ContentLabel, bool, error) {
	s.listCalls++
	if s.listCalls > 1 {
		if s.verificationErr != nil {
			return nil, false, s.verificationErr
		}
		if s.verificationTruncated {
			return append([]domain.ContentLabel(nil), s.labels...), true, nil
		}
	}
	return append([]domain.ContentLabel(nil), s.labels...), s.truncated, s.listErr
}

func (s *confluenceLabelStoreStub) AddContentLabels(ctx context.Context, _ string, labels []domain.ContentLabel) error {
	s.addCalls++
	s.addSingleAttempt = domain.SingleAttempt(ctx)
	if !s.skipMutation && (!s.noCommitOnError || s.writeErr == nil) {
		for _, added := range labels {
			if !labelRecordPresent(s.labels, added.Name) {
				s.labels = append(s.labels, added)
			}
		}
	}
	return s.writeErr
}

func (s *confluenceLabelStoreStub) RemoveContentLabel(ctx context.Context, _ string, name string) error {
	s.removeCalls++
	s.removeSingleAttempts = append(s.removeSingleAttempts, domain.SingleAttempt(ctx))
	writeErr := s.writeErr
	if index := s.removeCalls - 1; index < len(s.removeErrors) {
		writeErr = s.removeErrors[index]
	}
	if !s.skipMutation && (!s.noCommitOnError || writeErr == nil) {
		filtered := s.labels[:0]
		for _, label := range s.labels {
			if label.Name != name {
				filtered = append(filtered, label)
			}
		}
		s.labels = filtered
	}
	return writeErr
}

type confluenceLabelStatusError int

func (e confluenceLabelStatusError) Error() string   { return "rejected" }
func (e confluenceLabelStatusError) HTTPStatus() int { return int(e) }

func labelRecordPresent(labels []domain.ContentLabel, name string) bool {
	for _, label := range labels {
		if label.Name == name {
			return true
		}
	}
	return false
}

func TestConfluenceLabelsListAndGuardedAdd(t *testing.T) {
	store := &confluenceLabelStoreStub{stubStore: &stubStore{}, labels: []domain.ContentLabel{{Prefix: "global", Name: "existing"}}}
	service := &ConfluenceService{store: store}
	listed, err := service.ListLabels(context.Background(), "42")
	if err != nil || !listed.Complete || listed.Count != 1 || listed.Labels[0].Name != "existing" {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
	preview, err := service.MutateLabelsGuarded(context.Background(), "42", ConfluenceLabelMutationOpts{Operation: "add", Labels: []string{" release ", "release", "urgent"}})
	if err != nil || preview.Status != "would_apply" || preview.ProposalHash == "" || store.addCalls != 0 || !reflect.DeepEqual(preview.Requested, []string{"release", "urgent"}) {
		t.Fatalf("preview=%+v calls=%d err=%v", preview, store.addCalls, err)
	}
	applied, err := service.MutateLabelsGuarded(context.Background(), "42", ConfluenceLabelMutationOpts{
		Operation: "add", Labels: []string{"urgent", "release"}, ExpectedProposalHash: preview.ProposalHash, Apply: true,
	})
	if err != nil || applied.Status != "applied" || store.addCalls != 1 || !store.addSingleAttempt || len(applied.Final) != 3 || applied.Final[1].Name != "release" || applied.Final[2].Name != "urgent" {
		t.Fatalf("applied=%+v calls=%d labels=%+v err=%v", applied, store.addCalls, store.labels, err)
	}
}

func TestConfluenceLabelsDoNotTreatPersonalLabelAsGlobal(t *testing.T) {
	store := &confluenceLabelStoreStub{stubStore: &stubStore{}, labels: []domain.ContentLabel{{Prefix: "my", Name: "same"}}}
	result, err := (&ConfluenceService{store: store}).MutateLabelsGuarded(context.Background(), "42", ConfluenceLabelMutationOpts{Operation: "add", Labels: []string{"same"}})
	if err != nil || result.Status != "would_apply" {
		t.Fatalf("personal label incorrectly satisfied global proposal: result=%+v err=%v", result, err)
	}
}

func TestConfluenceLabelsRefuseAmbiguousNonGlobalRemoval(t *testing.T) {
	store := &confluenceLabelStoreStub{stubStore: &stubStore{}, labels: []domain.ContentLabel{{Prefix: "global", Name: "same"}, {Prefix: "my", Name: "same"}}}
	_, err := (&ConfluenceService{store: store}).MutateLabelsGuarded(context.Background(), "42", ConfluenceLabelMutationOpts{Operation: "remove", Labels: []string{"same"}})
	if !errors.Is(err, domain.ErrCheckFailed) || store.removeCalls != 0 {
		t.Fatalf("ambiguous removal error=%v calls=%d", err, store.removeCalls)
	}
}

func TestConfluenceLabelsApplyGatesBeforeAlreadySatisfied(t *testing.T) {
	store := &confluenceLabelStoreStub{stubStore: &stubStore{}, labels: []domain.ContentLabel{{Prefix: "global", Name: "done"}}}
	service := &ConfluenceService{store: store}
	result, err := service.MutateLabelsGuarded(context.Background(), "42", ConfluenceLabelMutationOpts{
		Operation: "add", Labels: []string{"done"}, ExpectedProposalHash: "stale", Apply: true,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || store.addCalls != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, store.addCalls, err)
	}
	preview, err := service.MutateLabelsGuarded(context.Background(), "42", ConfluenceLabelMutationOpts{Operation: "add", Labels: []string{"done"}})
	if err != nil || preview.Status != "already_satisfied" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
}

func TestConfluenceLabelsReconcilesAmbiguousWriteAndRefusesTruncation(t *testing.T) {
	store := &confluenceLabelStoreStub{stubStore: &stubStore{}, writeErr: errors.New("connection lost")}
	service := &ConfluenceService{store: store}
	preview, err := service.MutateLabelsGuarded(context.Background(), "42", ConfluenceLabelMutationOpts{Operation: "add", Labels: []string{"one"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.MutateLabelsGuarded(context.Background(), "42", ConfluenceLabelMutationOpts{
		Operation: "add", Labels: []string{"one"}, ExpectedProposalHash: preview.ProposalHash, Apply: true,
	})
	if err != nil || result.Status != "applied" || !result.Reconciled || store.addCalls != 1 {
		t.Fatalf("reconciled=%+v calls=%d err=%v", result, store.addCalls, err)
	}

	truncated := &confluenceLabelStoreStub{stubStore: &stubStore{}, truncated: true}
	_, err = (&ConfluenceService{store: truncated}).MutateLabelsGuarded(context.Background(), "42", ConfluenceLabelMutationOpts{Operation: "remove", Labels: []string{"one"}})
	if !errors.Is(err, domain.ErrCheckFailed) || truncated.removeCalls != 0 {
		t.Fatalf("truncated mutation err=%v calls=%d", err, truncated.removeCalls)
	}
}

func TestConfluenceLabelsGuardedRemoveOutcomeMatrix(t *testing.T) {
	tests := []struct {
		name                  string
		store                 *confluenceLabelStoreStub
		wantStatus            string
		wantRemoveCalls       int
		wantAmbiguous         bool
		wantErr               bool
		wantReconciled        bool
		wantFinalGlobalLabels []string
	}{
		{
			name: "multi-label success uses one delete per requested label",
			store: &confluenceLabelStoreStub{stubStore: &stubStore{}, labels: []domain.ContentLabel{
				{Prefix: "global", Name: "one"}, {Prefix: "global", Name: "two"}, {Prefix: "global", Name: "keep"},
			}},
			wantStatus: "applied", wantRemoveCalls: 2, wantFinalGlobalLabels: []string{"keep"},
		},
		{
			name: "partial success remains ambiguous after a later definitive rejection",
			store: &confluenceLabelStoreStub{stubStore: &stubStore{}, labels: []domain.ContentLabel{
				{Prefix: "global", Name: "one"}, {Prefix: "global", Name: "two"},
			}, removeErrors: []error{nil, confluenceLabelStatusError(400)}, noCommitOnError: true},
			wantStatus: "unknown", wantRemoveCalls: 2, wantAmbiguous: true, wantErr: true, wantReconciled: true,
			wantFinalGlobalLabels: []string{"two"},
		},
		{
			name: "definitive rejection before any successful delete is failed",
			store: &confluenceLabelStoreStub{stubStore: &stubStore{}, labels: []domain.ContentLabel{
				{Prefix: "global", Name: "one"}, {Prefix: "global", Name: "two"},
			}, removeErrors: []error{confluenceLabelStatusError(400)}, noCommitOnError: true},
			wantStatus: "failed", wantRemoveCalls: 1, wantErr: true, wantReconciled: true,
			wantFinalGlobalLabels: []string{"one", "two"},
		},
		{
			name: "failed verification read is ambiguous",
			store: &confluenceLabelStoreStub{stubStore: &stubStore{}, labels: []domain.ContentLabel{
				{Prefix: "global", Name: "one"}, {Prefix: "global", Name: "two"},
			}, verificationErr: errors.New("verification unavailable")},
			wantStatus: "unknown", wantRemoveCalls: 2, wantAmbiguous: true, wantErr: true,
		},
		{
			name: "incomplete verification read is ambiguous",
			store: &confluenceLabelStoreStub{stubStore: &stubStore{}, labels: []domain.ContentLabel{
				{Prefix: "global", Name: "one"}, {Prefix: "global", Name: "two"},
			}, verificationTruncated: true},
			wantStatus: "unknown", wantRemoveCalls: 2, wantAmbiguous: true, wantErr: true,
		},
		{
			name: "complete verification goal mismatch is ambiguous",
			store: &confluenceLabelStoreStub{stubStore: &stubStore{}, labels: []domain.ContentLabel{
				{Prefix: "global", Name: "one"}, {Prefix: "global", Name: "two"},
			}, skipMutation: true},
			wantStatus: "unknown", wantRemoveCalls: 2, wantAmbiguous: true, wantErr: true,
			wantFinalGlobalLabels: []string{"one", "two"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &ConfluenceService{store: test.store}
			preview, err := service.MutateLabelsGuarded(context.Background(), "42", ConfluenceLabelMutationOpts{
				Operation: "remove", Labels: []string{"one", "two"},
			})
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			test.store.listCalls = 0
			result, err := service.MutateLabelsGuarded(context.Background(), "42", ConfluenceLabelMutationOpts{
				Operation: "remove", Labels: []string{"one", "two"}, Apply: true,
				ExpectedProposalHash: preview.ProposalHash,
			})
			if result == nil || result.Status != test.wantStatus || (err != nil) != test.wantErr ||
				test.store.removeCalls != test.wantRemoveCalls || test.store.listCalls != 2 ||
				result.Reconciled != test.wantReconciled {
				t.Fatalf("result=%+v err=%v removes=%d lists=%d", result, err, test.store.removeCalls, test.store.listCalls)
			}
			for index, singleAttempt := range test.store.removeSingleAttempts {
				if !singleAttempt {
					t.Fatalf("remove write %d did not receive single-attempt context", index+1)
				}
			}
			var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
			gotAmbiguous := errors.As(err, &ambiguous) && ambiguous.DiagnosticAmbiguousWrite()
			if gotAmbiguous != test.wantAmbiguous {
				t.Fatalf("ambiguous=%t want=%t err=%v", gotAmbiguous, test.wantAmbiguous, err)
			}
			if test.wantFinalGlobalLabels != nil {
				got := make([]string, 0, len(result.Final))
				for _, label := range result.Final {
					if label.Prefix == "global" {
						got = append(got, label.Name)
					}
				}
				if !reflect.DeepEqual(got, test.wantFinalGlobalLabels) {
					t.Fatalf("final global labels=%v want=%v", got, test.wantFinalGlobalLabels)
				}
			}
		})
	}
}

func TestNormalizeConfluenceLabelNamesRejectsUnsafeInput(t *testing.T) {
	for _, labels := range [][]string{nil, {""}, {"bad\nlabel"}, {string([]byte{0xff})}} {
		if _, err := normalizeConfluenceLabelNames(labels); !errors.Is(err, domain.ErrUsage) {
			t.Errorf("labels=%q error=%v", labels, err)
		}
	}
}
