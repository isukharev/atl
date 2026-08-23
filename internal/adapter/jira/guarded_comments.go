package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraGuardedCommentReferenceMaxBytes = 64
	jiraGuardedCommentBodyMaxBytes      = 1 << 20
)

var _ domain.JiraGuardedCommentPort = (*Jira)(nil)

type guardedCommentNoAttemptError struct{ cause error }

func (e *guardedCommentNoAttemptError) Error() string {
	return "guarded Jira comment write was denied before dispatch"
}
func (e *guardedCommentNoAttemptError) Unwrap() error                  { return e.cause }
func (e *guardedCommentNoAttemptError) DiagnosticWriteAttempted() bool { return false }

// ReadGuardedCommentIssue performs one strict issue qualification read. A
// requested key and every later immutable-id read must produce the same exact
// canonical key/project relationship at the app boundary.
func (j *Jira) ReadGuardedCommentIssue(ctx context.Context, reference string) (domain.JiraGuardedCommentIssue, error) {
	if len(reference) == 0 || len(reference) > jiraGuardedCommentReferenceMaxBytes ||
		(!domain.ValidJiraIssueKey(reference) && !domain.ValidConfluenceContentID(reference)) {
		return domain.JiraGuardedCommentIssue{}, guardedCommentDecodeError()
	}
	path := "/rest/api/2/issue/" + url.PathEscape(reference) + "?fields=project,updated"
	data, err := j.c.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return domain.JiraGuardedCommentIssue{}, err
	}
	root, ok := guardedCommentObject(data)
	if !ok {
		return domain.JiraGuardedCommentIssue{}, guardedCommentDecodeError()
	}
	id, okID := guardedCommentRequiredString(root, "id")
	key, okKey := guardedCommentRequiredString(root, "key")
	fieldsRaw, fieldsPresent := root["fields"]
	fields, okFields := guardedCommentRawObject(fieldsRaw, fieldsPresent)
	if !okID || !okKey || !okFields || !guardedLinkID(id) || !guardedLinkKey(key) {
		return domain.JiraGuardedCommentIssue{}, guardedCommentDecodeError()
	}
	projectRaw, projectPresent := fields["project"]
	projectObject, okProject := guardedCommentRawObject(projectRaw, projectPresent)
	project, okProjectKey := guardedCommentRequiredString(projectObject, "key")
	updated, okUpdated := guardedCommentRequiredString(fields, "updated")
	if !okProject || !okProjectKey || !guardedLinkProject(project) || !strings.HasPrefix(key, project+"-") ||
		!okUpdated || strings.TrimSpace(updated) != updated || !domain.ValidJiraGuardedCommentInstant(updated) {
		return domain.JiraGuardedCommentIssue{}, guardedCommentDecodeError()
	}
	return domain.JiraGuardedCommentIssue{ID: id, Key: key, Project: project, Updated: updated, Complete: true}, nil
}

// ReadGuardedCommentActor qualifies only stable Jira Data Center name/key
// members. Display name, account id, email, and other profile fields never
// enter guarded evidence.
func (j *Jira) ReadGuardedCommentActor(ctx context.Context) (domain.JiraGuardedCommentActor, error) {
	data, err := j.c.Do(ctx, http.MethodGet, "/rest/api/2/myself", nil, nil)
	if err != nil {
		return domain.JiraGuardedCommentActor{}, err
	}
	root, ok := guardedCommentObject(data)
	if !ok {
		return domain.JiraGuardedCommentActor{}, guardedCommentDecodeError()
	}
	name, okName := guardedCommentRequiredString(root, "name")
	key, okKey := guardedCommentRequiredString(root, "key")
	if !okName || !okKey || !domain.ValidJiraCommentEvidenceMetadata(name, false) ||
		!domain.ValidJiraCommentEvidenceMetadata(key, false) || strings.TrimSpace(name) != name || strings.TrimSpace(key) != key {
		return domain.JiraGuardedCommentActor{}, guardedCommentDecodeError()
	}
	return domain.JiraGuardedCommentActor{Name: name, Key: key, Complete: true}, nil
}

