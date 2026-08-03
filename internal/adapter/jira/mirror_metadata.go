package jira

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraIssueMetadataBatchMaxKeys     = 100
	jiraIssueMetadataSelectorMaxBytes = 16 << 10
)

// PlanIssueMetadataBatches partitions exact issue keys without I/O. The byte
// cap is measured after JQL literal escaping and includes separating commas.
func (j *Jira) PlanIssueMetadataBatches(keys []string) ([][]string, error) {
	var batches [][]string
	for start := 0; start < len(keys); {
		end := start
		selectorBytes := 0
		for end < len(keys) && end-start < jiraIssueMetadataBatchMaxKeys {
			quoted, err := quoteJiraJQLString(keys[end])
			if err != nil {
				return nil, err
			}
			encoded := len(quoted)
			if end > start {
				encoded++
			}
			if selectorBytes+encoded > jiraIssueMetadataSelectorMaxBytes {
				break
			}
			selectorBytes += encoded
			end++
		}
		if end == start {
			return nil, fmt.Errorf("%w: Jira issue metadata selector exceeds the bounded batch size", domain.ErrUsage)
		}
		batches = append(batches, append([]string(nil), keys[start:end]...))
		start = end
	}
	return batches, nil
}

// ReadIssueMetadataBatch performs exactly one qualified search request for a
// batch already produced by PlanIssueMetadataBatches.
func (j *Jira) ReadIssueMetadataBatch(ctx context.Context, keys, fields []string) (domain.JiraIssueMetadataBatch, error) {
	if len(keys) == 0 || len(keys) > jiraIssueMetadataBatchMaxKeys {
		return domain.JiraIssueMetadataBatch{}, fmt.Errorf("%w: Jira issue metadata batch must contain 1-%d keys", domain.ErrUsage, jiraIssueMetadataBatchMaxKeys)
	}
	parts := make([]string, len(keys))
	selectorBytes := 0
	for i, key := range keys {
		quoted, err := quoteJiraJQLString(key)
		if err != nil {
			return domain.JiraIssueMetadataBatch{}, err
		}
		parts[i] = quoted
		selectorBytes += len(quoted)
		if i > 0 {
			selectorBytes++
		}
	}
	if selectorBytes > jiraIssueMetadataSelectorMaxBytes {
		return domain.JiraIssueMetadataBatch{}, fmt.Errorf("%w: Jira issue metadata selector exceeds the bounded batch size", domain.ErrUsage)
	}
	page, err := j.SearchQualified(ctx, "key in ("+strings.Join(parts, ",")+")", fields, len(keys), "")
	if err != nil {
		return domain.JiraIssueMetadataBatch{}, err
	}
	result := domain.JiraIssueMetadataBatch{Issues: page.Issues, Complete: page.Complete && page.Next == ""}
	if !result.Complete {
		result.PartialReason = page.PartialReason
		if !domain.ValidIssueSearchPartialReason(result.PartialReason) {
			result.PartialReason = domain.IssueSearchPartialPaginationUnqualified
		}
	}
	return result, nil
}

func quoteJiraJQLString(value string) (string, error) {
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: Jira issue metadata key contains unsupported control characters", domain.ErrUsage)
		}
	}
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`, nil
}
