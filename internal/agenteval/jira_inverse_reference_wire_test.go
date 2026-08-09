package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDecodeJiraInverseReferenceViewAcceptsCompleteAndFastReleasedShapes(t *testing.T) {
	for _, fixture := range []string{
		validJiraInverseReferencePrimary(), validJiraInverseReferenceHoldout(),
		validJiraInverseReferencePartialSource(), validJiraInverseReferenceDescriptionMatch(),
		validJiraInverseReferenceRemoteLiteralMatch(),
	} {
		view, err := DecodeJiraInverseReferenceView(strings.NewReader(fixture))
		if err != nil {
			t.Fatal(err)
		}
		if view.SchemaVersion != 1 || !view.Reconciliation.Counts || !view.Usage.Reconciled {
			t.Fatalf("decoded inverse-reference view drifted: %+v", view)
		}
	}
}

func TestDecodeJiraInverseReferenceViewRejectsWireAndPrivacyDrift(t *testing.T) {
	valid := validJiraInverseReferencePrimary()
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unknown root URL", mutate: func(doc map[string]any) { doc["url"] = "https://leak.example.test" }},
		{name: "input JQL", mutate: func(doc map[string]any) { doc["jql"] = "project = PRIVATE" }},
		{name: "target host", mutate: func(doc map[string]any) { doc["target"].(map[string]any)["host"] = "private.example.test" }},
		{name: "target path", mutate: func(doc map[string]any) { doc["target"].(map[string]any)["path"] = "group/private" }},
		{name: "match snippet", mutate: func(doc map[string]any) { doc["matches"].([]any)[0].(map[string]any)["snippet"] = "private text" }},
		{name: "match body", mutate: func(doc map[string]any) { doc["matches"].([]any)[0].(map[string]any)["body"] = "private text" }},
		{name: "match title", mutate: func(doc map[string]any) { doc["matches"].([]any)[0].(map[string]any)["title"] = "private title" }},
		{name: "match username", mutate: func(doc map[string]any) { doc["matches"].([]any)[0].(map[string]any)["username"] = "private-user" }},
		{name: "application name", mutate: func(doc map[string]any) {
			doc["matches"].([]any)[0].(map[string]any)["application_name"] = "GitLab Private"
		}},
		{name: "property key", mutate: func(doc map[string]any) { doc["matches"].([]any)[0].(map[string]any)["property_key"] = "private.key" }},
		{name: "closed source", mutate: func(doc map[string]any) { doc["sources"] = []any{"history"} }},
		{name: "closed reason", mutate: func(doc map[string]any) {
			doc["selection"].(map[string]any)["complete"] = false
			doc["selection"].(map[string]any)["reason"] = "backend prose"
		}},
		{name: "closed relation", mutate: func(doc map[string]any) { doc["matches"].([]any)[0].(map[string]any)["relation"] = "mentioned_by_user" }},
		{name: "relation tuple", mutate: func(doc map[string]any) {
			doc["matches"].([]any)[0].(map[string]any)["relation"] = "structured_remote_link"
		}},
		{name: "match completeness", mutate: func(doc map[string]any) {
			doc["matches"].([]any)[0].(map[string]any)["complete"] = false
		}},
		{name: "complete frontier", mutate: func(doc map[string]any) {
			doc["frontier"].(map[string]any)["phase"] = "verification"
		}},
		{name: "complete exhaustive candidate geometry", mutate: func(doc map[string]any) {
			doc["counts"].(map[string]any)["candidate_issues"] = float64(2)
		}},
		{name: "complete exhaustive scan geometry", mutate: func(doc map[string]any) {
			doc["counts"].(map[string]any)["scanned_issues"] = float64(3)
		}},
		{name: "verification aggregate", mutate: func(doc map[string]any) {
			doc["verification"] = map[string]any{"complete": false, "reason": "source_incomplete"}
			doc["frontier"] = map[string]any{"phase": "verification", "verified_issues": float64(1)}
			doc["complete"] = false
		}},
		{name: "released issue bound", mutate: func(doc map[string]any) {
			doc["usage"].(map[string]any)["max_issues"] = float64(5001)
		}},
		{name: "source reason narrative", mutate: func(doc map[string]any) {
			doc["source_counts"].([]any)[0].(map[string]any)["reasons"] = []any{map[string]any{"reason": "backend prose", "count": float64(1)}}
		}},
		{name: "unreconciled source total", mutate: func(doc map[string]any) { doc["source_counts"].([]any)[0].(map[string]any)["total"] = float64(2) }},
		{name: "false absence", mutate: func(doc map[string]any) { doc["absence_proven"] = true }},
	}
	fieldIDMismatch := mutateJiraInverseReferenceWire(t, validJiraInverseReferenceDescriptionMatch(), func(doc map[string]any) {
		doc["sources"] = []any{"fields"}
		doc["effective_field_ids"] = []any{"customfield_10001"}
		doc["source_counts"].([]any)[0].(map[string]any)["source"] = "fields"
		match := doc["matches"].([]any)[0].(map[string]any)
		match["source"] = "fields"
		match["technical_field_id"] = "customfield_10002"
	})
	if _, err := DecodeJiraInverseReferenceView(bytes.NewReader(fieldIDMismatch)); err == nil {
		t.Fatal("inverse-reference decoder admitted an unselected technical field id")
	}
	fastComplete := mutateJiraInverseReferenceWire(t, validJiraInverseReferenceHoldout(), func(doc map[string]any) {
		doc["selection"] = map[string]any{"complete": true}
	})
	if _, err := DecodeJiraInverseReferenceView(bytes.NewReader(fastComplete)); err == nil {
		t.Fatal("inverse-reference decoder admitted a complete fast selection")
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := mutateJiraInverseReferenceWire(t, valid, test.mutate)
			if _, err := DecodeJiraInverseReferenceView(bytes.NewReader(mutated)); err == nil {
				t.Fatalf("inverse-reference decoder admitted %s", test.name)
			}
		})
	}
}

