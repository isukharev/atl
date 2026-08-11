package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	atlCoreEquivalenceManifestPath = "testdata/atl-core-equivalence.v1.json"
	atlCoreEquivalenceMaxBytes     = 1 << 20
	atlCoreEquivalencePrivacyInput = "SYNTHETIC_PRIVATE_CANARY_INPUT_ONLY"
)

type atlCoreEquivalenceManifest struct {
	SchemaVersion     int                                 `json:"schema_version"`
	LegacyBaseline    atlCoreEquivalenceLegacyBaseline    `json:"legacy_baseline"`
	ReadabilitySource atlCoreEquivalenceReadabilitySource `json:"readability_source"`
	Files             []atlCoreEquivalenceFile            `json:"files"`
}

type atlCoreEquivalenceLegacyBaseline struct {
	RepositoryCommit         string `json:"repository_commit"`
	EvaluateSourceSHA256     string `json:"evaluate_source_sha256"`
	AggregateSourceSHA256    string `json:"aggregate_source_sha256"`
	ResultWriterSourceSHA256 string `json:"result_writer_source_sha256"`
	ReceiptSourceSHA256      string `json:"receipt_source_sha256"`
}

type atlCoreEquivalenceReadabilitySource struct {
	Path                     string `json:"path"`
	SHA256                   string `json:"sha256"`
	EntryCount               int    `json:"entry_count"`
	SemanticProjectionSHA256 string `json:"semantic_projection_sha256"`
}

