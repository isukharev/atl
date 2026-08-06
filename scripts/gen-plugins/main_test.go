package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/skillmeta"
)

func TestRenderSubstitutesVars(t *testing.T) {
	got, err := render("run `{{atl.setup_cmd}}` and stop.\n", map[string]string{"setup_cmd": "/atl:setup"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "run `/atl:setup` and stop.\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderUnknownVarIsError(t *testing.T) {
	if _, err := render("hello {{atl.no_such_var}}\n", map[string]string{"setup_cmd": "x"}); err == nil {
		t.Fatal("expected an error for an unknown placeholder")
	}
}

func TestRenderDropsEmptyPlaceholderLineWithoutGap(t *testing.T) {
	src := "intro.\n\n{{atl.note}}\n\n## Next\n"
	got, err := render(src, map[string]string{"note": ""})
	if err != nil {
		t.Fatal(err)
	}
	if want := "intro.\n\n## Next\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderKeepsNonEmptyPlaceholderLine(t *testing.T) {
	src := "intro.\n\n{{atl.note}}\n\n## Next\n"
	got, err := render(src, map[string]string{"note": "Invocation: use `$setup`."})
	if err != nil {
		t.Fatal(err)
	}
	if want := "intro.\n\nInvocation: use `$setup`.\n\n## Next\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWithHeaderRespectsFrontmatter(t *testing.T) {
	src := "---\nname: x\n---\nbody\n"
	got, err := withHeader(src, "x/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "---\n") {
		t.Fatalf("frontmatter must stay at byte 0, got %q", got[:20])
	}
	if !strings.Contains(got, "---\n<!-- Generated from skills-src/x/SKILL.md") {
		t.Fatalf("header not placed after frontmatter close: %q", got)
	}
	if !strings.HasSuffix(got, "-->\nbody\n") {
		t.Fatalf("body must follow the header: %q", got)
	}
}

func TestWithHeaderNoFrontmatterGoesOnTop(t *testing.T) {
	got, err := withHeader("body\n", "x/reference/y.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "<!-- Generated from skills-src/x/reference/y.md") {
		t.Fatalf("header must lead the file: %q", got)
	}
}

func TestPlatformVarSetsAreComplete(t *testing.T) {
	// Every platform must define the same variable names, or a source using a
	// var would render on one platform and error on the other.
	base := platforms[0].vars
	for _, pl := range platforms[1:] {
		for k := range base {
			if _, ok := pl.vars[k]; !ok {
				t.Errorf("platform %s is missing var %q", pl.name, k)
			}
		}
		for k := range pl.vars {
			if _, ok := base[k]; !ok {
				t.Errorf("platform %s defines extra var %q missing from %s", pl.name, k, platforms[0].name)
			}
		}
	}
}

func TestVerifyPublishedSkillTreeRejectsExecutableFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "SKILL.md")
	data := []byte("generated\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := verifyPublishedSkillTree(root, []renderedFile{{rel: "SKILL.md", data: data}}); err == nil {
		t.Fatal("executable generated skill file passed publication verification")
	}
}

func TestPlatformPluginUpdateCommands(t *testing.T) {
	updates := map[string]string{}
	for _, pl := range platforms {
		updates[pl.name] = pl.vars["plugin_update_instructions"]
	}

	if got, want := updates["claude"], "Use Claude Code's `/plugin update atl` command."; got != want {
		t.Fatalf("Claude plugin update instructions = %q, want %q", got, want)
	}
	const oldCodexCommand = "codex plugin update atl"
	if strings.Contains(updates["codex"], oldCodexCommand) {
		t.Fatalf("Codex plugin refresh must not use unsupported command %q", oldCodexCommand)
	}
	for _, want := range []string{
		"Run `codex plugin marketplace upgrade atl --json`.",
		"If it succeeds, run `codex plugin add atl@atl --json`.",
		"Then start a new Codex chat or CLI session before retrying.",
	} {
		if !strings.Contains(updates["codex"], want) {
			t.Fatalf("Codex plugin refresh instructions %q do not contain %q", updates["codex"], want)
		}
	}
	if strings.Contains(updates["claude"], "```sh") || strings.Contains(updates["codex"], "```sh") {
		t.Fatal("interactive plugin refresh instructions must not be mislabeled as a shell block")
	}
}

