package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	capabilitydef "github.com/isukharev/atl/internal/capability"
)

func TestCommandEffectCatalogClassifiesEveryExecutableLeaf(t *testing.T) {
	catalog, err := buildCommandEffectCatalog(commandEffectSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.SchemaVersion != commandEffectCatalogSchemaVersion || catalog.Enforcement != "informational" || catalog.Selection.Count != 168 {
		t.Fatalf("catalog metadata=%+v", catalog)
	}
	profiles := capabilitydef.EffectProfiles()
	if !reflect.DeepEqual(catalog.Profiles, profiles) {
		t.Fatalf("profiles=%+v want=%+v", catalog.Profiles, profiles)
	}
	seenProfiles := map[string]bool{}
	for i, command := range catalog.Commands {
		if command.Command == "" || command.EffectProfile == "" || i > 0 && catalog.Commands[i-1].Command >= command.Command {
			t.Fatalf("commands are empty, duplicated, or unsorted at %+v", command)
		}
		profile, ok := capabilitydef.EffectProfileByID(command.EffectProfile)
		if !ok || profile.ID == "" {
			t.Fatalf("command %q has invalid profile %q", command.Command, command.EffectProfile)
		}
		seenProfiles[profile.ID] = true
		registration := commandRegistry.nodes[command.Command]
		if registration.effectProfile != command.EffectProfile {
			t.Fatalf("command %q catalog profile=%q registry=%q", command.Command, command.EffectProfile, registration.effectProfile)
		}
		if !sort.StringsAreSorted(command.CapabilityIDs) {
			t.Fatalf("command %q capability ids are unsorted: %v", command.Command, command.CapabilityIDs)
		}
	}
	for _, profile := range profiles {
		if !seenProfiles[profile.ID] {
			t.Errorf("effect profile %q is unused", profile.ID)
		}
	}
}

func TestCommandEffectCatalogReferencesCuratedCapabilitiesByCommand(t *testing.T) {
	catalog, err := buildCommandEffectCatalog(commandEffectSelection{Command: "jira board list"})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Selection.Count != 1 || len(catalog.Commands) != 1 || len(catalog.Profiles) != 1 {
		t.Fatalf("exact catalog=%+v", catalog)
	}
	command := catalog.Commands[0]
	want := []string{"jira.board.list"}
	if !reflect.DeepEqual(command.CapabilityIDs, want) || command.EffectProfile != capabilitydef.EffectRemoteRead {
		t.Fatalf("command=%+v want capability_ids=%v profile=%s", command, want, capabilitydef.EffectRemoteRead)
	}
	if _, err := buildCommandEffectCatalog(commandEffectSelection{Command: "jira unknown"}); err == nil {
		t.Fatal("unknown exact command selection succeeded")
	}
}

func TestMCPServeEffectProfilePreservesHardReadOnlyBoundary(t *testing.T) {
	catalog, err := buildCommandEffectCatalog(commandEffectSelection{Command: "mcp serve"})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Commands) != 1 || len(catalog.Profiles) != 1 {
		t.Fatalf("mcp effect catalog=%+v", catalog)
	}
	command, profile := catalog.Commands[0], catalog.Profiles[0]
	if command.Access != "read-only" || command.EffectProfile != capabilitydef.EffectStdioServer ||
		profile.RemoteEffect != "read" || profile.LocalEffect != "read" || profile.OutputKind != "protocol" {
		t.Fatalf("mcp command=%+v profile=%+v", command, profile)
	}
}

func TestOptionalRemoteProfileMapsExactlyToLocalFirstMirrorInspections(t *testing.T) {
	catalog, err := buildCommandEffectCatalog(commandEffectSelection{})
	if err != nil {
		t.Fatal(err)
	}
	wantCommands := []string{"conf snapshot", "conf status", "jira snapshot", "jira status"}
	var gotCommands []string
	for _, command := range catalog.Commands {
		if command.EffectProfile == capabilitydef.EffectOptionalRemoteRead {
			gotCommands = append(gotCommands, command.Command)
		}
	}
	if !reflect.DeepEqual(gotCommands, wantCommands) {
		t.Fatalf("optional remote commands=%v want=%v", gotCommands, wantCommands)
	}

	profile, ok := capabilitydef.EffectProfileByID(capabilitydef.EffectOptionalRemoteRead)
	if !ok || profile.RemoteEffect != "read" || profile.LocalEffect != "read" ||
		profile.CredentialAccess != "possible" || profile.NetworkBound != "unknown" ||
		profile.ProcessEffect != "none" || profile.ReplayClass != "replay_safe" ||
		profile.OutputKind != "data" || profile.LocalArtifact != "none" ||
		profile.Configuration != "read" || profile.SelfUpdate != "possible" {
		t.Fatalf("optional remote profile=%+v/%t", profile, ok)
	}

	root := newRoot()
	for _, path := range wantCommands {
		command, args, findErr := root.Find(strings.Fields(path))
		if findErr != nil || len(args) != 0 {
			t.Fatalf("find %q command=%v args=%v err=%v", path, command, args, findErr)
		}
		remote := command.Flags().Lookup("remote")
		if remote == nil || remote.Value.Type() != "bool" || remote.DefValue != "false" {
			t.Fatalf("%q remote flag=%+v, want explicit optional bool default false", path, remote)
		}
	}
}

