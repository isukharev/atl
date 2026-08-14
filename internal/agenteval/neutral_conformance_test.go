package agenteval

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/agenteval/agentadapter"
	"github.com/isukharev/atl/internal/agenteval/core"
	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/extension"
	"github.com/isukharev/atl/internal/agenteval/grading"
)

const neutralContractVersion = "0.1.0-pre-release"

type neutralCase struct {
	ID       string `json:"id"`
	Family   string `json:"family"`
	Expected string `json:"expected"`
}

type neutralManifest struct {
	Schema          string        `json:"schema"`
	SchemaVersion   int           `json:"schema_version"`
	ContractVersion string        `json:"contract_version"`
	Cases           []neutralCase `json:"cases"`
}

type neutralExpectedCase struct {
	ID       string `json:"id"`
	Outcome  string `json:"outcome"`
	Check    string `json:"check"`
	Resource string `json:"resource"`
	Evidence string `json:"evidence"`
}

type neutralExpectedResults struct {
	Schema        string                `json:"schema"`
	SchemaVersion int                   `json:"schema_version"`
	Cases         []neutralExpectedCase `json:"cases"`
}

type neutralStatistics struct {
	Schema          string         `json:"schema"`
	SchemaVersion   int            `json:"schema_version"`
	ContractVersion string         `json:"contract_version"`
	CaseCount       int            `json:"case_count"`
	Outcomes        map[string]int `json:"outcomes"`
	Families        map[string]int `json:"families"`
}

type neutralProfile struct{}

func (neutralProfile) Descriptor() core.ProfileDescriptor {
	return core.ProfileDescriptor{ID: "neutral/reference", Capabilities: []core.Capability{{ID: "reference/execute", Support: core.SupportSupported}}}
}

func (neutralProfile) Open(_ context.Context, _ core.AdmittedPlan, identity core.AttemptIdentity) (core.AttemptRuntime, error) {
	return core.AttemptRuntime{
		Adapter: neutralAdapter{caseID: string(identity.Task)},
		Backend: neutralBackend{},
		Grader:  neutralGrader{},
	}, nil
}

type neutralAdapter struct{ caseID string }

func (a neutralAdapter) Execute(_ context.Context, input core.AttemptInput) (core.Observation, error) {
	caseID := a.caseID
	if caseID == "" {
		caseID = string(input.Task().ID)
	}
	check := core.CheckObservation{ID: "decision", Presence: core.PresenceObserved, Passed: true}
	resource := core.ResourceObservation{ID: "cost", Presence: core.PresenceObserved, Value: 7}
	evidence := core.EvidenceObservation{ID: "proof", Presence: core.PresenceObserved, Accepted: true}
	switch caseID {
	case "no-lift", "near-miss-negative":
		check.Passed = false
	case "negative-stale-guidance":
		check.Presence = core.PresenceUnknown
		check.Passed = false
		resource.Presence = core.PresenceUnsupported
		resource.Value = 0
		evidence.Presence = core.PresenceUnknown
		evidence.Accepted = false
	case "resource-tax":
		check.Passed = false
		resource.Value = 99
		evidence.Presence = core.PresenceUnsupported
		evidence.Accepted = false
	case "verifier-isolation":
		resource.Presence = core.PresenceNotApplicable
		resource.Value = 0
	case "lifecycle-security":
		check.Presence = core.PresenceUnsupported
		check.Passed = false
		resource.Presence = core.PresenceUnknown
		resource.Value = 0
		evidence.Presence = core.PresenceNotApplicable
		evidence.Accepted = false
	}
	return core.Observation{Checks: []core.CheckObservation{check}, Resources: []core.ResourceObservation{resource}, Evidence: []core.EvidenceObservation{evidence}}, nil
}

type neutralBackend struct{}

func (neutralBackend) Run(ctx context.Context, input core.AttemptInput, adapter core.AgentAdapter) (core.Observation, error) {
	return adapter.Execute(ctx, input)
}

type neutralGrader struct{}

func (neutralGrader) Grade(_ context.Context, _ core.AttemptInput, observation core.Observation) (core.Grade, error) {
	return core.Grade{Checks: []core.CheckGrade{{ID: observation.Checks[0].ID, Presence: observation.Checks[0].Presence, Passed: observation.Checks[0].Passed}}}, nil
}

