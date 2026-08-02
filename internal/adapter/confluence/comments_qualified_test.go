package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
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
			// Some Data Center versions keep the semantic response location
			// inline while reporting the resolved state separately.
			_, _ = w.Write([]byte(qualifiedCommentPage(qualifiedCommentJSON("30", "inline", "resolved", `[{"id":"1","type":"page"}]`, "<p>resolved</p>"))))
		}
	}))
	defer srv.Close()
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1, DepthAll: true})
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

func TestListConfluenceCommentsResolvedSelectorRequiresExplicitResolvedState(t *testing.T) {
	for _, test := range []struct {
		name       string
		resolution string
	}{
		{name: "open", resolution: "open"},
		{name: "reopened", resolution: "reopened"},
		{name: "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(qualifiedCommentPage(qualifiedCommentJSON("30", "inline", test.resolution, `[{"id":"1","type":"page"}]`, "<p>x</p>"))))
			}))
			defer srv.Close()

			inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{
				ParentVersion: 1,
				Locations:     []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorResolved},
			})
			if err != nil {
				t.Fatal(err)
			}
			if inventory.CommentsComplete || inventory.Capabilities.Resolved != domain.ConfluenceCapabilityUnknown ||
				!containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialLocationUnavailable) {
				t.Fatalf("resolved selector accepted %s state: %+v", test.name, inventory)
			}
		})
	}
}

func TestListConfluenceCommentsMapsExplicitReopenedStateToOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualifiedCommentPage(qualifiedCommentJSON("20", "inline", "reopened", `[{"id":"1","type":"page"}]`, "<p>x</p>"))))
	}))
	defer srv.Close()

	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{
		ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorInline}, DepthAll: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.CommentsComplete || len(inventory.PartialReasons) != 0 || len(inventory.Diagnostics) != 0 ||
		len(inventory.Comments) != 1 || inventory.Comments[0].Resolution != domain.ConfluenceCommentResolutionOpen ||
		inventory.Capabilities.Resolution != domain.ConfluenceCapabilityObserved {
		t.Fatalf("inventory=%+v", inventory)
	}
}

func TestDecodeCommentResolutionRejectsUnknownWireStates(t *testing.T) {
	for _, status := range []string{"", "unknown", "closed", "OPEN", "reopened "} {
		raw := json.RawMessage(`{"status":` + strconv.Quote(status) + `}`)
		if resolution, present, ok := decodeCommentResolution(raw); !present || ok || resolution != domain.ConfluenceCommentResolutionUnknown {
			t.Errorf("status=%q resolution=%q present=%v ok=%v", status, resolution, present, ok)
		}
	}
	if resolution, present, ok := decodeCommentResolution(nil); present || ok || resolution != domain.ConfluenceCommentResolutionUnknown {
		t.Errorf("absent resolution=%q present=%v ok=%v", resolution, present, ok)
	}
	for _, raw := range []json.RawMessage{json.RawMessage("null"), json.RawMessage("[]"), json.RawMessage(`{"status":3}`)} {
		if resolution, present, ok := decodeCommentResolution(raw); !present || ok || resolution != domain.ConfluenceCommentResolutionUnknown {
			t.Errorf("raw=%s resolution=%q present=%v ok=%v", raw, resolution, present, ok)
		}
	}
}

func TestListConfluenceCommentsRejectsExplicitUnknownStateAtResolvedLocation(t *testing.T) {
	row := qualifiedCommentJSON("30", "resolved", "future-state", `[{"id":"1","type":"page"}]`, "<p>x</p>")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualifiedCommentPage(row)))
	}))
	defer srv.Close()

	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{
		ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorResolved}, DepthAll: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.CommentsComplete || len(inventory.Comments) != 1 ||
		inventory.Comments[0].Resolution != domain.ConfluenceCommentResolutionUnknown ||
		!containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialResolutionUnavailable) ||
		inventory.Capabilities.Resolution != domain.ConfluenceCapabilityUnknown {
		t.Fatalf("inventory=%+v", inventory)
	}
}

