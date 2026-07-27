package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

func TestPrivateSyntheticSamplingPublicExampleMatchesGoContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-synthetic-sampling.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec, canonical, err := decodePrivateSyntheticSamplingSpec(data)
	if err != nil || spec.SchemaVersion != PrivateSyntheticSamplingSchemaVersion ||
		spec.Tier != PrivateSamplingTierRegression || len(spec.Holdout) != 1 ||
		!bytes.Equal(data, canonical) {
		t.Fatalf("spec=%+v canonical=%t err=%v", spec, bytes.Equal(data, canonical), err)
	}
	var schema any
	schemaData, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-synthetic-sampling.schema.json"))
	if err != nil || json.Unmarshal(schemaData, &schema) != nil {
		t.Fatalf("public schema is invalid JSON: %v", err)
	}
}

func TestPrivateSyntheticSamplingUsesCanonicalPreviewAndApplyLifecycle(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	primary := fixture.addSyntheticRoot(t, "primary-synthetic-runs", "jira.synthetic-primary", 3, true,
		strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64))
	holdout := fixture.addSyntheticRoot(t, "holdout-synthetic-runs", "jira.synthetic-holdout", 1, true,
		strings.Repeat("4", 64), strings.Repeat("5", 64), strings.Repeat("6", 64))
	fixture.writeSyntheticSpec(t, PrivateSyntheticSamplingSpec{
		SchemaVersion: PrivateSyntheticSamplingSchemaVersion, Tier: PrivateSamplingTierRegression,
		Primary: primary, Holdout: []PrivateSyntheticSamplingRootRef{holdout},
	})
	before := privateCheckpointTree(t, fixture.root)
	preview, assessment, err := previewPrivateSampling(fixture.options(), fixture.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	after := privateCheckpointTree(t, fixture.root)
	if !bytes.Equal(before, after) {
		t.Fatalf("preview mutated workspace\nbefore=%s\nafter=%s", before, after)
	}
	if preview.SchemaVersion != PrivateSyntheticSamplingSchemaVersion ||
		preview.Tier != PrivateSamplingTierRegression || !preview.EvidenceReady ||
		preview.RegressionAccepted == nil || !*preview.RegressionAccepted ||
		preview.Primary.Observed != 3 || preview.Primary.Statuses.Pass != 3 ||
		preview.Holdout.Observed != 1 || preview.Holdout.Statuses.Pass != 1 ||
		!validSHA256(preview.SourceSHA256) || !validSHA256(preview.AssessmentSHA256) {
		t.Fatalf("preview=%+v", preview)
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		primary.Root, primary.SourceSHA256, holdout.Root, holdout.SourceSHA256,
		"jira.synthetic-primary", "jira.synthetic-holdout", fixture.root,
	} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("private value %q leaked in %s", private, encoded)
		}
	}
	options := fixture.options()
	options.ExpectedAssessmentSHA256, options.Confirm = preview.AssessmentSHA256, PrivateSamplingConfirmation
	first, err := applyPrivateSampling(options, fixture.dependencies())
	if err != nil || !first.Stored {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	path := filepath.Join(fixture.root, "reports", "sampling", preview.AssessmentSHA256+".json")
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, assessment) {
		t.Fatalf("stored assessment drift: %v", err)
	}
	second, err := applyPrivateSampling(options, fixture.dependencies())
	if err != nil || second.Stored {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	loaded, loadedPrimary, loadedHoldout, err := loadPrivateSyntheticSamplingAssessment(fixture.root, preview.AssessmentSHA256)
	if err != nil || loaded.SchemaVersion != 2 || len(loadedPrimary) != 3 || len(loadedHoldout) != 1 {
		t.Fatalf("loaded=%+v primary=%d holdout=%d err=%v", loaded, len(loadedPrimary), len(loadedHoldout), err)
	}
	primaryMarker := filepath.Join(fixture.root, "reports", privateSyntheticRootDirectory, primary.Root, privateOutputRootMarker)
	if err := os.WriteFile(primaryMarker, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadPrivateSyntheticSamplingAssessment(fixture.root, preview.AssessmentSHA256); !errors.Is(err, ErrPrivateSamplingRejected) {
		t.Fatalf("changed root load err=%v", err)
	}
}

func TestPrivateSyntheticSamplingRejectsUnattestedDriftingAndIncompatibleRoots(t *testing.T) {
	t.Run("wrong source digest", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addSyntheticRoot(t, "primary-synthetic-runs", "jira.synthetic-primary", 1, true,
			strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64))
		primary.SourceSHA256 = strings.Repeat("9", 64)
		fixture.writeSyntheticSpec(t, PrivateSyntheticSamplingSpec{
			SchemaVersion: 2, Tier: PrivateSamplingTierCalibration, Primary: primary,
		})
		fixture.previewMustReject(t)
	})
	t.Run("regression cardinality", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addSyntheticRoot(t, "primary-synthetic-runs", "jira.synthetic-primary", 2, true,
			strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64))
		holdout := fixture.addSyntheticRoot(t, "holdout-synthetic-runs", "jira.synthetic-holdout", 1, true,
			strings.Repeat("4", 64), strings.Repeat("5", 64), strings.Repeat("6", 64))
		fixture.writeSyntheticSpec(t, PrivateSyntheticSamplingSpec{
			SchemaVersion: 2, Tier: PrivateSamplingTierRegression, Primary: primary,
			Holdout: []PrivateSyntheticSamplingRootRef{holdout},
		})
		fixture.previewMustReject(t)
	})
	t.Run("reused task contract", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addSyntheticRoot(t, "primary-synthetic-runs", "jira.synthetic-primary", 3, true,
			strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64))
		holdout := fixture.addSyntheticRoot(t, "holdout-synthetic-runs", "jira.synthetic-holdout", 1, true,
			strings.Repeat("1", 64), strings.Repeat("5", 64), strings.Repeat("6", 64))
		fixture.writeSyntheticSpec(t, PrivateSyntheticSamplingSpec{
			SchemaVersion: 2, Tier: PrivateSamplingTierRegression, Primary: primary,
			Holdout: []PrivateSyntheticSamplingRootRef{holdout},
		})
		fixture.previewMustReject(t)
	})
	t.Run("ambiguous spec versions", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addSyntheticRoot(t, "primary-synthetic-runs", "jira.synthetic-primary", 1, true,
			strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64))
		fixture.writeSyntheticSpec(t, PrivateSyntheticSamplingSpec{
			SchemaVersion: 2, Tier: PrivateSamplingTierCalibration, Primary: primary,
		})
		fixture.writeSpec(t, PrivateSamplingSpec{
			SchemaVersion: 1, Tier: PrivateSamplingTierCalibration,
			Primary: []PrivateFindingRunRef{fixture.addResult(t, 1, "primary-01",
				privateSamplingResult(t, "jira.primary-evidence", true), strings.Repeat("1", 64))},
		})
		fixture.previewMustReject(t)
	})
	t.Run("root symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("owner-only synthetic roots fail closed on Windows")
		}
		fixture := newPrivateSamplingFixture(t)
		outside := newSyntheticOutputRoot(t)
		result := privateSyntheticSamplingResult(t, "jira.synthetic-primary", strings.Repeat("3", 64), true)
		writeSyntheticRootResultForCohort(t, outside, 1, 1, result)
		aggregate, err := AggregateSyntheticOutputRoot(outside)
		if err != nil {
			t.Fatal(err)
		}
		parent := filepath.Join(fixture.root, "reports", privateSyntheticRootDirectory)
		if err := os.MkdirAll(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(parent, "linked-root")); err != nil {
			t.Fatal(err)
		}
		fixture.writeSyntheticSpec(t, PrivateSyntheticSamplingSpec{
			SchemaVersion: 2, Tier: PrivateSamplingTierCalibration,
			Primary: PrivateSyntheticSamplingRootRef{Root: "linked-root", SourceSHA256: aggregate.SourceSHA256},
		})
		fixture.previewMustReject(t)
	})
}

