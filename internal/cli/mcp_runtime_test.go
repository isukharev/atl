package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mcpserver"
	"github.com/isukharev/atl/internal/plugincontract"
)

func TestMCPRunProjectsPersistedSafetyAndIgnoresBackendEnvironment(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"read_only":true,"jira_list_views":{"broken":{"columns":[]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_CONFIG_DIR", configDir)
	t.Setenv("ATL_JIRA_URL", "private-invalid-jira-environment-value")
	t.Setenv("ATL_CONFLUENCE_URL", "private-invalid-confluence-environment-value")
	t.Setenv("ATL_UPDATE_URL", "private-invalid-update-environment-value")
	t.Setenv("ATL_READ_ONLY", "")

	cmd := mcpRuntimeTestCommand(false)
	plugin := plugincontract.StartupStatus{
		InterfaceContract: plugincontract.InterfaceCompatible,
		ProductVersion:    plugincontract.ProductMismatch,
	}
	var called int
	err := runMCPService(cmd, "1.2.3", mcpserver.ServiceJira, plugin,
		func(_ context.Context, version string, profile mcpserver.ServiceProfile, snapshot mcpserver.RuntimeSnapshot) error {
			called++
			if version != "1.2.3" || profile != mcpserver.ServiceJira {
				t.Fatalf("serve version=%q profile=%q", version, profile)
			}
			wantPolicy := mcpserver.RuntimeReadOnlyPolicy{
				ConfiguredReadOnly: true,
				EffectiveReadOnly:  true,
				ReadOnlySource:     mcpserver.RuntimeReadOnlyConfiguration,
			}
			if snapshot.GlobalReadOnlyPolicy != wantPolicy || snapshot.Plugin != (mcpserver.RuntimePluginStatus{
				InterfaceContract: mcpserver.RuntimeInterfaceCompatible,
				ProductVersion:    mcpserver.RuntimeProductMismatch,
			}) {
				t.Fatalf("runtime snapshot=%+v", snapshot)
			}
			return nil
		})
	if err != nil || called != 1 {
		t.Fatalf("run error=%v calls=%d", err, called)
	}
}

func TestMCPRunReadOnlyProjectionPrecedence(t *testing.T) {
	tests := []struct {
		name                  string
		configured, flag, env bool
		wantSource            mcpserver.RuntimeReadOnlySource
	}{
		{name: "none", wantSource: mcpserver.RuntimeReadOnlyNone},
		{name: "configuration", configured: true, wantSource: mcpserver.RuntimeReadOnlyConfiguration},
		{name: "environment", env: true, wantSource: mcpserver.RuntimeReadOnlyEnvironment},
		{name: "environment over configuration", configured: true, env: true, wantSource: mcpserver.RuntimeReadOnlyEnvironment},
		{name: "flag", flag: true, wantSource: mcpserver.RuntimeReadOnlyFlag},
		{name: "flag over all", configured: true, flag: true, env: true, wantSource: mcpserver.RuntimeReadOnlyFlag},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configDir := t.TempDir()
			body := `{"read_only":false}`
			if test.configured {
				body = `{"read_only":true}`
			}
			if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ATL_CONFIG_DIR", configDir)
			if test.env {
				t.Setenv("ATL_READ_ONLY", "1")
			} else {
				t.Setenv("ATL_READ_ONLY", "")
			}
			called := false
			err := runMCPService(mcpRuntimeTestCommand(test.flag), "test", mcpserver.ServiceDefault,
				plugincontract.StartupStatus{InterfaceContract: plugincontract.InterfaceUnverified, ProductVersion: plugincontract.ProductUnverified},
				func(_ context.Context, _ string, _ mcpserver.ServiceProfile, snapshot mcpserver.RuntimeSnapshot) error {
					called = true
					policy := snapshot.GlobalReadOnlyPolicy
					if policy.ConfiguredReadOnly != test.configured || policy.EffectiveReadOnly != (test.configured || test.flag || test.env) || policy.ReadOnlySource != test.wantSource {
						t.Fatalf("policy=%+v", policy)
					}
					return nil
				})
			if err != nil || !called {
				t.Fatalf("run error=%v called=%t", err, called)
			}
		})
	}
}

