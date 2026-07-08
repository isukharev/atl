package confluence

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/isukharev/atl/internal/httpx"
)

func newTestClient(url string) *httpx.Client {
	return httpx.New(url, "token", "test")
}

// TestGetMetaGroupRestrictions verifies that a page whose read.user.results is
// empty but read.group.results is non-empty reports Restrictions==true. The old
// substring heuristic missed group-only restrictions.
func TestGetMetaGroupRestrictions(t *testing.T) {
	const body = `{
		"id": "100",
		"title": "Secret",
		"space": {"key": "DOC"},
		"version": {"number": 3},
		"restrictions": {
			"read": {
				"restrictions": {
					"user": {"results": []},
					"group": {"results": [{"name": "confluence-administrators"}]}
				}
			}
		}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	m, err := cf.GetMeta(context.Background(), "100")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if !m.Restrictions {
		t.Fatalf("expected Restrictions=true for group-only restriction, got false")
	}
}

// TestGetMetaNoRestrictions verifies that a page with both user and group
// results empty reports Restrictions==false.
func TestGetMetaNoRestrictions(t *testing.T) {
	const body = `{
		"id": "101",
		"title": "Open",
		"space": {"key": "DOC"},
		"version": {"number": 1},
		"restrictions": {
			"read": {
				"restrictions": {
					"user": {"results": []},
					"group": {"results": []}
				}
			}
		}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	m, err := cf.GetMeta(context.Background(), "101")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if m.Restrictions {
		t.Fatalf("expected Restrictions=false when both user and group are empty, got true")
	}
}

// TestListCommentsPaginates verifies that a server returning two pages of
// comments yields all items concatenated, following _links.next.
func TestListCommentsPaginates(t *testing.T) {
	page1 := `{
		"results": [
			{"id": "c1", "history": {"createdBy": {"displayName": "Alice"}}, "body": {"storage": {"value": "<p>one</p>"}}},
			{"id": "c2", "history": {"createdBy": {"displayName": "Bob"}}, "body": {"storage": {"value": "<p>two</p>"}}}
		],
		"_links": {"next": "/rest/api/content/200/child/comment?start=2"}
	}`
	page2 := `{
		"results": [
			{"id": "c3", "history": {"createdBy": {"displayName": "Carol"}}, "body": {"storage": {"value": "<p>three</p>"}}}
		],
		"_links": {}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("start") == "0" {
			_, _ = w.Write([]byte(page1))
			return
		}
		_, _ = w.Write([]byte(page2))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	got, truncated, err := cf.ListComments(context.Background(), "200")
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if truncated {
		t.Errorf("a naturally-exhausted listing must not report truncated")
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 comments across two pages, got %d: %+v", len(got), got)
	}
	wantIDs := []string{"c1", "c2", "c3"}
	for i, w := range wantIDs {
		if got[i].ID != w {
			t.Errorf("comment[%d].ID = %q, want %q", i, got[i].ID, w)
		}
	}
}

// TestListCommentsTruncates verifies that a server that never stops signaling
// _links.next drives the pagination safety cap and reports truncated=true, so a
// silently-clipped set can never be baked into the mirror.
func TestListCommentsTruncates(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		// Always return one result and always dangle a next link.
		_, _ = w.Write([]byte(`{"results":[{"id":"c","history":{"createdBy":{"displayName":"A"}},"body":{"storage":{"value":"<p>x</p>"}}}],"_links":{"next":"/next"}}`))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	got, truncated, err := cf.ListComments(context.Background(), "200")
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if !truncated {
		t.Errorf("a server that never stops paging must report truncated=true")
	}
	if hits != maxPages {
		t.Errorf("expected the cap to stop after %d requests, got %d", maxPages, hits)
	}
	if len(got) != maxPages {
		t.Errorf("expected %d comments collected before the cap, got %d", maxPages, len(got))
	}
}

// TestListAttachmentsPaginates verifies attachment paging follows _links.next.
func TestListAttachmentsPaginates(t *testing.T) {
	page1 := `{
		"results": [
			{"id": "a1", "title": "one.png", "version": {"number": 1}},
			{"id": "a2", "title": "two.png", "version": {"number": 1}}
		],
		"_links": {"next": "/rest/api/content/300/child/attachment?start=2"}
	}`
	page2 := `{
		"results": [
			{"id": "a3", "title": "three.png", "version": {"number": 1}}
		],
		"_links": {}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("start") == "0" {
			_, _ = w.Write([]byte(page1))
			return
		}
		_, _ = w.Write([]byte(page2))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	got, err := cf.ListAttachments(context.Background(), "300")
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 attachments across two pages, got %d", len(got))
	}
	if got[2].ID != "a3" {
		t.Errorf("attachment[2].ID = %q, want a3", got[2].ID)
	}
}

// TestSearchEmptyPageStopsCursor verifies that an empty results page with a
// populated _links.next returns "" as the next cursor, so an external caller
// does not loop forever on a non-advancing offset.
func TestSearchEmptyPageStopsCursor(t *testing.T) {
	const body = `{
		"results": [],
		"size": 0,
		"_links": {"next": "/rest/api/search?start=25", "base": "https://example"}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	out, next, err := cf.Search(context.Background(), "type=page", 25, "25")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected 0 results, got %d", len(out))
	}
	if next != "" {
		t.Fatalf("expected empty next cursor on empty page, got %q", next)
	}
}

// TestSearchNonEmptyAdvancesCursor is a sanity check that a populated page with
// _links.next still advances the cursor.
func TestSearchNonEmptyAdvancesCursor(t *testing.T) {
	const body = `{
		"results": [
			{"content": {"id": "1", "title": "A"}},
			{"content": {"id": "2", "title": "B"}}
		],
		"size": 2,
		"_links": {"next": "/rest/api/search?start=12", "base": "https://example"}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	cf := &Confluence{c: newTestClient(srv.URL), base: srv.URL}
	_, next, err := cf.Search(context.Background(), "type=page", 25, "10")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if next != "12" {
		t.Fatalf("expected next cursor 12 (start 10 + 2 results), got %q", next)
	}
}
