package corpus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildWorkspacePersistsAttemptActiveAndReceipt(t *testing.T) {
	root := privateBuildWorkspaceRoot(t)
	workspace, err := InitializeBuildWorkspace(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	attemptID, roots, err := workspace.BeginAttempt([]Service{ServiceJira})
	if err != nil {
		t.Fatal(err)
	}
	if roots[ServiceJira] != filepath.Join(root, buildAttemptsDir, attemptID, string(ServiceJira)) {
		t.Fatalf("attempt roots = %#v", roots)
	}
	for _, path := range []string{
		filepath.Join(root, buildAttemptsDir),
		filepath.Join(root, buildAttemptsDir, attemptID),
		roots[ServiceJira],
		filepath.Join(root, buildAttemptsDir, attemptID, buildReceiptsDir),
	} {
		info, err := os.Stat(path)
		if err != nil || !exactDirectoryMode(info.Mode()) {
			t.Fatalf("path %s mode=%v err=%v", path, infoMode(info), err)
		}
	}

	receipt := mustCaptureReceipt(t, validCaptureReceiptInput())
	active := buildWorkspaceActive(attemptID, receipt)
	if err := workspace.SaveActive(active); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := workspace.LoadActive()
	if err != nil || !found || loaded.AttemptID != attemptID || loaded.Services[0].ScopeDigest != "" {
		t.Fatalf("initial active=%#v found=%t err=%v", loaded, found, err)
	}
	if err := workspace.SaveCaptureReceipt(attemptID, receipt); err != nil {
		t.Fatal(err)
	}
	if err := workspace.SaveCaptureReceipt(attemptID, receipt); err != nil {
		t.Fatalf("idempotent receipt save: %v", err)
	}
	active.Services[0].ScopeDigest = receipt.ScopeDigest
	active.Services[0].ReceiptDigest = receipt.ReceiptDigest
	if err := workspace.SaveActive(active); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}

	workspace, err = OpenBuildWorkspace(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = workspace.Close() }()
	loaded, found, err = workspace.LoadActive()
	if err != nil || !found || loaded.Services[0].ReceiptDigest != receipt.ReceiptDigest {
		t.Fatalf("reopened active=%#v found=%t err=%v", loaded, found, err)
	}
	got, found, err := workspace.LoadCaptureReceipt(attemptID, ServiceJira)
	if err != nil || !found || got.ReceiptDigest != receipt.ReceiptDigest {
		t.Fatalf("receipt=%#v found=%t err=%v", got, found, err)
	}
	if info, err := os.Stat(filepath.Join(root, buildAttemptsDir, attemptID, buildReceiptsDir, "jira.capture.v1.json")); err != nil || !exactRegularMode(info.Mode(), privateFileMode) {
		t.Fatalf("receipt mode=%v err=%v", infoMode(info), err)
	}
}

func TestBuildWorkspaceSerializesWritersAndReleasesOnClose(t *testing.T) {
	root := privateBuildWorkspaceRoot(t)
	first, err := InitializeBuildWorkspace(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if second, err := OpenBuildWorkspace(ctx, root, Options{}); second != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent open=%#v err=%v", second, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenBuildWorkspace(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
}

func TestBuildWorkspaceActiveDurabilityBoundaries(t *testing.T) {
	root := privateBuildWorkspaceRoot(t)
	workspace, err := InitializeBuildWorkspace(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	attemptID, _, err := workspace.BeginAttempt([]Service{ServiceJira})
	if err != nil {
		t.Fatal(err)
	}
	receipt := mustCaptureReceipt(t, validCaptureReceiptInput())
	active := buildWorkspaceActive(attemptID, receipt)
	if err := workspace.SaveActive(active); err != nil {
		t.Fatal(err)
	}

	active.Usage.Attempts = 1
	workspace.store.testHook = func(step string) error {
		if step == "after_build_active_temp_sync" {
			return errors.New("fault")
		}
		return nil
	}
	if err := workspace.SaveActive(active); err == nil || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("pre-rename error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, buildActiveTemp)); err != nil {
		t.Fatalf("pre-rename evidence = %v", err)
	}
	workspace.store.testHook = nil
	loaded, found, err := workspace.LoadActive()
	if err != nil || !found || loaded.Usage.Attempts != 0 {
		t.Fatalf("old active=%#v found=%t err=%v", loaded, found, err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	workspace, err = OpenBuildWorkspace(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, buildActiveTemp)); !os.IsNotExist(err) {
		t.Fatalf("precommit temp was not recovered: %v", err)
	}

	workspace.store.testHook = func(step string) error {
		if step == "after_build_active_rename" {
			return errors.New("fault")
		}
		return nil
	}
	if err := workspace.SaveActive(active); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("post-rename error = %v", err)
	}
	workspace.store.testHook = nil
	loaded, found, err = workspace.LoadActive()
	if err != nil || !found || loaded.Usage.Attempts != 1 {
		t.Fatalf("renamed active=%#v found=%t err=%v", loaded, found, err)
	}
	_ = workspace.Close()
}

func TestBuildWorkspaceFailsClosedOnPartialOrTamperedState(t *testing.T) {
	t.Run("partial bootstrap", func(t *testing.T) {
		root := privateBuildWorkspaceRoot(t)
		store, err := Initialize(root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		_ = store.Close()
		if workspace, err := OpenBuildWorkspace(context.Background(), root, Options{}); err == nil || workspace != nil {
			t.Fatalf("workspace=%#v err=%v", workspace, err)
		}
	})

	t.Run("active symlink", func(t *testing.T) {
		root := privateBuildWorkspaceRoot(t)
		workspace, err := InitializeBuildWorkspace(context.Background(), root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		_ = workspace.Close()
		if err := os.Symlink("outside", filepath.Join(root, buildActiveFile)); err != nil {
			t.Fatal(err)
		}
		if workspace, err := OpenBuildWorkspace(context.Background(), root, Options{}); err == nil || workspace != nil {
			t.Fatalf("workspace=%#v err=%v", workspace, err)
		}
	})

	t.Run("mismatched receipt", func(t *testing.T) {
		root := privateBuildWorkspaceRoot(t)
		workspace, err := InitializeBuildWorkspace(context.Background(), root, Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = workspace.Close() }()
		attemptID, _, err := workspace.BeginAttempt([]Service{ServiceJira})
		if err != nil {
			t.Fatal(err)
		}
		receipt := mustCaptureReceipt(t, validCaptureReceiptInput())
		if err := workspace.SaveCaptureReceipt(attemptID, receipt); err != nil {
			t.Fatal(err)
		}
		other := receipt
		other.ReceiptDigest = strings.Repeat("f", 64)
		if err := workspace.SaveCaptureReceipt(attemptID, other); err == nil {
			t.Fatal("mismatched receipt was accepted")
		}
	})
}

func privateBuildWorkspaceRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, privateDirMode); err != nil {
		t.Fatal(err)
	}
	return root
}

func buildWorkspaceActive(attemptID string, receipt CaptureReceipt) BuildActive {
	started := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	return BuildActive{
		SchemaVersion: BuildActiveSchemaV1, AttemptID: attemptID, Status: BuildAttemptActive,
		OptionsDigest: digestByte('9'),
		Services:      []BuildServiceState{{Service: ServiceJira, SelectorDigest: receipt.SelectorDigest}},
		StartedAt:     NewBuildActiveTime(started), Deadline: NewBuildActiveTime(started.Add(time.Hour)),
		MaxAttempts: 100, MaxResponseBytes: 1 << 20,
	}
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode()
}
