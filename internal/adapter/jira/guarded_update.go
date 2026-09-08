package jira

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

var _ domain.JiraGuardedUpdatePort = (*Jira)(nil)

type guardedUpdateNoAttemptError struct{ cause error }

func (*guardedUpdateNoAttemptError) Error() string {
	return "guarded Jira issue update was denied before dispatch"
}
func (e *guardedUpdateNoAttemptError) Unwrap() error                  { return e.cause }
func (e *guardedUpdateNoAttemptError) DiagnosticWriteAttempted() bool { return false }

func (j *Jira) ReadGuardedUpdateFieldCatalog(ctx context.Context, fields []string) (domain.JiraGuardedFieldCatalog, error) {
	return j.ReadGuardedFieldCatalog(ctx, fields)
}

// ReadGuardedUpdateIssue returns strict presence-qualified evidence for every
// selected update field plus immutable identity, project, and updated marker.
func (j *Jira) ReadGuardedUpdateIssue(ctx context.Context, reference string, selected []string) (domain.JiraGuardedFieldIssue, error) {
	if len(reference) == 0 || len(reference) > domain.JiraGuardedUpdateMaxImmutableIDBytes ||
		(!domain.ValidJiraIssueKey(reference) && !guardedLinkID(reference)) {
		return domain.JiraGuardedFieldIssue{}, guardedUpdateDecodeError("issue")
	}
	fields, err := guardedUpdateFieldIDs(selected)
	if err != nil {
		return domain.JiraGuardedFieldIssue{}, err
	}
	queryFields := append(append([]string(nil), fields...), "project", "updated")
	sort.Strings(queryFields)
	query := url.Values{"fields": []string{strings.Join(queryFields, ",")}}.Encode()
	path := "/rest/api/2/issue/" + url.PathEscape(reference) + "?" + query
	if len(query) > domain.JiraGuardedUpdateMaxQueryAndPathBytes || len(path) > domain.JiraGuardedUpdateMaxQueryAndPathBytes {
		return domain.JiraGuardedFieldIssue{}, fmt.Errorf("%w: guarded Jira update query exceeds 64 KiB", domain.ErrCheckFailed)
	}
	data, err := j.c.DoWithBodyLimit(ctx, http.MethodGet, path, nil, nil, domain.JiraGuardedUpdateMaxIssueResponseBytes)
	if err != nil {
		return domain.JiraGuardedFieldIssue{}, err
	}
	return decodeGuardedUpdateIssue(data, fields)
}

func decodeGuardedUpdateIssue(data []byte, fields []string) (domain.JiraGuardedFieldIssue, error) {
	var root map[string]json.RawMessage
	if strictjson.Decode(data, &root) != nil || root == nil {
		return domain.JiraGuardedFieldIssue{}, guardedUpdateDecodeError("issue")
	}
	var id, key string
	if strictjson.Decode(root["id"], &id) != nil || strictjson.Decode(root["key"], &key) != nil ||
		len(id) > domain.JiraGuardedUpdateMaxImmutableIDBytes || !guardedLinkID(id) || !guardedLinkKey(key) {
		return domain.JiraGuardedFieldIssue{}, guardedUpdateDecodeError("issue")
	}
	var fieldObject map[string]json.RawMessage
	if strictjson.Decode(root["fields"], &fieldObject) != nil || fieldObject == nil {
		return domain.JiraGuardedFieldIssue{}, guardedUpdateDecodeError("issue")
	}
	var projectObject map[string]json.RawMessage
	if strictjson.Decode(fieldObject["project"], &projectObject) != nil || projectObject == nil {
		return domain.JiraGuardedFieldIssue{}, guardedUpdateDecodeError("issue")
	}
	var project, updated string
	if strictjson.Decode(projectObject["key"], &project) != nil || !guardedLinkProject(project) || !strings.HasPrefix(key, project+"-") ||
		strictjson.Decode(fieldObject["updated"], &updated) != nil || !guardedLabelUpdated(updated) || strings.TrimSpace(updated) != updated {
		return domain.JiraGuardedFieldIssue{}, guardedUpdateDecodeError("issue")
	}
	evidence := make(map[string]domain.JiraGuardedFieldEvidence, len(fields))
	for _, field := range fields {
		raw, present := fieldObject[field]
		if !present {
			return domain.JiraGuardedFieldIssue{}, guardedUpdateDecodeError("issue")
		}
		value, decodeErr := strictjson.DecodeValue(raw)
		if decodeErr != nil {
			return domain.JiraGuardedFieldIssue{}, guardedUpdateDecodeError("issue")
		}
		if !validGuardedUpdateObservedField(field, value) {
			return domain.JiraGuardedFieldIssue{}, guardedUpdateDecodeError("issue")
		}
		evidence[field] = domain.JiraGuardedFieldEvidence{Present: true, Value: value}
	}
	return domain.JiraGuardedFieldIssue{
		ID: id, Key: key, Project: project, Updated: updated, Fields: evidence, Complete: true,
	}, nil
}

