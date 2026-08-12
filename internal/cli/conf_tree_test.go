package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func TestConfSpaceTreeEmitsQualifiedPhysicalBudgetUsage(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"1","title":"Root","space":{"key":"DOC"},"version":{"number":1}}],"start":0,"size":1,"_links":{"next":"ignored"}}`))
	}))
	t.Cleanup(srv.Close)
	out, stderr, code := runCLIFull(t, confEnv(srv), "conf", "space", "tree", "--space", "DOC",
		"--max-requests", "1", "--max-response-bytes", "1048576", "--deadline", "1s")
	if code != exitOK {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	var result app.ConfluenceTreeResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode tree: %v\n%s", err, out)
	}
	if result.Complete || !result.Truncated || result.PartialReason != domain.ConfluenceTreePartialRequestLimit ||
		result.Consistency != domain.ConfluenceTreeConsistencyLiveUnproven || result.Count != 1 {
		t.Fatalf("result=%+v", result)
	}
	if result.Bounds.RequestsUsed != 1 || result.Bounds.ResponseBytesUsed <= 0 || requests.Load() != 1 {
		t.Fatalf("bounds=%+v physical=%d", result.Bounds, requests.Load())
	}
	if stderr == "" {
		t.Fatal("partial tree omitted stderr warning")
	}
}

func TestConfSpaceTreeExplicitZeroBoundsFailBeforeConfiguration(t *testing.T) {
	for _, flag := range []string{"--max-items", "--max-scanned-items", "--max-requests", "--max-response-bytes", "--deadline"} {
		t.Run(flag, func(t *testing.T) {
			out, code := runCLI(t, nil, "conf", "space", "tree", "--space", "DOC", flag, "0")
			if code != exitUsage || out != "" {
				t.Fatalf("flag=%s exit=%d stdout=%q", flag, code, out)
			}
		})
	}
}

func TestConfSpaceTreeInvalidSelectionBoundsFailBeforeConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"--space", "   "},
		{"--space", "DOC", "--depth", "-1"},
		{"--space", "DOC", "--max-items", "2001"},
		{"--space", "DOC", "--max-scanned-items", "20001"},
		{"--space", "DOC", "--max-requests", "101"},
		{"--space", "DOC", "--max-response-bytes", "268435457"},
		{"--space", "DOC", "--deadline", "10m1ns"},
	} {
		commandArgs := append([]string{"conf", "space", "tree"}, args...)
		out, code := runCLI(t, nil, commandArgs...)
		if code != exitUsage || out != "" {
			t.Fatalf("args=%v exit=%d stdout=%q", args, code, out)
		}
	}
}
