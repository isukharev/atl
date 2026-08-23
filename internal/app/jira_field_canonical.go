package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"sort"

	"github.com/isukharev/atl/internal/domain"
)

type jiraCanonicalFrame struct {
	value  any
	array  []any
	object map[string]any
	keys   []string
	index  int
	depth  int
	kind   byte
}

func guardedFieldEvidenceProjections(evidence map[string]domain.JiraGuardedFieldEvidence, fields []string, maximum int64) ([]JiraFieldValueProjection, int, error) {
	if len(evidence) != len(fields) {
		return nil, 0, fmt.Errorf("%w: guarded Jira field evidence is incomplete", domain.ErrCheckFailed)
	}
	out := make([]JiraFieldValueProjection, 0, len(fields))
	var total int64
	for _, field := range fields {
		value, ok := evidence[field]
		if !ok || !value.Present {
			return nil, 0, fmt.Errorf("%w: guarded Jira field evidence is incomplete", domain.ErrCheckFailed)
		}
		kind, bytes, digest, err := canonicalGuardedFieldValue(value.Value, maximum-total)
		if err != nil {
			return nil, 0, err
		}
		total += int64(bytes)
		if total > maximum {
			return nil, 0, fmt.Errorf("%w: guarded Jira current field evidence exceeds 64 MiB", domain.ErrCheckFailed)
		}
		out = append(out, JiraFieldValueProjection{Field: field, Present: true, Kind: kind, Bytes: bytes, SHA256: digest})
	}
	return out, int(total), nil
}

func canonicalGuardedFieldValue(value any, maximum int64) (string, int, string, error) {
	return canonicalGuardedFieldValueObserved(value, maximum, nil)
}

func canonicalGuardedFieldValueObserved(value any, maximum int64, maximumStack *int) (string, int, string, error) {
	kind := guardedFieldJSONKind(value)
	if kind == "" || maximum < 0 {
		return "", 0, "", fmt.Errorf("%w: guarded Jira field value is malformed or oversized", domain.ErrCheckFailed)
	}
	hasher := sha256.New()
	writer := &boundedHashWriter{hash: hasher, maximum: maximum}
	stack := []jiraCanonicalFrame{{value: value}}
	for len(stack) > 0 {
		if maximumStack != nil && len(stack) > *maximumStack {
			*maximumStack = len(stack)
		}
		last := len(stack) - 1
		frame := &stack[last]
		if frame.kind == '[' {
			if frame.index == len(frame.array) {
				if _, err := writer.Write([]byte("]")); err != nil {
					return "", 0, "", err
				}
				stack = stack[:last]
				continue
			}
			if frame.index > 0 {
				if _, err := writer.Write([]byte(",")); err != nil {
					return "", 0, "", err
				}
			}
			member := frame.array[frame.index]
			frame.index++
			stack = append(stack, jiraCanonicalFrame{value: member, depth: frame.depth})
			continue
		}
		if frame.kind == '{' {
			if frame.index == len(frame.keys) {
				if _, err := writer.Write([]byte("}")); err != nil {
					return "", 0, "", err
				}
				stack = stack[:last]
				continue
			}
			if frame.index > 0 {
				if _, err := writer.Write([]byte(",")); err != nil {
					return "", 0, "", err
				}
			}
			key := frame.keys[frame.index]
			frame.index++
			encodedKey, _ := json.Marshal(key)
			if _, err := writer.Write(encodedKey); err != nil {
				return "", 0, "", err
			}
			if _, err := writer.Write([]byte(":")); err != nil {
				return "", 0, "", err
			}
			stack = append(stack, jiraCanonicalFrame{value: frame.object[key], depth: frame.depth})
			continue
		}
		switch typed := frame.value.(type) {
		case nil:
			if _, err := writer.Write([]byte("null")); err != nil {
				return "", 0, "", err
			}
			stack = stack[:last]
		case bool, json.Number, float64:
			encoded, err := json.Marshal(typed)
			if err != nil {
				return "", 0, "", fmt.Errorf("%w: guarded Jira field scalar is malformed", domain.ErrCheckFailed)
			}
			if _, err := writer.Write(encoded); err != nil {
				return "", 0, "", err
			}
			stack = stack[:last]
		case string:
			encoded, err := json.Marshal(typed)
			if err != nil {
				return "", 0, "", fmt.Errorf("%w: guarded Jira field string is malformed", domain.ErrCheckFailed)
			}
			if _, err := writer.Write(encoded); err != nil {
				return "", 0, "", err
			}
			stack = stack[:last]
		case []any:
			depth := frame.depth + 1
			if depth > domain.JiraGuardedFieldMaxValueNestingDepth {
				return "", 0, "", fmt.Errorf("%w: guarded Jira field value exceeds the supported nesting bound", domain.ErrCheckFailed)
			}
			if _, err := writer.Write([]byte("[")); err != nil {
				return "", 0, "", err
			}
			*frame = jiraCanonicalFrame{array: typed, depth: depth, kind: '['}
		case map[string]any:
			depth := frame.depth + 1
			if depth > domain.JiraGuardedFieldMaxValueNestingDepth {
				return "", 0, "", fmt.Errorf("%w: guarded Jira field value exceeds the supported nesting bound", domain.ErrCheckFailed)
			}
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if _, err := writer.Write([]byte("{")); err != nil {
				return "", 0, "", err
			}
			*frame = jiraCanonicalFrame{object: typed, keys: keys, depth: depth, kind: '{'}
		default:
			return "", 0, "", fmt.Errorf("%w: guarded Jira field value has unsupported type", domain.ErrCheckFailed)
		}
	}
	return kind, writer.written, hex.EncodeToString(hasher.Sum(nil)), nil
}

type boundedHashWriter struct {
	hash    hash.Hash
	maximum int64
	written int
}

func (w *boundedHashWriter) Write(data []byte) (int, error) {
	if int64(w.written)+int64(len(data)) > w.maximum {
		return 0, fmt.Errorf("%w: guarded Jira current field evidence exceeds 64 MiB", domain.ErrCheckFailed)
	}
	n, err := w.hash.Write(data)
	w.written += n
	return n, err
}

func guardedFieldJSONKind(value any) string {
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
		return ""
	}
}