func TestReviewedEffectDimensionsKeepCredentialAndRequestBoundsHonest(t *testing.T) {
	tests := []struct {
		command, remote, local, credential, network, process, output string
	}{
		{command: "auth status", remote: "none", local: "read", credential: "possible", network: "none", process: "none", output: "data"},
		{command: "auth logout", remote: "none", local: "write", credential: "possible", network: "none", process: "none", output: "data"},
		{command: "auth login", remote: "read", local: "write", credential: "possible", network: "unknown", process: "none", output: "data"},
		{command: "completion bash", remote: "none", local: "none", credential: "none", network: "none", process: "none", output: "generator"},
		{command: "conf comment list", remote: "read", local: "none", credential: "required", network: "required_internal_cap", process: "none", output: "data"},
		{command: "conf comment preview", remote: "read", local: "read", credential: "required", network: "unknown", process: "none", output: "data"},
		{command: "conf page create", remote: "write", local: "write", credential: "required", network: "unknown", process: "none", output: "data"},
		{command: "conf page open", remote: "read", local: "none", credential: "required", network: "fixed", process: "launch", output: "data"},
		{command: "conf page section", remote: "read", local: "none", credential: "required", network: "fixed", process: "none", output: "data"},
		{command: "jira board view", remote: "read", local: "none", credential: "required", network: "required_internal_cap", process: "none", output: "data"},
		{command: "jira issue attachment get", remote: "read", local: "download", credential: "required", network: "unknown", process: "none", output: "data"},
		{command: "jira issue field preview", remote: "read", local: "read", credential: "required", network: "unknown", process: "none", output: "data"},
		{command: "jira issue field set", remote: "write", local: "read", credential: "required", network: "unknown", process: "none", output: "data"},
		{command: "jira issue get", remote: "read", local: "none", credential: "required", network: "unknown", process: "none", output: "data"},
		{command: "jira issue graph", remote: "read", local: "none", credential: "required", network: "caller", process: "none", output: "data"},
		{command: "jira issue reference search", remote: "read", local: "none", credential: "required", network: "caller", process: "none", output: "data"},
		{command: "jira issue search", remote: "read", local: "none", credential: "required", network: "fixed", process: "none", output: "data"},
		{command: "jira issue assign", remote: "write", local: "none", credential: "required", network: "unknown", process: "none", output: "data"},
		{command: "jira issue plan apply", remote: "write", local: "read", credential: "required", network: "unknown", process: "none", output: "data"},
		{command: "jira reconcile stage", remote: "none", local: "write", credential: "none", network: "none", process: "none", output: "data"},
		{command: "mcp serve", remote: "read", local: "read", credential: "possible", network: "unknown", process: "none", output: "protocol"},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			catalog, err := buildCommandEffectCatalog(commandEffectSelection{Command: test.command})
			if err != nil {
				t.Fatal(err)
			}
			if len(catalog.Profiles) != 1 {
				t.Fatalf("catalog=%+v", catalog)
			}
			profile := catalog.Profiles[0]
			if profile.RemoteEffect != test.remote || profile.LocalEffect != test.local ||
				profile.CredentialAccess != test.credential || profile.NetworkBound != test.network ||
				profile.ProcessEffect != test.process || profile.OutputKind != test.output {
				t.Fatalf("profile=%+v want remote=%s local=%s credential=%s network=%s process=%s output=%s",
					profile, test.remote, test.local, test.credential, test.network, test.process, test.output)
			}
		})
	}
}

func TestCapabilitiesEffectsInspectionIsOffline(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"ATL_CONFIG_DIR": configDir,
		"ATL_JIRA_URL":   server.URL, "ATL_JIRA_PAT": "test-pat",
		"ATL_CONFLUENCE_URL": server.URL, "ATL_CONFLUENCE_PAT": "test-pat",
	}
	out, code := runCLI(t, env, "capabilities", "--effects", "--command", "jira board list")
	if code != exitOK {
		t.Fatalf("effects exit=%d output=%s", code, out)
	}
	var catalog commandEffectCatalog
	if err := json.Unmarshal([]byte(out), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Selection.Command != "jira board list" || catalog.Selection.Count != 1 || requests != 0 {
		t.Fatalf("catalog=%+v requests=%d", catalog, requests)
	}
	if _, code := runCLI(t, env, "capabilities", "--command", "jira board list"); code != exitUsage {
		t.Fatalf("--command without --effects exit=%d", code)
	}
	if _, code := runCLI(t, env, "capabilities", "--effects", "--task", "jira/evidence"); code != exitUsage {
		t.Fatalf("mixed effect/capability filters exit=%d", code)
	}
	if requests != 0 {
		t.Fatalf("offline effect inspection made %d backend requests", requests)
	}
}
