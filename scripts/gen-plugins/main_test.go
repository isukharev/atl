package main

import (
	"bytes"
	"os"
	"path/filepath"
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
	} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("output %s was touched: data=%q err=%v", path, data, err)
		}
	}
}
