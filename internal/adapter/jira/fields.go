package jira

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

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
