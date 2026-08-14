package evolution

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// Encode emits one canonical JSON object and one terminating LF.
func Encode(proposal Proposal) ([]byte, error) {
	if err := Validate(proposal); err != nil {
		return nil, err
	}
	data, err := json.Marshal(proposal)
	if err != nil || len(data)+1 > MaxProposalBytes {
		return nil, fail(ErrorLimitExceeded)
	}
	return append(data, '\n'), nil
}

// Decode accepts only bounded, canonical, closed-schema proposal JSON.
func Decode(reader io.Reader) (Proposal, error) {
	if reader == nil {
		return Proposal{}, fail(ErrorInvalidProposal)
	}
	limited := &io.LimitedReader{R: reader, N: int64(MaxProposalBytes) + 1}
	data, err := io.ReadAll(limited)
	if err != nil || limited.N == 0 || len(data) < 3 || len(data) > MaxProposalBytes || !utf8.Valid(data) ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data[:len(data)-1], '\n') >= 0 || bytes.IndexByte(data, '\r') >= 0 ||
		validateJSONShape(data[:len(data)-1]) != nil {
		return Proposal{}, fail(ErrorInvalidProposal)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var proposal Proposal
	if err := decoder.Decode(&proposal); err != nil {
		return Proposal{}, fail(ErrorInvalidProposal)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Proposal{}, fail(ErrorInvalidProposal)
	}
	if err := Validate(proposal); err != nil {
		return Proposal{}, err
	}
	canonical, err := Encode(proposal)
	if err != nil || !bytes.Equal(canonical, data) {
		return Proposal{}, fail(ErrorInvalidProposal)
	}
	return cloneProposal(proposal), nil
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
			if !ok || seen[name] || jsonMemberUsesCaseAlias(path, name) {
				return errors.New("json_member")
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
			if err := validateJSONValue(decoder, depth+1, path+".*"); err != nil {
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
	case "failures", "skill_changes", "evaluation_changes":
		return MaxFailures
	case "failures.*.evidence_sha256", "skill_changes.*.evidence_sha256", "evaluation_changes.*.evidence_sha256":
		return MaxEvidenceRefs
	default:
		return 0
	}
}

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
		return []string{"schema", "schema_version", "contract_version", "lineage_sha256", "base_skill_sha256", "base_evaluation_sha256", "input_sha256", "self_feedback_only", "exploratory", "reusable_improvement", "failures", "skill_changes", "evaluation_changes", "proposal_sha256"}
	case "failures.*":
		return []string{"class", "count", "evidence_sha256"}
	case "skill_changes.*", "evaluation_changes.*":
		return []string{"class", "action", "evidence_sha256"}
	default:
		return nil
	}
}
