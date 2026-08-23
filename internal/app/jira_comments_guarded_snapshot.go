package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
)

func (s *JiraService) buildGuardedCommentSnapshot(ctx context.Context, port domain.JiraGuardedCommentPort, reference, requestedKey, expectedID string, opts JiraCommentAddOpts) (*jiraGuardedCommentSnapshot, error) {
	actor, err := port.ReadGuardedCommentActor(ctx)
	if err != nil {
		return nil, errors.Join(domain.ErrCheckFailed, err)
	}
	if !validGuardedCommentActor(actor) {
		return nil, fmt.Errorf("%w: Jira returned malformed or incomplete authenticated actor evidence", domain.ErrCheckFailed)
	}
	issue, updatedTime, err := readGuardedCommentIssue(ctx, port, reference, requestedKey, expectedID)
	if err != nil {
		return nil, err
	}
	if requestedKey == "" {
		requestedKey = issue.Key
	}
	inventory, err := port.ListJiraCommentsQualified(ctx, issue.ID, domain.JiraCommentReadOptions{
		MaxPages: domain.JiraCommentReadMaxPages, MaxItems: domain.JiraCommentReadMaxItems, MaxBytes: jiraGuardedCommentMaxInventoryBytes,
	})
	if err != nil {
		return nil, err
	}
	records, err := qualifyGuardedCommentInventory(inventory)
	if err != nil {
		return nil, err
	}
	backendHash, err := backendid.OriginSHA256(s.baseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Jira backend identity", domain.ErrCheckFailed)
	}
	body := append([]byte(nil), opts.Body...)
	result := newJiraCommentAddResult(requestedKey, opts)
	result.BackendSHA256, result.IssueID, result.Key, result.Project, result.Updated = backendHash, issue.ID, issue.Key, issue.Project, issue.Updated
	result.BodySHA256, result.BodyBytes = sha256Hex(body), len(body)
	result.ActorSHA256 = guardedCommentActorHash(actor)
	result.CurrentCount, result.BaselineSHA256 = len(records), guardedCommentBaselineHash(records)
	for _, record := range records {
		if record.Body == string(body) {
			result.ExactBodyCount++
		}
	}
	result.Complete, result.Status = true, "would_apply"
	result.ProposalHash = guardedCommentProposalHash(result, body, actor, records)
	return &jiraGuardedCommentSnapshot{result: result, issue: issue, records: records, updatedTime: updatedTime}, nil
}

func (s *JiraService) readGuardedCommentReadback(ctx context.Context, port domain.JiraGuardedCommentPort, before *jiraGuardedCommentPrewrite, requestedKey string) (*jiraGuardedCommentSnapshot, error) {
	issue, updatedTime, err := readGuardedCommentIssue(ctx, port, before.issue.ID, requestedKey, before.issue.ID)
	if err != nil {
		return nil, errors.Join(domain.ErrCheckFailed, err)
	}
	if issue.Project != before.issue.Project {
		return nil, fmt.Errorf("%w: guarded Jira comment readback issue identity moved", domain.ErrCheckFailed)
	}
	inventory, err := port.ListJiraCommentsQualified(ctx, issue.ID, domain.JiraCommentReadOptions{
		MaxPages: domain.JiraCommentReadMaxPages, MaxItems: domain.JiraCommentReadMaxItems, MaxBytes: jiraGuardedCommentMaxInventoryBytes,
	})
	if err != nil {
		return nil, err
	}
	records, err := qualifyGuardedCommentInventory(inventory)
	if err != nil {
		return nil, err
	}
	return &jiraGuardedCommentSnapshot{issue: issue, records: records, updatedTime: updatedTime}, nil
}

func readGuardedCommentIssue(ctx context.Context, port domain.JiraGuardedCommentPort, reference, requestedKey, expectedID string) (domain.JiraGuardedCommentIssue, time.Time, error) {
	issue, err := port.ReadGuardedCommentIssue(ctx, reference)
	if err != nil {
		return domain.JiraGuardedCommentIssue{}, time.Time{}, err
	}
	if !issue.Complete || !canonicalPositiveNumericString(issue.ID) || requestedKey != "" && issue.Key != requestedKey ||
		!domain.ValidJiraIssueKey(issue.Key) || !domain.ValidJiraIssueKey(issue.Project+"-1") ||
		!strings.HasPrefix(issue.Key, issue.Project+"-") || expectedID != "" && issue.ID != expectedID {
		return domain.JiraGuardedCommentIssue{}, time.Time{}, fmt.Errorf("%w: Jira returned missing, moved, or malformed comment issue evidence", domain.ErrCheckFailed)
	}
	updatedTime, err := parseJiraStrictInstant(issue.Updated)
	if err != nil || strings.TrimSpace(issue.Updated) != issue.Updated {
		return domain.JiraGuardedCommentIssue{}, time.Time{}, fmt.Errorf("%w: Jira returned an unsupported comment issue timestamp", domain.ErrCheckFailed)
	}
	return issue, updatedTime, nil
}

func validGuardedCommentActor(actor domain.JiraGuardedCommentActor) bool {
	return actor.Complete && strings.TrimSpace(actor.Name) == actor.Name && strings.TrimSpace(actor.Key) == actor.Key &&
		domain.ValidJiraCommentEvidenceMetadata(actor.Name, false) && domain.ValidJiraCommentEvidenceMetadata(actor.Key, false)
}

