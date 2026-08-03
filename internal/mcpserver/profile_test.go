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
	if len(jiraProfileTools) != 11 || len(confluenceProfileTools) != 12 {
		t.Fatalf("shared capability inventories jira=%d confluence=%d want=11/12", len(jiraProfileTools), len(confluenceProfileTools))
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
	if got := sha256.Sum256(profileJSON); hex.EncodeToString(got[:]) != "2bbde0b1ca5e46af6b7eef0bed867db0295e665b3534ce7c688a057872f1fee0" {
		t.Fatalf("default tool contract hash=%x", got)
	}
	if legacyClient.InitializeResult().Instructions != Instructions ||
		profileClient.InitializeResult().Instructions != Instructions {
		t.Fatal("default profile changed the legacy instruction bytes")
	}
}

func TestServiceProfileInstructionDigestsAreStable(t *testing.T) {
	want := map[ServiceProfile]string{
		ServiceDefault:    "c24f2df54a033d9771c4e6c265b028b1288f3670be5e7535fe8b79873957f732",
		ServiceJira:       "f47c4eef57cf8d3c61a1bc12557144438471847f29ee7a9d7c9d2a28c77c0bd4",
		ServiceConfluence: "2bdb992eafb5621728515beecd4d4bbed979fa47367be5d4a08b35f284a0ee79",
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
