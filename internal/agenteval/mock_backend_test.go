package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDecodeMockFixtureRejectsUnboundedOrAmbiguousContracts(t *testing.T) {
	valid := `{"schema_version":1,"jira_context":"/jira","confluence_context":"/wiki","routes":[{"method":"GET","path":"/jira/rest/api/2/field","status":200,"body":[]}]}`
	tests := map[string]string{
		"unknown field":   strings.TrimSuffix(valid, "}") + `,"unknown":true}`,
		"multiple values": valid + valid,
		"oversized":       fmt.Sprintf(`{"schema_version":1,"jira_context":"/jira","confluence_context":"/wiki","routes":[{"method":"GET","path":"/jira/rest/api/2/field","status":200,"body":%q}]}`, strings.Repeat("a", maxContractBytes)),
	}
	for name, contract := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeMockFixture(strings.NewReader(contract)); err == nil {
				t.Fatal("invalid mock fixture contract passed")
			}
		})
	}
}

func TestMockBackendEnvironmentIsExactAndIndependent(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{Method: "GET", Path: "/jira/rest/api/2/field", Status: 200, Body: []byte(`[]`)}},
	}
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)
	want := map[string]string{
		"ATL_JIRA_URL":       backend.HTTPServer().URL + "/jira",
		"ATL_CONFLUENCE_URL": backend.HTTPServer().URL + "/wiki",
		"ATL_JIRA_PAT":       "synthetic-jira-token",
		"ATL_CONFLUENCE_PAT": "synthetic-confluence-token",
		"ATL_ALLOW_INSECURE": "1",
	}
	got := backend.Environment()
	if len(got) != len(want) {
		t.Fatalf("environment keys=%v", got)
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatalf("environment %s=%q want %q", name, got[name], value)
		}
	}
	got["ATL_JIRA_PAT"] = "changed"
	if backend.Environment()["ATL_JIRA_PAT"] != want["ATL_JIRA_PAT"] {
		t.Fatal("environment map mutation escaped into backend state")
	}
}

func TestMockBackendCloseIsIdempotentAndStopsRequests(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{Method: "GET", Path: "/jira/rest/api/2/field", Status: 200, Body: []byte(`[]`)}},
	}
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	server := backend.HTTPServer()
	backend.Close()
	backend.Close()
	if response, err := server.Client().Get(server.URL + "/jira/rest/api/2/field"); err == nil {
		_ = response.Body.Close()
		t.Fatal("closed mock backend accepted a request")
	}
}

