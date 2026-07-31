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

func TestReadGraphPageMetadataUsesOneMinimalRelativeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodGet || request.URL.RequestURI() != "/rest/api/content/123" {
			t.Errorf("request = %s %s, want GET /rest/api/content/123", request.Method, request.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"123",
			"title":"Deployment notes",
			"body":{"storage":{"value":"private-body-canary"}},
			"ancestors":[{"title":"private-ancestor-canary"}],
			"metadata":{"labels":{"results":[{"name":"private-label-canary"}]}},
			"restrictions":{"read":{"restrictions":{"user":{"results":["private-user-canary"]}}}},
			"_links":{"base":"https://foreign.example.invalid","webui":"/private-path-canary"}
		}`))
	}))
	defer server.Close()

	reader := &Confluence{c: newTestClient(server.URL), base: server.URL}
	got, err := reader.ReadGraphPageMetadata(context.Background(), "123")
	if err != nil {
		t.Fatalf("ReadGraphPageMetadata: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if got != (domain.ConfluenceGraphPageMetadata{ID: "123", Title: "Deployment notes"}) {
		t.Fatalf("metadata = %#v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	for _, forbidden := range []string{
		"private-body-canary", "private-ancestor-canary", "private-label-canary",
		"private-user-canary", "foreign.example.invalid", "private-path-canary",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("extra response field %q crossed adapter boundary: %s", forbidden, encoded)
		}
	}
}

func TestReadGraphPageMetadataRejectsNonCanonicalIDBeforeNetwork(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()

	reader := &Confluence{c: newTestClient(server.URL), base: server.URL}
	invalid := []string{
		"", "0", "01", "+1", "-1", " 1", "1 ", "1/../2",
		"1?expand=body.storage", "https://foreign.example.invalid/pages/1",
		strings.Repeat("1", confluenceGraphPageIDMaxDigits+1),
	}
	for _, id := range invalid {
		if _, err := reader.ReadGraphPageMetadata(context.Background(), id); !errors.Is(err, domain.ErrUsage) {
			t.Errorf("id %q error = %v, want ErrUsage", id, err)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid ids caused %d requests", requests)
	}
}

func TestReadGraphPageMetadataRejectsMissingOrMismatchedIdentity(t *testing.T) {
	for name, response := range map[string]string{
		"missing":    `{"title":"Deployment notes"}`,
		"mismatched": `{"id":"124","title":"Deployment notes"}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()

			reader := &Confluence{c: newTestClient(server.URL), base: server.URL}
			if _, err := reader.ReadGraphPageMetadata(context.Background(), "123"); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error = %v, want ErrCheckFailed", err)
			}
		})
	}
}

func TestReadGraphPageMetadataForcesOneTransportAttempt(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"retryable status": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "backend detail", http.StatusTooManyRequests)
		},
		"redirect": func(w http.ResponseWriter, request *http.Request) {
			http.Redirect(w, request, "/rest/api/content/124", http.StatusFound)
		},
	} {
		t.Run(name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requests++
				handler(w, request)
			}))
			defer server.Close()

			reader := &Confluence{c: newTestClient(server.URL), base: server.URL}
			if _, err := reader.ReadGraphPageMetadata(context.Background(), "123"); err == nil {
				t.Fatal("error = nil, want transport error")
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
		})
	}
}

func TestReadGraphPageMetadataRejectsMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"123","title":`))
	}))
	defer server.Close()

	reader := &Confluence{c: newTestClient(server.URL), base: server.URL}
	if _, err := reader.ReadGraphPageMetadata(context.Background(), "123"); err == nil {
		t.Fatal("error = nil, want JSON decode error")
	}
}
