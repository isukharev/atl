package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	PrivateWorkspaceMigrationSchemaVersion = 1
	PrivateWorkspaceMigrationConfirmation  = "MIGRATE"
	privateWorkspaceMigrationDomain        = "atl-private-workspace-migration-v1"
	privateWorkspaceMigrationStageName     = "private-workspace-migration-v1.source"
	privateWorkspaceMigrationArchiveName   = "private-workspace-migration-v1-source.json"
	maxPrivateWorkspaceMigrationTreeBytes  = int64(1 << 40)
)

var ErrPrivateWorkspaceMigrationRejected = errors.New("private workspace migration is rejected")

var (
	privateWorkspaceMigrationWrite   = safepath.WriteFileExclusiveWithin
	privateWorkspaceMigrationSync    = safepath.SyncDirectoryWithin
	privateWorkspaceMigrationRename  = safepath.RenameWithin
	privateWorkspaceMigrationRemove  = safepath.RemoveWithin
	privateWorkspaceMigrationInspect = InspectPrivateWorkspace
	privateWorkspaceMigrationGOOS    = runtime.GOOS
)

type PrivateWorkspaceMigrationPreview struct {
	SchemaVersion       int    `json:"schema_version"`
	Status              string `json:"status"`
	FromSchemaVersion   int    `json:"from_schema_version"`
	ToSchemaVersion     int    `json:"to_schema_version"`
	SourceSHA256        string `json:"source_sha256"`
	CandidateSHA256     string `json:"candidate_sha256"`
	MigrationSHA256     string `json:"migration_sha256"`
	PreservedRunSets    int    `json:"preserved_run_sets"`
	PreservedSpecRefs   int    `json:"preserved_spec_references"`
	PreservedRunRecords int    `json:"preserved_run_records"`
}

type PrivateWorkspaceMigrationOptions struct {
	Root                    string
	RepositoryRoot          string
	ExpectedMigrationSHA256 string
	Confirm                 string
}

type PrivateWorkspaceMigrationSummary struct {
	SchemaVersion     int    `json:"schema_version"`
	Status            string `json:"status"`
	FromSchemaVersion int    `json:"from_schema_version"`
	ToSchemaVersion   int    `json:"to_schema_version"`
	MigrationSHA256   string `json:"migration_sha256"`
}

type privateWorkspaceMigrationContract struct {
	Domain            string `json:"domain"`
	SchemaVersion     int    `json:"schema_version"`
	FromSchemaVersion int    `json:"from_schema_version"`
	ToSchemaVersion   int    `json:"to_schema_version"`
	SourceName        string `json:"source_name"`
	CandidateName     string `json:"candidate_name"`
	SourceSHA256      string `json:"source_sha256"`
	CandidateSHA256   string `json:"candidate_sha256"`
}

type privateWorkspaceMigrationMaterial struct {
	root            string
	rootInfo        os.FileInfo
	legacyPath      string
	currentPath     string
	stagePath       string
	archivePath     string
	sourcePath      string
	sourceData      []byte
	sourceInfo      os.FileInfo
	candidateInfo   os.FileInfo
	candidateData   []byte
	recoverable     bool
	sourceLegacy    bool
	staged          bool
	archived        bool
	duplicateLegacy bool
	manifest        PrivateWorkspaceManifest
	protectedTree   map[string]privateWorkspaceMigrationTreeEntry
	preview         PrivateWorkspaceMigrationPreview
}

type privateWorkspaceMigrationTreeEntry struct {
	info   os.FileInfo
	digest string
}

func PreviewPrivateWorkspaceMigration(root, repositoryRoot string) (PrivateWorkspaceMigrationPreview, error) {
	absRoot, absRepository, err := privateWorkspaceLocations(root, repositoryRoot, false)
	if err != nil {
		return PrivateWorkspaceMigrationPreview{}, privateWorkspaceMigrationError("workspace")
	}
	lock, err := acquirePrivateWorkspaceLock(absRoot)
	if err != nil {
		return PrivateWorkspaceMigrationPreview{}, privateWorkspaceMigrationError("workspace_busy")
	}
	defer func() { _ = lock.Unlock() }()
	material, err := loadPrivateWorkspaceMigration(absRoot, absRepository, false)
	if err != nil {
		return PrivateWorkspaceMigrationPreview{}, err
	}
	return material.preview, nil
}

