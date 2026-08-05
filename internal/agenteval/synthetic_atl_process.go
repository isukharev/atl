package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const (
	defaultSyntheticATLTimeout        = 10 * time.Second
	defaultSyntheticATLStdoutBytes    = 4 << 20
	defaultSyntheticATLStderrBytes    = 1 << 20
	defaultSyntheticATLMCPBytes       = 8 << 20
	maximumSyntheticATLStdoutBytes    = 16 << 20
	maximumSyntheticATLStderrBytes    = 4 << 20
	maximumSyntheticATLMCPBytes       = 64 << 20
	maximumSyntheticATLProcessTimeout = 15 * time.Minute
)

// SyntheticATLProcessConfig binds evaluator-owned fixture bytes and exact
// CLI/MCP admissions to one selected ATL executable. ScratchRoot must already
// exist as an owner-only directory; the process creates and removes one unique
// owner-only child beneath it. MirrorTemplate, when set, is copied into that
// child before a synthetic MCP server starts; it is never used as the child
// process's mirror root.
type SyntheticATLProcessConfig struct {
	Binary         string
	Fixture        MockFixture
	ScratchRoot    string
	MirrorTemplate string
	// VerifyMCPToolInventory performs the extra bounded tools/list profile
	// attestation before admitted MCP calls. Mirror templates require it; other
	// high-volume synthetic cohorts retain their already-reviewed admission
	// boundary without paying this per-process compatibility cost.
	VerifyMCPToolInventory bool
	CLIPolicy              CLICommandPolicy
	MCPService             string
	MCPInvocations         []MCPInvocation
	Timeout                time.Duration
	MaxStdoutBytes         int64
	MaxStderrBytes         int64
	MaxMCPBytes            int64
}

// SyntheticCLIResult preserves the selected binary's exit status and bounded
// stderr. JSON is populated only for exit zero and is exactly one JSON value.
// A typed nonzero CLI error therefore remains observable without being
// converted into a process-execution failure.
type SyntheticCLIResult struct {
	ExitCode int
	JSON     json.RawMessage
	Stderr   []byte
}

// SyntheticCLIBytesResult preserves the selected binary's bounded stdout and
// stderr byte-for-byte. It deliberately assigns no text, UTF-8, or format
// meaning to either stream.
type SyntheticCLIBytesResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// SyntheticMCPResult preserves both successful and application-error tool
// evidence. Protocol/transport failures remain Go errors; an ATL tool error is
// represented by IsError with its bounded text and optional structured object.
type SyntheticMCPResult struct {
	IsError           bool
	StructuredContent json.RawMessage
	TextContent       []string
}

// SyntheticATLProcessSummary is content-free accounting for one process
// lifecycle. CLI counts use reviewed rule names; MCP counts use tool names and
// deliberately omit arguments.
type SyntheticATLProcessSummary struct {
	HTTPMethods        map[string]int
	UnexpectedRequests int
	DuplicateRequests  int
	CLIInvocations     map[string]int
	MCPInvocations     map[string]int
}

type selectedSyntheticATLBinary struct {
	selectedPath  string
	canonicalPath string
	executionPath string
	sha256        string
}

// SyntheticATLProcess owns one bounded selected-binary lifecycle and its
// evaluator synthetic backend. Call Close even after a CLI or MCP failure.
type SyntheticATLProcess struct {
	config      SyntheticATLProcessConfig
	binary      selectedSyntheticATLBinary
	scratchRoot string
	runtimeRoot string
	environment []string
	backend     *MockBackend
	mcp         *boundedMCPCommand

	mu              sync.Mutex
	closed          bool
	cliCounts       map[string]int
	mcpCounts       map[string]int
	mcpExactBudgets map[string]int
	mcpExactCounts  map[string]int
	closeOnce       sync.Once
	closeErr        error
}

