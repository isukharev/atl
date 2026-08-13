package confluence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func qualifiedNestedPage(results, next string) string {
	return fmt.Sprintf(`{"results":%s,"start":0,"limit":100,"size":%d,"_links":{%s}}`,
		results, strings.Count(results, `"name"`)+strings.Count(results, `"principal"`), next)
}

func qualifiedMetadataRow(id, title, ancestors, labels, users, groups, webUI string) string {
	return fmt.Sprintf(`{"id":%q,"type":"page","title":%q,"space":{"key":"DOC"},"version":{"number":2,"when":"2026-08-13T12:00:00Z"},"ancestors":%s,"metadata":{"labels":%s},"restrictions":{"read":{"restrictions":{"user":%s,"group":%s}}},"_links":{"webui":%q}}`,
		id, title, ancestors, labels, users, groups, webUI)
}

func qualifiedMetadataPage(results string, start, limit, size int, next string) string {
	return fmt.Sprintf(`{"results":[%s],"start":%d,"limit":%d,"size":%d,"_links":{%s}}`, results, start, limit, size, next)
}

func qualifiedMetadataPageWithTotal(results string, start, limit, size, total int, next string) string {
	return fmt.Sprintf(`{"results":[%s],"start":%d,"limit":%d,"size":%d,"totalSize":%d,"_links":{%s}}`, results, start, limit, size, total, next)
}

func emptyQualifiedNestedPage() string { return qualifiedNestedPage(`[]`, "") }

func TestReadConfluenceCorpusMetadataReturnsExactNonBodyInventory(t *testing.T) {
	labelsRoot := qualifiedNestedPage(`[{"name":"zeta"},{"name":"alpha"}]`, "")
	root := qualifiedMetadataRow("1", "Root", `[]`, labelsRoot, emptyQualifiedNestedPage(), emptyQualifiedNestedPage(), "/pages/1")
	child := qualifiedMetadataRow("2", "Child", `[{"id":"1","type":"page","title":"Root"}]`, emptyQualifiedNestedPage(), emptyQualifiedNestedPage(), qualifiedNestedPage(`[{"principal":"opaque"}]`, `"next":"/ignored"`), "/pages/2")
	body := qualifiedMetadataPage(root+","+child, 0, 100, 2, "")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodGet || request.URL.Path != "/rest/api/content/search" {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		if got := request.URL.Query().Get("cql"); got != `space="DOC" and type=page` {
			t.Errorf("cql=%q", got)
		}
		if expand := request.URL.Query().Get("expand"); expand != confluenceCorpusMetadataExpand || strings.Contains(expand, "body") {
			t.Errorf("expand=%q", expand)
		}
		_, _ = io.WriteString(writer, body)
	}))
	defer server.Close()

	inventory, err := (&Confluence{c: newTestClient(server.URL), base: server.URL}).ReadConfluenceCorpusMetadata(context.Background(), "DOC", 10)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || !inventory.Complete || len(inventory.Rows) != 2 {
		t.Fatalf("requests=%d inventory=%+v", requests, inventory)
	}
	if got := inventory.Rows[0]; got.ID != "1" || got.Type != "page" || got.Title != "Root" || got.Space != "DOC" || got.Version != 2 ||
		got.Updated != "2026-08-13T12:00:00Z" || got.Parent != "" || len(got.Ancestors) != 0 ||
		len(got.Labels) != 2 || got.Labels[0] != "alpha" || got.Labels[1] != "zeta" || got.Restricted || got.URL != server.URL+"/pages/1" {
		t.Fatalf("root row=%+v", got)
	}
	if got := inventory.Rows[1]; got.Parent != "1" || len(got.AncestorIDs) != 1 || got.AncestorIDs[0] != "1" ||
		len(got.Ancestors) != 1 || got.Ancestors[0] != "Root" || !got.Restricted || got.URL != server.URL+"/pages/2" {
		t.Fatalf("child row=%+v", got)
	}
}

func TestReadConfluenceCorpusMetadataCapReturnsIncomplete(t *testing.T) {
	row := qualifiedMetadataRow("1", "Root", `[]`, emptyQualifiedNestedPage(), emptyQualifiedNestedPage(), emptyQualifiedNestedPage(), "/pages/1")
	body := qualifiedMetadataPage(row, 0, 1, 1, `"next":"/ignored"`)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = io.WriteString(writer, body)
	}))
	defer server.Close()
	inventory, err := (&Confluence{c: newTestClient(server.URL), base: server.URL}).ReadConfluenceCorpusMetadata(context.Background(), "DOC", 1)
	if err != nil || inventory.Complete || len(inventory.Rows) != 1 || requests != 1 {
		t.Fatalf("inventory=%+v requests=%d error=%v", inventory, requests, err)
	}
}

