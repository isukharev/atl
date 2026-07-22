package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/agenteval"
)

type proxyRecord struct {
	CommandFamily                string `json:"command_family,omitempty"`
	CalibrationObservationSHA256 string `json:"calibration_observation_sha256,omitempty"`
	Denied                       bool   `json:"denied,omitempty"`
	StdoutBytes                  int64  `json:"stdout_bytes"`
	StderrBytes                  int64  `json:"stderr_bytes"`
	ExitCode                     int    `json:"exit_code"`
}

type guardRecord struct {
	Decision string `json:"decision"`
	Family   string `json:"family"`
}

type claudeHookInput struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	} `json:"tool_input"`
}

const (
	privateCLIResultInlineBytes = 24 << 10
	privateCLIResultReadLines   = 500
	privateCLIResultReadLimit   = 8
)

var cliRuleNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var privateCLIResultNameRE = regexp.MustCompile(`^atl-result-[0-9a-f]{64}\.txt$`)

func runClaudeBashGuard(input io.Reader, output, errorOutput io.Writer) int {
	data, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		fmt.Fprintln(errorOutput, "atl evaluation guard rejected oversized hook input")
		return 2
	}
	var hook claudeHookInput
	if err := json.Unmarshal(data, &hook); err != nil || !validGuardToolName(hook.ToolName) {
		fmt.Fprintln(errorOutput, "atl evaluation guard rejected malformed hook input")
		return 2
	}
	decision := "deny"
	family := "other"
	var reason string
	guardMode := os.Getenv("ATL_EVAL_GUARD_MODE")
	if guardMode == "provider-calibration" {
		reason = "provider calibration admits only the literal atl version command"
		if hook.ToolName == "Bash" && hook.ToolInput.Command == "atl version" {
			decision = "allow"
			family = "atl"
			reason = "command exactly matches the backend-free calibration contract"
		}
		return writeGuardDecision(output, errorOutput, decision, family, reason)
	}
	if guardMode == "mcp-only" {
		reason = "typed-MCP benchmark blocks every non-MCP model tool"
		if hook.ToolName == "StructuredOutput" || allowedMCPGuardTool(hook.ToolName, os.Getenv("ATL_EVAL_ALLOWED_MCP_TOOLS")) {
			decision = "allow"
			family = "mcp"
			reason = "tool is required structured output or an exact reviewed MCP tool"
		}
		return writeGuardDecision(output, errorOutput, decision, family, reason)
	}
	if guardMode == "mcp-with-skill-read" {
		reason = "private-live MCP allows only confined reads of reviewed skill files"
		switch hook.ToolName {
		case "StructuredOutput":
			decision = "allow"
			family = "structured_output"
			reason = "structured output is required by the reviewed response schema"
		case "Read":
			allowed, err := allowedPrivateReadPath(hook.ToolInput.FilePath, os.Getenv("ATL_EVAL_ALLOWED_READ_ROOTS"))
			if err != nil {
				fmt.Fprintln(errorOutput, "atl evaluation guard could not enforce the read limit")
				return 2
			}
			if allowed {
				decision = "allow"
				family = "read"
				reason = "read target is within a reviewed benchmark root"
				if skillRead, _ := allowedPrivateReadPath(hook.ToolInput.FilePath, os.Getenv("ATL_EVAL_SKILL_READ_ROOTS")); skillRead {
					family = "skill_read"
					reason = "read target is within the reviewed skill root"
				}
			}
		case "Bash":
			if allowedSkillReadCommand(hook.ToolInput.Command, os.Getenv("ATL_EVAL_ALLOWED_READ_ROOTS")) {
				decision = "allow"
				family = "read"
				reason = "command contains only confined reader invocations"
				if allowedSkillReadCommand(hook.ToolInput.Command, os.Getenv("ATL_EVAL_SKILL_READ_ROOTS")) {
					family = "skill_read"
					reason = "command contains only confined skill-reader invocations"
				}
			}
		default:
			if allowedMCPGuardTool(hook.ToolName, os.Getenv("ATL_EVAL_ALLOWED_MCP_TOOLS")) {
				decision = "allow"
				family = "mcp"
				reason = "tool is an exact reviewed MCP tool"
			}
		}
		return writeGuardDecision(output, errorOutput, decision, family, reason)
	}
	if guardMode == "private-cli" {
		reason = "confined CLI allows only reviewed skill reads and atl invocations"
		switch hook.ToolName {
		case "Read":
			allowed, err := allowedPrivateCLIResultRead(hook.ToolInput.FilePath, hook.ToolInput.Offset, hook.ToolInput.Limit,
				os.Getenv("ATL_EVAL_CLI_RESULT_DIR"), os.Getenv("ATL_EVAL_GUARD_COUNTER"))
			if err != nil {
				fmt.Fprintln(errorOutput, "atl evaluation guard could not enforce the read limit")
				return 2
			}
			if allowed {
				decision = "allow"
				family = "tool_result_read"
				reason = "read target is a bounded result of a reviewed atl invocation"
				break
			}
			allowed, err = allowedPrivateReadPath(hook.ToolInput.FilePath, os.Getenv("ATL_EVAL_ALLOWED_READ_ROOTS"))
			if err != nil {
				fmt.Fprintln(errorOutput, "atl evaluation guard could not enforce the read limit")
				return 2
			}
			if allowed {
				decision = "allow"
				family = "read"
				reason = "read target is within a reviewed benchmark root"
				if skillRead, _ := allowedPrivateReadPath(hook.ToolInput.FilePath, os.Getenv("ATL_EVAL_SKILL_READ_ROOTS")); skillRead {
					family = "skill_read"
					reason = "read target is within the reviewed skill root"
				}
			}
		case "Bash":
			if allowedSkillReadCommand(hook.ToolInput.Command, os.Getenv("ATL_EVAL_ALLOWED_READ_ROOTS")) {
				decision = "allow"
				family = "read"
				reason = "command contains only confined reader invocations"
				if allowedSkillReadCommand(hook.ToolInput.Command, os.Getenv("ATL_EVAL_SKILL_READ_ROOTS")) {
					family = "skill_read"
					reason = "command contains only confined skill-reader invocations"
				}
			} else if safePrivateCLICommandShape(hook.ToolInput.Command) || reviewedWriteEnvironmentEnabled() && safeReviewedWriteCLICommandShape(hook.ToolInput.Command) {
				decision = "allow"
				family = "atl"
				reason = "command shape delegates exact argument enforcement to the atl evaluation shim"
			}
		}
		return writeGuardDecision(output, errorOutput, decision, family, reason)
	}
	switch hook.ToolName {
	case "Bash":
		var allowed []string
		if err := json.Unmarshal([]byte(os.Getenv("ATL_EVAL_ALLOWED_COMMANDS")), &allowed); err != nil || len(allowed) == 0 {
			fmt.Fprintln(errorOutput, "atl evaluation guard has no command policy")
			return 2
		}
		reason = "benchmark Bash is limited to a bounded block of reviewed atl commands"
		if allowedGuardCommand(hook.ToolInput.Command, allowed) {
			decision = "allow"
			family = "atl"
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
			family = "agent"
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
			family = "read"
			reason = "read target is within a reviewed benchmark root"
		} else {
			reason = "read target is outside reviewed benchmark roots"
		}
	}
	return writeGuardDecision(output, errorOutput, decision, family, reason)
}