// StartSyntheticATLProcess performs the exact selected-binary capability gate
// before it creates a runtime directory, starts the synthetic backend, or
// launches an MCP child.
func StartSyntheticATLProcess(ctx context.Context, input SyntheticATLProcessConfig) (*SyntheticATLProcess, error) {
	config, exactBudgets, err := normalizeSyntheticATLProcessConfig(input)
	if err != nil {
		return nil, err
	}
	binary, err := inspectSelectedSyntheticATLBinary(config.Binary)
	if err != nil {
		return nil, err
	}
	if err := VerifyATLCapabilityCatalog(ctx, binary.canonicalPath); err != nil {
		return nil, err
	}
	if err := binary.verify(); err != nil {
		return nil, err
	}

	if err := requirePrivateDirectory("synthetic ATL scratch root", config.ScratchRoot); err != nil {
		return nil, err
	}
	scratchRoot, err := filepath.EvalSymlinks(config.ScratchRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve synthetic ATL scratch root")
	}
	runtimeRoot, err := os.MkdirTemp(scratchRoot, ".atl-process-")
	if err != nil {
		return nil, fmt.Errorf("create synthetic ATL runtime")
	}
	if err := os.Chmod(runtimeRoot, 0o700); err != nil {
		_ = os.RemoveAll(runtimeRoot)
		return nil, fmt.Errorf("protect synthetic ATL runtime")
	}
	inside, pathErr := pathWithin(scratchRoot, runtimeRoot)
	if pathErr != nil || !inside {
		_ = os.RemoveAll(runtimeRoot)
		return nil, fmt.Errorf("synthetic ATL runtime escaped its scratch root")
	}
	process := &SyntheticATLProcess{
		config: config, binary: binary, scratchRoot: scratchRoot, runtimeRoot: runtimeRoot,
		cliCounts: map[string]int{}, mcpCounts: map[string]int{},
		mcpExactBudgets: exactBudgets, mcpExactCounts: map[string]int{},
	}
	fail := func(startErr error) (*SyntheticATLProcess, error) {
		return nil, errors.Join(startErr, process.Close())
	}
	for _, directory := range []string{
		filepath.Join(runtimeRoot, "config"),
		filepath.Join(runtimeRoot, "tmp"),
	} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return fail(fmt.Errorf("create synthetic ATL runtime directory"))
		}
	}
	mirrorRoot := filepath.Join(runtimeRoot, "mirror")
	insideMirror, pathErr := pathWithin(runtimeRoot, mirrorRoot)
	if pathErr != nil || !insideMirror {
		return fail(fmt.Errorf("synthetic ATL mirror runtime escaped its root"))
	}
	if err := seedSyntheticATLMirrorTemplate(config.MirrorTemplate, mirrorRoot); err != nil {
		return fail(err)
	}
	process.binary, err = materializeSelectedSyntheticATLBinary(process.binary, runtimeRoot)
	if err != nil {
		return fail(err)
	}
	backend, err := StartMockBackend(config.Fixture)
	if err != nil {
		return fail(err)
	}
	process.backend = backend
	process.environment = syntheticATLProcessEnvironment(backend, runtimeRoot)
	if len(config.MCPInvocations) > 0 {
		expectedTools, ok := syntheticMCPToolsForService(config.MCPService)
		if !ok {
			return fail(fmt.Errorf("synthetic ATL MCP service must be a closed profile"))
		}
		if err := process.binary.verify(); err != nil {
			return fail(err)
		}
		process.mcp, err = startBoundedMCPCommand(
			ctx, process.binary.executionPath,
			syntheticMCPServeArgs(config.MCPService),
			runtimeRoot, process.environment, config.Timeout,
			config.MaxMCPBytes, config.MaxStderrBytes,
		)
		if err != nil {
			return fail(err)
		}
		if err := process.binary.verify(); err != nil {
			return fail(err)
		}
		if config.VerifyMCPToolInventory {
			if err := process.mcp.verifyToolInventory(ctx, expectedTools); err != nil {
				return fail(err)
			}
			if err := process.binary.verify(); err != nil {
				return fail(err)
			}
		}
	}
	if err := process.binary.verify(); err != nil {
		return fail(err)
	}
	return process, nil
}

