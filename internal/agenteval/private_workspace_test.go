package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
)

func TestPrivateWorkspacePublicExampleMatchesStrictManifestContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-workspace.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.RunSets) != 1 || len(manifest.RunSets[0].SpecPaths) != 3 || manifest.Execution.MaxEstimatedCostMicroUSD != 10_000_000 {
		t.Fatalf("manifest=%+v", manifest)
	}
}

func TestPrivateWorkspaceRunSetCapacityBoundary(t *testing.T) {
	manifest := DefaultPrivateWorkspaceManifest()
	manifest.RunSets = make([]PrivateWorkspaceRunSet, maxPrivateWorkspaceRunSets)
	for index := range manifest.RunSets {
		alias := fmt.Sprintf("comparison-%03d", index+1)
		manifest.RunSets[index] = PrivateWorkspaceRunSet{
			Alias: alias, SpecPaths: []string{"cases/" + alias + "/run.json"},
		}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data)); err != nil {
		t.Fatalf("maximum run-set inventory rejected: %v", err)
	}
	manifest.RunSets = append(manifest.RunSets, PrivateWorkspaceRunSet{
		Alias: "comparison-overflow", SpecPaths: []string{"cases/comparison-overflow/run.json"},
	})
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data)); err == nil {
		t.Fatal("run-set inventory above the maximum was accepted")
	}
}

