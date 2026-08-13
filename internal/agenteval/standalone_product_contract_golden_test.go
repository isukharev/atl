package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/analysis"
	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/experiment"
	"github.com/isukharev/atl/internal/agenteval/extension"
	"github.com/isukharev/atl/internal/agenteval/grading"
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
		if entry.Kind == "activation-reference" || entry.Kind == "agent-observation" || entry.Kind == "analysis-report" || entry.Kind == "grade-receipt" ||
			entry.Kind == "grading-plan" || entry.Kind == "scheduler-report" || entry.Kind == "trial-plan" || entry.Kind == "trial-receipt" || entry.Kind == "trial-record" {
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
	standaloneValidateAgentAdapterGoldenBindings(t, fixture)
	standaloneValidateExecutionBackendGoldenBindings(t, fixture)
	standaloneValidateGradingGoldenBindings(t, fixture)
	standaloneValidateExperimentGoldenBindings(t, fixture)
	standaloneValidateSchedulerGoldenBindings(t, fixture)
	return fixture
}

func standaloneValidateSchedulerGoldenBindings(t *testing.T, fixture standaloneReadabilityGoldenFixture) {
	t.Helper()
	planEntry, planOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "scheduler-plan", 1))
	reportEntry, reportOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "scheduler-report", 1))
	if !planOK || !reportOK || reportEntry.ReaderSupportPath != planEntry.SourcePath ||
		reportEntry.ReaderSupportSHA256 != planEntry.SourceSHA256 {
		t.Fatal("standalone scheduler readability sources are not transitively bound")
	}
	plan, err := DecodeSchedulerPlan(bytes.NewReader(standaloneGoldenDocument(t, planEntry)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSchedulerReport(bytes.NewReader(standaloneGoldenDocument(t, reportEntry)), plan); err != nil {
		t.Fatal(err)
	}
}

func standaloneValidateExperimentGoldenBindings(t *testing.T, fixture standaloneReadabilityGoldenFixture) {
	t.Helper()
	capabilityEntry, capabilityOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "experiment-capability-contract", 1))
	designEntry, designOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "experiment-design", 1))
	analysisEntry, analysisOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "analysis-plan", 1))
	reportEntry, reportOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "analysis-report", 1))
	manifestEntry, manifestOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "experiment-manifest", 1))
	recordEntry, recordOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "trial-record", 1))
	if !capabilityOK || !designOK || !analysisOK || !reportOK || !manifestOK || !recordOK ||
		reportEntry.ReaderSupportPath != manifestEntry.SourcePath || reportEntry.ReaderSupportSHA256 != manifestEntry.SourceSHA256 ||
		recordEntry.ReaderSupportPath != manifestEntry.SourcePath || recordEntry.ReaderSupportSHA256 != manifestEntry.SourceSHA256 {
		t.Fatal("standalone experiment readability sources are not transitively bound")
	}
	capability, err := DecodeExperimentCapabilityContract(bytes.NewReader(standaloneGoldenDocument(t, capabilityEntry)))
	if err != nil {
		t.Fatal(err)
	}
	design, err := DecodeExperimentDesign(bytes.NewReader(standaloneGoldenDocument(t, designEntry)))
	if err != nil {
		t.Fatal(err)
	}
	analysisPlan, err := DecodeExperimentAnalysisPlan(bytes.NewReader(standaloneGoldenDocument(t, analysisEntry)))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeExperimentManifest(bytes.NewReader(standaloneGoldenDocument(t, manifestEntry)))
	if err != nil {
		t.Fatal(err)
	}
	encodedCapability, capabilityErr := EncodeExperimentCapabilityContract(manifest.CapabilityContract)
	encodedDesign, designErr := EncodeExperimentDesign(manifest.Design)
	encodedAnalysis, analysisErr := EncodeExperimentAnalysisPlan(manifest.AnalysisPlan)
	if capabilityErr != nil || designErr != nil || analysisErr != nil ||
		!bytes.Equal(encodedCapability, standaloneGoldenDocument(t, capabilityEntry)) ||
		!bytes.Equal(encodedDesign, standaloneGoldenDocument(t, designEntry)) ||
		!bytes.Equal(encodedAnalysis, standaloneGoldenDocument(t, analysisEntry)) ||
		design.CapabilityContractSHA256 != capability.CapabilityContractSHA256 || design.AnalysisPlanSHA256 != analysisPlan.AnalysisPlanSHA256 {
		t.Fatal("standalone experiment manifest does not bind its exact source contracts")
	}
	record, err := DecodeExperimentTrialRecord(bytes.NewReader(standaloneGoldenDocument(t, recordEntry)), manifest)
	if err != nil {
		t.Fatalf("decode bound trial record: %v", err)
	}
	report, err := analysis.Analyze(manifest, standaloneAnalysisGoldenRecords(t, manifest, record))
	if err != nil {
		t.Fatalf("analyze bound trial record: %v", err)
	}
	encodedReport, err := analysis.EncodeReport(report, manifest)
	if err != nil || !bytes.Equal(encodedReport, standaloneGoldenDocument(t, reportEntry)) {
		t.Fatal("standalone analysis report is not generated from its bound manifest and trial record")
	}
}

