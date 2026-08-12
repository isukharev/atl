package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/mirror"
)

func TestCorpusExportBypassesConfigCredentialsAndSelfUpdate(t *testing.T) {
	mirrorRoot := t.TempDir()
	seedCLICorpusJira(t, mirrorRoot)
	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	configRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "credentials.json"), []byte("credential canary"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	env := map[string]string{
		"ATL_CONFIG_DIR": configRoot,
		"ATL_UPDATE_URL": server.URL,
		"ATL_NO_UPDATE":  "",
	}
	out, _, execErr := executeCLIRaw(t, env, "corpus", "export", "--jira", mirrorRoot, "--store", storeRoot, "--initialize-store")
	code := exitOK
	if execErr != nil {
		code = codeFor(execErr)
	}
	if code != exitOK {
		t.Fatalf("corpus export exit=%d error=%v output=%s", code, execErr, out)
	}
	var result struct {
		SchemaVersion int  `json:"schema_version"`
		Reused        bool `json:"reused"`
		Projection    struct {
			Readiness string `json:"readiness"`
		} `json:"projection"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if result.SchemaVersion != 2 || result.Reused || result.Projection.Readiness != "partial" || requests.Load() != 0 {
		t.Fatalf("result=%#v requests=%d", result, requests.Load())
	}
	for _, private := range []string{mirrorRoot, storeRoot, "Synthetic", "backend.example.test", "credential canary"} {
		if strings.Contains(out, private) {
			t.Fatalf("content-free output contains private canary %q: %s", private, out)
		}
	}

	out, _, execErr = executeCLIRaw(t, env, "corpus", "export", "--jira", mirrorRoot, "--store", storeRoot)
	code = exitOK
	if execErr != nil {
		code = codeFor(execErr)
	}
	if code != exitOK {
		t.Fatalf("idempotent corpus export exit=%d output=%s", code, out)
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || !result.Reused || requests.Load() != 0 {
		t.Fatalf("idempotent result=%#v err=%v requests=%d", result, err, requests.Load())
	}
}

func TestCorpusExportRejectsStrayArgumentsBeforeLocalState(t *testing.T) {
	out, _, err := executeCLIRaw(t, nil, "corpus", "export", "unexpected")
	if err == nil || codeFor(err) != exitUsage || strings.Contains(out, "unexpected") || strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("stray argument output=%q error=%v", out, err)
	}
}

func TestCorpusDiffIsContentFreeZeroEgressAndWritesOnlyExplicitPrivateArtifact(t *testing.T) {
	storeRoot := seedCLICorpusQualifiedDiffStore(t)
	configRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	env := map[string]string{"ATL_CONFIG_DIR": configRoot, "ATL_UPDATE_URL": server.URL, "ATL_NO_UPDATE": ""}

	out, _, execErr := executeCLIRaw(t, env, "corpus", "diff", "--store", storeRoot)
	if execErr != nil {
		t.Fatalf("corpus diff error=%v output=%s", execErr, out)
	}
	var result app.CorpusGenerationDiffResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if result.SchemaVersion != app.CorpusGenerationDiffSchemaV1 || result.Qualification != "qualified" ||
		result.Counts.Added != 1 || result.Counts.Tombstoned != 1 || result.IdentityArtifactWritten || requests.Load() != 0 {
		t.Fatalf("result=%#v requests=%d", result, requests.Load())
	}
	for _, private := range []string{storeRoot, "SECRET-OLD", "PRIVATE/SECRET-OLD.wiki", "credential canary"} {
		if strings.Contains(out, private) {
			t.Fatalf("content-free output contains %q: %s", private, out)
		}
	}

	artifactDir := t.TempDir()
	if err := os.Chmod(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(artifactDir, "identities.json")
	out, _, execErr = executeCLIRaw(t, env, "corpus", "diff", "--store", storeRoot, "--identity-artifact", artifactPath)
	if execErr != nil {
		t.Fatalf("artifact diff error=%v output=%s", execErr, out)
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || !result.IdentityArtifactWritten || strings.Contains(out, artifactPath) {
		t.Fatalf("artifact result=%#v error=%v output=%s", result, err, out)
	}
	artifactBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := corpus.ParseGenerationDiffArtifact(artifactBytes, corpus.Limits{})
	if err != nil || artifact.Counts.Added != 1 || artifact.Counts.Tombstoned != 1 {
		t.Fatalf("artifact=%#v error=%v", artifact, err)
	}
	if info, err := os.Stat(artifactPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact mode=%v error=%v", info, err)
	}

	out, _, execErr = executeCLIRaw(t, env, "corpus", "diff", "--store", storeRoot, "--identity-artifact", artifactPath)
	if execErr == nil || codeFor(execErr) != exitCheckFailed || out != "" || strings.Contains(execErr.Error(), artifactPath) {
		t.Fatalf("exclusive refusal output=%q error=%v", out, execErr)
	}
	preserved, err := os.ReadFile(artifactPath)
	if err != nil || string(preserved) != string(artifactBytes) {
		t.Fatalf("artifact was replaced error=%v", err)
	}
}

func TestCorpusDiffRejectsStrayArgumentsBeforeLocalState(t *testing.T) {
	out, _, err := executeCLIRaw(t, nil, "corpus", "diff", "private-canary")
	if err == nil || codeFor(err) != exitUsage || strings.Contains(out, "private-canary") || strings.Contains(err.Error(), "private-canary") {
		t.Fatalf("stray argument output=%q error=%v", out, err)
	}
}

func TestCorpusHandoffIsContentFreeZeroEgressAndExplicit(t *testing.T) {
	storeRoot := seedCLICorpusQualifiedDiffStore(t)
	configRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	env := map[string]string{"ATL_CONFIG_DIR": configRoot, "ATL_UPDATE_URL": server.URL, "ATL_NO_UPDATE": ""}

	out, _, execErr := executeCLIRaw(t, env, "corpus", "handoff", "--store", storeRoot)
	if execErr != nil {
		t.Fatalf("corpus handoff error=%v output=%s", execErr, out)
	}
	var result app.CorpusHandoffResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.Qualification != "sealed" ||
		result.HandoffArtifactWritten || requests.Load() != 0 {
		t.Fatalf("result=%#v error=%v requests=%d", result, err, requests.Load())
	}
	for _, private := range []string{storeRoot, "SECRET-OLD", "PRIVATE/SECRET-OLD.wiki", "credential canary"} {
		if strings.Contains(out, private) {
			t.Fatalf("content-free output contains %q: %s", private, out)
		}
	}

	artifactRoot := t.TempDir()
	if err := os.Chmod(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(artifactRoot, "handoff.json")
	out, _, execErr = executeCLIRaw(t, env, "corpus", "handoff", "--store", storeRoot, "--handoff-artifact", artifactPath)
	if execErr != nil {
		t.Fatalf("artifact handoff error=%v output=%s", execErr, out)
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || !result.HandoffArtifactWritten || strings.Contains(out, artifactPath) {
		t.Fatalf("artifact result=%#v error=%v output=%s", result, err, out)
	}
	artifactBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := corpus.ParseIndexerHandoff(artifactBytes, corpus.Limits{})
	if err != nil || handoff.Documents.StableID != corpus.IndexerDocumentsStableID {
		t.Fatalf("handoff=%#v error=%v", handoff, err)
	}
	if _, _, execErr = executeCLIRaw(t, env, "corpus", "handoff", "--store", storeRoot, "--handoff-artifact", artifactPath); execErr == nil || codeFor(execErr) != exitCheckFailed {
		t.Fatalf("exclusive artifact error=%v", execErr)
	}
}

func seedCLICorpusJira(t *testing.T, root string) {
	t.Helper()
	seedCLICorpusJiraItem(t, root, "EX-1", "10001", "EX/EX-1.wiki", "Synthetic")
}

func seedCLICorpusJiraItem(t *testing.T, root, key, providerID, relative, title string) {
	t.Helper()
	m := mirror.New(root)
	origin, err := backendid.OriginSHA256("https://backend.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.BindBackend(mirror.BackendBinding{Service: mirror.CorpusSnapshotJira, OriginSHA256: origin}); err != nil {
		t.Fatal(err)
	}
	body := []byte("h1. " + title)
	state := mirror.SyncState{ID: key, Hash: mirror.Hash(body), Path: relative}
	if err := m.SaveBaseExt(state.ID, body, ".wiki"); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(map[string]any{
		"key": key, "id": providerID, "fields": map[string]any{"summary": title},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, strings.TrimSuffix(filepath.FromSlash(relative), ".wiki")+".json")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, append(metadata, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	batch, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	batch.Record(state)
	if err := batch.Flush(); err != nil {
		t.Fatal(err)
	}
}

func seedCLICorpusQualifiedDiffStore(t *testing.T) string {
	t.Helper()
	beforeRoot := t.TempDir()
	seedCLICorpusJiraItem(t, beforeRoot, "SECRET-OLD", "10001", "PRIVATE/SECRET-OLD.wiki", "Private before")
	afterRoot := t.TempDir()
	seedCLICorpusJiraItem(t, afterRoot, "SECRET-NEW", "10002", "PRIVATE/SECRET-NEW.wiki", "Private after")
	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	beforeCapture := cliCorpusCaptureReceipt(t, beforeRoot)
	if _, err := app.ExportCorpus(context.Background(), app.CorpusExportOptions{
		JiraRoot: beforeRoot, StoreRoot: storeRoot, InitializeStore: true,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		CaptureReceipts: []corpus.CaptureReceipt{beforeCapture},
	}); err != nil {
		t.Fatal(err)
	}
	afterCapture := cliCorpusCaptureReceipt(t, afterRoot)
	if _, err := app.ExportCorpus(context.Background(), app.CorpusExportOptions{
		JiraRoot: afterRoot, StoreRoot: storeRoot,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
		CaptureReceipts: []corpus.CaptureReceipt{afterCapture},
	}); err != nil {
		t.Fatal(err)
	}
	return storeRoot
}

func cliCorpusCaptureReceipt(t *testing.T, root string) corpus.CaptureReceipt {
	t.Helper()
	snapshot, err := mirror.New(root).BeginCorpusSnapshot(mirror.CorpusSnapshotJira, mirror.CorpusSnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	providerIDs := make([]string, 0, snapshot.Len())
	for _, item := range snapshot.Inventory() {
		providerIDs = append(providerIDs, item.ProviderID)
	}
	selection, err := corpus.CaptureSelectionDigest(corpus.ServiceJira, providerIDs)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := corpus.PrincipalScopeDigest(corpus.ServiceJira, snapshot.OriginSHA256(), "synthetic-user")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	receipt, err := corpus.BuildCaptureReceipt(corpus.CaptureReceiptInput{
		Service: corpus.ServiceJira, ScopeDigest: scope,
		SelectorDigest: strings.Repeat("a", 64), OptionsDigest: strings.Repeat("b", 64),
		SelectionDigest: selection, SnapshotDigest: snapshot.Fingerprint(),
		StartedAt: started, CompletedAt: started.Add(time.Minute), Total: snapshot.Len(), Completed: snapshot.Len(),
		Dimensions: []corpus.CaptureDimensionEvidence{
			{Dimension: corpus.CaptureAttachments, State: corpus.CaptureNotRequested},
			{Dimension: corpus.CaptureComments, State: corpus.CaptureNotRequested},
			{Dimension: corpus.CaptureMetadata, State: corpus.CaptureComplete},
			{Dimension: corpus.CaptureNative, State: corpus.CaptureComplete},
		},
	}, corpus.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
