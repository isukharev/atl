package brokercontract

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

func EncodeJiraIssueReadResultV1(value domain.BrokerJiraIssueReadResult) ([]byte, error) {
	value.Fields = sortedJiraReadResultFields(value.Fields)
	if err := validateJiraIssueReadResult(value); err != nil {
		return nil, err
	}
	fields := make([]jiraIssueReadFieldWire, len(value.Fields))
	for index, field := range value.Fields {
		fields[index] = jiraIssueReadFieldWire{Field: string(field.Field), Present: field.Present, Null: field.Null, Value: field.Value}
	}
	return marshalReadResult(jiraIssueReadResultWire{SchemaVersion: SchemaVersion, ArgumentsSHA256: value.ArgumentsSHA256, IssueID: value.IssueID, Key: value.Key, Project: value.Project, Updated: value.Updated, Fields: fields, Complete: value.Complete})
}

func DecodeJiraIssueReadResultV1(data []byte) (domain.BrokerJiraIssueReadResult, error) {
	var wire jiraIssueReadResultWire
	if err := strictDecode(data, MaxReadResultWireBytes, &wire); err != nil {
		return domain.BrokerJiraIssueReadResult{}, err
	}
	fields := make([]domain.BrokerJiraIssueReadField, len(wire.Fields))
	for index, field := range wire.Fields {
		fields[index] = domain.BrokerJiraIssueReadField{Field: domain.BrokerJiraIssueField(field.Field), Present: field.Present, Null: field.Null, Value: field.Value}
	}
	value := domain.BrokerJiraIssueReadResult{SchemaVersion: wire.SchemaVersion, ArgumentsSHA256: wire.ArgumentsSHA256, IssueID: wire.IssueID, Key: wire.Key, Project: wire.Project, Updated: wire.Updated, Fields: fields, Complete: wire.Complete}
	if err := validateJiraIssueReadResult(value); err != nil {
		return domain.BrokerJiraIssueReadResult{}, err
	}
	return value, nil
}

func validateJiraIssueReadResult(value domain.BrokerJiraIssueReadResult) error {
	if value.SchemaVersion != SchemaVersion || !validDigest(value.ArgumentsSHA256) || !validPositiveDecimal(value.IssueID) || !domain.ValidJiraIssueKey(value.Key) || len(value.Key) > domain.BrokerMaxIdentifierBytes || !validIdentifier(value.Project) ||
		!strings.HasPrefix(value.Key, value.Project+"-") || value.Updated == "" || len(value.Updated) > domain.BrokerMaxIdentifierBytes || !utf8.ValidString(value.Updated) ||
		!validBrokerText(value.Updated) || !value.Complete || len(value.Fields) == 0 || len(value.Fields) > 3 {
		return reject(domain.BrokerReasonMalformed)
	}
	seen := map[domain.BrokerJiraIssueField]bool{}
	previous := domain.BrokerJiraIssueField("")
	var total, escaped int64
	for _, field := range value.Fields {
		if !field.Present || seen[field.Field] || previous != "" && field.Field <= previous || !validIssueFields([]domain.BrokerJiraIssueField{field.Field}) || !validBrokerText(field.Value) {
			return reject(domain.BrokerReasonMalformed)
		}
		if field.Null {
			if field.Field != domain.BrokerJiraIssueFieldDescription || field.Value != "" {
				return reject(domain.BrokerReasonMalformed)
			}
		} else if field.Field == domain.BrokerJiraIssueFieldSummary && field.Value == "" || field.Field == domain.BrokerJiraIssueFieldUpdated && field.Value == "" {
			return reject(domain.BrokerReasonMalformed)
		}
		if field.Field == domain.BrokerJiraIssueFieldUpdated && field.Value != value.Updated {
			return reject(domain.BrokerReasonMalformed)
		}
		total += int64(len(field.Value))
		encodedBytes, ok := escapedJSONTextBytes(field.Value)
		escaped += encodedBytes
		if !ok || total > MaxReadResultValueBytes || escaped > MaxReadResultWireBytes-MaxReadEnvelopeOverhead {
			return reject(domain.BrokerReasonMalformed)
		}
		seen[field.Field] = true
		previous = field.Field
	}
	return nil
}

func EncodeConfluencePageReadResultV1(value domain.BrokerConfluencePageReadResult) ([]byte, error) {
	if err := validateConfluencePageReadResult(value); err != nil {
		return nil, err
	}
	return marshalReadResult(confluencePageReadResultWire{
		SchemaVersion: SchemaVersion, ArgumentsSHA256: value.ArgumentsSHA256, PageID: value.PageID, Type: value.Type, Space: value.Space, Version: value.Version,
		Title: value.Title, Updated: value.Updated, Projection: string(value.Projection), StorageBase64: base64.StdEncoding.EncodeToString(value.Storage),
		StoragePresent: value.StoragePresent, Complete: value.Complete,
	})
}

