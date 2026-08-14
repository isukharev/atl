package atif

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func canonicalJSONObject(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 || len(raw) > MaxArgumentBytes || !json.Valid(raw) {
		return nil, errors.New("argument json")
	}
	if err := validateJSONValueBytes(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("argument trailing json")
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, errors.New("argument object")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func validateJSONValueBytes(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing json")
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth int) error {
	if depth > MaxJSONDepth {
		return errors.New("json depth")
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
				return errors.New("json key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate json member")
			}
			seen[key] = struct{}{}
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("json object")
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("json array")
		}
	default:
		return errors.New("json delimiter")
	}
	return nil
}
