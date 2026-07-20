package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
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
