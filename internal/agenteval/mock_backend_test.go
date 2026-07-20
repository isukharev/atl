package agenteval

import (
	"bytes"
	"encoding/json"
	"io"
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

func TestMockBackendConsumesBoundedResponseSequence(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{
			Method: "GET", Path: "/wiki/rest/api/content/7001",
			Responses: []MockResponse{
				{Status: http.StatusOK, Body: []byte(`{"version":{"number":3}}`)},
				{Status: http.StatusOK, Body: []byte(`{"version":{"number":4}}`)},
			},
		}},
	}
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	for index, want := range []string{`{"version":{"number":3}}`, `{"version":{"number":4}}`} {
		response, err := http.Get(backend.Environment()["ATL_CONFLUENCE_URL"] + "/rest/api/content/7001")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || string(body) != want {
			t.Fatalf("response %d status=%d body=%s", index, response.StatusCode, body)
		}
	}
	response, err := http.Get(backend.Environment()["ATL_CONFLUENCE_URL"] + "/rest/api/content/7001")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	methods, unexpected, duplicates := backend.Summary()
	if response.StatusCode != http.StatusNotFound || methods["GET"] != 3 || unexpected != 1 || duplicates != 2 {
		t.Fatalf("status=%d methods=%v unexpected=%d duplicates=%d", response.StatusCode, methods, unexpected, duplicates)
	}
}

func TestMockBackendDoesNotConsumeSequenceOnBodyMismatch(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{
			Method: "PUT", Path: "/wiki/rest/api/content/7001", RequestBody: []byte(`{"value":"approved"}`),
			Responses: []MockResponse{{Status: http.StatusOK, Body: []byte(`{"version":{"number":4}}`)}},
		}},
	}
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	for _, body := range []string{`{"value":"wrong"}`, `{"value":"approved"}`} {
		request, _ := http.NewRequest(http.MethodPut, backend.Environment()["ATL_CONFLUENCE_URL"]+"/rest/api/content/7001", bytes.NewBufferString(body))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if body == `{"value":"wrong"}` && response.StatusCode != http.StatusNotFound {
			t.Fatalf("wrong body status=%d", response.StatusCode)
		}
		if body == `{"value":"approved"}` && response.StatusCode != http.StatusOK {
			t.Fatalf("approved body status=%d", response.StatusCode)
		}
	}
}

func TestMockFixtureRejectsInvalidResponseSequenceShapes(t *testing.T) {
	valid := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{Method: "GET", Path: "/wiki/rest/api/content/7001", Responses: []MockResponse{{Status: http.StatusOK, Body: []byte(`{}`)}}}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMockFixture(bytes.NewReader(encoded))
	if err != nil || len(decoded.Routes[0].Responses) != 1 {
		t.Fatalf("round trip responses=%v err=%v encoded=%s", decoded.Routes, err, encoded)
	}
	for name, mutate := range map[string]func(*MockRoute){
		"neither": func(route *MockRoute) { route.Responses = nil },
		"both": func(route *MockRoute) {
			route.Status, route.Body = http.StatusOK, []byte(`{}`)
		},
		"bad sequence status": func(route *MockRoute) { route.Responses[0].Status = 0 },
		"bad sequence body":   func(route *MockRoute) { route.Responses[0].Body = []byte(`no`) },
	} {
		t.Run(name, func(t *testing.T) {
			fixture := valid
			fixture.Routes = append([]MockRoute(nil), valid.Routes...)
			fixture.Routes[0].Responses = append([]MockResponse(nil), valid.Routes[0].Responses...)
			mutate(&fixture.Routes[0])
			if err := fixture.Validate(); err == nil {
				t.Fatal("invalid response shape passed")
			}
		})
	}
}

func TestMockBackendQueryConstraintRejectsSemanticallyWrongSearch(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{
			Method: "GET", Path: "/jira/rest/api/2/search", QueryContains: map[string]string{"jql": "Orchid retry worker"},
			Status: 200, Body: []byte(`{"issues":[],"total":0}`),
		}},
	}
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	response, err := http.Get(backend.Environment()["ATL_JIRA_URL"] + "/rest/api/2/search?jql=unrelated")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", response.StatusCode)
	}
	methods, unexpected, _ := backend.Summary()
	if methods["GET"] != 1 || unexpected != 1 {
		t.Fatalf("methods=%v unexpected=%d", methods, unexpected)
	}
}

func TestMockBackendSelectsExactPaginatedQueryRoute(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{
			{Method: "GET", Path: "/wiki/rest/api/search", QueryContains: map[string]string{"cql": "Quartz rollout"},
				QueryEquals: map[string]string{"start": "0"}, Status: 200, Body: []byte(`{"page":1}`)},
			{Method: "GET", Path: "/wiki/rest/api/search", QueryContains: map[string]string{"cql": "Quartz rollout"},
				QueryEquals: map[string]string{"start": "2"}, Status: 200, Body: []byte(`{"page":2}`)},
		},
	}
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	for _, test := range []struct {
		start, body string
		status      int
	}{{"0", `{"page":1}`, 200}, {"2", `{"page":2}`, 200}, {"1", `{"errorMessages":["synthetic route not configured"]}`, 404}} {
		response, err := http.Get(backend.Environment()["ATL_CONFLUENCE_URL"] + "/rest/api/search?cql=Quartz+rollout&start=" + test.start)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != test.status || string(body) != test.body {
			t.Fatalf("start=%s status=%d body=%s", test.start, response.StatusCode, body)
		}
	}
}

