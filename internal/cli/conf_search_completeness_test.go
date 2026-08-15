package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ambiguousSearchPartialReason = "backend returned a full search page without terminal pagination evidence"

func TestConfSearchPreservesAmbiguousTerminalEvidence(t *testing.T) {
	server, requests := ambiguousSearchServer(t, 25)
	t.Cleanup(server.Close)
	out, code := runCLI(t, confEnv(server), "--read-only", "conf", "search", "--cql", "type=page", "--limit", "25")
	if code != exitOK {
		t.Fatalf("exit=%d output=%s", code, out)
	}
	var result struct {
		Count         int     `json:"count"`
		Complete      bool    `json:"complete"`
		Truncated     bool    `json:"truncated"`
		PartialReason string  `json:"partial_reason"`
		NextCursor    *string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 25 || result.Complete || !result.Truncated || result.PartialReason != ambiguousSearchPartialReason || result.NextCursor != nil {
		t.Fatalf("result=%+v", result)
	}
	if len(*requests) != 1 || !strings.HasPrefix((*requests)[0], http.MethodGet+" /rest/api/search?") {
		t.Fatalf("requests=%v", *requests)
	}
}

func TestConfSearchTextExplainsUnresumablePartialPage(t *testing.T) {
	server, _ := ambiguousSearchServer(t, 25)
	t.Cleanup(server.Close)
	out, code := runCLI(t, confEnv(server), "--read-only", "conf", "search", "--cql", "type=page", "--limit", "25", "-o", "text")
	if code != exitOK || !strings.Contains(out, "complete: false; rows: 25") ||
		!strings.Contains(out, "no safe continuation cursor; narrow the query or investigate terminal pagination evidence") {
		t.Fatalf("exit=%d output=%q", code, out)
	}
}

func TestConfCompletePullRejectsAmbiguousTerminalBeforeBodiesOrCheckpoint(t *testing.T) {
	server, requests := ambiguousSearchServer(t, 100)
	t.Cleanup(server.Close)
	root := t.TempDir()
	stdout, stderr, code := runCLIFull(t, confEnv(server), "--read-only", "conf", "pull", "--complete", "--cql", "type=page", "--into", root)
	if code != exitCheckFailed || stdout != "" || stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if len(*requests) != 1 || !strings.HasPrefix((*requests)[0], http.MethodGet+" /rest/api/content/search?") {
		t.Fatalf("complete pull requests=%v, want one search and no body read", *requests)
	}
	checkpointRoot := filepath.Join(root, ".atl", "complete-pulls")
	entries, err := os.ReadDir(checkpointRoot)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed selection created checkpoint state: %v", entries)
	}
}

func ambiguousSearchServer(t *testing.T, rows int) (*httptest.Server, *[]string) {
	t.Helper()
	results := make([]map[string]any, rows)
	for index := range results {
		results[index] = map[string]any{
			"content": map[string]any{
				"id": fmt.Sprintf("synthetic-%03d", index+1), "title": "Synthetic page",
				"space": map[string]any{"key": "DOC"}, "version": map[string]any{"number": 1},
			},
		}
	}
	body, err := json.Marshal(map[string]any{
		"results": results,
		"size":    rows,
		"_links":  map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		if request.URL.Path == "/rest/api/search" {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write(body)
			return
		}
		if request.URL.Path != "/rest/api/content/search" {
			http.NotFound(writer, request)
			return
		}
		if request.URL.Query().Get("limit") != fmt.Sprint(rows) || request.URL.Query().Get("start") != "0" {
			t.Fatalf("unexpected search pagination: %s", request.URL.RawQuery)
		}
		if request.URL.Query().Get("expand") != "ancestors,version,space" {
			t.Fatalf("unexpected content expansion: %s", request.URL.RawQuery)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		results := decoded["results"].([]any)
		contentResults := make([]map[string]any, 0, len(results))
		for _, raw := range results {
			row := raw.(map[string]any)["content"].(map[string]any)
			row["ancestors"] = []any{}
			row["_links"] = map[string]any{"webui": "/spaces/DOC/pages/" + row["id"].(string)}
			contentResults = append(contentResults, row)
		}
		contentBody, err := json.Marshal(map[string]any{
			"results": contentResults, "start": 0, "limit": rows, "size": rows,
			"_links": map[string]any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(contentBody)
	}))
	return server, &requests
}
