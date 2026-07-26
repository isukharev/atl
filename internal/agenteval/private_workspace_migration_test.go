package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/safepath"
)

func TestPrivateWorkspaceMigrationIsReviewedAndPreservesWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("migration apply requires durable directory sync")
	}
	root, repository, manifest := newPrivateWorkspaceMigrationFixture(t)
	sourceData, err := os.ReadFile(filepath.Join(root, LegacyCalibratedWorkspaceManifestName))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewPrivateWorkspaceMigration(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Status != "ready" || preview.FromSchemaVersion != LegacyCalibratedWorkspaceSchemaVersion ||
		preview.ToSchemaVersion != PrivateWorkspaceSchemaVersion || preview.PreservedRunSets != 1 ||
		preview.PreservedSpecRefs != 1 || len(preview.SourceSHA256) != 64 || len(preview.CandidateSHA256) != 64 ||
		len(preview.MigrationSHA256) != 64 || preview.SourceSHA256 != sha256HexBytes(sourceData) {
		t.Fatalf("preview=%+v", preview)
	}
	contractJSON := `{"domain":"atl-private-workspace-migration-v1","schema_version":1,"from_schema_version":3,"to_schema_version":4,` +
		`"source_name":"private-workspace.v3.json","candidate_name":"private-workspace.v4.json","source_sha256":"` +
		preview.SourceSHA256 + `","candidate_sha256":"` + preview.CandidateSHA256 + `"}`
	if preview.MigrationSHA256 != sha256HexBytes([]byte(contractJSON)) {
		t.Fatal("migration digest did not bind the documented domain, names, and exact byte digests")
	}
	if _, err := os.Lstat(filepath.Join(root, PrivateWorkspaceManifestName)); !os.IsNotExist(err) {
		t.Fatalf("preview created current manifest: %v", err)
	}
	wrong := strings.Repeat("a", 64)
	if wrong == preview.MigrationSHA256 {
		wrong = strings.Repeat("b", 64)
	}
	_, err = ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
		ExpectedMigrationSHA256: wrong, Confirm: PrivateWorkspaceMigrationConfirmation})
	if err == nil || !errors.Is(err, ErrPrivateWorkspaceMigrationRejected) {
		t.Fatalf("wrong digest err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, PrivateWorkspaceManifestName)); !os.IsNotExist(err) {
		t.Fatalf("wrong digest created current manifest: %v", err)
	}
	summary, err := ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
		ExpectedMigrationSHA256: preview.MigrationSHA256, Confirm: PrivateWorkspaceMigrationConfirmation})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "migrated" || summary.MigrationSHA256 != preview.MigrationSHA256 {
		t.Fatalf("summary=%+v", summary)
	}
	if _, err := os.Lstat(filepath.Join(root, LegacyCalibratedWorkspaceManifestName)); !os.IsNotExist(err) {
		t.Fatalf("legacy manifest remains: %v", err)
	}
	archivedSource, err := os.ReadFile(filepath.Join(root, "reports", privateWorkspaceMigrationArchiveName))
	if err != nil || !bytes.Equal(archivedSource, sourceData) {
		t.Fatalf("reviewed source archive changed: err=%v", err)
	}
	currentData, err := os.ReadFile(filepath.Join(root, PrivateWorkspaceManifestName))
	if err != nil || sha256HexBytes(currentData) != preview.CandidateSHA256 {
		t.Fatalf("candidate err=%v digest=%s", err, sha256HexBytes(currentData))
	}
	current, err := DecodePrivateWorkspaceManifest(bytes.NewReader(currentData))
	if err != nil {
		t.Fatal(err)
	}
	current.SchemaVersion = LegacyCalibratedWorkspaceSchemaVersion
	if !reflect.DeepEqual(current, manifest) {
		t.Fatalf("migrated manifest changed fields:\n got=%+v\nwant=%+v", current, manifest)
	}
	if data, err := os.ReadFile(filepath.Join(root, "cases", "preserved.txt")); err != nil || string(data) != "preserved\n" {
		t.Fatalf("case artifact changed: data=%q err=%v", data, err)
	}
	if report, err := DoctorPrivateWorkspace(root, repository); err != nil || !report.Healthy {
		t.Fatalf("doctor report=%+v err=%v", report, err)
	}
	if _, err := PreviewPrivateWorkspaceMigration(root, repository); err == nil {
		t.Fatal("current workspace was offered another migration")
	}
}

func TestPrivateWorkspaceMigrationRecoversExactCommittedCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("migration apply requires durable directory sync")
	}
	root, repository, manifest := newPrivateWorkspaceMigrationFixture(t)
	preview, err := PreviewPrivateWorkspaceMigration(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	candidate := manifest
	candidate.SchemaVersion = PrivateWorkspaceSchemaVersion
	candidateData, err := EncodePrivateWorkspaceManifest(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := safepath.WriteFileExclusiveWithin(root, filepath.Join(root, PrivateWorkspaceManifestName), candidateData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewPrivateWorkspaceMigration(root, repository); err == nil {
		t.Fatal("ambiguous workspace produced an ordinary preview")
	}
	summary, err := ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
		ExpectedMigrationSHA256: preview.MigrationSHA256, Confirm: PrivateWorkspaceMigrationConfirmation})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "recovered" {
		t.Fatalf("summary=%+v", summary)
	}
	if report, err := DoctorPrivateWorkspace(root, repository); err != nil || !report.Healthy {
		t.Fatalf("doctor report=%+v err=%v", report, err)
	}
}