func TestDecodeJiraInverseReferenceViewRejectsDuplicateNullMissingAndOversize(t *testing.T) {
	valid := validJiraInverseReferencePrimary()
	duplicate := strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1)
	missing := strings.Replace(valid, `,"absence_proven":false}`, `}`, 1)
	nullValue := strings.Replace(valid, `"effective_field_ids":[]`, `"effective_field_ids":null`, 1)
	for name, data := range map[string]string{"duplicate": duplicate, "missing": missing, "null": nullValue} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeJiraInverseReferenceView(strings.NewReader(data)); err == nil {
				t.Fatalf("inverse-reference decoder admitted %s drift", name)
			}
		})
	}
	if _, err := DecodeJiraInverseReferenceView(strings.NewReader(valid + strings.Repeat(" ", maxContractBytes))); err == nil {
		t.Fatal("inverse-reference decoder admitted oversized wire")
	}

	fields := make([]any, jiraInverseReferenceMaxFields)
	for index := range fields {
		fields[index] = fmt.Sprintf("customfield_%03d", index)
	}
	makeWire := func(selected []any) []byte {
		return mutateJiraInverseReferenceWire(t, validJiraInverseReferenceDescriptionMatch(), func(doc map[string]any) {
			doc["sources"] = []any{"fields"}
			doc["effective_field_ids"] = selected
			doc["source_counts"].([]any)[0].(map[string]any)["source"] = "fields"
			match := doc["matches"].([]any)[0].(map[string]any)
			match["source"] = "fields"
			match["technical_field_id"] = selected[0]
		})
	}
	if _, err := DecodeJiraInverseReferenceView(bytes.NewReader(makeWire(fields))); err != nil {
		t.Fatalf("decoder rejected %d released fields: %v", len(fields), err)
	}
	fields = append(fields, "customfield_128")
	if _, err := DecodeJiraInverseReferenceView(bytes.NewReader(makeWire(fields))); err == nil {
		t.Fatalf("decoder admitted %d fields above the released bound", len(fields))
	}
}

