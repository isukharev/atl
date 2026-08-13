package corpus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildActiveV3CachePublicationRoundTripAndMigration(t *testing.T) {
	root := privateBuildWorkspaceRoot(t)
	workspace, err := InitializeBuildWorkspace(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = workspace.Close() }()
	attemptID, _, err := workspace.BeginAttempt([]Service{ServiceConfluence})
	if err != nil {
		t.Fatal(err)
	}
	receipt := confluenceCaptureReceipt(t)
	active := buildWorkspaceActive(attemptID, receipt)
	active.Services[0].Service = ServiceConfluence
	if err := workspace.SaveActive(active); err != nil {
		t.Fatal(err)
	}
	active.SchemaVersion = BuildActiveSchemaV3
	active.PublicationTarget = PublicationTargetCache
	active.PublicationRootDigest = digestByte('8')
	workspace.store.testHook = func(step string) error {
		if step == "after_build_active_current_sync" {
			return errors.New("fault")
		}
		return nil
	}
	if err := workspace.SaveActive(active); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("migration error=%v", err)
	}
	workspace.store.testHook = nil
	loaded, found, err := workspace.LoadActive()
	if err != nil || !found || loaded.SchemaVersion != BuildActiveSchemaV3 ||
		loaded.PublicationTarget != PublicationTargetCache || loaded.PublicationRootDigest != active.PublicationRootDigest {
		t.Fatalf("loaded=%#v found=%t err=%v", loaded, found, err)
	}
	if _, err := os.Stat(filepath.Join(root, buildActiveFile)); err != nil {
		t.Fatalf("v2 recovery owner missing during migration: %v", err)
	}
	if err := workspace.SaveActive(active); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, buildActiveFile)); !os.IsNotExist(err) {
		t.Fatalf("v2 recovery owner survived reconciliation: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, buildActiveFileV3)); err != nil || !exactRegularMode(info.Mode(), privateFileMode) {
		t.Fatalf("v3 mode=%v err=%v", infoMode(info), err)
	}
}

func TestBuildActiveV3ChangedAttemptOwnsCrashRecovery(t *testing.T) {
	root := privateBuildWorkspaceRoot(t)
	workspace, err := InitializeBuildWorkspace(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = workspace.Close() }()
	receipt := mustCaptureReceipt(t, validCaptureReceiptInput())
	previousID, _, err := workspace.BeginAttempt([]Service{ServiceJira})
	if err != nil {
		t.Fatal(err)
	}
	previous := buildWorkspaceActive(previousID, receipt)
	if err := workspace.SaveActive(previous); err != nil {
		t.Fatal(err)
	}

	currentID, _, err := workspace.BeginAttempt([]Service{ServiceJira})
	if err != nil {
		t.Fatal(err)
	}
	current := buildWorkspaceActive(currentID, receipt)
	current.SchemaVersion = BuildActiveSchemaV3
	current.OptionsDigest = digestByte('8')
	current.PublicationTarget = PublicationTargetWorkspace
	workspace.store.testHook = func(step string) error {
		if step == "after_build_active_current_sync" {
			return errors.New("fault")
		}
		return nil
	}
	if err := workspace.SaveActive(current); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("transition error=%v", err)
	}
	workspace.store.testHook = nil

	loaded, found, err := workspace.LoadActive()
	if err != nil || !found || loaded.AttemptID != currentID || loaded.OptionsDigest != current.OptionsDigest || loaded.PublicationTarget != PublicationTargetWorkspace {
		t.Fatalf("loaded=%#v found=%t err=%v", loaded, found, err)
	}
	if _, err := os.Stat(filepath.Join(root, buildActiveFile)); err != nil {
		t.Fatalf("older recovery record missing at ambiguous boundary: %v", err)
	}
	if err := workspace.SaveActive(current); err != nil {
		t.Fatalf("transition reconciliation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, buildActiveFile)); !os.IsNotExist(err) {
		t.Fatalf("older recovery record survived reconciliation: %v", err)
	}
}

func TestBuildActiveV3PublicationBindingIsStrictAndV2BytesStayStable(t *testing.T) {
	active := buildWorkspaceActive("11111111111111111111111111111111", mustCaptureReceipt(t, validCaptureReceiptInput()))
	v2, err := CanonicalBuildActive(active, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseBuildActive(v2, Limits{})
	if err != nil || parsed.SchemaVersion != BuildActiveSchemaV2 || parsed.PublicationTarget != "" {
		t.Fatalf("v2 parsed=%#v err=%v", parsed, err)
	}
	base := active
	base.SchemaVersion = BuildActiveSchemaV3
	base.PublicationTarget = PublicationTargetWorkspace
	for name, mutate := range map[string]func(*BuildActive){
		"workspace digest":     func(value *BuildActive) { value.PublicationRootDigest = digestByte('1') },
		"cache missing digest": func(value *BuildActive) { value.PublicationTarget = PublicationTargetCache },
		"unknown target":       func(value *BuildActive) { value.PublicationTarget = "other" },
		"v2 fields": func(value *BuildActive) {
			value.SchemaVersion = BuildActiveSchemaV2
			value.PublicationTarget = PublicationTargetCache
			value.PublicationRootDigest = digestByte('1')
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			mutate(&value)
			if _, err := CanonicalBuildActive(value, Limits{}); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
