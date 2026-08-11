package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRepositoryCatalog(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	report, err := validateRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Documents == 0 || report.Excluded == 0 {
		t.Fatalf("unexpected catalog report: %+v", report)
	}
	if report.Links == 0 {
		t.Fatal("repository validation did not inspect any local links")
	}
}

func TestTrackedMarkdownUsesGitIndex(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "README.md", "# Root\n")
	writeFile(t, root, "docs/tracked.md", "# Tracked\n")
	writeFile(t, root, "docs/untracked.md", "# Untracked\n")
	runGit(t, root, "init", "-q")
	runGit(t, root, "add", "README.md", "docs/tracked.md")

	got, err := trackedMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"README.md", "docs/tracked.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tracked Markdown = %q, want %q", got, want)
	}
}

func TestTrackedMarkdownRejectsUnapprovedRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "examples/unclassified.md", "# Unclassified\n")
	runGit(t, root, "init", "-q")
	runGit(t, root, "add", "examples/unclassified.md")

	_, err := trackedMarkdown(root)
	requireErrorContains(t, err, "outside the approved documentation roots")
}

func TestApprovedMarkdownPathIncludesAgentSkillsFixturesOnly(t *testing.T) {
	if !approvedMarkdownPath("internal/agenteval/interchange/agentskills/testdata/guide-v1/skill/SKILL.md") {
		t.Fatal("Agent Skills Markdown fixture was not admitted")
	}
	if approvedMarkdownPath("internal/agenteval/interchange/other/testdata/SKILL.md") ||
		approvedMarkdownPath("internal/agenteval/interchange/agentskills/source/SKILL.md") {
		t.Fatal("unreviewed evaluator Markdown root was admitted")
	}
}

func TestLoadCatalogRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"documents":[],"exclusions":[],"extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadCatalog(path)
	requireErrorContains(t, err, "unknown field")
}

func TestCatalogInventoryRejectsInvalidClassifications(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*testing.T, string, *catalog, *[]string)
		wantError string
	}{
		{
			name: "missing tracked document",
			mutate: func(t *testing.T, root string, _ *catalog, tracked *[]string) {
				writeFile(t, root, "MISSING.md", "# Missing\n")
				*tracked = append(*tracked, "MISSING.md")
			},
			wantError: "is missing from documents and exclusions",
		},
		{
			name: "duplicate path",
			mutate: func(_ *testing.T, _ string, value *catalog, _ *[]string) {
				value.Documents = append(value.Documents, value.Documents[len(value.Documents)-1])
			},
			wantError: "is duplicated",
		},
		{
			name: "duplicate topic",
			mutate: func(_ *testing.T, _ string, value *catalog, _ *[]string) {
				value.Documents[1].Topic = value.Documents[0].Topic
			},
			wantError: "topic \"root\" is duplicated",
		},
		{
			name: "stale document",
			mutate: func(t *testing.T, root string, value *catalog, _ *[]string) {
				writeFile(t, root, "STALE.md", "# Stale\n")
				value.Documents = append(value.Documents, catalogEntry{
					Path: "STALE.md", Lane: "reference", Topic: "stale", LandingPage: "README.md", Language: "en",
				})
			},
			wantError: "stale or noncanonical path",
		},
		{
			name: "noncanonical document",
			mutate: func(_ *testing.T, _ string, value *catalog, _ *[]string) {
				value.Documents[0].Path = "./README.md"
			},
			wantError: "stale or noncanonical path",
		},
		{
			name: "unsupported lane",
			mutate: func(_ *testing.T, _ string, value *catalog, _ *[]string) {
				value.Documents[0].Lane = "tutorial"
			},
			wantError: "unsupported lane or language",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, value, tracked := inventoryFixture(t)
			test.mutate(t, root, &value, &tracked)
			_, _, err := validateCatalogInventory(root, value, tracked)
			requireErrorContains(t, err, test.wantError)
		})
	}
}

func TestCatalogInventoryRejectsInvalidExclusions(t *testing.T) {
	tests := []struct {
		name      string
		rule      exclusionRule
		tracked   []string
		files     map[string]string
		wantError string
	}{
		{
			name:      "stale exact exclusion",
			rule:      exclusionRule{Path: "excluded/stale.md", Reason: "testdata"},
			files:     map[string]string{"excluded/stale.md": "# Stale\n"},
			wantError: "stale and matches no tracked Markdown",
		},
		{
			name:      "stale exception",
			rule:      exclusionRule{Prefix: "excluded/", Except: []string{"excluded/stale.md"}, Reason: "testdata"},
			tracked:   []string{"excluded/data.md"},
			files:     map[string]string{"excluded/data.md": "# Data\n", "excluded/stale.md": "# Stale\n"},
			wantError: "stale except path",
		},
		{
			name:      "open reason vocabulary",
			rule:      exclusionRule{Path: "excluded/data.md", Reason: "miscellaneous"},
			tracked:   []string{"excluded/data.md"},
			files:     map[string]string{"excluded/data.md": "# Data\n"},
			wantError: "closed exclusion vocabulary",
		},
		{
			name:      "overlapping classification",
			rule:      exclusionRule{Path: "README.md", Reason: "testdata"},
			wantError: "classified more than once",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, value, tracked := inventoryFixture(t)
			for path, contents := range test.files {
				writeFile(t, root, path, contents)
			}
			tracked = append(tracked, test.tracked...)
			value.Exclusions = []exclusionRule{test.rule}
			_, _, err := validateCatalogInventory(root, value, tracked)
			requireErrorContains(t, err, test.wantError)
		})
	}
}

