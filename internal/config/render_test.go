package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLocal creates <root>/.atl/config.json with the given raw JSON.
func writeLocal(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".atl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadLocalMissingFile(t *testing.T) {
	lc, warns, err := LoadLocal(t.TempDir())
	if err != nil {
		t.Fatalf("missing file: err = %v, want nil", err)
	}
	if lc != nil || warns != nil {
		t.Fatalf("missing file: got (%v, %v), want (nil, nil)", lc, warns)
	}
}

func TestLoadLocalRenderOnly(t *testing.T) {
	root := t.TempDir()
	writeLocal(t, root, `{"render":{"jira":{"profile":"full","include":["sprint"]},"confluence":{"profile":"minimal"}}}`)
	lc, warns, err := LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Errorf("clean file produced warnings: %v", warns)
	}
	if lc == nil || lc.Render == nil || lc.Render.Jira == nil || lc.Render.Jira.Profile != "full" {
		t.Fatalf("render not parsed: %+v", lc)
	}
	if got := lc.Render.Jira.Include; len(got) != 1 || got[0] != "sprint" {
		t.Errorf("include = %v", got)
	}
	if lc.Render.Confluence == nil || lc.Render.Confluence.Profile != "minimal" {
		t.Errorf("confluence profile = %+v", lc.Render.Confluence)
	}
}

// TestLoadLocalMalformedJSONIgnored asserts a corrupt local file is dropped with
// a warning, never fatal — a shared/corrupt repo file must not brick commands.
func TestLoadLocalMalformedJSONIgnored(t *testing.T) {
	root := t.TempDir()
	writeLocal(t, root, `}{ not json`)
	lc, warns, err := LoadLocal(root)
	if err != nil {
		t.Fatalf("malformed local file returned a fatal error: %v", err)
	}
	if lc != nil {
		t.Errorf("malformed file applied: %+v", lc)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "malformed") {
		t.Errorf("warnings = %v, want one malformed-JSON warning", warns)
	}
}

// TestLoadLocalRejectsCredentialKeys is THE security guard: credential-adjacent
// keys in a local file are warned about and never surface in the parsed config.
func TestLoadLocalRejectsCredentialKeys(t *testing.T) {
	root := t.TempDir()
	writeLocal(t, root, `{"confluence_url":"https://evil.example.com","jira_url":"https://evil.example.com","update_base_url":"https://evil.example.com","render":{"jira":{"profile":"full"}}}`)
	lc, warns, err := LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	// The render section still loads; the URL keys are dropped with warnings.
	if lc == nil || lc.Render == nil || lc.Render.Jira == nil || lc.Render.Jira.Profile != "full" {
		t.Fatalf("render should still load: %+v", lc)
	}
	if len(warns) != 3 {
		t.Errorf("want 3 warnings (one per credential key), got %d: %v", len(warns), warns)
	}
	for _, w := range warns {
		if !strings.Contains(w, "confluence_url") && !strings.Contains(w, "jira_url") && !strings.Contains(w, "update_base_url") {
			t.Errorf("unexpected warning: %q", w)
		}
	}
}

// TestEffectiveRenderCredentialGuard asserts the GUARANTEE (not just the warning
// text): a local file carrying backend URLs never changes the effective global
// URLs — LoadLocal can only ever produce a LocalConfig with a Render section.
func TestEffectiveRenderCredentialGuard(t *testing.T) {
	root := t.TempDir()
	writeLocal(t, root, `{"jira_url":"https://evil.example.com","render":{"jira":{"profile":"minimal"}}}`)
	lc, _, err := LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	global := &Config{JiraURL: "https://real.example.com", ConfluenceURL: "https://real.example.com"}
	// The local layer only carries render; there is no field on LocalConfig that
	// could carry a URL, so the global URLs are structurally unreachable from a
	// local file.
	if global.JiraURL != "https://real.example.com" {
		t.Fatalf("global JiraURL mutated: %q", global.JiraURL)
	}
	render, _ := EffectiveRender(global, lc)
	if render.Jira.Profile != "minimal" {
		t.Errorf("render.jira.profile = %q, want minimal (the only local key that applies)", render.Jira.Profile)
	}
}

func TestLoadLocalUnknownKeyWarns(t *testing.T) {
	root := t.TempDir()
	writeLocal(t, root, `{"bogus_top_level":123,"render":{"jira":{"profile":"default"}}}`)
	_, warns, err := LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "bogus_top_level") || !strings.Contains(warns[0], "unknown") {
		t.Errorf("warnings = %v, want one unknown-key warning", warns)
	}
}

func TestLoadLocalInvalidProfileDropped(t *testing.T) {
	root := t.TempDir()
	writeLocal(t, root, `{"render":{"jira":{"profile":"gigantic","include":["sprint"]}}}`)
	lc, warns, err := LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "profile") {
		t.Errorf("warnings = %v, want one invalid-profile warning", warns)
	}
	// Invalid profile dropped to "" (default), but the valid sibling key stays.
	if lc == nil || lc.Render == nil || lc.Render.Jira == nil {
		t.Fatalf("render dropped entirely: %+v", lc)
	}
	if lc.Render.Jira.Profile != "" {
		t.Errorf("invalid profile kept: %q", lc.Render.Jira.Profile)
	}
	if got := lc.Render.Jira.Include; len(got) != 1 || got[0] != "sprint" {
		t.Errorf("valid sibling key dropped: %v", got)
	}
}

