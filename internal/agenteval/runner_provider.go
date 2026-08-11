package agenteval

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"
)

type headlessProviderResources struct {
	backend             *MockBackend
	liveGateway         *LiveGateway
	externalProxy       *ExternalMCPProxy
	commandBroker       *CommandBroker
	backendEnvironment  map[string]string
	providerConfinement ProviderConfinement
	providerBindings    providerCommandBindings
	atlConfigDir        string
	httpGuardPath       string
	cliPolicyPath       string
	brokerManifestPath  string
	externalAuditPath   string
	externalCanaries    []string
	temporaryConfigDir  string
}

func prepareHeadlessProviderResources(parent context.Context, contract resolvedRunContract, bindings runAttemptBindings, layout headlessAttemptLayout) (_ *headlessProviderResources, returnErr error) {
	resources := &headlessProviderResources{
		backendEnvironment: map[string]string{},
		atlConfigDir:       layout.atlConfigDir,
	}
	defer func() {
		if returnErr != nil {
			resources.closeDeferred()
		}
	}()
	if layout.guardedBrokerCLI {
		resources.providerConfinement.RequestDirectory = layout.brokerRequestDirectory
		resources.providerConfinement.ResponseDirectory = layout.brokerResponseDirectory
	}
	if layout.brokerCLI {
		resources.cliPolicyPath = filepath.Join(layout.evalDir, "cli-policy.json")
		cliPolicy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: contract.spec.AllowedCLICommands}
		policyData, err := EncodeCLICommandPolicy(cliPolicy)
		if err != nil {
			return nil, err
		}
		if err := writePrivateFile(resources.cliPolicyPath, policyData); err != nil {
			return nil, err
		}
	}
	if contract.spec.EffectiveBackendMode() == BackendModeSynthetic {
		if contract.fixture == nil {
			return nil, fmt.Errorf("synthetic run has no fixture")
		}
		backend, err := StartMockBackend(*contract.fixture)
		if err != nil {
			return nil, err
		}
		resources.backend = backend
		resources.backendEnvironment = backend.Environment()
		// Backend bindings are harness-owned setup, like installed benchmark
		// skills: they are injected only into the disposable copied workspace
		// after its task contract has been hashed. This keeps existing synthetic
		// mirror fixtures compatible with the product's explicit legacy-mirror
		// migration gate without changing the agent-visible command contract or
		// counting setup as an interface invocation.
		if contract.spec.EffectiveSurface() == SurfaceCLISkill {
			if err := mkdirPrivate(resources.atlConfigDir); err != nil {
				return nil, err
			}
			if err := bindSyntheticWorkspaceMirrors(parent, bindings.atlBinary, layout.workspace, resources.atlConfigDir, resources.backendEnvironment); err != nil {
				return nil, err
			}
		}
	} else {
		atlConfigDir, err := os.MkdirTemp(bindings.scratchRoot, "atl-agent-eval-live-config-")
		if err != nil {
			return nil, err
		}
		if err := os.Chmod(atlConfigDir, 0o700); err != nil {
			_ = os.RemoveAll(atlConfigDir)
			return nil, err
		}
		resources.atlConfigDir = atlConfigDir
		resources.temporaryConfigDir = atlConfigDir
		if contract.spec.ToolTransport == "cli" {
			resources.httpGuardPath = filepath.Join(layout.evalDir, "gateway-audit.jsonl")
			liveGateway, err := startPrivateLiveGateway(bindings.liveConfigDir, resources.atlConfigDir, resources.httpGuardPath, contract.spec, contract.scenario)
			if err != nil {
				return nil, err
			}
			resources.liveGateway = liveGateway
		} else if layout.gatewayBackedMCP {
			// Every private-live internal MCP uses the same credential boundary as
			// the private CLI. Route-less historical specs receive a harness-owned
			// GET/HEAD-only compatibility policy when this gateway starts.
			resources.httpGuardPath = filepath.Join(layout.evalDir, "gateway-audit.jsonl")
			liveGateway, err := startPrivateLiveGateway(bindings.liveConfigDir, resources.atlConfigDir, resources.httpGuardPath, contract.spec, contract.scenario)
			if err != nil {
				return nil, err
			}
			resources.liveGateway = liveGateway
		} else if contract.spec.EffectiveSurface() == SurfaceExternalMCP {
			headers, canaries, err := resolveExternalMCPHeaders(bindings.externalProfile, bindings.liveConfigDir)
			if err != nil {
				return nil, err
			}
			resources.externalAuditPath = filepath.Join(layout.evalDir, "external-mcp-audit.jsonl")
			resources.externalCanaries = append([]string(nil), canaries...)
			externalProxy, err := StartExternalMCPProxy(parent, bindings.externalProfile, headers, canaries, resources.externalAuditPath)
			if err != nil {
				return nil, err
			}
			resources.externalProxy = externalProxy
			endpoint, capability := externalProxy.Endpoint()
			resources.providerBindings.externalMCPServerURL = endpoint
			resources.providerBindings.externalMCPBearerTokenEnv = WrapperEnvExternalMCPToken
			resources.backendEnvironment[WrapperEnvExternalMCPToken] = capability
		}
	}
	if layout.brokerCLI {
		resources.brokerManifestPath = filepath.Join(layout.evalDir, "command-broker.json")
		brokerTimeout := time.Duration(contract.spec.TimeoutSeconds) * time.Second
		if brokerTimeout > 2*time.Minute {
			brokerTimeout = 2 * time.Minute
		}
		brokerEnvironment := map[string]string{
			"ATL_NO_UPDATE": "1", "NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost",
		}
		if layout.privateCLI {
			if !layout.privateLiveWriteCLI {
				brokerEnvironment["ATL_READ_ONLY"] = "1"
			}
			brokerEnvironment["ATL_CONFIG_DIR"] = resources.atlConfigDir
			brokerEnvironment["ATL_MIRROR_ROOT"] = layout.mirrorRoot
		} else {
			for name, value := range resources.backendEnvironment {
				brokerEnvironment[name] = value
			}
			brokerEnvironment["ATL_CONFIG_DIR"] = resources.atlConfigDir
			brokerEnvironment["ATL_MIRROR_ROOT"] = layout.mirrorRoot
			if layout.syntheticBrokerWriteCLI {
				brokerEnvironment[WrapperEnvAllowSyntheticWrites] = "1"
			} else {
				brokerEnvironment["ATL_READ_ONLY"] = "1"
			}
		}
		for _, name := range []string{"LANG", "LC_ALL", "TERM", "TZ"} {
			if value := os.Getenv(name); value != "" {
				brokerEnvironment[name] = value
			}
		}
		maxStdout := contract.scenario.Budgets.MaxOutputBytes
		if maxStdout > 4<<20 {
			maxStdout = 4 << 20
		}
		cliPolicy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: contract.spec.AllowedCLICommands}
		commandBroker, err := StartCommandBroker(CommandBrokerConfig{
			RequestDirectory: layout.brokerRequestDirectory, ResponseDirectory: layout.brokerResponseDirectory,
			ManifestPath: resources.brokerManifestPath,
			RealBinary:   bindings.atlBinary, WorkingDirectory: layout.workspace, Policy: cliPolicy,
			Environment:    flattenEnvironment(brokerEnvironment),
			MaxStdoutBytes: maxStdout, MaxStderrBytes: 64 << 10, CommandTimeout: brokerTimeout,
			AllowSyntheticWrites: layout.syntheticBrokerWriteCLI,
			AllowReviewedWrites:  layout.reviewedWriteCLI,
		})
		if err != nil {
			return nil, err
		}
		resources.commandBroker = commandBroker
	}
	if layout.mcpConfigPath != "" {
		mcpEnvironment := map[string]string{
			"ATL_READ_ONLY":   "1",
			"ATL_NO_UPDATE":   "1",
			"ATL_CONFIG_DIR":  resources.atlConfigDir,
			"ATL_MIRROR_ROOT": layout.mirrorRoot,
		}
		if layout.gatewayBackedMCP {
			// The MCP child must reach only the disposable loopback gateway, so it
			// receives a fixed allowlist instead of the ambient backend environment:
			// no upstream URL or PAT name, no insecure-transport switch, and no HTTP
			// guard file. NO_PROXY keeps the loopback ingress off any ambient proxy.
			mcpEnvironment = gatewayMCPEnvironment(resources.atlConfigDir, layout.mirrorRoot)
			if err := validateGatewayMCPEnvironment(mcpEnvironment); err != nil {
				return nil, err
			}
		} else {
			for name, value := range resources.backendEnvironment {
				mcpEnvironment[name] = value
			}
		}
		if contract.spec.EffectiveSurface() == SurfaceExternalMCP {
			if err := writeClaudeExternalMCPConfig(layout.mcpConfigPath, resources.providerBindings.externalMCPServerURL, resources.backendEnvironment[WrapperEnvExternalMCPToken]); err != nil {
				return nil, err
			}
		} else if err := writeClaudeMCPConfig(layout.mcpConfigPath, bindings.atlBinary, mcpChildArgs(contract.spec), mcpEnvironment); err != nil {
			return nil, err
		}
	}
	return resources, nil
}

