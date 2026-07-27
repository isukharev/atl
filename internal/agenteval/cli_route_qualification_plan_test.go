package agenteval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// privatePlanCLIRouteObservation records how a lifecycle phase called the
// backend-free qualifier without executing a real provider binary.
type privatePlanCLIRouteObservation struct {
	agentBinaries []string
	options       []CLIRouteQualificationOptions
}

func stubPrivatePlanCLIRouteQualifierWith(t *testing.T,
	build func(CLIRouteQualificationOptions, privateAgentBinaryContract) (CLIRouteQualificationReport, error),
) *privatePlanCLIRouteObservation {
	t.Helper()
	original := privatePlanQualifyCLIRoute
	observation := &privatePlanCLIRouteObservation{}
	privatePlanQualifyCLIRoute = func(_ context.Context, options CLIRouteQualificationOptions) (CLIRouteQualificationReport, error) {
		observation.agentBinaries = append(observation.agentBinaries, options.AgentBinary)
		observation.options = append(observation.options, options)
		agent, _, err := inspectPrivateAgentBinary(options.AgentBinary, "")
		if err != nil {
			return CLIRouteQualificationReport{}, err
		}
		return build(options, agent)
	}
	t.Cleanup(func() { privatePlanQualifyCLIRoute = original })
	return observation
}

func supportedPrivatePlanCLIRouteReport(route string) func(CLIRouteQualificationOptions, privateAgentBinaryContract) (CLIRouteQualificationReport, error) {
	return func(options CLIRouteQualificationOptions, agent privateAgentBinaryContract) (CLIRouteQualificationReport, error) {
		return CLIRouteQualificationReport{
			SchemaVersion: CLIRouteQualificationSchemaVersion, Provider: options.Provider, Surface: options.Surface,
			AgentIdentity: agent.identity, ContractSHA256: cliRouteQualificationContractSHA256(agent.identity, options),
			Status: CLIRouteQualificationSupported, Route: route, RequestObserved: true, SyntheticRequests: 1,
		}, nil
	}
}

func TestSameCLIRouteQualificationReportIgnoresOnlyAuxiliaryTiming(t *testing.T) {
	base := CLIRouteQualificationReport{
		SchemaVersion: CLIRouteQualificationSchemaVersion, Provider: "claude-code", Surface: SurfaceCLISkill,
		AgentIdentity: "binary-sha256:" + strings.Repeat("a", 64), ContractSHA256: strings.Repeat("b", 64),
		Status: CLIRouteQualificationSupported, Route: "bash", RequestObserved: true, SyntheticRequests: 1,
	}
	withAuxiliary := base
	withAuxiliary.AuxiliaryRequests = 1
	if !sameCLIRouteQualificationReport(base, withAuxiliary) {
		t.Fatal("incidental connectivity HEAD was treated as route drift")
	}
	withAuthority := base
	withAuthority.ProviderRequests = 1
	if sameCLIRouteQualificationReport(base, withAuthority) {
		t.Fatal("provider authority drift was ignored")
	}
}

// countPrivatePlanExecutionSideEffects makes provider authentication,
// calibration, and benchmark execution observable so a refusal can be proven to
// happen before any of them.
func countPrivatePlanExecutionSideEffects(t *testing.T) (*int, *int, *int) {
	t.Helper()
	authCalls, calibrationCalls, runCalls := 0, 0, 0
	originalAuth, originalCalibration, originalRun := privatePlanNewCodexAuth, privatePlanRunCalibration, privatePlanRunHeadless
	privatePlanNewCodexAuth = func(ambient []string) (*codexAuthSession, error) {
		authCalls++
		return originalAuth(ambient)
	}
	privatePlanRunCalibration = func(ctx context.Context, options CodexCLICalibrationOptions) (CodexCLICalibrationReceipt, error) {
		calibrationCalls++
		return originalCalibration(ctx, options)
	}
	privatePlanRunHeadless = func(ctx context.Context, options RunOptions) (RunOutput, error) {
		runCalls++
		return originalRun(ctx, options)
	}
	t.Cleanup(func() {
		privatePlanNewCodexAuth, privatePlanRunCalibration, privatePlanRunHeadless = originalAuth, originalCalibration, originalRun
	})
	return &authCalls, &calibrationCalls, &runCalls
}

