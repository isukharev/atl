package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/backendid"
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

func seedCLICorpusJira(t *testing.T, root string) {
	t.Helper()
	m := mirror.New(root)
	origin, err := backendid.OriginSHA256("https://backend.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.BindBackend(mirror.BackendBinding{Service: mirror.CorpusSnapshotJira, OriginSHA256: origin}); err != nil {
		t.Fatal(err)
	}
	body := []byte("h1. Synthetic")
	state := mirror.SyncState{ID: "EX-1", Hash: mirror.Hash(body), Path: "EX/EX-1.wiki"}
	if err := m.SaveBaseExt(state.ID, body, ".wiki"); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(map[string]any{
		"key": "EX-1", "id": "10001", "fields": map[string]any{"summary": "Synthetic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "EX", "EX-1.json")
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
