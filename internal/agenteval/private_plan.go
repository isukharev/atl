package agenteval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	PrivatePlanSchemaVersion                         = 6
	LegacyCalibratedPrivatePlanSchemaVersion         = 5
	LegacyActivationStudyPrivatePlanSchemaVersion    = 4
	LegacyCompleteActivationPrivatePlanSchemaVersion = 3
	LegacyPromptBoundPrivatePlanSchemaVersion        = 2
	LegacyPrivatePlanSchemaVersion                   = 1
	PrivatePlanConsentConfirmation                   = "CONSENT"
	PrivatePlanConfirmation                          = "RUN"
	privatePlanMaxBytes                              = 4 << 20
	privatePlanStateSchemaVersion                    = 3
	legacyActivationPrivatePlanStateSchemaVersion    = 2
	legacyComparisonPrivatePlanStateSchemaVersion    = 1
)

var ErrPrivatePlanRejected = errors.New("private plan rejected")

var (
	privatePlanRunHeadless     = RunHeadless
	privatePlanRunCalibration  = RunCodexCLICalibration
	privatePlanQualifyCodexCLI = QualifyCodexCLIToolAvailability
	privatePlanWriteState      = writePrivatePlanState
	privatePlanRemoveTree      = removePrivateTree
)

var privateGitCommitRE = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type PrivatePlanConsent struct {
	ExpiresAt                string `json:"expires_at"`
	ProviderDataApproved     bool   `json:"provider_data_approved"`
	ExternalUpstreamApproved bool   `json:"external_upstream_approved"`
}

type PrivatePlanCreateOptions struct {
	Root, RepositoryRoot, RunSetAlias                     string
	ATLBinary, PluginRoot, AgentBinary, WrapperExecutable string
	Consent                                               PrivatePlanConsent
	Confirm                                               string
	Now                                                   time.Time
}

type PrivatePlanPreview struct {
	SchemaVersion                       int      `json:"schema_version"`
	PlanID                              string   `json:"plan_id"`
	PlanSHA256                          string   `json:"plan_sha256"`
	Surfaces                            []string `json:"surfaces"`
	Provider                            string   `json:"provider"`
	Model                               string   `json:"model"`
	ExpiresAt                           string   `json:"expires_at"`
	MaxEstimatedCostMicroUSD            int64    `json:"max_estimated_cost_microusd"`
	Kind                                string   `json:"kind,omitempty"`
	OrderedTreatments                   []string `json:"ordered_treatments,omitempty"`
	CostAssurance                       string   `json:"cost_assurance,omitempty"`
	CostPreventive                      bool     `json:"cost_preventive,omitempty"`
	ReviewerReserveMicroUSD             int64    `json:"reviewer_reserve_microusd,omitempty"`
	CalibrationMaxEstimatedCostMicroUSD int64    `json:"calibration_max_estimated_cost_microusd,omitempty"`
	CalibrationBound                    bool     `json:"calibration_bound,omitempty"`
}

type PrivatePlanExecuteOptions struct {
	Root, RepositoryRoot, PlanID, ExpectedPlanSHA256, Confirm string
	ATLBinary, PluginRoot, AgentBinary, WrapperExecutable     string
	Now                                                       time.Time
}

type PrivatePlanExecutionSummary struct {
	SchemaVersion         int      `json:"schema_version"`
	PlanID                string   `json:"plan_id"`
	RunID                 string   `json:"run_id"`
	Status                string   `json:"status"`
	Surfaces              []string `json:"surfaces"`
	Completed             int      `json:"completed"`
	EstimatedCostMicroUSD int64    `json:"estimated_cost_microusd"`
	CostKnown             *bool    `json:"cost_known,omitempty"`
}

func privatePlanSummary(planID, runID, status string, surfaces []string, completed int, cost int64) PrivatePlanExecutionSummary {
	return PrivatePlanExecutionSummary{SchemaVersion: 1, PlanID: planID, RunID: runID, Status: status,
		Surfaces: surfaces, Completed: completed, EstimatedCostMicroUSD: cost}
}

type privatePlan struct {
	SchemaVersion                       int                                    `json:"schema_version"`
	PlanID                              string                                 `json:"plan_id"`
	RunSetAlias                         string                                 `json:"run_set_alias"`
	Kind                                string                                 `json:"kind,omitempty"`
	ContractSHA256                      string                                 `json:"contract_sha256"`
	InputsSHA256                        string                                 `json:"inputs_sha256"`
	CreatedAt                           string                                 `json:"created_at"`
	Consent                             PrivatePlanConsent                     `json:"consent"`
	RepositoryCommit                    string                                 `json:"repository_commit"`
	RepositoryDirty                     bool                                   `json:"repository_dirty"`
	Provider                            string                                 `json:"provider"`
	Model                               string                                 `json:"model"`
	MaxEstimatedCostMicroUSD            int64                                  `json:"max_estimated_cost_microusd"`
	ReviewerReserveMicroUSD             int64                                  `json:"reviewer_reserve_microusd,omitempty"`
	CalibrationMaxEstimatedCostMicroUSD int64                                  `json:"calibration_max_estimated_cost_microusd,omitempty"`
	CostAssurance                       string                                 `json:"cost_assurance,omitempty"`
	StudySeriesSHA256                   string                                 `json:"study_series_sha256,omitempty"`
	StudyContract                       *privateActivationStudyPlanContract    `json:"study_contract,omitempty"`
	ActivationContract                  *PrivateActivationStudyContract        `json:"activation_contract,omitempty"`
	QualitativeRequired                 bool                                   `json:"qualitative_required"`
	QualitativeReviewPanel              *privateQualitativeReviewPanelContract `json:"qualitative_review_panel,omitempty"`
	ToolAvailability                    *CodexCLIToolAvailabilityReport        `json:"tool_availability,omitempty"`
	Items                               []privatePlanItem                      `json:"items"`
}

type privateQualitativeReviewPanelContract struct {
	Method                string     `json:"method"`
	Reviewers             []Reviewer `json:"reviewers"`
	MaxCriterionRangeBPS  int        `json:"max_criterion_range_bps"`
	BlindAssignmentSHA256 string     `json:"blind_assignment_sha256,omitempty"`
}

type privatePlanItem struct {
	CellID                   string `json:"cell_id,omitempty"`
	SpecPath                 string `json:"spec_path"`
	ScenarioID               string `json:"scenario_id"`
	Provider                 string `json:"provider"`
	Variant                  string `json:"variant"`
	Surface                  string `json:"surface"`
	SkillActivation          string `json:"skill_activation,omitempty"`
	PromptContractSHA256     string `json:"prompt_contract_sha256,omitempty"`
	RubricSHA256             string `json:"rubric_sha256"`
	MaxEstimatedCostMicroUSD int64  `json:"max_estimated_cost_microusd,omitempty"`
}

type privatePlanState struct {
	SchemaVersion         int                               `json:"schema_version"`
	PlanSHA256            string                            `json:"plan_sha256"`
	RunID                 string                            `json:"run_id"`
	Status                string                            `json:"status"`
	CompletedSurfaces     []string                          `json:"completed_surfaces"`
	CompletedCells        []string                          `json:"completed_cells,omitempty"`
	Events                []PrivateActivationLifecycleEvent `json:"events,omitempty"`
	StopReason            string                            `json:"stop_reason,omitempty"`
	EstimatedCostMicroUSD int64                             `json:"estimated_cost_microusd,omitempty"`
	CompletedAt           string                            `json:"completed_at,omitempty"`
}

type PrivatePlanRunReference struct {
	RunID          string
	RunSetAlias    string
	PlanID         string
	State          string
	RunRequired    bool
	CompletedOrder int64
}

func CreatePrivatePlan(ctx context.Context, options PrivatePlanCreateOptions) (PrivatePlanPreview, error) {
	if options.Confirm != PrivatePlanConsentConfirmation || !options.Consent.ProviderDataApproved {
		return PrivatePlanPreview{}, privatePlanError("consent")
	}
	now := options.Now.UTC()
	if options.Now.IsZero() {
		now = time.Now().UTC()
	}
	expires, err := time.Parse(time.RFC3339, options.Consent.ExpiresAt)
	if err != nil || !expires.After(now) || expires.After(now.Add(7*24*time.Hour)) {
		return PrivatePlanPreview{}, privatePlanError("consent_expiry")
	}
	workspaceRoot, _, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivatePlanPreview{}, privatePlanError("workspace")
	}
	lock, err := acquirePrivateWorkspaceLock(workspaceRoot)
	if err != nil {
		return PrivatePlanPreview{}, privatePlanError("workspace_busy")
	}
	defer func() { _ = lock.Unlock() }()
	root, manifest, runSet, err := loadPrivatePlanWorkspace(options.Root, options.RepositoryRoot, options.RunSetAlias)
	if err != nil {
		return PrivatePlanPreview{}, err
	}
	if runSet.EffectiveKind() == PrivateRunSetKindActivationStudy && manifest.SchemaVersion != PrivateWorkspaceSchemaVersion {
		return PrivatePlanPreview{}, privatePlanError("legacy_workspace_read_only")
	}
	items, material, provider, model, maxCost, external, err := buildPrivatePlanMaterial(ctx, root, options.RepositoryRoot, "", runSet,
		options.ATLBinary, options.PluginRoot, options.AgentBinary, options.WrapperExecutable,
		os.Getenv(manifest.LiveConfigEnv), os.Getenv(manifest.ExternalMCPProfileEnv), "")
	if err != nil {
		return PrivatePlanPreview{}, err
	}
	totalAuthorized := maxCost
	if runSet.EffectiveKind() == PrivateRunSetKindActivationStudy {
		reserved := runSet.ReviewerReserveMicroUSD + runSet.CalibrationMaxEstimatedCostMicroUSD
		if reserved < runSet.ReviewerReserveMicroUSD || totalAuthorized > manifest.Execution.MaxEstimatedCostMicroUSD-reserved {
			return PrivatePlanPreview{}, privatePlanError("cost_budget")
		}
		totalAuthorized += reserved
	}
	if totalAuthorized > manifest.Execution.MaxEstimatedCostMicroUSD {
		return PrivatePlanPreview{}, privatePlanError("cost_budget")
	}
	if external && !options.Consent.ExternalUpstreamApproved {
		return PrivatePlanPreview{}, privatePlanError("external_consent")
	}
	var toolAvailability *CodexCLIToolAvailabilityReport
	if runSet.EffectiveKind() == PrivateRunSetKindActivationStudy {
		report, err := qualifyPrivateActivationAgent(ctx, root, material)
		if err != nil {
			return PrivatePlanPreview{}, err
		}
		material.bindToolAvailabilityResult(report)
		toolAvailability = &report
	}
	planID, err := privateRandomID("pln-")
	if err != nil {
		return PrivatePlanPreview{}, privatePlanError("id")
	}
	commit, dirty, err := privateRepositoryIdentity(options.RepositoryRoot)
	if err != nil {
		return PrivatePlanPreview{}, privatePlanError("repository")
	}
	contractDigest := sha256HexBytes([]byte(strings.Join(material.contract, "\x00")))
	inputDigest := sha256HexBytes([]byte(strings.Join(material.inputs, "\x00")))
	kind := runSet.EffectiveKind()
	var studyContract *privateActivationStudyPlanContract
	var activationContract *PrivateActivationStudyContract
	studySeriesSHA256 := ""
	if kind == PrivateRunSetKindActivationStudy {
		paths := make([]string, len(runSet.SpecPaths))
		for index, relative := range runSet.SpecPaths {
			paths[index] = filepath.Join(root, filepath.FromSlash(relative))
		}
		contract, contractErr := ValidatePrivateActivationStudy(paths...)
		if contractErr != nil {
			return PrivatePlanPreview{}, privatePlanError("activation_study")
		}
		activationContract = &contract
		studySeriesSHA256 = privateActivationStudySeriesDigest(contract, material)
		attempt, active, countErr := privateActivationStudyAttemptCount(root, studySeriesSHA256, now)
		if countErr != nil {
			return PrivatePlanPreview{}, countErr
		}
		if active {
			return PrivatePlanPreview{}, privatePlanError("study_active")
		}
		order, orderErr := PrivateActivationStudyOrder(attempt)
		if orderErr != nil {
			return PrivatePlanPreview{}, privatePlanError("study_order")
		}
		ordered := make([]privatePlanItem, 0, len(items))
		cells := make([]PrivateActivationStudyCell, 0, len(items))
		for _, identity := range order {
			for _, item := range items {
				if item.SkillActivation != identity.SkillActivation {
					continue
				}
				treatment, ok := contract.Treatment(item.SkillActivation)
				if !ok {
					return PrivatePlanPreview{}, privatePlanError("study_order")
				}
				cellID, idErr := privateRandomID("cell-")
				if idErr != nil {
					return PrivatePlanPreview{}, privatePlanError("cell_id")
				}
				item.CellID = cellID
				ordered = append(ordered, item)
				cells = append(cells, PrivateActivationStudyCell{CellID: item.CellID, SkillActivation: item.SkillActivation,
					ContractSHA256: treatment.RunSpecSHA256, MaxEstimatedCostMicroUSD: item.MaxEstimatedCostMicroUSD})
				break
			}
		}
		if len(ordered) != len(items) {
			return PrivatePlanPreview{}, privatePlanError("study_order")
		}
		items = ordered
		if material.calibration == nil {
			return PrivatePlanPreview{}, privatePlanError("calibration_contract")
		}
		lifecycleContract, lifecycleErr := NewPrivateActivationStudyPlan(PrivateActivationStudyPlanInput{StudyID: planID,
			TotalAuthorizedMicroUSD: totalAuthorized, ReviewerReserveMicroUSD: runSet.ReviewerReserveMicroUSD,
			Calibration: PrivateActivationCalibrationContract{ContractSHA256: material.calibration.SHA256,
				MaxEstimatedCostMicroUSD: runSet.CalibrationMaxEstimatedCostMicroUSD}, OrderedBalancedRoster: cells})
		if lifecycleErr != nil {
			return PrivatePlanPreview{}, privatePlanError("study_contract")
		}
		studyContract = &lifecycleContract
	} else {
		completedCount, countErr := completedPrivatePlanCount(root, runSet.Alias)
		if countErr != nil {
			return PrivatePlanPreview{}, countErr
		}
		items = rotatePrivatePlanItems(items, completedCount)
	}
	plan := privatePlan{SchemaVersion: PrivatePlanSchemaVersion, PlanID: planID, RunSetAlias: runSet.Alias,
		Kind:           kind,
		ContractSHA256: contractDigest, InputsSHA256: inputDigest, CreatedAt: now.Format(time.RFC3339), Consent: options.Consent,
		RepositoryCommit: commit, RepositoryDirty: dirty, Provider: provider, Model: model,
		MaxEstimatedCostMicroUSD: totalAuthorized, ReviewerReserveMicroUSD: runSet.ReviewerReserveMicroUSD,
		CalibrationMaxEstimatedCostMicroUSD: runSet.CalibrationMaxEstimatedCostMicroUSD,
		QualitativeRequired:                 runSet.QualitativeReviewRequired || runSet.QualitativeReviewPanel != nil,
		QualitativeReviewPanel:              material.qualitativePanel, ToolAvailability: toolAvailability, Items: items}
	if kind == PrivateRunSetKindActivationStudy {
		plan.CostAssurance = PrivateActivationCostAssuranceDetectionOnly
		plan.StudySeriesSHA256 = studySeriesSHA256
		plan.StudyContract = studyContract
		plan.ActivationContract = activationContract
	}
	data, err := encodePrivatePlan(plan)
	if err != nil {
		return PrivatePlanPreview{}, err
	}
	path := filepath.Join(root, "plans", planID+".json")
	if err := safepath.WriteFileExclusiveWithin(root, path, data, 0o600); err != nil {
		return PrivatePlanPreview{}, privatePlanError("write")
	}
	surfaces := privatePlanSurfaces(items)
	preview := PrivatePlanPreview{SchemaVersion: PrivatePlanSchemaVersion, PlanID: planID, PlanSHA256: sha256HexBytes(data), Surfaces: surfaces,
		Provider: provider, Model: model, ExpiresAt: options.Consent.ExpiresAt, MaxEstimatedCostMicroUSD: totalAuthorized, Kind: kind}
	if kind == PrivateRunSetKindActivationStudy {
		preview.Surfaces = []string{SurfaceCLISkill}
		preview.OrderedTreatments = privatePlanTreatments(items)
		preview.CostAssurance = PrivateActivationCostAssuranceDetectionOnly
		preview.CostPreventive = false
		preview.ReviewerReserveMicroUSD = runSet.ReviewerReserveMicroUSD
		preview.CalibrationBound = true
		preview.CalibrationMaxEstimatedCostMicroUSD = runSet.CalibrationMaxEstimatedCostMicroUSD
	}
	return preview, nil
}

