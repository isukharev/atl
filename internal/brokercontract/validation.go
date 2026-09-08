package brokercontract

import (
	"encoding/base64"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

func validIdentifier(value string) bool {
	if value == "" || len(value) > domain.BrokerMaxIdentifierBytes || !utf8.ValidString(value) {
		return false
	}
	for _, current := range value {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != domain.BrokerMaxDigestBytes {
		return false
	}
	for _, current := range value {
		if current < '0' || current > '9' {
			if current < 'a' || current > 'f' {
				return false
			}
		}
	}
	return true
}

func validService(value string) bool { return value == "jira" || value == "confluence" }

func validateContext(value domain.BrokerVerifiedContext) error {
	if !validIdentifier(value.PrincipalID) || !validIdentifier(value.WorkloadID) || !validIdentifier(value.ExecutionID) ||
		!validIdentifier(value.ExecutionEpoch) || !validIdentifier(value.BrokerID) || !validIdentifier(value.AuthorityRevision) ||
		value.Audience == "" || len(value.Audience) > domain.BrokerMaxAudienceBytes || !validIdentifier(value.Audience) ||
		!validService(value.Backend.Service) || !validDigest(value.Backend.OriginSHA256) || !validIdentifier(value.Backend.WorkloadBackendID) ||
		value.ExecutionNotBeforeMillis <= 0 || value.ExecutionExpiresMillis <= value.ExecutionNotBeforeMillis ||
		value.GrantExpiresMillis <= value.ExecutionNotBeforeMillis || value.CredentialExpiresMillis <= value.ExecutionNotBeforeMillis {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func validateRequest(value domain.BrokerRequest) error {
	if value.SchemaVersion != SchemaVersion || value.OperationVersion != OperationVersion || !validIdentifier(value.RequestID) ||
		!validIdentifier(value.Expect.ExecutionID) || !validIdentifier(value.Expect.ExecutionEpoch) || !validIdentifier(value.Expect.AuthorityRevision) {
		return reject(domain.BrokerReasonMalformed)
	}
	definition, ok := Definition(value.Operation, value.OperationVersion)
	if !ok {
		return reject(domain.BrokerReasonUnsupported)
	}
	if definition.Limits.MaxOperationMillis > domain.BrokerMaxOperationMillis || definition.Limits.MaxDecisionLeaseMillis > domain.BrokerMaxDecisionLeaseMillis {
		return reject(domain.BrokerReasonMalformed)
	}
	if value.Features == nil || !slices.Equal(value.Features, definition.RequiredFeatures) {
		return reject(domain.BrokerReasonUnsupported)
	}
	return validateArguments(value.Operation, value.Arguments)
}

func operationBackendMatches(definition domain.BrokerOperationDefinition, context domain.BrokerVerifiedContext) bool {
	return definition.BackendService == context.Backend.Service
}

func validateArguments(operation domain.BrokerOperationID, value domain.BrokerOperationArguments) error {
	present := 0
	for _, on := range []bool{value.JiraIssueRead != nil, value.ConfluencePageRead != nil, value.JiraComment != nil, value.Outcome != nil} {
		if on {
			present++
		}
	}
	if present != 1 {
		return reject(domain.BrokerReasonMalformed)
	}
	switch operation {
	case domain.BrokerOperationJiraIssueRead:
		if value.JiraIssueRead == nil || !domain.ValidJiraIssueKey(value.JiraIssueRead.IssueKey) || !validIssueFields(value.JiraIssueRead.Fields) {
			return reject(domain.BrokerReasonMalformed)
		}
	case domain.BrokerOperationConfluencePageRead:
		if value.ConfluencePageRead == nil || !domain.ValidConfluenceContentID(value.ConfluencePageRead.PageID) ||
			value.ConfluencePageRead.Projection != domain.BrokerConfluenceProjectionMetadata && value.ConfluencePageRead.Projection != domain.BrokerConfluenceProjectionStorage {
			return reject(domain.BrokerReasonMalformed)
		}
	case domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationJiraCommentApply:
		comment := value.JiraComment
		if comment == nil || !domain.ValidJiraIssueKey(comment.IssueKey) || len(comment.NativeBody) == 0 || int64(len(comment.NativeBody)) > MaxJiraCommentBodyBytes || !utf8.Valid(comment.NativeBody) || comment.SatisfactionPolicy != "append_always" {
			return reject(domain.BrokerReasonMalformed)
		}
		if operation == domain.BrokerOperationJiraCommentPreview {
			if comment.ExpectedProposalHash != "" || comment.OperationTicket != "" {
				return reject(domain.BrokerReasonMalformed)
			}
		} else if !validDigest(comment.ExpectedProposalHash) || !validIdentifier(comment.OperationTicket) {
			return reject(domain.BrokerReasonMalformed)
		}
	case domain.BrokerOperationOutcomeLookup:
		if value.Outcome == nil || !validIdentifier(value.Outcome.OperationTicket) {
			return reject(domain.BrokerReasonMalformed)
		}
	default:
		return reject(domain.BrokerReasonUnsupported)
	}
	return nil
}

func validIssueFields(fields []domain.BrokerJiraIssueField) bool {
	if len(fields) == 0 || len(fields) > 3 {
		return false
	}
	seen := map[domain.BrokerJiraIssueField]bool{}
	for _, field := range fields {
		if field != domain.BrokerJiraIssueFieldSummary && field != domain.BrokerJiraIssueFieldDescription && field != domain.BrokerJiraIssueFieldUpdated || seen[field] {
			return false
		}
		seen[field] = true
	}
	return true
}

func validDecisionStatus(status domain.BrokerDecisionStatus, reason domain.BrokerReason) bool {
	switch status {
	case domain.BrokerDecisionAllowed:
		return reason == ""
	case domain.BrokerDecisionDenied:
		return reason == domain.BrokerReasonDenied || reason == domain.BrokerReasonRevoked || reason == domain.BrokerReasonGrantExpired || reason == domain.BrokerReasonCredentialExpired || reason == domain.BrokerReasonStaleExecution || reason == domain.BrokerReasonStaleAuthority || reason == domain.BrokerReasonUnsupportedConsistency || reason == domain.BrokerReasonProposalClearanceRequired
	case domain.BrokerDecisionUnavailable:
		return reason == domain.BrokerReasonAuthorizationUnavailable || reason == domain.BrokerReasonDecisionExpired
	}
	return false
}

func validBase64(value string, maximum int64) ([]byte, bool) {
	if value == "" || int64(len(value)) > int64(base64.StdEncoding.EncodedLen(int(maximum))) {
		return nil, false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	return decoded, err == nil && int64(len(decoded)) <= maximum && base64.StdEncoding.EncodeToString(decoded) == value
}

func copyStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

func wireStrings(values []string) []string {
	return append([]string{}, values...)
}

func validSchemaID(value string) bool {
	return validIdentifier(value) && strings.HasPrefix(value, "#/$defs/")
}
