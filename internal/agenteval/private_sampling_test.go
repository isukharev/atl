package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrivateSamplingPreviewIsContentFreeDeterministicAndReadOnly(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	primary := fixture.addResult(t, 1, "primary-01", privateSamplingResult(t, "jira.primary-evidence", true), strings.Repeat("1", 64))
	fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierCalibration,
		Primary: []PrivateFindingRunRef{primary}})
	before := privateCheckpointTree(t, fixture.root)
	preview, _, err := previewPrivateSampling(fixture.options(), fixture.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	after := privateCheckpointTree(t, fixture.root)
	if !bytes.Equal(before, after) {
		t.Fatalf("preview mutated workspace\nbefore=%s\nafter=%s", before, after)
	}
	if preview.SchemaVersion != 1 || preview.Tier != PrivateSamplingTierCalibration || !preview.EvidenceReady ||
		preview.RegressionAccepted != nil || preview.Primary.Observed != 1 || preview.Primary.Statuses.Pass != 1 ||
		preview.Holdout.Observed != 0 || !validSHA256(preview.SourceSHA256) || !validSHA256(preview.AssessmentSHA256) {
		t.Fatalf("preview=%+v", preview)
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{primary.PlanID, primary.Baseline, "jira.primary-evidence", "model-v1", fixture.root, fixture.repository} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("private value %q leaked in %s", private, encoded)
		}
	}
	second, _, err := previewPrivateSampling(fixture.options(), fixture.dependencies())
	if err != nil || second.AssessmentSHA256 != preview.AssessmentSHA256 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestPrivateSamplingPublicExampleMatchesGoContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-sampling.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec, canonical, err := decodePrivateSamplingSpec(data)
	if err != nil || spec.SchemaVersion != PrivateSamplingSchemaVersion || spec.Tier != PrivateSamplingTierRegression ||
		len(spec.Primary) != 3 || len(spec.Holdout) != 1 || !bytes.Equal(data, canonical) {
		t.Fatalf("spec=%+v canonical=%t err=%v", spec, bytes.Equal(data, canonical), err)
	}
	var schema any
	schemaData, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-sampling.schema.json"))
	if err != nil || json.Unmarshal(schemaData, &schema) != nil {
		t.Fatalf("public schema is invalid JSON: %v", err)
	}
}

func TestPrivateSamplingPreviewUsesWorkspaceDoctor(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private")
	if report, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil || !report.Healthy {
		t.Fatalf("init report=%+v err=%v", report, err)
	}
	fixture := &privateSamplingFixture{root: root, repository: repository, sources: map[string]PrivateBaselineSource{}}
	if err := os.MkdirAll(filepath.Join(root, "cases", "sampling"), 0o700); err != nil {
		t.Fatal(err)
	}
	primary := fixture.addResult(t, 1, "primary-01", privateSamplingResult(t, "jira.primary-evidence", true), strings.Repeat("1", 64))
	fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierCalibration,
		Primary: []PrivateFindingRunRef{primary}})
	dependencies := fixture.dependencies()
	dependencies.doctor = DoctorPrivateWorkspace
	preview, _, err := previewPrivateSampling(fixture.options(), dependencies)
	if err != nil || !preview.EvidenceReady || preview.Primary.Observed != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	options := fixture.options()
	options.ExpectedAssessmentSHA256, options.Confirm = preview.AssessmentSHA256, PrivateSamplingConfirmation
	first, err := applyPrivateSampling(options, dependencies)
	if err != nil || !first.Stored {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := applyPrivateSampling(options, dependencies)
	if err != nil || second.Stored {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestPrivateSamplingRegressionRequiresPassingPrimaryAndHoldout(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	primary := fixture.addPrimary(t, 3, true)
	holdout := fixture.addHoldout(t, 4, true)
	fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression, Primary: primary, Holdout: holdout})
	preview, _, err := previewPrivateSampling(fixture.options(), fixture.dependencies())
	if err != nil || preview.RegressionAccepted == nil || !*preview.RegressionAccepted || preview.Primary.Observed != 3 ||
		preview.Primary.Eligibility.Supported != 3 || preview.Holdout.Observed != 1 || preview.Holdout.Statuses.Pass != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}

	fixture = newPrivateSamplingFixture(t)
	primary = fixture.addPrimary(t, 3, true)
	holdout = fixture.addHoldout(t, 4, false)
	fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression, Primary: primary, Holdout: holdout})
	preview, _, err = previewPrivateSampling(fixture.options(), fixture.dependencies())
	if err != nil || preview.RegressionAccepted == nil || *preview.RegressionAccepted || preview.Holdout.Statuses.Fail != 1 {
		t.Fatalf("failed holdout preview=%+v err=%v", preview, err)
	}
}

