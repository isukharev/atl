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
	labels      []domain.ContentLabel
	truncated   bool
	listErr     error
	writeErr    error
	addCalls    int
	removeCalls int
}

func (s *confluenceLabelStoreStub) ListContentLabels(context.Context, string) ([]domain.ContentLabel, bool, error) {
	return append([]domain.ContentLabel(nil), s.labels...), s.truncated, s.listErr
}

func (s *confluenceLabelStoreStub) AddContentLabels(_ context.Context, _ string, labels []domain.ContentLabel) error {
	s.addCalls++
	for _, added := range labels {
		if !labelRecordPresent(s.labels, added.Name) {
			s.labels = append(s.labels, added)
		}
	}
	return s.writeErr
}

func (s *confluenceLabelStoreStub) RemoveContentLabel(_ context.Context, _ string, name string) error {
	s.removeCalls++
	filtered := s.labels[:0]
	for _, label := range s.labels {
		if label.Name != name {
			filtered = append(filtered, label)
		}
	}
	s.labels = filtered
	return s.writeErr
}

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
	if err != nil || applied.Status != "applied" || store.addCalls != 1 || len(applied.Final) != 3 || applied.Final[1].Name != "release" || applied.Final[2].Name != "urgent" {
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

func TestNormalizeConfluenceLabelNamesRejectsUnsafeInput(t *testing.T) {
	for _, labels := range [][]string{nil, {""}, {"bad\nlabel"}, {string([]byte{0xff})}} {
		if _, err := normalizeConfluenceLabelNames(labels); !errors.Is(err, domain.ErrUsage) {
			t.Errorf("labels=%q error=%v", labels, err)
		}
	}
}
