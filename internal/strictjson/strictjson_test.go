package strictjson

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDecodeRejectsNormalizedOrTrailingEvidence(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		ok   bool
	}{
		{name: "object", body: []byte(`{"a":[{"b":1}]}`), ok: true},
		{name: "valid pair", body: []byte(`{"emoji":"\uD83D\uDE00"}`), ok: true},
		{name: "outer duplicate", body: []byte(`{"a":1,"a":2}`)},
		{name: "nested duplicate", body: []byte(`{"a":{"b":1,"b":2}}`)},
		{name: "array duplicate", body: []byte(`[{"a":1,"a":2}]`)},
		{name: "trailing value", body: []byte(`{} []`)},
		{name: "trailing byte", body: []byte(`{} x`)},
		{name: "high surrogate", body: []byte(`{"x":"\uD800"}`)},
		{name: "low surrogate", body: []byte(`{"x":"\uDC00"}`)},
		{name: "malformed pair", body: []byte(`{"x":"\uD800\u0041"}`)},
		{name: "invalid utf8", body: []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var value any
			err := Decode(test.body, &value)
			if (err == nil) != test.ok {
				t.Fatalf("Decode err=%v ok=%t", err, test.ok)
			}
		})
	}
}

func TestDecodePreservesJSONNumberAndDeepNestingIteratively(t *testing.T) {
	var value any
	if err := Decode([]byte(`{"n":9007199254740993}`), &value); err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["n"] != json.Number("9007199254740993") {
		t.Fatalf("number=%#v", value)
	}
	deep := strings.Repeat("[", MaxNestingDepth) + "0" + strings.Repeat("]", MaxNestingDepth)
	if err := Validate([]byte(deep)); err != nil {
		t.Fatalf("deep valid value: %v", err)
	}
	tooDeep := strings.Repeat("[", MaxNestingDepth+1) + "0" + strings.Repeat("]", MaxNestingDepth+1)
	if err := Validate([]byte(tooDeep)); !errors.Is(err, ErrNestingDepth) {
		t.Fatalf("over-depth error=%v", err)
	}
	incompleteTooDeep := strings.Repeat("[", MaxNestingDepth+1)
	if _, _, err := DecodeFirst([]byte(incompleteTooDeep)); !errors.Is(err, ErrNestingDepth) {
		t.Fatalf("over-depth incomplete error=%v", err)
	}
}

func TestDuplicateMemberDiagnosticIsContentFree(t *testing.T) {
	secretMember := "private_member_marker"
	err := Validate([]byte(`{"` + secretMember + `":1,"` + secretMember + `":2}`))
	if err == nil || strings.Contains(err.Error(), secretMember) {
		t.Fatalf("duplicate diagnostic leaked member name: %v", err)
	}
}

func TestDecodeFirstClassifiesIncompleteBeforeStrictValidation(t *testing.T) {
	for _, body := range []string{`{"a":1,"a":`, `{"x":"\uD800`} {
		if _, _, err := DecodeFirst([]byte(body)); err == nil {
			t.Fatalf("incomplete body %q decoded", body)
		}
	}
	if _, _, err := DecodeFirst([]byte(`{"a":1,"a":2}`)); err != nil {
		t.Fatalf("complete duplicate must reach strict boundary: %v", err)
	}
}

func FuzzValidate(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"a":1}`), []byte(`{"a":1,"a":2}`), []byte(`{"a":{"b":1,"b":2}}`),
		[]byte(`{} []`), []byte(`{} non-json-tail`),
		[]byte(`{"x":"\uD800"}`), []byte(`{"x":"\uDC00"}`), []byte(`{"x":"\uD800\u0041"}`), []byte(`{"x":"\uD83D\uDE00"}`),
		{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var value any
		decodeErr := Decode(data, &value)
		validateErr := Validate(data)
		if (decodeErr == nil) != (validateErr == nil) {
			t.Fatalf("Decode err=%v Validate err=%v", decodeErr, validateErr)
		}
		if decodeErr == nil {
			if !json.Valid(data) {
				t.Fatal("strict decoder accepted bytes that are not exactly one valid JSON value")
			}
			canonical, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("marshal accepted value: %v", err)
			}
			var roundTrip any
			if err := Decode(canonical, &roundTrip); err != nil {
				t.Fatalf("strict roundtrip: %v", err)
			}
		}
	})
}
