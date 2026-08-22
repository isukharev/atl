package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const guardedLabelTestBackend = "https://jira.example.test"

type guardedLabelStore struct {
	domain.Tracker
	snapshots  []domain.JiraGuardedLabelSnapshot
	readErr    error
	readErrAt  int
	writeErr   error
	readHook   func(context.Context, int)
	reads      int
	references []string
	writes     []domain.JiraGuardedLabelWrite
}

func (s *guardedLabelStore) ReadGuardedLabelSnapshot(ctx context.Context, reference string) (domain.JiraGuardedLabelSnapshot, error) {
	s.reads++
	s.references = append(s.references, reference)
	if s.readHook != nil {
		s.readHook(ctx, s.reads)
	}
	if s.readErr != nil && (s.readErrAt == 0 || s.reads == s.readErrAt) {
		return domain.JiraGuardedLabelSnapshot{}, s.readErr
	}
	if len(s.snapshots) == 0 {
		return domain.JiraGuardedLabelSnapshot{}, errors.New("missing fixture snapshot")
	}
	snapshot := s.snapshots[0]
	if len(s.snapshots) > 1 {
		s.snapshots = s.snapshots[1:]
	}
	snapshot.Labels = append([]string(nil), snapshot.Labels...)
	return snapshot, nil
}

func (s *guardedLabelStore) WriteGuardedLabelDelta(_ context.Context, write domain.JiraGuardedLabelWrite) error {
	s.writes = append(s.writes, write)
	return s.writeErr
}

func guardedLabelSnapshot(updated string, labels ...string) domain.JiraGuardedLabelSnapshot {
	return domain.JiraGuardedLabelSnapshot{
		ID: "10", Key: "OPS-1", Project: "OPS", Labels: labels, Updated: updated, Complete: true,
	}
}

func guardedLabelService(store *guardedLabelStore) *JiraService {
	return &JiraService{tr: store, baseURL: guardedLabelTestBackend}
}

