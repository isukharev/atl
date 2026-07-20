package agenteval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestClassifyCodexToolInventory(t *testing.T) {
	tests := []struct {
		name      string
		tools     string
		want      CodexCLIToolAvailabilityStatus
		wantShell string
	}{
		{name: "missing member", tools: "", want: CodexCLIToolAvailabilityMissing},
		{name: "null", tools: "null", want: CodexCLIToolAvailabilityMissing},
		{name: "empty", tools: "[]", want: CodexCLIToolAvailabilityMissing},
		{name: "unrelated", tools: `[{"type":"function","name":"update_plan","parameters":{}}]`, want: CodexCLIToolAvailabilityMissing},
		{name: "unified exec", tools: `[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}},{"type":"function","name":"write_stdin","parameters":{}}]`, want: CodexCLIToolAvailabilitySupported, wantShell: "exec_command"},
		{name: "shell command", tools: `[{"type":"function","name":"shell_command","parameters":{"type":"object","properties":{"command":{"type":"array"}},"required":["command"]}}]`, want: CodexCLIToolAvailabilitySupported, wantShell: "shell_command"},
		{name: "duplicate", tools: `[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{}},"required":["cmd"]}},{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{}},"required":["cmd"]}}]`, want: CodexCLIToolAvailabilityAmbiguous},
		{name: "two shell routes", tools: `[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{}},"required":["cmd"]}},{"type":"function","name":"shell_command","parameters":{"type":"object","properties":{"command":{}},"required":["command"]}}]`, want: CodexCLIToolAvailabilityAmbiguous},
		{name: "wrong shell type", tools: `[{"type":"custom","name":"exec_command","parameters":{}}]`, want: CodexCLIToolAvailabilityAmbiguous},
		{name: "missing shell schema", tools: `[{"type":"function","name":"exec_command"}]`, want: CodexCLIToolAvailabilityAmbiguous},
		{name: "invalid tools", tools: `{}`, want: CodexCLIToolAvailabilitySchemaFailed},
		{name: "invalid entry", tools: `[null]`, want: CodexCLIToolAvailabilitySchemaFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyCodexToolInventory(json.RawMessage(test.tools), false)
			if got.status != test.want || got.shellTool != test.wantShell {
				t.Fatalf("got=%+v want_status=%s want_shell=%q", got, test.want, test.wantShell)
			}
		})
	}
}

func TestClassifyCodexToolProbeInventoriesSupportsResponsesLiteAndRejectsConflicts(t *testing.T) {
	tools := json.RawMessage(`[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]`)
	input := json.RawMessage(`[{"type":"additional_tools","role":"developer","tools":` + string(tools) + `}]`)
	got := classifyCodexToolProbeInventories(nil, input)
	if got.status != CodexCLIToolAvailabilitySupported || got.shellTool != "exec_command" {
		t.Fatalf("responses lite inventory=%+v", got)
	}
	for name, top := range map[string]json.RawMessage{
		"conflicting top-level": tools,
		"late additional tools": nil,
	} {
		t.Run(name, func(t *testing.T) {
			candidateInput := input
			if name == "late additional tools" {
				candidateInput = json.RawMessage(`[{"type":"message"},{"type":"additional_tools","tools":` + string(tools) + `}]`)
			}
			candidate := classifyCodexToolProbeInventories(top, candidateInput)
			if candidate.status != CodexCLIToolAvailabilityAmbiguous && candidate.status != CodexCLIToolAvailabilitySchemaFailed {
				t.Fatalf("candidate=%+v", candidate)
			}
		})
	}
}

