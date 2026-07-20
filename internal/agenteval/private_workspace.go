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
	"unicode/utf8"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	PrivateWorkspaceSchemaVersion          = 4
	LegacyCalibratedWorkspaceSchemaVersion = 3
	LegacyActivationWorkspaceSchemaVersion = 2
	LegacyPrivateWorkspaceSchemaVersion    = 1
	PrivateWorkspaceManifestName           = "private-workspace.v4.json"
	LegacyCalibratedWorkspaceManifestName  = "private-workspace.v3.json"
	LegacyActivationWorkspaceManifestName  = "private-workspace.v2.json"
	LegacyPrivateWorkspaceManifestName     = "private-workspace.v1.json"

	maxPrivateWorkspaceManifestBytes = 1 << 20
	maxPrivateWorkspaceRunSets       = 64
	maxPrivateWorkspaceSpecsPerSet   = 4
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
	Kind                                string                         `json:"kind,omitempty"`
	Alias                               string                         `json:"alias"`
	SpecPaths                           []string                       `json:"spec_paths"`
	QualitativeReviewRequired           bool                           `json:"qualitative_review_required"`
	QualitativeReviewPanel              *PrivateQualitativeReviewPanel `json:"qualitative_review_panel,omitempty"`
	ReviewerReserveMicroUSD             int64                          `json:"reviewer_reserve_microusd,omitempty"`
	CalibrationMaxEstimatedCostMicroUSD int64                          `json:"calibration_max_estimated_cost_microusd,omitempty"`
}

const (
	PrivateRunSetKindComparison      = "comparison"
	PrivateRunSetKindActivationStudy = "activation-study"
)

func (r PrivateWorkspaceRunSet) EffectiveKind() string {
	if r.Kind == "" {
		return PrivateRunSetKindComparison
	}
	return r.Kind
}

const PrivateQualitativeReviewPanelMethod = QualitativePanelMethod

// PrivateQualitativeReviewPanel is the review policy selected before provider
// execution. BlindAssignment is a workspace-relative owner-private input below
// cases/; its contents never appear in lifecycle summaries.
type PrivateQualitativeReviewPanel struct {
	Method               string                     `json:"method"`
	Reviewers            []Reviewer                 `json:"reviewers"`
	MaxCriterionRangeBPS int                        `json:"max_criterion_range_bps"`
	BlindAssignment      string                     `json:"blind_assignment,omitempty"`
	Executions           []PrivateReviewerExecution `json:"executions,omitempty"`
}

// PrivateReviewerExecution binds the provider settings and maximum spend for
// one predeclared automated panel slot. Empty Executions preserves the manual
// review workflow used by legacy workspaces.
type PrivateReviewerExecution struct {
	ReviewerID               string  `json:"reviewer_id"`
	Reasoning                string  `json:"reasoning"`
	TimeoutSeconds           int     `json:"timeout_seconds"`
	Pricing                  Pricing `json:"pricing"`
	MaxEstimatedCostMicroUSD int64   `json:"max_estimated_cost_microusd"`
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
	if len(p.Executions) != 0 {
		if len(p.Executions) != len(p.Reviewers) {
			return privateWorkspaceContractError("reviewer_execution")
		}
		executions := make(map[string]PrivateReviewerExecution, len(p.Executions))
		for _, execution := range p.Executions {
			if !identifierRE.MatchString(execution.ReviewerID) || execution.Reasoning == "" ||
				len(execution.Reasoning) > 64 || strings.ContainsAny(execution.Reasoning, "\r\n\x00") ||
				execution.TimeoutSeconds < 1 || execution.TimeoutSeconds > 3600 ||
				execution.Pricing.InputMicroUSDPerMillionTokens < 0 || execution.Pricing.OutputMicroUSDPerMillionTokens < 0 ||
				execution.Pricing.InputMicroUSDPerMillionTokens+execution.Pricing.OutputMicroUSDPerMillionTokens < 1 ||
				execution.MaxEstimatedCostMicroUSD < 1 || execution.MaxEstimatedCostMicroUSD > 100_000_000 {
				return privateWorkspaceContractError("reviewer_execution")
			}
			if _, exists := executions[execution.ReviewerID]; exists {
				return privateWorkspaceContractError("reviewer_execution")
			}
			executions[execution.ReviewerID] = execution
		}
		for _, reviewer := range p.Reviewers {
			if reviewer.Kind != "codex" && reviewer.Kind != "claude-code" {
				return privateWorkspaceContractError("reviewer_execution")
			}
			execution, ok := executions[reviewer.ID]
			if !ok || !validPrivateReviewerReasoning(reviewer.Kind, execution.Reasoning) {
				return privateWorkspaceContractError("reviewer_execution")
			}
		}
	}
	return nil
}

