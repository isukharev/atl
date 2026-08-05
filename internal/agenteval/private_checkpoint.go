package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	PrivateCheckpointSchemaVersion = 2
	PrivateCheckpointConfirmation  = "CHECKPOINT"
)

var ErrPrivateCheckpointRejected = errors.New("private checkpoint rejected")

type PrivateCheckpointOptions struct {
	Root                     string
	RepositoryRoot           string
	ExpectedCheckpointSHA256 string
	Confirm                  string
	Now                      time.Time
}

type PrivateDailyCheckpoint struct {
	SchemaVersion int                         `json:"schema_version"`
	UTCDate       string                      `json:"utc_date"`
	Repository    PrivateCheckpointRepository `json:"repository"`
	Workspace     PrivateCheckpointWorkspace  `json:"workspace"`
	Scorecard     PrivateCheckpointScorecard  `json:"scorecard"`
	Coverage      PrivateCheckpointCoverage   `json:"coverage"`
	Contracts     PrivateCheckpointContracts  `json:"contracts"`
}

type PrivateCheckpointRepository struct {
	Commit string `json:"commit"`
	Dirty  bool   `json:"dirty"`
}

type PrivateCheckpointWorkspace struct {
	State  string                           `json:"state"`
	Counts PrivateCheckpointWorkspaceCounts `json:"counts"`
}

// PrivateCheckpointWorkspaceCounts intentionally retains the schema-v2 field
// set and order. Workspace status may add content-free operational counters,
// but those additions must not silently rewrite canonical historical
// checkpoint bytes or domain-separated digests.
type PrivateCheckpointWorkspaceCounts struct {
	FixedDirectories  int `json:"fixed_directories"`
	RunSets           int `json:"run_sets"`
	ActivationStudies int `json:"activation_studies"`
	SpecReferences    int `json:"spec_references"`
	ValidSpecs        int `json:"valid_specs"`
	PendingPlans      int `json:"pending_plans"`
	ActiveRuns        int `json:"active_runs"`
	IncompleteRuns    int `json:"incomplete_runs"`
	CompletedRuns     int `json:"completed_runs"`
	PrunedRuns        int `json:"pruned_runs"`
}

type PrivateCheckpointScorecard struct {
	SourceSHA256       string                       `json:"source_sha256"`
	Findings           int                          `json:"findings"`
	LinkedIssues       int                          `json:"linked_issues"`
	LinkedPullRequests int                          `json:"linked_pull_requests"`
	Regressions        int                          `json:"regressions"`
	Decisions          PrivateFindingDecisionCounts `json:"decisions"`
}

type PrivateCheckpointCoverage struct {
	SourceSHA256        string `json:"source_sha256"`
	Assessments         int    `json:"assessments"`
	Groups              int    `json:"groups"`
	PrimaryObservations int    `json:"primary_observations"`
	HoldoutObservations int    `json:"holdout_observations"`
}

type PrivateCheckpointContracts struct {
	Workspace         int `json:"workspace"`
	RunSpec           int `json:"run_spec"`
	Result            int `json:"result"`
	Aggregate         int `json:"aggregate"`
	Ledger            int `json:"finding_ledger"`
	Scorecard         int `json:"finding_scorecard"`
	CoverageIndex     int `json:"coverage_index"`
	CoverageScorecard int `json:"coverage_scorecard"`
}

type PrivateCheckpointPreview struct {
	SchemaVersion    int                    `json:"schema_version"`
	CheckpointSHA256 string                 `json:"checkpoint_sha256"`
	Checkpoint       PrivateDailyCheckpoint `json:"checkpoint"`
}

type PrivateCheckpointSummary struct {
	SchemaVersion    int    `json:"schema_version"`
	UTCDate          string `json:"utc_date"`
	CheckpointSHA256 string `json:"checkpoint_sha256"`
	Stored           bool   `json:"stored"`
}

type privateCheckpointDependencies struct {
	doctor     func(root, repository string) (PrivateWorkspaceReport, error)
	scorecard  func(PrivateFindingScorecardOptions) (PrivateFindingScorecard, error)
	coverage   func(PrivateCoverageScorecardOptions) (PrivateCoverageScorecard, error)
	repository func(root string) (string, bool, error)
}

