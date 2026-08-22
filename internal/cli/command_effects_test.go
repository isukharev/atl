package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	capabilitydef "github.com/isukharev/atl/internal/capability"
	"github.com/isukharev/atl/internal/domain"
)

func TestCommandEffectCatalogClassifiesEveryExecutableLeaf(t *testing.T) {
	catalog, err := buildCommandEffectCatalog(commandEffectSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.SchemaVersion != commandEffectCatalogSchemaVersion || catalog.Enforcement != "informational" || catalog.Selection.Count != 177 {
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

func TestCallerBoundedConfluenceReadsMapToExactCapabilities(t *testing.T) {
	tests := []struct {
		command      string
		capabilityID string
	}{
		{command: "conf attachment search", capabilityID: "confluence.attachment.search"},
		{command: "conf space tree", capabilityID: "confluence.space.tree"},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			catalog, err := buildCommandEffectCatalog(commandEffectSelection{Command: test.command})
			if err != nil {
				t.Fatal(err)
			}
			if len(catalog.Commands) != 1 || catalog.Commands[0].EffectProfile != capabilitydef.EffectRemoteReadCaller ||
				!reflect.DeepEqual(catalog.Commands[0].CapabilityIDs, []string{test.capabilityID}) {
				t.Fatalf("catalog=%+v", catalog)
			}
		})
	}
}

func TestCommandEffectCatalogTextIsStableAndContentFree(t *testing.T) {
	catalog := commandEffectCatalog{Commands: []commandEffect{
		{
			Command:       "conf page get",
			Access:        "read-only",
			EffectProfile: capabilitydef.EffectRemoteRead,
			CapabilityIDs: []string{"confluence.page.get"},
		},
		{
			Command:       "version",
			Access:        "read-only",
			EffectProfile: capabilitydef.EffectPure,
		},
	}}
	want := "| Command | Access | Effect profile | Capability IDs |\n" +
		"| --- | --- | --- | --- |\n" +
		"| `atl conf page get` | read-only | `remote-read` | confluence.page.get |\n" +
		"| `atl version` | read-only | `pure` |  |"
	if got := commandEffectCatalogText(catalog); got != want {
		t.Fatalf("effect catalog text:\n%s\nwant:\n%s", got, want)
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

func TestConfigurationReadingOfflineMutatorsMapExactlyToTheirExecutionPath(t *testing.T) {
	catalog, err := buildCommandEffectCatalog(commandEffectSelection{})
	if err != nil {
		t.Fatal(err)
	}
	wantByProfile := map[string][]string{
		capabilitydef.EffectCredentialWrite:     {"auth logout"},
		capabilitydef.EffectLocalArtifact:       {"corpus cache retention preview", "corpus export"},
		capabilitydef.EffectLocalArtifactConfig: {"profile revalidate", "profile suggest"},
		capabilitydef.EffectLocalOptionalWrite:  {"corpus diff", "corpus handoff"},
	}
	gotByProfile := map[string][]string{}
	for _, command := range catalog.Commands {
		if _, reviewed := wantByProfile[command.EffectProfile]; reviewed {
			gotByProfile[command.EffectProfile] = append(gotByProfile[command.EffectProfile], command.Command)
		}
	}
	if !reflect.DeepEqual(gotByProfile, wantByProfile) {
		t.Fatalf("reviewed profile reverse mappings=%v want=%v", gotByProfile, wantByProfile)
	}
	for _, profileID := range []string{capabilitydef.EffectCredentialWrite, capabilitydef.EffectLocalArtifactConfig} {
		profile, ok := capabilitydef.EffectProfileByID(profileID)
		if !ok || profile.Configuration != "read" {
			t.Fatalf("profile %q=%+v/%t, want configuration read", profileID, profile, ok)
		}
	}
	configFree, ok := capabilitydef.EffectProfileByID(capabilitydef.EffectLocalArtifact)
	if !ok || configFree.Configuration != "none" {
		t.Fatalf("config-free artifact profile=%+v/%t", configFree, ok)
	}

	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	commands := []struct {
		path string
		args []string
	}{
		{path: "auth logout", args: []string{"auth", "logout", "--service", "jira"}},
		{path: "profile revalidate", args: []string{"profile", "revalidate", "--from-file", filepath.Join(configDir, "missing-revalidation.json"), "--out", filepath.Join(configDir, "revalidated.json")}},
		{path: "profile suggest", args: []string{"profile", "suggest", "--from-file", filepath.Join(configDir, "missing-observations.json"), "--out", filepath.Join(configDir, "suggestion.json")}},
	}
	for _, command := range commands {
		t.Run(command.path, func(t *testing.T) {
			stdout, _, execErr := executeCLIRaw(t, map[string]string{"ATL_CONFIG_DIR": configDir}, command.args...)
			if !errors.Is(execErr, domain.ErrConfig) || codeFor(execErr) != exitConfig || stdout != "" {
				t.Fatalf("error=%v exit=%d stdout=%q, want configuration read before command effects", execErr, codeFor(execErr), stdout)
			}
		})
	}
}

func TestCorpusCacheEffectProfilesMatchOfflineLifecycle(t *testing.T) {
	want := map[string]string{
		"corpus cache retention apply":   capabilitydef.EffectLocalWrite,
		"corpus cache retention preview": capabilitydef.EffectLocalArtifact,
		"corpus cache status":            capabilitydef.EffectLocalRead,
	}
	for commandPath, profileID := range want {
		t.Run(commandPath, func(t *testing.T) {
			catalog, err := buildCommandEffectCatalog(commandEffectSelection{Command: commandPath})
			if err != nil {
				t.Fatal(err)
			}
			if len(catalog.Commands) != 1 || len(catalog.Profiles) != 1 {
				t.Fatalf("%s catalog=%+v", commandPath, catalog)
			}
			command, profile := catalog.Commands[0], catalog.Profiles[0]
			if command.EffectProfile != profileID || profile.ID != profileID ||
				profile.RemoteEffect != "none" || profile.CredentialAccess != "none" ||
				profile.NetworkBound != "none" || profile.ProcessEffect != "none" ||
				profile.SelfUpdate != "disabled" {
				t.Fatalf("%s command=%+v profile=%+v", commandPath, command, profile)
			}
		})
	}
}

func TestCorpusInspectionEffectProfileMatchesOptionalPrivateArtifactPaths(t *testing.T) {
	for _, test := range []struct {
		command string
		args    []string
		flag    string
	}{
		{command: "corpus diff", args: []string{"corpus", "diff"}, flag: "identity-artifact"},
		{command: "corpus handoff", args: []string{"corpus", "handoff"}, flag: "handoff-artifact"},
	} {
		t.Run(test.command, func(t *testing.T) {
			catalog, err := buildCommandEffectCatalog(commandEffectSelection{Command: test.command})
			if err != nil {
				t.Fatal(err)
			}
			if len(catalog.Commands) != 1 || len(catalog.Profiles) != 1 {
				t.Fatalf("%s catalog=%+v", test.command, catalog)
			}
			command, profile := catalog.Commands[0], catalog.Profiles[0]
			if command.Access != "read-only" || command.EffectProfile != capabilitydef.EffectLocalOptionalWrite ||
				profile.RemoteEffect != "none" || profile.LocalEffect != "write" ||
				profile.CredentialAccess != "none" || profile.NetworkBound != "none" ||
				profile.ProcessEffect != "none" || profile.ReplayClass != "non_replay_safe" ||
				profile.OutputKind != "data" || profile.LocalArtifact != "possible" ||
				profile.Configuration != "none" || profile.SelfUpdate != "disabled" {
				t.Fatalf("%s command=%+v profile=%+v", test.command, command, profile)
			}

			root := newRoot()
			leaf, args, findErr := root.Find(test.args)
			if findErr != nil || len(args) != 0 {
				t.Fatalf("find %s command=%v args=%v err=%v", test.command, leaf, args, findErr)
			}
			artifact := leaf.Flags().Lookup(test.flag)
			if artifact == nil || artifact.DefValue != "" {
				t.Fatalf("%s flag=%+v, want optional empty default", test.flag, artifact)
			}
		})
	}
}

func TestRemoteWriteLocalPossibleArtifactsMapToExactRegistrationLeaves(t *testing.T) {
	catalog, err := buildCommandEffectCatalog(commandEffectSelection{})
	if err != nil {
		t.Fatal(err)
	}
	wantProfileCommands := []string{
		"conf page copy",
		"conf page create",
		"conf plan apply",
		"conf push",
		"jira issue create",
		"jira push",
	}
	var gotProfileCommands []string
	for _, command := range catalog.Commands {
		if command.EffectProfile == capabilitydef.EffectRemoteWriteLocal {
			gotProfileCommands = append(gotProfileCommands, command.Command)
		}
	}
	if !reflect.DeepEqual(gotProfileCommands, wantProfileCommands) {
		t.Fatalf("remote-write-local commands=%v want=%v", gotProfileCommands, wantProfileCommands)
	}
	profile, ok := capabilitydef.EffectProfileByID(capabilitydef.EffectRemoteWriteLocal)
	if !ok || profile.LocalEffect != "write" || profile.LocalArtifact != "possible" {
		t.Fatalf("remote-write-local profile=%+v/%t, want local write with possible artifact", profile, ok)
	}

	root := newRoot()
	wantRegistrationCommands := []string{"conf page copy", "conf page create", "jira issue create"}
	var gotRegistrationCommands []string
	for _, command := range catalog.Commands {
		leaf, args, findErr := root.Find(strings.Fields(command.Command))
		if findErr != nil || len(args) != 0 {
			t.Fatalf("find %q command=%v args=%v err=%v", command.Command, leaf, args, findErr)
		}
		register := leaf.Flags().Lookup("register")
		if register == nil {
			continue
		}
		into := leaf.Flags().Lookup("into")
		if register.Value.Type() != "bool" || register.DefValue != "false" || into == nil || into.DefValue != "" {
			t.Fatalf("%q registration flags register=%+v into=%+v", command.Command, register, into)
		}
		if command.EffectProfile != capabilitydef.EffectRemoteWriteLocal {
			t.Fatalf("registration-capable command %q uses profile %q", command.Command, command.EffectProfile)
		}
		gotRegistrationCommands = append(gotRegistrationCommands, command.Command)
	}
	if !reflect.DeepEqual(gotRegistrationCommands, wantRegistrationCommands) {
		t.Fatalf("registration-capable commands=%v want=%v", gotRegistrationCommands, wantRegistrationCommands)
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
