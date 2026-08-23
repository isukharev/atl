package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBoundedCommandBufferReportsRetainedBytesAndStabilizesAfterOverflow(t *testing.T) {
	exact := &boundedCommandBuffer{maximum: 4}
	if n, err := exact.Write([]byte("abcd")); n != 4 || err != nil || exact.exceeded || string(exact.Bytes()) != "abcd" {
		t.Fatalf("exact n=%d err=%v exceeded=%v bytes=%q", n, err, exact.exceeded, exact.Bytes())
	}

	partial := &boundedCommandBuffer{maximum: 4}
	if n, err := partial.Write([]byte("abcdef")); n != 4 || !errors.Is(err, io.ErrShortWrite) || !partial.exceeded || string(partial.Bytes()) != "abcd" {
		t.Fatalf("partial n=%d err=%v exceeded=%v bytes=%q", n, err, partial.exceeded, partial.Bytes())
	}
	if n, err := partial.Write([]byte("z")); n != 0 || !errors.Is(err, io.ErrShortWrite) || string(partial.Bytes()) != "abcd" {
		t.Fatalf("subsequent n=%d err=%v bytes=%q", n, err, partial.Bytes())
	}
}

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
	workingDirectory := filepath.Join(root, "workspace")
	if err := os.Mkdir(workingDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workingDirectory, "relative-fixture"), []byte("ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "atl")
	script := "#!/bin/sh\n" +
		"if find \"$TEST_REQUEST_DIR\" -name 'processing-*' -o -name 'request-*' | grep -q .; then exit 91; fi\n" +
		"if [ ! -f relative-fixture ]; then exit 92; fi\n" +
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
		RealBinary: binary, WorkingDirectory: workingDirectory, Policy: policy,
		Environment:    []string{"TEST_REQUEST_DIR=" + requests, "TEST_EXECUTIONS=" + executions, "TEST_CHILD_CONFIG=disposable"},
		MaxStdoutBytes: 4096, MaxStderrBytes: 4096, CommandTimeout: time.Second,
		AllowSyntheticWrites: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	if allowed, err := CommandBrokerAllowsSyntheticWrites(manifest); err != nil || !allowed {
		t.Fatalf("synthetic write authority=%v err=%v", allowed, err)
	}

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

func TestCommandBrokerBindsAndConsumesExactPreviewProposalHash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable scripts are Unix-only")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	requests, responses := filepath.Join(root, "requests"), filepath.Join(root, "responses")
	for _, directory := range []string{requests, responses} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	hashA, hashB, wrong := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	preview := func(hash string) string {
		return `{"schema_version":1,"operation":"jira_issue_create","backend_sha256":"x","requested_project":"TEST","project":{"id":"1","key":"TEST","archived":false},"type_selector":{},"issue_type":{"id":"2","name":"Task","subtask":false},"summary":{},"description":{},"fields":{},"metadata_count":1,"metadata_sha256":"x","request_sha256":"x","request_bytes":1,"registration_requested":false,"bounds":{},"proposal_hash":"` + hash + `","mode":"preview","status":"would_apply","write_attempted":false,"readback_reconciled":false,"usage":{}}`
	}
	previewA, previewB := filepath.Join(root, "preview-a.json"), filepath.Join(root, "preview-b.json")
	if err := os.WriteFile(previewA, []byte(preview(hashA)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previewB, []byte(preview(hashB)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executions := filepath.Join(root, "executions")
	binary := filepath.Join(root, "atl")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >>\"$TEST_EXECUTIONS\"\n" +
		"case \"$*\" in 'jira issue create preview --project A') cat \"$PREVIEW_A\";; 'jira issue create preview --project B') cat \"$PREVIEW_B\";; *) printf '{}\\n';; esac\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	producer := func(name, binding, project string) CLICommandRule {
		return CLICommandRule{Name: name, Command: []string{"jira", "issue", "create", "preview"}, Flags: []CLIFlagRule{{Name: "--project", Values: []string{project}, Required: true}}, BindsProposalHash: binding, MaxInvocations: 1}
	}
	consumer := func(name, binding, project string) CLICommandRule {
		return CLICommandRule{Name: name, Command: []string{"jira", "issue", "create"}, Flags: []CLIFlagRule{{Name: "--project", Values: []string{project}, Required: true}, {Name: "--expected-proposal-hash", ValueFormat: "sha256", Required: true}}, RequiresProposalHash: binding, MaxInvocations: 2}
	}
	policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{
		producer("preview-a", "candidate_a", "A"), consumer("apply-a", "candidate_a", "A"),
		producer("preview-b", "candidate_b", "B"), consumer("apply-b", "candidate_b", "B"),
	}}
	manifest := filepath.Join(root, "broker.json")
	broker, err := StartCommandBroker(CommandBrokerConfig{
		RequestDirectory: requests, ResponseDirectory: responses, ManifestPath: manifest,
		RealBinary: binary, WorkingDirectory: root, Policy: policy,
		Environment:    []string{"PATH=/usr/bin:/bin", "TEST_EXECUTIONS=" + executions, "PREVIEW_A=" + previewA, "PREVIEW_B=" + previewB},
		MaxStdoutBytes: 4096, MaxStderrBytes: 4096, CommandTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	call := func(args []string, want string) {
		t.Helper()
		response, err := CallCommandBroker(manifest, args, false)
		if err != nil || response.Status != want {
			t.Fatalf("args=%v response=%+v err=%v want=%q", args, response, err, want)
		}
	}
	call([]string{"jira", "issue", "create", "preview", "--project", "A"}, "executed")
	call([]string{"jira", "issue", "create", "--project", "A", "--expected-proposal-hash", wrong}, "rejected")
	call([]string{"jira", "issue", "create", "--project", "B", "--expected-proposal-hash", hashA}, "rejected")
	call([]string{"jira", "issue", "create", "--project", "A", "--expected-proposal-hash", hashA}, "executed")
	call([]string{"jira", "issue", "create", "--project", "A", "--expected-proposal-hash", hashA}, "rejected")
	call([]string{"jira", "issue", "create", "preview", "--project", "B"}, "executed")
	call([]string{"jira", "issue", "create", "--project", "B", "--expected-proposal-hash", hashA}, "rejected")
	call([]string{"jira", "issue", "create", "--project", "B", "--expected-proposal-hash", hashB}, "executed")
	data, err := os.ReadFile(executions)
	if err != nil || string(data) != "jira issue create preview --project A\njira issue create --project A --expected-proposal-hash "+hashA+"\njira issue create preview --project B\njira issue create --project B --expected-proposal-hash "+hashB+"\n" {
		t.Fatalf("executions=%q err=%v", data, err)
	}
}

func TestCommandBrokerReadOnlyPreviewBindsWithoutAdmittingApply(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable scripts are Unix-only")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	requests, responses := filepath.Join(root, "requests"), filepath.Join(root, "responses")
	for _, directory := range []string{requests, responses} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	hash := strings.Repeat("a", 64)
	preview := `{"schema_version":1,"operation":"jira_issue_create","backend_sha256":"x","requested_project":"TEST","project":{"id":"1","key":"TEST","archived":false},"type_selector":{},"issue_type":{"id":"2","name":"Task","subtask":false},"summary":{},"description":{},"fields":{},"metadata_count":1,"metadata_sha256":"x","request_sha256":"x","request_bytes":1,"registration_requested":false,"bounds":{},"proposal_hash":"` + hash + `","mode":"preview","status":"would_apply","write_attempted":false,"readback_reconciled":false,"usage":{}}`
	executions := filepath.Join(root, "executions")
	binary := filepath.Join(root, "atl")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >>\"$TEST_EXECUTIONS\"\n" +
		"case \"$*\" in '--read-only jira issue create preview') printf '%s\\n' \"$PREVIEW\";; 'jira issue create --expected-proposal-hash '*) printf '{}\\n';; *) exit 91;; esac\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{
		{Name: "preview", Command: []string{"jira", "issue", "create", "preview"}, BindsProposalHash: "create", MaxInvocations: 1},
		{Name: "apply", Command: []string{"jira", "issue", "create"}, Flags: []CLIFlagRule{{Name: "--expected-proposal-hash", ValueFormat: "sha256", Required: true}}, RequiresProposalHash: "create", MaxInvocations: 1},
	}}
	manifest := filepath.Join(root, "broker.json")
	broker, err := StartCommandBroker(CommandBrokerConfig{
		RequestDirectory: requests, ResponseDirectory: responses, ManifestPath: manifest,
		RealBinary: binary, WorkingDirectory: root, Policy: policy,
		Environment:    []string{"TEST_EXECUTIONS=" + executions, "PREVIEW=" + preview},
		MaxStdoutBytes: 4096, MaxStderrBytes: 4096, CommandTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	response, err := CallCommandBrokerReadOnly(manifest, []string{"jira", "issue", "create", "preview"})
	if err != nil || response.Status != "executed" {
		t.Fatalf("read-only preview response=%+v err=%v", response, err)
	}
	response, err = CallCommandBrokerReadOnly(manifest, []string{"jira", "issue", "create", "--expected-proposal-hash", hash})
	if err != nil || response.Status != "rejected" {
		t.Fatalf("read-only apply response=%+v err=%v", response, err)
	}
	response, err = CallCommandBroker(manifest, []string{"jira", "issue", "create", "--expected-proposal-hash", hash}, false)
	if err != nil || response.Status != "executed" {
		t.Fatalf("reviewed apply response=%+v err=%v", response, err)
	}
	data, err := os.ReadFile(executions)
	if err != nil || string(data) != "--read-only jira issue create preview\njira issue create --expected-proposal-hash "+hash+"\n" {
		t.Fatalf("executions=%q err=%v", data, err)
	}
}

func TestCommandBrokerProposalDecoderIsProducerSpecific(t *testing.T) {
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	create := []byte(`{"schema_version":1,"operation":"jira_issue_create","backend_sha256":"` + hash + `","requested_project":"TEST","project":{"id":"1","key":"TEST","archived":false},"type_selector":{},"issue_type":{"id":"2","name":"Task","subtask":false},"summary":{},"description":{},"fields":{},"metadata_count":1,"metadata_sha256":"x","request_sha256":"x","request_bytes":1,"registration_requested":false,"bounds":{},"proposal_hash":"` + hash + `","mode":"preview","status":"would_apply","write_attempted":false,"readback_reconciled":false,"usage":{}}`)
	comment := validJiraTriageCommentPreviewWire(t)
	field := []byte(jiraGuardedFieldWireFixture())
	for _, test := range []struct {
		name    string
		command []string
		wire    []byte
		ok      bool
	}{
		{name: "create", command: []string{"jira", "issue", "create", "preview"}, wire: create, ok: true},
		{name: "comment", command: []string{"jira", "issue", "comment", "preview"}, wire: comment, ok: true},
		{name: "field", command: []string{"jira", "issue", "field", "preview"}, wire: field, ok: true},
		{name: "create on comment", command: []string{"jira", "issue", "comment", "preview"}, wire: create},
		{name: "comment on create", command: []string{"jira", "issue", "create", "preview"}, wire: comment},
		{name: "field on create", command: []string{"jira", "issue", "create", "preview"}, wire: field},
		{name: "unknown producer", command: []string{"preview"}, wire: create},
	} {
		t.Run(test.name, func(t *testing.T) {
			hash, _, err := commandBrokerProposalProducer(test.command, test.wire)
			if (err == nil) != test.ok || test.ok && !triageWireSHA256(hash) {
				t.Fatalf("hash=%q err=%v", hash, err)
			}
		})
	}
	if commandBrokerProposalConsumer("jira issue create preview", []string{"jira", "issue", "comment", "add"}) ||
		commandBrokerProposalConsumer("jira issue comment preview", []string{"jira", "issue", "create"}) ||
		!commandBrokerProposalConsumer("jira issue field preview", []string{"jira", "issue", "field", "set"}) {
		t.Fatal("cross-producer consumer admitted")
	}
}

func TestCommandBrokerRejectsProducerWireSwapBeforeBinding(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable scripts are Unix-only")
	}
	const createHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	createWire := []byte(`{"schema_version":1,"operation":"jira_issue_create","backend_sha256":"` + createHash + `","requested_project":"TEST","project":{"id":"1","key":"TEST","archived":false},"type_selector":{},"issue_type":{"id":"2","name":"Task","subtask":false},"summary":{},"description":{},"fields":{},"metadata_count":1,"metadata_sha256":"x","request_sha256":"x","request_bytes":1,"registration_requested":false,"bounds":{},"proposal_hash":"` + createHash + `","mode":"preview","status":"would_apply","write_attempted":false,"readback_reconciled":false,"usage":{}}`)
	for _, test := range []struct {
		name            string
		producerCommand []string
		consumerCommand []string
		wire            func(*testing.T) []byte
		consumerHash    string
	}{
		{name: "comment producer emits create wire", producerCommand: []string{"jira", "issue", "comment", "preview"}, consumerCommand: []string{"jira", "issue", "comment", "add"}, wire: func(*testing.T) []byte { return createWire }, consumerHash: createHash},
		{name: "create producer emits comment wire", producerCommand: []string{"jira", "issue", "create", "preview"}, consumerCommand: []string{"jira", "issue", "create"}, wire: validJiraTriageCommentPreviewWire, consumerHash: triageTriageHash("proposal")},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			requests, responses := filepath.Join(root, "requests"), filepath.Join(root, "responses")
			for _, directory := range []string{requests, responses} {
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			wirePath, executions := filepath.Join(root, "proposal.json"), filepath.Join(root, "executions")
			if err := os.WriteFile(wirePath, append(test.wire(t), '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(root, "atl")
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$TEST_EXECUTIONS\"\ncat \"$TEST_WIRE\"\n"
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{
				{Name: "producer", Command: test.producerCommand, BindsProposalHash: "candidate", MaxInvocations: 1},
				{Name: "consumer", Command: test.consumerCommand, Flags: []CLIFlagRule{{Name: "--expected-proposal-hash", ValueFormat: "sha256", Required: true}}, RequiresProposalHash: "candidate", MaxInvocations: 1},
			}}
			manifest := filepath.Join(root, "broker.json")
			broker, err := StartCommandBroker(CommandBrokerConfig{
				RequestDirectory: requests, ResponseDirectory: responses, ManifestPath: manifest, RealBinary: binary,
				WorkingDirectory: root, Policy: policy, Environment: []string{"PATH=/usr/bin:/bin", "TEST_EXECUTIONS=" + executions, "TEST_WIRE=" + wirePath},
				MaxStdoutBytes: 4096, MaxStderrBytes: 4096, CommandTimeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			response, err := CallCommandBroker(manifest, test.producerCommand, false)
			if err != nil || response.Status != "failed" {
				t.Fatalf("producer response=%+v err=%v", response, err)
			}
			consumerArgs := append(append([]string(nil), test.consumerCommand...), "--expected-proposal-hash", test.consumerHash)
			response, err = CallCommandBroker(manifest, consumerArgs, false)
			if err != nil || response.Status != "rejected" {
				t.Fatalf("unbound consumer response=%+v err=%v", response, err)
			}
			if err := broker.Close(); err != nil {
				t.Fatal(err)
			}
			if counts := broker.invocationCounts(); counts["producer"] != 1 || counts["consumer"] != 0 {
				t.Fatalf("invocation counts=%v", counts)
			}
			if len(broker.proposalHashes) != 0 {
				t.Fatalf("producer wire swap created bindings: %+v", broker.proposalHashes)
			}
			if len(broker.producedProposalHashes) != 0 {
				t.Fatalf("producer wire swap created grading evidence: %+v", broker.producedProposalHashes)
			}
			data, err := os.ReadFile(executions)
			if err != nil || string(data) != strings.Join(test.producerCommand, " ")+"\n" {
				t.Fatalf("executions=%q err=%v, consumer must not reach selected binary", data, err)
			}
		})
	}
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
		RealBinary: binary, WorkingDirectory: root,
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
		RequestDirectory: requests, ResponseDirectory: responses, ManifestPath: manifest, RealBinary: binary, WorkingDirectory: root,
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

func TestCommandBrokerRequiresOwnerOnlyAbsoluteWorkingDirectory(t *testing.T) {
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
	config := CommandBrokerConfig{
		RequestDirectory: requests, ResponseDirectory: responses,
		ManifestPath: filepath.Join(root, "broker.json"), RealBinary: filepath.Join(root, "atl"),
		Policy:         CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{{Name: "fields", Command: []string{"jira", "fields"}, MaxInvocations: 1}}},
		MaxStdoutBytes: 8, MaxStderrBytes: 8, CommandTimeout: time.Second,
	}
	if _, err := StartCommandBroker(config); err == nil {
		t.Fatal("missing working directory passed")
	}
	config.WorkingDirectory = "."
	if _, err := StartCommandBroker(config); err == nil {
		t.Fatal("relative working directory passed")
	}
	shared := filepath.Join(root, "shared")
	if err := os.Mkdir(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	config.WorkingDirectory = shared
	if _, err := StartCommandBroker(config); err == nil {
		t.Fatal("non-owner-only working directory passed")
	}
}

func TestCommandBrokerPinsResolvedWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and fake executable assertions are Unix-only")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	requests := filepath.Join(root, "requests")
	responses := filepath.Join(root, "responses")
	firstParent := filepath.Join(root, "first")
	secondParent := filepath.Join(root, "second")
	for _, directory := range []string{requests, responses, firstParent, secondParent} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for parent, marker := range map[string]string{firstParent: "reviewed\n", secondParent: "redirected\n"} {
		workspace := filepath.Join(parent, "workspace")
		if err := os.Mkdir(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "marker"), []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ancestor := filepath.Join(root, "current")
	if err := os.Symlink(firstParent, ancestor); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "atl")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ncat marker\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "broker.json")
	broker, err := StartCommandBroker(CommandBrokerConfig{
		RequestDirectory: requests, ResponseDirectory: responses, ManifestPath: manifest,
		RealBinary: binary, WorkingDirectory: filepath.Join(ancestor, "workspace"),
		Policy:         CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{{Name: "version", Command: []string{"version"}, MaxInvocations: 1}}},
		Environment:    []string{"PATH=/usr/bin:/bin"},
		MaxStdoutBytes: 4096, MaxStderrBytes: 4096, CommandTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	if err := os.Remove(ancestor); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secondParent, ancestor); err != nil {
		t.Fatal(err)
	}
	response, err := CallCommandBroker(manifest, []string{"version"}, false)
	if err != nil || response.Status != "executed" || string(response.Stdout) != "reviewed\n" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
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
