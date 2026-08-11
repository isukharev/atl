package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/extension"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

type standaloneReadabilityGoldenFixture struct {
	SchemaVersion int                                `json:"schema_version"`
	Entries       []standaloneReadabilityGoldenEntry `json:"entries"`
}

type standaloneReadabilityGoldenEntry struct {
	Namespace           string          `json:"namespace"`
	Kind                string          `json:"kind"`
	Version             int             `json:"version"`
	Document            json.RawMessage `json:"document,omitempty"`
	SourcePath          string          `json:"source_path,omitempty"`
	SourceSHA256        string          `json:"source_sha256,omitempty"`
	ReaderSupportPath   string          `json:"reader_support_path,omitempty"`
	ReaderSupportSHA256 string          `json:"reader_support_sha256,omitempty"`
	ExpectedProjection  json.RawMessage `json:"expected_projection"`
}

func loadStandaloneReadabilityGoldenFixture(t *testing.T, bundle standaloneGoldenBundle) standaloneReadabilityGoldenFixture {
	t.Helper()
	data := standaloneReadFixture(t, bundle.Path)
	digest := sha256.Sum256(data)
	if actual := hex.EncodeToString(digest[:]); actual != bundle.SHA256 {
		t.Fatalf("readability golden bundle digest=%s, want %s", actual, bundle.SHA256)
	}
	var fixture standaloneReadabilityGoldenFixture
	if err := standaloneDecodeClosedJSON(data, &fixture); err != nil {
		t.Fatalf("decode readability golden bundle: %v", err)
	}
	if fixture.SchemaVersion != 1 || len(fixture.Entries) == 0 {
		t.Fatal("readability golden bundle is incomplete")
	}
	previous := ""
	seen := make(map[string]bool, len(fixture.Entries))
	for _, entry := range fixture.Entries {
		key := standaloneVersionedContractKey(entry.Namespace, entry.Kind, entry.Version)
		hasDocument := len(entry.Document) != 0
		hasSource := entry.SourcePath != ""
		if !standaloneOneOf(entry.Namespace, "atl-profile", "standalone") || entry.Kind == "" || entry.Version < 1 || key <= previous || seen[key] ||
			hasDocument == hasSource || len(entry.ExpectedProjection) == 0 {
			t.Fatalf("invalid readability golden entry %q", key)
		}
		if hasSource && (!standaloneGoldenSourceAllowed(entry) || !standaloneValidSHA256(entry.SourceSHA256)) {
			t.Fatalf("readability golden entry %q has an unreviewed source path", key)
		}
		if hasDocument && entry.SourceSHA256 != "" {
			t.Fatalf("readability golden entry %q has an unexpected source digest", key)
		}
		hasReaderSupport := entry.ReaderSupportPath != "" || entry.ReaderSupportSHA256 != ""
		if entry.Kind == "activation-reference" {
			if !hasReaderSupport || !standaloneGoldenReaderSupportAllowed(entry) || !standaloneValidSHA256(entry.ReaderSupportSHA256) {
				t.Fatalf("readability golden entry %q has invalid reader support", key)
			}
		} else if hasReaderSupport {
			t.Fatalf("readability golden entry %q has unexpected reader support", key)
		}
		document := standaloneGoldenDocument(t, entry)
		var identity struct {
			SchemaVersion int `json:"schema_version"`
		}
		if err := json.Unmarshal(document, &identity); err != nil || identity.SchemaVersion != entry.Version {
			t.Fatalf("readability golden entry %q document version is invalid", key)
		}
		var projection map[string]any
		if err := json.Unmarshal(entry.ExpectedProjection, &projection); err != nil || len(projection) == 0 {
			t.Fatalf("readability golden entry %q projection is invalid", key)
		}
		previous = key
		seen[key] = true
	}
	standaloneValidateExtensionGoldenBindings(t, fixture)
	return fixture
}