func mutateJiraInverseReferenceWire(t *testing.T, data string, mutate func(map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(data), &doc); err != nil {
		t.Fatal(err)
	}
	mutate(doc)
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func validJiraInverseReferencePrimary() string {
	return `{"schema_version":1,"target":{"kind":"gitlab_project","opaque_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"mode":"exhaustive","sources":["development"],"effective_field_ids":[],"target_resolution":{"complete":true},"selection":{"complete":true},"verification":{"complete":true},"counts":{"selected_issues":1,"candidate_issues":1,"scanned_issues":2,"verified_issues":1,"matched_issues":1,"matches":1},"source_counts":[{"source":"development","complete":1,"empty":0,"partial":0,"forbidden":0,"unsupported":0,"skipped":0,"total":1,"reconciled":true,"reasons":[]}],"matches":[{"issue_key":"IR-41","relation":"development_association","direction":"issue_to_target","source":"development","stability":"experimental_api","confidence":"exact","complete":true}],"frontier":{"phase":"complete","verified_issues":1},"reconciliation":{"counts":true,"sources":true,"matches":true,"usage":true},"usage":{"max_issues":10,"max_requests":10,"requests":4,"max_response_bytes":65536,"response_bytes":1024,"reconciled":true},"complete":true,"absence_proven":false}`
}

func validJiraInverseReferenceHoldout() string {
	return `{"schema_version":1,"target":{"kind":"confluence_page","opaque_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"mode":"fast","sources":["description"],"effective_field_ids":[],"target_resolution":{"complete":true},"selection":{"complete":false,"reason":"mode_fast"},"verification":{"complete":true},"counts":{"selected_issues":1,"candidate_issues":1,"scanned_issues":1,"verified_issues":1,"matched_issues":0,"matches":0},"source_counts":[{"source":"description","complete":1,"empty":0,"partial":0,"forbidden":0,"unsupported":0,"skipped":0,"total":1,"reconciled":true,"reasons":[]}],"matches":[],"frontier":{"phase":"selection","pass":1,"verified_issues":0},"reconciliation":{"counts":true,"sources":true,"matches":true,"usage":true},"usage":{"max_issues":5,"max_requests":5,"requests":2,"max_response_bytes":32768,"response_bytes":512,"reconciled":true},"complete":false,"absence_proven":false}`
}

func validJiraInverseReferencePartialSource() string {
	return `{"schema_version":1,"target":{"kind":"gitlab_project","opaque_id":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"mode":"exhaustive","sources":["description"],"effective_field_ids":[],"target_resolution":{"complete":true},"selection":{"complete":true},"verification":{"complete":false,"reason":"source_incomplete"},"counts":{"selected_issues":1,"candidate_issues":1,"scanned_issues":2,"verified_issues":1,"matched_issues":0,"matches":0},"source_counts":[{"source":"description","complete":0,"empty":0,"partial":1,"forbidden":0,"unsupported":0,"skipped":0,"total":1,"reconciled":true,"reasons":[{"reason":"field_missing","count":1}]}],"matches":[],"frontier":{"phase":"verification","verified_issues":0,"source":"description","source_reason":"field_missing"},"reconciliation":{"counts":true,"sources":true,"matches":true,"usage":true},"usage":{"max_issues":5,"max_requests":5,"requests":3,"max_response_bytes":32768,"response_bytes":512,"reconciled":true},"complete":false,"absence_proven":false}`
}

func validJiraInverseReferenceDescriptionMatch() string {
	return `{"schema_version":1,"target":{"kind":"confluence_page","opaque_id":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},"mode":"exhaustive","sources":["description"],"effective_field_ids":[],"target_resolution":{"complete":true},"selection":{"complete":true},"verification":{"complete":true},"counts":{"selected_issues":1,"candidate_issues":1,"scanned_issues":2,"verified_issues":1,"matched_issues":1,"matches":1},"source_counts":[{"source":"description","complete":1,"empty":0,"partial":0,"forbidden":0,"unsupported":0,"skipped":0,"total":1,"reconciled":true,"reasons":[]}],"matches":[{"issue_key":"IR-42","relation":"literal_mention","direction":"issue_to_target","source":"description","technical_field_id":"description","stability":"heuristic","confidence":"high","complete":true}],"frontier":{"phase":"complete","verified_issues":1},"reconciliation":{"counts":true,"sources":true,"matches":true,"usage":true},"usage":{"max_issues":5,"max_requests":5,"requests":3,"max_response_bytes":32768,"response_bytes":512,"reconciled":true},"complete":true,"absence_proven":false}`
}

func validJiraInverseReferenceRemoteLiteralMatch() string {
	return `{"schema_version":1,"target":{"kind":"gitlab_project","opaque_id":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},"mode":"exhaustive","sources":["remote_links"],"effective_field_ids":[],"target_resolution":{"complete":true},"selection":{"complete":true},"verification":{"complete":true},"counts":{"selected_issues":1,"candidate_issues":1,"scanned_issues":2,"verified_issues":1,"matched_issues":1,"matches":1},"source_counts":[{"source":"remote_links","complete":1,"empty":0,"partial":0,"forbidden":0,"unsupported":0,"skipped":0,"total":1,"reconciled":true,"reasons":[]}],"matches":[{"issue_key":"IR-43","relation":"literal_mention","direction":"issue_to_target","source":"remote_links","stability":"heuristic","confidence":"high","complete":true}],"frontier":{"phase":"complete","verified_issues":1},"reconciliation":{"counts":true,"sources":true,"matches":true,"usage":true},"usage":{"max_issues":5,"max_requests":5,"requests":3,"max_response_bytes":32768,"response_bytes":512,"reconciled":true},"complete":true,"absence_proven":false}`
}
