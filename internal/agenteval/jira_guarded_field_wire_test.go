package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDecodeJiraGuardedFieldResultAcceptsReleasedPreview(t *testing.T) {
	result, err := DecodeJiraGuardedFieldResult(strings.NewReader(jiraGuardedFieldWireFixture()))
	if err != nil {
		t.Fatal(err)
	}
	if result.ProposalHash != strings.Repeat("a", 64) || result.Mode != "dry-run" || result.Status != "would_apply" ||
		result.RequestedKey != "PROJ-1" || result.IssueID != "12001" || result.WriteAttempted || result.Reconciled || !result.Complete {
		t.Fatalf("unexpected content-free projection: %+v", result)
	}
}

func TestDecodeJiraGuardedFieldResultAcceptsIncompleteBlockedTruth(t *testing.T) {
	var wire map[string]any
	if err := json.Unmarshal([]byte(jiraGuardedFieldWireFixture()), &wire); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"backend_sha256", "issue_id", "project", "expected_updated", "actual_updated", "proposal_hash"} {
		wire[name] = ""
	}
	wire["status"], wire["complete"] = "blocked", false
	wire["catalog"], wire["current"], wire["fields"] = []any{}, []any{}, []any{}
	wire["prepared"] = map[string]any{"bytes": float64(0), "sha256": ""}
	wire["usage"].(map[string]any)["desired_canonical_bytes"] = float64(0)
	wire["usage"].(map[string]any)["current_canonical_bytes"] = float64(0)
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	result, err := DecodeJiraGuardedFieldResult(bytes.NewReader(encoded))
	if err != nil || result.Status != "blocked" || result.Complete || result.ProposalHash != "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestDecodeJiraGuardedFieldResultAcceptsProductValidUTF8FieldIDs(t *testing.T) {
	for _, field := range []string{"plugin.vendor", "供应商.字段"} {
		wire := strings.ReplaceAll(jiraGuardedFieldWireFixture(), "customfield_12000", field)
		if _, err := DecodeJiraGuardedFieldResult(strings.NewReader(wire)); err != nil {
			t.Fatalf("field=%q err=%v", field, err)
		}
	}
	for _, field := range []string{" vendor.field", "vendor.field ", "vendor\x00field", "vendor\nfield", "project"} {
		wire := strings.ReplaceAll(jiraGuardedFieldWireFixture(), "customfield_12000", field)
		if _, err := DecodeJiraGuardedFieldResult(strings.NewReader(wire)); err == nil {
			t.Fatalf("invalid field=%q passed", field)
		}
	}
}