func writePrivatePlanForTest(t *testing.T, root string, plan privatePlan) []byte {
	t.Helper()
	data, err := encodePrivatePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plans", plan.PlanID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCreatePrivatePlanBindsOneCLIRouteQualificationPerPhase(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, true, false)
	observation := stubPrivatePlanCLIRouteQualifierWith(t, supportedPrivatePlanCLIRouteReport("exec_command"))
	authCalls, calibrationCalls, runCalls := countPrivatePlanExecutionSideEffects(t)

	preview := fixture.createPlan(t)
	if len(observation.agentBinaries) != 1 {
		t.Fatalf("create ran the qualifier %d times", len(observation.agentBinaries))
	}
	reviewedAgent, _, err := inspectPrivateAgentBinary(fixture.agent, "")
	if err != nil {
		t.Fatal(err)
	}
	if observation.agentBinaries[0] != reviewedAgent.canonicalPath {
		t.Fatalf("create qualified %q instead of the reviewed agent", observation.agentBinaries[0])
	}
	if *authCalls != 0 || *calibrationCalls != 0 || *runCalls != 0 {
		t.Fatalf("plan creation reached the provider: auth=%d calibration=%d run=%d", *authCalls, *calibrationCalls, *runCalls)
	}

	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != PrivatePlanSchemaVersion {
		t.Fatalf("plan schema=%d want=%d", plan.SchemaVersion, PrivatePlanSchemaVersion)
	}
	report := plan.CLIRouteQualification
	if report == nil || !report.Supported() || report.Route != "exec_command" ||
		report.Provider != plan.Provider || report.Surface != SurfaceCLISkill ||
		report.AgentIdentity != reviewedAgent.identity {
		t.Fatalf("plan cli route qualification=%+v", report)
	}
	if report.ProviderRequests != 0 || report.BackendRequests != 0 || report.RemoteWrites != 0 || report.SyntheticRequests != 1 {
		t.Fatalf("qualification claimed provider or backend authority: %+v", report)
	}
	assertPrivatePlanTextSafe(t, report.ContractSHA256+report.AgentIdentity+report.Route, fixture)
	options := observation.options[0]
	if options.ScratchRoot == "" || options.Model == "" || options.Surface != SurfaceCLISkill {
		t.Fatalf("qualifier options=%+v", options)
	}

	// Execution re-proves the route and then consumes the plan. The benchmark
	// invocations that follow are exercised elsewhere; what matters here is that
	// the qualifier ran exactly once more, against the snapshot's own binary.
	if _, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview)); err != nil {
		assertPrivatePlanError(t, err, "execution")
	}
	if _, statErr := os.Stat(filepath.Join(fixture.root, "plans", preview.PlanID+".state.json")); statErr != nil {
		t.Fatalf("execution did not pass the route gate: %v", statErr)
	}
	if len(observation.agentBinaries) != 2 {
		t.Fatalf("execution ran the qualifier %d times in total", len(observation.agentBinaries))
	}
	snapshotRoot, err := filepath.EvalSymlinks(filepath.Join(fixture.root, ".ephemeral"))
	if err != nil {
		t.Fatal(err)
	}
	executionAgent := observation.agentBinaries[1]
	if !strings.HasPrefix(executionAgent, snapshotRoot+string(filepath.Separator)) || executionAgent == reviewedAgent.canonicalPath {
		t.Fatalf("execution qualified %q instead of the execution snapshot binary", executionAgent)
	}
	if observation.options[1].PluginSHA256 != options.PluginSHA256 ||
		observation.options[1].SettingsSHA256 != options.SettingsSHA256 ||
		observation.options[1].ResponseSchemaSHA256 != options.ResponseSchemaSHA256 {
		t.Fatalf("execution qualified different plugin/settings inputs: %+v", observation.options[1])
	}
}

func TestPrivateComparisonPlanWithoutACLIItemCarriesNoRouteQualification(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, false, false)
	observation := stubPrivatePlanCLIRouteQualifierWith(t, supportedPrivatePlanCLIRouteReport("exec_command"))
	preview := fixture.createPlan(t)
	if len(observation.agentBinaries) != 0 {
		t.Fatalf("an MCP-only comparison ran the route qualifier %d times", len(observation.agentBinaries))
	}
	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CLIRouteQualification != nil {
		t.Fatalf("an MCP-only comparison bound a route qualification: %+v", plan.CLIRouteQualification)
	}
	summary, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
	if err != nil || summary.Status != "completed" {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	if len(observation.agentBinaries) != 0 {
		t.Fatalf("an MCP-only execution ran the route qualifier %d times", len(observation.agentBinaries))
	}
}

