package agenteval

import (
	"net/http"
	"strings"
	"testing"
)

func TestMockBackendRecordsMethodsWithoutExposingPaths(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{Method: "GET", Path: "/jira/rest/api/2/field", Status: 200, Body: []byte(`[]`)}},
	}
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	response, err := http.Get(backend.Environment()["ATL_JIRA_URL"] + "/rest/api/2/field")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	response, err = http.Get(backend.Environment()["ATL_JIRA_URL"] + "/rest/api/2/field")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	response, err = http.Post(backend.Environment()["ATL_CONFLUENCE_URL"]+"/rest/api/content", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	methods, unexpected, duplicates := backend.Summary()
	if methods["GET"] != 2 || methods["POST"] != 1 || unexpected != 1 || duplicates != 1 {
		t.Fatalf("methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
	}
}
