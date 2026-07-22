package agenteval

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildPrivateActivationStudyContractBindsExactlyFourTreatments(t *testing.T) {
	specs := privateActivationStudyTestSpecs()
	// Input ordering is not part of the canonical treatment contract.
	specs[0], specs[3] = specs[3], specs[0]
	contract, err := BuildPrivateActivationStudyContract(specs)
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.Validate(); err != nil {
		t.Fatal(err)
	}
	if contract.SchemaVersion != PrivateActivationStudyContractSchemaVersion || contract.Provider != "codex" ||
		contract.Surface != SurfaceCLISkill || len(contract.Cells) != 4 || !validSHA256(contract.CommonContractSHA256) {
		t.Fatalf("contract=%+v", contract)
	}
	for index, activation := range PrivateActivationStudyTreatments() {
		treatment := contract.Cells[index]
		if treatment.Cell != (PrivateActivationCellIdentity{Surface: SurfaceCLISkill, SkillActivation: activation}) ||
			treatment.Variant != "activation-"+activation || !validSHA256(treatment.RunSpecSHA256) {
			t.Fatalf("treatment %d=%+v", index, treatment)
		}
		lookedUp, ok := contract.Treatment(activation)
		if !ok || lookedUp != treatment {
			t.Fatalf("lookup %q=%+v ok=%v", activation, lookedUp, ok)
		}
	}
	if _, ok := contract.Treatment("automatic"); ok {
		t.Fatal("unknown treatment lookup succeeded")
	}

	normalized := specs[0]
	normalized.SkillActivation = ""
	normalized.Variant = ""
	normalizedJSON, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if contract.CommonContractSHA256 != sha256HexBytes(normalizedJSON) {
		t.Fatalf("common digest=%q", contract.CommonContractSHA256)
	}
	for _, spec := range specs {
		exactJSON, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		index := privateActivationTreatmentIndex(spec.SkillActivation)
		if contract.Cells[index].RunSpecSHA256 != sha256HexBytes(exactJSON) {
			t.Fatalf("treatment digest for %q does not bind exact spec", spec.SkillActivation)
		}
	}

	reordered := []RunSpec{specs[1], specs[3], specs[0], specs[2]}
	second, err := BuildPrivateActivationStudyContract(reordered)
	if err != nil || !reflect.DeepEqual(contract, second) {
		t.Fatalf("reordered contract=%+v err=%v", second, err)
	}
}

