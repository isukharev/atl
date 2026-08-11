package agenteval

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestStandaloneSchemaRegistryInspectionAndMigrationPreviewAreClosed(t *testing.T) {
	registry, err := BuiltInStandaloneSchemaRegistry()
	if err != nil {
		t.Fatal(err)
	}
	encodedRegistry, err := EncodeStandaloneSchemaRegistry(registry)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encodedRegistry, BuiltInStandaloneSchemaRegistryBytes()) {
		t.Fatal("root facade changed canonical registry bytes")
	}
	decodedRegistry, err := DecodeStandaloneSchemaRegistry(bytes.NewReader(encodedRegistry))
	if err != nil || len(decodedRegistry.Entries) != len(registry.Entries) {
		t.Fatalf("decoded entries=%d err=%v", len(decodedRegistry.Entries), err)
	}
	inspection, err := InspectStandaloneSchema("atl-profile", "private-workspace")
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Current != PrivateWorkspaceSchemaVersion || inspection.SupportedMigrations != 1 ||
		inspection.MigrationUnavailable || len(inspection.ImplementationSHA256s) != 1 || len(inspection.ImplementationSHA256s[0]) != 64 {
		t.Fatalf("inspection=%+v", inspection)
	}
	if _, err := InspectStandaloneSchema("atl-profile", "missing"); !errors.Is(err, ErrStandaloneSchemaRegistry) {
		t.Fatalf("unknown schema error=%v", err)
	}

	root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
	preview, err := PreviewStandaloneSchemaMigration(StandaloneMigrationPreviewOptions{
		Namespace: "atl-profile", Kind: "private-workspace", From: 3, To: 4,
		Root: root, RepositoryRoot: repository,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Status != "ready" || preview.Privacy != "owner_private" || len(preview.Counts) != 3 ||
		!validSHA256(preview.PreviewSHA256) || !validSHA256(preview.ImplementationSHA256) || !validSHA256(preview.RegistrySHA256) {
		t.Fatalf("preview=%+v", preview)
	}
	encoded, err := EncodeStandaloneMigrationPreview(preview)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeStandaloneMigrationPreview(bytes.NewReader(encoded))
	if err != nil || decoded.PreviewSHA256 != preview.PreviewSHA256 {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	for name, mutation := range map[string][]byte{
		"future":    bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"unknown":   bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":1,"unknown":true`), 1),
		"duplicate": bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1),
		"trailing":  append(slices.Clone(encoded), []byte(`{}`)...),
		"digest":    bytes.Replace(encoded, []byte(preview.PreviewSHA256), []byte(strings.Repeat("0", 64)), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeStandaloneMigrationPreview(bytes.NewReader(mutation)); !errors.Is(err, ErrStandaloneMigration) {
				t.Fatalf("error=%v, want migration rejection", err)
			}
		})
	}
}