func TestReadConfluenceCorpusMetadataQualifiesFullTerminalPageWithOneProbe(t *testing.T) {
	row := qualifiedMetadataRow("1", "Root", `[]`, emptyQualifiedNestedPage(), emptyQualifiedNestedPage(), emptyQualifiedNestedPage(), "/pages/1")
	for _, test := range []struct {
		name         string
		probeRows    string
		probeSize    int
		wantComplete bool
	}{
		{name: "empty terminal probe", probeSize: 0, wantComplete: true},
		{name: "cap overflow detected", probeRows: row, probeSize: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests++
				if request.URL.Query().Get("start") == "0" {
					_, _ = io.WriteString(writer, qualifiedMetadataPage(row, 0, 1, 1, ""))
					return
				}
				_, _ = io.WriteString(writer, qualifiedMetadataPage(test.probeRows, 1, 1, test.probeSize, ""))
			}))
			defer server.Close()
			inventory, err := (&Confluence{c: newTestClient(server.URL), base: server.URL}).ReadConfluenceCorpusMetadata(context.Background(), "DOC", 1)
			if requests != 2 || err != nil || inventory.Complete != test.wantComplete || len(inventory.Rows) != 1 {
				t.Fatalf("requests=%d inventory=%+v error=%v", requests, inventory, err)
			}
		})
	}
}

func TestReadConfluenceCorpusMetadataNonemptyRestrictionProvesRestricted(t *testing.T) {
	row := qualifiedMetadataRow("1", "Root", `[]`, emptyQualifiedNestedPage(), qualifiedNestedPage(`[{"principal":"opaque"}]`, `"next":"/ignored"`), `null`, "/pages/1")
	body := qualifiedMetadataPage(row, 0, 100, 1, "")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, body)
	}))
	defer server.Close()
	inventory, err := (&Confluence{c: newTestClient(server.URL), base: server.URL}).ReadConfluenceCorpusMetadata(context.Background(), "DOC", 1)
	if err != nil || !inventory.Complete || len(inventory.Rows) != 1 || !inventory.Rows[0].Restricted {
		t.Fatalf("inventory=%+v error=%v", inventory, err)
	}
}

func TestReadConfluenceCorpusMetadataRejectsTotalDriftAcrossPages(t *testing.T) {
	empty := emptyQualifiedNestedPage()
	root := qualifiedMetadataRow("1", "Root", `[]`, empty, empty, empty, "/pages/1")
	child := qualifiedMetadataRow("2", "Child", `[{"id":"1","type":"page","title":"Root"}]`, empty, empty, empty, "/pages/2")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("start") == "0" {
			_, _ = io.WriteString(writer, qualifiedMetadataPageWithTotal(root, 0, 100, 1, 2, `"next":"/ignored"`))
			return
		}
		_, _ = io.WriteString(writer, qualifiedMetadataPageWithTotal(child, 1, 100, 1, 3, ""))
	}))
	defer server.Close()
	_, err := (&Confluence{c: newTestClient(server.URL), base: server.URL}).ReadConfluenceCorpusMetadata(context.Background(), "DOC", 10)
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error=%v", err)
	}
}

