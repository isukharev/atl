package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	standaloneProductContractFixturePath = "testdata/standalone-product-contract.v1.json"
	standaloneConformanceFixturePath     = "testdata/standalone-conformance.v1.json"
)

type standaloneProductContractFixture struct {
	SchemaVersion        int                           `json:"schema_version"`
	ContractVersion      string                        `json:"contract_version"`
	Status               string                        `json:"status"`
	CompatibilityBegins  string                        `json:"compatibility_begins"`
	MaintainerCommands   []string                      `json:"maintainer_commands"`
	StandaloneOperations []standaloneOperation         `json:"standalone_operations"`
	Configuration        standaloneConfiguration       `json:"configuration"`
	Roles                []standaloneRole              `json:"roles"`
	CapabilityStates     []string                      `json:"capability_states"`
	MetricStates         []string                      `json:"metric_states"`
	ExitClasses          []standaloneExitClass         `json:"exit_classes"`
	ArtifactSchemas      []standaloneArtifactSchema    `json:"artifact_schemas"`
	ArtifactClasses      []standaloneArtifactClass     `json:"artifact_classes"`
	AttemptStates        []standaloneAttemptState      `json:"attempt_states"`
	CompatibilityPolicy  standaloneCompatibilityPolicy `json:"compatibility_policy"`
}

type standaloneOperation struct {
	ID               string `json:"id"`
	CurrentStatus    string `json:"current_status"`
	StandaloneStatus string `json:"standalone_status"`
	Authority        string `json:"authority"`
	ProviderContact  *bool  `json:"provider_contact"`
	BackendContact   *bool  `json:"backend_contact"`
	Network          *bool  `json:"network"`
}

type standaloneConfiguration struct {
	Precedence       []string `json:"precedence"`
	UnknownKeys      string   `json:"unknown_keys"`
	AmbientAuthority []string `json:"ambient_authority"`
}

type standaloneRole struct {
	ID         string   `json:"id"`
	Owns       []string `json:"owns"`
	DoesNotOwn []string `json:"does_not_own"`
}

type standaloneExitClass struct {
	Code *int   `json:"code"`
	ID   string `json:"id"`
}

type standaloneArtifactSchema struct {
	Namespace   string `json:"namespace"`
	Kind        string `json:"kind"`
	Current     int    `json:"current"`
	Readable    []int  `json:"readable"`
	Emitted     []int  `json:"emitted"`
	Executable  []int  `json:"executable"`
	Disposition string `json:"disposition"`
	Privacy     string `json:"privacy"`
	Migration   string `json:"migration"`
	MaxBytes    *int64 `json:"max_bytes"`
}

type standaloneArtifactClass struct {
	ID          string `json:"id"`
	Privacy     string `json:"privacy"`
	Disposition string `json:"disposition"`
}

type standaloneAttemptState struct {
	ID              string `json:"id"`
	Terminal        *bool  `json:"terminal"`
	AutomaticResume *bool  `json:"automatic_resume"`
}

type standaloneCompatibilityPolicy struct {
	StrictUnknownFields              *bool `json:"strict_unknown_fields"`
	AdditiveMembersRequireSchemaBump *bool `json:"additive_members_require_schema_bump"`
	SourceBytesPreserved             *bool `json:"source_bytes_preserved"`
	MigrationRequiresReviewedPreview *bool `json:"migration_requires_reviewed_preview"`
	MinimumDeprecationReleases       *int  `json:"minimum_deprecation_releases"`
	RootModuleLinkForbidden          *bool `json:"root_module_link_forbidden"`
}

type standaloneConformanceFixture struct {
	SchemaVersion    int                           `json:"schema_version"`
	Readability      []standaloneReadabilityVector `json:"readability"`
	ForwardRejection []standaloneForwardVector     `json:"forward_rejection"`
	MetricVectors    []standaloneMetricVector      `json:"metric_vectors"`
}

type standaloneReadabilityVector struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Versions  []int  `json:"versions"`
}

type standaloneForwardVector struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Version   int    `json:"version"`
}

type standaloneMetricVector struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Coverage *bool  `json:"coverage"`
	Value    *int64 `json:"value"`
	Valid    *bool  `json:"valid"`
}

type standaloneArtifactCompatibility struct {
	current    int
	readable   []int
	emitted    []int
	executable []int
}