func TestBuildPrivateActivationStudyContractFailsClosed(t *testing.T) {
	base := privateActivationStudyTestSpecs()
	tests := map[string]func([]RunSpec) []RunSpec{
		"three runs": func(specs []RunSpec) []RunSpec { return specs[:3] },
		"five runs":  func(specs []RunSpec) []RunSpec { return append(specs, specs[0]) },
		"duplicate treatment": func(specs []RunSpec) []RunSpec {
			specs[3].SkillActivation = SkillActivationImplicit
			return specs
		},
		"duplicate variant": func(specs []RunSpec) []RunSpec {
			specs[3].Variant = specs[0].Variant
			return specs
		},
		"old schema": func(specs []RunSpec) []RunSpec {
			specs[2].SchemaVersion--
			return specs
		},
		"repeated cell": func(specs []RunSpec) []RunSpec {
			specs[2].Repetitions = 2
			return specs
		},
		"claude": func(specs []RunSpec) []RunSpec {
			specs[2].Provider = "claude-code"
			specs[2].Pricing = Pricing{}
			return specs
		},
		"synthetic": func(specs []RunSpec) []RunSpec {
			specs[2].BackendMode = BackendModeSynthetic
			specs[2].FixtureFile = "fixture.json"
			return specs
		},
		"mcp surface": func(specs []RunSpec) []RunSpec {
			specs[2].Surface = SurfaceATLMCP
			specs[2].ToolTransport = "mcp"
			specs[2].SkillActivation = ""
			specs[2].AllowedTools = nil
			specs[2].AllowedCLICommands = nil
			specs[2].AllowedMCPTools = []string{"jira_epic_digest"}
			specs[2].AllowedGatewayRoutes = nil
			specs[2].GatewayMaxResponseBytes = 0
			specs[2].GatewayMaxTotalBytes = 0
			return specs
		},
		"model drift": func(specs []RunSpec) []RunSpec {
			specs[2].Model = "other-model"
			return specs
		},
		"prompt drift": func(specs []RunSpec) []RunSpec {
			specs[2].PromptFile = "other-prompt.md"
			return specs
		},
		"policy drift": func(specs []RunSpec) []RunSpec {
			specs[2].AllowedTools = append([]string(nil), specs[2].AllowedTools...)
			specs[2].AllowedTools = append(specs[2].AllowedTools, "Skill")
			return specs
		},
		"oracle drift": func(specs []RunSpec) []RunSpec {
			specs[2].Checks = append([]RunCheck(nil), specs[2].Checks...)
			specs[2].Checks[0].Expected = json.RawMessage(`"different"`)
			return specs
		},
		"capability drift": func(specs []RunSpec) []RunSpec {
			specs[2].DataCapabilities = []string{"jira.issue.fields"}
			return specs
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			specs := append([]RunSpec(nil), base...)
			if _, err := BuildPrivateActivationStudyContract(mutate(specs)); err == nil {
				t.Fatal("invalid activation study passed")
			}
		})
	}
}

func TestValidatePrivateActivationStudyLoadsOneCaseDirectory(t *testing.T) {
	directory, _, _, cli, _ := writePrivatePairFixture(t)
	writeTestFile(t, filepath.Join(directory, "response.json"), `{"type":"object","properties":{"complete":{"type":"boolean"},"evidence_outcome":{"type":"object","properties":{"state":{"type":"string","enum":["none","unavailable","blocked","failed","partial","succeeded"]}},"required":["state"],"additionalProperties":false}},"required":["complete","evidence_outcome"],"additionalProperties":false}`, 0o600)
	scenarioData, err := os.ReadFile(filepath.Join(directory, "scenario.json"))
	if err != nil {
		t.Fatal(err)
	}
	scenario, err := DecodeScenario(bytes.NewReader(scenarioData))
	if err != nil {
		t.Fatal(err)
	}
	scenario.Category = BenchmarkCategoryNeutralCommon
	scenario.Budgets.MaxInterfaceInvocations = scenario.Budgets.MaxATLInvocations
	scenario.RequiredMetrics = replacePrivatePlanTestString(scenario.RequiredMetrics, "atl_invocations", "interface_invocations")
	writeJSONTestFile(t, filepath.Join(directory, "scenario.json"), scenario)

	cli.Category = BenchmarkCategoryNeutralCommon
	cli.DataCapabilities = []string{"jira.fields"}
	cli.Checks = append([]RunCheck(nil), cli.Checks...)
	for index := range cli.Checks {
		switch cli.Checks[index].Kind {
		case "atl_all_succeeded":
			cli.Checks[index].Kind = "interface_all_succeeded"
		case "atl_invocations_min":
			cli.Checks[index].Kind = "interface_invocations_min"
		}
	}
	paths := make([]string, 0, 4)
	for _, treatment := range PrivateActivationStudyTreatments() {
		spec := cli
		spec.SkillActivation = treatment
		spec.Variant = "activation-" + treatment
		path := filepath.Join(directory, spec.Variant+".json")
		writeJSONTestFile(t, path, spec)
		paths = append(paths, path)
	}
	contract, err := ValidatePrivateActivationStudy(paths...)
	if err != nil || contract.CommonContractSHA256 == "" || len(contract.Cells) != 4 {
		t.Fatalf("contract=%+v err=%v", contract, err)
	}
	writeTestFile(t, filepath.Join(directory, "response.json"), `{"type":"object","properties":{"complete":{"type":"boolean"}},"required":["complete"],"additionalProperties":false}`, 0o600)
	if _, err := ValidatePrivateActivationStudy(paths...); err == nil {
		t.Fatal("activation study without bounded evidence outcome passed")
	}
	writeTestFile(t, filepath.Join(directory, "response.json"), `{"type":"object","properties":{"complete":{"type":"boolean"},"evidence_outcome":{"type":"object","properties":{"state":{"type":"string","enum":["none","unavailable","blocked","failed","partial","succeeded"]}},"required":["state"],"additionalProperties":false}},"required":["complete","evidence_outcome"],"additionalProperties":false}`, 0o600)
	for _, path := range paths {
		var spec RunSpec
		data, readErr := os.ReadFile(path)
		if readErr != nil || json.Unmarshal(data, &spec) != nil {
			t.Fatalf("read activation spec %s: %v", filepath.Base(path), readErr)
		}
		spec.SchemaVersion = LegacyRunSpecSchemaVersion
		writeJSONTestFile(t, path, spec)
	}
	if _, err := validateReadablePrivateActivationStudy(paths...); err != nil {
		t.Fatalf("read-compatible legacy activation study: %v", err)
	}
	if _, err := ValidatePrivateActivationStudy(paths...); err == nil || !strings.Contains(err.Error(), "legacy run spec") {
		t.Fatalf("current activation planning accepted legacy specs: %v", err)
	}

	otherDirectory := filepath.Join(t.TempDir(), "case")
	if err := copyWorkspace(directory, otherDirectory); err != nil {
		t.Fatal(err)
	}
	mixed := append([]string(nil), paths...)
	mixed[3] = filepath.Join(otherDirectory, filepath.Base(paths[3]))
	if _, err := ValidatePrivateActivationStudy(mixed...); err == nil {
		t.Fatal("activation study spanning case directories passed")
	}
}

