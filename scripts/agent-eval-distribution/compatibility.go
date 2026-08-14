package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	maxCompatibilityEntries = 1024
	compatibilitySchemaV1   = 1
)

var canonicalMetricVectorValidity = map[string]bool{
	"legacy-not-applicable-zero": false,
	"legacy-unknown-zero":        true,
	"legacy-unsupported-zero":    true,
	"missing-optional-entry":     true,
	"missing-required-entry":     false,
	"not-applicable-absent":      true,
	"not-applicable-zero":        false,
	"observed-zero":              true,
	"uncovered-nonzero":          false,
	"unknown-absent":             true,
	"unknown-covered":            false,
	"unsupported-absent":         true,
}

type compatibilityGoldenBundle struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type compatibilityReadability struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Versions  []int  `json:"versions"`
}

type compatibilityForwardRejection struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Version   int    `json:"version"`
}

type compatibilityMetricVector struct {
	ID             string          `json:"id"`
	Representation string          `json:"representation"`
	Present        bool            `json:"present"`
	Required       bool            `json:"required"`
	State          *string         `json:"state"`
	Coverage       bool            `json:"coverage"`
	Value          json.RawMessage `json:"value"`
	Valid          bool            `json:"valid"`
}

type compatibilityBundle struct {
	SchemaVersion   int                             `json:"schema_version"`
	ContractVersion string                          `json:"contract_version"`
	GoldenBundle    compatibilityGoldenBundle       `json:"golden_bundle"`
	Readability     []compatibilityReadability      `json:"readability"`
	Forward         []compatibilityForwardRejection `json:"forward_rejection"`
	MetricVectors   []compatibilityMetricVector     `json:"metric_vectors"`
}

func decodeCompatibilityBundle(data []byte) (compatibilityBundle, error) {
	members, err := strictJSONObject(data)
	if err != nil {
		return compatibilityBundle{}, errors.New("compatibility bundle is not a canonical JSON object")
	}
	if err := requireObjectMembers(members, map[string]bool{
		"schema_version": true, "contract_version": true, "golden_bundle": true,
		"readability": true, "forward_rejection": true, "metric_vectors": true,
	}, map[string]bool{
		"schema_version": true, "contract_version": true, "golden_bundle": true,
		"readability": true, "forward_rejection": true, "metric_vectors": true,
	}); err != nil {
		return compatibilityBundle{}, err
	}
	if err := validateCompatibilityNestedShape(members); err != nil {
		return compatibilityBundle{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var bundle compatibilityBundle
	if err := decoder.Decode(&bundle); err != nil {
		return compatibilityBundle{}, errors.New("compatibility bundle has invalid member types")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return compatibilityBundle{}, errors.New("compatibility bundle has trailing data")
	}
	return bundle, nil
}

// strictJSONObject rejects duplicate object members before typed decoding. The
// standard encoding/json decoder is intentionally last-member-wins, which is
// not an acceptable boundary for signed compatibility metadata.
func strictJSONObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder, 0); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON has trailing data")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	var members map[string]json.RawMessage
	if err := decoder.Decode(&members); err != nil || members == nil {
		return nil, errors.New("JSON value is not an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON has trailing data")
	}
	return members, nil
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting exceeds the compatibility limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("JSON object member is not a string")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("duplicate JSON member %q", name)
			}
			seen[name] = struct{}{}
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

func requireObjectMembers(members map[string]json.RawMessage, required, known map[string]bool) error {
	for name := range members {
		if !known[name] {
			return fmt.Errorf("compatibility bundle has unknown or non-canonical member %q", name)
		}
	}
	for name := range required {
		if _, ok := members[name]; !ok {
			return fmt.Errorf("compatibility bundle is missing member %q", name)
		}
	}
	return nil
}

func rawJSONArray(raw json.RawMessage) ([]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var values []json.RawMessage
	if err := decoder.Decode(&values); err != nil || values == nil {
		return nil, errors.New("compatibility array member has an invalid type")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("compatibility array member has trailing data")
	}
	return values, nil
}

