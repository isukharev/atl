package app

import (
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type guardedCreateDiagnosticTestError struct {
	status   int
	evidence domain.JiraGuardedCreateRejectionEvidence
}

func (e guardedCreateDiagnosticTestError) Error() string   { return "safe refusal" }
func (e guardedCreateDiagnosticTestError) Unwrap() error   { return domain.ErrUsage }
func (e guardedCreateDiagnosticTestError) HTTPStatus() int { return e.status }
func (e guardedCreateDiagnosticTestError) DiagnosticJiraGuardedCreateRejection() domain.JiraGuardedCreateRejectionEvidence {
	return e.evidence
}

func TestGuardedCreateRejectionRevalidatesAdapterEvidence(t *testing.T) {
	metadata := &domain.JiraQualifiedCreateMetadata{Fields: []domain.JiraQualifiedCreateField{
		{FieldID: "customfield_1"}, {FieldID: "reporter"},
	}}
	valid := domain.JiraGuardedCreateRejectionEvidence{
		HTTPStatus: http.StatusBadRequest, DetailsStatus: domain.JiraGuardedCreateRejectionAvailable,
		FieldIDs: []string{"reporter", "customfield_99", "customfield_1"}, GlobalErrorCount: 1,
	}
	result := guardedCreateRejection(guardedCreateDiagnosticTestError{status: http.StatusBadRequest, evidence: valid}, metadata)
	if result == nil || result.DetailsStatus != domain.JiraGuardedCreateRejectionAvailable ||
		!reflect.DeepEqual(result.FieldErrors, []JiraGuardedCreateFieldRejection{
			{FieldID: "customfield_1", Code: domain.JiraGuardedCreateFieldRejected},
			{FieldID: "reporter", Code: domain.JiraGuardedCreateFieldRejected},
		}) || result.GlobalErrorCount != 1 || result.OmittedFieldErrorCount != 1 {
		t.Fatalf("result=%+v", result)
	}

	for name, evidence := range map[string]domain.JiraGuardedCreateRejectionEvidence{
		"status mismatch": {
			HTTPStatus: http.StatusForbidden, DetailsStatus: domain.JiraGuardedCreateRejectionAvailable,
		},
		"duplicate field": {
			HTTPStatus: http.StatusBadRequest, DetailsStatus: domain.JiraGuardedCreateRejectionAvailable,
			FieldIDs: []string{"reporter", "reporter"},
		},
		"unsafe field": {
			HTTPStatus: http.StatusBadRequest, DetailsStatus: domain.JiraGuardedCreateRejectionAvailable,
			FieldIDs: []string{"Display Name"},
		},
		"invalid counts": {
			HTTPStatus: http.StatusBadRequest, DetailsStatus: domain.JiraGuardedCreateRejectionAvailable,
			GlobalErrorCount: -1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := guardedCreateRejection(guardedCreateDiagnosticTestError{status: http.StatusBadRequest, evidence: evidence}, metadata)
			if result == nil || result.DetailsStatus != domain.JiraGuardedCreateRejectionUnavailable ||
				len(result.FieldErrors) != 0 || result.GlobalErrorCount != 0 || result.OmittedFieldErrorCount != 0 {
				t.Fatalf("result=%+v", result)
			}
		})
	}

	if result := guardedCreateRejection(errors.New("transport failure"), metadata); result != nil {
		t.Fatalf("transport error produced rejection=%+v", result)
	}
}
