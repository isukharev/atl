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

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

var _ domain.JiraGuardedFieldPort = (*Jira)(nil)

type guardedFieldNoAttemptError struct{ cause error }

func (e *guardedFieldNoAttemptError) Error() string {
	return "guarded Jira field write was denied before dispatch"
}
func (e *guardedFieldNoAttemptError) Unwrap() error                  { return e.cause }
func (e *guardedFieldNoAttemptError) DiagnosticWriteAttempted() bool { return false }

// ReadGuardedFieldCatalog strictly qualifies the complete bounded Jira field
// catalog and returns only sorted selected custom-field evidence.
func (j *Jira) ReadGuardedFieldCatalog(ctx context.Context, selected []string) (domain.JiraGuardedFieldCatalog, error) {
	selectedSet, err := guardedFieldIDs(selected, domain.JiraGuardedFieldMaxSelected)
	if err != nil {
		return domain.JiraGuardedFieldCatalog{}, err
	}
	data, err := j.c.DoWithBodyLimit(ctx, http.MethodGet, "/rest/api/2/field", nil, nil, domain.JiraGuardedFieldMaxCatalogResponseBytes)
	if err != nil {
		return domain.JiraGuardedFieldCatalog{}, err
	}
	var rows []json.RawMessage
	if strictjson.Decode(data, &rows) != nil || rows == nil || len(rows) > domain.JiraGuardedFieldMaxCatalogEntries {
		return domain.JiraGuardedFieldCatalog{}, guardedFieldDecodeError("catalog")
	}
	qualified := make(map[string]domain.JiraGuardedFieldCatalogEntry, len(selectedSet))
	seen := make(map[string]struct{}, len(rows))
	for _, raw := range rows {
		var row map[string]json.RawMessage
		if strictjson.Decode(raw, &row) != nil || row == nil {
			return domain.JiraGuardedFieldCatalog{}, guardedFieldDecodeError("catalog")
		}
		var id string
		idRaw, idPresent := row["id"]
		customRaw, customPresent := row["custom"]
		var custom bool
		if !idPresent || strictjson.Decode(idRaw, &id) != nil || !domain.ValidJiraGuardedFieldID(id) ||
			!customPresent || strictjson.Decode(customRaw, &custom) != nil {
			return domain.JiraGuardedFieldCatalog{}, guardedFieldDecodeError("catalog")
		}
		if _, duplicate := seen[id]; duplicate {
			return domain.JiraGuardedFieldCatalog{}, guardedFieldDecodeError("catalog")
		}
		seen[id] = struct{}{}
		if selectedSet[id] {
			if !custom || domain.JiraGuardedFieldReserved(id) {
				return domain.JiraGuardedFieldCatalog{}, guardedFieldDecodeError("catalog")
			}
			qualified[id] = domain.JiraGuardedFieldCatalogEntry{ID: id, Custom: true}
		}
	}
	if len(qualified) != len(selectedSet) {
		return domain.JiraGuardedFieldCatalog{}, guardedFieldDecodeError("catalog")
	}
	fields := make([]domain.JiraGuardedFieldCatalogEntry, 0, len(qualified))
	for _, entry := range qualified {
		fields = append(fields, entry)
	}
	sort.Slice(fields, func(i, k int) bool { return fields[i].ID < fields[k].ID })
	return domain.JiraGuardedFieldCatalog{Fields: fields, Complete: true}, nil
}