func TestListConfluenceCommentsQualifiesInlineReplyWithoutRootAnchorProperties(t *testing.T) {
	root := qualifiedCommentJSON("20", "inline", "open", `[{"id":"1","type":"page"}]`, "<p>root</p>")
	reply := qualifiedCommentJSON("21", "inline", "open", `[{"id":"1","type":"page"},{"id":"20","type":"comment"}]`, "<p>reply</p>")
	reply = strings.Replace(reply, `,"inlineProperties":{"markerRef":"ref-21","originalSelection":"selection"}`, "", 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualifiedCommentPage(root, reply)))
	}))
	defer srv.Close()

	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{
		ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorInline}, DepthAll: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.CommentsComplete || !inventory.ThreadsComplete || len(inventory.PartialReasons) != 0 || len(inventory.Comments) != 2 ||
		inventory.Capabilities.InlineProperties != domain.ConfluenceCapabilityObserved {
		t.Fatalf("inventory = %+v", inventory)
	}
	byID := map[string]domain.ConfluenceCommentRecord{}
	for _, comment := range inventory.Comments {
		byID[comment.ID] = comment
	}
	if byID["20"].MarkerRef != "ref-20" || byID["21"].Relation != domain.ConfluenceCommentRelationReply ||
		byID["21"].ParentID == nil || *byID["21"].ParentID != "20" || byID["21"].MarkerRef != "" || byID["21"].OriginalSelection != "" {
		t.Fatalf("comments = %+v", byID)
	}
}

func TestListConfluenceCommentsSuppressesCopiedInlineReplyProperties(t *testing.T) {
	root := qualifiedCommentJSON("20", "inline", "open", `[{"id":"1","type":"page"}]`, "<p>root</p>")
	reply := qualifiedCommentJSON("21", "inline", "open", `[{"id":"1","type":"page"},{"id":"20","type":"comment"}]`, "<p>reply</p>")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualifiedCommentPage(root, reply)))
	}))
	defer srv.Close()

	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{
		ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorInline}, DepthAll: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Comments) != 2 || inventory.Comments[1].Relation != domain.ConfluenceCommentRelationReply ||
		inventory.Comments[1].MarkerRef != "" || inventory.Comments[1].OriginalSelection != "" || len(inventory.PartialReasons) != 0 {
		t.Fatalf("inventory = %+v", inventory)
	}
}

func TestListConfluenceCommentsReplyPropertyShapeDoesNotConflictDuplicate(t *testing.T) {
	requests := 0
	root := qualifiedCommentJSON("20", "inline", "open", `[{"id":"1","type":"page"}]`, "<p>root</p>")
	replyWithProperties := qualifiedCommentJSON("21", "inline", "open", `[{"id":"1","type":"page"},{"id":"20","type":"comment"}]`, "<p>reply</p>")
	replyWithoutProperties := strings.Replace(replyWithProperties, `,"inlineProperties":{"markerRef":"ref-21","originalSelection":"selection"}`, "", 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("start") == "0" {
			_, _ = fmt.Fprintf(w, `{"results":[%s,%s],"start":0,"limit":100,"size":2,"_links":{"next":"/ignored"}}`, root, replyWithProperties)
			return
		}
		_, _ = fmt.Fprintf(w, `{"results":[%s],"start":2,"limit":100,"size":1,"_links":{}}`, replyWithoutProperties)
	}))
	defer srv.Close()

	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{
		ParentVersion: 1,
		Locations:     []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorInline},
		DepthAll:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(inventory.Comments) != 2 || !inventory.CommentsComplete || !inventory.ThreadsComplete ||
		containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialConflictingDuplicates) || inventory.Comments[1].MarkerRef != "" {
		t.Fatalf("requests=%d inventory=%+v", requests, inventory)
	}
}

func TestListConfluenceCommentsDemotedInlineReplyWithoutPropertiesIsAnchorPartial(t *testing.T) {
	rootA := qualifiedCommentJSON("10", "inline", "open", `[{"id":"1","type":"page"}]`, "<p>a</p>")
	rootB := qualifiedCommentJSON("20", "inline", "open", `[{"id":"1","type":"page"}]`, "<p>b</p>")
	reply := qualifiedCommentJSON("30", "inline", "open", `[{"id":"1","type":"page"},{"id":"10","type":"comment"},{"id":"20","type":"comment"}]`, "<p>reply</p>")
	reply = strings.Replace(reply, `,"inlineProperties":{"markerRef":"ref-30","originalSelection":"selection"}`, "", 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualifiedCommentPage(rootA, rootB, reply)))
	}))
	defer srv.Close()

	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{
		ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorInline}, DepthAll: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.ConfluenceCommentRecord{}
	for _, comment := range inventory.Comments {
		byID[comment.ID] = comment
	}
	if byID["30"].Relation != domain.ConfluenceCommentRelationUnknown || inventory.ThreadsComplete ||
		!containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialMalformedAncestry) ||
		!containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialInlineExpansionUnavailable) {
		t.Fatalf("inventory = %+v", inventory)
	}
}

