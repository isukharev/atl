package brokercontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

func digestValue(kind string, value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil || int64(len(encoded)) > MaxEnvelopeBytes {
		return "", reject(domain.BrokerReasonMalformed)
	}
	decoded, err := strictjson.DecodeValue(encoded)
	if err != nil {
		return "", reject(domain.BrokerReasonMalformed)
	}
	var canonical bytes.Buffer
	if err := writeCanonical(&canonical, decoded, 0); err != nil {
		return "", err
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("atl.broker.v1/" + kind + "\x00"))
	_, _ = hasher.Write(canonical.Bytes())
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func NativeCandidateSHA256(operation domain.BrokerOperationID, body []byte) (string, error) {
	if operation != domain.BrokerOperationJiraCommentPreview && operation != domain.BrokerOperationJiraCommentApply || len(body) == 0 || int64(len(body)) > MaxJiraCommentBodyBytes {
		return "", reject(domain.BrokerReasonMalformed)
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("atl.broker.v1/native-candidate/" + string(operation) + "/v1\x00"))
	_, _ = hasher.Write(body)
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func writeCanonical(out *bytes.Buffer, value any, depth int) error {
	if depth > MaxCanonicalDepth {
		return reject(domain.BrokerReasonMalformed)
	}
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if typed {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
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
		encoded, _ := json.Marshal(typed)
		out.Write(encoded)
	case []any:
		out.WriteByte('[')
		for index, member := range typed {
			if index > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, member, depth+1); err != nil {
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
			encoded, _ := json.Marshal(key)
			out.Write(encoded)
			out.WriteByte(':')
			if err := writeCanonical(out, typed[key], depth+1); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("canonical broker contract value: %w", reject(domain.BrokerReasonMalformed))
	}
	return nil
}