func TestStandaloneProductContractV1IsClosedAndSelfConsistent(t *testing.T) {
	data := standaloneReadFixture(t, standaloneProductContractFixturePath)
	contract, err := decodeStandaloneProductContractFixture(data)
	if err != nil {
		t.Fatal(err)
	}

	mutations := map[string][]byte{
		"unknown top-level member": bytes.Replace(data, []byte("{"), []byte(`{"unknown":true,`), 1),
		"duplicate member":         bytes.Replace(data, []byte(`"schema_version": 1`), []byte(`"schema_version": 1, "schema_version": 1`), 1),
		"trailing value":           append(slices.Clone(data), []byte(` {}`)...),
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeStandaloneProductContractFixture(mutation); err == nil {
				t.Fatal("non-canonical standalone product contract passed")
			}
		})
	}

	wantRoles := []string{"agent-adapter", "atl-profile", "execution-backend", "grader", "reporter", "standalone-core"}
	gotRoles := make([]string, 0, len(contract.Roles))
	for _, role := range contract.Roles {
		gotRoles = append(gotRoles, role.ID)
	}
	if !slices.Equal(gotRoles, wantRoles) {
		t.Fatalf("standalone role ownership matrix=%v, want %v", gotRoles, wantRoles)
	}

	wantSchemas := map[string]standaloneArtifactCompatibility{
		"aggregate":                {current: AggregateSchemaVersion, emitted: []int{AggregateSchemaVersion}},
		"capability-catalog":       {current: CapabilityCatalogSchemaVersion, readable: []int{CapabilityCatalogSchemaVersion}, emitted: []int{CapabilityCatalogSchemaVersion}, executable: []int{CapabilityCatalogSchemaVersion}},
		"observation":              {current: ObservationSchemaVersion, readable: []int{ObservationSchemaVersion}, emitted: []int{ObservationSchemaVersion}, executable: []int{ObservationSchemaVersion}},
		"qualitative-panel":        {current: QualitativePanelSchemaVersion, readable: []int{QualitativePanelSchemaVersion}, emitted: []int{QualitativePanelSchemaVersion}, executable: []int{QualitativePanelSchemaVersion}},
		"result":                   {current: ResultSchemaVersion, readable: []int{LegacyResultSchemaVersion, PanelResultSchemaVersion, LegacyPromptBoundResultSchemaVersion, LegacyAttemptlessResultSchemaVersion, LegacyEvidenceResultSchemaVersion, ResultSchemaVersion}, emitted: []int{ResultSchemaVersion}, executable: []int{LegacyResultSchemaVersion, PanelResultSchemaVersion, LegacyPromptBoundResultSchemaVersion, LegacyAttemptlessResultSchemaVersion, LegacyEvidenceResultSchemaVersion, ResultSchemaVersion}},
		"review":                   {current: ReviewSchemaVersion, readable: []int{LegacyReviewSchemaVersion, ReviewSchemaVersion}, emitted: []int{ReviewSchemaVersion}, executable: []int{LegacyReviewSchemaVersion, ReviewSchemaVersion}},
		"rubric":                   {current: RubricSchemaVersion, readable: []int{RubricSchemaVersion}, executable: []int{RubricSchemaVersion}},
		"run-spec":                 {current: RunSpecSchemaVersion, readable: []int{LegacyPromptChannelRunSpecVersion, LegacyRunSpecSchemaVersion, RunSpecSchemaVersion}, emitted: []int{RunSpecSchemaVersion}, executable: []int{LegacyPromptChannelRunSpecVersion, LegacyRunSpecSchemaVersion, RunSpecSchemaVersion}},
		"scenario":                 {current: ScenarioSchemaVersion, readable: []int{ScenarioSchemaVersion}, emitted: []int{ScenarioSchemaVersion}, executable: []int{ScenarioSchemaVersion}},
		"synthetic-root-aggregate": {current: SyntheticRootAggregateSchemaVersion, emitted: []int{SyntheticRootAggregateSchemaVersion}},
		"synthetic-run-receipt":    {current: SyntheticRunReceiptSchemaVersion, readable: []int{SyntheticRunReceiptSchemaVersion}, emitted: []int{SyntheticRunReceiptSchemaVersion}, executable: []int{SyntheticRunReceiptSchemaVersion}},
	}
	for _, schema := range contract.ArtifactSchemas {
		if schema.Namespace != "atl-profile" {
			t.Fatalf("existing artifact %q has namespace %q", schema.Kind, schema.Namespace)
		}
		want, ok := wantSchemas[schema.Kind]
		if !ok {
			t.Fatalf("unclassified artifact schema %q", schema.Kind)
		}
		if schema.Current != want.current || !slices.Equal(schema.Readable, want.readable) ||
			!slices.Equal(schema.Emitted, want.emitted) || !slices.Equal(schema.Executable, want.executable) {
			t.Fatalf("artifact schema %q compatibility=%+v, want %+v", schema.Kind, schema, want)
		}
		delete(wantSchemas, schema.Kind)
	}
	if len(wantSchemas) != 0 {
		t.Fatalf("standalone contract omitted artifact schemas: %v", wantSchemas)
	}

	for _, state := range []string{"supported", "unknown", "unsupported"} {
		if !slices.Contains(contract.CapabilityStates, state) {
			t.Fatalf("capability state %q is absent", state)
		}
	}
	for _, state := range []string{"observed", "unknown", "unsupported"} {
		if !slices.Contains(contract.MetricStates, state) {
			t.Fatalf("metric state %q is absent", state)
		}
	}
	if len(contract.ExitClasses) < 2 || *contract.ExitClasses[0].Code != 0 || contract.ExitClasses[0].ID != "success" {
		t.Fatalf("exit class registry does not begin with success: %+v", contract.ExitClasses)
	}
	for _, exitClass := range contract.ExitClasses[1:] {
		if *exitClass.Code == 0 || exitClass.ID == "success" {
			t.Fatalf("non-success exit class aliases success: %+v", exitClass)
		}
	}
	policy := contract.CompatibilityPolicy
	if !standaloneTrue(policy.StrictUnknownFields) || !standaloneTrue(policy.AdditiveMembersRequireSchemaBump) ||
		!standaloneTrue(policy.SourceBytesPreserved) || !standaloneTrue(policy.MigrationRequiresReviewedPreview) ||
		!standaloneTrue(policy.RootModuleLinkForbidden) || policy.MinimumDeprecationReleases == nil || *policy.MinimumDeprecationReleases < 2 {
		t.Fatalf("standalone compatibility policy is incomplete: %+v", policy)
	}
}