func nestedJSONObject(raw json.RawMessage, required, known map[string]bool) (map[string]json.RawMessage, error) {
	members, err := strictJSONObject(raw)
	if err != nil {
		return nil, errors.New("compatibility nested member is not an object")
	}
	if err := requireObjectMembers(members, required, known); err != nil {
		return nil, err
	}
	return members, nil
}

func validateCompatibilityNestedShape(root map[string]json.RawMessage) error {
	if _, err := nestedJSONObject(root["golden_bundle"],
		map[string]bool{"path": true, "sha256": true},
		map[string]bool{"path": true, "sha256": true}); err != nil {
		return fmt.Errorf("compatibility golden bundle: %w", err)
	}
	readability, err := rawJSONArray(root["readability"])
	if err != nil {
		return fmt.Errorf("compatibility readability: %w", err)
	}
	previous := ""
	for _, raw := range readability {
		members, err := nestedJSONObject(raw,
			map[string]bool{"namespace": true, "kind": true, "versions": true},
			map[string]bool{"namespace": true, "kind": true, "versions": true})
		if err != nil {
			return fmt.Errorf("compatibility readability entry: %w", err)
		}
		namespace, ok := rawString(members["namespace"])
		if !ok || !safeName(namespace) {
			return errors.New("compatibility readability namespace is invalid")
		}
		kind, ok := rawString(members["kind"])
		if !ok || !safeName(kind) {
			return errors.New("compatibility readability kind is invalid")
		}
		key := namespace + "\x00" + kind
		if key <= previous {
			return errors.New("compatibility readability entries are not sorted and unique")
		}
		previous = key
	}
	forward, err := rawJSONArray(root["forward_rejection"])
	if err != nil {
		return fmt.Errorf("compatibility forward rejection: %w", err)
	}
	previous = ""
	for _, raw := range forward {
		members, err := nestedJSONObject(raw,
			map[string]bool{"namespace": true, "kind": true, "version": true},
			map[string]bool{"namespace": true, "kind": true, "version": true})
		if err != nil {
			return fmt.Errorf("compatibility forward rejection entry: %w", err)
		}
		namespace, ok := rawString(members["namespace"])
		if !ok || !safeName(namespace) {
			return errors.New("compatibility forward rejection namespace is invalid")
		}
		kind, ok := rawString(members["kind"])
		if !ok || !safeName(kind) {
			return errors.New("compatibility forward rejection kind is invalid")
		}
		key := namespace + "\x00" + kind
		if key <= previous {
			return errors.New("compatibility forward rejection entries are not sorted and unique")
		}
		previous = key
	}
	metrics, err := rawJSONArray(root["metric_vectors"])
	if err != nil {
		return fmt.Errorf("compatibility metric vectors: %w", err)
	}
	previous = ""
	seenMetricIDs := make(map[string]bool, len(metrics))
	for _, raw := range metrics {
		members, err := nestedJSONObject(raw,
			map[string]bool{"id": true, "representation": true, "present": true, "required": true, "valid": true},
			map[string]bool{"id": true, "representation": true, "present": true, "required": true, "state": true, "coverage": true, "value": true, "valid": true})
		if err != nil {
			return fmt.Errorf("compatibility metric vector: %w", err)
		}
		if err := validateCompatibilityMetricVector(members); err != nil {
			return fmt.Errorf("compatibility metric vector: %w", err)
		}
		id, _ := rawString(members["id"])
		if id <= previous || seenMetricIDs[id] {
			return errors.New("compatibility metric vectors are not sorted and unique")
		}
		previous, seenMetricIDs[id] = id, true
	}
	if len(seenMetricIDs) != len(canonicalMetricVectorValidity) {
		return errors.New("compatibility metric vector inventory is not canonical")
	}
	for id := range canonicalMetricVectorValidity {
		if !seenMetricIDs[id] {
			return fmt.Errorf("compatibility metric vector %q is missing", id)
		}
	}
	return nil
}

func rawString(raw json.RawMessage) (string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func rawBool(raw json.RawMessage) (bool, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false
	}
	return value, true
}

