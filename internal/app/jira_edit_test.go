package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

const editUpdatedBefore = "2026-08-22T10:00:00.000+0000"
const editUpdatedAfter = "2026-08-22T10:00:01.000+0000"

type guardedEditTracker struct {
	*recordingTracker
	reads         []*domain.Issue
	readErrs      []error
	readCalls     int
	updateCalls   int
	updateSingle  bool
	updateBodyNil bool
	updateErr     error
}

func (t *guardedEditTracker) GetIssue(_ context.Context, key string, fields []string) (*domain.Issue, error) {
	t.issueKey, t.issueFields = key, append([]string(nil), fields...)
	index := t.readCalls
	t.readCalls++
	if index < len(t.readErrs) && t.readErrs[index] != nil {
		return nil, t.readErrs[index]
	}
	if index >= len(t.reads) {
		return nil, fmt.Errorf("unexpected read")
	}
	return t.reads[index], nil
}

func (t *guardedEditTracker) Update(ctx context.Context, key, summary string, body []byte, fields map[string]domain.JiraFieldInput) error {
	t.updateCalls++
	t.updateSingle = domain.SingleAttempt(ctx)
	t.updateBodyNil = body == nil
	t.updateKey, t.updateSumm, t.updateBody, t.updateFields = key, summary, append([]byte(nil), body...), fields
	return t.updateErr
}

func editIssue(body any, updated string) *domain.Issue {
	fields := map[string]any{"description": body, "updated": updated}
	text, _ := body.(string)
	return &domain.Issue{ID: "10007", Key: "PROJ-1", Body: text, Fields: fields, Raw: fields}
}

func previewDescriptionEdit(t *testing.T, old, replacement string, all bool) *JiraDescriptionEditResult {
	t.Helper()
	return previewDescriptionEditBody(t, "a OLD b", old, replacement, all)
}

func previewDescriptionEditBody(t *testing.T, body, old, replacement string, all bool) *JiraDescriptionEditResult {
	t.Helper()
	tracker := &guardedEditTracker{recordingTracker: &recordingTracker{}, reads: []*domain.Issue{editIssue(body, editUpdatedBefore)}}
	result, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).EditDescriptionGuarded(t.Context(), " proj-1 ", JiraDescriptionEditOpts{Old: []byte(old), New: []byte(replacement), All: all})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type privateJiraEditCause struct {
	canary string
	status int
}

func (e *privateJiraEditCause) Error() string   { return e.canary }
func (e *privateJiraEditCause) HTTPStatus() int { return e.status }
func (e *privateJiraEditCause) Unwrap() error   { return domain.ErrForbidden }

func TestEditDescriptionGuardedPreviewBindsEvidenceAndDoesNotWrite(t *testing.T) {
	result := previewDescriptionEdit(t, "OLD", "NEW", false)
	if result.Status != "would_apply" || result.Mode != "dry-run" || result.RequestedKey != "PROJ-1" || result.Key != "PROJ-1" || result.IssueID != "10007" || result.Updated != editUpdatedBefore {
		t.Fatalf("unexpected identity/status: %#v", result)
	}
	if result.ProposalHash == "" || result.BackendSHA256 == "" || result.OldBytes != 3 || result.NewBytes != 3 || result.BeforeBytes != 7 || result.AfterBytes != 7 || result.Matcher.Pass != "exact" || result.Matcher.Count != 1 || len(result.Matcher.Offsets) != 1 || result.WriteAttempted || !result.Complete {
		t.Fatalf("incomplete proposal: %#v", result)
	}
}