func defaultPrivateCheckpointDependencies() privateCheckpointDependencies {
	return privateCheckpointDependencies{
		doctor: DoctorPrivateWorkspace, scorecard: BuildPrivateFindingScorecard,
		coverage: BuildPrivateCoverageScorecard, repository: privateRepositoryIdentity,
	}
}

func PreviewPrivateCheckpoint(options PrivateCheckpointOptions) (PrivateCheckpointPreview, error) {
	return previewPrivateCheckpoint(options, defaultPrivateCheckpointDependencies())
}

func previewPrivateCheckpoint(options PrivateCheckpointOptions, dependencies privateCheckpointDependencies) (PrivateCheckpointPreview, error) {
	root, repository, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivateCheckpointPreview{}, privateCheckpointError("workspace", err)
	}
	report, err := dependencies.doctor(root, repository)
	if err != nil || !report.Healthy || report.SchemaVersion != 1 || report.Counts.ActiveRuns != 0 {
		return PrivateCheckpointPreview{}, privateCheckpointError("workspace_state", err)
	}
	scorecard, err := dependencies.scorecard(PrivateFindingScorecardOptions{Root: root, RepositoryRoot: repository})
	if err != nil || !scorecard.Reconciled || scorecard.SchemaVersion != PrivateFindingScorecardSchemaVersion || !validSHA256(scorecard.SourceSHA256) {
		return PrivateCheckpointPreview{}, privateCheckpointError("scorecard", err)
	}
	coverage, err := dependencies.coverage(PrivateCoverageScorecardOptions{Root: root, RepositoryRoot: repository})
	if err != nil || !coverage.Reconciled || coverage.SchemaVersion != PrivateCoverageScorecardSchemaVersion ||
		(coverage.IndexSchemaVersion != PrivateCoverageIndexSchemaVersion &&
			coverage.IndexSchemaVersion != PrivateCoverageIndexV2SchemaVersion) ||
		!validSHA256(coverage.SourceSHA256) {
		return PrivateCheckpointPreview{}, privateCheckpointError("coverage", err)
	}
	commit, dirty, err := dependencies.repository(repository)
	if err != nil || !privateGitCommitRE.MatchString(commit) {
		return PrivateCheckpointPreview{}, privateCheckpointError("repository", err)
	}
	now := options.Now.UTC()
	if options.Now.IsZero() {
		now = time.Now().UTC()
	}
	checkpoint := PrivateDailyCheckpoint{
		SchemaVersion: PrivateCheckpointSchemaVersion,
		UTCDate:       now.Format(time.DateOnly),
		Repository:    PrivateCheckpointRepository{Commit: commit, Dirty: dirty},
		Workspace: PrivateCheckpointWorkspace{
			State: report.State, Counts: privateCheckpointWorkspaceCounts(report.Counts),
		},
		Scorecard: PrivateCheckpointScorecard{SourceSHA256: scorecard.SourceSHA256, Findings: scorecard.Findings,
			LinkedIssues: scorecard.LinkedIssues, LinkedPullRequests: scorecard.LinkedPullRequests,
			Regressions: scorecard.Regressions, Decisions: scorecard.Decisions},
		Coverage: PrivateCheckpointCoverage{
			SourceSHA256: coverage.SourceSHA256, Assessments: coverage.Assessments, Groups: len(coverage.Groups),
			PrimaryObservations: coverage.PrimaryObservations, HoldoutObservations: coverage.HoldoutObservations,
		},
		Contracts: PrivateCheckpointContracts{Workspace: PrivateWorkspaceSchemaVersion, RunSpec: RunSpecSchemaVersion,
			Result: ResultSchemaVersion, Aggregate: AggregateSchemaVersion, Ledger: scorecard.LedgerSchemaVersion,
			Scorecard: PrivateFindingScorecardSchemaVersion, CoverageIndex: coverage.IndexSchemaVersion,
			CoverageScorecard: PrivateCoverageScorecardSchemaVersion},
	}
	data, err := encodePrivateCheckpoint(checkpoint)
	if err != nil {
		return PrivateCheckpointPreview{}, privateCheckpointError("contract", err)
	}
	digest := sha256HexBytes(append([]byte("atl-private-daily-checkpoint-v2\x00"), data...))
	return PrivateCheckpointPreview{SchemaVersion: PrivateCheckpointSchemaVersion, CheckpointSHA256: digest, Checkpoint: checkpoint}, nil
}