func TestNormalizeJiraGuardedLabelOptsRejectsAmbiguousAndUnboundedInputs(t *testing.T) {
	max := strings.Repeat("x", jiraGuardedLabelMaxBytes)
	tooLong := max + "x"
	oneHundred := make([]string, 100)
	for index := range oneHundred {
		oneHundred[index] = fmt.Sprintf("label-%03d", index)
	}
	tests := []struct {
		name string
		opts JiraGuardedLabelOpts
		ok   bool
	}{
		{"combined and trimmed", JiraGuardedLabelOpts{Add: []string{" z ", max}, Remove: []string{"old"}}, true},
		{"100", JiraGuardedLabelOpts{Add: oneHundred}, true},
		{"none", JiraGuardedLabelOpts{}, false},
		{"explicit empty", JiraGuardedLabelOpts{Add: []string{""}}, false},
		{"empty element", JiraGuardedLabelOpts{Add: []string{"one", " "}}, false},
		{"duplicate", JiraGuardedLabelOpts{Add: []string{"one", " one "}}, false},
		{"overlap", JiraGuardedLabelOpts{Add: []string{"one"}, Remove: []string{"one"}}, false},
		{"invalid utf8", JiraGuardedLabelOpts{Add: []string{string([]byte{0xff})}}, false},
		{"256 bytes", JiraGuardedLabelOpts{Add: []string{tooLong}}, false},
		{"101", JiraGuardedLabelOpts{Add: append(append([]string(nil), oneHundred...), "extra")}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeJiraGuardedLabelOpts(test.opts)
			if (err == nil) != test.ok {
				t.Fatalf("got=%+v err=%v", got, err)
			}
			if err != nil && !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestGuardedLabelsPreviewPermutationAndApply(t *testing.T) {
	const before = "2026-08-22T10:00:00.000+0000"
	const after = "2026-08-22T10:00:01.000+0000"
	previewStore := &guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{guardedLabelSnapshot(before, "keep", "old")}}
	preview, err := guardedLabelService(previewStore).GuardedLabels(t.Context(), " ops-1 ", JiraGuardedLabelOpts{Add: []string{"z", "a"}, Remove: []string{"old"}})
	if err != nil || preview.Status != "would_apply" || preview.Mode != "preview" || !preview.Complete || preview.WriteAttempted || previewStore.reads != 1 || len(previewStore.writes) != 0 {
		t.Fatalf("preview=%+v err=%v reads=%d writes=%d", preview, err, previewStore.reads, len(previewStore.writes))
	}
	permuted, err := guardedLabelService(&guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{guardedLabelSnapshot(before, "keep", "old")}}).GuardedLabels(
		t.Context(), "OPS-1", JiraGuardedLabelOpts{Add: []string{"a", "z"}, Remove: []string{"old"}},
	)
	if err != nil || permuted.ProposalHash != preview.ProposalHash {
		t.Fatalf("permuted=%+v err=%v", permuted, err)
	}
	applyStore := &guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{
		guardedLabelSnapshot(before, "keep", "old"), guardedLabelSnapshot(before, "keep", "old"), guardedLabelSnapshot(after, "a", "keep", "z"),
	}}
	result, err := guardedLabelService(applyStore).GuardedLabels(t.Context(), "OPS-1", JiraGuardedLabelOpts{
		Add: []string{"z", "a"}, Remove: []string{"old"}, Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	if err != nil || result.Status != "applied" || !result.Complete || !result.WriteAttempted || !result.Reconciled || result.ReadbackUpdated != after || len(applyStore.writes) != 1 {
		t.Fatalf("result=%+v err=%v writes=%+v", result, err, applyStore.writes)
	}
	write := applyStore.writes[0]
	if write.ID != "10" || strings.Join(write.Add, ",") != "a,z" || strings.Join(write.Remove, ",") != "old" {
		t.Fatalf("write=%+v", write)
	}
	if got := strings.Join(applyStore.references, ","); got != "OPS-1,10,10" {
		t.Fatalf("read references=%q, want key then immutable id twice", got)
	}
}

func TestGuardedLabelProposalHashBindsEveryReviewedMemberAndExcludesRuntimeCloseout(t *testing.T) {
	result := newJiraGuardedLabelResult("OPS-1", JiraGuardedLabelOpts{Add: []string{"new"}, Remove: []string{"old"}})
	result.BackendSHA256, result.IssueID, result.Key, result.Project, result.Updated = strings.Repeat("a", 64), "10", "OPS-1", "OPS", "2026-08-22T10:00:00Z"
	current, desired, effectiveAdd, effectiveRemove := []string{"old"}, []string{"new"}, []string{"new"}, []string{"old"}
	baseline := guardedLabelProposalHash(result, current, desired, effectiveAdd, effectiveRemove)
	mutations := map[string]func(*JiraGuardedLabelResult, *[]string, *[]string, *[]string, *[]string){
		"schema":           func(r *JiraGuardedLabelResult, _, _, _, _ *[]string) { r.SchemaVersion++ },
		"operation":        func(r *JiraGuardedLabelResult, _, _, _, _ *[]string) { r.Operation += "_other" },
		"backend":          func(r *JiraGuardedLabelResult, _, _, _, _ *[]string) { r.BackendSHA256 = strings.Repeat("b", 64) },
		"requested key":    func(r *JiraGuardedLabelResult, _, _, _, _ *[]string) { r.RequestedKey = "OPS-2" },
		"identity":         func(r *JiraGuardedLabelResult, _, _, _, _ *[]string) { r.IssueID = "11" },
		"canonical key":    func(r *JiraGuardedLabelResult, _, _, _, _ *[]string) { r.Key = "OPS-2" },
		"project":          func(r *JiraGuardedLabelResult, _, _, _, _ *[]string) { r.Project = "ALT" },
		"updated":          func(r *JiraGuardedLabelResult, _, _, _, _ *[]string) { r.Updated = "2026-08-22T10:00:01Z" },
		"requested add":    func(r *JiraGuardedLabelResult, _, _, _, _ *[]string) { r.Add = []string{"other"} },
		"requested remove": func(r *JiraGuardedLabelResult, _, _, _, _ *[]string) { r.Remove = []string{"other"} },
		"current":          func(_ *JiraGuardedLabelResult, values, _, _, _ *[]string) { *values = []string{"other"} },
		"desired":          func(_ *JiraGuardedLabelResult, _, values, _, _ *[]string) { *values = []string{"other"} },
		"effective add":    func(_ *JiraGuardedLabelResult, _, _, values, _ *[]string) { *values = []string{"other"} },
		"effective remove": func(_ *JiraGuardedLabelResult, _, _, _, values *[]string) { *values = []string{"other"} },
		"bounds":           func(r *JiraGuardedLabelResult, _, _, _, _ *[]string) { r.Bounds.MaxLabelBytes++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			copyResult := *result
			copyResult.Add, copyResult.Remove = append([]string(nil), result.Add...), append([]string(nil), result.Remove...)
			c, d, a, rm := append([]string(nil), current...), append([]string(nil), desired...), append([]string(nil), effectiveAdd...), append([]string(nil), effectiveRemove...)
			mutate(&copyResult, &c, &d, &a, &rm)
			if got := guardedLabelProposalHash(&copyResult, c, d, a, rm); got == baseline {
				t.Fatal("reviewed proposal mutation did not change hash")
			}
		})
	}
	copyResult := *result
	copyResult.Mode, copyResult.Status, copyResult.WriteAttempted, copyResult.Reconciled = "apply", "applied", true, true
	copyResult.Usage = JiraGuardedLabelUsage{Requests: 4, ResponseBytes: 10}
	if got := guardedLabelProposalHash(&copyResult, current, desired, effectiveAdd, effectiveRemove); got != baseline {
		t.Fatalf("runtime closeout changed proposal hash: %s != %s", got, baseline)
	}
}

func TestGuardedLabelsHashPrecedesNoopAndPrewriteDrift(t *testing.T) {
	const updated = "2026-08-22T10:00:00.000+0000"
	preview, err := guardedLabelService(&guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{guardedLabelSnapshot(updated, "done")}}).GuardedLabels(
		t.Context(), "OPS-1", JiraGuardedLabelOpts{Add: []string{"done"}},
	)
	if err != nil || preview.Status != "already_satisfied" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	mismatchStore := &guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{guardedLabelSnapshot(updated, "done")}}
	result, err := guardedLabelService(mismatchStore).GuardedLabels(t.Context(), "OPS-1", JiraGuardedLabelOpts{
		Add: []string{"done"}, Apply: true, ExpectedProposalHash: strings.Repeat("0", 64),
	})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || !result.Complete || len(mismatchStore.writes) != 0 || mismatchStore.reads != 1 {
		t.Fatalf("result=%+v err=%v reads=%d writes=%d", result, err, mismatchStore.reads, len(mismatchStore.writes))
	}
	matchStore := &guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{guardedLabelSnapshot(updated, "done")}}
	result, err = guardedLabelService(matchStore).GuardedLabels(t.Context(), "OPS-1", JiraGuardedLabelOpts{
		Add: []string{"done"}, Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	if err != nil || result.Status != "already_satisfied" || !result.Complete || matchStore.reads != 1 || len(matchStore.writes) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	initial := guardedLabelSnapshot(updated, "old")
	drifted := guardedLabelSnapshot("2026-08-22T10:00:01.000+0000", "old")
	preview, _ = guardedLabelService(&guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{initial}}).GuardedLabels(t.Context(), "OPS-1", JiraGuardedLabelOpts{Add: []string{"new"}})
	driftStore := &guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{initial, drifted}}
	result, err = guardedLabelService(driftStore).GuardedLabels(t.Context(), "OPS-1", JiraGuardedLabelOpts{Add: []string{"new"}, Apply: true, ExpectedProposalHash: preview.ProposalHash})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || !result.Complete || len(driftStore.writes) != 0 || driftStore.reads != 2 {
		t.Fatalf("result=%+v err=%v reads=%d writes=%d", result, err, driftStore.reads, len(driftStore.writes))
	}
	failedPrewrite := &guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{initial}, readErr: errors.New("private read failure"), readErrAt: 2}
	result, err = guardedLabelService(failedPrewrite).GuardedLabels(t.Context(), "OPS-1", JiraGuardedLabelOpts{Add: []string{"new"}, Apply: true, ExpectedProposalHash: preview.ProposalHash})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || result.Complete || len(failedPrewrite.writes) != 0 || failedPrewrite.reads != 2 {
		t.Fatalf("result=%+v err=%v reads=%d writes=%d", result, err, failedPrewrite.reads, len(failedPrewrite.writes))
	}
}

