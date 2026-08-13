package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/capability"
	"github.com/isukharev/atl/internal/domain"
)

func TestClosedServiceProfilesExposeExactInventories(t *testing.T) {
	jiraProfileTools := mappedToolsForService(t, "jira")
	confluenceProfileTools := mappedToolsForService(t, "confluence")
	if len(jiraProfileTools) != 11 || len(confluenceProfileTools) != 13 {
		t.Fatalf("shared capability inventories jira=%d confluence=%d want=11/13", len(jiraProfileTools), len(confluenceProfileTools))
	}
	tests := []struct {
		name         string
		profile      ServiceProfile
		instructions string
		tools        []string
	}{
		{"default", ServiceDefault, Instructions, append(append([]string(nil), confluenceProfileTools...), jiraProfileTools...)},
		{"jira", ServiceJira, JiraInstructions, jiraProfileTools},
		{"confluence", ServiceConfluence, ConfluenceInstructions, confluenceProfileTools},
		{"offline", ServiceOffline, OfflineInstructions, []string{"confluence_mirror_snapshot", "jira_mirror_snapshot"}},
	}
	allTools := append(append([]string(nil), confluenceProfileTools...), jiraProfileTools...)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var jiraCalls, confluenceCalls, mirrorCalls atomic.Int32
			deps := Dependencies{
				Jira: func() (JiraReader, error) { jiraCalls.Add(1); return nil, errors.New("unexpected Jira construction") },
				Confluence: func() (ConfluenceReader, error) {
					confluenceCalls.Add(1)
					return nil, errors.New("unexpected Confluence construction")
				},
				MirrorRoot: func() (string, error) { mirrorCalls.Add(1); return "", errors.New("unexpected mirror read") },
			}
			client, closeSessions := connectTestClient(t, NewForService("test", deps, tt.profile))
			defer closeSessions()

			initialized := client.InitializeResult()
			if initialized == nil || initialized.Instructions != tt.instructions {
				t.Fatalf("instructions=%q want=%q", initialized.Instructions, tt.instructions)
			}
			listed, err := client.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(listed.Tools))
			for _, tool := range listed.Tools {
				got = append(got, tool.Name)
				assertClosedReadOnlyAnnotations(t, tool)
			}
			if !reflect.DeepEqual(got, tt.tools) {
				t.Fatalf("tools=%v want=%v", got, tt.tools)
			}
			if tt.profile != ServiceDefault {
				for _, name := range allTools {
					if !containsString(tt.tools, name) && strings.Contains(initialized.Instructions, name) {
						t.Errorf("instructions mention absent tool %q", name)
					}
				}
			}
			if jiraCalls.Load() != 0 || confluenceCalls.Load() != 0 || mirrorCalls.Load() != 0 {
				t.Fatalf("profile inventory constructed dependencies: jira=%d confluence=%d mirror=%d",
					jiraCalls.Load(), confluenceCalls.Load(), mirrorCalls.Load())
			}
		})
	}
}

func mappedToolsForService(t *testing.T, service string) []string {
	t.Helper()
	unique := map[string]bool{}
	for _, definition := range capability.Definitions() {
		if definition.Service == service && definition.MCPTool != "" {
			unique[definition.MCPTool] = true
		}
	}
	tools := make([]string, 0, len(unique))
	for name := range unique {
		tools = append(tools, name)
	}
	sort.Strings(tools)
	return tools
}

func TestDefaultProfilePreservesNewToolSchemasAndInstructions(t *testing.T) {
	legacyClient, closeLegacy := connectTestClient(t, New("test", Dependencies{}))
	defer closeLegacy()
	profileClient, closeProfile := connectTestClient(t, NewForService("test", Dependencies{}, ServiceDefault))
	defer closeProfile()

	legacy, err := legacyClient.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := profileClient.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	legacyJSON, err := json.Marshal(legacy.Tools)
	if err != nil {
		t.Fatal(err)
	}
	profileJSON, err := json.Marshal(profile.Tools)
	if err != nil {
		t.Fatal(err)
	}
	if string(legacyJSON) != string(profileJSON) {
		t.Fatal("default profile changed the legacy tool inventory or schemas")
	}
	if got := sha256.Sum256(profileJSON); hex.EncodeToString(got[:]) != "557766338f71b4814aa02ae046faed99746cc594e65c95eb7e2ea840ec34b3a9" {
		t.Fatalf("default tool contract hash=%x", got)
	}
	if legacyClient.InitializeResult().Instructions != Instructions ||
		profileClient.InitializeResult().Instructions != Instructions {
		t.Fatal("default profile changed the legacy instruction bytes")
	}
}

func TestServiceProfileInstructionDigestsAreStable(t *testing.T) {
	want := map[ServiceProfile]string{
		ServiceDefault:    "597bcaf0f7c500f492a6c222f0a8a1d557b07a04fc59c207a776f227763c7b4a",
		ServiceJira:       "50ce5c2d3f0fa71dff44762ab56f70ba3e284c2e099b949cf021bd30f6cb9764",
		ServiceConfluence: "8c44ef6db40ecdf4af22b91a8716210c669bbebb4ae0ab1d72f6cb6cbd56eb1d",
		ServiceOffline:    "9ab393f7baf37c2249e099d8c1682c00e4cd7768ed5211371b4f25888c6b7aaa",
	}
	for profile, expected := range want {
		got := sha256.Sum256([]byte(instructionsForService(profile)))
		if hex.EncodeToString(got[:]) != expected {
			t.Errorf("profile %q instruction digest=%x", profile, got)
		}
	}
}

func TestParseServiceProfileIsClosed(t *testing.T) {
	for _, value := range []string{"jira", "confluence", "offline"} {
		profile, err := ParseServiceProfile(value)
		if err != nil || string(profile) != value {
			t.Errorf("ParseServiceProfile(%q)=(%q,%v)", value, profile, err)
		}
	}
	for _, value := range []string{"", "default", "JIRA", "jira,confluence", "unknown"} {
		if _, err := ParseServiceProfile(value); !errors.Is(err, domain.ErrUsage) {
			t.Errorf("ParseServiceProfile(%q) error=%v, want usage", value, err)
		}
	}
}

func assertClosedReadOnlyAnnotations(t *testing.T, tool *mcp.Tool) {
	t.Helper()
	annotations := tool.Annotations
	if annotations == nil || !annotations.ReadOnlyHint || !annotations.IdempotentHint ||
		annotations.DestructiveHint == nil || *annotations.DestructiveHint ||
		annotations.OpenWorldHint == nil || *annotations.OpenWorldHint {
		t.Errorf("tool %q annotations=%+v", tool.Name, annotations)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
