package agenteval

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/agentadapter"
	"github.com/isukharev/atl/internal/agenteval/extension"
)

func TestBuiltInAgentAdapterContractsAreClosedAndAttemptBound(t *testing.T) {
	for _, provider := range []string{"claude-code", "codex"} {
		t.Run(provider, func(t *testing.T) {
			spec := validRunSpec()
			spec.Provider = provider
			spec.ToolTransport = "mcp"
			if provider == "claude-code" {
				spec.Pricing = Pricing{}
			}
			first, firstDigest, err := builtInAgentAdapterContract(spec, strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			if first.AdapterID != provider || len(first.Capabilities) != len(agentadapter.Capabilities()) || !validSHA256(firstDigest) {
				t.Fatalf("contract=%+v digest=%s", first, firstDigest)
			}
			_, binaryDigest, err := builtInAgentAdapterContract(spec, strings.Repeat("b", 64))
			if err != nil || binaryDigest == firstDigest {
				t.Fatalf("binary identity not bound: digest=%s err=%v", binaryDigest, err)
			}
			spec.Model += "-changed"
			_, configurationDigest, err := builtInAgentAdapterContract(spec, strings.Repeat("a", 64))
			if err != nil || configurationDigest == firstDigest {
				t.Fatalf("configuration not bound: digest=%s err=%v", configurationDigest, err)
			}
		})
	}
}

func TestAgentAdapterAdmissionRejectsUnsupportedBeforeUse(t *testing.T) {
	spec := validRunSpec()
	spec.Provider = "claude-code"
	spec.ToolTransport = "mcp"
	spec.Pricing = Pricing{}
	contract, _, err := builtInAgentAdapterContract(spec, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := admitAgentAdapterCapabilities(contract, []agentadapter.CapabilityID{agentadapter.CapabilityActivationDeveloperInstructions}); err == nil {
		t.Fatal("unsupported Claude developer-instruction capability passed admission")
	}
	if err := admitAgentAdapterCapabilities(contract, []agentadapter.CapabilityID{agentadapter.CapabilityMCP}); err != nil {
		t.Fatalf("supported capability rejected: %v", err)
	}
	for _, capability := range []agentadapter.CapabilityID{
		agentadapter.CapabilityCost,
		agentadapter.CapabilityPermissionPolicy,
		agentadapter.CapabilitySingle,
		agentadapter.CapabilityTrajectory,
	} {
		drifted := contract
		drifted.Capabilities = append([]agentadapter.Capability(nil), contract.Capabilities...)
		for index := range drifted.Capabilities {
			if drifted.Capabilities[index].ID == capability {
				drifted.Capabilities[index].Support = agentadapter.SupportUnsupported
			}
		}
		if err := admitAgentAdapterCapabilities(drifted, requiredAgentAdapterCapabilities(spec)); err == nil {
			t.Fatalf("unsupported required capability %q passed admission", capability)
		}
	}
}

func TestProviderRunnerDispatchUsesAdapterRegistry(t *testing.T) {
	files := []string{"provider.go", "provider_schema.go", "runner.go", "runner_attempt.go", "runner_outcome.go", "runner_provider.go", "runner_trajectory.go"}
	for _, path := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.CaseClause:
				for _, expression := range value.List {
					if providerLiteral(expression) {
						t.Errorf("%s contains provider switch at %d", path, parsed.Pos())
					}
				}
			case *ast.BinaryExpr:
				if (value.Op == token.EQL || value.Op == token.NEQ) &&
					(providerLiteral(value.X) || providerLiteral(value.Y)) {
					t.Errorf("%s compares a value to a built-in adapter literal", path)
				}
			}
			return true
		})
	}
}