// syntheticMCPServeArgs preserves the product CLI's default-service spelling:
// default is selected by omitting --service, while named restricted profiles
// remain explicit. The evaluator config keeps "default" as an admission
// profile so its capability boundary stays closed and inspectable.
func syntheticMCPServeArgs(service string) []string {
	args := []string{"mcp", "serve"}
	if service != "default" {
		args = append(args, "--service", service)
	}
	return args
}

// syntheticMCPToolsForService keeps the selected-process-only "default"
// sentinel separate from durable run-spec profiles. Product CLI syntax selects
// the complete default surface only by omitting --service; explicit profiles
// remain the three values accepted by mcpToolsForProfile.
func syntheticMCPToolsForService(service string) (map[string]bool, bool) {
	catalog := PinnedCapabilityCatalog()
	if service != "default" {
		return catalog.mcpToolsForProfile(service)
	}
	allowed := make(map[string]bool)
	for _, item := range catalog.Capabilities {
		if item.MCPTool != "" {
			allowed[item.MCPTool] = true
		}
	}
	return allowed, true
}

// seedSyntheticATLMirrorTemplate creates the child-visible mirror root from a
// bounded plain-directory template. copyWorkspace owns the descriptor-relative
// copy and rejects nested links and non-regular entries, keeping product mirror
// parsing out of the evaluator process boundary.
func seedSyntheticATLMirrorTemplate(template, target string) error {
	if template == "" {
		if err := os.Mkdir(target, 0o700); err != nil {
			return fmt.Errorf("create synthetic ATL mirror root")
		}
		return nil
	}
	info, err := os.Lstat(template)
	if err != nil {
		return fmt.Errorf("inspect synthetic ATL mirror template")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("synthetic ATL mirror template must be a plain non-symlink directory")
	}
	if err := copyWorkspace(template, target); err != nil {
		return fmt.Errorf("seed synthetic ATL mirror template: %w", err)
	}
	return requirePrivateDirectory("synthetic ATL mirror runtime", target)
}

func normalizeSyntheticATLProcessConfig(input SyntheticATLProcessConfig) (SyntheticATLProcessConfig, map[string]int, error) {
	config := input
	if config.Timeout == 0 {
		config.Timeout = defaultSyntheticATLTimeout
	}
	if config.MaxStdoutBytes == 0 {
		config.MaxStdoutBytes = defaultSyntheticATLStdoutBytes
	}
	if config.MaxStderrBytes == 0 {
		config.MaxStderrBytes = defaultSyntheticATLStderrBytes
	}
	if config.MaxMCPBytes == 0 {
		config.MaxMCPBytes = defaultSyntheticATLMCPBytes
	}
	if config.Timeout < time.Second || config.Timeout > maximumSyntheticATLProcessTimeout ||
		config.MaxStdoutBytes < 1 || config.MaxStdoutBytes > maximumSyntheticATLStdoutBytes ||
		config.MaxStderrBytes < 1 || config.MaxStderrBytes > maximumSyntheticATLStderrBytes ||
		config.MaxMCPBytes < 1 || config.MaxMCPBytes > maximumSyntheticATLMCPBytes {
		return SyntheticATLProcessConfig{}, nil, fmt.Errorf("synthetic ATL process bounds are invalid")
	}
	if err := config.Fixture.Validate(); err != nil {
		return SyntheticATLProcessConfig{}, nil, err
	}
	if len(config.CLIPolicy.Rules) > 0 {
		if err := config.CLIPolicy.Validate(); err != nil {
			return SyntheticATLProcessConfig{}, nil, err
		}
	} else if len(config.MCPInvocations) == 0 {
		return SyntheticATLProcessConfig{}, nil, fmt.Errorf("synthetic ATL process requires a CLI or MCP admission")
	}
	if (config.MCPService == "") != (len(config.MCPInvocations) == 0) {
		return SyntheticATLProcessConfig{}, nil, fmt.Errorf("synthetic ATL MCP service and invocations must be configured together")
	}
	if config.VerifyMCPToolInventory && len(config.MCPInvocations) == 0 {
		return SyntheticATLProcessConfig{}, nil, fmt.Errorf("synthetic ATL MCP tool inventory verification requires MCP invocations")
	}
	if config.MirrorTemplate != "" && !config.VerifyMCPToolInventory {
		return SyntheticATLProcessConfig{}, nil, fmt.Errorf("synthetic ATL mirror template requires MCP tool inventory verification")
	}
	exactBudgets := map[string]int{}
	if len(config.MCPInvocations) > 0 {
		allowed, ok := syntheticMCPToolsForService(config.MCPService)
		if !ok {
			return SyntheticATLProcessConfig{}, nil, fmt.Errorf("synthetic ATL MCP service must be a closed profile")
		}
		if len(config.MCPInvocations) > maxMCPInvocationExpectations {
			return SyntheticATLProcessConfig{}, nil, fmt.Errorf("synthetic ATL MCP invocation budget is oversized")
		}
		for _, invocation := range config.MCPInvocations {
			canonical, err := canonicalJSONObject(invocation.Arguments)
			if err != nil || !bytes.Equal(canonical, invocation.Arguments) || !allowed[invocation.Tool] {
				return SyntheticATLProcessConfig{}, nil, fmt.Errorf("synthetic ATL MCP invocation lies outside the closed profile")
			}
			exactBudgets[mcpInvocationKey(invocation)]++
		}
	}
	return config, exactBudgets, nil
}