func TestPrivateWorkspaceMigrationRecoversDuplicateRenameCrashState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("migration apply requires durable directory sync")
	}
	root, repository, manifest := newPrivateWorkspaceMigrationFixture(t)
	preview, err := PreviewPrivateWorkspaceMigration(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	candidate := manifest
	candidate.SchemaVersion = PrivateWorkspaceSchemaVersion
	candidateData, err := EncodePrivateWorkspaceManifest(candidate)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(root, LegacyCalibratedWorkspaceManifestName)
	stagePath := filepath.Join(root, ".ephemeral", privateWorkspaceMigrationStageName)
	archivePath := filepath.Join(root, "reports", privateWorkspaceMigrationArchiveName)
	sourceData, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := safepath.WriteFileExclusiveWithin(root, filepath.Join(root, PrivateWorkspaceManifestName), candidateData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := safepath.WriteFileExclusiveWithin(root, archivePath, sourceData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(legacyPath, stagePath); err != nil {
		t.Fatal(err)
	}
	summary, err := ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
		ExpectedMigrationSHA256: preview.MigrationSHA256, Confirm: PrivateWorkspaceMigrationConfirmation})
	if err != nil || summary.Status != "recovered" {
		t.Fatalf("duplicate rename recovery summary=%+v err=%v", summary, err)
	}
	if _, err := os.Lstat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("duplicate legacy source remains: %v", err)
	}
	if _, err := os.Lstat(stagePath); !os.IsNotExist(err) {
		t.Fatalf("staged source remains: %v", err)
	}
	if report, err := DoctorPrivateWorkspace(root, repository); err != nil || !report.Healthy {
		t.Fatalf("doctor report=%+v err=%v", report, err)
	}
}

func TestPrivateWorkspaceMigrationLeavesMismatchedDualManifestUntouched(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("migration apply requires durable directory sync")
	}
	root, repository, manifest := newPrivateWorkspaceMigrationFixture(t)
	preview, err := PreviewPrivateWorkspaceMigration(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	mismatch := manifest
	mismatch.SchemaVersion = PrivateWorkspaceSchemaVersion
	mismatch.Execution.MaxEstimatedCostMicroUSD++
	mismatchData, err := EncodePrivateWorkspaceManifest(mismatch)
	if err != nil {
		t.Fatal(err)
	}
	currentPath := filepath.Join(root, PrivateWorkspaceManifestName)
	if err := safepath.WriteFileExclusiveWithin(root, currentPath, mismatchData, 0o600); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(root, LegacyCalibratedWorkspaceManifestName)
	legacyBefore, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
		ExpectedMigrationSHA256: preview.MigrationSHA256, Confirm: PrivateWorkspaceMigrationConfirmation})
	if err == nil || !strings.Contains(err.Error(), "ambiguous_candidate") {
		t.Fatalf("mismatched recovery err=%v", err)
	}
	legacyAfter, legacyErr := os.ReadFile(legacyPath)
	currentAfter, currentErr := os.ReadFile(currentPath)
	if legacyErr != nil || currentErr != nil || !bytes.Equal(legacyAfter, legacyBefore) || !bytes.Equal(currentAfter, mismatchData) {
		t.Fatalf("mismatched dual manifests changed: legacy_err=%v current_err=%v", legacyErr, currentErr)
	}
}

