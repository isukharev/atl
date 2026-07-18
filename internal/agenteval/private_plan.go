package agenteval

import (
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
	"sort"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	PrivatePlanSchemaVersion       = 1
	PrivatePlanConsentConfirmation = "CONSENT"
	PrivatePlanConfirmation        = "RUN"
	privatePlanMaxBytes            = 4 << 20
)

var ErrPrivatePlanRejected = errors.New("private plan rejected")

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
	SchemaVersion            int      `json:"schema_version"`
	PlanID                   string   `json:"plan_id"`
	PlanSHA256               string   `json:"plan_sha256"`
	Surfaces                 []string `json:"surfaces"`
	Provider                 string   `json:"provider"`
	Model                    string   `json:"model"`
	ExpiresAt                string   `json:"expires_at"`
	MaxEstimatedCostMicroUSD int64    `json:"max_estimated_cost_microusd"`
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
}

func privatePlanSummary(planID, runID, status string, surfaces []string, completed int, cost int64) PrivatePlanExecutionSummary {
	return PrivatePlanExecutionSummary{SchemaVersion: 1, PlanID: planID, RunID: runID, Status: status,
		Surfaces: surfaces, Completed: completed, EstimatedCostMicroUSD: cost}
}

type privatePlan struct {
	SchemaVersion            int                `json:"schema_version"`
	PlanID                   string             `json:"plan_id"`
	RunSetAlias              string             `json:"run_set_alias"`
	ContractSHA256           string             `json:"contract_sha256"`
	InputsSHA256             string             `json:"inputs_sha256"`
	CreatedAt                string             `json:"created_at"`
	Consent                  PrivatePlanConsent `json:"consent"`
	RepositoryCommit         string             `json:"repository_commit"`
	RepositoryDirty          bool               `json:"repository_dirty"`
	Provider                 string             `json:"provider"`
	Model                    string             `json:"model"`
	MaxEstimatedCostMicroUSD int64              `json:"max_estimated_cost_microusd"`
	QualitativeRequired      bool               `json:"qualitative_required"`
	Items                    []privatePlanItem  `json:"items"`
}

type privatePlanItem struct {
	SpecPath     string `json:"spec_path"`
	ScenarioID   string `json:"scenario_id"`
	Provider     string `json:"provider"`
	Variant      string `json:"variant"`
	Surface      string `json:"surface"`
	RubricSHA256 string `json:"rubric_sha256"`
}

type privatePlanState struct {
	SchemaVersion     int      `json:"schema_version"`
	PlanSHA256        string   `json:"plan_sha256"`
	RunID             string   `json:"run_id"`
	Status            string   `json:"status"`
	CompletedSurfaces []string `json:"completed_surfaces"`
	CompletedAt       string   `json:"completed_at,omitempty"`
}