func standaloneValidateExtensionGoldenBindings(t *testing.T, fixture standaloneReadabilityGoldenFixture) {
	t.Helper()
	manifestEntry, manifestOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "adapter-manifest", 1))
	bundleEntry, bundleOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "extension-conformance-bundle", 1))
	reportEntry, reportOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "extension-conformance-report", 1))
	if !manifestOK || !bundleOK || !reportOK {
		t.Fatal("standalone extension readability sources are incomplete")
	}
	manifestData := standaloneGoldenDocument(t, manifestEntry)
	manifest, err := extension.DecodeManifest(manifestData)
	if err != nil {
		t.Fatalf("decode bound extension manifest: %v", err)
	}
	bundleData := standaloneGoldenDocument(t, bundleEntry)
	bundle, err := DecodeExtensionConformanceBundle(bundleData)
	if err != nil {
		t.Fatalf("decode bound extension bundle: %v", err)
	}
	report, err := DecodeExtensionConformanceReport(standaloneGoldenDocument(t, reportEntry))
	if err != nil {
		t.Fatalf("decode bound extension report: %v", err)
	}
	manifestDigest := sha256.Sum256(manifestData)
	bundleDigest := sha256.Sum256(bundleData)
	if bundle.ManifestSHA256 != hex.EncodeToString(manifestDigest[:]) || bundle.ExecutableSHA256 != manifest.ExecutableSHA256 ||
		report.BundleSHA256 != hex.EncodeToString(bundleDigest[:]) || report.ManifestSHA256 != bundle.ManifestSHA256 ||
		report.ExecutableSHA256 != bundle.ExecutableSHA256 || report.ComponentID != manifest.Component.ID ||
		report.ComponentVersion != manifest.Component.Version || report.Role != manifest.Component.Role ||
		len(report.Capabilities) != len(manifest.Component.Capabilities) || len(report.Cases) != len(bundle.Cases) {
		t.Fatal("standalone extension readability identities are not transitively bound")
	}
	for index := range manifest.Component.Capabilities {
		if report.Capabilities[index] != manifest.Component.Capabilities[index] {
			t.Fatal("standalone extension readability capability claims are not bound")
		}
	}
	for index := range bundle.Cases {
		if report.Cases[index].ID != bundle.Cases[index].ID || report.Cases[index].Operation != bundle.Cases[index].Operation ||
			report.Cases[index].Terminal != bundle.Cases[index].Expected.Type {
			t.Fatal("standalone extension readability case identities are not bound")
		}
	}
}

func standaloneReadabilityGoldenEntryFor(fixture standaloneReadabilityGoldenFixture, key string) (standaloneReadabilityGoldenEntry, bool) {
	for _, entry := range fixture.Entries {
		if standaloneVersionedContractKey(entry.Namespace, entry.Kind, entry.Version) == key {
			return entry, true
		}
	}
	return standaloneReadabilityGoldenEntry{}, false
}

func standaloneValidateReadabilityGolden(t *testing.T, entry standaloneReadabilityGoldenEntry) error {
	t.Helper()
	document := standaloneGoldenDocument(t, entry)
	projection, err := standaloneDecodeReadabilityProjection(t, entry, document)
	if err != nil {
		return err
	}
	actual, err := json.Marshal(projection)
	if err != nil {
		return err
	}
	var expected any
	if err := json.Unmarshal(entry.ExpectedProjection, &expected); err != nil {
		return err
	}
	want, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, want) {
		return fmt.Errorf("semantic projection=%s, want %s", actual, want)
	}
	return nil
}