func validGuardedUpdateObservedField(field string, value any) bool {
	switch field {
	case "summary":
		text, ok := value.(string)
		return ok && text != ""
	case "description":
		if value == nil {
			return true
		}
		_, ok := value.(string)
		return ok
	default:
		return true
	}
}

// PrepareGuardedUpdate is the sole coercion and canonical payload owner for a
// whole issue update. Generic inputs must be catalog-qualified custom fields.
func (j *Jira) PrepareGuardedUpdate(request domain.JiraGuardedUpdatePreparationRequest) (domain.JiraGuardedUpdatePreparation, error) {
	qualified, err := guardedUpdateQualified(request.Qualified)
	if err != nil {
		return domain.JiraGuardedUpdatePreparation{}, err
	}
	totalFields := len(request.Fields)
	if request.SummaryPresent {
		totalFields++
	}
	if request.DescriptionPresent {
		totalFields++
	}
	inputBytes := int64(len(request.Summary)) + int64(len(request.Description))
	if inputBytes > domain.JiraGuardedUpdateMaxInputBytes {
		return domain.JiraGuardedUpdatePreparation{}, fmt.Errorf("%w: guarded Jira update input exceeds its bound", domain.ErrUsage)
	}
	for field, input := range request.Fields {
		inputBytes += int64(len(field)) + int64(len(input.Value))
		if inputBytes > domain.JiraGuardedUpdateMaxInputBytes {
			return domain.JiraGuardedUpdatePreparation{}, fmt.Errorf("%w: guarded Jira update input exceeds its bound", domain.ErrUsage)
		}
	}
	if len(request.Fields) != len(qualified) || totalFields == 0 || totalFields > domain.JiraGuardedUpdateMaxFields {
		return domain.JiraGuardedUpdatePreparation{}, fmt.Errorf("%w: guarded Jira update qualification is incomplete", domain.ErrCheckFailed)
	}
	values := make(map[string]any, len(request.Fields)+2)
	inputKinds := make(map[string]string, len(request.Fields)+2)
	if request.SummaryPresent {
		if request.Summary == "" || !utf8.ValidString(request.Summary) {
			return domain.JiraGuardedUpdatePreparation{}, fmt.Errorf("%w: guarded Jira update summary is invalid", domain.ErrUsage)
		}
		values["summary"], inputKinds["summary"] = request.Summary, "summary"
	}
	if request.DescriptionPresent {
		if request.DescriptionSource != "wiki" && request.DescriptionSource != "markdown" || !utf8.Valid(request.Description) {
			return domain.JiraGuardedUpdatePreparation{}, fmt.Errorf("%w: guarded Jira update description is invalid", domain.ErrUsage)
		}
		values["description"], inputKinds["description"] = string(request.Description), request.DescriptionSource
	} else if request.DescriptionSource != "none" || len(request.Description) != 0 {
		return domain.JiraGuardedUpdatePreparation{}, fmt.Errorf("%w: guarded Jira update description presence is inconsistent", domain.ErrUsage)
	}
	for field, input := range request.Fields {
		if !qualified[field] || !domain.ValidJiraGuardedUpdateCandidateField(field) {
			return domain.JiraGuardedUpdatePreparation{}, fmt.Errorf("%w: guarded Jira update field is not a qualified custom field", domain.ErrCheckFailed)
		}
		value, coerceErr := coerceGuardedUpdateField(input)
		if coerceErr != nil {
			return domain.JiraGuardedUpdatePreparation{}, fmt.Errorf("%w: guarded Jira update field %q is invalid", domain.ErrUsage, field)
		}
		values[field] = value
		if input.ExplicitJSON {
			inputKinds[field] = "explicit_json"
		} else {
			inputKinds[field] = "legacy"
		}
	}
	if len(values) == 0 {
		return domain.JiraGuardedUpdatePreparation{}, fmt.Errorf("%w: nothing to update", domain.ErrUsage)
	}
	return prepareGuardedUpdate(values, inputKinds)
}

