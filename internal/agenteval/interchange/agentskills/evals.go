package agentskills

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"unicode/utf8"
)

type decodedEvals struct {
	format    Format
	skillName string
	cases     []decodedCase
}

type decodedCase struct {
	id              uint32
	prompt          string
	expectedOutput  string
	filesPresent    bool
	criteriaPresent bool
	files           []string
	criteria        []string
	criterionKind   CriterionKind
	criterionField  string
}

func decodeEvals(data []byte, requested Format) (decodedEvals, error) {
	if requested != FormatAuto && requested != FormatAgentSkillsGuideV1 && requested != FormatAnthropicSkillCreatorV1 {
		return decodedEvals{}, contractError(ErrorInvalidRequest, nil)
	}
	root, err := decodeBoundedJSONObject(data, ErrorInvalidEvals)
	if err != nil {
		return decodedEvals{}, err
	}
	if err := requireJSONMembers(root, []string{"skill_name", "evals"}, nil); err != nil {
		return decodedEvals{}, contractError(ErrorInvalidEvals, err)
	}
	skillName, err := decodeJSONString(root["skill_name"], 64, false)
	if err != nil || !validSkillName(skillName) {
		return decodedEvals{}, contractError(ErrorInvalidEvals, err)
	}
	var rawCases []json.RawMessage
	if err := json.Unmarshal(root["evals"], &rawCases); err != nil || len(rawCases) == 0 || len(rawCases) > MaxCases {
		if len(rawCases) > MaxCases {
			return decodedEvals{}, contractError(ErrorLimitExceeded, err)
		}
		return decodedEvals{}, contractError(ErrorInvalidEvals, err)
	}

	result := decodedEvals{skillName: skillName, cases: make([]decodedCase, 0, len(rawCases))}
	seenIDs := make(map[uint32]struct{}, len(rawCases))
	detected := Format("")
	for _, rawCase := range rawCases {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(rawCase, &object); err != nil || object == nil {
			return decodedEvals{}, contractError(ErrorInvalidEvals, err)
		}
		if err := requireJSONMembers(object, []string{"id", "prompt", "expected_output"}, []string{"files", "assertions", "expectations"}); err != nil {
			return decodedEvals{}, contractError(ErrorInvalidEvals, err)
		}
		_, assertions := object["assertions"]
		_, expectations := object["expectations"]
		if assertions && expectations {
			return decodedEvals{}, contractError(ErrorInvalidEvals, fmt.Errorf("ambiguous criteria spelling"))
		}
		caseFormat := Format("")
		if assertions {
			caseFormat = FormatAgentSkillsGuideV1
		}
		if expectations {
			caseFormat = FormatAnthropicSkillCreatorV1
		}
		if caseFormat != "" {
			if detected != "" && detected != caseFormat {
				return decodedEvals{}, contractError(ErrorInvalidEvals, fmt.Errorf("mixed criteria spelling"))
			}
			detected = caseFormat
		}
		if requested != FormatAuto && caseFormat != "" && requested != caseFormat {
			return decodedEvals{}, contractError(ErrorInvalidEvals, fmt.Errorf("criteria spelling does not match format"))
		}

		var current decodedCase
		if err := json.Unmarshal(object["id"], &current.id); err != nil {
			return decodedEvals{}, contractError(ErrorInvalidEvals, err)
		}
		if _, duplicate := seenIDs[current.id]; duplicate {
			return decodedEvals{}, contractError(ErrorInvalidEvals, fmt.Errorf("duplicate id"))
		}
		seenIDs[current.id] = struct{}{}
		current.prompt, err = decodeJSONString(object["prompt"], MaxTextBytes, false)
		if err != nil {
			return decodedEvals{}, contractError(ErrorInvalidEvals, err)
		}
		current.expectedOutput, err = decodeJSONString(object["expected_output"], MaxTextBytes, false)
		if err != nil {
			return decodedEvals{}, contractError(ErrorInvalidEvals, err)
		}
		if rawFiles, ok := object["files"]; ok {
			current.filesPresent = true
			current.files, err = decodeStringArray(rawFiles, MaxFilesPerCase, MaxPathBytes)
			if err != nil {
				return decodedEvals{}, contractError(ErrorInvalidEvals, err)
			}
			seenPaths := make(map[string]struct{}, len(current.files))
			for _, file := range current.files {
				if !validSourcePath(file) {
					return decodedEvals{}, contractError(ErrorInvalidEvals, fmt.Errorf("invalid file path"))
				}
				if _, duplicate := seenPaths[file]; duplicate {
					return decodedEvals{}, contractError(ErrorInvalidEvals, fmt.Errorf("duplicate file path"))
				}
				seenPaths[file] = struct{}{}
			}
		}
		if caseFormat != "" {
			current.criteriaPresent = true
			current.criterionField = "assertions"
			current.criterionKind = CriterionAssertion
			if caseFormat == FormatAnthropicSkillCreatorV1 {
				current.criterionField = "expectations"
				current.criterionKind = CriterionExpectation
			}
			current.criteria, err = decodeStringArray(object[current.criterionField], MaxCriteriaPerCase, MaxTextBytes)
			if err != nil {
				return decodedEvals{}, contractError(ErrorInvalidEvals, err)
			}
		}
		result.cases = append(result.cases, current)
	}

	if requested == FormatAuto {
		if detected == "" {
			return decodedEvals{}, contractError(ErrorInvalidEvals, fmt.Errorf("format is ambiguous"))
		}
		result.format = detected
	} else {
		result.format = requested
	}
	return result, nil
}

func validSkillName(value string) bool {
	if value == "" || len(value) > 64 || value[0] == '-' || value[len(value)-1] == '-' || strings.Contains(value, "--") {
		return false
	}
	for _, character := range []byte(value) {
		if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func validSourcePath(value string) bool {
	if value == "" || len(value) > MaxPathBytes || !utf8.ValidString(value) || !fs.ValidPath(value) || path.Clean(value) != value ||
		strings.ContainsAny(value, "\\\x00") || strings.HasPrefix(value, "/") {
		return false
	}
	if len(value) >= 2 && value[1] == ':' && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) {
		return false
	}
	return true
}
