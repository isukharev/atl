package agenteval

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

func TestHermeticReferenceExecutionBackendClosesDurableLifecycle(t *testing.T) {
	contract, plan, inputs := hermeticReferenceFixture(t, executionbackend.ProgramReferenceCopy)
	_ = contract
	session := hermeticReferenceSession(t, plan)
	result, err := RunHermeticReferenceTrial(context.Background(), session, plan, inputs)
	if err != nil || result.Receipt.Verdict != executionbackend.VerdictSucceeded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	inspection, err := session.store.Inspect(session.plan.AttemptID)
	if err != nil || inspection.Projection.State != lifecycle.StateSucceeded || !inspection.Projection.Terminal {
		t.Fatalf("inspection=%+v err=%v", inspection, err)
	}

	_, cancelPlan, cancelInputs := hermeticReferenceFixture(t, executionbackend.ProgramWaitForCancel)
	cancelSession := hermeticReferenceSession(t, cancelPlan)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	result, err = RunHermeticReferenceTrial(ctx, cancelSession, cancelPlan, cancelInputs)
	if !errors.Is(err, context.Canceled) || result.Receipt.Termination != executionbackend.PresenceObserved {
		t.Fatalf("cancel result=%+v err=%v", result, err)
	}
	inspection, err = cancelSession.store.Inspect(cancelSession.plan.AttemptID)
	if err != nil || inspection.Projection.State != lifecycle.StateCanceled || !inspection.Projection.Terminal {
		t.Fatalf("cancel inspection=%+v err=%v", inspection, err)
	}
}

func TestHermeticReferenceExecutionBackendRefusesBeforeCommit(t *testing.T) {
	_, canceledPlan, canceledInputs := hermeticReferenceFixture(t, executionbackend.ProgramReferenceCopy)
	canceledSession := hermeticReferenceSession(t, canceledPlan)
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunHermeticReferenceTrial(canceledContext, canceledSession, canceledPlan, canceledInputs); !errors.Is(err, context.Canceled) {
		t.Fatalf("precommit cancel err=%v", err)
	}
	inspection, err := canceledSession.store.Inspect(canceledSession.plan.AttemptID)
	if err != nil || inspection.Projection.State != lifecycle.StateCanceled || len(inspection.Events) != 1 {
		t.Fatalf("precommit cancel inspection=%+v err=%v", inspection, err)
	}

	_, plan, inputs := hermeticReferenceFixture(t, executionbackend.ProgramReferenceCopy)
	unsupported := plan
	unsupported.Resources.CPUTimeMillis = 1
	session := hermeticReferenceSession(t, unsupported)
	if _, err := RunHermeticReferenceTrial(context.Background(), session, unsupported, inputs); !errors.Is(err, executionbackend.ErrUnsupported) {
		t.Fatalf("unsupported err=%v", err)
	}
	inspection, err = session.store.Inspect(session.plan.AttemptID)
	if err != nil || inspection.Projection.State != lifecycle.StateUnsupported || len(inspection.Events) != 1 {
		t.Fatalf("unsupported inspection=%+v err=%v", inspection, err)
	}

	_, plan, inputs = hermeticReferenceFixture(t, executionbackend.ProgramReferenceCopy)
	otherPlan := plan
	otherPlan.Resources.MaxOutputBytes--
	bound := hermeticReferenceSession(t, otherPlan)
	if _, err := RunHermeticReferenceTrial(context.Background(), bound, plan, inputs); err == nil {
		t.Fatal("changed binding was admitted")
	}
	inspection, err = bound.store.Inspect(bound.plan.AttemptID)
	if err != nil || inspection.Projection.State != lifecycle.StatePolicyDenied || len(inspection.Events) != 1 {
		t.Fatalf("binding inspection=%+v err=%v", inspection, err)
	}

	_, plan, inputs = hermeticReferenceFixture(t, executionbackend.ProgramReferenceCopy)
	inputs.Fixture = append(inputs.Fixture, []byte("hidden")...)
	invalidInput := hermeticReferenceSession(t, plan)
	if _, err := RunHermeticReferenceTrial(context.Background(), invalidInput, plan, inputs); !errors.Is(err, executionbackend.ErrContract) {
		t.Fatalf("invalid input err=%v", err)
	}
	inspection, err = invalidInput.store.Inspect(invalidInput.plan.AttemptID)
	if err != nil || inspection.Projection.State != lifecycle.StatePolicyDenied || len(inspection.Events) != 1 {
		t.Fatalf("invalid input inspection=%+v err=%v", inspection, err)
	}

	_, plan, inputs = hermeticReferenceFixture(t, executionbackend.ProgramReferenceCopy)
	plan.Artifacts[0].MaxBytes = 1
	oversized := hermeticReferenceSession(t, plan)
	if _, err := RunHermeticReferenceTrial(context.Background(), oversized, plan, inputs); !errors.Is(err, executionbackend.ErrPolicy) {
		t.Fatalf("oversized precommit err=%v", err)
	}
	inspection, err = oversized.store.Inspect(oversized.plan.AttemptID)
	if err != nil || inspection.Projection.State != lifecycle.StatePolicyDenied || len(inspection.Events) != 1 {
		t.Fatalf("oversized inspection=%+v err=%v", inspection, err)
	}
}