func ExecutePrivatePlan(ctx context.Context, options PrivatePlanExecuteOptions) (summary PrivatePlanExecutionSummary, returnErr error) {
	if options.Confirm != PrivatePlanConfirmation || !privatePlanIDRE.MatchString(options.PlanID) || !validSHA256(options.ExpectedPlanSHA256) {
		return PrivatePlanExecutionSummary{}, privatePlanError("approval")
	}
	root, _, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivatePlanExecutionSummary{}, privatePlanError("workspace")
	}
	lock, err := acquirePrivateWorkspaceLock(root)
	if err != nil {
		return PrivatePlanExecutionSummary{}, privatePlanError("workspace_busy")
	}
	defer func() { _ = lock.Unlock() }()
	plan, planData, err := loadPrivatePlan(root, options.PlanID)
	if err != nil || sha256HexBytes(planData) != options.ExpectedPlanSHA256 {
		return PrivatePlanExecutionSummary{}, privatePlanError("plan_hash")
	}
	if plan.SchemaVersion != PrivatePlanSchemaVersion {
		return PrivatePlanExecutionSummary{}, privatePlanError("legacy_plan_read_only")
	}
	fixedNow := !options.Now.IsZero()
	now := options.Now.UTC()
	if !fixedNow {
		now = time.Now().UTC()
	}
	expires, _ := time.Parse(time.RFC3339, plan.Consent.ExpiresAt)
	if !expires.After(now) {
		return PrivatePlanExecutionSummary{}, privatePlanError("expired")
	}
	statePath := filepath.Join(root, "plans", plan.PlanID+".state.json")
	if _, err := os.Lstat(statePath); err == nil || !os.IsNotExist(err) {
		return PrivatePlanExecutionSummary{}, privatePlanError("consumed")
	}
	manifest, runSet, err := loadPrivateManifestRunSet(root, plan.RunSetAlias)
	if err != nil {
		return PrivatePlanExecutionSummary{}, privatePlanError("manifest")
	}
	liveConfig := os.Getenv(manifest.LiveConfigEnv)
	externalProfile := os.Getenv(manifest.ExternalMCPProfileEnv)
	items, material, _, _, _, _, err := buildPrivatePlanMaterial(ctx, root, options.RepositoryRoot, "", runSet,
		options.ATLBinary, options.PluginRoot, options.AgentBinary, options.WrapperExecutable, liveConfig, externalProfile, "")
	if err != nil {
		return PrivatePlanExecutionSummary{}, err
	}
	if plan.Kind == PrivateRunSetKindActivationStudy {
		report, err := qualifyPrivateActivationAgent(ctx, root, material)
		if err != nil {
			return PrivatePlanExecutionSummary{}, err
		}
		if plan.ToolAvailability == nil || !sameCodexToolAvailabilityReport(report, *plan.ToolAvailability) {
			return PrivatePlanExecutionSummary{}, privatePlanError("tool_availability_drift")
		}
	}
	if !privatePlanMaterialMatches(plan, items, material) {
		return PrivatePlanExecutionSummary{}, privatePlanError("input_drift")
	}
	runID, err := privateRandomID("run-")
	if err != nil {
		return PrivatePlanExecutionSummary{}, privatePlanError("id")
	}
	runRoot := filepath.Join(root, "runs", runID)
	installCodexPlugin := false
	for _, item := range plan.Items {
		if item.Surface == SurfaceCLISkill {
			installCodexPlugin = true
			break
		}
	}
	snapshot, err := createPrivateExecutionSnapshot(root, runID, options, runSet, liveConfig, externalProfile, plan.Provider, installCodexPlugin, material.agent)
	if err != nil {
		return PrivatePlanExecutionSummary{}, privatePlanError("execution_snapshot")
	}
	snapshotCleanupPending := true
	defer func() {
		if !snapshotCleanupPending {
			return
		}
		if cleanupErr := privatePlanRemoveTree(root, snapshot.root); cleanupErr != nil {
			returnErr = errors.Join(returnErr, privatePlanError("snapshot_cleanup"), cleanupErr)
			if summary.Status == "completed" {
				summary.Status = "interrupted"
			}
		}
	}()
	snapshotItems, snapshotMaterial, _, _, _, _, err := buildPrivatePlanMaterial(ctx, snapshot.root, options.RepositoryRoot, root, runSet,
		snapshot.atlBinary, snapshot.pluginRoot, snapshot.agentBinary, snapshot.wrapperExecutable, snapshot.liveConfig, snapshot.externalProfile, snapshot.agentProvenanceSHA256)
	if err != nil || !privatePlanMaterialMatches(plan, snapshotItems, snapshotMaterial) {
		return PrivatePlanExecutionSummary{}, privatePlanError("execution_snapshot")
	}
	state := privatePlanState{SchemaVersion: legacyComparisonPrivatePlanStateSchemaVersion, PlanSHA256: options.ExpectedPlanSHA256, RunID: runID, Status: "interrupted", CompletedSurfaces: []string{}}
	var activationLifecycle *PrivateActivationStudyLifecycle
	if plan.Kind == PrivateRunSetKindActivationStudy {
		if plan.StudyContract == nil {
			return PrivatePlanExecutionSummary{}, privatePlanError("study_contract")
		}
		lifecycle, lifecycleErr := NewPrivateActivationStudyLifecycle(*plan.StudyContract)
		if lifecycleErr != nil {
			return PrivatePlanExecutionSummary{}, privatePlanError("study_contract")
		}
		activationLifecycle = &lifecycle
		state.SchemaVersion = privatePlanStateSchemaVersion
		state.CompletedCells = []string{}
		defer func() {
			if summary.PlanID == "" {
				return
			}
			summary.SchemaVersion = 2
			known := privateActivationDetectedCostKnown(state.Events)
			summary.CostKnown = &known
		}()
	}
	stateData, _ := json.MarshalIndent(state, "", "  ")
	stateData = append(stateData, '\n')
	if err := safepath.WriteFileExclusiveWithin(root, statePath, stateData, 0o600); err != nil {
		return PrivatePlanExecutionSummary{}, privatePlanError("state")
	}
	if err := safepath.MkdirAllWithin(root, runRoot, 0o700); err != nil {
		return privatePlanSummary(plan.PlanID, runID, "interrupted", privatePlanExecutionSurfaces(plan), 0, 0), privatePlanError("run_root")
	}
	if err := persistPrivateRunContracts(root, runRoot, snapshot.root, plan, runSet); err != nil {
		return privatePlanSummary(plan.PlanID, runID, "interrupted", privatePlanExecutionSurfaces(plan), 0, 0), privatePlanError("run_contract")
	}
	var providerAuthSession *codexAuthSession
	providerAuthSessionClosed := false
	if plan.Provider == "codex" {
		providerAuthSession, err = newCodexAuthSession(os.Environ())
		if err != nil {
			return privatePlanSummary(plan.PlanID, runID, "interrupted", privatePlanExecutionSurfaces(plan), 0, 0), err
		}
		defer func() {
			if !providerAuthSessionClosed {
				returnErr = errors.Join(returnErr, providerAuthSession.Close())
			}
		}()
	}
	if activationLifecycle != nil {
		if err := persistPrivateActivationPlanState(statePath, plan, &state, activationLifecycle, ""); err != nil {
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), privatePlanExecutionSurfaces(plan), 0, 0), errors.Join(privatePlanError("state"), err)
		}
	}
	var total int64
	if activationLifecycle != nil {
		currentItems, currentMaterial, _, _, _, _, materialErr := buildPrivatePlanMaterial(ctx, root, options.RepositoryRoot, "", runSet,
			options.ATLBinary, options.PluginRoot, options.AgentBinary, options.WrapperExecutable, liveConfig, externalProfile, "")
		snapshotItems, executionMaterial, _, _, _, _, snapshotErr := buildPrivatePlanMaterial(ctx, snapshot.root, options.RepositoryRoot, root, runSet,
			snapshot.atlBinary, snapshot.pluginRoot, snapshot.agentBinary, snapshot.wrapperExecutable, snapshot.liveConfig, snapshot.externalProfile, snapshot.agentProvenanceSHA256)
		if materialErr != nil || !privatePlanMaterialMatches(plan, currentItems, currentMaterial) {
			stopErr := stopAndPersistPrivateActivationState(statePath, plan, &state, activationLifecycle, PrivateActivationStopInputDrift)
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), errors.Join(privatePlanError("input_drift"), stopErr)
		}
		if snapshotErr != nil || !privatePlanMaterialMatches(plan, snapshotItems, executionMaterial) || executionMaterial.calibration == nil ||
			executionMaterial.calibration.SHA256 != plan.StudyContract.Calibration.ContractSHA256 {
			stopErr := stopAndPersistPrivateActivationState(statePath, plan, &state, activationLifecycle, PrivateActivationStopSnapshotDrift)
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), errors.Join(privatePlanError("snapshot_drift"), stopErr)
		}
		if _, err := activationLifecycle.ReserveCalibration(); err != nil {
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), privatePlanError("calibration_lifecycle")
		}
		if err := persistPrivateActivationPlanState(statePath, plan, &state, activationLifecycle, ""); err != nil {
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), privatePlanError("state")
		}
		if err := activationLifecycle.MarkCalibrationLaunched(); err != nil {
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), privatePlanError("calibration_lifecycle")
		}
		if err := persistPrivateActivationPlanState(statePath, plan, &state, activationLifecycle, ""); err != nil {
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), privatePlanError("state")
		}
		calibrationOutputRoot, err := preparePrivateActivationOutputRoot(root, runRoot)
		if err != nil {
			stateErr := markAndPersistPrivateActivationCalibrationFailed(statePath, plan, &state, activationLifecycle, PrivateActivationUnknownInterrupted)
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), errors.Join(privatePlanError("calibration_output"), stateErr)
		}
		providerAttemptCommitted := func() error {
			candidate := *activationLifecycle
			candidate.Events = append([]PrivateActivationStudyEvent(nil), activationLifecycle.Events...)
			if err := candidate.MarkCalibrationProviderAttemptCommitted(); err != nil {
				return err
			}
			if err := persistPrivateActivationPlanState(statePath, plan, &state, &candidate, ""); err != nil {
				return err
			}
			*activationLifecycle = candidate
			return nil
		}
		calibrationReceipt, calibrationErr := privatePlanRunCalibration(ctx, CodexCLICalibrationOptions{
			OutputRoot: calibrationOutputRoot, RepositoryRoot: options.RepositoryRoot,
			AgentBinary: snapshot.agentBinary, ATLBinary: snapshot.atlBinary, PluginRoot: snapshot.pluginRoot,
			WrapperExecutable: snapshot.wrapperExecutable, ScratchRoot: snapshot.providerScratch,
			Model: executionMaterial.calibration.Model, Reasoning: executionMaterial.calibration.Reasoning,
			TimeoutSeconds:           executionMaterial.calibration.TimeoutSeconds,
			MaxEstimatedCostMicroUSD: executionMaterial.calibration.MaxEstimatedCostMicroUSD,
			Pricing:                  executionMaterial.calibration.Pricing, providerAuthSession: providerAuthSession,
			providerAttemptCommitted: providerAttemptCommitted,
		})
		if validatePrivateActivationOutputRoot(root, calibrationOutputRoot) != nil {
			stateErr := markAndPersistPrivateActivationCalibrationFailed(statePath, plan, &state, activationLifecycle, PrivateActivationUnknownContainment)
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), errors.Join(privatePlanError("calibration_output"), stateErr)
		}
		if modeErr := normalizePrivateCandidateTree(root, runRoot); modeErr != nil {
			stateErr := markAndPersistPrivateActivationCalibrationFailed(statePath, plan, &state, activationLifecycle, PrivateActivationUnknownContainment)
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), errors.Join(privatePlanError("run_modes"), stateErr)
		}
		if calibrationErr != nil {
			reason := privateActivationCalibrationPostRunUnknownReason(activationLifecycle)
			stateErr := markAndPersistPrivateActivationCalibrationFailed(statePath, plan, &state, activationLifecycle, reason)
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), errors.Join(privatePlanError("calibration_execution"), calibrationErr, stateErr)
		}
		total += calibrationReceipt.EstimatedCostMicroUSD
		receiptSHA256, receiptErr := persistPrivateActivationCalibrationReceipt(root, runRoot, plan, *executionMaterial.calibration, calibrationReceipt)
		if receiptErr != nil {
			stateErr := markAndPersistPrivateActivationCalibrationFailed(statePath, plan, &state, activationLifecycle, PrivateActivationUnknownPersistence)
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), errors.Join(privatePlanError("calibration_receipt"), receiptErr, stateErr)
		}
		if err := activationLifecycle.RecordCalibrationReceipt(PrivateActivationReceipt{SHA256: receiptSHA256, CostKnown: true,
			DetectedCostMicroUSD: calibrationReceipt.EstimatedCostMicroUSD, ProviderCompleted: true, PersistenceComplete: true, ContainmentCertain: true}); err != nil {
			stateErr := markAndPersistPrivateActivationCalibrationFailed(statePath, plan, &state, activationLifecycle, PrivateActivationUnknownPersistence)
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), errors.Join(privatePlanError("calibration_receipt"), err, stateErr)
		}
		if err := persistPrivateActivationPlanState(statePath, plan, &state, activationLifecycle, ""); err != nil {
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), errors.Join(privatePlanError("state"), err)
		}
		if activationLifecycle.Status() == PrivateActivationStudyStopped || !calibrationReceipt.Passed {
			return privatePlanSummary(plan.PlanID, runID, "stopped", []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), privatePlanError("calibration_failed")
		}
		if err := activationLifecycle.MarkCalibrationSucceeded(); err != nil {
			stateErr := markAndPersistPrivateActivationCalibrationFailed(statePath, plan, &state, activationLifecycle, PrivateActivationUnknownPersistence)
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), errors.Join(privatePlanError("calibration_lifecycle"), err, stateErr)
		}
		if err := persistPrivateActivationPlanState(statePath, plan, &state, activationLifecycle, ""); err != nil {
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, 0, state.EstimatedCostMicroUSD), errors.Join(privatePlanError("state"), err)
		}
	}
	for itemIndex, item := range plan.Items {
		currentItems, currentMaterial, _, _, _, _, materialErr := buildPrivatePlanMaterial(ctx, root, options.RepositoryRoot, "", runSet,
			options.ATLBinary, options.PluginRoot, options.AgentBinary, options.WrapperExecutable, liveConfig, externalProfile, "")
		if materialErr != nil || !privatePlanMaterialMatches(plan, currentItems, currentMaterial) {
			stopErr := stopAndPersistPrivateActivationState(statePath, plan, &state, activationLifecycle, PrivateActivationStopInputDrift)
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), privatePlanExecutionSurfaces(plan), privatePlanCompletedCount(state), state.EstimatedCostMicroUSD), errors.Join(privatePlanError("input_drift"), stopErr)
		}
		snapshotItems, snapshotMaterial, _, _, _, _, snapshotErr := buildPrivatePlanMaterial(ctx, snapshot.root, options.RepositoryRoot, root, runSet,
			snapshot.atlBinary, snapshot.pluginRoot, snapshot.agentBinary, snapshot.wrapperExecutable, snapshot.liveConfig, snapshot.externalProfile, snapshot.agentProvenanceSHA256)
		if snapshotErr != nil || !privatePlanMaterialMatches(plan, snapshotItems, snapshotMaterial) {
			stopErr := stopAndPersistPrivateActivationState(statePath, plan, &state, activationLifecycle, PrivateActivationStopSnapshotDrift)
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), privatePlanExecutionSurfaces(plan), privatePlanCompletedCount(state), state.EstimatedCostMicroUSD), errors.Join(privatePlanError("snapshot_drift"), stopErr)
		}
		specPath := filepath.Join(snapshot.root, filepath.FromSlash(item.SpecPath))
		loadedCell, loadCellErr := loadRunInputs(RunOptions{SpecPath: specPath})
		if loadCellErr != nil {
			stopErr := stopAndPersistPrivateActivationState(statePath, plan, &state, activationLifecycle, PrivateActivationStopCellContract)
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), privatePlanExecutionSurfaces(plan), privatePlanCompletedCount(state), state.EstimatedCostMicroUSD), errors.Join(privatePlanError("study_contract"), stopErr)
		}
		if activationLifecycle != nil {
			cell, reserveErr := activationLifecycle.ReserveNextCell()
			if reserveErr != nil || cell.CellID != item.CellID {
				stopErr := stopAndPersistPrivateActivationState(statePath, plan, &state, activationLifecycle, PrivateActivationStopReservation)
				return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, len(state.CompletedCells), state.EstimatedCostMicroUSD), errors.Join(privatePlanError("study_lifecycle"), reserveErr, stopErr)
			}
			if err := persistPrivateActivationPlanState(statePath, plan, &state, activationLifecycle, ""); err != nil {
				return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, len(state.CompletedCells), state.EstimatedCostMicroUSD), privatePlanError("state")
			}
			if err := activationLifecycle.MarkLaunched(item.CellID); err != nil {
				return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, len(state.CompletedCells), state.EstimatedCostMicroUSD), privatePlanError("study_lifecycle")
			}
			if err := persistPrivateActivationPlanState(statePath, plan, &state, activationLifecycle, ""); err != nil {
				return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, len(state.CompletedCells), state.EstimatedCostMicroUSD), privatePlanError("state")
			}
		}
		itemExternalProfile := ""
		if item.Surface == SurfaceExternalMCP {
			itemExternalProfile = snapshot.externalProfile
		}
		var providerAttemptCommitted func() error
		if activationLifecycle != nil {
			providerAttemptCommitted = func() error {
				candidate := *activationLifecycle
				candidate.Events = append([]PrivateActivationStudyEvent(nil), activationLifecycle.Events...)
				if err := candidate.MarkProviderAttemptCommitted(item.CellID); err != nil {
					return err
				}
				if err := persistPrivateActivationPlanState(statePath, plan, &state, &candidate, ""); err != nil {
					return err
				}
				*activationLifecycle = candidate
				return nil
			}
		}
		output, runErr := privatePlanRunHeadless(ctx, RunOptions{SpecPath: specPath, OutputRoot: filepath.Join(runRoot, "raw"), RepositoryRoot: options.RepositoryRoot,
			AgentBinary: snapshot.agentBinary, ATLBinary: snapshot.atlBinary, PluginRoot: snapshot.pluginRoot, WrapperExecutable: snapshot.wrapperExecutable,
			LiveConfigDir: snapshot.liveConfig, ExternalMCPProfile: itemExternalProfile, ScratchRoot: snapshot.providerScratch, PrivateWorkspaceRoot: root,
			qualifiedAgentVersion: snapshot.agentIdentity, providerAuthSession: providerAuthSession, providerAttemptCommitted: providerAttemptCommitted})
		if modeErr := normalizePrivateCandidateTree(root, runRoot); modeErr != nil {
			reason := privateActivationPostRunUnknownReason(activationLifecycle, PrivateActivationUnknownContainment)
			stateErr := markAndPersistPrivateActivationUnknown(statePath, plan, &state, activationLifecycle, item.CellID, reason)
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), privatePlanExecutionSurfaces(plan), privatePlanCompletedCount(state), state.EstimatedCostMicroUSD), errors.Join(privatePlanError("run_modes"), stateErr)
		}
		if runErr != nil {
			reason := privateActivationPostRunUnknownReason(activationLifecycle, PrivateActivationUnknownProvider)
			stateErr := markAndPersistPrivateActivationUnknown(statePath, plan, &state, activationLifecycle, item.CellID, reason)
			return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), privatePlanExecutionSurfaces(plan), privatePlanCompletedCount(state), state.EstimatedCostMicroUSD), errors.Join(privatePlanError("execution"), runErr, stateErr)
		}
		total += output.EstimatedCostMicroUSDTotal
		if activationLifecycle != nil {
			receiptSHA256, receiptWriteErr := persistPrivateActivationExecutionReceipt(root, runRoot, plan, item, output)
			if receiptWriteErr != nil {
				stateErr := markAndPersistPrivateActivationUnknown(statePath, plan, &state, activationLifecycle, item.CellID, PrivateActivationUnknownPersistence)
				return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, len(state.CompletedCells), state.EstimatedCostMicroUSD), errors.Join(privatePlanError("study_receipt"), receiptWriteErr, stateErr)
			}
			costKnown := len(output.Results) > 0
			for _, result := range output.Results {
				costKnown = costKnown && result.Coverage["estimated_cost_microusd"]
			}
			detectedCost := output.EstimatedCostMicroUSDTotal
			if !costKnown {
				detectedCost = 0
			}
			receiptErr := activationLifecycle.RecordReceipt(item.CellID, PrivateActivationReceipt{SHA256: receiptSHA256,
				CostKnown: costKnown, DetectedCostMicroUSD: detectedCost,
				ProviderCompleted: true, PersistenceComplete: true, ContainmentCertain: true})
			if receiptErr != nil {
				stateErr := markAndPersistPrivateActivationUnknown(statePath, plan, &state, activationLifecycle, item.CellID, PrivateActivationUnknownPersistence)
				return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, len(state.CompletedCells), state.EstimatedCostMicroUSD), errors.Join(privatePlanError("study_receipt"), receiptErr, stateErr)
			}
			// Persist the provider receipt before making any decision that can
			// advance or terminate the roster.
			if err := persistPrivateActivationPlanState(statePath, plan, &state, activationLifecycle, ""); err != nil {
				return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, len(state.CompletedCells), state.EstimatedCostMicroUSD), errors.Join(privatePlanError("state"), err)
			}
			if activationLifecycle.Status() == PrivateActivationStudyStopped {
				return privatePlanSummary(plan.PlanID, runID, "stopped", []string{SurfaceCLISkill}, len(state.CompletedCells), state.EstimatedCostMicroUSD), privatePlanError("study_stopped")
			}
			if output.BudgetExhausted {
				stateErr := markAndPersistPrivateActivationUnknown(statePath, plan, &state, activationLifecycle, item.CellID, PrivateActivationUnknownCostExceeded)
				return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, len(state.CompletedCells), state.EstimatedCostMicroUSD), errors.Join(privatePlanError("study_stopped"), stateErr)
			}
			classification := classifyPrivateActivationResults(output.Results, loadedCell.spec.Checks)
			if classification.Outcome == privateActivationSafetyRejected {
				stateErr := markAndPersistPrivateActivationUnknown(statePath, plan, &state, activationLifecycle, item.CellID, PrivateActivationUnknownContainment)
				return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, len(state.CompletedCells), state.EstimatedCostMicroUSD), errors.Join(privatePlanError("study_safety"), stateErr)
			}
			lastCell := itemIndex == len(plan.Items)-1
			if lastCell && providerAuthSession != nil {
				if err := providerAuthSession.Close(); err != nil {
					stateErr := markAndPersistPrivateActivationUnknown(statePath, plan, &state, activationLifecycle, item.CellID, PrivateActivationUnknownProvider)
					return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, len(state.CompletedCells), state.EstimatedCostMicroUSD), errors.Join(err, stateErr)
				}
				providerAuthSessionClosed = true
			}
			if lastCell {
				if err := privatePlanRemoveTree(root, snapshot.root); err != nil {
					// Keep the receipt-phase state recoverable and leave the
					// deferred retry armed. A terminal state here would strand a
					// credential-bearing execution snapshot outside the recovery
					// command's accepted state set.
					return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, len(state.CompletedCells), state.EstimatedCostMicroUSD), errors.Join(privatePlanError("snapshot_cleanup"), err)
				}
				snapshotCleanupPending = false
			}
			if err := activationLifecycle.MarkDefinitive(item.CellID, classification.Outcome); err != nil {
				stateErr := markAndPersistPrivateActivationUnknown(statePath, plan, &state, activationLifecycle, item.CellID, PrivateActivationUnknownPersistence)
				return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, len(state.CompletedCells), state.EstimatedCostMicroUSD), errors.Join(privatePlanError("study_lifecycle"), err, stateErr)
			}
			completedAt := ""
			if lastCell {
				completedAt = privatePlanCompletionTime(now, fixedNow).Format(time.RFC3339Nano)
			}
			if err := persistPrivateActivationPlanState(statePath, plan, &state, activationLifecycle, completedAt); err != nil {
				return privatePlanSummary(plan.PlanID, runID, privateActivationDurableSummaryStatus(state), []string{SurfaceCLISkill}, len(state.CompletedCells), state.EstimatedCostMicroUSD), errors.Join(privatePlanError("state"), err)
			}
		} else {
			state.CompletedSurfaces = append(state.CompletedSurfaces, item.Surface)
			if err := privatePlanWriteState(statePath, state); err != nil {
				return privatePlanSummary(plan.PlanID, runID, "interrupted", privatePlanExecutionSurfaces(plan), len(state.CompletedSurfaces), total), privatePlanError("state")
			}
		}
	}
	if providerAuthSession != nil && !providerAuthSessionClosed {
		if err := providerAuthSession.Close(); err != nil {
			stopErr := stopAndPersistPrivateActivationState(statePath, plan, &state, activationLifecycle, PrivateActivationStopProviderSession)
			status, cost := "interrupted", total
			if activationLifecycle != nil {
				status, cost = privateActivationDurableSummaryStatus(state), state.EstimatedCostMicroUSD
			}
			return privatePlanSummary(plan.PlanID, runID, status, privatePlanExecutionSurfaces(plan), privatePlanCompletedCount(state), cost), errors.Join(err, stopErr)
		}
		providerAuthSessionClosed = true
	}
	if activationLifecycle != nil {
		return privatePlanSummary(plan.PlanID, runID, "completed", []string{SurfaceCLISkill}, len(plan.Items), total), nil
	}
	if err := privatePlanRemoveTree(root, snapshot.root); err != nil {
		snapshotCleanupPending = false
		return privatePlanSummary(plan.PlanID, runID, "interrupted", privatePlanExecutionSurfaces(plan), len(state.CompletedSurfaces), total), errors.Join(privatePlanError("snapshot_cleanup"), err)
	}
	snapshotCleanupPending = false
	state.Status = "completed"
	state.CompletedAt = privatePlanCompletionTime(now, fixedNow).Format(time.RFC3339Nano)
	if err := privatePlanWriteState(statePath, state); err != nil {
		return PrivatePlanExecutionSummary{}, privatePlanError("state")
	}
	return privatePlanSummary(plan.PlanID, runID, "completed", privatePlanExecutionSurfaces(plan), len(plan.Items), total), nil
}

