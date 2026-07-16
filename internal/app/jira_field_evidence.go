package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const (
	JiraIssueFieldEvidenceDefaultMaxBytes = 16 << 10
	JiraIssueFieldEvidenceMinMaxBytes     = 256
	JiraIssueFieldEvidenceMaxMaxBytes     = 128 << 10
)

// JiraIssueFieldEvidenceOpts selects one exact field and bounds the encoded
// compact value returned to an agent. MaxBytes=0 selects the documented
// default; callers cannot request raw transport objects through this use case.
type JiraIssueFieldEvidenceOpts struct {
	Selector string
	MaxBytes int
}

type JiraIssueFieldEvidenceIssue struct {
	ID      string `json:"id,omitempty"`
	Key     string `json:"key"`
	Updated string `json:"updated"`
}

type JiraIssueFieldEvidenceField struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Custom    bool   `json:"custom"`
	Schema    string `json:"schema,omitempty"`
	Present   bool   `json:"present"`
	Empty     bool   `json:"empty"`
	ValueType string `json:"value_type"`
}

// JiraIssueFieldEvidenceResult is the shared CLI/MCP contract for expanding
// exactly one clipped field without re-reading a broad digest. Complete refers
// to the compact value projection, not to fields deliberately removed by that
// projection (for example email/avatar/self properties of Jira users).
type JiraIssueFieldEvidenceResult struct {
	SchemaVersion      int                         `json:"schema_version"`
	Issue              JiraIssueFieldEvidenceIssue `json:"issue"`
	Field              JiraIssueFieldEvidenceField `json:"field"`
	Projection         string                      `json:"projection"`
	MaxValueBytes      int                         `json:"max_value_bytes"`
	OriginalValueBytes int                         `json:"original_value_bytes"`
	EmittedValueBytes  int                         `json:"emitted_value_bytes"`
	Complete           bool                        `json:"complete"`
	Truncated          bool                        `json:"truncated"`
	Value              any                         `json:"value"`
}

func (s *JiraService) IssueFieldEvidence(ctx context.Context, key string, opts JiraIssueFieldEvidenceOpts) (*JiraIssueFieldEvidenceResult, error) {
	key = strings.TrimSpace(key)
	selector := strings.TrimSpace(opts.Selector)
	if key == "" {
		return nil, fmt.Errorf("%w: issue key is required", domain.ErrUsage)
	}
	if selector == "" {
		return nil, fmt.Errorf("%w: exactly one Jira field selector is required", domain.ErrUsage)
	}
	maxBytes := opts.MaxBytes
	if maxBytes == 0 {
		maxBytes = JiraIssueFieldEvidenceDefaultMaxBytes
	}
	if maxBytes < JiraIssueFieldEvidenceMinMaxBytes || maxBytes > JiraIssueFieldEvidenceMaxMaxBytes {
		return nil, fmt.Errorf("%w: max bytes must be between %d and %d", domain.ErrUsage, JiraIssueFieldEvidenceMinMaxBytes, JiraIssueFieldEvidenceMaxMaxBytes)
	}

	defs, err := s.resolveJiraFieldSelectors(ctx, []string{selector})
	if err != nil {
		return nil, err
	}
	def := defs[0]
	requestFields := []string{def.ID}
	if def.ID != "updated" {
		requestFields = append(requestFields, "updated")
	}
	issue, err := s.tr.GetIssue(ctx, key, requestFields)
	if err != nil {
		return nil, err
	}
	if issue == nil || strings.TrimSpace(issue.Key) == "" {
		return nil, fmt.Errorf("%w: issue %s returned no identity snapshot", domain.ErrCheckFailed, key)
	}
	updated, ok := issue.Fields["updated"].(string)
	if !ok || strings.TrimSpace(updated) == "" {
		return nil, fmt.Errorf("%w: issue %s omitted requested updated provenance", domain.ErrCheckFailed, issue.Key)
	}

	raw, present := issue.Fields[def.ID]
	rawJSON, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: encode Jira field %s value: %v", domain.ErrCheckFailed, def.ID, err)
	}
	value, emittedBytes, truncated, err := boundedCompactJiraFieldValue(raw, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: compact Jira field %s value: %v", domain.ErrCheckFailed, def.ID, err)
	}
	return &JiraIssueFieldEvidenceResult{
		SchemaVersion: 1,
		Issue:         JiraIssueFieldEvidenceIssue{ID: issue.ID, Key: issue.Key, Updated: updated},
		Field: JiraIssueFieldEvidenceField{
			ID: def.ID, Name: def.Name, Custom: def.Custom, Schema: def.Schema,
			Present: present, Empty: !present || jiraFieldValueEmpty(raw), ValueType: jiraFieldValueType(raw),
		},
		Projection: "compact", MaxValueBytes: maxBytes,
		OriginalValueBytes: len(rawJSON), EmittedValueBytes: emittedBytes,
		Complete: !truncated, Truncated: truncated, Value: value,
	}, nil
}

