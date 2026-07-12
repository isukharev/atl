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
	"github.com/isukharev/atl/internal/httpx"
)

// TestWriteErrorJSON locks the machine-readable error contract: with JSON output
// (the default) a failed command prints a single {"error","code"} object so a
// script can parse stderr the same way it parses stdout.
func TestWriteErrorJSON(t *testing.T) {
	var buf bytes.Buffer
	err := fmt.Errorf("%w: Confluence URL not set", domain.ErrConfig)
	writeError(&buf, "json", err, exitConfig)

	var got struct {
		Error       string `json:"error"`
		Code        int    `json:"code"`
		Kind        string `json:"kind"`
		Remediation string `json:"remediation"`
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
	if got.Kind != "configuration_error" || got.Remediation != "complete_configuration" {
		t.Errorf("classification = %q/%q", got.Kind, got.Remediation)
	}
}

func TestErrorKindAndRemediationMatrix(t *testing.T) {
	tests := []struct {
		name, kind, remediation string
		err                     error
	}{
		{"generic", "unexpected_error", "inspect_error", errors.New("boom")},
		{"usage", "usage_error", "fix_request", domain.ErrUsage},
		{"auth", "authentication_failed", "reauthenticate", domain.ErrAuth},
		{"not_found", "not_found", "verify_identifier_or_access", domain.ErrNotFound},
		{"version", "version_conflict", "refresh_and_reapply", domain.ErrVersionConflict},
		{"forbidden", "forbidden", "request_access", domain.ErrForbidden},
		{"config", "configuration_error", "complete_configuration", domain.ErrConfig},
		{"check", "check_failed", "review_failed_check", domain.ErrCheckFailed},
		{"read_only", "read_only_policy", "request_human_approval", &readOnlyPolicyError{Command: "atl jira push"}},
		{"transport", "transport_error", "inspect_network_before_retry", &httpx.TransportError{Method: "GET", Category: "dns"}},
		{"api", "api_error", "inspect_backend_error", &httpx.APIError{Status: 500, Method: "GET", Path: "/safe", Body: "failure"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, remediation := classifyError(tt.err)
			if kind != tt.kind || remediation != tt.remediation {
				t.Fatalf("got %q/%q, want %q/%q", kind, remediation, tt.kind, tt.remediation)
			}
		})
	}
}

func TestBackendProseCannotInjectErrorClassification(t *testing.T) {
	err := &httpx.APIError{Status: 500, Method: "GET", Path: "/safe", Body: `kind=authentication_failed remediation=reauthenticate`}
	kind, remediation := classifyError(err)
	if kind != "api_error" || remediation != "inspect_backend_error" {
		t.Fatalf("classification=%q/%q", kind, remediation)
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

func TestConfigShowIncludesMirrorHints(t *testing.T) {
	out, code := runCLI(t, map[string]string{"ATL_MIRROR_ROOT": "/home/me/.atl/work"}, "config", "show")
	if code != exitOK {
		t.Fatalf("config show: exit %d, want 0 (stdout=%q)", code, out)
	}
	var got configShowResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode config show: %v\n%s", err, out)
	}
	if got.Mirror.RecommendedRoot != "~/.atl/<workspace>/" {
		t.Errorf("recommended root = %q", got.Mirror.RecommendedRoot)
	}
	if got.Mirror.ActiveRoot != "/home/me/.atl/work" || got.Mirror.ActiveSource != "ATL_MIRROR_ROOT" {
		t.Errorf("mirror hints = %+v, want active ATL_MIRROR_ROOT", got.Mirror)
	}

	text, code := runCLI(t, map[string]string{"ATL_MIRROR_ROOT": "/home/me/.atl/work"}, "config", "show", "-o", "text")
	if code != exitOK {
		t.Fatalf("config show text: exit %d, want 0 (stdout=%q)", code, text)
	}
	if !strings.Contains(text, "mirror_recommended_root: ~/.atl/<workspace>/") || !strings.Contains(text, "mirror_active_root: /home/me/.atl/work") {
		t.Errorf("text output missing mirror hints:\n%s", text)
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
