package agenteval

import (
	"bytes"
	"context"
	"errors"

	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/extension"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

type ExtensionConformanceCaseReport struct {
	ID        string              `json:"id"`
	Operation extension.Operation `json:"operation"`
	Terminal  string              `json:"terminal"`
	Status    string              `json:"status"`
}

// VerifyExtensionProtocolFiles runs the internal protocol-only conformance
// command. It does not claim a sandbox or whole-product compatibility.
func VerifyExtensionProtocolFiles(
	ctx context.Context,
	manifestPath, executablePath, bundlePath, ledgerRoot string,
) (ExtensionConformanceReport, error) {
	manifestData, err := readStableExtensionContractFile(manifestPath, extension.MaxManifestBytes)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	bundleData, err := readStableExtensionContractFile(bundlePath, extensionConformanceMaxBytes)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	store, err := openOrCreateAttemptLedgerStore(ledgerRoot)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionOutcomeUnknown
	}
	return verifyExtensionProtocol(ctx, manifestData, executablePath, nil, bundleData, store, "")
}

// VerifyAgentAdapterProtocolFiles applies the agent-adapter semantic contract
// before invoking the generic process protocol verifier. It remains a scoped
// protocol diagnostic and grants no filesystem, network, or credential
// authority to the selected executable.
func VerifyAgentAdapterProtocolFiles(
	ctx context.Context,
	manifestPath, executablePath, bundlePath, contractPath, ledgerRoot string,
) (ExtensionConformanceReport, error) {
	manifestData, err := readStableExtensionContractFile(manifestPath, extension.MaxManifestBytes)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	bundleData, err := readStableExtensionContractFile(bundlePath, extensionConformanceMaxBytes)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	contractData, err := readStableExtensionContractFile(contractPath, AgentAdapterContractMaxBytes)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	manifest, err := extension.DecodeManifest(manifestData)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	contract, err := DecodeAgentAdapterContract(bytes.NewReader(contractData))
	if err != nil || validateAgentAdapterProcessBinding(manifest, contract) != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	store, err := openOrCreateAttemptLedgerStore(ledgerRoot)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionOutcomeUnknown
	}
	return verifyExtensionProtocol(ctx, manifestData, executablePath, nil, bundleData, store,
		sha256HexBytes(contractData))
}

// VerifyExecutionBackendProtocolFiles validates one non-hermetic process
// implementation against both the shared process protocol and the neutral
// backend contract. This diagnostic never upgrades an arbitrary child to
// isolated or hermetic; those assurances require backend-owned enforcement.
func VerifyExecutionBackendProtocolFiles(
	ctx context.Context,
	manifestPath, executablePath, bundlePath, contractPath, planPath, ledgerRoot string,
) (ExtensionConformanceReport, error) {
	manifestData, err := readStableExtensionContractFile(manifestPath, extension.MaxManifestBytes)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	bundleData, err := readStableExtensionContractFile(bundlePath, extensionConformanceMaxBytes)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	contractData, err := readStableExtensionContractFile(contractPath, executionbackend.MaxContractBytes)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	planData, err := readStableExtensionContractFile(planPath, executionbackend.MaxPlanBytes)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	manifest, err := extension.DecodeManifest(manifestData)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	contract, err := executionbackend.DecodeContract(bytes.NewReader(contractData))
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	plan, err := executionbackend.DecodePlan(bytes.NewReader(planData))
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	if _, err := executionbackend.Admit(contract, plan); err != nil || validateExecutionBackendProcessBinding(manifest, contract, plan) != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	semanticDigest, err := contentMinimizedAttemptDigest("execution-backend-process-contract", []string{sha256HexBytes(contractData), sha256HexBytes(planData)})
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	store, err := openOrCreateAttemptLedgerStore(ledgerRoot)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionOutcomeUnknown
	}
	return verifyExtensionProtocol(ctx, manifestData, executablePath, nil, bundleData, store, semanticDigest)
}

