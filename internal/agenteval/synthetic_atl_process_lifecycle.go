package agenteval

import (
	"errors"
	"fmt"
	"os"

	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

// Summary returns a content-free snapshot of backend and admission counts.
func (p *SyntheticATLProcess) Summary() SyntheticATLProcessSummary {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.summaryLocked()
}

func (p *SyntheticATLProcess) summaryLocked() SyntheticATLProcessSummary {
	methods := map[string]int{}
	unexpected, duplicates := 0, 0
	if p.backend != nil {
		methods, unexpected, duplicates = p.backend.Summary()
	}
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
		if p.attemptSession != nil {
			receipt, digestErr := contentMinimizedAttemptDigest("synthetic-atl-process-receipt", struct {
				PreflightSHA256 string                     `json:"preflight_sha256"`
				Summary         SyntheticATLProcessSummary `json:"summary"`
			}{p.preflightReceipt, p.summaryLocked()})
			closeErr = errors.Join(closeErr, digestErr)
			if closeErr != nil {
				closeErr = joinAttemptLifecycleError(closeErr,
					p.attemptSession.Unknown(lifecycle.ErrorCleanupAmbiguous, lifecycle.UnknownUsage()))
			} else if p.startErr != nil || p.operationFailed {
				closeErr = joinAttemptLifecycleError(p.startErr,
					p.attemptSession.Fail(receipt, lifecycle.UnknownUsage()))
			} else {
				closeErr = p.attemptSession.Succeed(receipt, lifecycle.UnknownUsage())
			}
		}
		p.closeErr = closeErr
	})
	return p.closeErr
}