func TestPrivateSyntheticSamplingRevalidatesTheCompleteRootSet(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	primary := fixture.addSyntheticRoot(t, "primary-synthetic-runs", "jira.synthetic-primary", 3, true,
		strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64))
	holdout := fixture.addSyntheticRoot(t, "holdout-synthetic-runs", "jira.synthetic-holdout", 1, true,
		strings.Repeat("4", 64), strings.Repeat("5", 64), strings.Repeat("6", 64))
	spec := PrivateSyntheticSamplingSpec{
		SchemaVersion: 2, Tier: PrivateSamplingTierRegression, Primary: primary,
		Holdout: []PrivateSyntheticSamplingRootRef{holdout},
	}
	specData, err := encodePrivateSyntheticSamplingSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	primaryMarker := filepath.Join(fixture.root, "reports", privateSyntheticRootDirectory, primary.Root, privateOutputRootMarker)
	_, _, _, err = buildPrivateSyntheticSamplingAssessmentWithHook(
		fixture.root, spec, sha256HexBytes(specData),
		func() {
			if writeErr := os.WriteFile(primaryMarker, []byte("changed\n"), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
		},
	)
	if !errors.Is(err, ErrPrivateSamplingRejected) {
		t.Fatalf("changed complete root set err=%v", err)
	}
}