const guardedLabelPrivateCanary = "private-backend-canary"

type guardedLabelStatusError struct{ status int }

func (e *guardedLabelStatusError) Error() string   { return guardedLabelPrivateCanary }
func (e *guardedLabelStatusError) HTTPStatus() int { return e.status }
func (e *guardedLabelStatusError) Unwrap() error   { return domain.ErrForbidden }

type guardedLabelNoAttemptTestError struct{}

func (guardedLabelNoAttemptTestError) Error() string                  { return "private denial" }
func (guardedLabelNoAttemptTestError) Unwrap() error                  { return domain.ErrForbidden }
func (guardedLabelNoAttemptTestError) DiagnosticWriteAttempted() bool { return false }

func TestGuardedLabelsClosedOutcomeMatrixAndPrivacy(t *testing.T) {
	const before = "2026-08-22T10:00:00.000+0000"
	const after = "2026-08-22T10:00:01.000+0000"
	initial := guardedLabelSnapshot(before, "old")
	preview, _ := guardedLabelService(&guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{initial}}).GuardedLabels(t.Context(), "OPS-1", JiraGuardedLabelOpts{Add: []string{"new"}, Remove: []string{"old"}})
	tests := []struct {
		name, status string
		writeErr     error
		readback     domain.JiraGuardedLabelSnapshot
		wantErr      bool
	}{
		{"definitive rejection", "not_applied", &guardedLabelStatusError{status: 403}, domain.JiraGuardedLabelSnapshot{}, true},
		{"acknowledged", "applied", nil, guardedLabelSnapshot(after, "new"), false},
		{"ambiguous recovered", "recovered", errors.New("private ambiguous transport"), guardedLabelSnapshot(after, "new"), false},
		{"non advancing", "outcome_unknown", nil, guardedLabelSnapshot(before, "new"), true},
		{"conflicting set", "outcome_unknown", nil, guardedLabelSnapshot(after, "other"), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshots := []domain.JiraGuardedLabelSnapshot{initial, initial}
			if test.status != "not_applied" {
				snapshots = append(snapshots, test.readback)
			}
			store := &guardedLabelStore{snapshots: snapshots, writeErr: test.writeErr}
			result, err := guardedLabelService(store).GuardedLabels(t.Context(), "OPS-1", JiraGuardedLabelOpts{
				Add: []string{"new"}, Remove: []string{"old"}, Apply: true, ExpectedProposalHash: preview.ProposalHash,
			})
			if (err != nil) != test.wantErr || result.Status != test.status || !result.Complete || !result.WriteAttempted {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if err != nil {
				joined := errors.Join(err, fmt.Errorf("outer: %w", err))
				for _, formatted := range []string{err.Error(), fmt.Sprint(err), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), fmt.Sprintf("%q", err), joined.Error()} {
					if strings.Contains(formatted, guardedLabelPrivateCanary) {
						t.Fatalf("private error content leaked: %s", formatted)
					}
				}
				var status interface{ HTTPStatus() int }
				if test.status == "not_applied" && (!errors.As(err, &status) || status.HTTPStatus() != 403) {
					t.Fatalf("safe status identity lost: %v", err)
				}
				var original *guardedLabelStatusError
				if errors.As(err, &original) || errors.Unwrap(err) != nil {
					t.Fatalf("private backend error identity escaped: %#v", err)
				}
				if test.status == "not_applied" && !errors.Is(err, domain.ErrForbidden) {
					t.Fatalf("safe sentinel identity lost: %v", err)
				}
			}
		})
	}
}

