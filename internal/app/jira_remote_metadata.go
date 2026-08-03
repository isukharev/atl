package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const jiraRemoteEvidenceIncomplete = "remote_evidence_incomplete"

type jiraRemoteMetadataEvidence struct {
	issue     *domain.Issue
	available bool
	reason    string
}

func readJiraRemoteMetadataBatches(ctx context.Context, reader domain.QualifiedJiraIssueMetadataBatchReader, keys []string, fieldsByKey map[string][]string) map[string]jiraRemoteMetadataEvidence {
	out := make(map[string]jiraRemoteMetadataEvidence, len(keys))
	mark := func(batch []string) {
		for _, key := range batch {
			out[canonicalJiraMirrorKey(key)] = jiraRemoteMetadataEvidence{reason: jiraRemoteEvidenceIncomplete}
		}
	}
	batches, err := reader.PlanIssueMetadataBatches(keys)
	if err != nil || !exactJiraMetadataBatchPlan(keys, batches) {
		mark(keys)
		return out
	}
	for _, batch := range batches {
		fields := jiraMetadataBatchFields(batch, fieldsByKey)
		page, readErr := reader.ReadIssueMetadataBatch(ctx, batch, fields)
		if readErr != nil || !page.Complete || page.PartialReason != "" || !validJiraMetadataBatch(batch, page.Issues) {
			mark(batch)
			continue
		}
		for i := range page.Issues {
			issue := page.Issues[i]
			out[canonicalJiraMirrorKey(issue.Key)] = jiraRemoteMetadataEvidence{issue: &issue, available: true}
		}
	}
	return out
}

func exactJiraMetadataBatchPlan(keys []string, batches [][]string) bool {
	position := 0
	for _, batch := range batches {
		if len(batch) == 0 || len(batch) > 100 || position+len(batch) > len(keys) {
			return false
		}
		for _, key := range batch {
			if keys[position] != key {
				return false
			}
			position++
		}
	}
	return position == len(keys)
}

func jiraMetadataBatchFields(batch []string, fieldsByKey map[string][]string) []string {
	fields := []string{"description"}
	seen := map[string]bool{"description": true}
	for _, key := range batch {
		for _, field := range fieldsByKey[canonicalJiraMirrorKey(key)] {
			if field != "" && !seen[field] {
				seen[field] = true
				fields = append(fields, field)
			}
		}
	}
	return fields
}

func validJiraMetadataBatch(requested []string, issues []domain.Issue) bool {
	want := make(map[string]bool, len(requested))
	for _, key := range requested {
		identity := canonicalJiraMirrorKey(key)
		if identity == "" || want[identity] {
			return false
		}
		want[identity] = true
	}
	seenKeys := make(map[string]bool, len(issues))
	seenIDs := make(map[string]bool, len(issues))
	for i := range issues {
		issue := &issues[i]
		key := canonicalJiraMirrorKey(issue.Key)
		id, ok := canonicalJiraNumericID(issue.ID)
		if key == "" || !want[key] || seenKeys[key] || !ok || seenIDs[id] || issue.Fields == nil {
			return false
		}
		description, present := issue.Fields["description"]
		if !present || (description != nil && !isString(description)) {
			return false
		}
		seenKeys[key] = true
		seenIDs[id] = true
	}
	return len(seenKeys) == len(want)
}

func canonicalJiraMirrorKey(key string) string {
	if key == "" || key != strings.TrimSpace(key) {
		return ""
	}
	return strings.ToUpper(key)
}

func canonicalJiraNumericID(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	n, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || n <= 0 || trimmed != value || strconv.FormatInt(n, 10) != value {
		return "", false
	}
	return value, true
}

func isString(value any) bool {
	_, ok := value.(string)
	return ok
}

// jiraBatchPendingStringField keeps a missing/null/non-string optional field
// as per-field drift after the batch itself has been qualified. Description has
// a separate contract where an explicitly present null is valid empty content.
func jiraBatchPendingStringField(fields map[string]any, id string) (string, bool) {
	value, present := fields[id]
	if !present || value == nil {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}
