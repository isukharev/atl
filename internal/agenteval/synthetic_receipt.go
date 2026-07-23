package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	SyntheticRunReceiptSchemaVersion = 1
	syntheticRunReceiptFileName      = "run-receipt.json"
	maxSyntheticRunReceiptBytes      = 16 << 10
)

// SyntheticRunReceipt binds one current synthetic result to the exact task,
// execution policy, and executable bytes used by the runner. It contains only
// owner-private identities and digests, never source paths or input content.
type SyntheticRunReceipt struct {
	SchemaVersion           int    `json:"schema_version"`
	ScenarioID              string `json:"scenario_id"`
	Provider                string `json:"provider"`
	Variant                 string `json:"variant"`
	Repetition              int    `json:"repetition"`
	Repetitions             int    `json:"repetitions"`
	TaskContractSHA256      string `json:"task_contract_sha256"`
	ExecutionContractSHA256 string `json:"execution_contract_sha256"`
	AgentExecutableSHA256   string `json:"agent_executable_sha256"`
	ATLExecutableSHA256     string `json:"atl_executable_sha256"`
	WrapperExecutableSHA256 string `json:"wrapper_executable_sha256"`
	ResultSHA256            string `json:"result_sha256"`
}

type syntheticExecutableDigests struct {
	agent   string
	atl     string
	wrapper string
}

type syntheticRunAttestation struct {
	spec        RunSpec
	executables syntheticExecutableDigests
}

func (r SyntheticRunReceipt) Validate() error {
	if r.SchemaVersion != SyntheticRunReceiptSchemaVersion ||
		validatePathComponentID("scenario id", r.ScenarioID) != nil ||
		(r.Provider != "codex" && r.Provider != "claude-code") ||
		validatePathComponentID("run variant", r.Variant) != nil ||
		r.Repetitions < 1 || r.Repetitions > 20 ||
		r.Repetition < 1 || r.Repetition > r.Repetitions {
		return fmt.Errorf("invalid synthetic run receipt")
	}
	for _, digest := range []string{
		r.TaskContractSHA256,
		r.ExecutionContractSHA256,
		r.AgentExecutableSHA256,
		r.ATLExecutableSHA256,
		r.WrapperExecutableSHA256,
		r.ResultSHA256,
	} {
		if !validSHA256(digest) {
			return fmt.Errorf("invalid synthetic run receipt")
		}
	}
	return nil
}

func DecodeSyntheticRunReceipt(reader io.Reader) (SyntheticRunReceipt, error) {
	var receipt SyntheticRunReceipt
	limited := &io.LimitedReader{R: reader, N: maxSyntheticRunReceiptBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return SyntheticRunReceipt{}, fmt.Errorf("decode synthetic run receipt: %w", err)
	}
	if limited.N <= 0 {
		return SyntheticRunReceipt{}, fmt.Errorf("synthetic run receipt exceeds %d bytes", maxSyntheticRunReceiptBytes)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return SyntheticRunReceipt{}, fmt.Errorf("synthetic run receipt contains trailing JSON data")
	}
	if err := receipt.Validate(); err != nil {
		return SyntheticRunReceipt{}, err
	}
	return receipt, nil
}

func newSyntheticRunAttestation(spec RunSpec, agentBinary, atlBinary, wrapperExecutable string) (*syntheticRunAttestation, error) {
	if spec.EffectiveBackendMode() != BackendModeSynthetic {
		return nil, nil
	}
	executables, err := inspectSyntheticExecutables(agentBinary, atlBinary, wrapperExecutable)
	if err != nil {
		return nil, err
	}
	return &syntheticRunAttestation{spec: normalizedSyntheticExecutionSpec(spec), executables: executables}, nil
}

func (a *syntheticRunAttestation) verifyExecutables(agentBinary, atlBinary, wrapperExecutable string) error {
	if a == nil {
		return nil
	}
	current, err := inspectSyntheticExecutables(agentBinary, atlBinary, wrapperExecutable)
	if err != nil || current != a.executables {
		return fmt.Errorf("synthetic run executables changed during execution")
	}
	return nil
}

