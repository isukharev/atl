package agenteval

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"

	"github.com/isukharev/atl/internal/safepath"
)

type privateExecutionSnapshot struct {
	root, atlBinary, pluginRoot, agentBinary, wrapperExecutable string
	liveConfig, externalProfile, providerScratch                string
	agentProvenanceSHA256                                       string
	agentIdentity                                               string
}

func createPrivateExecutionSnapshot(root, runID string, options PrivatePlanExecuteOptions, runSet PrivateWorkspaceRunSet, liveConfig, externalProfile, provider string, installCodexPlugin bool, reviewedAgent privateAgentBinaryContract) (privateExecutionSnapshot, error) {
	if !privateRunIDRE.MatchString(runID) {
		return privateExecutionSnapshot{}, privatePlanError("snapshot_id")
	}
	snapshot := privateExecutionSnapshot{root: filepath.Join(root, ".ephemeral", "execution-"+runID)}
	if err := safepath.MkdirAllWithin(root, snapshot.root, 0o700); err != nil {
		return privateExecutionSnapshot{}, err
	}
	failed := true
	defer func() {
		if failed {
			_ = removePrivateTree(root, snapshot.root)
		}
	}()
	if err := copyPrivateSelectedCases(root, snapshot.root, runSet.SpecPaths); err != nil {
		return privateExecutionSnapshot{}, err
	}
	if err := copyPrivateBlindAssignment(root, snapshot.root, runSet); err != nil {
		return privateExecutionSnapshot{}, err
	}
	snapshot.pluginRoot = filepath.Join(snapshot.root, "plugin")
	if err := safepath.MkdirAllWithin(root, snapshot.pluginRoot, 0o700); err != nil {
		return privateExecutionSnapshot{}, err
	}
	switch provider {
	case "claude-code":
		if err := copyWorkspace(filepath.Join(options.PluginRoot, "skills"), filepath.Join(snapshot.pluginRoot, "skills")); err != nil {
			return privateExecutionSnapshot{}, err
		}
		if err := copyWorkspace(filepath.Join(options.PluginRoot, ".claude-plugin"), filepath.Join(snapshot.pluginRoot, ".claude-plugin")); err != nil {
			return privateExecutionSnapshot{}, err
		}
	case "codex":
		if installCodexPlugin {
			if err := copyWorkspace(filepath.Join(options.PluginRoot, "plugins", "atl"), filepath.Join(snapshot.pluginRoot, "plugins", "atl")); err != nil {
				return privateExecutionSnapshot{}, err
			}
			marketplaceDestination := filepath.Join(snapshot.pluginRoot, ".agents", "plugins", "marketplace.json")
			if err := safepath.MkdirAllWithin(root, filepath.Dir(marketplaceDestination), 0o700); err != nil {
				return privateExecutionSnapshot{}, err
			}
			if err := copyPrivateSnapshotFile(root, filepath.Join(options.PluginRoot, ".agents", "plugins", "marketplace.json"), marketplaceDestination, 1<<20, 0o600); err != nil {
				return privateExecutionSnapshot{}, err
			}
		} else {
			if err := copyWorkspace(filepath.Join(options.PluginRoot, "plugins", "atl", "skills"), filepath.Join(snapshot.pluginRoot, "plugins", "atl", "skills")); err != nil {
				return privateExecutionSnapshot{}, err
			}
			if err := copyWorkspace(filepath.Join(options.PluginRoot, "plugins", "atl", ".codex-plugin"), filepath.Join(snapshot.pluginRoot, "plugins", "atl", ".codex-plugin")); err != nil {
				return privateExecutionSnapshot{}, err
			}
		}
	default:
		return privateExecutionSnapshot{}, privatePlanError("snapshot_plugin")
	}
	snapshot.liveConfig = filepath.Join(snapshot.root, "live-config")
	if err := safepath.MkdirAllWithin(root, snapshot.liveConfig, 0o700); err != nil {
		return privateExecutionSnapshot{}, err
	}
	for _, name := range []string{"config.json", "credentials.json"} {
		if err := copyPrivateSnapshotFile(root, filepath.Join(liveConfig, name), filepath.Join(snapshot.liveConfig, name), 16<<20, 0o600); err != nil {
			return privateExecutionSnapshot{}, err
		}
	}
	if externalProfile != "" {
		snapshot.externalProfile = filepath.Join(snapshot.root, "external-mcp-profile.json")
		if err := copyPrivateSnapshotFile(root, externalProfile, snapshot.externalProfile, 1<<20, 0o600); err != nil {
			return privateExecutionSnapshot{}, err
		}
	}
	binRoot := filepath.Join(snapshot.root, "bin")
	if err := safepath.MkdirAllWithin(root, binRoot, 0o700); err != nil {
		return privateExecutionSnapshot{}, err
	}
	for _, executable := range []struct {
		name        string
		source      string
		destination *string
	}{
		{name: "atl", source: options.ATLBinary, destination: &snapshot.atlBinary},
		{name: "wrapper", source: options.WrapperExecutable, destination: &snapshot.wrapperExecutable},
	} {
		*executable.destination = filepath.Join(binRoot, executable.name)
		if err := copyPrivateSnapshotFile(root, executable.source, *executable.destination, 512<<20, 0o700); err != nil {
			return privateExecutionSnapshot{}, err
		}
	}
	snapshot.agentBinary = filepath.Join(binRoot, privateAgentSnapshotName(reviewedAgent.canonicalPath))
	if err := copyReviewedPrivateAgent(root, snapshot.root, reviewedAgent, snapshot.agentBinary); err != nil {
		return privateExecutionSnapshot{}, err
	}
	snapshotAgent, _, err := inspectPrivateAgentBinary(snapshot.agentBinary, reviewedAgent.provenanceSHA256)
	if err != nil || snapshotAgent.bytesSHA256 != reviewedAgent.bytesSHA256 || snapshotAgent.identity != reviewedAgent.identity {
		return privateExecutionSnapshot{}, privatePlanError("agent_binary_snapshot")
	}
	snapshot.agentProvenanceSHA256 = reviewedAgent.provenanceSHA256
	snapshot.agentIdentity = reviewedAgent.identity
	snapshot.providerScratch = filepath.Join(snapshot.root, "provider-runtime")
	if err := safepath.MkdirAllWithin(root, snapshot.providerScratch, 0o700); err != nil {
		return privateExecutionSnapshot{}, err
	}
	if err := normalizePrivateSnapshotTree(snapshot.root); err != nil {
		return privateExecutionSnapshot{}, err
	}
	failed = false
	return snapshot, nil
}

