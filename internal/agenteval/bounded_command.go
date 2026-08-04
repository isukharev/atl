package agenteval

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

type boundedCommandResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

func executeBoundedCommand(
	ctx context.Context,
	binary string,
	args []string,
	directory string,
	environment []string,
	timeout time.Duration,
	maxStdoutBytes int64,
	maxStderrBytes int64,
) (boundedCommandResult, error) {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout := &boundedCommandBuffer{maximum: maxStdoutBytes}
	stderr := &boundedCommandBuffer{maximum: maxStderrBytes}
	command := exec.CommandContext(commandCtx, binary, args...)
	tree, err := prepareProcessTree(command)
	if err != nil {
		return boundedCommandResult{}, fmt.Errorf("prepare ATL command process tree")
	}
	command.Cancel = tree.kill
	command.Dir = directory
	command.Env = append([]string(nil), environment...)
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = 250 * time.Millisecond
	if err := command.Start(); err != nil {
		_ = tree.close()
		return boundedCommandResult{}, fmt.Errorf("start ATL command")
	}
	if err := tree.attach(); err != nil {
		_ = tree.kill()
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = tree.close()
		return boundedCommandResult{}, fmt.Errorf("attach ATL command process tree")
	}
	runErr := command.Wait()
	if err := errors.Join(tree.kill(), tree.close()); err != nil {
		return boundedCommandResult{}, fmt.Errorf("terminate ATL command process tree")
	}
	if stdout.exceeded || stderr.exceeded {
		return boundedCommandResult{}, fmt.Errorf("ATL command exceeded its output bound")
	}
	if commandCtx.Err() != nil {
		return boundedCommandResult{}, fmt.Errorf("ATL command did not complete: %w", commandCtx.Err())
	}
	result := boundedCommandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	if runErr == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if !errors.As(runErr, &exitError) || exitError.ExitCode() < 1 || exitError.ExitCode() > 255 {
		return boundedCommandResult{}, fmt.Errorf("start or wait for ATL command")
	}
	result.exitCode = exitError.ExitCode()
	return result, nil
}
