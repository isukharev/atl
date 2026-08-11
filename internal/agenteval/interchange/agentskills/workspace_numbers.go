package agentskills

import (
	"fmt"
	"strconv"
	"strings"
)

func secondsToMilliseconds(number string) (uint64, bool, error) {
	if number == "" || len(number) > 128 || number[0] == '-' {
		return 0, false, fmt.Errorf("invalid duration")
	}
	mantissa, exponentText := number, ""
	if index := strings.IndexAny(number, "eE"); index >= 0 {
		mantissa, exponentText = number[:index], number[index+1:]
	}
	exponent := 0
	if exponentText != "" {
		parsed, err := strconv.Atoi(exponentText)
		if err != nil || parsed < -1000 || parsed > 1000 {
			return 0, false, fmt.Errorf("invalid duration exponent")
		}
		exponent = parsed
	}
	whole, fraction := mantissa, ""
	if index := strings.IndexByte(mantissa, '.'); index >= 0 {
		whole, fraction = mantissa[:index], mantissa[index+1:]
	}
	if whole == "" || fraction == "" && strings.Contains(mantissa, ".") {
		return 0, false, fmt.Errorf("invalid duration mantissa")
	}
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return 0, true, nil
	}
	for _, character := range []byte(digits) {
		if character < '0' || character > '9' {
			return 0, false, fmt.Errorf("invalid duration digit")
		}
	}
	shift := 3 - (len(fraction) - exponent)
	if shift >= 0 {
		if len(digits)+shift > 20 {
			return 0, false, fmt.Errorf("duration overflow")
		}
		digits += strings.Repeat("0", shift)
	} else {
		cut := -shift
		if cut >= len(digits) {
			if strings.Trim(digits, "0") != "" {
				return 0, false, nil
			}
			return 0, true, nil
		}
		if strings.Trim(digits[len(digits)-cut:], "0") != "" {
			return 0, false, nil
		}
		digits = digits[:len(digits)-cut]
	}
	value, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("duration overflow")
	}
	return value, true, nil
}

func validUnitInterval(number string) bool {
	value, err := strconv.ParseFloat(number, 64)
	return err == nil && value >= 0 && value <= 1
}