func ApplyPrivateWorkspaceMigration(options PrivateWorkspaceMigrationOptions) (PrivateWorkspaceMigrationSummary, error) {
	if options.Confirm != PrivateWorkspaceMigrationConfirmation || !validSHA256(options.ExpectedMigrationSHA256) {
		return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("confirmation")
	}
	if privateWorkspaceMigrationGOOS == "windows" {
		return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("platform_durability")
	}
	absRoot, absRepository, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("workspace")
	}
	lock, err := acquirePrivateWorkspaceLock(absRoot)
	if err != nil {
		return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("workspace_busy")
	}
	defer func() { _ = lock.Unlock() }()
	material, err := loadPrivateWorkspaceMigration(absRoot, absRepository, true)
	if err != nil {
		return PrivateWorkspaceMigrationSummary{}, err
	}
	if !constantTimeStringEqual(material.preview.MigrationSHA256, options.ExpectedMigrationSHA256) {
		return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("reviewed_digest")
	}
	status := "migrated"
	if material.recoverable {
		status = "recovered"
	}
	if !material.recoverable {
		if err := privateWorkspaceMigrationWrite(material.root, material.currentPath, material.candidateData, 0o600); err != nil {
			return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("candidate_write")
		}
		candidateInfo, err := os.Lstat(material.currentPath)
		if err != nil {
			return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("candidate_verify")
		}
		material.candidateInfo = candidateInfo
		material.recoverable = true
	}
	if err := privateWorkspaceMigrationSync(material.root, material.root); err != nil {
		return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("candidate_durability")
	}
	if err := revalidatePrivateWorkspaceMigration(material, absRepository); err != nil {
		return PrivateWorkspaceMigrationSummary{}, err
	}
	if !material.archived {
		if err := privateWorkspaceMigrationWrite(material.root, material.archivePath, material.sourceData, 0o600); err != nil {
			return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("source_archive")
		}
		material.archived = true
	}
	if err := privateWorkspaceMigrationSync(material.root, filepath.Dir(material.archivePath)); err != nil {
		return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("source_archive_durability")
	}
	archiveData, err := safepath.ReadFileWithinLimit(material.root, material.archivePath, maxPrivateWorkspaceManifestBytes)
	if err != nil || !bytes.Equal(archiveData, material.sourceData) {
		return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("source_archive_changed")
	}
	if err := revalidatePrivateWorkspaceMigration(material, absRepository); err != nil {
		return PrivateWorkspaceMigrationSummary{}, err
	}
	if material.duplicateLegacy {
		if err := privateWorkspaceMigrationRemove(material.root, material.legacyPath); err != nil {
			return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("source_stage")
		}
		if err := privateWorkspaceMigrationSync(material.root, material.root); err != nil {
			return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("source_stage_durability")
		}
		material.duplicateLegacy = false
		if err := revalidatePrivateWorkspaceMigration(material, absRepository); err != nil {
			return PrivateWorkspaceMigrationSummary{}, err
		}
	}
	if material.sourceLegacy {
		before, err := os.Lstat(material.legacyPath)
		if err != nil || !os.SameFile(before, material.sourceInfo) {
			return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("source_changed")
		}
		if err := privateWorkspaceMigrationRename(material.root, material.legacyPath, material.stagePath); err != nil {
			return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("source_stage")
		}
		if err := privateWorkspaceMigrationSync(material.root, filepath.Dir(material.stagePath)); err != nil {
			return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("source_stage_durability")
		}
		if err := privateWorkspaceMigrationSync(material.root, material.root); err != nil {
			return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("source_stage_durability")
		}
		material.sourcePath = material.stagePath
		material.sourceLegacy = false
		material.staged = true
	}
	if err := revalidatePrivateWorkspaceMigration(material, absRepository); err != nil {
		return PrivateWorkspaceMigrationSummary{}, err
	}
	if material.staged {
		if err := privateWorkspaceMigrationRemove(material.root, material.stagePath); err != nil {
			return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("source_remove")
		}
		if err := privateWorkspaceMigrationSync(material.root, filepath.Dir(material.stagePath)); err != nil {
			return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("source_remove_durability")
		}
	}
	archiveInfo, err := os.Lstat(material.archivePath)
	if err != nil {
		return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("source_archive_changed")
	}
	material.sourcePath = material.archivePath
	material.sourceInfo = archiveInfo
	material.sourceLegacy = false
	material.staged = false
	material.archived = true
	if err := revalidatePrivateWorkspaceMigration(material, absRepository); err != nil {
		return PrivateWorkspaceMigrationSummary{}, err
	}
	report := privateWorkspaceMigrationInspect(material.root, absRepository)
	if !report.Healthy {
		return PrivateWorkspaceMigrationSummary{}, privateWorkspaceMigrationError("postcondition")
	}
	if err := revalidatePrivateWorkspaceMigration(material, absRepository); err != nil {
		return PrivateWorkspaceMigrationSummary{}, err
	}
	return PrivateWorkspaceMigrationSummary{SchemaVersion: PrivateWorkspaceMigrationSchemaVersion, Status: status,
		FromSchemaVersion: LegacyCalibratedWorkspaceSchemaVersion, ToSchemaVersion: PrivateWorkspaceSchemaVersion,
		MigrationSHA256: material.preview.MigrationSHA256}, nil
}

