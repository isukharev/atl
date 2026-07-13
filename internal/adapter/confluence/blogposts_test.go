package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestCreateBlogPostUsesClosedNativePayloadAndExpandedResponse(t *testing.T) {
	var (
		calls   int
		payload map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		if request.Method != http.MethodPost || request.URL.Path != "/rest/api/content" {
			t.Fatalf("request=%s %s", request.Method, request.URL.String())
		}
		if got := request.URL.Query().Get("expand"); got != "body.storage,version,space" {
			t.Fatalf("expand=%q", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"900","type":"blogpost","title":"Release notes","space":{"key":"DOC"},"version":{"number":1},"body":{"storage":{"value":"<p>created</p>"}},"_links":{"webui":"/spaces/DOC/blog/900"}}`)
	}))
	t.Cleanup(server.Close)

	created, err := (&Confluence{c: newTestClient(server.URL), base: server.URL}).CreateBlogPost(
		context.Background(), "DOC", "Release notes", []byte("<p>created</p>"),
	)
	if err != nil || calls != 1 || created.ID != "900" || created.Type != "blogpost" || created.Version != 1 || !created.BodyPresent || string(created.Body) != "<p>created</p>" {
		t.Fatalf("created=%+v calls=%d err=%v", created, calls, err)
	}
	if payload["type"] != "blogpost" || payload["title"] != "Release notes" {
		t.Fatalf("payload=%v", payload)
	}
	if _, found := payload["ancestors"]; found {
		t.Fatalf("blog post payload contains page ancestors: %v", payload)
	}
	space, _ := payload["space"].(map[string]any)
	body, _ := payload["body"].(map[string]any)
	storage, _ := body["storage"].(map[string]any)
	if space["key"] != "DOC" || storage["value"] != "<p>created</p>" || storage["representation"] != "storage" {
		t.Fatalf("payload=%v", payload)
	}
}

func TestCreateBlogPostPreservesMissingBodyAndSentinels(t *testing.T) {
	partial := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"900","type":"blogpost","title":"T","space":{"key":"DOC"},"version":{"number":1}}`)
	}))
	created, err := (&Confluence{c: newTestClient(partial.URL), base: partial.URL}).CreateBlogPost(context.Background(), "DOC", "T", []byte("<p>x</p>"))
	partial.Close()
	if err != nil || created.BodyPresent {
		t.Fatalf("created=%+v err=%v", created, err)
	}

	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	_, err = (&Confluence{c: newTestClient(forbidden.URL), base: forbidden.URL}).CreateBlogPost(context.Background(), "DOC", "T", []byte("<p>x</p>"))
	forbidden.Close()
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error=%v, want ErrForbidden", err)
	}
}

func TestCreateBlogPostDoesNotRetryWrite(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)
	_, _ = (&Confluence{c: newTestClient(server.URL), base: server.URL}).CreateBlogPost(context.Background(), "DOC", "T", []byte("<p>x</p>"))
	if calls != 1 {
		t.Fatalf("non-idempotent blog create was retried %d times", calls)
	}
}
