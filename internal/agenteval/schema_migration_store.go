package agenteval

import (
	"bytes"
	"os"
	"path/filepath"
)

const standaloneMigrationReceiptDirectory = "schema-migrations"

var (
	standaloneMigrationReceiptMkdir = hardenedMkdirAllWithin
	standaloneMigrationReceiptWrite = hardenedWriteFileExclusiveWithin
	standaloneMigrationReceiptRead  = hardenedReadFileWithinLimit
	standaloneMigrationReceiptSync  = hardenedSyncDirectoryWithin
)

func loadStandaloneMigrationPreviewForApply(options StandaloneMigrationPreviewOptions) (StandaloneMigrationPreview, bool, error) {
	_, edge, inspection, resolveErr := resolveStandaloneMigration(options.Namespace, options.Kind, options.From, options.To)
	if resolveErr != nil {
		return StandaloneMigrationPreview{}, false, resolveErr
	}
	preview, err := PreviewStandaloneSchemaMigration(options)
	if err == nil {
		return preview, false, nil
	}
	privatePreview, completed, recoveryErr := loadPrivateWorkspacePreviewForApply(options.Root, options.RepositoryRoot)
	if recoveryErr != nil {
		return StandaloneMigrationPreview{}, false, err
	}
	preview, buildErr := buildStandaloneMigrationPreview(privatePreview, edge, inspection)
	if buildErr != nil {
		return StandaloneMigrationPreview{}, false, buildErr
	}
	return preview, completed, nil
}

func loadPrivateWorkspacePreviewForApply(root, repositoryRoot string) (PrivateWorkspaceMigrationPreview, bool, error) {
	absRoot, absRepository, err := privateWorkspaceLocations(root, repositoryRoot, false)
	if err != nil {
		return PrivateWorkspaceMigrationPreview{}, false, privateWorkspaceMigrationError("workspace", err)
	}
	lock, err := acquirePrivateWorkspaceLock(absRoot)
	if err != nil {
		return PrivateWorkspaceMigrationPreview{}, false, privateWorkspaceMigrationError("workspace_busy", err)
	}
	defer func() { _ = lock.Unlock() }()
	if material, loadErr := loadPrivateWorkspaceMigration(absRoot, absRepository, true); loadErr == nil {
		return material.preview, false, nil
	}
	preview, err := loadCompletedPrivateWorkspaceMigration(absRoot, absRepository)
	if err != nil {
		return PrivateWorkspaceMigrationPreview{}, false, err
	}
	return preview, true, nil
}