func validateExecutionBackendProcessBinding(manifest extension.Manifest, contract executionbackend.Contract, plan executionbackend.Plan) error {
	if manifest.Component.Role != extension.RoleExecutionBackend || manifest.Component.ID != contract.BackendID ||
		manifest.Component.Version != contract.BackendVersion || manifest.ExecutableSHA256 != contract.ContentSHA256 ||
		contract.Assurance != executionbackend.AssuranceLocalProcess || plan.Program.Kind != executionbackend.ProgramExternalAdapter ||
		len(manifest.Component.Operations) != 2 {
		return errExtensionCompatibility
	}
	for _, claim := range manifest.Component.Capabilities {
		if claim.State != extension.CapabilitySupported {
			return errExtensionCompatibility
		}
	}
	for _, field := range manifest.ConfigurationSchema {
		if field.Required {
			return errExtensionCompatibility
		}
	}
	return nil
}

func validateAgentAdapterProcessBinding(manifest extension.Manifest, contract AgentAdapterContract) error {
	if manifest.Component.Role != extension.RoleAgentAdapter || manifest.Component.ID != contract.AdapterID ||
		manifest.Component.Version != contract.AdapterVersion || manifest.ExecutableSHA256 != contract.ExecutableSHA256 {
		return errExtensionCompatibility
	}
	if len(manifest.Component.Operations) != 3 {
		return errExtensionCompatibility
	}
	for _, claim := range manifest.Component.Capabilities {
		if claim.State != extension.CapabilitySupported {
			return errExtensionCompatibility
		}
	}
	if len(manifest.ConfigurationSchema) != len(contract.ConfigurationKeys) {
		return errExtensionCompatibility
	}
	for index := range manifest.ConfigurationSchema {
		if manifest.ConfigurationSchema[index].Name != contract.ConfigurationKeys[index].Name ||
			contract.ConfigurationKeys[index].Sensitive {
			return errExtensionCompatibility
		}
	}
	return nil
}

func prepareExtensionProtocolAttempts(
	store *AttemptLedgerStore,
	manifest extension.Manifest,
	bundle ExtensionConformanceBundle,
	manifestDigest, bundleDigest, adapterContractDigest string,
) ([]*DurableAttemptSession, func(), error) {
	if store == nil {
		return nil, func() {}, nil
	}
	sessions, err := prepareExtensionAttemptSessions(store, manifest, bundle, manifestDigest, bundleDigest, adapterContractDigest)
	if err != nil {
		return nil, nil, err
	}
	closePlanned := func() {
		for _, session := range sessions {
			inspection, inspectErr := session.store.Inspect(session.plan.AttemptID)
			if inspectErr == nil && inspection.Projection.State == lifecycle.StatePlanned {
				_ = session.Cancel(false, inspection.Projection.Usage)
			}
		}
	}
	return sessions, closePlanned, nil
}

func beginExtensionProtocolAttempt(sessions []*DurableAttemptSession, index int) (*DurableAttemptSession, error) {
	if sessions == nil {
		return nil, nil
	}
	attempt := sessions[index]
	if err := attempt.Commit(); err != nil {
		return nil, err
	}
	if err := attempt.SpawnIntent(); err != nil {
		return nil, errors.Join(err, attempt.FailBeforeSpawn())
	}
	return attempt, nil
}

func finalizeExtensionAttempt(
	attempt *DurableAttemptSession,
	testCase ExtensionConformanceCase,
	report ExtensionConformanceCaseReport,
	assurance string,
	runErr, removeErr error,
) error {
	inspection, err := attempt.store.Inspect(attempt.plan.AttemptID)
	if err != nil || inspection.Projection.Terminal {
		return err
	}
	if runErr != nil || removeErr != nil {
		return attempt.Unknown(lifecycle.ErrorTerminationAmbiguous, inspection.Projection.Usage)
	}
	if testCase.Expected.Type == extensionExpectedCanceled {
		return attempt.Cancel(true, inspection.Projection.Usage)
	}
	receipt, err := contentMinimizedAttemptDigest("extension-case-receipt", struct {
		Report    ExtensionConformanceCaseReport `json:"report"`
		Assurance string                         `json:"assurance"`
	}{report, assurance})
	if err != nil {
		return attempt.Unknown(lifecycle.ErrorInternal, inspection.Projection.Usage)
	}
	return attempt.Succeed(receipt, inspection.Projection.Usage)
}
