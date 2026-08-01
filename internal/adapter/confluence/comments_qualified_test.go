package confluence

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func qualifiedCommentJSON(id, location, resolution, ancestors, body string) string {
	extensions := `"location":"` + location + `"`
	if resolution != "" {
		extensions += `,"resolution":{"status":"` + resolution + `"}`
	}
	if location == "inline" || location == "resolved" {
		extensions += `,"inlineProperties":{"markerRef":"ref-` + id + `","originalSelection":"selection"}`
	}
	return fmt.Sprintf(`{"id":%q,"type":"comment","status":"current","history":{"createdDate":"2026-01-01T00:00:00Z","createdBy":{"userKey":"u1","displayName":"User"}},"version":{"number":1,"when":"2026-01-02T00:00:00Z"},"ancestors":%s,"body":{"storage":{"value":%q,"representation":"storage"}},"extensions":{%s}}`, id, ancestors, body, extensions)
}

func qualifiedCommentPage(rows ...string) string {
	return `{"results":[` + strings.Join(rows, ",") + `],"start":0,"limit":100,"size":` + fmt.Sprint(len(rows)) + `,"_links":{}}`
}

func TestListConfluenceCommentsMapsExactShapesAndResolvedSemantics(t *testing.T) {
	seen := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		seen = append(seen, q.Get("location"))
		if r.URL.Path != "/rest/api/content/1/child/comment" || q.Get("depth") != "all" || q.Get("expand") != confluenceCommentExpand || q.Get("start") != "0" || q.Get("limit") != "100" {
			t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		switch q.Get("location") {
		case "footer":
			_, _ = w.Write([]byte(qualifiedCommentPage(qualifiedCommentJSON("10", "footer", "", `[{"id":"1","type":"page"}]`, "<p>footer</p>"))))
		case "inline":
			_, _ = w.Write([]byte(qualifiedCommentPage(qualifiedCommentJSON("20", "inline", "open", `[{"id":"1","type":"page"}]`, "<p>inline</p>"))))
		case "resolved":
			_, _ = w.Write([]byte(qualifiedCommentPage(qualifiedCommentJSON("30", "resolved", "resolved", `[{"id":"1","type":"page"}]`, "<p>resolved</p>"))))
		}
	}))
	defer srv.Close()
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{DepthAll: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, ",") != "footer,inline,resolved" || len(inventory.Comments) != 3 || !inventory.CommentsComplete || !inventory.ThreadsComplete {
		t.Fatalf("seen=%v inventory=%+v", seen, inventory)
	}
	if got := inventory.Comments[2]; got.ID != "30" || got.Location != domain.ConfluenceCommentLocationInline || got.Resolution != domain.ConfluenceCommentResolutionResolved {
		t.Fatalf("resolved selector projection = %+v", got)
	}
}

func TestListConfluenceCommentsDoesNotInferMissingLocationOrAncestry(t *testing.T) {
	row := qualifiedCommentJSON("20", "inline", "open", `[{"id":"1","type":"page"}]`, "<p>x</p>")
	row = strings.Replace(row, `"ancestors":[{"id":"1","type":"page"}],`, "", 1)
	row = strings.Replace(row, `"location":"inline",`, "", 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualifiedCommentPage(row)))
	}))
	defer srv.Close()
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorInline}, DepthAll: true})
	if err != nil {
		t.Fatal(err)
	}
	got := inventory.Comments[0]
	if got.Location != domain.ConfluenceCommentLocationUnknown || got.Relation != domain.ConfluenceCommentRelationUnknown || inventory.CommentsComplete || inventory.ThreadsComplete {
		t.Fatalf("missing fields were inferred: %+v inventory=%+v", got, inventory)
	}
}

func TestListConfluenceCommentsSelectorMismatchIsPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualifiedCommentPage(qualifiedCommentJSON("20", "inline", "open", `[{"id":"1","type":"page"}]`, "<p>x</p>"))))
	}))
	defer srv.Close()
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.CommentsComplete || inventory.Capabilities.Footer != domain.ConfluenceCapabilityUnknown ||
		!containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialLocationUnavailable) {
		t.Fatalf("selector mismatch inventory = %+v", inventory)
	}
}

func TestListConfluenceCommentsConflictingDuplicateIsOmitted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := "<p>one</p>"
		if r.URL.Query().Get("location") == "resolved" {
			body = "<p>two</p>"
		}
		_, _ = w.Write([]byte(qualifiedCommentPage(qualifiedCommentJSON("20", "inline", "open", `[{"id":"1","type":"page"}]`, body))))
	}))
	defer srv.Close()
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorInline, domain.ConfluenceCommentSelectorResolved}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Comments) != 0 || inventory.CommentsComplete || !containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialConflictingDuplicates) {
		t.Fatalf("conflict inventory=%+v", inventory)
	}
}

func TestListConfluenceCommentsAggregatePageCapStopsLaterSelectors(t *testing.T) {
	requests := 0
	seenSelectors := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		selector := r.URL.Query().Get("location")
		seenSelectors[selector]++
		start := r.URL.Query().Get("start")
		row := qualifiedCommentJSON(fmt.Sprintf("comment-%03d", requests), selector, "", `[{"id":"1","type":"page"}]`, "<p>x</p>")
		_, _ = fmt.Fprintf(w, `{"results":[%s],"start":%s,"limit":100,"size":1,"_links":{"next":"/ignored"}}`, row, start)
	}))
	defer srv.Close()
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if requests != maxPages || seenSelectors["footer"] != maxPages || seenSelectors["inline"] != 0 || seenSelectors["resolved"] != 0 {
		t.Fatalf("requests=%d selectors=%v", requests, seenSelectors)
	}
	if len(inventory.Comments) != maxPages || inventory.CommentsComplete || !containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialPageLimit) {
		t.Fatalf("inventory=%+v", inventory)
	}
}

