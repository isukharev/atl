package confluence

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestContentLabelsPaginationAndWritePayloads(t *testing.T) {
	var posted []domain.ContentLabel
	var deletedName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if request.URL.Path != "/rest/api/content/42/label" {
			t.Fatalf("path=%s", request.URL.Path)
		}
		switch request.Method {
		case http.MethodGet:
			if request.URL.Query().Get("start") == "0" {
				_, _ = io.WriteString(w, `{"results":[{"id":"1","prefix":"global","name":"one","label":"global:one"}],"_links":{"next":"/next"}}`)
			} else {
				_, _ = io.WriteString(w, `{"results":[{"id":"2","prefix":"my","name":"two","label":"my:two"}],"_links":{}}`)
			}
		case http.MethodPost:
			if err := json.NewDecoder(request.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"results":[]}`)
		case http.MethodDelete:
			deletedName = request.URL.Query().Get("name")
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("method=%s", request.Method)
		}
	}))
	t.Cleanup(server.Close)
	adapter := &Confluence{c: newTestClient(server.URL), base: server.URL}
	labels, truncated, err := adapter.ListContentLabels(context.Background(), "42")
	if err != nil || truncated || len(labels) != 2 || labels[1].Prefix != "my" || labels[1].Name != "two" {
		t.Fatalf("labels=%+v truncated=%t err=%v", labels, truncated, err)
	}
	if err := adapter.AddContentLabels(context.Background(), "42", []domain.ContentLabel{{Prefix: "global", Name: "release"}}); err != nil {
		t.Fatal(err)
	}
	if len(posted) != 1 || posted[0].Prefix != "global" || posted[0].Name != "release" {
		t.Fatalf("posted=%+v", posted)
	}
	if err := adapter.RemoveContentLabel(context.Background(), "42", "team/blue"); err != nil {
		t.Fatal(err)
	}
	if deletedName != "team/blue" {
		t.Fatalf("delete query name=%q", deletedName)
	}
}

func TestContentLabelWritesAreNeverRetried(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		t.Run(strings.ToLower(method), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			t.Cleanup(server.Close)
			adapter := &Confluence{c: newTestClient(server.URL), base: server.URL}
			if method == http.MethodPost {
				_ = adapter.AddContentLabels(context.Background(), "42", []domain.ContentLabel{{Prefix: "global", Name: "one"}})
			} else {
				_ = adapter.RemoveContentLabel(context.Background(), "42", "one")
			}
			if calls != 1 {
				t.Fatalf("ambiguous %s was retried %d times", method, calls)
			}
		})
	}
}

func TestContentLabelsReportsPaginationCap(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"prefix":"global","name":"one"}],"_links":{"next":"/next"}}`)
	}))
	t.Cleanup(server.Close)
	adapter := &Confluence{c: newTestClient(server.URL), base: server.URL}
	labels, truncated, err := adapter.ListContentLabels(context.Background(), "42")
	if err != nil || !truncated || calls != maxPages || len(labels) != maxPages {
		t.Fatalf("labels=%d truncated=%t calls=%d err=%v", len(labels), truncated, calls, err)
	}
}
