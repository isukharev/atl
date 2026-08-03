package agenteval

import (
	"fmt"
	"os/exec"
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
	if command == nil {
		return providerAttemptStageStart, fmt.Errorf("provider attempt requires a command")
	}
	if commit != nil {
		if err := commit(); err != nil {
			return providerAttemptStageCommit, err
		}
	}
	if revalidate != nil {
		if err := revalidate(); err != nil {
			return providerAttemptStageRevalidate, err
		}
	}
	if err := command.Start(); err != nil {
		return providerAttemptStageStart, err
	}
	if err := command.Wait(); err != nil {
		return providerAttemptStageWait, err
	}
	return providerAttemptStageComplete, nil
}
