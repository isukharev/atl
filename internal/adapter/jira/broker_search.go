package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

const (
	brokerJiraProjectIdentityBytes     int64 = 256 << 10
	brokerJiraProjectPageIdentityBytes int64 = 1 << 20
	brokerJiraProjectPageBusinessBytes int64 = 64 << 20

	brokerJiraProjectSupportMaxMembers = 256
	brokerJiraProjectSupportMaxDepth   = 8
	brokerJiraProjectSupportMaxString  = 64 << 10
)

var _ domain.BrokerJiraProjectPageReadPort = (*Jira)(nil)

func (j *Jira) QualifyBrokerProject(ctx context.Context, key string) (domain.BrokerJiraProjectIdentityV2, error) {
	if !guardedLinkProject(key) {
		return domain.BrokerJiraProjectIdentityV2{}, brokerJiraProjectPageError()
	}
	data, err := j.c.DoWithBodyLimit(domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx)), http.MethodGet, "/rest/api/2/project/"+url.PathEscape(key), nil, nil, brokerJiraProjectIdentityBytes)
	if err != nil {
		return domain.BrokerJiraProjectIdentityV2{}, brokerJiraTransportFailure(err)
	}
	identity, err := decodeBrokerJiraProjectIdentity(data)
	if err != nil || identity.Key != key {
		return domain.BrokerJiraProjectIdentityV2{}, brokerJiraProjectPageError()
	}
	return identity, nil
}

func (j *Jira) QualifyBrokerProjectIssuePage(ctx context.Context, projectID string, startAt, maxResults int) (domain.BrokerJiraProjectPageIdentitySnapshotV2, error) {
	data, err := j.brokerProjectPage(ctx, projectID, nil, startAt, maxResults, brokerJiraProjectPageIdentityBytes)
	if err != nil {
		return domain.BrokerJiraProjectPageIdentitySnapshotV2{}, err
	}
	return decodeBrokerJiraProjectIdentityPage(data, projectID, startAt, maxResults)
}

func (j *Jira) ReadBrokerProjectIssuePage(ctx context.Context, projectID string, selected []domain.BrokerProjectPageField, startAt, maxResults int) (domain.BrokerJiraProjectPageSnapshotV2, error) {
	if selected == nil || !validBrokerProjectPageFields(selected) {
		return domain.BrokerJiraProjectPageSnapshotV2{}, brokerJiraProjectPageError()
	}
	data, err := j.brokerProjectPage(ctx, projectID, selected, startAt, maxResults, brokerJiraProjectPageBusinessBytes)
	if err != nil {
		return domain.BrokerJiraProjectPageSnapshotV2{}, err
	}
	return decodeBrokerJiraProjectBusinessPage(data, projectID, selected, startAt, maxResults)
}

func (j *Jira) brokerProjectPage(ctx context.Context, projectID string, selected []domain.BrokerProjectPageField, startAt, maxResults int, maximum int64) ([]byte, error) {
	if !guardedLinkID(projectID) || startAt < 0 || startAt > domain.BrokerProjectPageMaxStartAt || maxResults <= 0 || maxResults > domain.BrokerProjectPageMaxResults ||
		selected != nil && !validBrokerProjectPageFields(selected) {
		return nil, brokerJiraProjectPageError()
	}
	fields := []string{"project", "updated"}
	for _, field := range selected {
		fields = append(fields, string(field))
	}
	sort.Strings(fields)
	query := url.Values{}
	query.Set("fields", strings.Join(fields, ","))
	query.Set("jql", "project = "+projectID+" ORDER BY id ASC")
	query.Set("maxResults", strconv.Itoa(maxResults))
	query.Set("startAt", strconv.Itoa(startAt))
	data, err := j.c.DoWithBodyLimit(domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx)), http.MethodGet, "/rest/api/2/search?"+query.Encode(), nil, nil, maximum)
	if err != nil {
		return nil, brokerJiraTransportFailure(err)
	}
	return data, nil
}

