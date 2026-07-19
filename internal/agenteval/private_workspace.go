package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	PrivateWorkspaceSchemaVersion = 1
	PrivateWorkspaceManifestName  = "private-workspace.v1.json"

	maxPrivateWorkspaceManifestBytes = 1 << 20
	maxPrivateWorkspaceRunSets       = 64
	maxPrivateWorkspaceSpecsPerSet   = 3
	maxPrivateWorkspaceTreeEntries   = 100_000
)

var (
	ErrPrivateWorkspaceUnhealthy = errors.New("private workspace is unhealthy")
	privateWorkspaceAliasRE      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	privateWorkspaceEnvRE        = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

var privateWorkspaceFixedDirectories = []string{".ephemeral", "baselines", "cases", "plans", "reports", "runs"}

// PrivateWorkspaceManifest intentionally contains bindings and lifecycle
// policy only. Concrete credentials, backend identities, prompts, expected
// facts, and output paths belong in the owner-private files referenced by it.
type PrivateWorkspaceManifest struct {
	SchemaVersion         int                       `json:"schema_version"`
	LiveConfigEnv         string                    `json:"live_config_env"`
	ExternalMCPProfileEnv string                    `json:"external_mcp_profile_env,omitempty"`
	Execution             PrivateWorkspaceExecution `json:"execution"`
	Retention             PrivateWorkspaceRetention `json:"retention"`
	RunSets               []PrivateWorkspaceRunSet  `json:"run_sets"`
}

type PrivateWorkspaceExecution struct {
	MaxEstimatedCostMicroUSD int64 `json:"max_estimated_cost_microusd"`
}

type PrivateWorkspaceRetention struct {
	KeepCompletedRunSetsPerAlias int   `json:"keep_completed_run_sets_per_alias"`
	MaxCandidateAgeDays          int   `json:"max_candidate_age_days"`
	MaxCandidateBytes            int64 `json:"max_candidate_bytes"`
	RetainBaselineTranscripts    bool  `json:"retain_baseline_transcripts"`
}

// PrivateWorkspaceRunSet gives an operator a generic local alias for one
// comparable set. Spec paths are slash-separated and rooted below cases/.
type PrivateWorkspaceRunSet struct {
	Alias                     string                         `json:"alias"`
	SpecPaths                 []string                       `json:"spec_paths"`
	QualitativeReviewRequired bool                           `json:"qualitative_review_required"`
	QualitativeReviewPanel    *PrivateQualitativeReviewPanel `json:"qualitative_review_panel,omitempty"`
}

const PrivateQualitativeReviewPanelMethod = QualitativePanelMethod

// PrivateQualitativeReviewPanel is the review policy selected before provider
// execution. BlindAssignment is a workspace-relative owner-private input below
// cases/; its contents never appear in lifecycle summaries.
type PrivateQualitativeReviewPanel struct {
	Method               string     `json:"method"`
	Reviewers            []Reviewer `json:"reviewers"`
	MaxCriterionRangeBPS int        `json:"max_criterion_range_bps"`
	BlindAssignment      string     `json:"blind_assignment,omitempty"`
}

func (p PrivateQualitativeReviewPanel) validate() error {
	policy := QualitativePanelPolicy{SchemaVersion: QualitativePanelSchemaVersion, Method: p.Method,
		ExpectedReviewers: len(p.Reviewers), MaxCriterionRangeBPS: p.MaxCriterionRangeBPS}
	if policy.Validate() != nil {
		return privateWorkspaceContractError("qualitative_review_panel")
	}
	seen := make(map[string]struct{}, len(p.Reviewers))
	for _, reviewer := range p.Reviewers {
		if !identifierRE.MatchString(reviewer.ID) || reviewer.validate() != nil {
			return privateWorkspaceContractError("qualitative_review_panel")
		}
		if _, exists := seen[reviewer.ID]; exists {
			return privateWorkspaceContractError("qualitative_review_panel")
		}
		seen[reviewer.ID] = struct{}{}
	}
	if p.BlindAssignment != "" && !validPrivateWorkspaceCaseFilePath(p.BlindAssignment) {
		return privateWorkspaceContractError("qualitative_review_panel")
	}
	return nil
}

type PrivateWorkspaceCounts struct {
	FixedDirectories int `json:"fixed_directories"`
	RunSets          int `json:"run_sets"`
	SpecReferences   int `json:"spec_references"`
	ValidSpecs       int `json:"valid_specs"`
	PendingPlans     int `json:"pending_plans"`
	ActiveRuns       int `json:"active_runs"`
	IncompleteRuns   int `json:"incomplete_runs"`
	CompletedRuns    int `json:"completed_runs"`
	PrunedRuns       int `json:"pruned_runs"`
}

type PrivateWorkspaceCheck struct {
	Code   string `json:"code"`
	Status string `json:"status"`
}

// PrivateWorkspaceReport is safe to emit from maintainer automation. It never
// contains filesystem paths, run-set aliases, spec identities, or raw errors.
type PrivateWorkspaceReport struct {
	SchemaVersion int                     `json:"schema_version"`
	Healthy       bool                    `json:"healthy"`
	State         string                  `json:"state"`
	NextActions   []string                `json:"next_actions"`
	Counts        PrivateWorkspaceCounts  `json:"counts"`
	Checks        []PrivateWorkspaceCheck `json:"checks"`
}

const (
	PrivateWorkspaceCheckRootExists     = "root_exists"
	PrivateWorkspaceCheckRootOwnerOnly  = "root_owner_only"
	PrivateWorkspaceCheckRootMarker     = "root_marker"
	PrivateWorkspaceCheckGitBoundary    = "git_boundary"
	PrivateWorkspaceCheckManifestMode   = "manifest_owner_only"
	PrivateWorkspaceCheckManifestValid  = "manifest_valid"
	PrivateWorkspaceCheckFixedLayout    = "fixed_layout"
	PrivateWorkspaceCheckTreeOwnerOnly  = "tree_owner_only"
	PrivateWorkspaceCheckTreeNoSymlinks = "tree_no_symlinks"
	PrivateWorkspaceCheckSpecsContained = "specs_contained"
	PrivateWorkspaceCheckSpecsValid     = "specs_valid"
	PrivateWorkspaceCheckScratchClean   = "scratch_clean"
	PrivateWorkspaceCheckLifecycleValid = "lifecycle_valid"
)

func DefaultPrivateWorkspaceManifest() PrivateWorkspaceManifest {
	return PrivateWorkspaceManifest{
		SchemaVersion:         PrivateWorkspaceSchemaVersion,
		LiveConfigEnv:         "ATL_AGENT_EVAL_LIVE_CONFIG_DIR",
		ExternalMCPProfileEnv: "ATL_AGENT_EVAL_EXTERNAL_MCP_PROFILE",
		Execution: PrivateWorkspaceExecution{
			MaxEstimatedCostMicroUSD: 10_000_000,
		},
		Retention: PrivateWorkspaceRetention{
			KeepCompletedRunSetsPerAlias: 3,
			MaxCandidateAgeDays:          14,
			MaxCandidateBytes:            2 << 30,
			RetainBaselineTranscripts:    true,
		},
		RunSets: []PrivateWorkspaceRunSet{},
	}
}

func (m PrivateWorkspaceManifest) Validate() error {
	if m.SchemaVersion != PrivateWorkspaceSchemaVersion {
		return privateWorkspaceContractError("schema_version")
	}
	if !privateWorkspaceEnvRE.MatchString(m.LiveConfigEnv) {
		return privateWorkspaceContractError("live_config_env")
	}
	if m.ExternalMCPProfileEnv != "" && !privateWorkspaceEnvRE.MatchString(m.ExternalMCPProfileEnv) {
		return privateWorkspaceContractError("external_mcp_profile_env")
	}
	if m.Execution.MaxEstimatedCostMicroUSD < 1 || m.Execution.MaxEstimatedCostMicroUSD > 100_000_000 {
		return privateWorkspaceContractError("execution")
	}
	if m.Retention.KeepCompletedRunSetsPerAlias < 1 || m.Retention.KeepCompletedRunSetsPerAlias > 100 ||
		m.Retention.MaxCandidateAgeDays < 1 || m.Retention.MaxCandidateAgeDays > 365 ||
		m.Retention.MaxCandidateBytes < 1<<20 || m.Retention.MaxCandidateBytes > 1<<40 {
		return privateWorkspaceContractError("retention")
	}
	if m.RunSets == nil || len(m.RunSets) > maxPrivateWorkspaceRunSets {
		return privateWorkspaceContractError("run_sets")
	}
	aliases := make(map[string]struct{}, len(m.RunSets))
	for _, runSet := range m.RunSets {
		if !privateWorkspaceAliasRE.MatchString(runSet.Alias) {
			return privateWorkspaceContractError("run_set_alias")
		}
		if _, exists := aliases[runSet.Alias]; exists {
			return privateWorkspaceContractError("run_set_alias")
		}
		aliases[runSet.Alias] = struct{}{}
		if len(runSet.SpecPaths) < 1 || len(runSet.SpecPaths) > maxPrivateWorkspaceSpecsPerSet {
			return privateWorkspaceContractError("spec_paths")
		}
		seenSpecs := make(map[string]struct{}, len(runSet.SpecPaths))
		for _, path := range runSet.SpecPaths {
			if !validPrivateWorkspaceSpecPath(path) {
				return privateWorkspaceContractError("spec_path")
			}
			if _, exists := seenSpecs[path]; exists {
				return privateWorkspaceContractError("spec_path")
			}
			seenSpecs[path] = struct{}{}
		}
		if runSet.QualitativeReviewRequired && runSet.QualitativeReviewPanel != nil {
			return privateWorkspaceContractError("qualitative_review_policy")
		}
		if runSet.QualitativeReviewPanel != nil {
			if err := runSet.QualitativeReviewPanel.validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

func validPrivateWorkspaceSpecPath(path string) bool {
	return validPrivateWorkspaceCaseFilePath(path) && strings.HasSuffix(path, ".json")
}

func validPrivateWorkspaceCaseFilePath(path string) bool {
	if path == "" || len(path) > 512 || filepath.IsAbs(path) || strings.ContainsAny(path, "\\\r\n\x00") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	return clean == path && strings.HasPrefix(path, "cases/") && path != "cases/"
}

func DecodePrivateWorkspaceManifest(r io.Reader) (PrivateWorkspaceManifest, error) {
	limited := &io.LimitedReader{R: r, N: maxPrivateWorkspaceManifestBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return PrivateWorkspaceManifest{}, privateWorkspaceContractError("decode")
	}
	if len(data) > maxPrivateWorkspaceManifestBytes {
		return PrivateWorkspaceManifest{}, privateWorkspaceContractError("size")
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return PrivateWorkspaceManifest{}, privateWorkspaceContractError("decode")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest PrivateWorkspaceManifest
	if err := decoder.Decode(&manifest); err != nil {
		return PrivateWorkspaceManifest{}, privateWorkspaceContractError("decode")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return PrivateWorkspaceManifest{}, privateWorkspaceContractError("trailing_data")
	}
	if err := manifest.Validate(); err != nil {
		return PrivateWorkspaceManifest{}, err
	}
	return manifest, nil
}

func EncodePrivateWorkspaceManifest(manifest PrivateWorkspaceManifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, privateWorkspaceContractError("encode")
	}
	return append(data, '\n'), nil
}

// InitPrivateWorkspace creates or resumes an owner-private fixed-layout
// workspace. Existing non-empty roots without a valid marker are never adopted.
func InitPrivateWorkspace(root, repositoryRoot string, manifest PrivateWorkspaceManifest) (PrivateWorkspaceReport, error) {
	if err := manifest.Validate(); err != nil {
		return emptyPrivateWorkspaceReport(), privateWorkspaceOperationError("manifest_invalid")
	}
	absRoot, absRepository, err := privateWorkspaceLocations(root, repositoryRoot, true)
	if err != nil {
		return emptyPrivateWorkspaceReport(), err
	}
	if err := privateWorkspaceGitBoundary(absRoot, absRepository, false); err != nil {
		return emptyPrivateWorkspaceReport(), err
	}
	if err := prepareMarkedPrivateRoot(absRoot); err != nil {
		return emptyPrivateWorkspaceReport(), privateWorkspaceOperationError("root_marker")
	}

	markerPath := filepath.Join(absRoot, PrivateWorkspaceManifestName)
	markerInfo, markerErr := os.Lstat(markerPath)
	if os.IsNotExist(markerErr) {
		entries, err := os.ReadDir(absRoot)
		if err != nil {
			return emptyPrivateWorkspaceReport(), privateWorkspaceOperationError("root_read")
		}
		if len(entries) != 1 || entries[0].Name() != privateOutputRootMarker {
			return emptyPrivateWorkspaceReport(), privateWorkspaceOperationError("unmarked_nonempty_root")
		}
		data, err := EncodePrivateWorkspaceManifest(manifest)
		if err != nil {
			return emptyPrivateWorkspaceReport(), privateWorkspaceOperationError("manifest_invalid")
		}
		if err := safepath.WriteFileExclusiveWithin(absRoot, markerPath, data, 0o600); err != nil {
			return emptyPrivateWorkspaceReport(), privateWorkspaceOperationError("manifest_create")
		}
	} else if markerErr != nil {
		return emptyPrivateWorkspaceReport(), privateWorkspaceOperationError("manifest_stat")
	} else {
		if markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() || !privateWorkspaceFileMode(markerInfo.Mode()) {
			return emptyPrivateWorkspaceReport(), privateWorkspaceOperationError("manifest_mode")
		}
		file, err := os.Open(markerPath)
		if err != nil {
			return emptyPrivateWorkspaceReport(), privateWorkspaceOperationError("manifest_read")
		}
		existing, decodeErr := DecodePrivateWorkspaceManifest(file)
		closeErr := file.Close()
		if decodeErr != nil || closeErr != nil || !reflect.DeepEqual(existing, manifest) {
			return emptyPrivateWorkspaceReport(), privateWorkspaceOperationError("manifest_mismatch")
		}
	}

	if err := ensurePrivateWorkspaceDirectories(absRoot); err != nil {
		return emptyPrivateWorkspaceReport(), err
	}
	report := InspectPrivateWorkspace(absRoot, absRepository)
	if !report.Healthy {
		return report, ErrPrivateWorkspaceUnhealthy
	}
	return report, nil
}

// InspectPrivateWorkspace returns a bounded, privacy-safe health report and
// never returns raw filesystem, Git, or run-contract errors.
func InspectPrivateWorkspace(root, repositoryRoot string) PrivateWorkspaceReport {
	report := emptyPrivateWorkspaceReport()
	absRoot, absRepository, err := privateWorkspaceLocations(root, repositoryRoot, false)
	if err != nil {
		return failPrivateWorkspaceChecks(report, PrivateWorkspaceCheckRootExists)
	}
	report = appendPrivateWorkspaceCheck(report, PrivateWorkspaceCheckRootExists, true)

	rootInfo, err := os.Lstat(absRoot)
	rootOK := err == nil && rootInfo.IsDir() && rootInfo.Mode()&os.ModeSymlink == 0 && privateWorkspaceDirectoryMode(rootInfo.Mode())
	report = appendPrivateWorkspaceCheck(report, PrivateWorkspaceCheckRootOwnerOnly, rootOK)
	rootMarkerOK := rootOK && privateWorkspaceRootMarkerOK(absRoot)
	report = appendPrivateWorkspaceCheck(report, PrivateWorkspaceCheckRootMarker, rootMarkerOK)

	gitOK := rootOK && privateWorkspaceGitBoundary(absRoot, absRepository, true) == nil
	report = appendPrivateWorkspaceCheck(report, PrivateWorkspaceCheckGitBoundary, gitOK)

	markerPath := filepath.Join(absRoot, PrivateWorkspaceManifestName)
	markerInfo, markerErr := os.Lstat(markerPath)
	markerOK := markerErr == nil && markerInfo.Mode()&os.ModeSymlink == 0 && markerInfo.Mode().IsRegular() && privateWorkspaceFileMode(markerInfo.Mode())
	report = appendPrivateWorkspaceCheck(report, PrivateWorkspaceCheckManifestMode, markerOK)

	var manifest PrivateWorkspaceManifest
	manifestOK := false
	if markerOK {
		file, openErr := os.Open(markerPath)
		if openErr == nil {
			manifest, err = DecodePrivateWorkspaceManifest(file)
			closeErr := file.Close()
			manifestOK = err == nil && closeErr == nil
		}
	}
	report = appendPrivateWorkspaceCheck(report, PrivateWorkspaceCheckManifestValid, manifestOK)
	if manifestOK {
		report.Counts.RunSets = len(manifest.RunSets)
		for _, runSet := range manifest.RunSets {
			report.Counts.SpecReferences += len(runSet.SpecPaths)
		}
	}

	layoutOK := rootOK && privateWorkspaceLayoutOK(absRoot)
	report = appendPrivateWorkspaceCheck(report, PrivateWorkspaceCheckFixedLayout, layoutOK)
	if layoutOK {
		report.Counts.FixedDirectories = len(privateWorkspaceFixedDirectories)
	}

	treeModeOK, treeSymlinkOK := false, false
	if rootOK {
		treeModeOK, treeSymlinkOK = inspectPrivateWorkspaceTree(absRoot)
	}
	report = appendPrivateWorkspaceCheck(report, PrivateWorkspaceCheckTreeOwnerOnly, treeModeOK)
	report = appendPrivateWorkspaceCheck(report, PrivateWorkspaceCheckTreeNoSymlinks, treeSymlinkOK)

	containedOK, validSpecs := false, 0
	specsOK := false
	if manifestOK && layoutOK && treeSymlinkOK {
		containedOK, specsOK, validSpecs = inspectPrivateWorkspaceSpecs(absRoot, manifest)
	}
	report.Counts.ValidSpecs = validSpecs
	report = appendPrivateWorkspaceCheck(report, PrivateWorkspaceCheckSpecsContained, containedOK)
	report = appendPrivateWorkspaceCheck(report, PrivateWorkspaceCheckSpecsValid, specsOK)
	scratchOK := layoutOK && inspectPrivateWorkspaceScratch(absRoot)
	report = appendPrivateWorkspaceCheck(report, PrivateWorkspaceCheckScratchClean, scratchOK)

	lifecycleOK := false
	if layoutOK && treeModeOK && treeSymlinkOK {
		lifecycle, lifecycleErr := inspectPrivatePlanLifecycleAtRoot(absRoot)
		lifecycleOK = lifecycleErr == nil
		if lifecycleOK {
			report.Counts.PendingPlans = lifecycle.pendingPlans
			report.Counts.ActiveRuns = lifecycle.activeRuns
			report.Counts.IncompleteRuns = lifecycle.incompleteRuns
			report.Counts.CompletedRuns = lifecycle.completedRuns
			report.Counts.PrunedRuns = lifecycle.prunedRuns
		}
	}
	report = appendPrivateWorkspaceCheck(report, PrivateWorkspaceCheckLifecycleValid, lifecycleOK)
	report.Healthy = true
	for _, check := range report.Checks {
		if check.Status != "pass" {
			report.Healthy = false
			break
		}
	}
	switch {
	case !report.Healthy:
		report.State = "unhealthy"
		report.NextActions = []string{"repair_workspace"}
	case report.Counts.RunSets == 0:
		report.State = "needs_configuration"
		report.NextActions = []string{"configure_run_sets"}
	case report.Counts.ActiveRuns > 0:
		report.State = "run_in_progress"
		report.NextActions = []string{"inspect_active_run"}
	case report.Counts.IncompleteRuns > 0:
		report.State = "needs_review"
		report.NextActions = []string{"review_incomplete_run", "create_reviewed_plan"}
	case report.Counts.CompletedRuns > 0:
		report.State = "ready"
		report.NextActions = []string{"assess_compare_or_promote", "create_reviewed_plan"}
	case report.Counts.PendingPlans > 0:
		report.State = "plan_pending"
		report.NextActions = []string{"review_pending_plan"}
	default:
		report.State = "ready"
		report.NextActions = []string{"create_reviewed_plan"}
	}
	return report
}

func DoctorPrivateWorkspace(root, repositoryRoot string) (PrivateWorkspaceReport, error) {
	report := InspectPrivateWorkspace(root, repositoryRoot)
	if !report.Healthy {
		return report, ErrPrivateWorkspaceUnhealthy
	}
	return report, nil
}

func privateWorkspaceLocations(root, repositoryRoot string, allowMissingRoot bool) (string, string, error) {
	absRepository, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return "", "", privateWorkspaceOperationError("repository_root")
	}
	absRepository, err = filepath.EvalSymlinks(absRepository)
	if err != nil {
		return "", "", privateWorkspaceOperationError("repository_root")
	}
	repositoryInfo, err := os.Stat(absRepository)
	if err != nil || !repositoryInfo.IsDir() {
		return "", "", privateWorkspaceOperationError("repository_root")
	}
	absInput, err := filepath.Abs(root)
	if err != nil {
		return "", "", privateWorkspaceOperationError("root")
	}
	if info, lstatErr := os.Lstat(absInput); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", "", privateWorkspaceOperationError("root_symlink")
	} else if lstatErr != nil && !os.IsNotExist(lstatErr) {
		return "", "", privateWorkspaceOperationError("root")
	}
	var absRoot string
	if allowMissingRoot {
		absRoot, err = canonicalizeForCreation(absInput)
	} else {
		absRoot, err = filepath.EvalSymlinks(absInput)
	}
	if err != nil {
		return "", "", privateWorkspaceOperationError("root")
	}
	return absRoot, absRepository, nil
}

func privateWorkspaceGitBoundary(root, repository string, inspectTree bool) error {
	inside, err := pathWithin(repository, root)
	if err != nil {
		return privateWorkspaceOperationError("git_boundary")
	}
	if !inside {
		return nil
	}
	if root == repository {
		return privateWorkspaceOperationError("git_boundary")
	}
	relative, err := filepath.Rel(repository, root)
	if err != nil {
		return privateWorkspaceOperationError("git_boundary")
	}
	if !gitPathIgnored(repository, relative) {
		return privateWorkspaceOperationError("git_boundary")
	}
	tracked := exec.Command("git", "-C", repository, "ls-files", "--cached", "--", relative)
	trackedOutput, err := tracked.Output()
	if err != nil || len(bytes.TrimSpace(trackedOutput)) != 0 {
		return privateWorkspaceOperationError("git_boundary")
	}
	if !inspectTree {
		return nil
	}
	var ignoredInput bytes.Buffer
	expected := map[string]struct{}{}
	walkErr := filepath.WalkDir(root, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entryRelative, err := filepath.Rel(repository, path)
		if err != nil {
			return ErrPrivateWorkspaceUnhealthy
		}
		entryRelative = filepath.ToSlash(entryRelative)
		expected[entryRelative] = struct{}{}
		ignoredInput.WriteString(entryRelative)
		ignoredInput.WriteByte(0)
		return nil
	})
	if walkErr != nil {
		return privateWorkspaceOperationError("git_boundary")
	}
	command := exec.Command("git", "-C", repository, "check-ignore", "--no-index", "--stdin", "-z")
	command.Stdin = &ignoredInput
	output, err := command.Output()
	if err != nil {
		return privateWorkspaceOperationError("git_boundary")
	}
	for _, item := range bytes.Split(output, []byte{0}) {
		if len(item) == 0 {
			continue
		}
		path := filepath.ToSlash(string(item))
		if _, exists := expected[path]; !exists {
			return privateWorkspaceOperationError("git_boundary")
		}
		delete(expected, path)
	}
	if len(expected) != 0 {
		return privateWorkspaceOperationError("git_boundary")
	}
	return nil
}

func gitPathIgnored(repository, relative string) bool {
	command := exec.Command("git", "-C", repository, "check-ignore", "--quiet", "--no-index", "--", relative)
	if command.Run() == nil {
		return true
	}
	command = exec.Command("git", "-C", repository, "check-ignore", "--quiet", "--no-index", "--", relative+string(filepath.Separator))
	return command.Run() == nil
}

func ensurePrivateWorkspaceDirectories(root string) error {
	for _, name := range privateWorkspaceFixedDirectories {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		switch {
		case os.IsNotExist(err):
			if err := os.Mkdir(path, 0o700); err != nil {
				return privateWorkspaceOperationError("layout_create")
			}
		case err != nil:
			return privateWorkspaceOperationError("layout_stat")
		case info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !privateWorkspaceDirectoryMode(info.Mode()):
			return privateWorkspaceOperationError("layout_mode")
		}
	}
	return nil
}

func privateWorkspaceLayoutOK(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	allowed := map[string]struct{}{PrivateWorkspaceManifestName: {}, privateOutputRootMarker: {}}
	for _, name := range privateWorkspaceFixedDirectories {
		allowed[name] = struct{}{}
	}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok {
			return false
		}
	}
	for _, name := range privateWorkspaceFixedDirectories {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !privateWorkspaceDirectoryMode(info.Mode()) {
			return false
		}
	}
	return true
}