func TestDecodeJiraGuardedFieldResultUsesExactCaseAndReleasedValueDepth(t *testing.T) {
	withValue := func(value, kind string) string {
		wire := jiraGuardedFieldWireFixture()
		wire = strings.Replace(wire, `"kind":"string","bytes":29`, `"kind":"`+kind+`","bytes":29`, 1)
		return strings.Replace(wire, `"value":"Synthetic approved narrative.\n"`, `"value":`+value, 1)
	}
	for _, depth := range []int{129, jiraGuardedFieldValueMaxNestingDepth} {
		value := strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth)
		if _, err := DecodeJiraGuardedFieldResult(strings.NewReader(withValue(value, "array"))); err != nil {
			t.Fatalf("depth=%d err=%v", depth, err)
		}
	}
	if _, err := DecodeJiraGuardedFieldResult(strings.NewReader(withValue(`{"A":1,"a":2}`, "object"))); err != nil {
		t.Fatalf("case-distinct desired members: %v", err)
	}
	for name, value := range map[string]string{
		"over value envelope": strings.Repeat("[", jiraGuardedFieldValueMaxNestingDepth+1) + "0" + strings.Repeat("]", jiraGuardedFieldValueMaxNestingDepth+1),
		"exact duplicate":     `{"A":1,"A":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			kind := "array"
			if value[0] == '{' {
				kind = "object"
			}
			if _, err := DecodeJiraGuardedFieldResult(strings.NewReader(withValue(value, kind))); err == nil {
				t.Fatal("invalid desired value passed")
			}
		})
	}
}

func TestDecodeJiraGuardedFieldResultEnforcesAdvertisedIdentityByteBounds(t *testing.T) {
	project := strings.Repeat("P", 63)
	key := project + "-1"
	keyWire := strings.ReplaceAll(jiraGuardedFieldWireFixture(), "PROJ-1", key)
	keyWire = strings.Replace(keyWire, `"project":"PROJ"`, `"project":"`+project+`"`, 1)
	if _, err := DecodeJiraGuardedFieldResult(strings.NewReader(keyWire)); err == nil {
		t.Fatal("65-byte requested key passed")
	}
	idWire := strings.Replace(jiraGuardedFieldWireFixture(), `"issue_id":"12001"`, `"issue_id":"1`+strings.Repeat("0", 64)+`"`, 1)
	if _, err := DecodeJiraGuardedFieldResult(strings.NewReader(idWire)); err == nil {
		t.Fatal("65-byte immutable id passed")
	}
}

func TestDecodeJiraGuardedFieldResultReconcilesProjectionAggregates(t *testing.T) {
	decode := func(t *testing.T) map[string]any {
		t.Helper()
		var wire map[string]any
		if err := json.Unmarshal([]byte(jiraGuardedFieldWireFixture()), &wire); err != nil {
			t.Fatal(err)
		}
		return wire
	}
	mutations := map[string]func(map[string]any){
		"desired usage contradiction": func(wire map[string]any) { wire["usage"].(map[string]any)["desired_canonical_bytes"] = float64(30) },
		"current usage contradiction": func(wire map[string]any) { wire["usage"].(map[string]any)["current_canonical_bytes"] = float64(4) },
		"current projection oversized": func(wire map[string]any) {
			wire["current"].([]any)[0].(map[string]any)["bytes"] = float64((64 << 20) + 1)
		},
		"readback aggregate oversized": func(wire map[string]any) {
			wire["mode"], wire["status"], wire["write_attempted"], wire["reconciled"] = "apply", "unknown", true, true
			wire["current"].([]any)[0].(map[string]any)["bytes"] = float64(64 << 20)
			wire["readback"] = []any{map[string]any{"field": "customfield_12000", "present": true, "kind": "string", "bytes": float64(1), "sha256": strings.Repeat("c", 64)}}
			wire["usage"].(map[string]any)["current_canonical_bytes"] = float64(64 << 20)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			wire := decode(t)
			mutate(wire)
			encoded, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeJiraGuardedFieldResult(bytes.NewReader(encoded)); err == nil {
				t.Fatal("aggregate contradiction passed")
			}
		})
	}
}

func TestJiraGuardedFieldTruthTable(t *testing.T) {
	valid := []struct {
		mode, status                    string
		attempted, reconciled, complete bool
	}{
		{"dry-run", "would_apply", false, false, true}, {"dry-run", "already_satisfied", false, false, true}, {"dry-run", "blocked", false, false, false},
		{"apply", "already_satisfied", false, false, true}, {"apply", "already_satisfied", true, true, true}, {"apply", "blocked", false, false, false}, {"apply", "blocked", false, false, true},
		{"apply", "applied", true, true, true}, {"apply", "unknown", true, true, true}, {"apply", "unknown", true, false, false}, {"apply", "failed", true, true, true}, {"apply", "failed", true, false, false},
	}
	for _, row := range valid {
		if !validJiraGuardedFieldTruth(row.mode, row.status, row.attempted, row.reconciled, row.complete) {
			t.Fatalf("rejected %+v", row)
		}
	}
	for _, row := range []struct {
		mode, status                    string
		attempted, reconciled, complete bool
	}{{"dry-run", "applied", true, true, true}, {"apply", "would_apply", false, false, true}, {"apply", "unknown", false, false, false}, {"apply", "failed", true, false, true}} {
		if validJiraGuardedFieldTruth(row.mode, row.status, row.attempted, row.reconciled, row.complete) {
			t.Fatalf("accepted %+v", row)
		}
	}
}

func TestDecodeJiraGuardedFieldResultFailsClosed(t *testing.T) {
	valid := jiraGuardedFieldWireFixture()
	tests := map[string]string{
		"duplicate":       strings.Replace(valid, `"schema_version":3`, `"schema_version":3,"schema_version":3`, 1),
		"trailing":        valid + `{}`,
		"unknown":         strings.Replace(valid, `"operation":`, `"extra":true,"operation":`, 1),
		"bad hash":        strings.Replace(valid, strings.Repeat("a", 64), "ABC", 1),
		"truth":           strings.Replace(valid, `"write_attempted":false`, `"write_attempted":true`, 1),
		"duplicate field": strings.Replace(valid, `"fields":[`, `"fields":[{"field":"customfield_12000","source":"raw","kind":"string","bytes":1,"sha256":"`+strings.Repeat("d", 64)+`","value":"x"},`, 1),
	}
	for name, wire := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeJiraGuardedFieldResult(strings.NewReader(wire)); err == nil {
				t.Fatal("decoder accepted invalid guarded-field wire")
			}
		})
	}
	oversized := bytes.Repeat([]byte(" "), jiraGuardedFieldWireMaxBytes+1)
	if _, err := DecodeJiraGuardedFieldResult(bytes.NewReader(oversized)); err == nil {
		t.Fatal("decoder accepted oversized wire")
	}
	invalidUTF8 := append([]byte(jiraGuardedFieldWireFixture()), 0xff)
	if _, err := DecodeJiraGuardedFieldResult(bytes.NewReader(invalidUTF8)); err == nil {
		t.Fatal("decoder accepted invalid UTF-8")
	}
}

func jiraGuardedFieldWireFixture() string {
	wire := strings.Replace(jiraGuardedFieldWireFixtureLegacy(), `"max_field_id_bytes":64`, `"max_field_id_bytes":1024`, 1)
	wire = strings.Replace(wire, `"max_immutable_id_bytes":64,`, `"max_immutable_id_bytes":64,"max_json_nesting_depth":10000,"max_value_nesting_depth":9997,`, 1)
	return strings.Replace(wire, `"backend_sha256":"`+strings.Repeat("b", 64)+`"`, `"backend_sha256":"sha256:`+strings.Repeat("b", 64)+`"`, 1)
}

func jiraGuardedFieldWireFixtureLegacy() string {
	hash := func(ch byte) string { return strings.Repeat(string(ch), 64) }
	return fmt.Sprintf(`{"schema_version":3,"operation":"jira_issue_field_set","backend_sha256":%q,"requested_key":"PROJ-1","issue_id":"12001","key":"PROJ-1","project":"PROJ","mode":"dry-run","status":"would_apply","expected_updated":"2026-07-15T09:30:00.000+0000","actual_updated":"2026-07-15T09:30:00.000+0000","proposal_hash":%q,"catalog":[{"id":"customfield_12000","custom":true}],"current":[{"field":"customfield_12000","present":true,"kind":"string","bytes":3,"sha256":%q}],"prepared":{"bytes":53,"sha256":%q},"bounds":{"max_catalog_entries":4096,"max_selected_fields":1024,"max_allowlist_entries":1024,"max_field_id_bytes":64,"max_requested_key_bytes":64,"max_immutable_id_bytes":64,"max_catalog_response_bytes":16777216,"max_issue_response_bytes":67108864,"max_input_bytes":67108864,"max_desired_canonical_bytes":67108864,"max_current_canonical_bytes":67108864,"max_prepared_bytes":67108864,"max_query_and_path_bytes":65536,"max_write_response_bytes":1048576,"preview_max_requests":2,"apply_max_requests":6,"preview_max_aggregate_response_bytes":83886080,"apply_max_aggregate_response_bytes":235929600,"deadline_millis":60000},"usage":{"requests":2,"response_bytes":100,"input_bytes":29,"desired_canonical_bytes":29,"current_canonical_bytes":3},"write_attempted":false,"reconciled":false,"complete":true,"fields":[{"field":"customfield_12000","source":"raw","kind":"string","bytes":29,"sha256":%q,"value":"Synthetic approved narrative.\n"}]}`, hash('b'), hash('a'), hash('c'), hash('d'), hash('e'))
}