func TestListConfluenceCommentsDemotedInlineReplyPreservesUnambiguousProperties(t *testing.T) {
	rootA := qualifiedCommentJSON("10", "inline", "open", `[{"id":"1","type":"page"}]`, "<p>a</p>")
	rootB := qualifiedCommentJSON("20", "inline", "open", `[{"id":"1","type":"page"}]`, "<p>b</p>")
	reply := qualifiedCommentJSON("30", "inline", "open", `[{"id":"1","type":"page"},{"id":"10","type":"comment"},{"id":"20","type":"comment"}]`, "<p>reply</p>")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualifiedCommentPage(rootA, rootB, reply)))
	}))
	defer srv.Close()

	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{
		ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorInline}, DepthAll: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.ConfluenceCommentRecord{}
	for _, comment := range inventory.Comments {
		byID[comment.ID] = comment
	}
	if byID["30"].Relation != domain.ConfluenceCommentRelationUnknown || byID["30"].MarkerRef != "ref-30" ||
		byID["30"].OriginalSelection != "selection" ||
		containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialInlineExpansionUnavailable) {
		t.Fatalf("inventory = %+v", inventory)
	}
}

func TestListConfluenceCommentsDemotedReplyConflictingPrivatePropertiesIsAnchorPartial(t *testing.T) {
	rootA := qualifiedCommentJSON("10", "inline", "open", `[{"id":"1","type":"page"}]`, "<p>a</p>")
	rootB := qualifiedCommentJSON("20", "inline", "open", `[{"id":"1","type":"page"}]`, "<p>b</p>")
	reply := qualifiedCommentJSON("30", "inline", "open", `[{"id":"1","type":"page"},{"id":"10","type":"comment"},{"id":"20","type":"comment"}]`, "<p>reply</p>")
	conflictingReply := strings.Replace(reply, `"markerRef":"ref-30"`, `"markerRef":"different-ref"`, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("start") == "0" {
			_, _ = fmt.Fprintf(w, `{"results":[%s,%s,%s],"start":0,"limit":100,"size":3,"_links":{"next":"/ignored"}}`, rootA, rootB, reply)
			return
		}
		_, _ = fmt.Fprintf(w, `{"results":[%s],"start":3,"limit":100,"size":1,"_links":{}}`, conflictingReply)
	}))
	defer srv.Close()

	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{
		ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorInline}, DepthAll: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.ConfluenceCommentRecord{}
	for _, comment := range inventory.Comments {
		byID[comment.ID] = comment
	}
	if byID["30"].Relation != domain.ConfluenceCommentRelationUnknown || byID["30"].MarkerRef != "" ||
		containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialConflictingDuplicates) ||
		!containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialInlineExpansionUnavailable) ||
		inventory.Capabilities.InlineProperties != domain.ConfluenceCapabilityUnknown {
		t.Fatalf("inventory = %+v", inventory)
	}
}

func TestListConfluenceCommentsUnknownInlineRelationStillRequiresAnchorProperties(t *testing.T) {
	row := qualifiedCommentJSON("20", "inline", "open", `[{"id":"1","type":"page"}]`, "<p>x</p>")
	row = strings.Replace(row, `"ancestors":[{"id":"1","type":"page"}],`, "", 1)
	row = strings.Replace(row, `,"inlineProperties":{"markerRef":"ref-20","originalSelection":"selection"}`, "", 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualifiedCommentPage(row)))
	}))
	defer srv.Close()

	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{
		ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorInline}, DepthAll: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.ThreadsComplete || !containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialParentUnavailable) ||
		!containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialInlineExpansionUnavailable) {
		t.Fatalf("inventory = %+v", inventory)
	}
}

func TestListConfluenceCommentsBindsParentVersionAcrossSelectorsAndPagination(t *testing.T) {
	starts := map[string][]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selector := r.URL.Query().Get("location")
		if r.URL.Query().Get("parentVersion") != "37" {
			t.Errorf("parentVersion=%q for selector %q, want 37", r.URL.Query().Get("parentVersion"), selector)
		}
		starts[selector] = append(starts[selector], r.URL.Query().Get("start"))
		if r.URL.Query().Get("start") == "0" {
			id := map[string]string{"footer": "10", "inline": "20", "resolved": "30"}[selector]
			resolution := ""
			if selector == "resolved" {
				resolution = "resolved"
			}
			row := qualifiedCommentJSON(id, selector, resolution, `[{"id":"1","type":"page"}]`, "<p>x</p>")
			_, _ = fmt.Fprintf(w, `{"results":[%s],"start":0,"limit":100,"size":1,"_links":{"next":"/ignored"}}`, row)
			return
		}
		_, _ = w.Write([]byte(`{"results":[],"start":1,"limit":100,"size":0,"_links":{}}`))
	}))
	defer srv.Close()

	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 37, DepthAll: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{"footer", "inline", "resolved"} {
		if !reflect.DeepEqual(starts[selector], []string{"0", "1"}) {
			t.Errorf("selector %s starts=%v, want [0 1]", selector, starts[selector])
		}
	}
	if len(inventory.Comments) != 3 || !inventory.ThreadsComplete {
		t.Fatalf("inventory=%+v", inventory)
	}
}

