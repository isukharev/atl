//go:build !darwin && !windows

package agenteval

import (
	"fmt"
	"syscall"
	"testing"
)

func TestOtherUnixPreservesProcessGroupSignalError(t *testing.T) {
	signalErr := fmt.Errorf("group signal: %w", syscall.EPERM)
	if got := normalizeProcessGroupSignalError(41, signalErr); got != signalErr {
		t.Fatalf("error=%v want original=%v", got, signalErr)
	}
}
