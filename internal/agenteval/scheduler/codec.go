package scheduler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

func EncodePlan(plan Plan) ([]byte, error) {
	if err := ValidatePlan(plan); err != nil {
		return nil, err
	}
	return canonicalJSON(plan, MaxPlanBytes)
}

func DecodePlan(reader io.Reader) (Plan, error) {
	var plan Plan
	data, err := readBounded(reader, MaxPlanBytes)
	if err != nil || validateJSONStructure(data) != nil || decodeClosed(data, &plan) != nil || ValidatePlan(plan) != nil {
		return Plan{}, contractError("plan_decode")
	}
	want, err := canonicalJSON(plan, MaxPlanBytes)
	if err != nil || !bytes.Equal(data, want) {
		return Plan{}, contractError("plan_canonical")
	}
	return clonePlan(plan), nil
}

func EncodeReport(plan Plan, report Report) ([]byte, error) {
	if err := ValidateReport(plan, report); err != nil {
		return nil, err
	}
	return canonicalJSON(report, MaxReportBytes)
}

func DecodeReport(reader io.Reader, plan Plan) (Report, error) {
	var report Report
	data, err := readBounded(reader, MaxReportBytes)
	if err != nil || validateJSONStructure(data) != nil || decodeClosed(data, &report) != nil || ValidateReport(plan, report) != nil {
		return Report{}, contractError("report_decode")
	}
	want, err := canonicalJSON(report, MaxReportBytes)
	if err != nil || !bytes.Equal(data, want) {
		return Report{}, contractError("report_canonical")
	}
	return report, nil
}

func canonicalJSON(value any, maximum int64) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, contractError("encode")
	}
	data = append(data, '\n')
	if int64(len(data)) > maximum {
		return nil, contractError("encode_limit")
	}
	return data, nil
}

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	if reader == nil || maximum < 1 {
		return nil, contractError("read")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, contractError("read_limit")
	}
	return data, nil
}

func decodeClosed(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return contractError("trailing_json")
	}
	return nil
}

type jsonCounter struct {
	values int
}

func validateJSONStructure(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	counter := &jsonCounter{}
	if err := consumeJSONValue(decoder, "", 0, counter); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return contractError("json_trailing")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder, path string, depth int, counter *jsonCounter) error {
	if depth > MaxJSONDepth {
		return contractError("json_depth")
	}
	counter.values++
	if counter.values > MaxTasks*(MaxTaskCohorts+32) {
		return contractError("json_values")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			key, ok := keyToken.(string)
			if keyErr != nil || !ok {
				return contractError("json_member")
			}
			if _, duplicate := seen[key]; duplicate {
				return contractError("json_duplicate")
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder, path+"/"+key, depth+1, counter); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim('}') {
			return contractError("json_object")
		}
		return nil
	case '[':
		limit := jsonArrayLimit(path)
		count := 0
		for decoder.More() {
			count++
			if count > limit {
				return contractError("json_array_limit")
			}
			if err := consumeJSONValue(decoder, path+"[]", depth+1, counter); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim(']') {
			return contractError("json_array")
		}
		return nil
	default:
		return fmt.Errorf("%w: json delimiter %q", ErrContract, delimiter)
	}
}

func jsonArrayLimit(path string) int {
	switch path {
	case "/tasks":
		return MaxTasks
	case "/limits/cohorts":
		return MaxCohorts
	case "/tasks[]/cohort_sha256s":
		return MaxTaskCohorts
	default:
		// Unknown members are rejected by the typed decoder. Bounding them here
		// prevents an unknown large array from becoming a prevalidation sink.
		if strings.Contains(path, "cohort") {
			return MaxCohorts
		}
		return MaxTasks
	}
}