func copyReviewedPrivateAgent(root, snapshotRoot string, reviewed privateAgentBinaryContract, destination string) error {
	if reviewed.canonicalPath == "" || !validSHA256(reviewed.bytesSHA256) || !validSHA256(reviewed.provenanceSHA256) || reviewed.identity != "binary-sha256:"+reviewed.bytesSHA256 {
		return privatePlanError("agent_binary_snapshot")
	}
	data, err := readBoundedFile(reviewed.canonicalPath, privateAgentBinaryMaxBytes)
	if err != nil || sha256HexBytes(data) != reviewed.bytesSHA256 {
		return privatePlanError("agent_binary_drift")
	}
	if !privatePathWithin(root, snapshotRoot, destination) {
		return privatePlanError("agent_binary_snapshot")
	}
	if err := writePrivateAgentCopy(destination, data); err != nil {
		return privatePlanError("agent_binary_snapshot")
	}
	return nil
}

func copyPrivateSelectedCases(root, snapshotRoot string, specPaths []string) error {
	var directories []string
	for _, relative := range specPaths {
		if !validPrivateWorkspaceSpecPath(relative) {
			return privatePlanError("snapshot_case")
		}
		directory := filepath.Dir(filepath.Join(root, filepath.FromSlash(relative)))
		covered := false
		for _, existing := range directories {
			if inside, err := pathWithin(existing, directory); err == nil && inside {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		filtered := directories[:0]
		for _, existing := range directories {
			if inside, err := pathWithin(directory, existing); err != nil || !inside {
				filtered = append(filtered, existing)
			}
		}
		directories = append(filtered, directory)
	}
	sort.Strings(directories)
	for _, source := range directories {
		relative, err := filepath.Rel(root, source)
		if err != nil || relative == "." || filepath.IsAbs(relative) {
			return privatePlanError("snapshot_case")
		}
		destination := filepath.Join(snapshotRoot, relative)
		if err := safepath.MkdirAllWithin(root, filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		if err := copyWorkspace(source, destination); err != nil {
			return err
		}
	}
	return nil
}

func copyPrivateBlindAssignment(root, snapshotRoot string, runSet PrivateWorkspaceRunSet) error {
	if runSet.QualitativeReviewPanel == nil || runSet.QualitativeReviewPanel.BlindAssignment == "" {
		return nil
	}
	relative := runSet.QualitativeReviewPanel.BlindAssignment
	if !validPrivateWorkspaceCaseFilePath(relative) {
		return privatePlanError("snapshot_blind_assignment")
	}
	source := filepath.Join(root, filepath.FromSlash(relative))
	data, err := readPrivatePlanLifecycleFile(root, source, maxReviewBytes)
	if err != nil || len(data) == 0 {
		return privatePlanError("snapshot_blind_assignment")
	}
	destination := filepath.Join(snapshotRoot, filepath.FromSlash(relative))
	if _, statErr := safepath.StatWithin(root, destination); statErr == nil {
		existing, readErr := readPrivatePlanLifecycleFile(root, destination, maxReviewBytes)
		if readErr != nil {
			return privatePlanError("snapshot_blind_assignment")
		}
		if !bytes.Equal(existing, data) {
			return privatePlanError("snapshot_blind_assignment")
		}
		return nil
	} else if !os.IsNotExist(statErr) {
		return privatePlanError("snapshot_blind_assignment")
	}
	if err := safepath.MkdirAllWithin(root, filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if err := safepath.WriteFileExclusiveWithin(root, destination, data, 0o600); err != nil {
		return privatePlanError("snapshot_blind_assignment")
	}
	return nil
}

func persistPrivateRunContracts(root, runRoot, snapshotRoot string, plan privatePlan, runSet PrivateWorkspaceRunSet) error {
	var panelData, assignmentData []byte
	if plan.QualitativeReviewPanel != nil {
		var err error
		panelData, err = encodePrivateQualitativeReviewPanelContract(*plan.QualitativeReviewPanel)
		if err != nil {
			return privatePlanError("contract_panel")
		}
		if plan.QualitativeReviewPanel.BlindAssignmentSHA256 != "" {
			if runSet.QualitativeReviewPanel == nil || runSet.QualitativeReviewPanel.BlindAssignment == "" {
				return privatePlanError("contract_assignment")
			}
			assignmentPath := filepath.Join(snapshotRoot, filepath.FromSlash(runSet.QualitativeReviewPanel.BlindAssignment))
			assignmentData, err = readPrivatePlanLifecycleFile(snapshotRoot, assignmentPath, maxReviewBytes)
			if err != nil || len(assignmentData) == 0 || sha256HexBytes(assignmentData) != plan.QualitativeReviewPanel.BlindAssignmentSHA256 {
				return privatePlanError("contract_assignment")
			}
		}
	}
	for _, item := range plan.Items {
		contractKey := privatePlanItemContractKey(plan, item)
		specPath := filepath.Join(snapshotRoot, filepath.FromSlash(item.SpecPath))
		spec, scenario, err := ValidateRunSpecFile(specPath)
		if err != nil || scenario.ID != item.ScenarioID {
			return privatePlanError("contract_spec")
		}
		rubricPath, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(specPath), filepath.FromSlash(spec.QualitativeRubricFile)))
		if err != nil {
			return privatePlanError("contract_rubric")
		}
		inside, err := pathWithin(filepath.Dir(specPath), rubricPath)
		if err != nil || !inside {
			return privatePlanError("contract_rubric")
		}
		data, err := readBoundedFile(rubricPath, maxReviewBytes)
		if err != nil {
			return privatePlanError("contract_rubric")
		}
		rubric, err := DecodeRubric(bytes.NewReader(data))
		if err != nil || rubric.ScenarioID != item.ScenarioID || rubricSHA256(rubric) != item.RubricSHA256 {
			return privatePlanError("contract_rubric")
		}
		destination := filepath.Join(runRoot, "contracts", contractKey, "rubric.json")
		if err := safepath.MkdirAllWithin(root, filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		if err := safepath.WriteFileExclusiveWithin(root, destination, data, 0o600); err != nil {
			return err
		}
		if plan.QualitativeReviewPanel != nil {
			panelDestination := filepath.Join(runRoot, "contracts", contractKey, "qualitative-panel.json")
			if err := safepath.WriteFileExclusiveWithin(root, panelDestination, panelData, 0o600); err != nil {
				return err
			}
			if len(assignmentData) != 0 {
				assignmentDestination := filepath.Join(runRoot, "contracts", contractKey, "blind-assignment")
				if err := safepath.WriteFileExclusiveWithin(root, assignmentDestination, assignmentData, 0o600); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func copyPrivateSnapshotFile(root, source, destination string, limit int64, mode os.FileMode) error {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return privatePlanError("snapshot_file")
	}
	data, err := readBoundedFile(source, limit)
	if err != nil {
		return err
	}
	return safepath.WriteFileExclusiveWithin(root, destination, data, mode)
}

func normalizePrivateSnapshotTree(root string) error {
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = rootHandle.Close() }()
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return privatePlanError("snapshot_symlink")
		}
		mode := os.FileMode(0o600)
		if entry.IsDir() {
			mode = 0o700
		} else {
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return privatePlanError("snapshot_file")
			}
			if filepath.Dir(path) == filepath.Join(root, "bin") {
				mode = 0o700
			}
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return rootHandle.Chmod(relative, mode)
	})
}
