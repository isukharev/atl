package agenteval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/agenteval/extension"
	"github.com/isukharev/atl/internal/agenteval/grading"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

func openOrCreateAttemptLedgerStore(root string) (*AttemptLedgerStore, error) {
	store, err := OpenAttemptLedgerStore(root)
	if os.IsNotExist(unwrappedPathError(err)) {
		store, err = CreateAttemptLedgerStore(root, nil)
	}
	return store, err
}

type runAttemptBindings struct {
	outputRoot               string
	repetition               int
	agentBinary              string
	atlBinary                string
	pluginRoot               string
	wrapperExecutable        string
	liveConfigDir            string
	scratchRoot              string
	runtime                  Runtime
	externalProfile          ExternalMCPProfile
	providerRuntime          *providerRuntimeCapsule
	attestation              *syntheticRunAttestation
	providerAttemptCommitted func() error
	attemptSession           *DurableAttemptSession
	receipt                  *SyntheticRunReceipt
	gradingPlan              grading.Plan
}

func prepareRunAttemptSessions(outputRoot string, contract resolvedRunContract, options RunOptions, skillDigest string) ([]*DurableAttemptSession, []grading.Plan, error) {
	ledgerRoot := filepath.Join(outputRoot, "attempt-ledger")
	store, err := openOrCreateAttemptLedgerStore(ledgerRoot)
	if err != nil {
		return nil, nil, err
	}
	if err := store.RecoverIncomplete(); err != nil {
		return nil, nil, err
	}
	binding, err := runAttemptBinding(contract, options, skillDigest)
	if err != nil {
		return nil, nil, err
	}
	bindings := make([]lifecycle.Binding, contract.spec.Repetitions+1)
	gradingPlans := make([]grading.Plan, contract.spec.Repetitions)
	bindings[0] = binding
	bindings[0].Identity.TaskSHA256, err = contentMinimizedAttemptDigest("preflight-task", binding.Identity.TaskSHA256)
	if err != nil {
		return nil, nil, err
	}
	for index := 1; index < len(bindings); index++ {
		bindings[index] = binding
		bindings[index].Identity.TaskSHA256, err = contentMinimizedAttemptDigest("repetition-task", struct {
			TaskSHA256 string `json:"task_sha256"`
			Repetition int    `json:"repetition"`
		}{binding.Identity.TaskSHA256, index})
		if err != nil {
			return nil, nil, err
		}
		gradingPlans[index-1], err = newATLGradingPlan(contract.spec.Checks, contract.workspaceTemplate,
			bindings[index].Identity.TaskSHA256)
		if err != nil {
			return nil, nil, err
		}
		bindings[index], err = BindGradingPlan(bindings[index], gradingPlans[index-1])
		if err != nil {
			return nil, nil, err
		}
	}
	plans, err := store.AllocateRoster(bindings)
	if err != nil {
		return nil, nil, err
	}
	sessions := make([]*DurableAttemptSession, len(plans))
	for index, plan := range plans {
		sessions[index], err = NewDurableAttemptSession(store, plan)
		if err != nil {
			return nil, nil, err
		}
	}
	return sessions, gradingPlans, nil
}

