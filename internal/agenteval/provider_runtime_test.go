package agenteval

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProvisionCodexBenchmarkPluginUsesFreshLocalMarketplaceInventory(t *testing.T) {
	requireCodexRuntimePOSIX(t)
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"synthetic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + codexHome})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	capsule, err := newCodexProviderRuntime(scratch, session)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = capsule.Close() }()
	pluginRoot := filepath.Join(t.TempDir(), "plugin")
	writeTestPluginTrees(t, pluginRoot, "0.4.0", "Synthetic plugin.")
	commandLog := filepath.Join(t.TempDir(), "commands")
	binary := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
printf '%s\n' "$*" >>` + shellQuote(commandLog) + `
if [ "$1" = "plugin" ] && [ "$2" = "marketplace" ] && [ "$3" = "add" ]; then
  printf '%s\n' "$4" >"$CODEX_HOME/marketplace-root"
  printf '%s\n' '[plugins."atl@atl"]' 'enabled = true' >"$CODEX_HOME/config.toml"
  exit 0
fi
if [ "$1" = "plugin" ] && [ "$2" = "add" ]; then
  root=$(/bin/cat "$CODEX_HOME/marketplace-root") || exit 5
  installed="$CODEX_HOME/plugins/cache/atl/atl/0.4.0"
  /bin/mkdir -p "$CODEX_HOME/plugins/cache/atl/atl" || exit 5
  /bin/cp -R "$root/plugins/atl" "$installed" || exit 5
  printf '{"pluginId":"atl@atl","name":"atl","marketplaceName":"atl","version":"0.4.0","installedPath":"%s"}\n' "$installed"
  exit 0
fi
if [ "$1" = "plugin" ] && [ "$2" = "list" ]; then
  root=$(/bin/cat "$CODEX_HOME/marketplace-root") || exit 5
  printf '{"installed":[{"pluginId":"atl@atl","name":"atl","marketplaceName":"atl","version":"0.4.0","installed":true,"enabled":true,"source":{"source":"local","path":"%s/plugins/atl"}}]}\n' "$root"
  exit 0
fi
if [ "$1" = "mcp" ] && [ "$2" = "list" ]; then
  atl_enabled=true
  extra_enabled=true
  config=$(/bin/cat "$CODEX_HOME/config.toml") || exit 5
  case "$config" in *'mcp_servers."atl"'*) atl_enabled=false;; esac
  case "$config" in *'mcp_servers."extra-read"'*) extra_enabled=false;; esac
  printf '[{"name":"atl","enabled":%s},{"name":"extra-read","enabled":%s}]\n' "$atl_enabled" "$extra_enabled"
  exit 0
fi
exit 6
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := provisionCodexBenchmarkPlugin(context.Background(), binary, pluginRoot, capsule); err != nil {
		t.Fatal(err)
	}
	commands, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatal(err)
	}
	expected := "plugin marketplace add " + pluginRoot + "\nplugin add atl@atl --json\nplugin list --json\nmcp list --json\nmcp list --json\n"
	if string(commands) != expected {
		t.Fatalf("commands=%q want=%q", commands, expected)
	}
	if _, err := os.Stat(filepath.Join(capsule.Environment()["CODEX_HOME"], "config.toml")); err != nil {
		t.Fatalf("isolated plugin config missing: %v", err)
	}
	config, err := os.ReadFile(filepath.Join(capsule.Environment()["CODEX_HOME"], "config.toml"))
	if err != nil || !strings.Contains(string(config), `[plugins."atl@atl".mcp_servers."atl"]`) || !strings.Contains(string(config), `[plugins."atl@atl".mcp_servers."extra-read"]`) {
		t.Fatalf("effective plugin MCP disable missing: %q err=%v", config, err)
	}
	expectedSkillRoot, err := filepath.EvalSymlinks(filepath.Join(capsule.Environment()["CODEX_HOME"], "plugins", "cache", "atl", "atl", "0.4.0", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	if capsule.PluginSkillRoot() != expectedSkillRoot {
		t.Fatalf("installed skill root=%q", capsule.PluginSkillRoot())
	}
}

func TestProvisionCodexBenchmarkPluginRejectsUnexpectedInventoryWithoutLeakingOutput(t *testing.T) {
	requireCodexRuntimePOSIX(t)
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"synthetic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + codexHome})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	capsule, err := newCodexProviderRuntime(scratch, session)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = capsule.Close() }()
	pluginRoot := filepath.Join(t.TempDir(), "private-path-canary")
	writeTestPluginTrees(t, pluginRoot, "0.4.0", "Synthetic plugin.")
	binary := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
if [ "$2" = "marketplace" ]; then printf '%s\n' "$4" >"$CODEX_HOME/marketplace-root"; printf enabled >"$CODEX_HOME/config.toml"; exit 0; fi
if [ "$2" = "add" ]; then
  root=$(/bin/cat "$CODEX_HOME/marketplace-root") || exit 5
  installed="$CODEX_HOME/plugins/cache/atl/atl/0.4.0"
  /bin/mkdir -p "$CODEX_HOME/plugins/cache/atl/atl" || exit 5
  /bin/cp -R "$root/plugins/atl" "$installed" || exit 5
  printf '{"pluginId":"atl@atl","name":"atl","marketplaceName":"atl","version":"0.4.0","installedPath":"%s"}\n' "$installed"
  exit 0
fi
if [ "$1" = "mcp" ]; then printf '%s\n' '[{"name":"atl","enabled":true}]'; exit 0; fi
printf '%s\n' '{"installed":[{"pluginId":"unexpected@ambient","name":"unexpected","marketplaceName":"ambient","version":"1","installed":true,"enabled":true,"source":{"source":"local","path":"private-path-canary"}}]}'
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	err = provisionCodexBenchmarkPlugin(context.Background(), binary, pluginRoot, capsule)
	if err == nil || strings.Contains(err.Error(), "private-path-canary") || strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("unsafe inventory result: %v", err)
	}
}

