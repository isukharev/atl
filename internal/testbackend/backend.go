// Package testbackend provides a bounded synthetic Atlassian HTTP backend for
// product contract tests and onboarding demonstrations.
package testbackend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
)

const (
	MockFixtureSchemaVersion = 1
	maxContractBytes         = 1 << 20
)

var (
	identifierRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,127}$`)
	methodRE        = regexp.MustCompile(`^[A-Z][A-Z0-9-]{0,31}$`)
	mockQueryNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$`)
)

type MockFixture struct {
	SchemaVersion     int         `json:"schema_version"`
	JiraContext       string      `json:"jira_context"`
	ConfluenceContext string      `json:"confluence_context"`
	Routes            []MockRoute `json:"routes"`
	RequestSequence   []string    `json:"request_sequence,omitempty"`
}

type MockRoute struct {
	Name          string            `json:"name,omitempty"`
	Method        string            `json:"method"`
	Path          string            `json:"path"`
	QueryContains map[string]string `json:"query_contains,omitempty"`
	QueryEquals   map[string]string `json:"query_equals,omitempty"`
	RequestBody   json.RawMessage   `json:"request_body,omitempty"`
	Status        int               `json:"status,omitempty"`
	Body          json.RawMessage   `json:"body,omitempty"`
	Responses     []MockResponse    `json:"responses,omitempty"`
}

// MockResponse is one bounded response in a stateful synthetic route. A
// sequence is consumed only by requests that satisfy the route's query and
// request-body constraints; any request after the last response is unexpected.
// This lets synthetic mutation tests model preflight, reconciliation, and
// replay refusal without teaching the backend product-specific state changes.
type MockResponse struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

type MockBackend struct {
	server *httptest.Server

	mu           sync.Mutex
	methods      map[string]int
	routeHits    map[string]int
	sequenceHits map[string]int
	requestIndex int
	unexpected   int
	routes       map[string][]MockRoute
	fixture      MockFixture
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

func decodeStrict(r io.Reader, dst any) error {
	limited := &io.LimitedReader{R: r, N: maxContractBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode contract: %w", err)
	}
	if limited.N <= 0 {
		return fmt.Errorf("contract exceeds %d bytes", maxContractBytes)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("contract contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing contract data: %w", err)
	}
	return nil
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
	if len(f.Routes) == 0 || len(f.Routes) > 2048 {
		return fmt.Errorf("mock routes must contain 1..2048 entries")
	}
	seen := map[string]struct{}{}
	seenNames := map[string]struct{}{}
	type routeDiscriminator struct {
		query bool
		body  bool
	}
	seenPaths := map[string]routeDiscriminator{}
	for _, route := range f.Routes {
		if route.Name != "" {
			if !identifierRE.MatchString(route.Name) {
				return fmt.Errorf("invalid mock route name")
			}
			if _, duplicate := seenNames[route.Name]; duplicate {
				return fmt.Errorf("duplicate mock route name %q", route.Name)
			}
			seenNames[route.Name] = struct{}{}
		} else if len(f.RequestSequence) > 0 {
			return fmt.Errorf("all mock routes must be named when request_sequence is configured")
		}
		if !methodRE.MatchString(route.Method) || !strings.HasPrefix(route.Path, "/") || strings.ContainsAny(route.Path, "?#\r\n\x00") || len(route.Path) > 512 {
			return fmt.Errorf("invalid mock route")
		}
		if !strings.HasPrefix(route.Path, f.JiraContext+"/") && !strings.HasPrefix(route.Path, f.ConfluenceContext+"/") {
			return fmt.Errorf("mock route lies outside configured contexts")
		}
		hasSingle := route.Status != 0 || len(route.Body) != 0
		hasSequence := len(route.Responses) != 0
		if hasSingle == hasSequence {
			return fmt.Errorf("mock route must define exactly one response or response sequence for %s %s", route.Method, route.Path)
		}
		if hasSingle && !validMockResponse(route.Status, route.Body) {
			return fmt.Errorf("invalid mock response for %s %s", route.Method, route.Path)
		}
		if len(route.Responses) > 32 {
			return fmt.Errorf("mock response sequence exceeds 32 entries for %s %s", route.Method, route.Path)
		}
		for _, response := range route.Responses {
			if !validMockResponse(response.Status, response.Body) {
				return fmt.Errorf("invalid mock response sequence for %s %s", route.Method, route.Path)
			}
		}
		if len(route.RequestBody) > 1<<20 || len(route.RequestBody) > 0 && (!json.Valid(route.RequestBody) || route.Method == http.MethodGet || route.Method == http.MethodHead) {
			return fmt.Errorf("invalid mock request body for %s %s", route.Method, route.Path)
		}
		if len(route.QueryContains)+len(route.QueryEquals) > 16 {
			return fmt.Errorf("mock route query constraints exceed 16 entries")
		}
		for name := range route.QueryEquals {
			if _, duplicate := route.QueryContains[name]; duplicate {
				return fmt.Errorf("mock route query constraint is ambiguous")
			}
		}
		for name, value := range mergeMockQueryConstraints(route.QueryContains, route.QueryEquals) {
			if !mockQueryNameRE.MatchString(name) || value == "" || len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
				return fmt.Errorf("invalid mock route query constraint")
			}
		}
		pathKey := route.Method + " " + route.Path
		discriminator := routeDiscriminator{
			query: len(route.QueryContains)+len(route.QueryEquals) > 0,
			body:  len(route.RequestBody) > 0,
		}
		if previous, duplicatePath := seenPaths[pathKey]; duplicatePath {
			readRoute := route.Method == http.MethodGet || route.Method == http.MethodHead
			if readRoute && (!previous.query || !discriminator.query) || !readRoute && (!previous.body || !discriminator.body) {
				return fmt.Errorf("duplicate mock route %s", pathKey)
			}
		} else {
			seenPaths[pathKey] = discriminator
		}
		key := mockRouteSelectorKey(route)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate mock route %s", pathKey)
		}
		seen[key] = struct{}{}
	}
	if len(f.RequestSequence) > 4096 {
		return fmt.Errorf("mock request_sequence exceeds 4096 entries")
	}
	for _, name := range f.RequestSequence {
		if _, ok := seenNames[name]; !ok {
			return fmt.Errorf("mock request_sequence references unknown route %q", name)
		}
	}
	return nil
}