func loadPrivateWorkspaceMigration(root, repository string, allowRecoverable bool) (privateWorkspaceMigrationMaterial, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return privateWorkspaceMigrationMaterial{}, privateWorkspaceMigrationError("workspace")
	}
	legacyPath := filepath.Join(root, LegacyCalibratedWorkspaceManifestName)
	currentPath := filepath.Join(root, PrivateWorkspaceManifestName)
	stagePath := filepath.Join(root, ".ephemeral", privateWorkspaceMigrationStageName)
	archivePath := filepath.Join(root, "reports", privateWorkspaceMigrationArchiveName)
	v1Path := filepath.Join(root, LegacyPrivateWorkspaceManifestName)
	v2Path := filepath.Join(root, LegacyActivationWorkspaceManifestName)
	legacyExists, err := privateWorkspaceMigrationRegularFile(legacyPath)
	if err != nil {
		return privateWorkspaceMigrationMaterial{}, err
	}
	currentExists, err := privateWorkspaceMigrationRegularFile(currentPath)
	if err != nil {
		return privateWorkspaceMigrationMaterial{}, err
	}
	v1Exists, err := privateWorkspaceMigrationRegularFile(v1Path)
	if err != nil {
		return privateWorkspaceMigrationMaterial{}, err
	}
	v2Exists, err := privateWorkspaceMigrationRegularFile(v2Path)
	if err != nil {
		return privateWorkspaceMigrationMaterial{}, err
	}
	stageExists, err := privateWorkspaceMigrationRegularFile(stagePath)
	if err != nil {
		return privateWorkspaceMigrationMaterial{}, err
	}
	archiveExists, err := privateWorkspaceMigrationRegularFile(archivePath)
	if err != nil {
		return privateWorkspaceMigrationMaterial{}, err
	}
	normal := legacyExists && !currentExists && !stageExists && !archiveExists
	dualRecovery := legacyExists && currentExists && !stageExists
	stagedRecovery := !legacyExists && currentExists && stageExists
	archivedRecovery := !legacyExists && currentExists && archiveExists
	duplicateStageRecovery := legacyExists && currentExists && stageExists && archiveExists
	if v1Exists || v2Exists || (!normal && (!allowRecoverable || (!dualRecovery && !stagedRecovery && !archivedRecovery && !duplicateStageRecovery))) {
		return privateWorkspaceMigrationMaterial{}, privateWorkspaceMigrationError("unsupported_state")
	}
	sourcePath := legacyPath
	if stagedRecovery || duplicateStageRecovery {
		sourcePath = stagePath
	} else if archivedRecovery {
		sourcePath = archivePath
	}
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil {
		return privateWorkspaceMigrationMaterial{}, privateWorkspaceMigrationError("source_read")
	}
	sourceData, err := safepath.ReadFileWithinLimit(root, sourcePath, maxPrivateWorkspaceManifestBytes)
	if err != nil {
		return privateWorkspaceMigrationMaterial{}, privateWorkspaceMigrationError("source_read")
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(sourceData))
	if err != nil || manifest.SchemaVersion != LegacyCalibratedWorkspaceSchemaVersion {
		return privateWorkspaceMigrationMaterial{}, privateWorkspaceMigrationError("source_invalid")
	}
	if duplicateStageRecovery {
		legacyInfo, statErr := os.Lstat(legacyPath)
		legacyData, readErr := safepath.ReadFileWithinLimit(root, legacyPath, maxPrivateWorkspaceManifestBytes)
		if statErr != nil || readErr != nil || !os.SameFile(legacyInfo, sourceInfo) || !bytes.Equal(legacyData, sourceData) {
			return privateWorkspaceMigrationMaterial{}, privateWorkspaceMigrationError("unsupported_state")
		}
	}
	candidate := manifest
	candidate.SchemaVersion = PrivateWorkspaceSchemaVersion
	candidateData, err := EncodePrivateWorkspaceManifest(candidate)
	if err != nil {
		return privateWorkspaceMigrationMaterial{}, privateWorkspaceMigrationError("candidate_invalid")
	}
	if currentExists {
		currentData, readErr := safepath.ReadFileWithinLimit(root, currentPath, maxPrivateWorkspaceManifestBytes)
		if readErr != nil || !bytes.Equal(currentData, candidateData) {
			return privateWorkspaceMigrationMaterial{}, privateWorkspaceMigrationError("ambiguous_candidate")
		}
		current, decodeErr := DecodePrivateWorkspaceManifest(bytes.NewReader(currentData))
		if decodeErr != nil || current.SchemaVersion != PrivateWorkspaceSchemaVersion {
			return privateWorkspaceMigrationMaterial{}, privateWorkspaceMigrationError("ambiguous_candidate")
		}
	}
	var candidateInfo os.FileInfo
	if currentExists {
		candidateInfo, err = os.Lstat(currentPath)
		if err != nil {
			return privateWorkspaceMigrationMaterial{}, privateWorkspaceMigrationError("ambiguous_candidate")
		}
	}
	counts, err := validatePrivateWorkspaceMigrationHealth(root, repository, manifest, stageExists)
	if err != nil {
		return privateWorkspaceMigrationMaterial{}, err
	}
	protectedTree, err := snapshotPrivateWorkspaceMigrationTree(root)
	if err != nil {
		return privateWorkspaceMigrationMaterial{}, err
	}
	contract := privateWorkspaceMigrationContract{Domain: privateWorkspaceMigrationDomain, SchemaVersion: PrivateWorkspaceMigrationSchemaVersion,
		FromSchemaVersion: LegacyCalibratedWorkspaceSchemaVersion, ToSchemaVersion: PrivateWorkspaceSchemaVersion,
		SourceName: LegacyCalibratedWorkspaceManifestName, CandidateName: PrivateWorkspaceManifestName,
		SourceSHA256: sha256HexBytes(sourceData), CandidateSHA256: sha256HexBytes(candidateData)}
	contractData, err := json.Marshal(contract)
	if err != nil {
		return privateWorkspaceMigrationMaterial{}, privateWorkspaceMigrationError("contract")
	}
	status := "ready"
	if currentExists {
		status = "recoverable"
	}
	preview := PrivateWorkspaceMigrationPreview{SchemaVersion: PrivateWorkspaceMigrationSchemaVersion, Status: status,
		FromSchemaVersion: contract.FromSchemaVersion, ToSchemaVersion: contract.ToSchemaVersion,
		SourceSHA256: contract.SourceSHA256, CandidateSHA256: contract.CandidateSHA256,
		MigrationSHA256: sha256HexBytes(contractData), PreservedRunSets: counts.RunSets,
		PreservedSpecRefs: counts.SpecReferences, PreservedRunRecords: counts.IncompleteRuns + counts.CompletedRuns + counts.PrunedRuns}
	return privateWorkspaceMigrationMaterial{root: root, rootInfo: rootInfo, legacyPath: legacyPath, currentPath: currentPath,
		stagePath: stagePath, archivePath: archivePath, sourcePath: sourcePath, sourceData: sourceData, sourceInfo: sourceInfo,
		candidateInfo: candidateInfo, candidateData: candidateData, recoverable: currentExists,
		sourceLegacy: legacyExists && !duplicateStageRecovery, staged: stageExists, duplicateLegacy: duplicateStageRecovery,
		archived: archiveExists, manifest: manifest, protectedTree: protectedTree, preview: preview}, nil
}