func TestMockFixtureRouteLimit(t *testing.T) {
	validRoute := func(index int) MockRoute {
		return MockRoute{
			Method: "GET", Path: fmt.Sprintf("/wiki/rest/api/content/%d", index),
			Status: http.StatusOK, Body: []byte(`{}`),
		}
	}
	fixture := MockFixture{SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki"}

	t.Run("rejects zero", func(t *testing.T) {
		if err := fixture.Validate(); err == nil {
			t.Fatal("empty route set passed")
		}
	})
	t.Run("accepts 2048", func(t *testing.T) {
		fixture.Routes = make([]MockRoute, 2048)
		for index := range fixture.Routes {
			fixture.Routes[index] = validRoute(index)
		}
		if err := fixture.Validate(); err != nil {
			t.Fatalf("2048 bounded routes rejected: %v", err)
		}
	})
	t.Run("rejects 2049", func(t *testing.T) {
		fixture.Routes = make([]MockRoute, 2049)
		for index := range fixture.Routes {
			fixture.Routes[index] = validRoute(index)
		}
		if err := fixture.Validate(); err == nil {
			t.Fatal("2049 routes passed")
		}
	})
	t.Run("rejects duplicate", func(t *testing.T) {
		route := validRoute(1)
		fixture.Routes = []MockRoute{route, route}
		if err := fixture.Validate(); err == nil {
			t.Fatal("duplicate route passed")
		}
	})
	t.Run("rejects ambiguous", func(t *testing.T) {
		route := validRoute(1)
		route.QueryContains = map[string]string{"start": "0"}
		route.QueryEquals = map[string]string{"start": "0"}
		fixture.Routes = []MockRoute{route}
		if err := fixture.Validate(); err == nil {
			t.Fatal("ambiguous query selector passed")
		}
	})
	t.Run("rejects unmatched context", func(t *testing.T) {
		route := validRoute(1)
		route.Path = "/outside/rest/api/content/1"
		fixture.Routes = []MockRoute{route}
		if err := fixture.Validate(); err == nil {
			t.Fatal("route outside configured contexts passed")
		}
	})
}

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

func TestMockFixtureAcceptsCaseSensitiveProductQueryNames(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{
			Method: "GET", Path: "/jira/rest/api/2/issue/PROJ-1/worklog",
			QueryEquals: map[string]string{"maxResults": "100", "startAt": "0"},
			Status:      http.StatusOK, Body: []byte(`{}`),
		}},
	}
	if err := fixture.Validate(); err != nil {
		t.Fatal(err)
	}
	fixture.Routes[0].QueryEquals["bad name"] = "value"
	if err := fixture.Validate(); err == nil {
		t.Fatal("query name containing whitespace passed")
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

func TestMockFixtureValidatesRequestSequence(t *testing.T) {
	valid := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{
			{Name: "source.read", Method: "GET", Path: "/wiki/rest/api/content/1", Status: http.StatusOK, Body: []byte(`{}`)},
			{Name: "item.create", Method: "POST", Path: "/jira/rest/api/2/issue", RequestBody: []byte(`{"fields":{}}`), Status: http.StatusCreated, Body: []byte(`{}`)},
		},
		RequestSequence: []string{"source.read", "item.create", "source.read"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid request sequence rejected: %v", err)
	}

	for name, mutate := range map[string]func(*MockFixture){
		"unnamed route": func(fixture *MockFixture) { fixture.Routes[0].Name = "" },
		"invalid name":  func(fixture *MockFixture) { fixture.Routes[0].Name = "Source Read" },
		"duplicate name": func(fixture *MockFixture) {
			fixture.Routes[1].Name = fixture.Routes[0].Name
		},
		"unknown reference": func(fixture *MockFixture) { fixture.RequestSequence[0] = "missing" },
		"too long": func(fixture *MockFixture) {
			fixture.RequestSequence = make([]string, 4097)
			for index := range fixture.RequestSequence {
				fixture.RequestSequence[index] = "source.read"
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := valid
			fixture.Routes = append([]MockRoute(nil), valid.Routes...)
			fixture.RequestSequence = append([]string(nil), valid.RequestSequence...)
			mutate(&fixture)
			if err := fixture.Validate(); err == nil {
				t.Fatal("invalid request sequence passed")
			}
		})
	}
}

func TestMockBackendEnforcesRequestSequence(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{
			{Name: "source.read", Method: "GET", Path: "/wiki/rest/api/content/1", Status: http.StatusOK, Body: []byte(`{"source":true}`)},
			{Name: "item.create", Method: "POST", Path: "/jira/rest/api/2/issue", RequestBody: []byte(`{"fields":{}}`), Status: http.StatusCreated, Body: []byte(`{"key":"SYN-1"}`)},
		},
		RequestSequence: []string{"source.read", "item.create", "source.read"},
	}
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	if backend.RequestSequenceComplete() {
		t.Fatal("incomplete request sequence reported complete")
	}

	post := func() int {
		response, err := http.Post(backend.Environment()["ATL_JIRA_URL"]+"/rest/api/2/issue", "application/json", strings.NewReader(`{"fields":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		return response.StatusCode
	}
	get := func() int {
		response, err := http.Get(backend.Environment()["ATL_CONFLUENCE_URL"] + "/rest/api/content/1")
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		return response.StatusCode
	}

	if status := post(); status != http.StatusNotFound {
		t.Fatalf("out-of-order request status=%d", status)
	}
	for index, request := range []func() int{get, post, get} {
		if status := request(); status < 200 || status >= 300 {
			t.Fatalf("ordered request %d status=%d", index, status)
		}
	}
	if !backend.RequestSequenceComplete() {
		t.Fatal("completed request sequence reported incomplete")
	}
	if status := get(); status != http.StatusNotFound {
		t.Fatalf("request after completed sequence status=%d", status)
	}
	methods, unexpected, _ := backend.Summary()
	if methods[http.MethodGet] != 3 || methods[http.MethodPost] != 2 || unexpected != 2 {
		t.Fatalf("methods=%v unexpected=%d", methods, unexpected)
	}
}

func TestMockBackendOrderMismatchDoesNotConsumeResponseSequence(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{
			{Name: "first", Method: "GET", Path: "/wiki/rest/api/content/first", Status: http.StatusOK, Body: []byte(`{}`)},
			{Name: "stateful", Method: "GET", Path: "/wiki/rest/api/content/stateful", Responses: []MockResponse{
				{Status: http.StatusOK, Body: []byte(`{"step":1}`)},
				{Status: http.StatusOK, Body: []byte(`{"step":2}`)},
			}},
		},
		RequestSequence: []string{"first", "stateful", "stateful"},
	}
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	get := func(path string) (int, string) {
		response, err := http.Get(backend.Environment()["ATL_CONFLUENCE_URL"] + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		return response.StatusCode, string(body)
	}
	if status, _ := get("/rest/api/content/stateful"); status != http.StatusNotFound {
		t.Fatalf("out-of-order stateful status=%d", status)
	}
	if status, _ := get("/rest/api/content/first"); status != http.StatusOK {
		t.Fatalf("first status=%d", status)
	}
	for index, want := range []string{`{"step":1}`, `{"step":2}`} {
		status, body := get("/rest/api/content/stateful")
		if status != http.StatusOK || body != want {
			t.Fatalf("stateful response %d status=%d body=%s", index, status, body)
		}
	}
	if !backend.RequestSequenceComplete() {
		t.Fatal("repeated route sequence did not complete")
	}
}