func TestPrivateSamplingSpecReadRejectsConcurrentVersionCreation(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	fixture.writeSpec(t, PrivateSamplingSpec{
		SchemaVersion: 1, Tier: PrivateSamplingTierCalibration,
		Primary: []PrivateFindingRunRef{fixture.addResult(t, 1, "primary-01",
			privateSamplingResult(t, "jira.primary-evidence", true), strings.Repeat("1", 64))},
	})
	directory := filepath.Join(fixture.root, "cases", "sampling")
	_, _, err := readPrivateSamplingSpecWithHook(fixture.root, directory, "sample-set", func() {
		if writeErr := os.WriteFile(fixture.syntheticSpecPath(), []byte("{}\n"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	})
	if !errors.Is(err, ErrPrivateSamplingRejected) {
		t.Fatalf("concurrent second version err=%v", err)
	}
	// The recheck compares the re-observed entries against the ones the read
	// was bound to, so it decides without a probe failure in hand.
	assertPrivateSamplingCode(t, err, "spec_file")
	if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
		t.Fatalf("causes=%v, want an identity-only rejection", causes)
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

func TestPrivateSamplingErrorKeepsCausesInspectableAndOutOfTheMessage(t *testing.T) {
	privatePath := filepath.Join("private", "cases", "sampling", "sample-set.v1.json")
	statCause := &fs.PathError{Op: "statat", Path: privatePath, Err: fs.ErrPermission}
	closeCause := errors.New("synthetic close failure")

	err := privateSamplingError("spec_file", statCause, nil, closeCause)
	assertPrivateSamplingCode(t, err, "spec_file")
	if strings.Contains(err.Error(), privatePath) || strings.Contains(err.Error(), closeCause.Error()) {
		t.Fatalf("message leaked a cause: %q", err.Error())
	}
	if !errors.Is(err, fs.ErrPermission) || !errors.Is(err, closeCause) {
		t.Fatalf("error %v lost a cause", err)
	}
	var typed *fs.PathError
	if !errors.As(err, &typed) || typed.Path != statCause.Path {
		t.Fatalf("error %v does not expose the concrete stat failure", err)
	}
	// The spec recheck passes its final-stat and close failures in a fixed
	// order, which is the ordering pinned here: that branch cannot be driven
	// into a both-failed state without racing the reader.
	causes := privateSamplingErrorCauses(t, err)
	if len(causes) != 2 || causes[0] != error(statCause) || causes[1] != closeCause {
		t.Fatalf("causes=%v, want both non-nil causes retained in order", causes)
	}

	// A rejection with nothing in hand classifies exactly as it did before.
	assertPrivateSamplingCode(t, privateSamplingError("spec_ambiguous"), "spec_ambiguous")
	if causes := privateSamplingErrorCauses(t, privateSamplingError("confirmation", nil, nil)); len(causes) != 0 {
		t.Fatalf("causes=%v, want nil causes dropped", causes)
	}

	// A sampling classification raised deeper stays reachable below the
	// unchanged outer code.
	nested := privateSamplingError("assessment_file")
	outer := privateSamplingError("assessment_evidence", nested)
	assertPrivateSamplingCode(t, outer, "assessment_evidence")
	var classified interface{ Code() string }
	if !errors.As(outer, &classified) || classified.Code() != "assessment_evidence" {
		t.Fatalf("error %v does not report the outer sampling code", outer)
	}
	if inner := privateSamplingErrorCauses(t, outer); len(inner) != 1 || inner[0] != nested {
		t.Fatalf("causes=%v, want the nested classification retained", inner)
	}
}

func TestPrivateSamplingPreviewAttachesWorkspaceAndSpecDirectoryCauses(t *testing.T) {
	t.Run("unresolvable workspace", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		options := fixture.options()
		options.Root = filepath.Join(t.TempDir(), "absent")
		_, _, err := previewPrivateSampling(options, fixture.dependencies())
		assertPrivateSamplingCode(t, err, "workspace")
		// The workspace layer classifies the failure under its own sentinel,
		// and that classification stays reachable below the sampling code.
		if !errors.Is(err, ErrPrivateWorkspaceUnhealthy) || !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error %v does not expose the concrete resolution failure", err)
		}
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the workspace classification retained", causes)
		}
		if strings.Contains(err.Error(), options.Root) {
			t.Fatalf("message leaked the workspace root: %q", err.Error())
		}
	})

	t.Run("rejected spec alias", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		options := fixture.options()
		options.Spec = "../escape"
		_, _, err := previewPrivateSampling(options, fixture.dependencies())
		assertPrivateSamplingCode(t, err, "workspace")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want an alias-only rejection", causes)
		}
	})

	t.Run("doctor failure", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		doctorFailure := errors.New("synthetic doctor failure")
		dependencies := fixture.dependencies()
		dependencies.doctor = func(_, _ string) (PrivateWorkspaceReport, error) {
			return PrivateWorkspaceReport{}, doctorFailure
		}
		_, _, err := previewPrivateSampling(fixture.options(), dependencies)
		assertPrivateSamplingCode(t, err, "workspace_state")
		if !errors.Is(err, doctorFailure) {
			t.Fatalf("error %v does not expose the doctor failure", err)
		}
		if strings.Contains(err.Error(), doctorFailure.Error()) {
			t.Fatalf("message leaked the dependency failure: %q", err.Error())
		}
	})

	t.Run("unhealthy report", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		dependencies := fixture.dependencies()
		dependencies.doctor = func(_, _ string) (PrivateWorkspaceReport, error) {
			return PrivateWorkspaceReport{SchemaVersion: 1, State: "unhealthy"}, nil
		}
		_, _, err := previewPrivateSampling(fixture.options(), dependencies)
		assertPrivateSamplingCode(t, err, "workspace_state")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a report-only rejection", causes)
		}
	})

	t.Run("absent spec directory", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		if err := os.RemoveAll(filepath.Join(fixture.root, "cases", "sampling")); err != nil {
			t.Fatal(err)
		}
		_, _, err := previewPrivateSampling(fixture.options(), fixture.dependencies())
		assertPrivateSamplingCode(t, err, "spec_directory")
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) || !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error %v does not expose the concrete stat failure", err)
		}
	})

	t.Run("loose spec directory", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("owner-only directory modes are not observable on Windows")
		}
		fixture := newPrivateSamplingFixture(t)
		if err := os.Chmod(filepath.Join(fixture.root, "cases", "sampling"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, _, err := previewPrivateSampling(fixture.options(), fixture.dependencies())
		assertPrivateSamplingCode(t, err, "spec_directory")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a mode-only rejection", causes)
		}
	})
}