func TestHermeticReferenceVerifierFailureIsDurablyFailed(t *testing.T) {
	_, plan, inputs := hermeticReferenceFixture(t, executionbackend.ProgramReferenceCopy)
	plan.Verifier.ExpectedSHA256 = strings.Repeat("0", 64)
	session := hermeticReferenceSession(t, plan)
	result, err := RunHermeticReferenceTrial(context.Background(), session, plan, inputs)
	if err != nil || result.Receipt.Verdict != executionbackend.VerdictFailed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	inspection, err := session.store.Inspect(session.plan.AttemptID)
	if err != nil || inspection.Projection.State != lifecycle.StateFailed || !inspection.Projection.Terminal {
		t.Fatalf("inspection=%+v err=%v", inspection, err)
	}
}

func TestLocalExecutionBackendIsBoundAndExplicitlyNonHermetic(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(workspace+"/input.txt", []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	contract := resolvedRunContract{spec: RunSpec{TimeoutSeconds: 30, BackendMode: BackendModeSynthetic, Surface: SurfaceCLISkill},
		scenario: Scenario{ID: "synthetic-task"}, prompt: []byte("prompt"), providerPrompt: []byte("provider"),
		responseSchema: []byte("{}"), workspaceTemplate: workspace}
	digest := strings.Repeat("a", 64)
	backend, plan, first, err := localExecutionBackendTrialPlan(contract, digest, strings.Repeat("d", 64), strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	claims := map[executionbackend.CapabilityID]executionbackend.Support{}
	for _, capability := range backend.Capabilities {
		claims[capability.ID] = capability.Support
	}
	if backend.Assurance != executionbackend.AssuranceLocalProcess || plan.Network.Mode != executionbackend.NetworkAmbient ||
		plan.Credentials.Mode != executionbackend.CredentialsAmbient || claims[executionbackend.CapabilityNetworkDeny] != executionbackend.SupportUnsupported ||
		claims[executionbackend.CapabilityProcessTree] != executionbackend.SupportUnknown ||
		claims[executionbackend.CapabilityWorkspaceOutputOnly] != executionbackend.SupportUnknown ||
		!slices.Equal(plan.Requirements, executionbackend.SortedRequirements(executionbackend.CapabilityCredentialsAmbient,
			executionbackend.CapabilityNetworkAmbient, executionbackend.CapabilityResourceDeadline, executionbackend.CapabilityWorkspaceFresh)) ||
		plan.Mounts[0].ReadOnly || plan.Mounts[1].ReadOnly || plan.Mounts[2].ReadOnly {
		t.Fatalf("backend=%+v plan=%+v", backend, plan)
	}
	if _, err := executionbackend.Admit(backend, plan); err != nil {
		t.Fatal(err)
	}
	readOnlyDrift := plan
	readOnlyDrift.Mounts = append([]executionbackend.Mount{}, plan.Mounts...)
	readOnlyDrift.Mounts[0].ReadOnly = true
	if _, err := executionbackend.Admit(backend, readOnlyDrift); !errors.Is(err, executionbackend.ErrUnsupported) {
		t.Fatalf("unproved read-only mount err=%v", err)
	}
	policyDrift := contract
	policyDrift.spec.ToolTransport = "mcp"
	_, _, policyDigest, err := localExecutionBackendTrialPlan(policyDrift, digest, strings.Repeat("d", 64), strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil || policyDigest == first {
		t.Fatalf("policy digest=%q original=%q err=%v", policyDigest, first, err)
	}
	fixtureDrift := contract
	fixtureDrift.fixture = &MockFixture{SchemaVersion: MockFixtureSchemaVersion, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{Method: "GET", Path: "/jira/item", Status: 200, Body: []byte(`{}`)}}}
	_, _, fixtureDigest, err := localExecutionBackendTrialPlan(fixtureDrift, digest, strings.Repeat("d", 64), strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil || fixtureDigest == first {
		t.Fatalf("fixture digest=%q original=%q err=%v", fixtureDigest, first, err)
	}
	if err := os.WriteFile(workspace+"/input.txt", []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, second, err := localExecutionBackendTrialPlan(contract, digest, strings.Repeat("d", 64), strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("workspace drift retained local backend plan identity")
	}
}

func TestLocalExecutionBackendRejectsChangedCopiedInputsBeforeCommit(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "input.txt"), []byte("admitted"), 0o600); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(t.TempDir(), "skill")
	if err := os.Mkdir(skill, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("admitted"), 0o600); err != nil {
		t.Fatal(err)
	}
	skillDigest, err := digestTree(skill)
	if err != nil {
		t.Fatal(err)
	}
	skillDigest, err = normalizedRunSkillDigest(skillDigest)
	if err != nil {
		t.Fatal(err)
	}
	contract := resolvedRunContract{spec: RunSpec{TimeoutSeconds: 30, BackendMode: BackendModeSynthetic, Surface: SurfaceCLISkill},
		scenario: Scenario{ID: "synthetic-task"}, prompt: []byte("prompt"), providerPrompt: []byte("provider"),
		responseSchema: []byte("{}"), workspaceTemplate: workspace}
	pluginDigest := "sha256:" + strings.Repeat("d", 64)
	_, plan, _, err := localExecutionBackendTrialPlan(contract, skillDigest, pluginDigest, strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	admission := &localExecutionBackendAttemptAdmission{plan: plan, skillSHA256: skillDigest, pluginSHA256: pluginDigest,
		atlSHA256: strings.Repeat("b", 64), wrapperSHA256: strings.Repeat("c", 64)}

	if err := os.WriteFile(filepath.Join(workspace, "input.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	copiedWorkspace := filepath.Join(t.TempDir(), "copied-workspace")
	if err := copyWorkspace(workspace, copiedWorkspace); err != nil {
		t.Fatal(err)
	}
	if err := verifyExecutionBackendWorkspaceCopy(copiedWorkspace, admission); err == nil {
		t.Fatal("changed workspace bytes matched the admitted fixture")
	}

	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	copiedSkill := filepath.Join(t.TempDir(), "copied-skill")
	if err := copyWorkspace(skill, copiedSkill); err != nil {
		t.Fatal(err)
	}
	if err := verifyExecutionBackendSkillCopy(copiedSkill, admission); err == nil {
		t.Fatal("changed skill bytes matched the admitted skill")
	}

	wrapper := filepath.Join(t.TempDir(), "wrapper")
	if err := os.WriteFile(wrapper, []byte("changed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyExecutionBackendWrapper(wrapper, filepath.Join(t.TempDir(), "copied-wrapper"), admission); err == nil {
		t.Fatal("changed wrapper bytes matched the admitted executable")
	}
}

func TestLocalExecutionBackendCopiesOnlyProviderPluginInputs(t *testing.T) {
	for _, provider := range []string{"codex", "claude-code"} {
		t.Run(provider, func(t *testing.T) {
			spec := RunSpec{Provider: provider}
			root := t.TempDir()
			writeTestPluginTrees(t, root, "0.4.0", "Synthetic skill.")
			unrelated := filepath.Join(root, "private", "runs", "unrelated.txt")
			if err := os.MkdirAll(filepath.Dir(unrelated), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(unrelated, []byte("must not enter the admitted snapshot"), 0o600); err != nil {
				t.Fatal(err)
			}
			want, err := digestProviderPluginRoot(root, spec)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "private", "runs", "run-01", ".atl-eval", "admitted", "plugin")
			if err := copyProviderPluginRoot(root, target, spec); err != nil {
				t.Fatal(err)
			}
			got, err := digestProviderPluginRoot(target, spec)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("copied provider plugin digest=%q want=%q", got, want)
			}
			if _, err := os.Lstat(filepath.Join(target, "private")); !os.IsNotExist(err) {
				t.Fatalf("admitted snapshot copied unrelated repository state: %v", err)
			}
		})
	}
}

func TestLocalExecutionBackendAcceptsCodexSnapshotWithoutMarketplace(t *testing.T) {
	mcpSpec := RunSpec{Provider: "codex", BackendMode: BackendModeSynthetic, Surface: SurfaceATLMCP, ToolTransport: "mcp"}
	root := t.TempDir()
	writeTestPluginTrees(t, root, "0.4.0", "Synthetic skill.")
	if err := os.RemoveAll(filepath.Join(root, ".agents")); err != nil {
		t.Fatal(err)
	}
	want, err := digestProviderPluginRoot(root, mcpSpec)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "plugin")
	if err := copyProviderPluginRoot(root, target, mcpSpec); err != nil {
		t.Fatal(err)
	}
	got, err := digestProviderPluginRoot(target, mcpSpec)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("minimal codex plugin digest=%q want=%q", got, want)
	}
	if _, err := os.Lstat(filepath.Join(target, ".agents", "plugins")); !os.IsNotExist(err) {
		t.Fatalf("minimal codex snapshot invented a marketplace: %v", err)
	}
	privateCLISpec := RunSpec{Provider: "codex", BackendMode: BackendModePrivateLive, Surface: SurfaceCLISkill, ToolTransport: "cli"}
	if _, err := digestProviderPluginRoot(root, privateCLISpec); err == nil {
		t.Fatal("private codex CLI admitted a missing marketplace")
	}
	if err := copyProviderPluginRoot(root, filepath.Join(t.TempDir(), "private-cli-plugin"), privateCLISpec); err == nil {
		t.Fatal("private codex CLI copied a missing marketplace")
	}
}

func TestExecutionBackendReadabilitySourcesAreCanonicalAndBound(t *testing.T) {
	contractData, err := os.ReadFile("testdata/standalone-readability/execution-backend-contract-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	contract, err := DecodeExecutionBackendContract(bytes.NewReader(contractData))
	if err != nil {
		t.Fatal(err)
	}
	planData, err := os.ReadFile("testdata/standalone-readability/trial-plan-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := DecodeExecutionBackendTrialPlan(bytes.NewReader(planData))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executionbackend.Admit(contract, plan); err != nil {
		t.Fatal(err)
	}
	receiptData, err := os.ReadFile("testdata/standalone-readability/trial-receipt-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := DecodeExecutionBackendTrialReceipt(bytes.NewReader(receiptData), plan)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Verdict != executionbackend.VerdictSucceeded || receipt.ContractSHA256 != plan.ContractSHA256 {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func hermeticReferenceFixture(t *testing.T, kind executionbackend.ProgramKind) (ExecutionBackendContract, ExecutionBackendTrialPlan, ExecutionBackendReferenceInputs) {
	t.Helper()
	fixture := hermeticArchive(t, "input.txt", []byte("fixture"))
	skill := hermeticArchive(t, "SKILL.md", []byte("skill"))
	definitions := hermeticArchive(t, "task.json", []byte("definition"))
	fixtureSHA, _ := executionbackend.ArchiveSHA256(fixture, executionbackend.MaxArchiveBytes, executionbackend.MaxSnapshotEntries)
	skillSHA, _ := executionbackend.ArchiveSHA256(skill, executionbackend.MaxArchiveBytes, executionbackend.MaxSnapshotEntries)
	definitionsSHA, _ := executionbackend.ArchiveSHA256(definitions, executionbackend.MaxArchiveBytes, executionbackend.MaxSnapshotEntries)
	contract, err := HermeticReferenceExecutionBackendContract()
	if err != nil {
		t.Fatal(err)
	}
	options := executionbackend.ReferencePlanOptions{FixtureSHA256: fixtureSHA, SkillSHA256: skillSHA, DefinitionsSHA256: definitionsSHA,
		Resources: executionbackend.ResourcePolicy{DeadlineMillis: 5000, MaxInputBytes: executionbackend.MaxArchiveBytes,
			MaxOutputBytes: executionbackend.MaxArtifactBytes, MaxEntries: executionbackend.MaxSnapshotEntries, MaxArtifacts: 1, MaxOperations: 1},
		Artifacts: []executionbackend.ArtifactDeclaration{{ID: "result", MaxBytes: 1024, Privacy: executionbackend.PrivacyPublic}},
		Program:   executionbackend.Program{Kind: kind, SourceMount: executionbackend.MountFixture, SourcePath: "input.txt", ArtifactID: "result"},
		Verifier:  executionbackend.Verifier{Kind: executionbackend.VerifierSHA256Equals, ArtifactID: "result", ExpectedSHA256: sha256HexBytes([]byte("fixture"))}}
	if kind == executionbackend.ProgramWaitForCancel {
		options.Artifacts = []executionbackend.ArtifactDeclaration{}
		options.Program = executionbackend.Program{Kind: kind}
		options.Verifier = executionbackend.Verifier{Kind: executionbackend.VerifierProfileDecision}
		options.Resources.MaxArtifacts = 0
	}
	plan, err := NewHermeticReferenceTrialPlan(contract, options)
	if err != nil {
		t.Fatal(err)
	}
	return contract, plan, executionbackend.ReferenceInputs{Fixture: fixture, Skill: skill, Definitions: definitions}
}

func hermeticReferenceSession(t *testing.T, plan ExecutionBackendTrialPlan) *DurableAttemptSession {
	t.Helper()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := CreateAttemptLedgerStore(parent+"/ledger", nil)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	binding := lifecycle.Binding{Privacy: lifecycle.PrivacyContentMinimized, Identity: lifecycle.Identity{
		ExperimentSHA256: digest, TaskSHA256: digest, SkillSHA256: digest, AgentSHA256: digest, ModelSHA256: digest,
		EnvironmentSHA256: digest, GraderSHA256: digest, BudgetsSHA256: digest, AdapterSHA256: digest, AuthoritySHA256: digest}}
	binding, err = BindExecutionBackendTrial(binding, plan)
	if err != nil {
		t.Fatal(err)
	}
	durablePlan, err := store.Allocate(binding)
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewDurableAttemptSession(store, durablePlan)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func hermeticArchive(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	header := &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o444, Size: int64(len(data)), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
	if err := writer.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