func TestMockFixtureRejectsDuplicateOrAmbiguousQuerySelectors(t *testing.T) {
	route := MockRoute{Method: "GET", Path: "/wiki/rest/api/search", QueryEquals: map[string]string{"start": "0"}, Status: 200, Body: []byte(`{}`)}
	fixture := MockFixture{SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki", Routes: []MockRoute{route, route}}
	if err := fixture.Validate(); err == nil {
		t.Fatal("duplicate exact query selector passed")
	}
	fixture.Routes = []MockRoute{{Method: "GET", Path: "/wiki/rest/api/search", QueryContains: map[string]string{"start": "0"},
		QueryEquals: map[string]string{"start": "0"}, Status: 200, Body: []byte(`{}`)}}
	if err := fixture.Validate(); err == nil {
		t.Fatal("same query key in contains and equals passed")
	}
	for _, routes := range [][]MockRoute{
		{
			{Method: "GET", Path: "/wiki/rest/api/search", Status: 200, Body: []byte(`{}`)},
			{Method: "GET", Path: "/wiki/rest/api/search", QueryEquals: map[string]string{"start": "0"}, Status: 200, Body: []byte(`{}`)},
		},
		{
			{Method: "GET", Path: "/wiki/rest/api/search", QueryEquals: map[string]string{"start": "0"}, Status: 200, Body: []byte(`{}`)},
			{Method: "GET", Path: "/wiki/rest/api/search", Status: 200, Body: []byte(`{}`)},
		},
	} {
		fixture.Routes = routes
		if err := fixture.Validate(); err == nil {
			t.Fatal("mixed constrained and unconstrained duplicate routes passed")
		}
	}
}

func TestMockBackendExactQueryRejectsMultipleValues(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{Method: "GET", Path: "/wiki/rest/api/search", QueryEquals: map[string]string{"start": "0"}, Status: 200, Body: []byte(`{}`)}},
	}
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	response, err := http.Get(backend.Environment()["ATL_CONFLUENCE_URL"] + "/rest/api/search?start=0&start=2")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want %d", response.StatusCode, http.StatusNotFound)
	}
}

func TestMockBackendSelectsExactRequestBodyRoute(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{
			{Method: "POST", Path: "/jira/rest/structure/2.0/value", RequestBody: []byte(`{"kind":"labels"}`), Status: 200, Body: []byte(`{"response":"labels"}`)},
			{Method: "POST", Path: "/jira/rest/structure/2.0/value", RequestBody: []byte(`{"kind":"values"}`), Status: 200, Body: []byte(`{"response":"values"}`)},
		},
	}
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	for _, test := range []struct{ request, response string }{{`{"kind":"labels"}`, `{"response":"labels"}`}, {`{"kind":"values"}`, `{"response":"values"}`}} {
		response, err := http.Post(backend.Environment()["ATL_JIRA_URL"]+"/rest/structure/2.0/value", "application/json", strings.NewReader(test.request))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || string(body) != test.response {
			t.Fatalf("request=%s status=%d body=%s", test.request, response.StatusCode, body)
		}
	}
	methods, unexpected, duplicates := backend.Summary()
	if methods["POST"] != 2 || unexpected != 0 || duplicates != 1 {
		t.Fatalf("methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
	}
}

func TestMockFixtureRejectsDuplicateSemanticRequestBody(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{
			{Method: "POST", Path: "/jira/rest/structure/2.0/value", RequestBody: []byte(`{"a":1,"b":2}`), Status: 200, Body: []byte(`{}`)},
			{Method: "POST", Path: "/jira/rest/structure/2.0/value", RequestBody: []byte(`{"b":2,"a":1}`), Status: 200, Body: []byte(`{}`)},
		},
	}
	if err := fixture.Validate(); err == nil {
		t.Fatal("duplicate semantic request body passed")
	}
	constrained := MockRoute{Method: "POST", Path: "/jira/rest/structure/2.0/value", RequestBody: []byte(`{"kind":"values"}`), Status: 200, Body: []byte(`{}`)}
	unconstrained := MockRoute{Method: "POST", Path: "/jira/rest/structure/2.0/value", Status: 200, Body: []byte(`{}`)}
	for _, routes := range [][]MockRoute{{constrained, unconstrained}, {unconstrained, constrained}} {
		fixture.Routes = routes
		if err := fixture.Validate(); err == nil {
			t.Fatal("mixed constrained and unconstrained request-body routes passed")
		}
	}
}

func TestMockBackendMatchesExpectedJSONRequestBody(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{
			Method: "PUT", Path: "/jira/rest/api/2/issue/PROJ-1", RequestBody: []byte(`{"fields":{"customfield_1":"approved"}}`),
			Status: http.StatusNoContent, Body: []byte(`{}`),
		}},
	}
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	request, _ := http.NewRequest(http.MethodPut, backend.Environment()["ATL_JIRA_URL"]+"/rest/api/2/issue/PROJ-1", bytes.NewBufferString(`{"fields":{"customfield_1":"wrong"}}`))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong body status=%d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodPut, backend.Environment()["ATL_JIRA_URL"]+"/rest/api/2/issue/PROJ-1", bytes.NewBufferString(`{ "fields": { "customfield_1": "approved" } }`))
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	methods, unexpected, duplicates := backend.Summary()
	if response.StatusCode != http.StatusNoContent || methods["PUT"] != 2 || unexpected != 1 || duplicates != 1 {
		t.Fatalf("status=%d methods=%v unexpected=%d duplicates=%d", response.StatusCode, methods, unexpected, duplicates)
	}
}