func TestClassifyCodexToolProbeInventoriesSupportsClosedCodeMode(t *testing.T) {
	description := "All nested tools are available on the global `tools` object.\n" +
		"### `exec_command`\ndeclare const tools: { exec_command(args: {\n" +
		"### `write_stdin`\ndeclare const tools: { write_stdin(args: {"
	exec := fmt.Sprintf(`{"type":"custom","name":"exec","description":%q,"format":{"type":"grammar","syntax":"lark","definition":%q}}`, description, codexCodeModeExecGrammar)
	wait := `{"type":"function","name":"wait","parameters":{"type":"object","properties":{"cell_id":{"type":"string"},"max_tokens":{"type":"number"},"terminate":{"type":"boolean"},"yield_time_ms":{"type":"number"}},"required":["cell_id"],"additionalProperties":false}}`
	input := json.RawMessage(`[{"type":"additional_tools","role":"developer","tools":[` + exec + `,` + wait + `]}]`)
	got := classifyCodexToolProbeInventories(nil, input)
	if got.status != CodexCLIToolAvailabilitySupported || got.shellTool != "exec" {
		t.Fatalf("code-mode inventory=%+v", got)
	}
	for name, tools := range map[string]string{
		"direct custom exec":          `[` + exec + `,` + wait + `]`,
		"missing wait":                `[` + exec + `]`,
		"negated nested shell":        fmt.Sprintf(`[{"type":"custom","name":"exec","description":%q,"format":{"type":"grammar","syntax":"lark","definition":%q}},%s]`, "All nested tools are available on the global `tools` object. Does not expose exec_command or write_stdin.", codexCodeModeExecGrammar, wait),
		"wrong grammar syntax":        fmt.Sprintf(`[{"type":"custom","name":"exec","description":%q,"format":{"type":"grammar","syntax":"regex","definition":%q}},%s]`, description, codexCodeModeExecGrammar, wait),
		"extra grammar field":         fmt.Sprintf(`[{"type":"custom","name":"exec","description":%q,"format":{"type":"grammar","syntax":"lark","definition":%q,"extra":true}},%s]`, description, codexCodeModeExecGrammar, wait),
		"broadened wait schema":       `[` + exec + `,{"type":"function","name":"wait","parameters":{"type":"object","properties":{"cell_id":{"type":"string"},"max_tokens":{"type":"number"},"terminate":{"type":"boolean"},"yield_time_ms":{"type":"number"},"command":{"type":"string"}},"required":["cell_id"],"additionalProperties":false}}]`,
		"open wait schema":            `[` + exec + `,{"type":"function","name":"wait","parameters":{"type":"object","properties":{"cell_id":{"type":"string"},"max_tokens":{"type":"number"},"terminate":{"type":"boolean"},"yield_time_ms":{"type":"number"}},"required":["cell_id"],"additionalProperties":true}}]`,
		"wrong wait property types":   `[` + exec + `,{"type":"function","name":"wait","parameters":{"type":"object","properties":{"cell_id":{"type":"number"},"max_tokens":{"type":"string"},"terminate":{"type":"integer"},"yield_time_ms":{"type":"boolean"}},"required":["cell_id"],"additionalProperties":false}}]`,
		"mixed direct shell and code": `[` + exec + `,` + wait + `,{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{}},"required":["cmd"]}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			var candidate codexToolProbeObservation
			if name == "direct custom exec" {
				candidate = classifyCodexToolProbeInventories(json.RawMessage(tools), nil)
			} else {
				candidate = classifyCodexToolProbeInventories(nil, json.RawMessage(`[{"type":"additional_tools","tools":`+tools+`}]`))
			}
			if candidate.status == CodexCLIToolAvailabilitySupported {
				t.Fatalf("unsafe code-mode inventory passed: %+v", candidate)
			}
		})
	}
}

func TestCodexCLIToolAvailabilityReportValidation(t *testing.T) {
	identity := "binary-sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	report := CodexCLIToolAvailabilityReport{
		SchemaVersion:     CodexCLIToolAvailabilitySchemaVersion,
		Provider:          "codex",
		AgentIdentity:     identity,
		ContractSHA256:    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Status:            CodexCLIToolAvailabilitySupported,
		ShellTool:         "exec_command",
		RequestObserved:   true,
		SyntheticRequests: 1,
	}
	if err := report.Validate(); err != nil || !report.Supported() {
		t.Fatalf("valid report rejected: %+v err=%v", report, err)
	}
	codeMode := report
	codeMode.ShellTool = "exec"
	if err := codeMode.Validate(); err != nil || !codeMode.Supported() {
		t.Fatalf("valid code-mode report rejected: %+v err=%v", codeMode, err)
	}
	legacy := report
	legacy.SchemaVersion = 1
	if err := legacy.Validate(); err != nil || !legacy.Supported() {
		t.Fatalf("valid legacy direct-shell report rejected: %+v err=%v", legacy, err)
	}
	legacy.ShellTool = "exec"
	if legacy.Validate() == nil {
		t.Fatalf("legacy schema accepted code mode: %+v", legacy)
	}
	for name, mutate := range map[string]func(*CodexCLIToolAvailabilityReport){
		"raw shell":        func(value *CodexCLIToolAvailabilityReport) { value.ShellTool = "arbitrary" },
		"provider request": func(value *CodexCLIToolAvailabilityReport) { value.ProviderRequests = 1 },
		"missing request":  func(value *CodexCLIToolAvailabilityReport) { value.RequestObserved = false },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := report
			mutate(&candidate)
			if candidate.Validate() == nil {
				t.Fatalf("invalid report passed: %+v", candidate)
			}
		})
	}
}

func TestQualifyCodexCLIToolAvailabilityUsesOneCredentialFreeSyntheticRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native private agent qualification requires POSIX owner-only runtime")
	}
	description := "All nested tools are available on the global `tools` object.\n" +
		"### `exec_command`\ndeclare const tools: { exec_command(args: {\n" +
		"### `write_stdin`\ndeclare const tools: { write_stdin(args: {"
	codeModeBody := fmt.Sprintf(`{"model":"synthetic-model","stream":true,"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"exec","description":%q,"format":{"type":"grammar","syntax":"lark","definition":%q}},{"type":"function","name":"wait","parameters":{"type":"object","properties":{"cell_id":{"type":"string"},"max_tokens":{"type":"number"},"terminate":{"type":"boolean"},"yield_time_ms":{"type":"number"}},"required":["cell_id"],"additionalProperties":false}}]}]}`, description, codexCodeModeExecGrammar)
	tests := []struct {
		name          string
		body          string
		requestCount  int
		authorization bool
		want          CodexCLIToolAvailabilityStatus
		wantShell     string
	}{
		{name: "supported", body: `{"model":"synthetic-model","stream":true,"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}},{"type":"function","name":"write_stdin","parameters":{}}]}`, requestCount: 1, want: CodexCLIToolAvailabilitySupported, wantShell: "exec_command"},
		{name: "code mode", body: codeModeBody, requestCount: 1, want: CodexCLIToolAvailabilitySupported, wantShell: "exec"},
		{name: "missing", body: `{"model":"synthetic-model","stream":true,"tools":[]}`, requestCount: 1, want: CodexCLIToolAvailabilityMissing},
		{name: "schema", body: `{"model":"wrong-model","stream":true,"tools":[]}`, requestCount: 1, want: CodexCLIToolAvailabilitySchemaFailed},
		{name: "authorization rejected", body: `{"model":"synthetic-model","stream":true,"tools":[]}`, requestCount: 1, authorization: true, want: CodexCLIToolAvailabilitySchemaFailed},
		{name: "process", want: CodexCLIToolAvailabilityProcessFailed},
		{name: "repeated request", body: `{"model":"synthetic-model","stream":true,"tools":[]}`, requestCount: 2, want: CodexCLIToolAvailabilityProcessFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := buildCodexToolProbeTestAgent(t, test.body, test.requestCount, test.authorization)
			scratch := t.TempDir()
			if err := os.Chmod(scratch, 0o700); err != nil {
				t.Fatal(err)
			}
			report, err := QualifyCodexCLIToolAvailability(context.Background(), CodexCLIToolAvailabilityOptions{
				AgentBinary: agent, ScratchRoot: scratch, Model: "synthetic-model", TimeoutSeconds: 10,
			})
			if err != nil {
				t.Fatal(err)
			}
			if report.Validate() != nil || report.Status != test.want || report.ShellTool != test.wantShell {
				t.Fatalf("report=%+v want_status=%s want_shell=%q", report, test.want, test.wantShell)
			}
			entries, readErr := os.ReadDir(scratch)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("probe runtime was retained: entries=%d err=%v", len(entries), readErr)
			}
		})
	}
}