func TestPrivateActivationStudyKeepsToolAvailabilityAndCarriesNoRouteQualification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executable scripts are Unix-only")
	}
	fixture := newPrivateActivationPlanFixture(t)
	observation := stubPrivatePlanCLIRouteQualifierWith(t, supportedPrivatePlanCLIRouteReport("exec_command"))
	preview := fixture.createPlan(t)
	if len(observation.agentBinaries) != 0 {
		t.Fatalf("an activation study ran the route qualifier %d times", len(observation.agentBinaries))
	}
	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != PrivateRunSetKindActivationStudy || plan.CLIRouteQualification != nil {
		t.Fatalf("activation plan kind=%q route=%+v", plan.Kind, plan.CLIRouteQualification)
	}
	if plan.ToolAvailability == nil || !plan.ToolAvailability.Supported() {
		t.Fatalf("activation plan lost its tool-availability result: %+v", plan.ToolAvailability)
	}
	candidate := plan
	report := CLIRouteQualificationReport{SchemaVersion: CLIRouteQualificationSchemaVersion, Provider: plan.Provider,
		Surface: SurfaceCLISkill, AgentIdentity: "binary-sha256:" + strings.Repeat("a", 64),
		ContractSHA256: strings.Repeat("b", 64), Status: CLIRouteQualificationSupported, Route: "exec_command",
		RequestObserved: true, SyntheticRequests: 1}
	candidate.CLIRouteQualification = &report
	if err := validatePrivatePlan(candidate, candidate.PlanID); err == nil {
		t.Fatal("an activation study accepted a cli route qualification")
	}
}

func TestCreatePrivatePlanRefusesUnqualifiedCLIRoutes(t *testing.T) {
	tests := []struct {
		name  string
		code  string
		build func(CLIRouteQualificationOptions, privateAgentBinaryContract) (CLIRouteQualificationReport, error)
	}{
		{name: "missing route", code: "cli_route_route_inventory_missing", build: func(options CLIRouteQualificationOptions, agent privateAgentBinaryContract) (CLIRouteQualificationReport, error) {
			return CLIRouteQualificationReport{SchemaVersion: CLIRouteQualificationSchemaVersion, Provider: options.Provider,
				Surface: options.Surface, AgentIdentity: agent.identity, ContractSHA256: cliRouteQualificationContractSHA256(agent.identity, options),
				Status: CLIRouteQualificationRouteMissing, RequestObserved: true, SyntheticRequests: 1}, nil
		}},
		{name: "ambiguous route", code: "cli_route_route_inventory_ambiguous", build: func(options CLIRouteQualificationOptions, agent privateAgentBinaryContract) (CLIRouteQualificationReport, error) {
			return CLIRouteQualificationReport{SchemaVersion: CLIRouteQualificationSchemaVersion, Provider: options.Provider,
				Surface: options.Surface, AgentIdentity: agent.identity, ContractSHA256: cliRouteQualificationContractSHA256(agent.identity, options),
				Status: CLIRouteQualificationAmbiguous, RequestObserved: true, SyntheticRequests: 1}, nil
		}},
		{name: "process failure", code: "cli_route_process_failed", build: func(options CLIRouteQualificationOptions, agent privateAgentBinaryContract) (CLIRouteQualificationReport, error) {
			return CLIRouteQualificationReport{SchemaVersion: CLIRouteQualificationSchemaVersion, Provider: options.Provider,
				Surface: options.Surface, AgentIdentity: agent.identity, ContractSHA256: cliRouteQualificationContractSHA256(agent.identity, options),
				Status: CLIRouteQualificationProcessFailed}, nil
		}},
		{name: "unbound contract", code: "cli_route_report", build: func(options CLIRouteQualificationOptions, agent privateAgentBinaryContract) (CLIRouteQualificationReport, error) {
			return CLIRouteQualificationReport{SchemaVersion: CLIRouteQualificationSchemaVersion, Provider: options.Provider,
				Surface: options.Surface, AgentIdentity: agent.identity, ContractSHA256: strings.Repeat("f", 64),
				Status: CLIRouteQualificationSupported, Route: "exec_command", RequestObserved: true, SyntheticRequests: 1}, nil
		}},
		{name: "cross provider route", code: "cli_route_report", build: func(options CLIRouteQualificationOptions, agent privateAgentBinaryContract) (CLIRouteQualificationReport, error) {
			return CLIRouteQualificationReport{SchemaVersion: CLIRouteQualificationSchemaVersion, Provider: options.Provider,
				Surface: options.Surface, AgentIdentity: agent.identity, ContractSHA256: cliRouteQualificationContractSHA256(agent.identity, options),
				Status: CLIRouteQualificationSupported, Route: "bash", RequestObserved: true, SyntheticRequests: 1}, nil
		}},
		{name: "qualifier failure", code: "cli_route_execution", build: func(CLIRouteQualificationOptions, privateAgentBinaryContract) (CLIRouteQualificationReport, error) {
			return CLIRouteQualificationReport{}, errors.New("probe failed")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrivatePlanTestFixture(t, true, false)
			stubPrivatePlanCLIRouteQualifierWith(t, test.build)
			authCalls, calibrationCalls, runCalls := countPrivatePlanExecutionSideEffects(t)
			_, err := CreatePrivatePlan(context.Background(), fixture.createOptions())
			assertPrivatePlanError(t, err, test.code)
			assertPrivatePlanTextSafe(t, err.Error(), fixture)
			if entries, readErr := os.ReadDir(filepath.Join(fixture.root, "plans")); readErr != nil || len(entries) != 0 {
				t.Fatalf("an unqualified route persisted a plan: entries=%d err=%v", len(entries), readErr)
			}
			if *authCalls != 0 || *calibrationCalls != 0 || *runCalls != 0 {
				t.Fatalf("an unqualified route reached the provider: auth=%d calibration=%d run=%d", *authCalls, *calibrationCalls, *runCalls)
			}
			assertPrivatePlanNoRuntimeInvocation(t, fixture)
		})
	}
}