func validPrivateReviewerReasoning(kind, reasoning string) bool {
	switch kind {
	case "codex":
		return reasoning == "minimal" || reasoning == "low" || reasoning == "medium" || reasoning == "high" || reasoning == "xhigh"
	case "claude-code":
		return reasoning == "low" || reasoning == "medium" || reasoning == "high" || reasoning == "xhigh" || reasoning == "max"
	default:
		return false
	}
}

type PrivateWorkspaceCounts struct {
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
	if m.SchemaVersion != PrivateWorkspaceSchemaVersion && m.SchemaVersion != LegacyCalibratedWorkspaceSchemaVersion &&
		m.SchemaVersion != LegacyActivationWorkspaceSchemaVersion && m.SchemaVersion != LegacyPrivateWorkspaceSchemaVersion {
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
		kind := runSet.EffectiveKind()
		if kind != PrivateRunSetKindComparison && kind != PrivateRunSetKindActivationStudy {
			return privateWorkspaceContractError("run_set_kind")
		}
		if m.SchemaVersion == LegacyPrivateWorkspaceSchemaVersion && (runSet.Kind != "" || runSet.ReviewerReserveMicroUSD != 0 || runSet.CalibrationMaxEstimatedCostMicroUSD != 0) {
			return privateWorkspaceContractError("run_set_kind")
		}
		if !privateWorkspaceAliasRE.MatchString(runSet.Alias) {
			return privateWorkspaceContractError("run_set_alias")
		}
		if _, exists := aliases[runSet.Alias]; exists {
			return privateWorkspaceContractError("run_set_alias")
		}
		aliases[runSet.Alias] = struct{}{}
		maxSpecs := 3
		if kind == PrivateRunSetKindActivationStudy {
			maxSpecs = maxPrivateWorkspaceSpecsPerSet
		}
		if len(runSet.SpecPaths) < 1 || len(runSet.SpecPaths) > maxSpecs ||
			(kind == PrivateRunSetKindActivationStudy && len(runSet.SpecPaths) != 4) {
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
			if len(runSet.QualitativeReviewPanel.Executions) != 0 && m.SchemaVersion != PrivateWorkspaceSchemaVersion {
				return privateWorkspaceContractError("reviewer_execution")
			}
		}
		reviewerExecutionCost := int64(0)
		if runSet.QualitativeReviewPanel != nil {
			for _, execution := range runSet.QualitativeReviewPanel.Executions {
				if reviewerExecutionCost > m.Execution.MaxEstimatedCostMicroUSD-execution.MaxEstimatedCostMicroUSD {
					return privateWorkspaceContractError("reviewer_reserve")
				}
				reviewerExecutionCost += execution.MaxEstimatedCostMicroUSD
			}
		}
		if reviewerExecutionCost != 0 {
			if reviewerExecutionCost > m.Execution.MaxEstimatedCostMicroUSD/int64(len(runSet.SpecPaths)) {
				return privateWorkspaceContractError("reviewer_reserve")
			}
			reviewerExecutionCost *= int64(len(runSet.SpecPaths))
		}
		if reviewerExecutionCost != 0 && (runSet.ReviewerReserveMicroUSD < reviewerExecutionCost || runSet.ReviewerReserveMicroUSD > m.Execution.MaxEstimatedCostMicroUSD) {
			return privateWorkspaceContractError("reviewer_reserve")
		}
		if kind == PrivateRunSetKindActivationStudy {
			commonInvalid := runSet.QualitativeReviewRequired || runSet.QualitativeReviewPanel == nil ||
				runSet.QualitativeReviewPanel.BlindAssignment == "" || runSet.ReviewerReserveMicroUSD < 1 ||
				runSet.ReviewerReserveMicroUSD > m.Execution.MaxEstimatedCostMicroUSD
			currentInvalid := (m.SchemaVersion == PrivateWorkspaceSchemaVersion || m.SchemaVersion == LegacyCalibratedWorkspaceSchemaVersion) &&
				(runSet.CalibrationMaxEstimatedCostMicroUSD < 1 || runSet.CalibrationMaxEstimatedCostMicroUSD > m.Execution.MaxEstimatedCostMicroUSD-runSet.ReviewerReserveMicroUSD)
			legacyInvalid := m.SchemaVersion == LegacyActivationWorkspaceSchemaVersion && runSet.CalibrationMaxEstimatedCostMicroUSD != 0
			if commonInvalid || currentInvalid || legacyInvalid || m.SchemaVersion == LegacyPrivateWorkspaceSchemaVersion {
				return privateWorkspaceContractError("activation_study")
			}
		} else if runSet.CalibrationMaxEstimatedCostMicroUSD != 0 ||
			(runSet.ReviewerReserveMicroUSD != 0 && reviewerExecutionCost == 0) {
			return privateWorkspaceContractError("reviewer_reserve")
		}
	}
	return nil
}