func DecodeConfluencePageReadResultV1(data []byte) (domain.BrokerConfluencePageReadResult, error) {
	var wire confluencePageReadResultWire
	if err := strictDecode(data, MaxReadResultWireBytes, &wire); err != nil {
		return domain.BrokerConfluencePageReadResult{}, err
	}
	if !wire.StoragePresent && wire.StorageBase64 != "" {
		return domain.BrokerConfluencePageReadResult{}, reject(domain.BrokerReasonMalformed)
	}
	storage := []byte(nil)
	if wire.StoragePresent {
		decoded, err := base64.StdEncoding.Strict().DecodeString(wire.StorageBase64)
		if err != nil || base64.StdEncoding.EncodeToString(decoded) != wire.StorageBase64 {
			return domain.BrokerConfluencePageReadResult{}, reject(domain.BrokerReasonMalformed)
		}
		storage = decoded
	}
	value := domain.BrokerConfluencePageReadResult{SchemaVersion: wire.SchemaVersion, ArgumentsSHA256: wire.ArgumentsSHA256, PageID: wire.PageID, Type: wire.Type, Space: wire.Space, Version: wire.Version, Title: wire.Title, Updated: wire.Updated, Projection: domain.BrokerConfluenceProjection(wire.Projection), Storage: storage, StoragePresent: wire.StoragePresent, Complete: wire.Complete}
	if err := validateConfluencePageReadResult(value); err != nil {
		return domain.BrokerConfluencePageReadResult{}, err
	}
	return value, nil
}

func EncodeJiraCommentResultV1(value domain.BrokerJiraCommentResult) ([]byte, error) {
	if err := validateJiraCommentResult(value); err != nil {
		return nil, err
	}
	return marshalReadResult(jiraCommentResultToWire(value))
}

func DecodeJiraCommentResultV1(data []byte) (domain.BrokerJiraCommentResult, error) {
	var wire jiraCommentResultWire
	if err := strictDecode(data, 1<<20, &wire); err != nil {
		return domain.BrokerJiraCommentResult{}, err
	}
	value := jiraCommentResultFromWire(wire)
	if err := validateJiraCommentResult(value); err != nil {
		return domain.BrokerJiraCommentResult{}, err
	}
	return value, nil
}

