//go:build !darwin && !windows

package agenteval

func normalizeProcessGroupSignalError(_ int, signalErr error) error {
	return signalErr
}