func standaloneDecodeFutureReadabilityGolden(t *testing.T, entry standaloneReadabilityGoldenEntry, version int) error {
	t.Helper()
	if entry.Namespace == "standalone" || entry.Kind == "activation-report" {
		data, err := standaloneMutateCanonicalSchemaVersion(standaloneGoldenDocument(t, entry), entry.Version, version)
		if err != nil {
			return err
		}
		_, err = standaloneDecodeReadabilityProjection(t, entry, data)
		return err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(standaloneGoldenDocument(t, entry), &document); err != nil {
		return err
	}
	document["schema_version"] = json.RawMessage(strconv.Itoa(version))
	data, err := json.Marshal(document)
	if err != nil {
		return err
	}
	_, err = standaloneDecodeReadabilityProjection(t, entry, data)
	return err
}

func standaloneMutateCanonicalSchemaVersion(data []byte, current, future int) ([]byte, error) {
	if current < 1 || future < 1 || len(data) < 2 || data[len(data)-1] != '\n' || bytes.Count(data, []byte{'\n'}) != 1 {
		return nil, fmt.Errorf("readability source framing is invalid")
	}
	currentMember := []byte(`"schema_version":` + strconv.Itoa(current))
	if bytes.Count(data, currentMember) != 1 {
		return nil, fmt.Errorf("readability source schema identity is not canonical")
	}
	futureMember := []byte(`"schema_version":` + strconv.Itoa(future))
	return bytes.Replace(data, currentMember, futureMember, 1), nil
}

func standaloneGoldenDocument(t *testing.T, entry standaloneReadabilityGoldenEntry) []byte {
	t.Helper()
	if entry.SourcePath == "" {
		return bytes.Clone(entry.Document)
	}
	return standaloneReadGoldenSource(t, entry.SourcePath, entry.SourceSHA256)
}

func standaloneReadGoldenSource(t *testing.T, path, wantSHA256 string) []byte {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("readability golden source is not a regular file: %v", err)
	}
	data := standaloneReadFixture(t, path)
	digest := sha256.Sum256(data)
	if actual := hex.EncodeToString(digest[:]); actual != wantSHA256 {
		t.Fatalf("readability golden source digest=%s, want %s", actual, wantSHA256)
	}
	return data
}

func standaloneGoldenSourceAllowed(entry standaloneReadabilityGoldenEntry) bool {
	switch entry.Kind {
	case "activation-reference", "activation-report", "private-plan", "private-review-attempt", "private-review-receipt":
		return entry.Namespace == "atl-profile" && entry.SourcePath == fmt.Sprintf("testdata/standalone-readability/%s-v%d.json", entry.Kind, entry.Version)
	case "capability-catalog":
		return entry.Namespace == "atl-profile" && entry.Version == CapabilityCatalogSchemaVersion && entry.SourcePath == "testdata/capability-catalog.v1.json"
	case "adapter-manifest", "adapter-message", "attempt-event", "attempt-ledger", "attempt-plan", "extension-conformance-bundle", "extension-conformance-report", "migration-preview", "migration-result", "project-config":
		return entry.Namespace == "standalone" && entry.Version == 1 &&
			entry.SourcePath == fmt.Sprintf("testdata/standalone-readability/%s-v1.json", entry.Kind)
	case "schema-registry":
		return entry.Namespace == "standalone" && entry.Version == StandaloneSchemaRegistryVersion &&
			entry.SourcePath == "schemaregistry/registry.v1.json"
	default:
		return false
	}
}

func standaloneGoldenReaderSupportAllowed(entry standaloneReadabilityGoldenEntry) bool {
	want := map[int]string{
		LegacyPrivateActivationReferenceSchemaVersion: "testdata/standalone-readability/private-plan-v4.json",
		PrivateActivationReferenceSchemaVersion:       "testdata/standalone-readability/activation-plan-v9.json",
	}
	return entry.ReaderSupportPath == want[entry.Version]
}