func TestReadConfluenceCorpusMetadataRejectsUnqualifiedEvidenceContentFreely(t *testing.T) {
	labels := qualifiedNestedPage(`[{"name":"private-label-canary"}]`, "")
	row := qualifiedMetadataRow("1", "private-title-canary", `[]`, labels, emptyQualifiedNestedPage(), emptyQualifiedNestedPage(), "/private-path-canary")
	good := qualifiedMetadataPage(row, 0, 100, 1, "")
	duplicateLabels := qualifiedNestedPage(`[{"name":"private-label-canary"},{"name":"private-label-canary"}]`, "")
	ambiguousLabels := strings.Replace(labels, `"limit":100`, `"limit":1`, 1)
	nonemptyRestriction := qualifiedNestedPage(`[{"principal":"opaque"}]`, "")
	restrictedRow := qualifiedMetadataRow("1", "private-title-canary", `[]`, emptyQualifiedNestedPage(), nonemptyRestriction, emptyQualifiedNestedPage(), "/private-path-canary")
	malformedNonemptyRestriction := strings.Replace(nonemptyRestriction, `"size":1`, `"size":2`, 1)
	malformedEmptyRestriction := strings.Replace(emptyQualifiedNestedPage(), `"_links":{}`, `"_links":{"next":"/more"}`, 1)
	tests := map[string]string{
		"outer pagination omitted":       strings.Replace(good, `,"start":0`, "", 1),
		"outer size contradicts":         strings.Replace(good, `,"size":1`, `,"size":2`, 1),
		"outer total contradicts":        strings.Replace(good, `}],"start":0,"limit":100,"size":1,"_links":`, `}],"start":0,"limit":100,"size":1,"totalSize":2,"_links":`, 1),
		"ancestors omitted":              strings.Replace(good, `"ancestors":[]`, `"ancestors":null`, 1),
		"unexpected type":                strings.Replace(good, `"type":"page"`, `"type":"blogpost"`, 1),
		"unexpected space":               strings.Replace(good, `"key":"DOC"`, `"key":"OTHER"`, 1),
		"unsafe identity":                strings.Replace(good, `"id":"1"`, `"id":"01"`, 1),
		"unsafe update":                  strings.Replace(good, `2026-08-13T12:00:00Z`, `not-a-time`, 1),
		"unexpected body":                strings.Replace(good, `"version":`, `"body":{"storage":{"value":"private-body-canary"}},"version":`, 1),
		"labels omitted":                 strings.Replace(good, `"metadata":{"labels":`, `"metadata":{"other":`, 1),
		"labels clipped":                 strings.Replace(good, `"size":1,"_links":{}`, `"size":1,"_links":{"next":"/more"}`, 1),
		"labels exact cap without total": strings.Replace(good, labels, ambiguousLabels, 1),
		"labels total contradicts":       strings.Replace(good, `"size":1,"_links":{}`, `"size":1,"totalSize":2,"_links":{}`, 1),
		"labels duplicated":              strings.Replace(good, labels, duplicateLabels, 1),
		"restrictions omitted":           strings.Replace(good, `"restrictions":{"read":`, `"other":{"read":`, 1),
		"empty restriction clipped": strings.Replace(good, `"results":[],"start":0,"limit":100,"size":0,"_links":{}`,
			`"results":[],"start":0,"limit":100,"size":0,"_links":{"next":"/more"}`, 1),
		"nonempty restriction malformed":         qualifiedMetadataPage(strings.Replace(restrictedRow, nonemptyRestriction, malformedNonemptyRestriction, 1), 0, 100, 1, ""),
		"nonempty restriction malformed sibling": qualifiedMetadataPage(qualifiedMetadataRow("1", "private-title-canary", `[]`, emptyQualifiedNestedPage(), nonemptyRestriction, malformedEmptyRestriction, "/private-path-canary"), 0, 100, 1, ""),
		"webui omitted":                          strings.Replace(good, `"_links":{"webui":"/private-path-canary"}`, `"_links":{}`, 1),
		"foreign webui":                          strings.Replace(good, `/private-path-canary`, `https://foreign.example.invalid/private-path-canary`, 1),
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(writer, response)
			}))
			defer server.Close()
			_, err := (&Confluence{c: newTestClient(server.URL), base: server.URL}).ReadConfluenceCorpusMetadata(context.Background(), "DOC", 10)
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error=%v, want ErrCheckFailed", err)
			}
			for _, sensitive := range []string{"private-title-canary", "private-label-canary", "private-body-canary", "private-path-canary", "foreign.example.invalid"} {
				if strings.Contains(err.Error(), sensitive) {
					t.Fatalf("error leaked backend content: %v", err)
				}
			}
		})
	}
}

func TestReadConfluenceCorpusMetadataRejectsDuplicatesAndBrokenHierarchy(t *testing.T) {
	empty := emptyQualifiedNestedPage()
	root := qualifiedMetadataRow("1", "Root", `[]`, empty, empty, empty, "/pages/1")
	brokenChild := qualifiedMetadataRow("2", "Child", `[{"id":"1","type":"page","title":"Wrong"}]`, empty, empty, empty, "/pages/2")
	tests := map[string]string{
		"duplicate row":    qualifiedMetadataPage(root+","+root, 0, 100, 2, ""),
		"broken hierarchy": qualifiedMetadataPage(root+","+brokenChild, 0, 100, 2, ""),
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(writer, response) }))
			defer server.Close()
			_, err := (&Confluence{c: newTestClient(server.URL), base: server.URL}).ReadConfluenceCorpusMetadata(context.Background(), "DOC", 10)
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestGetPageRetainsOrdinaryResourceShapeWithStrictMetadataDTO(t *testing.T) {
	row := qualifiedMetadataRow("1", "Root", `[]`, emptyQualifiedNestedPage(), emptyQualifiedNestedPage(), emptyQualifiedNestedPage(), "/pages/1")
	row = strings.Replace(row, `"version":`, `"body":{"storage":{"value":"<p>native</p>"}},"version":`, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(writer, row) }))
	defer server.Close()
	page, err := (&Confluence{c: newTestClient(server.URL), base: server.URL}).GetPage(context.Background(), "1", domain.PullOpts{IncludeRestrictions: true})
	if err != nil {
		t.Fatal(err)
	}
	if !page.BodyPresent || !page.AncestorsPresent || page.Restricted == nil || *page.Restricted || page.URL != server.URL+"/pages/1" {
		t.Fatalf("page=%+v", page)
	}
}
