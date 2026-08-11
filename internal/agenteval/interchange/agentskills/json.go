package agentskills

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"unicode/utf8"
)

func decodeBoundedJSONObject(data []byte, code ErrorCode) (map[string]json.RawMessage, error) {
	if len(data) > MaxJSONBytes {
		return nil, contractError(ErrorLimitExceeded, nil)
	}
	if len(data) == 0 || !utf8.Valid(data) {
		return nil, contractError(code, nil)
	}
	if err := validateJSONDocument(data); err != nil {
		return nil, contractError(code, err)
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, contractError(code, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, contractError(code, err)
	}
	return object, nil
}

func validateJSONDocument(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing json")
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth int) error {
	if depth > MaxJSONDepth {
		return fmt.Errorf("json depth")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate member")
			}
			seen[key] = struct{}{}
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("object close")
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("array close")
		}
	default:
		return fmt.Errorf("json delimiter")
	}
	return nil
}

func requireJSONMembers(object map[string]json.RawMessage, required, optional []string) error {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = true
		if _, ok := object[name]; !ok {
			return fmt.Errorf("required member")
		}
	}
	for _, name := range optional {
		allowed[name] = true
	}
	for name := range object {
		if !allowed[name] {
			return fmt.Errorf("unknown member")
		}
	}
	return nil
}

func decodeJSONString(raw json.RawMessage, maximum int, allowEmpty bool) (string, error) {
	var value string
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fmt.Errorf("invalid string")
	}
	if err := json.Unmarshal(raw, &value); err != nil || len(value) > maximum || (!allowEmpty && value == "") || !utf8.ValidString(value) {
		return "", fmt.Errorf("invalid string")
	}
	return value, nil
}

func decodeStringArray(raw json.RawMessage, maximumEntries, maximumBytes int) ([]string, error) {
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || values == nil || len(values) > maximumEntries {
		return nil, fmt.Errorf("invalid string array")
	}
	for _, value := range values {
		if value == "" || len(value) > maximumBytes || !utf8.ValidString(value) {
			return nil, fmt.Errorf("invalid string array value")
		}
	}
	return values, nil
}

func decodeJSONBool(raw json.RawMessage) (bool, error) {
	var value bool
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, fmt.Errorf("invalid boolean")
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("invalid boolean")
	}
	return value, nil
}

func decodeJSONUint32(raw json.RawMessage) (uint32, error) {
	var value uint32
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, fmt.Errorf("invalid uint32")
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("invalid uint32")
	}
	return value, nil
}

func decodeJSONUint64(raw json.RawMessage) (uint64, error) {
	var value uint64
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, fmt.Errorf("invalid uint64")
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("invalid uint64")
	}
	return value, nil
}

func decodeJSONNumber(raw json.RawMessage) (json.Number, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value json.Number
	if err := decoder.Decode(&value); err != nil || value == "" {
		return "", fmt.Errorf("invalid number")
	}
	return value, nil
}

func decodeFeedback(raw json.RawMessage) ([]FeedbackEntry, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil || len(values) > MaxFeedbackEntries {
		return nil, fmt.Errorf("invalid feedback")
	}
	entries := make([]FeedbackEntry, 0, len(values))
	for key, rawValue := range values {
		value, err := decodeJSONString(rawValue, MaxTextBytes, true)
		if err != nil || key == "" || len(key) > 128 || !utf8.ValidString(key) {
			return nil, fmt.Errorf("invalid feedback member")
		}
		entries = append(entries, FeedbackEntry{Key: key, Value: value})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries, nil
}
