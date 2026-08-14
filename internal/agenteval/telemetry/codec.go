package telemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// Encode emits one bounded canonical JSON projection terminated by LF.
func Encode(projection Projection) ([]byte, error) {
	if err := Validate(projection); err != nil {
		return nil, err
	}
	data, err := json.Marshal(projection)
	if err != nil || len(data)+1 > MaxProjectionBytes {
		return nil, fail(ErrorLimitExceeded)
	}
	return append(data, '\n'), nil
}

// Decode accepts only the closed, canonical projection emitted by Encode.
func Decode(reader io.Reader) (Projection, error) {
	data, err := readCanonical(reader)
	if err != nil {
		return Projection{}, err
	}
	var projection Projection
	if decodeClosed(data, &projection) != nil || Validate(projection) != nil {
		return Projection{}, fail(ErrorInvalidProjection)
	}
	canonical, err := Encode(projection)
	if err != nil || !bytes.Equal(data, canonical) {
		return Projection{}, fail(ErrorInvalidProjection)
	}
	return cloneProjection(projection), nil
}

func readCanonical(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, fail(ErrorInvalidProjection)
	}
	limited := &io.LimitedReader{R: reader, N: int64(MaxProjectionBytes) + 1}
	data, err := io.ReadAll(limited)
	if err != nil || limited.N == 0 || len(data) < 3 || len(data) > MaxProjectionBytes ||
		!utf8.Valid(data) || bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) ||
		data[len(data)-1] != '\n' || bytes.IndexByte(data[:len(data)-1], '\n') >= 0 ||
		bytes.IndexByte(data, '\r') >= 0 || validateJSONShape(data[:len(data)-1]) != nil {
		return nil, fail(ErrorInvalidProjection)
	}
	return data, nil
}

func decodeClosed(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing_json")
	}
	return nil
}

func validateJSONShape(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing_json")
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth int) error {
	if depth > MaxJSONDepth {
		return errors.New("json_depth")
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
		seen := map[string]bool{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok || seen[name] {
				return errors.New("duplicate_json_member")
			}
			seen[name] = true
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("json_object")
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("json_array")
		}
	default:
		return errors.New("json_delimiter")
	}
	return nil
}