func TestStandaloneContractClassifiesCurrentCommandsAndArtifacts(t *testing.T) {
	contract := loadStandaloneProductContractFixture(t)
	commands := standaloneCoordinatorCommands(t, filepath.Join("cmd", "agent-eval", "main.go"))
	if len(commands) != 14 || !slices.Equal(commands, contract.MaintainerCommands) {
		t.Fatalf("coordinator commands=%v, contract=%v", commands, contract.MaintainerCommands)
	}

	operationByID := make(map[string]standaloneOperation, len(contract.StandaloneOperations))
	for _, operation := range contract.StandaloneOperations {
		operationByID[operation.ID] = operation
	}
	for _, command := range []string{"run", "validate"} {
		operation, ok := operationByID[command]
		if !ok || operation.CurrentStatus != "maintainer_compat" || operation.StandaloneStatus != "reserved" {
			t.Fatalf("current command %q classification=%+v", command, operation)
		}
	}

	behavior := loadEvaluatorBehaviorContract(t)
	publicClasses := make(map[string]bool, len(behavior.Artifacts.PublicTrackedClasses))
	privateClasses := make(map[string]bool, len(behavior.Artifacts.PrivateOnlyClasses))
	wantClasses := make([]string, 0, len(behavior.Artifacts.PublicTrackedClasses)+len(behavior.Artifacts.PrivateOnlyClasses))
	for _, class := range behavior.Artifacts.PublicTrackedClasses {
		publicClasses[class.Name] = true
		wantClasses = append(wantClasses, class.Name)
	}
	for _, class := range behavior.Artifacts.PrivateOnlyClasses {
		privateClasses[class.Name] = true
		wantClasses = append(wantClasses, class.Name)
	}
	slices.Sort(wantClasses)
	gotClasses := make([]string, 0, len(contract.ArtifactClasses))
	for _, class := range contract.ArtifactClasses {
		gotClasses = append(gotClasses, class.ID)
		if publicClasses[class.ID] && class.Privacy == "owner_private" {
			t.Fatalf("public artifact class %q became owner-private", class.ID)
		}
		if privateClasses[class.ID] && class.Privacy != "owner_private" {
			t.Fatalf("private artifact class %q has privacy %q", class.ID, class.Privacy)
		}
	}
	if !slices.Equal(gotClasses, wantClasses) {
		t.Fatalf("artifact class inventory=%v, behavior contract=%v", gotClasses, wantClasses)
	}

	schemas := make(map[string]standaloneArtifactSchema, len(contract.ArtifactSchemas))
	for _, schema := range contract.ArtifactSchemas {
		schemas[schema.Kind] = schema
	}
	for _, behaviorSchema := range behavior.Schemas {
		kind := strings.ReplaceAll(behaviorSchema.Name, "_", "-")
		schema, ok := schemas[kind]
		if !ok || !slices.Equal(schema.Readable, behaviorSchema.Readable) ||
			!slices.Equal(schema.Emitted, behaviorSchema.Emitted) || !slices.Equal(schema.Executable, behaviorSchema.Executable) {
			t.Fatalf("behavior schema %q is not reconciled: behavior=%+v standalone=%+v", kind, behaviorSchema, schema)
		}
	}
}

