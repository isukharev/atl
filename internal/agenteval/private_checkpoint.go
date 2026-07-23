package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	PrivateCheckpointSchemaVersion = 1
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
	Contracts     PrivateCheckpointContracts  `json:"contracts"`
}

type PrivateCheckpointRepository struct {
	Commit string `json:"commit"`
	Dirty  bool   `json:"dirty"`
}

type PrivateCheckpointWorkspace struct {
	State  string                 `json:"state"`
	Counts PrivateWorkspaceCounts `json:"counts"`
}

type PrivateCheckpointScorecard struct {
	SourceSHA256       string                       `json:"source_sha256"`
	Findings           int                          `json:"findings"`
	LinkedIssues       int                          `json:"linked_issues"`
	LinkedPullRequests int                          `json:"linked_pull_requests"`
	Regressions        int                          `json:"regressions"`
	Decisions          PrivateFindingDecisionCounts `json:"decisions"`
}

type PrivateCheckpointContracts struct {
	Workspace int `json:"workspace"`
	RunSpec   int `json:"run_spec"`
	Result    int `json:"result"`
	Aggregate int `json:"aggregate"`
	Ledger    int `json:"finding_ledger"`
	Scorecard int `json:"finding_scorecard"`
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
	repository func(root string) (string, bool, error)
}

func defaultPrivateCheckpointDependencies() privateCheckpointDependencies {
	return privateCheckpointDependencies{doctor: DoctorPrivateWorkspace, scorecard: BuildPrivateFindingScorecard, repository: privateRepositoryIdentity}
}

func PreviewPrivateCheckpoint(options PrivateCheckpointOptions) (PrivateCheckpointPreview, error) {
	return previewPrivateCheckpoint(options, defaultPrivateCheckpointDependencies())
}

func previewPrivateCheckpoint(options PrivateCheckpointOptions, dependencies privateCheckpointDependencies) (PrivateCheckpointPreview, error) {
	root, repository, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivateCheckpointPreview{}, privateCheckpointError("workspace")
	}
	report, err := dependencies.doctor(root, repository)
	if err != nil || !report.Healthy || report.SchemaVersion != 1 || report.Counts.ActiveRuns != 0 {
		return PrivateCheckpointPreview{}, privateCheckpointError("workspace_state")
	}
	scorecard, err := dependencies.scorecard(PrivateFindingScorecardOptions{Root: root, RepositoryRoot: repository})
	if err != nil || !scorecard.Reconciled || scorecard.SchemaVersion != PrivateFindingScorecardSchemaVersion || !validSHA256(scorecard.SourceSHA256) {
		return PrivateCheckpointPreview{}, privateCheckpointError("scorecard")
	}
	commit, dirty, err := dependencies.repository(repository)
	if err != nil || !privateGitCommitRE.MatchString(commit) {
		return PrivateCheckpointPreview{}, privateCheckpointError("repository")
	}
	now := options.Now.UTC()
	if options.Now.IsZero() {
		now = time.Now().UTC()
	}
	checkpoint := PrivateDailyCheckpoint{
		SchemaVersion: PrivateCheckpointSchemaVersion,
		UTCDate:       now.Format(time.DateOnly),
		Repository:    PrivateCheckpointRepository{Commit: commit, Dirty: dirty},
		Workspace:     PrivateCheckpointWorkspace{State: report.State, Counts: report.Counts},
		Scorecard: PrivateCheckpointScorecard{SourceSHA256: scorecard.SourceSHA256, Findings: scorecard.Findings,
			LinkedIssues: scorecard.LinkedIssues, LinkedPullRequests: scorecard.LinkedPullRequests,
			Regressions: scorecard.Regressions, Decisions: scorecard.Decisions},
		Contracts: PrivateCheckpointContracts{Workspace: PrivateWorkspaceSchemaVersion, RunSpec: RunSpecSchemaVersion,
			Result: ResultSchemaVersion, Aggregate: AggregateSchemaVersion, Ledger: scorecard.LedgerSchemaVersion,
			Scorecard: PrivateFindingScorecardSchemaVersion},
	}
	data, err := encodePrivateCheckpoint(checkpoint)
	if err != nil {
		return PrivateCheckpointPreview{}, privateCheckpointError("contract")
	}
	digest := sha256HexBytes(append([]byte("atl-private-daily-checkpoint-v1\x00"), data...))
	return PrivateCheckpointPreview{SchemaVersion: PrivateCheckpointSchemaVersion, CheckpointSHA256: digest, Checkpoint: checkpoint}, nil
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
		return PrivateCheckpointSummary{}, privateCheckpointError("workspace")
	}
	lock, err := acquirePrivateWorkspaceLock(root)
	if err != nil {
		return PrivateCheckpointSummary{}, privateCheckpointError("workspace_busy")
	}
	defer func() { _ = lock.Unlock() }()
	preview, err := previewPrivateCheckpoint(options, dependencies)
	if err != nil || preview.CheckpointSHA256 != options.ExpectedCheckpointSHA256 {
		return PrivateCheckpointSummary{}, privateCheckpointError("checkpoint_drift")
	}
	data, err := encodePrivateCheckpoint(preview.Checkpoint)
	if err != nil {
		return PrivateCheckpointSummary{}, privateCheckpointError("contract")
	}
	directory := filepath.Join(root, "reports", "checkpoints")
	if err := safepath.MkdirAllWithin(root, directory, 0o700); err != nil {
		return PrivateCheckpointSummary{}, privateCheckpointError("directory")
	}
	if info, statErr := safepath.StatWithin(root, directory); statErr != nil || !info.IsDir() ||
		(runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		return PrivateCheckpointSummary{}, privateCheckpointError("directory_mode")
	}
	path := filepath.Join(directory, preview.Checkpoint.UTCDate+".json")
	if existing, readErr := safepath.ReadFileWithinLimit(root, path, privateFindingLedgerMaxBytes); readErr == nil {
		info, statErr := safepath.StatWithin(root, path)
		if statErr != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) || !bytes.Equal(existing, data) {
			return PrivateCheckpointSummary{}, privateCheckpointError("checkpoint_exists")
		}
		return PrivateCheckpointSummary{SchemaVersion: PrivateCheckpointSchemaVersion, UTCDate: preview.Checkpoint.UTCDate,
			CheckpointSHA256: preview.CheckpointSHA256, Stored: false}, nil
	} else if !os.IsNotExist(readErr) {
		return PrivateCheckpointSummary{}, privateCheckpointError("checkpoint_read")
	}
	if err := safepath.WriteFileExclusiveWithin(root, path, data, 0o600); err != nil {
		return PrivateCheckpointSummary{}, privateCheckpointError("checkpoint_write")
	}
	return PrivateCheckpointSummary{SchemaVersion: PrivateCheckpointSchemaVersion, UTCDate: preview.Checkpoint.UTCDate,
		CheckpointSHA256: preview.CheckpointSHA256, Stored: true}, nil
}