func preparePrivateActivationOutputRoot(root, runRoot string) (string, error) {
	outputRoot := filepath.Join(runRoot, "raw")
	if err := safepath.MkdirAllWithin(root, outputRoot, 0o700); err != nil {
		return "", err
	}
	marker := filepath.Join(outputRoot, privateOutputRootMarker)
	if err := safepath.WriteFileExclusiveWithin(root, marker, []byte(privateOutputRootMarkerContents), 0o600); err != nil {
		return "", err
	}
	if err := validatePrivateActivationOutputRoot(root, outputRoot); err != nil {
		return "", err
	}
	return outputRoot, nil
}

func validatePrivateActivationOutputRoot(root, outputRoot string) error {
	relative, err := filepath.Rel(root, outputRoot)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return privatePlanError("calibration_output")
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return privatePlanError("calibration_output")
	}
	defer func() { _ = rootHandle.Close() }()
	pathInfo, err := rootHandle.Lstat(relative)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 ||
		(runtime.GOOS != "windows" && pathInfo.Mode().Perm() != 0o700) {
		return privatePlanError("calibration_output")
	}
	outputHandle, err := rootHandle.OpenRoot(relative)
	if err != nil {
		return privatePlanError("calibration_output")
	}
	defer func() { _ = outputHandle.Close() }()
	openedInfo, err := outputHandle.Stat(".")
	if err != nil || !os.SameFile(pathInfo, openedInfo) {
		return privatePlanError("calibration_output")
	}
	markerInfo, err := outputHandle.Lstat(privateOutputRootMarker)
	if err != nil || !markerInfo.Mode().IsRegular() || markerInfo.Mode()&os.ModeSymlink != 0 ||
		(runtime.GOOS != "windows" && markerInfo.Mode().Perm() != 0o600) {
		return privatePlanError("calibration_output")
	}
	data, err := outputHandle.ReadFile(privateOutputRootMarker)
	if err != nil || string(data) != privateOutputRootMarkerContents {
		return privatePlanError("calibration_output")
	}
	finalPathInfo, pathErr := rootHandle.Lstat(relative)
	finalOpenedInfo, openedErr := outputHandle.Stat(".")
	finalMarkerInfo, markerErr := outputHandle.Lstat(privateOutputRootMarker)
	if pathErr != nil || openedErr != nil || markerErr != nil || !os.SameFile(pathInfo, finalPathInfo) ||
		!os.SameFile(pathInfo, finalOpenedInfo) || !os.SameFile(markerInfo, finalMarkerInfo) ||
		!finalPathInfo.IsDir() || !finalMarkerInfo.Mode().IsRegular() ||
		finalPathInfo.Mode()&os.ModeSymlink != 0 || finalMarkerInfo.Mode()&os.ModeSymlink != 0 ||
		(runtime.GOOS != "windows" && (finalPathInfo.Mode().Perm() != 0o700 || finalMarkerInfo.Mode().Perm() != 0o600)) {
		return privatePlanError("calibration_output")
	}
	return nil
}

