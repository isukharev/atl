package confluence

import (
	"encoding/json"
	"testing"
)

func FuzzContentEvidenceProjection(f *testing.F) {
	for _, seed := range []string{`{"id":"123","version":{"number":4}}`, `{"ID":"123"}`, `{"version":{"number":3,"Number":4}}`, `{"version":{"number":3,"number":4}}`, `{"restrictions":{"read":{"restrictions":{"group":{"results":[{}]}}}}}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			return
		}
		page, err := decodeContentEvidence(data)
		if err != nil {
			return
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) != nil {
			t.Fatal("accepted non-object")
		}
		var id string
		_ = json.Unmarshal(object["id"], &id)
		if page.ID != id {
			t.Fatal("non-canonical identity influenced projection")
		}
		var version map[string]json.RawMessage
		_ = json.Unmarshal(object["version"], &version)
		var number int
		_ = json.Unmarshal(version["number"], &number)
		if page.Version.Number != number {
			t.Fatal("non-canonical version influenced projection")
		}
	})
}