func TestPrivateSamplingUnsupportedOrDriftCannotPassRegression(t *testing.T) {
	for _, eligibility := range []string{EligibilityUnsupportedCapability, EligibilityInvalidatedDrift} {
		t.Run(eligibility, func(t *testing.T) {
			fixture := newPrivateSamplingFixture(t)
			primary := fixture.addPrimary(t, 3, true)
			holdoutResult := privateSamplingResult(t, "jira.holdout-evidence", true)
			holdoutResult.Eligibility = eligibility
			holdoutResult.Status = "ineligible"
			if eligibility == EligibilityUnsupportedCapability {
				holdoutResult.UnavailableCapabilities = []string{"jira.issue.history"}
			}
			holdout := fixture.addResult(t, 4, "holdout-01", holdoutResult, strings.Repeat("2", 64))
			fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
				Primary: primary, Holdout: []PrivateFindingRunRef{holdout}})
			preview, _, err := previewPrivateSampling(fixture.options(), fixture.dependencies())
			if err != nil || preview.RegressionAccepted == nil || *preview.RegressionAccepted || preview.Holdout.Statuses.Ineligible != 1 {
				t.Fatalf("preview=%+v err=%v", preview, err)
			}
		})
	}
}

func TestPrivateSamplingDecisionRequiresTenButMakesNoAutomaticDecision(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	primary := fixture.addPrimary(t, 10, true)
	holdout := fixture.addHoldout(t, 11, true)
	fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierDecision, Primary: primary, Holdout: holdout})
	preview, _, err := previewPrivateSampling(fixture.options(), fixture.dependencies())
	if err != nil || !preview.EvidenceReady || preview.RegressionAccepted != nil || preview.Primary.Observed != 10 || preview.Holdout.Observed != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
}

