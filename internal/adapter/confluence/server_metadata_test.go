package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

func TestServerMetadataProjectsOnlyStaticProductAndVersion(t *testing.T) {
	requests := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
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
	if len(requests) != 1 {
		t.Fatalf("requests = %v, want one modern metadata GET", requests)
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

func TestServerMetadataFallsBackToOneBodyFreeLegacyProbe(t *testing.T) {
	requests := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		switch len(requests) {
		case 1:
			if r.Method != http.MethodGet || r.URL.RequestURI() != "/rest/api/server-information" {
				t.Errorf("first request = %s %s", r.Method, r.URL.RequestURI())
			}
			http.Error(w, "private-not-found-canary", http.StatusNotFound)
		case 2:
			if r.Method != http.MethodHead || r.URL.RequestURI() != "/rest/api/content" {
				t.Errorf("fallback request = %s %s", r.Method, r.URL.RequestURI())
			}
			w.Header().Set("X-Private-Backend", "private-header-canary")
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	got, err := cf.ServerMetadata(domain.WithSingleAttempt(context.Background()))
	if err != nil {
		t.Fatalf("ServerMetadata: %v", err)
	}
	want := domain.ServerMetadata{Product: domain.ServerProductConfluence}
	if got != want {
		t.Fatalf("metadata = %#v, want %#v", got, want)
	}
	if strings.Join(requests, ",") != "GET /rest/api/server-information,HEAD /rest/api/content" {
		t.Fatalf("requests = %v", requests)
	}
	projected, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	for _, sensitive := range []string{"private-not-found-canary", "private-header-canary"} {
		if strings.Contains(string(projected), sensitive) {
			t.Fatalf("private fallback value %q crossed adapter boundary: %s", sensitive, projected)
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

func TestServerMetadataDoesNotFallbackOnClosedPrimaryHTTPFailures(t *testing.T) {
	for name, status := range map[string]int{
		"authentication": http.StatusUnauthorized,
		"forbidden":      http.StatusForbidden,
		"rate limited":   http.StatusTooManyRequests,
		"server error":   http.StatusInternalServerError,
	} {
		t.Run(name, func(t *testing.T) {
			requests := []string{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requests = append(requests, request.Method+" "+request.URL.RequestURI())
				http.Error(w, "private backend detail", status)
			}))
			defer srv.Close()

			cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
			if _, err := cf.ServerMetadata(domain.WithSingleAttempt(context.Background())); err == nil {
				t.Fatal("ServerMetadata error = nil, want HTTP error")
			}
			if strings.Join(requests, ",") != "GET /rest/api/server-information" {
				t.Fatalf("requests = %v, non-404 failure must not trigger fallback", requests)
			}
		})
	}
}

func TestServerMetadataDoesNotFallbackOnTimeout(t *testing.T) {
	requestSeen := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		requestSeen <- request.Method + " " + request.URL.RequestURI()
		<-request.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(domain.WithSingleAttempt(context.Background()), 50*time.Millisecond)
	defer cancel()
	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	if _, err := cf.ServerMetadata(ctx); err == nil {
		t.Fatal("ServerMetadata error = nil, want timeout error")
	}
	if request := <-requestSeen; request != "GET /rest/api/server-information" {
		t.Fatalf("request = %q", request)
	}
}

func TestServerMetadataMalformedJSONFails(t *testing.T) {
	requests := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	if _, err := cf.ServerMetadata(domain.WithSingleAttempt(context.Background())); err == nil {
		t.Fatal("ServerMetadata error = nil, want JSON decode error")
	}
	if strings.Join(requests, ",") != "GET /rest/api/server-information" {
		t.Fatalf("requests = %v, malformed JSON must not trigger fallback", requests)
	}
}

func TestServerMetadataPreservesHTTPErrorMapping(t *testing.T) {
	requests := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, err := cf.ServerMetadata(domain.WithSingleAttempt(context.Background()))
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("ServerMetadata error = %v, want ErrForbidden", err)
	}
	if strings.Join(requests, ",") != "GET /rest/api/server-information" {
		t.Fatalf("requests = %v, forbidden must not trigger fallback", requests)
	}
}

func TestServerMetadataLegacyProbeFailureIsClosedAtTwoRequests(t *testing.T) {
	for name, status := range map[string]int{
		"authentication": http.StatusUnauthorized,
		"forbidden":      http.StatusForbidden,
		"not found":      http.StatusNotFound,
		"server error":   http.StatusInternalServerError,
	} {
		t.Run(name, func(t *testing.T) {
			requests := []string{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requests = append(requests, request.Method+" "+request.URL.RequestURI())
				if len(requests) == 1 {
					http.Error(w, "private primary detail", http.StatusNotFound)
					return
				}
				http.Error(w, "private fallback detail", status)
			}))
			defer srv.Close()

			cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
			_, err := cf.ServerMetadata(domain.WithSingleAttempt(context.Background()))
			if err == nil {
				t.Fatal("ServerMetadata error = nil, want fallback error")
			}
			if strings.Join(requests, ",") != "GET /rest/api/server-information,HEAD /rest/api/content" {
				t.Fatalf("requests = %v, want exactly one GET and one HEAD", requests)
			}
		})
	}
}