func standaloneDecodeReadabilityProjection(t *testing.T, entry standaloneReadabilityGoldenEntry, data []byte) (map[string]any, error) {
	t.Helper()
	if entry.Namespace == "standalone" {
		return standaloneDecodeExtensionReadabilityProjection(entry, data)
	}
	if entry.Namespace != "atl-profile" {
		return nil, fmt.Errorf("unsupported readability golden namespace %q", entry.Namespace)
	}
	switch entry.Kind {
	case "activation-reference":
		stored, err := standaloneLoadActivationReferenceGolden(t, entry, data)
		if err != nil {
			return nil, err
		}
		gates, err := PrivateActivationReferenceGates(stored.Reference)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"reference_alias": stored.ReferenceAlias, "plan_id": stored.PlanID,
			"reference_version": stored.Reference.SchemaVersion, "cell_count": len(stored.Reference.Cells),
			"causal_eligible": gates.CausalEligible, "promotion_eligible": gates.PromotionEligible,
		}, nil
	case "activation-report":
		report, err := DecodePrivateActivationReport(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		canonical, err := EncodePrivateActivationReport(report)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("activation report golden is not canonical")
		}
		metricCount := 0
		for _, treatment := range report.Treatments {
			metricCount += len(treatment.Metrics)
		}
		return map[string]any{
			"schema_version": report.SchemaVersion, "treatment_count": len(report.Treatments),
			"metric_count": metricCount, "contrast_count": len(report.Contrasts),
			"capture_eligible": report.Gates.CaptureEligible, "causal_eligible": report.Gates.CausalEligible,
			"promotion_eligible": report.Gates.PromotionEligible,
		}, nil
	case "capability-catalog":
		catalog, err := DecodeCapabilityCatalog(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"schema_version": catalog.SchemaVersion, "capability_count": len(catalog.Capabilities),
			"first_capability": catalog.Capabilities[0].ID, "last_capability": catalog.Capabilities[len(catalog.Capabilities)-1].ID,
		}, nil
	case "observation":
		observation, err := DecodeObservation(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"scenario_id": observation.ScenarioID, "variant": observation.Variant,
			"surface": observation.EffectiveSurface(), "eligibility": observation.EffectiveEligibility(),
			"evidence_state": string(observation.EvidenceReport.State),
		}, nil
	case "private-plan":
		var plan privatePlan
		if decodePrivateLifecycleJSON(data, &plan) != nil || validatePrivatePlan(plan, plan.PlanID) != nil {
			return nil, fmt.Errorf("private plan golden is invalid")
		}
		kind := plan.Kind
		if kind == "" {
			kind = PrivateRunSetKindComparison
		}
		studyContractVersion := 0
		if plan.StudyContract != nil {
			studyContractVersion = plan.StudyContract.SchemaVersion
		}
		automatedReview := plan.QualitativeReviewPanel != nil && len(plan.QualitativeReviewPanel.Executions) != 0
		liveWrites := false
		queryOnlyRequests := 0
		for _, item := range plan.Items {
			liveWrites = liveWrites || item.LiveWrites
			queryOnlyRequests += item.MaxQueryOnlyRequests
		}
		toolQualified := plan.ToolAvailability != nil && plan.ToolAvailability.Status == CodexCLIToolAvailabilitySupported
		cliRouteSupported := plan.CLIRouteQualification != nil && plan.CLIRouteQualification.Status == CLIRouteQualificationSupported
		return map[string]any{
			"plan_id": plan.PlanID, "run_set_alias": plan.RunSetAlias, "kind": kind,
			"item_count": len(plan.Items), "first_surface": plan.Items[0].Surface,
			"execution_eligible":     plan.SchemaVersion == PrivatePlanSchemaVersion,
			"first_skill_activation": plan.Items[0].SkillActivation, "prompt_bound": plan.Items[0].PromptContractSHA256 != "",
			"kind_declared": plan.Kind != "", "item_cost_bound": plan.Items[0].MaxEstimatedCostMicroUSD != 0,
			"study_contract_version": studyContractVersion, "calibration_bound": plan.CalibrationMaxEstimatedCostMicroUSD != 0,
			"tool_qualified": toolQualified, "automated_review": automatedReview, "live_writes": liveWrites,
			"query_only_requests": queryOnlyRequests, "cli_route_qualified": cliRouteSupported,
		}, nil
	case "private-workspace":
		workspace, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if len(workspace.RunSets) == 0 {
			return nil, fmt.Errorf("private workspace golden has no run set")
		}
		first := workspace.RunSets[0]
		automatedReview := first.QualitativeReviewPanel != nil && len(first.QualitativeReviewPanel.Executions) != 0
		return map[string]any{
			"live_config_env": workspace.LiveConfigEnv, "run_set_count": len(workspace.RunSets),
			"first_kind": first.EffectiveKind(), "first_spec_count": len(first.SpecPaths),
			"calibration_bound": first.CalibrationMaxEstimatedCostMicroUSD != 0, "automated_review": automatedReview,
		}, nil
	case "qualitative-panel":
		var panel QualitativePanelPolicy
		if err := decodeStrict(bytes.NewReader(data), &panel); err != nil {
			return nil, err
		}
		if err := panel.Validate(); err != nil {
			return nil, err
		}
		return map[string]any{
			"method": panel.Method, "expected_reviewers": panel.ExpectedReviewers,
			"max_criterion_range_bps": panel.MaxCriterionRangeBPS,
		}, nil
	case "result":
		result, err := DecodeResult(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		qualitativeMode := "none"
		if result.Qualitative != nil {
			qualitativeMode = "singleton"
		}
		if result.QualitativeReviewSet != nil {
			qualitativeMode = "panel"
		}
		coveredMetricCount := 0
		for _, covered := range result.Coverage {
			if covered {
				coveredMetricCount++
			}
		}
		return map[string]any{
			"scenario_id": result.ScenarioID, "status": result.Status, "surface": result.EffectiveSurface(),
			"eligibility": result.EffectiveEligibility(), "data_class": result.DataClass, "category": result.EffectiveCategory(),
			"qualitative_mode": qualitativeMode, "skill_activation": result.Runtime.SkillActivation,
			"prompt_bound": result.Runtime.PromptContractSHA256 != "", "evidence_state": string(result.EvidenceReport.State),
			"evidence_coverage":   result.EvidenceAttempt.Coverage && result.EvidenceReport.Coverage,
			"backend_observation": result.BackendObservation, "safety_assurance": result.SafetyAssurance,
			"covered_metric_count": coveredMetricCount,
		}, nil
	case "review":
		review, err := DecodeReview(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"rubric_id": review.RubricID, "scenario_id": review.ScenarioID,
			"reviewer_kind": review.Reviewer.Kind, "reviewer_id": review.Reviewer.ID,
			"criteria_count": len(review.Criteria),
		}, nil
	case "rubric":
		rubric, err := DecodeRubric(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"id": rubric.ID, "scenario_id": rubric.ScenarioID, "minimum_score_bps": rubric.MinimumScoreBPS,
			"criteria_count": len(rubric.Criteria),
		}, nil
	case "run-spec":
		spec, err := DecodeRunSpec(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		gatewayRouteCount := 0
		for _, routes := range spec.AllowedGatewayRoutes {
			gatewayRouteCount += len(routes)
		}
		return map[string]any{
			"backend_mode": spec.EffectiveBackendMode(), "provider": spec.Provider, "variant": spec.Variant,
			"surface": spec.EffectiveSurface(), "tool_transport": spec.EffectiveToolTransport(),
			"skill_activation": spec.SkillActivation, "repetitions": spec.Repetitions, "check_count": len(spec.Checks),
			"compatibility_subset": spec.Variant,
			"allow_live_writes":    spec.AllowLiveWrites, "cli_rule_count": len(spec.AllowedCLICommands),
			"gateway_route_count": gatewayRouteCount,
		}, nil
	case "private-review-attempt":
		var attempt privateReviewAttempt
		if decodePrivateLifecycleJSON(data, &attempt) != nil {
			return nil, fmt.Errorf("private review attempt golden is invalid")
		}
		canonical, err := encodePrivateReviewAttempt(attempt)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("private review attempt golden is not canonical")
		}
		return map[string]any{
			"schema_version": attempt.SchemaVersion, "reviewer_kind": attempt.ReviewerKind,
			"attempt_bound": attempt.AttemptBindingSHA256 != "",
		}, nil
	case "private-review-receipt":
		var receipt privateReviewReceipt
		if decodePrivateLifecycleJSON(data, &receipt) != nil {
			return nil, fmt.Errorf("private review receipt golden is invalid")
		}
		canonical, err := encodePrivateReviewReceipt(receipt, PrivateReviewerExecution{})
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("private review receipt golden is not canonical")
		}
		return map[string]any{
			"schema_version": receipt.SchemaVersion, "reviewer_kind": receipt.ReviewerKind,
			"status": receipt.Status, "attempt_bound": receipt.AttemptBindingSHA256 != "",
			"cost_known": receipt.CostKnown, "model_requests": receipt.ModelRequests,
		}, nil
	case "scenario":
		scenario, err := DecodeScenario(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"id": scenario.ID, "category": scenario.EffectiveCategory(), "task_class": scenario.TaskClass,
			"data_class": scenario.DataClass, "required_check_count": len(scenario.RequiredChecks),
			"required_metric_count": len(scenario.RequiredMetrics),
		}, nil
	case "synthetic-run-receipt":
		receipt, err := DecodeSyntheticRunReceipt(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"schema_version": receipt.SchemaVersion, "scenario_id": receipt.ScenarioID, "provider": receipt.Provider, "variant": receipt.Variant,
			"repetition": receipt.Repetition, "repetitions": receipt.Repetitions,
			"attempt_bound": receipt.AttemptBindingSHA256 != "",
		}, nil
	default:
		return nil, fmt.Errorf("unsupported readability golden kind %q", entry.Kind)
	}
}