func TestDisableCodexBenchmarkPluginMCPRejectsIneffectivePolicy(t *testing.T) {
	requireCodexRuntimePOSIX(t)
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"synthetic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + codexHome})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	capsule, err := newCodexProviderRuntime(scratch, session)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = capsule.Close() }()
	if err := os.WriteFile(filepath.Join(capsule.Environment()["CODEX_HOME"], "config.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "calls")
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf called >>" + shellQuote(marker) + "\nprintf '%s\\n' '[{\"name\":\"private-server-canary\",\"enabled\":true}]'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	err = disableCodexBenchmarkPluginMCP(context.Background(), binary, flattenEnvironment(capsule.Environment()), capsule)
	if err == nil || strings.Contains(err.Error(), "private-server-canary") {
		t.Fatalf("ineffective MCP policy result: %v", err)
	}
	calls, readErr := os.ReadFile(marker)
	if readErr != nil || string(calls) != "calledcalled" {
		t.Fatalf("post-policy verification did not run: calls=%q err=%v", calls, readErr)
	}
}

func TestDisableCodexBenchmarkPluginMCPRejectsDuplicatePostPolicyInventory(t *testing.T) {
	requireCodexRuntimePOSIX(t)
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"synthetic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + codexHome})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	capsule, err := newCodexProviderRuntime(scratch, session)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = capsule.Close() }()
	if err := os.WriteFile(filepath.Join(capsule.Environment()["CODEX_HOME"], "config.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state")
	binary := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
if [ -f ` + shellQuote(state) + ` ]; then
  printf '%s\n' '[{"name":"one","enabled":false},{"name":"one","enabled":false}]'
else
  printf called >` + shellQuote(state) + `
  printf '%s\n' '[{"name":"one","enabled":true},{"name":"two","enabled":true}]'
fi
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := disableCodexBenchmarkPluginMCP(context.Background(), binary, flattenEnvironment(capsule.Environment()), capsule); err == nil {
		t.Fatal("duplicate post-policy inventory was accepted")
	}
}

func TestDisableCodexBenchmarkPluginMCPRequiresExplicitDisabledState(t *testing.T) {
	requireCodexRuntimePOSIX(t)
	for _, after := range []string{
		`[{"name":"one"}]`,
		`[{"name":"one","enabled":null}]`,
	} {
		t.Run(after, func(t *testing.T) {
			home := t.TempDir()
			codexHome := filepath.Join(home, ".codex")
			if err := os.Mkdir(codexHome, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"synthetic"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			scratch := filepath.Join(t.TempDir(), "scratch")
			if err := os.Mkdir(scratch, 0o700); err != nil {
				t.Fatal(err)
			}
			session, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + codexHome})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			capsule, err := newCodexProviderRuntime(scratch, session)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = capsule.Close() }()
			if err := os.WriteFile(filepath.Join(capsule.Environment()["CODEX_HOME"], "config.toml"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(t.TempDir(), "state")
			binary := filepath.Join(t.TempDir(), "codex")
			script := `#!/bin/sh
if [ -f ` + shellQuote(state) + ` ]; then
  printf '%s\n' ` + shellQuote(after) + `
else
  printf called >` + shellQuote(state) + `
  printf '%s\n' '[{"name":"one","enabled":true}]'
fi
`
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := disableCodexBenchmarkPluginMCP(context.Background(), binary, flattenEnvironment(capsule.Environment()), capsule); err == nil {
				t.Fatal("missing explicit disabled state was accepted")
			}
		})
	}
}

func TestProvisionCodexBenchmarkPluginRejectsPackageSymlinkBeforeProviderCommand(t *testing.T) {
	requireCodexRuntimePOSIX(t)
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"synthetic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + codexHome})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	capsule, err := newCodexProviderRuntime(scratch, session)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = capsule.Close() }()
	pluginRoot := filepath.Join(t.TempDir(), "plugin")
	writeTestPluginTrees(t, pluginRoot, "0.4.0", "Synthetic plugin.")
	external := filepath.Join(t.TempDir(), "private-package-canary")
	if err := os.WriteFile(external, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(pluginRoot, "plugins", "atl", ".mcp.json")
	if err := os.Remove(mcpPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, mcpPath); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "provider-called")
	binary := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf called >"+shellQuote(marker)+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err = provisionCodexBenchmarkPlugin(context.Background(), binary, pluginRoot, capsule)
	if err == nil || strings.Contains(err.Error(), "private-package-canary") {
		t.Fatalf("package symlink result: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("provider command ran before package validation: %v", err)
	}
}

func TestCodexProviderRuntimeFailsClosedBeforeAuthLookupOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific ACL fail-closed control")
	}
	privateCanary := "private-auth-path-canary"
	if session, err := newCodexAuthSession([]string{"HOME=C:\\" + privateCanary}); err == nil || session != nil || strings.Contains(err.Error(), privateCanary) {
		t.Fatalf("Windows provider runtime result: session=%v err=%v", session, err)
	}
}

func TestCodexProviderRuntimeProjectsOnlyAuthAndConnectionEnvironment(t *testing.T) {
	requireCodexRuntimePOSIX(t)
	home := t.TempDir()
	codexHome := filepath.Join(home, "ambient-codex")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(codexHome, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	auth := []byte(`{"tokens":{"access_token":"synthetic-auth-secret"}}`)
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), auth, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"AGENTS.md": "hostile-global-instruction", "config.toml": "hostile-config",
		"history.jsonl": "hostile-history",
	} {
		if err := os.WriteFile(filepath.Join(codexHome, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(codexHome, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "skills", "SKILL.md"), []byte("hostile-skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	ambient := []string{
		"HOME=" + home, "CODEX_HOME=" + codexHome,
		"SHELL=/private/ambient-shell",
		"HTTPS_PROXY=http://proxy.invalid", "SSL_CERT_FILE=/certs/ca.pem", "CODEX_CA_CERTIFICATE=/certs/codex.pem",
		"ALL_PROXY=socks5://ambient-proxy.invalid",
		"OPENAI_API_KEY=ambient-secret", "GH_TOKEN=ambient-secret",
	}
	session, err := newCodexAuthSession(ambient)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	capsule, err := newCodexProviderRuntime(scratch, session)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := capsule.root
	environment := capsule.Environment()
	for _, name := range []string{"HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "TMPDIR", "TMP", "TEMP"} {
		inside, pathErr := pathWithin(runtimeRoot, environment[name])
		if pathErr != nil || !inside {
			t.Fatalf("%s escaped runtime capsule", name)
		}
		info, statErr := os.Stat(environment[name])
		if statErr != nil || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
			t.Fatalf("%s is not owner-only: info=%v err=%v", name, info, statErr)
		}
	}
	if environment["HTTPS_PROXY"] != "http://proxy.invalid" || environment["SSL_CERT_FILE"] != "/certs/ca.pem" || environment["CODEX_CA_CERTIFICATE"] != "/certs/codex.pem" {
		t.Fatalf("connection environment=%v", environment)
	}
	if environment["SHELL"] != codexIsolatedShell {
		t.Fatalf("isolated shell=%q", environment["SHELL"])
	}
	for _, name := range []string{"OPENAI_API_KEY", "GH_TOKEN", "CLAUDE_CONFIG_DIR", "ALL_PROXY"} {
		if _, ok := environment[name]; ok {
			t.Fatalf("ambient credential %s survived", name)
		}
	}
	projected, err := os.ReadFile(filepath.Join(environment["CODEX_HOME"], "auth.json"))
	if err != nil || !bytes.Equal(projected, auth) {
		t.Fatalf("auth projection err=%v", err)
	}
	projectedInfo, err := os.Stat(filepath.Join(environment["CODEX_HOME"], "auth.json"))
	if err != nil || (runtime.GOOS != "windows" && projectedInfo.Mode().Perm() != 0o600) {
		t.Fatalf("projected auth mode=%v err=%v", projectedInfo.Mode(), err)
	}
	for _, relative := range []string{"AGENTS.md", "config.toml", "history.jsonl", filepath.Join("skills", "SKILL.md")} {
		if _, err := os.Stat(filepath.Join(environment["CODEX_HOME"], relative)); !os.IsNotExist(err) {
			t.Fatalf("ambient provider state was projected: %s", relative)
		}
	}
	if err := capsule.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runtimeRoot); !os.IsNotExist(err) {
		t.Fatalf("runtime capsule survived cleanup: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(codexHome, "auth.json"))
	if err != nil || !bytes.Equal(source, auth) {
		t.Fatalf("source auth changed err=%v", err)
	}
}

func TestCodexProviderRuntimeRejectsUnsafeAuthWithoutLeakingIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode and symlink assertions are Unix-specific")
	}
	secret := "synthetic-auth-secret-marker"
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, root string)
	}{
		{name: "missing", prepare: func(t *testing.T, _ string) {
			t.Helper()
		}},
		{name: "symlink", prepare: func(t *testing.T, root string) {
			t.Helper()
			target := filepath.Join(filepath.Dir(root), secret+"-target")
			if err := os.WriteFile(target, []byte(`{"token":"x"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(root, "auth.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "group-readable", prepare: func(t *testing.T, root string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, "auth.json"), []byte(`{"token":"x"}`), 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversize", prepare: func(t *testing.T, root string) {
			t.Helper()
			data := append([]byte(`{"token":"`), bytes.Repeat([]byte("x"), codexAuthMaxBytes)...)
			data = append(data, []byte(`"}`)...)
			if err := os.WriteFile(filepath.Join(root, "auth.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "non-object-json", prepare: func(t *testing.T, root string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, "auth.json"), []byte(`["`+secret+`"]`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			codexHome := filepath.Join(home, secret+"-codex")
			if err := os.Mkdir(codexHome, 0o700); err != nil {
				t.Fatal(err)
			}
			test.prepare(t, codexHome)
			scratch := filepath.Join(t.TempDir(), "scratch")
			if err := os.Mkdir(scratch, 0o700); err != nil {
				t.Fatal(err)
			}
			_, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + codexHome})
			if err == nil {
				t.Fatal("unsafe auth was accepted")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), codexHome) {
				t.Fatalf("auth source leaked in error: %v", err)
			}
			entries, readErr := os.ReadDir(scratch)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("failed runtime left scratch residue: entries=%v err=%v", entries, readErr)
			}
		})
	}
}

func TestCodexProviderRuntimeRejectsNonOwnerOnlyAuthRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode assertion is Unix-specific")
	}
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(codexHome, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"synthetic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + codexHome}); err == nil || strings.Contains(err.Error(), codexHome) {
		t.Fatalf("non-owner-only auth root result: %v", err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed runtime left scratch residue: entries=%v err=%v", entries, err)
	}
}

