package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/plugincontract"
)

func TestMCPPluginStartupValidationPrecedesServeDependencyBoundary(t *testing.T) {
	called := false
	root := newRoot()
	serve, _, err := root.Find([]string{"mcp", "serve"})
	if err != nil {
		t.Fatal(err)
	}
	serve.RunE = func(*cobra.Command, []string) error {
		called = true
		return nil
	}
	privateMarker := "private-interface-marker"
	root.SetArgs([]string{
		"mcp", "serve",
		"--" + plugincontract.InterfaceFlagName + "=" + privateMarker,
		"--" + plugincontract.ProductFlagName + "=1.2.3",
	})
	err = root.ExecuteContext(context.Background())
	if !errors.Is(err, domain.ErrUsage) || codeFor(err) != exitUsage {
		t.Fatalf("error=%v code=%d, want usage/2", err, codeFor(err))
	}
	if called {
		t.Fatal("incompatible contract entered the MCP serve/dependency boundary")
	}
	if strings.Contains(err.Error(), privateMarker) {
		t.Fatalf("startup error disclosed marker value: %v", err)
	}
}

func TestMCPPluginStartupRefusalPrecedesConfigCredentialsAndNetwork(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	privateInterface := "private-interface-value"
	privateProduct := "private-product-value"
	stdout, cobraStderr, err := executeCLIRaw(t, map[string]string{
		"ATL_CONFIG_DIR": configDir,
		"ATL_JIRA_URL":   server.URL,
		"ATL_JIRA_PAT":   privateProduct,
	},
		"mcp", "serve",
		"--"+plugincontract.InterfaceFlagName+"="+privateInterface,
		"--"+plugincontract.ProductFlagName+"="+privateProduct,
	)
	if !errors.Is(err, domain.ErrUsage) || errors.Is(err, domain.ErrConfig) || codeFor(err) != exitUsage {
		t.Fatalf("error=%v code=%d, want pre-config usage/2", err, codeFor(err))
	}
	if stdout != "" || cobraStderr != "" || requests.Load() != 0 {
		t.Fatalf("stdout=%q stderr=%q requests=%d, want no startup effects", stdout, cobraStderr, requests.Load())
	}
	var rendered bytes.Buffer
	writeError(&rendered, "json", err, codeFor(err))
	for _, private := range []string{privateInterface, privateProduct} {
		if strings.Contains(err.Error(), private) || strings.Contains(rendered.String(), private) {
			t.Fatalf("startup refusal disclosed marker or credential %q: error=%v wire=%s", private, err, rendered.String())
		}
	}
	var envelope struct {
		Code int    `json:"code"`
		Kind string `json:"kind"`
	}
	if decodeErr := json.Unmarshal(rendered.Bytes(), &envelope); decodeErr != nil || envelope.Code != exitUsage || envelope.Kind != "usage_error" {
		t.Fatalf("error wire=%s decode=%v", rendered.String(), decodeErr)
	}
}

