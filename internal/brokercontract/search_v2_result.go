package brokercontract

import (
	"sort"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const (
	BrokerProjectPagePartialOffsetLimitV2       = "offset_limit"
	BrokerProjectPagePartialPaginationStalledV2 = "pagination_stalled"
)

func EncodeJiraProjectPageResultV2(value domain.BrokerJiraProjectPageResultV2) ([]byte, error) {
	value = normalizeJiraProjectPageResultV2(value)
	if err := validateJiraProjectPageResultV2(value); err != nil {
		return nil, err
	}
	return marshalReadResult(jiraProjectPageResultToWireV2(value))
}

func DecodeJiraProjectPageResultV2(data []byte) (domain.BrokerJiraProjectPageResultV2, error) {
	var wire projectPageResultWireV2
	if err := strictDecodeExecutionV2(data, MaxProjectPageResultWireBytesV2, &wire); err != nil {
		return domain.BrokerJiraProjectPageResultV2{}, err
	}
	if wire.SchemaVersion != ExecutionSchemaVersionV2 {
		return domain.BrokerJiraProjectPageResultV2{}, reject(domain.BrokerReasonUnsupported)
	}
	value := jiraProjectPageResultFromWireV2(wire)
	if err := validateJiraProjectPageResultV2(value); err != nil {
		return domain.BrokerJiraProjectPageResultV2{}, err
	}
	return value, nil
}

func ValidateJiraProjectPageResultForRequestV2(value domain.BrokerJiraProjectPageResultV2, request domain.BrokerProjectPageRequestV2) error {
	request = normalizeProjectPageRequestV2(request)
	value = normalizeJiraProjectPageResultV2(value)
	if validateProjectPageRequestV2(request) != nil || validateJiraProjectPageResultV2(value) != nil {
		return reject(domain.BrokerReasonMalformed)
	}
	digest, err := ProjectPageArgumentsSHA256V2(request)
	if err != nil || value.ArgumentsSHA256 != digest || value.ProjectKey != request.Arguments.ProjectKey ||
		value.Page.StartAt != request.Arguments.StartAt || value.Page.MaxResults > request.Arguments.MaxResults {
		return reject(domain.BrokerReasonMalformed)
	}
	for _, issue := range value.Issues {
		if len(issue.Fields) != len(request.Arguments.Fields) {
			return reject(domain.BrokerReasonMalformed)
		}
		for index, field := range issue.Fields {
			if string(field.Field) != string(request.Arguments.Fields[index]) {
				return reject(domain.BrokerReasonMalformed)
			}
		}
	}
	return nil
}

func validateJiraProjectPageResultV2(value domain.BrokerJiraProjectPageResultV2) error {
	if value.SchemaVersion != ExecutionSchemaVersionV2 || !validDigest(value.ArgumentsSHA256) ||
		value.ConsistencyProfile != domain.BrokerReadConsistencyIdentitySnapshotV1 || !validPositiveDecimal(value.ProjectID) ||
		!validProjectKeyV2(value.ProjectKey) || value.Issues == nil || len(value.Issues) > domain.BrokerProjectPageMaxResults || !value.Complete ||
		!validJiraProjectPageResultPageV2(value.Page, len(value.Issues)) {
		return reject(domain.BrokerReasonMalformed)
	}
	seenIDs, seenKeys := map[string]bool{}, map[string]bool{}
	var valueBytes, escapedBytes int64
	for _, issue := range value.Issues {
		identity := domain.BrokerJiraProjectPageIssueIdentityV2{
			ID: issue.ID, Key: issue.Key, ProjectID: issue.ProjectID, ProjectKey: issue.ProjectKey,
			Updated: issue.Updated, Complete: true,
		}
		if !validJiraProjectPageIssueIdentityV2(identity) || issue.ProjectID != value.ProjectID || issue.ProjectKey != value.ProjectKey ||
			seenIDs[issue.ID] || seenKeys[issue.Key] || len(issue.Fields) > 2 {
			return reject(domain.BrokerReasonMalformed)
		}
		seenIDs[issue.ID], seenKeys[issue.Key] = true, true
		previous := domain.BrokerJiraIssueField("")
		seenFields := map[domain.BrokerJiraIssueField]bool{}
		for _, field := range issue.Fields {
			if !field.Present || field.Field != domain.BrokerJiraIssueFieldDescription && field.Field != domain.BrokerJiraIssueFieldSummary ||
				seenFields[field.Field] || previous != "" && field.Field <= previous || !validBrokerText(field.Value) {
				return reject(domain.BrokerReasonMalformed)
			}
			if field.Null {
				if field.Field != domain.BrokerJiraIssueFieldDescription || field.Value != "" {
					return reject(domain.BrokerReasonMalformed)
				}
			} else if field.Field == domain.BrokerJiraIssueFieldSummary && field.Value == "" {
				return reject(domain.BrokerReasonMalformed)
			}
			valueBytes += int64(len(field.Value))
			escaped, ok := escapedJSONTextBytes(field.Value)
			escapedBytes += escaped
			if !ok || valueBytes > MaxReadResultValueBytes || escapedBytes > MaxProjectPageResultWireBytesV2-MaxReadEnvelopeOverhead {
				return reject(domain.BrokerReasonMalformed)
			}
			seenFields[field.Field] = true
			previous = field.Field
		}
	}
	return nil
}

func validJiraProjectPageResultPageV2(value domain.BrokerJiraProjectPageResultPageV2, count int) bool {
	if value.StartAt < 0 || value.StartAt > domain.BrokerProjectPageMaxStartAt || value.MaxResults <= 0 ||
		value.MaxResults > domain.BrokerProjectPageMaxResults || value.Total < 0 || value.Count != count || count > value.MaxResults ||
		value.SelectionComplete || value.NextCursorPresent != (value.NextCursor != "") {
		return false
	}
	next := value.StartAt + count
	if next < value.StartAt || next > value.Total {
		return false
	}
	exhausted := next == value.Total
	stalled := count == 0 && next < value.Total
	offsetLimited := next > domain.BrokerProjectPageMaxStartAt
	if value.CoordinateExhausted != exhausted {
		return false
	}
	switch {
	case exhausted:
		return !value.NextCursorPresent && value.PartialReason == ""
	case stalled:
		return !value.NextCursorPresent && value.PartialReason == BrokerProjectPagePartialPaginationStalledV2
	case offsetLimited:
		return !value.NextCursorPresent && value.PartialReason == BrokerProjectPagePartialOffsetLimitV2
	default:
		return value.NextCursorPresent && value.NextCursor == strconv.Itoa(next) && value.PartialReason == ""
	}
}

func normalizeJiraProjectPageResultV2(value domain.BrokerJiraProjectPageResultV2) domain.BrokerJiraProjectPageResultV2 {
	value.Issues = append([]domain.BrokerJiraProjectPageResultIssueV2{}, value.Issues...)
	for index := range value.Issues {
		value.Issues[index].Fields = append([]domain.BrokerJiraIssueReadField{}, value.Issues[index].Fields...)
		sort.Slice(value.Issues[index].Fields, func(i, j int) bool {
			return strings.Compare(string(value.Issues[index].Fields[i].Field), string(value.Issues[index].Fields[j].Field)) < 0
		})
	}
	return value
}

func jiraProjectPageResultToWireV2(value domain.BrokerJiraProjectPageResultV2) projectPageResultWireV2 {
	issues := make([]projectPageResultIssueWireV2, len(value.Issues))
	for index, issue := range value.Issues {
		fields := make([]jiraIssueReadFieldWire, len(issue.Fields))
		for fieldIndex, field := range issue.Fields {
			fields[fieldIndex] = jiraIssueReadFieldWire{Field: string(field.Field), Present: field.Present, Null: field.Null, Value: field.Value}
		}
		issues[index] = projectPageResultIssueWireV2{ID: issue.ID, Key: issue.Key, ProjectID: issue.ProjectID, ProjectKey: issue.ProjectKey, Updated: issue.Updated, Fields: fields}
	}
	page := projectPageResultPageWireV2{StartAt: value.Page.StartAt, MaxResults: value.Page.MaxResults, Total: value.Page.Total, Count: value.Page.Count, CoordinateExhausted: value.Page.CoordinateExhausted, SelectionComplete: value.Page.SelectionComplete, PartialReason: value.Page.PartialReason}
	if value.Page.NextCursorPresent {
		cursor := value.Page.NextCursor
		page.NextCursor = &cursor
	}
	return projectPageResultWireV2{SchemaVersion: value.SchemaVersion, ArgumentsSHA256: value.ArgumentsSHA256, ConsistencyProfile: value.ConsistencyProfile, ProjectID: value.ProjectID, ProjectKey: value.ProjectKey, Issues: issues, Page: page, Complete: value.Complete}
}

func jiraProjectPageResultFromWireV2(wire projectPageResultWireV2) domain.BrokerJiraProjectPageResultV2 {
	issues := make([]domain.BrokerJiraProjectPageResultIssueV2, len(wire.Issues))
	for index, issue := range wire.Issues {
		fields := make([]domain.BrokerJiraIssueReadField, len(issue.Fields))
		for fieldIndex, field := range issue.Fields {
			fields[fieldIndex] = domain.BrokerJiraIssueReadField{Field: domain.BrokerJiraIssueField(field.Field), Present: field.Present, Null: field.Null, Value: field.Value}
		}
		issues[index] = domain.BrokerJiraProjectPageResultIssueV2{ID: issue.ID, Key: issue.Key, ProjectID: issue.ProjectID, ProjectKey: issue.ProjectKey, Updated: issue.Updated, Fields: fields}
	}
	page := domain.BrokerJiraProjectPageResultPageV2{StartAt: wire.Page.StartAt, MaxResults: wire.Page.MaxResults, Total: wire.Page.Total, Count: wire.Page.Count, CoordinateExhausted: wire.Page.CoordinateExhausted, SelectionComplete: wire.Page.SelectionComplete, PartialReason: wire.Page.PartialReason}
	if wire.Page.NextCursor != nil {
		page.NextCursor, page.NextCursorPresent = *wire.Page.NextCursor, true
	}
	return domain.BrokerJiraProjectPageResultV2{SchemaVersion: wire.SchemaVersion, ArgumentsSHA256: wire.ArgumentsSHA256, ConsistencyProfile: wire.ConsistencyProfile, ProjectID: wire.ProjectID, ProjectKey: wire.ProjectKey, Issues: issues, Page: page, Complete: wire.Complete}
}
