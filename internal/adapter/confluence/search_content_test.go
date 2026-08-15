package confluence

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchCompleteContentUsesQualifiedContentPagination(t *testing.T) {
	var starts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/content/search" {
			t.Fatalf("path=%q, want content search", r.URL.Path)
		}
		if r.URL.Query().Get("cql") != `space = "DOC" and type = page` {
			t.Fatalf("cql=%q", r.URL.Query().Get("cql"))
		}
		if r.URL.Query().Get("expand") != "ancestors,version,space" {
			t.Fatalf("expand=%q", r.URL.Query().Get("expand"))
		}
		starts = append(starts, r.URL.Query().Get("start"))
		w.Header().Set("Content-Type", "application/json")
		start := r.URL.Query().Get("start")
		if start == "0" {
			_, _ = w.Write(contentSearchPage(t, 0, []map[string]any{
				contentSearchRow("1", "One", "DOC"), contentSearchRow("2", "Two", "DOC"),
			}, 3, "/rest/api/content/search?start=2"))
			return
		}
		if start != "2" {
			t.Fatalf("unexpected start=%q", start)
		}
		_, _ = w.Write(contentSearchPage(t, 2, []map[string]any{contentSearchRow("3", "Three", "DOC")}, 3, ""))
	}))
	defer server.Close()

	adapter := &Confluence{c: newTestClient(server.URL), base: server.URL}
	first, err := adapter.SearchCompleteContent(context.Background(), `space = "DOC" and type = page`, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Complete || first.Next != "2" || first.ExactTotal == nil || *first.ExactTotal != 3 || len(first.Results) != 2 {
		t.Fatalf("first=%+v", first)
	}
	second, err := adapter.SearchCompleteContent(context.Background(), `space = "DOC" and type = page`, 100, first.Next)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Complete || second.Next != "" || second.ExactTotal == nil || *second.ExactTotal != 3 || len(second.Results) != 1 || second.Results[0].Parent != "" {
		t.Fatalf("second=%+v", second)
	}
	if strings.Join(starts, ",") != "0,2" {
		t.Fatalf("starts=%v", starts)
	}
}

func TestSearchCompleteContentRejectsUnreachableAdvertisedResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/content/search" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(contentSearchPage(t, 0, []map[string]any{contentSearchRow("1", "One", "DOC")}, 3, ""))
	}))
	defer server.Close()

	adapter := &Confluence{c: newTestClient(server.URL), base: server.URL}
	page, err := adapter.SearchCompleteContent(context.Background(), `space = "DOC"`, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Complete || page.Next != "" || !strings.Contains(page.PartialReason, "contradictory pagination totals") {
		t.Fatalf("page=%+v", page)
	}
}

func TestSearchCompleteContentRejectsInvalidPageIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(contentSearchPage(t, 0, []map[string]any{contentSearchRowWithType("1", "Folder", "DOC", "folder")}, 1, ""))
	}))
	defer server.Close()

	adapter := &Confluence{c: newTestClient(server.URL), base: server.URL}
	page, err := adapter.SearchCompleteContent(context.Background(), `space = "DOC"`, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Complete || !strings.Contains(page.PartialReason, "invalid page identity") {
		t.Fatalf("page=%+v", page)
	}
}

func TestSearchCompleteContentRejectsMissingSpaceIdentity(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"omitted": func(row map[string]any) { delete(row, "space") },
		"blank":   func(row map[string]any) { row["space"] = map[string]any{"key": " \t"} },
	} {
		t.Run(name, func(t *testing.T) {
			row := contentSearchRow("1", "One", "DOC")
			mutate(row)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(contentSearchPage(t, 0, []map[string]any{row}, 1, ""))
			}))
			defer server.Close()

			adapter := &Confluence{c: newTestClient(server.URL), base: server.URL}
			page, err := adapter.SearchCompleteContent(context.Background(), `type = page`, 100, "")
			if err != nil {
				t.Fatal(err)
			}
			if page.Complete || !strings.Contains(page.PartialReason, "invalid page identity") {
				t.Fatalf("page=%+v", page)
			}
		})
	}
}

func contentSearchRow(id, title, space string) map[string]any {
	return contentSearchRowWithType(id, title, space, "page")
}

func contentSearchRowWithType(id, title, space, contentType string) map[string]any {
	return map[string]any{
		"id": id, "type": contentType, "status": "current", "title": title,
		"space":     map[string]any{"key": space},
		"version":   map[string]any{"number": 1, "when": "2026-08-15T12:00:00Z"},
		"ancestors": []any{},
		"_links":    map[string]any{"webui": "/spaces/" + space + "/pages/" + id},
	}
}

func contentSearchPage(t *testing.T, start int, rows []map[string]any, total int, next string) []byte {
	t.Helper()
	body := map[string]any{
		"results": rows, "start": start, "limit": len(rows), "size": len(rows), "totalCount": total,
		"_links": map[string]any{"next": next},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