func validatePrivateWorkspaceMigrationHealth(root, repository string, manifest PrivateWorkspaceManifest, allowStage bool) (PrivateWorkspaceCounts, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || !privateWorkspaceDirectoryMode(rootInfo.Mode()) ||
		!privateWorkspaceRootMarkerOK(root) || privateWorkspaceGitBoundary(root, repository, true) != nil || !privateWorkspaceLayoutOK(root) {
		return PrivateWorkspaceCounts{}, privateWorkspaceMigrationError("workspace_unhealthy")
	}
	modeOK, symlinkOK := inspectPrivateWorkspaceTree(root)
	contained, specsOK, validSpecs := inspectPrivateWorkspaceSpecs(root, manifest)
	if !modeOK || !symlinkOK || !contained || !specsOK || !inspectPrivateWorkspaceMigrationScratch(root, allowStage) {
		return PrivateWorkspaceCounts{}, privateWorkspaceMigrationError("workspace_unhealthy")
	}
	lifecycle, err := inspectPrivatePlanLifecycleAtRoot(root)
	if err != nil {
		return PrivateWorkspaceCounts{}, privateWorkspaceMigrationError("lifecycle")
	}
	if lifecycle.pendingPlans != 0 || lifecycle.activeRuns != 0 {
		return PrivateWorkspaceCounts{}, privateWorkspaceMigrationError("lifecycle_busy")
	}
	counts := PrivateWorkspaceCounts{RunSets: len(manifest.RunSets), ValidSpecs: validSpecs,
		PendingPlans: lifecycle.pendingPlans, ActiveRuns: lifecycle.activeRuns, IncompleteRuns: lifecycle.incompleteRuns,
		CompletedRuns: lifecycle.completedRuns, PrunedRuns: lifecycle.prunedRuns}
	for _, runSet := range manifest.RunSets {
		counts.SpecReferences += len(runSet.SpecPaths)
		if runSet.EffectiveKind() == PrivateRunSetKindActivationStudy {
			counts.ActivationStudies++
		}
	}
	return counts, nil
}