func validPrivateWorkspaceSpecPath(path string) bool {
	return validPrivateWorkspaceCaseFilePath(path) && strings.HasSuffix(path, ".json")
}

func validPrivateWorkspaceCaseFilePath(path string) bool {
	if path == "" || utf8.RuneCountInString(path) > 512 || filepath.IsAbs(path) || strings.ContainsAny(path, "\\\r\n\x00") {
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
	if err := validatePrivateWorkspaceManifestPresence(data); err != nil {
		return PrivateWorkspaceManifest{}, err
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

func validatePrivateWorkspaceManifestPresence(data []byte) error {
	var value any
	if json.Unmarshal(data, &value) != nil || privateWorkspaceJSONContainsNull(value) {
		return privateWorkspaceContractError("decode")
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || validatePrivateWorkspaceObjectKeys(root,
		[]string{"schema_version", "live_config_env", "execution", "retention", "run_sets"},
		[]string{"external_mcp_profile_env"}) != nil {
		return privateWorkspaceContractError("decode")
	}
	var execution map[string]json.RawMessage
	if json.Unmarshal(root["execution"], &execution) != nil || validatePrivateWorkspaceObjectKeys(execution,
		[]string{"max_estimated_cost_microusd"}, nil) != nil {
		return privateWorkspaceContractError("decode")
	}
	var retention map[string]json.RawMessage
	if json.Unmarshal(root["retention"], &retention) != nil || validatePrivateWorkspaceObjectKeys(retention,
		[]string{"keep_completed_run_sets_per_alias", "max_candidate_age_days", "max_candidate_bytes", "retain_baseline_transcripts"}, nil) != nil {
		return privateWorkspaceContractError("decode")
	}
	var runSets []map[string]json.RawMessage
	if json.Unmarshal(root["run_sets"], &runSets) != nil {
		return privateWorkspaceContractError("decode")
	}
	for _, runSet := range runSets {
		if validatePrivateWorkspaceObjectKeys(runSet, []string{"alias", "spec_paths", "qualitative_review_required"},
			[]string{"kind", "reviewer_reserve_microusd", "calibration_max_estimated_cost_microusd", "qualitative_review_panel"}) != nil {
			return privateWorkspaceContractError("decode")
		}
		if panelData, ok := runSet["qualitative_review_panel"]; ok {
			var panel map[string]json.RawMessage
			if json.Unmarshal(panelData, &panel) != nil || validatePrivateWorkspaceObjectKeys(panel,
				[]string{"method", "reviewers", "max_criterion_range_bps"}, []string{"blind_assignment", "executions"}) != nil {
				return privateWorkspaceContractError("decode")
			}
			var reviewers []map[string]json.RawMessage
			if json.Unmarshal(panel["reviewers"], &reviewers) != nil {
				return privateWorkspaceContractError("decode")
			}
			for _, reviewer := range reviewers {
				if validatePrivateWorkspaceObjectKeys(reviewer, []string{"id", "kind"}, []string{"model"}) != nil {
					return privateWorkspaceContractError("decode")
				}
			}
			if encoded, ok := panel["executions"]; ok {
				var executions []map[string]json.RawMessage
				if json.Unmarshal(encoded, &executions) != nil {
					return privateWorkspaceContractError("decode")
				}
				for _, execution := range executions {
					if validatePrivateWorkspaceObjectKeys(execution,
						[]string{"reviewer_id", "reasoning", "timeout_seconds", "pricing", "max_estimated_cost_microusd"}, nil) != nil {
						return privateWorkspaceContractError("decode")
					}
					var pricing map[string]json.RawMessage
					if json.Unmarshal(execution["pricing"], &pricing) != nil || validatePrivateWorkspaceObjectKeys(pricing,
						[]string{"input_microusd_per_million_tokens", "output_microusd_per_million_tokens"}, nil) != nil {
						return privateWorkspaceContractError("decode")
					}
				}
			}
		}
	}
	var raw struct {
		SchemaVersion         int                          `json:"schema_version"`
		ExternalMCPProfileEnv json.RawMessage              `json:"external_mcp_profile_env"`
		Retention             map[string]json.RawMessage   `json:"retention"`
		RunSets               []map[string]json.RawMessage `json:"run_sets"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return privateWorkspaceContractError("decode")
	}
	if _, ok := raw.Retention["retain_baseline_transcripts"]; !ok {
		return privateWorkspaceContractError("retention_presence")
	}
	if len(raw.ExternalMCPProfileEnv) != 0 {
		var value string
		if json.Unmarshal(raw.ExternalMCPProfileEnv, &value) != nil || value == "" {
			return privateWorkspaceContractError("external_mcp_profile_env")
		}
	}
	for _, runSet := range raw.RunSets {
		if _, ok := runSet["qualitative_review_required"]; !ok {
			return privateWorkspaceContractError("qualitative_review_presence")
		}
		kind := PrivateRunSetKindComparison
		if encoded, ok := runSet["kind"]; ok {
			if json.Unmarshal(encoded, &kind) != nil || kind == "" {
				return privateWorkspaceContractError("run_set_kind")
			}
		}
		_, reservePresent := runSet["reviewer_reserve_microusd"]
		_, calibrationPresent := runSet["calibration_max_estimated_cost_microusd"]
		executionsPresent := false
		if panelData, ok := runSet["qualitative_review_panel"]; ok {
			var panel map[string]json.RawMessage
			if json.Unmarshal(panelData, &panel) != nil {
				return privateWorkspaceContractError("decode")
			}
			if executionData, ok := panel["executions"]; ok {
				var executions []json.RawMessage
				if json.Unmarshal(executionData, &executions) != nil || len(executions) == 0 {
					return privateWorkspaceContractError("reviewer_execution")
				}
				executionsPresent = true
			}
		}
		if raw.SchemaVersion == LegacyPrivateWorkspaceSchemaVersion && reservePresent {
			return privateWorkspaceContractError("reviewer_reserve")
		}
		if kind == PrivateRunSetKindActivationStudy && (!reservePresent ||
			(raw.SchemaVersion == PrivateWorkspaceSchemaVersion || raw.SchemaVersion == LegacyCalibratedWorkspaceSchemaVersion) && !calibrationPresent) {
			return privateWorkspaceContractError("reviewer_reserve")
		}
		if kind != PrivateRunSetKindActivationStudy && calibrationPresent {
			return privateWorkspaceContractError("reviewer_reserve")
		}
		if kind != PrivateRunSetKindActivationStudy && reservePresent != executionsPresent {
			return privateWorkspaceContractError("reviewer_reserve")
		}
		if raw.SchemaVersion != PrivateWorkspaceSchemaVersion && raw.SchemaVersion != LegacyCalibratedWorkspaceSchemaVersion && calibrationPresent {
			return privateWorkspaceContractError("calibration_reserve")
		}
		if raw.SchemaVersion != PrivateWorkspaceSchemaVersion {
			if reservePresent && kind != PrivateRunSetKindActivationStudy {
				return privateWorkspaceContractError("reviewer_reserve")
			}
			if panelData, ok := runSet["qualitative_review_panel"]; ok {
				var panel map[string]json.RawMessage
				if json.Unmarshal(panelData, &panel) != nil {
					return privateWorkspaceContractError("decode")
				}
				if _, executionPresent := panel["executions"]; executionPresent {
					return privateWorkspaceContractError("reviewer_execution")
				}
			}
		}
	}
	return nil
}

func validatePrivateWorkspaceObjectKeys(value map[string]json.RawMessage, required, optional []string) error {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = struct{}{}
		if _, ok := value[key]; !ok {
			return privateWorkspaceContractError("decode")
		}
	}
	for _, key := range optional {
		allowed[key] = struct{}{}
	}
	for key := range value {
		if _, ok := allowed[key]; !ok {
			return privateWorkspaceContractError("decode")
		}
	}
	return nil
}

func privateWorkspaceJSONContainsNull(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case []any:
		for _, item := range typed {
			if privateWorkspaceJSONContainsNull(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if privateWorkspaceJSONContainsNull(item) {
				return true
			}
		}
	}
	return false
}

func loadPrivateWorkspaceManifest(root string) (PrivateWorkspaceManifest, string, error) {
	path, err := privateWorkspaceManifestPath(root)
	if err != nil {
		return PrivateWorkspaceManifest{}, "", err
	}
	data, err := safepath.ReadFileWithinLimit(root, path, maxPrivateWorkspaceManifestBytes)
	if err != nil {
		return PrivateWorkspaceManifest{}, "", privateWorkspaceOperationError("manifest_read")
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data))
	if err != nil || !privateWorkspaceManifestSchemaMatchesPath(path, manifest.SchemaVersion) {
		return PrivateWorkspaceManifest{}, "", privateWorkspaceOperationError("manifest_mismatch")
	}
	return manifest, path, nil
}

func privateWorkspaceManifestPath(root string) (string, error) {
	current := filepath.Join(root, PrivateWorkspaceManifestName)
	legacyCalibrated := filepath.Join(root, LegacyCalibratedWorkspaceManifestName)
	legacyActivation := filepath.Join(root, LegacyActivationWorkspaceManifestName)
	legacy := filepath.Join(root, LegacyPrivateWorkspaceManifestName)
	_, currentErr := os.Lstat(current)
	_, legacyCalibratedErr := os.Lstat(legacyCalibrated)
	_, legacyActivationErr := os.Lstat(legacyActivation)
	_, legacyErr := os.Lstat(legacy)
	present := 0
	for _, err := range []error{currentErr, legacyCalibratedErr, legacyActivationErr, legacyErr} {
		if err == nil {
			present++
		}
	}
	if present > 1 {
		return "", privateWorkspaceOperationError("manifest_ambiguous")
	}
	if currentErr == nil {
		return current, nil
	}
	if legacyCalibratedErr == nil {
		return legacyCalibrated, nil
	}
	if legacyActivationErr == nil {
		return legacyActivation, nil
	}
	if legacyErr == nil {
		return legacy, nil
	}
	if !os.IsNotExist(currentErr) || !os.IsNotExist(legacyCalibratedErr) || !os.IsNotExist(legacyActivationErr) || !os.IsNotExist(legacyErr) {
		return "", privateWorkspaceOperationError("manifest_stat")
	}
	return current, nil
}

func privateWorkspaceManifestPathForSchema(root string, schemaVersion int) string {
	switch schemaVersion {
	case LegacyPrivateWorkspaceSchemaVersion:
		return filepath.Join(root, LegacyPrivateWorkspaceManifestName)
	case LegacyActivationWorkspaceSchemaVersion:
		return filepath.Join(root, LegacyActivationWorkspaceManifestName)
	case LegacyCalibratedWorkspaceSchemaVersion:
		return filepath.Join(root, LegacyCalibratedWorkspaceManifestName)
	default:
		return filepath.Join(root, PrivateWorkspaceManifestName)
	}
}

func privateWorkspaceManifestSchemaMatchesPath(path string, schemaVersion int) bool {
	switch filepath.Base(path) {
	case PrivateWorkspaceManifestName:
		return schemaVersion == PrivateWorkspaceSchemaVersion
	case LegacyCalibratedWorkspaceManifestName:
		return schemaVersion == LegacyCalibratedWorkspaceSchemaVersion
	case LegacyActivationWorkspaceManifestName:
		return schemaVersion == LegacyActivationWorkspaceSchemaVersion
	case LegacyPrivateWorkspaceManifestName:
		return schemaVersion == LegacyPrivateWorkspaceSchemaVersion
	default:
		return false
	}
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

	markerPath, pathErr := privateWorkspaceManifestPath(absRoot)
	if pathErr != nil {
		return emptyPrivateWorkspaceReport(), pathErr
	}
	markerInfo, markerErr := os.Lstat(markerPath)
	if os.IsNotExist(markerErr) {
		markerPath = privateWorkspaceManifestPathForSchema(absRoot, manifest.SchemaVersion)
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
		if decodeErr != nil || closeErr != nil || !privateWorkspaceManifestSchemaMatchesPath(markerPath, existing.SchemaVersion) ||
			(existing.SchemaVersion == PrivateWorkspaceSchemaVersion && !reflect.DeepEqual(existing, manifest)) {
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

	markerPath, markerPathErr := privateWorkspaceManifestPath(absRoot)
	if markerPathErr != nil {
		markerPath = filepath.Join(absRoot, PrivateWorkspaceManifestName)
	}
	markerInfo, markerErr := os.Lstat(markerPath)
	if markerPathErr != nil {
		markerErr = markerPathErr
	}
	markerOK := markerErr == nil && markerInfo.Mode()&os.ModeSymlink == 0 && markerInfo.Mode().IsRegular() && privateWorkspaceFileMode(markerInfo.Mode())
	report = appendPrivateWorkspaceCheck(report, PrivateWorkspaceCheckManifestMode, markerOK)

	var manifest PrivateWorkspaceManifest
	manifestOK := false
	if markerOK {
		file, openErr := os.Open(markerPath)
		if openErr == nil {
			manifest, err = DecodePrivateWorkspaceManifest(file)
			closeErr := file.Close()
			manifestOK = err == nil && closeErr == nil && privateWorkspaceManifestSchemaMatchesPath(markerPath, manifest.SchemaVersion)
		}
	}
	report = appendPrivateWorkspaceCheck(report, PrivateWorkspaceCheckManifestValid, manifestOK)
	if manifestOK {
		report.Counts.RunSets = len(manifest.RunSets)
		for _, runSet := range manifest.RunSets {
			report.Counts.SpecReferences += len(runSet.SpecPaths)
			if runSet.EffectiveKind() == PrivateRunSetKindActivationStudy {
				report.Counts.ActivationStudies++
			}
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
	allowed := map[string]struct{}{PrivateWorkspaceManifestName: {}, LegacyCalibratedWorkspaceManifestName: {},
		LegacyActivationWorkspaceManifestName: {}, LegacyPrivateWorkspaceManifestName: {}, privateOutputRootMarker: {}}
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
		if runSet.EffectiveKind() == PrivateRunSetKindActivationStudy {
			var err error
			if manifest.SchemaVersion == LegacyActivationWorkspaceSchemaVersion {
				err = validateLegacyPrivateActivationStudy(paths...)
			} else {
				_, err = ValidatePrivateActivationStudy(paths...)
			}
			if err != nil {
				specsOK = false
			}
		} else if len(paths) > 1 {
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
