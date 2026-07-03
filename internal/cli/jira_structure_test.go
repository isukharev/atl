package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestJiraStructureGetCLI(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/structure/2.0/structure/123", http.StatusOK, `{"id":123,"name":"Release plan","readOnly":true}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "get", "123")
	if code != exitOK {
		t.Fatalf("structure get: exit %d, want 0 (stdout=%q)", code, out)
	}
	var got struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		ReadOnly bool   `json:"read_only"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if got.ID != 123 || got.Name != "Release plan" || !got.ReadOnly {
		t.Fatalf("output = %+v, want structure metadata", got)
	}
	if !strings.Contains(js.requests()[0].query, "withPermissions=true") {
		t.Errorf("query = %q, want withPermissions=true", js.requests()[0].query)
	}
}

func TestJiraStructureRowsCLI(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/structure/2.0/forest/latest", http.StatusOK, `{
		"formula":"100:0:10001,101:1:10002",
		"version":{"signature":55,"version":7}
	}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "rows", "123")
	if code != exitOK {
		t.Fatalf("structure rows: exit %d, want 0 (stdout=%q)", code, out)
	}
	var got struct {
		StructureID int64 `json:"structure_id"`
		Rows        []struct {
			RowID       int64  `json:"row_id"`
			ParentRowID int64  `json:"parent_row_id"`
			ItemType    string `json:"item_type"`
			ItemID      string `json:"item_id"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if got.StructureID != 123 || len(got.Rows) != 2 || got.Rows[1].ParentRowID != 100 || got.Rows[1].ItemType != "issue" {
		t.Fatalf("rows output = %+v, want parsed issue hierarchy", got)
	}

	idOut, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "rows", "123", "-o", "id")
	if code != exitOK {
		t.Fatalf("structure rows -o id: exit %d, want 0 (stdout=%q)", code, idOut)
	}
	if idOut != "100\n101\n" {
		t.Fatalf("id output = %q, want row ids", idOut)
	}
}

func TestJiraStructureValuesCLI(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodPost, "/rest/structure/2.0/value", http.StatusOK, `{"responses":[{"rows":[100],"data":[]}],"inaccessibleRows":[]}`)

	out, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "values", "123", "--rows", "100", "--fields", "key,summary")
	if code != exitOK {
		t.Fatalf("structure values: exit %d, want 0 (stdout=%q)", code, out)
	}
	if !strings.Contains(out, `"responses"`) {
		t.Fatalf("values output = %q, want responses", out)
	}
	reqs := js.writeReqsTo("/rest/structure/2.0/value")
	if len(reqs) != 1 {
		t.Fatalf("writes = %d, want one value POST", len(reqs))
	}
	var payload struct {
		Requests []struct {
			Rows       []int64 `json:"rows"`
			Attributes []struct {
				ID string `json:"id"`
			} `json:"attributes"`
		} `json:"requests"`
	}
	if err := json.Unmarshal([]byte(reqs[0].body), &payload); err != nil {
		t.Fatalf("decode value request: %v", err)
	}
	if len(payload.Requests) != 1 || len(payload.Requests[0].Rows) != 1 || payload.Requests[0].Rows[0] != 100 {
		t.Fatalf("rows payload = %+v, want row 100", payload)
	}
	if len(payload.Requests[0].Attributes) != 2 || payload.Requests[0].Attributes[0].ID != "key" || payload.Requests[0].Attributes[1].ID != "summary" {
		t.Fatalf("attributes payload = %+v, want key,summary", payload.Requests[0].Attributes)
	}
}

func TestJiraStructureRejectsBadIDsBeforeNetwork(t *testing.T) {
	js := newJiraServer(t)

	_, code := runCLI(t, jiraEnv(js.srv), "jira", "structure", "get", "nope")
	if code != exitUsage {
		t.Fatalf("bad structure id exit = %d, want %d", code, exitUsage)
	}
	if len(js.requests()) != 0 {
		t.Fatalf("sent %d requests, want none", len(js.requests()))
	}
}
