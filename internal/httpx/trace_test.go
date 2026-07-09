package httpx

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerboseTrace_LogsRequestAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	SetTrace(&buf)
	defer SetTrace(nil)

	c := New(srv.URL, "secret-token", "test")
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
	SetTrace(nil) // explicit: disabled
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