// ReadGuardedFieldIssue performs one strict key-or-immutable-id issue read and
// requires presence evidence for every selected field, including explicit null.
func (j *Jira) ReadGuardedFieldIssue(ctx context.Context, reference string, selected []string) (domain.JiraGuardedFieldIssue, error) {
	if len(reference) == 0 || len(reference) > domain.JiraGuardedFieldMaxImmutableIDBytes ||
		(!domain.ValidJiraIssueKey(reference) && !domain.ValidConfluenceContentID(reference)) {
		return domain.JiraGuardedFieldIssue{}, guardedFieldDecodeError("issue")
	}
	selectedSet, err := guardedFieldIDs(selected, domain.JiraGuardedFieldMaxSelected)
	if err != nil {
		return domain.JiraGuardedFieldIssue{}, err
	}
	fields := make([]string, 0, len(selectedSet)+2)
	for field := range selectedSet {
		fields = append(fields, field)
	}
	fields = append(fields, "project", "updated")
	sort.Strings(fields)
	query := url.Values{"fields": []string{strings.Join(fields, ",")}}.Encode()
	path := "/rest/api/2/issue/" + url.PathEscape(reference) + "?" + query
	if len(query) > domain.JiraGuardedFieldMaxQueryAndPathBytes || len(path) > domain.JiraGuardedFieldMaxQueryAndPathBytes {
		return domain.JiraGuardedFieldIssue{}, fmt.Errorf("%w: guarded Jira field issue query exceeds 64 KiB", domain.ErrCheckFailed)
	}
	data, err := j.c.DoWithBodyLimit(ctx, http.MethodGet, path, nil, nil, domain.JiraGuardedFieldMaxIssueResponseBytes)
	if err != nil {
		return domain.JiraGuardedFieldIssue{}, err
	}
	var root map[string]json.RawMessage
	if strictjson.Decode(data, &root) != nil || root == nil {
		return domain.JiraGuardedFieldIssue{}, guardedFieldDecodeError("issue")
	}
	var id, key string
	if strictjson.Decode(root["id"], &id) != nil || strictjson.Decode(root["key"], &key) != nil ||
		len(id) > domain.JiraGuardedFieldMaxImmutableIDBytes || !guardedLinkID(id) || !guardedLinkKey(key) {
		return domain.JiraGuardedFieldIssue{}, guardedFieldDecodeError("issue")
	}
	fieldsRaw, fieldsPresent := root["fields"]
	var fieldObject map[string]json.RawMessage
	if !fieldsPresent || strictjson.Decode(fieldsRaw, &fieldObject) != nil || fieldObject == nil {
		return domain.JiraGuardedFieldIssue{}, guardedFieldDecodeError("issue")
	}
	var projectObject map[string]json.RawMessage
	projectRaw, projectPresent := fieldObject["project"]
	if !projectPresent || strictjson.Decode(projectRaw, &projectObject) != nil || projectObject == nil {
		return domain.JiraGuardedFieldIssue{}, guardedFieldDecodeError("issue")
	}
	var project, updated string
	updatedRaw, updatedPresent := fieldObject["updated"]
	if strictjson.Decode(projectObject["key"], &project) != nil || !guardedLinkProject(project) || !strings.HasPrefix(key, project+"-") ||
		!updatedPresent || strictjson.Decode(updatedRaw, &updated) != nil || updated == "" || strings.TrimSpace(updated) != updated || !guardedLabelUpdated(updated) {
		return domain.JiraGuardedFieldIssue{}, guardedFieldDecodeError("issue")
	}
	evidence := make(map[string]domain.JiraGuardedFieldEvidence, len(selectedSet))
	for field := range selectedSet {
		raw, present := fieldObject[field]
		if !present {
			return domain.JiraGuardedFieldIssue{}, guardedFieldDecodeError("issue")
		}
		value, decodeErr := strictjson.DecodeValue(raw)
		if decodeErr != nil {
			return domain.JiraGuardedFieldIssue{}, guardedFieldDecodeError("issue")
		}
		evidence[field] = domain.JiraGuardedFieldEvidence{Present: true, Value: value}
	}
	return domain.JiraGuardedFieldIssue{
		ID: id, Key: key, Project: project, Updated: updated, Fields: evidence, Complete: true,
	}, nil
}

