package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type fieldSetTracker struct {
	domain.Tracker
	issue          *domain.Issue
	defs           []domain.FieldDef
	getFields      []string
	setKey         string
	setFields      map[string]any
	setCalls       int
	setError       error
	getCalls       int
	fieldCalls     int
	reconcileError error
	commitOnError  bool
}

type fieldSetHTTPError int

func (e fieldSetHTTPError) Error() string   { return "HTTP write rejected" }
func (e fieldSetHTTPError) HTTPStatus() int { return int(e) }

type leakyFieldSetHTTPError struct{}

func (leakyFieldSetHTTPError) Error() string   { return "HTTP 403 echoed super-secret-field-body" }
func (leakyFieldSetHTTPError) HTTPStatus() int { return 403 }
func (leakyFieldSetHTTPError) Unwrap() error   { return domain.ErrForbidden }

func (t *fieldSetTracker) Fields(context.Context) ([]domain.FieldDef, error) {
	t.fieldCalls++
	return t.defs, nil
}

func (t *fieldSetTracker) GetIssue(_ context.Context, _ string, fields []string) (*domain.Issue, error) {
	t.getCalls++
	if t.getCalls > 1 && t.reconcileError != nil {
		return nil, t.reconcileError
	}
	t.getFields = append([]string(nil), fields...)
	return t.issue, nil
}

func (t *fieldSetTracker) SetFields(_ context.Context, key string, fields map[string]any) error {
	t.setCalls++
	t.setKey, t.setFields = key, fields
	if t.setError != nil && t.commitOnError {
		for field, value := range fields {
			t.issue.Fields[field] = value
		}
		t.issue.Fields["updated"] = "2026-07-10T10:01:00.000+0000"
	}
	return t.setError
}

func fieldSetFixture() *fieldSetTracker {
	return &fieldSetTracker{
		issue: &domain.Issue{Key: "PROJ-1", Fields: map[string]any{
			"updated":       "2026-07-10T10:00:00.000+0000",
			"customfield_1": "old",
			"customfield_2": map[string]any{"id": "1", "name": "existing"},
		}},
		defs: []domain.FieldDef{
			{ID: "customfield_1", Name: "Notes", Custom: true, Schema: "string"},
			{ID: "customfield_2", Name: "Choice", Custom: true, Schema: "option"},
			{ID: "summary", Name: "Summary", Custom: false, Schema: "string"},
		},
	}
}

func fieldSetApplyOpts(t *testing.T, allow []string, updated string, proposals []JiraFieldProposal) JiraFieldSetOpts {
	t.Helper()
	previews, _, err := normalizeFieldProposals(proposals, exactAllowSet(allow))
	if err != nil {
		t.Fatalf("normalize proposals: %v", err)
	}
	hash, err := jiraFieldProposalHash("PROJ-1", previews)
	if err != nil {
		t.Fatalf("proposal hash: %v", err)
	}
	return JiraFieldSetOpts{
		AllowFields: allow, Proposals: proposals, ExpectedUpdated: updated,
		ExpectedProposalHash: hash, Apply: true,
	}
}

func TestSetFieldsGuardedDryRunCapturesUpdatedAndNormalizesValues(t *testing.T) {
	tr := fieldSetFixture()
	svc := &JiraService{tr: tr}
	res, err := svc.SetFieldsGuarded(context.Background(), "PROJ-1", JiraFieldSetOpts{
		AllowFields: []string{"customfield_1", "customfield_2"},
		Proposals: []JiraFieldProposal{
			{Field: "customfield_2", Source: "raw", Value: map[string]any{"id": "2"}},
			{Field: "customfield_1", Source: "markdown", Value: "h2. Progress"},
		},
	})
	if err != nil {
		t.Fatalf("SetFieldsGuarded: %v", err)
	}
	if res.Status != "would_apply" || res.Mode != "dry-run" || res.ExpectedUpdated != tr.issue.Fields["updated"] {
		t.Fatalf("result = %+v", res)
	}
	if len(res.ProposalHash) != 64 {
		t.Fatalf("proposal hash = %q", res.ProposalHash)
	}
	if tr.setCalls != 0 {
		t.Fatalf("dry-run made %d writes", tr.setCalls)
	}
	wantGet := []string{"customfield_1", "customfield_2", "updated"}
	if !reflect.DeepEqual(tr.getFields, wantGet) {
		t.Fatalf("GetIssue fields = %v, want %v", tr.getFields, wantGet)
	}
	if len(res.Fields) != 2 || res.Fields[0].Field != "customfield_1" || res.Fields[0].Kind != "string" || res.Fields[1].Kind != "object" {
		t.Fatalf("normalized previews = %+v", res.Fields)
	}
}