func TestPrivateActivationStudyOrdersAreCanonicalAndBalanced(t *testing.T) {
	orders := CanonicalPrivateActivationStudyOrders()
	want := [][]string{
		{SkillActivationImplicit, SkillActivationExplicit, SkillActivationCombined, SkillActivationDeveloper},
		{SkillActivationExplicit, SkillActivationDeveloper, SkillActivationImplicit, SkillActivationCombined},
		{SkillActivationDeveloper, SkillActivationCombined, SkillActivationExplicit, SkillActivationImplicit},
		{SkillActivationCombined, SkillActivationImplicit, SkillActivationDeveloper, SkillActivationExplicit},
	}
	if len(orders) != len(want) {
		t.Fatalf("orders=%v", orders)
	}
	positions := map[string]map[int]int{}
	transitions := map[string]int{}
	for row, order := range orders {
		if len(order) != 4 {
			t.Fatalf("order %d=%v", row, order)
		}
		seen := map[string]bool{}
		for column, cell := range order {
			if err := cell.Validate(); err != nil || cell.SkillActivation != want[row][column] || seen[cell.SkillActivation] {
				t.Fatalf("order %d cell %d=%+v err=%v", row, column, cell, err)
			}
			seen[cell.SkillActivation] = true
			if positions[cell.SkillActivation] == nil {
				positions[cell.SkillActivation] = map[int]int{}
			}
			positions[cell.SkillActivation][column]++
			if column > 0 {
				key := order[column-1].SkillActivation + "\x00" + cell.SkillActivation
				transitions[key]++
			}
		}
	}
	for _, treatment := range PrivateActivationStudyTreatments() {
		for position := 0; position < 4; position++ {
			if positions[treatment][position] != 1 {
				t.Fatalf("treatment %q position %d count=%d", treatment, position, positions[treatment][position])
			}
		}
		for _, next := range PrivateActivationStudyTreatments() {
			if treatment != next && transitions[treatment+"\x00"+next] != 1 {
				t.Fatalf("transition %q -> %q count=%d", treatment, next, transitions[treatment+"\x00"+next])
			}
		}
	}
	for completed := 0; completed < 8; completed++ {
		order, err := PrivateActivationStudyOrder(completed)
		if err != nil || !reflect.DeepEqual(order, orders[completed%4]) {
			t.Fatalf("completed=%d order=%v err=%v", completed, order, err)
		}
		if got := ActivationStudyOrder(completed); !reflect.DeepEqual(got, want[completed%4]) {
			t.Fatalf("completed=%d treatment order=%v", completed, got)
		}
	}
	if _, err := PrivateActivationStudyOrder(-1); err == nil {
		t.Fatal("negative completed block count passed")
	}
	if ActivationStudyOrder(-1) != nil {
		t.Fatal("negative activation study attempt returned an order")
	}

	orders[0][0].SkillActivation = "mutated"
	fresh := CanonicalPrivateActivationStudyOrders()
	if fresh[0][0].SkillActivation != SkillActivationImplicit {
		t.Fatal("caller mutated canonical order state")
	}
}