func inspectSelectedSyntheticATLBinary(path string) (selectedSyntheticATLBinary, error) {
	if path == "" {
		return selectedSyntheticATLBinary{}, fmt.Errorf("synthetic ATL process requires a selected binary")
	}
	selectedPath, err := filepath.Abs(path)
	if err != nil {
		return selectedSyntheticATLBinary{}, fmt.Errorf("resolve selected ATL binary")
	}
	canonicalPath, err := resolveCapabilityCatalogExecutable(selectedPath)
	if err != nil {
		return selectedSyntheticATLBinary{}, err
	}
	digest, err := digestSyntheticExecutable(canonicalPath, privateAgentBinaryMaxBytes)
	if err != nil {
		return selectedSyntheticATLBinary{}, fmt.Errorf("hash selected ATL binary")
	}
	return selectedSyntheticATLBinary{
		selectedPath: selectedPath, canonicalPath: canonicalPath, sha256: digest,
	}, nil
}

func (b selectedSyntheticATLBinary) verify() error {
	canonicalPath, err := resolveCapabilityCatalogExecutable(b.selectedPath)
	if err != nil || canonicalPath != b.canonicalPath {
		return fmt.Errorf("selected ATL binary changed during synthetic execution")
	}
	digest, err := digestSyntheticExecutable(canonicalPath, privateAgentBinaryMaxBytes)
	if err != nil || digest != b.sha256 {
		return fmt.Errorf("selected ATL binary changed during synthetic execution")
	}
	if b.executionPath != "" {
		digest, err = digestSyntheticExecutable(b.executionPath, privateAgentBinaryMaxBytes)
		if err != nil || digest != b.sha256 {
			return fmt.Errorf("private ATL execution copy changed during synthetic execution")
		}
	}
	return nil
}

func materializeSelectedSyntheticATLBinary(
	binary selectedSyntheticATLBinary,
	runtimeRoot string,
) (selectedSyntheticATLBinary, error) {
	data, err := readBoundedFile(binary.canonicalPath, privateAgentBinaryMaxBytes)
	if err != nil || sha256HexBytes(data) != binary.sha256 {
		return selectedSyntheticATLBinary{}, fmt.Errorf("selected ATL binary changed before private copy")
	}
	executionPath := filepath.Join(runtimeRoot, "selected-atl"+filepath.Ext(binary.canonicalPath))
	file, err := os.OpenFile(executionPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o500)
	if err != nil {
		return selectedSyntheticATLBinary{}, fmt.Errorf("create private ATL execution copy")
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return selectedSyntheticATLBinary{}, fmt.Errorf("write private ATL execution copy")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return selectedSyntheticATLBinary{}, fmt.Errorf("sync private ATL execution copy")
	}
	if err := file.Close(); err != nil {
		return selectedSyntheticATLBinary{}, fmt.Errorf("close private ATL execution copy")
	}
	if err := os.Chmod(executionPath, 0o500); err != nil {
		return selectedSyntheticATLBinary{}, fmt.Errorf("protect private ATL execution copy")
	}
	binary.executionPath = executionPath
	if err := binary.verify(); err != nil {
		return selectedSyntheticATLBinary{}, err
	}
	return binary, nil
}