var sedLineRangeRE = regexp.MustCompile(`^(\d+)(?:,(\d+))?p$`)

func allowedSkillReadCommand(command, rawRoots string) bool {
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsAny(command, "\r|`><$()") || strings.Contains(strings.ReplaceAll(command, "&&", ""), "&") {
		return false
	}
	command = strings.ReplaceAll(command, "\n", ";")
	parts := strings.Split(strings.ReplaceAll(command, "&&", ";"), ";")
	if len(parts) > 8 {
		return false
	}
	for _, part := range parts {
		fields := strings.Fields(strings.TrimSpace(part))
		var targets []string
		switch {
		case len(fields) >= 2 && len(fields) <= 17 && fields[0] == "cat":
			targets = fields[1:]
		case len(fields) == 4 && fields[0] == "sed" && fields[1] == "-n" && validSedRange(fields[2]):
			targets = fields[3:]
		case len(fields) >= 3 && len(fields) <= 18 && fields[0] == "wc" && fields[1] == "-l":
			targets = fields[2:]
		default:
			return false
		}
		for _, target := range targets {
			target = strings.Trim(target, `'"`)
			allowed, err := allowedPrivateReadPath(target, rawRoots)
			if err != nil || !allowed {
				return false
			}
		}
	}
	return true
}

