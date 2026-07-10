package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigSetRenderGlobal persists a dotted render key to the global config.
func TestConfigSetRenderGlobal(t *testing.T) {
	cfgDir := t.TempDir()
	out, code := runCLI(t, map[string]string{"ATL_CONFIG_DIR": cfgDir}, "config", "set", "render.jira.profile", "full")
	if code != exitOK {
		t.Fatalf("config set: exit %d (out=%q)", code, out)
	}
	b, err := os.ReadFile(filepath.Join(cfgDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"profile": "full"`) {
		t.Errorf("global config missing render key:\n%s", b)
	}
}

// TestConfigSetLocalInsideMirror writes the per-mirror file when run from inside
// a mirror (an .atl marker dir present).
func TestConfigSetLocalInsideMirror(t *testing.T) {
	mirror := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mirror, ".atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(mirror)
	out, code := runCLI(t, nil, "config", "set", "--local", "render.confluence.profile", "minimal")
	if code != exitOK {
		t.Fatalf("config set --local: exit %d (out=%q)", code, out)
	}
	b, err := os.ReadFile(filepath.Join(mirror, ".atl", "config.json"))
	if err != nil {
		t.Fatalf("local file not written: %v", err)
	}
	if !strings.Contains(string(b), `"profile": "minimal"`) {
		t.Errorf("local file missing render key:\n%s", b)
	}
	// The local file must never carry a URL/credential key.
	if strings.Contains(string(b), "url") {
		t.Errorf("local file leaked a url key:\n%s", b)
	}
}

// TestConfigSetLocalIntoRoot targets a root explicitly with --into, creating the
// .atl dir there.
func TestConfigSetLocalIntoRoot(t *testing.T) {
	root := t.TempDir()
	out, code := runCLI(t, nil, "config", "set", "--local", "--into", root, "render.jira.profile", "full")
	if code != exitOK {
		t.Fatalf("config set --local --into: exit %d (out=%q)", code, out)
	}
	if _, err := os.Stat(filepath.Join(root, ".atl", "config.json")); err != nil {
		t.Fatalf("local file not written under --into root: %v", err)
	}
}

func TestConfigSetLocalJiraFieldViews(t *testing.T) {
	root := t.TempDir()
	value := `[{"id":"customfield_1","label":"Risk","placement":"section","format":"jira_wiki"}]`
	out, code := runCLI(t, nil, "config", "set", "--local", "--into", root, "render.jira.field_views", value)
	if code != exitOK {
		t.Fatalf("config set field_views: exit %d (out=%q)", code, out)
	}
	b, err := os.ReadFile(filepath.Join(root, ".atl", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"placement": "section"`) || !strings.Contains(string(b), `"format": "jira_wiki"`) {
		t.Errorf("local config missing typed field view:\n%s", b)
	}
	_, code = runCLI(t, nil, "config", "set", "--local", "--into", root, "render.jira.field_views", `[{"id":"customfield_1","placement":"frontmatter"}]`)
	if code != exitUsage {
		t.Errorf("invalid field view: exit %d, want %d", code, exitUsage)
	}
}

func TestConfigSetLocalConfluencePageFields(t *testing.T) {
	root := t.TempDir()
	value := `[{"id":"updated","label":"Changed","format":"date"},{"id":"labels","placement":"section"}]`
	out, code := runCLI(t, nil, "config", "set", "--local", "--into", root, "render.confluence.page_fields", value)
	if code != exitOK {
		t.Fatalf("config set page_fields: exit %d (out=%q)", code, out)
	}
	b, err := os.ReadFile(filepath.Join(root, ".atl", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"label": "Changed"`) || !strings.Contains(string(b), `"format": "list"`) {
		t.Errorf("local config missing normalized page fields:\n%s", b)
	}
	_, code = runCLI(t, nil, "config", "set", "--local", "--into", root, "render.jira.page_fields", value)
	if code != exitUsage {
		t.Errorf("Jira accepted Confluence page_fields: exit %d, want %d", code, exitUsage)
	}
}

// TestConfigSetLocalOutsideMirrorExits2 fails clearly when no mirror is found.
func TestConfigSetLocalOutsideMirrorExits2(t *testing.T) {
	dir := t.TempDir() // no .atl anywhere up
	t.Chdir(dir)
	_, code := runCLI(t, nil, "config", "set", "--local", "render.jira.profile", "full")
	if code != exitUsage {
		t.Errorf("outside mirror: exit %d, want %d", code, exitUsage)
	}
}

// TestConfigSetLocalRefusesURL is the CLI-layer security guard: --local rejects a
// backend URL flag with a usage error, never writing it.
func TestConfigSetLocalRefusesURL(t *testing.T) {
	mirror := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mirror, ".atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(mirror)
	_, code := runCLI(t, nil, "config", "set", "--local", "--confluence-url", "https://evil.example.com")
	if code != exitUsage {
		t.Errorf("--local --confluence-url: exit %d, want %d", code, exitUsage)
	}
	if _, err := os.Stat(filepath.Join(mirror, ".atl", "config.json")); !os.IsNotExist(err) {
		t.Errorf("a local file was written despite the refusal")
	}
}

// TestConfigSetUnknownKeyExits2 rejects an unknown dotted key.
func TestConfigSetUnknownKeyExits2(t *testing.T) {
	_, code := runCLI(t, map[string]string{"ATL_CONFIG_DIR": t.TempDir()}, "config", "set", "render.jira.bogus", "x")
	if code != exitUsage {
		t.Errorf("unknown key: exit %d, want %d", code, exitUsage)
	}
}

// TestConfigSetLoneKeyExits2 rejects a key with no value.
func TestConfigSetLoneKeyExits2(t *testing.T) {
	_, code := runCLI(t, map[string]string{"ATL_CONFIG_DIR": t.TempDir()}, "config", "set", "render.jira.profile")
	if code != exitUsage {
		t.Errorf("lone key: exit %d, want %d", code, exitUsage)
	}
}

// TestConfigShowRenderProvenance shows the effective render block and marks a
// local key's provenance as "local" when a local file is active.
func TestConfigShowRenderProvenance(t *testing.T) {
	mirror := t.TempDir()
	atl := filepath.Join(mirror, ".atl")
	if err := os.MkdirAll(atl, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(atl, "config.json"),
		[]byte(`{"render":{"confluence":{"profile":"minimal"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(mirror)
	out, code := runCLI(t, nil, "config", "show")
	if code != exitOK {
		t.Fatalf("config show: exit %d (out=%q)", code, out)
	}
	var got configShowResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if got.Render.Confluence == nil || got.Render.Confluence.Profile != "minimal" {
		t.Errorf("effective confluence profile = %+v, want minimal", got.Render.Confluence)
	}
	if got.RenderProvenance["render.confluence.profile"] != "local" {
		t.Errorf("provenance = %v, want confluence profile = local", got.RenderProvenance)
	}
	if got.LocalConfigPath == "" {
		t.Errorf("local_config_path not set despite an active local file")
	}
	// Defaults must not clutter provenance.
	if _, ok := got.RenderProvenance["render.jira.profile"]; ok {
		t.Errorf("default-sourced key leaked into provenance: %v", got.RenderProvenance)
	}
}

// TestConfigShowNoLocal omits local_config_path/provenance when no mirror is in
// scope, keeping the output backward-compatible.
func TestConfigShowNoLocal(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	out, code := runCLI(t, nil, "config", "show")
	if code != exitOK {
		t.Fatalf("config show: exit %d (out=%q)", code, out)
	}
	var got configShowResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if got.LocalConfigPath != "" {
		t.Errorf("local_config_path = %q, want empty", got.LocalConfigPath)
	}
	if got.RenderProvenance != nil {
		t.Errorf("render_provenance = %v, want nil (all default)", got.RenderProvenance)
	}
	// The effective render block is always present with defaults.
	if got.Render.Jira == nil || got.Render.Jira.Profile != "default" {
		t.Errorf("effective jira default = %+v", got.Render.Jira)
	}
}