func TestNeutralConformanceCorpusIsDeterministicAndContentAddressed(t *testing.T) {
	root := neutralCorpusRoot(t)
	manifest := readNeutralJSON[neutralManifest](t, filepath.Join(root, "fixtures", "manifest.v1.json"))
	if manifest.Schema != "agent-eval/neutral-conformance" || manifest.SchemaVersion != 1 || manifest.ContractVersion != neutralContractVersion || len(manifest.Cases) != 10 {
		t.Fatalf("unexpected neutral manifest: %+v", manifest)
	}
	expected := readNeutralJSON[neutralExpectedResults](t, filepath.Join(root, "expected", "neutral-results.v1.json"))
	if expected.Schema != "agent-eval/neutral-results" || expected.SchemaVersion != 1 || len(expected.Cases) != len(manifest.Cases) {
		t.Fatalf("unexpected expected-results fixture: %+v", expected)
	}
	statistics := readNeutralJSON[neutralStatistics](t, filepath.Join(root, "statistics", "neutral-summary.v1.json"))
	if statistics.Schema != "agent-eval/neutral-statistics" || statistics.SchemaVersion != 1 || statistics.ContractVersion != neutralContractVersion || statistics.CaseCount != len(manifest.Cases) {
		t.Fatalf("unexpected neutral statistics fixture: %+v", statistics)
	}
	if !reflect.DeepEqual(statistics.Outcomes, map[string]int{"failed": 3, "succeeded": 5, "unknown": 2}) || !reflect.DeepEqual(statistics.Families, map[string]int{"activation": 1, "lifecycle": 1, "negative_guidance": 2, "resource": 1, "selection": 1, "skill_lift": 2, "stateful": 1, "verifier": 1}) {
		t.Fatalf("unexpected neutral statistics counts: %+v", statistics)
	}
	registry, err := core.NewRegistry(neutralProfile{})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := core.NewEngine(registry)
	if err != nil {
		t.Fatal(err)
	}
	actual := neutralExpectedResults{Schema: expected.Schema, SchemaVersion: expected.SchemaVersion, Cases: make([]neutralExpectedCase, 0, len(manifest.Cases))}
	for _, testCase := range manifest.Cases {
		attempts := uint32(1)
		if testCase.ID == "stateful-multi-step" {
			attempts = 2
		}
		plan := neutralCorePlan(testCase.ID, attempts)
		first, err := engine.Run(context.Background(), plan)
		if err != nil {
			t.Fatalf("case %s first run: %v", testCase.ID, err)
		}
		second, err := engine.Run(context.Background(), plan)
		if err != nil {
			t.Fatalf("case %s second run: %v", testCase.ID, err)
		}
		firstData := neutralCanonicalJSON(t, first)
		secondData := neutralCanonicalJSON(t, second)
		if !bytes.Equal(firstData, secondData) {
			t.Fatalf("case %s result is not deterministic", testCase.ID)
		}
		if len(first.Attempts) == 0 {
			t.Fatalf("case %s has no attempts", testCase.ID)
		}
		attempt := first.Attempts[0]
		actual.Cases = append(actual.Cases, neutralExpectedCase{
			ID: testCase.ID, Outcome: neutralOutcomeName(attempt.Outcome),
			Check:    neutralCheckName(attempt.Observation.Checks[0]),
			Resource: neutralPresenceName(attempt.Observation.Resources[0].Presence),
			Evidence: neutralPresenceName(attempt.Observation.Evidence[0].Presence),
		})
	}
	actualData := neutralCanonicalJSON(t, actual)
	expectedData, err := os.ReadFile(filepath.Join(root, "expected", "neutral-results.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actualData, expectedData) {
		t.Fatalf("neutral result projection drifted:\nwant %s\n got %s", expectedData, actualData)
	}
	resultDigest := neutralSHA256(actualData)
	if got := strings.TrimSpace(string(readNeutralFile(t, filepath.Join(root, "expected", "neutral-results.sha256")))); got != resultDigest {
		t.Fatalf("neutral result digest=%s want=%s", resultDigest, got)
	}
	corpusDigest := neutralCorpusDigest(t, root)
	if got := strings.TrimSpace(string(readNeutralFile(t, filepath.Join(root, "expected", "corpus.sha256")))); got != corpusDigest {
		t.Fatalf("neutral corpus digest=%s want=%s", corpusDigest, got)
	}
	assertNeutralPrivacy(t, root, actualData)
}

func TestNeutralConformanceAdapterBackendGraderAndExtensionLanes(t *testing.T) {
	adapterContract, err := agentadapter.ReferenceContract()
	if err != nil {
		t.Fatal(err)
	}
	for _, activated := range []bool{false, true} {
		observation, err := agentadapter.NewReferenceObservation(adapterContract, strings.Repeat("a", 64), activated)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := agentadapter.EncodeObservation(adapterContract, observation)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := agentadapter.DecodeObservation(bytes.NewReader(encoded), adapterContract)
		if err != nil || !reflect.DeepEqual(decoded, observation) {
			t.Fatalf("adapter round trip activated=%v decoded=%+v err=%v", activated, decoded, err)
		}
	}

	backendContract, err := executionbackend.ReferenceContract()
	if err != nil {
		t.Fatal(err)
	}
	fixture := neutralArchive(t, map[string][]byte{"input.txt": []byte("42\n")})
	skill := neutralArchive(t, map[string][]byte{"SKILL.md": []byte("synthetic skill\n")})
	definitions := neutralArchive(t, map[string][]byte{"task.json": []byte(`{"task":"addition"}`)})
	fixtureSHA, _ := executionbackend.ArchiveSHA256(fixture, executionbackend.MaxArchiveBytes, executionbackend.MaxSnapshotEntries)
	skillSHA, _ := executionbackend.ArchiveSHA256(skill, executionbackend.MaxArchiveBytes, executionbackend.MaxSnapshotEntries)
	definitionsSHA, _ := executionbackend.ArchiveSHA256(definitions, executionbackend.MaxArchiveBytes, executionbackend.MaxSnapshotEntries)
	plan, err := executionbackend.NewReferencePlan(backendContract, executionbackend.ReferencePlanOptions{
		FixtureSHA256: fixtureSHA, SkillSHA256: skillSHA, DefinitionsSHA256: definitionsSHA,
		Resources: executionbackend.ResourcePolicy{DeadlineMillis: 5000, MaxInputBytes: executionbackend.MaxArchiveBytes, MaxOutputBytes: executionbackend.MaxArtifactBytes, MaxEntries: executionbackend.MaxSnapshotEntries, MaxArtifacts: 1, MaxOperations: 1},
		Artifacts: []executionbackend.ArtifactDeclaration{{ID: "answer", MaxBytes: 64, Privacy: executionbackend.PrivacyPublic}},
		Program:   executionbackend.Program{Kind: executionbackend.ProgramReferenceCopy, SourceMount: executionbackend.MountFixture, SourcePath: "input.txt", ArtifactID: "answer"},
		Verifier:  executionbackend.Verifier{Kind: executionbackend.VerifierSHA256Equals, ArtifactID: "answer", ExpectedSHA256: neutralSHA256([]byte("42\n"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := executionbackend.Admit(backendContract, plan)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := executionbackend.PrepareReferenceInputs(context.Background(), admitted, executionbackend.ReferenceInputs{Fixture: fixture, Skill: skill, Definitions: definitions})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executionbackend.RunReference(context.Background(), admitted, prepared)
	if err != nil || result.Receipt.Verdict != executionbackend.VerdictSucceeded || len(result.Artifacts) != 1 || string(result.Artifacts[0].Data) != "42\n" {
		t.Fatalf("reference backend result=%+v err=%v", result, err)
	}
	if result.Receipt.Network != executionbackend.PresenceObserved || result.Receipt.Credentials != executionbackend.PresenceObserved {
		t.Fatalf("reference backend did not record bounded authority coverage: %+v", result.Receipt)
	}
	clear(prepared.Fixture)
	clear(prepared.Skill)
	clear(prepared.Definitions)

	gradingContract, err := grading.BuiltinContract()
	if err != nil {
		t.Fatal(err)
	}
	gradingContractSHA, err := grading.ContractSHA256(gradingContract)
	if err != nil {
		t.Fatal(err)
	}
	gradingPlan := grading.Plan{Schema: grading.PlanSchema, SchemaVersion: grading.SchemaVersion, ContractVersion: grading.ContractVersion, ContractSHA256: gradingContractSHA,
		Mode: grading.ModeDeterministic, InputProjectionSHA256: strings.Repeat("1", 64), EnvironmentSHA256: strings.Repeat("2", 64),
		Checks: []grading.Check{{ID: "answer", Kind: grading.CheckFileExists, Visibility: grading.VisibilityPublic, FileExists: &grading.FileExistsRule{EvidenceID: "proof", Expected: true}}},
		Limits: grading.PlanLimits{DeadlineMillis: 1000, MaxInputBytes: grading.MaxEvidenceBytes, MaxOutputBytes: grading.MaxReceiptBytes}}
	gradingAdmitted, err := grading.Admit(gradingContract, gradingPlan)
	if err != nil {
		t.Fatal(err)
	}
	preparedEvidence, err := grading.PrepareEvidence(context.Background(), gradingAdmitted, grading.EvidenceSet{InputProjectionSHA256: gradingPlan.InputProjectionSHA256,
		Files: []grading.FileEvidence{{ID: "proof", Visibility: grading.VisibilityPublic, Present: true, Mode: 0o644, Data: []byte("42\n")}}, Commands: []grading.CommandEvidence{}, Trees: []grading.TreeEvidence{}, Sequences: []grading.SequenceEvidence{}, Counters: []grading.CounterEvidence{}})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := grading.EvaluateDeterministic(context.Background(), gradingAdmitted, preparedEvidence)
	preparedEvidence.Destroy()
	if err != nil || receipt.Status != grading.ReceiptComplete || len(receipt.Decisions) != 1 || !receipt.Decisions[0].Passed {
		t.Fatalf("deterministic grade receipt=%+v err=%v", receipt, err)
	}
	receiptData, err := grading.EncodeReceipt(gradingPlan, receipt)
	if err != nil {
		t.Fatal(err)
	}
	decodedReceipt, err := grading.DecodeReceipt(bytes.NewReader(receiptData), gradingPlan)
	if err != nil {
		t.Fatal(err)
	}
	canonicalReceipt, err := grading.EncodeReceipt(gradingPlan, decodedReceipt)
	if err != nil || !bytes.Equal(canonicalReceipt, receiptData) {
		t.Fatalf("grader receipt is not canonical: %v", err)
	}
	if bytes.Contains(receiptData, []byte("42\n")) {
		t.Fatal("grader receipt leaked evidence bytes")
	}

	manifest := neutralExtensionManifest()
	manifestData, err := extension.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	decodedManifest, err := extension.DecodeManifest(manifestData)
	if err != nil || !reflect.DeepEqual(decodedManifest, manifest) {
		t.Fatalf("extension manifest round trip: %+v err=%v", decodedManifest, err)
	}
	initialize, err := extension.NewInitialize(manifest, strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	initialized, err := extension.NewInitialized(manifest, initialize)
	if err != nil {
		t.Fatal(err)
	}
	if err := extension.ValidateInitialized(manifest, initialize, initialized); err != nil {
		t.Fatal(err)
	}
	invoke, err := extension.NewInvoke(manifest, initialize, initialized, strings.Repeat("d", 64), extension.OperationExecute, []extension.ConfigurationValue{}, []extension.ArtifactReference{}, extension.InvocationPolicy{MaxOutputArtifacts: 1, MaxOutputBytes: 64, OutputPrivacy: extension.PrivacyPublic, Replay: extension.ReplayUnsafe})
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := extension.NewResult(invoke, []extension.ArtifactReference{{ID: "answer", Schema: "agent-eval/artifact", SchemaVersion: 1, SHA256: strings.Repeat("e", 64), SizeBytes: 3, Privacy: extension.PrivacyPublic}})
	if err != nil || extension.ValidateTerminal(manifest, invoke, terminal) != nil {
		t.Fatalf("extension terminal: %+v err=%v", terminal, err)
	}
	line, err := extension.EncodeFrameLine(terminal)
	if err != nil {
		t.Fatal(err)
	}
	decodedTerminal, err := extension.DecodeFrameLine(line)
	if err != nil || extension.ValidateTerminal(manifest, invoke, decodedTerminal) != nil {
		t.Fatalf("extension frame round trip: %+v err=%v", decodedTerminal, err)
	}
}

func TestNeutralConformanceReporterAndPrivacyLanes(t *testing.T) {
	root := neutralCorpusRoot(t)
	junit, err := ProjectJUnit(JUnitProjectionInput{Results: []JUnitResultInput{
		{Identity: "result/positive", SchemaVersion: ResultSchemaVersion, Status: "pass", EvidenceCovered: true, EvidenceState: string(EvidenceAttemptStateSucceeded)},
		{Identity: "result/no-lift", SchemaVersion: ResultSchemaVersion, Status: "fail", Violations: []JUnitViolationInput{{Code: "required_check_failed", Subject: "answer"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	junitData, err := EncodeJUnit(junit)
	if err != nil {
		t.Fatal(err)
	}
	expectedJUnit := readNeutralFile(t, filepath.Join(root, "reports", "neutral-junit.xml"))
	if !bytes.Equal(junitData, expectedJUnit) {
		t.Fatalf("JUnit projection drifted:\nwant %s\n got %s", expectedJUnit, junitData)
	}
	htmlReport, err := ProjectHTML(validHTMLProjectionInput())
	if err != nil {
		t.Fatal(err)
	}
	htmlData, err := EncodeHTML(htmlReport)
	if err != nil {
		t.Fatal(err)
	}
	if len(htmlData) == 0 || bytes.Contains(bytes.ToLower(htmlData), []byte("http://")) || bytes.Contains(bytes.ToLower(htmlData), []byte("https://")) {
		t.Fatal("static HTML reporter emitted active external content")
	}
	staticReport := readNeutralFile(t, filepath.Join(root, "reports", "neutral-report.html"))
	for _, marker := range []string{"<script", "<img", "http://", "https://", "/tmp", "/home/"} {
		if bytes.Contains(bytes.ToLower(staticReport), []byte(marker)) {
			t.Fatalf("static report contains forbidden marker %q", marker)
		}
	}

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, importSpec := range parsed.Imports {
		path := strings.Trim(importSpec.Path.Value, `"`)
		forbidden := []string{"internal/adapter/", "internal/app", "internal/cli", "internal/httpx", "net/http", "os/exec", "os/user", "syscall"}
		for _, marker := range forbidden {
			if strings.Contains(path, marker) {
				t.Fatalf("neutral conformance imported forbidden authority %q", path)
			}
		}
	}
	if filepath.IsAbs(filepath.Join(root, "extensions", "reference-extension")) == false {
		t.Fatal("out-of-tree extension fixture path was not resolved")
	}
	if _, err := os.Stat(filepath.Join(root, "extensions", "reference-extension", "manifest.json")); err != nil {
		t.Fatal(err)
	}
	registry, err := core.NewRegistry(neutralProfile{})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := core.NewEngine(registry)
	if err != nil {
		t.Fatal(err)
	}
	invalid := neutralCorePlan("positive-skill-lift", 1)
	invalid.Task.RequiredCapabilities = []core.CapabilityID{"missing/authority"}
	if _, err := engine.Run(context.Background(), invalid); err == nil || !neutralErrorCodeIs(err, core.ErrorCapabilityUndeclared) || strings.Contains(err.Error(), "missing/authority") {
		t.Fatalf("invalid neutral admission was not content-minimized: %v", err)
	}
}

func neutralCorePlan(id string, attempts uint32) core.Plan {
	return core.Plan{ID: core.PlanID("neutral/" + id), Profile: "neutral/reference", Task: core.Task{ID: core.TaskID(id), RequiredCapabilities: []core.CapabilityID{"reference/execute"}, Checks: []core.Check{{ID: "decision", Weight: 1}}, Resources: []core.ResourceID{"cost"}, Evidence: []core.EvidenceID{"proof"}}, Fixture: core.Fixture{ID: core.FixtureID("fixture/" + id)}, Treatment: core.Treatment{ID: core.TreatmentID("treatment/" + id), Skills: []core.Skill{{ID: "synthetic/skill"}}}, Attempts: attempts}
}

func neutralOutcomeName(value core.Outcome) string {
	switch value {
	case core.OutcomeSucceeded:
		return "succeeded"
	case core.OutcomeFailed:
		return "failed"
	case core.OutcomeNotApplicable:
		return "not_applicable"
	default:
		return "unknown"
	}
}

func neutralCheckName(value core.CheckObservation) string {
	if value.Presence != core.PresenceObserved {
		return neutralPresenceName(value.Presence)
	}
	if value.Passed {
		return "observed_pass"
	}
	return "observed_fail"
}

func neutralPresenceName(value core.Presence) string {
	switch value {
	case core.PresenceObserved:
		return "observed"
	case core.PresenceUnsupported:
		return "unsupported"
	case core.PresenceNotApplicable:
		return "not_applicable"
	default:
		return "unknown"
	}
}

func neutralExtensionManifest() extension.Manifest {
	operations := extension.OperationsForRole(extension.RoleAgentAdapter)
	claims := make([]extension.CapabilityClaim, len(operations))
	for index, operation := range operations {
		claims[index] = extension.CapabilityClaim{ID: extension.CapabilityFor(extension.RoleAgentAdapter, operation), State: extension.CapabilitySupported}
	}
	return extension.Manifest{Schema: extension.ManifestSchema, SchemaVersion: extension.ManifestSchemaVersion, ContractVersion: extension.ContractVersion, ProtocolVersions: []int{extension.ProtocolVersion},
		Component:        extension.Descriptor{ID: "neutral-reference-extension", Version: "1.0.0", Role: extension.RoleAgentAdapter, Operations: operations, Capabilities: claims},
		ExecutableSHA256: strings.Repeat("a", 64), ConfigurationSchema: []extension.ConfigurationField{}, Platforms: []extension.Platform{{OS: "linux", Architecture: "amd64"}},
		Requirements: []extension.EnforcementRequirement{extension.EnforcementBoundedIO, extension.EnforcementDeadline, extension.EnforcementExactEnvironment, extension.EnforcementFilesystemIsolation, extension.EnforcementNetworkIsolation}}
}

func neutralArchive(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, key := range keys {
		data := entries[key]
		if err := writer.WriteHeader(&tar.Header{Name: key, Mode: 0o444, Size: int64(len(data)), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func neutralCorpusRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "benchmarks", "agent-eval-standalone")
}

func readNeutralFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readNeutralJSON[T any](t *testing.T, path string) T {
	t.Helper()
	data := readNeutralFile(t, path)
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func neutralCanonicalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func neutralSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func neutralCorpusDigest(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	for _, directory := range []string{"fixtures", "authoring", "extensions", "reports", "statistics"} {
		base := filepath.Join(root, directory)
		err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, relative := range paths {
		data := readNeutralFile(t, filepath.Join(root, filepath.FromSlash(relative)))
		fmt.Fprintf(hash, "%s\x00%d\x00%s\x00", relative, len(data), neutralSHA256(data))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func assertNeutralPrivacy(t *testing.T, root string, result []byte) {
	t.Helper()
	for _, marker := range []string{"/tmp/", "/home/", "/workspaces/", "jira", "confluence", "api_key", "password", "private-key"} {
		if bytes.Contains(bytes.ToLower(result), []byte(marker)) {
			t.Fatalf("neutral result contains privacy marker %q", marker)
		}
	}
	staticReport := readNeutralFile(t, filepath.Join(root, "reports", "neutral-report.html"))
	if bytes.Contains(bytes.ToLower(staticReport), []byte("provider")) {
		t.Fatal("neutral static report contains provider-specific wording")
	}
}

func neutralErrorCodeIs(err error, want core.ErrorCode) bool {
	code, ok := core.CodeOf(err)
	return ok && code == want
}