func TestPrivateSamplingRejectsUnhealthyOrActiveWorkspace(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	primary := fixture.addResult(t, 1, "primary-01", privateSamplingResult(t, "jira.primary-evidence", true), strings.Repeat("1", 64))
	fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierCalibration,
		Primary: []PrivateFindingRunRef{primary}})
	for _, test := range []struct {
		name   string
		report PrivateWorkspaceReport
	}{
		{"unhealthy", PrivateWorkspaceReport{SchemaVersion: 1, Healthy: false, State: "unhealthy"}},
		{"active", PrivateWorkspaceReport{SchemaVersion: 1, Healthy: true, State: "run_in_progress", Counts: PrivateWorkspaceCounts{ActiveRuns: 1}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencies := fixture.dependencies()
			dependencies.doctor = func(_, _ string) (PrivateWorkspaceReport, error) { return test.report, nil }
			if _, _, err := previewPrivateSampling(fixture.options(), dependencies); !errors.Is(err, ErrPrivateSamplingRejected) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestPrivateSamplingApplyIsDigestBoundOwnerOnlyAndIdempotent(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
		Primary: fixture.addPrimary(t, 3, true), Holdout: fixture.addHoldout(t, 4, true)})
	options := fixture.options()
	preview, assessment, err := previewPrivateSampling(options, fixture.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	options.ExpectedAssessmentSHA256, options.Confirm = preview.AssessmentSHA256, PrivateSamplingConfirmation
	first, err := applyPrivateSampling(options, fixture.dependencies())
	if err != nil || !first.Stored || first.RegressionAccepted == nil || !*first.RegressionAccepted {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	path := filepath.Join(fixture.root, "reports", "sampling", preview.AssessmentSHA256+".json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("info=%v err=%v", info, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, assessment) {
		t.Fatalf("stored assessment drift: %v", err)
	}
	second, err := applyPrivateSampling(options, fixture.dependencies())
	if err != nil || second.Stored || second.AssessmentSHA256 != first.AssessmentSHA256 {
		t.Fatalf("second=%+v err=%v", second, err)
	}

	fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierCalibration,
		Primary: []PrivateFindingRunRef{fixture.primary[0]}})
	if _, err := applyPrivateSampling(options, fixture.dependencies()); !errors.Is(err, ErrPrivateSamplingRejected) {
		t.Fatalf("changed spec apply err=%v", err)
	}
}

func TestPrivateSamplingRejectsCardinalityDuplicatesAndIncompatibility(t *testing.T) {
	t.Run("regression cardinality", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		spec := PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: fixture.addPrimary(t, 2, true), Holdout: fixture.addHoldout(t, 3, true)}
		fixture.writeRawSpec(t, spec)
		fixture.previewMustReject(t)
	})
	t.Run("decision cardinality", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		spec := PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierDecision,
			Primary: fixture.addPrimary(t, 9, true), Holdout: fixture.addHoldout(t, 10, true)}
		fixture.writeRawSpec(t, spec)
		fixture.previewMustReject(t)
	})
	t.Run("duplicate plan", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addPrimary(t, 3, true)
		holdout := primary[0]
		holdout.Baseline = "other"
		fixture.writeRawSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: []PrivateFindingRunRef{holdout}})
		fixture.previewMustReject(t)
	})
	t.Run("current baseline", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addPrimary(t, 3, true)
		primary[0].Baseline = "current"
		fixture.writeRawSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: fixture.addHoldout(t, 4, true)})
		fixture.previewMustReject(t)
	})
	t.Run("mutable source", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addPrimary(t, 3, true)
		source := fixture.sources[primary[0].PlanID]
		source.Immutable = false
		fixture.sources[primary[0].PlanID] = source
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: fixture.addHoldout(t, 4, true)})
		fixture.previewMustReject(t)
	})
	t.Run("duplicate plan digest", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addPrimary(t, 3, true)
		first := fixture.sources[primary[0].PlanID]
		second := fixture.sources[primary[1].PlanID]
		second.PlanSHA256 = first.PlanSHA256
		fixture.sources[primary[1].PlanID] = second
		fixture.rewriteManifestPlanSHA(t, primary[1], second)
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: fixture.addHoldout(t, 4, true)})
		fixture.previewMustReject(t)
	})
	t.Run("duplicate completed run", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addPrimary(t, 3, true)
		first := fixture.sources[primary[0].PlanID]
		second := fixture.sources[primary[1].PlanID]
		second.RunID = first.RunID
		fixture.sources[primary[1].PlanID] = second
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: fixture.addHoldout(t, 4, true)})
		fixture.previewMustReject(t)
	})
	t.Run("primary contract", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addPrimary(t, 2, true)
		changed := privateSamplingResult(t, "jira.primary-evidence", true)
		primary = append(primary, fixture.addResult(t, 3, "primary-03", changed, strings.Repeat("3", 64)))
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: fixture.addHoldout(t, 4, true)})
		fixture.previewMustReject(t)
	})
	t.Run("primary runtime", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addPrimary(t, 2, true)
		changed := privateSamplingResult(t, "jira.primary-evidence", true)
		changed.Runtime.ATLVersion = "different"
		primary = append(primary, fixture.addResult(t, 3, "primary-03", changed, strings.Repeat("1", 64)))
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: fixture.addHoldout(t, 4, true)})
		fixture.previewMustReject(t)
	})
	t.Run("same scenario holdout", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addPrimary(t, 3, true)
		holdout := fixture.addResult(t, 4, "holdout-01", privateSamplingResult(t, "jira.primary-evidence", true), strings.Repeat("2", 64))
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: []PrivateFindingRunRef{holdout}})
		fixture.previewMustReject(t)
	})
	t.Run("empty prompt same case contract", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addPrimary(t, 3, true)
		holdout := fixture.addResult(t, 4, "holdout-01", privateSamplingResult(t, "jira.holdout-evidence", true), strings.Repeat("1", 64))
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: []PrivateFindingRunRef{holdout}})
		fixture.previewMustReject(t)
	})
	t.Run("equal bound prompt", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := make([]PrivateFindingRunRef, 0, 3)
		for index := 1; index <= 3; index++ {
			primary = append(primary, fixture.addResult(t, index, fmt.Sprintf("primary-%02d", index),
				privateSamplingCodexResult(t, "jira.primary-evidence", strings.Repeat("a", 64)), strings.Repeat("1", 64)))
		}
		holdout := fixture.addResult(t, 4, "holdout-01",
			privateSamplingCodexResult(t, "jira.holdout-evidence", strings.Repeat("a", 64)), strings.Repeat("2", 64))
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: []PrivateFindingRunRef{holdout}})
		fixture.previewMustReject(t)
	})
	t.Run("runtime drift", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addPrimary(t, 3, true)
		holdoutResult := privateSamplingResult(t, "jira.holdout-evidence", true)
		holdoutResult.Runtime.Model = "different"
		holdout := fixture.addResult(t, 4, "holdout-01", holdoutResult, strings.Repeat("2", 64))
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: []PrivateFindingRunRef{holdout}})
		fixture.previewMustReject(t)
	})
}

