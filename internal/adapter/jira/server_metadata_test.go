package jira

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

func TestServerMetadataProjectsOnlyProductDeploymentAndVersion(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/rest/api/2/serverInfo" {
			t.Errorf("request = %s %s, want GET /rest/api/2/serverInfo", r.Method, r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"product":"untrusted-product",
			"deploymentType":"Data Center",
			"version":"9.12.1",
			"baseUrl":"https://private.example.invalid/jira",
			"serverTitle":"Private Jira",
			"buildDate":"2026-07-01",
			"serverTime":"2026-07-31T10:11:12.000+0000"
		}`))
	}))
	defer srv.Close()

	got, err := newTestJira(srv).ServerMetadata(domain.WithSingleAttempt(context.Background()))
	if err != nil {
		t.Fatalf("ServerMetadata: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	want := (domain.ServerMetadata{Product: domain.ServerProductJira, DeploymentType: "Data Center", Version: "9.12.1"})
	if got != want {
		t.Fatalf("metadata = %#v, want %#v", got, want)
	}
	if strings.Contains(got.Product, "untrusted") {
		t.Fatalf("backend product crossed adapter boundary: %#v", got)
	}
	projected, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	for _, sensitive := range []string{"private.example.invalid", "Private Jira", "2026-07-01", "2026-07-31"} {
		if strings.Contains(string(projected), sensitive) {
			t.Fatalf("sensitive response field %q crossed adapter boundary: %s", sensitive, projected)
		}
	}
}

func TestServerMetadataPreservesSingleAttemptOnHTTPFailure(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "private backend detail", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := newTestJira(srv).ServerMetadata(domain.WithSingleAttempt(context.Background()))
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

	if _, err := newTestJira(srv).ServerMetadata(domain.WithSingleAttempt(context.Background())); err == nil {
		t.Fatal("ServerMetadata error = nil, want JSON decode error")
	}
}

func TestServerMetadataProjectsNumericAndQuotedBuildNumbers(t *testing.T) {
	for name, raw := range map[string]string{"numeric": `812000`, "quoted": `"812001"`} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"deploymentType":"Data Center","version":"9.12.1","buildNumber":` + raw + `}`))
			}))
			defer srv.Close()
			got, err := newTestJira(srv).ExactServerMetadata(domain.WithSingleAttempt(context.Background()))
			if err != nil {
				t.Fatal(err)
			}
			if got.BuildNumber != strings.Trim(raw, `"`) {
				t.Fatalf("build = %q, raw %s", got.BuildNumber, raw)
			}
		})
	}
}

func TestOrdinaryServerMetadataIgnoresMalformedBuildButExactPathRejectsIt(t *testing.T) {
	for _, raw := range []string{`1.5`, `-1`, `"12x"`, `"123456789012345678901"`} {
		t.Run(raw, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"version":"9.12.1","buildNumber":` + raw + `}`))
			}))
			defer srv.Close()
			adapter := newTestJira(srv)
			metadata, err := adapter.ServerMetadata(domain.WithSingleAttempt(context.Background()))
			if err != nil || metadata.BuildNumber != "" {
				t.Fatalf("ordinary metadata = %+v, error = %v; want empty build without error", metadata, err)
			}
			if _, err := adapter.ExactServerMetadata(domain.WithSingleAttempt(context.Background())); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("exact error = %v, want ErrCheckFailed", err)
			}
		})
	}
}

func TestServerMetadataPreservesHTTPErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := newTestJira(srv).ServerMetadata(domain.WithSingleAttempt(context.Background()))
	if !errors.Is(err, domain.ErrAuth) {
		t.Fatalf("ServerMetadata error = %v, want ErrAuth", err)
	}
}
