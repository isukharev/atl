package agenteval

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

func TestVerifyATLCapabilityCatalogUsesExactBoundedOfflineCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	t.Setenv("ATL_JIRA_PAT", "must-not-reach-child")
	t.Setenv("ATL_CONFLUENCE_URL", "https://must-not-reach-child.invalid")
	root := t.TempDir()
	catalogPath := filepath.Join(root, "catalog.json")
	if err := os.WriteFile(catalogPath, pinnedCapabilityCatalogJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "atl")
	script := `#!/bin/sh
[ "$#" -eq 3 ] || exit 41
[ "$1" = "capabilities" ] || exit 42
[ "$2" = "-o" ] || exit 43
[ "$3" = "json" ] || exit 44
[ "$ATL_NO_UPDATE" = "1" ] || exit 45
[ "$ATL_READ_ONLY" = "1" ] || exit 46
[ -z "$ATL_JIRA_PAT" ] || exit 47
[ -z "$ATL_CONFLUENCE_URL" ] || exit 48
/bin/cat "` + catalogPath + `"
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := VerifyATLCapabilityCatalog(context.Background(), binary, attemptLedgerRootForTest(t)); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyATLCapabilityCatalogRejectsSemanticDriftAndTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	root := t.TempDir()
	drifted := bytes.Replace(pinnedCapabilityCatalogJSON, []byte(`"summary": "`), []byte(`"summary": "drift `), 1)
	catalogPath := filepath.Join(root, "catalog.json")
	if err := os.WriteFile(catalogPath, drifted, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "atl")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n/bin/cat \""+catalogPath+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := VerifyATLCapabilityCatalog(context.Background(), binary, attemptLedgerRootForTest(t)); err == nil || !strings.Contains(err.Error(), "differs from the pinned") {
		t.Fatalf("semantic drift error=%v", err)
	}

	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexec /bin/sleep 10\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	ledgerRoot := attemptLedgerRootForTest(t)
	if err := VerifyATLCapabilityCatalog(ctx, binary, ledgerRoot); err == nil || !strings.Contains(err.Error(), "did not complete") {
		t.Fatalf("timeout error=%v", err)
	}
	report, err := InspectAttemptLedger(ledgerRoot)
	if err != nil || len(report.Attempts) != 1 || report.Attempts[0].State != string(lifecycle.StateTimedOut) {
		t.Fatalf("timeout lifecycle=%+v err=%v", report, err)
	}

	line := strings.Repeat("x", 127) + "\n"
	for name, script := range map[string]string{
		"stdout": "#!/bin/sh\ni=0; while [ \"$i\" -lt 9000 ]; do printf '%s' '" + line + "'; i=$((i + 1)); done\n",
		"stderr": "#!/bin/sh\ni=0; while [ \"$i\" -lt 600 ]; do printf '%s' '" + line + "' >&2; i=$((i + 1)); done\n",
	} {
		t.Run(name+" overflow", func(t *testing.T) {
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := VerifyATLCapabilityCatalog(context.Background(), binary, attemptLedgerRootForTest(t)); err == nil || !strings.Contains(err.Error(), "exceeded its output bound") {
				t.Fatalf("overflow error=%v", err)
			}
		})
	}
}

func TestVerifyATLCapabilityCatalogReportsBoundedFailureDetails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	binary := filepath.Join(t.TempDir(), "atl")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'diagnostic-detail' >&2\nexit 23\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := VerifyATLCapabilityCatalog(context.Background(), binary, attemptLedgerRootForTest(t))
	if err == nil || !strings.Contains(err.Error(), "exit 23") || !strings.Contains(err.Error(), "diagnostic-detail") {
		t.Fatalf("preflight error=%v, want exit code and stderr detail", err)
	}
}

func TestRunHeadlessChecksSelectedATLCatalogInsideDurablePreflight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "atl")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' '{}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sourceCase := filepath.Join(repositoryRoot, "benchmarks", "agent-eval", "jira-artifact-graph-mcp")
	profiledSpec := filepath.Join(sourceCase, "run.mcp.codex.json")
	unprofiledCase := filepath.Join(t.TempDir(), "case")
	if err := os.CopyFS(unprofiledCase, os.DirFS(sourceCase)); err != nil {
		t.Fatal(err)
	}
	unprofiledSpec := filepath.Join(unprofiledCase, "run.mcp.codex.json")
	data, err := os.ReadFile(unprofiledSpec)
	if err != nil {
		t.Fatal(err)
	}
	withoutProfile := bytes.Replace(data, []byte("  \"mcp_service_profile\": \"jira\",\n"), nil, 1)
	if bytes.Equal(withoutProfile, data) {
		t.Fatal("test fixture did not contain the optional MCP service profile")
	}
	if err := os.WriteFile(unprofiledSpec, withoutProfile, 0o600); err != nil {
		t.Fatal(err)
	}

	for name, specPath := range map[string]string{"profiled": profiledSpec, "complete default": unprofiledSpec} {
		t.Run(name, func(t *testing.T) {
			outputRoot := filepath.Join(t.TempDir(), "runs")
			_, err := RunHeadless(context.Background(), RunOptions{
				SpecPath: specPath, OutputRoot: outputRoot, RepositoryRoot: repositoryRoot,
				AgentBinary: current, ATLBinary: binary, PluginRoot: repositoryRoot, WrapperExecutable: current,
				DryRun: true,
			})
			if err == nil || !strings.Contains(err.Error(), "capability catalog") {
				t.Fatalf("preflight error=%v", err)
			}
			report, reportErr := InspectAttemptLedger(filepath.Join(outputRoot, "attempt-ledger"))
			if reportErr != nil || len(report.Attempts) < 2 || report.Attempts[0].State != string(lifecycle.StateFailed) {
				t.Fatalf("failed preflight ledger=%+v err=%v", report, reportErr)
			}
			for _, attempt := range report.Attempts[1:] {
				if attempt.State != string(lifecycle.StateCanceled) {
					t.Fatalf("unstarted attempt was not canceled: %+v", report)
				}
			}
		})
	}
}