func TestCatalogInventoryRejectsSymlinkPath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "README.md", "# Root\n")
	if err := os.Symlink("README.md", filepath.Join(root, "Linked.md")); err != nil {
		t.Fatal(err)
	}
	value := catalog{SchemaVersion: 1, Documents: []catalogEntry{
		{Path: "Linked.md", Lane: "reference", Topic: "linked", LandingPage: "README.md", Language: "en"},
		{Path: "README.md", Lane: "start", Topic: "root", Language: "en"},
	}, Exclusions: []exclusionRule{}}
	_, _, err := validateCatalogInventory(root, value, []string{"Linked.md", "README.md"})
	requireErrorContains(t, err, "symlink")
}

func TestDocumentLinksIgnoreCodeAndExternalSchemes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.md", strings.Join([]string{
		"# A",
		"[valid](B.md#target)",
		"[web](https://example.com/missing.md)",
		"[mail](mailto:test@example.com)",
		"`[inline](missing.md)`",
		"```md",
		"[fenced](missing.md)",
		"```",
	}, "\n"))
	writeFile(t, root, "B.md", "# Target\n")

	count, _, err := validateDocumentLinks(root, []catalogEntry{{Path: "a.md"}, {Path: "B.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("validated local links = %d, want 1", count)
	}
}

func TestDocumentLinksRequireCaseExactFilesAndAnchors(t *testing.T) {
	tests := []struct {
		name      string
		dest      string
		wantError string
	}{
		{name: "file", dest: "b.md", wantError: "path case does not match"},
		{name: "anchor", dest: "B.md#Target", wantError: "does not case-exactly match"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "a.md", "# A\n[bad]("+test.dest+")\n")
			writeFile(t, root, "B.md", "# Target\n")
			_, _, err := validateDocumentLinks(root, []catalogEntry{{Path: "a.md"}, {Path: "B.md"}})
			requireErrorContains(t, err, test.wantError)
		})
	}
}

func TestDocumentLinksUseRenderedHeadingTextForAnchors(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.md", strings.Join([]string{
		"# A",
		"[link](B.md#foo--bar)",
		"[autolink](B.md#httpsexamplecom)",
		"[code](B.md#footargetmd)",
	}, "\n"))
	writeFile(t, root, "B.md", strings.Join([]string{
		"# [Foo](target.md) &amp; <em>bar</em>",
		"## <https://example.com>",
		"## `[Foo](target.md)`",
	}, "\n"))
	writeFile(t, root, "target.md", "# Target\n")
	if _, _, err := validateDocumentLinks(root, []catalogEntry{{Path: "a.md"}, {Path: "B.md"}}); err != nil {
		t.Fatal(err)
	}
}

func TestRequiredAnchorsAcceptExplicitCompatibilityAnchor(t *testing.T) {
	documents := map[string]markdownDocument{
		"guide.md": parseMarkdown("<a id=\"old-route\"></a>\n\n# New route\n"),
	}
	entries := []catalogEntry{{Path: "guide.md", RequiredAnchors: []string{"old-route"}}}
	if err := validateRequiredAnchors(entries, documents); err != nil {
		t.Fatal(err)
	}
	entries[0].RequiredAnchors = []string{"missing"}
	requireErrorContains(t, validateRequiredAnchors(entries, documents), "missing required anchor")
}

func TestDocumentLinksRejectSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.md", "# A\n[bad](linked.md)\n")
	writeFile(t, root, "target.md", "# Target\n")
	if err := os.Symlink("target.md", filepath.Join(root, "linked.md")); err != nil {
		t.Fatal(err)
	}
	_, _, err := validateDocumentLinks(root, []catalogEntry{{Path: "a.md"}})
	requireErrorContains(t, err, "symlink")
}

func TestLandingPagesRequireRelativeEdgesAndReachableGraph(t *testing.T) {
	entries := []catalogEntry{
		{Path: "README.md"},
		{Path: "docs/README.md", LandingPage: "README.md"},
		{Path: "docs/topic.md", LandingPage: "docs/README.md"},
	}
	valid := map[string]markdownDocument{
		"README.md":      {Links: []markdownLink{{Local: true, Relative: true, Path: "docs/README.md"}}},
		"docs/README.md": {Links: []markdownLink{{Local: true, Relative: true, Path: "docs/topic.md"}}},
	}
	if err := validateLandingPages(entries, valid); err != nil {
		t.Fatalf("valid graph: %v", err)
	}

	absolute := cloneDocuments(valid)
	absolute["docs/README.md"] = markdownDocument{Links: []markdownLink{{Local: true, Path: "docs/topic.md"}}}
	requireErrorContains(t, validateLandingPages(entries, absolute), "does not contain a real relative link")

	cycleEntries := []catalogEntry{
		{Path: "README.md"},
		{Path: "a.md", LandingPage: "b.md"},
		{Path: "b.md", LandingPage: "a.md"},
	}
	cycleDocuments := map[string]markdownDocument{
		"a.md": {Links: []markdownLink{{Local: true, Relative: true, Path: "b.md"}}},
		"b.md": {Links: []markdownLink{{Local: true, Relative: true, Path: "a.md"}}},
	}
	requireErrorContains(t, validateLandingPages(cycleEntries, cycleDocuments), "contains a cycle")
}

