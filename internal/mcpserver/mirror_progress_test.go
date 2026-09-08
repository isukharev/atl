package mcpserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/mirror"
)

func TestJiraMirrorSnapshotCompletePullStructuredAndText(t *testing.T) {
	root := t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	checkpoint := mirror.CompletePullCheckpoint{Service: mirror.CompletePullServiceJira, SelectorSHA256: strings.Repeat("a", 64), OptionsSHA256: strings.Repeat("b", 64), IDs: []string{"10001"}}
	body, err := json.Marshal(checkpoint.IDs)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.SelectionSHA256 = mirror.Hash(body)
	if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, ".atl", "complete-pulls", checkpoint.SelectorSHA256+".publish")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) {
			t.Error("backend resolved by offline snapshot")
			return nil, fmt.Errorf("unexpected backend")
		},
		MirrorRoot: func() (string, error) { return root, nil },
	}))
	defer closeSessions()
	result := callToolOK(t, client, "jira_mirror_snapshot", map[string]any{})
	content, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("unexpected output: %#v", result.StructuredContent)
	}
	progress, ok := content["complete_pull"].(map[string]any)
	if !ok || content["schema_version"] != float64(2) || content["complete"] != false || progress["status"] != "recovery_pending" || progress["recovery"] != "preserve_for_inspection" || progress["healthy"] != false {
		t.Fatalf("content=%#v", content)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{root, "10001", checkpoint.SelectorSHA256, "options_sha256"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("result leaked fixture value: %s", encoded)
		}
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("snapshot changed stage: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("missing text projection: %#v", result.Content)
	}
	projection, ok := result.Content[0].(*mcp.TextContent)
	var projected map[string]any
	if !ok || json.Unmarshal([]byte(projection.Text), &projected) != nil || !reflect.DeepEqual(projected, content) {
		t.Fatalf("text projection differs from structured content: %#v", result.Content)
	}
}