func standaloneAnalysisGoldenRecords(t *testing.T, manifest ExperimentManifest, exemplar ExperimentTrialRecord) []ExperimentTrialRecord {
	t.Helper()
	roles := make(map[string]experiment.TreatmentRole, len(manifest.Treatments))
	for _, treatment := range manifest.Treatments {
		roles[treatment.ID] = treatment.Role
	}
	records := make([]ExperimentTrialRecord, 0, len(manifest.Blocks)*len(manifest.Treatments))
	foundExemplar := false
	for _, block := range manifest.Blocks {
		for _, assignment := range block.Assignments {
			if assignment.TrialID == exemplar.TrialID {
				records = append(records, exemplar)
				foundExemplar = true
				continue
			}
			outcome := uint64(0)
			if block.Ordinal == 2 {
				outcome = 1
			}
			stages := make([]experiment.StageObservation, 0, len(manifest.AnalysisPlan.Stages))
			for _, declaration := range manifest.AnalysisPlan.Stages {
				value := false
				if declaration.Stage == experiment.StageVerifierOutcome {
					value = outcome == 1
				}
				stages = append(stages, experiment.StageObservation{Stage: declaration.Stage, Presence: experiment.PresenceObserved, Value: &value})
			}
			duration := uint64(10)
			if roles[assignment.TreatmentID] == experiment.RoleCandidate {
				duration = 20
			}
			metrics := make([]experiment.MetricObservation, 0, len(manifest.AnalysisPlan.Metrics))
			for _, declaration := range manifest.AnalysisPlan.Metrics {
				value := outcome
				if declaration.ID == experiment.MetricDurationMillis {
					value = duration
				}
				metrics = append(metrics, experiment.MetricObservation{Metric: declaration.ID, Presence: experiment.PresenceObserved, Value: &value})
			}
			state := experiment.LifecycleFailed
			if outcome == 1 {
				state = experiment.LifecycleSucceeded
			}
			record, err := experiment.SealTrialRecord(manifest, experiment.TrialRecord{
				TrialID: assignment.TrialID, BlockID: block.ID, TreatmentID: assignment.TreatmentID,
				AttemptPlanSHA256: rootExperimentDigest("analysis-attempt-" + assignment.TrialID), LifecycleState: state,
				Eligibility: experiment.EligibilitySupported, Exclusion: experiment.ExclusionNone,
				AgentObservationSHA256: rootExperimentDigest("analysis-observation-" + assignment.TrialID),
				GradeReceiptSHA256:     rootExperimentDigest("analysis-grade-" + assignment.TrialID),
				LifecycleEventSHA256:   rootExperimentDigest("analysis-lifecycle-" + assignment.TrialID),
				Stages:                 stages, Metrics: metrics,
			})
			if err != nil {
				t.Fatal(err)
			}
			records = append(records, record)
		}
	}
	if !foundExemplar || len(records) != len(manifest.Blocks)*len(manifest.Treatments) {
		t.Fatalf("analysis golden roster exemplar=%t records=%d", foundExemplar, len(records))
	}
	return records
}

