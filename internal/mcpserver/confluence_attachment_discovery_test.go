package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

type recordingAttachmentDiscoveryReader struct {
	*recordingConfluenceReader
	result *app.ConfluenceAttachmentDiscoveryResult
	err    error
	opts   app.ConfluenceAttachmentDiscoveryOpts
	calls  int
}

func (r *recordingAttachmentDiscoveryReader) DiscoverAttachments(_ context.Context, opts app.ConfluenceAttachmentDiscoveryOpts) (*app.ConfluenceAttachmentDiscoveryResult, error) {
	r.opts = opts
	r.calls++
	return r.result, r.err
}

func attachmentDiscoveryMCPResult() *app.ConfluenceAttachmentDiscoveryResult {
	return &app.ConfluenceAttachmentDiscoveryResult{
		SchemaVersion: app.ConfluenceAttachmentDiscoverySchemaVersion,
		Qualification: app.ConfluenceAttachmentDiscoveryComplete, Complete: true,
		Consistency: domain.ConfluenceAttachmentDiscoveryConsistencyLiveUnproven,
		ScopeSHA256: strings.Repeat("a", 64), Count: 1,
		Bounds: app.ConfluenceAttachmentDiscoveryBounds{
			MaxItems: 2, MaxRequests: 3, MaxResponseBytes: 4096, DeadlineMillis: 1000,
			RequestsUsed: 1, ResponseBytesUsed: 256,
		},
		Attachments: []domain.ConfluenceAttachmentMetadata{{
			ID: "21", Title: "diagram.png", Type: "attachment", Version: 3,
			ContainerID: "10", ContainerType: "page", ContainerVersion: 7,
			Space: "DOC", MediaType: "image/png", FileSize: 42,
		}},
	}
}

func TestConfluenceAttachmentSearchProjectsSameBoundedAppDTO(t *testing.T) {
	reader := &recordingAttachmentDiscoveryReader{recordingConfluenceReader: &recordingConfluenceReader{}, result: attachmentDiscoveryMCPResult()}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	t.Cleanup(closeSessions)
	result := callToolOK(t, client, "confluence_attachment_search", map[string]any{
		"space": "DOC", "cql": "creator = currentUser()", "max_items": 2,
		"max_requests": 3, "max_response_bytes": 4096, "deadline_ms": 1000,
	})
	if reader.calls != 1 || reader.opts.Space != "DOC" || reader.opts.CQL != "creator = currentUser()" ||
		reader.opts.MaxItems != 2 || reader.opts.MaxRequests != 3 || reader.opts.MaxResponseBytes != 4096 || reader.opts.Deadline.Milliseconds() != 1000 {
		t.Fatalf("calls=%d opts=%+v", reader.calls, reader.opts)
	}
	content, _ := result.StructuredContent.(map[string]any)
	attachments, _ := content["attachments"].([]any)
	if content["qualification"] != "complete" || content["consistency"] != "live_unproven" || len(attachments) != 1 {
		t.Fatalf("content=%#v", content)
	}
	row, _ := attachments[0].(map[string]any)
	if row["id"] != "21" || row["container_id"] != "10" || row["container_version"] != float64(7) {
		t.Fatalf("row=%#v", row)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"http://", "https://", "download_path", "comment", "body"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("result leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestConfluenceAttachmentSearchRequiresEveryExecutionBound(t *testing.T) {
	reader := &recordingAttachmentDiscoveryReader{recordingConfluenceReader: &recordingConfluenceReader{}, result: attachmentDiscoveryMCPResult()}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	t.Cleanup(closeSessions)
	base := map[string]any{"max_items": 2, "max_requests": 3, "max_response_bytes": 4096, "deadline_ms": 1000}
	for _, missing := range []string{"max_items", "max_requests", "max_response_bytes", "deadline_ms"} {
		args := make(map[string]any, len(base)-1)
		for key, value := range base {
			if key != missing {
				args[key] = value
			}
		}
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "confluence_attachment_search", Arguments: args})
		if err != nil || result == nil || !result.IsError {
			t.Fatalf("missing=%s result=%+v err=%v", missing, result, err)
		}
	}
	if reader.calls != 0 {
		t.Fatalf("missing bound reached app %d times", reader.calls)
	}
}

func TestConfluenceAttachmentSearchPreservesFailedSnapshotAndToolFailure(t *testing.T) {
	const privateMarker = "https://private.example.test/synthetic"
	failed := attachmentDiscoveryMCPResult()
	failed.Qualification = app.ConfluenceAttachmentDiscoveryFailed
	failed.Complete = false
	failed.Reason = app.ConfluenceAttachmentDiscoveryReadFailed
	failed.Count = 0
	failed.TotalSize = nil
	failed.Attachments = []domain.ConfluenceAttachmentMetadata{}
	reader := &recordingAttachmentDiscoveryReader{
		recordingConfluenceReader: &recordingConfluenceReader{}, result: failed,
		err: errors.New(privateMarker),
	}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	t.Cleanup(closeSessions)
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "confluence_attachment_search", Arguments: map[string]any{
		"max_items": 2, "max_requests": 3, "max_response_bytes": 4096, "deadline_ms": 1000,
	}})
	if err != nil || result == nil || !result.IsError || result.StructuredContent == nil {
		t.Fatalf("result=%+v err=%v, want structured MCP tool failure", result, err)
	}
	content, _ := result.StructuredContent.(map[string]any)
	if content["qualification"] != app.ConfluenceAttachmentDiscoveryFailed || content["reason"] != app.ConfluenceAttachmentDiscoveryReadFailed {
		t.Fatalf("content=%#v", content)
	}
	if _, exists := content["next_cursor"]; exists {
		t.Fatalf("failed discovery advertised continuation: %#v", content)
	}
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), privateMarker) {
		t.Fatalf("MCP failure leaked backend detail: %s", encoded)
	}
}
