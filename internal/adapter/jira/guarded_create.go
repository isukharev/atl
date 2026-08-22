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
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraGuardedCreateMaxPayloadBytes = 64 << 20
	jiraGuardedCreateMaxFields       = 1024
	jiraGuardedCreateMaxStringBytes  = 1024
	jiraGuardedCreateMaxQueryBytes   = 64 << 10
)

var _ domain.JiraGuardedCreatePort = (*Jira)(nil)

type guardedCreateNoAttemptError struct{ cause error }

func (e *guardedCreateNoAttemptError) Error() string {
	return "guarded Jira create was denied before dispatch"
}
func (e *guardedCreateNoAttemptError) Unwrap() error                  { return e.cause }
func (e *guardedCreateNoAttemptError) DiagnosticWriteAttempted() bool { return false }

// PrepareGuardedCreate is the sole create-payload normalization owner. Legacy
// Create and the guarded workflow both use these exact bytes.
func (j *Jira) PrepareGuardedCreate(request domain.JiraGuardedCreatePreparationRequest) (domain.JiraGuardedCreatePreparation, error) {
	typedFields, err := coerceCreateFields(request.Fields)
	if err != nil {
		return domain.JiraGuardedCreatePreparation{}, err
	}
	fields := map[string]any{
		"project":   map[string]string{"key": request.ProjectKey},
		"issuetype": map[string]string{"id": request.IssueTypeID},
		"summary":   request.Summary,
	}
	if request.DescriptionPresent && len(request.Description) > 0 {
		fields["description"] = string(request.Description)
	}
	projection := make([]domain.JiraGuardedCreatePreparedField, 0, len(typedFields))
	keys := make([]string, 0, len(typedFields))
	for key := range typedFields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := typedFields[key]
		fields[key] = value
		normalized, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return domain.JiraGuardedCreatePreparation{}, fmt.Errorf("%w: normalize Jira create field", domain.ErrUsage)
		}
		sum := sha256.Sum256(normalized)
		inputKind := "legacy"
		if request.Fields[key].ExplicitJSON {
			inputKind = "explicit_json"
		}
		projection = append(projection, domain.JiraGuardedCreatePreparedField{
			FieldID: key, InputKind: inputKind, JSONKind: guardedCreateJSONKind(value),
			SHA256: hex.EncodeToString(sum[:]), Bytes: len(normalized),
		})
	}
	payload, err := json.Marshal(map[string]any{"fields": fields})
	if err != nil {
		return domain.JiraGuardedCreatePreparation{}, err
	}
	if len(payload) > jiraGuardedCreateMaxPayloadBytes {
		return domain.JiraGuardedCreatePreparation{}, fmt.Errorf("%w: Jira create payload exceeds 64 MiB", domain.ErrUsage)
	}
	return domain.JiraGuardedCreatePreparation{Payload: append([]byte(nil), payload...), Fields: projection}, nil
}

