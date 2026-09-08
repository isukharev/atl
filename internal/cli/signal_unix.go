//go:build !windows

package cli

import (
	"os"
	"syscall"
)

func terminationSignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM} }