func TestMCPProductionStartupMarkersAreHidden(t *testing.T) {
	root := newRoot()
	serve, _, err := root.Find([]string{"mcp", "serve"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{plugincontract.InterfaceFlagName, plugincontract.ProductFlagName} {
		flag := serve.Flags().Lookup(name)
		if flag == nil || !flag.Hidden {
			t.Errorf("generated marker --%s is absent or public", name)
		}
	}
}

func TestMCPPluginBinaryProcessMatrix(t *testing.T) {
	currentContract := strconv.Itoa(plugincontract.InterfaceVersion)
	const incompatibleContract = "future-contract-canary"
	const (
		binaryVersion       = "2.0.0"
		matchingProduct     = "2.0.0"
		mismatchingProduct  = "1.9.0"
		privateProductValue = "private-product-canary"
	)
	markers := func(contract, product string) []string {
		return []string{
			"--" + plugincontract.InterfaceFlagName + "=" + contract,
			"--" + plugincontract.ProductFlagName + "=" + product,
		}
	}
	tests := []struct {
		name, binaryMode string
		args             []string
		wantExit         int
		wantReached      bool
		stderrContains   string
		private          string
	}{
		{
			name:        "legacy plugin with legacy binary",
			binaryMode:  "legacy",
			wantReached: true,
		},
		{
			name:           "marked plugin with legacy binary",
			binaryMode:     "legacy",
			args:           markers(currentContract, privateProductValue),
			wantExit:       exitUsage,
			stderrContains: "unknown flag: --" + plugincontract.InterfaceFlagName,
			private:        privateProductValue,
		},
		{
			name:        "legacy plugin or standalone with current binary",
			binaryMode:  "current",
			wantReached: true,
		},
		{
			name:        "compatible marked plugin with matching product",
			binaryMode:  "current",
			args:        markers(currentContract, matchingProduct),
			wantReached: true,
		},
		{
			name:        "compatible marked plugin with product mismatch",
			binaryMode:  "current",
			args:        markers(currentContract, mismatchingProduct),
			wantReached: true,
		},
		{
			name:           "incompatible marked plugin with current binary",
			binaryMode:     "current",
			args:           markers(incompatibleContract, matchingProduct),
			wantExit:       exitUsage,
			stderrContains: "plugin interface contract is incompatible",
			private:        incompatibleContract,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"-test.run=^TestMCPPluginBinaryProcessHelper$", "--", "mcp", "serve"}
			args = append(args, test.args...)
			command := exec.Command(os.Args[0], args...)
			command.Env = append(os.Environ(),
				"ATL_MCP_STARTUP_HELPER=1",
				"ATL_MCP_STARTUP_BINARY_MODE="+test.binaryMode,
				"ATL_MCP_STARTUP_BINARY_VERSION="+binaryVersion,
				"ATL_MCP_STARTUP_EXPECT_REACHED="+strconv.FormatBool(test.wantReached),
			)
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			err := command.Run()
			gotExit := exitOK
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatal(err)
				}
				gotExit = exitErr.ExitCode()
			}
			if gotExit != test.wantExit || stdout.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want exit=%d and clean stdout", gotExit, stdout.String(), stderr.String(), test.wantExit)
			}
			if test.wantExit == exitOK && stderr.Len() != 0 {
				t.Fatalf("successful startup wrote stderr: %q", stderr.String())
			}
			if test.stderrContains != "" && !strings.Contains(stderr.String(), test.stderrContains) {
				t.Fatalf("stderr=%q, want %q", stderr.String(), test.stderrContains)
			}
			if test.private != "" && strings.Contains(stderr.String(), test.private) {
				t.Fatalf("stderr disclosed marker value %q: %s", test.private, stderr.String())
			}
		})
	}
}

func TestMCPPluginBinaryProcessHelper(_ *testing.T) {
	if os.Getenv("ATL_MCP_STARTUP_HELPER") != "1" {
		return
	}
	args := processHelperArgs(os.Args)
	reached := false
	group := &cobra.Command{Use: "mcp"}
	serve := &cobra.Command{Use: "serve", Args: cobra.NoArgs}
	switch os.Getenv("ATL_MCP_STARTUP_BINARY_MODE") {
	case "legacy":
		serve.RunE = func(*cobra.Command, []string) error {
			reached = true
			return nil
		}
	case "current":
		bindMCPPluginStartup(serve, os.Getenv("ATL_MCP_STARTUP_BINARY_VERSION"))
		serve.RunE = func(*cobra.Command, []string) error {
			reached = true
			return nil
		}
	default:
		os.Exit(90)
	}
	group.AddCommand(serve)
	root := mcpStartupTestRoot(group)
	root.SetArgs(args)
	cmd, err := root.ExecuteContextC(context.Background())
	wantReached, parseErr := strconv.ParseBool(os.Getenv("ATL_MCP_STARTUP_EXPECT_REACHED"))
	if parseErr != nil || reached != wantReached {
		os.Exit(91)
	}
	if err != nil {
		code := codeFor(err)
		writeErrorWithCommand(os.Stderr, "json", err, code, recoveryOperation(cmd), cmd)
		os.Exit(code)
	}
	os.Exit(exitOK)
}

func mcpStartupTestRoot(mcpCommand *cobra.Command) *cobra.Command {
	root := &cobra.Command{Use: "atl", SilenceErrors: true, SilenceUsage: true}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageErr("%v", err)
	})
	root.AddCommand(mcpCommand)
	normalizeArgs(root)
	return root
}

func processHelperArgs(args []string) []string {
	for index, arg := range args {
		if arg == "--" {
			return args[index+1:]
		}
	}
	return nil
}