func TestStandaloneSchemaMigrationApplyIsReviewedPreservingAndIdempotent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("migration apply requires durable directory sync")
	}
	root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
	legacyPath := filepath.Join(root, LegacyCalibratedWorkspaceManifestName)
	sourceBytes, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	options := StandaloneMigrationPreviewOptions{
		Namespace: "atl-profile", Kind: "private-workspace", From: 3, To: 4,
		Root: root, RepositoryRoot: repository,
	}
	preview, err := PreviewStandaloneSchemaMigration(options)
	if err != nil {
		t.Fatal(err)
	}
	wrong := StandaloneMigrationApplyOptions{StandaloneMigrationPreviewOptions: options,
		ExpectedPreviewSHA256: strings.Repeat("0", 64), Confirm: StandaloneMigrationConfirmation}
	if _, err := ApplyStandaloneSchemaMigration(wrong); err == nil {
		t.Fatal("apply accepted a stale reviewed digest")
	}
	if got, err := os.ReadFile(legacyPath); err != nil || !bytes.Equal(got, sourceBytes) {
		t.Fatalf("rejected apply changed source: err=%v", err)
	}
	apply := StandaloneMigrationApplyOptions{StandaloneMigrationPreviewOptions: options,
		ExpectedPreviewSHA256: preview.PreviewSHA256, Confirm: StandaloneMigrationConfirmation}
	result, err := ApplyStandaloneSchemaMigration(apply)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "migrated" || result.PreviewSHA256 != preview.PreviewSHA256 {
		t.Fatalf("result=%+v", result)
	}
	archive := filepath.Join(root, "reports", privateWorkspaceMigrationArchiveName)
	if archived, err := os.ReadFile(archive); err != nil || !bytes.Equal(archived, sourceBytes) {
		t.Fatalf("archive did not preserve exact source: err=%v", err)
	}
	current, err := os.ReadFile(filepath.Join(root, PrivateWorkspaceManifestName))
	if err != nil || sha256HexBytes(current) != preview.CandidateSHA256 {
		t.Fatalf("candidate mismatch: err=%v", err)
	}
	again, err := ApplyStandaloneSchemaMigration(apply)
	if err != nil || again != result {
		t.Fatalf("idempotent result=%+v first=%+v err=%v", again, result, err)
	}
	receiptPath := filepath.Join(root, "reports", standaloneMigrationReceiptDirectory, preview.PreviewSHA256+".result.v1.json")
	receipt, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeStandaloneMigrationResult(bytes.NewReader(receipt))
	if err != nil || decoded != result {
		t.Fatalf("receipt=%+v err=%v", decoded, err)
	}
}

func TestStandaloneSchemaMigrationRecoversReceiptDurabilityBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("migration apply requires durable directory sync")
	}
	root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
	options := StandaloneMigrationPreviewOptions{Namespace: "atl-profile", Kind: "private-workspace", From: 3, To: 4,
		Root: root, RepositoryRoot: repository}
	preview, err := PreviewStandaloneSchemaMigration(options)
	if err != nil {
		t.Fatal(err)
	}
	apply := StandaloneMigrationApplyOptions{StandaloneMigrationPreviewOptions: options,
		ExpectedPreviewSHA256: preview.PreviewSHA256, Confirm: StandaloneMigrationConfirmation}
	originalSync := standaloneMigrationReceiptSync
	t.Cleanup(func() { standaloneMigrationReceiptSync = originalSync })
	calls := 0
	standaloneMigrationReceiptSync = func(root, target string) error {
		calls++
		if calls == 2 {
			return errors.New("synthetic receipt sync failure")
		}
		return originalSync(root, target)
	}
	if _, err := ApplyStandaloneSchemaMigration(apply); err == nil || !strings.Contains(err.Error(), "receipt_durability") {
		t.Fatalf("first apply error=%v", err)
	}
	standaloneMigrationReceiptSync = originalSync
	result, err := ApplyStandaloneSchemaMigration(apply)
	if err != nil || result.Status != "migrated" {
		t.Fatalf("recovered result=%+v err=%v", result, err)
	}
}

func TestStandaloneSchemaMigrationRecoversReceiptWriteBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("migration apply requires durable directory sync")
	}
	root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
	options := StandaloneMigrationPreviewOptions{Namespace: "atl-profile", Kind: "private-workspace", From: 3, To: 4,
		Root: root, RepositoryRoot: repository}
	preview, err := PreviewStandaloneSchemaMigration(options)
	if err != nil {
		t.Fatal(err)
	}
	apply := StandaloneMigrationApplyOptions{StandaloneMigrationPreviewOptions: options,
		ExpectedPreviewSHA256: preview.PreviewSHA256, Confirm: StandaloneMigrationConfirmation}
	originalWrite := standaloneMigrationReceiptWrite
	t.Cleanup(func() { standaloneMigrationReceiptWrite = originalWrite })
	standaloneMigrationReceiptWrite = func(string, string, []byte, os.FileMode) error {
		return errors.New("synthetic receipt write failure")
	}
	if _, err := ApplyStandaloneSchemaMigration(apply); err == nil || !strings.Contains(err.Error(), "receipt_write") {
		t.Fatalf("first apply error=%v", err)
	}
	standaloneMigrationReceiptWrite = originalWrite
	result, err := ApplyStandaloneSchemaMigration(apply)
	if err != nil || result.Status != "recovered" {
		t.Fatalf("recovered result=%+v err=%v", result, err)
	}
}