func TestBuiltInAgentAdapterRegistryIsClosedImmutableAndEquivalent(t *testing.T) {
	registry := builtInAgentAdapterRegistry()
	if got := []string{registry[0].id(), registry[1].id()}; !reflect.DeepEqual(got, []string{"claude-code", "codex"}) {
		t.Fatalf("registry=%v", got)
	}
	registry[0] = nil
	if adapter, err := builtInAgentAdapterFor("claude-code"); err != nil || adapter == nil {
		t.Fatalf("caller mutation changed registry: adapter=%T err=%v", adapter, err)
	}
	if _, err := builtInAgentAdapterFor("unregistered"); err == nil {
		t.Fatal("unregistered adapter was admitted")
	}

	for _, provider := range []string{"claude-code", "codex"} {
		t.Run(provider, func(t *testing.T) {
			spec := validRunSpec()
			spec.Provider = provider
			if provider == "claude-code" {
				spec.Model = "claude-test-1"
				spec.Pricing = Pricing{}
			}
			originalSchema := []byte(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)
			adapter, err := builtInAgentAdapterFor(provider)
			if err != nil {
				t.Fatal(err)
			}
			activationSupport := adapter.capabilitySupport()
			if provider == "claude-code" &&
				(activationSupport[agentadapter.CapabilityActivationNative] != agentadapter.SupportUnsupported ||
					activationSupport[agentadapter.CapabilityActivationForcedInjection] != agentadapter.SupportUnsupported) {
				t.Fatalf("Claude activation treatments were flattened: %v", activationSupport)
			}
			if provider == "codex" &&
				(activationSupport[agentadapter.CapabilityActivationNative] != agentadapter.SupportSupported ||
					activationSupport[agentadapter.CapabilityActivationForcedInjection] != agentadapter.SupportSupported ||
					activationSupport[agentadapter.CapabilityActivationDeveloperInstructions] != agentadapter.SupportSupported) {
				t.Fatalf("Codex activation treatment support drifted: %v", activationSupport)
			}
			projected, err := providerResponseSchema(spec, originalSchema)
			if err != nil {
				t.Fatal(err)
			}
			directSchema, err := adapter.projectResponseSchema(spec, originalSchema)
			if err != nil || !bytes.Equal(projected, directSchema) {
				t.Fatalf("schema projection drift: direct=%s facade=%s err=%v", directSchema, projected, err)
			}
			input := providerCommandInput{spec: spec, agentBinary: "/agent", atlBinary: "/atl", guardPath: "/guard",
				workspace: "/workspace", schemaPath: "/schema", finalPath: "/final", pluginRoot: "/plugin",
				settingsPath: "/settings", mcpConfigPath: "/mcp.json", responseSchema: projected}
			directCommand, err := adapter.buildCommand(input)
			if err != nil {
				t.Fatal(err)
			}
			facadeCommand, err := buildProviderCommand(spec, input.agentBinary, input.atlBinary, input.guardPath, input.workspace,
				input.schemaPath, input.finalPath, input.pluginRoot, input.settingsPath, input.mcpConfigPath,
				input.confinement, originalSchema, input.bindings)
			if err != nil || !reflect.DeepEqual(facadeCommand, directCommand) {
				t.Fatalf("launch projection drift: direct=%+v facade=%+v err=%v", directCommand, facadeCommand, err)
			}

			transcript := []byte(`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":2}}`)
			final := []byte(`{"answer":"ok"}`)
			if provider == "claude-code" {
				transcript = []byte(`{"type":"result","num_turns":1,"duration_ms":2,"usage":{"input_tokens":1,"output_tokens":2},"structured_output":{"answer":"ok"}}`)
				final = nil
			}
			directMetrics, directFinal, err := adapter.parseOutput(transcript, final)
			if err != nil {
				t.Fatal(err)
			}
			facadeMetrics, facadeFinal, err := ParseProviderOutput(provider, transcript, final)
			if err != nil || !reflect.DeepEqual(facadeMetrics, directMetrics) || !bytes.Equal(facadeFinal, directFinal) {
				t.Fatalf("parser drift: direct=%+v/%s facade=%+v/%s err=%v", directMetrics, directFinal,
					facadeMetrics, facadeFinal, err)
			}
		})
	}
}

func TestAgentAdapterProcessReferenceConformanceIsProviderFree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the persistent attempt ledger remains unavailable on Windows")
	}
	executable := buildOutOfPackageExtensionSample(t)
	_, executableDigest, err := stableReadExtensionExecutable(executable)
	if err != nil {
		t.Fatal(err)
	}
	manifest := extensionHostTestManifest(executableDigest)
	manifestData, err := extension.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	support := make(map[agentadapter.CapabilityID]agentadapter.Support, len(agentadapter.Capabilities()))
	for _, capability := range agentadapter.Capabilities() {
		support[capability] = agentadapter.SupportSupported
	}
	contract, err := agentadapter.NewContract(manifest.Component.ID, manifest.Component.Version,
		strings.Repeat("a", 64), executableDigest, strings.Repeat("c", 64), support, nil)
	if err != nil {
		t.Fatal(err)
	}
	sensitiveManifest := manifest
	sensitiveManifest.ConfigurationSchema = []extension.ConfigurationField{{Name: "secret", Kind: extension.ConfigurationBoolean}}
	sensitiveContract := contract
	sensitiveContract.ConfigurationKeys = []agentadapter.ConfigurationKey{{Name: "secret", Sensitive: true}}
	if validateAgentAdapterProcessBinding(sensitiveManifest, sensitiveContract) == nil {
		t.Fatal("process adapter admitted a sensitive configuration key that the transport cannot classify")
	}
	contractData, err := agentadapter.EncodeContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	bundleData, err := EncodeExtensionConformanceBundle(extensionHostTestBundle(manifestData, executableDigest, manifest))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(directory, "manifest.json")
	bundlePath := filepath.Join(directory, "bundle.json")
	contractPath := filepath.Join(directory, "agent-adapter-contract.json")
	for path, data := range map[string][]byte{manifestPath: manifestData, bundlePath: bundleData, contractPath: contractData} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ledgerRoot := filepath.Join(directory, "ledger")
	report, err := VerifyAgentAdapterProtocolFiles(context.Background(), manifestPath, executable, bundlePath, contractPath, ledgerRoot)
	if err != nil || !report.ProtocolConformant || report.Role != extension.RoleAgentAdapter {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	wantAdapterBinding, err := contentMinimizedAttemptDigest("agent-adapter-process-binding",
		[]string{sha256HexBytes(manifestData), sha256HexBytes(contractData)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenAttemptLedgerStore(ledgerRoot)
	if err != nil {
		t.Fatal(err)
	}
	inspections, err := store.InspectAll()
	if err != nil || len(inspections) != len(report.Cases) {
		t.Fatalf("inspections=%d cases=%d err=%v", len(inspections), len(report.Cases), err)
	}
	for _, inspection := range inspections {
		if inspection.Plan.Binding.Identity.AdapterSHA256 != wantAdapterBinding || !inspection.Projection.Terminal {
			t.Fatalf("adapter process attempt is not contract-bound: %+v", inspection)
		}
	}

	contract.AdapterID = "different.agent"
	drifted, err := agentadapter.EncodeContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contractPath, drifted, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAgentAdapterProtocolFiles(context.Background(), manifestPath, executable, bundlePath, contractPath,
		filepath.Join(directory, "drift-ledger")); !errors.Is(err, errExtensionCompatibility) {
		t.Fatalf("identity drift error=%v", err)
	}
}

func providerLiteral(expression ast.Expr) bool {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(literal.Value)
	return err == nil && (value == "codex" || value == "claude-code")
}