type privatePlanMaterial struct {
	contract, inputs, series       []string
	agent                          privateAgentBinaryContract
	qualitativePanel               *privateQualitativeReviewPanelContract
	calibration                    *CodexCLICalibrationContract
	toolAvailabilityContractSHA256 string
}

func qualifyPrivateActivationAgent(ctx context.Context, root string, material privatePlanMaterial) (CodexCLIToolAvailabilityReport, error) {
	if material.calibration == nil || material.agent.canonicalPath == "" ||
		!validSHA256(material.toolAvailabilityContractSHA256) {
		return CodexCLIToolAvailabilityReport{}, privatePlanError("tool_availability_contract")
	}
	options := CodexCLIToolAvailabilityOptions{
		AgentBinary: material.agent.canonicalPath,
		ScratchRoot: filepath.Join(root, ".ephemeral"),
		Model:       material.calibration.Model,
		Reasoning:   material.calibration.Reasoning,
	}
	report, err := privatePlanQualifyCodexCLI(ctx, options)
	if err != nil {
		return CodexCLIToolAvailabilityReport{}, privatePlanError("tool_availability_execution")
	}
	if report.Validate() != nil || report.AgentIdentity != material.agent.identity ||
		!constantTimeStringEqual(report.ContractSHA256, material.toolAvailabilityContractSHA256) {
		return CodexCLIToolAvailabilityReport{}, privatePlanError("tool_availability_report")
	}
	if report.Status != CodexCLIToolAvailabilitySupported {
		return CodexCLIToolAvailabilityReport{}, privatePlanError("tool_availability_" + string(report.Status))
	}
	return report, nil
}

func (m *privatePlanMaterial) bindToolAvailabilityResult(report CodexCLIToolAvailabilityReport) {
	result := "tool-availability-result:" + report.ContractSHA256 + ":" + report.ShellTool
	m.contract = append(m.contract, result)
	m.series = append(m.series, result)
}

func sameCodexToolAvailabilityReport(left, right CodexCLIToolAvailabilityReport) bool {
	return left == right
}