func loadCompletedPrivateWorkspaceMigration(root, repository string) (PrivateWorkspaceMigrationPreview, error) {
	currentPath := filepath.Join(root, PrivateWorkspaceManifestName)
	archivePath := filepath.Join(root, "reports", privateWorkspaceMigrationArchiveName)
	legacyPath := filepath.Join(root, LegacyCalibratedWorkspaceManifestName)
	legacyV1Path := filepath.Join(root, LegacyPrivateWorkspaceManifestName)
	legacyV2Path := filepath.Join(root, LegacyActivationWorkspaceManifestName)
	stagePath := filepath.Join(root, ".ephemeral", privateWorkspaceMigrationStageName)
	currentExists, currentErr := privateWorkspaceMigrationRegularFile(currentPath)
	archiveExists, archiveErr := privateWorkspaceMigrationRegularFile(archivePath)
	legacyExists, legacyErr := privateWorkspaceMigrationRegularFile(legacyPath)
	legacyV1Exists, legacyV1Err := privateWorkspaceMigrationRegularFile(legacyV1Path)
	legacyV2Exists, legacyV2Err := privateWorkspaceMigrationRegularFile(legacyV2Path)
	stageExists, stageErr := privateWorkspaceMigrationRegularFile(stagePath)
	if currentErr != nil || archiveErr != nil || legacyErr != nil || legacyV1Err != nil || legacyV2Err != nil || stageErr != nil ||
		!currentExists || !archiveExists || legacyExists || legacyV1Exists || legacyV2Exists || stageExists {
		return PrivateWorkspaceMigrationPreview{}, privateWorkspaceMigrationError(
			"unsupported_state", currentErr, archiveErr, legacyErr, legacyV1Err, legacyV2Err, stageErr,
		)
	}
	sourceData, err := hardenedReadFileWithinLimit(root, archivePath, maxPrivateWorkspaceManifestBytes)
	if err != nil {
		return PrivateWorkspaceMigrationPreview{}, privateWorkspaceMigrationError("source_archive_changed", err)
	}
	currentData, err := hardenedReadFileWithinLimit(root, currentPath, maxPrivateWorkspaceManifestBytes)
	if err != nil {
		return PrivateWorkspaceMigrationPreview{}, privateWorkspaceMigrationError("candidate_changed", err)
	}
	source, err := DecodePrivateWorkspaceManifest(bytes.NewReader(sourceData))
	if err != nil || source.SchemaVersion != LegacyCalibratedWorkspaceSchemaVersion {
		return PrivateWorkspaceMigrationPreview{}, privateWorkspaceMigrationError("source_invalid", err)
	}
	candidate := source
	candidate.SchemaVersion = PrivateWorkspaceSchemaVersion
	wantCurrent, err := EncodePrivateWorkspaceManifest(candidate)
	if err != nil || !bytes.Equal(wantCurrent, currentData) {
		return PrivateWorkspaceMigrationPreview{}, privateWorkspaceMigrationError("candidate_changed", err)
	}
	decodedCurrent, err := DecodePrivateWorkspaceManifest(bytes.NewReader(currentData))
	if err != nil || decodedCurrent.SchemaVersion != PrivateWorkspaceSchemaVersion {
		return PrivateWorkspaceMigrationPreview{}, privateWorkspaceMigrationError("candidate_changed", err)
	}
	counts, err := validatePrivateWorkspaceMigrationHealth(root, repository, source, false)
	if err != nil {
		return PrivateWorkspaceMigrationPreview{}, err
	}
	return buildPrivateWorkspaceMigrationPreview(sourceData, currentData, counts, "completed")
}

func preserveStandaloneMigrationResult(root string, result StandaloneMigrationResult) (StandaloneMigrationResult, error) {
	data, err := EncodeStandaloneMigrationResult(result)
	if err != nil {
		return StandaloneMigrationResult{}, err
	}
	directory := filepath.Join(root, "reports", standaloneMigrationReceiptDirectory)
	if err := standaloneMigrationReceiptMkdir(root, directory, 0o700); err != nil {
		return StandaloneMigrationResult{}, standaloneMigrationError("receipt_directory", err)
	}
	if err := standaloneMigrationReceiptSync(root, filepath.Dir(directory)); err != nil {
		return StandaloneMigrationResult{}, standaloneMigrationError("receipt_directory_durability", err)
	}
	path := filepath.Join(directory, result.PreviewSHA256+".result.v1.json")
	if err := standaloneMigrationReceiptWrite(root, path, data, 0o600); err != nil {
		if !os.IsExist(err) {
			return StandaloneMigrationResult{}, standaloneMigrationError("receipt_write", err)
		}
		existing, readErr := standaloneMigrationReceiptRead(root, path, StandaloneMigrationArtifactMaxBytes)
		decoded, decodeErr := DecodeStandaloneMigrationResult(bytes.NewReader(existing))
		if readErr != nil || decodeErr != nil || !sameStandaloneMigrationIdentity(decoded, result) {
			return StandaloneMigrationResult{}, standaloneMigrationError("receipt_changed", readErr, decodeErr)
		}
		result, data = decoded, existing
	}
	if err := standaloneMigrationReceiptSync(root, directory); err != nil {
		return StandaloneMigrationResult{}, standaloneMigrationError("receipt_durability", err)
	}
	verified, err := standaloneMigrationReceiptRead(root, path, StandaloneMigrationArtifactMaxBytes)
	if err != nil || !bytes.Equal(verified, data) {
		return StandaloneMigrationResult{}, standaloneMigrationError("receipt_changed", err)
	}
	return result, nil
}

func sameStandaloneMigrationIdentity(left, right StandaloneMigrationResult) bool {
	left.Status, right.Status = "", ""
	return left == right
}
