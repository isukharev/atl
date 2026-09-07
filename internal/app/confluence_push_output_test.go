package app

import (
	"encoding/json"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

func TestPushItemMarshalJSONDryRunReviewFields(t *testing.T) {
	tests := []struct {
		name string
		item PushItem
		want map[string]any
	}{
		{
			name: "clean",
			item: PushItem{Path: "clean.csf", ID: "101", DryRun: true},
			want: map[string]any{
				"remote_drifted":    false,
				"added_fragments":   []any{},
				"removed_fragments": []any{},
				"problems":          []any{},
			},
		},
		{
			name: "changed",
			item: PushItem{
				Path: "changed.csf", ID: "102", DryRun: true, Drifted: true,
				Added:   []domain.Ref{{Kind: domain.RefAttachment, Key: "new.png"}},
				Removed: []domain.Ref{{Kind: domain.RefDrawio, Key: "diagram"}},
				Problems: []csf.Problem{{
					Severity: "warning", Rule: "review", Message: "review candidate",
				}},
			},
			want: map[string]any{
				"remote_drifted": true,
				"added_fragments": []any{map[string]any{
					"kind": "attachment", "key": "new.png",
				}},
				"removed_fragments": []any{map[string]any{
					"kind": "drawio", "key": "diagram",
				}},
				"problems": []any{map[string]any{
					"severity": "warning", "rule": "review", "message": "review candidate",
				}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(tt.item)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			for field, want := range tt.want {
				if encodedWant, _ := json.Marshal(want); string(encodedWant) != mustMarshalField(t, got, field) {
					t.Errorf("%s = %s, want %s; output=%s", field, mustMarshalField(t, got, field), encodedWant, encoded)
				}
			}
		})
	}
}

func TestPushItemMarshalJSONApplyKeepsNeutralReviewFieldsOmitted(t *testing.T) {
	encoded, err := json.Marshal(PushItem{Path: "page.csf", ID: "101", Pushed: true, NewVersion: 8})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, field := range []string{"remote_drifted", "added_fragments", "removed_fragments", "problems"} {
		if _, ok := got[field]; ok {
			t.Errorf("apply output unexpectedly contains %q: %s", field, encoded)
		}
	}
}

func mustMarshalField(t *testing.T, object map[string]any, field string) string {
	t.Helper()
	value, ok := object[field]
	if !ok {
		t.Fatalf("output omitted %q: %+v", field, object)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", field, err)
	}
	return string(encoded)
}