// WriteGuardedUpdate validates and authorizes the immutable prepared request,
// then sends one exact PUT addressed only by numeric issue id.
func (j *Jira) WriteGuardedUpdate(ctx context.Context, write domain.JiraGuardedUpdateWrite) error {
	if err := validateGuardedUpdateWrite(write); err != nil {
		return &guardedUpdateNoAttemptError{cause: err}
	}
	cleared, err := j.authorize(ctx, domain.WriteVerbSet{domain.WriteVerbUpdate}, []domain.WriteTarget{{
		Service: "jira", Kind: "issue", Key: write.Key, Project: write.Project,
	}})
	if err != nil {
		return &guardedUpdateNoAttemptError{cause: err}
	}
	if err := validateGuardedUpdateWrite(write); err != nil {
		return &guardedUpdateNoAttemptError{cause: err}
	}
	_, err = j.c.DoWithBodyLimit(domain.WithSingleAttempt(domain.WithWriteClearance(cleared)), http.MethodPut,
		"/rest/api/2/issue/"+url.PathEscape(write.ID), bytes.Clone(write.Prepared.Payload),
		map[string]string{"Content-Type": "application/json"}, domain.JiraGuardedUpdateMaxWriteResponseBytes)
	return err
}

func prepareGuardedUpdate(values map[string]any, inputKinds map[string]string) (domain.JiraGuardedUpdatePreparation, error) {
	fields := make([]domain.JiraGuardedUpdatePreparedField, 0, len(values))
	for field, value := range values {
		projection, err := guardedUpdatePreparedField(field, inputKinds[field], value)
		if err != nil {
			return domain.JiraGuardedUpdatePreparation{}, err
		}
		fields = append(fields, projection)
	}
	sort.Slice(fields, func(a, b int) bool { return fields[a].FieldID < fields[b].FieldID })
	payload, err := json.Marshal(map[string]any{"fields": values})
	if err != nil || int64(len(payload)) > domain.JiraGuardedUpdateMaxPreparedBytes {
		return domain.JiraGuardedUpdatePreparation{}, fmt.Errorf("%w: guarded Jira update payload is invalid or oversized", domain.ErrUsage)
	}
	return domain.JiraGuardedUpdatePreparation{
		Payload: bytes.Clone(payload), Fields: append([]domain.JiraGuardedUpdatePreparedField(nil), fields...),
		Values: cloneGuardedUpdateValues(values),
	}, nil
}

func guardedUpdatePreparedField(field, inputKind string, value any) (domain.JiraGuardedUpdatePreparedField, error) {
	if !domain.ValidJiraGuardedFieldID(field) || !guardedUpdateInputKind(field, inputKind) ||
		!domain.JiraGuardedFieldValueWithinNestingBound(value) {
		return domain.JiraGuardedUpdatePreparedField{}, domain.ErrCheckFailed
	}
	canonical, err := json.Marshal(value)
	if err != nil || int64(len(canonical)) > domain.JiraGuardedUpdateMaxDesiredBytes {
		return domain.JiraGuardedUpdatePreparedField{}, domain.ErrCheckFailed
	}
	sum := sha256.Sum256(canonical)
	return domain.JiraGuardedUpdatePreparedField{
		FieldID: field, InputKind: inputKind, JSONKind: guardedCreateJSONKind(value),
		Bytes: len(canonical), SHA256: hex.EncodeToString(sum[:]),
	}, nil
}

