package httpx

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type evalRoundTripFunc func(*http.Request) (*http.Response, error)

func (f evalRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestEvaluationHTTPGuardRecordsReadsWithoutPrivateURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(evaluationHTTPGuardEnv, path)
	var calls int
	transport := withEvaluationHTTPGuard(evalRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(nil)), Request: request}, nil
	}))
	request, err := http.NewRequest(http.MethodGet, "https://example.invalid/rest?q=private-selector", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !bytes.Contains(data, []byte(`"method":"GET"`)) || bytes.Contains(data, []byte("private-selector")) || bytes.Contains(data, []byte("example.invalid")) {
		t.Fatalf("calls=%d audit=%s", calls, data)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
}

func TestEvaluationHTTPGuardBlocksWritesBeforeTransport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(evaluationHTTPGuardEnv, path)
	called := false
	transport := withEvaluationHTTPGuard(evalRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	}))
	request, err := http.NewRequest(http.MethodPost, "https://example.invalid/rest", strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "blocked non-read method POST") {
		t.Fatalf("err=%v", err)
	}
	if called {
		t.Fatal("underlying transport observed a blocked write")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("blocked request unexpectedly created audit file: %v", err)
	}
}

func TestClientConstructionAlwaysInstallsEvaluationHTTPGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(evaluationHTTPGuardEnv, path)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(server.URL, "token", "test")
	if _, err := client.Do(context.Background(), http.MethodPost, "/rest", []byte("body"), nil); err == nil {
		t.Fatal("client allowed a write while the evaluation guard was enabled")
	}
	if calls != 0 {
		t.Fatalf("guarded client sent %d requests", calls)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("blocked request unexpectedly created audit file: %v", err)
	}
}
