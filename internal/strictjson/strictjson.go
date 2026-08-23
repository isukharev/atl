// Package strictjson provides transport-neutral JSON validation for security-
// sensitive evidence. It rejects byte sequences that encoding/json otherwise
// normalizes, while preserving json.Number for decoded numeric values.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// MaxNestingDepth matches encoding/json's supported nesting ceiling. Keeping
// it explicit lets classification callers reject over-deep structured input
// instead of silently treating a decoder depth failure as an ordinary string.
const MaxNestingDepth = 10_000

var ErrNestingDepth = errors.New("JSON nesting exceeds the supported maximum")

type validationFrame struct {
	kind      json.Delim
	expectKey bool
	seen      map[string]struct{}
}

// Decode validates and decodes exactly one JSON value. Duplicate object
// members, invalid UTF-8, unpaired surrogate escapes, and trailing values or
// non-whitespace bytes are rejected.
func Decode(data []byte, out any) error {
	if err := Validate(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(out)
}

// DecodeValue is Decode for an open dynamic JSON value.
func DecodeValue(data []byte) (any, error) {
	var value any
	if err := Decode(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// DecodeFirst decodes the first complete JSON value without applying strict
// duplicate or surrogate rules and returns the decoder's byte offset. It is a
// narrow classification seam: callers must use Decode or Validate before
// trusting a successfully classified structured value.
func DecodeFirst(data []byte) (value any, end int64, err error) {
	if err := ValidateNestingDepth(data, MaxNestingDepth); err != nil {
		return nil, 0, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, 0, err
	}
	return value, decoder.InputOffset(), nil
}

// Validate checks exactly one complete JSON value without recursive descent.
func Validate(data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("JSON is not valid UTF-8")
	}
	if err := ValidateNestingDepth(data, MaxNestingDepth); err != nil {
		return err
	}
	if !ValidUnicodeEscapes(data) {
		return fmt.Errorf("JSON contains an unpaired Unicode surrogate escape")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	stack := make([]validationFrame, 0, 16)
	rootSeen := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if !rootSeen || len(stack) != 0 {
				return fmt.Errorf("JSON value is incomplete")
			}
			return nil
		}
		if err != nil {
			return err
		}
		if len(stack) == 0 {
			if rootSeen {
				return fmt.Errorf("JSON has trailing data")
			}
			rootSeen = true
		}
		delimiter, compound := token.(json.Delim)
		if compound && (delimiter == '}' || delimiter == ']') {
			if len(stack) == 0 || stack[len(stack)-1].kind+2 != delimiter {
				return fmt.Errorf("JSON compound delimiter is malformed")
			}
			if delimiter == '}' && !stack[len(stack)-1].expectKey {
				return fmt.Errorf("JSON object value is missing")
			}
			stack = stack[:len(stack)-1]
			continue
		}
		if len(stack) > 0 {
			parent := &stack[len(stack)-1]
			if parent.kind == '{' && parent.expectKey {
				name, ok := token.(string)
				if !ok {
					return fmt.Errorf("JSON object member name is malformed")
				}
				if _, duplicate := parent.seen[name]; duplicate {
					return fmt.Errorf("JSON object contains a duplicate member")
				}
				parent.seen[name] = struct{}{}
				parent.expectKey = false
				continue
			}
		}
		if compound {
			switch delimiter {
			case '{':
				markParentValueComplete(stack)
				stack = append(stack, validationFrame{kind: delimiter, expectKey: true, seen: map[string]struct{}{}})
			case '[':
				markParentValueComplete(stack)
				stack = append(stack, validationFrame{kind: delimiter})
			default:
				return fmt.Errorf("JSON delimiter is malformed")
			}
			continue
		}
		markParentValueComplete(stack)
	}
}

// ValidateNestingDepth is deliberately syntax-light: the JSON decoder remains
// the syntax authority, while this pass only supplies a stable, content-free
// failure before encoding/json reaches its own depth error. It can also enforce
// a smaller envelope-derived value limit without changing the parser ceiling.
func ValidateNestingDepth(data []byte, maximum int) error {
	if maximum < 0 || maximum > MaxNestingDepth {
		return fmt.Errorf("JSON nesting limit is invalid")
	}
	depth := 0
	inString := false
	escaped := false
	for _, current := range data {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch current {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch current {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > maximum {
				return fmt.Errorf("%w (%d)", ErrNestingDepth, maximum)
			}
		case '}', ']':
			if depth > 0 {
				depth--
			}
		}
	}
	return nil
}

func markParentValueComplete(stack []validationFrame) {
	if len(stack) > 0 && stack[len(stack)-1].kind == '{' {
		stack[len(stack)-1].expectKey = true
	}
}

// ValidUnicodeEscapes rejects surrogate escapes that encoding/json otherwise
// replaces with U+FFFD. General JSON syntax remains Validate's responsibility.
func ValidUnicodeEscapes(data []byte) bool {
	inString := false
	for index := 0; index < len(data); index++ {
		switch data[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString || index+1 >= len(data) {
				continue
			}
			if data[index+1] != 'u' {
				index++
				continue
			}
			value, ok := hexEscape(data[index+2:])
			if !ok {
				continue
			}
			escapeEnd := index + 6
			switch {
			case value >= 0xdc00 && value <= 0xdfff:
				return false
			case value >= 0xd800 && value <= 0xdbff:
				if escapeEnd+6 > len(data) || data[escapeEnd] != '\\' || data[escapeEnd+1] != 'u' {
					return false
				}
				low, valid := hexEscape(data[escapeEnd+2:])
				if !valid || low < 0xdc00 || low > 0xdfff {
					return false
				}
				index = escapeEnd + 5
			default:
				index += 5
			}
		}
	}
	return true
}

func hexEscape(data []byte) (uint16, bool) {
	if len(data) < 4 {
		return 0, false
	}
	var value uint16
	for _, current := range data[:4] {
		value <<= 4
		switch {
		case current >= '0' && current <= '9':
			value += uint16(current - '0')
		case current >= 'a' && current <= 'f':
			value += uint16(current-'a') + 10
		case current >= 'A' && current <= 'F':
			value += uint16(current-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