func TestPrivateWorkspaceRunSetCapacityMatchesPublicSchema(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-workspace.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			RunSets struct {
				MaxItems int `json:"maxItems"`
			} `json:"run_sets"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties.RunSets.MaxItems != maxPrivateWorkspaceRunSets {
		t.Fatalf("schema run-set maximum=%d, runtime=%d", schema.Properties.RunSets.MaxItems, maxPrivateWorkspaceRunSets)
	}
}

func TestActivationStudyManifestRequiresPositiveReviewerReserve(t *testing.T) {
	manifest := DefaultPrivateWorkspaceManifest()
	panel := privateReviewTestPanel()
	panel.BlindAssignment = "cases/study/blind-assignment.txt"
	manifest.RunSets = []PrivateWorkspaceRunSet{{Kind: PrivateRunSetKindActivationStudy, Alias: "study",
		SpecPaths:              []string{"cases/study/run-1.json", "cases/study/run-2.json", "cases/study/run-3.json", "cases/study/run-4.json"},
		QualitativeReviewPanel: &panel}}
	if err := manifest.Validate(); err == nil {
		t.Fatal("activation study accepted a zero or omitted reviewer reserve")
	}
	manifest.RunSets[0].ReviewerReserveMicroUSD = 1
	manifest.RunSets[0].CalibrationMaxEstimatedCostMicroUSD = 1
	if err := manifest.Validate(); err != nil {
		t.Fatalf("positive reviewer reserve rejected: %v", err)
	}
}

func TestExecutableReviewPanelSupportsMixedProvidersAndRequiresWholeReserve(t *testing.T) {
	manifest := DefaultPrivateWorkspaceManifest()
	panel := privateReviewTestPanel()
	panel.Reviewers[1].Kind = "claude-code"
	for _, reviewer := range panel.Reviewers {
		panel.Executions = append(panel.Executions, PrivateReviewerExecution{ReviewerID: reviewer.ID, Reasoning: "high",
			TimeoutSeconds: 60, Pricing: Pricing{InputMicroUSDPerMillionTokens: 1, OutputMicroUSDPerMillionTokens: 2},
			MaxEstimatedCostMicroUSD: 10})
	}
	manifest.RunSets = []PrivateWorkspaceRunSet{{Kind: PrivateRunSetKindComparison, Alias: "comparison",
		SpecPaths: []string{"cases/comparison/cli.json", "cases/comparison/mcp.json"}, QualitativeReviewPanel: &panel,
		ReviewerReserveMicroUSD: 60}}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("mixed executable panel rejected: %v", err)
	}
	manifest.RunSets[0].ReviewerReserveMicroUSD--
	if err := manifest.Validate(); err == nil {
		t.Fatal("panel reserve did not cover every reviewer on every surface")
	}
	manifest.RunSets[0].ReviewerReserveMicroUSD = 60
	executions := append([]PrivateReviewerExecution(nil), panel.Executions...)
	panel.Executions = panel.Executions[:2]
	manifest.RunSets[0].QualitativeReviewPanel = &panel
	if err := manifest.Validate(); err == nil {
		t.Fatal("partial executable roster was accepted")
	}
	panel.Executions = executions
	panel.Executions[1].Reasoning = "minimal"
	if err := manifest.Validate(); err == nil {
		t.Fatal("Claude Code slot accepted a Codex-only reasoning level")
	}
}

func TestLegacyCalibratedWorkspaceRemainsReadableButCannotDeclareReviewerExecution(t *testing.T) {
	manifest := DefaultPrivateWorkspaceManifest()
	manifest.SchemaVersion = LegacyCalibratedWorkspaceSchemaVersion
	manifest.RunSets = []PrivateWorkspaceRunSet{{Alias: "comparison", SpecPaths: []string{"cases/comparison/run.json"}}}
	data, err := EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data))
	if err != nil || decoded.SchemaVersion != LegacyCalibratedWorkspaceSchemaVersion {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	panel := privateReviewTestPanel()
	for _, reviewer := range panel.Reviewers {
		panel.Executions = append(panel.Executions, PrivateReviewerExecution{ReviewerID: reviewer.ID, Reasoning: "high",
			TimeoutSeconds: 60, Pricing: Pricing{InputMicroUSDPerMillionTokens: 1}, MaxEstimatedCostMicroUSD: 1})
	}
	manifest.RunSets[0].QualitativeReviewPanel = &panel
	manifest.RunSets[0].ReviewerReserveMicroUSD = 3
	if err := manifest.Validate(); err == nil {
		t.Fatal("legacy workspace accepted reviewer execution")
	}

	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "legacy-private")
	manifest.RunSets = []PrivateWorkspaceRunSet{}
	if report, err := InitPrivateWorkspace(root, repository, manifest); err != nil || !report.Healthy {
		t.Fatalf("legacy workspace layout report=%+v err=%v", report, err)
	}
}

func TestLegacyActivationManifestIsReadOnlyDecodableWithoutCalibration(t *testing.T) {
	manifest := DefaultPrivateWorkspaceManifest()
	manifest.SchemaVersion = LegacyActivationWorkspaceSchemaVersion
	panel := privateReviewTestPanel()
	panel.BlindAssignment = "cases/study/blind-assignment.txt"
	manifest.RunSets = []PrivateWorkspaceRunSet{{Kind: PrivateRunSetKindActivationStudy, Alias: "study",
		SpecPaths:              []string{"cases/study/run-1.json", "cases/study/run-2.json", "cases/study/run-3.json", "cases/study/run-4.json"},
		QualitativeReviewPanel: &panel, ReviewerReserveMicroUSD: 1}}
	data, err := EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data))
	if err != nil || decoded.SchemaVersion != LegacyActivationWorkspaceSchemaVersion || decoded.RunSets[0].CalibrationMaxEstimatedCostMicroUSD != 0 {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	manifest.RunSets[0].CalibrationMaxEstimatedCostMicroUSD = 1
	if err := manifest.Validate(); err == nil {
		t.Fatal("legacy activation manifest accepted current calibration field")
	}
}

func TestPrivateWorkspaceManifestPreservesSchemaPresenceRules(t *testing.T) {
	comparison := DefaultPrivateWorkspaceManifest()
	comparison.RunSets = []PrivateWorkspaceRunSet{{Alias: "comparison", SpecPaths: []string{"cases/comparison/run.json"}}}
	comparisonData, err := EncodePrivateWorkspaceManifest(comparison)
	if err != nil {
		t.Fatal(err)
	}
	activation := comparison
	panel := privateReviewTestPanel()
	panel.BlindAssignment = "cases/study/blind-assignment.txt"
	activation.RunSets = []PrivateWorkspaceRunSet{{Kind: PrivateRunSetKindActivationStudy, Alias: "study",
		SpecPaths:              []string{"cases/study/implicit.json", "cases/study/explicit.json", "cases/study/developer.json", "cases/study/combined.json"},
		QualitativeReviewPanel: &panel, ReviewerReserveMicroUSD: 1, CalibrationMaxEstimatedCostMicroUSD: 1}}
	activationData, err := EncodePrivateWorkspaceManifest(activation)
	if err != nil {
		t.Fatal(err)
	}
	legacy := comparison
	legacy.SchemaVersion = LegacyPrivateWorkspaceSchemaVersion
	legacyData, err := EncodePrivateWorkspaceManifest(legacy)
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		base   []byte
		mutate func(map[string]any)
	}{
		"missing retention boolean": {comparisonData, func(root map[string]any) {
			delete(root["retention"].(map[string]any), "retain_baseline_transcripts")
		}},
		"null retention boolean": {comparisonData, func(root map[string]any) {
			root["retention"].(map[string]any)["retain_baseline_transcripts"] = nil
		}},
		"missing qualitative boolean": {comparisonData, func(root map[string]any) {
			delete(root["run_sets"].([]any)[0].(map[string]any), "qualitative_review_required")
		}},
		"null qualitative boolean": {comparisonData, func(root map[string]any) {
			root["run_sets"].([]any)[0].(map[string]any)["qualitative_review_required"] = nil
		}},
		"null external profile env": {comparisonData, func(root map[string]any) {
			root["external_mcp_profile_env"] = nil
		}},
		"empty kind": {comparisonData, func(root map[string]any) {
			root["run_sets"].([]any)[0].(map[string]any)["kind"] = ""
		}},
		"null kind": {comparisonData, func(root map[string]any) {
			root["run_sets"].([]any)[0].(map[string]any)["kind"] = nil
		}},
		"null optional panel": {comparisonData, func(root map[string]any) {
			root["run_sets"].([]any)[0].(map[string]any)["qualitative_review_panel"] = nil
		}},
		"comparison reserve present": {comparisonData, func(root map[string]any) {
			root["run_sets"].([]any)[0].(map[string]any)["reviewer_reserve_microusd"] = float64(0)
		}},
		"activation reserve missing": {activationData, func(root map[string]any) {
			delete(root["run_sets"].([]any)[0].(map[string]any), "reviewer_reserve_microusd")
		}},
		"activation calibration reserve missing": {activationData, func(root map[string]any) {
			delete(root["run_sets"].([]any)[0].(map[string]any), "calibration_max_estimated_cost_microusd")
		}},
		"legacy kind present": {legacyData, func(root map[string]any) {
			root["run_sets"].([]any)[0].(map[string]any)["kind"] = "comparison"
		}},
		"legacy reserve present": {legacyData, func(root map[string]any) {
			root["run_sets"].([]any)[0].(map[string]any)["reviewer_reserve_microusd"] = float64(0)
		}},
		"mixed-case root key": {comparisonData, func(root map[string]any) {
			root["SCHEMA_VERSION"] = root["schema_version"]
			delete(root, "schema_version")
		}},
		"mixed-case retention key": {comparisonData, func(root map[string]any) {
			retention := root["retention"].(map[string]any)
			retention["Max_Candidate_Bytes"] = retention["max_candidate_bytes"]
			delete(retention, "max_candidate_bytes")
		}},
		"mixed-case run-set key": {comparisonData, func(root map[string]any) {
			runSet := root["run_sets"].([]any)[0].(map[string]any)
			runSet["Alias"] = runSet["alias"]
			delete(runSet, "alias")
		}},
		"mixed-case panel key": {activationData, func(root map[string]any) {
			panel := root["run_sets"].([]any)[0].(map[string]any)["qualitative_review_panel"].(map[string]any)
			panel["Reviewers"] = panel["reviewers"]
			delete(panel, "reviewers")
		}},
		"mixed-case reviewer key": {activationData, func(root map[string]any) {
			reviewer := root["run_sets"].([]any)[0].(map[string]any)["qualitative_review_panel"].(map[string]any)["reviewers"].([]any)[0].(map[string]any)
			reviewer["Kind"] = reviewer["kind"]
			delete(reviewer, "kind")
		}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var root map[string]any
			if err := json.Unmarshal(test.base, &root); err != nil {
				t.Fatal(err)
			}
			test.mutate(root)
			data, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data)); err == nil {
				t.Fatal("schema-invalid field presence was accepted")
			}
		})
	}
}

func TestPrivateWorkspaceManifestUnicodeLengthsMatchJSONSchema(t *testing.T) {
	validPath := "cases/" + strings.Repeat("界", 501) + ".json"
	if !validPrivateWorkspaceSpecPath(validPath) {
		t.Fatal("512-code-point case path was rejected")
	}
	if validPrivateWorkspaceSpecPath("cases/" + strings.Repeat("界", 502) + ".json") {
		t.Fatal("513-code-point case path was accepted")
	}
	if err := (Reviewer{ID: "reviewer-01", Kind: "codex", Model: strings.Repeat("界", 256)}).validate(); err != nil {
		t.Fatalf("256-code-point reviewer model rejected: %v", err)
	}
	if err := (Reviewer{ID: "reviewer-01", Kind: "codex", Model: strings.Repeat("界", 257)}).validate(); err == nil {
		t.Fatal("257-code-point reviewer model accepted")
	}
}

func TestInitPrivateWorkspaceCreatesFixedOwnerOnlyLayout(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private-evaluations")
	manifest := DefaultPrivateWorkspaceManifest()

	report, err := InitPrivateWorkspace(root, repository, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Healthy || report.Counts.FixedDirectories != len(privateWorkspaceFixedDirectories) || report.Counts.RunSets != 0 {
		t.Fatalf("report=%+v", report)
	}
	for _, path := range append([]string{root}, privateWorkspacePaths(root, privateWorkspaceFixedDirectories)...) {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("directory mode=%v", info.Mode())
		}
	}
	for _, name := range []string{privateOutputRootMarker, PrivateWorkspaceManifestName} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%v", name, info.Mode())
		}
	}

	second, err := InitPrivateWorkspace(root, repository, manifest)
	if err != nil || !second.Healthy {
		t.Fatalf("idempotent init report=%+v err=%v", second, err)
	}
	mismatched := manifest
	mismatched.Retention.MaxCandidateAgeDays++
	if _, err := InitPrivateWorkspace(root, repository, mismatched); err == nil || !errors.Is(err, ErrPrivateWorkspaceUnhealthy) {
		t.Fatalf("current manifest accepted mismatched init settings: %v", err)
	}
}

func TestInitPrivateWorkspaceResumesLegacyFilenameAndSchema(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private-evaluations")
	legacyManifest := DefaultPrivateWorkspaceManifest()
	legacyManifest.SchemaVersion = LegacyPrivateWorkspaceSchemaVersion
	legacyManifest.Retention.MaxCandidateAgeDays = 21
	current := filepath.Join(root, PrivateWorkspaceManifestName)
	legacy := filepath.Join(root, LegacyPrivateWorkspaceManifestName)
	if report, err := InitPrivateWorkspace(root, repository, legacyManifest); err != nil || !report.Healthy {
		t.Fatalf("legacy init report=%+v err=%v", report, err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy manifest missing: %v", err)
	}
	if _, err := os.Stat(current); !os.IsNotExist(err) {
		t.Fatalf("current manifest unexpectedly created: %v", err)
	}
	if report, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil || !report.Healthy {
		t.Fatalf("legacy resume with current default report=%+v err=%v", report, err)
	}
	if report, err := DoctorPrivateWorkspace(root, repository); err != nil || !report.Healthy {
		t.Fatalf("legacy doctor report=%+v err=%v", report, err)
	}
	legacyData, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	resumedManifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(legacyData))
	if err != nil || !reflect.DeepEqual(resumedManifest, legacyManifest) {
		t.Fatalf("legacy manifest changed during resume: manifest=%+v err=%v", resumedManifest, err)
	}
	if _, err := os.Stat(current); !os.IsNotExist(err) {
		t.Fatalf("legacy resume unexpectedly migrated manifest: %v", err)
	}
}

func TestPrivateWorkspaceManifestFilenamesAreUnambiguous(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private-evaluations")
	legacyManifest := DefaultPrivateWorkspaceManifest()
	legacyManifest.SchemaVersion = LegacyPrivateWorkspaceSchemaVersion
	if _, err := InitPrivateWorkspace(root, repository, legacyManifest); err != nil {
		t.Fatal(err)
	}
	currentData, err := EncodePrivateWorkspaceManifest(DefaultPrivateWorkspaceManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, PrivateWorkspaceManifestName), currentData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err == nil || !errors.Is(err, ErrPrivateWorkspaceUnhealthy) {
		t.Fatalf("ambiguous manifest filenames were accepted by init: %v", err)
	}
	report, err := DoctorPrivateWorkspace(root, repository)
	if !errors.Is(err, ErrPrivateWorkspaceUnhealthy) || report.Healthy || privateWorkspaceCheckStatus(report, PrivateWorkspaceCheckManifestMode) != "fail" {
		t.Fatalf("ambiguous manifest filenames were accepted by doctor: report=%+v err=%v", report, err)
	}
}

func TestPrivateWorkspaceManifestLoaderRejectsSchemaFilenameMismatch(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private-evaluations")
	manifest := DefaultPrivateWorkspaceManifest()
	if _, err := InitPrivateWorkspace(root, repository, manifest); err != nil {
		t.Fatal(err)
	}
	legacy := manifest
	legacy.SchemaVersion = LegacyPrivateWorkspaceSchemaVersion
	legacy.RunSets = []PrivateWorkspaceRunSet{{Alias: "comparison", SpecPaths: []string{"cases/comparison/run.json"}}}
	data, err := EncodePrivateWorkspaceManifest(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, PrivateWorkspaceManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadPrivateManifestRunSet(root, "comparison"); err == nil {
		t.Fatal("execution manifest loader accepted a legacy schema under the current filename")
	}
	if _, err := PreviewPrivatePrune(PrivatePruneOptions{Root: root, RepositoryRoot: repository}); err == nil {
		t.Fatal("prune manifest loader accepted a legacy schema under the current filename")
	}
}

func TestInitPrivateWorkspaceRequiresIgnoredRootInsideRepository(t *testing.T) {
	repository := newPrivateWorkspaceGitRepository(t)
	root := filepath.Join(repository, "private")
	if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); !errors.Is(err, ErrPrivateWorkspaceUnhealthy) {
		t.Fatalf("unignored root err=%v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("unignored init created root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("private/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest())
	if err != nil || !report.Healthy {
		t.Fatalf("ignored init report=%+v err=%v", report, err)
	}

	tracked := filepath.Join(root, "cases", "tracked.json")
	if err := os.WriteFile(tracked, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", repository, "add", "-f", "private/cases/tracked.json").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	report = InspectPrivateWorkspace(root, repository)
	if report.Healthy || privateWorkspaceCheckStatus(report, PrivateWorkspaceCheckGitBoundary) != "fail" {
		t.Fatalf("tracked private file passed: %+v", report)
	}
}

func TestInitPrivateWorkspaceRefusesUnmarkedNonemptyAndSymlinkRoots(t *testing.T) {
	repository := t.TempDir()
	t.Run("nonempty", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "private")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "existing"), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); !errors.Is(err, ErrPrivateWorkspaceUnhealthy) {
			t.Fatalf("err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(root, privateOutputRootMarker)); !os.IsNotExist(err) {
			t.Fatalf("marker unexpectedly created: %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink permissions vary on Windows")
		}
		target := filepath.Join(t.TempDir(), "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(t.TempDir(), "private")
		if err := os.Symlink(target, root); err != nil {
			t.Fatal(err)
		}
		if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); !errors.Is(err, ErrPrivateWorkspaceUnhealthy) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestPrivateWorkspaceManifestStrictSchemaAndContainedSpecs(t *testing.T) {
	valid := DefaultPrivateWorkspaceManifest()
	valid.RunSets = []PrivateWorkspaceRunSet{{Alias: "portfolio-01", SpecPaths: []string{"cases/portfolio-01/run.cli.json"}}}
	data, err := EncodePrivateWorkspaceManifest(valid)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data))
	if err != nil || len(decoded.RunSets) != 1 {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	if decoded.Retention.MaxCandidateAgeDays != 14 || decoded.Retention.MaxCandidateBytes != 2<<30 || !decoded.Retention.RetainBaselineTranscripts {
		t.Fatalf("retention=%+v", decoded.Retention)
	}

	privateMarker := "PRIVATE_MARKER_SHOULD_NOT_BE_ECHOED"
	unknown := strings.TrimSuffix(string(data), "}\n") + `,"` + privateMarker + `":true}`
	if _, err := DecodePrivateWorkspaceManifest(strings.NewReader(unknown)); err == nil || strings.Contains(err.Error(), privateMarker) {
		t.Fatalf("unknown-field err=%v", err)
	}
	versionField := fmt.Sprintf(`"schema_version": %d`, PrivateWorkspaceSchemaVersion)
	duplicate := strings.Replace(string(data), versionField, versionField+", "+versionField, 1)
	if _, err := DecodePrivateWorkspaceManifest(strings.NewReader(duplicate)); err == nil {
		t.Fatal("duplicate manifest key passed")
	}

	for name, mutate := range map[string]func(*PrivateWorkspaceManifest){
		"version":        func(m *PrivateWorkspaceManifest) { m.SchemaVersion = PrivateWorkspaceSchemaVersion + 1 },
		"env":            func(m *PrivateWorkspaceManifest) { m.LiveConfigEnv = "TOKEN=value" },
		"alias":          func(m *PrivateWorkspaceManifest) { m.RunSets[0].Alias = "Private Project" },
		"traversal":      func(m *PrivateWorkspaceManifest) { m.RunSets[0].SpecPaths[0] = "cases/../outside.json" },
		"absolute":       func(m *PrivateWorkspaceManifest) { m.RunSets[0].SpecPaths[0] = "/outside.json" },
		"outside cases":  func(m *PrivateWorkspaceManifest) { m.RunSets[0].SpecPaths[0] = "runs/result.json" },
		"age retention":  func(m *PrivateWorkspaceManifest) { m.Retention.MaxCandidateAgeDays = 0 },
		"byte retention": func(m *PrivateWorkspaceManifest) { m.Retention.MaxCandidateBytes = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.RunSets = []PrivateWorkspaceRunSet{{Alias: valid.RunSets[0].Alias, SpecPaths: append([]string(nil), valid.RunSets[0].SpecPaths...)}}
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid manifest passed")
			}
		})
	}
}

func TestPrivateWorkspaceManifestQualitativeReviewPolicies(t *testing.T) {
	legacy := DefaultPrivateWorkspaceManifest()
	legacy.RunSets = []PrivateWorkspaceRunSet{{Alias: "portfolio", SpecPaths: []string{"cases/portfolio/run.json"}, QualitativeReviewRequired: true}}
	data, err := EncodePrivateWorkspaceManifest(legacy)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data))
	if err != nil || !decoded.RunSets[0].QualitativeReviewRequired || decoded.RunSets[0].QualitativeReviewPanel != nil {
		t.Fatalf("legacy policy decoded=%+v err=%v", decoded.RunSets[0], err)
	}

	valid := legacy
	valid.RunSets = []PrivateWorkspaceRunSet{{Alias: "portfolio", SpecPaths: []string{"cases/portfolio/run.json"}, QualitativeReviewPanel: testPrivateQualitativePanel()}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid panel: %v", err)
	}
	for name, mutate := range map[string]func(*PrivateWorkspaceManifest){
		"both policies": func(manifest *PrivateWorkspaceManifest) { manifest.RunSets[0].QualitativeReviewRequired = true },
		"two reviewers": func(manifest *PrivateWorkspaceManifest) {
			manifest.RunSets[0].QualitativeReviewPanel.Reviewers = manifest.RunSets[0].QualitativeReviewPanel.Reviewers[:2]
		},
		"four reviewers": func(manifest *PrivateWorkspaceManifest) {
			manifest.RunSets[0].QualitativeReviewPanel.Reviewers = append(manifest.RunSets[0].QualitativeReviewPanel.Reviewers, Reviewer{ID: "judge-4", Kind: "codex", Model: "model-d"})
		},
		"duplicate id": func(manifest *PrivateWorkspaceManifest) {
			manifest.RunSets[0].QualitativeReviewPanel.Reviewers[1].ID = "judge-1"
		},
		"missing id": func(manifest *PrivateWorkspaceManifest) {
			manifest.RunSets[0].QualitativeReviewPanel.Reviewers[1].ID = ""
		},
		"path reviewer id": func(manifest *PrivateWorkspaceManifest) {
			manifest.RunSets[0].QualitativeReviewPanel.Reviewers[1].ID = "judge/../../reports"
		},
		"invalid model": func(manifest *PrivateWorkspaceManifest) {
			manifest.RunSets[0].QualitativeReviewPanel.Reviewers[1].Model = ""
		},
		"invalid method": func(manifest *PrivateWorkspaceManifest) {
			manifest.RunSets[0].QualitativeReviewPanel.Method = "mean-v1"
		},
		"zero range": func(manifest *PrivateWorkspaceManifest) {
			manifest.RunSets[0].QualitativeReviewPanel.MaxCriterionRangeBPS = 0
		},
		"outside assignment": func(manifest *PrivateWorkspaceManifest) {
			manifest.RunSets[0].QualitativeReviewPanel.BlindAssignment = "reports/blind.txt"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			panel := *valid.RunSets[0].QualitativeReviewPanel
			panel.Reviewers = append([]Reviewer(nil), panel.Reviewers...)
			candidate.RunSets = []PrivateWorkspaceRunSet{{Alias: valid.RunSets[0].Alias, SpecPaths: append([]string(nil), valid.RunSets[0].SpecPaths...), QualitativeReviewPanel: &panel}}
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid qualitative review policy passed")
			}
		})
	}
}

func testPrivateQualitativePanel() *PrivateQualitativeReviewPanel {
	return &PrivateQualitativeReviewPanel{
		Method: PrivateQualitativeReviewPanelMethod,
		Reviewers: []Reviewer{
			{ID: "judge-1", Kind: "codex", Model: "model-a"},
			{ID: "judge-2", Kind: "claude-code", Model: "model-b"},
			{ID: "judge-3", Kind: "codex", Model: "model-c"},
		},
		MaxCriterionRangeBPS: 2500,
	}
}

func TestPrivateWorkspaceReportGuidesTheNextSafeAction(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private")
	report, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest())
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "needs_configuration" || !reflect.DeepEqual(report.NextActions, []string{"configure_run_sets"}) {
		t.Fatalf("report=%+v", report)
	}
	if strings.Contains(strings.Join(report.NextActions, "\n"), root) {
		t.Fatal("next actions leaked the private root")
	}
}

func TestPrivateWorkspaceDoctorDetectsModeAndSymlinkControls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission and symlink controls")
	}
	repository := t.TempDir()

	t.Run("mode", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "private")
		if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, PrivateWorkspaceManifestName), 0o644); err != nil {
			t.Fatal(err)
		}
		report, err := DoctorPrivateWorkspace(root, repository)
		if !errors.Is(err, ErrPrivateWorkspaceUnhealthy) || report.Healthy || privateWorkspaceCheckStatus(report, PrivateWorkspaceCheckManifestMode) != "fail" || privateWorkspaceCheckStatus(report, PrivateWorkspaceCheckTreeOwnerOnly) != "fail" {
			t.Fatalf("report=%+v err=%v", report, err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "private")
		if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, PrivateWorkspaceManifestName), filepath.Join(root, "cases", "link.json")); err != nil {
			t.Fatal(err)
		}
		report, err := DoctorPrivateWorkspace(root, repository)
		if !errors.Is(err, ErrPrivateWorkspaceUnhealthy) || report.Healthy || privateWorkspaceCheckStatus(report, PrivateWorkspaceCheckTreeNoSymlinks) != "fail" {
			t.Fatalf("report=%+v err=%v", report, err)
		}
	})
}

func TestPrivateWorkspaceDoctorNeverEchoesPrivateInputs(t *testing.T) {
	privateMarker := "PRIVATE_BACKEND_MARKER_7XQ"
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, string)
	}{
		{
			name: "manifest decoder",
			prepare: func(t *testing.T, root string) {
				malformed := `{"schema_version":1,"unknown_` + privateMarker + `":true}`
				if err := os.WriteFile(filepath.Join(root, PrivateWorkspaceManifestName), []byte(malformed), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "spec decoder",
			prepare: func(t *testing.T, root string) {
				manifest := DefaultPrivateWorkspaceManifest()
				manifest.RunSets = []PrivateWorkspaceRunSet{{Alias: "case-01", SpecPaths: []string{"cases/case-01/run.json"}}}
				data, err := EncodePrivateWorkspaceManifest(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, PrivateWorkspaceManifestName), data, 0o600); err != nil {
					t.Fatal(err)
				}
				directory := filepath.Join(root, "cases", "case-01")
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "run.json"), []byte(`{"private":"`+privateMarker+`"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := t.TempDir()
			root := filepath.Join(t.TempDir(), "private")
			if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil {
				t.Fatal(err)
			}
			test.prepare(t, root)
			report, err := DoctorPrivateWorkspace(root, repository)
			if !errors.Is(err, ErrPrivateWorkspaceUnhealthy) || report.Healthy {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			encoded, marshalErr := json.Marshal(report)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			combined := string(encoded) + "\n" + err.Error()
			if strings.Contains(combined, privateMarker) || strings.Contains(combined, root) {
				t.Fatalf("private input leaked: %s", combined)
			}
			for _, check := range report.Checks {
				if !privateWorkspaceDoctorCode(check.Code) || (check.Status != "pass" && check.Status != "fail") {
					t.Fatalf("unbounded check=%+v", check)
				}
			}
		})
	}
}