func syntheticATLProcessEnvironment(backend *MockBackend, runtimeRoot string) []string {
	values := backend.Environment()
	values["ATL_NO_UPDATE"] = "1"
	values["ATL_READ_ONLY"] = "1"
	values["ATL_CONFIG_DIR"] = filepath.Join(runtimeRoot, "config")
	values["ATL_MIRROR_ROOT"] = filepath.Join(runtimeRoot, "mirror")
	temporary := filepath.Join(runtimeRoot, "tmp")
	values["TMPDIR"] = temporary
	values["TMP"] = temporary
	values["TEMP"] = temporary
	if runtime.GOOS == "windows" {
		for _, name := range []string{"SYSTEMROOT", "WINDIR"} {
			if value, ok := os.LookupEnv(name); ok && value != "" {
				values[name] = value
			}
		}
	}
	return flattenEnvironment(values)
}

// RunCLIBytes admits one exact CLI command and returns its bounded output
// byte-for-byte. A nonzero exit is evidence, not a retryable process failure.
func (p *SyntheticATLProcess) RunCLIBytes(ctx context.Context, args ...string) (SyntheticCLIBytesResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.runCLIBytesLocked(ctx, args)
}

// RunCLIJSON executes through the same single admission/accounting path as
// RunCLIBytes, then requires successful stdout to be exactly one strict JSON
// value. It never re-executes the selected binary while interpreting stdout.
func (p *SyntheticATLProcess) RunCLIJSON(ctx context.Context, args ...string) (SyntheticCLIResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	bytesResult, err := p.runCLIBytesLocked(ctx, args)
	if err != nil {
		return SyntheticCLIResult{}, err
	}
	result := SyntheticCLIResult{
		ExitCode: bytesResult.ExitCode,
		Stderr:   append([]byte(nil), bytesResult.Stderr...),
	}
	if result.ExitCode != 0 {
		return result, nil
	}
	value, err := oneSyntheticJSONValue(bytesResult.Stdout)
	if err != nil {
		return SyntheticCLIResult{}, err
	}
	result.JSON = value
	return result, nil
}

// runCLIBytesLocked is the sole CLI admission and execution path. The caller
// holds p.mu across admission, one accounting increment, both executable
// attestations, and bounded process execution.
func (p *SyntheticATLProcess) runCLIBytesLocked(ctx context.Context, args []string) (SyntheticCLIBytesResult, error) {
	if p.closed {
		return SyntheticCLIBytesResult{}, fmt.Errorf("synthetic ATL process is closed")
	}
	if err := p.binary.verify(); err != nil {
		return SyntheticCLIBytesResult{}, err
	}
	if len(p.config.CLIPolicy.Rules) == 0 {
		return SyntheticCLIBytesResult{}, fmt.Errorf("synthetic ATL process has no CLI admission")
	}
	match, err := p.config.CLIPolicy.Match(args)
	if err != nil || p.cliCounts[match.Name] >= match.MaxInvocations {
		return SyntheticCLIBytesResult{}, fmt.Errorf("synthetic ATL CLI invocation is outside its reviewed budget")
	}
	p.cliCounts[match.Name]++
	commandResult, runErr := executeBoundedCommand(
		ctx, p.binary.executionPath, args, p.runtimeRoot, p.environment,
		p.config.Timeout, p.config.MaxStdoutBytes, p.config.MaxStderrBytes,
	)
	if verifyErr := p.binary.verify(); verifyErr != nil {
		return SyntheticCLIBytesResult{}, verifyErr
	}
	if runErr != nil {
		return SyntheticCLIBytesResult{}, runErr
	}
	return SyntheticCLIBytesResult{
		ExitCode: commandResult.exitCode,
		Stdout:   append([]byte(nil), commandResult.stdout...),
		Stderr:   append([]byte(nil), commandResult.stderr...),
	}, nil
}

