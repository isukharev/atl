package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

const (
	brokerJiraQualificationBytes int64 = 64 << 10
	brokerJiraBusinessBytes      int64 = 64 << 20
)

var _ domain.BrokerJiraIssueReadPort = (*Jira)(nil)

// BrokerOriginSHA256 derives the contract digest from this adapter's immutable
// configured base. It performs no backend I/O and never returns the URL.
func (j *Jira) BrokerOriginSHA256() (string, error) {
	if j == nil {
		return "", fmt.Errorf("invalid Jira broker reader")
	}
	digest, err := backendid.OriginSHA256(j.base)
	return strings.TrimPrefix(digest, backendid.Prefix), err
}

// QualifyBrokerIssue reads only the identity evidence admitted before final
// authorization. The configured HTTP client owns the destination and credentials.
func (j *Jira) QualifyBrokerIssue(ctx context.Context, key string) (domain.BrokerJiraIssueIdentity, error) {
	if !guardedLinkKey(key) {
		return domain.BrokerJiraIssueIdentity{}, brokerJiraReadError()
	}
	data, err := j.brokerReadIssue(ctx, key, []string{"project", "updated"}, brokerJiraQualificationBytes)
	if err != nil {
		return domain.BrokerJiraIssueIdentity{}, err
	}
	identity, _, err := decodeBrokerJiraIssue(data, map[string]bool{"project": true, "updated": true})
	if err != nil || identity.Key != key {
		return domain.BrokerJiraIssueIdentity{}, brokerJiraReadError()
	}
	return identity, nil
}

// ReadBrokerIssue addresses the qualified immutable ID and requests exactly the
// selected business fields plus project/updated identity evidence. The caller
// compares that evidence with qualification before releasing any content.
func (j *Jira) ReadBrokerIssue(ctx context.Context, id string, selected []domain.BrokerJiraIssueField) (domain.BrokerJiraIssueSnapshot, error) {
	if !guardedLinkID(id) || len(selected) == 0 || len(selected) > 3 {
		return domain.BrokerJiraIssueSnapshot{}, brokerJiraReadError()
	}
	expected := map[string]bool{"project": true, "updated": true}
	seen := make(map[domain.BrokerJiraIssueField]bool, len(selected))
	for _, field := range selected {
		if seen[field] || field != domain.BrokerJiraIssueFieldSummary && field != domain.BrokerJiraIssueFieldDescription && field != domain.BrokerJiraIssueFieldUpdated {
			return domain.BrokerJiraIssueSnapshot{}, brokerJiraReadError()
		}
		seen[field], expected[string(field)] = true, true
	}
	queryFields := make([]string, 0, len(expected))
	for field := range expected {
		queryFields = append(queryFields, field)
	}
	sort.Strings(queryFields)
	data, err := j.brokerReadIssue(ctx, id, queryFields, brokerJiraBusinessBytes)
	if err != nil {
		return domain.BrokerJiraIssueSnapshot{}, err
	}
	identity, fields, err := decodeBrokerJiraIssue(data, expected)
	if err != nil || identity.ID != id {
		return domain.BrokerJiraIssueSnapshot{}, brokerJiraReadError()
	}
	result := domain.BrokerJiraIssueSnapshot{Identity: identity, Fields: make([]domain.BrokerJiraIssueReadField, 0, len(selected)), Complete: true}
	for _, field := range selected {
		raw := fields[string(field)]
		value := domain.BrokerJiraIssueReadField{Field: field, Present: true, Null: bytes.Equal(bytes.TrimSpace(raw), []byte("null"))}
		if value.Null {
			if field != domain.BrokerJiraIssueFieldDescription {
				return domain.BrokerJiraIssueSnapshot{}, brokerJiraReadError()
			}
		} else if strictjson.Decode(raw, &value.Value) != nil {
			return domain.BrokerJiraIssueSnapshot{}, brokerJiraReadError()
		}
		result.Fields = append(result.Fields, value)
	}
	return result, nil
}

func (j *Jira) brokerReadIssue(ctx context.Context, selector string, fields []string, maximum int64) ([]byte, error) {
	query := url.Values{"fields": []string{strings.Join(fields, ",")}}.Encode()
	data, err := j.c.DoWithBodyLimit(domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx)), http.MethodGet, "/rest/api/2/issue/"+url.PathEscape(selector)+"?"+query, nil, nil, maximum)
	if err != nil {
		return nil, brokerJiraTransportFailure(err)
	}
	return data, nil
}