func TestStandaloneContractCompatibilityVectors(t *testing.T) {
	contract := loadStandaloneProductContractFixture(t)
	conformance := loadStandaloneConformanceFixture(t)

	schemaByKey := make(map[string]standaloneArtifactSchema, len(contract.ArtifactSchemas))
	for _, schema := range contract.ArtifactSchemas {
		schemaByKey[standaloneContractKey(schema.Namespace, schema.Kind)] = schema
	}
	readabilityByKey := make(map[string][]int, len(conformance.Readability))
	for _, vector := range conformance.Readability {
		key := standaloneContractKey(vector.Namespace, vector.Kind)
		readabilityByKey[key] = vector.Versions
		schema, ok := schemaByKey[key]
		if !ok || !slices.Equal(vector.Versions, schema.Readable) {
			t.Fatalf("readability vector %q=%v, schema=%+v", key, vector.Versions, schema)
		}
		for _, version := range vector.Versions {
			if err := standaloneDecodeArtifactVersion(t, vector.Kind, version); err != nil {
				t.Fatalf("%s v%d declared readable but rejected: %v", vector.Kind, version, err)
			}
		}
	}
	for key, schema := range schemaByKey {
		if len(schema.Readable) == 0 {
			continue
		}
		if versions, ok := readabilityByKey[key]; !ok || !slices.Equal(versions, schema.Readable) {
			t.Fatalf("readable artifact %q has no exact conformance vector", key)
		}
	}

	forwardByKey := make(map[string]int, len(conformance.ForwardRejection))
	for _, vector := range conformance.ForwardRejection {
		key := standaloneContractKey(vector.Namespace, vector.Kind)
		if _, exists := forwardByKey[key]; exists {
			t.Fatalf("duplicate forward rejection vector %q", key)
		}
		forwardByKey[key] = vector.Version
		if vector.Namespace == "standalone" && vector.Kind == "product-contract" {
			data := standaloneReadFixture(t, standaloneProductContractFixturePath)
			future := bytes.Replace(data, []byte(`"schema_version": 1`), []byte(fmt.Sprintf(`"schema_version": %d`, vector.Version)), 1)
			if _, err := decodeStandaloneProductContractFixture(future); err == nil {
				t.Fatalf("future standalone product contract v%d passed", vector.Version)
			}
			continue
		}
		schema, ok := schemaByKey[key]
		if !ok || vector.Version != schema.Current+1 {
			t.Fatalf("forward vector %q=%d, schema=%+v", key, vector.Version, schema)
		}
		if err := standaloneDecodeArtifactVersion(t, vector.Kind, vector.Version); err == nil {
			t.Fatalf("future %s v%d passed", vector.Kind, vector.Version)
		}
	}
	for key, schema := range schemaByKey {
		if len(schema.Readable) == 0 {
			continue
		}
		if version, ok := forwardByKey[key]; !ok || version != schema.Current+1 {
			t.Fatalf("readable artifact %q lacks its next-version rejection", key)
		}
	}
	if version := forwardByKey[standaloneContractKey("standalone", "product-contract")]; version != 2 {
		t.Fatalf("standalone product-contract future vector=%d, want 2", version)
	}

	metricStates := make(map[string]bool, len(contract.MetricStates))
	for _, state := range contract.MetricStates {
		metricStates[state] = true
	}
	wantMetricVectors := map[string]bool{
		"observed-zero":     true,
		"unknown-zero":      true,
		"unsupported-zero":  true,
		"unknown-covered":   false,
		"uncovered-nonzero": false,
	}
	for _, vector := range conformance.MetricVectors {
		valid := standaloneMetricVectorValid(vector, metricStates)
		if vector.Valid == nil || valid != *vector.Valid {
			t.Fatalf("metric vector %q validity=%t, fixture=%v", vector.ID, valid, vector.Valid)
		}
		want, ok := wantMetricVectors[vector.ID]
		if !ok || want != *vector.Valid {
			t.Fatalf("metric vector %q has unexpected contract: %+v", vector.ID, vector)
		}
		delete(wantMetricVectors, vector.ID)
	}
	if len(wantMetricVectors) != 0 {
		t.Fatalf("metric conformance vectors are missing: %v", wantMetricVectors)
	}
}