func buildPrivatePlanMaterial(_ context.Context, root, repository, trustedWorkspaceRoot string, runSet PrivateWorkspaceRunSet,
	atlBinary, pluginRoot, agentBinary, wrapper, liveConfig, externalProfile, agentProvenanceSHA256 string,
) ([]privatePlanItem, privatePlanMaterial, string, string, int64, bool, error) {
	paths := make([]string, 0, len(runSet.SpecPaths))
	items := make([]privatePlanItem, 0, len(paths))
	material := privatePlanMaterial{}
	var provider, model string
	var maxCost int64
	external := false
	neutral := false
	var activationSpec *RunSpec
	for _, rel := range runSet.SpecPaths {
		path := filepath.Join(root, filepath.FromSlash(rel))
		paths = append(paths, path)
		loaded, err := loadRunInputs(RunOptions{SpecPath: path})
		if err != nil {
			return nil, material, "", "", 0, false, privatePlanError("spec")
		}
		spec, scenario := loaded.spec, loaded.scenario
		if runSet.EffectiveKind() == PrivateRunSetKindActivationStudy && activationSpec == nil {
			copySpec := spec
			activationSpec = &copySpec
		}
		neutral = neutral || scenario.EffectiveCategory() == BenchmarkCategoryNeutralCommon
		if spec.EffectiveBackendMode() != BackendModePrivateLive {
			return nil, material, "", "", 0, false, privatePlanError("spec")
		}
		if err := requirePrivateLiveInputsForWorkspace(path, liveConfig, repository, trustedWorkspaceRoot); err != nil {
			return nil, material, "", "", 0, false, privatePlanError("live_config")
		}
		caseDigest, err := digestTree(filepath.Dir(path))
		if err != nil {
			return nil, material, "", "", 0, false, privatePlanError("case_digest")
		}
		material.contract = append(material.contract, rel, caseDigest, spec.EffectiveSurface(), spec.Provider, spec.Model)
		material.series = append(material.series, "case:"+caseDigest)
		// Bind the complete prompt envelope without retaining prompt contents or a
		// separately visible prompt digest in the plan-level public preview.
		if loaded.promptContractSHA256 != "" {
			material.inputs = append(material.inputs, "prompt-contract:"+loaded.promptContractSHA256)
		}
		cellID := ""
		if runSet.EffectiveKind() == PrivateRunSetKindActivationStudy {
			cellID = spec.SkillActivationIdentity()
		}
		items = append(items, privatePlanItem{CellID: cellID, SpecPath: rel, ScenarioID: scenario.ID, Provider: spec.Provider, Variant: spec.Variant,
			Surface: spec.EffectiveSurface(), SkillActivation: spec.SkillActivationIdentity(), PromptContractSHA256: loaded.promptContractSHA256,
			RubricSHA256: rubricSHA256(loaded.rubric), MaxEstimatedCostMicroUSD: spec.MaxEstimatedCostMicroUSD})
		if provider == "" {
			provider, model = spec.Provider, spec.Model
		}
		maxCost += spec.MaxEstimatedCostMicroUSD
		if spec.EffectiveSurface() == SurfaceExternalMCP {
			external = true
			p := externalProfile
			profile, err := loadExternalMCPProfileForWorkspace(p, repository, trustedWorkspaceRoot)
			if err != nil {
				return nil, material, "", "", 0, false, privatePlanError("external_profile")
			}
			if err := validateExternalMCPProfileForRun(profile, spec, scenario); err != nil {
				return nil, material, "", "", 0, false, privatePlanError("external_profile")
			}
			d, _ := privateFileDigest(p)
			material.inputs = append(material.inputs, "external:"+d)
		}
	}
	if runSet.EffectiveKind() == PrivateRunSetKindActivationStudy {
		if _, err := ValidatePrivateActivationStudy(paths...); err != nil {
			return nil, material, "", "", 0, false, privatePlanError("activation_study")
		}
		if activationSpec == nil {
			return nil, material, "", "", 0, false, privatePlanError("calibration_contract")
		}
		calibration, err := BuildCodexCLICalibrationContract(activationSpec.Model, activationSpec.Reasoning,
			codexCLICalibrationTimeout(activationSpec.TimeoutSeconds), runSet.CalibrationMaxEstimatedCostMicroUSD, activationSpec.Pricing)
		if err != nil {
			return nil, material, "", "", 0, false, privatePlanError("calibration_contract")
		}
		if err := calibration.Validate(); err != nil {
			return nil, material, "", "", 0, false, privatePlanError("calibration_contract")
		}
		material.calibration = &calibration
		material.contract = append(material.contract, "calibration:"+calibration.SHA256)
		material.series = append(material.series, "calibration:"+calibration.SHA256)
	} else if len(paths) > 1 {
		if _, err := ValidatePrivateRunComparisonSet(paths...); err != nil {
			return nil, material, "", "", 0, false, privatePlanError("comparison")
		}
	}
	panel, panelJSON, assignment, err := buildPrivateQualitativePanelMaterial(root, runSet, neutral)
	if err != nil {
		return nil, material, "", "", 0, false, err
	}
	material.qualitativePanel = panel
	if panel != nil {
		material.contract = append(material.contract, "qualitative-panel:"+sha256HexBytes(panelJSON))
		material.series = append(material.series, "qualitative-panel:"+sha256HexBytes(panelJSON))
		if len(assignment) != 0 {
			material.contract = append(material.contract, "blind-assignment:"+sha256HexBytes(assignment))
			material.series = append(material.series, "blind-assignment:"+sha256HexBytes(assignment))
		}
	}
	for _, executable := range []struct{ name, path string }{
		{name: "atl", path: atlBinary},
		{name: "wrapper", path: wrapper},
	} {
		d, err := privateFileDigest(executable.path)
		if err != nil {
			return nil, material, "", "", 0, false, privatePlanError(executable.name + "_binary")
		}
		material.inputs = append(material.inputs, executable.name+":"+d)
	}
	agent, _, err := inspectPrivateAgentBinary(agentBinary, agentProvenanceSHA256)
	if err != nil {
		return nil, material, "", "", 0, false, err
	}
	material.agent = agent
	material.inputs = append(material.inputs,
		"agent-bytes:"+agent.bytesSHA256,
		"agent-provenance:"+agent.provenanceSHA256,
	)
	if agent.resourceRelativePath != "" {
		material.inputs = append(material.inputs,
			"agent-resource-layout:"+agent.resourceRelativePath,
			"agent-resource-bytes:"+agent.resourceBytesSHA256,
		)
	}
	if material.calibration != nil {
		material.toolAvailabilityContractSHA256 = codexToolAvailabilityContractSHA256(agent.identity, CodexCLIToolAvailabilityOptions{
			Model: material.calibration.Model, Reasoning: material.calibration.Reasoning,
		})
		qualification := "tool-availability:" + material.toolAvailabilityContractSHA256
		material.contract = append(material.contract, qualification)
		material.series = append(material.series, qualification)
	}
	pluginManifestPath, pluginSkills, err := providerPluginLayout(pluginRoot, provider)
	if err != nil {
		return nil, material, "", "", 0, false, privatePlanError("plugin")
	}
	pluginDigest, err := digestTree(pluginSkills)
	if err != nil {
		return nil, material, "", "", 0, false, privatePlanError("plugin")
	}
	material.inputs = append(material.inputs, "plugin:"+pluginDigest)
	pluginManifestDigest, err := privateFileDigest(pluginManifestPath)
	if err != nil {
		return nil, material, "", "", 0, false, privatePlanError("plugin_manifest")
	}
	material.inputs = append(material.inputs, "plugin-manifest:"+pluginManifestDigest)
	installCodexPlugin := false
	for _, item := range items {
		if item.Surface == SurfaceCLISkill {
			installCodexPlugin = true
			break
		}
	}
	if provider == "codex" && installCodexPlugin {
		packageDigest, err := digestTree(filepath.Join(pluginRoot, "plugins", "atl"))
		if err != nil {
			return nil, material, "", "", 0, false, privatePlanError("plugin_package")
		}
		material.inputs = append(material.inputs, "plugin-package:"+packageDigest)
		marketplaceDigest, err := privateFileDigest(filepath.Join(pluginRoot, ".agents", "plugins", "marketplace.json"))
		if err != nil {
			return nil, material, "", "", 0, false, privatePlanError("plugin_marketplace")
		}
		material.inputs = append(material.inputs, "plugin-marketplace:"+marketplaceDigest)
	}
	configDigest, err := privateFileDigest(filepath.Join(liveConfig, "config.json"))
	if err != nil {
		return nil, material, "", "", 0, false, privatePlanError("config")
	}
	material.inputs = append(material.inputs, "config:"+configDigest)
	commit, dirty, err := privateRepositoryIdentity(repository)
	if err != nil {
		return nil, material, "", "", 0, false, privatePlanError("repository")
	}
	material.inputs = append(material.inputs, "commit:"+commit, fmt.Sprintf("dirty:%t", dirty))
	return items, material, provider, model, maxCost, external, nil
}

func buildPrivateQualitativePanelMaterial(root string, runSet PrivateWorkspaceRunSet, neutral bool) (*privateQualitativeReviewPanelContract, []byte, []byte, error) {
	if runSet.QualitativeReviewPanel == nil {
		// Legacy-single plans keep their historical review-time assignment
		// workflow. New panel plans bind every judge and any required blind
		// assignment before provider execution.
		return nil, nil, nil, nil
	}
	panel := runSet.QualitativeReviewPanel
	if err := panel.validate(); err != nil {
		return nil, nil, nil, privatePlanError("qualitative_panel")
	}
	var assignment []byte
	if panel.BlindAssignment != "" {
		path := filepath.Join(root, filepath.FromSlash(panel.BlindAssignment))
		data, err := readPrivatePlanLifecycleFile(root, path, maxReviewBytes)
		if err != nil || len(data) == 0 {
			return nil, nil, nil, privatePlanError("blind_assignment")
		}
		assignment = data
	}
	if neutral && len(assignment) == 0 {
		return nil, nil, nil, privatePlanError("blind_assignment")
	}
	contract := &privateQualitativeReviewPanelContract{
		Method: panel.Method, Reviewers: append([]Reviewer(nil), panel.Reviewers...),
		MaxCriterionRangeBPS: panel.MaxCriterionRangeBPS,
	}
	if len(assignment) != 0 {
		contract.BlindAssignmentSHA256 = sha256HexBytes(assignment)
	}
	encoded, err := encodePrivateQualitativeReviewPanelContract(*contract)
	if err != nil {
		return nil, nil, nil, err
	}
	return contract, encoded, assignment, nil
}

func encodePrivateQualitativeReviewPanelContract(contract privateQualitativeReviewPanelContract) ([]byte, error) {
	data, err := json.MarshalIndent(contract, "", "  ")
	if err != nil {
		return nil, privatePlanError("qualitative_panel")
	}
	return append(data, '\n'), nil
}

func loadPrivatePlanWorkspace(root, repository, alias string) (string, PrivateWorkspaceManifest, PrivateWorkspaceRunSet, error) {
	abs, _, err := privateWorkspaceLocations(root, repository, false)
	if err != nil {
		return "", PrivateWorkspaceManifest{}, PrivateWorkspaceRunSet{}, privatePlanError("workspace")
	}
	if _, err := DoctorPrivateWorkspace(abs, repository); err != nil {
		return "", PrivateWorkspaceManifest{}, PrivateWorkspaceRunSet{}, privatePlanError("doctor")
	}
	m, s, err := loadPrivateManifestRunSet(abs, alias)
	return abs, m, s, err
}
func loadPrivateManifestRunSet(root, alias string) (PrivateWorkspaceManifest, PrivateWorkspaceRunSet, error) {
	m, _, err := loadPrivateWorkspaceManifest(root)
	if err != nil {
		return m, PrivateWorkspaceRunSet{}, privatePlanError("manifest")
	}
	for _, s := range m.RunSets {
		if s.Alias == alias {
			return m, s, nil
		}
	}
	return m, PrivateWorkspaceRunSet{}, privatePlanError("run_set")
}
func loadPrivatePlan(root, id string) (privatePlan, []byte, error) {
	if !privatePlanIDRE.MatchString(id) {
		return privatePlan{}, nil, privatePlanError("id")
	}
	data, err := readPrivatePlanLifecycleFile(root, filepath.Join(root, "plans", id+".json"), privatePlanMaxBytes)
	if err != nil {
		return privatePlan{}, nil, privatePlanError("read")
	}
	var p privatePlan
	if err := decodePrivateLifecycleJSON(data, &p); err != nil || validatePrivatePlan(p, id) != nil {
		return privatePlan{}, nil, privatePlanError("decode")
	}
	return p, data, nil
}
func encodePrivatePlan(p privatePlan) ([]byte, error) {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, privatePlanError("encode")
	}
	return append(data, '\n'), nil
}
func writePrivatePlanState(path string, s privatePlanState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(path, append(data, '\n'))
}
func privateRepositoryIdentity(root string) (string, bool, error) {
	c := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	out, err := c.Output()
	if err != nil {
		return "", false, err
	}
	d := exec.Command("git", "-C", root, "status", "--porcelain")
	dirtyOut, err := d.Output()
	return strings.TrimSpace(string(out)), len(dirtyOut) > 0, err
}
func privateFileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func privatePlanSurfaces(items []privatePlanItem) []string {
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, i.Surface)
	}
	return out
}

func privatePlanExecutionSurfaces(plan privatePlan) []string {
	if plan.Kind == PrivateRunSetKindActivationStudy {
		return []string{SurfaceCLISkill}
	}
	return privatePlanSurfaces(plan.Items)
}

func privatePlanCompletedCount(state privatePlanState) int {
	if state.SchemaVersion == legacyActivationPrivatePlanStateSchemaVersion || state.SchemaVersion == privatePlanStateSchemaVersion {
		return len(state.CompletedCells)
	}
	return len(state.CompletedSurfaces)
}

func privateActivationDurableSummaryStatus(state privatePlanState) string {
	if state.Status != "" {
		return state.Status
	}
	return "interrupted"
}

// privateActivationDetectedCostKnown reports whether the durable detected total
// is complete. A provider commitment makes it unknown until a receipt proves
// both completion and cost coverage. Pre-provider interruption is known zero.
func privateActivationDetectedCostKnown(events []PrivateActivationLifecycleEvent) bool {
	known := true
	for _, event := range events {
		switch event.Type {
		case PrivateActivationEventCalibrationProviderCommitted, PrivateActivationEventProviderCommitted:
			known = false
		case PrivateActivationEventCalibrationReceipt, PrivateActivationEventReceipt:
			known = event.ProviderCompleted && event.CostKnown
		}
	}
	return known
}

func privatePlanCompletionTime(now time.Time, fixed bool) time.Time {
	if fixed {
		return now
	}
	return time.Now().UTC()
}

func privatePlanTreatments(items []privatePlanItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.SkillActivation)
	}
	return out
}

func privatePlanCellIDs(items []privatePlanItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.CellID)
	}
	return out
}