func TestPrivateWorkspaceMigrationDetectsConcurrentManualMutation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("migration apply requires durable directory sync")
	}
	t.Run("source", func(t *testing.T) {
		root, repository, manifest := newPrivateWorkspaceMigrationFixture(t)
		preview, err := PreviewPrivateWorkspaceMigration(root, repository)
		if err != nil {
			t.Fatal(err)
		}
		originalWrite := privateWorkspaceMigrationWrite
		privateWorkspaceMigrationWrite = func(writeRoot, target string, data []byte, mode os.FileMode) error {
			if err := originalWrite(writeRoot, target, data, mode); err != nil {
				return err
			}
			changed := manifest
			changed.Retention.MaxCandidateAgeDays++
			changedData, err := EncodePrivateWorkspaceManifest(changed)
			if err != nil {
				return err
			}
			return writePrivateFile(filepath.Join(root, LegacyCalibratedWorkspaceManifestName), changedData)
		}
		t.Cleanup(func() { privateWorkspaceMigrationWrite = originalWrite })
		_, err = ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
			ExpectedMigrationSHA256: preview.MigrationSHA256, Confirm: PrivateWorkspaceMigrationConfirmation})
		if err == nil || !strings.Contains(err.Error(), "source_changed") {
			t.Fatalf("source mutation err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(root, LegacyCalibratedWorkspaceManifestName)); err != nil {
			t.Fatalf("mutated source was removed: %v", err)
		}
	})
	t.Run("candidate", func(t *testing.T) {
		root, repository, manifest := newPrivateWorkspaceMigrationFixture(t)
		preview, err := PreviewPrivateWorkspaceMigration(root, repository)
		if err != nil {
			t.Fatal(err)
		}
		originalSync := privateWorkspaceMigrationSync
		calls := 0
		privateWorkspaceMigrationSync = func(syncRoot, target string) error {
			calls++
			if err := originalSync(syncRoot, target); err != nil {
				return err
			}
			if calls == 1 {
				changed := manifest
				changed.SchemaVersion = PrivateWorkspaceSchemaVersion
				changed.Execution.MaxEstimatedCostMicroUSD++
				changedData, err := EncodePrivateWorkspaceManifest(changed)
				if err != nil {
					return err
				}
				return writePrivateFile(filepath.Join(root, PrivateWorkspaceManifestName), changedData)
			}
			return nil
		}
		t.Cleanup(func() { privateWorkspaceMigrationSync = originalSync })
		_, err = ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
			ExpectedMigrationSHA256: preview.MigrationSHA256, Confirm: PrivateWorkspaceMigrationConfirmation})
		if err == nil || !strings.Contains(err.Error(), "candidate_changed") {
			t.Fatalf("candidate mutation err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(root, LegacyCalibratedWorkspaceManifestName)); err != nil {
			t.Fatalf("source was removed after candidate mutation: %v", err)
		}
	})
	t.Run("byte-identical candidate inode", func(t *testing.T) {
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		preview, err := PreviewPrivateWorkspaceMigration(root, repository)
		if err != nil {
			t.Fatal(err)
		}
		originalSync := privateWorkspaceMigrationSync
		calls := 0
		privateWorkspaceMigrationSync = func(syncRoot, target string) error {
			calls++
			if err := originalSync(syncRoot, target); err != nil {
				return err
			}
			if calls == 1 {
				current := filepath.Join(root, PrivateWorkspaceManifestName)
				data, err := os.ReadFile(current)
				if err != nil {
					return err
				}
				return writePrivateFile(current, data)
			}
			return nil
		}
		t.Cleanup(func() { privateWorkspaceMigrationSync = originalSync })
		_, err = ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
			ExpectedMigrationSHA256: preview.MigrationSHA256, Confirm: PrivateWorkspaceMigrationConfirmation})
		if err == nil || !strings.Contains(err.Error(), "candidate_changed") {
			t.Fatalf("byte-identical candidate replacement err=%v", err)
		}
	})
	t.Run("workspace tree", func(t *testing.T) {
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		preview, err := PreviewPrivateWorkspaceMigration(root, repository)
		if err != nil {
			t.Fatal(err)
		}
		originalWrite := privateWorkspaceMigrationWrite
		privateWorkspaceMigrationWrite = func(writeRoot, target string, data []byte, mode os.FileMode) error {
			if err := originalWrite(writeRoot, target, data, mode); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(root, "cases", "preserved.txt"), []byte("changed but valid\n"), 0o600)
		}
		t.Cleanup(func() { privateWorkspaceMigrationWrite = originalWrite })
		_, err = ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
			ExpectedMigrationSHA256: preview.MigrationSHA256, Confirm: PrivateWorkspaceMigrationConfirmation})
		if err == nil || !strings.Contains(err.Error(), "workspace_changed") {
			t.Fatalf("tree mutation err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(root, LegacyCalibratedWorkspaceManifestName)); err != nil {
			t.Fatalf("source was removed after tree mutation: %v", err)
		}
	})
	t.Run("candidate before success", func(t *testing.T) {
		root, repository, manifest := newPrivateWorkspaceMigrationFixture(t)
		preview, err := PreviewPrivateWorkspaceMigration(root, repository)
		if err != nil {
			t.Fatal(err)
		}
		originalSync := privateWorkspaceMigrationSync
		calls := 0
		privateWorkspaceMigrationSync = func(syncRoot, target string) error {
			calls++
			if err := originalSync(syncRoot, target); err != nil {
				return err
			}
			if calls == 5 {
				changed := manifest
				changed.SchemaVersion = PrivateWorkspaceSchemaVersion
				changed.Retention.MaxCandidateAgeDays++
				changedData, err := EncodePrivateWorkspaceManifest(changed)
				if err != nil {
					return err
				}
				return writePrivateFile(filepath.Join(root, PrivateWorkspaceManifestName), changedData)
			}
			return nil
		}
		t.Cleanup(func() { privateWorkspaceMigrationSync = originalSync })
		_, err = ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
			ExpectedMigrationSHA256: preview.MigrationSHA256, Confirm: PrivateWorkspaceMigrationConfirmation})
		if err == nil || !strings.Contains(err.Error(), "candidate_changed") {
			t.Fatalf("late candidate mutation err=%v", err)
		}
		archived, archiveErr := os.ReadFile(filepath.Join(root, "reports", privateWorkspaceMigrationArchiveName))
		if archiveErr != nil || sha256HexBytes(archived) != preview.SourceSHA256 {
			t.Fatalf("reviewed source was not durably archived: err=%v", archiveErr)
		}
	})
	t.Run("workspace after postcondition inspection", func(t *testing.T) {
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		preview, err := PreviewPrivateWorkspaceMigration(root, repository)
		if err != nil {
			t.Fatal(err)
		}
		originalInspect := privateWorkspaceMigrationInspect
		privateWorkspaceMigrationInspect = func(inspectRoot, inspectRepository string) PrivateWorkspaceReport {
			report := originalInspect(inspectRoot, inspectRepository)
			if err := os.WriteFile(filepath.Join(root, "cases", "preserved.txt"), []byte("late valid mutation\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return report
		}
		t.Cleanup(func() { privateWorkspaceMigrationInspect = originalInspect })
		_, err = ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
			ExpectedMigrationSHA256: preview.MigrationSHA256, Confirm: PrivateWorkspaceMigrationConfirmation})
		if err == nil || !strings.Contains(err.Error(), "workspace_changed") {
			t.Fatalf("late tree mutation err=%v", err)
		}
	})
}

