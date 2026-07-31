package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestServerMetadataProjectsOnlyStaticProductAndVersion(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/rest/api/server-information" {
			t.Errorf("request = %s %s, want GET /rest/api/server-information", r.Method, r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"product":"untrusted-product",
			"deploymentType":"untrusted-deployment",
			"version":"9.5.2",
			"baseUrl":"https://private.example.invalid/wiki",
			"serverTitle":"Private Confluence",
			"buildDate":"2026-07-01"
		}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	got, err := cf.ServerMetadata(domain.WithSingleAttempt(context.Background()))
	if err != nil {
		t.Fatalf("ServerMetadata: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	want := (domain.ServerMetadata{Product: domain.ServerProductConfluence, Version: "9.5.2"})
	if got != want {
		t.Fatalf("metadata = %#v, want %#v", got, want)
	}
	projected, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	for _, sensitive := range []string{"untrusted-product", "untrusted-deployment", "private.example.invalid", "Private Confluence", "2026-07-01"} {
		if strings.Contains(string(projected), sensitive) {
			t.Fatalf("sensitive response field %q crossed adapter boundary: %s", sensitive, projected)
		}
	}
}

func TestServerMetadataPreservesSingleAttemptOnHTTPFailure(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "private backend detail", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, err := cf.ServerMetadata(domain.WithSingleAttempt(context.Background()))
	if err == nil {
		t.Fatal("ServerMetadata error = nil, want HTTP error")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestServerMetadataMalformedJSONFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	if _, err := cf.ServerMetadata(domain.WithSingleAttempt(context.Background())); err == nil {
		t.Fatal("ServerMetadata error = nil, want JSON decode error")
	}
}

func TestServerMetadataPreservesHTTPErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, err := cf.ServerMetadata(domain.WithSingleAttempt(context.Background()))
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("ServerMetadata error = %v, want ErrForbidden", err)
	}
}