func TestPrivateWorkspaceDoctorDetectsStaleCredentialScratch(t *testing.T) {
	for _, name := range []string{"atl-agent-eval-live-config-stale", "atl-agent-eval-provider-runtime-stale"} {
		t.Run(name, func(t *testing.T) {
			repository := t.TempDir()
			root := filepath.Join(t.TempDir(), "private")
			if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil {
				t.Fatal(err)
			}
			scratch := filepath.Join(root, ".ephemeral", name)
			if err := os.Mkdir(scratch, 0o700); err != nil {
				t.Fatal(err)
			}
			secret := "synthetic-provider-auth-canary"
			if err := os.WriteFile(filepath.Join(scratch, "credentials.json"), []byte(`{"secret":"`+secret+`"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			report, err := DoctorPrivateWorkspace(root, repository)
			if !errors.Is(err, ErrPrivateWorkspaceUnhealthy) || report.Healthy {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			found := false
			for _, check := range report.Checks {
				if check.Code == PrivateWorkspaceCheckScratchClean {
					found = check.Status == "fail"
				}
			}
			if !found {
				t.Fatalf("scratch check did not fail: %+v", report.Checks)
			}
			encoded, marshalErr := json.Marshal(report)
			if marshalErr != nil || bytes.Contains(encoded, []byte(secret)) || bytes.Contains(encoded, []byte(root)) {
				t.Fatalf("scratch diagnostics leaked private material: %s err=%v", encoded, marshalErr)
			}
		})
	}
}

func TestPrivateWorkspaceOperationErrorKeepsCausesInspectableAndOutOfTheMessage(t *testing.T) {
	privateRoot := filepath.Join("private", "evaluations", "private-workspace.v4.json")
	statCause := &fs.PathError{Op: "lstat", Path: privateRoot, Err: fs.ErrPermission}
	decodeCause := privateWorkspaceContractError("decode")

	err := privateWorkspaceOperationError("manifest_stat", statCause, nil, decodeCause)
	assertPrivateWorkspaceOperationCode(t, err, "manifest_stat")
	if strings.Contains(err.Error(), privateRoot) || strings.Contains(err.Error(), decodeCause.Error()) {
		t.Fatalf("message leaked a cause: %q", err.Error())
	}
	if !errors.Is(err, fs.ErrPermission) || !errors.Is(err, decodeCause) {
		t.Fatalf("error %v lost a cause", err)
	}
	var typed *fs.PathError
	if !errors.As(err, &typed) || typed.Path != statCause.Path {
		t.Fatalf("error %v does not expose the concrete stat failure", err)
	}
	causes := privateWorkspaceOperationErrorCauses(t, err)
	if len(causes) != 2 || causes[0] != error(statCause) || causes[1] != decodeCause {
		t.Fatalf("causes=%v, want both non-nil causes retained in order", causes)
	}
	var classified interface{ Code() string }
	if !errors.As(err, &classified) || classified.Code() != "manifest_stat" {
		t.Fatalf("error %v does not expose its stable code", err)
	}

	// A rejection with nothing to attach classifies exactly as it did before.
	assertPrivateWorkspaceOperationCode(t, privateWorkspaceOperationError("git_boundary"), "git_boundary")
	if causes := privateWorkspaceOperationErrorCauses(t, privateWorkspaceOperationError("git_boundary", nil, nil)); len(causes) != 0 {
		t.Fatalf("causes=%v, want nil causes dropped", causes)
	}
}

func TestPrivateWorkspaceLocationsAttachOnlyRejectingFailures(t *testing.T) {
	t.Run("missing root", func(t *testing.T) {
		_, _, err := privateWorkspaceLocations(filepath.Join(t.TempDir(), "absent"), t.TempDir(), false)
		assertPrivateWorkspaceOperationCode(t, err, "root")
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) || !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error %v does not expose the concrete resolution failure", err)
		}
	})
	t.Run("symlinked root", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation needs elevated privileges on Windows")
		}
		root := filepath.Join(t.TempDir(), "private")
		if err := os.Symlink(t.TempDir(), root); err != nil {
			t.Fatal(err)
		}
		_, _, err := privateWorkspaceLocations(root, t.TempDir(), false)
		assertPrivateWorkspaceOperationCode(t, err, "root_symlink")
		// The stat succeeded, so the symlink itself is the only rejection.
		if causes := privateWorkspaceOperationErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a mode-only rejection", causes)
		}
	})
	t.Run("missing repository", func(t *testing.T) {
		_, _, err := privateWorkspaceLocations(t.TempDir(), filepath.Join(t.TempDir(), "absent"), false)
		assertPrivateWorkspaceOperationCode(t, err, "repository_root")
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error %v does not expose the concrete resolution failure", err)
		}
	})
	t.Run("repository is a file", func(t *testing.T) {
		repository := filepath.Join(t.TempDir(), "repository")
		if err := os.WriteFile(repository, []byte("not a repository\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := privateWorkspaceLocations(t.TempDir(), repository, false)
		assertPrivateWorkspaceOperationCode(t, err, "repository_root")
		// The stat succeeded; only the file type rejects the repository root.
		if causes := privateWorkspaceOperationErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a type-only rejection", causes)
		}
	})
}

func TestPrivateWorkspaceManifestOperationsAttachIOAndDecodeCauses(t *testing.T) {
	newWorkspace := func(t *testing.T) (string, string) {
		t.Helper()
		repository, root := t.TempDir(), filepath.Join(t.TempDir(), "private")
		if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil {
			t.Fatal(err)
		}
		return root, repository
	}

	t.Run("absent manifest", func(t *testing.T) {
		root, _ := newWorkspace(t)
		if err := os.Remove(filepath.Join(root, PrivateWorkspaceManifestName)); err != nil {
			t.Fatal(err)
		}
		_, _, err := loadPrivateWorkspaceManifest(root)
		assertPrivateWorkspaceOperationCode(t, err, "manifest_read")
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) || !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error %v does not expose the concrete read failure", err)
		}
		if strings.Contains(err.Error(), root) {
			t.Fatalf("message leaked the workspace root: %q", err.Error())
		}
	})

	t.Run("undecodable manifest", func(t *testing.T) {
		root, _ := newWorkspace(t)
		privateMarker := "PRIVATE_MANIFEST_CAUSE_MARKER"
		if err := os.WriteFile(filepath.Join(root, PrivateWorkspaceManifestName),
			[]byte(`{"schema_version":4,"`+privateMarker+`":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := loadPrivateWorkspaceManifest(root)
		assertPrivateWorkspaceOperationCode(t, err, "manifest_mismatch")
		causes := privateWorkspaceOperationErrorCauses(t, err)
		// The decoder classifies the failure under the separate manifest
		// contract family, and that classification stays reachable below the
		// unchanged operation code.
		if len(causes) != 1 || causes[0].Error() != privateWorkspaceContractError("decode").Error() {
			t.Fatalf("causes=%v, want the manifest contract classification", causes)
		}
		if strings.Contains(err.Error(), privateMarker) || strings.Contains(err.Error(), root) {
			t.Fatalf("message leaked private manifest content: %q", err.Error())
		}
	})

	t.Run("schema filename mismatch", func(t *testing.T) {
		root, _ := newWorkspace(t)
		legacy := DefaultPrivateWorkspaceManifest()
		legacy.SchemaVersion = LegacyPrivateWorkspaceSchemaVersion
		data, err := EncodePrivateWorkspaceManifest(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, PrivateWorkspaceManifestName), data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err = loadPrivateWorkspaceManifest(root)
		assertPrivateWorkspaceOperationCode(t, err, "manifest_mismatch")
		// A manifest that decodes cleanly under the wrong filename is rejected
		// by comparison alone.
		if causes := privateWorkspaceOperationErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a comparison-only rejection", causes)
		}
	})

	t.Run("unreadable manifest candidates", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("path-type stat failures differ on Windows")
		}
		root := filepath.Join(t.TempDir(), "private")
		if err := os.WriteFile(root, []byte("not a workspace\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := privateWorkspaceManifestPath(root)
		assertPrivateWorkspaceOperationCode(t, err, "manifest_stat")
		causes := privateWorkspaceOperationErrorCauses(t, err)
		if len(causes) != 4 {
			t.Fatalf("causes=%v, want one cause per probed manifest candidate", causes)
		}
		for index, name := range []string{PrivateWorkspaceManifestName, LegacyCalibratedWorkspaceManifestName,
			LegacyActivationWorkspaceManifestName, LegacyPrivateWorkspaceManifestName} {
			var pathErr *fs.PathError
			if !errors.As(causes[index], &pathErr) || filepath.Base(pathErr.Path) != name {
				t.Fatalf("cause %d = %v, want the %s stat failure in probe order", index, causes[index], name)
			}
			// An ordinary absence is expected here and must never be reported
			// as the cause of the rejection.
			if errors.Is(causes[index], fs.ErrNotExist) {
				t.Fatalf("cause %d = %v, want a real stat failure", index, causes[index])
			}
		}
	})

	t.Run("absent manifest candidates", func(t *testing.T) {
		root := t.TempDir()
		path, err := privateWorkspaceManifestPath(root)
		if err != nil || path != filepath.Join(root, PrivateWorkspaceManifestName) {
			t.Fatalf("path=%q err=%v, want an ordinary absence to resolve to the current manifest", path, err)
		}
	})

	t.Run("ambiguous manifests", func(t *testing.T) {
		root, _ := newWorkspace(t)
		legacy := DefaultPrivateWorkspaceManifest()
		legacy.SchemaVersion = LegacyPrivateWorkspaceSchemaVersion
		data, err := EncodePrivateWorkspaceManifest(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, LegacyPrivateWorkspaceManifestName), data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = privateWorkspaceManifestPath(root)
		assertPrivateWorkspaceOperationCode(t, err, "manifest_ambiguous")
		// Both stats succeeded; the conflicting layout is the whole rejection.
		if causes := privateWorkspaceOperationErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a layout conflict", causes)
		}
	})
}

func TestInitPrivateWorkspaceAttachesOperationCauses(t *testing.T) {
	t.Run("invalid manifest", func(t *testing.T) {
		manifest := DefaultPrivateWorkspaceManifest()
		manifest.Retention.MaxCandidateAgeDays = 0
		_, err := InitPrivateWorkspace(filepath.Join(t.TempDir(), "private"), t.TempDir(), manifest)
		assertPrivateWorkspaceOperationCode(t, err, "manifest_invalid")
		causes := privateWorkspaceOperationErrorCauses(t, err)
		if len(causes) != 1 || causes[0].Error() != privateWorkspaceContractError("retention").Error() {
			t.Fatalf("causes=%v, want the manifest contract classification", causes)
		}
	})

	t.Run("uninitialized root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "private")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "existing"), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := InitPrivateWorkspace(root, t.TempDir(), DefaultPrivateWorkspaceManifest())
		assertPrivateWorkspaceOperationCode(t, err, "root_marker")
		if causes := privateWorkspaceOperationErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the root preparation failure retained", causes)
		}
		if strings.Contains(err.Error(), root) {
			t.Fatalf("message leaked the workspace root: %q", err.Error())
		}
	})

	t.Run("marked root with foreign entries", func(t *testing.T) {
		repository, root := t.TempDir(), filepath.Join(t.TempDir(), "private")
		if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(root, PrivateWorkspaceManifestName)); err != nil {
			t.Fatal(err)
		}
		_, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest())
		assertPrivateWorkspaceOperationCode(t, err, "unmarked_nonempty_root")
		// Refusing to adopt the root follows from the directory listing alone.
		if causes := privateWorkspaceOperationErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for an adoption refusal", causes)
		}
	})

	t.Run("mismatched resume settings", func(t *testing.T) {
		repository, root := t.TempDir(), filepath.Join(t.TempDir(), "private")
		manifest := DefaultPrivateWorkspaceManifest()
		if _, err := InitPrivateWorkspace(root, repository, manifest); err != nil {
			t.Fatal(err)
		}
		manifest.Retention.MaxCandidateAgeDays++
		_, err := InitPrivateWorkspace(root, repository, manifest)
		assertPrivateWorkspaceOperationCode(t, err, "manifest_mismatch")
		// Both the decode and the close succeeded; only the comparison rejects.
		if causes := privateWorkspaceOperationErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a settings comparison", causes)
		}
	})

	t.Run("unreadable manifest", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("owner-only read denial is not observable here")
		}
		repository, root := t.TempDir(), filepath.Join(t.TempDir(), "private")
		if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil {
			t.Fatal(err)
		}
		// Write-only is still owner-only, so the manifest passes the mode gate
		// and fails in the read instead.
		if err := os.Chmod(filepath.Join(root, PrivateWorkspaceManifestName), 0o200); err != nil {
			t.Fatal(err)
		}
		_, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest())
		assertPrivateWorkspaceOperationCode(t, err, "manifest_read")
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) || !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("error %v does not expose the concrete open failure", err)
		}
	})

	t.Run("group-readable manifest", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Unix permission controls")
		}
		repository, root := t.TempDir(), filepath.Join(t.TempDir(), "private")
		if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, PrivateWorkspaceManifestName), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest())
		assertPrivateWorkspaceOperationCode(t, err, "manifest_mode")
		// The stat succeeded; the observed mode is the whole rejection.
		if causes := privateWorkspaceOperationErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a mode-only rejection", causes)
		}
	})
}