func guardedCreateJSONKind(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case json.Number, float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

// WriteGuardedCreate sends the immutable reviewed bytes once. It authorizes
// the already-qualified project directly and performs no identity lookup.
func (j *Jira) WriteGuardedCreate(ctx context.Context, write domain.JiraGuardedCreateWrite) (domain.JiraGuardedCreateAcknowledgement, error) {
	if !guardedLinkID(write.ProjectID) || !guardedLinkProject(write.ProjectKey) || len(write.Payload) == 0 || len(write.Payload) > jiraGuardedCreateMaxPayloadBytes || !json.Valid(write.Payload) || !guardedCreatePayloadProject(write.Payload, write.ProjectKey) {
		return domain.JiraGuardedCreateAcknowledgement{}, &guardedCreateNoAttemptError{cause: domain.ErrCheckFailed}
	}
	cleared, err := j.authorize(ctx, domain.WriteVerbSet{domain.WriteVerbCreate}, []domain.WriteTarget{{Service: "jira", Kind: "issue", Project: write.ProjectKey}})
	if err != nil {
		return domain.JiraGuardedCreateAcknowledgement{}, &guardedCreateNoAttemptError{cause: err}
	}
	data, err := j.c.Do(domain.WithWriteClearance(cleared), http.MethodPost, "/rest/api/2/issue", append([]byte(nil), write.Payload...), nil)
	if err != nil {
		return domain.JiraGuardedCreateAcknowledgement{}, err
	}
	var response struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := decodeOneJSON(data, &response); err != nil {
		return domain.JiraGuardedCreateAcknowledgement{}, fmt.Errorf("%w: Jira create acknowledgement is malformed", domain.ErrCheckFailed)
	}
	ack := domain.JiraGuardedCreateAcknowledgement{}
	if guardedLinkID(response.ID) {
		ack.ID = response.ID
	}
	if response.Key != "" {
		if !guardedLinkKey(response.Key) || !strings.HasPrefix(response.Key, write.ProjectKey+"-") {
			return ack, fmt.Errorf("%w: Jira create acknowledgement key is malformed or outside the reviewed project", domain.ErrCheckFailed)
		}
		ack.Key = response.Key
	}
	if ack.ID == "" {
		return domain.JiraGuardedCreateAcknowledgement{}, fmt.Errorf("%w: Jira create acknowledgement omitted a canonical immutable id", domain.ErrCheckFailed)
	}
	return ack, nil
}

func guardedCreatePayloadProject(payload []byte, project string) bool {
	var envelope struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if decodeOneJSON(payload, &envelope) != nil || envelope.Fields == nil {
		return false
	}
	var identity struct {
		Key string `json:"key"`
	}
	return decodeOneJSON(envelope.Fields["project"], &identity) == nil && identity.Key == project
}

// ReadGuardedCreate performs the sole immutable-id readback. The final encoded
// query and complete route are both capped at 64 KiB.
func (j *Jira) ReadGuardedCreate(ctx context.Context, request domain.JiraGuardedCreateReadRequest) (domain.JiraGuardedCreateReadback, error) {
	if !guardedLinkID(request.ID) {
		return domain.JiraGuardedCreateReadback{}, guardedCreateDecodeError()
	}
	fields, err := guardedCreateReadFields(request.Fields)
	if err != nil {
		return domain.JiraGuardedCreateReadback{}, err
	}
	query := url.Values{"fields": []string{strings.Join(fields, ",")}}.Encode()
	path := "/rest/api/2/issue/" + url.PathEscape(request.ID) + "?" + query
	if len(query) > jiraGuardedCreateMaxQueryBytes || len(path) > jiraGuardedCreateMaxQueryBytes {
		return domain.JiraGuardedCreateReadback{}, fmt.Errorf("%w: guarded Jira create readback query exceeds 64 KiB", domain.ErrCheckFailed)
	}
	data, err := j.c.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return domain.JiraGuardedCreateReadback{}, err
	}
	var response struct {
		ID     string                     `json:"id"`
		Key    string                     `json:"key"`
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := decodeOneJSON(data, &response); err != nil || !guardedLinkID(response.ID) || !guardedLinkKey(response.Key) || response.Fields == nil {
		return domain.JiraGuardedCreateReadback{}, guardedCreateDecodeError()
	}
	project, issueType := struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}{}, struct {
		ID string `json:"id"`
	}{}
	if decodeOneJSON(response.Fields["project"], &project) != nil || !guardedLinkID(project.ID) || !guardedLinkProject(project.Key) || decodeOneJSON(response.Fields["issuetype"], &issueType) != nil || !guardedLinkID(issueType.ID) {
		return domain.JiraGuardedCreateReadback{}, guardedCreateDecodeError()
	}
	var summary string
	if decodeOneJSON(response.Fields["summary"], &summary) != nil || len(summary) > jiraGuardedCreateMaxPayloadBytes || !utf8.ValidString(summary) {
		return domain.JiraGuardedCreateReadback{}, guardedCreateDecodeError()
	}
	out := domain.JiraGuardedCreateReadback{
		ID: response.ID, Key: response.Key, ProjectID: project.ID, ProjectKey: project.Key,
		IssueTypeID: issueType.ID, Summary: summary, Fields: make(map[string]domain.JiraGuardedCreateFieldEvidence, len(fields)),
	}
	for _, field := range fields {
		raw, present := response.Fields[field]
		evidence := domain.JiraGuardedCreateFieldEvidence{Present: present}
		if present {
			var value any
			if decodeOneJSON(raw, &value) != nil {
				return domain.JiraGuardedCreateReadback{}, guardedCreateDecodeError()
			}
			evidence.Value = value
		}
		out.Fields[field] = evidence
		switch field {
		case "description":
			out.Description = evidence
		case "created":
			out.Created = evidence
		case "updated":
			out.Updated = evidence
		}
	}
	return out, nil
}

func guardedCreateReadFields(requested []string) ([]string, error) {
	all := append([]string{"project", "issuetype", "summary", "description", "created", "updated"}, requested...)
	seen := make(map[string]struct{}, len(all))
	out := make([]string, 0, len(all))
	for _, field := range all {
		if field == "" || len(field) > jiraGuardedCreateMaxStringBytes || !utf8.ValidString(field) || strings.ContainsAny(field, "\x00\r\n") {
			return nil, guardedCreateDecodeError()
		}
		if _, duplicate := seen[field]; duplicate {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	if len(out) > jiraGuardedCreateMaxFields {
		return nil, fmt.Errorf("%w: guarded Jira create readback exceeds 1024 fields", domain.ErrCheckFailed)
	}
	sort.Strings(out)
	return out, nil
}

func guardedCreateDecodeError() error {
	return fmt.Errorf("%w: Jira returned malformed or incomplete guarded create evidence", domain.ErrCheckFailed)
}

// clonePreparedPayload makes the legacy response path visibly independent of
// the caller-owned map and of later guarded comparisons.
func clonePreparedPayload(payload []byte) []byte { return bytes.Clone(payload) }
