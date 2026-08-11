package agenteval

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

type providerAttemptStage uint8

const (
	providerAttemptStageCommit providerAttemptStage = iota + 1
	providerAttemptStageRevalidate
	providerAttemptStageStart
	providerAttemptStageWait
	providerAttemptStageComplete
)

// executeProviderAttempt owns only the irrevocable process boundary. Callers
// retain command construction, error classification, timing, resource cleanup,
// authentication, durable lifecycle state, and retained artifact policy.
func executeProviderAttempt(command *exec.Cmd, commit, revalidate func() error) (providerAttemptStage, error) {
	stage, _, _, err := executeProviderAttemptWithSession(command, commit, revalidate, nil)
	return stage, err
}

func executeProviderAttemptWithSession(command *exec.Cmd, commit, revalidate func() error, session *DurableAttemptSession) (providerAttemptStage, bool, string, error) {
	if command == nil {
		return providerAttemptStageStart, false, "", fmt.Errorf("provider attempt requires a command")
	}
	if session != nil {
		inspection, err := session.store.Inspect(session.plan.AttemptID)
		if err != nil {
			return providerAttemptStageCommit, false, "", err
		}
		if inspection.Projection.State == lifecycle.StatePlanned {
			if err := session.Commit(); err != nil {
				return providerAttemptStageCommit, false, "", err
			}
		}
	}
	if commit != nil {
		if err := commit(); err != nil {
			if session != nil {
				err = joinAttemptLifecycleError(err, session.FailBeforeSpawn())
			}
			return providerAttemptStageCommit, false, "", err
		}
	}
	if revalidate != nil {
		if err := revalidate(); err != nil {
			if session != nil {
				err = joinAttemptLifecycleError(err, session.FailBeforeSpawn())
			}
			return providerAttemptStageRevalidate, false, "", err
		}
	}
	var tree *boundedProcessTree
	if session != nil {
		inspection, err := session.store.Inspect(session.plan.AttemptID)
		if err != nil {
			return providerAttemptStageStart, false, "", err
		}
		if inspection.Projection.State == lifecycle.StateCommitted {
			if err := session.SpawnIntent(); err != nil {
				return providerAttemptStageStart, false, "", err
			}
		} else if inspection.Projection.State != lifecycle.StateSpawning {
			return providerAttemptStageStart, false, "", attemptLedgerError("provider_attempt_state")
		}
		tree, err = prepareProcessTree(command)
		if err != nil {
			return providerAttemptStageStart, false, "", joinAttemptLifecycleError(err, session.FailBeforeSpawn())
		}
	}
	if err := command.Start(); err != nil {
		if tree != nil {
			err = errors.Join(err, tree.close())
		}
		if session != nil {
			var lifecycleErr error
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				lifecycleErr = session.Timeout(false, UnknownAttemptUsage())
			case errors.Is(err, context.Canceled):
				lifecycleErr = session.Cancel(false, UnknownAttemptUsage())
			default:
				lifecycleErr = session.FailBeforeSpawn()
			}
			err = joinAttemptLifecycleError(err, lifecycleErr)
		}
		return providerAttemptStageStart, false, "", err
	}
	if tree != nil {
		if err := tree.attach(); err != nil {
			cleanupErr := errors.Join(tree.kill(), command.Wait(), tree.close())
			lifecycleErr := session.Unknown(lifecycle.ErrorCleanupAmbiguous, UnknownAttemptUsage())
			return providerAttemptStageStart, false, attemptTerminalReceipt(command, err), joinAttemptLifecycleError(errors.Join(err, cleanupErr), lifecycleErr)
		}
		identity, err := processAttemptIdentity(session.plan, command)
		if err == nil {
			err = session.Running(identity)
		}
		if err != nil {
			cleanupErr := errors.Join(tree.kill(), command.Wait(), tree.close())
			lifecycleErr := session.Unknown(lifecycle.ErrorCleanupAmbiguous, UnknownAttemptUsage())
			return providerAttemptStageStart, false, attemptTerminalReceipt(command, err), joinAttemptLifecycleError(errors.Join(err, cleanupErr), lifecycleErr)
		}
	}
	waitErr := command.Wait()
	terminationProven := true
	if tree != nil {
		cleanupErr := errors.Join(tree.kill(), tree.close())
		terminationProven = cleanupErr == nil
		waitErr = errors.Join(waitErr, cleanupErr)
	}
	receipt := attemptTerminalReceipt(command, waitErr)
	if waitErr != nil {
		return providerAttemptStageWait, terminationProven, receipt, waitErr
	}
	return providerAttemptStageComplete, terminationProven, receipt, nil
}