func TestJiraFieldProposalHashIsOrderIndependentAndValueBound(t *testing.T) {
	allow := exactAllowSet([]string{"customfield_1", "customfield_2"})
	a := []JiraFieldProposal{
		{Field: "customfield_2", Source: "raw", Value: map[string]any{"id": "2"}},
		{Field: "customfield_1", Source: "markdown", Value: "value"},
	}
	b := []JiraFieldProposal{a[1], a[0]}
	previewA, _, err := normalizeFieldProposals(a, allow)
	if err != nil {
		t.Fatal(err)
	}
	previewB, _, err := normalizeFieldProposals(b, allow)
	if err != nil {
		t.Fatal(err)
	}
	hashA, _ := jiraFieldProposalHash("ENG-1", previewA)
	hashB, _ := jiraFieldProposalHash("ENG-1", previewB)
	if hashA != hashB {
		t.Fatalf("input order changed hash: %s != %s", hashA, hashB)
	}
	previewB[0].Value = "changed"
	changed, _ := jiraFieldProposalHash("ENG-1", previewB)
	if changed == hashA {
		t.Fatal("changed normalized value kept proposal hash")
	}
	otherKey, _ := jiraFieldProposalHash("ENG-2", previewA)
	if otherKey == hashA {
		t.Fatal("proposal hash was reusable for another issue key")
	}
}

func TestSetFieldsGuardedApplyRequiresMatchingProposalHashBeforeNetwork(t *testing.T) {
	proposals := []JiraFieldProposal{{Field: "customfield_1", Source: "raw", Value: "new"}}

	t.Run("missing", func(t *testing.T) {
		tr := fieldSetFixture()
		_, err := (&JiraService{tr: tr}).SetFieldsGuarded(context.Background(), "PROJ-1", JiraFieldSetOpts{
			AllowFields: []string{"customfield_1"}, Proposals: proposals,
			ExpectedUpdated: "fresh", Apply: true,
		})
		if !errors.Is(err, domain.ErrUsage) || tr.fieldCalls != 0 || tr.getCalls != 0 || tr.setCalls != 0 {
			t.Fatalf("err=%v calls fields/get/set=%d/%d/%d", err, tr.fieldCalls, tr.getCalls, tr.setCalls)
		}
	})

	t.Run("changed", func(t *testing.T) {
		tr := fieldSetFixture()
		res, err := (&JiraService{tr: tr}).SetFieldsGuarded(context.Background(), "PROJ-1", JiraFieldSetOpts{
			AllowFields: []string{"customfield_1"}, Proposals: proposals,
			ExpectedUpdated: "fresh", ExpectedProposalHash: strings.Repeat("0", 64), Apply: true,
		})
		if !errors.Is(err, domain.ErrCheckFailed) || res == nil || res.Status != "blocked" || len(res.ProposalHash) != 64 {
			t.Fatalf("result=%+v err=%v", res, err)
		}
		if tr.fieldCalls != 0 || tr.getCalls != 0 || tr.setCalls != 0 {
			t.Fatalf("mismatch reached network: fields/get/set=%d/%d/%d", tr.fieldCalls, tr.getCalls, tr.setCalls)
		}
	})
}

func TestSetFieldsGuardedStaleApplyFailsClosed(t *testing.T) {
	tr := fieldSetFixture()
	res, err := (&JiraService{tr: tr}).SetFieldsGuarded(context.Background(), "PROJ-1", fieldSetApplyOpts(t,
		[]string{"customfield_1"}, "older",
		[]JiraFieldProposal{{Field: "customfield_1", Source: "raw", Value: "new"}},
	))
	if !errors.Is(err, domain.ErrCheckFailed) || res == nil || res.Status != "blocked" {
		t.Fatalf("result=%+v err=%v, want blocked ErrCheckFailed", res, err)
	}
	if tr.setCalls != 0 {
		t.Fatalf("stale apply made %d writes", tr.setCalls)
	}
}