func TestExecutePrivatePlanRefusesCLIRouteDriftBeforeAnyProviderWork(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, true, false)
	stubPrivatePlanCLIRouteQualifierWith(t, supportedPrivatePlanCLIRouteReport("exec_command"))
	preview := fixture.createPlan(t)

	for _, test := range []struct {
		name  string
		code  string
		build func(CLIRouteQualificationOptions, privateAgentBinaryContract) (CLIRouteQualificationReport, error)
	}{
		{name: "different route", code: "cli_route_drift", build: supportedPrivatePlanCLIRouteReport("shell_command")},
		{name: "unavailable route", code: "cli_route_route_inventory_missing", build: func(options CLIRouteQualificationOptions, agent privateAgentBinaryContract) (CLIRouteQualificationReport, error) {
			return CLIRouteQualificationReport{SchemaVersion: CLIRouteQualificationSchemaVersion, Provider: options.Provider,
				Surface: options.Surface, AgentIdentity: agent.identity, ContractSHA256: cliRouteQualificationContractSHA256(agent.identity, options),
				Status: CLIRouteQualificationRouteMissing, RequestObserved: true, SyntheticRequests: 1}, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			observation := stubPrivatePlanCLIRouteQualifierWith(t, test.build)
			authCalls, calibrationCalls, runCalls := countPrivatePlanExecutionSideEffects(t)
			_, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview))
			assertPrivatePlanError(t, err, test.code)
			assertPrivatePlanTextSafe(t, err.Error(), fixture)
			if len(observation.agentBinaries) != 1 {
				t.Fatalf("execution ran the qualifier %d times", len(observation.agentBinaries))
			}
			if *authCalls != 0 || *calibrationCalls != 0 || *runCalls != 0 {
				t.Fatalf("route drift reached the provider: auth=%d calibration=%d run=%d", *authCalls, *calibrationCalls, *runCalls)
			}
			assertPrivatePlanNoRuntimeInvocation(t, fixture)
			if _, statErr := os.Stat(filepath.Join(fixture.root, "plans", preview.PlanID+".state.json")); !os.IsNotExist(statErr) {
				t.Fatalf("route drift consumed the plan: %v", statErr)
			}
			if runs, readErr := os.ReadDir(filepath.Join(fixture.root, "runs")); readErr != nil || len(runs) != 0 {
				t.Fatalf("route drift created a run: entries=%d err=%v", len(runs), readErr)
			}
			if !inspectPrivateWorkspaceScratch(fixture.root) {
				t.Fatal("route drift retained an execution snapshot")
			}
		})
	}
}