func qualifyGuardedCommentInventory(inventory domain.JiraCommentInventory) ([]jiraGuardedCommentRecord, error) {
	if err := domain.ValidateJiraCommentInventory(inventory); err != nil || !inventory.Complete || inventory.PartialReason != "" ||
		!inventory.TotalKnown || inventory.Total != len(inventory.Comments) || inventory.PageCount > domain.JiraCommentReadMaxPages ||
		len(inventory.Comments) > domain.JiraCommentReadMaxItems {
		return nil, fmt.Errorf("%w: Jira comment inventory is incomplete or malformed", domain.ErrCheckFailed)
	}
	records := make([]jiraGuardedCommentRecord, 0, len(inventory.Comments))
	for _, comment := range inventory.Comments {
		created, createdErr := parseJiraStrictInstant(comment.Created)
		updated, updatedErr := parseJiraStrictInstant(comment.Updated)
		if !canonicalPositiveNumericString(comment.ID) || !domain.ValidJiraCommentEvidenceMetadata(comment.AuthorName, false) ||
			!domain.ValidJiraCommentEvidenceMetadata(comment.AuthorKey, false) || strings.TrimSpace(comment.AuthorName) != comment.AuthorName ||
			strings.TrimSpace(comment.AuthorKey) != comment.AuthorKey || createdErr != nil || updatedErr != nil || updated.Before(created) ||
			comment.ParentID != "" && !canonicalPositiveNumericString(comment.ParentID) || len(comment.Body) > domain.JiraCommentEvidenceBodyMaxBytes || !utf8.ValidString(comment.Body) {
			return nil, fmt.Errorf("%w: Jira comment inventory contains malformed guarded evidence", domain.ErrCheckFailed)
		}
		records = append(records, jiraGuardedCommentRecord{
			ID: comment.ID, AuthorName: comment.AuthorName, AuthorKey: comment.AuthorKey,
			Created: comment.Created, Updated: comment.Updated, ParentID: comment.ParentID, Body: comment.Body,
		})
	}
	sort.Slice(records, func(i, j int) bool { return guardedCommentDecimalLess(records[i].ID, records[j].ID) })
	return records, nil
}

func parseJiraStrictInstant(value string) (time.Time, error) {
	if !domain.ValidJiraGuardedCommentInstant(value) {
		return time.Time{}, fmt.Errorf("unsupported Jira timestamp")
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700", "2006-01-02T15:04:05-0700"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported Jira timestamp")
}

func guardedCommentDecimalLess(left, right string) bool {
	if len(left) != len(right) {
		return len(left) < len(right)
	}
	return left < right
}

func guardedCommentActorHash(actor domain.JiraGuardedCommentActor) string {
	data, _ := json.Marshal(struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	}{actor.Name, actor.Key})
	return sha256Hex(data)
}

func guardedCommentBaselineHash(records []jiraGuardedCommentRecord) string {
	data, _ := json.Marshal(records)
	return sha256Hex(data)
}

func guardedCommentProposalHash(result *JiraCommentAddResult, body []byte, actor domain.JiraGuardedCommentActor, records []jiraGuardedCommentRecord) string {
	payload := struct {
		SchemaVersion      int                        `json:"schema_version"`
		Operation          string                     `json:"operation"`
		SatisfactionPolicy string                     `json:"satisfaction_policy"`
		BackendSHA256      string                     `json:"backend_sha256"`
		RequestedKey       string                     `json:"requested_key"`
		IssueID            string                     `json:"issue_id"`
		Key                string                     `json:"key"`
		Project            string                     `json:"project"`
		Updated            string                     `json:"updated"`
		Body               string                     `json:"body"`
		BodySHA256         string                     `json:"body_sha256"`
		BodyBytes          int                        `json:"body_bytes"`
		ActorName          string                     `json:"actor_name"`
		ActorKey           string                     `json:"actor_key"`
		Records            []jiraGuardedCommentRecord `json:"records"`
		CurrentCount       int                        `json:"current_count"`
		BaselineSHA256     string                     `json:"baseline_sha256"`
		ExactBodyPresent   bool                       `json:"exact_body_present"`
		ExactBodyCount     int                        `json:"exact_body_count"`
		Bounds             JiraCommentBounds          `json:"bounds"`
	}{
		result.SchemaVersion, result.Operation, result.SatisfactionPolicy, result.BackendSHA256,
		result.RequestedKey, result.IssueID, result.Key, result.Project, result.Updated,
		string(body), result.BodySHA256, result.BodyBytes, actor.Name, actor.Key, records,
		result.CurrentCount, result.BaselineSHA256, result.ExactBodyCount > 0, result.ExactBodyCount, result.Bounds,
	}
	data, _ := json.Marshal(payload)
	return guardedProposalDigest(data)
}

func guardedCommentRecordDigests(records []jiraGuardedCommentRecord) map[string]string {
	baseline := make(map[string]string, len(records))
	for _, record := range records {
		data, _ := json.Marshal(record)
		baseline[record.ID] = sha256Hex(data)
	}
	return baseline
}

func guardedCommentNewRecordsPrepared(before map[string]string, after []jiraGuardedCommentRecord) ([]jiraGuardedCommentRecord, bool) {
	if len(after) < len(before) {
		return nil, false
	}
	newRecords := make([]jiraGuardedCommentRecord, 0, len(after)-len(before))
	seen := make(map[string]bool, len(before))
	for _, record := range after {
		if digest, present := before[record.ID]; present {
			data, _ := json.Marshal(record)
			if sha256Hex(data) != digest {
				return nil, false
			}
			seen[record.ID] = true
			continue
		}
		newRecords = append(newRecords, record)
	}
	return newRecords, len(seen) == len(before)
}

func guardedCommentPreparedCandidate(record jiraGuardedCommentRecord, body []byte, actorSHA256 string) bool {
	return record.ParentID == "" && record.Body == string(body) &&
		guardedCommentActorHash(domain.JiraGuardedCommentActor{Name: record.AuthorName, Key: record.AuthorKey}) == actorSHA256
}
