package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/cli"
)

func TestRepositoryDocumentationFreshness(t *testing.T) {
	root := freshnessRepositoryRoot(t)
	result, err := validateRepository(root, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Commands == 0 || result.Flags == 0 || result.Routes == 0 || result.MutationProfiles == 0 || result.ImpactRules == 0 {
		t.Fatalf("incomplete report: %+v", result)
	}
}

func TestRepositoryDocumentationFreshnessRejectsHeadWithoutBase(t *testing.T) {
	_, err := validateRepository(freshnessRepositoryRoot(t), "", "HEAD", "")
	if err == nil || !strings.Contains(err.Error(), "requires ATL_DOCS_BASE") {
		t.Fatalf("unexpected head-only result: %v", err)
	}
}

func TestCommandCoverageRejectsRegressions(t *testing.T) {
	root := freshnessRepositoryRoot(t)
	commands, err := cli.RepositoryCommandInventory()
	if err != nil {
		t.Fatal(err)
	}
	flags, err := cli.RepositoryFlagInventory()
	if err != nil {
		t.Fatal(err)
	}
	documents, err := loadDocuments(filepath.Join(root, filepath.FromSlash(docsCatalogPath)))
	if err != nil {
		t.Fatal(err)
	}
	load := func(t *testing.T) commandManifest {
		t.Helper()
		manifest, err := loadCommandManifest(filepath.Join(root, filepath.FromSlash(commandManifestPath)))
		if err != nil {
			t.Fatal(err)
		}
		return manifest
	}
	tests := []struct {
		name string
		edit func(*commandManifest)
		want string
	}{
		{
			name: "missing executable leaf",
			edit: func(value *commandManifest) { value.Routes[0].Commands = nil },
			want: "empty command list",
		},
		{
			name: "duplicate executable leaf",
			edit: func(value *commandManifest) {
				value.Routes[1].Commands = append([]string{value.Routes[0].Commands[0]}, value.Routes[1].Commands...)
			},
			want: "covered by routes",
		},
		{
			name: "stale evidence",
			edit: func(value *commandManifest) { value.Routes[0].Evidence = "## Missing heading" },
			want: "evidence line occurs 0 times",
		},
		{
			name: "evidence is not a section heading",
			edit: func(value *commandManifest) { value.Routes[0].Evidence = "ordinary body line" },
			want: "bounded Markdown heading",
		},
		{
			name: "mutator loses safety route",
			edit: func(value *commandManifest) {
				for index := range value.Routes {
					if value.Routes[index].SafetyDocument != "" {
						value.Routes[index].SafetyDocument = ""
						value.Routes[index].SafetyEvidence = ""
						return
					}
				}
			},
			want: "requires a canonical safety document",
		},
		{
			name: "read only route claims mutation safety",
			edit: func(value *commandManifest) {
				value.Routes[0].SafetyDocument = "docs/safe-writes.md"
				value.Routes[0].SafetyEvidence = "## Pre-write checklist"
			},
			want: "must not declare mutation safety evidence",
		},
		{
			name: "mutation profile loses safety route",
			edit: func(value *commandManifest) { value.MutationProfiles = value.MutationProfiles[1:] },
			want: "has no documented safety route",
		},
		{
			name: "visible global flag loses route",
			edit: func(value *commandManifest) { value.GlobalFlagRoutes = value.GlobalFlagRoutes[1:] },
			want: "visible global flag --output has no documentation route",
		},
		{
			name: "stale global flag route",
			edit: func(value *commandManifest) { value.GlobalFlagRoutes[0].Flag = "missing" },
			want: "global flag route --missing is stale or non-visible",
		},
		{
			name: "hidden flag loses exclusion",
			edit: func(value *commandManifest) { value.FlagExclusions = value.FlagExclusions[:1] },
			want: "class \"hidden\" has no explicit exclusion",
		},
		{
			name: "visible flag is excluded",
			edit: func(value *commandManifest) {
				value.FlagExclusions = append([]flagExclusion{{Command: "", Flag: "output", Class: cli.RepositoryFlagHidden, Reason: "invalid visible exclusion"}}, value.FlagExclusions...)
			},
			want: "must be documented, not excluded",
		},
		{
			name: "exclusion class drifts",
			edit: func(value *commandManifest) { value.FlagExclusions[1].Class = cli.RepositoryFlagDeprecated },
			want: "inventory requires \"hidden\"",
		},
		{
			name: "stale exclusion",
			edit: func(value *commandManifest) {
				value.FlagExclusions = append(value.FlagExclusions, flagExclusion{Command: "zzzz", Flag: "removed", Class: cli.RepositoryFlagHidden, Reason: "stale fixture"})
			},
			want: "is stale",
		},
		{
			name: "wildcard is reserved for framework help",
			edit: func(value *commandManifest) { value.FlagExclusions[0].Flag = "version" },
			want: "only the framework --help flag may use a wildcard exclusion",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := load(t)
			test.edit(&manifest)
			err := validateCommandCoverage(root, manifest, commands, flags, documents)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateFlagCoverageRequiresExactTokensAndExplicitExclusions(t *testing.T) {
	root := t.TempDir()
	document := "docs/reference.md"
	path := filepath.Join(root, filepath.FromSlash(document))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("# Reference\n\n## Global\n\n`--output`\n\n## Demo\n\n`--alpha`\n")
	manifest := commandManifest{
		GlobalFlagRoutes: []flagRoute{{Flag: "output", Document: document, Evidence: "## Global"}},
		FlagExclusions: []flagExclusion{
			{Command: "*", Flag: "help", Class: cli.RepositoryFlagFramework, Reason: "framework flag"},
			{Command: "demo", Flag: "legacy", Class: cli.RepositoryFlagHidden, Reason: "hidden compatibility flag"},
			{Command: "demo", Flag: "old", Class: cli.RepositoryFlagDeprecated, Reason: "deprecated compatibility flag"},
		},
	}
	flags := []cli.RepositoryFlag{
		{Command: "", Name: "help", Class: cli.RepositoryFlagFramework},
		{Command: "", Name: "output", Class: cli.RepositoryFlagVisible},
		{Command: "demo", Name: "alpha", Class: cli.RepositoryFlagVisible},
		{Command: "demo", Name: "help", Class: cli.RepositoryFlagFramework},
		{Command: "demo", Name: "legacy", Class: cli.RepositoryFlagHidden},
		{Command: "demo", Name: "old", Class: cli.RepositoryFlagDeprecated},
	}
	documents := map[string]docsEntry{document: {Path: document, Lane: "reference"}}
	commandDocuments := map[string]string{"demo": document}
	if err := validateFlagCoverage(root, manifest, flags, commandDocuments, documents); err != nil {
		t.Fatal(err)
	}

	write("# Reference\n\n## Global\n\n`--output`\n\n## Demo\n\n`--alphabet`\n")
	if err := validateFlagCoverage(root, manifest, flags, commandDocuments, documents); err == nil || !strings.Contains(err.Error(), "demo --alpha") {
		t.Fatalf("prefix-only flag documentation passed: %v", err)
	}

	manifest.FlagExclusions = manifest.FlagExclusions[:1]
	if err := validateFlagCoverage(root, manifest, flags, commandDocuments, documents); err == nil || !strings.Contains(err.Error(), "no explicit exclusion") {
		t.Fatalf("hidden flag without exclusion passed: %v", err)
	}
}

func TestContainsExactFlagToken(t *testing.T) {
	for _, test := range []struct {
		body string
		want bool
	}{
		{"use `--limit`", true},
		{"use --limit=10", true},
		{"use --limit-extra", false},
		{"use x--limit", false},
		{"use ---limit", false},
	} {
		if got := containsExactFlagToken([]byte(test.body), "limit"); got != test.want {
			t.Errorf("containsExactFlagToken(%q)=%t, want %t", test.body, got, test.want)
		}
	}
}

func TestMaintainerImpactRejectsRegressions(t *testing.T) {
	root := freshnessRepositoryRoot(t)
	tracked, err := trackedFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	load := func(t *testing.T) impactManifest {
		t.Helper()
		manifest, err := loadImpactManifest(filepath.Join(root, filepath.FromSlash(impactManifestPath)))
		if err != nil {
			t.Fatal(err)
		}
		return manifest
	}
	tests := []struct {
		name string
		edit func(*impactManifest)
		want string
	}{
		{
			name: "missing Make target",
			edit: func(value *impactManifest) { value.Checks[0].MakeTarget = "missing-target" },
			want: "lacks a Make target",
		},
		{
			name: "stale selector",
			edit: func(value *impactManifest) { value.Rules[len(value.Rules)-1].Prefix = "zzzz-never-present/" },
			want: "matches no tracked path",
		},
		{
			name: "unsorted checks",
			edit: func(value *impactManifest) { value.Rules[5].Checks = []string{"test", "docs-catalog"} },
			want: "stale, duplicated, or unsorted check",
		},
		{
			name: "tracked path loses classification",
			edit: func(value *impactManifest) { value.Rules = value.Rules[1:] },
			want: "tracked path \".editorconfig\" has no maintainer impact classification",
		},
		{
			name: "excluded prefix escapes selector",
			edit: func(value *impactManifest) {
				for index := range value.Rules {
					if value.Rules[index].Prefix == "internal/" {
						value.Rules[index].ExcludePrefixes = []string{"scripts/"}
					}
				}
			},
			want: "malformed, duplicated, or unsorted excluded prefix",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := load(t)
			test.edit(&manifest)
			err := validateImpactManifest(manifest, tracked, makeTargets(filepath.Join(root, "Makefile")))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}

	manifest := load(t)
	unknown := changedPathSet{Paths: []string{"unclassified/new.file"}, Historical: map[string]bool{}}
	if _, err := classifyImpact(manifest, nil, unknown); err == nil || !strings.Contains(err.Error(), "no maintainer impact classification") {
		t.Fatalf("unexpected classification error: %v", err)
	}
}

func TestClassifyImpactKeepsNestedEvaluatorIndependent(t *testing.T) {
	root := freshnessRepositoryRoot(t)
	manifest, err := loadImpactManifest(filepath.Join(root, filepath.FromSlash(impactManifestPath)))
	if err != nil {
		t.Fatal(err)
	}
	checks, err := classifyImpact(manifest, nil, changedPathSet{
		Paths:      []string{"internal/agenteval/runner.go"},
		Historical: map[string]bool{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(checks, ","), "agent-eval-full,maintainability"; got != want {
		t.Fatalf("nested evaluator checks = %q, want %q", got, want)
	}
}

func TestClassifyImpactUsesBaselineOnlyForHistoricalPaths(t *testing.T) {
	current := impactManifest{
		Checks: []impactCheck{{ID: "test", MakeTarget: "test"}},
		Rules:  []impactRule{{Prefix: "current/", Checks: []string{"test"}}},
	}
	baseline := &impactManifest{
		Checks: []impactCheck{{ID: "test", MakeTarget: "test"}},
		Rules:  []impactRule{{Path: "removed.txt", Checks: []string{"test"}}, {Path: "new.txt", Checks: []string{"test"}}},
	}
	checks, err := classifyImpact(current, baseline, changedPathSet{
		Paths: []string{"removed.txt"}, Historical: map[string]bool{"removed.txt": true},
	})
	if err != nil || len(checks) != 1 || checks[0] != "test" {
		t.Fatalf("historical classification = %v, %v", checks, err)
	}
	if _, err := classifyImpact(current, baseline, changedPathSet{
		Paths: []string{"new.txt"}, Historical: map[string]bool{},
	}); err == nil || !strings.Contains(err.Error(), "no maintainer impact classification") {
		t.Fatalf("new path unexpectedly used baseline: %v", err)
	}
	baseline.Rules = []impactRule{{Path: "removed.txt", Checks: []string{"retired-check"}}}
	if _, err := classifyImpact(current, baseline, changedPathSet{
		Paths: []string{"removed.txt"}, Historical: map[string]bool{"removed.txt": true},
	}); err == nil || !strings.Contains(err.Error(), "requires unavailable check") {
		t.Fatalf("retired check unexpectedly accepted: %v", err)
	}
}

func TestClassifyImpactUnionsCurrentAndBaselineForHistoricalPaths(t *testing.T) {
	current := impactManifest{
		Checks: []impactCheck{{ID: "docs", MakeTarget: "check-docs-catalog"}, {ID: "plugins", MakeTarget: "check-plugins"}},
		Rules:  []impactRule{{Prefix: "docs/", Checks: []string{"docs"}}},
	}
	baseline := &impactManifest{
		Checks: []impactCheck{{ID: "docs", MakeTarget: "check-docs-catalog"}, {ID: "plugins", MakeTarget: "check-plugins"}},
		Rules: []impactRule{
			{Path: "docs/plugins.md", Checks: []string{"plugins"}},
			{Prefix: "docs/", Checks: []string{"docs"}},
		},
	}
	checks, err := classifyImpact(current, baseline, changedPathSet{
		Paths: []string{"docs/plugins.md"}, Historical: map[string]bool{"docs/plugins.md": true},
	})
	if err != nil || strings.Join(checks, ",") != "docs,plugins" {
		t.Fatalf("historical union = %v, %v", checks, err)
	}
}

func TestParseNameStatusIncludesRenameCopyAndNewlinePaths(t *testing.T) {
	output := []byte("R100\x00old\nname.md\x00new\nname.md\x00C087\x00source.go\x00copy.go\x00D\x00gone.txt\x00M\x00kept.txt\x00")
	changed, err := parseNameStatus(output)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"copy.go", "gone.txt", "kept.txt", "new\nname.md", "old\nname.md", "source.go"}
	if strings.Join(changed.Paths, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("paths = %q, want %q", changed.Paths, want)
	}
	if !changed.Historical["old\nname.md"] || !changed.Historical["gone.txt"] || changed.Historical["source.go"] || changed.Historical["new\nname.md"] {
		t.Fatalf("historical paths = %#v", changed.Historical)
	}
	for _, malformed := range [][]byte{
		[]byte("R100\x00only-one-path\x00"),
		[]byte("Q\x00path\x00"),
		[]byte("M\x00\x00"),
	} {
		if _, err := parseNameStatus(malformed); err == nil {
			t.Fatalf("malformed name-status accepted: %q", malformed)
		}
	}
}

func TestChangedFilesIncludesBothSidesOfRename(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.name", "Fixture")
	runGit(t, root, "config", "user.email", "fixture@example.invalid")
	oldPath := filepath.Join(root, "old\nname.txt")
	newPath := filepath.Join(root, "new\nname.txt")
	if err := os.WriteFile(oldPath, []byte("stable content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "base")
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	changed, err := changedFiles(root, "HEAD", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(changed.Paths) != 2 || !changed.Historical["old\nname.txt"] || changed.Historical["new\nname.txt"] {
		t.Fatalf("rename classification = %+v", changed)
	}
}

func TestPrivateMarkerScanRedactsValuesAndIgnoresRetiredMarkers(t *testing.T) {
	const secret = "SENSITIVE-MARKER-CONTENT"
	path := filepath.Join(t.TempDir(), "markers.json")
	writeMarkerRegistry(t, path, []privateMarker{
		{ID: "literal-marker", Category: "host", MatchType: "literal", Value: secret, State: "active"},
	})
	_, err := scanPrivateMarkers(path, []byte("prefix "+secret+" suffix"))
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "literal-marker") || strings.Contains(err.Error(), "host") {
		t.Fatalf("marker error is absent or leaks content: %v", err)
	}

	writeMarkerRegistry(t, path, []privateMarker{
		{ID: "regex-marker", Category: "target", MatchType: "regexp", Value: `PRIVATE-[0-9]+`, State: "active"},
	})
	_, err = scanPrivateMarkers(path, []byte("PRIVATE-12345"))
	if err == nil || strings.Contains(err.Error(), "12345") || strings.Contains(err.Error(), "regex-marker") || strings.Contains(err.Error(), "target") {
		t.Fatalf("regexp error is absent or leaks content: %v", err)
	}

	writeMarkerRegistry(t, path, []privateMarker{
		{ID: "retired-marker", Category: "other", MatchType: "literal", Value: secret, State: "retired"},
	})
	count, err := scanPrivateMarkers(path, []byte(secret))
	if err != nil || count != 0 {
		t.Fatalf("retired marker result = %d, %v", count, err)
	}
}

func TestAddedDiffContentContainsOnlyAddedLines(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.name", "Fixture")
	runGit(t, root, "config", "user.email", "fixture@example.invalid")
	path := filepath.Join(root, "fixture.txt")
	if err := os.WriteFile(path, []byte("REMOVE-PRIVATE\nstable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "fixture.txt")
	runGit(t, root, "commit", "-qm", "base")
	if err := os.WriteFile(path, []byte("stable\nclean addition\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := addedDiffContent(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "REMOVE-PRIVATE") || !strings.Contains(string(content), "clean addition") {
		t.Fatalf("unexpected added diff content: %q", content)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("UNTRACKED-PRIVATE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err = addedDiffContent(root, "", "")
	if err != nil || !strings.Contains(string(content), "UNTRACKED-PRIVATE") {
		t.Fatalf("untracked diff content = %q, %v", content, err)
	}
	if err := os.WriteFile(path, []byte("stable\nADDED-PRIVATE\n++PREFIXED-PRIVATE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "fixture.txt")
	content, err = addedDiffContent(root, "HEAD", "")
	if err != nil || !strings.Contains(string(content), "ADDED-PRIVATE") || !strings.Contains(string(content), "++PREFIXED-PRIVATE") {
		t.Fatalf("base diff content = %q, %v", content, err)
	}
	binaryPath := filepath.Join(root, "binary.dat")
	if err := os.WriteFile(binaryPath, []byte{'\x00', 'B', 'I', 'N', 'A', 'R', 'Y', '-', 'P', 'R', 'I', 'V', 'A', 'T', 'E', '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "binary.dat")
	content, err = addedDiffContent(root, "HEAD", "")
	if err != nil || !strings.Contains(string(content), "BINARY-PRIVATE") {
		t.Fatalf("binary diff content = %q, %v", content, err)
	}
}

func TestStrictManifestDecodeRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"global_flag_routes":[],"flag_exclusions":[],"routes":[],"mutation_profiles":[],"extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCommandManifest(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unexpected strict decode result: %v", err)
	}
}

func freshnessRepositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeMarkerRegistry(t *testing.T, path string, markers []privateMarker) {
	t.Helper()
	body, err := json.Marshal(markerRegistry{SchemaVersion: 1, Markers: markers})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
}