func validSedRange(value string) bool {
	value = strings.Trim(value, `'"`)
	match := sedLineRangeRE.FindStringSubmatch(value)
	if match == nil {
		return false
	}
	start, _ := strconv.Atoi(match[1])
	end := start
	if match[2] != "" {
		end, _ = strconv.Atoi(match[2])
	}
	return start >= 1 && end >= start && end <= 10_000
}

func runSkillReader(name string, args []string, output, errorOutput io.Writer) int {
	var target string
	var start, end int
	switch name {
	case "cat":
		return runSkillCat(args, output, errorOutput)
	case "sed":
		if len(args) != 3 || args[0] != "-n" || !validSedRange(args[1]) {
			fmt.Fprintln(errorOutput, "private benchmark sed accepts only -n START,ENDp FILE")
			return 2
		}
		match := sedLineRangeRE.FindStringSubmatch(strings.Trim(args[1], `'"`))
		start, _ = strconv.Atoi(match[1])
		end = start
		if match[2] != "" {
			end, _ = strconv.Atoi(match[2])
		}
		target = args[2]
	case "wc":
		if len(args) < 2 || len(args) > 17 || args[0] != "-l" {
			fmt.Fprintln(errorOutput, "private benchmark wc accepts only -l FILE...")
			return 2
		}
		return runSkillLineCount(args[1:], output, errorOutput)
	default:
		return 2
	}
	resolved, allowed, err := resolveAllowedReadPath(target, os.Getenv("ATL_EVAL_ALLOWED_READ_ROOTS"), true)
	if err != nil || !allowed {
		fmt.Fprintln(errorOutput, "private benchmark reader denied path")
		return 2
	}
	file, err := os.Open(resolved)
	if err != nil {
		fmt.Fprintln(errorOutput, "private benchmark reader could not open file")
		return 1
	}
	data, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > 1<<20 {
		fmt.Fprintln(errorOutput, "private benchmark reader rejected file")
		return 1
	}
	if name == "sed" {
		lines := bytes.SplitAfter(data, []byte{'\n'})
		if start > len(lines) {
			return 0
		}
		if end > len(lines) {
			end = len(lines)
		}
		data = bytes.Join(lines[start-1:end], nil)
	}
	if _, err := output.Write(data); err != nil {
		fmt.Fprintln(errorOutput, "private benchmark reader could not write output")
		return 1
	}
	return 0
}