func encodePrivateCheckpoint(checkpoint PrivateDailyCheckpoint) ([]byte, error) {
	if checkpoint.SchemaVersion != PrivateCheckpointSchemaVersion || !privateGitCommitRE.MatchString(checkpoint.Repository.Commit) ||
		checkpoint.UTCDate == "" || checkpoint.Scorecard.Findings < 0 || checkpoint.Scorecard.LinkedIssues < 0 ||
		checkpoint.Scorecard.LinkedPullRequests < 0 || checkpoint.Scorecard.Regressions < 0 ||
		checkpoint.Scorecard.LinkedIssues > checkpoint.Scorecard.Findings || checkpoint.Scorecard.LinkedPullRequests > checkpoint.Scorecard.Findings ||
		checkpoint.Scorecard.Regressions > checkpoint.Scorecard.Findings || !validPrivateCheckpointDecisions(checkpoint.Scorecard) ||
		!validPrivateCheckpointWorkspace(checkpoint.Workspace) ||
		!validSHA256(checkpoint.Scorecard.SourceSHA256) ||
		(checkpoint.Contracts.Ledger != PrivateFindingLedgerSchemaVersion &&
			checkpoint.Contracts.Ledger != PrivateFindingLedgerV2SchemaVersion) ||
		checkpoint.Contracts.Workspace != PrivateWorkspaceSchemaVersion ||
		checkpoint.Contracts.RunSpec != RunSpecSchemaVersion ||
		checkpoint.Contracts.Result != ResultSchemaVersion ||
		checkpoint.Contracts.Aggregate != AggregateSchemaVersion ||
		checkpoint.Contracts.Scorecard != PrivateFindingScorecardSchemaVersion {
		return nil, privateCheckpointError("contract")
	}
	parsed, err := time.Parse(time.DateOnly, checkpoint.UTCDate)
	if err != nil || parsed.Format(time.DateOnly) != checkpoint.UTCDate {
		return nil, privateCheckpointError("date")
	}
	data, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
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

func privateCheckpointError(code string) error {
	return fmt.Errorf("%w: %s", ErrPrivateCheckpointRejected, code)
}