func TestSetFieldsGuardedApplyWritesTypedValuesAtomically(t *testing.T) {
	tr := fieldSetFixture()
	updated := tr.issue.Fields["updated"].(string)
	res, err := (&JiraService{tr: tr}).SetFieldsGuarded(context.Background(), "PROJ-1", fieldSetApplyOpts(t,
		[]string{"customfield_1", "customfield_2"}, updated,
		[]JiraFieldProposal{
			{Field: "customfield_1", Source: "markdown", Value: "{}"},
			{Field: "customfield_2", Source: "raw", Value: map[string]any{"id": "2"}},
		},
	))
	if err != nil || res.Status != "applied" {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if tr.setCalls != 1 || tr.setKey != "PROJ-1" {
		t.Fatalf("write calls=%d key=%q", tr.setCalls, tr.setKey)
	}
	if got, ok := tr.setFields["customfield_1"].(string); !ok || got != "{}" {
		t.Fatalf("Markdown string was retyped: %#v", tr.setFields["customfield_1"])
	}
	if got, ok := tr.setFields["customfield_2"].(map[string]any); !ok || got["id"] != "2" {
		t.Fatalf("object proposal was flattened: %#v", tr.setFields["customfield_2"])
	}
}

func TestSetFieldsGuardedAlreadySatisfiedStillRequiresFreshReview(t *testing.T) {
	tr := fieldSetFixture()
	res, err := (&JiraService{tr: tr}).SetFieldsGuarded(context.Background(), "PROJ-1", fieldSetApplyOpts(t,
		[]string{"customfield_2"}, "older",
		[]JiraFieldProposal{{Field: "customfield_2", Source: "raw", Value: map[string]any{"id": "1"}}},
	))
	if !errors.Is(err, domain.ErrCheckFailed) || res.Status != "blocked" || tr.setCalls != 0 {
		t.Fatalf("result=%+v writes=%d err=%v", res, tr.setCalls, err)
	}
}

func TestJiraFieldProposalStringEqualityIsExact(t *testing.T) {
	for _, current := range []any{" value ", "value\n", []any{"value"}, nil} {
		if jiraFieldProposalEqual(current, "value") {
			t.Errorf("current %#v must not equal exact desired string", current)
		}
	}
	if !jiraFieldProposalEqual("value", "value") {
		t.Error("identical strings should be equal")
	}
	current := map[string]any{"large": json.Number("9007199254740993")}
	adjacent := map[string]any{"large": json.Number("9007199254740992")}
	if jiraFieldProposalEqual(current, adjacent) {
		t.Error("adjacent integers above 2^53 must not compare equal")
	}
}

func TestSetFieldsGuardedEnforcesAllowlistAndCustomMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		allow []string
	}{
		{name: "not allowed", field: "customfield_1", allow: []string{"customfield_2"}},
		{name: "system field", field: "summary", allow: []string{"summary"}},
		{name: "unknown field", field: "customfield_999", allow: []string{"customfield_999"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := fieldSetFixture()
			_, err := (&JiraService{tr: tr}).SetFieldsGuarded(context.Background(), "PROJ-1", JiraFieldSetOpts{
				AllowFields: tc.allow,
				Proposals:   []JiraFieldProposal{{Field: tc.field, Source: "raw", Value: "x"}},
			})
			if !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("err=%v, want ErrUsage", err)
			}
		})
	}
}

func TestSetFieldsGuardedRejectsStructuredMarkdownAtAppBoundary(t *testing.T) {
	tr := fieldSetFixture()
	_, err := (&JiraService{tr: tr}).SetFieldsGuarded(context.Background(), "PROJ-1", JiraFieldSetOpts{
		AllowFields: []string{"customfield_1"},
		Proposals: []JiraFieldProposal{{
			Field: "customfield_1", Source: "markdown", Value: map[string]any{"id": "1"},
		}},
	})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("err=%v, want ErrUsage", err)
	}
}