func TestPrivateWorkspaceGitBoundaryAttachesCommandAndTraversalCauses(t *testing.T) {
	newIgnoredWorkspace := func(t *testing.T) (string, string) {
		t.Helper()
		repository := newPrivateWorkspaceGitRepository(t)
		if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("private/\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(repository, "private")
		if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil {
			t.Fatal(err)
		}
		absRoot, absRepository, err := privateWorkspaceLocations(root, repository, false)
		if err != nil {
			t.Fatal(err)
		}
		return absRoot, absRepository
	}

	t.Run("failed git command", func(t *testing.T) {
		root, repository := newIgnoredWorkspace(t)
		// A corrupt index leaves ignore resolution working while the tracked
		// listing fails, so the command failure is what rejects the boundary.
		if err := os.WriteFile(filepath.Join(repository, ".git", "index"), []byte("corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := privateWorkspaceGitBoundary(root, repository, false)
		assertPrivateWorkspaceOperationCode(t, err, "git_boundary")
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("error %v does not expose the concrete command failure", err)
		}
		if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), repository) {
			t.Fatalf("message leaked configured locations: %q", err.Error())
		}
	})

	t.Run("unreadable subtree", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("owner-only read denial is not observable here")
		}
		root, repository := newIgnoredWorkspace(t)
		cases := filepath.Join(root, "cases")
		if err := os.Chmod(cases, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(cases, 0o700) })
		err := privateWorkspaceGitBoundary(root, repository, true)
		assertPrivateWorkspaceOperationCode(t, err, "git_boundary")
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) || !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("error %v does not expose the concrete traversal failure", err)
		}
	})

	t.Run("workspace is the repository", func(t *testing.T) {
		repository := newPrivateWorkspaceGitRepository(t)
		absRepository, err := filepath.EvalSymlinks(repository)
		if err != nil {
			t.Fatal(err)
		}
		boundaryErr := privateWorkspaceGitBoundary(absRepository, absRepository, false)
		assertPrivateWorkspaceOperationCode(t, boundaryErr, "git_boundary")
		if causes := privateWorkspaceOperationErrorCauses(t, boundaryErr); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a comparison-only rejection", causes)
		}
	})

	t.Run("unignored workspace", func(t *testing.T) {
		repository := newPrivateWorkspaceGitRepository(t)
		absRepository, err := filepath.EvalSymlinks(repository)
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(absRepository, "private")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		boundaryErr := privateWorkspaceGitBoundary(root, absRepository, false)
		assertPrivateWorkspaceOperationCode(t, boundaryErr, "git_boundary")
		// Ignore status is reported as a boolean, so nothing is retained.
		if causes := privateWorkspaceOperationErrorCauses(t, boundaryErr); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for an ignore-status rejection", causes)
		}
	})

	t.Run("tracked workspace file", func(t *testing.T) {
		root, repository := newIgnoredWorkspace(t)
		tracked := filepath.Join(root, "cases", "tracked.json")
		if err := os.WriteFile(tracked, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command("git", "-C", repository, "add", "-f", "private/cases/tracked.json").CombinedOutput(); err != nil {
			t.Fatalf("git add: %v: %s", err, output)
		}
		err := privateWorkspaceGitBoundary(root, repository, false)
		assertPrivateWorkspaceOperationCode(t, err, "git_boundary")
		// The listing succeeded; its output is what rejects the boundary.
		if causes := privateWorkspaceOperationErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a successful tracked listing", causes)
		}
	})
}

