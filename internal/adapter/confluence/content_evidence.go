package confluence

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

// decodeContentEvidence retains forward-compatible extra fields, but never
// lets duplicate or case-aliased members change the typed page projection.
func decodeContentEvidence(raw []byte) (content, error) {
	var result content
	if strictjson.Validate(raw) != nil || !canonicalContentMembers(raw, reflect.TypeFor[content]()) || json.Unmarshal(raw, &result) != nil {
		return content{}, fmt.Errorf("%w: page response is not canonical content evidence", domain.ErrCheckFailed)
	}
	return result, nil
}

func canonicalContentMembers(raw json.RawMessage, typ reflect.Type) bool {
	if string(raw) == "null" {
		return true
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeFor[json.RawMessage]() {
		return true
	}
	switch typ.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil {
			return false
		}
		for key, value := range object {
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
				if name == "" {
					name = field.Name
				}
				if !strings.EqualFold(key, name) {
					continue
				}
				if key != name || !canonicalContentMembers(value, field.Type) {
					return false
				}
				break
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		for _, value := range values {
			if !canonicalContentMembers(value, typ.Elem()) {
				return false
			}
		}
	}
	return true
}