func decodeBrokerJiraIssue(data []byte, expected map[string]bool) (domain.BrokerJiraIssueIdentity, map[string]json.RawMessage, error) {
	var root map[string]json.RawMessage
	var identity domain.BrokerJiraIssueIdentity
	if strictjson.Decode(data, &root) != nil || root == nil {
		return identity, nil, brokerJiraReadError()
	}
	for key := range root {
		if key != "id" && key != "key" && key != "fields" && key != "self" && key != "expand" {
			return identity, nil, brokerJiraReadError()
		}
		if (key == "self" || key == "expand") && !brokerJiraDiscardString(root[key]) {
			return identity, nil, brokerJiraReadError()
		}
	}
	var fields map[string]json.RawMessage
	if strictjson.Decode(root["id"], &identity.ID) != nil || !guardedLinkID(identity.ID) || strictjson.Decode(root["key"], &identity.Key) != nil || !guardedLinkKey(identity.Key) || strictjson.Decode(root["fields"], &fields) != nil || len(fields) != len(expected) {
		return domain.BrokerJiraIssueIdentity{}, nil, brokerJiraReadError()
	}
	for field := range fields {
		if !expected[field] {
			return domain.BrokerJiraIssueIdentity{}, nil, brokerJiraReadError()
		}
	}
	var project map[string]json.RawMessage
	if strictjson.Decode(fields["project"], &project) != nil || project == nil || strictjson.Decode(project["key"], &identity.Project) != nil || !guardedLinkProject(identity.Project) || !strings.HasPrefix(identity.Key, identity.Project+"-") || strictjson.Decode(fields["updated"], &identity.Updated) != nil || len(identity.Updated) > domain.BrokerMaxIdentifierBytes || !guardedLabelUpdated(identity.Updated) {
		return domain.BrokerJiraIssueIdentity{}, nil, brokerJiraReadError()
	}
	if !brokerJiraProjectMetadata(project) {
		return domain.BrokerJiraIssueIdentity{}, nil, brokerJiraReadError()
	}
	identity.Complete = true
	return identity, fields, nil
}

// Standard Jira metadata may accompany project even with an exact fields
// query. Validate this closed, bounded vocabulary and discard it completely.
func brokerJiraProjectMetadata(project map[string]json.RawMessage) bool {
	for key, raw := range project {
		switch key {
		case "key", "name", "self", "projectTypeKey":
			if !brokerJiraDiscardString(raw) {
				return false
			}
		case "id":
			var id string
			if strictjson.Decode(raw, &id) != nil || !guardedLinkID(id) {
				return false
			}
		case "simplified":
			var value *bool
			if strictjson.Decode(raw, &value) != nil || value == nil {
				return false
			}
		case "avatarUrls", "projectCategory":
			var metadata map[string]json.RawMessage
			if strictjson.Decode(raw, &metadata) != nil || metadata == nil || len(metadata) > 4 {
				return false
			}
			for name, value := range metadata {
				known := key == "avatarUrls" && (name == "16x16" || name == "24x24" || name == "32x32" || name == "48x48") || key == "projectCategory" && (name == "id" || name == "name" || name == "description" || name == "self")
				if !known || !brokerJiraDiscardString(value) {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

func brokerJiraDiscardString(raw json.RawMessage) bool {
	var value *string
	return strictjson.Decode(raw, &value) == nil && value != nil && len(*value) <= 4096
}

func brokerJiraReadError() error {
	return fmt.Errorf("%w: Jira broker read evidence is invalid or incomplete", domain.ErrCheckFailed)
}

type brokerJiraReadTransportError struct {
	cause  error
	status int
}

func (*brokerJiraReadTransportError) Error() string     { return "Jira broker issue read failed" }
func (e *brokerJiraReadTransportError) Unwrap() error   { return e.cause }
func (e *brokerJiraReadTransportError) HTTPStatus() int { return e.status }

func brokerJiraTransportFailure(err error) error {
	var causes []error
	for _, sentinel := range []error{
		domain.ErrUsage, domain.ErrAuth, domain.ErrForbidden, domain.ErrNotFound,
		domain.ErrVersionConflict, domain.ErrConfig, domain.ErrCheckFailed,
		domain.ErrReadAttemptBudgetExhausted, domain.ErrReadResponseBudgetExhausted,
		context.Canceled, context.DeadlineExceeded,
	} {
		if errors.Is(err, sentinel) {
			causes = append(causes, sentinel)
		}
	}
	result := &brokerJiraReadTransportError{cause: errors.Join(causes...)}
	var status interface{ HTTPStatus() int }
	if errors.As(err, &status) {
		result.status = status.HTTPStatus()
	}
	return result
}