// TestEffectiveRenderPrecedence checks per-key merge: local overrides global for
// the keys it sets, global supplies the rest, defaults fill the remainder.
func TestEffectiveRenderPrecedence(t *testing.T) {
	global := &Config{Render: &RenderConfig{
		Jira: &RenderService{Profile: "full", Include: []string{"sprint"}, CustomFields: []string{"customfield_1"}},
	}}
	local := &LocalConfig{Render: &RenderConfig{
		Jira: &RenderService{Profile: "minimal"}, // overrides profile only
	}}
	render, prov := EffectiveRender(global, local)

	if render.Jira.Profile != "minimal" {
		t.Errorf("profile = %q, want minimal (local override)", render.Jira.Profile)
	}
	if prov["render.jira.profile"] != "local" {
		t.Errorf("profile provenance = %q, want local", prov["render.jira.profile"])
	}
	// include not set locally: global value + provenance survive per-key.
	if got := render.Jira.Include; len(got) != 1 || got[0] != "sprint" {
		t.Errorf("include = %v, want [sprint] from global", got)
	}
	if prov["render.jira.include"] != "global" {
		t.Errorf("include provenance = %q, want global", prov["render.jira.include"])
	}
	if got := render.Jira.CustomFields; len(got) != 1 || got[0] != "customfield_1" {
		t.Errorf("custom_fields = %v, want global value", got)
	}
	if prov["render.jira.custom_fields"] != "global" {
		t.Errorf("custom_fields provenance = %q, want global", prov["render.jira.custom_fields"])
	}
	// Confluence untouched -> defaults.
	if render.Confluence.Profile != DefaultProfile {
		t.Errorf("confluence profile = %q, want default", render.Confluence.Profile)
	}
	if prov["render.confluence.profile"] != "default" {
		t.Errorf("confluence provenance = %q, want default", prov["render.confluence.profile"])
	}
}

func TestEffectiveRenderNilInputs(t *testing.T) {
	render, prov := EffectiveRender(nil, nil)
	if render.Jira == nil || render.Jira.Profile != DefaultProfile {
		t.Errorf("jira default = %+v", render.Jira)
	}
	if render.Confluence == nil || render.Confluence.Profile != DefaultProfile {
		t.Errorf("confluence default = %+v", render.Confluence)
	}
	if prov["render.jira.profile"] != "default" {
		t.Errorf("provenance = %v", prov)
	}
}

func TestSetRenderKey(t *testing.T) {
	rc := &RenderConfig{}
	if err := SetRenderKey(rc, "render.jira.profile", "full"); err != nil {
		t.Fatal(err)
	}
	if rc.Jira == nil || rc.Jira.Profile != "full" {
		t.Errorf("profile not set: %+v", rc.Jira)
	}
	if err := SetRenderKey(rc, "render.jira.include", " sprint , epic ,"); err != nil {
		t.Fatal(err)
	}
	if got := rc.Jira.Include; len(got) != 2 || got[0] != "sprint" || got[1] != "epic" {
		t.Errorf("include = %v, want [sprint epic] (trimmed, empties dropped)", got)
	}
	if err := SetRenderKey(rc, "render.confluence.exclude", "comments"); err != nil {
		t.Fatal(err)
	}
	if rc.Confluence == nil || len(rc.Confluence.Exclude) != 1 {
		t.Errorf("confluence exclude not set: %+v", rc.Confluence)
	}
}

func TestSetRenderKeyErrors(t *testing.T) {
	rc := &RenderConfig{}
	cases := []struct {
		key, val      string
		wantNotRender bool
	}{
		{"confluence_url", "x", true},                   // not a render key at all
		{"render.jira", "x", false},                     // too few segments
		{"render.bogus.profile", "x", false},            // bad service
		{"render.jira.bogus", "x", false},               // bad field
		{"render.confluence.custom_fields", "x", false}, // jira-only field
		{"render.jira.profile", "gigantic", false},      // bad profile value
	}
	for _, c := range cases {
		err := SetRenderKey(rc, c.key, c.val)
		if err == nil {
			t.Errorf("%q=%q: want error", c.key, c.val)
			continue
		}
		gotNotRender := err == ErrNotRenderKey
		if gotNotRender != c.wantNotRender {
			t.Errorf("%q: ErrNotRenderKey=%v, want %v (err=%v)", c.key, gotNotRender, c.wantNotRender, err)
		}
	}
}

// TestSaveLocalRoundTrip writes a render-only file and reloads it; the file must
// contain only the render section.
func TestSaveLocalRoundTrip(t *testing.T) {
	root := t.TempDir()
	rc := &RenderConfig{Jira: &RenderService{Profile: "full"}}
	if err := SaveLocal(root, &LocalConfig{Render: rc}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(localConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "url") {
		t.Errorf("local file contains a url key:\n%s", b)
	}
	lc, warns, err := LoadLocal(root)
	if err != nil || len(warns) != 0 {
		t.Fatalf("reload: err=%v warns=%v", err, warns)
	}
	if lc.Render.Jira.Profile != "full" {
		t.Errorf("round-trip profile = %q", lc.Render.Jira.Profile)
	}
}

// TestSaveConfigByteStableWithoutRender pins that adding the render field does
// not add an empty "render":{} to a config that has none.
func TestSaveConfigByteStableWithoutRender(t *testing.T) {
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	c := &Config{ConfluenceURL: "https://c.example.com", JiraURL: "https://j.example.com"}
	if err := Save(c); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "render") {
		t.Errorf("config without render keys emitted a render section:\n%s", b)
	}
}
