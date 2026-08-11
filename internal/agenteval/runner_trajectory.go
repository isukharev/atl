package agenteval

import (
	"fmt"
	"os"
)

type headlessTrajectoryCaptureInput struct {
	contract          resolvedRunContract
	transcriptPath    string
	stderrPath        string
	finalPath         string
	mcpConfigPath     string
	externalAuditPath string
	counterPath       string
	guardCounterPath  string
	httpGuardPath     string
	externalCanaries  []string
	backend           *MockBackend
	liveGateway       *LiveGateway
}

type headlessTrajectory struct {
	providerMetrics     ProviderMetrics
	final               []byte
	proxyRecords        []atlProxyRecord
	methods             map[string]int
	unexpected          int
	duplicateRequests   int
	httpMethodsObserved bool
	failedATL           int
	guardDenials        int
	atlInvocations      int
	cliExitCodes        []int
	cliErrorContracts   []CLIErrorContract
	guardSummary        guardDecisionSummary
	externalOutputBytes int64
	externalFamilies    []CapabilityFamilyMetric
}

func captureHeadlessTrajectory(input headlessTrajectoryCaptureInput) (headlessTrajectory, error) {
	transcriptData, err := readBoundedFile(input.transcriptPath, 64<<20)
	if err != nil {
		return headlessTrajectory{}, err
	}
	if input.contract.spec.EffectiveSurface() == SurfaceExternalMCP {
		stderrData, readErr := readBoundedFile(input.stderrPath, 4<<20)
		if readErr != nil {
			return headlessTrajectory{}, readErr
		}
		configData := []byte(nil)
		if input.mcpConfigPath != "" {
			configData, readErr = readBoundedFile(input.mcpConfigPath, 4<<20)
			if readErr != nil {
				return headlessTrajectory{}, readErr
			}
		}
		for _, data := range [][]byte{transcriptData, stderrData, configData} {
			if containsCanary(data, input.externalCanaries) {
				return headlessTrajectory{}, fmt.Errorf("external MCP protected material reached a provider-visible artifact")
			}
		}
	}
	var finalData []byte
	if input.contract.spec.Provider == "codex" {
		finalData, err = readBoundedFile(input.finalPath, 4<<20)
		if err != nil {
			return headlessTrajectory{}, err
		}
	}
	providerMetrics, final, err := ParseProviderOutput(input.contract.spec.Provider, transcriptData, finalData)
	if err != nil {
		return headlessTrajectory{}, err
	}
	trajectory := headlessTrajectory{providerMetrics: providerMetrics, final: final}
	if input.contract.spec.EffectiveSurface() == SurfaceExternalMCP {
		for _, data := range [][]byte{finalData, final} {
			if containsCanary(data, input.externalCanaries) {
				return trajectory, fmt.Errorf("external MCP protected material reached the final provider artifact")
			}
		}
	}
	externalCalls, externalFailures, externalDenials := 0, 0, 0
	var externalOutputBytes int64
	var externalFamilies []CapabilityFamilyMetric
	if input.contract.spec.EffectiveSurface() == SurfaceExternalMCP {
		externalCalls, externalFailures, externalDenials, externalOutputBytes, externalFamilies, err = readExternalMCPAudit(input.externalAuditPath)
		if err != nil {
			return trajectory, err
		}
	}
	proxyRecords, err := readProxyRecords(input.counterPath)
	if err != nil {
		return trajectory, err
	}
	var methods map[string]int
	unexpected := 0
	duplicateRequests := 0
	httpMethodsObserved := false
	if input.backend != nil {
		methods, unexpected, duplicateRequests = input.backend.Summary()
		httpMethodsObserved = true
	} else if input.liveGateway != nil {
		methods, duplicateRequests, httpMethodsObserved, err = closeAndReadLiveGatewayRecords(input.liveGateway)
		if err != nil {
			return trajectory, err
		}
	} else {
		methods, duplicateRequests, httpMethodsObserved, err = readLiveHTTPRecords(input.httpGuardPath)
		if err != nil {
			return trajectory, err
		}
	}
	if input.contract.spec.Provider == "claude-code" {
		if err := writePrivateFile(input.finalPath, append(append([]byte(nil), final...), '\n')); err != nil {
			return trajectory, err
		}
	} else if err := os.Chmod(input.finalPath, 0o600); err != nil {
		return trajectory, err
	}
	var failedATL int
	cliExitCodes := make([]int, 0, len(proxyRecords))
	cliErrorContracts := make([]CLIErrorContract, 0, len(proxyRecords))
	for _, record := range proxyRecords {
		cliExitCodes = append(cliExitCodes, record.ExitCode)
		if record.ExitCode != 0 {
			failedATL++
		}
		errorContract, classified, contractErr := record.errorContract()
		if contractErr != nil {
			return trajectory, contractErr
		}
		if classified {
			cliErrorContracts = append(cliErrorContracts, errorContract)
		}
	}
	guardSummary, err := readGuardDecisionSummary(input.guardCounterPath)
	if err != nil {
		return trajectory, err
	}
	guardDenials := guardSummary.Denials
	atlInvocations := len(proxyRecords) + providerMetrics.MCPToolCalls
	failedATL += providerMetrics.FailedMCPToolCalls
	if input.contract.spec.EffectiveSurface() == SurfaceExternalMCP {
		atlInvocations = externalCalls
		failedATL = externalFailures
		guardDenials += externalDenials
		proxyRecords = nil
		cliExitCodes = nil
		cliErrorContracts = nil
		providerMetrics.MCPToolCalls = externalCalls
		providerMetrics.FailedMCPToolCalls = externalFailures
	}
	trajectory.proxyRecords = proxyRecords
	trajectory.methods = methods
	trajectory.unexpected = unexpected
	trajectory.duplicateRequests = duplicateRequests
	trajectory.httpMethodsObserved = httpMethodsObserved
	trajectory.failedATL = failedATL
	trajectory.guardDenials = guardDenials
	trajectory.atlInvocations = atlInvocations
	trajectory.cliExitCodes = cliExitCodes
	trajectory.cliErrorContracts = cliErrorContracts
	trajectory.guardSummary = guardSummary
	trajectory.externalOutputBytes = externalOutputBytes
	trajectory.externalFamilies = externalFamilies
	return trajectory, nil
}
