package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

const (
	jiraGuardedLabelMaxBytes     = 255
	jiraGuardedLabelMaxDelta     = 100
	jiraGuardedLabelMaxCurrent   = 4096
	jiraGuardedLabelReferenceMax = 64
)

var _ domain.JiraGuardedLabelPort = (*Jira)(nil)

type guardedLabelNoAttemptError struct{ cause error }

func (e *guardedLabelNoAttemptError) Error() string {
	return "guarded Jira label write was denied before dispatch"
}
func (e *guardedLabelNoAttemptError) Unwrap() error                  { return e.cause }
func (e *guardedLabelNoAttemptError) DiagnosticWriteAttempted() bool { return false }

type guardedLabelSnapshotDTO struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Project json.RawMessage `json:"project"`
		Labels  json.RawMessage `json:"labels"`
		Updated json.RawMessage `json:"updated"`
	} `json:"fields"`
}

// ReadGuardedLabelSnapshot performs exactly one strict key-or-immutable-id
// issue read and requests only the fields needed by the guarded workflow.
func (j *Jira) ReadGuardedLabelSnapshot(ctx context.Context, reference string) (domain.JiraGuardedLabelSnapshot, error) {
	if len(reference) == 0 || len(reference) > jiraGuardedLabelReferenceMax ||
		(!domain.ValidJiraIssueKey(reference) && !domain.ValidConfluenceContentID(reference)) {
		return domain.JiraGuardedLabelSnapshot{}, guardedLabelDecodeError()
	}
	query := url.Values{}
	query.Set("fields", "project,labels,updated")
	path := "/rest/api/2/issue/" + url.PathEscape(reference) + "?" + query.Encode()
	data, err := j.c.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return domain.JiraGuardedLabelSnapshot{}, err
	}
	var response guardedLabelSnapshotDTO
	if strictjson.Decode(data, &response) != nil || !guardedLinkID(response.ID) || !guardedLinkKey(response.Key) {
		return domain.JiraGuardedLabelSnapshot{}, guardedLabelDecodeError()
	}
	var project struct {
		Key string `json:"key"`
	}
	if !jsonObjectPresent(response.Fields.Project) || json.Unmarshal(response.Fields.Project, &project) != nil ||
		!guardedLinkProject(project.Key) || !strings.HasPrefix(response.Key, project.Key+"-") {
		return domain.JiraGuardedLabelSnapshot{}, guardedLabelDecodeError()
	}
	if !jsonArrayPresent(response.Fields.Labels) {
		return domain.JiraGuardedLabelSnapshot{}, guardedLabelDecodeError()
	}
	var labels []string
	if json.Unmarshal(response.Fields.Labels, &labels) != nil || len(labels) > jiraGuardedLabelMaxCurrent {
		return domain.JiraGuardedLabelSnapshot{}, guardedLabelDecodeError()
	}
	if !guardedLabelSet(labels, jiraGuardedLabelMaxCurrent) {
		return domain.JiraGuardedLabelSnapshot{}, guardedLabelDecodeError()
	}
	var updated string
	if len(bytes.TrimSpace(response.Fields.Updated)) == 0 || json.Unmarshal(response.Fields.Updated, &updated) != nil ||
		updated == "" || strings.TrimSpace(updated) != updated || !guardedLabelUpdated(updated) {
		return domain.JiraGuardedLabelSnapshot{}, guardedLabelDecodeError()
	}
	sort.Strings(labels)
	return domain.JiraGuardedLabelSnapshot{
		ID: response.ID, Key: response.Key, Project: project.Key,
		Labels: labels, Updated: updated, Complete: true,
	}, nil
}

// WriteGuardedLabelDelta authorizes and dispatches one deterministic numeric-ID
// PUT. It performs no identity lookup and never calls the legacy label writer.
func (j *Jira) WriteGuardedLabelDelta(ctx context.Context, write domain.JiraGuardedLabelWrite) error {
	add, remove := append([]string(nil), write.Add...), append([]string(nil), write.Remove...)
	if !guardedLinkID(write.ID) || !guardedLinkKey(write.Key) || !guardedLinkProject(write.Project) ||
		!strings.HasPrefix(write.Key, write.Project+"-") || len(add)+len(remove) == 0 ||
		len(add)+len(remove) > jiraGuardedLabelMaxDelta || !guardedLabelSet(add, jiraGuardedLabelMaxDelta) ||
		!guardedLabelSet(remove, jiraGuardedLabelMaxDelta) || guardedLabelOverlap(add, remove) {
		return &guardedLabelNoAttemptError{cause: domain.ErrCheckFailed}
	}
	sort.Strings(add)
	sort.Strings(remove)
	cleared, err := j.authorize(ctx, domain.WriteVerbSet{domain.WriteVerbUpdate}, []domain.WriteTarget{{
		Service: "jira", Kind: "issue", Key: write.Key, Project: write.Project,
	}})
	if err != nil {
		return &guardedLabelNoAttemptError{cause: err}
	}
	ops := make([]map[string]string, 0, len(add)+len(remove))
	for _, label := range add {
		ops = append(ops, map[string]string{"add": label})
	}
	for _, label := range remove {
		ops = append(ops, map[string]string{"remove": label})
	}
	payload := map[string]any{"update": map[string]any{"labels": ops}}
	return j.c.SendJSON(domain.WithWriteClearance(cleared), http.MethodPut, "/rest/api/2/issue/"+url.PathEscape(write.ID), payload, nil)
}

func guardedLabelSet(labels []string, max int) bool {
	if len(labels) > max {
		return false
	}
	seen := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		if label == "" || len(label) > jiraGuardedLabelMaxBytes || !utf8.ValidString(label) {
			return false
		}
		if _, duplicate := seen[label]; duplicate {
			return false
		}
		seen[label] = struct{}{}
	}
	return true
}

func guardedLabelOverlap(add, remove []string) bool {
	seen := make(map[string]struct{}, len(add))
	for _, label := range add {
		seen[label] = struct{}{}
	}
	for _, label := range remove {
		if _, overlap := seen[label]; overlap {
			return true
		}
	}
	return false
}

func guardedLabelUpdated(value string) bool {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700", "2006-01-02T15:04:05-0700"} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func guardedLabelDecodeError() error {
	return fmt.Errorf("%w: Jira returned malformed or incomplete guarded label evidence", domain.ErrCheckFailed)
}