func runSkillCat(paths []string, output, errorOutput io.Writer) int {
	if len(paths) < 1 || len(paths) > 16 {
		fmt.Fprintln(errorOutput, "private benchmark cat accepts 1..16 files")
		return 2
	}
	resolved := make([]string, 0, len(paths))
	for _, path := range paths {
		target, allowed, err := resolveAllowedReadPath(path, os.Getenv("ATL_EVAL_ALLOWED_READ_ROOTS"), true)
		if err != nil || !allowed {
			fmt.Fprintln(errorOutput, "private benchmark reader denied path")
			return 2
		}
		resolved = append(resolved, target)
	}
	var combined bytes.Buffer
	for _, path := range resolved {
		file, err := os.Open(path)
		if err != nil {
			fmt.Fprintln(errorOutput, "private benchmark reader could not open file")
			return 1
		}
		remaining := int64((1 << 20) + 1 - combined.Len())
		data, readErr := io.ReadAll(io.LimitReader(file, remaining))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || combined.Len()+len(data) > 1<<20 {
			fmt.Fprintln(errorOutput, "private benchmark reader rejected files")
			return 1
		}
		_, _ = combined.Write(data)
	}
	if _, err := output.Write(combined.Bytes()); err != nil {
		return 1
	}
	return 0
}

func runSkillLineCount(paths []string, output, errorOutput io.Writer) int {
	total := 0
	for _, path := range paths {
		resolved, allowed, err := resolveAllowedReadPath(path, os.Getenv("ATL_EVAL_ALLOWED_READ_ROOTS"), true)
		if err != nil || !allowed {
			fmt.Fprintln(errorOutput, "private benchmark reader denied path")
			return 2
		}
		file, err := os.Open(resolved)
		if err != nil {
			fmt.Fprintln(errorOutput, "private benchmark reader could not open file")
			return 1
		}
		data, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(data) > 1<<20 {
			fmt.Fprintln(errorOutput, "private benchmark reader rejected file")
			return 1
		}
		count := bytes.Count(data, []byte{'\n'})
		total += count
		if _, err := fmt.Fprintf(output, "%d %s\n", count, path); err != nil {
			return 1
		}
	}
	if len(paths) > 1 {
		if _, err := fmt.Fprintf(output, "%d total\n", total); err != nil {
			return 1
		}
	}
	return 0
}

var guardToolNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{0,255}$`)

func validGuardToolName(name string) bool {
	return guardToolNameRE.MatchString(name)
}

func allowedMCPGuardTool(name, rawAllowed string) bool {
	var allowed []string
	if json.Unmarshal([]byte(rawAllowed), &allowed) != nil {
		return false
	}
	for _, candidate := range allowed {
		if name == candidate {
			return true
		}
	}
	return false
}

func writeGuardDecision(output, errorOutput io.Writer, decision, family, reason string) int {
	// The family is deliberately coarse and content-free. It proves which
	// reviewed policy branch admitted a tool without retaining its command,
	// arguments, path, or backend data.
	if err := appendGuardRecord(os.Getenv("ATL_EVAL_GUARD_COUNTER"), guardRecord{Decision: decision, Family: family}); err != nil {
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
	_, allowed, err := resolveAllowedReadPath(path, rawRoots, false)
	return allowed, err
}

func allowedPrivateReadPath(path, rawRoots string) (bool, error) {
	_, allowed, err := resolveAllowedReadPath(path, rawRoots, true)
	return allowed, err
}

func allowedPrivateCLIResultRead(path string, offset, limit int, root, counterPath string) (bool, error) {
	if path == "" || root == "" {
		return false, nil
	}
	if offset < 0 || limit < 1 || limit > privateCLIResultReadLines {
		return false, nil
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || counterPath == "" {
		return false, fmt.Errorf("invalid result read policy")
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false, nil
	}
	canonicalTarget, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false, nil
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalTarget)
	if err != nil || filepath.Dir(relative) != "." || filepath.IsAbs(relative) || !privateCLIResultNameRE.MatchString(relative) {
		return false, nil
	}
	info, err := os.Lstat(canonicalTarget)
	if err != nil {
		return false, nil
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o400 ||
		info.Size() <= privateCLIResultInlineBytes || info.Size() > 4<<20 {
		return false, nil
	}
	data, err := os.ReadFile(canonicalTarget)
	if err != nil || int64(len(data)) != info.Size() {
		return false, nil
	}
	contentDigest := sha256.Sum256(data)
	if relative != fmt.Sprintf("atl-result-%x.txt", contentDigest) {
		return false, nil
	}
	for slot := 1; slot <= privateCLIResultReadLimit; slot++ {
		marker := filepath.Join(filepath.Dir(counterPath), fmt.Sprintf("result-read-slot-%d", slot))
		file, openErr := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if openErr == nil {
			return true, file.Close()
		}
		if !os.IsExist(openErr) {
			return false, openErr
		}
	}
	return false, nil
}

func resolveAllowedReadPath(path, rawRoots string, requireWorkspace bool) (string, bool, error) {
	if path == "" {
		return "", false, nil
	}
	var roots []string
	if err := json.Unmarshal([]byte(rawRoots), &roots); err != nil || len(roots) == 0 {
		return "", false, fmt.Errorf("invalid read policy")
	}
	target := path
	if !filepath.IsAbs(target) {
		workspace := os.Getenv("ATL_EVAL_WORKSPACE_ROOT")
		if workspace == "" {
			if requireWorkspace {
				return "", false, nil
			}
		} else {
			if !filepath.IsAbs(workspace) {
				return "", false, fmt.Errorf("invalid workspace read policy")
			}
			target = filepath.Join(workspace, target)
		}
	}
	target, err := filepath.Abs(target)
	if err != nil {
		return "", false, err
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return "", false, nil
	}
	for _, root := range roots {
		root, err = filepath.Abs(root)
		if err != nil {
			return "", false, err
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return "", false, err
		}
		relative, err := filepath.Rel(root, target)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return target, true, nil
		}
	}
	return "", false, nil
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
	if command == "" || strings.Contains(command, "\r") {
		return false
	}
	// Parse only a small shell-list subset. Every resulting command is checked
	// independently; unsupported operators and substitutions fail closed.
	if strings.ContainsAny(command, "|`><") || strings.Contains(command, "$(") {
		return false
	}
	command = strings.ReplaceAll(command, "&&", "\n")
	command = strings.ReplaceAll(command, ";", "\n")
	if strings.Contains(command, "&") {
		return false
	}
	lines := strings.Split(command, "\n")
	if len(lines) == 0 || len(lines) > 8 {
		return false
	}
	exportSeen := false
	commandCheckSeen := false
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			return false
		}
		switch line {
		case "export ATL_READ_ONLY=1":
			if exportSeen || i != 0 {
				return false
			}
			exportSeen = true
		case "command -v atl":
			if commandCheckSeen {
				return false
			}
			commandCheckSeen = true
		default:
			if !allowedSingleGuardATLCommand(line, prefixes) {
				return false
			}
		}
	}
	return true
}

func allowedSingleGuardATLCommand(command string, prefixes []string) bool {
	if strings.HasPrefix(command, "ATL_READ_ONLY=1 ") {
		command = strings.TrimSpace(strings.TrimPrefix(command, "ATL_READ_ONLY=1 "))
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

func safePrivateCLICommandShape(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" || strings.Contains(command, "\r") {
		return false
	}
	if strings.HasPrefix(command, "export ATL_READ_ONLY=1;") {
		command = strings.TrimSpace(strings.TrimPrefix(command, "export ATL_READ_ONLY=1;"))
	} else if strings.HasPrefix(command, "ATL_READ_ONLY=1 ") {
		command = strings.TrimSpace(strings.TrimPrefix(command, "ATL_READ_ONLY=1 "))
	}
	lines := strings.Split(command, "\n")
	if len(lines) == 0 || len(lines) > 16 {
		return false
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case line == "export ATL_READ_ONLY=1":
		case line == "command -v atl":
		case strings.HasPrefix(line, "atl ") && !strings.ContainsAny(line, "\x00;&|`><$(){}[]*?!~#"):
		default:
			return false
		}
	}
	return true
}