func TestNormalizeFieldProposalsEnforcesNormalizedAggregateCap(t *testing.T) {
	_, _, err := normalizeFieldProposalsWithLimit([]JiraFieldProposal{
		{Field: "customfield_1", Source: "markdown", Value: "1234"},
	}, map[string]bool{"customfield_1": true}, 3)
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("err=%v, want ErrUsage", err)
	}
}

func TestSetFieldsGuardedReconcilesAmbiguousWrite(t *testing.T) {
	tr := fieldSetFixture()
	tr.setError = errors.New("connection closed")
	tr.commitOnError = true
	updated := tr.issue.Fields["updated"].(string)
	res, err := (&JiraService{tr: tr}).SetFieldsGuarded(context.Background(), "PROJ-1", fieldSetApplyOpts(t,
		[]string{"customfield_1"}, updated,
		[]JiraFieldProposal{{Field: "customfield_1", Source: "raw", Value: "new"}},
	))
	if err != nil || res.Status != "applied" || !res.Reconciled || tr.getCalls != 2 {
		t.Fatalf("result=%+v getCalls=%d err=%v", res, tr.getCalls, err)
	}
}

func TestSetFieldsGuardedDefinitiveRejectionWithConcurrentDesiredStateIsSatisfied(t *testing.T) {
	tr := fieldSetFixture()
	tr.setError = fieldSetHTTPError(400)
	tr.commitOnError = true // models another actor producing the desired end state
	updated := tr.issue.Fields["updated"].(string)
	res, err := (&JiraService{tr: tr}).SetFieldsGuarded(context.Background(), "PROJ-1", fieldSetApplyOpts(t,
		[]string{"customfield_1"}, updated,
		[]JiraFieldProposal{{Field: "customfield_1", Source: "raw", Value: "new"}},
	))
	if err != nil || res.Status != "already_satisfied" || !res.Reconciled {
		t.Fatalf("result=%+v err=%v", res, err)
	}
}

func TestSetFieldsGuardedClassifiesRejectedAndUnreconciledWrites(t *testing.T) {
	for _, tc := range []struct {
		name           string
		writeErr       error
		reconcileErr   error
		wantStatus     string
		wantReconciled bool
	}{
		{name: "definitive rejection", writeErr: fieldSetHTTPError(400), wantStatus: "failed", wantReconciled: true},
		{name: "definitive rejection without fresh read", writeErr: fieldSetHTTPError(403), reconcileErr: errors.New("read unavailable"), wantStatus: "failed"},
		{name: "ambiguous write still not visible", writeErr: errors.New("connection closed"), wantStatus: "unknown", wantReconciled: true},
		{name: "fresh read unavailable", writeErr: errors.New("connection closed"), reconcileErr: errors.New("read unavailable"), wantStatus: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := fieldSetFixture()
			tr.setError = tc.writeErr
			tr.reconcileError = tc.reconcileErr
			updated := tr.issue.Fields["updated"].(string)
			res, err := (&JiraService{tr: tr}).SetFieldsGuarded(context.Background(), "PROJ-1", fieldSetApplyOpts(t,
				[]string{"customfield_1"}, updated,
				[]JiraFieldProposal{{Field: "customfield_1", Source: "raw", Value: "new"}},
			))
			if err == nil || res.Status != tc.wantStatus || res.Reconciled != tc.wantReconciled || tr.setCalls != 1 {
				t.Fatalf("result=%+v err=%v", res, err)
			}
		})
	}
}

func TestSetFieldsGuardedSanitizesBackendWriteBodyAndPreservesClassification(t *testing.T) {
	tr := fieldSetFixture()
	tr.setError = leakyFieldSetHTTPError{}
	updated := tr.issue.Fields["updated"].(string)
	_, err := (&JiraService{tr: tr}).SetFieldsGuarded(context.Background(), "PROJ-1", fieldSetApplyOpts(t,
		[]string{"customfield_1"}, updated,
		[]JiraFieldProposal{{Field: "customfield_1", Source: "raw", Value: "new"}},
	))
	if err == nil || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("write error was not sanitized: %v", err)
	}
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("sanitized error lost sentinel: %v", err)
	}
}