func inspectPrivateWorkspaceTree(root string) (modeOK, symlinkOK bool) {
	modeOK, symlinkOK = true, true
	entries := 0
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > maxPrivateWorkspaceTreeEntries {
			return ErrPrivateWorkspaceUnhealthy
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			symlinkOK = false
			return nil
		}
		if info.IsDir() {
			modeOK = modeOK && privateWorkspaceDirectoryMode(info.Mode())
			return nil
		}
		modeOK = modeOK && info.Mode().IsRegular() && privateWorkspaceFileMode(info.Mode())
		return nil
	})
	if err != nil {
		return false, false
	}
	return modeOK, symlinkOK
}

func inspectPrivateWorkspaceScratch(root string) bool {
	entries, err := os.ReadDir(filepath.Join(root, ".ephemeral"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(privateWorkspaceLockPath) || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return false
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
			return false
		}
	}
	return true
}

func inspectPrivateWorkspaceSpecs(root string, manifest PrivateWorkspaceManifest) (containedOK, specsOK bool, validSpecs int) {
	containedOK, specsOK = true, true
	for _, runSet := range manifest.RunSets {
		paths := make([]string, 0, len(runSet.SpecPaths))
		for _, relative := range runSet.SpecPaths {
			target := filepath.Join(root, filepath.FromSlash(relative))
			resolved, err := filepath.EvalSymlinks(target)
			if err != nil {
				containedOK, specsOK = false, false
				continue
			}
			inside, err := pathWithin(filepath.Join(root, "cases"), resolved)
			if err != nil || !inside {
				containedOK, specsOK = false, false
				continue
			}
			paths = append(paths, resolved)
			if _, _, err := ValidateRunSpecFile(resolved); err != nil {
				specsOK = false
				continue
			}
			validSpecs++
		}
		if len(paths) != len(runSet.SpecPaths) {
			continue
		}
		if len(paths) > 1 {
			if _, err := ValidatePrivateRunComparisonSet(paths...); err != nil {
				specsOK = false
			}
		} else {
			spec, _, err := ValidateRunSpecFile(paths[0])
			if err != nil || spec.EffectiveBackendMode() != BackendModePrivateLive {
				specsOK = false
			}
		}
	}
	return containedOK, specsOK, validSpecs
}

