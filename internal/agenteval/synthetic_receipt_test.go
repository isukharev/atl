package agenteval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyntheticTaskContractBindsEffectiveInputsAndIsProviderNeutral(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "input.json"), `{"value":1}`+"\n", 0o600)
	base := syntheticReceiptLoadedRun(t)

	digest, err := syntheticTaskContractSHA256(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	paired := base
	paired.spec.Provider = "claude-code"
	paired.spec.Model = "claude-test"
	paired.spec.Variant = "other-surface"
	paired.spec.Surface = SurfaceCLISkill
	paired.spec.ToolTransport = "cli"
	if pairedDigest, err := syntheticTaskContractSHA256(paired, workspace); err != nil || pairedDigest != digest {
		t.Fatalf("provider-neutral digest=%q want=%q err=%v", pairedDigest, digest, err)
	}

	tests := map[string]func(*loadedRun){
		"scenario": func(run *loadedRun) { run.scenario.Description = "Changed task." },
		"prompt":   func(run *loadedRun) { run.prompt = []byte("changed prompt\n") },
		"schema":   func(run *loadedRun) { run.responseSchema = []byte(`{"type":"array"}`) },
		"rubric":   func(run *loadedRun) { run.rubric.MinimumScoreBPS++ },
		"fixture": func(run *loadedRun) {
			fixture := *run.fixture
			fixture.Routes = append([]MockRoute(nil), fixture.Routes...)
			fixture.Routes[0].Body = json.RawMessage(`{"changed":true}`)
			run.fixture = &fixture
		},
		"semantic check": func(run *loadedRun) {
			run.spec.Checks = append([]RunCheck(nil), run.spec.Checks...)
			run.spec.Checks[0].Expected = json.RawMessage(`"changed"`)
		},
		"capabilities": func(run *loadedRun) { run.spec.DataCapabilities = []string{"jira.issue.fields"} },
		"write intent": func(run *loadedRun) { run.spec.AllowSyntheticWrites = true },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			changed, err := syntheticTaskContractSHA256(candidate, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if changed == digest {
				t.Fatalf("%s did not change task digest", name)
			}
		})
	}

	writeTestFile(t, filepath.Join(workspace, "input.json"), `{"value":2}`+"\n", 0o600)
	if changed, err := syntheticTaskContractSHA256(base, workspace); err != nil || changed == digest {
		t.Fatalf("workspace digest=%q original=%q err=%v", changed, digest, err)
	}
}

func TestSyntheticExecutionContractBindsPolicyRuntimeAndExecutables(t *testing.T) {
	base := syntheticReceiptLoadedRun(t)
	attestation := &syntheticRunAttestation{
		spec: normalizedSyntheticExecutionSpec(base.spec),
		executables: syntheticExecutableDigests{
			agent: strings.Repeat("a", 64), atl: strings.Repeat("b", 64), wrapper: strings.Repeat("c", 64),
		},
	}
	runtime := Runtime{
		Provider: "codex", AgentVersion: "agent", Model: base.spec.Model, Reasoning: base.spec.Reasoning,
		ATLVersion: "atl", PluginVersion: "plugin", SkillDigest: "sha256:" + strings.Repeat("d", 64),
		PromptContractSHA256: strings.Repeat("e", 64),
	}
	task := strings.Repeat("f", 64)
	schema := []byte(`{"type":"object"}`)
	digest, err := syntheticExecutionContractSHA256(attestation, task, runtime, schema)
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(*syntheticRunAttestation, *Runtime, *[]byte){
		"run policy": func(value *syntheticRunAttestation, _ *Runtime, _ *[]byte) {
			value.spec.TimeoutSeconds++
		},
		"model": func(_ *syntheticRunAttestation, value *Runtime, _ *[]byte) { value.Model = "other" },
		"agent runtime": func(_ *syntheticRunAttestation, value *Runtime, _ *[]byte) {
			value.AgentVersion = "other"
		},
		"atl runtime": func(_ *syntheticRunAttestation, value *Runtime, _ *[]byte) { value.ATLVersion = "other" },
		"plugin":      func(_ *syntheticRunAttestation, value *Runtime, _ *[]byte) { value.PluginVersion = "other" },
		"skill":       func(_ *syntheticRunAttestation, value *Runtime, _ *[]byte) { value.SkillDigest = "other" },
		"prompt": func(_ *syntheticRunAttestation, value *Runtime, _ *[]byte) {
			value.PromptContractSHA256 = strings.Repeat("1", 64)
		},
		"agent executable": func(value *syntheticRunAttestation, _ *Runtime, _ *[]byte) {
			value.executables.agent = strings.Repeat("2", 64)
		},
		"atl executable": func(value *syntheticRunAttestation, _ *Runtime, _ *[]byte) {
			value.executables.atl = strings.Repeat("3", 64)
		},
		"wrapper executable": func(value *syntheticRunAttestation, _ *Runtime, _ *[]byte) {
			value.executables.wrapper = strings.Repeat("4", 64)
		},
		"provider schema": func(_ *syntheticRunAttestation, _ *Runtime, value *[]byte) {
			*value = []byte(`{"type":"array"}`)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := *attestation
			candidateRuntime := runtime
			candidateSchema := append([]byte(nil), schema...)
			mutate(&candidate, &candidateRuntime, &candidateSchema)
			changed, err := syntheticExecutionContractSHA256(&candidate, task, candidateRuntime, candidateSchema)
			if err != nil {
				t.Fatal(err)
			}
			if changed == digest {
				t.Fatalf("%s did not change execution digest", name)
			}
		})
	}
}

func TestSyntheticRunAttestationRejectsExecutableDrift(t *testing.T) {
	directory := t.TempDir()
	agent := filepath.Join(directory, "agent")
	atl := filepath.Join(directory, "atl")
	wrapper := filepath.Join(directory, "wrapper")
	for _, path := range []string{agent, atl, wrapper} {
		writeTestFile(t, path, "first\n", 0o700)
	}
	attestation, err := newSyntheticRunAttestation(validRunSpec(), agent, atl, wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if err := attestation.verifyExecutables(agent, atl, wrapper); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(atl, []byte("second\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := attestation.verifyExecutables(agent, atl, wrapper); err == nil {
		t.Fatal("changed executable was accepted")
	}
}

func syntheticReceiptLoadedRun(t *testing.T) loadedRun {
	t.Helper()
	scenario := validScenario()
	fixture := MockFixture{
		SchemaVersion: MockFixtureSchemaVersion, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{Method: "GET", Path: "/jira/rest/api/2/field", Status: 200, Body: json.RawMessage(`[]`)}},
	}
	rubric := Rubric{
		SchemaVersion: RubricSchemaVersion, ID: "synthetic-receipt", ScenarioID: scenario.ID,
		MinimumScoreBPS:   5000,
		Criteria:          []RubricCriterion{{ID: "correct", Description: "Correct.", Maximum: 4, Minimum: 2, Weight: 1}},
		AllowedFindingIDs: []string{"missing"},
	}
	spec := validRunSpec()
	spec.Repetitions = 1
	return loadedRun{
		spec: spec, scenario: scenario, fixture: &fixture,
		prompt: []byte("answer the task\n"), responseSchema: []byte(`{"type":"object"}`), rubric: rubric,
	}
}