func TestReadmeTranslationLinkParity(t *testing.T) {
	entries := []catalogEntry{
		{Path: "README.md", Language: "en"},
		{Path: "README.ru.md", LandingPage: "README.md", Language: "ru", TranslationOf: "README.md"},
	}
	docLink := markdownLink{Destination: "docs/start.md", Local: true, Relative: true, Path: "docs/start.md"}
	parsed := map[string]markdownDocument{
		"README.md": {Links: []markdownLink{
			{Destination: "README.ru.md", Local: true, Relative: true, Path: "README.ru.md"},
			docLink,
		}},
		"README.ru.md": {Links: []markdownLink{
			{Destination: "README.md", Local: true, Relative: true, Path: "README.md"},
			docLink,
		}},
	}
	if err := validateTranslations(entries, parsed); err != nil {
		t.Fatalf("matching translations: %v", err)
	}
	parsed["README.ru.md"] = markdownDocument{Links: []markdownLink{
		{Destination: "README.md", Local: true, Relative: true, Path: "README.md"},
	}}
	requireErrorContains(t, validateTranslations(entries, parsed), "link parity differs")
}

func TestTranslationsRequireTargetsAndMutualLanguageSwitch(t *testing.T) {
	entries := []catalogEntry{
		{Path: "README.md", Language: "en"},
		{Path: "README.ru.md", LandingPage: "README.md", Language: "ru", TranslationOf: "README.md"},
	}
	parsed := map[string]markdownDocument{
		"README.md": {Links: []markdownLink{
			{Destination: "README.ru.md", Local: true, Relative: true, Path: "README.ru.md"},
		}},
		"README.ru.md": {},
	}
	requireErrorContains(t, validateTranslations(entries, parsed), "language switch must be mutual")

	entries[1].TranslationOf = ""
	requireErrorContains(t, validateTranslations(entries, parsed), "requires translation_of")

	entries[1].TranslationOf = "missing.md"
	requireErrorContains(t, validateTranslations(entries, parsed), "does not point to one canonical English document")
}

func TestContext7RejectsSelectedMaintainerDocuments(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "README.md", "# Root\n")
	writeFile(t, root, "AGENTS.md", "# Agents\n")
	writeFile(t, root, "docs/maintainer.md", "# Maintainer\n")
	writeFile(t, root, "context7.json", `{"folders":["docs"],"excludeFolders":[],"excludeFiles":[]}`)
	entries := map[string]catalogEntry{
		"README.md":          {Path: "README.md", Lane: "start"},
		"AGENTS.md":          {Path: "AGENTS.md", Lane: "maintainers"},
		"docs/maintainer.md": {Path: "docs/maintainer.md", Lane: "maintainers"},
	}
	requireErrorContains(t, validateContext7(root, entries), "selects maintainer document")

	writeFile(t, root, "context7.json", `{"folders":["docs"],"excludeFolders":[],"excludeFiles":["AGENTS.md","maintainer.md"]}`)
	if err := validateContext7(root, entries); err != nil {
		t.Fatalf("excluded maintainer document: %v", err)
	}
}

func TestContext7DirectoryExclusionsPreserveRootScopeAndPathGlobs(t *testing.T) {
	if context7ExcludedDirectory("docs/internal", []string{"./internal"}) {
		t.Fatal("root-specific exclusion matched a nested directory")
	}
	if !context7ExcludedDirectory("internal", []string{"./internal"}) {
		t.Fatal("root-specific exclusion did not match the root directory")
	}
	if !context7ExcludedDirectory("docs/reference/internal", []string{"docs/**/internal"}) {
		t.Fatal("path-shaped glob did not match a nested directory")
	}
}

func inventoryFixture(t *testing.T) (string, catalog, []string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "README.md", "# Root\n")
	writeFile(t, root, "README.ru.md", "# Root RU\n")
	value := catalog{SchemaVersion: 1, Documents: []catalogEntry{
		{Path: "README.md", Lane: "start", Topic: "root", Language: "en"},
		{Path: "README.ru.md", Lane: "start", Topic: "root-ru", LandingPage: "README.md", Language: "ru", TranslationOf: "README.md"},
	}, Exclusions: []exclusionRule{}}
	return root, value, []string{"README.md", "README.ru.md"}
}

func cloneDocuments(source map[string]markdownDocument) map[string]markdownDocument {
	result := make(map[string]markdownDocument, len(source))
	for path, document := range source {
		result[path] = document
	}
	return result
}

func writeFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func requireErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}