func privatePlanItemContractKey(plan privatePlan, item privatePlanItem) string {
	if plan.Kind == PrivateRunSetKindActivationStudy {
		return item.CellID
	}
	return item.Surface
}
func rotatePrivatePlanItems(items []privatePlanItem, n int) []privatePlanItem {
	out := append([]privatePlanItem(nil), items...)
	if len(out) > 1 {
		shift := n % len(out)
		out = append(out[shift:], out[:shift]...)
	}
	return out
}
func completedPrivatePlanCount(root, runSetAlias string) (int, error) {
	entries, err := os.ReadDir(filepath.Join(root, "plans"))
	if err != nil {
		return 0, privatePlanError("plans")
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".state.json") {
			planID := strings.TrimSuffix(e.Name(), ".state.json")
			plan, planData, loadErr := loadPrivatePlan(root, planID)
			if loadErr != nil {
				return 0, privatePlanError("rotation_state")
			}
			data, readErr := readPrivatePlanLifecycleFile(root, filepath.Join(root, "plans", e.Name()), 1<<20)
			if readErr != nil {
				return 0, privatePlanError("rotation_state")
			}
			var s privatePlanState
			if decodePrivateLifecycleJSON(data, &s) != nil || validatePrivatePlanState(s) != nil || s.PlanSHA256 != sha256HexBytes(planData) {
				return 0, privatePlanError("rotation_state")
			}
			if s.Status == "completed" && plan.RunSetAlias == runSetAlias {
				n++
			}
		}
	}
	return n, nil
}

// privateActivationStudyAttemptCount is scoped to the reviewed material rather
// than a mutable manifest alias. An expired plan that never executed does not
// advance the order, so repeatedly allocating or renaming plans cannot select a
// preferred order. Any live pending plan still serializes the series.
func privateActivationStudyAttemptCount(root, seriesSHA256 string, now time.Time) (attempts int, active bool, err error) {
	entries, readErr := os.ReadDir(filepath.Join(root, "plans"))
	if readErr != nil {
		return 0, false, privatePlanError("plans")
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".state.json") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		planID := strings.TrimSuffix(entry.Name(), ".json")
		plan, planData, loadErr := loadPrivatePlan(root, planID)
		if loadErr != nil {
			return 0, false, privatePlanError("study_state")
		}
		if plan.Kind != PrivateRunSetKindActivationStudy || plan.StudySeriesSHA256 != seriesSHA256 {
			continue
		}
		statePath := filepath.Join(root, "plans", planID+".state.json")
		if _, statErr := os.Lstat(statePath); os.IsNotExist(statErr) {
			expires, expiryErr := time.Parse(time.RFC3339, plan.Consent.ExpiresAt)
			if expiryErr != nil {
				return 0, false, privatePlanError("study_state")
			}
			if expires.After(now) {
				active = true
			}
			continue
		} else if statErr != nil {
			return 0, false, privatePlanError("study_state")
		}
		data, stateErr := readPrivatePlanLifecycleFile(root, statePath, 1<<20)
		if stateErr != nil {
			return 0, false, privatePlanError("study_state")
		}
		var state privatePlanState
		if decodePrivateLifecycleJSON(data, &state) != nil || validatePrivatePlanState(state) != nil ||
			state.PlanSHA256 != sha256HexBytes(planData) || validatePrivateActivationPlanState(plan, state) != nil {
			return 0, false, privatePlanError("study_state")
		}
		if state.Status == "running" || state.Status == "interrupted" {
			active = true
		} else if privateActivationAttemptStarted(state.Events) {
			attempts++
		}
	}
	return attempts, active, nil
}

func privateActivationStudySeriesDigest(contract PrivateActivationStudyContract, material privatePlanMaterial) string {
	contractParts := append([]string(nil), material.series...)
	inputParts := append([]string(nil), material.inputs...)
	sort.Strings(contractParts)
	sort.Strings(inputParts)
	return sha256HexBytes([]byte(contract.CommonContractSHA256 + "\x00" + strings.Join(contractParts, "\x00") + "\x00" + strings.Join(inputParts, "\x00")))
}

func privateActivationAttemptStarted(events []PrivateActivationLifecycleEvent) bool {
	for _, event := range events {
		if event.Type == PrivateActivationEventProviderCommitted || event.Type == PrivateActivationEventReceipt {
			return true
		}
	}
	return false
}

func privateActivationPostRunUnknownReason(lifecycle *PrivateActivationStudyLifecycle, committedReason string) string {
	if lifecycle == nil {
		return committedReason
	}
	projection, err := lifecycle.project()
	if err == nil && (projection.activePhase == PrivateActivationEventProviderCommitted || projection.activePhase == PrivateActivationEventReceipt) {
		return committedReason
	}
	return PrivateActivationUnknownInterrupted
}
func samePrivatePlanItemsUnordered(a, b []privatePlanItem) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(x privatePlanItem) string {
		return x.SpecPath + "\x00" + x.ScenarioID + "\x00" + x.Provider + "\x00" + x.Variant + "\x00" + x.Surface + "\x00" + x.SkillActivation + "\x00" + x.PromptContractSHA256 + "\x00" + x.RubricSHA256 + fmt.Sprintf("\x00%d", x.MaxEstimatedCostMicroUSD)
	}
	aa := make([]string, len(a))
	bb := make([]string, len(b))
	for i := range a {
		aa[i] = key(a[i])
	}
	for i := range b {
		bb[i] = key(b[i])
	}
	sort.Strings(aa)
	sort.Strings(bb)
	return strings.Join(aa, "\n") == strings.Join(bb, "\n")
}

func privatePlanMaterialMatches(plan privatePlan, items []privatePlanItem, material privatePlanMaterial) bool {
	if plan.SchemaVersion == PrivatePlanSchemaVersion && plan.Kind == PrivateRunSetKindActivationStudy {
		if plan.ToolAvailability == nil || !plan.ToolAvailability.Supported() {
			return false
		}
		material.bindToolAvailabilityResult(*plan.ToolAvailability)
	}
	seriesMatches := true
	if plan.Kind == PrivateRunSetKindActivationStudy {
		seriesMatches = plan.ActivationContract != nil && privateActivationStudySeriesDigest(*plan.ActivationContract, material) == plan.StudySeriesSHA256
	}
	return seriesMatches && privateQualitativePanelContractsEqual(plan.QualitativeReviewPanel, material.qualitativePanel) &&
		sha256HexBytes([]byte(strings.Join(material.contract, "\x00"))) == plan.ContractSHA256 &&
		sha256HexBytes([]byte(strings.Join(material.inputs, "\x00"))) == plan.InputsSHA256 &&
		samePrivatePlanItemsUnordered(items, plan.Items)
}

func privateQualitativePanelContractsEqual(left, right *privateQualitativeReviewPanelContract) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftData, leftErr := encodePrivateQualitativeReviewPanelContract(*left)
	rightData, rightErr := encodePrivateQualitativeReviewPanelContract(*right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftData, rightData)
}

func markAndPersistPrivateActivationUnknown(statePath string, plan privatePlan, state *privatePlanState,
	lifecycle *PrivateActivationStudyLifecycle, cellID, reason string,
) error {
	if lifecycle == nil {
		return nil
	}
	if err := lifecycle.MarkUnknown(cellID, reason); err != nil {
		return err
	}
	return persistPrivateActivationPlanState(statePath, plan, state, lifecycle, "")
}

func stopAndPersistPrivateActivationState(statePath string, plan privatePlan, state *privatePlanState,
	lifecycle *PrivateActivationStudyLifecycle, reason string,
) error {
	if lifecycle == nil {
		return nil
	}
	projection, err := lifecycle.project()
	if err != nil {
		return err
	}
	if projection.activePhase != "" {
		unknownReason := PrivateActivationUnknownInterrupted
		if reason == PrivateActivationStopCellContract || reason == PrivateActivationStopSnapshotCleanup {
			unknownReason = PrivateActivationUnknownContainment
		}
		if err := lifecycle.MarkUnknown(projection.activeCell, unknownReason); err != nil {
			return err
		}
	} else if err := lifecycle.Stop(reason); err != nil {
		return err
	}
	return persistPrivateActivationPlanState(statePath, plan, state, lifecycle, "")
}

func persistPrivateActivationPlanState(statePath string, plan privatePlan, state *privatePlanState,
	lifecycle *PrivateActivationStudyLifecycle, completedAt string,
) error {
	projection, err := lifecycle.project()
	if err != nil {
		return err
	}
	candidate := *state
	candidate.Events = append([]PrivateActivationLifecycleEvent(nil), lifecycle.Events...)
	candidate.CompletedCells = append([]string(nil), projection.completedCells...)
	candidate.EstimatedCostMicroUSD = projection.detectedCostMicroUSD
	candidate.StopReason = projection.stopReason
	candidate.CompletedAt = ""
	switch projection.status {
	case PrivateActivationStudyPending, PrivateActivationStudyRunning:
		candidate.Status = "running"
	case PrivateActivationStudyStopped:
		candidate.Status = "stopped"
	case PrivateActivationStudyCompleted:
		if completedAt == "" {
			return privatePlanError("completed_at")
		}
		candidate.Status = "completed"
		candidate.CompletedAt = completedAt
	default:
		return privatePlanError("study_state")
	}
	if validatePrivatePlanState(candidate) != nil || validatePrivateActivationPlanState(plan, candidate) != nil {
		return privatePlanError("study_state")
	}
	if err := privatePlanWriteState(statePath, candidate); err != nil {
		return err
	}
	*state = candidate
	return nil
}

func validatePrivateActivationPlanState(plan privatePlan, state privatePlanState) error {
	if plan.Kind != PrivateRunSetKindActivationStudy || plan.StudyContract == nil {
		return privatePlanError("study_state")
	}
	if plan.SchemaVersion == LegacyActivationStudyPrivatePlanSchemaVersion {
		return validateLegacyPrivateActivationPlanState(plan, state)
	}
	if (plan.SchemaVersion != PrivatePlanSchemaVersion && plan.SchemaVersion != LegacyCalibratedPrivatePlanSchemaVersion) || state.SchemaVersion != privatePlanStateSchemaVersion {
		return privatePlanError("study_state")
	}
	lifecycle, err := NewPrivateActivationStudyLifecycle(*plan.StudyContract)
	if err != nil {
		return privatePlanError("study_state")
	}
	lifecycle.Events = append([]PrivateActivationStudyEvent(nil), state.Events...)
	projection, err := lifecycle.project()
	if err != nil || !equalStrings(state.CompletedCells, projection.completedCells) ||
		state.EstimatedCostMicroUSD != projection.detectedCostMicroUSD || state.StopReason != projection.stopReason {
		return privatePlanError("study_state")
	}
	switch state.Status {
	case "interrupted":
		if projection.status != PrivateActivationStudyPending || len(state.Events) != 0 || len(state.CompletedCells) != 0 ||
			state.EstimatedCostMicroUSD != 0 || state.StopReason != "" || state.CompletedAt != "" {
			return privatePlanError("study_state")
		}
	case "running":
		if projection.status != PrivateActivationStudyPending && projection.status != PrivateActivationStudyRunning || state.CompletedAt != "" {
			return privatePlanError("study_state")
		}
	case "stopped":
		if projection.status != PrivateActivationStudyStopped || state.CompletedAt != "" {
			return privatePlanError("study_state")
		}
	case "completed":
		if projection.status != PrivateActivationStudyCompleted || !equalStrings(state.CompletedCells, privatePlanCellIDs(plan.Items)) || state.CompletedAt == "" {
			return privatePlanError("study_state")
		}
	default:
		return privatePlanError("study_state")
	}
	return nil
}

func privatePlanError(code string) error { return fmt.Errorf("%w: %s", ErrPrivatePlanRejected, code) }

