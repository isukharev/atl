package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func TestConfPageCopyApplyGatesPrecedeInvalidConfig(t *testing.T) {
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := [][]string{
		{"conf", "page", "copy"},
		{"conf", "page", "copy", "--id", "10", "--title", "Copied", "--expected-version", "3"},
		{"conf", "page", "copy", "--id", "10", "--title", "Copied", "--apply", "--expected-proposal-hash", "hash"},
		{"conf", "page", "copy", "--id", "10", "--title", "Copied", "--apply", "--expected-version", "3"},
		{"conf", "page", "copy", "--id", "10", "--title", "Copied", "--register"},
		{"-o", "id", "conf", "page", "copy", "--id", "10", "--title", "Copied"},
	}
	for _, args := range tests {
		out, code := runCLI(t, map[string]string{"ATL_CONFIG_DIR": cfgDir}, args...)
		if code != exitUsage || out != "" {
			t.Fatalf("args=%v exit=%d out=%q, want usage before invalid config", args, code, out)
		}
	}
}

func TestConfPageCopyOutputFailureAfterAttemptIsNoReplayCheckFailure(t *testing.T) {
	cs := newConfServer(t)
	source := strings.Replace(pageJSON("10", "Source", 3, "<p>source body</p>"), `"body":`, `"ancestors":[],"body":`, 1)
	readback := strings.Replace(pageJSON("99", "Copied", 1, "<p>source body</p>"), `"body":`, `"ancestors":[],"body":`, 1)
	source = strings.Replace(source, `"title":`, `"status":"current","title":`, 1)
	readback = strings.Replace(readback, `"title":`, `"status":"current","title":`, 1)
	cs.gets = []cannedResp{
		{status: http.StatusOK, body: source},
		{status: http.StatusOK, body: source},
		{status: http.StatusOK, body: source},
		{status: http.StatusOK, body: readback},
	}
	cs.writes = []cannedResp{{status: http.StatusCreated, body: `{"id":"99"}`}}
	env := confEnv(cs.srv)
	previewOut, code := runCLI(t, env, "conf", "page", "copy", "--id", "10", "--title", "Copied")
	if code != exitOK {
		t.Fatalf("preview exit=%d output=%s", code, previewOut)
	}
	var preview app.ConfluencePageCopyResult
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("stdout unavailable")
	err := runCLIWithFailingStdoutEnv(t, env, cause, "conf", "page", "copy", "--id", "10", "--title", "Copied",
		"--apply", "--expected-version", "3", "--expected-proposal-hash", preview.ProposalHash)
	if !errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, cause) || !strings.Contains(err.Error(), "do not replay") {
		t.Fatalf("error=%v", err)
	}
	posts := 0
	for _, request := range cs.requests() {
		if request.method == http.MethodPost {
			posts++
		}
	}
	if posts != 1 {
		t.Fatalf("POSTs=%d, want 1", posts)
	}
}

func TestConfPageCopyIDModePreservesBlockedApplyClassification(t *testing.T) {
	cs := newConfServer(t)
	source := strings.Replace(pageJSON("10", "Source", 3, "body"), `"body":`, `"ancestors":[],"body":`, 1)
	source = strings.Replace(source, `"title":`, `"status":"current","title":`, 1)
	cs.gets = []cannedResp{{status: http.StatusOK, body: source}}
	out, code := runCLI(t, confEnv(cs.srv), "-o", "id", "conf", "page", "copy", "--id", "10", "--title", "Copied",
		"--apply", "--expected-version", "3", "--expected-proposal-hash", strings.Repeat("0", 64))
	if code != exitCheckFailed || out != "" {
		t.Fatalf("exit=%d output=%q, want check failure with no id output", code, out)
	}
	for _, request := range cs.requests() {
		if request.method == http.MethodPost {
			t.Fatal("blocked apply issued POST")
		}
	}
}