// TestPrivateSamplingSpecReadAttachesOnlyRejectingFailures covers the shared
// spec-reader frames both schema versions enter. The OpenRoot, per-file open,
// final-stat, and close failures have no deterministic seam: reaching them
// requires the directory or the file to change identity between the reader's
// own probes, which cannot be arranged from a test without racing the reader
// or adding a production hook, so they are left to the direct constructor test.
func TestPrivateSamplingSpecReadAttachesOnlyRejectingFailures(t *testing.T) {
	specDirectory := func(fixture *privateSamplingFixture) string {
		return filepath.Join(fixture.root, "cases", "sampling")
	}

	t.Run("absent directory", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		_, _, err := readPrivateSamplingSpec(fixture.root, filepath.Join(fixture.root, "cases", "absent"), "sample-set")
		assertPrivateSamplingCode(t, err, "spec_directory")
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) || !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error %v does not expose the concrete stat failure", err)
		}
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want only the failed stat", causes)
		}
	})

	t.Run("oversized spec", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		if err := os.WriteFile(fixture.specPath(), bytes.Repeat([]byte{' '}, privateSamplingMaxBytes+1), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := readPrivateSamplingSpec(fixture.root, specDirectory(fixture), "sample-set")
		assertPrivateSamplingCode(t, err, "spec_read")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the failed bounded read retained", causes)
		}
	})

	t.Run("absent spec pair", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		_, _, err := readPrivateSamplingSpec(fixture.root, specDirectory(fixture), "sample-set")
		assertPrivateSamplingCode(t, err, "spec_file")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want an absence-only rejection", causes)
		}
	})

	t.Run("ambiguous spec pair", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		for _, path := range []string{fixture.specPath(), fixture.syntheticSpecPath()} {
			if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		_, _, err := readPrivateSamplingSpec(fixture.root, specDirectory(fixture), "sample-set")
		assertPrivateSamplingCode(t, err, "spec_ambiguous")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a pair-only rejection", causes)
		}
	})

	t.Run("loose spec mode", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("owner-only file modes are not observable on Windows")
		}
		fixture := newPrivateSamplingFixture(t)
		if err := os.WriteFile(fixture.specPath(), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(fixture.specPath(), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := readPrivateSamplingSpec(fixture.root, specDirectory(fixture), "sample-set")
		assertPrivateSamplingCode(t, err, "spec_file")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a mode-only rejection", causes)
		}
	})

	t.Run("spec symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinked specs are refused differently on Windows")
		}
		fixture := newPrivateSamplingFixture(t)
		target := filepath.Join(t.TempDir(), "spec.json")
		if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, fixture.specPath()); err != nil {
			t.Fatal(err)
		}
		_, _, err := readPrivateSamplingSpec(fixture.root, specDirectory(fixture), "sample-set")
		assertPrivateSamplingCode(t, err, "spec_file")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a file-type-only rejection", causes)
		}
	})

	t.Run("directory removed during read", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierCalibration,
			Primary: []PrivateFindingRunRef{fixture.addResult(t, 1, "primary-01",
				privateSamplingResult(t, "jira.primary-evidence", true), strings.Repeat("1", 64))}})
		directory := specDirectory(fixture)
		_, _, err := readPrivateSamplingSpecWithHook(fixture.root, directory, "sample-set", func() {
			if renameErr := os.Rename(directory, filepath.Join(fixture.root, "cases", "moved")); renameErr != nil {
				t.Fatal(renameErr)
			}
		})
		assertPrivateSamplingCode(t, err, "spec_directory")
		// The held handle still stats cleanly here, so only the ambient probe
		// fails and the nil first cause is dropped rather than recorded.
		causes := privateSamplingErrorCauses(t, err)
		if len(causes) != 1 {
			t.Fatalf("causes=%v, want only the failed ambient stat", causes)
		}
		var pathErr *fs.PathError
		if !errors.As(causes[0], &pathErr) || !errors.Is(causes[0], fs.ErrNotExist) {
			t.Fatalf("cause=%v, want the concrete ambient stat failure", causes[0])
		}
	})

	t.Run("directory replaced during read", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierCalibration,
			Primary: []PrivateFindingRunRef{fixture.addResult(t, 1, "primary-01",
				privateSamplingResult(t, "jira.primary-evidence", true), strings.Repeat("1", 64))}})
		directory := specDirectory(fixture)
		_, _, err := readPrivateSamplingSpecWithHook(fixture.root, directory, "sample-set", func() {
			if renameErr := os.Rename(directory, filepath.Join(fixture.root, "cases", "moved")); renameErr != nil {
				t.Fatal(renameErr)
			}
			if mkdirErr := os.Mkdir(directory, 0o700); mkdirErr != nil {
				t.Fatal(mkdirErr)
			}
		})
		assertPrivateSamplingCode(t, err, "spec_directory")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want an identity-only rejection", causes)
		}
	})
}