func privateCheckpointWorkspaceCounts(counts PrivateWorkspaceCounts) PrivateCheckpointWorkspaceCounts {
	return PrivateCheckpointWorkspaceCounts{
		FixedDirectories: counts.FixedDirectories, RunSets: counts.RunSets,
		ActivationStudies: counts.ActivationStudies, SpecReferences: counts.SpecReferences,
		ValidSpecs: counts.ValidSpecs, PendingPlans: counts.PendingPlans,
		ActiveRuns: counts.ActiveRuns, IncompleteRuns: counts.IncompleteRuns,
		CompletedRuns: counts.CompletedRuns, PrunedRuns: counts.PrunedRuns,
	}
}

func ApplyPrivateCheckpoint(options PrivateCheckpointOptions) (PrivateCheckpointSummary, error) {
	return applyPrivateCheckpoint(options, defaultPrivateCheckpointDependencies())
}

func applyPrivateCheckpoint(options PrivateCheckpointOptions, dependencies privateCheckpointDependencies) (PrivateCheckpointSummary, error) {
	if options.Confirm != PrivateCheckpointConfirmation || !validSHA256(options.ExpectedCheckpointSHA256) {
		return PrivateCheckpointSummary{}, privateCheckpointError("confirmation")
	}
	root, _, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivateCheckpointSummary{}, privateCheckpointError("workspace", err)
	}
	lock, err := acquirePrivateWorkspaceLock(root)
	if err != nil {
		return PrivateCheckpointSummary{}, privateCheckpointError("workspace_busy", err)
	}
	defer func() { _ = lock.Unlock() }()
	preview, err := previewPrivateCheckpoint(options, dependencies)
	if err != nil || preview.CheckpointSHA256 != options.ExpectedCheckpointSHA256 {
		return PrivateCheckpointSummary{}, privateCheckpointError("checkpoint_drift", err)
	}
	data, err := encodePrivateCheckpoint(preview.Checkpoint)
	if err != nil {
		return PrivateCheckpointSummary{}, privateCheckpointError("contract", err)
	}
	directory := filepath.Join(root, "reports", "checkpoints")
	if err := hardenedMkdirAllWithin(root, directory, 0o700); err != nil {
		return PrivateCheckpointSummary{}, privateCheckpointError("directory", err)
	}
	if info, statErr := hardenedStatWithin(root, directory); statErr != nil || !info.IsDir() ||
		(runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		return PrivateCheckpointSummary{}, privateCheckpointError("directory_mode", statErr)
	}
	path := filepath.Join(directory, preview.Checkpoint.UTCDate+".json")
	if existing, readErr := hardenedReadFileWithinLimit(root, path, privateFindingLedgerMaxBytes); readErr == nil {
		info, statErr := hardenedStatWithin(root, path)
		if statErr != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) || !bytes.Equal(existing, data) {
			return PrivateCheckpointSummary{}, privateCheckpointError("checkpoint_exists", statErr)
		}
		return PrivateCheckpointSummary{SchemaVersion: PrivateCheckpointSchemaVersion, UTCDate: preview.Checkpoint.UTCDate,
			CheckpointSHA256: preview.CheckpointSHA256, Stored: false}, nil
	} else if !os.IsNotExist(readErr) {
		return PrivateCheckpointSummary{}, privateCheckpointError("checkpoint_read", readErr)
	}
	if err := hardenedWriteFileExclusiveWithin(root, path, data, 0o600); err != nil {
		return PrivateCheckpointSummary{}, privateCheckpointError("checkpoint_write", err)
	}
	return PrivateCheckpointSummary{SchemaVersion: PrivateCheckpointSchemaVersion, UTCDate: preview.Checkpoint.UTCDate,
		CheckpointSHA256: preview.CheckpointSHA256, Stored: true}, nil
}