// PrepareGuardedFields is the sole deterministic guarded field payload owner.
func (j *Jira) PrepareGuardedFields(request domain.JiraGuardedFieldPreparationRequest) (domain.JiraGuardedFieldPreparation, error) {
	qualified, err := guardedFieldQualified(request.Qualified)
	if err != nil || len(request.Values) == 0 || len(request.Values) != len(qualified) {
		return domain.JiraGuardedFieldPreparation{}, fmt.Errorf("%w: guarded Jira field preparation is incomplete", domain.ErrCheckFailed)
	}
	fields := make([]domain.JiraGuardedFieldPreparedProjection, 0, len(request.Values))
	for field, value := range request.Values {
		if !qualified[field] || domain.JiraGuardedFieldReserved(field) {
			return domain.JiraGuardedFieldPreparation{}, fmt.Errorf("%w: guarded Jira field preparation is unauthorized", domain.ErrCheckFailed)
		}
		projection, projectionErr := guardedPreparedProjection(field, value)
		if projectionErr != nil {
			return domain.JiraGuardedFieldPreparation{}, projectionErr
		}
		fields = append(fields, projection)
	}
	sort.Slice(fields, func(i, k int) bool { return fields[i].FieldID < fields[k].FieldID })
	payload, err := json.Marshal(map[string]any{"fields": request.Values})
	if err != nil {
		return domain.JiraGuardedFieldPreparation{}, fmt.Errorf("%w: normalize guarded Jira field payload", domain.ErrUsage)
	}
	if int64(len(payload)) > domain.JiraGuardedFieldMaxPreparedBytes {
		return domain.JiraGuardedFieldPreparation{}, fmt.Errorf("%w: guarded Jira field payload exceeds 64 MiB", domain.ErrUsage)
	}
	return domain.JiraGuardedFieldPreparation{Payload: bytes.Clone(payload), Fields: fields}, nil
}

// WriteGuardedFields validates, authorizes, and dispatches one exact numeric-ID
// PUT without identity reads, redirects, retries, or remarshal.
func (j *Jira) WriteGuardedFields(ctx context.Context, write domain.JiraGuardedFieldWrite) error {
	if err := validateGuardedFieldWrite(write); err != nil {
		return &guardedFieldNoAttemptError{cause: err}
	}
	cleared, err := j.authorize(ctx, domain.WriteVerbSet{domain.WriteVerbUpdate}, []domain.WriteTarget{{
		Service: "jira", Kind: "issue", Key: write.Key, Project: write.Project,
	}})
	if err != nil {
		return &guardedFieldNoAttemptError{cause: err}
	}
	if err := validateGuardedFieldWrite(write); err != nil {
		return &guardedFieldNoAttemptError{cause: err}
	}
	_, err = j.c.DoWithBodyLimit(domain.WithSingleAttempt(domain.WithWriteClearance(cleared)), http.MethodPut, "/rest/api/2/issue/"+url.PathEscape(write.ID),
		bytes.Clone(write.Prepared.Payload), map[string]string{"Content-Type": "application/json"}, domain.JiraGuardedFieldMaxWriteResponseBytes)
	return err
}