func validateGuardedUpdateWrite(write domain.JiraGuardedUpdateWrite) error {
	if !guardedLinkID(write.ID) || len(write.ID) > domain.JiraGuardedUpdateMaxImmutableIDBytes ||
		!guardedLinkKey(write.Key) || !guardedLinkProject(write.Project) || !strings.HasPrefix(write.Key, write.Project+"-") ||
		len(write.Prepared.Payload) == 0 || int64(len(write.Prepared.Payload)) > domain.JiraGuardedUpdateMaxPreparedBytes {
		return domain.ErrCheckFailed
	}
	qualified, err := guardedUpdateQualified(write.Qualified)
	if err != nil {
		return err
	}
	var root map[string]json.RawMessage
	if strictjson.Decode(write.Prepared.Payload, &root) != nil || len(root) != 1 {
		return domain.ErrCheckFailed
	}
	var wireValues map[string]any
	if strictjson.Decode(root["fields"], &wireValues) != nil || len(wireValues) == 0 ||
		len(wireValues) != len(write.Prepared.Values) || len(wireValues) != len(write.Prepared.Fields) {
		return domain.ErrCheckFailed
	}
	wirePayload, wireErr := json.Marshal(map[string]any{"fields": wireValues})
	valuePayload, valueErr := json.Marshal(map[string]any{"fields": write.Prepared.Values})
	if wireErr != nil || valueErr != nil || !bytes.Equal(wirePayload, write.Prepared.Payload) || !bytes.Equal(valuePayload, write.Prepared.Payload) {
		return domain.ErrCheckFailed
	}
	projections := make([]domain.JiraGuardedUpdatePreparedField, 0, len(wireValues))
	customFields := 0
	for field, value := range wireValues {
		if field != "summary" && field != "description" && !qualified[field] {
			return domain.ErrCheckFailed
		}
		if field != "summary" && field != "description" {
			customFields++
		}
		inputKind := ""
		for _, projection := range write.Prepared.Fields {
			if projection.FieldID == field {
				inputKind = projection.InputKind
				break
			}
		}
		projection, projectionErr := guardedUpdatePreparedField(field, inputKind, value)
		if projectionErr != nil {
			return projectionErr
		}
		projections = append(projections, projection)
	}
	sort.Slice(projections, func(a, b int) bool { return projections[a].FieldID < projections[b].FieldID })
	if !reflect.DeepEqual(projections, write.Prepared.Fields) {
		return domain.ErrCheckFailed
	}
	if customFields != len(qualified) {
		return domain.ErrCheckFailed
	}
	return nil
}

func guardedUpdateFieldIDs(fields []string) ([]string, error) {
	if len(fields) == 0 || len(fields) > domain.JiraGuardedUpdateMaxFields {
		return nil, fmt.Errorf("%w: guarded Jira update selection is out of bounds", domain.ErrUsage)
	}
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if !domain.ValidJiraGuardedFieldID(field) || field == "project" || field == "updated" || seen[field] {
			return nil, fmt.Errorf("%w: guarded Jira update selection is invalid", domain.ErrUsage)
		}
		seen[field] = true
		out = append(out, field)
	}
	sort.Strings(out)
	return out, nil
}

func guardedUpdateQualified(entries []domain.JiraGuardedFieldCatalogEntry) (map[string]bool, error) {
	if len(entries) > domain.JiraGuardedUpdateMaxFields {
		return nil, domain.ErrCheckFailed
	}
	out := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if !entry.Custom || !domain.ValidJiraGuardedUpdateCandidateField(entry.ID) || out[entry.ID] {
			return nil, domain.ErrCheckFailed
		}
		out[entry.ID] = true
	}
	return out, nil
}

func coerceGuardedUpdateField(input domain.JiraFieldInput) (any, error) {
	if !utf8.ValidString(input.Value) {
		return nil, domain.ErrUsage
	}
	if input.ExplicitJSON {
		return strictjson.DecodeValue([]byte(input.Value))
	}
	trimmed := strings.TrimSpace(input.Value)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		if !json.Valid([]byte(input.Value)) {
			return nil, domain.ErrUsage
		}
		value, err := strictjson.DecodeValue([]byte(input.Value))
		if err != nil {
			return nil, err
		}
		switch value.(type) {
		case map[string]any, []any:
			return value, nil
		}
	}
	return input.Value, nil
}

func guardedUpdateInputKind(field, inputKind string) bool {
	switch field {
	case "summary":
		return inputKind == "summary"
	case "description":
		return inputKind == "wiki" || inputKind == "markdown"
	default:
		return inputKind == "legacy" || inputKind == "explicit_json"
	}
}

func cloneGuardedUpdateValues(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = cloneGuardedUpdateValue(value)
	}
	return out
}

func cloneGuardedUpdateValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, member := range typed {
			out[key] = cloneGuardedUpdateValue(member)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, member := range typed {
			out[index] = cloneGuardedUpdateValue(member)
		}
		return out
	default:
		return value
	}
}

func guardedUpdateDecodeError(owner string) error {
	return fmt.Errorf("%w: Jira returned malformed or incomplete guarded update %s evidence", domain.ErrCheckFailed, owner)
}