func ValidateJiraCommentResultForV1(value domain.BrokerJiraCommentResult, request domain.BrokerRequest) error {
	if request.Operation != domain.BrokerOperationJiraCommentPreview && request.Operation != domain.BrokerOperationJiraCommentApply || validateRequest(request) != nil || validateJiraCommentResult(value) != nil {
		return reject(domain.BrokerReasonMalformed)
	}
	digest, err := ArgumentsSHA256(request)
	nativeDigest, nativeErr := NativeCandidateSHA256(request.Operation, request.Arguments.JiraComment.NativeBody)
	wantMode := "preview"
	if request.Operation == domain.BrokerOperationJiraCommentApply {
		wantMode = "apply"
	}
	if err != nil || nativeErr != nil || value.ArgumentsSHA256 != digest || value.NativeCandidateSHA256 != nativeDigest || value.Mode != wantMode ||
		request.Operation == domain.BrokerOperationJiraCommentApply && (value.OperationTicket != request.Arguments.JiraComment.OperationTicket || value.ProposalHash != request.Arguments.JiraComment.ExpectedProposalHash) {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func validateJiraCommentResult(value domain.BrokerJiraCommentResult) error {
	if value.SchemaVersion != SchemaVersion || !validDigest(value.ArgumentsSHA256) || !validIdentifier(value.OperationTicket) ||
		!validDigest(value.ProposalHash) || !validDigest(value.NativeCandidateSHA256) || !validDigest(value.VersionEvidenceSHA256) {
		return reject(domain.BrokerReasonMalformed)
	}
	switch value.Mode {
	case "preview":
		if value.Status != "proposed" || value.CommentID != "" || value.WriteAttempted || !value.Complete || value.Reconciled {
			return reject(domain.BrokerReasonMalformed)
		}
	case "apply":
		switch value.Status {
		case "applied", "recovered":
			if !validPositiveDecimal(value.CommentID) || !value.WriteAttempted || !value.Complete || !value.Reconciled {
				return reject(domain.BrokerReasonMalformed)
			}
		case "not_applied":
			if value.CommentID != "" || !value.WriteAttempted || !value.Complete || value.Reconciled {
				return reject(domain.BrokerReasonMalformed)
			}
		case "outcome_unknown":
			if value.CommentID != "" || !value.WriteAttempted || value.Complete {
				return reject(domain.BrokerReasonMalformed)
			}
		default:
			return reject(domain.BrokerReasonMalformed)
		}
	default:
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func jiraCommentResultToWire(value domain.BrokerJiraCommentResult) jiraCommentResultWire {
	return jiraCommentResultWire{SchemaVersion: value.SchemaVersion, ArgumentsSHA256: value.ArgumentsSHA256, OperationTicket: value.OperationTicket, Mode: value.Mode, Status: value.Status, ProposalHash: value.ProposalHash, NativeCandidateSHA256: value.NativeCandidateSHA256, VersionEvidenceSHA256: value.VersionEvidenceSHA256, CommentID: value.CommentID, WriteAttempted: value.WriteAttempted, Complete: value.Complete, Reconciled: value.Reconciled}
}

func jiraCommentResultFromWire(wire jiraCommentResultWire) domain.BrokerJiraCommentResult {
	return domain.BrokerJiraCommentResult{SchemaVersion: wire.SchemaVersion, ArgumentsSHA256: wire.ArgumentsSHA256, OperationTicket: wire.OperationTicket, Mode: wire.Mode, Status: wire.Status, ProposalHash: wire.ProposalHash, NativeCandidateSHA256: wire.NativeCandidateSHA256, VersionEvidenceSHA256: wire.VersionEvidenceSHA256, CommentID: wire.CommentID, WriteAttempted: wire.WriteAttempted, Complete: wire.Complete, Reconciled: wire.Reconciled}
}

func validateConfluencePageReadResult(value domain.BrokerConfluencePageReadResult) error {
	if value.SchemaVersion != SchemaVersion || !validDigest(value.ArgumentsSHA256) || !domain.ValidConfluenceContentID(value.PageID) || value.Type != "page" || !validIdentifier(value.Space) ||
		value.Version <= 0 || value.Title == "" || int64(len(value.Title)) > MaxReadTitleBytes || !validBrokerText(value.Title) ||
		value.Updated == "" || len(value.Updated) > domain.BrokerMaxIdentifierBytes || !validBrokerText(value.Updated) || !value.Complete ||
		value.Projection != domain.BrokerConfluenceProjectionMetadata && value.Projection != domain.BrokerConfluenceProjectionStorage ||
		int64(len(value.Storage)) > MaxReadResultValueBytes || !utf8.Valid(value.Storage) {
		return reject(domain.BrokerReasonMalformed)
	}
	if value.Projection == domain.BrokerConfluenceProjectionMetadata && (value.StoragePresent || value.Storage != nil) {
		return reject(domain.BrokerReasonMalformed)
	}
	if value.Projection == domain.BrokerConfluenceProjectionStorage && (!value.StoragePresent || value.Storage == nil) {
		return reject(domain.BrokerReasonMalformed)
	}
	titleBytes, ok := escapedJSONTextBytes(value.Title)
	if !ok || titleBytes+int64(base64.StdEncoding.EncodedLen(len(value.Storage))) > MaxReadResultWireBytes-MaxReadEnvelopeOverhead {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func ValidateJiraIssueReadResultForV1(value domain.BrokerJiraIssueReadResult, request domain.BrokerRequest) error {
	if request.Operation != domain.BrokerOperationJiraIssueRead || validateRequest(request) != nil || validateJiraIssueReadResult(value) != nil {
		return reject(domain.BrokerReasonMalformed)
	}
	digest, err := ArgumentsSHA256(request)
	if err != nil || value.ArgumentsSHA256 != digest || value.Key != request.Arguments.JiraIssueRead.IssueKey || len(value.Fields) != len(request.Arguments.JiraIssueRead.Fields) {
		return reject(domain.BrokerReasonMalformed)
	}
	for index, field := range value.Fields {
		if field.Field != sortedJiraIssueFields(request.Arguments.JiraIssueRead.Fields)[index] {
			return reject(domain.BrokerReasonMalformed)
		}
	}
	return nil
}

func ValidateConfluencePageReadResultForV1(value domain.BrokerConfluencePageReadResult, request domain.BrokerRequest) error {
	if request.Operation != domain.BrokerOperationConfluencePageRead || validateRequest(request) != nil || validateConfluencePageReadResult(value) != nil {
		return reject(domain.BrokerReasonMalformed)
	}
	digest, err := ArgumentsSHA256(request)
	if err != nil || value.ArgumentsSHA256 != digest || value.PageID != request.Arguments.ConfluencePageRead.PageID || value.Projection != request.Arguments.ConfluencePageRead.Projection {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func sortedJiraIssueFields(values []domain.BrokerJiraIssueField) []domain.BrokerJiraIssueField {
	out := append([]domain.BrokerJiraIssueField{}, values...)
	slices.Sort(out)
	return out
}

func validBrokerText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, current := range value {
		if current < 0x20 && current != '\t' && current != '\n' && current != '\r' {
			return false
		}
	}
	return true
}

func escapedJSONTextBytes(value string) (int64, bool) {
	if !validBrokerText(value) {
		return 0, false
	}
	var total int64
	for _, current := range value {
		switch current {
		case '"', '\\', '\t', '\n', '\r':
			total += 2
		case '\u2028', '\u2029':
			total += 6
		default:
			total += int64(utf8.RuneLen(current))
		}
	}
	return total, true
}

func marshalReadResult(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil || int64(buffer.Len()) > MaxReadResultWireBytes+1 {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	encoded := buffer.Bytes()
	return encoded[:len(encoded)-1], nil
}

func sortedJiraReadResultFields(values []domain.BrokerJiraIssueReadField) []domain.BrokerJiraIssueReadField {
	out := append([]domain.BrokerJiraIssueReadField(nil), values...)
	slices.SortFunc(out, func(a, b domain.BrokerJiraIssueReadField) int {
		return strings.Compare(string(a.Field), string(b.Field))
	})
	return out
}