func TestStandaloneContractAuthorityMatrix(t *testing.T) {
	contract := loadStandaloneProductContractFixture(t)
	configuration := contract.Configuration
	if !slices.Equal(configuration.Precedence, []string{"flags", "project_file", "opt_in_environment"}) ||
		configuration.UnknownKeys != "reject" || configuration.AmbientAuthority == nil || len(configuration.AmbientAuthority) != 0 {
		t.Fatalf("configuration authority contract=%+v", configuration)
	}

	wantOperations := map[string]struct {
		authority string
		contact   bool
	}{
		"capabilities":    {authority: "none"},
		"compare":         {authority: "none"},
		"compat verify":   {authority: "none"},
		"grade":           {authority: "verifier_execution"},
		"import":          {authority: "local_write"},
		"init":            {authority: "local_write"},
		"inspect":         {authority: "none"},
		"migrate apply":   {authority: "local_write"},
		"migrate preview": {authority: "none"},
		"plan":            {authority: "none"},
		"reconcile":       {authority: "local_write"},
		"report":          {authority: "none"},
		"resume":          {authority: "agent_execution", contact: true},
		"run":             {authority: "agent_execution", contact: true},
		"schema inspect":  {authority: "none"},
		"validate":        {authority: "none"},
		"version":         {authority: "none"},
	}
	for _, operation := range contract.StandaloneOperations {
		want, ok := wantOperations[operation.ID]
		if !ok {
			t.Fatalf("operation %q has no reviewed authority classification", operation.ID)
		}
		provider := standaloneBool(operation.ProviderContact)
		backend := standaloneBool(operation.BackendContact)
		network := standaloneBool(operation.Network)
		if operation.Authority != want.authority || provider != want.contact || backend != want.contact || network != want.contact {
			t.Fatalf("operation %q authority/contact=%+v, want authority=%q contact=%t", operation.ID, operation, want.authority, want.contact)
		}
		if (provider || backend) && !network {
			t.Fatalf("operation %q permits contact without network authority", operation.ID)
		}
		delete(wantOperations, operation.ID)
	}
	if len(wantOperations) != 0 {
		t.Fatalf("authority matrix omitted operations: %v", wantOperations)
	}

	wantAttempts := map[string]struct {
		terminal        bool
		automaticResume bool
	}{
		"cancelled":     {terminal: true},
		"committed":     {},
		"failed":        {terminal: true},
		"planned":       {automaticResume: true},
		"policy_denied": {terminal: true},
		"running":       {},
		"spawning":      {},
		"succeeded":     {terminal: true},
		"timed_out":     {terminal: true},
		"unknown":       {terminal: true},
		"unsupported":   {terminal: true},
	}
	for _, state := range contract.AttemptStates {
		want, ok := wantAttempts[state.ID]
		if !ok || state.Terminal == nil || state.AutomaticResume == nil ||
			*state.Terminal != want.terminal || *state.AutomaticResume != want.automaticResume {
			t.Fatalf("attempt state %q violates no-replay semantics: %+v", state.ID, state)
		}
		if *state.AutomaticResume && state.ID != "planned" {
			t.Fatalf("started attempt state %q permits automatic replay", state.ID)
		}
		delete(wantAttempts, state.ID)
	}
	if len(wantAttempts) != 0 {
		t.Fatalf("attempt lifecycle states are missing: %v", wantAttempts)
	}
}