func validBrokerProjectPageFields(fields []domain.BrokerProjectPageField) bool {
	if len(fields) > 2 {
		return false
	}
	seen := map[domain.BrokerProjectPageField]bool{}
	for _, field := range fields {
		if field != domain.BrokerProjectPageFieldDescription && field != domain.BrokerProjectPageFieldSummary || seen[field] {
			return false
		}
		seen[field] = true
	}
	return true
}

func decodeBrokerJiraProjectIdentity(data []byte) (domain.BrokerJiraProjectIdentityV2, error) {
	var root map[string]json.RawMessage
	if strictjson.Decode(data, &root) != nil || root == nil {
		return domain.BrokerJiraProjectIdentityV2{}, brokerJiraProjectPageError()
	}
	allowed := map[string]bool{
		"archived": true, "assigneeType": true, "avatarUrls": true, "components": true,
		"description": true, "email": true, "expand": true, "id": true, "issueTypes": true,
		"key": true, "lead": true, "name": true, "projectCategory": true, "projectKeys": true,
		"projectTypeKey": true, "roles": true, "self": true, "url": true, "versions": true,
	}
	for name, raw := range root {
		if !allowed[name] || name != "id" && name != "key" && !brokerJiraBoundedSupportingValue(raw) {
			return domain.BrokerJiraProjectIdentityV2{}, brokerJiraProjectPageError()
		}
	}
	var identity domain.BrokerJiraProjectIdentityV2
	if strictjson.Decode(root["id"], &identity.ID) != nil || !guardedLinkID(identity.ID) ||
		strictjson.Decode(root["key"], &identity.Key) != nil || !guardedLinkProject(identity.Key) {
		return domain.BrokerJiraProjectIdentityV2{}, brokerJiraProjectPageError()
	}
	identity.Complete = true
	return identity, nil
}

func brokerJiraBoundedSupportingValue(raw json.RawMessage) bool {
	if len(raw) == 0 || len(raw) > brokerJiraProjectSupportMaxString {
		return false
	}
	value, err := strictjson.DecodeValue(raw)
	return err == nil && brokerJiraBoundedSupportingDecoded(value, 0)
}