func oneSyntheticJSONValue(data []byte) (json.RawMessage, error) {
	if validateJSONNoDuplicateKeys(data) != nil {
		return nil, fmt.Errorf("ATL CLI stdout is not one strict JSON value")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value json.RawMessage
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("ATL CLI stdout is not one JSON value")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("ATL CLI stdout is not one JSON value")
	}
	return append(json.RawMessage(nil), bytes.TrimSpace(value)...), nil
}

// CallMCPJSON admits one exact tool-and-canonical-arguments pair and returns
// the bounded structuredContent object from the selected ATL process.
func (p *SyntheticATLProcess) CallMCPJSON(ctx context.Context, invocation MCPInvocation) (SyntheticMCPResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.mcp == nil {
		return SyntheticMCPResult{}, fmt.Errorf("synthetic ATL MCP process is unavailable")
	}
	if err := p.binary.verify(); err != nil {
		return SyntheticMCPResult{}, err
	}
	canonical, err := canonicalJSONObject(invocation.Arguments)
	key := mcpInvocationKey(invocation)
	if err != nil || !bytes.Equal(canonical, invocation.Arguments) ||
		p.mcpExactCounts[key] >= p.mcpExactBudgets[key] {
		return SyntheticMCPResult{}, fmt.Errorf("synthetic ATL MCP invocation is outside its reviewed budget")
	}
	p.mcpExactCounts[key]++
	p.mcpCounts[invocation.Tool]++
	result, callErr := p.mcp.call(ctx, invocation)
	if verifyErr := p.binary.verify(); verifyErr != nil {
		return SyntheticMCPResult{}, verifyErr
	}
	if callErr != nil {
		return SyntheticMCPResult{}, callErr
	}
	return result, nil
}

// Summary returns a content-free snapshot of backend and admission counts.
func (p *SyntheticATLProcess) Summary() SyntheticATLProcessSummary {
	p.mu.Lock()
	defer p.mu.Unlock()
	methods, unexpected, duplicates := p.backend.Summary()
	return SyntheticATLProcessSummary{
		HTTPMethods: methods, UnexpectedRequests: unexpected, DuplicateRequests: duplicates,
		CLIInvocations: cloneSyntheticCounts(p.cliCounts),
		MCPInvocations: cloneSyntheticCounts(p.mcpCounts),
	}
}

func cloneSyntheticCounts(source map[string]int) map[string]int {
	clone := make(map[string]int, len(source))
	for name, count := range source {
		clone[name] = count
	}
	return clone
}

// RequestSequenceComplete reports whether the evaluator-owned backend accepted
// every configured ordered request.
func (p *SyntheticATLProcess) RequestSequenceComplete() bool {
	return p.backend.RequestSequenceComplete()
}

// Close is idempotent. It stops the MCP child first, then the backend, checks
// the selected binary binding one final time, and removes only the unique
// runtime child created by StartSyntheticATLProcess.
func (p *SyntheticATLProcess) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.closed = true
		var closeErr error
		if p.mcp != nil {
			closeErr = errors.Join(closeErr, p.mcp.Close())
		}
		if p.backend != nil {
			p.backend.Close()
		}
		closeErr = errors.Join(closeErr, p.binary.verify())
		if p.runtimeRoot != "" {
			inside, err := pathWithin(p.scratchRoot, p.runtimeRoot)
			if err != nil || !inside {
				closeErr = errors.Join(closeErr, fmt.Errorf("synthetic ATL runtime cleanup path is invalid"))
			} else if err := os.RemoveAll(p.runtimeRoot); err != nil {
				closeErr = errors.Join(closeErr, fmt.Errorf("remove synthetic ATL runtime"))
			}
		}
		p.closeErr = closeErr
	})
	return p.closeErr
}