func TestCodexProviderRuntimeRejectsSymlinkedAuthRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink assertion is Unix-specific")
	}
	home := t.TempDir()
	realRoot := filepath.Join(home, "real-codex")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "auth.json"), []byte(`{"token":"synthetic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedRoot := filepath.Join(home, "linked-codex")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + linkedRoot}); err == nil || strings.Contains(err.Error(), linkedRoot) {
		t.Fatalf("symlinked auth root result: %v", err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed runtime left scratch residue: entries=%v err=%v", entries, err)
	}
}

func TestCodexProviderRuntimeFallsBackToHomeAndStaysInPrivateScratch(t *testing.T) {
	requireCodexRuntimePOSIX(t)
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"synthetic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	privateRoot := t.TempDir()
	scratch := filepath.Join(privateRoot, ".ephemeral")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := newCodexAuthSession([]string{"HOME=" + home})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	capsule, err := newCodexProviderRuntime(scratch, session)
	if err != nil {
		t.Fatal(err)
	}
	inside, err := pathWithin(scratch, capsule.root)
	if err != nil || !inside {
		t.Fatal("runtime capsule escaped private scratch")
	}
	if _, err := os.Stat(filepath.Join(capsule.Environment()["CODEX_HOME"], "auth.json")); err != nil {
		t.Fatal(err)
	}
	if err := capsule.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCodexProviderRuntimeCarriesOnlyRefreshedAuthBetweenFreshCapsules(t *testing.T) {
	requireCodexRuntimePOSIX(t)
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"token":"original"}`)
	refreshed := []byte(`{"token":"refreshed"}`)
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + codexHome})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	first, err := newCodexProviderRuntime(scratch, session)
	if err != nil {
		t.Fatal(err)
	}
	firstRoot := first.root
	if err := os.WriteFile(filepath.Join(first.Environment()["CODEX_HOME"], "history.jsonl"), []byte("surface-one-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Environment()["CODEX_HOME"], "auth.json"), refreshed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := newCodexProviderRuntime(scratch, session)
	if err != nil {
		t.Fatal(err)
	}
	secondRoot := second.root
	if firstRoot == secondRoot {
		t.Fatal("provider capsule was reused")
	}
	if _, err := os.Stat(filepath.Join(second.Environment()["CODEX_HOME"], "history.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("state crossed capsule boundary: %v", err)
	}
	projected, err := os.ReadFile(filepath.Join(second.Environment()["CODEX_HOME"], "auth.json"))
	if err != nil || !bytes.Equal(projected, refreshed) {
		t.Fatalf("refreshed auth was not carried forward: %s err=%v", projected, err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(codexHome, "auth.json"))
	if err != nil || !bytes.Equal(source, original) {
		t.Fatalf("ambient auth was mutated: %s err=%v", source, err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("runtime residue: entries=%v err=%v", entries, err)
	}
}

func TestCodexProviderRuntimeRejectsMalformedRefreshedAuthAndCleansCapsule(t *testing.T) {
	requireCodexRuntimePOSIX(t)
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"original"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + codexHome})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	capsule, err := newCodexProviderRuntime(scratch, session)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := capsule.root
	if err := os.WriteFile(filepath.Join(capsule.Environment()["CODEX_HOME"], "auth.json"), []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := capsule.Close(); err == nil || strings.Contains(err.Error(), "original") {
		t.Fatalf("malformed refresh result=%v", err)
	}
	if _, err := os.Stat(runtimeRoot); !os.IsNotExist(err) {
		t.Fatalf("malformed capsule survived cleanup: %v", err)
	}
}

func TestCodexProviderRuntimeDoesNotFollowReplacedCapsuleHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink assertion is Unix-specific")
	}
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"original"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + codexHome})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	capsule, err := newCodexProviderRuntime(scratch, session)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := capsule.root
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "auth.json"), []byte(`{"token":"outside"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	isolatedHome := capsule.Environment()["CODEX_HOME"]
	if err := os.Rename(isolatedHome, isolatedHome+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, isolatedHome); err != nil {
		t.Fatal(err)
	}
	if err := capsule.Close(); err == nil || strings.Contains(err.Error(), external) || strings.Contains(err.Error(), "outside") {
		t.Fatalf("replaced capsule home result=%v", err)
	}
	if _, err := os.Stat(runtimeRoot); !os.IsNotExist(err) {
		t.Fatalf("replaced capsule survived cleanup: %v", err)
	}
}

func TestCodexProviderRuntimeCleanupFailureRetainsRootForRetry(t *testing.T) {
	requireCodexRuntimePOSIX(t)
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"original"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + codexHome})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	capsule, err := newCodexProviderRuntime(scratch, session)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := capsule.root
	originalScratch := capsule.scratchRoot
	capsule.scratchRoot = t.TempDir()
	if err := capsule.Close(); err == nil || capsule.root == "" {
		t.Fatalf("cleanup failure was not retryable: err=%v root=%q", err, capsule.root)
	}
	capsule.scratchRoot = originalScratch
	if err := capsule.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runtimeRoot); !os.IsNotExist(err) {
		t.Fatalf("retry left capsule behind: %v", err)
	}
}

func TestCodexVersionProbeUsesIsolatedRuntimeEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable script is Unix-specific")
	}
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"synthetic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + codexHome})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	capsule, err := newCodexProviderRuntime(scratch, session)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = capsule.Close() }()
	marker := filepath.Join(t.TempDir(), "probe-home")
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf '%s' \"$HOME\" >" + shellQuote(marker) + "\nprintf '%s\\n' codex-test-1\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	version, err := commandVersionWithEnvironment(context.Background(), binary, flattenEnvironment(capsule.Environment()))
	if err != nil || version != "codex-test-1" {
		t.Fatalf("version=%q err=%v", version, err)
	}
	observed, err := os.ReadFile(marker)
	if err != nil || string(observed) != capsule.Environment()["HOME"] {
		t.Fatalf("version probe home=%q err=%v", observed, err)
	}
}

func requireCodexRuntimePOSIX(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("actual Codex runtime isolation fails closed until Windows ACL validation is implemented")
	}
}
