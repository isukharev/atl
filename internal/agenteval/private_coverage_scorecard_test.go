package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestPrivateCoverageIndexPublicExampleMatchesGoContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-coverage-index.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	index, canonical, err := decodePrivateCoverageIndex(data)
	if err != nil || index.SchemaVersion != PrivateCoverageIndexSchemaVersion ||
		len(index.Entries) != 2 || !bytes.Equal(data, canonical) {
		t.Fatalf("index=%+v canonical=%t err=%v", index, bytes.Equal(data, canonical), err)
	}
	var schema any
	schemaData, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-coverage-index.schema.json"))
	if err != nil || json.Unmarshal(schemaData, &schema) != nil {
		t.Fatalf("public schema is invalid JSON: %v", err)
	}
}

func TestPrivateCoverageIndexV2PublicExampleMatchesGoContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-coverage-index-v2.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	index, canonical, err := decodePrivateCoverageIndexV2(data)
	if err != nil || index.SchemaVersion != PrivateCoverageIndexV2SchemaVersion ||
		len(index.Entries) != 2 ||
		index.Entries[0].AssessmentSource != PrivateFindingAcceptanceSourcePrivateLive ||
		index.Entries[1].AssessmentSource != PrivateFindingAcceptanceSourceSyntheticRoot ||
		!bytes.Equal(data, canonical) {
		t.Fatalf("index=%+v canonical=%t err=%v", index, bytes.Equal(data, canonical), err)
	}
	var schema any
	schemaData, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-coverage-index-v2.schema.json"))
	if err != nil || json.Unmarshal(schemaData, &schema) != nil {
		t.Fatalf("public schema is invalid JSON: %v", err)
	}
}

func TestPrivateCoverageIndexRejectsAmbiguousOrLooseContracts(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("{\"schema_version\":1,\"entries\":[]}\n"),
		[]byte("{\"schema_version\":1,\"entries\":[{\"assessment_sha256\":\"" +
			strings.Repeat("2", 64) + "\"},{\"assessment_sha256\":\"" + strings.Repeat("1", 64) + "\"}]}\n"),
		[]byte("{\"schema_version\":1,\"entries\":[{\"assessment_sha256\":\"" +
			strings.Repeat("1", 64) + "\"},{\"assessment_sha256\":\"" + strings.Repeat("1", 64) + "\"}]}\n"),
		[]byte("{\"schema_version\":1,\"entries\":[{\"assessment_sha256\":\"" +
			strings.Repeat("1", 64) + "\",\"label\":\"private\"}]}\n"),
	} {
		if _, _, err := decodePrivateCoverageIndex(data); err == nil {
			t.Fatalf("invalid index accepted: %s", data)
		}
	}

	if runtime.GOOS == "windows" {
		return
	}
	fixture := newPrivateSamplingFixture(t)
	digest := addPrivateCoverageAssessment(t, fixture, "selected", "jira.primary",
		"jira.holdout", "jira.issue.refs", "1")
	writePrivateCoverageIndex(t, fixture.root, []string{digest})
	path := filepath.Join(fixture.root, filepath.FromSlash(PrivateCoverageIndexRelativePath))
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository,
	}); !errors.Is(err, ErrPrivateCoverageIndexRejected) {
		t.Fatalf("loose index err=%v", err)
	}
}

func TestPrivateCoverageIndexV2RejectsUnsortedOrUntypedEntries(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("{\"schema_version\":2,\"entries\":[]}\n"),
		[]byte("{\"schema_version\":2,\"entries\":[{\"assessment_source\":\"future\",\"assessment_sha256\":\"" +
			strings.Repeat("1", 64) + "\"}]}\n"),
		[]byte("{\"schema_version\":2,\"entries\":[{\"assessment_source\":\"synthetic-root\",\"assessment_sha256\":\"" +
			strings.Repeat("1", 64) + "\"},{\"assessment_source\":\"private-live\",\"assessment_sha256\":\"" +
			strings.Repeat("2", 64) + "\"}]}\n"),
		[]byte("{\"schema_version\":2,\"entries\":[{\"assessment_source\":\"private-live\",\"assessment_sha256\":\"" +
			strings.Repeat("2", 64) + "\"},{\"assessment_source\":\"private-live\",\"assessment_sha256\":\"" +
			strings.Repeat("1", 64) + "\"}]}\n"),
		[]byte("{\"schema_version\":2,\"entries\":[{\"assessment_source\":\"private-live\",\"assessment_sha256\":\"" +
			strings.Repeat("1", 64) + "\",\"label\":\"private\"}]}\n"),
	} {
		if _, _, err := decodePrivateCoverageIndexV2(data); err == nil {
			t.Fatalf("invalid v2 index accepted: %s", data)
		}
	}
}