func prepareCalibrationAttemptSession(outputRoot string, contract CodexCLICalibrationContract, options CodexCLICalibrationOptions) (*DurableAttemptSession, error) {
	ledgerRoot := filepath.Join(outputRoot, "attempt-ledger")
	store, err := openOrCreateAttemptLedgerStore(ledgerRoot)
	if err != nil {
		return nil, err
	}
	if err := store.RecoverIncomplete(); err != nil {
		return nil, err
	}
	agentDigest, err := digestSyntheticExecutable(options.AgentBinary, privateAgentBinaryMaxBytes)
	if err != nil {
		return nil, err
	}
	atlDigest, err := digestSyntheticExecutable(options.ATLBinary, privateAgentBinaryMaxBytes)
	if err != nil {
		return nil, err
	}
	wrapperDigest, err := digestSyntheticExecutable(options.WrapperExecutable, 128<<20)
	if err != nil {
		return nil, err
	}
	domainDigest := func(domain string, value any) (string, error) {
		return contentMinimizedAttemptDigest("calibration-"+domain, value)
	}
	task, err := domainDigest("task", contract.SHA256)
	if err != nil {
		return nil, err
	}
	model, err := domainDigest("model", []string{contract.Provider, contract.Model, contract.Reasoning})
	if err != nil {
		return nil, err
	}
	environment, err := domainDigest("environment", []string{atlDigest, wrapperDigest})
	if err != nil {
		return nil, err
	}
	budgets, err := domainDigest("budgets", []any{contract.TimeoutSeconds, contract.MaxEstimatedCostMicroUSD, contract.Pricing})
	if err != nil {
		return nil, err
	}
	adapter, err := domainDigest("adapter", []string{contract.Provider, agentDigest})
	if err != nil {
		return nil, err
	}
	authority, err := domainDigest("authority", struct {
		ProviderContact bool             `json:"provider_contact"`
		BackendContact  bool             `json:"backend_contact"`
		Network         bool             `json:"network"`
		CLI             CLICommandPolicy `json:"cli_policy"`
	}{ProviderContact: true, BackendContact: false, Network: true, CLI: calibrationCLICommandPolicy()})
	if err != nil {
		return nil, err
	}
	none, err := domainDigest("none", "not_applicable")
	if err != nil {
		return nil, err
	}
	binding := lifecycle.Binding{Privacy: lifecycle.PrivacyOwnerPrivate, Identity: lifecycle.Identity{
		ExperimentSHA256: contract.SHA256, TaskSHA256: task, SkillSHA256: none, AgentSHA256: agentDigest,
		ModelSHA256: model, EnvironmentSHA256: environment, GraderSHA256: none, BudgetsSHA256: budgets, AdapterSHA256: adapter,
		AuthoritySHA256: authority,
	}}
	plans, err := store.AllocateRoster([]lifecycle.Binding{binding})
	if err != nil {
		return nil, err
	}
	return NewDurableAttemptSession(store, plans[0])
}