func privateWorkspaceDirectoryMode(mode os.FileMode) bool {
	return runtime.GOOS == "windows" || mode.Perm() == 0o700
}

func privateWorkspaceRootMarkerOK(root string) bool {
	path := filepath.Join(root, privateOutputRootMarker)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
		return false
	}
	data, err := readBoundedFile(path, int64(len(privateOutputRootMarkerContents)))
	return err == nil && string(data) == privateOutputRootMarkerContents
}

func privateWorkspaceFileMode(mode os.FileMode) bool {
	return runtime.GOOS == "windows" || mode.Perm()&0o077 == 0
}

func privateWorkspaceContractError(code string) error {
	return fmt.Errorf("private workspace manifest is invalid: %s", code)
}

func privateWorkspaceOperationError(code string) error {
	return fmt.Errorf("%w: %s", ErrPrivateWorkspaceUnhealthy, code)
}

func emptyPrivateWorkspaceReport() PrivateWorkspaceReport {
	return PrivateWorkspaceReport{SchemaVersion: 1, State: "unhealthy", NextActions: []string{"repair_workspace"}, Checks: []PrivateWorkspaceCheck{}}
}

func appendPrivateWorkspaceCheck(report PrivateWorkspaceReport, code string, pass bool) PrivateWorkspaceReport {
	status := "fail"
	if pass {
		status = "pass"
	}
	report.Checks = append(report.Checks, PrivateWorkspaceCheck{Code: code, Status: status})
	return report
}