func TestPrivateCoverageScorecardSelectsAcceptedEvidenceWithoutLeakingReferences(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	selected := addPrivateCoverageAssessment(t, fixture, "selected", "jira.selected-primary",
		"jira.selected-holdout", "jira.issue.refs", "1")
	historical := addPrivateCoverageAssessment(t, fixture, "historical", "jira.historical-primary",
		"jira.historical-holdout", "jira.issue.history", "8")
	writePrivateCoverageIndex(t, fixture.root, []string{selected})

	before := privateCheckpointTree(t, fixture.root)
	report, err := BuildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository,
	})
	if err != nil {
		t.Fatal(err)
	}
	after := privateCheckpointTree(t, fixture.root)
	if !bytes.Equal(before, after) {
		t.Fatal("read-only coverage scorecard changed the workspace")
	}
	if report.SchemaVersion != PrivateCoverageScorecardSchemaVersion || !report.Reconciled ||
		report.Assessments != 1 || report.PrimaryObservations != 3 ||
		report.HoldoutObservations != 1 || len(report.Groups) != 1 {
		t.Fatalf("report=%+v", report)
	}
	group := report.Groups[0]
	if report.IndexSchemaVersion != PrivateCoverageIndexSchemaVersion ||
		group.AssessmentSource != PrivateFindingAcceptanceSourceSyntheticRoot ||
		group.TaskClass != "jira/evidence" || group.Category != BenchmarkCategoryRouteFixed ||
		group.Surface != SurfaceATLMCP || group.Provider != "codex" ||
		group.Model != "gpt-5.6-luna" || group.Reasoning != "high" ||
		!reflect.DeepEqual(group.CapabilityFamilies, []string{"jira.issue.refs"}) ||
		group.Primary.Statuses.Pass != 3 || group.Holdout.Statuses.Pass != 1 ||
		group.Primary.Eligibility.Supported != 3 || group.Holdout.Eligibility.Supported != 1 ||
		group.Primary.SafetyAssurance.ObservedHTTPPolicy != 3 ||
		group.Primary.Metrics.CapabilityInvocations.ObservedRuns != 3 ||
		group.Primary.Metrics.CapabilityInvocations.P50 != 1 ||
		group.Primary.Metrics.BackendRequests.P50 != 7 ||
		group.Primary.Metrics.RemoteWrites.P50 != 0 {
		t.Fatalf("group=%+v", group)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		selected, historical, "selected-primary", "selected-holdout",
		"historical-primary", "historical-holdout", strings.Repeat("1", 64),
	} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("private value %q leaked in %s", private, encoded)
		}
	}
	second, err := BuildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondEncoded, _ := json.Marshal(second)
	if !bytes.Equal(encoded, secondEncoded) {
		t.Fatalf("scorecard is not deterministic\n%s\n%s", encoded, secondEncoded)
	}
}

func TestPrivateCoverageScorecardSelectsTypedPrivateLiveEvidence(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	digest := addPrivateLiveCoverageAssessment(t, fixture, 10, "jira.issue.comment.list")
	writePrivateCoverageIndexV2(t, fixture.root, []PrivateCoverageIndexV2Entry{{
		AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive,
		AssessmentSHA256: digest,
	}})

	before := privateCheckpointTree(t, fixture.root)
	report, err := buildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository,
	}, fixture.dependencies().load)
	if err != nil {
		t.Fatal(err)
	}
	after := privateCheckpointTree(t, fixture.root)
	if !bytes.Equal(before, after) {
		t.Fatal("read-only coverage scorecard changed the workspace")
	}
	if report.SchemaVersion != PrivateCoverageScorecardSchemaVersion ||
		report.IndexSchemaVersion != PrivateCoverageIndexV2SchemaVersion ||
		report.Assessments != 1 || report.PrimaryObservations != 3 ||
		report.HoldoutObservations != 1 || len(report.Groups) != 1 {
		t.Fatalf("report=%+v", report)
	}
	group := report.Groups[0]
	if group.AssessmentSource != PrivateFindingAcceptanceSourcePrivateLive ||
		group.TaskClass != "jira/evidence" || group.Provider != "codex" ||
		group.Model != "gpt-5.6-luna" || group.Reasoning != "high" ||
		!reflect.DeepEqual(group.CapabilityFamilies, []string{"jira.issue.comment.list"}) ||
		group.Primary.Statuses.Pass != 3 || group.Holdout.Statuses.Pass != 1 ||
		group.Primary.BackendObservation.ObservedHTTP != 3 ||
		group.Primary.SafetyAssurance.ObservedHTTPPolicy != 3 {
		t.Fatalf("group=%+v", group)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{digest, "jira.primary-evidence", "jira.holdout-evidence", "pln-", fixture.root} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("private value %q leaked in %s", private, encoded)
		}
	}
}