func TestPrivateWorkspaceMigrationRecoversTransactionFaultBoundaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("migration apply requires durable directory sync")
	}
	tests := []struct {
		name string
		hook func(*testing.T)
	}{
		{name: "candidate directory sync", hook: func(t *testing.T) {
			original := privateWorkspaceMigrationSync
			privateWorkspaceMigrationSync = func(_, _ string) error { return errors.New("synthetic sync failure") }
			t.Cleanup(func() { privateWorkspaceMigrationSync = original })
		}},
		{name: "staged source directory sync", hook: func(t *testing.T) {
			original := privateWorkspaceMigrationSync
			calls := 0
			privateWorkspaceMigrationSync = func(root, target string) error {
				calls++
				if calls == 3 {
					return errors.New("synthetic stage sync failure")
				}
				return original(root, target)
			}
			t.Cleanup(func() { privateWorkspaceMigrationSync = original })
		}},
		{name: "source archive directory sync", hook: func(t *testing.T) {
			original := privateWorkspaceMigrationSync
			calls := 0
			privateWorkspaceMigrationSync = func(root, target string) error {
				calls++
				if calls == 2 {
					return errors.New("synthetic archive sync failure")
				}
				return original(root, target)
			}
			t.Cleanup(func() { privateWorkspaceMigrationSync = original })
		}},
		{name: "staged source removal", hook: func(t *testing.T) {
			original := privateWorkspaceMigrationRemove
			privateWorkspaceMigrationRemove = func(_, _ string) error { return errors.New("synthetic remove failure") }
			t.Cleanup(func() { privateWorkspaceMigrationRemove = original })
		}},
		{name: "staged source removal sync", hook: func(t *testing.T) {
			original := privateWorkspaceMigrationSync
			calls := 0
			privateWorkspaceMigrationSync = func(root, target string) error {
				calls++
				if calls == 5 {
					return errors.New("synthetic removal sync failure")
				}
				return original(root, target)
			}
			t.Cleanup(func() { privateWorkspaceMigrationSync = original })
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
			preview, err := PreviewPrivateWorkspaceMigration(root, repository)
			if err != nil {
				t.Fatal(err)
			}
			test.hook(t)
			_, err = ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
				ExpectedMigrationSHA256: preview.MigrationSHA256, Confirm: PrivateWorkspaceMigrationConfirmation})
			if err == nil {
				t.Fatal("synthetic transaction fault was ignored")
			}
			privateWorkspaceMigrationWrite = safepath.WriteFileExclusiveWithin
			privateWorkspaceMigrationSync = safepath.SyncDirectoryWithin
			privateWorkspaceMigrationRename = safepath.RenameWithin
			privateWorkspaceMigrationRemove = safepath.RemoveWithin
			summary, err := ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
				ExpectedMigrationSHA256: preview.MigrationSHA256, Confirm: PrivateWorkspaceMigrationConfirmation})
			if err != nil || summary.Status != "recovered" {
				t.Fatalf("recovery summary=%+v err=%v", summary, err)
			}
			if report, err := DoctorPrivateWorkspace(root, repository); err != nil || !report.Healthy {
				t.Fatalf("doctor report=%+v err=%v", report, err)
			}
		})
	}
}

func TestPrivateWorkspaceMigrationRejectsUnsafeOrUnsupportedState(t *testing.T) {
	t.Run("windows apply fails before workspace access", func(t *testing.T) {
		originalGOOS := privateWorkspaceMigrationGOOS
		privateWorkspaceMigrationGOOS = "windows"
		t.Cleanup(func() { privateWorkspaceMigrationGOOS = originalGOOS })
		missingRoot := filepath.Join(t.TempDir(), "must-not-be-created")
		_, err := ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: missingRoot, RepositoryRoot: t.TempDir(),
			ExpectedMigrationSHA256: strings.Repeat("a", 64), Confirm: PrivateWorkspaceMigrationConfirmation})
		if err == nil || !strings.Contains(err.Error(), "platform_durability") {
			t.Fatalf("windows durability error=%v", err)
		}
		if _, statErr := os.Lstat(missingRoot); !os.IsNotExist(statErr) {
			t.Fatalf("windows apply touched workspace: %v", statErr)
		}
	})
	t.Run("confirmation", func(t *testing.T) {
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		preview, err := PreviewPrivateWorkspaceMigration(root, repository)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
			ExpectedMigrationSHA256: preview.MigrationSHA256}); err == nil {
			t.Fatal("migration applied without confirmation")
		}
	})
	t.Run("current candidate symlink", func(t *testing.T) {
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		if err := os.Symlink(filepath.Join(root, LegacyCalibratedWorkspaceManifestName), filepath.Join(root, PrivateWorkspaceManifestName)); err != nil {
			t.Fatal(err)
		}
		if _, err := PreviewPrivateWorkspaceMigration(root, repository); err == nil {
			t.Fatal("migration accepted a symlink candidate")
		}
	})
	t.Run("source symlink", func(t *testing.T) {
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		source := filepath.Join(root, LegacyCalibratedWorkspaceManifestName)
		if err := os.Remove(source); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "cases", "preserved.txt"), source); err != nil {
			t.Fatal(err)
		}
		if _, err := PreviewPrivateWorkspaceMigration(root, repository); err == nil {
			t.Fatal("migration accepted a symlink source")
		}
	})
	t.Run("source mode", func(t *testing.T) {
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		if err := os.Chmod(filepath.Join(root, LegacyCalibratedWorkspaceManifestName), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := PreviewPrivateWorkspaceMigration(root, repository); err == nil {
			t.Fatal("migration accepted a non-owner-only source")
		}
	})
	t.Run("workspace busy", func(t *testing.T) {
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		lock, err := acquirePrivateWorkspaceLock(root)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lock.Unlock() }()
		if _, err := PreviewPrivateWorkspaceMigration(root, repository); err == nil {
			t.Fatal("migration ignored the workspace lock")
		}
	})
	for _, version := range []int{LegacyPrivateWorkspaceSchemaVersion, LegacyActivationWorkspaceSchemaVersion, PrivateWorkspaceSchemaVersion} {
		t.Run("schema "+strconv.Itoa(version), func(t *testing.T) {
			repository := t.TempDir()
			root := filepath.Join(t.TempDir(), "private")
			manifest := DefaultPrivateWorkspaceManifest()
			manifest.SchemaVersion = version
			if report, err := InitPrivateWorkspace(root, repository, manifest); err != nil || !report.Healthy {
				t.Fatalf("init report=%+v err=%v", report, err)
			}
			if _, err := PreviewPrivateWorkspaceMigration(root, repository); err == nil {
				t.Fatalf("schema %d was offered a v3 migration", version)
			}
		})
	}
	t.Run("privacy-safe error", func(t *testing.T) {
		marker := "private-host.example.invalid-PROJ-123"
		_, err := PreviewPrivateWorkspaceMigration(filepath.Join(t.TempDir(), marker), t.TempDir())
		if err == nil || strings.Contains(err.Error(), marker) {
			t.Fatalf("migration error leaked private marker: %v", err)
		}
	})
}