type PrivatePlanRunReference struct {
	RunID          string
	RunSetAlias    string
	PlanID         string
	State          string
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
	items, material, provider, model, maxCost, external, err := buildPrivatePlanMaterial(ctx, root, options.RepositoryRoot, "", runSet,
		options.ATLBinary, options.PluginRoot, options.AgentBinary, options.WrapperExecutable,
		os.Getenv(manifest.LiveConfigEnv), os.Getenv(manifest.ExternalMCPProfileEnv), "")
	if err != nil {
		return PrivatePlanPreview{}, err
	}
	if maxCost > manifest.Execution.MaxEstimatedCostMicroUSD {
		return PrivatePlanPreview{}, privatePlanError("cost_budget")
	}
	if external && !options.Consent.ExternalUpstreamApproved {
		return PrivatePlanPreview{}, privatePlanError("external_consent")
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
	completedCount, err := completedPrivatePlanCount(root, runSet.Alias)
	if err != nil {
		return PrivatePlanPreview{}, err
	}
	items = rotatePrivatePlanItems(items, completedCount)
	plan := privatePlan{SchemaVersion: PrivatePlanSchemaVersion, PlanID: planID, RunSetAlias: runSet.Alias,
		ContractSHA256: contractDigest, InputsSHA256: inputDigest, CreatedAt: now.Format(time.RFC3339), Consent: options.Consent,
		RepositoryCommit: commit, RepositoryDirty: dirty, Provider: provider, Model: model,
		MaxEstimatedCostMicroUSD: maxCost, QualitativeRequired: runSet.QualitativeReviewRequired, Items: items}
	data, err := encodePrivatePlan(plan)
	if err != nil {
		return PrivatePlanPreview{}, err
	}
	path := filepath.Join(root, "plans", planID+".json")
	if err := safepath.WriteFileExclusiveWithin(root, path, data, 0o600); err != nil {
		return PrivatePlanPreview{}, privatePlanError("write")
	}
	surfaces := privatePlanSurfaces(items)
	return PrivatePlanPreview{SchemaVersion: 1, PlanID: planID, PlanSHA256: sha256HexBytes(data), Surfaces: surfaces,
		Provider: provider, Model: model, ExpiresAt: options.Consent.ExpiresAt, MaxEstimatedCostMicroUSD: maxCost}, nil
}

func ExecutePrivatePlan(ctx context.Context, options PrivatePlanExecuteOptions) (PrivatePlanExecutionSummary, error) {
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
	if !privatePlanMaterialMatches(plan, items, material) {
		return PrivatePlanExecutionSummary{}, privatePlanError("input_drift")
	}
	runID, err := privateRandomID("run-")
	if err != nil {
		return PrivatePlanExecutionSummary{}, privatePlanError("id")
	}
	runRoot := filepath.Join(root, "runs", runID)
	snapshot, err := createPrivateExecutionSnapshot(root, runID, options, runSet, liveConfig, externalProfile, material.agent)
	if err != nil {
		return PrivatePlanExecutionSummary{}, privatePlanError("execution_snapshot")
	}
	defer func() { _ = removePrivateTree(root, snapshot.root) }()
	snapshotItems, snapshotMaterial, _, _, _, _, err := buildPrivatePlanMaterial(ctx, snapshot.root, options.RepositoryRoot, root, runSet,
		snapshot.atlBinary, snapshot.pluginRoot, snapshot.agentBinary, snapshot.wrapperExecutable, snapshot.liveConfig, snapshot.externalProfile, snapshot.agentProvenanceSHA256)
	if err != nil || !privatePlanMaterialMatches(plan, snapshotItems, snapshotMaterial) {
		return PrivatePlanExecutionSummary{}, privatePlanError("execution_snapshot")
	}
	state := privatePlanState{SchemaVersion: 1, PlanSHA256: options.ExpectedPlanSHA256, RunID: runID, Status: "interrupted", CompletedSurfaces: []string{}}
	stateData, _ := json.MarshalIndent(state, "", "  ")
	stateData = append(stateData, '\n')
	if err := safepath.WriteFileExclusiveWithin(root, statePath, stateData, 0o600); err != nil {
		return PrivatePlanExecutionSummary{}, privatePlanError("state")
	}
	if err := safepath.MkdirAllWithin(root, runRoot, 0o700); err != nil {
		return privatePlanSummary(plan.PlanID, runID, "interrupted", privatePlanSurfaces(plan.Items), 0, 0), privatePlanError("run_root")
	}
	if err := persistPrivateRunContracts(root, runRoot, snapshot.root, plan.Items); err != nil {
		return privatePlanSummary(plan.PlanID, runID, "interrupted", privatePlanSurfaces(plan.Items), 0, 0), privatePlanError("run_contract")
	}
	var total int64
	for _, item := range plan.Items {
		currentItems, currentMaterial, _, _, _, _, materialErr := buildPrivatePlanMaterial(ctx, root, options.RepositoryRoot, "", runSet,
			options.ATLBinary, options.PluginRoot, options.AgentBinary, options.WrapperExecutable, liveConfig, externalProfile, "")
		if materialErr != nil || !privatePlanMaterialMatches(plan, currentItems, currentMaterial) {
			return privatePlanSummary(plan.PlanID, runID, "interrupted", privatePlanSurfaces(plan.Items), len(state.CompletedSurfaces), total), privatePlanError("input_drift")
		}
		snapshotItems, snapshotMaterial, _, _, _, _, snapshotErr := buildPrivatePlanMaterial(ctx, snapshot.root, options.RepositoryRoot, root, runSet,
			snapshot.atlBinary, snapshot.pluginRoot, snapshot.agentBinary, snapshot.wrapperExecutable, snapshot.liveConfig, snapshot.externalProfile, snapshot.agentProvenanceSHA256)
		if snapshotErr != nil || !privatePlanMaterialMatches(plan, snapshotItems, snapshotMaterial) {
			return privatePlanSummary(plan.PlanID, runID, "interrupted", privatePlanSurfaces(plan.Items), len(state.CompletedSurfaces), total), privatePlanError("snapshot_drift")
		}
		specPath := filepath.Join(snapshot.root, filepath.FromSlash(item.SpecPath))
		itemExternalProfile := ""
		if item.Surface == SurfaceExternalMCP {
			itemExternalProfile = snapshot.externalProfile
		}
		output, runErr := RunHeadless(ctx, RunOptions{SpecPath: specPath, OutputRoot: filepath.Join(runRoot, "raw"), RepositoryRoot: options.RepositoryRoot,
			AgentBinary: snapshot.agentBinary, ATLBinary: snapshot.atlBinary, PluginRoot: snapshot.pluginRoot, WrapperExecutable: snapshot.wrapperExecutable,
			LiveConfigDir: snapshot.liveConfig, ExternalMCPProfile: itemExternalProfile, ScratchRoot: filepath.Join(root, ".ephemeral"), PrivateWorkspaceRoot: root,
			qualifiedAgentVersion: snapshot.agentIdentity})
		if modeErr := normalizePrivateCandidateTree(root, runRoot); modeErr != nil {
			return privatePlanSummary(plan.PlanID, runID, "interrupted", privatePlanSurfaces(plan.Items), len(state.CompletedSurfaces), total), privatePlanError("run_modes")
		}
		if runErr != nil {
			return privatePlanSummary(plan.PlanID, runID, "interrupted", privatePlanSurfaces(plan.Items), len(state.CompletedSurfaces), total), privatePlanError("execution")
		}
		total += output.EstimatedCostMicroUSDTotal
		state.CompletedSurfaces = append(state.CompletedSurfaces, item.Surface)
		if err := writePrivatePlanState(statePath, state); err != nil {
			return privatePlanSummary(plan.PlanID, runID, "interrupted", privatePlanSurfaces(plan.Items), len(state.CompletedSurfaces), total), privatePlanError("state")
		}
	}
	state.Status = "completed"
	completedAt := time.Now().UTC()
	if fixedNow {
		completedAt = now
	}
	state.CompletedAt = completedAt.Format(time.RFC3339Nano)
	if err := writePrivatePlanState(statePath, state); err != nil {
		return PrivatePlanExecutionSummary{}, privatePlanError("state")
	}
	return privatePlanSummary(plan.PlanID, runID, "completed", privatePlanSurfaces(plan.Items), len(plan.Items), total), nil
}

type privatePlanMaterial struct {
	contract, inputs []string
	agent            privateAgentBinaryContract
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
	for _, rel := range runSet.SpecPaths {
		path := filepath.Join(root, filepath.FromSlash(rel))
		paths = append(paths, path)
		loaded, err := loadRunInputs(RunOptions{SpecPath: path})
		if err != nil {
			return nil, material, "", "", 0, false, privatePlanError("spec")
		}
		spec, scenario := loaded.spec, loaded.scenario
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
		items = append(items, privatePlanItem{SpecPath: rel, ScenarioID: scenario.ID, Provider: spec.Provider, Variant: spec.Variant,
			Surface: spec.EffectiveSurface(), RubricSHA256: rubricSHA256(loaded.rubric)})
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
	if len(paths) > 1 {
		if _, err := ValidatePrivateRunComparisonSet(paths...); err != nil {
			return nil, material, "", "", 0, false, privatePlanError("comparison")
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
	pluginDigest, err := digestTree(filepath.Join(pluginRoot, "skills"))
	if err != nil {
		return nil, material, "", "", 0, false, privatePlanError("plugin")
	}
	material.inputs = append(material.inputs, "plugin:"+pluginDigest)
	pluginManifestDigest, err := privateFileDigest(filepath.Join(pluginRoot, ".claude-plugin", "plugin.json"))
	if err != nil {
		return nil, material, "", "", 0, false, privatePlanError("plugin_manifest")
	}
	material.inputs = append(material.inputs, "plugin-manifest:"+pluginManifestDigest)
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
	f, err := os.Open(filepath.Join(root, PrivateWorkspaceManifestName))
	if err != nil {
		return PrivateWorkspaceManifest{}, PrivateWorkspaceRunSet{}, privatePlanError("manifest")
	}
	defer f.Close()
	m, err := DecodePrivateWorkspaceManifest(f)
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
func samePrivatePlanItemsUnordered(a, b []privatePlanItem) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(x privatePlanItem) string {
		return x.SpecPath + "\x00" + x.ScenarioID + "\x00" + x.Provider + "\x00" + x.Variant + "\x00" + x.Surface + "\x00" + x.RubricSHA256
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
	return sha256HexBytes([]byte(strings.Join(material.contract, "\x00"))) == plan.ContractSHA256 &&
		sha256HexBytes([]byte(strings.Join(material.inputs, "\x00"))) == plan.InputsSHA256 &&
		samePrivatePlanItemsUnordered(items, plan.Items)
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
	stateData, err := readPrivatePlanLifecycleFile(abs, filepath.Join(abs, "plans", planID+".state.json"), 1<<20)
	if err != nil {
		return PrivateBaselineSource{}, privatePlanError("state")
	}
	var s privatePlanState
	if decodePrivateLifecycleJSON(stateData, &s) != nil || validatePrivatePlanState(s) != nil || s.Status != "completed" ||
		s.PlanSHA256 != sha256HexBytes(data) || !equalStrings(s.CompletedSurfaces, privatePlanSurfaces(p.Items)) {
		return PrivateBaselineSource{}, privatePlanError("state")
	}
	if pruned, pruneErr := inspectPrivatePrunedRun(abs, s.RunID, p.PlanID); pruneErr != nil || pruned {
		return PrivateBaselineSource{}, privatePlanError("run_pruned")
	}
	source := PrivateBaselineSource{PlanID: p.PlanID, PlanPath: filepath.Join(abs, "plans", p.PlanID+".json"), PlanSHA256: s.PlanSHA256, ContractSHA256: p.ContractSHA256, RunID: s.RunID, RunRoot: filepath.Join(abs, "runs", s.RunID), Completed: true, Immutable: true}
	for _, i := range p.Items {
		source.Surfaces = append(source.Surfaces, PrivateBaselineSurfaceSource{
			Surface: i.Surface, RunDirectory: filepath.Join(source.RunRoot, "raw", i.ScenarioID, i.Provider, i.Variant, "run-01"),
			RubricPath: filepath.Join(source.RunRoot, "contracts", i.Surface, "rubric.json"), RubricSHA256: i.RubricSHA256,
			QualitativeRequired: p.QualitativeRequired,
		})
	}
	return source, nil
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
		if s.Status == "completed" && !equalStrings(s.CompletedSurfaces, privatePlanSurfaces(plan.Items)) {
			return nil, privatePlanError("state_surfaces")
		}
		var state string
		completedOrder := int64(0)
		switch s.Status {
		case "running":
			state = "active"
		case "interrupted":
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
		out = append(out, PrivatePlanRunReference{RunID: s.RunID, RunSetAlias: plan.RunSetAlias, PlanID: planID, State: state, CompletedOrder: completedOrder})
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
	referencedRuns := make(map[string]struct{}, len(references))
	for _, reference := range references {
		referencedRuns[reference.RunID] = struct{}{}
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
	if len(referencedRuns) != 0 {
		return privatePlanLifecycle{}, privatePlanError("missing_run")
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

func validatePrivatePlan(plan privatePlan, expectedID string) error {
	created, createdErr := time.Parse(time.RFC3339Nano, plan.CreatedAt)
	expires, expiryErr := time.Parse(time.RFC3339, plan.Consent.ExpiresAt)
	if plan.SchemaVersion != PrivatePlanSchemaVersion || plan.PlanID != expectedID || !privatePlanIDRE.MatchString(plan.PlanID) ||
		!privateWorkspaceAliasRE.MatchString(plan.RunSetAlias) || !validSHA256(plan.ContractSHA256) || !validSHA256(plan.InputsSHA256) ||
		createdErr != nil || expiryErr != nil || !expires.After(created) || expires.After(created.Add(7*24*time.Hour)) ||
		!plan.Consent.ProviderDataApproved || !privateGitCommitRE.MatchString(plan.RepositoryCommit) ||
		(plan.Provider != "codex" && plan.Provider != "claude-code") || strings.TrimSpace(plan.Model) == "" || len(plan.Model) > 256 ||
		plan.MaxEstimatedCostMicroUSD < 1 || plan.MaxEstimatedCostMicroUSD > 3*maxRunCostMicroUSD || len(plan.Items) < 1 || len(plan.Items) > 3 {
		return privatePlanError("plan")
	}
	seenSurfaces := map[string]struct{}{}
	for _, item := range plan.Items {
		if !validPrivateWorkspaceSpecPath(item.SpecPath) || validatePathComponentID("scenario id", item.ScenarioID) != nil ||
			(item.Provider != "codex" && item.Provider != "claude-code") || item.Provider != plan.Provider ||
			validatePathComponentID("run variant", item.Variant) != nil || !validRunSurface(item.Surface) || !validSHA256(item.RubricSHA256) {
			return privatePlanError("item")
		}
		if _, exists := seenSurfaces[item.Surface]; exists {
			return privatePlanError("item")
		}
		seenSurfaces[item.Surface] = struct{}{}
		if item.Surface == SurfaceExternalMCP && !plan.Consent.ExternalUpstreamApproved {
			return privatePlanError("external_consent")
		}
	}
	return nil
}

func validatePrivatePlanState(state privatePlanState) error {
	if state.SchemaVersion != 1 || !validSHA256(state.PlanSHA256) || !privateRunIDRE.MatchString(state.RunID) ||
		(state.Status != "running" && state.Status != "interrupted" && state.Status != "completed") || len(state.CompletedSurfaces) > 3 {
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