func loadStandaloneProductContractFixture(t *testing.T) standaloneProductContractFixture {
	t.Helper()
	contract, err := decodeStandaloneProductContractFixture(standaloneReadFixture(t, standaloneProductContractFixturePath))
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

func loadStandaloneConformanceFixture(t *testing.T) standaloneConformanceFixture {
	t.Helper()
	fixture, err := decodeStandaloneConformanceFixture(standaloneReadFixture(t, standaloneConformanceFixturePath))
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func standaloneReadFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeStandaloneProductContractFixture(data []byte) (standaloneProductContractFixture, error) {
	var contract standaloneProductContractFixture
	if err := standaloneDecodeClosedJSON(data, &contract); err != nil {
		return standaloneProductContractFixture{}, fmt.Errorf("decode standalone product contract: %w", err)
	}
	if err := validateStandaloneProductContractFixture(contract); err != nil {
		return standaloneProductContractFixture{}, err
	}
	return contract, nil
}

func decodeStandaloneConformanceFixture(data []byte) (standaloneConformanceFixture, error) {
	var fixture standaloneConformanceFixture
	if err := standaloneDecodeClosedJSON(data, &fixture); err != nil {
		return standaloneConformanceFixture{}, fmt.Errorf("decode standalone conformance fixture: %w", err)
	}
	if fixture.SchemaVersion != 1 || len(fixture.Readability) == 0 || len(fixture.ForwardRejection) == 0 || len(fixture.MetricVectors) == 0 {
		return standaloneConformanceFixture{}, fmt.Errorf("standalone conformance fixture is incomplete")
	}
	previous := ""
	seenReadability := map[string]bool{}
	for _, vector := range fixture.Readability {
		key := standaloneContractKey(vector.Namespace, vector.Kind)
		if vector.Namespace == "" || vector.Kind == "" || key <= previous || seenReadability[key] || standaloneValidateVersions(vector.Versions, false) != nil {
			return standaloneConformanceFixture{}, fmt.Errorf("invalid readability vector %q", key)
		}
		previous = key
		seenReadability[key] = true
	}
	previous = ""
	seenForward := map[string]bool{}
	for _, vector := range fixture.ForwardRejection {
		key := standaloneContractKey(vector.Namespace, vector.Kind)
		if vector.Namespace == "" || vector.Kind == "" || vector.Version < 1 || key <= previous || seenForward[key] {
			return standaloneConformanceFixture{}, fmt.Errorf("invalid forward rejection vector %q", key)
		}
		previous = key
		seenForward[key] = true
	}
	seenMetrics := map[string]bool{}
	for _, vector := range fixture.MetricVectors {
		if vector.ID == "" || vector.State == "" || vector.Coverage == nil || vector.Value == nil || vector.Valid == nil || seenMetrics[vector.ID] {
			return standaloneConformanceFixture{}, fmt.Errorf("invalid metric vector %q", vector.ID)
		}
		seenMetrics[vector.ID] = true
	}
	return fixture, nil
}

func standaloneDecodeClosedJSON(data []byte, target any) error {
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return fmt.Errorf("contains multiple JSON values")
		}
		return fmt.Errorf("contains trailing JSON data: %w", err)
	}
	return nil
}

func validateStandaloneProductContractFixture(contract standaloneProductContractFixture) error {
	if contract.SchemaVersion != 1 {
		return fmt.Errorf("unsupported standalone product contract schema_version %d", contract.SchemaVersion)
	}
	if contract.ContractVersion == "" || contract.Status == "" || contract.CompatibilityBegins == "" {
		return fmt.Errorf("standalone product contract identity is incomplete")
	}
	if err := standaloneValidateSortedUniqueStrings("maintainer commands", contract.MaintainerCommands, false); err != nil {
		return err
	}
	previous := ""
	seenOperations := map[string]bool{}
	for _, operation := range contract.StandaloneOperations {
		if operation.ID == "" || operation.ID <= previous || seenOperations[operation.ID] || operation.CurrentStatus == "" ||
			operation.StandaloneStatus == "" || operation.Authority == "" || operation.ProviderContact == nil ||
			operation.BackendContact == nil || operation.Network == nil {
			return fmt.Errorf("invalid standalone operation %q", operation.ID)
		}
		previous = operation.ID
		seenOperations[operation.ID] = true
	}
	if contract.Configuration.Precedence == nil || contract.Configuration.AmbientAuthority == nil || contract.Configuration.UnknownKeys == "" {
		return fmt.Errorf("standalone configuration contract is incomplete")
	}
	previous = ""
	seenRoles := map[string]bool{}
	for _, role := range contract.Roles {
		if role.ID == "" || role.ID <= previous || seenRoles[role.ID] ||
			standaloneValidateDistinctNonemptyStrings(role.Owns) != nil || standaloneValidateDistinctNonemptyStrings(role.DoesNotOwn) != nil {
			return fmt.Errorf("invalid standalone role %q", role.ID)
		}
		previous = role.ID
		seenRoles[role.ID] = true
	}
	for owner, states := range map[string][]string{"capability states": contract.CapabilityStates, "metric states": contract.MetricStates} {
		if err := standaloneValidateSortedUniqueStrings(owner, states, false); err != nil {
			return err
		}
	}
	previousCode := -1
	seenExitIDs := map[string]bool{}
	for _, exitClass := range contract.ExitClasses {
		if exitClass.Code == nil || *exitClass.Code < 0 || *exitClass.Code > 255 || *exitClass.Code <= previousCode || exitClass.ID == "" || seenExitIDs[exitClass.ID] {
			return fmt.Errorf("invalid standalone exit class %+v", exitClass)
		}
		previousCode = *exitClass.Code
		seenExitIDs[exitClass.ID] = true
	}
	previous = ""
	seenSchemas := map[string]bool{}
	for _, schema := range contract.ArtifactSchemas {
		key := standaloneContractKey(schema.Namespace, schema.Kind)
		if schema.Namespace == "" || schema.Kind == "" || key <= previous || seenSchemas[key] || schema.Current < 1 ||
			schema.Readable == nil || schema.Emitted == nil || schema.Executable == nil || schema.Disposition == "" ||
			schema.Privacy == "" || schema.Migration == "" || schema.MaxBytes == nil || *schema.MaxBytes < 1 {
			return fmt.Errorf("invalid standalone artifact schema %q", key)
		}
		if err := standaloneValidateVersions(schema.Readable, true); err != nil {
			return fmt.Errorf("artifact schema %q readable versions: %w", key, err)
		}
		if err := standaloneValidateVersions(schema.Emitted, true); err != nil {
			return fmt.Errorf("artifact schema %q emitted versions: %w", key, err)
		}
		if err := standaloneValidateVersions(schema.Executable, true); err != nil {
			return fmt.Errorf("artifact schema %q executable versions: %w", key, err)
		}
		if len(schema.Emitted) != 0 && !slices.Contains(schema.Emitted, schema.Current) {
			return fmt.Errorf("artifact schema %q does not emit its current version", key)
		}
		previous = key
		seenSchemas[key] = true
	}
	previous = ""
	seenClasses := map[string]bool{}
	for _, class := range contract.ArtifactClasses {
		if class.ID == "" || class.ID <= previous || seenClasses[class.ID] || class.Privacy == "" || class.Disposition == "" {
			return fmt.Errorf("invalid standalone artifact class %q", class.ID)
		}
		previous = class.ID
		seenClasses[class.ID] = true
	}
	previous = ""
	seenAttempts := map[string]bool{}
	for _, state := range contract.AttemptStates {
		if state.ID == "" || state.ID <= previous || seenAttempts[state.ID] || state.Terminal == nil || state.AutomaticResume == nil {
			return fmt.Errorf("invalid standalone attempt state %q", state.ID)
		}
		previous = state.ID
		seenAttempts[state.ID] = true
	}
	policy := contract.CompatibilityPolicy
	if policy.StrictUnknownFields == nil || policy.AdditiveMembersRequireSchemaBump == nil || policy.SourceBytesPreserved == nil ||
		policy.MigrationRequiresReviewedPreview == nil || policy.MinimumDeprecationReleases == nil || policy.RootModuleLinkForbidden == nil {
		return fmt.Errorf("standalone compatibility policy is incomplete")
	}
	return nil
}