func prepareExtensionAttemptSessions(store *AttemptLedgerStore, manifest extension.Manifest, bundle ExtensionConformanceBundle, manifestDigest, bundleDigest, componentContractDigest string) ([]*DurableAttemptSession, error) {
	if store == nil || !validSHA256(manifestDigest) || !validSHA256(bundleDigest) {
		return nil, attemptLedgerError("extension_binding")
	}
	if err := store.RecoverIncomplete(); err != nil {
		return nil, err
	}
	none, err := contentMinimizedAttemptDigest("extension-not-applicable", "not_applicable")
	if err != nil {
		return nil, err
	}
	environment, err := contentMinimizedAttemptDigest("extension-environment", struct {
		Requirements []extension.EnforcementRequirement `json:"requirements"`
		Platforms    []extension.Platform               `json:"platforms"`
	}{manifest.Requirements, manifest.Platforms})
	if err != nil {
		return nil, err
	}
	adapterIdentity := manifestDigest
	graderIdentity := ""
	if componentContractDigest != "" {
		if !validSHA256(componentContractDigest) ||
			(manifest.Component.Role != extension.RoleAgentAdapter && manifest.Component.Role != extension.RoleExecutionBackend &&
				manifest.Component.Role != extension.RoleGrader) {
			return nil, attemptLedgerError("extension_component_binding")
		}
		domain := "grader-process-binding"
		switch manifest.Component.Role {
		case extension.RoleAgentAdapter:
			domain = "agent-adapter-process-binding"
		case extension.RoleExecutionBackend:
			domain = "execution-backend-process-binding"
		}
		boundIdentity, digestErr := contentMinimizedAttemptDigest(domain, []string{manifestDigest, componentContractDigest})
		err = digestErr
		if err != nil {
			return nil, err
		}
		if manifest.Component.Role == extension.RoleGrader {
			graderIdentity = boundIdentity
		} else {
			adapterIdentity = boundIdentity
		}
	}
	bindings := make([]lifecycle.Binding, len(bundle.Cases))
	for index, testCase := range bundle.Cases {
		task, digestErr := contentMinimizedAttemptDigest("extension-case", testCase)
		if digestErr != nil {
			return nil, digestErr
		}
		budgets, digestErr := contentMinimizedAttemptDigest("extension-case-budgets", struct {
			Deadline int64                      `json:"deadline_milliseconds"`
			Policy   extension.InvocationPolicy `json:"policy"`
		}{testCase.DeadlineMilliseconds, testCase.Policy})
		if digestErr != nil {
			return nil, digestErr
		}
		grader := none
		if manifest.Component.Role == extension.RoleGrader {
			grader = graderIdentity
			if grader == "" {
				grader, digestErr = contentMinimizedAttemptDigest("extension-grader", manifest.Component.Capabilities)
				if digestErr != nil {
					return nil, digestErr
				}
			}
		}
		authority, digestErr := contentMinimizedAttemptDigest("extension-authority", struct {
			Requirements []extension.EnforcementRequirement `json:"requirements"`
			Capabilities []extension.CapabilityClaim        `json:"capabilities"`
			Policy       extension.InvocationPolicy         `json:"policy"`
		}{manifest.Requirements, manifest.Component.Capabilities, testCase.Policy})
		if digestErr != nil {
			return nil, digestErr
		}
		bindings[index] = lifecycle.Binding{Privacy: lifecycle.PrivacyContentMinimized, Identity: lifecycle.Identity{
			ExperimentSHA256: bundleDigest, TaskSHA256: task, SkillSHA256: none,
			AgentSHA256: manifest.ExecutableSHA256, ModelSHA256: none, EnvironmentSHA256: environment,
			GraderSHA256: grader, BudgetsSHA256: budgets, AdapterSHA256: adapterIdentity, AuthoritySHA256: authority,
		}}
	}
	plans, err := store.AllocateRoster(bindings)
	if err != nil {
		return nil, err
	}
	result := make([]*DurableAttemptSession, len(plans))
	for index, plan := range plans {
		result[index], err = NewDurableAttemptSession(store, plan)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func prepareQualificationAttemptSession(ledgerRoot, domain, agentIdentity, contractSHA256 string, modelIdentity any, timeoutSeconds int) (*DurableAttemptSession, error) {
	if ledgerRoot == "" || !validSHA256(contractSHA256) || !strings.HasPrefix(agentIdentity, "binary-sha256:") ||
		!validSHA256(strings.TrimPrefix(agentIdentity, "binary-sha256:")) {
		return nil, attemptLedgerError("qualification_binding")
	}
	digest := func(part string, value any) (string, error) {
		return contentMinimizedAttemptDigest("qualification-"+domain+"-"+part, value)
	}
	none, err := digest("not-applicable", "not_applicable")
	if err != nil {
		return nil, err
	}
	task, err := digest("task", contractSHA256)
	if err != nil {
		return nil, err
	}
	model, err := digest("model", modelIdentity)
	if err != nil {
		return nil, err
	}
	environment, err := digest("environment", []string{"owner-private-runtime", "loopback-only"})
	if err != nil {
		return nil, err
	}
	budgets, err := digest("budgets", timeoutSeconds)
	if err != nil {
		return nil, err
	}
	adapter, err := digest("adapter", []string{domain, agentIdentity})
	if err != nil {
		return nil, err
	}
	authority, err := digest("authority", struct {
		Process, Loopback                          bool
		Provider, Backend, ExternalNetwork, Writes bool
	}{true, true, false, false, false, false})
	if err != nil {
		return nil, err
	}
	binding := lifecycle.Binding{Privacy: lifecycle.PrivacyContentMinimized, Identity: lifecycle.Identity{
		ExperimentSHA256: contractSHA256, TaskSHA256: task, SkillSHA256: none,
		AgentSHA256: strings.TrimPrefix(agentIdentity, "binary-sha256:"), ModelSHA256: model,
		EnvironmentSHA256: environment, GraderSHA256: none, BudgetsSHA256: budgets,
		AdapterSHA256: adapter, AuthoritySHA256: authority,
	}}
	store, err := openOrCreateAttemptLedgerStore(ledgerRoot)
	if err != nil {
		return nil, err
	}
	if err := store.RecoverIncomplete(); err != nil {
		return nil, err
	}
	plans, err := store.AllocateRoster([]lifecycle.Binding{binding})
	if err != nil {
		return nil, err
	}
	return NewDurableAttemptSession(store, plans[0])
}

func finalizeQualificationAttempt(session *DurableAttemptSession, report any, terminationOK bool, processReceipt string,
	timedOut, canceled bool, runErr error,
) error {
	inspection, err := session.store.Inspect(session.plan.AttemptID)
	if err != nil || inspection.Projection.Terminal {
		return err
	}
	usage := inspection.Projection.Usage
	if timedOut {
		return session.Timeout(terminationOK, usage)
	}
	if canceled {
		return session.Cancel(terminationOK, usage)
	}
	if inspection.Projection.State == lifecycle.StatePlanned {
		return session.Cancel(false, usage)
	}
	if !terminationOK || !validSHA256(processReceipt) {
		return session.Unknown(lifecycle.ErrorTerminationAmbiguous, usage)
	}
	receipt, err := contentMinimizedAttemptDigest("qualification-receipt", struct {
		Process string `json:"process_sha256"`
		Report  any    `json:"report"`
	}{processReceipt, report})
	if err != nil {
		return session.Unknown(lifecycle.ErrorInternal, usage)
	}
	if runErr != nil {
		return session.Fail(receipt, usage)
	}
	return session.Succeed(receipt, usage)
}

func verifyRunCapabilityPreflight(ctx context.Context, binary string, session *DurableAttemptSession) (string, error) {
	terminationOK, receipt, err := verifyATLCapabilityCatalogWithSession(ctx, binary, session)
	if err == nil {
		return receipt, nil
	}
	lifecycleErr := finalizeQualificationAttempt(session, struct {
		Compatible bool `json:"compatible"`
	}{false}, terminationOK, receipt, errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), err)
	return receipt, errors.Join(err, lifecycleErr)
}

func prepareSyntheticATLProcessAttemptSession(config SyntheticATLProcessConfig, binary selectedSyntheticATLBinary) (*DurableAttemptSession, error) {
	ledgerRoot := filepath.Join(filepath.Dir(config.ScratchRoot), "."+filepath.Base(config.ScratchRoot)+"-attempt-ledger")
	digest := func(part string, value any) (string, error) {
		return contentMinimizedAttemptDigest("synthetic-atl-process-"+part, value)
	}
	experiment, err := digest("experiment", struct {
		Fixture             MockFixture         `json:"fixture"`
		CLIPolicy           CLICommandPolicy    `json:"cli_policy"`
		MCPService          string              `json:"mcp_service"`
		MCPInvocations      []MCPInvocation     `json:"mcp_invocations"`
		SyntheticWriteRules SyntheticWriteRules `json:"synthetic_write_rules"`
	}{config.Fixture, config.CLIPolicy, config.MCPService, config.MCPInvocations, config.SyntheticWriteRules})
	if err != nil {
		return nil, err
	}
	task, err := digest("task", []any{config.Fixture, config.MirrorTemplate != "", config.WorkspaceTemplate != ""})
	if err != nil {
		return nil, err
	}
	none, err := digest("not-applicable", "not_applicable")
	if err != nil {
		return nil, err
	}
	environment, err := digest("environment", []any{binary.sha256, config.VerifyMCPToolInventory})
	if err != nil {
		return nil, err
	}
	budgets, err := digest("budgets", []any{config.Timeout, config.MaxStdoutBytes, config.MaxStderrBytes, config.MaxMCPBytes})
	if err != nil {
		return nil, err
	}
	adapter, err := digest("adapter", []string{"selected-atl-process-v1", binary.sha256})
	if err != nil {
		return nil, err
	}
	authority, err := digest("authority", struct {
		Process, Loopback, SyntheticWrites bool
	}{true, true, len(config.SyntheticWriteRules) != 0})
	if err != nil {
		return nil, err
	}
	binding := lifecycle.Binding{Privacy: lifecycle.PrivacyContentMinimized, Identity: lifecycle.Identity{
		ExperimentSHA256: experiment, TaskSHA256: task, SkillSHA256: none, AgentSHA256: binary.sha256,
		ModelSHA256: none, EnvironmentSHA256: environment, GraderSHA256: none, BudgetsSHA256: budgets,
		AdapterSHA256: adapter, AuthoritySHA256: authority,
	}}
	store, err := openOrCreateAttemptLedgerStore(ledgerRoot)
	if err != nil {
		return nil, err
	}
	if err := store.RecoverIncomplete(); err != nil {
		return nil, err
	}
	plans, err := store.AllocateRoster([]lifecycle.Binding{binding})
	if err != nil {
		return nil, err
	}
	return NewDurableAttemptSession(store, plans[0])
}

func calibrationAttemptUsage(receipt CodexCLICalibrationReceipt) lifecycle.Usage {
	usage := lifecycle.UnknownUsage()
	if receipt.EstimatedCostMicroUSD >= 0 {
		usage.EstimatedCostMicroUSD = lifecycle.ObservedMetric(uint64(receipt.EstimatedCostMicroUSD)) // #nosec G115 -- nonnegative guard above.
	}
	if receipt.InputTokens >= 0 {
		usage.InputTokens = lifecycle.ObservedMetric(uint64(receipt.InputTokens)) // #nosec G115 -- nonnegative guard above.
	}
	if receipt.OutputTokens >= 0 {
		usage.OutputTokens = lifecycle.ObservedMetric(uint64(receipt.OutputTokens)) // #nosec G115 -- nonnegative guard above.
	}
	return usage
}

func unwrappedPathError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

type DurableAttemptSession struct {
	store *AttemptLedgerStore
	plan  lifecycle.Plan
}

func NewDurableAttemptSession(store *AttemptLedgerStore, plan lifecycle.Plan) (*DurableAttemptSession, error) {
	if store == nil || plan.LedgerID != store.header.LedgerID {
		return nil, attemptLedgerError("session")
	}
	inspection, err := store.Inspect(plan.AttemptID)
	if err != nil || inspection.Plan.PlanSHA256 != plan.PlanSHA256 || inspection.Projection.State != lifecycle.StatePlanned {
		return nil, attemptLedgerError("session_plan", err)
	}
	return &DurableAttemptSession{store: store, plan: plan}, nil
}

func (session *DurableAttemptSession) Plan() lifecycle.Plan { return session.plan }

func (session *DurableAttemptSession) Commit() error {
	_, err := session.store.Append(session.plan.AttemptID, lifecycle.StateCommitted,
		[]lifecycle.Proof{lifecycle.ProofDurableCommit}, attemptEvidenceWithUsage(lifecycle.ErrorNone, lifecycle.UnknownUsage()))
	return err
}

func (session *DurableAttemptSession) Unsupported() error {
	return session.precommitTerminal(lifecycle.StateUnsupported, lifecycle.ErrorUnsupported, lifecycle.ProofDurableCapabilityRefusal)
}

func (session *DurableAttemptSession) PolicyDenied() error {
	return session.precommitTerminal(lifecycle.StatePolicyDenied, lifecycle.ErrorPolicyDenied, lifecycle.ProofDurablePolicyRefusal)
}

func (session *DurableAttemptSession) precommitTerminal(to lifecycle.State, code string, proof lifecycle.Proof) error {
	inspection, err := session.store.Inspect(session.plan.AttemptID)
	if err != nil || inspection.Projection.State != lifecycle.StatePlanned {
		return attemptLedgerError("precommit_terminal", err)
	}
	_, err = session.store.Append(session.plan.AttemptID, to,
		[]lifecycle.Proof{lifecycle.ProofCompleteLedger, proof, lifecycle.ProofNoCommit},
		attemptEvidenceWithUsage(code, inspection.Projection.Usage))
	return err
}

func (session *DurableAttemptSession) SpawnIntent() error {
	_, err := session.store.Append(session.plan.AttemptID, lifecycle.StateSpawning,
		[]lifecycle.Proof{lifecycle.ProofDurableSpawnIntent}, attemptEvidenceWithUsage(lifecycle.ErrorNone, lifecycle.UnknownUsage()))
	return err
}

func (session *DurableAttemptSession) Running(processIdentitySHA256 string) error {
	evidence := attemptEvidenceWithUsage(lifecycle.ErrorNone, lifecycle.UnknownUsage())
	evidence.ProcessIdentitySHA256 = processIdentitySHA256
	_, err := session.store.Append(session.plan.AttemptID, lifecycle.StateRunning,
		[]lifecycle.Proof{lifecycle.ProofDurableProcessIdentity}, evidence)
	return err
}

func (session *DurableAttemptSession) FailBeforeSpawn() error {
	inspection, err := session.store.Inspect(session.plan.AttemptID)
	if err != nil {
		return err
	}
	proofs := []lifecycle.Proof{lifecycle.ProofDefinitiveSpawnFailure, lifecycle.ProofNonExecution}
	if inspection.Projection.State != lifecycle.StateCommitted && inspection.Projection.State != lifecycle.StateSpawning {
		return attemptLedgerError("pre_spawn_state")
	}
	_, err = session.store.Append(session.plan.AttemptID, lifecycle.StateFailed, proofs,
		attemptEvidenceWithUsage(lifecycle.ErrorSpawnFailure, inspection.Projection.Usage))
	return err
}

func (session *DurableAttemptSession) Succeed(receiptSHA256 string, usage lifecycle.Usage) error {
	return session.terminal(lifecycle.StateSucceeded, lifecycle.ErrorNone, receiptSHA256, usage)
}

func (session *DurableAttemptSession) Fail(receiptSHA256 string, usage lifecycle.Usage) error {
	return session.terminal(lifecycle.StateFailed, lifecycle.ErrorComponentFailure, receiptSHA256, usage)
}

func (session *DurableAttemptSession) Cancel(terminationProven bool, usage lifecycle.Usage) error {
	return session.triggerTerminal(lifecycle.StateCanceled, lifecycle.ErrorCanceled, lifecycle.ProofDurableCancel, terminationProven, usage)
}

func (session *DurableAttemptSession) Timeout(terminationProven bool, usage lifecycle.Usage) error {
	return session.triggerTerminal(lifecycle.StateTimedOut, lifecycle.ErrorDeadline, lifecycle.ProofDurableDeadline, terminationProven, usage)
}

func (session *DurableAttemptSession) Unknown(code string, usage lifecycle.Usage) error {
	inspection, err := session.store.Inspect(session.plan.AttemptID)
	if err != nil {
		return err
	}
	if inspection.Projection.Terminal {
		return nil
	}
	if code == "" || code == lifecycle.ErrorNone {
		code = lifecycle.ErrorInternal
	}
	_, err = session.store.Append(session.plan.AttemptID, lifecycle.StateUnknown,
		[]lifecycle.Proof{lifecycle.ProofIncompleteTerminal}, attemptEvidenceWithUsage(code, usage))
	return err
}

func (session *DurableAttemptSession) terminal(to lifecycle.State, code, receiptSHA256 string, usage lifecycle.Usage) error {
	inspection, err := session.store.Inspect(session.plan.AttemptID)
	if err != nil {
		return err
	}
	if inspection.Projection.State != lifecycle.StateRunning && inspection.Projection.State != lifecycle.StateSpawning {
		return attemptLedgerError("terminal_state")
	}
	evidence := attemptEvidenceWithUsage(code, usage)
	evidence.ReceiptSHA256 = receiptSHA256
	_, err = session.store.Append(session.plan.AttemptID, to,
		[]lifecycle.Proof{lifecycle.ProofTerminalReceipt, lifecycle.ProofTermination}, evidence)
	return err
}

func (session *DurableAttemptSession) triggerTerminal(to lifecycle.State, code string, trigger lifecycle.Proof, terminationProven bool, usage lifecycle.Usage) error {
	inspection, err := session.store.Inspect(session.plan.AttemptID)
	if err != nil {
		return err
	}
	proofs := []lifecycle.Proof{trigger, lifecycle.ProofNonExecution}
	if inspection.Projection.State == lifecycle.StatePlanned {
		proofs = []lifecycle.Proof{lifecycle.ProofCompleteLedger, trigger, lifecycle.ProofNoCommit}
	} else if inspection.Projection.State == lifecycle.StateRunning ||
		(inspection.Projection.State == lifecycle.StateSpawning && terminationProven) {
		proofs = []lifecycle.Proof{trigger, lifecycle.ProofTermination}
	}
	if !terminationProven && (inspection.Projection.State == lifecycle.StateRunning || inspection.Projection.State == lifecycle.StateSpawning) {
		return session.Unknown(lifecycle.ErrorTerminationAmbiguous, usage)
	}
	_, err = session.store.Append(session.plan.AttemptID, to, proofs, attemptEvidenceWithUsage(code, usage))
	return err
}

func processAttemptIdentity(plan lifecycle.Plan, command *exec.Cmd) (string, error) {
	if command == nil || command.Process == nil || command.Process.Pid <= 0 {
		return "", attemptLedgerError("process_identity")
	}
	projection := struct {
		AttemptID string `json:"attempt_id"`
		PID       int    `json:"pid"`
	}{AttemptID: plan.AttemptID, PID: command.Process.Pid}
	return contentMinimizedAttemptDigest("process", projection)
}

func attemptTerminalReceipt(command *exec.Cmd, waitErr error) string {
	exitCode := -1
	if command != nil && command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
	}
	projection := struct {
		Exited   bool `json:"exited"`
		ExitCode int  `json:"exit_code"`
		Success  bool `json:"success"`
	}{Exited: command != nil && command.ProcessState != nil, ExitCode: exitCode, Success: waitErr == nil}
	digest, _ := contentMinimizedAttemptDigest("terminal-receipt", projection)
	return digest
}

