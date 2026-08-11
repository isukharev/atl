package agentskills

import (
	"encoding/json"
	"fmt"
)

type decodedTiming struct {
	duration    OptionalUint64
	tokens      OptionalUint64
	detailCount uint32
}

func decodeGuideTiming(data []byte) (decodedTiming, error) {
	root, err := decodeBoundedJSONObject(data, ErrorInvalidWorkspace)
	if err != nil {
		return decodedTiming{}, err
	}
	if err := requireJSONMembers(root, []string{"total_tokens", "duration_ms"}, nil); err != nil {
		return decodedTiming{}, contractError(ErrorInvalidWorkspace, err)
	}
	tokens, err := decodeJSONUint64(root["total_tokens"])
	if err != nil {
		return decodedTiming{}, contractError(ErrorInvalidWorkspace, err)
	}
	duration, err := decodeJSONUint64(root["duration_ms"])
	if err != nil {
		return decodedTiming{}, contractError(ErrorInvalidWorkspace, err)
	}
	return decodedTiming{
		duration: OptionalUint64{Presence: MetricObserved, Value: duration},
		tokens:   OptionalUint64{Presence: MetricObserved, Value: tokens},
	}, nil
}

func decodeAnthropicTiming(data []byte) (decodedTiming, error) {
	root, err := decodeBoundedJSONObject(data, ErrorInvalidWorkspace)
	if err != nil {
		return decodedTiming{}, err
	}
	if err := requireJSONMembers(root, []string{"total_tokens", "duration_ms"}, []string{
		"total_duration_seconds", "executor_start", "executor_end", "executor_duration_seconds",
		"grader_start", "grader_end", "grader_duration_seconds",
	}); err != nil {
		return decodedTiming{}, contractError(ErrorInvalidWorkspace, err)
	}
	tokens, err := decodeJSONUint64(root["total_tokens"])
	if err != nil {
		return decodedTiming{}, contractError(ErrorInvalidWorkspace, err)
	}
	duration, err := decodeJSONUint64(root["duration_ms"])
	if err != nil {
		return decodedTiming{}, contractError(ErrorInvalidWorkspace, err)
	}
	result := decodedTiming{
		duration: OptionalUint64{Presence: MetricObserved, Value: duration},
		tokens:   OptionalUint64{Presence: MetricObserved, Value: tokens},
	}
	for _, name := range []string{"executor_start", "executor_end", "grader_start", "grader_end"} {
		if raw, ok := root[name]; ok {
			if _, err := decodeJSONString(raw, 128, false); err != nil {
				return decodedTiming{}, contractError(ErrorInvalidWorkspace, err)
			}
			result.detailCount++
		}
	}
	for _, name := range []string{"executor_duration_seconds", "grader_duration_seconds", "total_duration_seconds"} {
		raw, ok := root[name]
		if !ok {
			continue
		}
		number, err := decodeJSONNumber(raw)
		if err != nil {
			return decodedTiming{}, contractError(ErrorInvalidWorkspace, err)
		}
		milliseconds, exact, err := secondsToMilliseconds(number.String())
		if err != nil {
			return decodedTiming{}, contractError(ErrorInvalidWorkspace, err)
		}
		if name == "total_duration_seconds" && exact && milliseconds != duration {
			return decodedTiming{}, contractError(ErrorInvalidWorkspace, fmt.Errorf("inconsistent total duration"))
		}
		result.detailCount++
	}
	return result, nil
}

func decodeOptionalDuration(raw json.RawMessage) (OptionalUint64, bool, error) {
	number, err := decodeJSONNumber(raw)
	if err != nil {
		return OptionalUint64{}, false, err
	}
	milliseconds, exact, err := secondsToMilliseconds(number.String())
	if err != nil {
		return OptionalUint64{}, false, err
	}
	if !exact {
		return OptionalUint64{Presence: MetricUnsupported}, true, nil
	}
	return OptionalUint64{Presence: MetricObserved, Value: milliseconds}, false, nil
}