func TestPrivateSamplingContractRejectionsSeparateDecodingFromCanonicalBytes(t *testing.T) {
	regressionSpec := func(fixture *privateSamplingFixture) PrivateSamplingSpec {
		return PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: fixture.addPrimary(t, 3, true), Holdout: fixture.addHoldout(t, 4, true)}
	}

	t.Run("undecodable spec", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		if err := os.WriteFile(fixture.specPath(), []byte("{\"schema_version\": nope}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := previewPrivateSampling(fixture.options(), fixture.dependencies())
		assertPrivateSamplingCode(t, err, "spec_contract")
		var syntaxErr *json.SyntaxError
		if !errors.As(err, &syntaxErr) {
			t.Fatalf("error %v does not expose the concrete decoding failure", err)
		}
	})

	t.Run("non-canonical spec", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		fixture.writeSpec(t, regressionSpec(fixture))
		data, err := os.ReadFile(fixture.specPath())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixture.specPath(), bytes.TrimSpace(data), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, previewErr := previewPrivateSampling(fixture.options(), fixture.dependencies())
		assertPrivateSamplingCode(t, previewErr, "spec_contract")
		if causes := privateSamplingErrorCauses(t, previewErr); len(causes) != 0 {
			t.Fatalf("causes=%v, want a byte-comparison-only rejection", causes)
		}
	})

	t.Run("undecodable assessment", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		digest := fixture.storeAssessment(t, regressionSpec(fixture))
		fixture.rewriteAssessment(t, digest, []byte("{\"schema_version\": nope}\n"))
		_, _, _, err := loadPrivateSamplingAssessment(fixture.root, fixture.repository, digest, fixture.dependencies().load)
		assertPrivateSamplingCode(t, err, "assessment_decode")
		var syntaxErr *json.SyntaxError
		if !errors.As(err, &syntaxErr) {
			t.Fatalf("error %v does not expose the concrete decoding failure", err)
		}
	})

	t.Run("trailing assessment value", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		digest := fixture.storeAssessment(t, regressionSpec(fixture))
		fixture.rewriteAssessment(t, digest, append(fixture.readAssessment(t, digest), []byte("{}\n")...))
		_, _, _, err := loadPrivateSamplingAssessment(fixture.root, fixture.repository, digest, fixture.dependencies().load)
		assertPrivateSamplingCode(t, err, "assessment_decode")
		// A decoded trailing value produces no error at all, and the clean
		// end-of-input signal is never attached as a cause.
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a trailing-value-only rejection", causes)
		}
	})

	t.Run("non-canonical assessment", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		digest := fixture.storeAssessment(t, regressionSpec(fixture))
		fixture.rewriteAssessment(t, digest, append(fixture.readAssessment(t, digest), '\n'))
		_, _, _, err := loadPrivateSamplingAssessment(fixture.root, fixture.repository, digest, fixture.dependencies().load)
		assertPrivateSamplingCode(t, err, "assessment_contract")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a byte-comparison-only rejection", causes)
		}
	})

	t.Run("uncanonicalizable assessment", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		digest := fixture.storeAssessment(t, regressionSpec(fixture))
		var stored privateSamplingAssessment
		if err := json.Unmarshal(fixture.readAssessment(t, digest), &stored); err != nil {
			t.Fatal(err)
		}
		stored.EvidenceReady = false
		data, err := json.MarshalIndent(stored, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		fixture.rewriteAssessment(t, digest, append(data, '\n'))
		_, _, _, loadErr := loadPrivateSamplingAssessment(fixture.root, fixture.repository, digest, fixture.dependencies().load)
		assertPrivateSamplingCode(t, loadErr, "assessment_contract")
		causes := privateSamplingErrorCauses(t, loadErr)
		if len(causes) != 1 || !errors.Is(causes[0], ErrPrivateSamplingRejected) {
			t.Fatalf("causes=%v, want the encoder classification retained", causes)
		}
	})
}