func LoadCompletedPrivateRun(root, repository, planID string) (PrivateBaselineSource, error) {
	abs, _, err := privateWorkspaceLocations(root, repository, false)
	if err != nil {
		return PrivateBaselineSource{}, privatePlanError("workspace")
	}
	p, data, err := loadPrivatePlan(abs, planID)
	if err != nil {
		return PrivateBaselineSource{}, err
	}
	if p.Kind == PrivateRunSetKindActivationStudy && p.SchemaVersion != PrivatePlanSchemaVersion {
		return PrivateBaselineSource{}, privatePlanError("legacy_plan_read_only")
	}
	stateData, err := readPrivatePlanLifecycleFile(abs, filepath.Join(abs, "plans", planID+".state.json"), 1<<20)
	if err != nil {
		return PrivateBaselineSource{}, privatePlanError("state")
	}
	var s privatePlanState
	if decodePrivateLifecycleJSON(stateData, &s) != nil || validatePrivatePlanState(s) != nil || s.Status != "completed" ||
		s.PlanSHA256 != sha256HexBytes(data) {
		return PrivateBaselineSource{}, privatePlanError("state")
	}
	if p.Kind == PrivateRunSetKindActivationStudy {
		if validatePrivateActivationPlanState(p, s) != nil || validatePrivateActivationStateEvidence(abs, p, s) != nil {
			return PrivateBaselineSource{}, privatePlanError("state")
		}
	} else if !equalStrings(s.CompletedSurfaces, privatePlanSurfaces(p.Items)) {
		return PrivateBaselineSource{}, privatePlanError("state")
	}
	if pruned, pruneErr := inspectPrivatePrunedRun(abs, s.RunID, p.PlanID); pruneErr != nil || pruned {
		return PrivateBaselineSource{}, privatePlanError("run_pruned")
	}
	source := PrivateBaselineSource{Kind: p.Kind, PlanID: p.PlanID, PlanPath: filepath.Join(abs, "plans", p.PlanID+".json"), PlanSHA256: s.PlanSHA256, ContractSHA256: p.ContractSHA256, RunID: s.RunID, RunRoot: filepath.Join(abs, "runs", s.RunID), Completed: true, Immutable: true}
	for _, i := range p.Items {
		contractKey := privatePlanItemContractKey(p, i)
		surface := PrivateBaselineSurfaceSource{
			Surface: i.Surface, CellID: i.CellID, SkillActivation: i.SkillActivation,
			RunDirectory: filepath.Join(source.RunRoot, "raw", i.ScenarioID, i.Provider, i.Variant, "run-01"),
			RubricPath:   filepath.Join(source.RunRoot, "contracts", contractKey, "rubric.json"), RubricSHA256: i.RubricSHA256,
			QualitativeRequired: p.QualitativeRequired,
		}
		if p.Kind == PrivateRunSetKindActivationStudy {
			receipt, ok := privateActivationReceiptEvent(s.Events, i.CellID)
			if !ok || !receipt.CostKnown || !receipt.ProviderCompleted || !receipt.PersistenceComplete || !receipt.ContainmentCertain ||
				!validSHA256(receipt.ReceiptSHA256) {
				return PrivateBaselineSource{}, privatePlanError("execution_receipt")
			}
			surface.ExecutionReceiptPath = filepath.Join(source.RunRoot, "contracts", contractKey, "execution-receipt.json")
			surface.ExecutionReceiptSHA256 = receipt.ReceiptSHA256
			surface.ExecutionCostMicroUSD = receipt.DetectedCostMicroUSD
		}
		if p.QualitativeReviewPanel != nil {
			contractPath := filepath.Join(source.RunRoot, "contracts", contractKey, "qualitative-panel.json")
			assignmentPath := ""
			if p.QualitativeReviewPanel.BlindAssignmentSHA256 != "" {
				assignmentPath = filepath.Join(source.RunRoot, "contracts", contractKey, "blind-assignment")
			}
			contractDigest, assignmentDigest, err := validatePersistedPrivatePanelMaterials(abs, contractPath, assignmentPath, *p.QualitativeReviewPanel)
			if err != nil {
				return PrivateBaselineSource{}, err
			}
			surface.QualitativePanelContractPath = contractPath
			surface.QualitativePanelContractSHA256 = contractDigest
			surface.BlindAssignmentPath = assignmentPath
			surface.BlindAssignmentSHA256 = assignmentDigest
		}
		source.Surfaces = append(source.Surfaces, surface)
	}
	return source, nil
}

func validatePersistedPrivatePanelMaterials(root, contractPath, assignmentPath string, expected privateQualitativeReviewPanelContract) (string, string, error) {
	contractData, err := readPrivatePlanLifecycleFile(root, contractPath, maxReviewBytes)
	if err != nil {
		return "", "", privatePlanError("panel_contract")
	}
	expectedData, err := encodePrivateQualitativeReviewPanelContract(expected)
	if err != nil || !bytes.Equal(contractData, expectedData) {
		return "", "", privatePlanError("panel_contract")
	}
	assignmentDigest := ""
	if expected.BlindAssignmentSHA256 != "" {
		if assignmentPath == "" {
			return "", "", privatePlanError("blind_assignment")
		}
		assignmentData, readErr := readPrivatePlanLifecycleFile(root, assignmentPath, maxReviewBytes)
		if readErr != nil || len(assignmentData) == 0 || sha256HexBytes(assignmentData) != expected.BlindAssignmentSHA256 {
			return "", "", privatePlanError("blind_assignment")
		}
		assignmentDigest = expected.BlindAssignmentSHA256
	} else if assignmentPath != "" {
		return "", "", privatePlanError("blind_assignment")
	}
	return sha256HexBytes(contractData), assignmentDigest, nil
}
func InspectPrivatePlanRunReferences(root, repository string) ([]PrivatePlanRunReference, error) {
	abs, _, err := privateWorkspaceLocations(root, repository, false)
	if err != nil {
		return nil, privatePlanError("workspace")
	}
	return inspectPrivatePlanRunReferencesAtRoot(abs)
}

func inspectPrivatePlanRunReferencesAtRoot(abs string) ([]PrivatePlanRunReference, error) {
	entries, err := os.ReadDir(filepath.Join(abs, "plans"))
	if err != nil {
		return nil, privatePlanError("plans")
	}
	var out []PrivatePlanRunReference
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".state.json") {
			continue
		}
		statePath := filepath.Join(abs, "plans", e.Name())
		data, err := readPrivatePlanLifecycleFile(abs, statePath, 1<<20)
		if err != nil {
			return nil, privatePlanError("state")
		}
		var s privatePlanState
		if decodePrivateLifecycleJSON(data, &s) != nil || validatePrivatePlanState(s) != nil {
			return nil, privatePlanError("state")
		}
		planID := strings.TrimSuffix(e.Name(), ".state.json")
		plan, planData, err := loadPrivatePlan(abs, planID)
		if err != nil || s.PlanSHA256 != sha256HexBytes(planData) {
			return nil, privatePlanError("state_plan")
		}
		if plan.Kind == PrivateRunSetKindActivationStudy && validatePrivateActivationPlanState(plan, s) != nil {
			return nil, privatePlanError("study_state")
		}
		if s.Status == "completed" {
			if plan.Kind == PrivateRunSetKindActivationStudy {
				if !equalStrings(s.CompletedCells, privatePlanCellIDs(plan.Items)) {
					return nil, privatePlanError("state_cells")
				}
			} else if !equalStrings(s.CompletedSurfaces, privatePlanSurfaces(plan.Items)) {
				return nil, privatePlanError("state_surfaces")
			}
		}
		var state string
		completedOrder := int64(0)
		switch s.Status {
		case "running":
			state = "active"
		case "interrupted":
			state = "incomplete"
		case "stopped":
			state = "incomplete"
		case "completed":
			pruned, pruneErr := inspectPrivatePrunedRun(abs, s.RunID, planID)
			if pruneErr != nil {
				return nil, privatePlanError("pruned_run")
			}
			if pruned {
				state = "pruned"
				break
			}
			completedAt, parseErr := time.Parse(time.RFC3339Nano, s.CompletedAt)
			if parseErr != nil {
				// Compatibility for plans completed before completed_at became part
				// of the private lifecycle state. The immutable plan creation time
				// remains a deterministic, conservative retention order.
				completedAt, parseErr = time.Parse(time.RFC3339, plan.CreatedAt)
			}
			if parseErr != nil || completedAt.UnixNano() < 1 {
				return nil, privatePlanError("completed_at")
			}
			state = "completed"
			completedOrder = completedAt.UnixNano()
		default:
			return nil, privatePlanError("state_status")
		}
		if plan.Kind == PrivateRunSetKindActivationStudy && state != "pruned" {
			if err := validatePrivateActivationStateEvidence(abs, plan, s); err != nil {
				return nil, privatePlanError("study_evidence")
			}
		}
		runRequired := s.Status != "interrupted" || len(s.Events) != 0 || len(s.CompletedSurfaces) != 0 || len(s.CompletedCells) != 0
		out = append(out, PrivatePlanRunReference{RunID: s.RunID, RunSetAlias: plan.RunSetAlias, PlanID: planID,
			State: state, RunRequired: runRequired, CompletedOrder: completedOrder})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RunID < out[j].RunID })
	return out, nil
}

type privatePlanLifecycle struct {
	pendingPlans   int
	activeRuns     int
	incompleteRuns int
	completedRuns  int
	prunedRuns     int
}

func inspectPrivatePlanLifecycleAtRoot(root string) (privatePlanLifecycle, error) {
	entries, err := os.ReadDir(filepath.Join(root, "plans"))
	if err != nil {
		return privatePlanLifecycle{}, privatePlanError("plans")
	}
	plans := map[string]struct{}{}
	states := map[string]struct{}{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return privatePlanLifecycle{}, privatePlanError("plan_entry")
		}
		switch {
		case strings.HasSuffix(name, ".state.json"):
			planID := strings.TrimSuffix(name, ".state.json")
			if !privatePlanIDRE.MatchString(planID) {
				return privatePlanLifecycle{}, privatePlanError("state_name")
			}
			states[planID] = struct{}{}
		case strings.HasSuffix(name, ".json"):
			planID := strings.TrimSuffix(name, ".json")
			if !privatePlanIDRE.MatchString(planID) {
				return privatePlanLifecycle{}, privatePlanError("plan_name")
			}
			if _, _, err := loadPrivatePlan(root, planID); err != nil {
				return privatePlanLifecycle{}, err
			}
			plans[planID] = struct{}{}
		default:
			return privatePlanLifecycle{}, privatePlanError("plan_entry")
		}
	}
	for planID := range states {
		if _, exists := plans[planID]; !exists {
			return privatePlanLifecycle{}, privatePlanError("orphan_state")
		}
	}
	references, err := inspectPrivatePlanRunReferencesAtRoot(root)
	if err != nil {
		return privatePlanLifecycle{}, err
	}
	lifecycle := privatePlanLifecycle{pendingPlans: len(plans) - len(states)}
	referencedRuns := make(map[string]PrivatePlanRunReference, len(references))
	for _, reference := range references {
		referencedRuns[reference.RunID] = reference
		switch reference.State {
		case "active":
			lifecycle.activeRuns++
		case "incomplete":
			lifecycle.incompleteRuns++
		case "completed":
			lifecycle.completedRuns++
		case "pruned":
			lifecycle.prunedRuns++
		}
	}
	runEntries, err := os.ReadDir(filepath.Join(root, "runs"))
	if err != nil {
		return privatePlanLifecycle{}, privatePlanError("runs")
	}
	for _, entry := range runEntries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !privateRunIDRE.MatchString(entry.Name()) {
			return privatePlanLifecycle{}, privatePlanError("run_entry")
		}
		if _, exists := referencedRuns[entry.Name()]; !exists {
			return privatePlanLifecycle{}, privatePlanError("orphan_run")
		}
		delete(referencedRuns, entry.Name())
	}
	for _, reference := range referencedRuns {
		if reference.RunRequired {
			return privatePlanLifecycle{}, privatePlanError("missing_run")
		}
	}
	return lifecycle, nil
}

func inspectPrivatePrunedRun(root, runID, planID string) (bool, error) {
	runRoot := filepath.Join(root, "runs", runID)
	tombstonePath := filepath.Join(runRoot, privatePrunedRunName)
	info, err := safepath.StatWithin(root, tombstonePath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
		return false, privatePlanError("pruned_tombstone")
	}
	entries, err := os.ReadDir(runRoot)
	if err != nil || len(entries) != 1 || entries[0].Name() != privatePrunedRunName {
		return false, privatePlanError("pruned_tree")
	}
	data, err := safepath.ReadFileWithinLimit(root, tombstonePath, 1<<20)
	if err != nil {
		return false, privatePlanError("pruned_tombstone")
	}
	var tombstone privatePrunedRun
	if decodePrivateLifecycleJSON(data, &tombstone) != nil || tombstone.SchemaVersion != 1 ||
		tombstone.RunID != runID || tombstone.PlanID != planID || !validSHA256(tombstone.OriginalTreeSHA256) {
		return false, privatePlanError("pruned_tombstone")
	}
	return true, nil
}

func decodePrivateLifecycleJSON(data []byte, target any) error {
	if len(data) > privatePlanMaxBytes || validateJSONNoDuplicateKeys(data) != nil {
		return privatePlanError("decode")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(new(any)) != io.EOF {
		return privatePlanError("decode")
	}
	return nil
}

func readPrivatePlanLifecycleFile(root, path string, limit int64) ([]byte, error) {
	info, err := safepath.StatWithin(root, path)
	if err != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
		return nil, privatePlanError("file_mode")
	}
	data, err := safepath.ReadFileWithinLimit(root, path, limit)
	if err != nil {
		return nil, privatePlanError("read")
	}
	return data, nil
}