func TestEnsurePrivateWorkspaceDirectoriesAttachesLayoutCauses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission controls")
	}
	t.Run("mode", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, privateWorkspaceFixedDirectories[0]), 0o755); err != nil {
			t.Fatal(err)
		}
		err := ensurePrivateWorkspaceDirectories(root)
		assertPrivateWorkspaceOperationCode(t, err, "layout_mode")
		// The stat succeeded; the observed mode is the whole rejection.
		if causes := privateWorkspaceOperationErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a mode-only rejection", causes)
		}
	})
	t.Run("create", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("owner-only write denial is not observable here")
		}
		root := t.TempDir()
		if err := os.Chmod(root, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
		err := ensurePrivateWorkspaceDirectories(root)
		assertPrivateWorkspaceOperationCode(t, err, "layout_create")
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) || !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("error %v does not expose the concrete create failure", err)
		}
	})
	t.Run("stat", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("owner-only traversal denial is not observable here")
		}
		root := t.TempDir()
		if err := os.Chmod(root, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
		err := ensurePrivateWorkspaceDirectories(root)
		assertPrivateWorkspaceOperationCode(t, err, "layout_stat")
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) || !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("error %v does not expose the concrete stat failure", err)
		}
	})
}

func TestPrivateWorkspaceContractErrorKeepsCausesInspectableAndOutOfTheMessage(t *testing.T) {
	manifestPath := filepath.Join("private", "evaluations", "private-workspace.v4.json")
	readCause := &fs.PathError{Op: "read", Path: manifestPath, Err: fs.ErrPermission}
	var syntaxCause *json.SyntaxError
	if !errors.As(json.Unmarshal([]byte(`{"live_config_env":x}`), new(any)), &syntaxCause) {
		t.Fatal("fixture did not produce a JSON syntax failure")
	}

	err := privateWorkspaceContractError("decode", readCause, nil, syntaxCause)
	// The rendered text is byte-for-byte what this family produced before the
	// sentinel existed.
	if got, want := err.Error(), "private workspace manifest is invalid: decode"; got != want {
		t.Fatalf("message=%q, want %q", got, want)
	}
	if !errors.Is(err, ErrPrivateWorkspaceManifestInvalid) {
		t.Fatalf("err=%v, want the manifest contract sentinel", err)
	}
	if errors.Is(err, ErrPrivateWorkspaceUnhealthy) {
		t.Fatalf("err=%v, want a distinct family from the workspace health sentinel", err)
	}
	if strings.Contains(err.Error(), manifestPath) || strings.Contains(err.Error(), readCause.Error()) ||
		strings.Contains(err.Error(), syntaxCause.Error()) {
		t.Fatalf("message leaked a cause: %q", err.Error())
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) || pathErr.Path != manifestPath {
		t.Fatalf("error %v does not expose the concrete read failure", err)
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) || syntaxErr != syntaxCause {
		t.Fatalf("error %v does not expose the concrete JSON failure", err)
	}
	var classified interface{ Code() string }
	if !errors.As(err, &classified) || classified.Code() != "decode" {
		t.Fatalf("error %v does not expose its stable code", err)
	}
	causes := privateWorkspaceContractErrorCauses(t, err)
	if len(causes) != 2 || causes[0] != error(readCause) || causes[1] != error(syntaxCause) {
		t.Fatalf("causes=%v, want both non-nil causes retained in order", causes)
	}

	// A verdict with nothing in hand classifies exactly as it did before, and a
	// nil passed unguarded is dropped rather than retained.
	cleanErr := privateWorkspaceContractError("retention")
	if got, want := cleanErr.Error(), "private workspace manifest is invalid: retention"; got != want {
		t.Fatalf("message=%q, want %q", got, want)
	}
	if causes := privateWorkspaceContractErrorCauses(t, privateWorkspaceContractError("trailing_data", nil, nil)); len(causes) != 0 {
		t.Fatalf("causes=%v, want nil causes dropped", causes)
	}
}

