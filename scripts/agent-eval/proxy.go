package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type proxyRecord struct {
	StdoutBytes int64 `json:"stdout_bytes"`
	StderrBytes int64 `json:"stderr_bytes"`
	ExitCode    int   `json:"exit_code"`
}

type guardRecord struct {
	Decision string `json:"decision"`
}

type claudeHookInput struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
	} `json:"tool_input"`
}

func runClaudeBashGuard(input io.Reader, output, errorOutput io.Writer) int {
	data, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		fmt.Fprintln(errorOutput, "atl evaluation guard rejected oversized hook input")
		return 2
	}
	var hook claudeHookInput
	if err := json.Unmarshal(data, &hook); err != nil || !knownGuardTool(hook.ToolName) {
		fmt.Fprintln(errorOutput, "atl evaluation guard rejected malformed hook input")
		return 2
	}
	decision := "deny"
	var reason string
	if os.Getenv("ATL_EVAL_GUARD_MODE") == "mcp-only" {
		reason = "typed-MCP benchmark blocks every non-MCP model tool"
		return writeGuardDecision(output, errorOutput, decision, reason)
	}
	switch hook.ToolName {
	case "Bash":
		var allowed []string
		if err := json.Unmarshal([]byte(os.Getenv("ATL_EVAL_ALLOWED_COMMANDS")), &allowed); err != nil || len(allowed) == 0 {
			fmt.Fprintln(errorOutput, "atl evaluation guard has no command policy")
			return 2
		}
		reason = "benchmark Bash is limited to one reviewed atl command"
		if allowedGuardCommand(hook.ToolInput.Command, allowed) {
			decision = "allow"
			reason = "command matches the reviewed benchmark allowlist"
		}
	case "Agent":
		allowed, err := reserveDelegationSlot(os.Getenv("ATL_EVAL_GUARD_COUNTER"), os.Getenv("ATL_EVAL_MAX_DELEGATIONS"))
		if err != nil {
			fmt.Fprintln(errorOutput, "atl evaluation guard could not enforce the delegation limit")
			return 2
		}
		if allowed {
			decision = "allow"
			reason = "delegation is within the reviewed benchmark limit"
		} else {
			reason = "benchmark delegation limit reached"
		}
	case "Read":
		allowed, err := allowedReadPath(hook.ToolInput.FilePath, os.Getenv("ATL_EVAL_ALLOWED_READ_ROOTS"))
		if err != nil {
			fmt.Fprintln(errorOutput, "atl evaluation guard could not enforce the read limit")
			return 2
		}
		if allowed {
			decision = "allow"
			reason = "read target is within a reviewed benchmark root"
		} else {
			reason = "read target is outside reviewed benchmark roots"
		}
	}
	return writeGuardDecision(output, errorOutput, decision, reason)
}

func knownGuardTool(name string) bool {
	switch name {
	case "Bash", "Agent", "Read", "apply_patch", "Edit", "Write":
		return true
	default:
		return false
	}
}

func writeGuardDecision(output, errorOutput io.Writer, decision, reason string) int {
	if err := appendGuardRecord(os.Getenv("ATL_EVAL_GUARD_COUNTER"), guardRecord{Decision: decision}); err != nil {
		fmt.Fprintln(errorOutput, "atl evaluation guard could not record its decision")
		return 2
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

func allowedReadPath(path, rawRoots string) (bool, error) {
	if path == "" {
		return false, nil
	}
	var roots []string
	if err := json.Unmarshal([]byte(rawRoots), &roots); err != nil || len(roots) == 0 {
		return false, fmt.Errorf("invalid read policy")
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return false, nil
	}
	for _, root := range roots {
		root, err = filepath.Abs(root)
		if err != nil {
			return false, err
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return false, err
		}
		relative, err := filepath.Rel(root, target)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return true, nil
		}
	}
	return false, nil
}

func reserveDelegationSlot(counterPath, rawLimit string) (bool, error) {
	limit, err := strconv.Atoi(rawLimit)
	if err != nil || limit < 0 || limit > 3 || counterPath == "" {
		return false, fmt.Errorf("invalid delegation policy")
	}
	for slot := 1; slot <= limit; slot++ {
		path := filepath.Join(filepath.Dir(counterPath), fmt.Sprintf("delegation-slot-%d", slot))
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return true, file.Close()
		}
		if !os.IsExist(err) {
			return false, err
		}
	}
	return false, nil
}

func appendGuardRecord(path string, record guardRecord) error {
	if path == "" {
		return fmt.Errorf("guard counter is not configured")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
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
	if strings.HasPrefix(command, "atl --read-only ") {
		command = "atl " + strings.TrimPrefix(command, "atl --read-only ")
	}
	if !strings.HasPrefix(command, "atl ") {
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
