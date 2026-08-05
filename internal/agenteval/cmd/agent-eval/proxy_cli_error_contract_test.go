package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/agenteval"
)

const cliErrorContractFakeATL = `#!/bin/sh
printf 'warning: unrelated diagnostic for private fixture\n' >&2
case "$4" in
typed)
  printf '{"error":"page not found: private fixture","code":4,"kind":"not_found","remediation":"verify_identifier_or_access"}\n' >&2
  exit 4
  ;;
text)
  printf 'error: page not found: private fixture\n' >&2
  exit 4
  ;;
mismatch)
  printf '{"error":"x","code":9,"kind":"not_found","remediation":"verify_identifier_or_access"}\n' >&2
  exit 4
  ;;
success)
  printf '{"error":"x","code":4,"kind":"not_found","remediation":"verify_identifier_or_access"}\n' >&2
  printf '{"ok":true}\n'
  exit 0
  ;;
esac
exit 1
`

func cliErrorContractPolicy() agenteval.CLICommandPolicy {
	return agenteval.CLICommandPolicy{
		SchemaVersion: agenteval.CLICommandPolicySchemaVersion,
		Rules: []agenteval.CLICommandRule{{
			Name: "jira_issue_get", Command: []string{"jira", "issue", "get"},
			Positionals:    []agenteval.CLIArgumentRule{{Values: []string{"typed", "text", "mismatch", "success"}}},
			MaxInvocations: 4,
		}},
	}
}

func captureProxyStderr(t *testing.T, run func()) string {
	t.Helper()
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = writeEnd
	run()
	os.Stderr = previous
	_ = writeEnd.Close()
	var captured bytes.Buffer
	_, _ = io.Copy(&captured, readEnd)
	_ = readEnd.Close()
	return captured.String()
}

func readProxyRecordMembers(t *testing.T, path string) []map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("record %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

// The brokered path is the only one that holds a reviewed invocation's exact
// stderr, so it is the only one that may classify a failure — and it may keep
// nothing but the closed kind/remediation pair.
func TestATLProxyRecordsOnlyTheClosedCLIErrorContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable scripts are Unix-only")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	requests := filepath.Join(directory, "requests")
	responses := filepath.Join(directory, "responses")
	for _, path := range []string{requests, responses} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	realBinary := filepath.Join(directory, "real-atl")
	if err := os.WriteFile(realBinary, []byte(cliErrorContractFakeATL), 0o700); err != nil {
		t.Fatal(err)
	}
	policy := cliErrorContractPolicy()
	policyData, err := agenteval.EncodeCLICommandPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(directory, "policy.json")
	if err := os.WriteFile(policyPath, policyData, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(directory, "broker.json")
	broker, err := agenteval.StartCommandBroker(agenteval.CommandBrokerConfig{
		RequestDirectory: requests, ResponseDirectory: responses, ManifestPath: manifest,
		RealBinary: realBinary, WorkingDirectory: directory, Policy: policy,
		MaxStdoutBytes: 4096, MaxStderrBytes: 4096, CommandTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	counter := filepath.Join(directory, "counter.jsonl")
	t.Setenv("ATL_READ_ONLY", "1")
	t.Setenv("ATL_EVAL_REAL_BINARY", "")
	t.Setenv("ATL_EVAL_COUNTER", counter)
	t.Setenv("ATL_EVAL_CLI_POLICY_FILE", policyPath)
	t.Setenv("ATL_EVAL_COMMAND_BROKER_FILE", manifest)

	stderr := captureProxyStderr(t, func() {
		for _, mode := range []string{"typed", "text", "mismatch", "success"} {
			want := 4
			if mode == "success" {
				want = 0
			}
			if code := runATLProxy([]string{"jira", "issue", "get", mode}); code != want {
				t.Errorf("mode %s code=%d, want %d", mode, code, want)
			}
		}
	})
	if !strings.Contains(stderr, "page not found: private fixture") {
		t.Fatalf("brokered stderr passthrough changed: %q", stderr)
	}
	records := readProxyRecordMembers(t, counter)
	if len(records) != 4 {
		t.Fatalf("records=%d", len(records))
	}
	classified := records[0]
	if string(classified["error_kind"]) != `"not_found"` ||
		string(classified["error_remediation"]) != `"verify_identifier_or_access"` ||
		string(classified["exit_code"]) != "4" {
		t.Fatalf("typed failure record=%v", classified)
	}
	for member := range classified {
		switch member {
		case "command_family", "error_kind", "error_remediation", "stdout_bytes", "stderr_bytes", "exit_code":
		default:
			t.Fatalf("typed failure record carries %q", member)
		}
	}
	for index, record := range records[1:] {
		if _, ok := record["error_kind"]; ok {
			t.Fatalf("unclassified record %d holds a contract: %v", index+1, record)
		}
		if _, ok := record["error_remediation"]; ok {
			t.Fatalf("unclassified record %d holds a remediation: %v", index+1, record)
		}
	}
	raw, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"private fixture", "page not found", "unrelated diagnostic", "typed", "mismatch"} {
		if bytes.Contains(raw, []byte(marker)) {
			t.Fatalf("audit %s retained %q", raw, marker)
		}
	}
}

// A direct proxy invocation streams the child's stderr without ever holding it,
// so its failures stay unclassified. This is the documented fail-closed
// boundary: reviewed private-live cells always run behind the broker.
func TestATLProxyDirectPathLeavesCLIFailuresUnclassified(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable scripts are Unix-only")
	}
	directory := t.TempDir()
	realBinary := filepath.Join(directory, "real-atl")
	if err := os.WriteFile(realBinary, []byte("#!/bin/sh\nexit 4\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	policyData, err := agenteval.EncodeCLICommandPolicy(cliErrorContractPolicy())
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(directory, "policy.json")
	if err := os.WriteFile(policyPath, policyData, 0o600); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(directory, "counter.jsonl")
	t.Setenv("ATL_READ_ONLY", "1")
	t.Setenv("ATL_EVAL_REAL_BINARY", realBinary)
	t.Setenv("ATL_EVAL_COUNTER", counter)
	t.Setenv("ATL_EVAL_CLI_POLICY_FILE", policyPath)
	t.Setenv("ATL_EVAL_COMMAND_BROKER_FILE", "")

	if code := runATLProxy([]string{"jira", "issue", "get", "typed"}); code != 4 {
		t.Errorf("direct typed failure code=%d", code)
	}
	records := readProxyRecordMembers(t, counter)
	if len(records) != 1 || string(records[0]["exit_code"]) != "4" {
		t.Fatalf("records=%v", records)
	}
	if _, ok := records[0]["error_kind"]; ok {
		t.Fatalf("direct path classified a failure: %v", records[0])
	}
	if _, ok := records[0]["error_remediation"]; ok {
		t.Fatalf("direct path classified a remediation: %v", records[0])
	}
}