func validMockResponse(status int, body json.RawMessage) bool {
	return status >= 100 && status <= 599 && json.Valid(body)
}

func StartMockBackend(fixture MockFixture) (*MockBackend, error) {
	if err := fixture.Validate(); err != nil {
		return nil, err
	}
	backend := &MockBackend{methods: map[string]int{}, routeHits: map[string]int{}, sequenceHits: map[string]int{}, routes: map[string][]MockRoute{}, fixture: fixture}
	for _, route := range fixture.Routes {
		key := route.Method + " " + route.Path
		backend.routes[key] = append(backend.routes[key], route)
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

// RequestSequenceComplete reports whether every configured ordered request was
// accepted. Fixtures without an ordered sequence are complete by definition.
func (b *MockBackend) RequestSequenceComplete() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.requestIndex == len(b.fixture.RequestSequence)
}

func (b *MockBackend) handle(w http.ResponseWriter, r *http.Request) {
	routeKey := r.Method + " " + r.URL.Path
	candidates := b.routes[routeKey]
	needsBody := false
	for _, candidate := range candidates {
		if len(candidate.RequestBody) > 0 {
			needsBody = true
			break
		}
	}
	var requestBody []byte
	bodyOK := true
	if needsBody {
		var err error
		requestBody, err = io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		bodyOK = err == nil && len(requestBody) <= 1<<20 && json.Valid(requestBody)
	}
	var route MockRoute
	matched := 0
	for _, candidate := range candidates {
		if mockRouteQueryMatches(candidate, r) && bodyOK && (len(candidate.RequestBody) == 0 || equalJSONBody(requestBody, candidate.RequestBody)) {
			route = candidate
			matched++
		}
	}
	ok := matched == 1
	b.mu.Lock()
	b.methods[r.Method]++
	requestKey := r.Method + " " + r.URL.RequestURI()
	b.routeHits[requestKey]++
	if ok && len(b.fixture.RequestSequence) > 0 && (b.requestIndex >= len(b.fixture.RequestSequence) || route.Name != b.fixture.RequestSequence[b.requestIndex]) {
		ok = false
	}
	if ok && len(route.Responses) > 0 {
		sequenceKey := mockRouteSelectorKey(route)
		index := b.sequenceHits[sequenceKey]
		if index >= len(route.Responses) {
			ok = false
		} else {
			route.Status = route.Responses[index].Status
			route.Body = route.Responses[index].Body
		}
	}
	if ok {
		if len(route.Responses) > 0 {
			b.sequenceHits[mockRouteSelectorKey(route)]++
		}
		if len(b.fixture.RequestSequence) > 0 {
			b.requestIndex++
		}
	} else {
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

func mockRouteQueryMatches(route MockRoute, request *http.Request) bool {
	query := request.URL.Query()
	for name, value := range route.QueryContains {
		if !strings.Contains(query.Get(name), value) {
			return false
		}
	}
	for name, value := range route.QueryEquals {
		values, ok := query[name]
		if !ok || len(values) != 1 || values[0] != value {
			return false
		}
	}
	return true
}

func mockRouteSelectorKey(route MockRoute) string {
	requestBody := canonicalMockRequestBody(route.RequestBody)
	selector, _ := json.Marshal(struct {
		Method        string            `json:"method"`
		Path          string            `json:"path"`
		QueryContains map[string]string `json:"query_contains,omitempty"`
		QueryEquals   map[string]string `json:"query_equals,omitempty"`
		RequestBody   json.RawMessage   `json:"request_body,omitempty"`
	}{route.Method, route.Path, route.QueryContains, route.QueryEquals, requestBody})
	return string(selector)
}

func canonicalMockRequestBody(body []byte) json.RawMessage {
	if len(body) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(body, &value) != nil {
		return append(json.RawMessage(nil), body...)
	}
	canonical, _ := json.Marshal(value)
	return canonical
}

func mergeMockQueryConstraints(left, right map[string]string) map[string]string {
	merged := make(map[string]string, len(left)+len(right))
	for name, value := range left {
		merged[name] = value
	}
	for name, value := range right {
		merged[name] = value
	}
	return merged
}

func equalJSONBody(left, right []byte) bool {
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&leftValue) != nil || leftDecoder.Decode(new(any)) != io.EOF || rightDecoder.Decode(&rightValue) != nil || rightDecoder.Decode(new(any)) != io.EOF {
		return false
	}
	leftJSON, leftErr := json.Marshal(leftValue)
	rightJSON, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func validContextPath(value string) bool {
	if len(value) < 2 || value[0] != '/' || strings.Count(value, "/") != 1 {
		return false
	}
	return identifierRE.MatchString(value[1:])
}
