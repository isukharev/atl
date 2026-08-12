package grading

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

func decodeEvidenceJSON(data []byte) (any, bool) {
	if len(data) == 0 || len(data) > MaxEvidenceBytes || validateJSONShape(data) != nil {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	return value, true
}

func resolveJSONPointer(value any, pointer string) (any, bool) {
	if pointer == "" {
		return value, true
	}
	current := value
	for _, raw := range strings.Split(pointer[1:], "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch typed := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = typed[part]
			if !ok {
				return nil, false
			}
		case []any:
			if part == "" || len(part) > 1 && part[0] == '0' {
				return nil, false
			}
			index, err := strconv.ParseUint(part, 10, 31)
			if err != nil || index >= uint64(len(typed)) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func jsonValueEqual(value any, expected json.RawMessage) bool {
	actual, err := json.Marshal(value)
	return err == nil && bytes.Equal(actual, expected)
}

func jsonValueHasType(value any, want JSONType) bool {
	switch want {
	case JSONTypeAny:
		return true
	case JSONTypeNull:
		return value == nil
	case JSONTypeBoolean:
		_, ok := value.(bool)
		return ok
	case JSONTypeString:
		_, ok := value.(string)
		return ok
	case JSONTypeArray:
		_, ok := value.([]any)
		return ok
	case JSONTypeObject:
		_, ok := value.(map[string]any)
		return ok
	case JSONTypeNumber:
		_, ok := value.(json.Number)
		return ok
	case JSONTypeInteger:
		number, ok := value.(json.Number)
		return ok && jsonNumberIsInteger(number.String())
	default:
		return false
	}
}

func jsonNumberIsInteger(value string) bool {
	unsigned := strings.TrimPrefix(value, "-")
	exponent := 0
	if position := strings.IndexAny(unsigned, "eE"); position >= 0 {
		exponent = boundedDecimalExponent(unsigned[position+1:], len(unsigned)+1)
		unsigned = unsigned[:position]
	}
	fractionDigits := 0
	if point := strings.IndexByte(unsigned, '.'); point >= 0 {
		fractionDigits = len(unsigned) - point - 1
		unsigned = unsigned[:point] + unsigned[point+1:]
	}
	scale := fractionDigits - exponent
	if scale <= 0 {
		return true
	}
	if scale > len(unsigned) {
		return strings.Trim(unsigned, "0") == ""
	}
	return strings.Trim(unsigned[len(unsigned)-scale:], "0") == ""
}

func boundedDecimalExponent(value string, limit int) int {
	sign := 1
	if strings.HasPrefix(value, "+") {
		value = value[1:]
	} else if strings.HasPrefix(value, "-") {
		sign = -1
		value = value[1:]
	}
	result := 0
	for _, digit := range value {
		if result > (limit-int(digit-'0'))/10 {
			return sign * limit
		}
		result = result*10 + int(digit-'0')
	}
	return sign * result
}