func TestPrivateSamplingApplyAttachesStoreCauses(t *testing.T) {
	regressionSpec := func(fixture *privateSamplingFixture) PrivateSamplingSpec {
		return PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: fixture.addPrimary(t, 3, true), Holdout: fixture.addHoldout(t, 4, true)}
	}
	reviewed := func(fixture *privateSamplingFixture, digest string) PrivateSamplingOptions {
		options := fixture.options()
		options.ExpectedAssessmentSHA256, options.Confirm = digest, PrivateSamplingConfirmation
		return options
	}

	t.Run("unconfirmed apply", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		_, err := applyPrivateSampling(fixture.options(), fixture.dependencies())
		assertPrivateSamplingCode(t, err, "confirmation")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want an input-only rejection", causes)
		}
	})

	t.Run("held workspace lock", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		fixture.writeSpec(t, regressionSpec(fixture))
		lock, err := acquirePrivateWorkspaceLock(fixture.root)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lock.Unlock() }()
		_, applyErr := applyPrivateSampling(reviewed(fixture, strings.Repeat("a", 64)), fixture.dependencies())
		assertPrivateSamplingCode(t, applyErr, "workspace_busy")
		if !errors.Is(applyErr, ErrPrivateBaselineRejected) {
			t.Fatalf("error %v does not expose the lock classification", applyErr)
		}
		if causes := privateSamplingErrorCauses(t, applyErr); len(causes) != 1 {
			t.Fatalf("causes=%v, want the lock classification retained", causes)
		}
	})

	t.Run("preview failure during apply", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		digest := fixture.previewDigest(t, regressionSpec(fixture))
		if err := os.Remove(fixture.specPath()); err != nil {
			t.Fatal(err)
		}
		_, err := applyPrivateSampling(reviewed(fixture, digest), fixture.dependencies())
		assertPrivateSamplingCode(t, err, "assessment_drift")
		causes := privateSamplingErrorCauses(t, err)
		var classified interface{ Code() string }
		if len(causes) != 1 || !errors.As(causes[0], &classified) || classified.Code() != "spec_file" {
			t.Fatalf("causes=%v, want the nested preview classification", causes)
		}
	})

	t.Run("digest mismatch", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		fixture.writeSpec(t, regressionSpec(fixture))
		_, err := applyPrivateSampling(reviewed(fixture, strings.Repeat("a", 64)), fixture.dependencies())
		assertPrivateSamplingCode(t, err, "assessment_drift")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a digest-comparison-only rejection", causes)
		}
	})

	t.Run("unmakeable report directory", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		digest := fixture.previewDigest(t, regressionSpec(fixture))
		if err := os.WriteFile(filepath.Join(fixture.root, "reports", "sampling"), []byte("occupied\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := applyPrivateSampling(reviewed(fixture, digest), fixture.dependencies())
		assertPrivateSamplingCode(t, err, "directory")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the failed directory creation retained", causes)
		}
	})

	t.Run("loose report directory", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("owner-only directory modes are not observable on Windows")
		}
		fixture := newPrivateSamplingFixture(t)
		digest := fixture.previewDigest(t, regressionSpec(fixture))
		directory := filepath.Join(fixture.root, "reports", "sampling")
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := applyPrivateSampling(reviewed(fixture, digest), fixture.dependencies())
		assertPrivateSamplingCode(t, err, "directory_mode")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a mode-only rejection", causes)
		}
	})

	t.Run("unreadable existing assessment", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		digest := fixture.previewDigest(t, regressionSpec(fixture))
		if err := os.MkdirAll(filepath.Join(fixture.root, "reports", "sampling", digest+".json"), 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := applyPrivateSampling(reviewed(fixture, digest), fixture.dependencies())
		assertPrivateSamplingCode(t, err, "assessment_read")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the failed read retained", causes)
		}
	})

	t.Run("conflicting existing assessment", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		digest := fixture.previewDigest(t, regressionSpec(fixture))
		directory := filepath.Join(fixture.root, "reports", "sampling")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, digest+".json"), []byte("conflict\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := applyPrivateSampling(reviewed(fixture, digest), fixture.dependencies())
		assertPrivateSamplingCode(t, err, "assessment_exists")
		// The stored bytes stat cleanly and simply differ, so the
		// never-overwrite rejection has nothing to attach.
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a byte-comparison-only rejection", causes)
		}
	})

	t.Run("synthetic preview failure during apply", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addSyntheticRoot(t, "primary-synthetic-runs", "jira.synthetic-primary", 1, true,
			strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64))
		fixture.writeSyntheticSpec(t, PrivateSyntheticSamplingSpec{
			SchemaVersion: PrivateSyntheticSamplingSchemaVersion, Tier: PrivateSamplingTierCalibration, Primary: primary,
		})
		preview, _, err := previewPrivateSampling(fixture.options(), fixture.dependencies())
		if err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(fixture.root, "reports", privateSyntheticRootDirectory, primary.Root, privateOutputRootMarker)
		if err := os.WriteFile(marker, []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, applyErr := applyPrivateSampling(reviewed(fixture, preview.AssessmentSHA256), fixture.dependencies())
		assertPrivateSamplingCode(t, applyErr, "assessment_drift")
		// The shared apply frame retains whatever the schema-v2 preview
		// returns, including causes that path attaches in a later slice.
		if causes := privateSamplingErrorCauses(t, applyErr); len(causes) != 1 ||
			!errors.Is(causes[0], ErrPrivateSamplingRejected) {
			t.Fatalf("causes=%v, want the nested synthetic classification", causes)
		}
	})
}