func standaloneCoordinatorCommands(t *testing.T, path string) []string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var runFunction *ast.FuncDecl
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Recv == nil && function.Name.Name == "run" {
			runFunction = function
			break
		}
	}
	if runFunction == nil {
		t.Fatal("coordinator run function is absent")
	}
	var commands []string
	var commandSwitches int
	ast.Inspect(runFunction.Body, func(node ast.Node) bool {
		switchStatement, ok := node.(*ast.SwitchStmt)
		if !ok || !standaloneArgsZeroExpression(switchStatement.Tag) {
			return true
		}
		commandSwitches++
		for _, statement := range switchStatement.Body.List {
			clause, ok := statement.(*ast.CaseClause)
			if !ok {
				t.Fatal("coordinator command switch contains a non-case statement")
			}
			for _, expression := range clause.List {
				literal, ok := expression.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatal("coordinator command switch has a non-literal command")
				}
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal(err)
				}
				commands = append(commands, value)
			}
		}
		return false
	})
	if commandSwitches != 1 {
		t.Fatalf("coordinator command switch count=%d, want 1", commandSwitches)
	}
	slices.Sort(commands)
	return commands
}

func standaloneArgsZeroExpression(expression ast.Expr) bool {
	index, ok := expression.(*ast.IndexExpr)
	if !ok {
		return false
	}
	identifier, ok := index.X.(*ast.Ident)
	if !ok || identifier.Name != "args" {
		return false
	}
	literal, ok := index.Index.(*ast.BasicLit)
	return ok && literal.Kind == token.INT && literal.Value == "0"
}