func TestPrivateCoverageScorecardKeepsEvidenceSourcesDistinct(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	live := addPrivateLiveCoverageAssessment(t, fixture, 10, "jira.issue.refs")
	if err := os.Remove(fixture.specPath()); err != nil {
		t.Fatal(err)
	}
	synthetic := addPrivateCoverageAssessment(t, fixture, "selected", "jira.synthetic-primary",
		"jira.synthetic-holdout", "jira.issue.refs", "1")
	writePrivateCoverageIndexV2(t, fixture.root, []PrivateCoverageIndexV2Entry{
		{AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive, AssessmentSHA256: live},
		{AssessmentSource: PrivateFindingAcceptanceSourceSyntheticRoot, AssessmentSHA256: synthetic},
	})
	report, err := buildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository,
	}, fixture.dependencies().load)
	if err != nil {
		t.Fatal(err)
	}
	if report.Assessments != 2 || len(report.Groups) != 2 ||
		report.Groups[0].AssessmentSource != PrivateFindingAcceptanceSourcePrivateLive ||
		report.Groups[1].AssessmentSource != PrivateFindingAcceptanceSourceSyntheticRoot {
		t.Fatalf("report=%+v", report)
	}
}

func TestPrivateCoverageScorecardRejectsMislabeledOrAmbiguousIndex(t *testing.T) {
	t.Run("mislabeled assessment", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		digest := addPrivateLiveCoverageAssessment(t, fixture, 10, "jira.issue.refs")
		writePrivateCoverageIndexV2(t, fixture.root, []PrivateCoverageIndexV2Entry{{
			AssessmentSource: PrivateFindingAcceptanceSourceSyntheticRoot,
			AssessmentSHA256: digest,
		}})
		if _, err := buildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
			Root: fixture.root, RepositoryRoot: fixture.repository,
		}, fixture.dependencies().load); !errors.Is(err, ErrPrivateCoverageIndexRejected) ||
			!strings.Contains(err.Error(), "assessment") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("both index versions", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		synthetic := addPrivateCoverageAssessment(t, fixture, "selected", "jira.primary",
			"jira.holdout", "jira.issue.refs", "1")
		writePrivateCoverageIndex(t, fixture.root, []string{synthetic})
		writePrivateCoverageIndexV2(t, fixture.root, []PrivateCoverageIndexV2Entry{{
			AssessmentSource: PrivateFindingAcceptanceSourceSyntheticRoot,
			AssessmentSHA256: synthetic,
		}})
		if _, err := BuildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
			Root: fixture.root, RepositoryRoot: fixture.repository,
		}); !errors.Is(err, ErrPrivateCoverageIndexRejected) ||
			!strings.Contains(err.Error(), "index_ambiguous") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestPrivateCoverageScorecardSortsDistinctGenericGroups(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	refs := addPrivateCoverageAssessment(t, fixture, "refs", "jira.refs-primary",
		"jira.refs-holdout", "jira.issue.refs", "1")
	history := addPrivateCoverageAssessment(t, fixture, "history", "jira.history-primary",
		"jira.history-holdout", "jira.issue.history", "8")
	digests := []string{refs, history}
	if digests[0] > digests[1] {
		digests[0], digests[1] = digests[1], digests[0]
	}
	writePrivateCoverageIndex(t, fixture.root, digests)
	report, err := BuildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Assessments != 2 || len(report.Groups) != 2 ||
		report.Groups[0].CapabilityFamilies[0] != "jira.issue.history" ||
		report.Groups[1].CapabilityFamilies[0] != "jira.issue.refs" {
		t.Fatalf("report=%+v", report)
	}
}

func TestPrivateCoverageScorecardRejectsDuplicateActiveCohort(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	first := addPrivateCoverageAssessment(t, fixture, "first", "jira.first-primary",
		"jira.first-holdout", "jira.issue.refs", "1")
	second := addPrivateCoverageAssessment(t, fixture, "second", "jira.second-primary",
		"jira.second-holdout", "jira.issue.refs", "8")
	digests := []string{first, second}
	if digests[0] > digests[1] {
		digests[0], digests[1] = digests[1], digests[0]
	}
	writePrivateCoverageIndex(t, fixture.root, digests)
	if _, err := BuildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository,
	}); !errors.Is(err, ErrPrivateCoverageIndexRejected) ||
		!strings.Contains(err.Error(), "duplicate_cohort") {
		t.Fatalf("err=%v", err)
	}
}

func TestPrivateCoverageScorecardV2RejectsDuplicateSameSourceCohort(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	first := addPrivateCoverageAssessment(t, fixture, "first", "jira.first-primary",
		"jira.first-holdout", "jira.issue.refs", "1")
	second := addPrivateCoverageAssessment(t, fixture, "second", "jira.second-primary",
		"jira.second-holdout", "jira.issue.refs", "8")
	entries := []PrivateCoverageIndexV2Entry{
		{AssessmentSource: PrivateFindingAcceptanceSourceSyntheticRoot, AssessmentSHA256: first},
		{AssessmentSource: PrivateFindingAcceptanceSourceSyntheticRoot, AssessmentSHA256: second},
	}
	if entries[0].AssessmentSHA256 > entries[1].AssessmentSHA256 {
		entries[0], entries[1] = entries[1], entries[0]
	}
	writePrivateCoverageIndexV2(t, fixture.root, entries)
	if _, err := BuildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
		Root: fixture.root, RepositoryRoot: fixture.repository,
	}); !errors.Is(err, ErrPrivateCoverageIndexRejected) ||
		!strings.Contains(err.Error(), "duplicate_cohort") {
		t.Fatalf("err=%v", err)
	}
}