func TestListConfluenceCommentsMapsExactFooterReadbackEvidence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(qualifiedCommentPage(qualifiedCommentJSON("10", "footer", "", `[{"id":"1","type":"page"}]`, "<p>footer</p>"))))
	}))
	defer srv.Close()

	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1,
		Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}, DepthAll: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Comments) != 1 || !inventory.CommentsComplete || !inventory.ThreadsComplete {
		t.Fatalf("inventory = %+v", inventory)
	}
	comment := inventory.Comments[0]
	if comment.ID != "10" || comment.PageID != "1" || comment.Location != domain.ConfluenceCommentLocationFooter ||
		comment.Relation != domain.ConfluenceCommentRelationRoot || comment.ParentID != nil || comment.RootID == nil || *comment.RootID != "10" ||
		comment.AuthorID != "u1" || comment.AuthorDisplayName != "User" || comment.BodyStorage != "<p>footer</p>" || comment.Body != "footer" ||
		comment.Version != 1 || comment.CreatedAt != "2026-01-01T00:00:00Z" || comment.UpdatedAt != "2026-01-02T00:00:00Z" {
		t.Fatalf("comment = %+v", comment)
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
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorInline}, DepthAll: true})
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
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}})
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
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorInline, domain.ConfluenceCommentSelectorResolved}})
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
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1})
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

func TestListConfluenceCommentsEnforcesExplicitAggregateBounds(t *testing.T) {
	t.Run("pages", func(t *testing.T) {
		requests := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			row := qualifiedCommentJSON(fmt.Sprintf("comment-%03d", requests), "footer", "", `[{"id":"1","type":"page"}]`, "<p>x</p>")
			_, _ = fmt.Fprintf(w, `{"results":[%s],"start":%s,"limit":100,"size":1,"_links":{"next":"/ignored"}}`, row, r.URL.Query().Get("start"))
		}))
		defer srv.Close()
		inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1,
			Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}, MaxPages: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if requests != 2 || len(inventory.Comments) != 2 || inventory.CommentsComplete || inventory.ThreadsComplete ||
			!containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialPageLimit) {
			t.Fatalf("requests=%d inventory=%+v", requests, inventory)
		}
	})

	t.Run("items", func(t *testing.T) {
		requests := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests++
			rows := []string{
				qualifiedCommentJSON("10", "footer", "", `[{"id":"1","type":"page"}]`, "<p>a</p>"),
				qualifiedCommentJSON("11", "footer", "", `[{"id":"1","type":"page"}]`, "<p>b</p>"),
				qualifiedCommentJSON("12", "footer", "", `[{"id":"1","type":"page"}]`, "<p>c</p>"),
			}
			_, _ = fmt.Fprintf(w, `{"results":[%s],"start":0,"limit":100,"size":3,"_links":{}}`, strings.Join(rows, ","))
		}))
		defer srv.Close()
		inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1,
			Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}, MaxItems: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if requests != 1 || len(inventory.Comments) != 2 || inventory.CommentsComplete || inventory.ThreadsComplete ||
			!containsString(inventory.PartialReasons, domain.ConfluenceCommentPartialItemLimit) {
			t.Fatalf("requests=%d inventory=%+v", requests, inventory)
		}
	})
}

func TestListConfluenceCommentsRejectsInvalidBoundsBeforeRequest(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer srv.Close()
	for _, options := range []domain.ConfluenceCommentReadOptions{
		{}, {ParentVersion: -1},
		{ParentVersion: 1, MaxPages: -1}, {ParentVersion: 1, MaxItems: -1},
	} {
		_, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", options)
		if !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("options=%+v error=%v, want ErrUsage", options, err)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid bounds made %d requests", requests)
	}
}

func TestListConfluenceCommentsPropagatesSentinelErrors(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{{http.StatusUnauthorized, domain.ErrAuth}, {http.StatusNotFound, domain.ErrNotFound}, {http.StatusForbidden, domain.ErrForbidden}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(test.status) }))
		_, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}})
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
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1})
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
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorInline}, DepthAll: true})
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
		inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}})
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
		_, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}})
		if !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("error = %v, want ErrCheckFailed", err)
		}
	})

	t.Run("missing pagination metadata", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"results":[],"_links":{}}`))
		}))
		defer srv.Close()
		inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}})
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
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}, DepthAll: true})
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
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}, DepthAll: true})
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
	inventory, err := (&Confluence{c: newTestClient(srv.URL), base: srv.URL}).ListConfluenceComments(context.Background(), "1", domain.ConfluenceCommentReadOptions{ParentVersion: 1, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}, DepthAll: true})
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