func TestEditDescriptionGuardedProposalHashBindsEveryReviewedMember(t *testing.T) {
	base := previewDescriptionEdit(t, "OLD", "NEW", false)
	tests := []struct {
		name   string
		mutate func(*JiraDescriptionEditResult)
	}{
		{"schema version", func(r *JiraDescriptionEditResult) { r.SchemaVersion++ }},
		{"backend", func(r *JiraDescriptionEditResult) { r.BackendSHA256 += "x" }},
		{"requested key", func(r *JiraDescriptionEditResult) { r.RequestedKey = "ALT-1" }},
		{"canonical key", func(r *JiraDescriptionEditResult) { r.Key = "ALT-1" }},
		{"issue id", func(r *JiraDescriptionEditResult) { r.IssueID = "10008" }},
		{"updated", func(r *JiraDescriptionEditResult) { r.Updated = editUpdatedAfter }},
		{"old hash", func(r *JiraDescriptionEditResult) { r.OldSHA256 += "x" }},
		{"old bytes", func(r *JiraDescriptionEditResult) { r.OldBytes++ }},
		{"new hash", func(r *JiraDescriptionEditResult) { r.NewSHA256 += "x" }},
		{"new bytes", func(r *JiraDescriptionEditResult) { r.NewBytes++ }},
		{"all", func(r *JiraDescriptionEditResult) { r.All = !r.All }},
		{"matcher pass", func(r *JiraDescriptionEditResult) { r.Matcher.Pass += "x" }},
		{"matcher count", func(r *JiraDescriptionEditResult) { r.Matcher.Count++ }},
		{"matcher offset", func(r *JiraDescriptionEditResult) { r.Matcher.Offsets[0].Start++ }},
		{"before hash", func(r *JiraDescriptionEditResult) { r.BeforeSHA256 += "x" }},
		{"before bytes", func(r *JiraDescriptionEditResult) { r.BeforeBytes++ }},
		{"after hash", func(r *JiraDescriptionEditResult) { r.AfterSHA256 += "x" }},
		{"after bytes", func(r *JiraDescriptionEditResult) { r.AfterBytes++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := *base
			changed.Matcher.Offsets = append([]JiraDescriptionEditOffset(nil), base.Matcher.Offsets...)
			test.mutate(&changed)
			if jiraDescriptionEditProposalHash(&changed) == base.ProposalHash {
				t.Fatal("proposal hash did not change")
			}
		})
	}
}

func TestEditDescriptionGuardedApplyUsesImmutableIDSingleAttemptAndAdvancingReadback(t *testing.T) {
	preview := previewDescriptionEdit(t, "OLD", "NEW", false)
	tracker := &guardedEditTracker{recordingTracker: &recordingTracker{}, reads: []*domain.Issue{
		editIssue("a OLD b", editUpdatedBefore), editIssue("a OLD b", editUpdatedBefore), editIssue("a NEW b", editUpdatedAfter),
	}}
	result, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).EditDescriptionGuarded(t.Context(), "PROJ-1", JiraDescriptionEditOpts{
		Old: []byte("OLD"), New: []byte("NEW"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "applied" || !result.WriteAttempted || !result.Reconciled || tracker.updateCalls != 1 || !tracker.updateSingle || tracker.updateKey != "10007" || tracker.updateSumm != "" || tracker.updateFields != nil || string(tracker.updateBody) != "a NEW b" {
		t.Fatalf("result=%#v tracker=%#v", result, tracker)
	}
}

func TestEditDescriptionGuardedPreservesMatcherCompatibility(t *testing.T) {
	tests := []struct {
		name, body, old, replacement, pass, after string
		all                                       bool
		count                                     int
	}{
		{name: "same-line whitespace", body: "left  target   value right", old: "target value", replacement: "X", pass: "whitespace", after: "left  X right", count: 1},
		{name: "invisible and NBSP", body: "left target\u200b\u00a0value right", old: "target value", replacement: "X", pass: "invisible", after: "left X right", count: 1},
		{name: "all", body: "OLD x OLD", old: "OLD", replacement: "NEW", pass: "exact", after: "NEW x NEW", all: true, count: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := previewDescriptionEditBody(t, test.body, test.old, test.replacement, test.all)
			if result.Matcher.Pass != test.pass || result.Matcher.Count != test.count || result.AfterSHA256 != hashBytes([]byte(test.after)) || result.AfterBytes != len([]byte(test.after)) {
				t.Fatalf("matcher=%#v after=%s/%d", result.Matcher, result.AfterSHA256, result.AfterBytes)
			}
			for i := 1; i < len(result.Matcher.Offsets); i++ {
				if result.Matcher.Offsets[i-1].End > result.Matcher.Offsets[i].Start {
					t.Fatalf("offsets are not ordered: %#v", result.Matcher.Offsets)
				}
			}
		})
	}
}

func TestEditDescriptionGuardedRefusesCrossLineWhitespaceMatch(t *testing.T) {
	tracker := &guardedEditTracker{recordingTracker: &recordingTracker{}, reads: []*domain.Issue{editIssue("Verify\nsteps", editUpdatedBefore)}}
	_, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).EditDescriptionGuarded(t.Context(), "PROJ-1", JiraDescriptionEditOpts{Old: []byte("Verify steps"), New: []byte("done")})
	if !errors.Is(err, domain.ErrCheckFailed) || tracker.updateCalls != 0 {
		t.Fatalf("err=%v writes=%d", err, tracker.updateCalls)
	}
}