func validateGuardedFieldWrite(write domain.JiraGuardedFieldWrite) error {
	if !guardedLinkID(write.ID) || len(write.ID) > domain.JiraGuardedFieldMaxImmutableIDBytes ||
		!guardedLinkKey(write.Key) || !guardedLinkProject(write.Project) || !strings.HasPrefix(write.Key, write.Project+"-") ||
		len(write.Prepared.Payload) == 0 || int64(len(write.Prepared.Payload)) > domain.JiraGuardedFieldMaxPreparedBytes {
		return domain.ErrCheckFailed
	}
	qualified, err := guardedFieldQualified(write.Qualified)
	if err != nil {
		return err
	}
	var envelope map[string]json.RawMessage
	if strictjson.Decode(write.Prepared.Payload, &envelope) != nil || len(envelope) != 1 {
		return domain.ErrCheckFailed
	}
	fieldsRaw, present := envelope["fields"]
	var values map[string]any
	if !present || strictjson.Decode(fieldsRaw, &values) != nil || len(values) == 0 || len(values) != len(qualified) {
		return domain.ErrCheckFailed
	}
	canonical, marshalErr := json.Marshal(map[string]any{"fields": values})
	if marshalErr != nil || !bytes.Equal(canonical, write.Prepared.Payload) {
		return domain.ErrCheckFailed
	}
	projections := make([]domain.JiraGuardedFieldPreparedProjection, 0, len(values))
	for field, value := range values {
		if !qualified[field] || domain.JiraGuardedFieldReserved(field) {
			return domain.ErrCheckFailed
		}
		projection, projectionErr := guardedPreparedProjection(field, value)
		if projectionErr != nil {
			return projectionErr
		}
		projections = append(projections, projection)
	}
	sort.Slice(projections, func(i, k int) bool { return projections[i].FieldID < projections[k].FieldID })
	if !reflect.DeepEqual(projections, write.Prepared.Fields) {
		return domain.ErrCheckFailed
	}
	return nil
}

func guardedPreparedProjection(field string, value any) (domain.JiraGuardedFieldPreparedProjection, error) {
	if !domain.ValidJiraGuardedFieldID(field) || domain.JiraGuardedFieldReserved(field) {
		return domain.JiraGuardedFieldPreparedProjection{}, domain.ErrCheckFailed
	}
	if !domain.JiraGuardedFieldValueWithinNestingBound(value) {
		return domain.JiraGuardedFieldPreparedProjection{}, fmt.Errorf("%w: guarded Jira field value exceeds the supported nesting bound", domain.ErrUsage)
	}
	var kind string
	var normalized []byte
	switch typedValue := value.(type) {
	case string:
		kind, normalized = "string", []byte(typedValue)
	case map[string]any:
		kind = "object"
	case []any:
		kind = "array"
	default:
		return domain.JiraGuardedFieldPreparedProjection{}, fmt.Errorf("%w: guarded field values must be strings, objects, or arrays", domain.ErrUsage)
	}
	if normalized == nil {
		var err error
		normalized, err = json.Marshal(value)
		if err != nil {
			return domain.JiraGuardedFieldPreparedProjection{}, fmt.Errorf("%w: normalize guarded field value", domain.ErrUsage)
		}
	}
	sum := sha256.Sum256(normalized)
	return domain.JiraGuardedFieldPreparedProjection{FieldID: field, Kind: kind, Bytes: len(normalized), SHA256: hex.EncodeToString(sum[:])}, nil
}

func guardedFieldIDs(fields []string, maximum int) (map[string]bool, error) {
	if len(fields) == 0 || len(fields) > maximum {
		return nil, fmt.Errorf("%w: guarded Jira field selection is out of bounds", domain.ErrUsage)
	}
	out := make(map[string]bool, len(fields))
	for _, field := range fields {
		if !domain.ValidJiraGuardedFieldID(field) || domain.JiraGuardedFieldReserved(field) || out[field] {
			return nil, fmt.Errorf("%w: guarded Jira field selection is invalid", domain.ErrUsage)
		}
		out[field] = true
	}
	return out, nil
}

func guardedFieldQualified(entries []domain.JiraGuardedFieldCatalogEntry) (map[string]bool, error) {
	if len(entries) == 0 || len(entries) > domain.JiraGuardedFieldMaxSelected {
		return nil, domain.ErrCheckFailed
	}
	out := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if !entry.Custom || !domain.ValidJiraGuardedFieldID(entry.ID) || domain.JiraGuardedFieldReserved(entry.ID) || out[entry.ID] {
			return nil, domain.ErrCheckFailed
		}
		out[entry.ID] = true
	}
	return out, nil
}

func guardedFieldDecodeError(owner string) error {
	return fmt.Errorf("%w: Jira returned malformed or incomplete guarded field %s evidence", domain.ErrCheckFailed, owner)
}