func TestRenderStrayPlaceholderTyposAreErrors(t *testing.T) {
	vars := map[string]string{"setup_cmd": "/atl:setup"}
	for _, src := range []string{
		"run {{atl.Setup_cmd}} now\n",   // wrong case
		"run {{ atl.setup_cmd }} now\n", // inner spaces
		"run {{atl.setup_cmd }} now\n",  // trailing space
	} {
		if _, err := render(src, vars); err == nil {
			t.Errorf("typo %q must be a hard error, rendered fine", src)
		}
	}
}

func TestRenderNeverTouchesBlankLinesInContent(t *testing.T) {
	src := "```\nline1\n\n\nline2\n```\ntail\n\n\n"
	got, err := render(src, map[string]string{"setup_cmd": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got != src {
		t.Fatalf("content without placeholders must pass through verbatim:\ngot  %q\nwant %q", got, src)
	}
}

func TestWithHeaderUnterminatedFrontmatterIsError(t *testing.T) {
	if _, err := withHeader("---\nname: x\nbody with no close\n", "x/SKILL.md"); err == nil {
		t.Fatal("unterminated frontmatter must be a hard error")
	}
}

func TestRenderFileSkipsRoutingMetadata(t *testing.T) {
	for _, platform := range platforms {
		got, err := renderFile([]byte(`{"schema_version":1}`), skillmeta.RoutingFileName, platform)
		if err != nil || got != nil {
			t.Fatalf("platform %s: output=%q err=%v", platform.name, got, err)
		}
	}
}

func TestBuildCodexSkillCatalogIsDeterministicAndComplete(t *testing.T) {
	catalog := skillmeta.Catalog{Skills: []skillmeta.Skill{
		{Name: "zeta", OpenAI: skillmeta.OpenAI{AllowImplicitInvocation: false}},
		{Name: "alpha", OpenAI: skillmeta.OpenAI{AllowImplicitInvocation: true}},
	}}
	rendered := []renderedFile{
		{rel: filepath.Join("zeta", "SKILL.md"), data: []byte("zeta\n")},
		{rel: filepath.Join("alpha", "agents", "openai.yaml"), data: []byte("implicit: true\n")},
		{rel: filepath.Join("alpha", "SKILL.md"), data: []byte("alpha\n")},
	}

	first, err := buildCodexSkillCatalog(catalog, rendered)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildCodexSkillCatalog(catalog, append([]renderedFile(nil), rendered...))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || len(first) == 0 || first[len(first)-1] != '\n' {
		t.Fatal("catalog encoding is not deterministic newline-terminated JSON")
	}

	var got codexSkillCatalog
	if err := json.Unmarshal(first, &got); err != nil {
		t.Fatal(err)
	}
	wantSkills := []codexSkillCatalogSkill{
		{Name: "alpha", AllowImplicitInvocation: true},
		{Name: "zeta", AllowImplicitInvocation: false},
	}
	if got.SchemaVersion != codexCatalogSchema || !reflect.DeepEqual(got.Skills, wantSkills) {
		t.Fatalf("catalog semantics=%+v schema=%d", got.Skills, got.SchemaVersion)
	}
	wantPaths := []string{"alpha/SKILL.md", "alpha/agents/openai.yaml", "zeta/SKILL.md"}
	if len(got.Files) != len(wantPaths) {
		t.Fatalf("catalog files=%d, want %d", len(got.Files), len(wantPaths))
	}
	byPath := make(map[string][]byte, len(rendered))
	for _, file := range rendered {
		byPath[filepath.ToSlash(file.rel)] = file.data
	}
	for index, file := range got.Files {
		if file.Path != wantPaths[index] {
			t.Fatalf("catalog file[%d]=%q, want %q", index, file.Path, wantPaths[index])
		}
		digest := sha256.Sum256(byPath[file.Path])
		if file.SHA256 != hex.EncodeToString(digest[:]) {
			t.Fatalf("catalog digest for %q does not cover exact rendered bytes", file.Path)
		}
	}
}

func TestBuildCodexSkillCatalogRejectsDuplicateOrEscapingInventory(t *testing.T) {
	validCatalog := skillmeta.Catalog{Skills: []skillmeta.Skill{{Name: "demo"}}}
	validFiles := []renderedFile{{rel: "demo/SKILL.md", data: []byte("demo")}}
	for name, input := range map[string]struct {
		catalog skillmeta.Catalog
		files   []renderedFile
	}{
		"duplicate skill": {
			catalog: skillmeta.Catalog{Skills: []skillmeta.Skill{{Name: "demo"}, {Name: "demo"}}},
			files:   validFiles,
		},
		"duplicate file": {
			catalog: validCatalog,
			files:   []renderedFile{validFiles[0], validFiles[0]},
		},
		"escaping file": {
			catalog: validCatalog,
			files:   []renderedFile{{rel: filepath.Join("..", "escape"), data: []byte("escape")}},
		},
		"noncanonical file": {
			catalog: validCatalog,
			files: []renderedFile{{
				rel:  "demo" + string(filepath.Separator) + ".." + string(filepath.Separator) + "demo" + string(filepath.Separator) + "SKILL.md",
				data: []byte("demo"),
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := buildCodexSkillCatalog(input.catalog, input.files); err == nil {
				t.Fatal("invalid catalog inventory passed")
			}
		})
	}
}

func TestBuildCodexSkillCatalogEnforcesSchemaV1Bounds(t *testing.T) {
	validCatalog := skillmeta.Catalog{Skills: []skillmeta.Skill{{Name: "demo"}}}
	validFiles := []renderedFile{{rel: "demo/SKILL.md", data: []byte("demo")}}

	tooManySkills := make([]skillmeta.Skill, maxCodexCatalogSkills+1)
	for index := range tooManySkills {
		tooManySkills[index].Name = fmt.Sprintf("skill-%03d", index)
	}
	tooManyFiles := make([]renderedFile, maxCodexCatalogFiles+1)
	for index := range tooManyFiles {
		tooManyFiles[index].rel = fmt.Sprintf("demo/file-%04d.md", index)
	}
	largeChunk := make([]byte, maxCodexSkillFile)
	largeTree := make([]renderedFile, 0, 9)
	for index := 0; index < 8; index++ {
		largeTree = append(largeTree, renderedFile{rel: fmt.Sprintf("demo/large-%d", index), data: largeChunk})
	}
	largeTree = append(largeTree, renderedFile{rel: "demo/overflow", data: []byte("x")})
	largeCatalog := make([]renderedFile, maxCodexCatalogFiles)
	for index := range largeCatalog {
		largeCatalog[index].rel = fmt.Sprintf("demo/%s-%04d", strings.Repeat("a", 400), index)
	}

	for name, input := range map[string]struct {
		catalog skillmeta.Catalog
		files   []renderedFile
	}{
		"skill count":   {catalog: skillmeta.Catalog{Skills: tooManySkills}, files: validFiles},
		"file count":    {catalog: validCatalog, files: tooManyFiles},
		"skill name":    {catalog: skillmeta.Catalog{Skills: []skillmeta.Skill{{Name: strings.Repeat("a", maxCodexSkillName+1)}}}, files: validFiles},
		"file path":     {catalog: validCatalog, files: []renderedFile{{rel: "demo/" + strings.Repeat("a", maxCodexSkillPath), data: []byte("x")}}},
		"file bytes":    {catalog: validCatalog, files: []renderedFile{{rel: "demo/SKILL.md", data: make([]byte, maxCodexSkillFile+1)}}},
		"tree bytes":    {catalog: validCatalog, files: largeTree},
		"catalog bytes": {catalog: validCatalog, files: largeCatalog},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := buildCodexSkillCatalog(input.catalog, input.files); err == nil {
				t.Fatal("schema-v1 bound violation passed")
			}
		})
	}
}

func TestRunPublishesCodexSkillCatalogOutsideProviderSkillRoot(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	writeValidGeneratorSkill(t)
	if err := os.MkdirAll(filepath.Join("plugins", "atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(codexSkillCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	var catalog codexSkillCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.SchemaVersion != codexCatalogSchema || !reflect.DeepEqual(catalog.Skills, []codexSkillCatalogSkill{{Name: "demo", AllowImplicitInvocation: true}}) {
		t.Fatalf("published catalog semantics=%+v schema=%d", catalog.Skills, catalog.SchemaVersion)
	}
	skillRoot := filepath.Join("plugins", "atl", "skills")
	entries, err := os.ReadDir(skillRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "demo" || !entries[0].IsDir() {
		t.Fatalf("provider-visible root contains companion metadata: %+v", entries)
	}
	if _, err := os.Stat(filepath.Join(skillRoot, filepath.Base(codexSkillCatalogPath))); !os.IsNotExist(err) {
		t.Fatalf("catalog entered provider-visible skill root: %v", err)
	}
	wantPaths := []string{"demo/SKILL.md", "demo/agents/openai.yaml"}
	if len(catalog.Files) != len(wantPaths) {
		t.Fatalf("published file inventory=%d, want %d", len(catalog.Files), len(wantPaths))
	}
	for index, file := range catalog.Files {
		if file.Path != wantPaths[index] {
			t.Fatalf("published file[%d]=%q, want %q", index, file.Path, wantPaths[index])
		}
		generated, err := os.ReadFile(filepath.Join(skillRoot, filepath.FromSlash(file.Path)))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(generated)
		if file.SHA256 != hex.EncodeToString(digest[:]) {
			t.Fatalf("published digest for %q does not match generated bytes", file.Path)
		}
	}
}

func TestRunRejectsInvalidCatalogBeforeRemovingOutputs(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	for _, path := range []string{
		filepath.Join(srcRoot, "invalid"),
		filepath.Join("skills", "sentinel"),
		filepath.Join("plugins", "atl", "skills", "sentinel"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		filepath.Join("skills", "sentinel", "keep"),
		filepath.Join("plugins", "atl", "skills", "sentinel", "keep"),
	} {
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := run(); err == nil {
		t.Fatal("invalid source catalog passed")
	}
	for _, path := range []string{
		filepath.Join("skills", "sentinel", "keep"),
		filepath.Join("plugins", "atl", "skills", "sentinel", "keep"),
	} {
		if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
			t.Fatalf("output %s was touched: data=%q err=%v", path, data, err)
		}
	}
}

func TestRunRejectsLateRenderErrorBeforeRemovingOutputs(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	skillRoot := filepath.Join(srcRoot, "demo")
	if err := os.MkdirAll(filepath.Join(skillRoot, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: demo\ndescription: Demonstrate routing. USE WHEN this fixture is selected. DO NOT USE WHEN another fixture applies.\n---\n\n# Demo\n"
	metadata := "interface:\n  display_name: \"Demo\"\n  short_description: \"Demonstrate a synthetic routing fixture\"\n  default_prompt: \"Use $demo for this fixture.\"\npolicy:\n  allow_implicit_invocation: true\n"
	for path, data := range map[string]string{
		filepath.Join(skillRoot, "SKILL.md"):              skill,
		filepath.Join(skillRoot, "agents", "openai.yaml"): metadata,
		filepath.Join(skillRoot, "unexpected.txt"):        "late render error",
		filepath.Join("skills", "keep"):                   "claude sentinel",
		filepath.Join("plugins", "atl", "skills", "keep"): "codex sentinel",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeGeneratorRoutingContract(t)
	if err := run(); err == nil || !strings.Contains(err.Error(), "unexpected file type") {
		t.Fatalf("late render error passed: %v", err)
	}
	for path, want := range map[string]string{
		filepath.Join("skills", "keep"):                   "claude sentinel",
		filepath.Join("plugins", "atl", "skills", "keep"): "codex sentinel",
	} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("output %s was touched: data=%q err=%v", path, data, err)
		}
	}
}

func TestRunRejectsRoutingDriftBeforeRemovingOutputs(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	writeValidGeneratorSkill(t)
	if err := os.WriteFile(filepath.Join(srcRoot, skillmeta.RoutingFileName), []byte(`{"schema_version":1,"skills":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeGeneratorSentinels(t)
	if err := run(); err == nil || !strings.Contains(err.Error(), "must contain") {
		t.Fatalf("invalid routing contract passed: %v", err)
	}
	assertGeneratorSentinels(t)
}

func TestRunRendersTheValidatedSourceSnapshot(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	writeValidGeneratorSkill(t)
	if err := os.MkdirAll(filepath.Join("plugins", "atl"), 0o700); err != nil {
		t.Fatal(err)
	}

	afterSourceSnapshotValidated = func() {
		path := filepath.Join(srcRoot, "demo", "SKILL.md")
		if err := os.WriteFile(path, []byte("changed after validation\n"), 0o600); err != nil {
			t.Fatalf("mutate source: %v", err)
		}
	}
	t.Cleanup(func() { afterSourceSnapshotValidated = nil })
	if err := run(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join("skills", "demo", "SKILL.md"),
		filepath.Join("plugins", "atl", "skills", "demo", "SKILL.md"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("changed after validation")) || !bytes.Contains(data, []byte("# Demo")) {
			t.Fatalf("%s was not rendered from the validated snapshot", path)
		}
	}
}

func TestRunRejectsSymlinkedSourceBeforeRemovingOutputs(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	writeValidGeneratorSkill(t)
	external := filepath.Join(t.TempDir(), "external.md")
	if err := os.WriteFile(external, []byte("external bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	referenceRoot := filepath.Join(srcRoot, "demo", "reference")
	if err := os.MkdirAll(referenceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(referenceRoot, "outside.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	writeGeneratorSentinels(t)

	if err := run(); err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("symlinked source passed: %v", err)
	}
	assertGeneratorSentinels(t)
}

func TestRunRejectsSymlinkedOutputParentBeforeRemovingOutputs(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	writeValidGeneratorSkill(t)
	if err := os.MkdirAll("skills", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("skills", "keep"), []byte("claude sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "keep"), []byte("external sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, "plugins"); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if err := run(); err == nil || !strings.Contains(err.Error(), "output path component plugins is a symlink") {
		t.Fatalf("symlinked output parent passed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join("skills", "keep"))
	if err != nil || string(data) != "claude sentinel" {
		t.Fatalf("first output was touched before validating all roots: data=%q err=%v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(external, "keep"))
	if err != nil || string(data) != "external sentinel" {
		t.Fatalf("external directory was touched: data=%q err=%v", data, err)
	}
}

func TestRunRejectsSymlinkedCatalogDestinationBeforeRemovingOutputs(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	writeValidGeneratorSkill(t)
	writeGeneratorSentinels(t)
	external := filepath.Join(t.TempDir(), "external-catalog.json")
	if err := os.WriteFile(external, []byte("external sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(codexSkillCatalogPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, codexSkillCatalogPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if err := run(); err == nil || !strings.Contains(err.Error(), "catalog output") {
		t.Fatalf("symlinked catalog destination passed: %v", err)
	}
	for path, want := range map[string]string{
		filepath.Join("skills", "keep"):                   "claude sentinel",
		filepath.Join("plugins", "atl", "skills", "keep"): "codex sentinel",
	} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("output %s was touched: data=%q err=%v", path, data, err)
		}
	}
	data, err := os.ReadFile(external)
	if err != nil || string(data) != "external sentinel" {
		t.Fatalf("external catalog target was touched: data=%q err=%v", data, err)
	}
}

func TestRunRejectsReplacedCatalogTemporaryAndPreservesPreviousCompanion(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	writeValidGeneratorSkill(t)
	if err := os.MkdirAll(filepath.Dir(codexSkillCatalogPath), 0o700); err != nil {
		t.Fatal(err)
	}
	const previous = "previous companion\n"
	if err := os.WriteFile(codexSkillCatalogPath, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	afterGeneratedTempClosed = func(name string) {
		temporary := filepath.Join(filepath.Dir(codexSkillCatalogPath), name)
		if removeErr := os.Remove(temporary); removeErr != nil {
			t.Fatal(removeErr)
		}
		if writeErr := os.WriteFile(temporary, []byte("attacker bytes\n"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	t.Cleanup(func() { afterGeneratedTempClosed = nil })
	if err := run(); err == nil || !strings.Contains(err.Error(), "temporary file") {
		t.Fatalf("replaced catalog temporary passed: %v", err)
	}
	data, err := os.ReadFile(codexSkillCatalogPath)
	if err != nil || string(data) != previous {
		t.Fatalf("previous companion was not preserved: data=%q err=%v", data, err)
	}
}

func TestRunRejectsCodexSkillMutationBeforeCatalogPublication(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	writeValidGeneratorSkill(t)
	if err := os.MkdirAll(filepath.Dir(codexSkillCatalogPath), 0o700); err != nil {
		t.Fatal(err)
	}
	const previous = "previous companion\n"
	if err := os.WriteFile(codexSkillCatalogPath, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeCodexCatalogPublish = func() {
		path := filepath.Join("plugins", "atl", "skills", "demo", "SKILL.md")
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if writeErr := os.WriteFile(path, append(data, []byte("changed\n")...), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	t.Cleanup(func() { beforeCodexCatalogPublish = nil })
	if err := run(); err == nil || !strings.Contains(err.Error(), "verify codex skill tree") {
		t.Fatalf("mutated codex tree passed: %v", err)
	}
	data, err := os.ReadFile(codexSkillCatalogPath)
	if err != nil || string(data) != previous {
		t.Fatalf("catalog was published for a mutated tree: data=%q err=%v", data, err)
	}
}

func TestWriteGeneratedFileRestoresPreviousDestinationAfterRenameFailure(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	const name = "catalog.json"
	if err := root.WriteFile(name, []byte("previous\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeGeneratedPublishRename = func(temporary, _ string) {
		if removeErr := root.Remove(temporary); removeErr != nil {
			t.Fatal(removeErr)
		}
	}
	t.Cleanup(func() { beforeGeneratedPublishRename = nil })
	if err := writeGeneratedFile(root, name, []byte("next\n")); err == nil {
		t.Fatal("injected publication failure passed")
	}
	data, err := root.ReadFile(name)
	if err != nil || string(data) != "previous\n" {
		t.Fatalf("previous destination was not restored: data=%q err=%v", data, err)
	}
	if _, err := root.Lstat("." + name + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("backup survived restoration: %v", err)
	}
}

func writeValidGeneratorSkill(t *testing.T) {
	t.Helper()
	skillRoot := filepath.Join(srcRoot, "demo")
	if err := os.MkdirAll(filepath.Join(skillRoot, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: demo\ndescription: Demonstrate routing. USE WHEN this fixture is selected. DO NOT USE WHEN another fixture applies.\n---\n\n# Demo\n"
	metadata := "interface:\n  display_name: \"Demo\"\n  short_description: \"Demonstrate a synthetic routing fixture\"\n  default_prompt: \"Use $demo for this fixture.\"\npolicy:\n  allow_implicit_invocation: true\n"
	for path, data := range map[string]string{
		filepath.Join(skillRoot, "SKILL.md"):              skill,
		filepath.Join(skillRoot, "agents", "openai.yaml"): metadata,
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeGeneratorRoutingContract(t)
}

func writeGeneratorRoutingContract(t *testing.T) {
	t.Helper()
	registry := `{"schema_version":1,"skills":[{"name":"demo","implicit":true,"owned_task_classes":["synthetic/positive"],"excluded_task_classes":["synthetic/negative"]}]}`
	corpus := `{"schema_version":1,"cases":[{"id":"demo-positive","prompt":"Use the synthetic demo workflow.","task_class":"synthetic/positive","invocation":"implicit","expected_skill":"demo"},{"id":"demo-negative","prompt":"Do not use the synthetic demo workflow.","task_class":"synthetic/negative","invocation":"implicit","expected_skill":null,"forbidden_skills":["demo"]}]}`
	for path, data := range map[string]string{
		filepath.Join(srcRoot, skillmeta.RoutingFileName): registry,
		routingCorpus: corpus,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeGeneratorSentinels(t *testing.T) {
	t.Helper()
	for path, data := range map[string]string{
		filepath.Join("skills", "keep"):                   "claude sentinel",
		filepath.Join("plugins", "atl", "skills", "keep"): "codex sentinel",
		filepath.FromSlash(codexSkillCatalogPath):         "catalog sentinel",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertGeneratorSentinels(t *testing.T) {
	t.Helper()
	for path, want := range map[string]string{
		filepath.Join("skills", "keep"):                   "claude sentinel",
		filepath.Join("plugins", "atl", "skills", "keep"): "codex sentinel",
		filepath.FromSlash(codexSkillCatalogPath):         "catalog sentinel",
	} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("output %s was touched: data=%q err=%v", path, data, err)
		}
	}
}
