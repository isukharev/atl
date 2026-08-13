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
	total := 1
	return &app.ConfluenceAttachmentDiscoveryResult{
		SchemaVersion: app.ConfluenceAttachmentDiscoverySchemaVersion,
		Qualification: app.ConfluenceAttachmentDiscoveryComplete, Complete: true,
		Consistency: domain.ConfluenceAttachmentDiscoveryConsistencyLiveUnproven,
		ScopeSHA256: strings.Repeat("a", 64), Count: 1, TotalSize: &total,
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

func TestConfluenceAttachmentSearchStrictInputIsPreBackend(t *testing.T) {
	reader := &recordingAttachmentDiscoveryReader{recordingConfluenceReader: &recordingConfluenceReader{}, result: attachmentDiscoveryMCPResult()}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Confluence: func() (ConfluenceReader, error) { return reader, nil },
	}))
	t.Cleanup(closeSessions)
	valid := map[string]any{"max_items": 2, "max_requests": 3, "max_response_bytes": 4096, "deadline_ms": 1000}
	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "unknown", args: map[string]any{"max_items": 2, "max_requests": 3, "max_response_bytes": 4096, "deadline_ms": 1000, "url": "not released"}},
		{name: "null required", args: map[string]any{"max_items": nil, "max_requests": 3, "max_response_bytes": 4096, "deadline_ms": 1000}},
		{name: "null optional", args: map[string]any{"space": nil, "max_items": 2, "max_requests": 3, "max_response_bytes": 4096, "deadline_ms": 1000}},
		{name: "zero item bound", args: map[string]any{"max_items": 0, "max_requests": 3, "max_response_bytes": 4096, "deadline_ms": 1000}},
		{name: "oversize request bound", args: map[string]any{"max_items": 2, "max_requests": 101, "max_response_bytes": 4096, "deadline_ms": 1000}},
		{name: "zero response bound", args: map[string]any{"max_items": 2, "max_requests": 3, "max_response_bytes": 0, "deadline_ms": 1000}},
		{name: "oversize deadline", args: map[string]any{"max_items": 2, "max_requests": 3, "max_response_bytes": 4096, "deadline_ms": 600001}},
		{name: "overflow deadline", args: map[string]any{"max_items": 2, "max_requests": 3, "max_response_bytes": 4096, "deadline_ms": int64(^uint64(0) >> 1)}},
		{name: "ordered CQL", args: map[string]any{"cql": "creator=currentUser() ORDER BY created", "max_items": 2, "max_requests": 3, "max_response_bytes": 4096, "deadline_ms": 1000}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "confluence_attachment_search", Arguments: test.args})
			if err != nil || result == nil || !result.IsError {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
	if reader.calls != 0 {
		t.Fatalf("invalid input reached app %d times", reader.calls)
	}
	result := callToolOK(t, client, "confluence_attachment_search", valid)
	if result.StructuredContent == nil || reader.calls != 1 {
		t.Fatalf("valid input result=%+v calls=%d", result, reader.calls)
	}
}

func TestConfluenceAttachmentSearchRejectsContradictoryResults(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*app.ConfluenceAttachmentDiscoveryResult)
	}{
		{name: "unknown qualification", mutate: func(result *app.ConfluenceAttachmentDiscoveryResult) { result.Qualification = "unknown" }},
		{name: "complete with reason", mutate: func(result *app.ConfluenceAttachmentDiscoveryResult) { result.Reason = "item_limit" }},
		{name: "complete with cursor", mutate: func(result *app.ConfluenceAttachmentDiscoveryResult) { result.NextCursor = "not-valid" }},
		{name: "complete without total", mutate: func(result *app.ConfluenceAttachmentDiscoveryResult) { result.TotalSize = nil }},
		{name: "nil attachments", mutate: func(result *app.ConfluenceAttachmentDiscoveryResult) { result.Attachments = nil }},
		{name: "count mismatch", mutate: func(result *app.ConfluenceAttachmentDiscoveryResult) { result.Count = 2 }},
		{name: "failed with row", mutate: func(result *app.ConfluenceAttachmentDiscoveryResult) {
			result.Qualification, result.Complete, result.Reason = app.ConfluenceAttachmentDiscoveryFailed, false, app.ConfluenceAttachmentDiscoveryReadFailed
			result.TotalSize = nil
		}},
		{name: "failed with total", mutate: func(result *app.ConfluenceAttachmentDiscoveryResult) {
			result.Qualification, result.Complete, result.Reason = app.ConfluenceAttachmentDiscoveryFailed, false, app.ConfluenceAttachmentDiscoveryReadFailed
			result.Count, result.Attachments = 0, []domain.ConfluenceAttachmentMetadata{}
		}},
		{name: "failed with cursor", mutate: func(result *app.ConfluenceAttachmentDiscoveryResult) {
			result.Qualification, result.Complete, result.Reason = app.ConfluenceAttachmentDiscoveryFailed, false, app.ConfluenceAttachmentDiscoveryReadFailed
			result.Count, result.Attachments, result.TotalSize, result.NextCursor = 0, []domain.ConfluenceAttachmentMetadata{}, nil, "not-valid"
		}},
		{name: "URL-shaped leaked title is still data", mutate: func(result *app.ConfluenceAttachmentDiscoveryResult) {
			result.Attachments[0].Title = "https://example.invalid/untrusted"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := attachmentDiscoveryMCPResult()
			test.mutate(out)
			reader := &recordingAttachmentDiscoveryReader{recordingConfluenceReader: &recordingConfluenceReader{}, result: out}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Confluence: func() (ConfluenceReader, error) { return reader, nil },
			}))
			defer closeSessions()
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "confluence_attachment_search", Arguments: map[string]any{
				"max_items": 2, "max_requests": 3, "max_response_bytes": 4096, "deadline_ms": 1000,
			}})
			if test.name == "URL-shaped leaked title is still data" {
				if err != nil || result == nil || result.IsError {
					t.Fatalf("untrusted title must remain metadata: result=%+v err=%v", result, err)
				}
				return
			}
			if err != nil || result == nil || !result.IsError {
				t.Fatalf("contradictory result=%+v err=%v", result, err)
			}
		})
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
	if _, exists := content["total_size"]; exists {
		t.Fatalf("failed discovery advertised a total: %#v", content)
	}
	attachments, ok := content["attachments"].([]any)
	if !ok || len(attachments) != 0 || content["count"] != float64(0) {
		t.Fatalf("failed discovery contains prefix evidence: %#v", content)
	}
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), privateMarker) {
		t.Fatalf("MCP failure leaked backend detail: %s", encoded)
	}
}