func encodePrivateCheckpoint(checkpoint PrivateDailyCheckpoint) ([]byte, error) {
	if checkpoint.SchemaVersion != PrivateCheckpointSchemaVersion || !privateGitCommitRE.MatchString(checkpoint.Repository.Commit) ||
		checkpoint.UTCDate == "" || checkpoint.Scorecard.Findings < 0 || checkpoint.Scorecard.LinkedIssues < 0 ||
		checkpoint.Scorecard.LinkedPullRequests < 0 || checkpoint.Scorecard.Regressions < 0 ||
		checkpoint.Scorecard.Regressions > checkpoint.Scorecard.Findings || !validPrivateCheckpointDecisions(checkpoint.Scorecard) ||
		!validPrivateCheckpointCoverage(checkpoint.Coverage) ||
		!validPrivateCheckpointWorkspace(checkpoint.Workspace) ||
		!validSHA256(checkpoint.Scorecard.SourceSHA256) ||
		(checkpoint.Contracts.Ledger != PrivateFindingLedgerSchemaVersion &&
			checkpoint.Contracts.Ledger != PrivateFindingLedgerV2SchemaVersion) ||
		checkpoint.Contracts.Workspace != PrivateWorkspaceSchemaVersion ||
		checkpoint.Contracts.RunSpec != RunSpecSchemaVersion ||
		checkpoint.Contracts.Result != ResultSchemaVersion ||
		checkpoint.Contracts.Aggregate != AggregateSchemaVersion ||
		checkpoint.Contracts.Scorecard != PrivateFindingScorecardSchemaVersion ||
		(checkpoint.Contracts.CoverageIndex != PrivateCoverageIndexSchemaVersion &&
			checkpoint.Contracts.CoverageIndex != PrivateCoverageIndexV2SchemaVersion) ||
		checkpoint.Contracts.CoverageScorecard != PrivateCoverageScorecardSchemaVersion {
		return nil, privateCheckpointError("contract")
	}
	parsed, err := time.Parse(time.DateOnly, checkpoint.UTCDate)
	if err != nil || parsed.Format(time.DateOnly) != checkpoint.UTCDate {
		return nil, privateCheckpointError("date", err)
	}
	data, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return nil, privateCheckpointError("contract", err)
	}
	return append(data, '\n'), nil
}

func validPrivateCheckpointCoverage(coverage PrivateCheckpointCoverage) bool {
	return validSHA256(coverage.SourceSHA256) &&
		coverage.Assessments > 0 && coverage.Groups == coverage.Assessments &&
		coverage.PrimaryObservations == coverage.Assessments*3 &&
		coverage.HoldoutObservations >= coverage.Assessments
}

func validPrivateCheckpointDecisions(scorecard PrivateCheckpointScorecard) bool {
	counts := scorecard.Decisions
	if counts.Fixed < 0 || counts.Accepted < 0 || counts.Unsupported < 0 || counts.Deferred < 0 || counts.Investigate < 0 {
		return false
	}
	return counts.Fixed+counts.Accepted+counts.Unsupported+counts.Deferred+counts.Investigate == scorecard.Findings
}

func validPrivateCheckpointWorkspace(workspace PrivateCheckpointWorkspace) bool {
	switch workspace.State {
	case "needs_configuration", "needs_review", "ready", "plan_pending":
	default:
		return false
	}
	counts := workspace.Counts
	return counts.FixedDirectories >= 0 && counts.RunSets >= 0 && counts.ActivationStudies >= 0 && counts.SpecReferences >= 0 &&
		counts.ValidSpecs >= 0 && counts.PendingPlans >= 0 && counts.ActiveRuns == 0 && counts.IncompleteRuns >= 0 &&
		counts.CompletedRuns >= 0 && counts.PrunedRuns >= 0 && counts.ActivationStudies <= counts.RunSets &&
		counts.ValidSpecs <= counts.SpecReferences
}

// privateCheckpointError classifies a checkpoint rejection under the stable
// sentinel and code while retaining the concrete causes already in hand. The
// rendered message stays exactly the sentinel plus the code, so a configured
// workspace path never reaches a log line; errors.Is and errors.As still reach
// every attached cause. Nil causes are dropped, so a branch that rejects on
// either a failure or a validation result can pass its error unguarded.
func privateCheckpointError(code string, causes ...error) error {
	return codedError(ErrPrivateCheckpointRejected, code, causes...)
}