func TestPrivateCoverageScorecardRejectsDeclaredAndObservedCohortMismatch(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	digest := addPrivateCoverageAssessment(t, fixture, "selected", "jira.primary",
		"jira.holdout", "jira.issue.refs", "1")
	assessment, primary, holdout, err := loadPrivateSyntheticSamplingAssessment(fixture.root, digest)
	if err != nil {
		t.Fatal(err)
	}
	primary[0].TaskClass = "confluence/evidence"
	if _, err := validatePrivateCoverageAssessment(
		PrivateFindingAcceptanceSourceSyntheticRoot, assessment, primary, holdout,
	); !errors.Is(err, ErrPrivateCoverageIndexRejected) ||
		!strings.Contains(err.Error(), "assessment_cohort") {
		t.Fatalf("err=%v", err)
	}
}

func TestPrivateCoverageScorecardFailsClosedOnIndexAndAssessmentContracts(t *testing.T) {
	t.Run("noncanonical index", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		digest := addPrivateCoverageAssessment(t, fixture, "selected", "jira.primary",
			"jira.holdout", "jira.issue.refs", "1")
		path := filepath.Join(fixture.root, filepath.FromSlash(PrivateCoverageIndexRelativePath))
		if err := os.WriteFile(path, []byte(`{"schema_version":1,"entries":[{"assessment_sha256":"`+digest+`"}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
			Root: fixture.root, RepositoryRoot: fixture.repository,
		}); !errors.Is(err, ErrPrivateCoverageIndexRejected) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("unknown runtime class", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		result := privateCoverageTestResult(t, "jira.primary", "jira.issue.refs")
		result.Runtime.Model = "owner-private-model"
		primary := addSyntheticFindingRoot(t, fixture, "runtime-primary-synthetic-runs", result, result.ScenarioID, 3,
			strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64))
		holdout := addSyntheticFindingRoot(t, fixture, "runtime-holdout-synthetic-runs", result, "jira.holdout", 1,
			strings.Repeat("4", 64), strings.Repeat("5", 64), strings.Repeat("6", 64))
		digest := fixture.storeSyntheticAssessment(t, PrivateSyntheticSamplingSpec{
			SchemaVersion: PrivateSyntheticSamplingSchemaVersion, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: []PrivateSyntheticSamplingRootRef{holdout},
		})
		writePrivateCoverageIndex(t, fixture.root, []string{digest})
		if _, err := BuildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
			Root: fixture.root, RepositoryRoot: fixture.repository,
		}); !errors.Is(err, ErrPrivateCoverageIndexRejected) ||
			!strings.Contains(err.Error(), "runtime_class") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("unaccepted regression", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		result := privateCoverageTestResult(t, "jira.primary", "jira.issue.refs")
		primary := addSyntheticFindingRoot(t, fixture, "unaccepted-primary-synthetic-runs", result, result.ScenarioID, 3,
			strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64))
		failed := result
		failed.Status = "fail"
		failed.Checks = map[string]bool{"answer_correct": false, "sources_complete": true}
		failed.Violations = []Violation{{Code: "required_check_failed", Subject: "answer_correct", Limit: 1}}
		holdout := addSyntheticFindingRoot(t, fixture, "unaccepted-holdout-synthetic-runs", failed, "jira.holdout", 1,
			strings.Repeat("4", 64), strings.Repeat("5", 64), strings.Repeat("6", 64))
		digest := fixture.storeSyntheticAssessment(t, PrivateSyntheticSamplingSpec{
			SchemaVersion: PrivateSyntheticSamplingSchemaVersion, Tier: PrivateSamplingTierRegression,
			Primary: primary, Holdout: []PrivateSyntheticSamplingRootRef{holdout},
		})
		writePrivateCoverageIndex(t, fixture.root, []string{digest})
		if _, err := BuildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
			Root: fixture.root, RepositoryRoot: fixture.repository,
		}); !errors.Is(err, ErrPrivateCoverageIndexRejected) ||
			!strings.Contains(err.Error(), "assessment_acceptance") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("assessment drift", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		digest := addPrivateCoverageAssessment(t, fixture, "selected", "jira.primary",
			"jira.holdout", "jira.issue.refs", "1")
		writePrivateCoverageIndex(t, fixture.root, []string{digest})
		path := filepath.Join(fixture.root, "reports", "sampling", digest+".json")
		if err := os.WriteFile(path, append([]byte{}, '{', '}', '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
			Root: fixture.root, RepositoryRoot: fixture.repository,
		}); !errors.Is(err, ErrPrivateCoverageIndexRejected) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestPrivateCoverageErrorKeepsCausesInspectableAndOutOfTheMessage(t *testing.T) {
	privatePath := filepath.Join("private", "reports", "sampling-coverage.v2.json")
	statCause := &fs.PathError{Op: "statat", Path: privatePath, Err: fs.ErrPermission}
	nestedCause := privateSamplingError("assessment_file")

	err := privateCoverageError("index_file", statCause, nil, nestedCause)
	assertPrivateCoverageCode(t, err, "index_file")
	if strings.Contains(err.Error(), privatePath) || strings.Contains(err.Error(), "assessment_file") {
		t.Fatalf("message leaked a cause: %q", err.Error())
	}
	if !errors.Is(err, fs.ErrPermission) || !errors.Is(err, ErrPrivateSamplingRejected) {
		t.Fatalf("error %v lost a cause", err)
	}
	var typed *fs.PathError
	if !errors.As(err, &typed) || typed.Path != statCause.Path {
		t.Fatalf("error %v does not expose the concrete stat failure", err)
	}
	// The two-candidate recheck passes its legacy and current failures in a
	// fixed order, which is the ordering pinned here: the recheck itself cannot
	// be driven into that state without racing the reader.
	causes := privateCoverageErrorCauses(t, err)
	if len(causes) != 2 || causes[0] != error(statCause) || causes[1] != nestedCause {
		t.Fatalf("causes=%v, want both non-nil causes retained in order", causes)
	}
	var classified interface{ Code() string }
	if !errors.As(err, &classified) || classified.Code() != "index_file" {
		t.Fatalf("error %v does not expose its stable code", err)
	}

	// A rejection with nothing in hand classifies exactly as it did before.
	assertPrivateCoverageCode(t, privateCoverageError("duplicate_cohort"), "duplicate_cohort")
	if causes := privateCoverageErrorCauses(t, privateCoverageError("index_contract", nil, nil)); len(causes) != 0 {
		t.Fatalf("causes=%v, want nil causes dropped", causes)
	}
}

func TestPrivateCoverageScorecardAttachesWorkspaceAndAssessmentCauses(t *testing.T) {
	t.Run("unresolvable workspace", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "absent")
		_, err := BuildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
			Root: root, RepositoryRoot: t.TempDir(),
		})
		assertPrivateCoverageCode(t, err, "workspace")
		// The workspace layer classifies the failure under its own sentinel,
		// and that classification stays reachable below the unchanged outer
		// coverage code.
		if !errors.Is(err, ErrPrivateWorkspaceUnhealthy) || !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error %v does not expose the concrete resolution failure", err)
		}
		if causes := privateCoverageErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the workspace classification retained", causes)
		}
		var classified interface{ Code() string }
		if !errors.As(err, &classified) || classified.Code() != "workspace" {
			t.Fatalf("error %v does not report the outer coverage code", err)
		}
		if strings.Contains(err.Error(), root) {
			t.Fatalf("message leaked the workspace root: %q", err.Error())
		}
	})

	t.Run("unresolvable assessment", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		digest := strings.Repeat("1", 64)
		writePrivateCoverageIndex(t, fixture.root, []string{digest})
		_, err := BuildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
			Root: fixture.root, RepositoryRoot: fixture.repository,
		})
		assertPrivateCoverageCode(t, err, "assessment")
		if !errors.Is(err, ErrPrivateSamplingRejected) {
			t.Fatalf("error %v does not expose the evidence loader failure", err)
		}
		if causes := privateCoverageErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the loader classification retained", causes)
		}
		if strings.Contains(err.Error(), fixture.root) || strings.Contains(err.Error(), digest) {
			t.Fatalf("message leaked private evidence references: %q", err.Error())
		}
	})
}

func TestPrivateCoverageScorecardAttachesDriftCauses(t *testing.T) {
	t.Run("index reload failure", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		digest := addPrivateLiveCoverageAssessment(t, fixture, 10, "jira.issue.refs")
		writePrivateCoverageIndexV2(t, fixture.root, []PrivateCoverageIndexV2Entry{{
			AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive,
			AssessmentSHA256: digest,
		}})
		indexPath := filepath.Join(fixture.root, filepath.FromSlash(PrivateCoverageIndexV2RelativePath))
		calls := 0
		load := func(_, _, planID string) (PrivateBaselineSource, error) {
			calls++
			if calls == 4 {
				if err := os.Remove(indexPath); err != nil {
					t.Fatal(err)
				}
			}
			return fixture.sources[planID], nil
		}
		_, err := buildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
			Root: fixture.root, RepositoryRoot: fixture.repository,
		}, load)
		assertPrivateCoverageCode(t, err, "index_drift")
		causes := privateCoverageErrorCauses(t, err)
		if len(causes) != 1 {
			t.Fatalf("causes=%v, want the failed index reload retained", causes)
		}
		var classified interface{ Code() string }
		if !errors.As(causes[0], &classified) || classified.Code() != "index_file" {
			t.Fatalf("cause=%v, want the nested index-file classification", causes[0])
		}
	})

	t.Run("evidence reload failure", func(t *testing.T) {
		fixture := newPrivateSamplingFixture(t)
		digest := addPrivateLiveCoverageAssessment(t, fixture, 10, "jira.issue.refs")
		writePrivateCoverageIndexV2(t, fixture.root, []PrivateCoverageIndexV2Entry{{
			AssessmentSource: PrivateFindingAcceptanceSourcePrivateLive,
			AssessmentSHA256: digest,
		}})
		loadFailure := errors.New("synthetic evidence reload failure")
		calls := 0
		load := func(_, _, planID string) (PrivateBaselineSource, error) {
			calls++
			if calls > 4 {
				return PrivateBaselineSource{}, loadFailure
			}
			return fixture.sources[planID], nil
		}
		_, err := buildPrivateCoverageScorecard(PrivateCoverageScorecardOptions{
			Root: fixture.root, RepositoryRoot: fixture.repository,
		}, load)
		assertPrivateCoverageCode(t, err, "evidence_drift")
		if !errors.Is(err, ErrPrivateSamplingRejected) {
			t.Fatalf("error %v does not expose the sampling reload classification", err)
		}
		if causes := privateCoverageErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the failed evidence reload retained", causes)
		}
		if strings.Contains(err.Error(), loadFailure.Error()) {
			t.Fatalf("message leaked the dependency failure: %q", err.Error())
		}
	})
}

func TestPrivateCoverageIndexLoadAttachesOnlyRejectingFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("index permission and symlink rejections differ on Windows")
	}
	legacyName := filepath.Base(PrivateCoverageIndexRelativePath)

	t.Run("absent reports directory", func(t *testing.T) {
		root := newPrivateCoverageIndexRoot(t)
		if err := os.Remove(filepath.Join(root, "reports")); err != nil {
			t.Fatal(err)
		}
		_, _, err := loadPrivateCoverageIndex(root)
		assertPrivateCoverageCode(t, err, "index_directory")
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) || !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error %v does not expose the concrete stat failure", err)
		}
		if strings.Contains(err.Error(), root) {
			t.Fatalf("message leaked the workspace root: %q", err.Error())
		}
	})

	t.Run("loose reports directory", func(t *testing.T) {
		root := newPrivateCoverageIndexRoot(t)
		if err := os.Chmod(filepath.Join(root, "reports"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, _, err := loadPrivateCoverageIndex(root)
		assertPrivateCoverageCode(t, err, "index_directory")
		// The stat succeeded; only the observed permission mode rejects it.
		if causes := privateCoverageErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a mode-only rejection", causes)
		}
	})

	t.Run("unstattable index", func(t *testing.T) {
		root := newPrivateCoverageIndexRoot(t)
		if err := os.Symlink(filepath.Join(root, "reports", "elsewhere.json"),
			filepath.Join(root, "reports", legacyName)); err != nil {
			t.Fatal(err)
		}
		_, _, err := loadPrivateCoverageIndex(root)
		assertPrivateCoverageCode(t, err, "index_file")
		if causes := privateCoverageErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the refused stat retained", causes)
		}
	})

	t.Run("index is not a regular file", func(t *testing.T) {
		root := newPrivateCoverageIndexRoot(t)
		if err := os.Mkdir(filepath.Join(root, "reports", legacyName), 0o700); err != nil {
			t.Fatal(err)
		}
		_, _, err := loadPrivateCoverageIndex(root)
		assertPrivateCoverageCode(t, err, "index_file")
		// The stat succeeded; only the observed file type rejects it.
		if causes := privateCoverageErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a type-only rejection", causes)
		}
	})

	t.Run("no index present", func(t *testing.T) {
		_, _, err := loadPrivateCoverageIndex(newPrivateCoverageIndexRoot(t))
		assertPrivateCoverageCode(t, err, "index_file")
		// Both probes reported ordinary absence, which is not a failure.
		if causes := privateCoverageErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for an ordinary absence", causes)
		}
	})

	t.Run("unreadable index", func(t *testing.T) {
		root := newPrivateCoverageIndexRoot(t)
		oversized := bytes.Repeat([]byte("0"), privateFindingLedgerMaxBytes+1)
		if err := os.WriteFile(filepath.Join(root, "reports", legacyName), oversized, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := loadPrivateCoverageIndex(root)
		assertPrivateCoverageCode(t, err, "index_read")
		if causes := privateCoverageErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the read failure retained", causes)
		}
	})
}

func TestPrivateCoverageIndexContractAttachesOnlyDecodeFailures(t *testing.T) {
	digest := strings.Repeat("1", 64)
	for _, testCase := range []struct {
		name     string
		relative string
		data     []byte
		decoded  bool
	}{
		{
			name: "undecodable v1", relative: PrivateCoverageIndexRelativePath,
			data: []byte("{\"schema_version\":\"1\",\"entries\":[]}\n"),
		},
		{
			name: "undecodable v2", relative: PrivateCoverageIndexV2RelativePath,
			data: []byte("{\"schema_version\":\"2\",\"entries\":[]}\n"),
		},
		{
			name: "noncanonical v1", relative: PrivateCoverageIndexRelativePath, decoded: true,
			data: []byte("{\"schema_version\":1,\"entries\":[{\"assessment_sha256\":\"" + digest + "\"}]}"),
		},
		{
			name: "noncanonical v2", relative: PrivateCoverageIndexV2RelativePath, decoded: true,
			data: []byte("{\"schema_version\":2,\"entries\":[{\"assessment_source\":\"synthetic-root\"," +
				"\"assessment_sha256\":\"" + digest + "\"}]}"),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := newPrivateCoverageIndexRoot(t)
			path := filepath.Join(root, filepath.FromSlash(testCase.relative))
			if err := os.WriteFile(path, testCase.data, 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := loadPrivateCoverageIndex(root)
			assertPrivateCoverageCode(t, err, "index_contract")
			causes := privateCoverageErrorCauses(t, err)
			if testCase.decoded {
				// The index decodes and validates; only the byte comparison
				// against the canonical encoding rejects it.
				if len(causes) != 0 {
					t.Fatalf("causes=%v, want none for a comparison-only rejection", causes)
				}
				return
			}
			if len(causes) != 1 {
				t.Fatalf("causes=%v, want the decode failure retained", causes)
			}
			var typeErr *json.UnmarshalTypeError
			if !errors.As(err, &typeErr) {
				t.Fatalf("error %v does not expose the concrete decode failure", err)
			}
			if strings.Contains(err.Error(), digest) || strings.Contains(err.Error(), causes[0].Error()) {
				t.Fatalf("message leaked index content: %q", err.Error())
			}
		})
	}
}

func TestPrivateCoverageValidationRejectionsCarryNoCauses(t *testing.T) {
	fixture := newPrivateSamplingFixture(t)
	digest := addPrivateCoverageAssessment(t, fixture, "selected", "jira.primary",
		"jira.holdout", "jira.issue.refs", "1")
	assessment, primary, holdout, err := loadPrivateSyntheticSamplingAssessment(fixture.root, digest)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("cohort mismatch", func(t *testing.T) {
		drifted := append([]Result{}, primary...)
		drifted[0].TaskClass = "confluence/evidence"
		_, err := validatePrivateCoverageAssessment(
			PrivateFindingAcceptanceSourceSyntheticRoot, assessment, drifted, holdout)
		assertPrivateCoverageCode(t, err, "assessment_cohort")
		if causes := privateCoverageErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a comparison-only rejection", causes)
		}
	})

	t.Run("unexpected data class", func(t *testing.T) {
		drifted := append([]Result{}, primary...)
		drifted[0].DataClass = "private-local"
		_, err := validatePrivateCoverageAssessment(
			PrivateFindingAcceptanceSourceSyntheticRoot, assessment, drifted, holdout)
		assertPrivateCoverageCode(t, err, "assessment_result")
		if causes := privateCoverageErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a validation-only rejection", causes)
		}
	})

	t.Run("unknown assessment source", func(t *testing.T) {
		_, _, _, err := loadPrivateCoverageAssessment(fixture.root, fixture.repository,
			"future-source", digest, LoadCompletedPrivateRun)
		assertPrivateCoverageCode(t, err, "assessment_source")
		if causes := privateCoverageErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a label-only rejection", causes)
		}
	})
}

func newPrivateCoverageIndexRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "private")
	reports := filepath.Join(root, "reports")
	if err := os.MkdirAll(reports, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(reports, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func assertPrivateCoverageCode(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, ErrPrivateCoverageIndexRejected) {
		t.Fatalf("err=%v, want the coverage sentinel", err)
	}
	if got, want := err.Error(), ErrPrivateCoverageIndexRejected.Error()+": "+code; got != want {
		t.Fatalf("message=%q, want %q", got, want)
	}
}

func privateCoverageErrorCauses(t *testing.T, err error) []error {
	t.Helper()
	multi, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("%T does not unwrap to multiple errors", err)
	}
	tree := multi.Unwrap()
	if len(tree) == 0 || !errors.Is(tree[0], ErrPrivateCoverageIndexRejected) {
		t.Fatalf("unwrap tree=%v, want the sentinel first", tree)
	}
	return tree[1:]
}

func addPrivateCoverageAssessment(
	t *testing.T,
	fixture *privateSamplingFixture,
	prefix, primaryScenario, holdoutScenario, family, digestSeed string,
) string {
	t.Helper()
	result := privateCoverageTestResult(t, primaryScenario, family)
	primary := addSyntheticFindingRoot(t, fixture, prefix+"-primary-synthetic-runs", result, primaryScenario, 3,
		strings.Repeat(digestSeed, 64), strings.Repeat(nextHexSeed(digestSeed), 64),
		strings.Repeat(nextHexSeed(nextHexSeed(digestSeed)), 64))
	holdoutResult := result
	holdout := addSyntheticFindingRoot(t, fixture, prefix+"-holdout-synthetic-runs", holdoutResult, holdoutScenario, 1,
		strings.Repeat(nextHexSeed(nextHexSeed(nextHexSeed(digestSeed))), 64),
		strings.Repeat(nextHexSeed(nextHexSeed(nextHexSeed(nextHexSeed(digestSeed)))), 64),
		strings.Repeat(nextHexSeed(nextHexSeed(nextHexSeed(nextHexSeed(nextHexSeed(digestSeed))))), 64))
	return fixture.storeSyntheticAssessment(t, PrivateSyntheticSamplingSpec{
		SchemaVersion: PrivateSyntheticSamplingSchemaVersion, Tier: PrivateSamplingTierRegression,
		Primary: primary, Holdout: []PrivateSyntheticSamplingRootRef{holdout},
	})
}

func privateCoverageTestResult(t *testing.T, scenarioID, family string) Result {
	t.Helper()
	scenario := validScenario()
	scenario.ID = scenarioID
	scenario.Budgets.MaxAgentTurns = 2
	scenario.Budgets.MaxToolCalls = 2
	scenario.Budgets.MaxInputTokens = 1_000
	scenario.Budgets.MaxOutputTokens = 1_000
	scenario.Budgets.MaxEstimatedCostMicroUSD = 10_000
	scenario.Budgets.MaxDurationMillis = 10_000
	observation := validObservation()
	observation.ScenarioID = scenarioID
	observation.Surface = SurfaceATLMCP
	observation.BackendObservation = BackendObservationHTTP
	observation.SafetyAssurance = SafetyAssuranceObservedHTTP
	observation.Runtime = Runtime{
		Provider: "codex", AgentVersion: "test-agent", Model: "gpt-5.6-luna",
		Reasoning: "high", ATLVersion: "test-atl", PluginVersion: "test-plugin",
		SkillDigest: strings.Repeat("7", 64), PromptContractSHA256: strings.Repeat("3", 64),
	}
	observation.Coverage["agent_turns"] = true
	observation.Coverage["tool_calls"] = true
	observation.Coverage["duplicate_backend_requests"] = true
	observation.Coverage["remote_writes"] = true
	observation.Coverage["input_tokens"] = true
	observation.Coverage["output_tokens"] = true
	observation.Coverage["estimated_cost_microusd"] = true
	observation.Coverage["duration_millis"] = true
	observation.Coverage["capability_families"] = true
	observation.Metrics.AgentTurns = 1
	observation.Metrics.ToolCalls = 1
	observation.Metrics.InputTokens = 100
	observation.Metrics.OutputTokens = 10
	observation.Metrics.EstimatedCostMicroUSD = 1_000
	observation.Metrics.DurationMillis = 2_000
	observation.CapabilityFamilies = []CapabilityFamilyMetric{{
		Family: family, Invocations: 1, Successes: 1,
	}}
	result, err := Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pass" {
		t.Fatalf("result=%+v", result)
	}
	return result
}

func writePrivateCoverageIndex(t *testing.T, root string, digests []string) {
	t.Helper()
	index := PrivateCoverageIndex{SchemaVersion: PrivateCoverageIndexSchemaVersion}
	for _, digest := range digests {
		index.Entries = append(index.Entries, PrivateCoverageIndexEntry{AssessmentSHA256: digest})
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, filepath.FromSlash(PrivateCoverageIndexRelativePath))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writePrivateCoverageIndexV2(t *testing.T, root string, entries []PrivateCoverageIndexV2Entry) {
	t.Helper()
	index := PrivateCoverageIndexV2{SchemaVersion: PrivateCoverageIndexV2SchemaVersion, Entries: entries}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, filepath.FromSlash(PrivateCoverageIndexV2RelativePath))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func addPrivateLiveCoverageAssessment(t *testing.T, fixture *privateSamplingFixture, start int, family string) string {
	t.Helper()
	primaryResult := privateCoverageTestResult(t, "jira.primary-evidence", family)
	primaryResult.DataClass = "private-local"
	primaryResult.Runtime.PromptContractSHA256 = ""
	holdoutResult := primaryResult
	holdoutResult.ScenarioID = "jira.holdout-evidence"
	primary := make([]PrivateFindingRunRef, 0, 3)
	for offset := 0; offset < 3; offset++ {
		ref := fixture.addResult(t, start+offset, fmt.Sprintf("live-primary-%02d", offset+1),
			primaryResult, strings.Repeat("a", 64))
		if _, _, err := privateFindingBaselineResult(fixture.root, fixture.sources[ref.PlanID], ref); err != nil {
			t.Fatalf("primary baseline %d: %v", offset+1, err)
		}
		primary = append(primary, ref)
	}
	holdoutRef := fixture.addResult(t, start+3, "live-holdout-01", holdoutResult, strings.Repeat("b", 64))
	if _, _, err := privateFindingBaselineResult(fixture.root, fixture.sources[holdoutRef.PlanID], holdoutRef); err != nil {
		t.Fatalf("holdout baseline: %v", err)
	}
	holdout := []PrivateFindingRunRef{holdoutRef}
	return fixture.storeAssessment(t, PrivateSamplingSpec{
		SchemaVersion: PrivateSamplingSchemaVersion, Tier: PrivateSamplingTierRegression,
		Primary: primary, Holdout: holdout,
	})
}

func nextHexSeed(value string) string {
	const digits = "123456789abcdef"
	index := strings.Index(digits, value)
	if index < 0 || index+1 == len(digits) {
		return "1"
	}
	return digits[index+1 : index+2]
}