func TestStandaloneSchemaMigrationConcurrentRetryConvergesToOneReceipt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("migration apply requires durable directory sync")
	}
	root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
	options := StandaloneMigrationPreviewOptions{Namespace: "atl-profile", Kind: "private-workspace", From: 3, To: 4,
		Root: root, RepositoryRoot: repository}
	preview, err := PreviewStandaloneSchemaMigration(options)
	if err != nil {
		t.Fatal(err)
	}
	apply := StandaloneMigrationApplyOptions{StandaloneMigrationPreviewOptions: options,
		ExpectedPreviewSHA256: preview.PreviewSHA256, Confirm: StandaloneMigrationConfirmation}
	start := make(chan struct{})
	var results [2]StandaloneMigrationResult
	var applyErrors [2]error
	var group sync.WaitGroup
	for index := range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results[index], applyErrors[index] = ApplyStandaloneSchemaMigration(apply)
		}()
	}
	close(start)
	group.Wait()
	for index, err := range applyErrors {
		if err == nil {
			continue
		}
		results[index], applyErrors[index] = ApplyStandaloneSchemaMigration(apply)
		if applyErrors[index] != nil {
			t.Fatalf("explicit retry after concurrent apply error=%v retry=%v", err, applyErrors[index])
		}
	}
	if results[0] != results[1] {
		t.Fatalf("receipts differ: first=%+v second=%+v", results[0], results[1])
	}
}

func TestStandaloneSchemaMigrationRejectsUnknownFutureAndPrivateLeakage(t *testing.T) {
	marker := "private-schema-migration-marker-must-not-appear"
	_, err := PreviewStandaloneSchemaMigration(StandaloneMigrationPreviewOptions{
		Namespace: "atl-profile", Kind: "private-workspace", From: 4, To: 5,
		Root: filepath.Join(t.TempDir(), marker), RepositoryRoot: t.TempDir(),
	})
	if err == nil || strings.Contains(err.Error(), marker) {
		t.Fatalf("error=%v", err)
	}
	_, err = ApplyStandaloneSchemaMigration(StandaloneMigrationApplyOptions{
		StandaloneMigrationPreviewOptions: StandaloneMigrationPreviewOptions{
			Namespace: "atl-profile", Kind: "private-workspace", From: 4, To: 5,
			Root: filepath.Join(t.TempDir(), marker), RepositoryRoot: t.TempDir(),
		},
		ExpectedPreviewSHA256: strings.Repeat("a", 64), Confirm: StandaloneMigrationConfirmation,
	})
	if err == nil || strings.Contains(err.Error(), marker) {
		t.Fatalf("apply error=%v", err)
	}
}

func TestStandaloneSchemaMigrationCompletedStateRejectsConflictingLegacyBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("migration apply requires durable directory sync")
	}
	root, repository, _ := newPrivateWorkspaceMigrationFixture(t)
	options := StandaloneMigrationPreviewOptions{Namespace: "atl-profile", Kind: "private-workspace", From: 3, To: 4,
		Root: root, RepositoryRoot: repository}
	preview, err := PreviewStandaloneSchemaMigration(options)
	if err != nil {
		t.Fatal(err)
	}
	apply := StandaloneMigrationApplyOptions{StandaloneMigrationPreviewOptions: options,
		ExpectedPreviewSHA256: preview.PreviewSHA256, Confirm: StandaloneMigrationConfirmation}
	if _, err := ApplyStandaloneSchemaMigration(apply); err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(filepath.Join(root, "reports", privateWorkspaceMigrationArchiveName))
	if err != nil {
		t.Fatal(err)
	}
	conflicting := append(slices.Clone(archive), ' ')
	if err := os.WriteFile(filepath.Join(root, LegacyCalibratedWorkspaceManifestName), conflicting, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyStandaloneSchemaMigration(apply); err == nil {
		t.Fatal("completed migration accepted conflicting legacy source bytes")
	}
}