func (resources *headlessProviderResources) closeDeferred() {
	if resources == nil {
		return
	}
	if resources.commandBroker != nil {
		_ = resources.commandBroker.Close()
	}
	if resources.liveGateway != nil {
		_ = resources.liveGateway.Close(context.Background())
	}
	if resources.externalProxy != nil {
		_ = resources.externalProxy.closeBounded()
	}
	if resources.temporaryConfigDir != "" {
		_ = os.RemoveAll(resources.temporaryConfigDir)
	}
	if resources.backend != nil {
		resources.backend.Close()
	}
}

type headlessProviderExecutionInput struct {
	contract   resolvedRunContract
	bindings   runAttemptBindings
	layout     headlessAttemptLayout
	resources  *headlessProviderResources
	command    *exec.Cmd
	transcript *os.File
	stderr     *os.File
	ctx        context.Context
	cancel     context.CancelFunc
}

type headlessProviderExecutionSummary struct {
	durationMillis     int64
	brokerCloseErr     error
	gatewayCloseErr    error
	externalCloseErr   error
	timedOut           bool
	guardAborted       bool
	runErr             error
	closeTranscriptErr error
	closeStderrErr     error
	terminationProven  bool
	terminalReceipt    string
}

func executeAndCloseHeadlessProvider(input headlessProviderExecutionInput) headlessProviderExecutionSummary {
	started := time.Now()
	var guardAborted atomic.Bool
	guardStop := make(chan struct{})
	var guardDone chan struct{}
	if requiresCleanGuard(input.contract.spec.Checks) {
		guardDone = make(chan struct{})
		go func() {
			defer close(guardDone)
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-guardStop:
					return
				case <-ticker.C:
					denials, countErr := countGuardDenials(input.layout.guardCounterPath)
					if countErr == nil && denials > 0 {
						guardAborted.Store(true)
						input.cancel()
						return
					}
				}
			}
		}()
	}
	// Persist immediately before spawn: there is no atomic primitive spanning
	// durable storage and exec, so even a failed Start consumes the attempt.
	// Revalidation remains private-CLI-only and occurs after that commitment.
	var revalidateProvider func() error
	isolatedRuntimeCLI := input.layout.isolatedRuntimeCLI
	if isolatedRuntimeCLI {
		revalidateProvider = input.bindings.providerRuntime.verifyPluginPackage
	}
	attemptStage, terminationProven, terminalReceipt, runErr := executeProviderAttemptWithSession(
		input.command, input.bindings.providerAttemptCommitted, revalidateProvider, input.bindings.attemptSession,
	)
	if runErr != nil && attemptStage == providerAttemptStageCommit {
		runErr = fmt.Errorf("persist provider attempt boundary: %w", runErr)
	}
	var brokerCloseErr error
	if input.resources.commandBroker != nil {
		brokerCloseErr = input.resources.commandBroker.Close()
	}
	var gatewayCloseErr error
	if input.resources.liveGateway != nil {
		gatewayCloseErr = input.resources.liveGateway.Close(context.Background())
	}
	var externalCloseErr error
	if input.resources.externalProxy != nil {
		externalCloseErr = input.resources.externalProxy.closeBounded()
	}
	close(guardStop)
	if guardDone != nil {
		<-guardDone
	}
	duration := time.Since(started).Milliseconds()
	closeTranscriptErr := input.transcript.Close()
	closeStderrErr := input.stderr.Close()
	return headlessProviderExecutionSummary{
		durationMillis: duration, brokerCloseErr: brokerCloseErr, gatewayCloseErr: gatewayCloseErr,
		externalCloseErr: externalCloseErr, timedOut: input.ctx.Err() == context.DeadlineExceeded,
		guardAborted: guardAborted.Load(), runErr: runErr,
		closeTranscriptErr: closeTranscriptErr, closeStderrErr: closeStderrErr,
		terminationProven: terminationProven, terminalReceipt: terminalReceipt,
	}
}