// WriteGuardedComment validates and authorizes the already-qualified exact
// request immediately before one numeric-ID POST. It performs no hidden read.
func (j *Jira) WriteGuardedComment(ctx context.Context, write domain.JiraGuardedCommentWrite) (domain.JiraGuardedCommentAcknowledgement, error) {
	if !guardedLinkID(write.ID) || !guardedLinkKey(write.Key) || !guardedLinkProject(write.Project) ||
		!strings.HasPrefix(write.Key, write.Project+"-") || len(write.Body) == 0 ||
		len(write.Body) > jiraGuardedCommentBodyMaxBytes || !utf8.Valid(write.Body) || strings.TrimSpace(string(write.Body)) == "" {
		return domain.JiraGuardedCommentAcknowledgement{}, &guardedCommentNoAttemptError{cause: domain.ErrCheckFailed}
	}
	cleared, err := j.authorize(ctx, domain.WriteVerbSet{domain.WriteVerbComment}, []domain.WriteTarget{{
		Service: "jira", Kind: "issue", Key: write.Key, Project: write.Project,
	}})
	if err != nil {
		return domain.JiraGuardedCommentAcknowledgement{}, &guardedCommentNoAttemptError{cause: err}
	}
	payload := struct {
		Body string `json:"body"`
	}{Body: string(write.Body)}
	var data json.RawMessage
	err = j.c.SendJSON(domain.WithWriteClearance(cleared), http.MethodPost,
		"/rest/api/2/issue/"+url.PathEscape(write.ID)+"/comment", payload, &data)
	if err != nil {
		return domain.JiraGuardedCommentAcknowledgement{}, err
	}
	root, ok := guardedCommentObject(data)
	if !ok {
		return domain.JiraGuardedCommentAcknowledgement{}, guardedCommentAcknowledgementError()
	}
	id, present := guardedCommentRequiredString(root, "id")
	if !present || !guardedLinkID(id) {
		return domain.JiraGuardedCommentAcknowledgement{}, guardedCommentAcknowledgementError()
	}
	return domain.JiraGuardedCommentAcknowledgement{ID: id}, nil
}

func guardedCommentObject(data []byte) (map[string]json.RawMessage, bool) {
	if !utf8.Valid(data) || !guardedLabelUnicodeEscapesValid(data) || !guardedCommentUniqueJSONMembers(data) {
		return nil, false
	}
	var value map[string]json.RawMessage
	if decodeOneJSON(data, &value) != nil || value == nil {
		return nil, false
	}
	return value, true
}

func guardedCommentRawObject(raw json.RawMessage, present bool) (map[string]json.RawMessage, bool) {
	if !present || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	return guardedCommentObject(raw)
}

func guardedCommentUniqueJSONMembers(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if !guardedCommentUniqueJSONValue(decoder) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func guardedCommentUniqueJSONValue(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return true
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			member, err := decoder.Token()
			name, stringMember := member.(string)
			if err != nil || !stringMember {
				return false
			}
			if _, duplicate := seen[name]; duplicate {
				return false
			}
			seen[name] = struct{}{}
			if !guardedCommentUniqueJSONValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for decoder.More() {
			if !guardedCommentUniqueJSONValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}

func guardedCommentRequiredString(object map[string]json.RawMessage, name string) (string, bool) {
	value, present := guardedCommentPresentString(object, name)
	return value, present && value != ""
}

func guardedCommentPresentString(object map[string]json.RawMessage, name string) (string, bool) {
	raw, present := object[name]
	if !present || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var value string
	if decodeOneJSON(raw, &value) != nil || !utf8.ValidString(value) {
		return "", false
	}
	return value, true
}

func guardedCommentOptionalString(object map[string]json.RawMessage, name string) (string, bool) {
	raw, present := object[name]
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true
	}
	return guardedCommentPresentString(object, name)
}

func guardedCommentRequiredInt(object map[string]json.RawMessage, name string) (int, bool) {
	raw, present := object[name]
	if !present || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, false
	}
	var value int
	if decodeOneJSON(raw, &value) != nil {
		return 0, false
	}
	return value, true
}

func guardedCommentArray(object map[string]json.RawMessage, name string) ([]json.RawMessage, bool) {
	raw, present := object[name]
	if !present || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	var values []json.RawMessage
	if decodeOneJSON(raw, &values) != nil || values == nil {
		return nil, false
	}
	return values, true
}

func guardedCommentDecodeError() error {
	return fmt.Errorf("%w: Jira returned malformed or incomplete guarded comment evidence", domain.ErrCheckFailed)
}

func guardedCommentAcknowledgementError() error {
	return fmt.Errorf("%w: Jira returned a malformed guarded comment acknowledgement", domain.ErrCheckFailed)
}