func revalidatePrivateWorkspaceMigration(material privateWorkspaceMigrationMaterial, repository string) error {
	rootInfo, err := os.Lstat(material.root)
	if err != nil || !os.SameFile(rootInfo, material.rootInfo) || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return privateWorkspaceMigrationError("workspace_changed")
	}
	sourceInfo, err := os.Lstat(material.sourcePath)
	if err != nil || !os.SameFile(sourceInfo, material.sourceInfo) || sourceInfo.Mode()&os.ModeSymlink != 0 ||
		!sourceInfo.Mode().IsRegular() || !privateWorkspaceFileMode(sourceInfo.Mode()) {
		return privateWorkspaceMigrationError("source_changed")
	}
	sourceData, err := safepath.ReadFileWithinLimit(material.root, material.sourcePath, maxPrivateWorkspaceManifestBytes)
	if err != nil || !bytes.Equal(sourceData, material.sourceData) {
		return privateWorkspaceMigrationError("source_changed")
	}
	candidateInfo, err := os.Lstat(material.currentPath)
	if err != nil || material.candidateInfo == nil || !os.SameFile(candidateInfo, material.candidateInfo) ||
		candidateInfo.Mode()&os.ModeSymlink != 0 || !candidateInfo.Mode().IsRegular() ||
		!privateWorkspaceFileMode(candidateInfo.Mode()) {
		return privateWorkspaceMigrationError("candidate_changed")
	}
	candidateData, err := safepath.ReadFileWithinLimit(material.root, material.currentPath, maxPrivateWorkspaceManifestBytes)
	if err != nil || !bytes.Equal(candidateData, material.candidateData) {
		return privateWorkspaceMigrationError("candidate_changed")
	}
	legacyExists, err := privateWorkspaceMigrationRegularFile(material.legacyPath)
	if err != nil || legacyExists != (material.sourceLegacy || material.duplicateLegacy) {
		return privateWorkspaceMigrationError("source_changed")
	}
	stageExists, err := privateWorkspaceMigrationRegularFile(material.stagePath)
	if err != nil || stageExists != material.staged {
		return privateWorkspaceMigrationError("source_changed")
	}
	archiveExists, err := privateWorkspaceMigrationRegularFile(material.archivePath)
	if err != nil || archiveExists != material.archived {
		return privateWorkspaceMigrationError("source_archive_changed")
	}
	if archiveExists {
		archiveData, readErr := safepath.ReadFileWithinLimit(material.root, material.archivePath, maxPrivateWorkspaceManifestBytes)
		if readErr != nil || !bytes.Equal(archiveData, material.sourceData) {
			return privateWorkspaceMigrationError("source_archive_changed")
		}
	}
	if _, err := validatePrivateWorkspaceMigrationHealth(material.root, repository, material.manifest, material.staged); err != nil {
		return err
	}
	currentTree, err := snapshotPrivateWorkspaceMigrationTree(material.root)
	if err != nil || !equalPrivateWorkspaceMigrationTrees(material.protectedTree, currentTree) {
		return privateWorkspaceMigrationError("workspace_changed")
	}
	return nil
}

