package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestPlanPageMetadataBatchesBoundsCountAndEncodedSelectorBytes(t *testing.T) {
	cf := &Confluence{}
	shortIDs := make([]string, 101)
	for i := range shortIDs {
		shortIDs[i] = string(rune('a' + i%26))
	}
	shortBatches, err := cf.PlanPageMetadataBatches(shortIDs)
	if err != nil || len(shortBatches) != 2 || len(shortBatches[0]) != 100 || len(shortBatches[1]) != 1 {
		t.Fatalf("short batches=%v err=%v", batchLengths(shortBatches), err)
	}

	ids := make([]string, 101)
	for i := range ids {
		ids[i] = strings.Repeat(`x"\`, 70)
	}
	batches, err := cf.PlanPageMetadataBatches(ids)
	if err != nil {
		t.Fatal(err)
	}
	var flattened []string
	for _, batch := range batches {
		if len(batch) == 0 || len(batch) > confluencePageMetadataBatchMaxIDs {
			t.Fatalf("batch size=%d", len(batch))
		}
		parts := make([]string, len(batch))
		for i, id := range batch {
			parts[i] = cqlQuote(id)
		}
		if got := len(strings.Join(parts, ",")); got > confluencePageMetadataSelectorMaxBytes {
			t.Fatalf("encoded selector bytes=%d", got)
		}
		flattened = append(flattened, batch...)
	}
	if !reflect.DeepEqual(flattened, ids) {
		t.Fatal("batch plan did not preserve input order")
	}
	if _, err := cf.PlanPageMetadataBatches([]string{strings.Repeat(`\`, confluencePageMetadataSelectorMaxBytes)}); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("oversized selector error=%v", err)
	}
}

func batchLengths(batches [][]string) []int {
	out := make([]int, len(batches))
	for i := range batches {
		out[i] = len(batches[i])
	}
	return out
}

func TestReadPageMetadataBatchBuildsEscapedQualifiedQueryOnce(t *testing.T) {
	ids := []string{"123", `x" or type=blogpost`, `a\b`}
	wantCQL := `type=page and id in ("123","x\" or type=blogpost","a\\b")`
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		if got := request.URL.Query().Get("cql"); got != wantCQL {
			t.Errorf("cql=%q want %q", got, wantCQL)
		}
		if got := request.URL.Query().Get("limit"); got != "3" {
			t.Errorf("limit=%q", got)
		}
		response := map[string]any{
			"results": []any{
				map[string]any{"content": map[string]any{"id": ids[2], "type": "page", "version": map[string]any{"number": 3}}},
				map[string]any{"content": map[string]any{"id": ids[0], "type": "page", "version": map[string]any{"number": 1}}},
				map[string]any{"content": map[string]any{"id": ids[1], "type": "page", "version": map[string]any{"number": 2}}},
			},
			"size": 3, "totalCount": 3, "_links": map[string]any{},
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	got, err := New(server.URL, "token", "test").ReadPageMetadataBatch(context.Background(), ids)
	if err != nil || !got.Complete || got.PartialReason != "" || len(got.Results) != 3 || requests != 1 {
		t.Fatalf("requests=%d batch=%+v err=%v", requests, got, err)
	}
}

func TestReadPageMetadataBatchMapsUnqualifiedPaginationToStaticReason(t *testing.T) {
	responses := map[string]string{
		"unreachable advertised result":         `{"results":[{"content":{"id":"1","version":{"number":1}}}],"size":1,"totalCount":2,"_links":{}}`,
		"continuation requires another request": `{"results":[{"content":{"id":"1","version":{"number":1}}}],"size":1,"totalCount":2,"_links":{"next":"/rest/api/search?start=1"}}`,
	}
	for name, response := range responses {
		t.Run(name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()
			got, err := New(server.URL, "token", "test").ReadPageMetadataBatch(context.Background(), []string{"1", "2"})
			if err != nil || got.Complete || got.PartialReason != domain.ConfluencePageMetadataPartialPaginationUnqualified || requests != 1 {
				t.Fatalf("requests=%d batch=%+v err=%v", requests, got, err)
			}
		})
	}
}

func TestReadPageMetadataBatchRejectsInputsOutsideAdapterBoundsBeforeNetwork(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	cf := New(server.URL, "token", "test")
	tooMany := make([]string, confluencePageMetadataBatchMaxIDs+1)
	if _, err := cf.ReadPageMetadataBatch(context.Background(), tooMany); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("too-many error=%v", err)
	}
	if _, err := cf.ReadPageMetadataBatch(context.Background(), []string{strings.Repeat(`\`, confluencePageMetadataSelectorMaxBytes)}); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("oversized error=%v", err)
	}
	if requests != 0 {
		t.Fatalf("requests=%d", requests)
	}
}
