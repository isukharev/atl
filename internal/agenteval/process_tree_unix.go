//go:build !windows

package agenteval

import (
	"errors"
	"os/exec"
	"syscall"
)

type boundedProcessTree struct {
	command *exec.Cmd
}

func prepareProcessTree(command *exec.Cmd) (*boundedProcessTree, error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &boundedProcessTree{command: command}, nil
}

func (t *boundedProcessTree) attach() error {
	return nil
}

func (t *boundedProcessTree) interrupt() error {
	return t.signal(syscall.SIGINT)
}

func (t *boundedProcessTree) kill() error {
	return t.signal(syscall.SIGKILL)
}

func (t *boundedProcessTree) close() error {
	return nil
}

func (t *boundedProcessTree) signal(signal syscall.Signal) error {
	if t == nil || t.command == nil || t.command.Process == nil {
		return nil
	}
	err := syscall.Kill(-t.command.Process.Pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return normalizeProcessGroupSignalError(t.command.Process.Pid, err)
}