func standaloneDecodeArtifactVersion(t *testing.T, kind string, version int) error {
	t.Helper()
	encode := func(value any) ([]byte, error) { return json.Marshal(value) }
	switch kind {
	case "capability-catalog":
		catalog := mustPinnedCapabilityCatalog(t)
		catalog.SchemaVersion = version
		data, err := encode(catalog)
		if err != nil {
			return err
		}
		_, err = DecodeCapabilityCatalog(bytes.NewReader(data))
		return err
	case "observation":
		observation := validObservation()
		observation.SchemaVersion = version
		data, err := encode(observation)
		if err != nil {
			return err
		}
		_, err = DecodeObservation(bytes.NewReader(data))
		return err
	case "qualitative-panel":
		panel := QualitativePanelPolicy{SchemaVersion: version, Method: QualitativePanelMethod, ExpectedReviewers: 3, MaxCriterionRangeBPS: 2500}
		data, err := encode(panel)
		if err != nil {
			return err
		}
		var decoded QualitativePanelPolicy
		if err := decodeStrict(bytes.NewReader(data), &decoded); err != nil {
			return err
		}
		return decoded.Validate()
	case "result":
		_, err := DecodeResult(bytes.NewReader(minimalResultContractJSON(t, version)))
		return err
	case "review":
		result, err := Evaluate(validScenario(), validObservation())
		if err != nil {
			return err
		}
		resultData, err := encode(result)
		if err != nil {
			return err
		}
		rubric := testRubric(result.ScenarioID)
		review, err := NewReviewTemplate(result, resultData, []byte(`{"answer":"synthetic"}`), rubric, Reviewer{Kind: "human"})
		if err != nil {
			return err
		}
		review.SchemaVersion = version
		data, err := encode(review)
		if err != nil {
			return err
		}
		_, err = DecodeReview(bytes.NewReader(data))
		return err
	case "rubric":
		rubric := testRubric(validScenario().ID)
		rubric.SchemaVersion = version
		data, err := encode(rubric)
		if err != nil {
			return err
		}
		_, err = DecodeRubric(bytes.NewReader(data))
		return err
	case "run-spec":
		spec := validRunSpec()
		spec.SchemaVersion = version
		data, err := encode(spec)
		if err != nil {
			return err
		}
		_, err = DecodeRunSpec(bytes.NewReader(data))
		return err
	case "scenario":
		scenario := validScenario()
		scenario.SchemaVersion = version
		data, err := encode(scenario)
		if err != nil {
			return err
		}
		_, err = DecodeScenario(bytes.NewReader(data))
		return err
	case "synthetic-run-receipt":
		receipt := SyntheticRunReceipt{
			SchemaVersion: version, ScenarioID: "synthetic-task", Provider: "codex", Variant: "baseline",
			Repetition: 1, Repetitions: 1, TaskContractSHA256: strings.Repeat("1", 64),
			ExecutionContractSHA256: strings.Repeat("2", 64), AgentExecutableSHA256: strings.Repeat("3", 64),
			ATLExecutableSHA256: strings.Repeat("4", 64), WrapperExecutableSHA256: strings.Repeat("5", 64),
			ResultSHA256: strings.Repeat("6", 64),
		}
		data, err := encode(receipt)
		if err != nil {
			return err
		}
		_, err = DecodeSyntheticRunReceipt(bytes.NewReader(data))
		return err
	default:
		return fmt.Errorf("unsupported conformance artifact kind %q", kind)
	}
}

func standaloneMetricVectorValid(vector standaloneMetricVector, states map[string]bool) bool {
	if !states[vector.State] || vector.Coverage == nil || vector.Value == nil || *vector.Value < 0 {
		return false
	}
	if vector.State == "observed" {
		return *vector.Coverage
	}
	return !*vector.Coverage && *vector.Value == 0
}

func standaloneValidateSortedUniqueStrings(owner string, values []string, allowEmpty bool) error {
	if values == nil || !allowEmpty && len(values) == 0 {
		return fmt.Errorf("%s is missing", owner)
	}
	previous := ""
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value || value <= previous {
			return fmt.Errorf("%s is not sorted, unique, and non-empty", owner)
		}
		previous = value
	}
	return nil
}

func standaloneValidateDistinctNonemptyStrings(values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("list is empty")
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value || seen[value] {
			return fmt.Errorf("list contains an invalid value")
		}
		seen[value] = true
	}
	return nil
}

func standaloneValidateVersions(versions []int, allowEmpty bool) error {
	if versions == nil || !allowEmpty && len(versions) == 0 {
		return fmt.Errorf("version list is missing")
	}
	previous := 0
	for _, version := range versions {
		if version < 1 || version <= previous {
			return fmt.Errorf("versions are not positive, sorted, and unique")
		}
		previous = version
	}
	return nil
}

func standaloneContractKey(namespace, kind string) string {
	return namespace + "\x00" + kind
}

func standaloneTrue(value *bool) bool {
	return value != nil && *value
}

func standaloneBool(value *bool) bool {
	return value != nil && *value
}
