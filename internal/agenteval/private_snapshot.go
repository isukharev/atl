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
	liveConfig, externalProfile                                 string
}

func createPrivateExecutionSnapshot(root, runID string, options PrivatePlanExecuteOptions, runSet PrivateWorkspaceRunSet, liveConfig, externalProfile string) (privateExecutionSnapshot, error) {
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
	snapshot.pluginRoot = filepath.Join(snapshot.root, "plugin")
	if err := safepath.MkdirAllWithin(root, snapshot.pluginRoot, 0o700); err != nil {
		return privateExecutionSnapshot{}, err
	}
	if err := copyWorkspace(filepath.Join(options.PluginRoot, "skills"), filepath.Join(snapshot.pluginRoot, "skills")); err != nil {
		return privateExecutionSnapshot{}, err
	}
	if err := copyWorkspace(filepath.Join(options.PluginRoot, ".claude-plugin"), filepath.Join(snapshot.pluginRoot, ".claude-plugin")); err != nil {
		return privateExecutionSnapshot{}, err
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
		{name: "agent", source: options.AgentBinary, destination: &snapshot.agentBinary},
		{name: "wrapper", source: options.WrapperExecutable, destination: &snapshot.wrapperExecutable},
	} {
		*executable.destination = filepath.Join(binRoot, executable.name)
		if err := copyPrivateSnapshotFile(root, executable.source, *executable.destination, 512<<20, 0o700); err != nil {
			return privateExecutionSnapshot{}, err
		}
	}
	if err := normalizePrivateSnapshotTree(snapshot.root); err != nil {
		return privateExecutionSnapshot{}, err
	}
	failed = false
	return snapshot, nil
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

func persistPrivateRunContracts(root, runRoot, snapshotRoot string, items []privatePlanItem) error {
	for _, item := range items {
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
		destination := filepath.Join(runRoot, "contracts", item.Surface, "rubric.json")
		if err := safepath.MkdirAllWithin(root, filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		if err := safepath.WriteFileExclusiveWithin(root, destination, data, 0o600); err != nil {
			return err
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