func safeReviewedWriteCLICommandShape(command string) bool {
	command = strings.TrimSpace(command)
	const prefix = "env -u ATL_READ_ONLY "
	if strings.ContainsAny(command, "\r\n") || !strings.HasPrefix(command, prefix) {
		return false
	}
	command = strings.TrimSpace(strings.TrimPrefix(command, prefix))
	if !strings.HasPrefix(command, "atl ") {
		return false
	}

	// The exact argv broker is the authority for which atl invocation may run,
	// but this outer hook must first prove that the shell will execute only one
	// command. Structured field values need JSON punctuation, so admit it only
	// while single-quoted, where the shell treats every byte as inert data.
	// Double quotes remain deliberately narrow because substitutions are active
	// there, and unmatched quotes fail closed.
	var quote byte
	for i := 0; i < len(command); i++ {
		c := command[i]
		if c < 0x20 || c == 0x7f {
			return false
		}
		switch quote {
		case '\'':
			if c == '\'' {
				quote = 0
			}
		case '"':
			switch {
			case c == '"':
				quote = 0
			case strings.ContainsRune("\\`$(){}[]", rune(c)):
				return false
			}
		default:
			switch {
			case c == '\'' || c == '"':
				quote = c
			case strings.ContainsRune("\\;&|`><$(){}[]*?!~#", rune(c)):
				return false
			}
		}
	}
	return quote == 0
}

func runReviewedWriteEnv(args []string) int {
	if !reviewedWriteEnvironmentEnabled() || len(args) < 4 || args[0] != "-u" || args[1] != "ATL_READ_ONLY" || args[2] != "atl" {
		// Provider runtimes may probe an env executable while initializing their
		// shell. That is not a model ATL interface attempt: model-originated Bash
		// commands are already audited by the outer hook. Keep the shim fail-closed
		// without polluting the ATL proxy counter; only the exact reviewed form
		// below enters policy, broker, and interface accounting.
		fmt.Fprintln(os.Stderr, "atl evaluation env shim rejected the invocation")
		return 2
	}
	return runATLProxyWithWriteIntent(args[3:], true)
}

func reviewedWriteEnvironmentEnabled() bool {
	return os.Getenv("ATL_EVAL_ALLOW_REVIEWED_WRITES") == "1" || os.Getenv("ATL_EVAL_ALLOW_SYNTHETIC_WRITES") == "1"
}

func runATLProxy(args []string) int {
	return runATLProxyWithWriteIntent(args, false)
}

