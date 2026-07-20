package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
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
