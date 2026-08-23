package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepositoryJiraGuardedFieldPreparedPUTOracleIsByteExact(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval", "jira-field-mutation")
	want := []byte(`{"fields":{"customfield_12000":"Synthetic approved narrative.\n"}}`)
	for _, variant := range []string{"apply", "unknown"} {
		fixture := loadGuardedFieldFixture(t, filepath.Join(root, "fixture."+variant+".json"))
		found := false
		for _, route := range fixture.Routes {
			if route.Name != "write_id" {
				continue
			}
			found = true
			if route.RequestBodyMatch != "exact" || !bytes.Equal(route.RequestBody, want) {
				t.Fatalf("variant=%s matcher=%q body=%s", variant, route.RequestBodyMatch, route.RequestBody)
			}
		}
		if !found {
			t.Fatalf("variant=%s has no prepared PUT oracle", variant)
		}
	}
}

func TestJiraGuardedFieldDynamicBindingDiffersBySelectedBackendAndGrades(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval", "jira-field-mutation")
	fixture := loadGuardedFieldFixture(t, filepath.Join(root, "fixture.apply.json"))
	hashes := make([]string, 2)
	for index := range hashes {
		broker, backend, manifest := startGuardedFieldBroker(t, fixture)
		hashes[index] = runGuardedFieldBrokerPreview(t, manifest)
		runGuardedFieldBrokerApply(t, manifest, hashes[index], "executed")
		if err := broker.Close(); err != nil {
			t.Fatal(err)
		}
		methods, unexpected, duplicates := backend.Summary()
		backend.Close()
		if methods["GET"] != 7 || methods["PUT"] != 1 || unexpected != 0 || duplicates != 4 {
			t.Fatalf("geometry=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
		}
		check := RunCheck{Name: "proposal", Kind: "json_equals_proposal_hash_binding", Pointer: "/proposal_hash", Expected: json.RawMessage(`{"binding":"jira_field"}`)}
		plan, err := newATLGradingPlan([]RunCheck{check}, "", strings.Repeat("d", 64))
		if err != nil {
			t.Fatal(err)
		}
		evaluation, err := evaluateATLChecksWithPlan(context.Background(), plan, []RunCheck{check}, atlGradingObservation{
			final: []byte(`{"proposal_hash":"` + hashes[index] + `"}`), producedProposalHashes: broker.producedProposalHashSnapshot()})
		if err != nil || !evaluation.checks[check.Name] {
			t.Fatalf("grading=%+v err=%v", evaluation, err)
		}
	}
	if hashes[0] == hashes[1] {
		t.Fatal("independent backend origins produced the same schema-v3 proposal hash")
	}
}

func TestJiraGuardedFieldBrokerRejectsCrossBackendHashBeforeSelectedBinary(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval", "jira-field-mutation")
	fixture := loadGuardedFieldFixture(t, filepath.Join(root, "fixture.apply.json"))
	brokerA, backendA, manifestA := startGuardedFieldBroker(t, fixture)
	hashA := runGuardedFieldBrokerPreview(t, manifestA)
	if err := brokerA.Close(); err != nil {
		t.Fatal(err)
	}
	backendA.Close()

	brokerB, backendB, manifestB := startGuardedFieldBroker(t, fixture)
	hashB := runGuardedFieldBrokerPreview(t, manifestB)
	if hashA == hashB {
		t.Fatal("backend-bound hashes unexpectedly match")
	}
	runGuardedFieldBrokerApply(t, manifestB, hashA, "rejected")
	methods, unexpected, _ := backendB.Summary()
	if methods["GET"] != 2 || methods["PUT"] != 0 || unexpected != 0 {
		t.Fatalf("cross-backend rejection reached selected binary/backend: %v unexpected=%d", methods, unexpected)
	}
	runGuardedFieldBrokerApply(t, manifestB, hashB, "executed")
	runGuardedFieldBrokerApply(t, manifestB, hashB, "rejected")
	if err := brokerB.Close(); err != nil {
		t.Fatal(err)
	}
	methods, unexpected, duplicates := backendB.Summary()
	backendB.Close()
	if methods["GET"] != 7 || methods["PUT"] != 1 || unexpected != 0 || duplicates != 4 {
		t.Fatalf("one-use geometry=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
	}
}

func loadGuardedFieldFixture(t *testing.T, path string) MockFixture {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fixture, decodeErr := DecodeMockFixture(file)
	closeErr := file.Close()
	if decodeErr != nil || closeErr != nil {
		t.Fatalf("fixture decode=%v close=%v", decodeErr, closeErr)
	}
	for index := range fixture.Routes {
		fixture.Routes[index].closedQuery = true
	}
	return fixture
}

func startGuardedFieldBroker(t *testing.T, fixture MockFixture) (*CommandBroker, *MockBackend, string) {
	t.Helper()
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "field-value.txt"), []byte("Synthetic approved narrative.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	requests, responses := filepath.Join(root, "requests"), filepath.Join(root, "responses")
	for _, dir := range []string{requests, responses} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	environment := backend.Environment()
	environment["ATL_NO_UPDATE"] = "1"
	environment["ATL_CONFIG_DIR"] = filepath.Join(root, "config")
	environment["ATL_MIRROR_ROOT"] = filepath.Join(root, "mirror")
	policy := guardedFieldBrokerPolicy()
	manifest := filepath.Join(root, "broker.json")
	broker, err := StartCommandBroker(CommandBrokerConfig{RequestDirectory: requests, ResponseDirectory: responses, ManifestPath: manifest,
		RealBinary: repositorySyntheticATLBinary(t), WorkingDirectory: root, Policy: policy, Environment: flattenEnvironment(environment),
		MaxStdoutBytes: jiraGuardedFieldWireMaxBytes, MaxStderrBytes: 32 << 10, CommandTimeout: 30 * time.Second, AllowSyntheticWrites: true})
	if err != nil {
		backend.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	return broker, backend, manifest
}

func guardedFieldBrokerPolicy() CLICommandPolicy {
	common := []CLIFlagRule{{Name: "--from-file", Values: []string{"customfield_12000=field-value.txt"}, Required: true}, {Name: "--allow-fields", Values: []string{"customfield_12000"}, Required: true}}
	apply := append(append([]CLIFlagRule(nil), common...), CLIFlagRule{Name: "--expected-updated", Values: []string{"2026-07-15T09:30:00.000+0000"}, Required: true}, CLIFlagRule{Name: "--expected-proposal-hash", ValueFormat: "sha256", Required: true}, CLIFlagRule{Name: "--apply", Required: true})
	return CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{
		{Name: "preview", Command: []string{"jira", "issue", "field", "preview"}, Positionals: []CLIArgumentRule{{Values: []string{"PROJ-1"}}}, Flags: common, MaxInvocations: 1, BindsProposalHash: "jira_field"},
		{Name: "apply", Command: []string{"jira", "issue", "field", "set"}, Positionals: []CLIArgumentRule{{Values: []string{"PROJ-1"}}}, Flags: apply, MaxInvocations: 1, RequiresProposalHash: "jira_field"},
	}}
}

func guardedFieldPreviewArgs() []string {
	return []string{"jira", "issue", "field", "preview", "PROJ-1", "--from-file", "customfield_12000=field-value.txt", "--allow-fields", "customfield_12000"}
}
func guardedFieldApplyArgs(hash string) []string {
	return []string{"jira", "issue", "field", "set", "PROJ-1", "--from-file", "customfield_12000=field-value.txt", "--allow-fields", "customfield_12000", "--expected-updated", "2026-07-15T09:30:00.000+0000", "--expected-proposal-hash", hash, "--apply"}
}

func runGuardedFieldBrokerPreview(t *testing.T, manifest string) string {
	t.Helper()
	response, err := CallCommandBrokerReadOnly(manifest, guardedFieldPreviewArgs())
	if err != nil || response.Status != "executed" || response.ExitCode != 0 {
		t.Fatalf("preview response=%+v err=%v", response, err)
	}
	result, err := DecodeJiraGuardedFieldResult(strings.NewReader(string(response.Stdout)))
	if err != nil {
		t.Fatal(err)
	}
	return result.ProposalHash
}

func runGuardedFieldBrokerApply(t *testing.T, manifest, hash, status string) {
	t.Helper()
	response, err := CallCommandBroker(manifest, guardedFieldApplyArgs(hash), false)
	if err != nil || response.Status != status {
		t.Fatalf("apply response=%+v err=%v want=%s", response, err, status)
	}
	if status == "executed" && response.ExitCode != 0 {
		t.Fatalf("apply exit=%d stderr=%s", response.ExitCode, response.Stderr)
	}
}