func TestDecodePrivateWorkspaceManifestAttachesReaderAndJSONCauses(t *testing.T) {
	base := privateWorkspaceContractManifest(t)

	t.Run("reader failure", func(t *testing.T) {
		readErr := errors.New("private manifest reader failed")
		_, err := DecodePrivateWorkspaceManifest(iotest.ErrReader(readErr))
		assertPrivateWorkspaceContractCode(t, err, "decode")
		if !errors.Is(err, readErr) {
			t.Fatalf("error %v does not expose the concrete reader failure", err)
		}
	})

	t.Run("oversize manifest", func(t *testing.T) {
		_, err := DecodePrivateWorkspaceManifest(bytes.NewReader(
			bytes.Repeat([]byte{' '}, maxPrivateWorkspaceManifestBytes+1)))
		assertPrivateWorkspaceContractCode(t, err, "size")
		// The length comparison is the whole rejection.
		if causes := privateWorkspaceContractErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a size-only rejection", causes)
		}
	})

	t.Run("duplicate key", func(t *testing.T) {
		opened := bytes.TrimSuffix(bytes.TrimRight(base, "\n"), []byte("}"))
		duplicated := append(append([]byte(nil), opened...), []byte(`,"schema_version":4}`)...)
		_, err := DecodePrivateWorkspaceManifest(bytes.NewReader(duplicated))
		assertPrivateWorkspaceContractCode(t, err, "decode")
		if causes := privateWorkspaceContractErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the duplicate-key failure retained", causes)
		}
	})

	t.Run("malformed syntax", func(t *testing.T) {
		malformed := bytes.Replace(base, []byte(`"schema_version": 4,`), []byte(`"schema_version": 4x,`), 1)
		if bytes.Equal(malformed, base) {
			t.Fatal("fixture did not contain the expected schema field")
		}
		_, err := DecodePrivateWorkspaceManifest(bytes.NewReader(malformed))
		assertPrivateWorkspaceContractCode(t, err, "decode")
		var syntaxErr *json.SyntaxError
		if !errors.As(err, &syntaxErr) {
			t.Fatalf("error %v does not expose the concrete JSON syntax failure", err)
		}
		if strings.Contains(err.Error(), syntaxErr.Error()) {
			t.Fatalf("message leaked the decoder failure: %q", err.Error())
		}
	})

	t.Run("truncated input", func(t *testing.T) {
		_, err := DecodePrivateWorkspaceManifest(bytes.NewReader(base[:len(base)/2]))
		assertPrivateWorkspaceContractCode(t, err, "decode")
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("error %v does not expose the concrete truncation failure", err)
		}
	})

	t.Run("presence scalar overflow", func(t *testing.T) {
		// The duplicate-key pass deliberately uses json.Number, so this value is
		// syntactically valid there but fails when the presence pass decodes it
		// into an ordinary interface value.
		_, err := DecodePrivateWorkspaceManifest(strings.NewReader("1e999"))
		assertPrivateWorkspaceContractCode(t, err, "decode")
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			t.Fatalf("error %v does not expose the presence-pass type failure", err)
		}
	})

	t.Run("root document is not an object", func(t *testing.T) {
		_, err := DecodePrivateWorkspaceManifest(strings.NewReader("[]"))
		assertPrivateWorkspaceContractCode(t, err, "decode")
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			t.Fatalf("error %v does not expose the root-object type failure", err)
		}
	})

	// Each tier below decodes one nested fragment of the manifest, so a wrongly
	// typed value there must surface that decoder failure under the same code.
	typeTests := map[string]struct {
		code   string
		mutate func(map[string]any)
	}{
		"root scalar tier": {"decode", func(root map[string]any) {
			root["schema_version"] = "4"
		}},
		"strict manifest tier": {"decode", func(root map[string]any) {
			// The presence pass intentionally treats this field as opaque; the
			// strict manifest decoder owns its concrete type failure.
			root["live_config_env"] = float64(5)
		}},
		"execution tier": {"decode", func(root map[string]any) {
			root["execution"] = float64(1)
		}},
		"retention tier": {"decode", func(root map[string]any) {
			root["retention"] = []any{}
		}},
		"run-set tier": {"decode", func(root map[string]any) {
			root["run_sets"] = map[string]any{}
		}},
		"panel tier": {"decode", func(root map[string]any) {
			root["run_sets"].([]any)[0].(map[string]any)["qualitative_review_panel"] = "panel"
		}},
		"reviewer tier": {"decode", func(root map[string]any) {
			privateWorkspaceContractPanel(root)["reviewers"] = map[string]any{}
		}},
		"execution roster tier": {"decode", func(root map[string]any) {
			privateWorkspaceContractPanel(root)["executions"] = "roster"
		}},
		"pricing tier": {"decode", func(root map[string]any) {
			privateWorkspaceContractPanel(root)["executions"].([]any)[0].(map[string]any)["pricing"] = float64(1)
		}},
		"external profile env tier": {"external_mcp_profile_env", func(root map[string]any) {
			root["external_mcp_profile_env"] = float64(1)
		}},
		"run-set kind tier": {"run_set_kind", func(root map[string]any) {
			root["run_sets"].([]any)[0].(map[string]any)["kind"] = float64(1)
		}},
	}
	for name, test := range typeTests {
		t.Run(name, func(t *testing.T) {
			_, err := DecodePrivateWorkspaceManifest(bytes.NewReader(
				privateWorkspaceContractMutation(t, base, test.mutate)))
			assertPrivateWorkspaceContractCode(t, err, test.code)
			var typeErr *json.UnmarshalTypeError
			if !errors.As(err, &typeErr) {
				t.Fatalf("error %v does not expose the concrete JSON type failure", err)
			}
			if strings.Contains(err.Error(), typeErr.Error()) {
				t.Fatalf("message leaked the decoder failure: %q", err.Error())
			}
		})
	}

	// Every rejection below is decided by the decoded document itself, so none of
	// them may acquire a cause.
	cleanTests := map[string]struct {
		code   string
		mutate func(map[string]any)
	}{
		"null value": {"decode", func(root map[string]any) {
			root["retention"].(map[string]any)["retain_baseline_transcripts"] = nil
		}},
		"unknown root key": {"decode", func(root map[string]any) {
			root["unknown_key"] = true
		}},
		"missing root key": {"decode", func(root map[string]any) {
			delete(root, "live_config_env")
		}},
		"unknown reviewer key": {"decode", func(root map[string]any) {
			privateWorkspaceContractPanel(root)["reviewers"].([]any)[0].(map[string]any)["unknown_key"] = true
		}},
		"unknown pricing key": {"decode", func(root map[string]any) {
			privateWorkspaceContractPanel(root)["executions"].([]any)[0].(map[string]any)["pricing"].(map[string]any)["unknown_key"] = true
		}},
		// The required-key gate rejects both booleans before the dedicated
		// presence codes can fire, so retention_presence and
		// qualitative_review_presence stay defensive and cause-free.
		"missing retention boolean": {"decode", func(root map[string]any) {
			delete(root["retention"].(map[string]any), "retain_baseline_transcripts")
		}},
		"missing qualitative boolean": {"decode", func(root map[string]any) {
			delete(root["run_sets"].([]any)[0].(map[string]any), "qualitative_review_required")
		}},
		"empty external profile env": {"external_mcp_profile_env", func(root map[string]any) {
			root["external_mcp_profile_env"] = ""
		}},
		"empty run-set kind": {"run_set_kind", func(root map[string]any) {
			root["run_sets"].([]any)[0].(map[string]any)["kind"] = ""
		}},
		"empty execution roster": {"reviewer_execution", func(root map[string]any) {
			privateWorkspaceContractPanel(root)["executions"] = []any{}
		}},
		"out-of-range retention": {"retention", func(root map[string]any) {
			root["retention"].(map[string]any)["max_candidate_age_days"] = float64(0)
		}},
		"unsupported schema version": {"schema_version", func(root map[string]any) {
			// The panel and its reserve are tied to the current schema, so they are
			// dropped to reach the version verdict itself.
			runSet := root["run_sets"].([]any)[0].(map[string]any)
			delete(runSet, "qualitative_review_panel")
			delete(runSet, "reviewer_reserve_microusd")
			root["schema_version"] = float64(99)
		}},
		"duplicate spec path": {"spec_path", func(root map[string]any) {
			paths := root["run_sets"].([]any)[0].(map[string]any)["spec_paths"].([]any)
			paths[1] = paths[0]
		}},
		"invalid panel range": {"qualitative_review_panel", func(root map[string]any) {
			privateWorkspaceContractPanel(root)["max_criterion_range_bps"] = float64(0)
		}},
		"invalid reviewer reasoning": {"reviewer_execution", func(root map[string]any) {
			privateWorkspaceContractPanel(root)["executions"].([]any)[0].(map[string]any)["reasoning"] = "max"
		}},
	}
	for name, test := range cleanTests {
		t.Run(name, func(t *testing.T) {
			_, err := DecodePrivateWorkspaceManifest(bytes.NewReader(
				privateWorkspaceContractMutation(t, base, test.mutate)))
			assertPrivateWorkspaceContractCode(t, err, test.code)
			if causes := privateWorkspaceContractErrorCauses(t, err); len(causes) != 0 {
				t.Fatalf("causes=%v, want none for a document-only verdict", causes)
			}
		})
	}

	t.Run("trailing data is rejected before the trailing gate", func(t *testing.T) {
		trailing := append(append([]byte(nil), base...), []byte("{}\n")...)
		_, err := DecodePrivateWorkspaceManifest(bytes.NewReader(trailing))
		// The duplicate-key pass already refuses a second document, so the
		// trailing_data code stays defensive. Its cause handling is pinned on the
		// constructor instead.
		assertPrivateWorkspaceContractCode(t, err, "decode")
		if causes := privateWorkspaceContractErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the trailing-document failure retained", causes)
		}
	})
}