func normalizePrivateCandidateTree(root, runRoot string) error {
	if !privatePathWithin(root, filepath.Join(root, "runs"), runRoot) {
		return privatePlanError("run_containment")
	}
	rootHandle, err := os.OpenRoot(runRoot)
	if err != nil {
		return err
	}
	defer func() { _ = rootHandle.Close() }()
	return filepath.WalkDir(runRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return privatePlanError("run_symlink")
		}
		mode := os.FileMode(0o600)
		if entry.IsDir() {
			mode = 0o700
		} else {
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return privatePlanError("run_file_type")
			}
		}
		relative, err := filepath.Rel(runRoot, path)
		if err != nil {
			return err
		}
		return rootHandle.Chmod(relative, mode)
	})
}

func privatePlanHasActivationShape(schemaVersion int) bool {
	return schemaVersion == PrivatePlanSchemaVersion || schemaVersion == LegacyCalibratedPrivatePlanSchemaVersion ||
		schemaVersion == LegacyActivationStudyPrivatePlanSchemaVersion
}

func validatePrivatePlan(plan privatePlan, expectedID string) error {
	created, createdErr := time.Parse(time.RFC3339Nano, plan.CreatedAt)
	expires, expiryErr := time.Parse(time.RFC3339, plan.Consent.ExpiresAt)
	if (plan.SchemaVersion != PrivatePlanSchemaVersion && plan.SchemaVersion != LegacyCalibratedPrivatePlanSchemaVersion && plan.SchemaVersion != LegacyActivationStudyPrivatePlanSchemaVersion && plan.SchemaVersion != LegacyCompleteActivationPrivatePlanSchemaVersion &&
		plan.SchemaVersion != LegacyPromptBoundPrivatePlanSchemaVersion && plan.SchemaVersion != LegacyPrivatePlanSchemaVersion) || plan.PlanID != expectedID || !privatePlanIDRE.MatchString(plan.PlanID) ||
		!privateWorkspaceAliasRE.MatchString(plan.RunSetAlias) || !validSHA256(plan.ContractSHA256) || !validSHA256(plan.InputsSHA256) ||
		createdErr != nil || expiryErr != nil || !expires.After(created) || expires.After(created.Add(7*24*time.Hour)) ||
		!plan.Consent.ProviderDataApproved || !privateGitCommitRE.MatchString(plan.RepositoryCommit) ||
		(plan.Provider != "codex" && plan.Provider != "claude-code") || strings.TrimSpace(plan.Model) == "" || len(plan.Model) > 256 ||
		plan.MaxEstimatedCostMicroUSD < 1 || plan.MaxEstimatedCostMicroUSD > 100_000_000 || len(plan.Items) < 1 || len(plan.Items) > 4 {
		return privatePlanError("plan")
	}
	activationShape := privatePlanHasActivationShape(plan.SchemaVersion)
	if plan.SchemaVersion != PrivatePlanSchemaVersion && plan.ToolAvailability != nil {
		return privatePlanError("plan")
	}
	if !activationShape {
		if plan.Kind != "" || plan.ReviewerReserveMicroUSD != 0 || plan.CostAssurance != "" || plan.StudySeriesSHA256 != "" || plan.StudyContract != nil || plan.ActivationContract != nil || len(plan.Items) > 3 {
			return privatePlanError("plan")
		}
	} else if plan.Kind != PrivateRunSetKindComparison && plan.Kind != PrivateRunSetKindActivationStudy {
		return privatePlanError("plan_kind")
	}
	if plan.QualitativeReviewPanel != nil {
		if !plan.QualitativeRequired || validatePrivateQualitativeReviewPanelContract(*plan.QualitativeReviewPanel) != nil {
			return privatePlanError("qualitative_panel")
		}
	}
	study := activationShape && plan.Kind == PrivateRunSetKindActivationStudy
	if study {
		commonInvalid := len(plan.Items) != 4 || plan.Provider != "codex" || !plan.QualitativeRequired || plan.QualitativeReviewPanel == nil ||
			plan.CostAssurance != PrivateActivationCostAssuranceDetectionOnly || !validSHA256(plan.StudySeriesSHA256) || plan.StudyContract == nil || plan.ActivationContract == nil ||
			plan.ActivationContract.Validate() != nil || plan.StudyContract.StudyID != plan.PlanID ||
			plan.StudyContract.Cost.TotalAuthorizedMicroUSD != plan.MaxEstimatedCostMicroUSD ||
			plan.StudyContract.Cost.ReviewerReserveMicroUSD != plan.ReviewerReserveMicroUSD ||
			plan.ActivationContract.CommonContractSHA256 == ""
		if commonInvalid {
			return privatePlanError("study_contract")
		}
		if plan.SchemaVersion == PrivatePlanSchemaVersion {
			if plan.StudyContract.Validate() != nil || plan.CalibrationMaxEstimatedCostMicroUSD < 1 ||
				plan.StudyContract.Calibration.MaxEstimatedCostMicroUSD != plan.CalibrationMaxEstimatedCostMicroUSD ||
				plan.ToolAvailability == nil || !plan.ToolAvailability.Supported() {
				return privatePlanError("study_contract")
			}
		} else if plan.SchemaVersion == LegacyCalibratedPrivatePlanSchemaVersion {
			if plan.StudyContract.Validate() != nil || plan.CalibrationMaxEstimatedCostMicroUSD < 1 ||
				plan.StudyContract.Calibration.MaxEstimatedCostMicroUSD != plan.CalibrationMaxEstimatedCostMicroUSD {
				return privatePlanError("study_contract")
			}
		} else if validateLegacyPrivateActivationStudyPlan(*plan.StudyContract) != nil || plan.CalibrationMaxEstimatedCostMicroUSD != 0 {
			return privatePlanError("study_contract")
		}
	} else if activationShape &&
		(plan.ReviewerReserveMicroUSD != 0 || plan.CalibrationMaxEstimatedCostMicroUSD != 0 || plan.CostAssurance != "" || plan.StudySeriesSHA256 != "" || plan.StudyContract != nil || plan.ActivationContract != nil || plan.ToolAvailability != nil || len(plan.Items) > 3) {
		return privatePlanError("plan")
	}
	seenSurfaces := map[string]struct{}{}
	seenCells := map[string]struct{}{}
	for _, item := range plan.Items {
		if !validPrivateWorkspaceSpecPath(item.SpecPath) || validatePathComponentID("scenario id", item.ScenarioID) != nil ||
			(item.Provider != "codex" && item.Provider != "claude-code") || item.Provider != plan.Provider ||
			validatePathComponentID("run variant", item.Variant) != nil || !validRunSurface(item.Surface) || !validSHA256(item.RubricSHA256) ||
			!validPrivatePlanPromptIdentity(plan.SchemaVersion, item) ||
			(activationShape && item.MaxEstimatedCostMicroUSD < 1) {
			return privatePlanError("item")
		}
		if study {
			if !identifierRE.MatchString(item.CellID) || item.Surface != SurfaceCLISkill || privateActivationTreatmentIndex(item.SkillActivation) < 0 {
				return privatePlanError("study_item")
			}
			if _, exists := seenCells[item.CellID]; exists {
				return privatePlanError("study_item")
			}
			seenCells[item.CellID] = struct{}{}
			treatment, ok := plan.ActivationContract.Treatment(item.SkillActivation)
			if !ok || treatment.Variant != item.Variant {
				return privatePlanError("study_item")
			}
		} else {
			if item.CellID != "" || (!activationShape && item.MaxEstimatedCostMicroUSD != 0) {
				return privatePlanError("item")
			}
			if _, exists := seenSurfaces[item.Surface]; exists {
				return privatePlanError("item")
			}
			seenSurfaces[item.Surface] = struct{}{}
		}
		if item.Surface == SurfaceExternalMCP && !plan.Consent.ExternalUpstreamApproved {
			return privatePlanError("external_consent")
		}
	}
	if study {
		for index, item := range plan.Items {
			cell := plan.StudyContract.Cells[index]
			treatment, _ := plan.ActivationContract.Treatment(item.SkillActivation)
			if cell.CellID != item.CellID || cell.SkillActivation != item.SkillActivation || cell.ContractSHA256 != treatment.RunSpecSHA256 ||
				cell.MaxEstimatedCostMicroUSD != item.MaxEstimatedCostMicroUSD {
				return privatePlanError("study_item")
			}
		}
	}
	return nil
}

func validPrivatePlanPromptIdentity(schemaVersion int, item privatePlanItem) bool {
	if schemaVersion == LegacyPrivatePlanSchemaVersion {
		return item.SkillActivation == "" && item.PromptContractSHA256 == ""
	}
	if schemaVersion != LegacyPromptBoundPrivatePlanSchemaVersion && schemaVersion != LegacyCompleteActivationPrivatePlanSchemaVersion && schemaVersion != LegacyActivationStudyPrivatePlanSchemaVersion && schemaVersion != LegacyCalibratedPrivatePlanSchemaVersion && schemaVersion != PrivatePlanSchemaVersion {
		return false
	}
	activationCell := item.Provider == "codex" && item.Surface == SurfaceCLISkill
	if !activationCell {
		return item.SkillActivation == "" && item.PromptContractSHA256 == ""
	}
	if !validSHA256(item.PromptContractSHA256) {
		return false
	}
	if schemaVersion == LegacyPromptBoundPrivatePlanSchemaVersion {
		return item.SkillActivation == SkillActivationImplicit || item.SkillActivation == SkillActivationExplicit
	}
	return item.SkillActivation == SkillActivationImplicit || item.SkillActivation == SkillActivationExplicit ||
		item.SkillActivation == SkillActivationDeveloper || item.SkillActivation == SkillActivationCombined
}

func validatePrivateQualitativeReviewPanelContract(panel privateQualitativeReviewPanelContract) error {
	policy := QualitativePanelPolicy{SchemaVersion: QualitativePanelSchemaVersion, Method: panel.Method,
		ExpectedReviewers: len(panel.Reviewers), MaxCriterionRangeBPS: panel.MaxCriterionRangeBPS}
	if policy.Validate() != nil || (panel.BlindAssignmentSHA256 != "" && !validSHA256(panel.BlindAssignmentSHA256)) {
		return privatePlanError("qualitative_panel")
	}
	seen := map[string]struct{}{}
	for _, reviewer := range panel.Reviewers {
		if !identifierRE.MatchString(reviewer.ID) || reviewer.validate() != nil {
			return privatePlanError("qualitative_panel")
		}
		if _, exists := seen[reviewer.ID]; exists {
			return privatePlanError("qualitative_panel")
		}
		seen[reviewer.ID] = struct{}{}
	}
	return nil
}

func validatePrivatePlanState(state privatePlanState) error {
	if (state.SchemaVersion != legacyComparisonPrivatePlanStateSchemaVersion && state.SchemaVersion != legacyActivationPrivatePlanStateSchemaVersion && state.SchemaVersion != privatePlanStateSchemaVersion) || !validSHA256(state.PlanSHA256) || !privateRunIDRE.MatchString(state.RunID) ||
		(state.Status != "running" && state.Status != "interrupted" && state.Status != "completed" && state.Status != "stopped") ||
		len(state.CompletedSurfaces) > 3 || len(state.CompletedCells) > 4 || state.EstimatedCostMicroUSD < 0 {
		return privatePlanError("state")
	}
	if state.SchemaVersion == legacyComparisonPrivatePlanStateSchemaVersion && (len(state.CompletedCells) != 0 || len(state.Events) != 0 || state.StopReason != "" || state.EstimatedCostMicroUSD != 0 || state.Status == "stopped") {
		return privatePlanError("state")
	}
	if state.SchemaVersion == legacyActivationPrivatePlanStateSchemaVersion && (len(state.CompletedSurfaces) != 0 || len(state.Events) > 20 || (state.Status == "stopped") != (state.StopReason != "")) {
		return privatePlanError("state")
	}
	if state.SchemaVersion == privatePlanStateSchemaVersion && (len(state.CompletedSurfaces) != 0 || len(state.Events) > 26 || (state.Status == "stopped") != (state.StopReason != "")) {
		return privatePlanError("state")
	}
	seen := map[string]struct{}{}
	for _, surface := range state.CompletedSurfaces {
		if !validRunSurface(surface) {
			return privatePlanError("state")
		}
		if _, exists := seen[surface]; exists {
			return privatePlanError("state")
		}
		seen[surface] = struct{}{}
	}
	seenCells := map[string]struct{}{}
	for _, cell := range state.CompletedCells {
		if !identifierRE.MatchString(cell) {
			return privatePlanError("state")
		}
		if _, exists := seenCells[cell]; exists {
			return privatePlanError("state")
		}
		seenCells[cell] = struct{}{}
	}
	if state.CompletedAt != "" {
		if state.Status != "completed" {
			return privatePlanError("state")
		}
		if _, err := time.Parse(time.RFC3339Nano, state.CompletedAt); err != nil {
			return privatePlanError("state")
		}
	}
	return nil
}