func TestGuardedLabelsCancellationBeforeDispatchAndCurrentBound(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	store := &guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{guardedLabelSnapshot(time.Now().Format(time.RFC3339Nano))}}
	result, err := guardedLabelService(store).GuardedLabels(ctx, "OPS-1", JiraGuardedLabelOpts{Add: []string{"new"}})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || result.Complete || store.reads != 0 || result.WriteAttempted {
		t.Fatalf("result=%+v err=%v reads=%d", result, err, store.reads)
	}
	labels := make([]string, jiraGuardedLabelMaxCurrent+1)
	for index := range labels {
		labels[index] = fmt.Sprintf("label-%04d", index)
	}
	store = &guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{guardedLabelSnapshot("2026-08-22T10:00:00Z", labels...)}}
	result, err = guardedLabelService(store).GuardedLabels(t.Context(), "OPS-1", JiraGuardedLabelOpts{Add: []string{"new"}})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || result.Complete || len(store.writes) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGuardedLabelsRejectsOversizedKeyBeforePortAccess(t *testing.T) {
	store := &guardedLabelStore{}
	key := "OPS-" + strings.Repeat("1", jiraGuardedLabelMaxKeyBytes)
	result, err := guardedLabelService(store).GuardedLabels(t.Context(), key, JiraGuardedLabelOpts{Add: []string{"new"}})
	if result != nil || !errors.Is(err, domain.ErrUsage) || store.reads != 0 || len(store.writes) != 0 {
		t.Fatalf("result=%+v err=%v reads=%d writes=%d", result, err, store.reads, len(store.writes))
	}
}