func inspectSyntheticExecutables(agentBinary, atlBinary, wrapperExecutable string) (syntheticExecutableDigests, error) {
	agent, err := digestSyntheticExecutable(agentBinary, privateAgentBinaryMaxBytes)
	if err != nil {
		return syntheticExecutableDigests{}, fmt.Errorf("hash agent executable: %w", err)
	}
	atl, err := digestSyntheticExecutable(atlBinary, privateAgentBinaryMaxBytes)
	if err != nil {
		return syntheticExecutableDigests{}, fmt.Errorf("hash atl executable: %w", err)
	}
	wrapper, err := digestSyntheticExecutable(wrapperExecutable, 128<<20)
	if err != nil {
		return syntheticExecutableDigests{}, fmt.Errorf("hash evaluation wrapper: %w", err)
	}
	return syntheticExecutableDigests{agent: agent, atl: atl, wrapper: wrapper}, nil
}

func digestSyntheticExecutable(path string, limit int64) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("executable is not a plain file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return "", fmt.Errorf("executable changed while it was opened")
	}
	data, readErr := ioReadAllLimit(file, limit)
	finalInfo, finalStatErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil {
		return "", readErr
	}
	if finalStatErr != nil || !os.SameFile(openedInfo, finalInfo) ||
		finalInfo.Size() != int64(len(data)) || finalInfo.Size() != openedInfo.Size() ||
		!finalInfo.ModTime().Equal(openedInfo.ModTime()) || finalInfo.Mode() != openedInfo.Mode() {
		return "", fmt.Errorf("executable changed while it was hashed")
	}
	if closeErr != nil {
		return "", closeErr
	}
	return sha256HexBytes(data), nil
}

func normalizedSyntheticExecutionSpec(spec RunSpec) RunSpec {
	spec.BackendMode = spec.EffectiveBackendMode()
	spec.Category = spec.EffectiveCategory()
	spec.Surface = spec.EffectiveSurface()
	spec.ToolTransport = spec.EffectiveToolTransport()
	spec.ScenarioFile = ""
	spec.PromptFile = ""
	spec.ResponseSchemaFile = ""
	spec.QualitativeRubricFile = ""
	spec.WorkspaceTemplate = ""
	spec.FixtureFile = ""
	return spec
}

func syntheticTaskContractSHA256(loaded loadedRun, copiedWorkspace string) (string, error) {
	if loaded.spec.EffectiveBackendMode() != BackendModeSynthetic || loaded.fixture == nil {
		return "", fmt.Errorf("synthetic task contract requires a synthetic fixture")
	}
	semanticChecks, err := semanticRunChecks(loaded.spec.Checks)
	if err != nil {
		return "", err
	}
	workspaceSHA256, err := digestWorkspaceTree(copiedWorkspace)
	if err != nil {
		return "", fmt.Errorf("hash copied synthetic workspace: %w", err)
	}
	envelope := struct {
		SchemaVersion        int         `json:"schema_version"`
		Scenario             Scenario    `json:"scenario"`
		CorePrompt           []byte      `json:"core_prompt"`
		ResponseSchema       []byte      `json:"response_schema"`
		Rubric               Rubric      `json:"rubric"`
		Fixture              MockFixture `json:"fixture"`
		WorkspaceSHA256      string      `json:"workspace_sha256"`
		SemanticChecks       []RunCheck  `json:"semantic_checks"`
		DataCapabilities     []string    `json:"data_capabilities"`
		AllowSyntheticWrites bool        `json:"allow_synthetic_writes"`
	}{
		SchemaVersion: 1, Scenario: loaded.scenario, CorePrompt: loaded.prompt,
		ResponseSchema: loaded.responseSchema, Rubric: loaded.rubric, Fixture: *loaded.fixture,
		WorkspaceSHA256: workspaceSHA256, SemanticChecks: semanticChecks,
		DataCapabilities:     append([]string(nil), loaded.spec.DataCapabilities...),
		AllowSyntheticWrites: loaded.spec.AllowSyntheticWrites,
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode synthetic task contract: %w", err)
	}
	return sha256HexBytes(append([]byte("atl-agent-eval-synthetic-task-v1\x00"), canonical...)), nil
}