func runATLProxyWithWriteIntent(args []string, reviewedWriteIntent bool) int {
	counterPath := os.Getenv("ATL_EVAL_COUNTER")
	brokerPath := os.Getenv("ATL_EVAL_COMMAND_BROKER_FILE")
	allowReviewedWrites := reviewedWriteEnvironmentEnabled()
	reviewedWriteAuthority := os.Getenv("ATL_EVAL_ALLOW_SYNTHETIC_WRITES") == "1" && syntheticBackendsAreLoopback()
	brokerAllowsReviewedWrites := false
	if allowReviewedWrites && brokerPath != "" {
		brokerAllowsWrites, err := agenteval.CommandBrokerAllowsReviewedWrites(brokerPath)
		brokerAllowsReviewedWrites = err == nil && brokerAllowsWrites
		reviewedWriteAuthority = brokerAllowsReviewedWrites
	}
	if reviewedWriteIntent && !brokerAllowsReviewedWrites {
		return rejectATLProxy(counterPath, "atl evaluation proxy rejected untrusted reviewed write intent")
	}
	if os.Getenv("ATL_READ_ONLY") != "1" && !reviewedWriteAuthority {
		return rejectATLProxy(counterPath, "atl evaluation proxy requires ATL_READ_ONLY=1")
	}
	realBinary := os.Getenv("ATL_EVAL_REAL_BINARY")
	if counterPath == "" || realBinary == "" && brokerPath == "" {
		return rejectATLProxy(counterPath, "atl evaluation proxy is not configured")
	}
	commandFamily, _ := agenteval.CapabilityFamilyForCLI(args)
	if policyPath := os.Getenv("ATL_EVAL_CLI_POLICY_FILE"); policyPath != "" {
		policy, err := agenteval.LoadCLICommandPolicy(policyPath)
		if err != nil {
			return rejectATLProxy(counterPath, "atl evaluation proxy rejected its command policy")
		}
		match, err := policy.Match(args)
		if err != nil {
			return rejectATLProxy(counterPath, "atl evaluation proxy rejected command arguments")
		}
		if match.Name == "atl_version" {
			// This reserved family is emitted only by the backend-free calibration
			// policy, binding its content-free receipt to that exact reviewed rule.
			commandFamily = match.Name
		}
		allowed, err := reserveCLIInvocation(counterPath, match.Name, match.MaxInvocations)
		if err != nil {
			return rejectATLProxy(counterPath, "atl evaluation proxy could not enforce its invocation budget")
		}
		if !allowed {
			return rejectATLProxy(counterPath, "atl evaluation proxy rejected an exhausted command budget")
		}
	} else if !allowedATLArgs(args, os.Getenv("ATL_EVAL_ALLOWED_COMMANDS")) {
		return rejectATLProxy(counterPath, "atl evaluation proxy rejected command arguments")
	}
	if brokerPath != "" {
		brokerArgs := args
		if brokerAllowsReviewedWrites && !reviewedWriteIntent && (len(args) == 0 || args[0] != "--read-only") {
			brokerArgs = append([]string{"--read-only"}, args...)
		}
		response, err := agenteval.CallCommandBroker(brokerPath, brokerArgs, false)
		if err != nil {
			return failATLProxy(counterPath, "atl evaluation proxy could not reach its confined command broker")
		}
		if response.Status == "rejected" {
			return rejectATLProxy(counterPath, "atl evaluation proxy command broker rejected the invocation")
		}
		if response.Status != "executed" {
			return failATLProxy(counterPath, "atl evaluation proxy command broker failed the invocation")
		}
		calibrationObservation, err := calibrationProxyObservation(commandFamily, os.Getenv("ATL_EVAL_GUARD_MODE"), response)
		if err != nil {
			return failATLProxy(counterPath, "atl evaluation proxy rejected invalid calibration output")
		}
		stdoutErr := emitBrokeredCLIStdout(response.Stdout, os.Stdout)
		stderrBytes, stderrErr := os.Stderr.Write(response.Stderr)
		if stdoutErr != nil || stderrErr != nil {
			return failATLProxy(counterPath, "atl evaluation proxy could not emit brokered output")
		}
		if err := appendProxyRecord(counterPath, proxyRecord{CommandFamily: commandFamily, CalibrationObservationSHA256: calibrationObservation, StdoutBytes: int64(len(response.Stdout)), StderrBytes: int64(stderrBytes), ExitCode: response.ExitCode}); err != nil {
			fmt.Fprintln(os.Stderr, "record atl evaluation metric:", err)
			return 1
		}
		return response.ExitCode
	}
	command := exec.Command(realBinary, args...)
	command.Stdin = os.Stdin
	stdout, err := command.StdoutPipe()
	if err != nil {
		return failATLProxy(counterPath, "atl evaluation proxy could not attach stdout")
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return failATLProxy(counterPath, "atl evaluation proxy could not attach stderr")
	}
	if err := command.Start(); err != nil {
		return failATLProxy(counterPath, "atl evaluation proxy could not start atl")
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
	if err := appendProxyRecord(counterPath, proxyRecord{CommandFamily: commandFamily, StdoutBytes: stdoutBytes, StderrBytes: stderrBytes, ExitCode: exitCode}); err != nil {
		fmt.Fprintln(os.Stderr, "record atl evaluation metric:", err)
		return 1
	}
	return exitCode
}

func emitBrokeredCLIStdout(data []byte, output io.Writer) error {
	root := os.Getenv("ATL_EVAL_CLI_RESULT_DIR")
	if len(data) <= privateCLIResultInlineBytes || root == "" || !utf8.Valid(data) {
		_, err := output.Write(data)
		return err
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fmt.Errorf("invalid result staging directory")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("invalid result staging directory")
	}
	digest := sha256.Sum256(data)
	path := filepath.Join(root, fmt.Sprintf("atl-result-%x.txt", digest))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if os.IsExist(err) {
		existing, readErr := os.ReadFile(path)
		info, statErr := os.Lstat(path)
		if readErr != nil || statErr != nil || !bytes.Equal(existing, data) || !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 {
			return fmt.Errorf("invalid existing staged result")
		}
		file = nil
		err = nil
	}
	if err != nil {
		return err
	}
	if file != nil {
		keep := false
		defer func() {
			if !keep {
				_ = os.Remove(path)
			}
		}()
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Chmod(0o400); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		keep = true
	}
	lines := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lines++
	}
	offsets := make([]string, 0, privateCLIResultReadLimit)
	for offset := 0; offset < lines && len(offsets) < privateCLIResultReadLimit; offset += privateCLIResultReadLines {
		offsets = append(offsets, strconv.Itoa(offset))
	}
	_, err = fmt.Fprintf(output,
		"The complete output of this reviewed atl invocation (%d bytes, %d lines) is staged at %s. Only the Read tool is authorized for this file: use limit=%d and these offsets in order: %s. Do not use Bash, shell assignments, grep, cat, sed, wc, or any other tool on the staged file.\n",
		len(data), lines, path, privateCLIResultReadLines, strings.Join(offsets, ","))
	return err
}