func TestCodexToolAvailabilityContractBindsRuntimeInputs(t *testing.T) {
	base := CodexCLIToolAvailabilityOptions{Model: "synthetic-model", Reasoning: "high", TimeoutSeconds: 30}
	identity := "binary-sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	first := codexToolAvailabilityContractSHA256(identity, base)
	changed := base
	changed.Reasoning = "medium"
	if second := codexToolAvailabilityContractSHA256(identity, changed); first == second {
		t.Fatal("reasoning did not change the tool-availability contract")
	}
	changed = base
	changed.TimeoutSeconds++
	if second := codexToolAvailabilityContractSHA256(identity, changed); first == second {
		t.Fatal("timeout did not change the tool-availability contract")
	}
}

func buildCodexToolProbeTestAgent(t *testing.T, body string, requestCount int, authorization bool) string {
	t.Helper()
	directory := t.TempDir()
	source := filepath.Join(directory, "main.go")
	binary := filepath.Join(directory, "agent")
	program := fmt.Sprintf(`package main
import ("bytes"; "io"; "net/http"; "os"; "path/filepath"; "strconv"; "strings")
const body = %q
const requestCount = %d
const authorization = %t
func main() {
	executable, _ := os.Executable()
	if !strings.HasPrefix(filepath.Base(filepath.Dir(executable)), "codex-tool-availability-") { os.Exit(6) }
	if requestCount == 0 { os.Exit(0) }
  base := ""
  for index := 1; index+1 < len(os.Args); index++ {
    if os.Args[index] == "-c" && strings.HasPrefix(os.Args[index+1], "model_providers.atl_tool_probe.base_url=") {
      raw := strings.TrimPrefix(os.Args[index+1], "model_providers.atl_tool_probe.base_url=")
      base, _ = strconv.Unquote(raw)
    }
  }
  if base == "" { os.Exit(2) }
	for index := 0; index < requestCount; index++ {
	  request, err := http.NewRequest(http.MethodPost, base+"/responses", bytes.NewBufferString(body))
	  if err != nil { os.Exit(3) }
	  request.Header.Set("Content-Type", "application/json")
	  if authorization { request.Header.Set("Authorization", "Bearer forbidden") }
	  response, err := http.DefaultClient.Do(request)
	  if err != nil { os.Exit(4) }
	  _, _ = io.Copy(io.Discard, response.Body)
	  _ = response.Body.Close()
	  if response.StatusCode != http.StatusOK { os.Exit(5) }
	}
}
`, body, requestCount, authorization)
	writeTestFile(t, source, program, 0o600)
	command := exec.Command("go", "build", "-buildvcs=false", "-o", binary, source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build native tool probe fixture: %v: %s", err, output)
	}
	return binary
}
