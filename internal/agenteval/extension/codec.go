package extension

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

const maxJSONDepth = 128

// EncodeManifest emits the only canonical v1 manifest representation.
func EncodeManifest(value Manifest) ([]byte, error) {
	if err := ValidateManifest(value); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil || len(data)+1 > MaxManifestBytes {
		return nil, contractError(ErrorLimitExceeded, err)
	}
	return append(data, '\n'), nil
}

// DecodeManifest requires canonical UTF-8 JSON followed by exactly one LF.
func DecodeManifest(data []byte) (Manifest, error) {
	var value Manifest
	if len(data) < 3 || len(data) > MaxManifestBytes || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data[:len(data)-1], '\n') >= 0 || bytes.IndexByte(data, '\r') >= 0 {
		return value, contractError(ErrorInvalidManifest, nil)
	}
	body := data[:len(data)-1]
	if err := decodeStrictCanonicalJSON(body, &value, ErrorInvalidManifest); err != nil {
		return Manifest{}, err
	}
	if err := ValidateManifest(value); err != nil {
		return Manifest{}, err
	}
	encoded, err := EncodeManifest(value)
	if err != nil || !bytes.Equal(encoded, data) {
		return Manifest{}, contractError(ErrorInvalidManifest, err)
	}
	return value, nil
}

// EncodeFrame emits one canonical JSON frame without its transport LF.
func EncodeFrame(value Frame) ([]byte, error) {
	if err := ValidateFrame(value); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > MaxFrameBytes {
		return nil, contractError(ErrorLimitExceeded, err)
	}
	return data, nil
}

// EncodeFrameLine emits one complete canonical JSONL frame.
func EncodeFrameLine(value Frame) ([]byte, error) {
	data, err := EncodeFrame(value)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// DecodeFrame strictly decodes one canonical frame after transport framing has
// removed the LF.
func DecodeFrame(data []byte) (Frame, error) {
	var value Frame
	if len(data) < 2 || len(data) > MaxFrameBytes || bytes.ContainsAny(data, "\r\n") {
		return value, contractError(ErrorInvalidMessage, nil)
	}
	if err := decodeStrictCanonicalJSON(data, &value, ErrorInvalidMessage); err != nil {
		return Frame{}, err
	}
	if err := ValidateFrame(value); err != nil {
		return Frame{}, err
	}
	encoded, err := EncodeFrame(value)
	if err != nil || !bytes.Equal(encoded, data) {
		return Frame{}, contractError(ErrorInvalidMessage, err)
	}
	return value, nil
}

// DecodeFrameLine strictly decodes one LF-terminated canonical frame.
func DecodeFrameLine(data []byte) (Frame, error) {
	if len(data) < 3 || len(data) > MaxFrameBytes+1 || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data[:len(data)-1], '\n') >= 0 || bytes.IndexByte(data, '\r') >= 0 {
		return Frame{}, contractError(ErrorInvalidMessage, nil)
	}
	return DecodeFrame(data[:len(data)-1])
}

func decodeStrictCanonicalJSON(data []byte, target any, code ErrorCode) error {
	if !utf8.Valid(data) {
		return contractError(code, nil)
	}
	if err := validateJSONMembers(data); err != nil {
		return contractError(code, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return contractError(code, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return contractError(code, err)
	}
	return nil
}

func validateJSONMembers(data []byte) error {
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
	if depth > maxJSONDepth {
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
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object_key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate_member")
			}
			seen[key] = struct{}{}
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("object_close")
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("array_close")
		}
	default:
		return errors.New("json_delimiter")
	}
	return nil
}