func resultAttemptReceipt(result Result) (string, error) {
	return contentMinimizedAttemptDigest("result-receipt", struct {
		Status   string          `json:"status"`
		Coverage map[string]bool `json:"coverage"`
		Metrics  Metrics         `json:"metrics"`
	}{result.Status, result.Coverage, result.Metrics})
}

func providerMetricsAttemptUsage(metrics ProviderMetrics, pricing Pricing) lifecycle.Usage {
	usage := lifecycle.UnknownUsage()
	if metrics.Coverage["input_tokens"] && metrics.InputTokens >= 0 {
		usage.InputTokens = lifecycle.ObservedMetric(uint64(metrics.InputTokens)) // #nosec G115 -- nonnegative guard above.
	}
	if metrics.Coverage["output_tokens"] && metrics.OutputTokens >= 0 {
		usage.OutputTokens = lifecycle.ObservedMetric(uint64(metrics.OutputTokens)) // #nosec G115 -- nonnegative guard above.
	}
	if metrics.Coverage["estimated_cost_microusd"] && metrics.EstimatedCostMicroUSD >= 0 {
		usage.EstimatedCostMicroUSD = lifecycle.ObservedMetric(uint64(metrics.EstimatedCostMicroUSD)) // #nosec G115 -- nonnegative guard above.
	} else if metrics.Coverage["input_tokens"] && metrics.Coverage["output_tokens"] {
		if cost, err := estimateCost(metrics.InputTokens, metrics.OutputTokens, pricing); err == nil && cost >= 0 {
			usage.EstimatedCostMicroUSD = lifecycle.ObservedMetric(uint64(cost)) // #nosec G115 -- nonnegative guard above.
		}
	}
	return usage
}

