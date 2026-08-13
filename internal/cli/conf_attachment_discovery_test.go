package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

const cliAttachmentDiscoveryRow = `{"id":"21","title":"diagram.png","type":"attachment","version":{"number":3},"container":{"id":"10","type":"page","version":{"number":7}},"space":{"key":"DOC"},"metadata":{"mediaType":"image/png"},"extensions":{"fileSize":42}}`

func attachmentDiscoveryCLIArgs() []string {
	return []string{"conf", "attachment", "search", "--space", "DOC", "--max-items", "1", "--max-requests", "1", "--max-response-bytes", "1048576", "--deadline", "1s"}
}

func TestConfAttachmentSearchEmitsBoundedMetadataOnlyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/content/search" || r.URL.Query().Get("cql") != `type = attachment and space = "DOC"` {
			t.Fatalf("request=%q", r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[` + cliAttachmentDiscoveryRow + `],"start":0,"limit":1,"size":1,"totalCount":1,"_links":{}}`))
	}))
	t.Cleanup(srv.Close)
	out, stderr, code := runCLIFull(t, confEnv(srv), attachmentDiscoveryCLIArgs()...)
	if code != exitOK || stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	var result app.ConfluenceAttachmentDiscoveryResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if result.Qualification != app.ConfluenceAttachmentDiscoveryComplete || !result.Complete || result.Count != 1 ||
		result.Consistency != domain.ConfluenceAttachmentDiscoveryConsistencyLiveUnproven || result.Attachments[0].ID != "21" ||
		strings.Contains(out, "download") || strings.Contains(out, "http") || strings.Contains(out, "comment") {
		t.Fatalf("result=%+v output=%q", result, out)
	}
}

func TestConfAttachmentSearchPartialTextCarriesLiveCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[` + cliAttachmentDiscoveryRow + `],"start":0,"limit":1,"size":1,"totalSize":2,"_links":{"next":"ignored"}}`))
	}))
	t.Cleanup(srv.Close)
	args := append(attachmentDiscoveryCLIArgs(), "-o", "text")
	out, stderr, code := runCLIFull(t, confEnv(srv), args...)
	if code != exitOK || stderr != "" || !strings.Contains(out, "qualification: partial complete=false consistency=live_unproven count=1 reason=item_limit") ||
		!strings.Contains(out, "next_cursor:") || !strings.Contains(out, "21\t\"diagram.png\"") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func TestConfAttachmentSearchFailedReadEmitsSnapshotBeforeNonzero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":`))
	}))
	t.Cleanup(srv.Close)
	out, _, code := runCLIFull(t, confEnv(srv), attachmentDiscoveryCLIArgs()...)
	if code == exitOK {
		t.Fatalf("unexpected success: %s", out)
	}
	var result app.ConfluenceAttachmentDiscoveryResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode failed snapshot: %v\n%s", err, out)
	}
	if result.Qualification != app.ConfluenceAttachmentDiscoveryFailed || result.Complete ||
		result.Reason != app.ConfluenceAttachmentDiscoveryReadFailed || result.NextCursor != "" || result.Count != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestConfAttachmentSearchRequiresAllCallerBoundsBeforeConfiguration(t *testing.T) {
	base := attachmentDiscoveryCLIArgs()
	for _, omitted := range []string{"--max-items", "--max-requests", "--max-response-bytes", "--deadline"} {
		args := make([]string, 0, len(base)-2)
		for i := 0; i < len(base); i++ {
			if base[i] == omitted {
				i++
				continue
			}
			args = append(args, base[i])
		}
		out, code := runCLI(t, nil, args...)
		if code != exitUsage || out != "" {
			t.Fatalf("omitted=%s exit=%d stdout=%q", omitted, code, out)
		}
	}
}