func TestEditDescriptionGuardedFullBodyClearUsesNonNilDescriptionPUT(t *testing.T) {
	preview := previewDescriptionEditBody(t, "obsolete", "obsolete", "", false)
	tracker := &guardedEditTracker{recordingTracker: &recordingTracker{}, reads: []*domain.Issue{
		editIssue("obsolete", editUpdatedBefore), editIssue("obsolete", editUpdatedBefore), editIssue("", editUpdatedAfter),
	}}
	result, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).EditDescriptionGuarded(t.Context(), "PROJ-1", JiraDescriptionEditOpts{
		Old: []byte("obsolete"), New: []byte{}, Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	if err != nil || result.Status != "applied" || tracker.updateCalls != 1 || tracker.updateBodyNil || len(tracker.updateBody) != 0 {
		t.Fatalf("result=%#v err=%v writes=%d body_nil=%t body_len=%d", result, err, tracker.updateCalls, tracker.updateBodyNil, len(tracker.updateBody))
	}
}

func TestEditDescriptionGuardedPrewriteDriftBlocksWithoutPUT(t *testing.T) {
	preview := previewDescriptionEdit(t, "OLD", "NEW", false)
	tracker := &guardedEditTracker{recordingTracker: &recordingTracker{}, reads: []*domain.Issue{
		editIssue("a OLD b", editUpdatedBefore), editIssue("changed OLD b", editUpdatedAfter),
	}}
	result, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).EditDescriptionGuarded(t.Context(), "PROJ-1", JiraDescriptionEditOpts{
		Old: []byte("OLD"), New: []byte("NEW"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || tracker.updateCalls != 0 || result.WriteAttempted {
		t.Fatalf("result=%#v err=%v writes=%d", result, err, tracker.updateCalls)
	}
}

func TestEditDescriptionGuardedAmbiguousWriteRecoversOnlyExactAdvancingReadback(t *testing.T) {
	preview := previewDescriptionEdit(t, "OLD", "NEW", false)
	tracker := &guardedEditTracker{recordingTracker: &recordingTracker{}, reads: []*domain.Issue{
		editIssue("a OLD b", editUpdatedBefore), editIssue("a OLD b", editUpdatedBefore), editIssue("a NEW b", editUpdatedAfter),
	}, updateErr: errors.New("private response body and path")}
	result, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).EditDescriptionGuarded(t.Context(), "PROJ-1", JiraDescriptionEditOpts{
		Old: []byte("OLD"), New: []byte("NEW"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	if err != nil || result.Status != "recovered" || !result.WriteAttempted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestEditDescriptionGuardedDefinitiveRejectionIsNotApplied(t *testing.T) {
	preview := previewDescriptionEdit(t, "OLD", "NEW", false)
	tracker := &guardedEditTracker{recordingTracker: &recordingTracker{}, reads: []*domain.Issue{
		editIssue("a OLD b", editUpdatedBefore), editIssue("a OLD b", editUpdatedBefore),
	}, updateErr: createHTTPStatusError{status: 400}}
	result, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).EditDescriptionGuarded(t.Context(), "PROJ-1", JiraDescriptionEditOpts{
		Old: []byte("OLD"), New: []byte("NEW"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	var statusErr interface{ HTTPStatus() int }
	if result.Status != "not_applied" || tracker.updateCalls != 1 || result.Reconciled || !errors.As(err, &statusErr) || statusErr.HTTPStatus() != 400 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestEditDescriptionGuardedAlreadySatisfiedDoesNotWrite(t *testing.T) {
	tracker := &guardedEditTracker{recordingTracker: &recordingTracker{}, reads: []*domain.Issue{editIssue("a OLD b", editUpdatedBefore)}}
	result, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).EditDescriptionGuarded(t.Context(), "PROJ-1", JiraDescriptionEditOpts{Old: []byte("OLD"), New: []byte("OLD")})
	if err != nil || result.Status != "already_satisfied" || tracker.updateCalls != 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestEditDescriptionGuardedUnchangedReadbackIsPrivateOutcomeUnknown(t *testing.T) {
	preview := previewDescriptionEdit(t, "OLD", "NEW", false)
	private := &privateJiraEditCause{canary: "SECRET /rest/api/2/issue/10007", status: 503}
	tracker := &guardedEditTracker{recordingTracker: &recordingTracker{}, reads: []*domain.Issue{
		editIssue("a OLD b", editUpdatedBefore), editIssue("a OLD b", editUpdatedBefore), editIssue("a OLD b", editUpdatedBefore),
	}, updateErr: private}
	result, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).EditDescriptionGuarded(t.Context(), "PROJ-1", JiraDescriptionEditOpts{
		Old: []byte("OLD"), New: []byte("NEW"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
	})
	var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
	if result.Status != "outcome_unknown" || !errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	for _, formatted := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), fmt.Sprintf("%q", err)} {
		if strings.Contains(formatted, "SECRET") || strings.Contains(formatted, "/rest/") {
			t.Fatalf("private cause escaped: %q", formatted)
		}
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("single-cause unwrap exposed an internal cause: %#v", errors.Unwrap(err))
	}
	var escaped *privateJiraEditCause
	if errors.As(err, &escaped) {
		t.Fatalf("private cause escaped through errors.As: %#v", escaped)
	}
	var statusErr interface{ HTTPStatus() int }
	if !errors.Is(err, domain.ErrForbidden) || !errors.As(err, &statusErr) || statusErr.HTTPStatus() != 503 {
		t.Fatalf("safe classifications were lost: err=%v status=%#v", err, statusErr)
	}
}

func TestEditDescriptionGuardedOutcomeUnknownContours(t *testing.T) {
	preview := previewDescriptionEdit(t, "OLD", "NEW", false)
	tests := []struct {
		name     string
		reads    []*domain.Issue
		readErrs []error
	}{
		{name: "readback failure", reads: []*domain.Issue{editIssue("a OLD b", editUpdatedBefore), editIssue("a OLD b", editUpdatedBefore)}, readErrs: []error{nil, nil, errors.New("private readback")}},
		{name: "desired body without advancing updated", reads: []*domain.Issue{editIssue("a OLD b", editUpdatedBefore), editIssue("a OLD b", editUpdatedBefore), editIssue("a NEW b", editUpdatedBefore)}},
		{name: "conflicting advancing body", reads: []*domain.Issue{editIssue("a OLD b", editUpdatedBefore), editIssue("a OLD b", editUpdatedBefore), editIssue("a OTHER b", editUpdatedAfter)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker := &guardedEditTracker{recordingTracker: &recordingTracker{}, reads: test.reads, readErrs: test.readErrs}
			result, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).EditDescriptionGuarded(t.Context(), "PROJ-1", JiraDescriptionEditOpts{
				Old: []byte("OLD"), New: []byte("NEW"), Apply: true, ExpectedProposalHash: preview.ProposalHash,
			})
			var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
			if result.Status != "outcome_unknown" || tracker.updateCalls != 1 || !result.WriteAttempted || !errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() {
				t.Fatalf("result=%#v err=%v writes=%d", result, err, tracker.updateCalls)
			}
		})
	}
}

func TestEditDescriptionGuardedMovedIdentityAndHashMismatchBlock(t *testing.T) {
	preview := previewDescriptionEdit(t, "OLD", "NEW", false)
	moved := editIssue("a OLD b", editUpdatedBefore)
	moved.Key = "PROJ-2"
	tests := []struct {
		name       string
		reads      []*domain.Issue
		expected   string
		wantStatus string
	}{
		{name: "prewrite moved", reads: []*domain.Issue{editIssue("a OLD b", editUpdatedBefore), moved}, expected: preview.ProposalHash, wantStatus: "blocked"},
		{name: "readback moved", reads: []*domain.Issue{editIssue("a OLD b", editUpdatedBefore), editIssue("a OLD b", editUpdatedBefore), moved}, expected: preview.ProposalHash, wantStatus: "outcome_unknown"},
		{name: "expected hash mismatch", reads: []*domain.Issue{editIssue("a OLD b", editUpdatedBefore)}, expected: strings.Repeat("0", 64), wantStatus: "blocked"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker := &guardedEditTracker{recordingTracker: &recordingTracker{}, reads: test.reads}
			result, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).EditDescriptionGuarded(t.Context(), "PROJ-1", JiraDescriptionEditOpts{
				Old: []byte("OLD"), New: []byte("NEW"), Apply: true, ExpectedProposalHash: test.expected,
			})
			wantWrites := 0
			if test.name == "readback moved" {
				wantWrites = 1
			}
			if result.Status != test.wantStatus || tracker.updateCalls != wantWrites || !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("result=%#v err=%v writes=%d", result, err, tracker.updateCalls)
			}
		})
	}
}

func TestEditDescriptionGuardedEvidenceRefusals(t *testing.T) {
	tests := []struct {
		name  string
		issue *domain.Issue
	}{
		{"missing id", &domain.Issue{Key: "PROJ-1", Fields: map[string]any{"description": "x", "updated": editUpdatedBefore}, Raw: map[string]any{"description": "x"}}},
		{"moved key", &domain.Issue{ID: "10007", Key: "PROJ-2", Fields: map[string]any{"description": "x", "updated": editUpdatedBefore}, Raw: map[string]any{"description": "x"}}},
		{"missing description", &domain.Issue{ID: "10007", Key: "PROJ-1", Fields: map[string]any{"updated": editUpdatedBefore}, Raw: map[string]any{}}},
		{"structured description", editIssue(map[string]any{"type": "doc"}, editUpdatedBefore)},
		{"malformed updated", editIssue("x", "yesterday")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker := &guardedEditTracker{recordingTracker: &recordingTracker{}, reads: []*domain.Issue{test.issue}}
			_, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).EditDescriptionGuarded(t.Context(), "PROJ-1", JiraDescriptionEditOpts{Old: []byte("x"), New: []byte("y")})
			if !errors.Is(err, domain.ErrCheckFailed) || tracker.updateCalls != 0 {
				t.Fatalf("err=%v writes=%d", err, tracker.updateCalls)
			}
		})
	}
}

func TestEditDescriptionGuardedNullDescriptionIsEmptyBytes(t *testing.T) {
	tracker := &guardedEditTracker{recordingTracker: &recordingTracker{}, reads: []*domain.Issue{editIssue(nil, editUpdatedBefore)}}
	_, err := (&JiraService{tr: tracker, baseURL: jiraMirrorTestBackendURL}).EditDescriptionGuarded(t.Context(), "PROJ-1", JiraDescriptionEditOpts{Old: []byte("x"), New: []byte("y")})
	if !errors.Is(err, domain.ErrNotFound) || tracker.updateCalls != 0 {
		t.Fatalf("err=%v writes=%d", err, tracker.updateCalls)
	}
}