func TestPrivateSamplingEvidenceRejectionsRetainNestedCauses(t *testing.T) {
	t.Run("source loader failure", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: fixture.addPrimary(t, 3, true), Holdout: fixture.addHoldout(t, 4, true)})
		loadFailure := errors.New("synthetic source failure")
		dependencies := fixture.dependencies()
		dependencies.load = func(_, _, _ string) (PrivateBaselineSource, error) {
			return PrivateBaselineSource{}, loadFailure
		}
		_, _, err := previewPrivateSampling(fixture.options(), dependencies)
		assertPrivateSamplingCode(t, err, "source")
		if !errors.Is(err, loadFailure) {
			t.Fatalf("error %v does not expose the loader failure", err)
		}
		if strings.Contains(err.Error(), loadFailure.Error()) {
			t.Fatalf("message leaked the dependency failure: %q", err.Error())
		}
	})

	t.Run("unusable baseline", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addPrimary(t, 3, true)
		source := fixture.sources[primary[0].PlanID]
		source.Immutable = false
		fixture.sources[primary[0].PlanID] = source
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: fixture.addHoldout(t, 4, true)})
		_, _, err := previewPrivateSampling(fixture.options(), fixture.dependencies())
		assertPrivateSamplingCode(t, err, "baseline")
		if !errors.Is(err, ErrPrivateFindingLedgerRejected) {
			t.Fatalf("error %v does not expose the finding classification", err)
		}
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the finding classification retained", causes)
		}
	})

	t.Run("evidence rebuild failure", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		digest := fixture.storeAssessment(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: fixture.addPrimary(t, 3, true), Holdout: fixture.addHoldout(t, 4, true)})
		loadFailure := errors.New("synthetic evidence reload failure")
		_, _, _, err := loadPrivateSamplingAssessment(fixture.root, fixture.repository, digest,
			func(_, _, _ string) (PrivateBaselineSource, error) { return PrivateBaselineSource{}, loadFailure })
		assertPrivateSamplingCode(t, err, "assessment_evidence")
		causes := privateSamplingErrorCauses(t, err)
		var classified interface{ Code() string }
		if len(causes) != 1 || !errors.As(causes[0], &classified) || classified.Code() != "source" {
			t.Fatalf("causes=%v, want the nested source classification", causes)
		}
		if !errors.Is(err, loadFailure) {
			t.Fatalf("error %v does not expose the loader failure", err)
		}
	})

	t.Run("invalid requested digest", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		_, _, _, err := loadPrivateSamplingAssessment(fixture.root, fixture.repository, "not-a-digest", fixture.dependencies().load)
		assertPrivateSamplingCode(t, err, "assessment_digest")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want an input-only rejection", causes)
		}
	})
}

func TestPrivateSamplingValidationOnlyRejectionsCarryNoCause(t *testing.T) {
	t.Run("incompatible primary", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addPrimary(t, 2, true)
		changed := privateSamplingResult(t, "jira.primary-evidence", true)
		primary = append(primary, fixture.addResult(t, 3, "primary-03", changed, strings.Repeat("3", 64)))
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: fixture.addHoldout(t, 4, true)})
		_, _, err := previewPrivateSampling(fixture.options(), fixture.dependencies())
		assertPrivateSamplingCode(t, err, "primary_incompatible")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a comparison-only rejection", causes)
		}
	})

	t.Run("duplicate observation", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addPrimary(t, 3, true)
		first := fixture.sources[primary[0].PlanID]
		second := fixture.sources[primary[1].PlanID]
		second.PlanSHA256 = first.PlanSHA256
		fixture.sources[primary[1].PlanID] = second
		fixture.rewriteManifestPlanSHA(t, primary[1], second)
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: fixture.addHoldout(t, 4, true)})
		_, _, err := previewPrivateSampling(fixture.options(), fixture.dependencies())
		assertPrivateSamplingCode(t, err, "duplicate_observation")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a dedup-only rejection", causes)
		}
	})

	t.Run("incompatible holdout", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		primary := fixture.addPrimary(t, 3, true)
		holdout := fixture.addResult(t, 4, "holdout-01",
			privateSamplingResult(t, "jira.primary-evidence", true), strings.Repeat("2", 64))
		fixture.writeSpec(t, PrivateSamplingSpec{SchemaVersion: 1, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: []PrivateFindingRunRef{holdout}})
		_, _, err := previewPrivateSampling(fixture.options(), fixture.dependencies())
		assertPrivateSamplingCode(t, err, "holdout_incompatible")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a compatibility-only rejection", causes)
		}
	})

	t.Run("uncanonicalizable assessment envelope", func(t *testing.T) {
		// The encoder rejects on in-frame validation alone, so the coded
		// error it hands its callers carries nothing itself.
		_, err := encodePrivateSamplingAssessment(privateSamplingAssessment{})
		assertPrivateSamplingCode(t, err, "assessment_contract")
		if causes := privateSamplingErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want a validation-only rejection", causes)
		}
	})
}