func standaloneDecodeExtensionReadabilityProjection(entry standaloneReadabilityGoldenEntry, data []byte) (map[string]any, error) {
	switch entry.Kind {
	case "adapter-manifest":
		manifest, err := extension.DecodeManifest(data)
		if err != nil {
			return nil, err
		}
		canonical, err := extension.EncodeManifest(manifest)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("adapter manifest golden is not canonical")
		}
		return map[string]any{
			"schema": manifest.Schema, "schema_version": manifest.SchemaVersion,
			"contract_version": manifest.ContractVersion, "protocol_version": manifest.ProtocolVersions[0],
			"component_id": manifest.Component.ID, "component_version": manifest.Component.Version,
			"role": manifest.Component.Role, "operation_count": len(manifest.Component.Operations),
			"capability_count": len(manifest.Component.Capabilities), "configuration_count": len(manifest.ConfigurationSchema),
			"platform_count": len(manifest.Platforms), "requirement_count": len(manifest.Requirements),
		}, nil
	case "adapter-message":
		frame, err := extension.DecodeFrameLine(data)
		if err != nil {
			return nil, err
		}
		if frame.Type != extension.MessageInitialize || frame.Initialize == nil {
			return nil, fmt.Errorf("adapter message golden is not an initialize frame")
		}
		canonical, err := extension.EncodeFrameLine(frame)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("adapter message golden is not canonical")
		}
		return map[string]any{
			"schema": frame.Schema, "schema_version": frame.SchemaVersion, "protocol_version": frame.ProtocolVersion,
			"direction": frame.Direction, "sequence": frame.Sequence, "role": frame.Role, "type": frame.Type,
			"component_id": frame.ComponentID, "component_version": frame.ComponentVersion,
			"required_capability_count": len(frame.Initialize.RequiredCapabilities),
		}, nil
	case "attempt-event":
		event, err := lifecycle.DecodeEvent(data)
		if err != nil {
			return nil, err
		}
		canonical, err := lifecycle.EncodeEvent(event)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("attempt event golden is not canonical")
		}
		return map[string]any{
			"schema": event.Schema, "schema_version": event.SchemaVersion, "sequence": event.Sequence,
			"from": event.From, "to": event.To, "proof_count": len(event.Proofs),
			"error_class": event.Evidence.ErrorClass, "usage_state": event.Evidence.Usage.InputTokens.State,
		}, nil
	case "attempt-ledger":
		header, err := lifecycle.DecodeHeader(data)
		if err != nil {
			return nil, err
		}
		canonical, err := lifecycle.EncodeHeader(header)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("attempt ledger golden is not canonical")
		}
		return map[string]any{
			"schema": header.Schema, "schema_version": header.SchemaVersion,
			"contract_version": header.ContractVersion, "ledger_id": header.LedgerID,
			"header_digest_bound": header.HeaderSHA256 != "",
		}, nil
	case "attempt-plan":
		plan, err := lifecycle.DecodePlan(data)
		if err != nil {
			return nil, err
		}
		canonical, err := lifecycle.EncodePlan(plan)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("attempt plan golden is not canonical")
		}
		return map[string]any{
			"schema": plan.Schema, "schema_version": plan.SchemaVersion, "ordinal": plan.Ordinal,
			"privacy": plan.Binding.Privacy, "identity_count": 10, "reconciled": plan.PredecessorAttemptID != "",
			"binding_digest_bound": plan.BindingSHA256 != "", "plan_digest_bound": plan.PlanSHA256 != "",
		}, nil
	case "extension-conformance-bundle":
		bundle, err := DecodeExtensionConformanceBundle(data)
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeExtensionConformanceBundle(bundle)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("extension conformance bundle golden is not canonical")
		}
		canceledCases := 0
		for _, testCase := range bundle.Cases {
			if testCase.Expected.Type == "canceled" {
				canceledCases++
			}
		}
		return map[string]any{
			"schema": bundle.Schema, "schema_version": bundle.SchemaVersion,
			"contract_version": bundle.ContractVersion, "protocol_version": bundle.ProtocolVersion,
			"case_count": len(bundle.Cases), "canceled_case_count": canceledCases,
			"first_role":      bundle.Cases[0].Role,
			"first_operation": bundle.Cases[0].Operation,
			"last_operation":  bundle.Cases[len(bundle.Cases)-1].Operation,
		}, nil
	case "extension-conformance-report":
		report, err := DecodeExtensionConformanceReport(data)
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeExtensionConformanceReport(report)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("extension conformance report golden is not canonical")
		}
		canceledCases := 0
		for _, testCase := range report.Cases {
			if testCase.Terminal == "canceled" {
				canceledCases++
			}
		}
		return map[string]any{
			"schema": report.Schema, "schema_version": report.SchemaVersion, "scope": report.Scope,
			"contract_version": report.ContractVersion, "protocol_version": report.ProtocolVersion,
			"component_id": report.ComponentID, "component_version": report.ComponentVersion, "role": report.Role,
			"capability_count": len(report.Capabilities), "cleanup_assurance": report.CleanupAssurance,
			"case_count": len(report.Cases), "canceled_case_count": canceledCases,
			"first_operation":     report.Cases[0].Operation,
			"last_operation":      report.Cases[len(report.Cases)-1].Operation,
			"protocol_conformant": report.ProtocolConformant,
		}, nil
	case "migration-preview":
		preview, err := DecodeStandaloneMigrationPreview(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeStandaloneMigrationPreview(preview)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("migration preview golden is not canonical")
		}
		return map[string]any{
			"schema": preview.Schema, "schema_version": preview.SchemaVersion, "status": preview.Status,
			"namespace": preview.Namespace, "kind": preview.Kind, "from": preview.From, "to": preview.To,
			"privacy": preview.Privacy, "count_count": len(preview.Counts), "preview_digest_bound": preview.PreviewSHA256 != "",
			"registry_digest_bound": preview.RegistrySHA256 != "", "implementation_digest_bound": preview.ImplementationSHA256 != "",
		}, nil
	case "migration-result":
		result, err := DecodeStandaloneMigrationResult(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeStandaloneMigrationResult(result)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("migration result golden is not canonical")
		}
		return map[string]any{
			"schema": result.Schema, "schema_version": result.SchemaVersion, "status": result.Status,
			"namespace": result.Namespace, "kind": result.Kind, "from": result.From, "to": result.To,
			"preview_digest_bound": result.PreviewSHA256 != "", "registry_digest_bound": result.RegistrySHA256 != "",
			"implementation_digest_bound": result.ImplementationSHA256 != "",
		}, nil
	case "project-config":
		config, err := DecodeStandaloneProjectConfig(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeStandaloneProjectConfig(config)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("project config golden is not canonical")
		}
		repetitions := 0
		if config.Repetitions != nil {
			repetitions = *config.Repetitions
		}
		return map[string]any{
			"schema": config.Schema, "schema_version": config.SchemaVersion,
			"contract_version": config.ContractVersion, "profile_configured": config.Profile != nil,
			"model_configured": config.Model != nil, "repetitions": repetitions,
		}, nil
	case "schema-registry":
		registry, err := DecodeStandaloneSchemaRegistry(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeStandaloneSchemaRegistry(registry)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("schema registry golden is not canonical")
		}
		edgeCount := 0
		for _, entry := range registry.Entries {
			edgeCount += len(entry.MigrationEdges)
		}
		return map[string]any{
			"schema": registry.Schema, "schema_version": registry.SchemaVersion,
			"contract_version": registry.ContractVersion, "entry_count": len(registry.Entries),
			"first_schema":         registry.Entries[0].Namespace + "/" + registry.Entries[0].Kind,
			"last_schema":          registry.Entries[len(registry.Entries)-1].Namespace + "/" + registry.Entries[len(registry.Entries)-1].Kind,
			"migration_edge_count": edgeCount,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported standalone readability golden kind %q", entry.Kind)
	}
}

