package httpx

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestVerboseTrace_LogsRequestAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	c := New(srv.URL, "secret-token", "test", WithTrace(&buf))
	if _, err := c.Do(context.Background(), http.MethodGet, "/x?jql=project%3DSECRET&fields=summary&fields=status", nil, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "→ GET") {
		t.Errorf("trace missing request line: %q", out)
	}
	if !strings.Contains(out, "← 200") {
		t.Errorf("trace missing status line: %q", out)
	}
	// The PAT must never appear in a trace.
	if strings.Contains(out, "secret-token") {
		t.Errorf("trace leaked the bearer token: %q", out)
	}
	if strings.Contains(out, "SECRET") || strings.Contains(out, "summary") || strings.Contains(out, "status") {
		t.Errorf("trace leaked query values: %q", out)
	}
	if !strings.Contains(out, "jql=%3Credacted%3E") || !strings.Contains(out, "fields=%3Credacted%3E") {
		t.Errorf("trace should retain redacted query keys: %q", out)
	}
}

func TestVerboseTrace_DisabledByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "test")
	if _, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

func TestTypedNilTraceWriterIsDisabled(t *testing.T) {
	var writer *bytes.Buffer
	client := New("https://example.invalid", "token", "test", WithTrace(writer))
	client.tracef("must stay disabled")
	if client.trace != nil {
		t.Fatalf("typed-nil trace writer retained as %#v", client.trace)
	}
}

func TestClientsKeepTraceConflictAndClearancePoliciesIndependent(t *testing.T) {
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			ready <- struct{}{}
			<-release
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"errorMessages":["conflict"]}`))
			return
		}
		writes.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	var genericTrace, versionTrace bytes.Buffer
	generic := New(server.URL, "generic-token", "test",
		WithTrace(&genericTrace), WithGenericConflict(), WithRequiredWriteClearance())
	versioned := New(server.URL, "version-token", "test", WithTrace(&versionTrace))

	type result struct {
		conflict error
		write    error
	}
	results := make(chan result, 2)
	go func() {
		_, conflictErr := generic.Do(context.Background(), http.MethodGet, "/generic-conflict", nil, nil)
		_, writeErr := generic.Do(context.Background(), http.MethodPost, "/blocked-write", []byte(`{}`), nil)
		results <- result{conflict: conflictErr, write: writeErr}
	}()
	go func() {
		_, conflictErr := versioned.Do(context.Background(), http.MethodGet, "/version-conflict", nil, nil)
		_, writeErr := versioned.Do(context.Background(), http.MethodPost, "/allowed-write", []byte(`{}`), nil)
		results <- result{conflict: conflictErr, write: writeErr}
	}()
	<-ready
	<-ready
	close(release)

	first, second := <-results, <-results
	var genericResult, versionResult result
	for _, got := range []result{first, second} {
		if errors.Is(got.conflict, domain.ErrVersionConflict) {
			versionResult = got
		} else {
			genericResult = got
		}
	}
	if genericResult.conflict == nil || errors.Is(genericResult.conflict, domain.ErrVersionConflict) {
		t.Fatalf("generic conflict error = %v", genericResult.conflict)
	}
	if !errors.Is(genericResult.write, domain.ErrCheckFailed) {
		t.Fatalf("required-clearance write error = %v", genericResult.write)
	}
	if !errors.Is(versionResult.conflict, domain.ErrVersionConflict) || versionResult.write != nil {
		t.Fatalf("versioned client errors = conflict %v, write %v", versionResult.conflict, versionResult.write)
	}
	if got := writes.Load(); got != 1 {
		t.Fatalf("transport writes = %d, want only the default client's write", got)
	}
	if got := genericTrace.String(); !strings.Contains(got, "/generic-conflict") || strings.Contains(got, "/version-conflict") || strings.Contains(got, "/allowed-write") {
		t.Fatalf("generic trace crossed client boundary: %q", got)
	}
	if got := versionTrace.String(); !strings.Contains(got, "/version-conflict") || !strings.Contains(got, "/allowed-write") || strings.Contains(got, "/generic-conflict") {
		t.Fatalf("versioned trace crossed client boundary: %q", got)
	}
}