func TestGuardedLabelsPreDispatchRefusalAndLateEvidenceCannotBecomeSuccess(t *testing.T) {
	const before = "2026-08-22T10:00:00.000+0000"
	const after = "2026-08-22T10:00:01.000+0000"
	initial := guardedLabelSnapshot(before, "old")
	preview, _ := guardedLabelService(&guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{initial}}).GuardedLabels(t.Context(), "OPS-1", JiraGuardedLabelOpts{Add: []string{"new"}, Remove: []string{"old"}})

	denied := &guardedLabelStore{
		snapshots: []domain.JiraGuardedLabelSnapshot{initial, initial, guardedLabelSnapshot(after, "new")},
		writeErr:  guardedLabelNoAttemptTestError{},
	}
	result, err := guardedLabelService(denied).GuardedLabels(t.Context(), "OPS-1", JiraGuardedLabelOpts{
		Add: []string{"new"}, Remove: []string{"old"}, Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, domain.ErrForbidden) || result.Status != "blocked" || result.WriteAttempted || denied.reads != 2 {
		t.Fatalf("denial result=%+v err=%v reads=%d", result, err, denied.reads)
	}

	latePreviewCtx, cancelPreview := context.WithCancel(t.Context())
	latePreview := &guardedLabelStore{
		snapshots: []domain.JiraGuardedLabelSnapshot{initial},
		readHook: func(_ context.Context, read int) {
			if read == 1 {
				cancelPreview()
			}
		},
	}
	result, err = guardedLabelService(latePreview).GuardedLabels(latePreviewCtx, "OPS-1", JiraGuardedLabelOpts{Add: []string{"new"}})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || result.WriteAttempted {
		t.Fatalf("late preview result=%+v err=%v", result, err)
	}

	deadlineCtx, cancelDeadline := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancelDeadline()
	lateReadback := &guardedLabelStore{
		snapshots: []domain.JiraGuardedLabelSnapshot{initial, initial, guardedLabelSnapshot(after, "new")},
		readHook: func(ctx context.Context, read int) {
			if read == 3 {
				<-ctx.Done()
			}
		},
	}
	result, err = guardedLabelService(lateReadback).GuardedLabels(deadlineCtx, "OPS-1", JiraGuardedLabelOpts{
		Add: []string{"new"}, Remove: []string{"old"}, Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "outcome_unknown" || result.Complete || !result.WriteAttempted || result.Reconciled {
		t.Fatalf("late readback result=%+v err=%v", result, err)
	}
}