func standaloneLoadActivationReferenceGolden(t *testing.T, entry standaloneReadabilityGoldenEntry, data []byte) (privateStoredActivationReference, error) {
	t.Helper()
	planData := standaloneReadGoldenSource(t, entry.ReaderSupportPath, entry.ReaderSupportSHA256)
	var placement privateStoredActivationReference
	if err := json.Unmarshal(data, &placement); err != nil || !validPrivateActivationReferenceAlias(placement.ReferenceAlias) ||
		!privatePlanIDRE.MatchString(placement.PlanID) {
		return privateStoredActivationReference{}, fmt.Errorf("activation reference golden placement is invalid")
	}
	root := t.TempDir()
	for _, directory := range []string{filepath.Join(root, "plans"), filepath.Join(root, "baselines"), filepath.Join(root, "baselines", "activation-studies")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return privateStoredActivationReference{}, fmt.Errorf("create activation reference golden directory: %w", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return privateStoredActivationReference{}, fmt.Errorf("protect activation reference golden directory: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "plans", placement.PlanID+".json"), planData, 0o600); err != nil {
		return privateStoredActivationReference{}, fmt.Errorf("write activation reference golden plan: %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, "baselines", "activation-studies", placement.ReferenceAlias+".json"), data, 0o600); err != nil {
		return privateStoredActivationReference{}, fmt.Errorf("write activation reference golden: %w", err)
	}
	stored, loadedData, err := loadPrivateStoredActivationReference(root, placement.ReferenceAlias)
	if err != nil {
		return privateStoredActivationReference{}, err
	}
	if !bytes.Equal(loadedData, data) {
		return privateStoredActivationReference{}, fmt.Errorf("activation reference reader changed golden bytes")
	}
	return stored, nil
}
