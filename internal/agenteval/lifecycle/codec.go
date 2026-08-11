package lifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

const maxJSONDepth = 64

func EncodeHeader(header LedgerHeader) ([]byte, error) {
	if err := ValidateHeader(header); err != nil {
		return nil, err
	}
	return encodeCanonical(header, MaxHeaderBytes)
}

func DecodeHeader(data []byte) (LedgerHeader, error) {
	var header LedgerHeader
	if err := decodeCanonical(data, MaxHeaderBytes, &header); err != nil {
		return LedgerHeader{}, err
	}
	if err := ValidateHeader(header); err != nil {
		return LedgerHeader{}, err
	}
	return header, nil
}

func EncodePlan(plan Plan) ([]byte, error) {
	if err := ValidatePlan(plan); err != nil {
		return nil, err
	}
	return encodeCanonical(plan, MaxPlanBytes)
}

func DecodePlan(data []byte) (Plan, error) {
	var plan Plan
	if err := decodeCanonical(data, MaxPlanBytes, &plan); err != nil {
		return Plan{}, err
	}
	if err := ValidatePlan(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func EncodeEvent(event Event) ([]byte, error) {
	if err := ValidateEvent(event); err != nil {
		return nil, err
	}
	return encodeCanonical(event, MaxEventBytes)
}

func DecodeEvent(data []byte) (Event, error) {
	var event Event
	if err := decodeCanonical(data, MaxEventBytes, &event); err != nil {
		return Event{}, err
	}
	if err := ValidateEvent(event); err != nil {
		return Event{}, err
	}
	return event, nil
}

func encodeCanonical(value any, maximum int) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil || len(data)+1 > maximum {
		return nil, contractError("encode", err)
	}
	return append(data, '\n'), nil
}

func decodeCanonical(data []byte, maximum int, target any) error {
	if len(data) < 3 || len(data) > maximum || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data[:len(data)-1], '\n') >= 0 || bytes.IndexByte(data, '\r') >= 0 || !utf8.Valid(data) {
		return contractError("encoding")
	}
	body := data[:len(data)-1]
	if err := validateJSONMembers(body); err != nil {
		return contractError("encoding", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return contractError("decode", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return contractError("trailing", err)
	}
	encoded, err := json.Marshal(target)
	if err != nil || !bytes.Equal(encoded, body) {
		return contractError("canonical", err)
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

func digestHeader(header LedgerHeader) (string, error) {
	projection := struct {
		Schema          string `json:"schema"`
		SchemaVersion   int    `json:"schema_version"`
		ContractVersion string `json:"contract_version"`
		LedgerID        string `json:"ledger_id"`
	}{header.Schema, header.SchemaVersion, header.ContractVersion, header.LedgerID}
	return digestValue("ledger-header", projection)
}

func digestBinding(binding Binding) (string, error) { return digestValue("attempt-binding", binding) }

func digestPlan(plan Plan) (string, error) {
	projection := struct {
		Schema               string  `json:"schema"`
		SchemaVersion        int     `json:"schema_version"`
		LedgerID             string  `json:"ledger_id"`
		AttemptID            string  `json:"attempt_id"`
		Ordinal              uint32  `json:"ordinal"`
		PredecessorAttemptID string  `json:"predecessor_attempt_id,omitempty"`
		ReconciliationSHA256 string  `json:"reconciliation_sha256,omitempty"`
		Binding              Binding `json:"binding"`
		BindingSHA256        string  `json:"binding_sha256"`
	}{plan.Schema, plan.SchemaVersion, plan.LedgerID, plan.AttemptID, plan.Ordinal, plan.PredecessorAttemptID,
		plan.ReconciliationSHA256, plan.Binding, plan.BindingSHA256}
	return digestValue("attempt-plan", projection)
}

func digestEvent(event Event) (string, error) {
	projection := struct {
		Schema         string   `json:"schema"`
		SchemaVersion  int      `json:"schema_version"`
		LedgerID       string   `json:"ledger_id"`
		AttemptID      string   `json:"attempt_id"`
		PlanSHA256     string   `json:"plan_sha256"`
		Sequence       uint32   `json:"sequence"`
		PreviousSHA256 string   `json:"previous_sha256,omitempty"`
		From           State    `json:"from"`
		To             State    `json:"to"`
		Proofs         []Proof  `json:"proofs"`
		Evidence       Evidence `json:"evidence"`
	}{event.Schema, event.SchemaVersion, event.LedgerID, event.AttemptID, event.PlanSHA256, event.Sequence,
		event.PreviousSHA256, event.From, event.To, event.Proofs, event.Evidence}
	return digestValue("attempt-event", projection)
}

func digestValue(domain string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", contractError("digest", err)
	}
	return hashDomain(domain, data), nil
}