func TestPrivateWorkspaceMigrationRejectsPendingPlan(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, false, false)
	_ = fixture.createPlan(t)
	downgradePrivateWorkspaceFixture(t, fixture)
	if report, err := DoctorPrivateWorkspace(fixture.root, fixture.repository); err != nil || !report.Healthy || report.Counts.PendingPlans != 1 {
		t.Fatalf("doctor report=%+v err=%v", report, err)
	}
	if _, err := PreviewPrivateWorkspaceMigration(fixture.root, fixture.repository); err == nil || !strings.Contains(err.Error(), "lifecycle_busy") {
		t.Fatalf("pending plan migration err=%v", err)
	}
}

func TestPrivateWorkspaceMigrationRejectsActiveRun(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, false, false)
	preview := fixture.createPlan(t)
	writePrivateWorkspaceMigrationState(t, fixture, preview, "running", true)
	downgradePrivateWorkspaceFixture(t, fixture)
	if report, err := DoctorPrivateWorkspace(fixture.root, fixture.repository); err != nil || !report.Healthy || report.Counts.ActiveRuns != 1 {
		t.Fatalf("doctor report=%+v err=%v", report, err)
	}
	if _, err := PreviewPrivateWorkspaceMigration(fixture.root, fixture.repository); err == nil || !strings.Contains(err.Error(), "lifecycle_busy") {
		t.Fatalf("active run migration err=%v", err)
	}
}

func TestPrivateWorkspaceMigrationPreservesNonzeroRunRecordCount(t *testing.T) {
	fixture := newPrivatePlanTestFixture(t, false, false)
	plan := fixture.createPlan(t)
	writePrivateWorkspaceMigrationState(t, fixture, plan, "interrupted", false)
	downgradePrivateWorkspaceFixture(t, fixture)
	preview, err := PreviewPrivateWorkspaceMigration(fixture.root, fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	if preview.PreservedRunRecords != 1 {
		t.Fatalf("preview=%+v", preview)
	}
}

func TestPrivateWorkspaceMigrationErrorKeepsCausesInspectableAndOutOfTheMessage(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "reports", privateWorkspaceMigrationArchiveName)
	archiveCause := &fs.PathError{Op: "rename", Path: archivePath, Err: fs.ErrPermission}
	stateCause := errors.New("staged source still present at " + archivePath)

	err := privateWorkspaceMigrationError("source_archive", archiveCause, nil, stateCause)
	assertPrivateWorkspaceMigrationCode(t, err, "source_archive")
	if strings.Contains(err.Error(), archivePath) {
		t.Fatalf("message leaked a configured path: %q", err.Error())
	}
	if !errors.Is(err, stateCause) || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("error %v lost a cause", err)
	}
	var typed *fs.PathError
	if !errors.As(err, &typed) || typed.Path != archivePath {
		t.Fatalf("error %v does not expose the concrete path error", err)
	}
	causes := privateWorkspaceMigrationErrorCauses(t, err)
	if len(causes) != 2 || causes[0] != error(archiveCause) || causes[1] != stateCause {
		t.Fatalf("causes=%v, want both non-nil causes retained in order", causes)
	}

	// A rejection with nothing to attach classifies exactly as it did before.
	assertPrivateWorkspaceMigrationCode(t, privateWorkspaceMigrationError("confirmation"), "confirmation")
	if causes := privateWorkspaceMigrationErrorCauses(t, privateWorkspaceMigrationError("confirmation", nil, nil)); len(causes) != 0 {
		t.Fatalf("causes=%v, want nil causes dropped", causes)
	}
}