func calibrationProxyObservation(commandFamily, guardMode string, response agenteval.CommandBrokerResponse) (string, error) {
	if commandFamily != "atl_version" || guardMode != "provider-calibration" || response.ExitCode != 0 || len(response.Stderr) != 0 {
		return "", nil
	}
	return agenteval.CalibrationVersionObservationSHA256(response.Stdout)
}

func allowedATLArgs(args []string, rawAllowed string) bool {
	var prefixes []string
	if json.Unmarshal([]byte(rawAllowed), &prefixes) != nil || len(prefixes) == 0 {
		return false
	}
	command := "atl " + strings.Join(args, " ")
	for _, prefix := range prefixes {
		if command == prefix || strings.HasPrefix(command, prefix+" ") {
			return true
		}
	}
	return false
}

func syntheticBackendsAreLoopback() bool {
	for _, name := range []string{"ATL_JIRA_URL", "ATL_CONFLUENCE_URL"} {
		parsed, err := url.Parse(os.Getenv(name))
		if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return false
		}
		host := parsed.Hostname()
		if host != "localhost" {
			address := net.ParseIP(host)
			if address == nil || !address.IsLoopback() {
				return false
			}
		}
	}
	return true
}

func rejectATLProxy(counterPath, message string) int {
	fmt.Fprintln(os.Stderr, message)
	if counterPath != "" {
		_ = appendProxyRecord(counterPath, proxyRecord{Denied: true, ExitCode: 2})
	}
	return 2
}

func failATLProxy(counterPath, message string) int {
	fmt.Fprintln(os.Stderr, message)
	if counterPath != "" {
		_ = appendProxyRecord(counterPath, proxyRecord{Denied: true, ExitCode: 1})
	}
	return 1
}

func reserveCLIInvocation(counterPath, ruleName string, limit int) (bool, error) {
	if counterPath == "" || !cliRuleNameRE.MatchString(ruleName) || limit < 1 || limit > 100 {
		return false, fmt.Errorf("invalid cli invocation policy")
	}
	for slot := 1; slot <= limit; slot++ {
		path := filepath.Join(filepath.Dir(counterPath), fmt.Sprintf("cli-slot-%s-%d", ruleName, slot))
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