func beginRunAttempt(session *DurableAttemptSession) error {
	if err := session.Commit(); err != nil {
		return err
	}
	if err := session.SpawnIntent(); err != nil {
		return joinAttemptLifecycleError(err, session.FailBeforeSpawn())
	}
	return nil
}

func finalizeRunAttempt(
	parent context.Context,
	session *DurableAttemptSession,
	result Result,
	runErr error,
	terminationProven bool,
	processReceipt string,
	timedOut bool,
	usage lifecycle.Usage,
) error {
	inspection, inspectErr := session.store.Inspect(session.plan.AttemptID)
	if inspectErr != nil {
		return joinAttemptLifecycleError(runErr, inspectErr)
	}
	if inspection.Projection.Terminal || inspection.Projection.State == lifecycle.StatePlanned {
		return runErr
	}
	var lifecycleErr error
	switch {
	case runErr == nil:
		resultReceipt, err := resultAttemptReceipt(result)
		if err == nil {
			resultReceipt, err = contentMinimizedAttemptDigest("completed-attempt", []string{processReceipt, resultReceipt})
		}
		if err != nil || !terminationProven {
			lifecycleErr = session.Unknown(lifecycle.ErrorTerminationAmbiguous, usage)
		} else {
			lifecycleErr = session.Succeed(resultReceipt, usage)
		}
	case timedOut:
		lifecycleErr = session.Timeout(terminationProven, usage)
	case errors.Is(parent.Err(), context.Canceled):
		lifecycleErr = session.Cancel(terminationProven, usage)
	case terminationProven && processReceipt != "":
		lifecycleErr = session.Fail(processReceipt, usage)
	default:
		lifecycleErr = session.Unknown(lifecycle.ErrorTerminationAmbiguous, usage)
	}
	return joinAttemptLifecycleError(runErr, lifecycleErr)
}

func UnknownAttemptUsage() lifecycle.Usage { return lifecycle.UnknownUsage() }

func contentMinimizedAttemptDigest(domain string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", attemptLedgerError("digest", err)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("agent-eval/" + domain + "/v1\x00"))
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func attemptEvidenceWithUsage(code string, usage lifecycle.Usage) lifecycle.Evidence {
	return lifecycle.Evidence{ErrorClass: code, Usage: usage}
}

func joinAttemptLifecycleError(operationErr, lifecycleErr error) error {
	if lifecycleErr == nil {
		return operationErr
	}
	if operationErr == nil {
		return fmt.Errorf("persist attempt lifecycle: %w", lifecycleErr)
	}
	return errors.Join(operationErr, fmt.Errorf("persist attempt lifecycle: %w", lifecycleErr))
}