func TestMCPRunConfigFailuresAreStaticAndPrecedeServe(t *testing.T) {
	privatePathMarker := "private-config-path-marker"
	privateBodyMarker := "private-config-body-marker"
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "malformed json", setup: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"`+privateBodyMarker), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "read failure", setup: func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, "config.json"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configDir := filepath.Join(t.TempDir(), privatePathMarker)
			if err := os.Mkdir(configDir, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ATL_CONFIG_DIR", configDir)
			t.Setenv("ATL_READ_ONLY", "")
			test.setup(t, configDir)
			called := false
			err := runMCPService(mcpRuntimeTestCommand(false), "test", mcpserver.ServiceOffline,
				plugincontract.StartupStatus{InterfaceContract: plugincontract.InterfaceUnverified, ProductVersion: plugincontract.ProductUnverified},
				func(context.Context, string, mcpserver.ServiceProfile, mcpserver.RuntimeSnapshot) error {
					called = true
					return nil
				})
			if !errors.Is(err, domain.ErrConfig) || called {
				t.Fatalf("error=%v called=%t", err, called)
			}
			if got := err.Error(); got != "not configured: persisted MCP safety configuration is unavailable" {
				t.Fatalf("error=%q", got)
			}
			var rendered bytes.Buffer
			writeError(&rendered, "json", err, codeFor(err))
			for _, private := range []string{privatePathMarker, privateBodyMarker} {
				if strings.Contains(err.Error(), private) || strings.Contains(rendered.String(), private) {
					t.Fatalf("config refusal disclosed %q: error=%v wire=%s", private, err, rendered.String())
				}
			}
		})
	}
}

func TestMCPCommandMalformedPersistedConfigProducesNoProtocolBytes(t *testing.T) {
	configDir := t.TempDir()
	privateBodyMarker := "private-malformed-config-value"
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"`+privateBodyMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeCLIRaw(t, map[string]string{"ATL_CONFIG_DIR": configDir}, "mcp", "serve")
	if !errors.Is(err, domain.ErrConfig) || codeFor(err) != exitConfig {
		t.Fatalf("error=%v code=%d", err, codeFor(err))
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("malformed config emitted stdout=%q stderr=%q", stdout, stderr)
	}
	if strings.Contains(err.Error(), privateBodyMarker) {
		t.Fatalf("malformed config disclosed value: %v", err)
	}
}

func TestMCPPluginStartupStatusIsInvocationLocalAndValueFree(t *testing.T) {
	command := &cobra.Command{Use: "serve", Args: cobra.NoArgs}
	command.RunE = func(*cobra.Command, []string) error { return nil }
	status := bindMCPPluginStartup(command, "2.0.0")
	if got := status(); got.InterfaceContract != plugincontract.InterfaceUnverified || got.ProductVersion != plugincontract.ProductUnverified {
		t.Fatalf("initial status=%+v", got)
	}
	command.SetArgs([]string{"--" + plugincontract.InterfaceFlagName + "=1", "--" + plugincontract.ProductFlagName + "=1.9.0"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := status(); got.InterfaceContract != plugincontract.InterfaceCompatible || got.ProductVersion != plugincontract.ProductMismatch {
		t.Fatalf("evaluated status=%+v", got)
	}

	other := &cobra.Command{Use: "serve", Args: cobra.NoArgs}
	other.RunE = func(*cobra.Command, []string) error { return nil }
	otherStatus := bindMCPPluginStartup(other, "2.0.0")
	if got := otherStatus(); got.InterfaceContract != plugincontract.InterfaceUnverified || got.ProductVersion != plugincontract.ProductUnverified {
		t.Fatalf("separate invocation inherited status=%+v", got)
	}

	matching := &cobra.Command{Use: "serve", Args: cobra.NoArgs}
	matching.RunE = func(*cobra.Command, []string) error { return nil }
	matchingStatus := bindMCPPluginStartup(matching, "2.0.0")
	matching.SetArgs([]string{"--" + plugincontract.InterfaceFlagName + "=1", "--" + plugincontract.ProductFlagName + "=2.0.0"})
	if err := matching.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := matchingStatus(); got.InterfaceContract != plugincontract.InterfaceCompatible || got.ProductVersion != plugincontract.ProductMatch {
		t.Fatalf("matching generated markers status=%+v", got)
	}
}

func mcpRuntimeTestCommand(readOnly bool) *cobra.Command {
	cmd := &cobra.Command{Use: "serve"}
	runtime := &invocationRuntime{readOnly: readOnly}
	cmd.SetContext(context.WithValue(context.Background(), invocationRuntimeContextKey{}, runtime))
	return cmd
}
