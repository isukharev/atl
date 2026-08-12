package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
)

func TestPrepareCorpusHandoffWritesOnlyExplicitPrivateArtifact(t *testing.T) {
	mirrorRoot := t.TempDir()
	seedCorpusExportJira(t, mirrorRoot, "EX-1", "10001", "EX/EX-1.wiki", []byte("synthetic body"), map[string]any{"summary": "Synthetic"})
	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	exported, err := ExportCorpus(context.Background(), CorpusExportOptions{
		JiraRoot: mirrorRoot, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		CaptureReceipts: []corpus.CaptureReceipt{corpusExportCaptureReceipt(t, corpus.ServiceJira, mirrorRoot)},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := PrepareCorpusHandoff(context.Background(), CorpusHandoffOptions{StoreRoot: storeRoot})
	if err != nil || result.Qualification != "sealed" || result.HandoffArtifactWritten ||
		result.Generation.GenerationDigest != exported.Generation.GenerationDigest {
		t.Fatalf("result=%#v error=%v", result, err)
	}

	artifactRoot := t.TempDir()
	if err := os.Chmod(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(artifactRoot, "handoff.json")
	result, err = PrepareCorpusHandoff(context.Background(), CorpusHandoffOptions{
		StoreRoot: storeRoot, HandoffArtifact: artifactPath,
	})
	if err != nil || !result.HandoffArtifactWritten {
		t.Fatalf("artifact result=%#v error=%v", result, err)
	}
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := corpus.ParseIndexerHandoff(data, corpus.Limits{})
	if err != nil || handoff.GenerationDigest != exported.Generation.GenerationDigest ||
		handoff.Documents.StableID != corpus.IndexerDocumentsStableID {
		t.Fatalf("handoff=%#v error=%v", handoff, err)
	}
	if info, statErr := os.Stat(artifactPath); statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact mode=%v error=%v", info, statErr)
	}
	if _, err := PrepareCorpusHandoff(context.Background(), CorpusHandoffOptions{
		StoreRoot: storeRoot, HandoffArtifact: artifactPath,
	}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("replacement error=%v", err)
	}
}

func TestPrepareCorpusHandoffRejectsStructuralGenerationWithoutArtifact(t *testing.T) {
	mirrorRoot := t.TempDir()
	seedCorpusExportJira(t, mirrorRoot, "EX-1", "10001", "EX/EX-1.wiki", []byte("synthetic body"), map[string]any{"summary": "Synthetic"})
	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportCorpus(context.Background(), CorpusExportOptions{
		JiraRoot: mirrorRoot, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
	}); err != nil {
		t.Fatal(err)
	}
	artifactRoot := t.TempDir()
	if err := os.Chmod(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(artifactRoot, "handoff.json")
	result, err := PrepareCorpusHandoff(context.Background(), CorpusHandoffOptions{
		StoreRoot: storeRoot, HandoffArtifact: artifactPath,
	})
	if result != nil || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if _, statErr := os.Lstat(artifactPath); !os.IsNotExist(statErr) {
		t.Fatalf("structural handoff artifact exists: %v", statErr)
	}
}

func TestPrepareCorpusHandoffRefusesArtifactInsideSealedStore(t *testing.T) {
	mirrorRoot := t.TempDir()
	seedCorpusExportJira(t, mirrorRoot, "EX-1", "10001", "EX/EX-1.wiki", []byte("synthetic body"), map[string]any{"summary": "Synthetic"})
	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportCorpus(context.Background(), CorpusExportOptions{
		JiraRoot: mirrorRoot, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		CaptureReceipts: []corpus.CaptureReceipt{corpusExportCaptureReceipt(t, corpus.ServiceJira, mirrorRoot)},
	}); err != nil {
		t.Fatal(err)
	}
	targets := []string{filepath.Join(storeRoot, "handoff.json")}
	aliasRoot := t.TempDir()
	if err := os.Chmod(aliasRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(aliasRoot, "store-alias")
	if err := os.Symlink(storeRoot, alias); err == nil {
		targets = append(targets, filepath.Join(alias, "aliased-handoff.json"))
	}
	for _, target := range targets {
		result, err := PrepareCorpusHandoff(context.Background(), CorpusHandoffOptions{
			StoreRoot: storeRoot, HandoffArtifact: target,
		})
		if result != nil || !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("target=%q result=%#v error=%v", target, result, err)
		}
		if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
			t.Fatalf("refused artifact exists: %v", statErr)
		}
	}
	store, err := corpus.Open(storeRoot, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	generation, err := store.SelectCurrent(context.Background())
	if err != nil {
		t.Fatalf("sealed generation was corrupted: %v", err)
	}
	_ = generation.Close()
	_ = store.Close()
}
