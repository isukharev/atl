package atif

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// Encode emits one strict canonical ATIF v1.7 document terminated by LF.
func Encode(projection Projection) ([]byte, error) {
	if err := Validate(projection); err != nil {
		return nil, err
	}
	data, err := json.Marshal(projection.Document)
	if err != nil || len(data)+1 > MaxDocumentBytes {
		return nil, fail(ErrorLimitExceeded)
	}
	return append(data, '\n'), nil
}

// Decode accepts only the closed canonical document emitted by Encode.
func Decode(reader io.Reader) (Projection, error) {
	data, err := readCanonical(reader)
	if err != nil {
		return Projection{}, err
	}
	var document Document
	if err := decodeClosed(data, &document); err != nil || validateDocument(document) != nil {
		return Projection{}, fail(ErrorInvalidWire)
	}
	projection := Projection{
		Document: document, SourceSHA256: document.Extra.SourceSHA256,
		ProjectionSHA256: document.Extra.ProjectionSHA256,
	}
	if err := Validate(projection); err != nil {
		return Projection{}, fail(ErrorInvalidWire)
	}
	canonical, err := Encode(projection)
	if err != nil || !bytes.Equal(data, canonical) {
		return Projection{}, fail(ErrorInvalidWire)
	}
	return cloneProjection(projection), nil
}

func readCanonical(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, fail(ErrorInvalidWire)
	}
	limited := &io.LimitedReader{R: reader, N: int64(MaxDocumentBytes) + 1}
	data, err := io.ReadAll(limited)
	if err != nil || limited.N == 0 || len(data) < 3 || len(data) > MaxDocumentBytes ||
		!utf8.Valid(data) || bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) ||
		data[len(data)-1] != '\n' || bytes.IndexByte(data[:len(data)-1], '\n') >= 0 || bytes.IndexByte(data, '\r') >= 0 ||
		validateJSONValueBytes(data[:len(data)-1]) != nil {
		return nil, fail(ErrorInvalidWire)
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
		return errors.New("trailing json")
	}
	return nil
}