func TestPrivateWorkspaceMigrationAttachesWorkspaceLockAndBoundaryCauses(t *testing.T) {
	t.Run("root symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation needs elevated privileges on Windows")
		}
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		linked := filepath.Join(t.TempDir(), "private-link")
		if err := os.Symlink(root, linked); err != nil {
			t.Fatal(err)
		}
		_, err := PreviewPrivateWorkspaceMigration(linked, repository)
		assertPrivateWorkspaceMigrationCode(t, err, "workspace")
		if !errors.Is(err, ErrPrivateWorkspaceUnhealthy) {
			t.Fatalf("error %v lost the workspace-location cause", err)
		}
		_, err = ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: linked, RepositoryRoot: repository,
			ExpectedMigrationSHA256: strings.Repeat("a", 64), Confirm: PrivateWorkspaceMigrationConfirmation})
		assertPrivateWorkspaceMigrationCode(t, err, "workspace")
		if !errors.Is(err, ErrPrivateWorkspaceUnhealthy) {
			t.Fatalf("error %v lost the workspace-location cause", err)
		}
	})
	t.Run("workspace busy", func(t *testing.T) {
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		lock, err := acquirePrivateWorkspaceLock(root)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lock.Unlock() }()
		_, err = PreviewPrivateWorkspaceMigration(root, repository)
		assertPrivateWorkspaceMigrationCode(t, err, "workspace_busy")
		if !errors.Is(err, ErrPrivateBaselineRejected) {
			t.Fatalf("error %v lost the lock cause", err)
		}
		_, err = ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
			ExpectedMigrationSHA256: strings.Repeat("a", 64), Confirm: PrivateWorkspaceMigrationConfirmation})
		assertPrivateWorkspaceMigrationCode(t, err, "workspace_busy")
		if !errors.Is(err, ErrPrivateBaselineRejected) {
			t.Fatalf("error %v lost the lock cause", err)
		}
	})
	t.Run("git boundary", func(t *testing.T) {
		root, _, _ := newPrivateWorkspaceMigrationFixture(t)
		// A workspace inside the repository tree is rejected by the git
		// boundary check rather than by any of the mode/marker predicates.
		_, err := PreviewPrivateWorkspaceMigration(root, filepath.Dir(root))
		assertPrivateWorkspaceMigrationCode(t, err, "workspace_unhealthy")
		if !errors.Is(err, ErrPrivateWorkspaceUnhealthy) {
			t.Fatalf("error %v lost the git-boundary cause", err)
		}
		if causes := privateWorkspaceMigrationErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want exactly the boundary failure", causes)
		}
	})
}

func TestPrivateWorkspaceMigrationAttachesManifestCauses(t *testing.T) {
	legacyName := LegacyCalibratedWorkspaceManifestName
	t.Run("source read limit", func(t *testing.T) {
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		if err := writePrivateFile(filepath.Join(root, legacyName),
			bytes.Repeat([]byte("a"), maxPrivateWorkspaceManifestBytes+1)); err != nil {
			t.Fatal(err)
		}
		_, err := PreviewPrivateWorkspaceMigration(root, repository)
		assertPrivateWorkspaceMigrationCode(t, err, "source_read")
		if causes := privateWorkspaceMigrationErrorCauses(t, err); len(causes) != 1 {
			t.Fatalf("causes=%v, want the read-limit failure retained", causes)
		}
	})
	t.Run("source decode", func(t *testing.T) {
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		broken := []byte(`{"schema_version":3,`)
		if err := writePrivateFile(filepath.Join(root, legacyName), broken); err != nil {
			t.Fatal(err)
		}
		_, want := DecodePrivateWorkspaceManifest(bytes.NewReader(broken))
		if want == nil {
			t.Fatal("fixture bytes decoded cleanly")
		}
		_, err := PreviewPrivateWorkspaceMigration(root, repository)
		assertPrivateWorkspaceMigrationCode(t, err, "source_invalid")
		causes := privateWorkspaceMigrationErrorCauses(t, err)
		if len(causes) != 1 || causes[0].Error() != want.Error() {
			t.Fatalf("causes=%v, want the decoder rejection %v", causes, want)
		}
	})
	t.Run("archive parent is not a directory", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("opening a path under a regular file is a Unix-specific failure")
		}
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		reports := filepath.Join(root, "reports")
		if err := os.RemoveAll(reports); err != nil {
			t.Fatal(err)
		}
		if err := writePrivateFile(reports, []byte("not a directory\n")); err != nil {
			t.Fatal(err)
		}
		_, err := PreviewPrivateWorkspaceMigration(root, repository)
		assertPrivateWorkspaceMigrationCode(t, err, "manifest_mode")
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("error %v does not expose the concrete stat failure", err)
		}
	})
}

