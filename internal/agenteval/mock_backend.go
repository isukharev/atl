package agenteval

import (
	"io"
	"net/http/httptest"
	"sync"

	"github.com/isukharev/atl/internal/testbackend"
)

const MockFixtureSchemaVersion = testbackend.MockFixtureSchemaVersion

type MockFixture = testbackend.MockFixture
type MockRoute = testbackend.MockRoute
type MockResponse = testbackend.MockResponse

// MockBackend preserves the evaluator-facing API while the implementation and
// fixture contract live in testbackend. The package-private compatibility
// fields remain only for older evaluator tests that issue raw synthetic
// requests and inspect ordered-sequence progress directly.
type MockBackend struct {
	backend      *testbackend.MockBackend
	server       *httptest.Server
	mu           mockBackendCompatibilityMutex
	requestIndex int
}

type mockBackendCompatibilityMutex struct {
	sync.Mutex
	beforeLock func()
}

func (m *mockBackendCompatibilityMutex) Lock() {
	m.Mutex.Lock()
	if m.beforeLock != nil {
		m.beforeLock()
	}
}

func DecodeMockFixture(r io.Reader) (MockFixture, error) {
	return testbackend.DecodeMockFixture(r)
}

func StartMockBackend(fixture MockFixture) (*MockBackend, error) {
	backend, err := testbackend.StartMockBackend(fixture)
	if err != nil {
		return nil, err
	}
	compatibility := &MockBackend{backend: backend, server: backend.HTTPServer()}
	compatibility.mu.beforeLock = func() {
		compatibility.requestIndex = backend.RequestIndex()
	}
	return compatibility, nil
}

func (b *MockBackend) Close() {
	if b != nil {
		b.backend.Close()
	}
}

func (b *MockBackend) Environment() map[string]string {
	return b.backend.Environment()
}

func (b *MockBackend) Summary() (map[string]int, int, int) {
	return b.backend.Summary()
}

func (b *MockBackend) RequestSequenceComplete() bool {
	return b.backend.RequestSequenceComplete()
}

func equalJSONBody(left, right []byte) bool {
	return testbackend.EqualJSONBody(left, right)
}