type atlCoreEquivalenceFile struct {
	ID     string `json:"id"`
	Role   string `json:"role"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type atlCoreEquivalenceProjection struct {
	Identity            string          `json:"identity"`
	SourceSHA256        string          `json:"source_sha256"`
	ReaderSupportSHA256 string          `json:"reader_support_sha256,omitempty"`
	Projection          json.RawMessage `json:"projection"`
}

type atlCoreEquivalenceEvaluateFixture struct {
	SchemaVersion int                                  `json:"schema_version"`
	Scenarios     []atlCoreEquivalenceScenarioDocument `json:"scenarios"`
	Cases         []atlCoreEquivalenceEvaluateCase     `json:"cases"`
}

type atlCoreEquivalenceScenarioDocument struct {
	ID       string          `json:"id"`
	Document json.RawMessage `json:"document"`
}

type atlCoreEquivalenceEvaluateCase struct {
	ID                     string          `json:"id"`
	Scenario               string          `json:"scenario"`
	Observation            json.RawMessage `json:"observation"`
	ExpectedStatus         string          `json:"expected_status"`
	ExpectedViolationCount int             `json:"expected_violation_count"`
	ExpectedResultFile     string          `json:"expected_result_file"`
}

type atlCoreEquivalenceAggregateFixture struct {
	SchemaVersion int                               `json:"schema_version"`
	Cases         []atlCoreEquivalenceAggregateCase `json:"cases"`
}

type atlCoreEquivalenceAggregateCase struct {
	ID                    string   `json:"id"`
	ResultSources         []string `json:"result_sources"`
	ExpectedGroupCount    int      `json:"expected_group_count"`
	ExpectedAggregateFile string   `json:"expected_aggregate_file"`
}

type atlCoreEquivalenceReceiptFixture struct {
	SchemaVersion           int    `json:"schema_version"`
	ResultFile              string `json:"result_file"`
	ScenarioID              string `json:"scenario_id"`
	Provider                string `json:"provider"`
	Variant                 string `json:"variant"`
	Repetition              int    `json:"repetition"`
	Repetitions             int    `json:"repetitions"`
	TaskContractSHA256      string `json:"task_contract_sha256"`
	ExecutionContractSHA256 string `json:"execution_contract_sha256"`
	AgentExecutableSHA256   string `json:"agent_executable_sha256"`
	ATLExecutableSHA256     string `json:"atl_executable_sha256"`
	WrapperExecutableSHA256 string `json:"wrapper_executable_sha256"`
	AttemptBindingSHA256    string `json:"attempt_binding_sha256"`
	ExpectedReceiptFile     string `json:"expected_receipt_file"`
}

func TestATLCoreEquivalenceReadableGenerations(t *testing.T) {
	manifest := loadATLCoreEquivalenceManifest(t)
	source := manifest.ReadabilitySource
	goldens := loadStandaloneReadabilityGoldenFixture(t, standaloneGoldenBundle{Path: source.Path, SHA256: source.SHA256})
	if len(goldens.Entries) != source.EntryCount || source.EntryCount != 52 {
		t.Fatalf("readability entries=%d, want %d", len(goldens.Entries), source.EntryCount)
	}

	records := make([]atlCoreEquivalenceProjection, 0, len(goldens.Entries))
	latest := make(map[string]standaloneReadabilityGoldenEntry)
	var sourceDocuments bytes.Buffer
	var semanticDocuments bytes.Buffer
	previousIdentity := ""
	for _, entry := range goldens.Entries {
		identity := fmt.Sprintf("%s/%s@%d", entry.Namespace, entry.Kind, entry.Version)
		if identity <= previousIdentity {
			t.Fatalf("readability identities are not sorted and unique at %q", identity)
		}
		previousIdentity = identity
		document := standaloneGoldenDocument(t, entry)
		sourceDocuments.Write(document)
		projection, err := standaloneDecodeReadabilityProjection(t, entry, document)
		if err != nil {
			t.Fatalf("%s real reader: %v", identity, err)
		}
		actualProjection, err := json.Marshal(projection)
		if err != nil {
			t.Fatalf("%s encode projection: %v", identity, err)
		}
		expectedProjection := canonicalATLCoreJSON(t, entry.ExpectedProjection)
		if !bytes.Equal(actualProjection, expectedProjection) {
			t.Fatalf("%s projection=%s, want %s", identity, actualProjection, expectedProjection)
		}
		semanticDocuments.Write(actualProjection)
		records = append(records, atlCoreEquivalenceProjection{
			Identity:            identity,
			SourceSHA256:        atlCoreEquivalenceSHA256(document),
			ReaderSupportSHA256: entry.ReaderSupportSHA256,
			Projection:          actualProjection,
		})
		family := entry.Namespace + "/" + entry.Kind
		if previous, ok := latest[family]; !ok || previous.Version < entry.Version {
			latest[family] = entry
		}
	}
	encodedRecords, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if actual := atlCoreEquivalenceSHA256(encodedRecords); actual != source.SemanticProjectionSHA256 {
		t.Fatalf("readability semantic projection digest=%s, want %s", actual, source.SemanticProjectionSHA256)
	}
	for family, entry := range latest {
		if err := standaloneDecodeFutureReadabilityGolden(t, entry, entry.Version+1); err == nil {
			t.Fatalf("%s accepted future schema_version %d", family, entry.Version+1)
		}
	}
	if !bytes.Contains(sourceDocuments.Bytes(), []byte("cases/")) || !bytes.Contains(sourceDocuments.Bytes(), []byte("prompt.txt")) {
		t.Fatal("readability sources lost the synthetic source-only privacy canaries")
	}
	for _, canary := range []string{"cases/", "prompt.txt"} {
		if bytes.Contains(semanticDocuments.Bytes(), []byte(canary)) {
			t.Fatalf("semantic projection retained source-only value %q", canary)
		}
	}
}

func TestATLCoreEquivalenceEvaluateBytes(t *testing.T) {
	manifest := loadATLCoreEquivalenceManifest(t)
	data := readATLCoreEquivalenceFile(t, manifest, "evaluate-cases")
	var fixture atlCoreEquivalenceEvaluateFixture
	decodeATLCoreEquivalenceJSON(t, data, &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.Scenarios) != 2 || len(fixture.Cases) != 5 {
		t.Fatalf("evaluate fixture is incomplete: scenarios=%d cases=%d", len(fixture.Scenarios), len(fixture.Cases))
	}
	if !bytes.Contains(data, []byte(atlCoreEquivalencePrivacyInput)) {
		t.Fatal("evaluate input lost its privacy canary")
	}

	scenarios := make(map[string]Scenario, len(fixture.Scenarios))
	previous := ""
	for _, source := range fixture.Scenarios {
		if source.ID <= previous {
			t.Fatalf("scenario fixture ids are not sorted and unique at %q", source.ID)
		}
		previous = source.ID
		scenario, err := DecodeScenario(bytes.NewReader(source.Document))
		if err != nil {
			t.Fatalf("scenario %s: %v", source.ID, err)
		}
		scenarios[source.ID] = scenario
	}

	results := make(map[string]Result, len(fixture.Cases))
	previous = ""
	for _, testCase := range fixture.Cases {
		if testCase.ID <= previous {
			t.Fatalf("evaluate case ids are not sorted and unique at %q", testCase.ID)
		}
		previous = testCase.ID
		scenario, ok := scenarios[testCase.Scenario]
		if !ok {
			t.Fatalf("case %s references unknown scenario %q", testCase.ID, testCase.Scenario)
		}
		observation, err := DecodeObservation(bytes.NewReader(testCase.Observation))
		if err != nil {
			t.Fatalf("case %s observation: %v", testCase.ID, err)
		}
		result, err := Evaluate(scenario, observation)
		if err != nil {
			t.Fatalf("case %s evaluate: %v", testCase.ID, err)
		}
		actual := encodeATLCoreEquivalenceJSON(t, result)
		expected := readATLCoreEquivalenceFile(t, manifest, testCase.ExpectedResultFile)
		if !bytes.Equal(actual, expected) {
			t.Fatalf("case %s result bytes differ\ngot:\n%s\nwant:\n%s", testCase.ID, actual, expected)
		}
		decoded, err := DecodeResult(bytes.NewReader(expected))
		if err != nil {
			t.Fatalf("case %s frozen result: %v", testCase.ID, err)
		}
		if decoded.Status != testCase.ExpectedStatus || len(decoded.Violations) != testCase.ExpectedViolationCount {
			t.Fatalf("case %s status=%s violations=%d", testCase.ID, decoded.Status, len(decoded.Violations))
		}
		if bytes.Contains(expected, []byte(atlCoreEquivalencePrivacyInput)) {
			t.Fatalf("case %s result retained the input-only privacy canary", testCase.ID)
		}
		results[testCase.ID] = decoded
	}

	failed := results["all-deterministic-gates"]
	wantBudgetSubjects := []string{
		"agent_turns", "atl_invocations", "backend_requests", "delegations", "duplicate_backend_requests",
		"duration_millis", "estimated_cost_microusd", "input_tokens", "interface_invocations",
		"main_thread_input_tokens", "main_thread_output_tokens", "output_bytes", "output_tokens",
		"remote_writes", "tool_calls",
	}
	var budgetSubjects []string
	violationKinds := make(map[string]bool)
	for _, violation := range failed.Violations {
		violationKinds[violation.Code] = true
		if violation.Code == "budget_exceeded" {
			budgetSubjects = append(budgetSubjects, violation.Subject)
		}
	}
	if !slices.Equal(budgetSubjects, wantBudgetSubjects) {
		t.Fatalf("budget branches=%v, want %v", budgetSubjects, wantBudgetSubjects)
	}
	for _, kind := range []string{"budget_exceeded", "http_method_not_allowed", "metric_not_observed", "required_check_failed"} {
		if !violationKinds[kind] {
			t.Fatalf("missing deterministic violation branch %q", kind)
		}
	}
	if !slices.Equal(failed.Warnings, []string{"alpha-warning", "zeta-warning"}) ||
		len(failed.CapabilityFamilies) != 2 || failed.CapabilityFamilies[0].Family != "confluence.page.resolve" || failed.CapabilityFamilies[1].Family != "jira.issue.fields" {
		t.Fatalf("deterministic normalization changed: warnings=%v families=%v", failed.Warnings, failed.CapabilityFamilies)
	}
	zero, unknown := results["observed-zero"], results["unknown-optional-metric"]
	if !zero.Coverage["output_bytes"] || zero.Metrics.OutputBytes != 0 || unknown.Coverage["output_bytes"] || unknown.Metrics.OutputBytes != 0 {
		t.Fatalf("zero/unknown presence collapsed: zero=%v/%d unknown=%v/%d", zero.Coverage["output_bytes"], zero.Metrics.OutputBytes, unknown.Coverage["output_bytes"], unknown.Metrics.OutputBytes)
	}
	if results["unsupported-capability"].EffectiveEligibility() != EligibilityUnsupportedCapability ||
		results["invalidated-drift"].EffectiveEligibility() != EligibilityInvalidatedDrift {
		t.Fatal("eligibility branches were not frozen")
	}

	if _, err := DecodeScenario(bytes.NewReader(withATLCoreSchemaVersion(t, fixture.Scenarios[0].Document, ScenarioSchemaVersion+1))); err == nil {
		t.Fatal("future scenario schema was accepted")
	}
	if _, err := DecodeObservation(bytes.NewReader(withATLCoreSchemaVersion(t, fixture.Cases[0].Observation, ObservationSchemaVersion+1))); err == nil {
		t.Fatal("future observation schema was accepted")
	}
	mismatch := resultsObservation(t, fixture.Cases, "observed-zero")
	mismatch.ScenarioID = "different-scenario"
	if _, err := Evaluate(scenarios["coverage-and-eligibility"], mismatch); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("scenario mismatch was not rejected: %v", err)
	}
	unrequired := resultsObservation(t, fixture.Cases, "unsupported-capability")
	unrequired.UnavailableCapabilities = []string{"confluence.page.read"}
	if _, err := Evaluate(scenarios["coverage-and-eligibility"], unrequired); err == nil || !strings.Contains(err.Error(), "is not required") {
		t.Fatalf("unrequired unavailable capability was not rejected: %v", err)
	}
}

func TestATLCoreEquivalenceAggregateBytes(t *testing.T) {
	manifest := loadATLCoreEquivalenceManifest(t)
	data := readATLCoreEquivalenceFile(t, manifest, "aggregate-cases")
	var fixture atlCoreEquivalenceAggregateFixture
	decodeATLCoreEquivalenceJSON(t, data, &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.Cases) != 2 {
		t.Fatalf("aggregate fixture is incomplete: cases=%d", len(fixture.Cases))
	}
	goldens := loadStandaloneReadabilityGoldenFixture(t, standaloneGoldenBundle{
		Path: manifest.ReadabilitySource.Path, SHA256: manifest.ReadabilitySource.SHA256,
	})

	previous := ""
	for _, testCase := range fixture.Cases {
		if testCase.ID <= previous {
			t.Fatalf("aggregate case ids are not sorted and unique at %q", testCase.ID)
		}
		previous = testCase.ID
		seenSources := make(map[string]bool, len(testCase.ResultSources))
		results := make([]Result, 0, len(testCase.ResultSources))
		for _, source := range testCase.ResultSources {
			if seenSources[source] {
				t.Fatalf("case %s repeats result source %q", testCase.ID, source)
			}
			seenSources[source] = true
			results = append(results, readATLCoreEquivalenceResult(t, manifest, goldens, source))
		}
		aggregate, err := AggregateResults(results)
		if err != nil {
			t.Fatalf("case %s aggregate: %v", testCase.ID, err)
		}
		if aggregate.SchemaVersion != AggregateSchemaVersion || len(aggregate.Groups) != testCase.ExpectedGroupCount {
			t.Fatalf("case %s aggregate schema/groups=%d/%d", testCase.ID, aggregate.SchemaVersion, len(aggregate.Groups))
		}
		actual := encodeATLCoreEquivalenceJSON(t, aggregate)
		expected := readATLCoreEquivalenceFile(t, manifest, testCase.ExpectedAggregateFile)
		if !bytes.Equal(actual, expected) {
			t.Fatalf("case %s aggregate bytes differ\ngot:\n%s\nwant:\n%s", testCase.ID, actual, expected)
		}
		if bytes.Contains(actual, []byte("prompt_contract_sha256")) || bytes.Contains(actual, []byte(atlCoreEquivalencePrivacyInput)) {
			t.Fatalf("case %s aggregate exposed a private contract identity", testCase.ID)
		}

		switch testCase.ID {
		case "historical-generations":
			singleton, panel := 0, 0
			activations := make(map[string]bool)
			for _, group := range aggregate.Groups {
				if group.Runtime.PromptContractSHA256 != "" {
					t.Fatalf("aggregate published a prompt contract hash: %+v", group.Runtime)
				}
				activations[group.Runtime.SkillActivation] = true
				if group.Qualitative != nil {
					singleton++
				}
				if group.QualitativeReviewSet != nil {
					panel++
				}
			}
			if singleton != 1 || panel != 1 || !activations[SkillActivationImplicit] || !activations[SkillActivationDeveloper] || !activations[SkillActivationCombined] {
				t.Fatalf("historical qualitative/activation coverage changed: singleton=%d panel=%d activations=%v", singleton, panel, activations)
			}
		case "unknown-zero-and-eligibility":
			group := aggregate.Groups[0]
			if group.Runs != 4 || group.EligibleRuns != 2 || group.UnsupportedRuns != 1 || group.DriftedRuns != 1 ||
				group.CoverageRate != 2.0/3.0 || group.SuccessRate != 1 ||
				group.Metrics.OutputBytes != (Quantiles{ObservedRuns: 1, P50: 0, P90: 0}) {
				t.Fatalf("unknown/zero/eligibility aggregate changed: %+v", group)
			}
		}
	}

	promptBound := readATLCoreEquivalenceResult(t, manifest, goldens, "readability:atl-profile/result@5")
	drifted := promptBound
	drifted.Runtime.PromptContractSHA256 = strings.Repeat("e", 64)
	if _, err := AggregateResults([]Result{promptBound, drifted}); err == nil || !strings.Contains(err.Error(), "prompt contract") {
		t.Fatalf("aggregate accepted hidden prompt-contract drift: %v", err)
	}
}

func TestATLCoreEquivalenceSyntheticReceiptBytes(t *testing.T) {
	manifest := loadATLCoreEquivalenceManifest(t)
	data := readATLCoreEquivalenceFile(t, manifest, "receipt-case")
	var fixture atlCoreEquivalenceReceiptFixture
	decodeATLCoreEquivalenceJSON(t, data, &fixture)
	if fixture.SchemaVersion != 1 {
		t.Fatalf("receipt fixture schema_version=%d", fixture.SchemaVersion)
	}
	resultData := readATLCoreEquivalenceFile(t, manifest, fixture.ResultFile)
	result, err := DecodeResult(bytes.NewReader(resultData))
	if err != nil {
		t.Fatalf("decode receipt result: %v", err)
	}
	attestation := &syntheticRunAttestation{
		spec: RunSpec{Repetitions: fixture.Repetitions},
		executables: syntheticExecutableDigests{
			agent: fixture.AgentExecutableSHA256, atl: fixture.ATLExecutableSHA256, wrapper: fixture.WrapperExecutableSHA256,
		},
	}
	loaded := resolvedRunContract{
		scenario: Scenario{ID: fixture.ScenarioID},
		spec:     RunSpec{Provider: fixture.Provider, Variant: fixture.Variant},
	}
	receipt, err := newSyntheticRunReceipt(
		attestation, loaded, result.Runtime, fixture.Repetition,
		fixture.TaskContractSHA256, fixture.ExecutionContractSHA256, fixture.AttemptBindingSHA256, resultData,
	)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := encodeSyntheticRunReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	expected := readATLCoreEquivalenceFile(t, manifest, fixture.ExpectedReceiptFile)
	if !bytes.Equal(actual, expected) {
		t.Fatalf("receipt bytes differ\ngot:\n%s\nwant:\n%s", actual, expected)
	}
	decoded, err := DecodeSyntheticRunReceipt(bytes.NewReader(expected))
	if err != nil {
		t.Fatalf("decode frozen receipt: %v", err)
	}
	if decoded != receipt || decoded.ResultSHA256 != atlCoreEquivalenceSHA256(resultData) ||
		!syntheticReceiptMatchesResult(decoded, result, resultData, fixture.ScenarioID, fixture.Provider, fixture.Variant, fixture.Repetition) {
		t.Fatalf("receipt/result binding changed: receipt=%+v", decoded)
	}
	if bytes.Contains(expected, []byte(atlCoreEquivalencePrivacyInput)) {
		t.Fatal("receipt retained the input-only privacy canary")
	}
}

func loadATLCoreEquivalenceManifest(t *testing.T) atlCoreEquivalenceManifest {
	t.Helper()
	data := readATLCoreEquivalencePath(t, atlCoreEquivalenceManifestPath)
	var manifest atlCoreEquivalenceManifest
	decodeATLCoreEquivalenceJSON(t, data, &manifest)
	if manifest.SchemaVersion != 1 || manifest.ReadabilitySource.Path != "testdata/standalone-readability-golden.v1.json" ||
		manifest.ReadabilitySource.EntryCount != 52 || !standaloneValidSHA256(manifest.ReadabilitySource.SHA256) ||
		!standaloneValidSHA256(manifest.ReadabilitySource.SemanticProjectionSHA256) ||
		len(manifest.LegacyBaseline.RepositoryCommit) != 40 || !atlCoreEquivalenceHex(manifest.LegacyBaseline.RepositoryCommit) ||
		!standaloneValidSHA256(manifest.LegacyBaseline.EvaluateSourceSHA256) ||
		!standaloneValidSHA256(manifest.LegacyBaseline.AggregateSourceSHA256) ||
		!standaloneValidSHA256(manifest.LegacyBaseline.ResultWriterSourceSHA256) ||
		!standaloneValidSHA256(manifest.LegacyBaseline.ReceiptSourceSHA256) || len(manifest.Files) == 0 {
		t.Fatal("ATL/core equivalence manifest is incomplete")
	}
	previousID := ""
	paths := make(map[string]bool, len(manifest.Files))
	for _, file := range manifest.Files {
		if file.ID <= previousID || paths[file.Path] || (file.Role != "input" && file.Role != "expected") ||
			!strings.HasPrefix(file.Path, "testdata/atl-core-equivalence-") || filepath.Dir(file.Path) != "testdata" ||
			filepath.Clean(file.Path) != file.Path || !standaloneValidSHA256(file.SHA256) {
			t.Fatalf("invalid manifest file %q at %q", file.ID, file.Path)
		}
		previousID = file.ID
		paths[file.Path] = true
		actual := atlCoreEquivalenceSHA256(readATLCoreEquivalencePath(t, file.Path))
		if actual != file.SHA256 {
			t.Fatalf("manifest file %s digest=%s, want %s", file.ID, actual, file.SHA256)
		}
	}
	return manifest
}

func readATLCoreEquivalenceFile(t *testing.T, manifest atlCoreEquivalenceManifest, id string) []byte {
	t.Helper()
	for _, file := range manifest.Files {
		if file.ID == id {
			return readATLCoreEquivalencePath(t, file.Path)
		}
	}
	t.Fatalf("unknown ATL/core equivalence file %q", id)
	return nil
}

func readATLCoreEquivalencePath(t *testing.T, path string) []byte {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > atlCoreEquivalenceMaxBytes {
		t.Fatalf("equivalence fixture %q is not a bounded regular file: %v", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeATLCoreEquivalenceJSON(t *testing.T, data []byte, destination any) {
	t.Helper()
	if err := decodeStrict(bytes.NewReader(data), destination); err != nil {
		t.Fatalf("decode ATL/core equivalence fixture: %v", err)
	}
}

func canonicalATLCoreJSON(t *testing.T, data []byte) []byte {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func encodeATLCoreEquivalenceJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func atlCoreEquivalenceSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func atlCoreEquivalenceHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func withATLCoreSchemaVersion(t *testing.T, data []byte, version int) []byte {
	t.Helper()
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["schema_version"] = json.RawMessage(strconv.Itoa(version))
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func resultsObservation(t *testing.T, cases []atlCoreEquivalenceEvaluateCase, id string) Observation {
	t.Helper()
	for _, testCase := range cases {
		if testCase.ID != id {
			continue
		}
		observation, err := DecodeObservation(bytes.NewReader(testCase.Observation))
		if err != nil {
			t.Fatal(err)
		}
		return observation
	}
	t.Fatalf("unknown evaluate case %q", id)
	return Observation{}
}

func readATLCoreEquivalenceResult(
	t *testing.T,
	manifest atlCoreEquivalenceManifest,
	goldens standaloneReadabilityGoldenFixture,
	source string,
) Result {
	t.Helper()
	var data []byte
	if id, ok := strings.CutPrefix(source, "fixture:"); ok {
		data = readATLCoreEquivalenceFile(t, manifest, id)
	} else if key, ok := strings.CutPrefix(source, "readability:"); ok {
		family, versionText, separated := strings.Cut(key, "@")
		namespace, kind, divided := strings.Cut(family, "/")
		version, versionErr := strconv.Atoi(versionText)
		if !separated || !divided || namespace == "" || kind == "" || versionErr != nil {
			t.Fatalf("invalid readability result reference %q", key)
		}
		entry, found := standaloneReadabilityGoldenEntryFor(goldens, standaloneVersionedContractKey(namespace, kind, version))
		if !found || entry.Kind != "result" {
			t.Fatalf("unknown readability result %q", key)
		}
		data = standaloneGoldenDocument(t, entry)
	} else {
		t.Fatalf("unknown result source %q", source)
	}
	result, err := DecodeResult(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode result source %s: %v", source, err)
	}
	return result
}