func TestPrivatePlanCLIRouteQualificationShapeIsSchemaBound(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, true, false)
	stubPrivatePlanCLIRouteQualifierWith(t, supportedPrivatePlanCLIRouteReport("exec_command"))
	preview := fixture.createPlan(t)
	current, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CLIRouteQualification == nil {
		t.Fatal("current comparison plan carries no route qualification")
	}

	for name, mutate := range map[string]func(*privatePlan){
		"absent report": func(plan *privatePlan) { plan.CLIRouteQualification = nil },
		"unsupported report": func(plan *privatePlan) {
			report := *plan.CLIRouteQualification
			report.Status, report.Route = CLIRouteQualificationRouteMissing, ""
			plan.CLIRouteQualification = &report
		},
		"invalid report": func(plan *privatePlan) {
			report := *plan.CLIRouteQualification
			report.SyntheticRequests = 2
			plan.CLIRouteQualification = &report
		},
		"foreign provider": func(plan *privatePlan) {
			report := *plan.CLIRouteQualification
			report.Provider, report.Route = "claude-code", "bash"
			plan.CLIRouteQualification = &report
		},
		"foreign surface": func(plan *privatePlan) {
			report := *plan.CLIRouteQualification
			report.Surface = SurfaceATLMCP
			plan.CLIRouteQualification = &report
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := current
			mutate(&candidate)
			if err := validatePrivatePlan(candidate, candidate.PlanID); err == nil {
				t.Fatalf("current comparison plan accepted %s", name)
			}
		})
	}

	t.Run("legacy schema cannot express the report", func(t *testing.T) {
		candidate := current
		candidate.SchemaVersion = LegacyLiveWritePrivatePlanSchemaVersion
		if err := validatePrivatePlan(candidate, candidate.PlanID); err == nil {
			t.Fatal("a schema-v8 plan accepted a cli route qualification")
		}
		candidate.CLIRouteQualification = nil
		if err := validatePrivatePlan(candidate, candidate.PlanID); err != nil {
			t.Fatalf("a schema-v8 comparison plan became unreadable: %v", err)
		}
	})

	t.Run("legacy schema stays executable-review readable", func(t *testing.T) {
		candidate := current
		candidate.SchemaVersion = LegacyExecutableReviewPrivatePlanSchemaVersion
		candidate.CLIRouteQualification = nil
		if err := validatePrivatePlan(candidate, candidate.PlanID); err != nil {
			t.Fatalf("a schema-v7 comparison plan became unreadable: %v", err)
		}
	})
}

func TestLegacyLiveWritePrivatePlanV8RemainsReadableButNotExecutable(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, true, false)
	stubPrivatePlanCLIRouteQualifierWith(t, supportedPrivatePlanCLIRouteReport("exec_command"))
	preview := fixture.createPlan(t)
	plan, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	plan.SchemaVersion = LegacyLiveWritePrivatePlanSchemaVersion
	plan.CLIRouteQualification = nil
	// A schema-v8 plan keeps exactly the fields it could express when it was
	// written, including bounded query-only transport.
	for index := range plan.Items {
		if !plan.Items[index].LiveWrites {
			plan.Items[index].MaxQueryOnlyRequests = 2
			break
		}
	}
	data := writePrivatePlanForTest(t, fixture.root, plan)
	loaded, _, err := loadPrivatePlan(fixture.root, preview.PlanID)
	if err != nil || loaded.SchemaVersion != LegacyLiveWritePrivatePlanSchemaVersion || loaded.CLIRouteQualification != nil {
		t.Fatalf("legacy live-write plan=%+v err=%v", loaded, err)
	}
	if privatePlanQueryOnlyAuthority(loaded.Items) != 2 {
		t.Fatalf("legacy query-only transport was not preserved: %+v", loaded.Items)
	}

	legacy := preview
	legacy.PlanSHA256 = sha256HexBytes(data)
	authCalls, calibrationCalls, runCalls := countPrivatePlanExecutionSideEffects(t)
	_, err = ExecutePrivatePlan(context.Background(), fixture.executeOptions(legacy))
	assertPrivatePlanError(t, err, "legacy_plan_read_only")
	if *authCalls != 0 || *calibrationCalls != 0 || *runCalls != 0 {
		t.Fatalf("a legacy plan reached the provider: auth=%d calibration=%d run=%d", *authCalls, *calibrationCalls, *runCalls)
	}
	assertPrivatePlanNoRuntimeInvocation(t, fixture)

	// The predecessor schema still cannot express query-only transport.
	older := loaded
	older.SchemaVersion = LegacyExecutableReviewPrivatePlanSchemaVersion
	if err := validatePrivatePlan(older, older.PlanID); err == nil {
		t.Fatal("a schema-v7 plan accepted query-only transport")
	}
}
