package agenteval

import (
	"context"
	"errors"

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
	return verifyExtensionProtocol(ctx, manifestData, executablePath, nil, bundleData, store)
}

func prepareExtensionProtocolAttempts(
	store *AttemptLedgerStore,
	manifest extension.Manifest,
	bundle ExtensionConformanceBundle,
	manifestDigest, bundleDigest string,
) ([]*DurableAttemptSession, func(), error) {
	if store == nil {
		return nil, func() {}, nil
	}
	sessions, err := prepareExtensionAttemptSessions(store, manifest, bundle, manifestDigest, bundleDigest)
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