func brokerJiraBoundedSupportingDecoded(value any, depth int) bool {
	if depth > brokerJiraProjectSupportMaxDepth {
		return false
	}
	switch typed := value.(type) {
	case nil, bool, json.Number:
		return true
	case string:
		return len(typed) <= brokerJiraProjectSupportMaxString && utf8.ValidString(typed)
	case []any:
		if len(typed) > brokerJiraProjectSupportMaxMembers {
			return false
		}
		for _, member := range typed {
			if !brokerJiraBoundedSupportingDecoded(member, depth+1) {
				return false
			}
		}
		return true
	case map[string]any:
		if len(typed) > brokerJiraProjectSupportMaxMembers {
			return false
		}
		for name, member := range typed {
			if name == "" || len(name) > 256 || !utf8.ValidString(name) || !brokerJiraBoundedSupportingDecoded(member, depth+1) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func decodeBrokerJiraProjectIdentityPage(data []byte, projectID string, startAt, maxResults int) (domain.BrokerJiraProjectPageIdentitySnapshotV2, error) {
	coordinates, rawIssues, err := decodeBrokerJiraProjectPageRoot(data, startAt, maxResults)
	if err != nil {
		return domain.BrokerJiraProjectPageIdentitySnapshotV2{}, err
	}
	issues := make([]domain.BrokerJiraProjectPageIssueIdentityV2, len(rawIssues))
	seenIDs, seenKeys := map[string]bool{}, map[string]bool{}
	for index, raw := range rawIssues {
		identity, _, decodeErr := decodeBrokerJiraProjectPageIssue(raw, projectID, nil)
		if decodeErr != nil || seenIDs[identity.ID] || seenKeys[identity.Key] {
			return domain.BrokerJiraProjectPageIdentitySnapshotV2{}, brokerJiraProjectPageError()
		}
		seenIDs[identity.ID], seenKeys[identity.Key] = true, true
		issues[index] = identity
	}
	return domain.BrokerJiraProjectPageIdentitySnapshotV2{
		StartAt: coordinates.startAt, MaxResults: coordinates.maxResults, Total: coordinates.total, Issues: issues,
		CoordinateExhausted: coordinates.exhausted, PaginationStalled: coordinates.stalled, Complete: true,
	}, nil
}

func decodeBrokerJiraProjectBusinessPage(data []byte, projectID string, selected []domain.BrokerProjectPageField, startAt, maxResults int) (domain.BrokerJiraProjectPageSnapshotV2, error) {
	coordinates, rawIssues, err := decodeBrokerJiraProjectPageRoot(data, startAt, maxResults)
	if err != nil {
		return domain.BrokerJiraProjectPageSnapshotV2{}, err
	}
	issues := make([]domain.BrokerJiraProjectPageIssueSnapshotV2, len(rawIssues))
	seenIDs, seenKeys := map[string]bool{}, map[string]bool{}
	for index, raw := range rawIssues {
		identity, fields, decodeErr := decodeBrokerJiraProjectPageIssue(raw, projectID, selected)
		if decodeErr != nil || seenIDs[identity.ID] || seenKeys[identity.Key] {
			return domain.BrokerJiraProjectPageSnapshotV2{}, brokerJiraProjectPageError()
		}
		seenIDs[identity.ID], seenKeys[identity.Key] = true, true
		issues[index] = domain.BrokerJiraProjectPageIssueSnapshotV2{Identity: identity, Fields: fields}
	}
	return domain.BrokerJiraProjectPageSnapshotV2{
		StartAt: coordinates.startAt, MaxResults: coordinates.maxResults, Total: coordinates.total, Issues: issues,
		CoordinateExhausted: coordinates.exhausted, PaginationStalled: coordinates.stalled, Complete: true,
	}, nil
}

type brokerJiraProjectPageCoordinates struct {
	startAt, maxResults, total int
	exhausted, stalled         bool
}

func decodeBrokerJiraProjectPageRoot(data []byte, requestedStart, requestedMax int) (brokerJiraProjectPageCoordinates, []json.RawMessage, error) {
	var root map[string]json.RawMessage
	if strictjson.Decode(data, &root) != nil || root == nil {
		return brokerJiraProjectPageCoordinates{}, nil, brokerJiraProjectPageError()
	}
	for name, raw := range root {
		switch name {
		case "startAt", "maxResults", "total", "issues":
		case "expand":
			if !brokerJiraDiscardString(raw) {
				return brokerJiraProjectPageCoordinates{}, nil, brokerJiraProjectPageError()
			}
		case "warningMessages":
			var warnings []json.RawMessage
			if strictjson.Decode(raw, &warnings) != nil || len(warnings) != 0 {
				return brokerJiraProjectPageCoordinates{}, nil, brokerJiraProjectPageError()
			}
		default:
			return brokerJiraProjectPageCoordinates{}, nil, brokerJiraProjectPageError()
		}
	}
	var coordinates brokerJiraProjectPageCoordinates
	var issues []json.RawMessage
	var startOK, maxOK, totalOK bool
	coordinates.startAt, startOK = brokerJiraPageInteger(root["startAt"])
	coordinates.maxResults, maxOK = brokerJiraPageInteger(root["maxResults"])
	coordinates.total, totalOK = brokerJiraPageInteger(root["total"])
	if !startOK || !maxOK || !totalOK || strictjson.Decode(root["issues"], &issues) != nil || issues == nil ||
		coordinates.startAt != requestedStart || coordinates.maxResults <= 0 || coordinates.maxResults > requestedMax || coordinates.total < 0 ||
		len(issues) > requestedMax || len(issues) > coordinates.maxResults {
		return brokerJiraProjectPageCoordinates{}, nil, brokerJiraProjectPageError()
	}
	next := coordinates.startAt + len(issues)
	if next < coordinates.startAt || next > coordinates.total {
		return brokerJiraProjectPageCoordinates{}, nil, brokerJiraProjectPageError()
	}
	coordinates.exhausted = next == coordinates.total
	coordinates.stalled = len(issues) == 0 && next < coordinates.total
	return coordinates, issues, nil
}

func brokerJiraPageInteger(raw json.RawMessage) (int, bool) {
	value, err := strictjson.DecodeValue(raw)
	number, ok := value.(json.Number)
	if err != nil || !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	if err != nil || parsed < 0 || int64(int(parsed)) != parsed {
		return 0, false
	}
	return int(parsed), true
}

func decodeBrokerJiraProjectPageIssue(raw json.RawMessage, projectID string, selected []domain.BrokerProjectPageField) (domain.BrokerJiraProjectPageIssueIdentityV2, []domain.BrokerJiraIssueReadField, error) {
	var root map[string]json.RawMessage
	if strictjson.Decode(raw, &root) != nil || root == nil {
		return domain.BrokerJiraProjectPageIssueIdentityV2{}, nil, brokerJiraProjectPageError()
	}
	for name, value := range root {
		if name != "id" && name != "key" && name != "fields" && name != "self" && name != "expand" ||
			(name == "self" || name == "expand") && !brokerJiraDiscardString(value) {
			return domain.BrokerJiraProjectPageIssueIdentityV2{}, nil, brokerJiraProjectPageError()
		}
	}
	var identity domain.BrokerJiraProjectPageIssueIdentityV2
	var fields map[string]json.RawMessage
	if strictjson.Decode(root["id"], &identity.ID) != nil || !guardedLinkID(identity.ID) || strictjson.Decode(root["key"], &identity.Key) != nil ||
		!guardedLinkKey(identity.Key) || strictjson.Decode(root["fields"], &fields) != nil || fields == nil {
		return domain.BrokerJiraProjectPageIssueIdentityV2{}, nil, brokerJiraProjectPageError()
	}
	expected := map[string]bool{"project": true, "updated": true}
	for _, field := range selected {
		expected[string(field)] = true
	}
	if len(fields) != len(expected) {
		return domain.BrokerJiraProjectPageIssueIdentityV2{}, nil, brokerJiraProjectPageError()
	}
	for name := range fields {
		if !expected[name] {
			return domain.BrokerJiraProjectPageIssueIdentityV2{}, nil, brokerJiraProjectPageError()
		}
	}
	var project map[string]json.RawMessage
	if strictjson.Decode(fields["project"], &project) != nil || project == nil || strictjson.Decode(project["id"], &identity.ProjectID) != nil ||
		identity.ProjectID != projectID || strictjson.Decode(project["key"], &identity.ProjectKey) != nil || !guardedLinkProject(identity.ProjectKey) ||
		!strings.HasPrefix(identity.Key, identity.ProjectKey+"-") || !brokerJiraProjectMetadata(project) ||
		strictjson.Decode(fields["updated"], &identity.Updated) != nil || len(identity.Updated) > domain.BrokerMaxIdentifierBytes || !guardedLabelUpdated(identity.Updated) {
		return domain.BrokerJiraProjectPageIssueIdentityV2{}, nil, brokerJiraProjectPageError()
	}
	identity.Complete = true
	result := make([]domain.BrokerJiraIssueReadField, 0, len(selected))
	for _, selectedField := range selected {
		field := domain.BrokerJiraIssueReadField{Field: domain.BrokerJiraIssueField(selectedField), Present: true}
		rawField := fields[string(selectedField)]
		field.Null = bytes.Equal(bytes.TrimSpace(rawField), []byte("null"))
		if field.Null {
			if selectedField != domain.BrokerProjectPageFieldDescription {
				return domain.BrokerJiraProjectPageIssueIdentityV2{}, nil, brokerJiraProjectPageError()
			}
		} else if strictjson.Decode(rawField, &field.Value) != nil || selectedField == domain.BrokerProjectPageFieldSummary && field.Value == "" {
			return domain.BrokerJiraProjectPageIssueIdentityV2{}, nil, brokerJiraProjectPageError()
		}
		result = append(result, field)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Field < result[j].Field })
	return identity, result, nil
}

func brokerJiraProjectPageError() error {
	return fmt.Errorf("%w: Jira broker project-page evidence is invalid or incomplete", domain.ErrCheckFailed)
}