func syntheticExecutionContractSHA256(attestation *syntheticRunAttestation, taskSHA256 string, runtime Runtime, providerResponseSchema []byte) (string, error) {
	if attestation == nil || !validSHA256(taskSHA256) || runtime.PromptContractSHA256 == "" {
		return "", fmt.Errorf("synthetic execution contract is incomplete")
	}
	envelope := struct {
		SchemaVersion                int     `json:"schema_version"`
		TaskContractSHA256           string  `json:"task_contract_sha256"`
		RunSpec                      RunSpec `json:"run_spec"`
		Runtime                      Runtime `json:"runtime"`
		ProviderResponseSchemaSHA256 string  `json:"provider_response_schema_sha256"`
		AgentExecutableSHA256        string  `json:"agent_executable_sha256"`
		ATLExecutableSHA256          string  `json:"atl_executable_sha256"`
		WrapperExecutableSHA256      string  `json:"wrapper_executable_sha256"`
	}{
		SchemaVersion: 1, TaskContractSHA256: taskSHA256,
		RunSpec: attestation.spec, Runtime: runtime,
		ProviderResponseSchemaSHA256: sha256HexBytes(providerResponseSchema),
		AgentExecutableSHA256:        attestation.executables.agent,
		ATLExecutableSHA256:          attestation.executables.atl,
		WrapperExecutableSHA256:      attestation.executables.wrapper,
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode synthetic execution contract: %w", err)
	}
	return sha256HexBytes(append([]byte("atl-agent-eval-synthetic-execution-v1\x00"), canonical...)), nil
}

func newSyntheticRunReceipt(attestation *syntheticRunAttestation, loaded loadedRun, runtime Runtime, repetition int, taskSHA256, executionSHA256 string, resultData []byte) (SyntheticRunReceipt, error) {
	if attestation == nil {
		return SyntheticRunReceipt{}, nil
	}
	receipt := SyntheticRunReceipt{
		SchemaVersion: SyntheticRunReceiptSchemaVersion,
		ScenarioID:    loaded.scenario.ID, Provider: loaded.spec.Provider, Variant: loaded.spec.Variant,
		Repetition: repetition, Repetitions: attestation.spec.Repetitions,
		TaskContractSHA256: taskSHA256, ExecutionContractSHA256: executionSHA256,
		AgentExecutableSHA256:   attestation.executables.agent,
		ATLExecutableSHA256:     attestation.executables.atl,
		WrapperExecutableSHA256: attestation.executables.wrapper,
		ResultSHA256:            sha256HexBytes(resultData),
	}
	if err := receipt.Validate(); err != nil {
		return SyntheticRunReceipt{}, err
	}
	if runtime.Provider != receipt.Provider {
		return SyntheticRunReceipt{}, fmt.Errorf("synthetic run receipt runtime does not match provider")
	}
	return receipt, nil
}

func encodeSyntheticRunReceipt(receipt SyntheticRunReceipt) ([]byte, error) {
	if err := receipt.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func writeSyntheticRunReceipt(root string, receipt SyntheticRunReceipt) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	runDir := filepath.Join(root, receipt.ScenarioID, receipt.Provider, receipt.Variant, fmt.Sprintf("run-%02d", receipt.Repetition))
	inside, err := pathWithin(root, runDir)
	if err != nil || !inside {
		return fmt.Errorf("synthetic run receipt escapes its output root")
	}
	data, err := encodeSyntheticRunReceipt(receipt)
	if err != nil {
		return err
	}
	return writePrivateFile(filepath.Join(runDir, syntheticRunReceiptFileName), data)
}

func syntheticReceiptMatchesResult(receipt SyntheticRunReceipt, result Result, resultData []byte, scenario, provider, variant string, repetition int) bool {
	return receipt.Validate() == nil &&
		receipt.ScenarioID == scenario && receipt.Provider == provider && receipt.Variant == variant &&
		receipt.Repetition == repetition &&
		result.ScenarioID == receipt.ScenarioID && result.Runtime.Provider == receipt.Provider && result.Variant == receipt.Variant &&
		receipt.ResultSHA256 == sha256HexBytes(resultData)
}

func decodeSyntheticRunReceiptBytes(data []byte) (SyntheticRunReceipt, error) {
	return DecodeSyntheticRunReceipt(bytes.NewReader(data))
}