func standaloneValidateGradingGoldenBindings(t *testing.T, fixture standaloneReadabilityGoldenFixture) {
	t.Helper()
	contractEntry, contractOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "grader-contract", 1))
	planEntry, planOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "grading-plan", 1))
	receiptEntry, receiptOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "grade-receipt", 1))
	if !contractOK || !planOK || !receiptOK || planEntry.ReaderSupportPath != contractEntry.SourcePath ||
		planEntry.ReaderSupportSHA256 != contractEntry.SourceSHA256 || receiptEntry.ReaderSupportPath != planEntry.SourcePath ||
		receiptEntry.ReaderSupportSHA256 != planEntry.SourceSHA256 {
		t.Fatal("standalone grading readability sources are not transitively bound")
	}
	contract, err := grading.DecodeContract(bytes.NewReader(standaloneGoldenDocument(t, contractEntry)))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := grading.DecodePlan(bytes.NewReader(standaloneGoldenDocument(t, planEntry)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grading.Admit(contract, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := grading.DecodeReceipt(bytes.NewReader(standaloneGoldenDocument(t, receiptEntry)), plan); err != nil {
		t.Fatal(err)
	}
}

func standaloneValidateExecutionBackendGoldenBindings(t *testing.T, fixture standaloneReadabilityGoldenFixture) {
	t.Helper()
	contractEntry, contractOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "execution-backend-contract", 1))
	planEntry, planOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "trial-plan", 1))
	receiptEntry, receiptOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "trial-receipt", 1))
	if !contractOK || !planOK || !receiptOK || planEntry.ReaderSupportPath != contractEntry.SourcePath ||
		planEntry.ReaderSupportSHA256 != contractEntry.SourceSHA256 || receiptEntry.ReaderSupportPath != planEntry.SourcePath ||
		receiptEntry.ReaderSupportSHA256 != planEntry.SourceSHA256 {
		t.Fatal("standalone execution backend readability sources are not transitively bound")
	}
	contract, err := executionbackend.DecodeContract(bytes.NewReader(standaloneGoldenDocument(t, contractEntry)))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := executionbackend.DecodePlan(bytes.NewReader(standaloneGoldenDocument(t, planEntry)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executionbackend.Admit(contract, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := executionbackend.DecodeReceipt(bytes.NewReader(standaloneGoldenDocument(t, receiptEntry)), plan); err != nil {
		t.Fatal(err)
	}
}

func standaloneValidateAgentAdapterGoldenBindings(t *testing.T, fixture standaloneReadabilityGoldenFixture) {
	t.Helper()
	contractEntry, contractOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "agent-adapter-contract", 1))
	observationEntry, observationOK := standaloneReadabilityGoldenEntryFor(fixture, standaloneVersionedContractKey("standalone", "agent-observation", 1))
	if !contractOK || !observationOK || observationEntry.ReaderSupportPath != contractEntry.SourcePath ||
		observationEntry.ReaderSupportSHA256 != contractEntry.SourceSHA256 {
		t.Fatal("standalone agent adapter readability sources are not transitively bound")
	}
	contract, err := DecodeAgentAdapterContract(bytes.NewReader(standaloneGoldenDocument(t, contractEntry)))
	if err != nil {
		t.Fatalf("decode bound agent adapter contract: %v", err)
	}
	observation, err := DecodeAgentAdapterObservation(bytes.NewReader(standaloneGoldenDocument(t, observationEntry)), contract)
	if err != nil {
		t.Fatalf("decode bound agent adapter observation: %v", err)
	}
	contractSHA256, err := AgentAdapterContractSHA256(contract)
	if err != nil || observation.AdapterContractSHA256 != contractSHA256 {
		t.Fatal("standalone agent adapter observation does not bind its contract")
	}
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
	if entry.Namespace == "standalone" && entry.Kind == "analysis-report" {
		data, err := standaloneFutureAnalysisReportGolden(t, entry, version)
		if err != nil {
			return err
		}
		_, err = standaloneDecodeReadabilityProjection(t, entry, data)
		return err
	}
	if entry.Namespace == "standalone" && standaloneExperimentGoldenKind(entry.Kind) {
		data, err := standaloneFutureExperimentGolden(t, entry, version)
		if err != nil {
			return err
		}
		_, err = standaloneDecodeReadabilityProjection(t, entry, data)
		return err
	}
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

func standaloneFutureAnalysisReportGolden(t *testing.T, entry standaloneReadabilityGoldenEntry, version int) ([]byte, error) {
	t.Helper()
	manifest, err := standaloneAnalysisGoldenManifest(t, entry)
	if err != nil {
		return nil, err
	}
	report, err := DecodeAnalysisReport(bytes.NewReader(standaloneGoldenDocument(t, entry)), manifest)
	if err != nil {
		return nil, err
	}
	report.SchemaVersion = version
	report.ReportSHA256 = ""
	data, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	report.ReportSHA256 = standaloneAnalysisIdentity("report", data)
	encoded, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func standaloneAnalysisGoldenManifest(t *testing.T, entry standaloneReadabilityGoldenEntry) (ExperimentManifest, error) {
	t.Helper()
	if entry.ReaderSupportPath == "" || entry.ReaderSupportSHA256 == "" {
		return ExperimentManifest{}, fmt.Errorf("analysis report has no manifest reader support")
	}
	return DecodeExperimentManifest(bytes.NewReader(standaloneReadGoldenSource(t, entry.ReaderSupportPath, entry.ReaderSupportSHA256)))
}

func standaloneAnalysisIdentity(domain string, projection []byte) string {
	hash := sha256.New()
	for _, part := range [][]byte{[]byte("agent-eval/analysis/v1"), []byte(domain), projection} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(part)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func standaloneExperimentGoldenKind(kind string) bool {
	switch kind {
	case "analysis-plan", "experiment-capability-contract", "experiment-design", "experiment-manifest", "trial-record":
		return true
	default:
		return false
	}
}

func standaloneFutureExperimentGolden(t *testing.T, entry standaloneReadabilityGoldenEntry, version int) ([]byte, error) {
	t.Helper()
	data := standaloneGoldenDocument(t, entry)
	var value any
	var domain string
	switch entry.Kind {
	case "analysis-plan":
		plan, err := DecodeExperimentAnalysisPlan(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		plan.SchemaVersion = version
		plan.AnalysisPlanSHA256 = ""
		plan.AnalysisPlanSHA256 = standaloneExperimentIdentity("analysis-plan", plan)
		value, domain = plan, "analysis-plan"
	case "experiment-capability-contract":
		contract, err := DecodeExperimentCapabilityContract(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		contract.SchemaVersion = version
		contract.CapabilityContractSHA256 = ""
		contract.CapabilityContractSHA256 = standaloneExperimentIdentity("capability-contract", contract)
		value, domain = contract, "capability-contract"
	case "experiment-design":
		design, err := DecodeExperimentDesign(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		design.SchemaVersion = version
		design.DesignSHA256 = ""
		design.DesignSHA256 = standaloneExperimentIdentity("design", design)
		value, domain = design, "design"
	case "experiment-manifest":
		manifest, err := DecodeExperimentManifest(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		manifest.SchemaVersion = version
		manifest.ManifestSHA256 = ""
		manifest.ManifestSHA256 = standaloneExperimentIdentity("manifest", manifest)
		value, domain = manifest, "manifest"
	case "trial-record":
		manifestData := standaloneReadGoldenSource(t, entry.ReaderSupportPath, entry.ReaderSupportSHA256)
		manifest, err := DecodeExperimentManifest(bytes.NewReader(manifestData))
		if err != nil {
			return nil, err
		}
		record, err := DecodeExperimentTrialRecord(bytes.NewReader(data), manifest)
		if err != nil {
			return nil, err
		}
		record.SchemaVersion = version
		record.RecordSHA256 = ""
		record.RecordSHA256 = standaloneExperimentIdentity("trial-record", record)
		value, domain = record, "trial-record"
	default:
		return nil, fmt.Errorf("unsupported experiment future golden %q", entry.Kind)
	}
	encoded, err := json.Marshal(value)
	if err != nil || domain == "" {
		return nil, fmt.Errorf("encode experiment future golden: %w", err)
	}
	return append(encoded, '\n'), nil
}

func standaloneExperimentIdentity(domain string, projection any) string {
	data, err := json.Marshal(projection)
	if err != nil {
		panic(err)
	}
	hash := sha256.New()
	for _, part := range [][]byte{[]byte("agent-eval/experiment/v1"), []byte(domain), data} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(part)
	}
	return hex.EncodeToString(hash.Sum(nil))
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
	case "adapter-manifest", "adapter-message", "agent-adapter-contract", "agent-observation", "analysis-plan", "analysis-report", "attempt-event", "attempt-ledger", "attempt-plan", "execution-backend-contract", "experiment-capability-contract", "experiment-design", "experiment-manifest", "extension-conformance-bundle", "extension-conformance-report", "grade-receipt", "grader-contract", "grading-plan", "migration-preview", "migration-result", "project-config", "scheduler-plan", "scheduler-report", "sequential-reference-bundle", "trial-plan", "trial-receipt", "trial-record":
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
	if entry.Namespace == "standalone" && entry.Kind == "trial-plan" && entry.Version == 1 {
		return entry.ReaderSupportPath == "testdata/standalone-readability/execution-backend-contract-v1.json"
	}
	if entry.Namespace == "standalone" && entry.Kind == "trial-receipt" && entry.Version == 1 {
		return entry.ReaderSupportPath == "testdata/standalone-readability/trial-plan-v1.json"
	}
	if entry.Namespace == "standalone" && entry.Kind == "trial-record" && entry.Version == 1 {
		return entry.ReaderSupportPath == "testdata/standalone-readability/experiment-manifest-v1.json"
	}
	if entry.Namespace == "standalone" && entry.Kind == "analysis-report" && entry.Version == 1 {
		return entry.ReaderSupportPath == "testdata/standalone-readability/experiment-manifest-v1.json"
	}
	if entry.Namespace == "standalone" && entry.Kind == "grading-plan" && entry.Version == 1 {
		return entry.ReaderSupportPath == "testdata/standalone-readability/grader-contract-v1.json"
	}
	if entry.Namespace == "standalone" && entry.Kind == "grade-receipt" && entry.Version == 1 {
		return entry.ReaderSupportPath == "testdata/standalone-readability/grading-plan-v1.json"
	}
	if entry.Namespace == "standalone" && entry.Kind == "scheduler-report" && entry.Version == 1 {
		return entry.ReaderSupportPath == "testdata/standalone-readability/scheduler-plan-v1.json"
	}
	if entry.Namespace == "standalone" && entry.Kind == "agent-observation" && entry.Version == 1 {
		return entry.ReaderSupportPath == "testdata/standalone-readability/agent-adapter-contract-v1.json"
	}
	want := map[int]string{
		LegacyPrivateActivationReferenceSchemaVersion: "testdata/standalone-readability/private-plan-v4.json",
		PrivateActivationReferenceSchemaVersion:       "testdata/standalone-readability/activation-plan-v9.json",
	}
	return entry.ReaderSupportPath == want[entry.Version]
}

func standaloneDecodeReadabilityProjection(t *testing.T, entry standaloneReadabilityGoldenEntry, data []byte) (map[string]any, error) {
	t.Helper()
	if entry.Namespace == "standalone" {
		return standaloneDecodeExtensionReadabilityProjection(t, entry, data)
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

func standaloneDecodeExtensionReadabilityProjection(t *testing.T, entry standaloneReadabilityGoldenEntry, data []byte) (map[string]any, error) {
	t.Helper()
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
	case "agent-adapter-contract":
		contract, err := DecodeAgentAdapterContract(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeAgentAdapterContract(contract)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("agent adapter contract golden is not canonical")
		}
		supported, sensitive := 0, 0
		for _, capability := range contract.Capabilities {
			if capability.Support == "supported" {
				supported++
			}
		}
		for _, key := range contract.ConfigurationKeys {
			if key.Sensitive {
				sensitive++
			}
		}
		return map[string]any{
			"schema": contract.Schema, "schema_version": contract.SchemaVersion,
			"contract_version": contract.ContractVersion, "adapter_id": contract.AdapterID,
			"adapter_version": contract.AdapterVersion, "capability_count": len(contract.Capabilities),
			"supported_capability_count": supported, "configuration_key_count": len(contract.ConfigurationKeys),
			"sensitive_configuration_key_count": sensitive,
		}, nil
	case "agent-observation":
		contractData := standaloneReadGoldenSource(t, entry.ReaderSupportPath, entry.ReaderSupportSHA256)
		contract, err := DecodeAgentAdapterContract(bytes.NewReader(contractData))
		if err != nil {
			return nil, err
		}
		observation, err := DecodeAgentAdapterObservation(bytes.NewReader(data), contract)
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeAgentAdapterObservation(contract, observation)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("agent observation golden is not canonical")
		}
		return map[string]any{
			"schema": observation.Schema, "schema_version": observation.SchemaVersion,
			"contract_version": observation.ContractVersion, "attempt_bound": observation.AttemptID != "",
			"adapter_contract_bound": observation.AdapterContractSHA256 != "", "profile": observation.Profile,
			"event_count": len(observation.Events), "coverage": observation.Coverage, "issue_count": len(observation.Issues),
			"parent_input_state":            observation.ParentUsage.InputTokens.State,
			"parent_input_value":            *observation.ParentUsage.InputTokens.Value,
			"tree_input_state":              observation.TreeUsage.InputTokens.State,
			"tree_input_value":              *observation.TreeUsage.InputTokens.Value,
			"consumed_child_evidence_state": observation.ConsumedChildEvidence.State,
		}, nil
	case "analysis-plan":
		plan, err := DecodeExperimentAnalysisPlan(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeExperimentAnalysisPlan(plan)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("analysis plan golden is not canonical")
		}
		primary := ""
		primaryStage := ""
		for _, stage := range plan.Stages {
			if stage.Role == "primary" {
				primaryStage = string(stage.Stage)
			}
		}
		for _, metric := range plan.Metrics {
			if metric.Role == "primary" {
				primary = string(metric.ID)
			}
		}
		return map[string]any{
			"schema": plan.Schema, "schema_version": plan.SchemaVersion, "contract_version": plan.ContractVersion,
			"confidence_basis_points": plan.ConfidenceBasisPoints, "minimum_inference_blocks": plan.MinimumInferenceBlocks,
			"bootstrap_samples": plan.BootstrapSamples, "repeated_attempt_kind": plan.RepeatedAttempts.Kind,
			"stage_count": len(plan.Stages), "metric_count": len(plan.Metrics), "comparison_count": len(plan.Comparisons),
			"primary_stage": primaryStage, "primary_metric": primary,
			"digest_bound": plan.AnalysisPlanSHA256 != "",
		}, nil
	case "analysis-report":
		manifest, err := standaloneAnalysisGoldenManifest(t, entry)
		if err != nil {
			return nil, err
		}
		report, err := DecodeAnalysisReport(bytes.NewReader(data), manifest)
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeAnalysisReport(report, manifest)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("analysis report golden is not canonical")
		}
		activationObserved := uint32(0)
		retainedBinaryPairs, retainedContinuousDeltas, inferentialResults := 0, 0, 0
		trialStageProjections, trialMetricProjections := 0, 0
		for _, member := range report.Coverage.Members {
			trialStageProjections += len(member.Stages)
			trialMetricProjections += len(member.Metrics)
		}
		for _, summary := range report.Activation {
			activationObserved += summary.Observed
		}
		for _, comparison := range report.Comparisons {
			for _, result := range comparison.Binary {
				retainedBinaryPairs += len(result.Pairs)
				if result.Status == analysis.InferenceInferential {
					inferentialResults++
				}
			}
			for _, result := range comparison.Continuous {
				retainedContinuousDeltas += len(result.Deltas)
				if result.Status == analysis.InferenceInferential {
					inferentialResults++
				}
			}
		}
		return map[string]any{
			"schema": report.Schema, "schema_version": report.SchemaVersion, "contract_version": report.ContractVersion,
			"confidence_basis_points": report.ConfidenceBasisPoints, "minimum_inference_blocks": report.MinimumInferenceBlocks,
			"bootstrap_samples": report.BootstrapSamples, "multiplicity": report.Multiplicity,
			"expected_records": report.Coverage.ExpectedRecords, "received_records": report.Coverage.ReceivedRecords,
			"unique_records": report.Coverage.UniqueRecords, "missing_records": report.Coverage.MissingRecords,
			"duplicate_records": report.Coverage.DuplicateRecords, "complete_pairs": report.Coverage.CompletePairs,
			"excluded_pairs": report.Coverage.ExcludedPairs, "member_count": len(report.Coverage.Members), "pair_count": len(report.Coverage.Pairs),
			"reason_count": len(report.Coverage.Reasons), "comparison_count": len(report.Comparisons),
			"trial_stage_projection_count": trialStageProjections, "trial_metric_projection_count": trialMetricProjections,
			"retained_binary_pair_count": retainedBinaryPairs, "retained_continuous_delta_count": retainedContinuousDeltas,
			"inferential_result_count": inferentialResults,
			"activation_strata_count":  len(report.Activation), "activation_observed": activationObserved, "funnel_count": len(report.Funnels),
			"pass_at_k_count": len(report.PassAtK), "manifest_bound": report.ManifestSHA256 != "",
			"analysis_plan_bound": report.AnalysisPlanSHA256 != "", "input_set_bound": report.InputSetSHA256 != "",
			"report_digest_bound": report.ReportSHA256 != "",
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
	case "execution-backend-contract":
		contract, err := executionbackend.DecodeContract(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		canonical, err := executionbackend.EncodeContract(contract)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("execution backend contract golden is not canonical")
		}
		supported := 0
		for _, capability := range contract.Capabilities {
			if capability.Support == executionbackend.SupportSupported {
				supported++
			}
		}
		return map[string]any{"schema": contract.Schema, "schema_version": contract.SchemaVersion, "contract_version": contract.ContractVersion,
			"backend_id": contract.BackendID, "backend_version": contract.BackendVersion, "assurance": contract.Assurance,
			"capability_count": len(contract.Capabilities), "supported_capability_count": supported}, nil
	case "experiment-capability-contract":
		contract, err := DecodeExperimentCapabilityContract(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeExperimentCapabilityContract(contract)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("experiment capability contract golden is not canonical")
		}
		supported := 0
		for _, capability := range contract.Capabilities {
			if capability.Support == "supported" {
				supported++
			}
		}
		return map[string]any{
			"schema": contract.Schema, "schema_version": contract.SchemaVersion, "contract_version": contract.ContractVersion,
			"runtime_binding_count": 9, "capability_count": len(contract.Capabilities), "supported_capability_count": supported,
			"digest_bound": contract.CapabilityContractSHA256 != "",
		}, nil
	case "experiment-design":
		design, err := DecodeExperimentDesign(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeExperimentDesign(design)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("experiment design golden is not canonical")
		}
		negativeControls, blocks := 0, uint32(0)
		for _, treatment := range design.Treatments {
			if treatment.Arm.Control != "positive" {
				negativeControls++
			}
		}
		for _, stratum := range design.Strata {
			blocks += stratum.Blocks
		}
		return map[string]any{
			"schema": design.Schema, "schema_version": design.SchemaVersion, "contract_version": design.ContractVersion,
			"compatibility_profile": design.CompatibilityProfile, "treatment_count": len(design.Treatments),
			"negative_control_count": negativeControls, "stratum_count": len(design.Strata), "block_count": blocks,
			"ordering": design.Ordering.Kind, "stopping": design.Stopping.Kind, "digest_bound": design.DesignSHA256 != "",
		}, nil
	case "experiment-manifest":
		manifest, err := DecodeExperimentManifest(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeExperimentManifest(manifest)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("experiment manifest golden is not canonical")
		}
		trials := 0
		for _, block := range manifest.Blocks {
			trials += len(block.Assignments)
		}
		return map[string]any{
			"schema": manifest.Schema, "schema_version": manifest.SchemaVersion, "contract_version": manifest.ContractVersion,
			"required_capability_count": len(manifest.RequiredCapabilities), "treatment_count": len(manifest.Treatments),
			"block_count": len(manifest.Blocks), "trial_count": trials, "pair_count": len(manifest.Pairs),
			"position_balance_complete": manifest.PositionBalanceComplete,
			"component_digests_bound": manifest.Design.CapabilityContractSHA256 == manifest.CapabilityContract.CapabilityContractSHA256 &&
				manifest.Design.AnalysisPlanSHA256 == manifest.AnalysisPlan.AnalysisPlanSHA256,
			"manifest_digest_bound": manifest.ManifestSHA256 != "",
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
	case "grader-contract":
		contract, err := grading.DecodeContract(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		canonical, err := grading.EncodeContract(contract)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("grader contract golden is not canonical")
		}
		supportedModes, supportedCapabilities := 0, 0
		for _, policy := range contract.Modes {
			if policy.Support == grading.SupportSupported {
				supportedModes++
			}
		}
		for _, capability := range contract.Capabilities {
			if capability.Support == grading.SupportSupported {
				supportedCapabilities++
			}
		}
		return map[string]any{
			"schema": contract.Schema, "schema_version": contract.SchemaVersion, "contract_version": contract.ContractVersion,
			"grader_id": contract.GraderID, "grader_version": contract.GraderVersion, "mode_count": len(contract.Modes),
			"supported_mode_count": supportedModes, "capability_count": len(contract.Capabilities),
			"supported_capability_count": supportedCapabilities,
		}, nil
	case "grading-plan":
		contractData := standaloneReadGoldenSource(t, entry.ReaderSupportPath, entry.ReaderSupportSHA256)
		contract, err := grading.DecodeContract(bytes.NewReader(contractData))
		if err != nil {
			return nil, err
		}
		plan, err := grading.DecodePlan(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if _, err := grading.Admit(contract, plan); err != nil {
			return nil, err
		}
		canonical, err := grading.EncodePlan(plan)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("grading plan golden is not canonical")
		}
		hidden, reviewers := 0, 0
		for _, check := range plan.Checks {
			if check.Visibility == grading.VisibilityHidden {
				hidden++
			}
		}
		if plan.Judge != nil {
			reviewers = len(plan.Judge.Reviewers)
		}
		return map[string]any{
			"schema": plan.Schema, "schema_version": plan.SchemaVersion, "contract_version": plan.ContractVersion,
			"mode": plan.Mode, "check_count": len(plan.Checks), "hidden_check_count": hidden,
			"script_instruction_count": len(plan.Script), "reviewer_count": reviewers,
			"contract_bound": plan.ContractSHA256 != "", "input_bound": plan.InputProjectionSHA256 != "",
		}, nil
	case "grade-receipt":
		planData := standaloneReadGoldenSource(t, entry.ReaderSupportPath, entry.ReaderSupportSHA256)
		plan, err := grading.DecodePlan(bytes.NewReader(planData))
		if err != nil {
			return nil, err
		}
		receipt, err := grading.DecodeReceipt(bytes.NewReader(data), plan)
		if err != nil {
			return nil, err
		}
		canonical, err := grading.EncodeReceipt(plan, receipt)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("grade receipt golden is not canonical")
		}
		observed := 0
		for _, decision := range receipt.Decisions {
			if decision.Presence == grading.PresenceObserved {
				observed++
			}
		}
		return map[string]any{
			"schema": receipt.Schema, "schema_version": receipt.SchemaVersion, "contract_version": receipt.ContractVersion,
			"status": receipt.Status, "evidence_count": len(receipt.Evidence), "decision_count": len(receipt.Decisions),
			"observed_decision_count": observed, "reviewer_count": len(receipt.Reviewers),
			"disagreement_count": len(receipt.Disagreements), "plan_bound": receipt.PlanSHA256 != "",
			"evidence_bound": receipt.EvidenceSHA256 != "",
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
	case "scheduler-plan":
		plan, err := DecodeSchedulerPlan(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeSchedulerPlan(plan)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("scheduler plan golden is not canonical")
		}
		return map[string]any{
			"schema": plan.Schema, "schema_version": plan.SchemaVersion, "contract_version": plan.ContractVersion,
			"worker_limit": plan.Limits.Workers, "cohort_limit_count": len(plan.Limits.Cohorts),
			"task_count": len(plan.Tasks), "first_ordinal": plan.Tasks[0].Ordinal, "first_round": plan.Tasks[0].Round,
			"total_cost_microusd": plan.Limits.TotalCostMicroUSD, "plan_digest_bound": plan.PlanSHA256 != "",
		}, nil
	case "scheduler-report":
		planData := standaloneReadGoldenSource(t, entry.ReaderSupportPath, entry.ReaderSupportSHA256)
		plan, err := DecodeSchedulerPlan(bytes.NewReader(planData))
		if err != nil {
			return nil, err
		}
		report, err := DecodeSchedulerReport(bytes.NewReader(data), plan)
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeSchedulerReport(plan, report)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("scheduler report golden is not canonical")
		}
		return map[string]any{
			"schema": report.Schema, "schema_version": report.SchemaVersion, "contract_version": report.ContractVersion,
			"plan_bound": report.PlanSHA256 == plan.PlanSHA256, "queued": report.Queued,
			"started": report.Started, "completed": report.Completed, "succeeded": report.Succeeded,
			"failed": report.Failed, "canceled": report.Canceled, "unknown": report.Unknown,
			"never_started": report.NeverStarted, "stop": report.Stop, "report_digest_bound": report.ReportSHA256 != "",
		}, nil
	case "sequential-reference-bundle":
		bundle, err := DecodeSequentialReferenceBundle(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeSequentialReferenceBundle(bundle)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("sequential reference bundle golden is not canonical")
		}
		gradingSHA256, err := GradingPlanSHA256(bundle.GradingPlan)
		if err != nil {
			return nil, err
		}
		totalInputBytes := 0
		for _, treatment := range bundle.Treatments {
			totalInputBytes += len(treatment.Inputs.Definitions) + len(treatment.Inputs.Fixture) + len(treatment.Inputs.Skill)
		}
		return map[string]any{
			"schema": bundle.Schema, "schema_version": bundle.SchemaVersion, "contract_version": bundle.ContractVersion,
			"manifest_bound": bundle.ManifestSHA256 != "", "grading_plan_bound": gradingSHA256 != "",
			"treatment_count": len(bundle.Treatments), "total_input_bytes": totalInputBytes,
		}, nil
	case "trial-plan":
		plan, err := executionbackend.DecodePlan(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		contractData := standaloneReadGoldenSource(t, entry.ReaderSupportPath, entry.ReaderSupportSHA256)
		contract, err := executionbackend.DecodeContract(bytes.NewReader(contractData))
		if err != nil {
			return nil, err
		}
		if _, err := executionbackend.Admit(contract, plan); err != nil {
			return nil, err
		}
		canonical, err := executionbackend.EncodePlan(plan)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("trial plan golden is not canonical")
		}
		return map[string]any{"schema": plan.Schema, "schema_version": plan.SchemaVersion, "contract_version": plan.ContractVersion,
			"requirement_count": len(plan.Requirements), "mount_count": len(plan.Mounts), "network": plan.Network.Mode,
			"credentials": plan.Credentials.Mode, "verifier_mode": plan.VerifierMode, "artifact_count": len(plan.Artifacts), "program": plan.Program.Kind}, nil
	case "trial-receipt":
		planData := standaloneReadGoldenSource(t, entry.ReaderSupportPath, entry.ReaderSupportSHA256)
		plan, err := executionbackend.DecodePlan(bytes.NewReader(planData))
		if err != nil {
			return nil, err
		}
		receipt, err := executionbackend.DecodeReceipt(bytes.NewReader(data), plan)
		if err != nil {
			return nil, err
		}
		canonical, err := executionbackend.EncodeReceipt(plan, receipt)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("trial receipt golden is not canonical")
		}
		return map[string]any{"schema": receipt.Schema, "schema_version": receipt.SchemaVersion, "contract_version": receipt.ContractVersion,
			"verdict": receipt.Verdict, "input_bytes": receipt.InputBytes, "input_entries": receipt.InputEntries,
			"operations": receipt.Operations, "artifact_count": len(receipt.Artifacts), "termination": receipt.Termination, "cleanup": receipt.Cleanup,
			"network": receipt.Network, "credentials": receipt.Credentials}, nil
	case "trial-record":
		manifestData := standaloneReadGoldenSource(t, entry.ReaderSupportPath, entry.ReaderSupportSHA256)
		manifest, err := DecodeExperimentManifest(bytes.NewReader(manifestData))
		if err != nil {
			return nil, err
		}
		record, err := DecodeExperimentTrialRecord(bytes.NewReader(data), manifest)
		if err != nil {
			return nil, err
		}
		canonical, err := EncodeExperimentTrialRecord(manifest, record)
		if err != nil || !bytes.Equal(canonical, data) {
			return nil, fmt.Errorf("trial record golden is not canonical")
		}
		observedStages, observedMetrics, outcome := 0, 0, uint64(0)
		for _, stage := range record.Stages {
			if stage.Presence == "observed" {
				observedStages++
			}
		}
		for _, metric := range record.Metrics {
			if metric.Presence == "observed" {
				observedMetrics++
			}
			if metric.Metric == "outcome" && metric.Value != nil {
				outcome = *metric.Value
			}
		}
		return map[string]any{
			"schema": record.Schema, "schema_version": record.SchemaVersion, "contract_version": record.ContractVersion,
			"lifecycle_state": record.LifecycleState, "eligibility": record.Eligibility, "exclusion": record.Exclusion,
			"stage_count": len(record.Stages), "observed_stage_count": observedStages,
			"metric_count": len(record.Metrics), "observed_metric_count": observedMetrics, "outcome": outcome,
			"record_digest_bound": record.RecordSHA256 != "",
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