func TestPrivateWorkspaceMigrationApplyAttachesTransactionCauses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("migration apply requires durable directory sync")
	}
	failSyncCall := func(t *testing.T, at int, failure error) {
		t.Helper()
		original := privateWorkspaceMigrationSync
		calls := 0
		privateWorkspaceMigrationSync = func(syncRoot, target string) error {
			calls++
			if calls == at {
				return failure
			}
			return original(syncRoot, target)
		}
		t.Cleanup(func() { privateWorkspaceMigrationSync = original })
	}
	for _, test := range []struct {
		name, code string
		hook       func(t *testing.T, root string, failure error)
	}{
		{"candidate write", "candidate_write", func(t *testing.T, _ string, failure error) {
			original := privateWorkspaceMigrationWrite
			privateWorkspaceMigrationWrite = func(string, string, []byte, os.FileMode) error { return failure }
			t.Cleanup(func() { privateWorkspaceMigrationWrite = original })
		}},
		{"candidate durability", "candidate_durability", func(t *testing.T, _ string, failure error) {
			failSyncCall(t, 1, failure)
		}},
		{"source archive write", "source_archive", func(t *testing.T, root string, failure error) {
			original := privateWorkspaceMigrationWrite
			archivePath := filepath.Join(root, "reports", privateWorkspaceMigrationArchiveName)
			privateWorkspaceMigrationWrite = func(writeRoot, target string, data []byte, mode os.FileMode) error {
				if target == archivePath {
					return failure
				}
				return original(writeRoot, target, data, mode)
			}
			t.Cleanup(func() { privateWorkspaceMigrationWrite = original })
		}},
		{"source archive durability", "source_archive_durability", func(t *testing.T, _ string, failure error) {
			failSyncCall(t, 2, failure)
		}},
		{"source stage rename", "source_stage", func(t *testing.T, _ string, failure error) {
			original := privateWorkspaceMigrationRename
			privateWorkspaceMigrationRename = func(string, string, string) error { return failure }
			t.Cleanup(func() { privateWorkspaceMigrationRename = original })
		}},
		{"source stage durability", "source_stage_durability", func(t *testing.T, _ string, failure error) {
			failSyncCall(t, 3, failure)
		}},
		{"source stage root durability", "source_stage_durability", func(t *testing.T, _ string, failure error) {
			failSyncCall(t, 4, failure)
		}},
		{"source remove", "source_remove", func(t *testing.T, _ string, failure error) {
			original := privateWorkspaceMigrationRemove
			privateWorkspaceMigrationRemove = func(string, string) error { return failure }
			t.Cleanup(func() { privateWorkspaceMigrationRemove = original })
		}},
		{"source remove durability", "source_remove_durability", func(t *testing.T, _ string, failure error) {
			failSyncCall(t, 5, failure)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
			preview, err := PreviewPrivateWorkspaceMigration(root, repository)
			if err != nil {
				t.Fatal(err)
			}
			// The cause text carries the configured root, so the exact-message
			// assertion also proves the root cannot reach a log line.
			failure := errors.New("synthetic transaction failure under " + root)
			test.hook(t, root, failure)

			_, err = ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
				ExpectedMigrationSHA256: preview.MigrationSHA256, Confirm: PrivateWorkspaceMigrationConfirmation})
			assertPrivateWorkspaceMigrationCode(t, err, test.code)
			if !errors.Is(err, failure) {
				t.Fatalf("error %v lost the transaction cause", err)
			}
			if causes := privateWorkspaceMigrationErrorCauses(t, err); len(causes) != 1 {
				t.Fatalf("causes=%v, want exactly the injected failure", causes)
			}
		})
	}
}

func TestPrivateWorkspaceMigrationTreeSnapshotKeepsNestedClassification(t *testing.T) {
	t.Run("unopenable root", func(t *testing.T) {
		_, err := snapshotPrivateWorkspaceMigrationTree(filepath.Join(t.TempDir(), "absent"))
		assertPrivateWorkspaceMigrationCode(t, err, "workspace_changed")
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("error %v does not expose the concrete open failure", err)
		}
	})
	t.Run("symlinked entry", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation needs elevated privileges on Windows")
		}
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "kept.txt"), "kept\n", 0o600)
		if err := os.Symlink(filepath.Join(root, "kept.txt"), filepath.Join(root, "linked.txt")); err != nil {
			t.Fatal(err)
		}
		_, err := snapshotPrivateWorkspaceMigrationTree(root)
		assertPrivateWorkspaceMigrationCode(t, err, "workspace_changed")
		causes := privateWorkspaceMigrationErrorCauses(t, err)
		if len(causes) != 1 {
			t.Fatalf("causes=%v, want the walk rejection retained", causes)
		}
		// The walk raised its own classification; it stays reachable instead of
		// collapsing into the outer code.
		var classified interface{ Code() string }
		if !errors.As(causes[0], &classified) || classified.Code() != "workspace_changed" {
			t.Fatalf("cause=%v, want the walk's own classification", causes[0])
		}
		if nested := privateWorkspaceMigrationErrorCauses(t, causes[0]); len(nested) != 0 {
			t.Fatalf("nested causes=%v, want none for a symlink rejection", nested)
		}
	})
}

func TestPrivateWorkspaceMigrationValidationOnlyRejectionsCarryNoCause(t *testing.T) {
	t.Run("confirmation", func(t *testing.T) {
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		_, err := ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
			ExpectedMigrationSHA256: strings.Repeat("a", 64)})
		assertPrivateWorkspaceMigrationCode(t, err, "confirmation")
		if causes := privateWorkspaceMigrationErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a rejection with no failure in hand", causes)
		}
	})
	t.Run("platform durability", func(t *testing.T) {
		original := privateWorkspaceMigrationGOOS
		privateWorkspaceMigrationGOOS = "windows"
		t.Cleanup(func() { privateWorkspaceMigrationGOOS = original })
		_, err := ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: t.TempDir(), RepositoryRoot: t.TempDir(),
			ExpectedMigrationSHA256: strings.Repeat("a", 64), Confirm: PrivateWorkspaceMigrationConfirmation})
		assertPrivateWorkspaceMigrationCode(t, err, "platform_durability")
		if causes := privateWorkspaceMigrationErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a rejection with no failure in hand", causes)
		}
	})
	t.Run("reviewed digest", func(t *testing.T) {
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		preview, err := PreviewPrivateWorkspaceMigration(root, repository)
		if err != nil {
			t.Fatal(err)
		}
		wrong := strings.Repeat("a", 64)
		if wrong == preview.MigrationSHA256 {
			wrong = strings.Repeat("b", 64)
		}
		_, err = ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{Root: root, RepositoryRoot: repository,
			ExpectedMigrationSHA256: wrong, Confirm: PrivateWorkspaceMigrationConfirmation})
		assertPrivateWorkspaceMigrationCode(t, err, "reviewed_digest")
		if causes := privateWorkspaceMigrationErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none when only the reviewed digest differs", causes)
		}
	})
	t.Run("unsupported state", func(t *testing.T) {
		root, repository, manifest := newPrivateWorkspaceMigrationFixture(t)
		candidate := manifest
		candidate.SchemaVersion = PrivateWorkspaceSchemaVersion
		candidateData, err := EncodePrivateWorkspaceManifest(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if err := safepath.WriteFileExclusiveWithin(root, filepath.Join(root, PrivateWorkspaceManifestName), candidateData, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = PreviewPrivateWorkspaceMigration(root, repository)
		assertPrivateWorkspaceMigrationCode(t, err, "unsupported_state")
		if causes := privateWorkspaceMigrationErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a rejection with no failure in hand", causes)
		}
	})
	t.Run("source schema version", func(t *testing.T) {
		root, repository, manifest := newPrivateWorkspaceMigrationFixture(t)
		current := manifest
		current.SchemaVersion = PrivateWorkspaceSchemaVersion
		currentData, err := EncodePrivateWorkspaceManifest(current)
		if err != nil {
			t.Fatal(err)
		}
		if err := writePrivateFile(filepath.Join(root, LegacyCalibratedWorkspaceManifestName), currentData); err != nil {
			t.Fatal(err)
		}
		_, err = PreviewPrivateWorkspaceMigration(root, repository)
		assertPrivateWorkspaceMigrationCode(t, err, "source_invalid")
		if causes := privateWorkspaceMigrationErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none when the source decodes but is the wrong schema", causes)
		}
	})
	t.Run("source mode", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("owner-only mode is not enforced on Windows")
		}
		root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
		if err := os.Chmod(filepath.Join(root, LegacyCalibratedWorkspaceManifestName), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := PreviewPrivateWorkspaceMigration(root, repository)
		assertPrivateWorkspaceMigrationCode(t, err, "manifest_mode")
		if causes := privateWorkspaceMigrationErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none when only the mode is wrong", causes)
		}
	})
}

