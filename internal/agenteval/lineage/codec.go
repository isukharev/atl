package lineage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// Encode emits exactly one canonical JSON object terminated by one LF.
func Encode(lineage Lineage) ([]byte, error) {
	if err := Validate(lineage); err != nil {
		return nil, err
	}
	data, err := json.Marshal(lineage)
	if err != nil || len(data)+1 > MaxLineageBytes {
		return nil, fail(ErrorLimitExceeded)
	}
	return append(data, '\n'), nil
}

// Decode accepts only bounded, canonical, closed-schema JSON and returns an
// owned snapshot. Future fields, duplicate members, aliases, trailing bytes,
// whitespace drift, and non-canonical encodings fail closed.
func Decode(reader io.Reader) (Lineage, error) {
	data, err := readCanonical(reader)
	if err != nil {
		return Lineage{}, err
	}
	var lineage Lineage
	if decodeClosed(data, &lineage) != nil || Validate(lineage) != nil {
		return Lineage{}, fail(ErrorInvalidLineage)
	}
	canonical, err := Encode(lineage)
	if err != nil || !bytes.Equal(data, canonical) {
		return Lineage{}, fail(ErrorInvalidLineage)
	}
	return cloneLineage(lineage), nil
}

func readCanonical(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, fail(ErrorInvalidLineage)
	}
	limited := &io.LimitedReader{R: reader, N: int64(MaxLineageBytes) + 1}
	data, err := io.ReadAll(limited)
	if err != nil || limited.N == 0 || len(data) < 3 || len(data) > MaxLineageBytes ||
		!utf8.Valid(data) || bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) ||
		data[len(data)-1] != '\n' || bytes.IndexByte(data[:len(data)-1], '\n') >= 0 ||
		bytes.IndexByte(data, '\r') >= 0 || validateJSONShape(data[:len(data)-1]) != nil {
		return nil, fail(ErrorInvalidLineage)
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
	if err := validateJSONValue(decoder, 0, ""); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing_json")
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth int, path string) error {
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
			if jsonMemberUsesCaseAlias(path, name) {
				return errors.New("json_member_case_alias")
			}
			seen[name] = true
			childPath := name
			if path != "" {
				childPath = path + "." + name
			}
			if err := validateJSONValue(decoder, depth+1, childPath); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("json_object")
		}
	case '[':
		limit := jsonArrayLimit(path)
		count := 0
		for decoder.More() {
			count++
			if limit > 0 && count > limit {
				return errors.New("json_array_limit")
			}
			childPath := path + ".*"
			if err := validateJSONValue(decoder, depth+1, childPath); err != nil {
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

func jsonArrayLimit(path string) int {
	switch path {
	case "roles":
		return MaxRoles
	case "holdouts":
		return MaxHoldouts
	case "primary_identity.dependency_sha256", "holdouts.*.holdout_identity.dependency_sha256":
		return MaxDependencies
	case "holdouts.*.differences":
		return len(closedAxes)
	case "holdouts.*.reviewed_material_axes":
		return MaxMaterialAxes
	default:
		return 0
	}
}

// jsonMemberUsesCaseAlias rejects the case-insensitive member matching that
// encoding/json applies to struct fields. A differently-cased known member
// must not reach typed decoding: it could otherwise bypass a path-specific
// array limit before DisallowUnknownFields recognizes the alias.
func jsonMemberUsesCaseAlias(path, name string) bool {
	for _, canonical := range canonicalJSONMembers(path) {
		if name != canonical && strings.EqualFold(name, canonical) {
			return true
		}
	}
	return false
}

func canonicalJSONMembers(path string) []string {
	switch path {
	case "":
		return []string{"schema", "schema_version", "contract_version", "roles", "primary_role", "primary_role_sha256", "primary_contract_sha256", "primary_identity", "holdouts", "lineage_sha256"}
	case "roles.*":
		return []string{"role", "content_sha256", "coverage", "legacy_id_sha256", "legacy_read_only", "role_sha256"}
	case "roles.*.coverage":
		return []string{"total", "covered"}
	case "primary_identity", "holdouts.*.holdout_identity":
		return identityJSONMembers()
	case "holdouts.*":
		return []string{"holdout_role", "holdout_role_sha256", "holdout_contract_sha256", "holdout_identity", "differences", "reviewed_material_axes", "binding_sha256"}
	case "holdouts.*.differences.*":
		return []string{"axis", "primary_sha256", "holdout_sha256", "difference_sha256"}
	default:
		return nil
	}
}

func identityJSONMembers() []string {
	return []string{"skill_sha256", "eval_sha256", "grader_sha256", "agent_sha256", "model_sha256", "harness_sha256", "environment_sha256", "tool_api_sha256", "dependency_sha256", "identity_sha256"}
}