func failPrivateWorkspaceChecks(report PrivateWorkspaceReport, first string) PrivateWorkspaceReport {
	codes := []string{
		PrivateWorkspaceCheckRootExists, PrivateWorkspaceCheckRootOwnerOnly,
		PrivateWorkspaceCheckRootMarker, PrivateWorkspaceCheckGitBoundary, PrivateWorkspaceCheckManifestMode,
		PrivateWorkspaceCheckManifestValid, PrivateWorkspaceCheckFixedLayout,
		PrivateWorkspaceCheckTreeOwnerOnly, PrivateWorkspaceCheckTreeNoSymlinks,
		PrivateWorkspaceCheckSpecsContained, PrivateWorkspaceCheckSpecsValid,
		PrivateWorkspaceCheckScratchClean,
		PrivateWorkspaceCheckLifecycleValid,
	}
	start := false
	for _, code := range codes {
		if code == first {
			start = true
		}
		if start {
			report = appendPrivateWorkspaceCheck(report, code, false)
		}
	}
	return report
}

// PrivateWorkspaceFixedDirectories returns a copy so callers cannot mutate the
// package's fixed-layout contract.
func PrivateWorkspaceFixedDirectories() []string {
	result := append([]string(nil), privateWorkspaceFixedDirectories...)
	sort.Strings(result)
	return result
}
