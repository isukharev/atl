package agenteval

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCommandBrokerExecutesOnlyReviewedArgumentsWithinIndependentBudget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable scripts are Unix-only")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	requests := filepath.Join(root, "requests")
	responses := filepath.Join(root, "responses")
	for _, directory := range []string{requests, responses} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	executions := filepath.Join(root, "executions")
	binary := filepath.Join(root, "atl")
	script := "#!/bin/sh\n" +
		"if find \"$TEST_REQUEST_DIR\" -name 'processing-*' -o -name 'request-*' | grep -q .; then exit 91; fi\n" +
		"printf '%s\\n' \"$*\" >>\"$TEST_EXECUTIONS\"\n" +
		"printf 'stdout:%s:%s\\n' \"$1\" \"$TEST_CHILD_CONFIG\"\n" +
		"printf 'stderr:%s\\n' \"$2\" >&2\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "broker.json")
	policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{{
		Name: "fields", Command: []string{"jira", "fields"}, MaxInvocations: 1,
	}}}
	broker, err := StartCommandBroker(CommandBrokerConfig{
		RequestDirectory: requests, ResponseDirectory: responses, ManifestPath: manifest,
		RealBinary: binary, Policy: policy,
		Environment:    []string{"TEST_REQUEST_DIR=" + requests, "TEST_EXECUTIONS=" + executions, "TEST_CHILD_CONFIG=disposable"},
		MaxStdoutBytes: 4096, MaxStderrBytes: 4096, CommandTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	probe, err := CallCommandBroker(manifest, nil, true)
	if err != nil || probe.Status != "ready" {
		t.Fatalf("probe=%+v err=%v", probe, err)
	}
	response, err := CallCommandBroker(manifest, []string{"jira", "fields"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "executed" || response.ExitCode != 0 || string(response.Stdout) != "stdout:jira:disposable\n" || string(response.Stderr) != "stderr:fields\n" {
		t.Fatalf("response=%+v", response)
	}
	for _, args := range [][]string{{"jira", "fields"}, {"jira", "issues"}} {
		response, err = CallCommandBroker(manifest, args, false)
		if err != nil || response.Status != "rejected" {
			t.Fatalf("args=%q response=%+v err=%v", args, response, err)
		}
	}
	data, err := os.ReadFile(executions)
	if err != nil || string(data) != "jira fields\n" {
		t.Fatalf("executions=%q err=%v", data, err)
	}
	assertNoCommandBrokerPayloads(t, requests, responses)
}

func TestCommandBrokerRejectsForgedCapabilityAndOversizedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable scripts are Unix-only")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	requests := filepath.Join(root, "requests")
	responses := filepath.Join(root, "responses")
	for _, directory := range []string{requests, responses} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(root, "atl")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nhead -c 64 /dev/zero\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "broker.json")
	broker, err := StartCommandBroker(CommandBrokerConfig{
		RequestDirectory: requests, ResponseDirectory: responses, ManifestPath: manifest,
		RealBinary:  binary,
		Policy:      CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{{Name: "fields", Command: []string{"jira", "fields"}, MaxInvocations: 2}}},
		Environment: []string{"PATH=/usr/bin:/bin"}, MaxStdoutBytes: 8, MaxStderrBytes: 8, CommandTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var forged CommandBrokerManifest
	if err := json.Unmarshal(data, &forged); err != nil {
		t.Fatal(err)
	}
	forged.Capability = strings.Repeat("A", 43)
	forgedPath := filepath.Join(root, "forged.json")
	forgedData, _ := json.Marshal(forged)
	if err := os.WriteFile(forgedPath, forgedData, 0o600); err != nil {
		t.Fatal(err)
	}
	response, err := CallCommandBroker(forgedPath, []string{"jira", "fields"}, false)
	if err != nil || response.Status != "rejected" {
		t.Fatalf("forged response=%+v err=%v", response, err)
	}
	response, err = CallCommandBroker(manifest, []string{"jira", "fields"}, false)
	if err != nil || response.Status != "failed" || len(response.Stdout) != 0 {
		t.Fatalf("oversized response=%+v err=%v", response, err)
	}
}

func TestCommandBrokerManifestAndArtifactsArePrivateAndCleaned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission assertions are not applicable")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	requests := filepath.Join(root, "requests")
	responses := filepath.Join(root, "responses")
	for _, directory := range []string{requests, responses} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(root, "atl")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "broker.json")
	broker, err := StartCommandBroker(CommandBrokerConfig{
		RequestDirectory: requests, ResponseDirectory: responses, ManifestPath: manifest, RealBinary: binary,
		Policy:         CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{{Name: "fields", Command: []string{"jira", "fields"}, MaxInvocations: 1}}},
		MaxStdoutBytes: 8, MaxStderrBytes: 8, CommandTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("manifest mode=%o", mode)
	}
	if err := os.Chmod(manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CallCommandBroker(manifest, nil, true); err == nil {
		t.Fatal("group-readable manifest passed")
	}
	if err := os.Chmod(manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if response, err := CallCommandBroker(manifest, nil, true); err != nil || response.Status != "ready" {
		t.Fatalf("probe=%+v err=%v", response, err)
	}
	if err := broker.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(manifest); !os.IsNotExist(err) {
		t.Fatalf("manifest survived close: %v", err)
	}
	assertNoCommandBrokerPayloads(t, requests, responses)
}

func assertNoCommandBrokerPayloads(t *testing.T, directories ...string) {
	t.Helper()
	for _, directory := range directories {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if bytes.HasPrefix([]byte(entry.Name()), []byte("request-")) || bytes.HasPrefix([]byte(entry.Name()), []byte("processing-")) || bytes.HasPrefix([]byte(entry.Name()), []byte("response-")) {
				t.Fatalf("broker payload survived in %s: %s", directory, entry.Name())
			}
		}
	}
}
