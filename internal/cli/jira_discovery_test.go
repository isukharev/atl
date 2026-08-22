package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestJiraProjectListOutputsBoundedJSONTextAndIDs(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/project", http.StatusOK, `[{"id":"2","key":"ZED","name":"Zed"},{"id":"1","key":"OPS","name":"Operations","projectTypeKey":"business"}]`)
	out, code := runCLI(t, jiraEnv(js.srv), "jira", "project", "list", "--limit", "1", "--include-archived")
	if code != exitOK {
		t.Fatalf("exit=%d output=%q", code, out)
	}
	var result appProjectListWire
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.Count != 1 || result.Total != 2 || result.Complete || !result.Truncated || result.Projects[0].Key != "OPS" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	requests := js.requests()
	if len(requests) != 1 || requests[0].query != "includeArchived=true" {
		t.Fatalf("requests=%+v", requests)
	}
	ids, code := runCLI(t, jiraEnv(js.srv), "jira", "project", "list", "-o", "id")
	if code != exitOK || ids != "OPS\nZED\n" {
		t.Fatalf("ids exit=%d output=%q", code, ids)
	}
	text, code := runCLI(t, jiraEnv(js.srv), "jira", "project", "list", "-o", "text")
	if code != exitOK || !strings.Contains(text, "| Key | Name | Type | Archived |") {
		t.Fatalf("text exit=%d output=%q", code, text)
	}
}

type appProjectListWire struct {
	Count     int  `json:"count"`
	Total     int  `json:"total"`
	Complete  bool `json:"complete"`
	Truncated bool `json:"truncated"`
	Projects  []struct {
		Key string `json:"key"`
	} `json:"projects"`
}

func TestJiraCreateMetadataCommandsAreContentFree(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/createmeta/OPS/issuetypes/10", http.StatusOK,
		`{"startAt":0,"total":2,"isLast":true,"values":[{"fieldId":"summary","name":"Summary","required":true,"hasDefaultValue":false,"schema":{"type":"string","system":"summary"}},{"fieldId":"priority","name":"Priority","required":false,"hasDefaultValue":true,"defaultValue":{"secret":"private default"},"schema":{"type":"array","items":"option"},"allowedValues":[{"name":"Private","value":"private-id"}],"autoCompleteUrl":"https://private.example.test/options"}]}`)
	js.route(http.MethodGet, "/rest/api/2/issue/createmeta/OPS/issuetypes", http.StatusOK,
		`{"startAt":0,"total":1,"isLast":true,"values":[{"id":"10","name":"Task","subtask":false}]}`)

	ids, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "types", "--project", "OPS", "-o", "id")
	if code != exitOK || ids != "10\n" {
		t.Fatalf("types exit=%d output=%q", code, ids)
	}
	out, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "create-check", "--project", "OPS", "--type", "Task")
	if code != exitOK || strings.Contains(out, "Private") || strings.Contains(out, "private-id") || strings.Contains(out, `"on_screen"`) || !strings.Contains(out, `"has_allowed_values": true`) {
		t.Fatalf("create-check exit=%d output=%q", code, out)
	}
	wantLegacy := "{\n  \"schema_version\": 1,\n  \"project\": \"OPS\",\n  \"issue_type\": {\n    \"id\": \"10\",\n    \"name\": \"Task\",\n    \"subtask\": false\n  },\n  \"count\": 2,\n  \"complete\": true,\n  \"fields\": [\n    {\n      \"field_id\": \"priority\",\n      \"name\": \"Priority\",\n      \"required\": false,\n      \"has_allowed_values\": true\n    },\n    {\n      \"field_id\": \"summary\",\n      \"name\": \"Summary\",\n      \"required\": true,\n      \"has_allowed_values\": false\n    }\n  ]\n}\n"
	if out != wantLegacy {
		t.Fatalf("legacy create-check bytes changed\n got: %s\nwant: %s", out, wantLegacy)
	}

	qualified, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "create-metadata", "--project", "OPS", "--type", "Task")
	if code != exitOK || !strings.Contains(qualified, `"omittability": "must_supply"`) ||
		!strings.Contains(qualified, `"mode": "inline_and_autocomplete"`) || !strings.Contains(qualified, `"requests_used": 2`) {
		t.Fatalf("create-metadata exit=%d output=%q", code, qualified)
	}
	for _, private := range []string{"private default", "Private", "private-id", "private.example.test"} {
		if strings.Contains(qualified, private) {
			t.Fatalf("create-metadata leaked %q: %s", private, qualified)
		}
	}
	text, code := runCLI(t, jiraEnv(js.srv), "jira", "issue", "create-metadata", "--project", "OPS", "--type", "Task", "-o", "text")
	if code != exitOK || !strings.Contains(text, "| Field ID | Name | Required | Schema | Default | Allowed values | Omittability |") || strings.Contains(text, "Private") {
		t.Fatalf("create-metadata text exit=%d output=%q", code, text)
	}
}

func TestJiraDiscoveryValidatesArgumentsBeforeBackendSetup(t *testing.T) {
	if _, code := runCLI(t, nil, "jira", "project", "list", "--limit", "0"); code != exitUsage {
		t.Fatalf("project limit exit=%d", code)
	}
	if _, code := runCLI(t, nil, "jira", "issue", "types"); code != exitUsage {
		t.Fatalf("types missing project exit=%d", code)
	}
	if _, code := runCLI(t, nil, "jira", "issue", "create-check", "--project", "OPS"); code != exitUsage {
		t.Fatalf("create-check missing type exit=%d", code)
	}
	if _, code := runCLI(t, nil, "jira", "issue", "create-metadata", "--project", "OPS"); code != exitUsage {
		t.Fatalf("create-metadata missing type exit=%d", code)
	}
}