func TestPrivateSamplingRejectsUnsafeSpecAndAssessmentPaths(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	spec := PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
		Primary: fixture.addPrimary(t, 3, true), Holdout: fixture.addHoldout(t, 4, true)}
	fixture.writeSpec(t, spec)
	path := fixture.specPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.TrimSpace(data), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.previewMustReject(t)

	if runtime.GOOS == "windows" {
		return
	}
	fixture.writeSpec(t, spec)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.previewMustReject(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	fixture.previewMustReject(t)

	for _, mode := range []os.FileMode{0o755} {
		candidate := newPrivateSamplingFixture(t)
		candidateSpec := PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: candidate.addPrimary(t, 3, true), Holdout: candidate.addHoldout(t, 4, true)}
		candidate.writeSpec(t, candidateSpec)
		if err := os.Chmod(filepath.Dir(candidate.specPath()), mode); err != nil {
			t.Fatal(err)
		}
		candidate.previewMustReject(t)
	}
	candidate := newPrivateSamplingFixture(t)
	external := filepath.Join(t.TempDir(), "sampling")
	if err := os.Mkdir(external, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(candidate.specPath())); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Dir(candidate.specPath())); err != nil {
		t.Fatal(err)
	}
	candidate.previewMustReject(t)

	for _, test := range []struct {
		name  string
		setup func(t *testing.T, fixture *privateSamplingFixture, directory, assessmentPath string, assessment []byte)
	}{
		{"directory symlink", func(t *testing.T, _ *privateSamplingFixture, directory, _ string, _ []byte) {
			target := filepath.Join(t.TempDir(), "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, directory); err != nil {
				t.Fatal(err)
			}
		}},
		{"loose directory", func(t *testing.T, _ *privateSamplingFixture, directory, _ string, _ []byte) {
			if err := os.Mkdir(directory, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(directory, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"file symlink", func(t *testing.T, _ *privateSamplingFixture, directory, assessmentPath string, _ []byte) {
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "assessment.json")
			if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, assessmentPath); err != nil {
				t.Fatal(err)
			}
		}},
		{"loose file", func(t *testing.T, _ *privateSamplingFixture, directory, assessmentPath string, assessment []byte) {
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(assessmentPath, assessment, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(assessmentPath, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"conflicting file", func(t *testing.T, _ *privateSamplingFixture, directory, assessmentPath string, _ []byte) {
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(assessmentPath, []byte("conflict\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := newPrivateSamplingFixture(t)
			candidate.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
				Primary: candidate.addPrimary(t, 3, true), Holdout: candidate.addHoldout(t, 4, true)})
			preview, assessment, err := previewPrivateSampling(candidate.options(), candidate.dependencies())
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(candidate.root, "reports", "sampling")
			assessmentPath := filepath.Join(directory, preview.AssessmentSHA256+".json")
			test.setup(t, candidate, directory, assessmentPath, assessment)
			options := candidate.options()
			options.ExpectedAssessmentSHA256, options.Confirm = preview.AssessmentSHA256, PrivateSamplingConfirmation
			if _, err := applyPrivateSampling(options, candidate.dependencies()); !errors.Is(err, ErrPrivateSamplingRejected) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

type privateSamplingFixture struct {
	root, repository string
	sources          map[string]PrivateBaselineSource
	primary          []PrivateFindingRunRef
}

func newPrivateSamplingFixture(t *testing.T) *privateSamplingFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "private")
	for _, directory := range []string{root, filepath.Join(root, "cases", "sampling"), filepath.Join(root, "reports"),
		filepath.Join(root, "baselines"), filepath.Join(root, ".ephemeral")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return &privateSamplingFixture{root: root, repository: t.TempDir(), sources: map[string]PrivateBaselineSource{}}
}

func (fixture *privateSamplingFixture) options() PrivateSamplingOptions {
	return PrivateSamplingOptions{Root: fixture.root, RepositoryRoot: fixture.repository, Spec: "sample-set"}
}

func (fixture *privateSamplingFixture) dependencies() privateSamplingDependencies {
	return privateSamplingDependencies{
		doctor: func(_, _ string) (PrivateWorkspaceReport, error) {
			return PrivateWorkspaceReport{SchemaVersion: 1, Healthy: true, State: "ready"}, nil
		},
		load: func(_, _, planID string) (PrivateBaselineSource, error) {
			source, ok := fixture.sources[planID]
			if !ok {
				return PrivateBaselineSource{}, errors.New("missing source")
			}
			return source, nil
		},
	}
}

func (fixture *privateSamplingFixture) addPrimary(t *testing.T, count int, pass bool) []PrivateFindingRunRef {
	t.Helper()
	refs := make([]PrivateFindingRunRef, 0, count)
	for index := 1; index <= count; index++ {
		refs = append(refs, fixture.addResult(t, index, fmt.Sprintf("primary-%02d", index),
			privateSamplingResult(t, "jira.primary-evidence", pass), strings.Repeat("1", 64)))
	}
	fixture.primary = append([]PrivateFindingRunRef(nil), refs...)
	return refs
}

func (fixture *privateSamplingFixture) addHoldout(t *testing.T, index int, pass bool) []PrivateFindingRunRef {
	t.Helper()
	return []PrivateFindingRunRef{fixture.addResult(t, index, "holdout-01",
		privateSamplingResult(t, "jira.holdout-evidence", pass), strings.Repeat("2", 64))}
}

func (fixture *privateSamplingFixture) addResult(t *testing.T, index int, baseline string, result Result, contractSHA256 string) PrivateFindingRunRef {
	t.Helper()
	planID := fmt.Sprintf("pln-%032x", index)
	planSHA256 := sha256HexBytes([]byte(planID))
	baselineRoot := filepath.Join(fixture.root, "baselines", contractSHA256, baseline)
	resultPath := filepath.Join(baselineRoot, "surfaces", result.EffectiveSurface(), "result.json")
	if err := os.MkdirAll(filepath.Dir(resultPath), 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivateBaselineResult(t, resultPath, result)
	treeSHA256, _, _, err := hashPrivateTree(baselineRoot, "baseline.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest := privateBaselineManifest{SchemaVersion: PrivateBaselineSchemaVersion, Baseline: baseline,
		ContractSHA256: contractSHA256, PlanSHA256: planSHA256, TreeSHA256: treeSHA256,
		Surfaces: []privateBaselineSurface{{Surface: result.EffectiveSurface(),
			ResultPath:   filepath.ToSlash(filepath.Join("surfaces", result.EffectiveSurface(), "result.json")),
			ResultSHA256: privateFindingResultFileSHA256(t, resultPath)}}}
	manifestData, err := encodePrivateBaselineManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baselineRoot, "baseline.v1.json"), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.sources[planID] = PrivateBaselineSource{PlanID: planID, PlanSHA256: planSHA256,
		ContractSHA256: contractSHA256, RunID: fmt.Sprintf("run-%032x", index), Completed: true, Immutable: true}
	return PrivateFindingRunRef{PlanID: planID, Surface: result.EffectiveSurface(), Baseline: baseline}
}

func (fixture *privateSamplingFixture) specPath() string {
	return filepath.Join(fixture.root, "cases", "sampling", "sample-set.v1.json")
}

func (fixture *privateSamplingFixture) rewriteManifestPlanSHA(t *testing.T, ref PrivateFindingRunRef, source PrivateBaselineSource) {
	t.Helper()
	path := filepath.Join(fixture.root, "baselines", source.ContractSHA256, ref.Baseline, "baseline.v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := decodePrivateBaselineManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	manifest.PlanSHA256 = source.PlanSHA256
	data, err = encodePrivateBaselineManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (fixture *privateSamplingFixture) writeSpec(t *testing.T, spec PrivateSamplingSpec) {
	t.Helper()
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.specPath(), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fixture.specPath(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (fixture *privateSamplingFixture) writeRawSpec(t *testing.T, spec PrivateSamplingSpec) {
	t.Helper()
	fixture.writeSpec(t, spec)
}

func (fixture *privateSamplingFixture) previewMustReject(t *testing.T) {
	t.Helper()
	if _, _, err := previewPrivateSampling(fixture.options(), fixture.dependencies()); !errors.Is(err, ErrPrivateSamplingRejected) {
		t.Fatalf("err=%v", err)
	}
}

func privateSamplingResult(t *testing.T, scenarioID string, pass bool) Result {
	t.Helper()
	result := privateFindingTestResult(t, pass)
	result.ScenarioID = scenarioID
	result.Runtime.AgentVersion = "agent-v1"
	result.Runtime.Model = "model-v1"
	result.Runtime.Reasoning = "high"
	result.Runtime.PluginVersion = "plugin-v1"
	result.Runtime.SkillDigest = strings.Repeat("d", 64)
	return result
}

func privateSamplingCodexResult(t *testing.T, scenarioID, promptSHA256 string) Result {
	t.Helper()
	result := privateSamplingResult(t, scenarioID, true)
	result.Runtime.Provider = "codex"
	result.Runtime.SkillActivation = SkillActivationImplicit
	result.Runtime.PromptContractSHA256 = promptSHA256
	return result
}