func boundedCompactJiraFieldValue(value any, maxBytes int) (any, int, bool, error) {
	if text, ok := value.(string); ok {
		encoded, err := json.Marshal(text)
		if err != nil {
			return nil, 0, false, err
		}
		if len(encoded) <= maxBytes {
			return text, len(encoded), false, nil
		}
		bounded := longestJSONEncodedStringPrefix(text, maxBytes)
		encoded, err = json.Marshal(bounded)
		return bounded, len(encoded), true, err
	}

	stringCap, arrayCap := maxBytes, jiraCompactFieldArrayCap
	for range 24 {
		compact, projectedTruncated := compactJiraFieldValueWithLimits(value, 0, stringCap, arrayCap)
		encoded, err := json.Marshal(compact)
		if err != nil {
			return nil, 0, false, err
		}
		if len(encoded) <= maxBytes {
			return compact, len(encoded), projectedTruncated || stringCap < maxBytes || arrayCap < jiraCompactFieldArrayCap, nil
		}
		if stringCap > 16 {
			stringCap /= 2
		}
		if arrayCap > 1 {
			arrayCap = (arrayCap + 1) / 2
		}
	}
	fallback := map[string]any{"kind": "bounded", "present": !jiraFieldValueEmpty(value)}
	encoded, err := json.Marshal(fallback)
	if err != nil {
		return nil, 0, false, err
	}
	if len(encoded) > maxBytes {
		return nil, 0, false, fmt.Errorf("minimum compact value exceeds %d bytes", maxBytes)
	}
	return fallback, len(encoded), true, nil
}

func longestJSONEncodedStringPrefix(value string, maxBytes int) string {
	low, high := 0, len(value)
	for low < high {
		mid := low + (high-low+1)/2
		for mid > 0 && !utf8.ValidString(value[:mid]) {
			mid--
		}
		if mid <= low {
			break
		}
		encoded, _ := json.Marshal(value[:mid])
		if len(encoded) <= maxBytes {
			low = mid
		} else {
			high = mid - 1
		}
	}
	for low > 0 && !utf8.ValidString(value[:low]) {
		low--
	}
	// The byte-oriented search may stop inside the next multi-byte rune. Grow
	// from the best valid boundary so the answer is the longest valid prefix.
	for low < len(value) {
		_, size := utf8.DecodeRuneInString(value[low:])
		if size == 0 {
			break
		}
		next := low + size
		encoded, _ := json.Marshal(value[:next])
		if len(encoded) > maxBytes {
			break
		}
		low = next
	}
	return value[:low]
}

func JiraIssueFieldEvidenceMarkdown(result *JiraIssueFieldEvidenceResult) string {
	if result == nil {
		return ""
	}
	value := "null"
	if text, ok := result.Value.(string); ok {
		value = text
	} else if encoded, err := json.Marshal(result.Value); err == nil {
		value = string(encoded)
	}
	if result.Truncated {
		value += fmt.Sprintf(" [truncated; emitted %d of %d encoded bytes]", result.EmittedValueBytes, result.OriginalValueBytes)
	}
	return MarkdownTable(
		[]string{"Issue", "Updated", "Field", "ID", "Value"},
		[][]string{{result.Issue.Key, result.Issue.Updated, result.Field.Name, result.Field.ID, value}},
	)
}
