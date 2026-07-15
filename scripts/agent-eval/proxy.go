package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type proxyRecord struct {
	StdoutBytes int64 `json:"stdout_bytes"`
	StderrBytes int64 `json:"stderr_bytes"`
	ExitCode    int   `json:"exit_code"`
}

type claudeHookInput struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

func runClaudeBashGuard(input io.Reader, output, errorOutput io.Writer) int {
	data, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		fmt.Fprintln(errorOutput, "atl evaluation guard rejected oversized hook input")
		return 2
	}
	var hook claudeHookInput
	if err := json.Unmarshal(data, &hook); err != nil || hook.ToolName != "Bash" {
		fmt.Fprintln(errorOutput, "atl evaluation guard rejected malformed hook input")
		return 2
	}
	var allowed []string
	if err := json.Unmarshal([]byte(os.Getenv("ATL_EVAL_ALLOWED_COMMANDS")), &allowed); err != nil || len(allowed) == 0 {
		fmt.Fprintln(errorOutput, "atl evaluation guard has no command policy")
		return 2
	}
	decision := "deny"
	reason := "benchmark Bash is limited to one reviewed atl command"
	if allowedGuardCommand(hook.ToolInput.Command, allowed) {
		decision = "allow"
		reason = "command matches the reviewed benchmark allowlist"
	}
	response := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse", "permissionDecision": decision,
			"permissionDecisionReason": reason,
		},
	}
	if err := json.NewEncoder(output).Encode(response); err != nil {
		fmt.Fprintln(errorOutput, "atl evaluation guard could not encode its decision")
		return 2
	}
	return 0
}

func allowedGuardCommand(command string, prefixes []string) bool {
	command = strings.TrimSpace(command)
	for _, export := range []string{"export ATL_READ_ONLY=1;", "export ATL_READ_ONLY=1\n"} {
		if strings.HasPrefix(command, export) {
			command = strings.TrimSpace(strings.TrimPrefix(command, export))
			break
		}
	}
	if command == "command -v atl" {
		return true
	}
	if strings.ContainsAny(command, "\r\n;&|`><") || strings.Contains(command, "$(") {
		return false
	}
	for _, prefix := range prefixes {
		if command == prefix || strings.HasPrefix(command, prefix+" ") {
			return true
		}
	}
	return false
}

func runATLProxy(args []string) int {
	if os.Getenv("ATL_READ_ONLY") != "1" {
		fmt.Fprintln(os.Stderr, "atl evaluation proxy requires ATL_READ_ONLY=1")
		return 2
	}
	realBinary := os.Getenv("ATL_EVAL_REAL_BINARY")
	counterPath := os.Getenv("ATL_EVAL_COUNTER")
	if realBinary == "" || counterPath == "" {
		fmt.Fprintln(os.Stderr, "atl evaluation proxy is not configured")
		return 2
	}
	command := exec.Command(realBinary, args...)
	command.Stdin = os.Stdin
	stdout, err := command.StdoutPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := command.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var stdoutBytes, stderrBytes int64
	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() {
		defer copyWG.Done()
		stdoutBytes, _ = io.Copy(os.Stdout, stdout)
	}()
	go func() {
		defer copyWG.Done()
		stderrBytes, _ = io.Copy(os.Stderr, stderr)
	}()
	waitErr := command.Wait()
	copyWG.Wait()
	exitCode := 0
	if waitErr != nil {
		if exitError, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = 1
		}
	}
	if err := appendProxyRecord(counterPath, proxyRecord{StdoutBytes: stdoutBytes, StderrBytes: stderrBytes, ExitCode: exitCode}); err != nil {
		fmt.Fprintln(os.Stderr, "record atl evaluation metric:", err)
		return 1
	}
	return exitCode
}

func appendProxyRecord(path string, record proxyRecord) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encodeErr := encoder.Encode(record)
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}
