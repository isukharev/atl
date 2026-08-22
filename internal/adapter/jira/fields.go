package jira

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/isukharev/atl/internal/domain"
)

// coerceFields decodes each extra Jira field before authorization or a write.
// ExplicitJSON is an opt-in for scalar types. Legacy values retain the
// historical object/array-or-string behavior.
func coerceFields(fields map[string]domain.JiraFieldInput) (map[string]any, error) {
	typed := make(map[string]any, len(fields))
	for key, input := range fields {
		value, err := coerceField(input)
		if err != nil {
			return nil, fmt.Errorf("%w: field %q has invalid explicit JSON", domain.ErrUsage, key)
		}
		typed[key] = value
	}
	return typed, nil
}

// coerceCreateFields rejects collisions with the dedicated create inputs
// before coercion, authorization, or serialization. Non-reserved keys are
// passed to coerceFields byte-for-byte unchanged.
func coerceCreateFields(fields map[string]domain.JiraFieldInput) (map[string]any, error) {
	for key := range fields {
		if reservedCreateField(key) {
			return nil, fmt.Errorf("%w: create fields must not override project, issuetype, summary, or description", domain.ErrUsage)
		}
	}
	return coerceFields(fields)
}

func reservedCreateField(key string) bool {
	var normalized strings.Builder
	normalized.Grow(len(key))
	for _, r := range key {
		if unicode.IsSpace(r) {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		if r > unicode.MaxASCII {
			return false
		}
		normalized.WriteRune(r)
	}
	switch normalized.String() {
	case "project", "issuetype", "summary", "description":
		return true
	default:
		return false
	}
}

func coerceField(input domain.JiraFieldInput) (any, error) {
	if input.ExplicitJSON {
		return decodeJiraJSON(input.Value)
	}
	if t := strings.TrimSpace(input.Value); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		if decoded, err := decodeJiraJSON(input.Value); err == nil {
			return decoded, nil
		}
	}
	return input.Value, nil
}

func decodeJiraJSON(value string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return decoded, nil
}
