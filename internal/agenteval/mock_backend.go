package agenteval

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

const MockFixtureSchemaVersion = 1

type MockFixture struct {
	SchemaVersion     int         `json:"schema_version"`
	JiraContext       string      `json:"jira_context"`
	ConfluenceContext string      `json:"confluence_context"`
	Routes            []MockRoute `json:"routes"`
}

type MockRoute struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

type MockBackend struct {
	server *httptest.Server

	mu         sync.Mutex
	methods    map[string]int
	routeHits  map[string]int
	unexpected int
	routes     map[string]MockRoute
	fixture    MockFixture
}

func DecodeMockFixture(r io.Reader) (MockFixture, error) {
	var fixture MockFixture
	if err := decodeStrict(r, &fixture); err != nil {
		return MockFixture{}, err
	}
	if err := fixture.Validate(); err != nil {
		return MockFixture{}, err
	}
	return fixture, nil
}

func (f MockFixture) Validate() error {
	if f.SchemaVersion != MockFixtureSchemaVersion {
		return fmt.Errorf("unsupported mock fixture schema_version %d", f.SchemaVersion)
	}
	for name, context := range map[string]string{"jira_context": f.JiraContext, "confluence_context": f.ConfluenceContext} {
		if !validContextPath(context) {
			return fmt.Errorf("%s must be one contained URL path segment", name)
		}
	}
	if f.JiraContext == f.ConfluenceContext {
		return fmt.Errorf("mock backend contexts must differ")
	}
	if len(f.Routes) == 0 || len(f.Routes) > 256 {
		return fmt.Errorf("mock routes must contain 1..256 entries")
	}
	seen := map[string]struct{}{}
	for _, route := range f.Routes {
		if !methodRE.MatchString(route.Method) || !strings.HasPrefix(route.Path, "/") || strings.ContainsAny(route.Path, "?#\r\n\x00") || len(route.Path) > 512 {
			return fmt.Errorf("invalid mock route")
		}
		if !strings.HasPrefix(route.Path, f.JiraContext+"/") && !strings.HasPrefix(route.Path, f.ConfluenceContext+"/") {
			return fmt.Errorf("mock route lies outside configured contexts")
		}
		if route.Status < 100 || route.Status > 599 || !json.Valid(route.Body) {
			return fmt.Errorf("invalid mock response for %s %s", route.Method, route.Path)
		}
		key := route.Method + " " + route.Path
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate mock route %s", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func StartMockBackend(fixture MockFixture) (*MockBackend, error) {
	if err := fixture.Validate(); err != nil {
		return nil, err
	}
	backend := &MockBackend{methods: map[string]int{}, routeHits: map[string]int{}, routes: map[string]MockRoute{}, fixture: fixture}
	for _, route := range fixture.Routes {
		backend.routes[route.Method+" "+route.Path] = route
	}
	backend.server = httptest.NewServer(http.HandlerFunc(backend.handle))
	return backend, nil
}

func (b *MockBackend) Close() {
	if b != nil && b.server != nil {
		b.server.Close()
	}
}

func (b *MockBackend) Environment() map[string]string {
	return map[string]string{
		"ATL_JIRA_URL":       b.server.URL + b.fixture.JiraContext,
		"ATL_CONFLUENCE_URL": b.server.URL + b.fixture.ConfluenceContext,
		"ATL_JIRA_PAT":       "synthetic-jira-token",
		"ATL_CONFLUENCE_PAT": "synthetic-confluence-token",
		"ATL_ALLOW_INSECURE": "1",
	}
}

func (b *MockBackend) Summary() (map[string]int, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	methods := make(map[string]int, len(b.methods))
	for method, count := range b.methods {
		methods[method] = count
	}
	var duplicates int
	for _, count := range b.routeHits {
		if count > 1 {
			duplicates += count - 1
		}
	}
	return methods, b.unexpected, duplicates
}

func (b *MockBackend) handle(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	b.methods[r.Method]++
	routeKey := r.Method + " " + r.URL.Path
	requestKey := r.Method + " " + r.URL.RequestURI()
	b.routeHits[requestKey]++
	route, ok := b.routes[routeKey]
	if !ok {
		b.unexpected++
	}
	b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errorMessages":["synthetic route not configured"]}`))
		return
	}
	w.WriteHeader(route.Status)
	_, _ = w.Write(route.Body)
}

func validContextPath(value string) bool {
	if len(value) < 2 || value[0] != '/' || strings.Count(value, "/") != 1 {
		return false
	}
	return identifierRE.MatchString(value[1:])
}