func TestEncodePrivateWorkspaceManifestReportsContractVerdictsWithoutCauses(t *testing.T) {
	manifest := DefaultPrivateWorkspaceManifest()
	manifest.Execution.MaxEstimatedCostMicroUSD = 0
	// Validation runs before the marshal, so an invalid manifest never reaches
	// the defensive encode branch and its verdict stays cause-free.
	_, err := EncodePrivateWorkspaceManifest(manifest)
	assertPrivateWorkspaceContractCode(t, err, "execution")
	if causes := privateWorkspaceContractErrorCauses(t, err); len(causes) != 0 {
		t.Fatalf("causes=%v, want none for a range verdict", causes)
	}
	if _, err := EncodePrivateWorkspaceManifest(DefaultPrivateWorkspaceManifest()); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
}

func TestPrivateWorkspaceManifestContractCausesTraverseNestedOperationErrors(t *testing.T) {
	repository, root := t.TempDir(), filepath.Join(t.TempDir(), "private")
	if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil {
		t.Fatal(err)
	}
	privateMarker := "PRIVATE_WORKSPACE_TRAVERSAL_MARKER"
	if err := os.WriteFile(filepath.Join(root, PrivateWorkspaceManifestName),
		[]byte(`{"schema_version":4,"live_config_env":"`+privateMarker+`"x}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadPrivateWorkspaceManifest(root)
	assertPrivateWorkspaceOperationCode(t, err, "manifest_mismatch")
	// The contract classification and the decoder failure it retained both stay
	// reachable below the unchanged operation code.
	if !errors.Is(err, ErrPrivateWorkspaceManifestInvalid) {
		t.Fatalf("error %v does not expose the nested manifest contract sentinel", err)
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("error %v does not expose the concrete JSON syntax failure", err)
	}
	if strings.Contains(err.Error(), privateMarker) || strings.Contains(err.Error(), root) ||
		strings.Contains(err.Error(), syntaxErr.Error()) {
		t.Fatalf("message leaked private manifest content: %q", err.Error())
	}
	var classified interface{ Code() string }
	if !errors.As(err, &classified) || classified.Code() != "manifest_mismatch" {
		t.Fatalf("error %v does not expose the outer operation code", err)
	}
}

// privateWorkspaceContractManifest builds a valid current-schema manifest whose
// run set carries a full executable review panel, so mutations can reach every
// nested decoding tier.
func privateWorkspaceContractManifest(t *testing.T) []byte {
	t.Helper()
	manifest := DefaultPrivateWorkspaceManifest()
	panel := privateReviewTestPanel()
	for _, reviewer := range panel.Reviewers {
		panel.Executions = append(panel.Executions, PrivateReviewerExecution{ReviewerID: reviewer.ID,
			Reasoning: "high", TimeoutSeconds: 60, MaxEstimatedCostMicroUSD: 10,
			Pricing: Pricing{InputMicroUSDPerMillionTokens: 1, OutputMicroUSDPerMillionTokens: 2}})
	}
	manifest.RunSets = []PrivateWorkspaceRunSet{{Alias: "comparison",
		SpecPaths:              []string{"cases/comparison/cli.json", "cases/comparison/mcp.json"},
		QualitativeReviewPanel: &panel, ReviewerReserveMicroUSD: 60}}
	data, err := EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data)); err != nil {
		t.Fatalf("contract fixture is not a valid manifest: %v", err)
	}
	return data
}

func privateWorkspaceContractPanel(root map[string]any) map[string]any {
	return root["run_sets"].([]any)[0].(map[string]any)["qualitative_review_panel"].(map[string]any)
}

func privateWorkspaceContractMutation(t *testing.T, base []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(base, &root); err != nil {
		t.Fatal(err)
	}
	mutate(root)
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertPrivateWorkspaceContractCode(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, ErrPrivateWorkspaceManifestInvalid) {
		t.Fatalf("err=%v, want the manifest contract sentinel", err)
	}
	if got, want := err.Error(), ErrPrivateWorkspaceManifestInvalid.Error()+": "+code; got != want {
		t.Fatalf("message=%q, want %q", got, want)
	}
}

func privateWorkspaceContractErrorCauses(t *testing.T, err error) []error {
	t.Helper()
	multi, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("%T does not unwrap to multiple errors", err)
	}
	tree := multi.Unwrap()
	if len(tree) == 0 || !errors.Is(tree[0], ErrPrivateWorkspaceManifestInvalid) {
		t.Fatalf("unwrap tree=%v, want the sentinel first", tree)
	}
	return tree[1:]
}

func assertPrivateWorkspaceOperationCode(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, ErrPrivateWorkspaceUnhealthy) {
		t.Fatalf("err=%v, want the workspace sentinel", err)
	}
	if got, want := err.Error(), ErrPrivateWorkspaceUnhealthy.Error()+": "+code; got != want {
		t.Fatalf("message=%q, want %q", got, want)
	}
}

func privateWorkspaceOperationErrorCauses(t *testing.T, err error) []error {
	t.Helper()
	multi, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("%T does not unwrap to multiple errors", err)
	}
	tree := multi.Unwrap()
	if len(tree) == 0 || !errors.Is(tree[0], ErrPrivateWorkspaceUnhealthy) {
		t.Fatalf("unwrap tree=%v, want the sentinel first", tree)
	}
	return tree[1:]
}

func newPrivateWorkspaceGitRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	if output, err := exec.Command("git", "-C", repository, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	return repository
}

func privateWorkspacePaths(root string, names []string) []string {
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, filepath.Join(root, name))
	}
	return paths
}

func privateWorkspaceCheckStatus(report PrivateWorkspaceReport, code string) string {
	for _, check := range report.Checks {
		if check.Code == code {
			return check.Status
		}
	}
	return ""
}

func privateWorkspaceDoctorCode(code string) bool {
	for _, allowed := range []string{
		PrivateWorkspaceCheckRootExists, PrivateWorkspaceCheckRootOwnerOnly,
		PrivateWorkspaceCheckRootMarker, PrivateWorkspaceCheckGitBoundary,
		PrivateWorkspaceCheckManifestMode, PrivateWorkspaceCheckManifestValid,
		PrivateWorkspaceCheckFixedLayout, PrivateWorkspaceCheckTreeOwnerOnly,
		PrivateWorkspaceCheckTreeNoSymlinks, PrivateWorkspaceCheckSpecsContained,
		PrivateWorkspaceCheckSpecsValid, PrivateWorkspaceCheckScratchClean,
		PrivateWorkspaceCheckLifecycleValid,
	} {
		if code == allowed {
			return true
		}
	}
	return false
}