func rawInt64(raw json.RawMessage) (int64, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, false
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	return value, true
}

func validateCompatibilityMetricVector(members map[string]json.RawMessage) error {
	id, idOK := rawString(members["id"])
	representation, representationOK := rawString(members["representation"])
	present, presentOK := rawBool(members["present"])
	required, requiredOK := rawBool(members["required"])
	valid, validOK := rawBool(members["valid"])
	if !idOK || !safeName(id) || !representationOK || representation != "atl-profile-legacy" && representation != "standalone" ||
		!presentOK || !requiredOK || !validOK {
		return errors.New("metric identity or required scalar is invalid")
	}
	state, statePresent := members["state"]
	stateValue, stateOK := rawString(state)
	coverage, coveragePresent := members["coverage"]
	coverageValue, coverageOK := rawBool(coverage)
	value, valuePresent := members["value"]
	valueNumber, valueOK := rawInt64(value)
	if !present {
		if statePresent && stateOK || coveragePresent && !rawNull(coverage) || valuePresent && !rawNull(value) {
			return errors.New("missing metric has observed fields")
		}
		if valid != !required || valid != canonicalMetricVectorValidity[id] {
			return errors.New("missing metric validity is inconsistent")
		}
		return nil
	}
	if !stateOK || !statePresent || stateValue != "observed" && stateValue != "unknown" && stateValue != "unsupported" && stateValue != "not_applicable" {
		return errors.New("observed metric state is invalid")
	}
	wantValid := false
	if representation == "standalone" {
		switch stateValue {
		case "observed":
			wantValid = coverageOK && coverageValue && valueOK && valueNumber >= 0
		default:
			wantValid = !coveragePresent && !valuePresent
		}
	} else {
		wantValid = coverageOK && valueOK && valueNumber >= 0
		switch stateValue {
		case "observed":
			wantValid = wantValid && coverageValue
		case "unknown", "unsupported":
			wantValid = wantValid && !coverageValue && valueNumber == 0
		default:
			wantValid = false
		}
	}
	if valid != wantValid || valid != canonicalMetricVectorValidity[id] {
		return errors.New("metric validity is inconsistent")
	}
	return nil
}

func rawNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func validateCompatibilityBundle(data []byte, contractVersion string) error {
	bundle, err := decodeCompatibilityBundle(data)
	if err != nil {
		return err
	}
	if bundle.SchemaVersion != compatibilitySchemaV1 || bundle.ContractVersion != contractVersion ||
		!safeName(bundle.GoldenBundle.Path) || !validDigest(bundle.GoldenBundle.SHA256) ||
		len(bundle.Readability) == 0 || len(bundle.Readability) > maxCompatibilityEntries ||
		len(bundle.Forward) == 0 || len(bundle.Forward) > maxCompatibilityEntries ||
		len(bundle.MetricVectors) == 0 || len(bundle.MetricVectors) > maxCompatibilityEntries {
		return errors.New("compatibility bundle metadata is invalid")
	}
	for _, entry := range bundle.Readability {
		if !safeName(entry.Namespace) || !safeName(entry.Kind) || len(entry.Versions) == 0 || len(entry.Versions) > maxCompatibilityEntries {
			return errors.New("compatibility readability entry is invalid")
		}
		previous := 0
		for _, version := range entry.Versions {
			if version <= 0 || version <= previous {
				return errors.New("compatibility readability versions are not canonical")
			}
			previous = version
		}
	}
	for _, entry := range bundle.Forward {
		if !safeName(entry.Namespace) || !safeName(entry.Kind) || entry.Version <= 0 {
			return errors.New("compatibility forward-rejection entry is invalid")
		}
	}
	for _, vector := range bundle.MetricVectors {
		if !safeName(vector.ID) || !safeName(vector.Representation) {
			return errors.New("compatibility metric vector identity is invalid")
		}
		if !vector.Present && len(vector.Value) != 0 {
			return errors.New("missing compatibility metric cannot carry a value")
		}
		if vector.State != nil && !safeName(*vector.State) {
			return errors.New("compatibility metric state is invalid")
		}
	}
	return nil
}