func assertPrivateSamplingCode(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, ErrPrivateSamplingRejected) {
		t.Fatalf("err=%v, want the sampling sentinel", err)
	}
	if got, want := err.Error(), ErrPrivateSamplingRejected.Error()+": "+code; got != want {
		t.Fatalf("message=%q, want %q", got, want)
	}
}

func privateSamplingErrorCauses(t *testing.T, err error) []error {
	t.Helper()
	multi, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("%T does not unwrap to multiple errors", err)
	}
	tree := multi.Unwrap()
	if len(tree) == 0 || tree[0] != ErrPrivateSamplingRejected {
		t.Fatalf("unwrap tree=%v, want the sentinel first", tree)
	}
	return tree[1:]
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

func (fixture *privateSamplingFixture) storeAssessment(t *testing.T, spec PrivateSamplingSpec) string {
	t.Helper()
	fixture.writeSpec(t, spec)
	options := fixture.options()
	preview, _, err := previewPrivateSampling(options, fixture.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	options.ExpectedAssessmentSHA256, options.Confirm = preview.AssessmentSHA256, PrivateSamplingConfirmation
	if summary, err := applyPrivateSampling(options, fixture.dependencies()); err != nil || !summary.Stored {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	return preview.AssessmentSHA256
}

func (fixture *privateSamplingFixture) storeSyntheticAssessment(t *testing.T, spec PrivateSyntheticSamplingSpec) string {
	t.Helper()
	fixture.writeSyntheticSpec(t, spec)
	options := fixture.options()
	preview, _, err := previewPrivateSampling(options, fixture.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	options.ExpectedAssessmentSHA256, options.Confirm = preview.AssessmentSHA256, PrivateSamplingConfirmation
	if summary, err := applyPrivateSampling(options, fixture.dependencies()); err != nil || !summary.Stored {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	return preview.AssessmentSHA256
}

func (fixture *privateSamplingFixture) previewDigest(t *testing.T, spec PrivateSamplingSpec) string {
	t.Helper()
	fixture.writeSpec(t, spec)
	preview, _, err := previewPrivateSampling(fixture.options(), fixture.dependencies())
	if err != nil {
		t.Fatal(err)
	}
	return preview.AssessmentSHA256
}

func (fixture *privateSamplingFixture) assessmentPath(digest string) string {
	return filepath.Join(fixture.root, "reports", "sampling", digest+".json")
}

func (fixture *privateSamplingFixture) readAssessment(t *testing.T, digest string) []byte {
	t.Helper()
	data, err := os.ReadFile(fixture.assessmentPath(digest))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (fixture *privateSamplingFixture) rewriteAssessment(t *testing.T, digest string, data []byte) {
	t.Helper()
	if err := os.WriteFile(fixture.assessmentPath(digest), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fixture.assessmentPath(digest), 0o600); err != nil {
		t.Fatal(err)
	}
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

func (fixture *privateSamplingFixture) syntheticSpecPath() string {
	return filepath.Join(fixture.root, "cases", "sampling", "sample-set.v2.json")
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

func (fixture *privateSamplingFixture) writeSyntheticSpec(t *testing.T, spec PrivateSyntheticSamplingSpec) {
	t.Helper()
	data, err := encodePrivateSyntheticSamplingSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.syntheticSpecPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fixture.syntheticSpecPath(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (fixture *privateSamplingFixture) addSyntheticRoot(t *testing.T, alias, scenarioID string, repetitions int, pass bool,
	taskSHA256, executionSHA256, promptSHA256 string) PrivateSyntheticSamplingRootRef {
	t.Helper()
	parent := filepath.Join(fixture.root, "reports", privateSyntheticRootDirectory)
	root := filepath.Join(parent, alias)
	for _, directory := range []string{parent, root, filepath.Join(root, ".ephemeral")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, privateOutputRootMarker), []byte(privateOutputRootMarkerContents), 0o600); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= repetitions; run++ {
		result := privateSyntheticSamplingResult(t, scenarioID, promptSHA256, pass)
		writeSyntheticRootResultForCohort(t, root, run, repetitions, result)
		receipt := readSyntheticRootReceipt(t, root, result, run)
		receipt.TaskContractSHA256 = taskSHA256
		receipt.ExecutionContractSHA256 = executionSHA256
		writeSyntheticRootReceiptTest(t, root, receipt)
	}
	aggregate, err := AggregateSyntheticOutputRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	return PrivateSyntheticSamplingRootRef{Root: alias, SourceSHA256: aggregate.SourceSHA256}
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

func privateSyntheticSamplingResult(t *testing.T, scenarioID, promptSHA256 string, pass bool) Result {
	t.Helper()
	scenario := validScenario()
	scenario.ID = scenarioID
	observation := validObservation()
	observation.ScenarioID = scenarioID
	observation.Runtime = Runtime{
		Provider: "codex", AgentVersion: "test-agent", Model: "gpt-test", Reasoning: "high",
		ATLVersion: "test-atl", PluginVersion: "test-plugin", SkillDigest: strings.Repeat("7", 64),
		PromptContractSHA256: promptSHA256,
	}
	if !pass {
		observation.Checks["answer_correct"] = false
	}
	result, err := Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