func TestListConfluenceCommentsPropagatesSentinelErrors(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{{http.StatusUnauthorized, domain.ErrAuth}, {http.StatusNotFound, domain.ErrNotFound}, {http.StatusForbidden, domain.ErrForbidden}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(test.status) }))
		_, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}})
		srv.Close()
		if !errors.Is(err, test.want) {
			t.Errorf("status %d error=%v want %v", test.status, err, test.want)
		}
	}
}

func TestListConfluenceCommentsReportsUnsupportedSelectorAsPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("location") == "inline" {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		_, _ = w.Write([]byte(qualifiedCommentPage()))
	}))
	defer srv.Close()
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.CommentsComplete || inventory.ThreadsComplete || inventory.Capabilities.Inline != domain.ConfluenceCapabilityUnsupported ||
		!containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialEndpointUnavailable) {
		t.Fatalf("unsupported selector inventory = %+v", inventory)
	}
}

func TestListConfluenceCommentsDoesNotCallGenericBadRequestUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorInline}, DepthAll: true})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Capabilities.Inline != domain.ConfluenceCapabilityUnknown || inventory.CommentsComplete ||
		!containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialEndpointUnavailable) {
		t.Fatalf("bad-request qualification = %+v", inventory)
	}
}

func TestListConfluenceCommentsPaginationStallAndMissingResultsFailClosed(t *testing.T) {
	t.Run("stalled", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"results":[],"start":0,"limit":100,"size":0,"_links":{"next":"/ignored"}}`))
		}))
		defer srv.Close()
		inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}})
		if err != nil {
			t.Fatal(err)
		}
		if inventory.CommentsComplete || !containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialPaginationStalled) {
			t.Fatalf("stalled inventory = %+v", inventory)
		}
	})

	t.Run("missing results", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"start":0,"limit":100,"size":0,"_links":{}}`))
		}))
		defer srv.Close()
		_, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}})
		if !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("error = %v, want ErrCheckFailed", err)
		}
	})

	t.Run("missing pagination metadata", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"results":[],"_links":{}}`))
		}))
		defer srv.Close()
		inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}})
		if err != nil {
			t.Fatal(err)
		}
		if inventory.CommentsComplete || !containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialPaginationUnqualified) {
			t.Fatalf("unqualified pagination inventory = %+v", inventory)
		}
	})
}

func TestListConfluenceCommentsProvesNestedReplyChain(t *testing.T) {
	root := qualifiedCommentJSON("10", "footer", "", `[{"id":"1","type":"page"}]`, "<p>root</p>")
	reply := qualifiedCommentJSON("11", "footer", "", `[{"id":"1","type":"page"},{"id":"10","type":"comment"}]`, "<p>reply</p>")
	nested := qualifiedCommentJSON("12", "footer", "", `[{"id":"1","type":"page"},{"id":"10","type":"comment"},{"id":"11","type":"comment"}]`, "<p>nested</p>")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualifiedCommentPage(nested, root, reply)))
	}))
	defer srv.Close()
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}, DepthAll: true})
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.ThreadsComplete || len(inventory.Comments) != 3 {
		t.Fatalf("inventory = %+v", inventory)
	}
	byID := map[string]domain.ConfluenceCommentRecord{}
	for _, comment := range inventory.Comments {
		byID[comment.ID] = comment
	}
	if byID["10"].Relation != domain.ConfluenceCommentRelationRoot || byID["11"].ParentID == nil || *byID["11"].ParentID != "10" ||
		byID["12"].ParentID == nil || *byID["12"].ParentID != "11" || byID["12"].RootID == nil || *byID["12"].RootID != "10" {
		t.Fatalf("relationships = %+v", byID)
	}
}

func TestListConfluenceCommentsRejectsInconsistentReplyChain(t *testing.T) {
	rootA := qualifiedCommentJSON("10", "footer", "", `[{"id":"1","type":"page"}]`, "<p>a</p>")
	rootB := qualifiedCommentJSON("20", "footer", "", `[{"id":"1","type":"page"}]`, "<p>b</p>")
	reply := qualifiedCommentJSON("30", "footer", "", `[{"id":"1","type":"page"},{"id":"10","type":"comment"},{"id":"20","type":"comment"}]`, "<p>reply</p>")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualifiedCommentPage(rootA, rootB, reply)))
	}))
	defer srv.Close()
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}, DepthAll: true})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.ThreadsComplete || !containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialMalformedAncestry) {
		t.Fatalf("inconsistent ancestry inventory = %+v", inventory)
	}
	for _, comment := range inventory.Comments {
		if comment.ID == "30" && (comment.Relation != domain.ConfluenceCommentRelationUnknown || comment.ParentID != nil || comment.RootID != nil) {
			t.Fatalf("inconsistent reply stayed linked: %+v", comment)
		}
	}
}

func TestListConfluenceCommentsRejectsReplyCycle(t *testing.T) {
	root := qualifiedCommentJSON("10", "footer", "", `[{"id":"1","type":"page"}]`, "<p>root</p>")
	first := qualifiedCommentJSON("20", "footer", "", `[{"id":"1","type":"page"},{"id":"10","type":"comment"},{"id":"30","type":"comment"}]`, "<p>first</p>")
	second := qualifiedCommentJSON("30", "footer", "", `[{"id":"1","type":"page"},{"id":"10","type":"comment"},{"id":"20","type":"comment"}]`, "<p>second</p>")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualifiedCommentPage(root, first, second)))
	}))
	defer srv.Close()
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}, DepthAll: true})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.ThreadsComplete || !containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialMalformedAncestry) {
		t.Fatalf("cycle inventory = %+v", inventory)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
