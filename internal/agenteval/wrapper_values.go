package agenteval

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// WrapperAuthorityEnabled intentionally accepts only the one reviewed
// presence value. Similar-looking truthy values must never grant a wrapper
// write capability.
func WrapperAuthorityEnabled(value string) bool {
	return value == "1"
}

// DecodeWrapperStringList owns only the common JSON decoding operation. Callers
// retain their distinct empty, malformed, and policy-error behavior.
func DecodeWrapperStringList(value string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(value), &values); err != nil {
		return nil, err
	}
	return values, nil
}

// ParseWrapperDelegationLimit preserves strconv.Atoi compatibility, including
// accepted leading signs and zeroes, while bounding the reviewed policy to
// 0..3. Slot allocation and counter-path validation remain wrapper-local.
func ParseWrapperDelegationLimit(value string) (int, error) {
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 0 || limit > 3 {
		return 0, fmt.Errorf("invalid delegation limit")
	}
	return limit, nil
}
