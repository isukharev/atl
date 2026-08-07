package app

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

func TestVerifyConfluenceReturnsName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"displayName":"Jane Doe"}`))
	}))
	defer srv.Close()

	name, err := VerifyConfluence(context.Background(), srv.URL, "tok", "test")
	if err != nil {
		t.Fatalf("VerifyConfluence: %v", err)
	}
	if name != "Jane Doe" {
		t.Fatalf("got %q, want Jane Doe", name)
	}
}

func TestVerifyJiraReturnsName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"displayName":"Jane Doe"}`))
	}))
	defer srv.Close()

	name, err := VerifyJira(context.Background(), srv.URL, "tok", "test")
	if err != nil {
		t.Fatalf("VerifyJira: %v", err)
	}
	if name != "Jane Doe" {
		t.Fatalf("got %q, want Jane Doe", name)
	}
}

func TestVerifyJiraUsesBackendScopedCABundle(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/2/myself" {
			t.Errorf("path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"displayName":"Private PKI User"}`))
	}))
	defer srv.Close()
	bundle := filepath.Join(t.TempDir(), "jira-ca.pem")
	body := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(bundle, body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Transport: &config.TransportConfig{Jira: &config.BackendTransportConfig{CABundle: bundle}}}
	name, err := VerifyJiraWithConfig(context.Background(), srv.URL, "tok", "test", cfg)
	if err != nil || name != "Private PKI User" {
		t.Fatalf("name=%q err=%v", name, err)
	}
	wrongBackend := &config.Config{Transport: &config.TransportConfig{Confluence: &config.BackendTransportConfig{CABundle: bundle}}}
	if _, err := VerifyJiraWithConfig(domain.WithSingleAttempt(context.Background()), srv.URL, "tok", "test", wrongBackend); err == nil {
		t.Fatal("Confluence CA bundle unexpectedly affected Jira trust")
	}
}

func TestVerifyRejectsInsecureURL(t *testing.T) {
	if _, err := VerifyConfluence(context.Background(), "http://confluence.example.com", "tok", "test"); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("got %v, want ErrUsage for non-https non-loopback URL", err)
	}
	if _, err := VerifyJira(context.Background(), "http://jira.example.com", "tok", "test"); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("VerifyJira: got %v, want ErrUsage for non-https non-loopback URL", err)
	}
}