func TestPrivateActivationStudyContractAndCellValidationFailClosed(t *testing.T) {
	contract, err := BuildPrivateActivationStudyContract(privateActivationStudyTestSpecs())
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*PrivateActivationStudyContract){
		"schema":        func(c *PrivateActivationStudyContract) { c.SchemaVersion++ },
		"provider":      func(c *PrivateActivationStudyContract) { c.Provider = "claude-code" },
		"surface":       func(c *PrivateActivationStudyContract) { c.Surface = SurfaceATLMCP },
		"common digest": func(c *PrivateActivationStudyContract) { c.CommonContractSHA256 = "" },
		"order": func(c *PrivateActivationStudyContract) {
			c.Cells[0], c.Cells[1] = c.Cells[1], c.Cells[0]
		},
		"duplicate digest":  func(c *PrivateActivationStudyContract) { c.Cells[1].RunSpecSHA256 = c.Cells[0].RunSpecSHA256 },
		"duplicate variant": func(c *PrivateActivationStudyContract) { c.Cells[1].Variant = c.Cells[0].Variant },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := contract
			candidate.Cells = append([]PrivateActivationTreatmentContract(nil), contract.Cells...)
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid contract passed")
			}
		})
	}
	for _, cell := range []PrivateActivationCellIdentity{
		{},
		{Surface: SurfaceATLMCP, SkillActivation: SkillActivationImplicit},
		{Surface: SurfaceCLISkill, SkillActivation: "automatic"},
	} {
		if err := cell.Validate(); err == nil {
			t.Fatalf("invalid cell passed: %+v", cell)
		}
	}
}

func privateActivationStudyTestSpecs() []RunSpec {
	treatments := PrivateActivationStudyTreatments()
	specs := make([]RunSpec, 0, len(treatments))
	for _, treatment := range treatments {
		spec := validRunSpec()
		spec.BackendMode = BackendModePrivateLive
		spec.Category = BenchmarkCategoryNeutralCommon
		spec.Surface = SurfaceCLISkill
		spec.FixtureFile = ""
		spec.Repetitions = 1
		spec.ToolTransport = "cli"
		spec.SkillActivation = treatment
		spec.Variant = "activation-" + treatment
		spec.AllowedTools = []string{"Bash(atl *)", "Read"}
		spec.AllowedATLCommands = nil
		spec.AllowedCLICommands = validCLICommandPolicy().Rules
		spec.DataCapabilities = []string{"jira.epic.digest"}
		spec.AllowedGatewayRoutes = map[string][]LiveGatewayRoute{
			"jira": {{Name: "jira_api", PathPrefix: "/rest/api/2"}},
		}
		spec.GatewayMaxResponseBytes = 1 << 20
		spec.GatewayMaxTotalBytes = 4 << 20
		specs = append(specs, spec)
	}
	return specs
}
