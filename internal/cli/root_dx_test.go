package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

// TestWriteErrorJSON locks the machine-readable error contract: with JSON output
// (the default) a failed command prints a single {"error","code"} object so a
// script can parse stderr the same way it parses stdout.
func TestWriteErrorJSON(t *testing.T) {
	var buf bytes.Buffer
	err := fmt.Errorf("%w: Confluence URL not set", domain.ErrConfig)
	writeError(&buf, "json", err, exitConfig)

	var got struct {
		Error string `json:"error"`
		Code  int    `json:"code"`
	}
	if e := json.Unmarshal(buf.Bytes(), &got); e != nil {
		t.Fatalf("stderr is not valid JSON: %v (raw=%q)", e, buf.String())
	}
	if got.Code != exitConfig {
		t.Errorf("code = %d, want %d", got.Code, exitConfig)
	}
	if !strings.Contains(got.Error, "Confluence URL not set") {
		t.Errorf("error = %q, want it to contain the message", got.Error)
	}
}

// TestWriteErrorText keeps the human-friendly `error: <msg>` line under -o text.
func TestWriteErrorText(t *testing.T) {
	var buf bytes.Buffer
	writeError(&buf, "text", errors.New("boom"), exitGeneric)
	if got := buf.String(); got != "error: boom\n" {
		t.Errorf("text error = %q, want %q", got, "error: boom\n")
	}
}

// TestMirrorRootDefault verifies ATL_MIRROR_ROOT overrides the per-command
// fallback, and the fallback is used when the env var is unset/empty.
func TestMirrorRootDefault(t *testing.T) {
	t.Setenv("ATL_MIRROR_ROOT", "")
	if got := mirrorRootDefault("mirror"); got != "mirror" {
		t.Errorf("unset: got %q, want fallback %q", got, "mirror")
	}
	if got := mirrorRootDefault("mirror-jira"); got != "mirror-jira" {
		t.Errorf("unset jira: got %q, want fallback %q", got, "mirror-jira")
	}

	t.Setenv("ATL_MIRROR_ROOT", "/home/me/.atl/payments")
	if got := mirrorRootDefault("mirror"); got != "/home/me/.atl/payments" {
		t.Errorf("set: got %q, want the env value", got)
	}
	if got := mirrorRootDefault("mirror-jira"); got != "/home/me/.atl/payments" {
		t.Errorf("set jira: got %q, want the env value (one root for the workspace)", got)
	}

	// A whitespace-only value is treated as unset, not as a literal " " dir.
	t.Setenv("ATL_MIRROR_ROOT", "   ")
	if got := mirrorRootDefault("mirror"); got != "mirror" {
		t.Errorf("whitespace-only: got %q, want fallback %q", got, "mirror")
	}
}

// TestWarnIfTruncated is the CLI-layer guard for the headline CQL-cap behavior:
// a truncated pull writes exactly one `warning:` line to the given (stderr)
// writer, and a complete pull writes nothing — so a regression that drops the
// warning, or misroutes it, fails here.
func TestWarnIfTruncated(t *testing.T) {
	var buf bytes.Buffer
	warnIfTruncated(&buf, &app.PullResult{Truncated: true, TruncatedAt: 1000})
	got := buf.String()
	if !strings.HasPrefix(got, "warning:") || !strings.Contains(got, "truncated at 1000") {
		t.Errorf("truncated pull: warning = %q, want a `warning: … truncated at 1000 …` line", got)
	}

	buf.Reset()
	warnIfTruncated(&buf, &app.PullResult{}) // complete pull
	if buf.Len() != 0 {
		t.Errorf("complete pull wrote %q to stderr, want nothing", buf.String())
	}
}

// TestCorruptCredentialsExitGeneric pins the review decision that a corrupt
// credentials file is a genuine error (exit 1), NOT "not configured" (exit 7):
// only an absent token maps to 7. The URL is set and https, so the failure comes
// from auth.Token unmarshaling the garbage store, before any HTTP call.
func TestCorruptCredentialsExitGeneric(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte("}{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// runCLIFull neutralizes ambient *_PAT, so resolution falls through to the
	// (corrupt) credentials file in our temp config dir.
	env := map[string]string{
		"ATL_CONFIG_DIR":     dir,
		"ATL_CONFLUENCE_URL": "https://confluence.example.com",
	}
	out, code := runCLI(t, env, "conf", "page", "meta", "--id", "1")
	if code != exitGeneric {
		t.Fatalf("corrupt credentials: exit %d, want %d (generic, not config/auth)", code, exitGeneric)
	}
	if out != "" {
		t.Errorf("corrupt credentials: stdout = %q, want empty", out)
	}
}