func assertPrivateWorkspaceMigrationCode(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, ErrPrivateWorkspaceMigrationRejected) {
		t.Fatalf("err=%v, want the migration sentinel", err)
	}
	if got, want := err.Error(), ErrPrivateWorkspaceMigrationRejected.Error()+": "+code; got != want {
		t.Fatalf("message=%q, want %q", got, want)
	}
}

func privateWorkspaceMigrationErrorCauses(t *testing.T, err error) []error {
	t.Helper()
	multi, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("%T does not unwrap to multiple errors", err)
	}
	tree := multi.Unwrap()
	if len(tree) == 0 || !errors.Is(tree[0], ErrPrivateWorkspaceMigrationRejected) {
		t.Fatalf("unwrap tree=%v, want the sentinel first", tree)
	}
	return tree[1:]
}

func downgradePrivateWorkspaceFixture(t *testing.T, fixture privatePlanTestFixture) {
	t.Helper()
	currentPath := filepath.Join(fixture.root, PrivateWorkspaceManifestName)
	data, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = LegacyCalibratedWorkspaceSchemaVersion
	legacyData, err := EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(fixture.root, LegacyCalibratedWorkspaceManifestName)
	if err := safepath.WriteFileExclusiveWithin(fixture.root, legacyPath, legacyData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := safepath.RemoveWithin(fixture.root, currentPath); err != nil {
		t.Fatal(err)
	}
	if err := safepath.SyncDirectoryWithin(fixture.root, fixture.root); err != nil {
		t.Fatal(err)
	}
}

func writePrivateWorkspaceMigrationState(t *testing.T, fixture privatePlanTestFixture, preview PrivatePlanPreview, status string, createRun bool) {
	t.Helper()
	planPath := filepath.Join(fixture.root, "plans", preview.PlanID+".json")
	planData, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-11111111111111111111111111111111"
	state := privatePlanState{SchemaVersion: legacyComparisonPrivatePlanStateSchemaVersion, PlanSHA256: sha256HexBytes(planData),
		RunID: runID, Status: status, CompletedSurfaces: []string{}}
	stateData, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	stateData = append(stateData, '\n')
	if err := writePrivateFile(filepath.Join(fixture.root, "plans", preview.PlanID+".state.json"), stateData); err != nil {
		t.Fatal(err)
	}
	if createRun {
		if err := os.Mkdir(filepath.Join(fixture.root, "runs", runID), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func newPrivateWorkspaceMigrationFixture(t *testing.T) (string, string, PrivateWorkspaceManifest) {
	t.Helper()
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private")
	manifest := DefaultPrivateWorkspaceManifest()
	manifest.SchemaVersion = LegacyCalibratedWorkspaceSchemaVersion
	manifest.Execution.MaxEstimatedCostMicroUSD = 17_000_000
	manifest.Retention.KeepCompletedRunSetsPerAlias = 7
	if report, err := InitPrivateWorkspace(root, repository, manifest); err != nil || !report.Healthy {
		t.Fatalf("init report=%+v err=%v", report, err)
	}
	sourceCase, _, _, _, _ := writePrivatePairFixture(t)
	caseRoot := filepath.Join(root, "cases", "comparison")
	if err := copyWorkspace(sourceCase, caseRoot); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "cases", "preserved.txt"), "preserved\n", 0o600)
	manifest.RunSets = []PrivateWorkspaceRunSet{{Alias: "comparison", SpecPaths: []string{"cases/comparison/run.mcp.json"}, QualitativeReviewRequired: true}}
	data, err := EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(filepath.Join(root, LegacyCalibratedWorkspaceManifestName), data); err != nil {
		t.Fatal(err)
	}
	if report, err := DoctorPrivateWorkspace(root, repository); err != nil || !report.Healthy {
		t.Fatalf("doctor report=%+v err=%v", report, err)
	}
	return root, repository, manifest
}
