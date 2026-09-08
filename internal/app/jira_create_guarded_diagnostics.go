package app

import (
	"errors"
	"sort"

	"github.com/isukharev/atl/internal/domain"
)

const (
	guardedCreateCheckProjectInventoryUnavailable     = "project_inventory_unavailable"
	guardedCreateCheckProjectInventoryIncomplete      = "project_inventory_incomplete"
	guardedCreateCheckProjectSelectorAmbiguous        = "project_selector_ambiguous"
	guardedCreateCheckProjectNotFound                 = "project_not_found"
	guardedCreateCheckProjectArchived                 = "project_archived"
	guardedCreateCheckMetadataUnavailable             = "metadata_unavailable"
	guardedCreateCheckMetadataIncomplete              = "metadata_incomplete"
	guardedCreateCheckSchemaInvalid                   = "schema_invalid"
	guardedCreateCheckRequiredFieldOmitted            = "required_field_omitted"
	guardedCreateCheckFieldNotOnScreen                = "field_not_on_screen"
	guardedCreateCheckPreparationFailed               = "preparation_failed"
	guardedCreateCheckPreparationInvalid              = "preparation_invalid"
	guardedCreateCheckBackendIdentityInvalid          = "backend_identity_invalid"
	guardedCreateCheckRegistrationQualificationFailed = "registration_qualification_failed"
	guardedCreateCheckReadbackProjectionInvalid       = "readback_projection_invalid"
	guardedCreateCheckProposalChanged                 = "proposal_changed"
	guardedCreateCheckDeadlineExceeded                = "deadline_exceeded"
)

type jiraGuardedCreateCheckError struct {
	code    string
	fieldID string
	cause   error
}

func (e *jiraGuardedCreateCheckError) Error() string {
	return "guarded Jira create check failed: " + e.code
}
func (e *jiraGuardedCreateCheckError) Unwrap() error { return e.cause }

func guardedCreateCheckFailure(code, fieldID string, cause error) error {
	if !validGuardedCreateCheckCode(code) {
		code = guardedCreateCheckPreparationInvalid
	}
	if !domain.ValidJiraTechnicalFieldID(fieldID) {
		fieldID = ""
	}
	return &jiraGuardedCreateCheckError{code: code, fieldID: fieldID, cause: cause}
}

func setGuardedCreateCheck(result *JiraGuardedCreateResult, err error, fallback string) {
	if result == nil {
		return
	}
	check := JiraGuardedCreateCheck{Code: fallback}
	var diagnostic *jiraGuardedCreateCheckError
	if errors.As(err, &diagnostic) {
		check.Code = diagnostic.code
		check.FieldID = diagnostic.fieldID
	}
	if !validGuardedCreateCheckCode(check.Code) {
		return
	}
	result.Check = &check
}

func validGuardedCreateCheckCode(code string) bool {
	switch code {
	case guardedCreateCheckProjectInventoryUnavailable,
		guardedCreateCheckProjectInventoryIncomplete,
		guardedCreateCheckProjectSelectorAmbiguous,
		guardedCreateCheckProjectNotFound,
		guardedCreateCheckProjectArchived,
		guardedCreateCheckMetadataUnavailable,
		guardedCreateCheckMetadataIncomplete,
		guardedCreateCheckSchemaInvalid,
		guardedCreateCheckRequiredFieldOmitted,
		guardedCreateCheckFieldNotOnScreen,
		guardedCreateCheckPreparationFailed,
		guardedCreateCheckPreparationInvalid,
		guardedCreateCheckBackendIdentityInvalid,
		guardedCreateCheckRegistrationQualificationFailed,
		guardedCreateCheckReadbackProjectionInvalid,
		guardedCreateCheckProposalChanged,
		guardedCreateCheckDeadlineExceeded:
		return true
	default:
		return false
	}
}

func guardedCreateRejection(err error, metadata *domain.JiraQualifiedCreateMetadata) *JiraGuardedCreateRejection {
	var statusError interface{ HTTPStatus() int }
	if !errors.As(err, &statusError) {
		return nil
	}
	status := statusError.HTTPStatus()
	result := &JiraGuardedCreateRejection{
		HTTPStatus: status, DetailsStatus: domain.JiraGuardedCreateRejectionUnavailable,
		FieldErrors: []JiraGuardedCreateFieldRejection{},
	}
	var diagnostic interface {
		DiagnosticJiraGuardedCreateRejection() domain.JiraGuardedCreateRejectionEvidence
	}
	if !errors.As(err, &diagnostic) {
		return result
	}
	evidence := diagnostic.DiagnosticJiraGuardedCreateRejection()
	if evidence.HTTPStatus != status || evidence.GlobalErrorCount < 0 ||
		evidence.OmittedFieldErrorCount < 0 ||
		evidence.GlobalErrorCount+evidence.OmittedFieldErrorCount+len(evidence.FieldIDs) > domain.JiraGuardedCreateRejectionMaxDetails {
		return result
	}
	if evidence.DetailsStatus == domain.JiraGuardedCreateRejectionOmittedBounds {
		result.DetailsStatus = evidence.DetailsStatus
		return result
	}
	if evidence.DetailsStatus != domain.JiraGuardedCreateRejectionAvailable {
		return result
	}
	seen := map[string]bool{}
	for _, fieldID := range evidence.FieldIDs {
		if !domain.ValidJiraTechnicalFieldID(fieldID) || seen[fieldID] {
			return result
		}
		seen[fieldID] = true
	}
	qualified := map[string]bool{}
	if metadata != nil {
		for _, field := range metadata.Fields {
			if domain.ValidJiraTechnicalFieldID(field.FieldID) {
				qualified[field.FieldID] = true
			}
		}
	}
	result.OmittedFieldErrorCount = evidence.OmittedFieldErrorCount
	for _, fieldID := range evidence.FieldIDs {
		if !qualified[fieldID] {
			result.OmittedFieldErrorCount++
			continue
		}
		result.FieldErrors = append(result.FieldErrors, JiraGuardedCreateFieldRejection{
			FieldID: fieldID, Code: domain.JiraGuardedCreateFieldRejected,
		})
	}
	sort.Slice(result.FieldErrors, func(a, b int) bool {
		return result.FieldErrors[a].FieldID < result.FieldErrors[b].FieldID
	})
	result.DetailsStatus = evidence.DetailsStatus
	result.GlobalErrorCount = evidence.GlobalErrorCount
	return result
}
