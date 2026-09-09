package brokercontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

func digestExecutionV3(label string, value any) (string, error) {
	encoded, err := marshalAttachmentV3(value, MaxEnvelopeBytes)
	if err != nil || label == "" {
		return "", reject(domain.BrokerReasonMalformed)
	}
	decoded, err := strictjson.DecodeValue(encoded)
	if err != nil {
		return "", reject(domain.BrokerReasonMalformed)
	}
	var canonical bytes.Buffer
	if writeCanonicalExecutionV3(&canonical, decoded, 0) != nil {
		return "", reject(domain.BrokerReasonMalformed)
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("atl.broker.execution.v3/" + label + "\x00"))
	var length [8]byte
	canonicalLength := canonical.Len()
	if canonicalLength < 0 || int64(canonicalLength) > MaxEnvelopeBytes {
		return "", reject(domain.BrokerReasonMalformed)
	}
	binary.BigEndian.PutUint64(length[:], uint64(canonicalLength)) // #nosec G115 -- bounded above by MaxEnvelopeBytes.
	_, _ = hasher.Write(length[:])
	_, _ = hasher.Write(canonical.Bytes())
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func marshalAttachmentV3(value any, maximum int64) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(value)
	encoded := bytes.TrimSuffix(out.Bytes(), []byte{'\n'})
	if err != nil || len(encoded) == 0 || int64(len(encoded)) > maximum {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return encoded, nil
}

func writeCanonicalExecutionV3(out *bytes.Buffer, value any, depth int) error {
	if depth > MaxCanonicalDepth {
		return reject(domain.BrokerReasonMalformed)
	}
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(typed))
	case json.Number:
		text := typed.String()
		if text == "" || bytes.ContainsAny([]byte(text), ".eE+") || text == "-0" {
			return reject(domain.BrokerReasonMalformed)
		}
		if _, err := strconv.ParseInt(text, 10, 64); err != nil {
			return reject(domain.BrokerReasonMalformed)
		}
		out.WriteString(text)
	case string:
		if err := writeJSONStringV3(out, typed); err != nil {
			return err
		}
	case []any:
		out.WriteByte('[')
		for index, member := range typed {
			if index > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonicalExecutionV3(out, member, depth+1); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				out.WriteByte(',')
			}
			if err := writeJSONStringV3(out, key); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := writeCanonicalExecutionV3(out, typed[key], depth+1); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func writeJSONStringV3(out *bytes.Buffer, value string) error {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return reject(domain.BrokerReasonMalformed)
	}
	quoted := bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'})
	if len(quoted) < 2 || quoted[0] != '"' || quoted[len(quoted)-1] != '"' {
		return reject(domain.BrokerReasonMalformed)
	}
	_, _ = out.Write(quoted)
	return nil
}

func marshalAttachmentPayloadV3(value any) (json.RawMessage, error) {
	body, err := marshalAttachmentV3(value, MaxEnvelopeBytes)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

func decodeAttachmentV3(data []byte, maximum int64, target any) bool {
	return len(data) > 0 && int64(len(data)) <= maximum && strictjson.DecodeExact(data, MaxCanonicalDepth, target) == nil
}