func snapshotPrivateWorkspaceMigrationTree(root string) (map[string]privateWorkspaceMigrationTreeEntry, error) {
	handle, err := os.OpenRoot(root)
	if err != nil {
		return nil, privateWorkspaceMigrationError("workspace_changed")
	}
	defer func() { _ = handle.Close() }()
	result := make(map[string]privateWorkspaceMigrationTreeEntry)
	var totalBytes int64
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if privateWorkspaceMigrationOwnedPath(relative) {
			return nil
		}
		if len(result) >= maxPrivateWorkspaceTreeEntries || entry.Type()&os.ModeSymlink != 0 {
			return privateWorkspaceMigrationError("workspace_changed")
		}
		info, err := entry.Info()
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			return privateWorkspaceMigrationError("workspace_changed")
		}
		snapshot := privateWorkspaceMigrationTreeEntry{info: info}
		if info.Mode().IsRegular() {
			if info.Size() < 0 || info.Size() > maxPrivateWorkspaceMigrationTreeBytes-totalBytes {
				return privateWorkspaceMigrationError("workspace_changed")
			}
			totalBytes += info.Size()
			file, err := handle.Open(filepath.FromSlash(relative))
			if err != nil {
				return privateWorkspaceMigrationError("workspace_changed")
			}
			hash := sha256.New()
			read, copyErr := io.Copy(hash, io.LimitReader(file, info.Size()+1))
			after, statErr := file.Stat()
			closeErr := file.Close()
			if copyErr != nil || statErr != nil || closeErr != nil || read != info.Size() ||
				!os.SameFile(info, after) || info.Mode() != after.Mode() || info.Size() != after.Size() {
				return privateWorkspaceMigrationError("workspace_changed")
			}
			snapshot.digest = hex.EncodeToString(hash.Sum(nil))
		}
		result[relative] = snapshot
		return nil
	})
	if err != nil {
		return nil, privateWorkspaceMigrationError("workspace_changed")
	}
	return result, nil
}

func privateWorkspaceMigrationOwnedPath(relative string) bool {
	switch relative {
	case PrivateWorkspaceManifestName,
		LegacyCalibratedWorkspaceManifestName,
		filepath.ToSlash(privateWorkspaceLockPath),
		filepath.ToSlash(filepath.Join(".ephemeral", privateWorkspaceMigrationStageName)),
		filepath.ToSlash(filepath.Join("reports", privateWorkspaceMigrationArchiveName)):
		return true
	default:
		return false
	}
}

func equalPrivateWorkspaceMigrationTrees(before, after map[string]privateWorkspaceMigrationTreeEntry) bool {
	if len(before) != len(after) {
		return false
	}
	for path, expected := range before {
		actual, ok := after[path]
		if !ok || !os.SameFile(expected.info, actual.info) || expected.info.Mode() != actual.info.Mode() ||
			(expected.info.Mode().IsRegular() && expected.info.Size() != actual.info.Size()) || expected.digest != actual.digest {
			return false
		}
	}
	return true
}

func inspectPrivateWorkspaceMigrationScratch(root string, allowStage bool) bool {
	directory := filepath.Join(root, ".ephemeral")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false
	}
	stageSeen := false
	for _, entry := range entries {
		name := entry.Name()
		if name != filepath.Base(privateWorkspaceLockPath) && (!allowStage || name != privateWorkspaceMigrationStageName) {
			return false
		}
		info, err := entry.Info()
		if err != nil || entry.Type()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
			return false
		}
		stageSeen = stageSeen || name == privateWorkspaceMigrationStageName
	}
	return stageSeen == allowStage
}

func privateWorkspaceMigrationRegularFile(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
		return false, privateWorkspaceMigrationError("manifest_mode")
	}
	return true, nil
}

func privateWorkspaceMigrationError(code string) error {
	return fmt.Errorf("%w: %s", ErrPrivateWorkspaceMigrationRejected, code)
}
