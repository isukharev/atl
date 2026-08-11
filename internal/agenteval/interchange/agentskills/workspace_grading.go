package agentskills

import (
	"encoding/json"
	"fmt"
	"math/big"
)

type decodedGrading struct {
	view              GradingView
	timingDetailCount uint32
	precisionLoss     bool
}

func decodeWorkspaceGrading(data []byte, format Format) (decodedGrading, error) {
	root, err := decodeBoundedJSONObject(data, ErrorInvalidWorkspace)
	if err != nil {
		return decodedGrading{}, err
	}
	resultField, textField := "assertion_results", "assertion"
	required := []string{"summary", resultField}
	optional := []string{"feedback"}
	if format == FormatAnthropicSkillCreatorV1 {
		resultField, textField = "expectations", "text"
		required = []string{"summary", resultField}
		optional = []string{"timing"}
	}
	if err := requireJSONMembers(root, required, optional); err != nil {
		return decodedGrading{}, contractError(ErrorInvalidWorkspace, err)
	}

	results, passed, err := decodeGradeResults(root[resultField], textField)
	if err != nil {
		return decodedGrading{}, contractError(ErrorInvalidWorkspace, err)
	}
	if err := validateGradeSummary(root["summary"], countSlice(results), passed); err != nil {
		return decodedGrading{}, contractError(ErrorInvalidWorkspace, err)
	}
	decoded := decodedGrading{view: GradingView{
		Results:               results,
		DurationMillis:        OptionalUint64{Presence: MetricUnknown},
		TotalTokens:           OptionalUint64{Presence: MetricUnknown},
		EstimatedCostMicroUSD: OptionalUint64{Presence: MetricUnsupported},
	}}
	if raw, ok := root["feedback"]; ok {
		decoded.view.Feedback, err = decodeFeedback(raw)
		if err != nil {
			return decodedGrading{}, contractError(ErrorInvalidWorkspace, err)
		}
		decoded.view.FeedbackPresent = true
	}
	if raw, ok := root["timing"]; ok {
		var timing map[string]json.RawMessage
		if err := json.Unmarshal(raw, &timing); err != nil || timing == nil {
			return decodedGrading{}, contractError(ErrorInvalidWorkspace, fmt.Errorf("grading timing"))
		}
		if err := requireJSONMembers(timing, nil, []string{
			"executor_duration_seconds", "grader_duration_seconds", "total_duration_seconds",
		}); err != nil {
			return decodedGrading{}, contractError(ErrorInvalidWorkspace, err)
		}
		if len(timing) == 0 {
			return decodedGrading{}, contractError(ErrorInvalidWorkspace, fmt.Errorf("empty grading timing"))
		}
		for name, value := range timing {
			number, err := decodeJSONNumber(value)
			if err != nil {
				return decodedGrading{}, contractError(ErrorInvalidWorkspace, err)
			}
			milliseconds, exact, err := secondsToMilliseconds(number.String())
			if err != nil {
				return decodedGrading{}, contractError(ErrorInvalidWorkspace, err)
			}
			if name == "total_duration_seconds" {
				if exact {
					decoded.view.DurationMillis = OptionalUint64{Presence: MetricObserved, Value: milliseconds}
				} else {
					decoded.view.DurationMillis = OptionalUint64{Presence: MetricUnsupported}
					decoded.precisionLoss = true
				}
			} else {
				decoded.timingDetailCount++
			}
		}
	}
	if err := validateGradingView(decoded.view); err != nil {
		return decodedGrading{}, contractError(ErrorInvalidWorkspace, err)
	}
	return decoded, nil
}

func decodeGradeResults(raw json.RawMessage, textField string) ([]GradeResult, uint32, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil || len(values) > MaxCriteriaPerCase+1 {
		return nil, 0, fmt.Errorf("grading results")
	}
	results := make([]GradeResult, 0, len(values))
	var passed uint32
	for _, value := range values {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(value, &object); err != nil || object == nil {
			return nil, 0, fmt.Errorf("grading result")
		}
		if err := requireJSONMembers(object, []string{textField, "passed", "evidence"}, nil); err != nil {
			return nil, 0, err
		}
		text, err := decodeJSONString(object[textField], MaxTextBytes, false)
		if err != nil {
			return nil, 0, err
		}
		evidence, err := decodeJSONString(object["evidence"], MaxTextBytes, true)
		if err != nil {
			return nil, 0, err
		}
		valuePassed, err := decodeJSONBool(object["passed"])
		if err != nil {
			return nil, 0, err
		}
		if valuePassed {
			passed++
		}
		results = append(results, GradeResult{Text: text, Passed: valuePassed, Evidence: evidence})
	}
	return results, passed, nil
}

func validateGradeSummary(raw json.RawMessage, resultCount, observedPassed uint32) error {
	var summary map[string]json.RawMessage
	if err := json.Unmarshal(raw, &summary); err != nil || summary == nil {
		return fmt.Errorf("grading summary")
	}
	if err := requireJSONMembers(summary, []string{"passed", "failed", "total", "pass_rate"}, nil); err != nil {
		return err
	}
	passed, err := decodeJSONUint32(summary["passed"])
	if err != nil {
		return err
	}
	failed, err := decodeJSONUint32(summary["failed"])
	if err != nil {
		return err
	}
	total, err := decodeJSONUint32(summary["total"])
	if err != nil {
		return err
	}
	passRate, err := decodeJSONNumber(summary["pass_rate"])
	if err != nil || !validUnitInterval(passRate.String()) || total != resultCount || passed != observedPassed ||
		passed > total || failed != total-passed {
		return fmt.Errorf("inconsistent grading summary")
	}
	rate, ok := new(big.Rat).SetString(passRate.String())
	want := new(big.Rat)
	if total != 0 {
		want.SetFrac64(int64(passed), int64(total))
	}
	if !ok || rate.Cmp(want) != 0 {
		return fmt.Errorf("inconsistent grading pass rate")
	}
	return nil
}
