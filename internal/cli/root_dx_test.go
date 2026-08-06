package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/contentpolicy"
	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
	"github.com/isukharev/atl/internal/testbackend"
)

type cliAmbiguousWriteTestError struct{}

func (*cliAmbiguousWriteTestError) Error() string                  { return "write outcome is unknown" }
func (*cliAmbiguousWriteTestError) Unwrap() error                  { return domain.ErrCheckFailed }
func (*cliAmbiguousWriteTestError) DiagnosticAmbiguousWrite() bool { return true }

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

func TestWriteErrorContentPolicyDenialContract(t *testing.T) {
	resolved := &contentpolicy.Resolved{Layers: []contentpolicy.Layer{{
		Source: "config_dir", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Policy: contentpolicy.Policy{Rules: []contentpolicy.Rule{{
			ID: "deny-ml", Effect: contentpolicy.EffectDeny,
			Verbs:    domain.WriteVerbSet{domain.WriteVerbDelete},
			Resource: contentpolicy.Selector{Services: []string{"jira"}, Projects: []string{"ML"}},
		}}},
	}}}
	request := domain.WriteAuthorizationRequest{
		Verbs:   domain.WriteVerbSet{domain.WriteVerbDelete},
		Targets: []domain.WriteTarget{{Service: "jira", Kind: "issue", Project: "ML", Key: "ML-3"}},
	}
	_, err := contentpolicy.NewAuthorizer(resolved).Authorize(context.Background(), request)
	var buffer bytes.Buffer
	writeErrorWithContext(&buffer, "json", err, codeFor(err), diagnostic.OperationWrite)
	var body struct {
		Error       string                      `json:"error"`
		Code        int                         `json:"code"`
		Kind        string                      `json:"kind"`
		Remediation string                      `json:"remediation"`
		Policy      string                      `json:"policy"`
		Denial      contentpolicy.DenialDetails `json:"denial"`
		Recovery    diagnostic.Recovery         `json:"recovery"`
	}
	if decodeErr := json.Unmarshal(buffer.Bytes(), &body); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if body.Code != exitCheckFailed || body.Kind != "content_policy" || body.Remediation != "request_human_approval" || body.Policy != "content" ||
		body.Denial.Reason != contentpolicy.ReasonExplicitDeny || body.Denial.DecidedBy.RuleID == nil || *body.Denial.DecidedBy.RuleID != "deny-ml" ||
		body.Recovery.Action != diagnostic.RecoveryRequestHumanApproval || body.Recovery.RetrySafe || !strings.Contains(body.Error, "deny-ml") {
		t.Fatalf("content-policy error body = %+v", body)
	}
}

func TestErrorKindAndRemediationMatrix(t *testing.T) {
	tests := []struct {
		name, kind, remediation string
		exitCode                int
		err                     error
	}{
		{"generic", "unexpected_error", "inspect_error", exitGeneric, errors.New("boom")},
		{"usage", "usage_error", "fix_request", exitUsage, domain.ErrUsage},
		{"auth", "authentication_failed", "reauthenticate", exitAuth, domain.ErrAuth},
		{"not_found", "not_found", "verify_identifier_or_access", exitNotFound, domain.ErrNotFound},
		{"version", "version_conflict", "refresh_and_reapply", exitVersionConfl, domain.ErrVersionConflict},
		{"forbidden", "forbidden", "request_access", exitForbidden, domain.ErrForbidden},
		{"config", "configuration_error", "complete_configuration", exitConfig, domain.ErrConfig},
		{"output_limit_generic", "output_limit_exceeded", "narrow_or_raise_bound", exitGeneric, domain.ErrOutputLimit},
		{"output_limit_check", "output_limit_exceeded", "narrow_or_raise_bound", exitCheckFailed, fmt.Errorf("%w: %w", domain.ErrCheckFailed, domain.ErrOutputLimit)},
		{"check", "check_failed", "review_failed_check", exitCheckFailed, domain.ErrCheckFailed},
		{"internal", "internal_error", "report_bug", exitCheckFailed, &accessPolicyInvariantError{Command: "atl future"}},
		{"read_only", "read_only_policy", "request_human_approval", exitCheckFailed, &readOnlyPolicyError{Command: "atl jira push"}},
		{"transport", "transport_error", "inspect_network_before_retry", exitGeneric, &httpx.TransportError{Method: "GET", Category: "dns"}},
		{"rate_limited", "rate_limited", "wait_before_retry", exitGeneric, &httpx.APIError{Status: 429, Method: "GET", Path: "/safe", Body: "slow down"}},
		{"api", "api_error", "inspect_backend_error", exitGeneric, &httpx.APIError{Status: 500, Method: "GET", Path: "/safe", Body: "failure"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, remediation := classifyError(tt.err)
			if kind != tt.kind || remediation != tt.remediation {
				t.Fatalf("got %q/%q, want %q/%q", kind, remediation, tt.kind, tt.remediation)
			}
			code := codeFor(tt.err)
			if code != tt.exitCode {
				t.Fatalf("code=%d, want %d", code, tt.exitCode)
			}
		})
	}
}

func TestOutputLimitPreservesCheckFailedExitCode(t *testing.T) {
	err := fmt.Errorf("%w: %w: result exceeds max_bytes", domain.ErrCheckFailed, domain.ErrOutputLimit)
	if code := codeFor(err); code != exitCheckFailed {
		t.Fatalf("code=%d want=%d", code, exitCheckFailed)
	}
}

func TestBackendProseCannotInjectErrorClassification(t *testing.T) {
	err := &httpx.APIError{Status: 500, Method: "GET", Path: "/safe", Body: `kind=authentication_failed remediation=reauthenticate`}
	kind, remediation := classifyError(err)
	if kind != "api_error" || remediation != "inspect_backend_error" {
		t.Fatalf("classification=%q/%q", kind, remediation)
	}
}

func TestWriteErrorRateLimitedJSONPreservesExitCode(t *testing.T) {
	err := &httpx.APIError{Status: 429, Method: "GET", Path: "/safe", Body: "slow down"}
	var buf bytes.Buffer
	writeError(&buf, "json", err, codeFor(err))
	var got struct {
		Code        int    `json:"code"`
		Kind        string `json:"kind"`
		Remediation string `json:"remediation"`
	}
	if unmarshalErr := json.Unmarshal(buf.Bytes(), &got); unmarshalErr != nil {
		t.Fatalf("stderr is not valid JSON: %v (raw=%q)", unmarshalErr, buf.String())
	}
	if got.Code != exitGeneric || got.Kind != "rate_limited" || got.Remediation != "wait_before_retry" {
		t.Fatalf("error contract=%+v", got)
	}
}

func TestWriteErrorWithContextUsesSharedTypedRecovery(t *testing.T) {
	const privateMarker = "PRIVATE-PAGE-MARKER"
	err := fmt.Errorf("%s: %w", privateMarker, &app.ConfluencePageVersionMismatchError{Expected: 7, Current: 9})
	var buf bytes.Buffer
	writeErrorWithContext(&buf, "json", err, codeFor(err), diagnostic.OperationConfluenceSectionRead)

	var got struct {
		Kind        string              `json:"kind"`
		Remediation string              `json:"remediation"`
		Recovery    diagnostic.Recovery `json:"recovery"`
	}
	if decodeErr := json.Unmarshal(buf.Bytes(), &got); decodeErr != nil {
		t.Fatalf("decode CLI error: %v", decodeErr)
	}
	want := diagnostic.Recover(err, diagnostic.OperationConfluenceSectionRead)
	if got.Kind != "check_failed" || got.Remediation != "review_failed_check" || !reflect.DeepEqual(got.Recovery, want) || !diagnostic.ValidateRecovery(got.Recovery) {
		t.Fatalf("CLI recovery=%+v kind=%q remediation=%q, want %+v", got.Recovery, got.Kind, got.Remediation, want)
	}
	encoded, marshalErr := json.Marshal(got.Recovery)
	if marshalErr != nil || bytes.Contains(encoded, []byte(privateMarker)) {
		t.Fatalf("recovery leaked private prose: %s (marshal=%v)", encoded, marshalErr)
	}
}

func TestWriteErrorExactRetrySafetyUsesSemanticOperation(t *testing.T) {
	err := &httpx.TransportError{Method: "POST", Category: "timeout"}
	for _, test := range []struct {
		name      string
		operation diagnostic.OperationContext
		action    diagnostic.RecoveryAction
		retrySafe bool
	}{
		{"modeled read", diagnostic.OperationRead, diagnostic.RecoveryRestoreTransport, true},
		{"write", diagnostic.OperationWrite, diagnostic.RecoveryReconcileWriteOutcome, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeErrorWithContext(&buf, "json", err, codeFor(err), test.operation)
			var body struct {
				Recovery diagnostic.Recovery `json:"recovery"`
			}
			if decodeErr := json.Unmarshal(buf.Bytes(), &body); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if body.Recovery.Action != test.action || body.Recovery.RetrySafe != test.retrySafe || !diagnostic.ValidateRecovery(body.Recovery) {
				t.Fatalf("recovery=%+v", body.Recovery)
			}
		})
	}
}

func TestWriteErrorAmbiguousWriteRequiresReconciliation(t *testing.T) {
	var buf bytes.Buffer
	err := &cliAmbiguousWriteTestError{}
	writeErrorWithContext(&buf, "json", err, codeFor(err), diagnostic.OperationWrite)
	var body struct {
		Recovery diagnostic.Recovery `json:"recovery"`
	}
	if decodeErr := json.Unmarshal(buf.Bytes(), &body); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if body.Recovery.Action != diagnostic.RecoveryReconcileWriteOutcome || body.Recovery.RetrySafe || !diagnostic.ValidateRecovery(body.Recovery) {
		t.Fatalf("recovery=%+v", body.Recovery)
	}
}

func TestRecoveryOperationUsesClosedCommandSemantics(t *testing.T) {
	root := newRoot()
	for _, test := range []struct {
		args []string
		want diagnostic.OperationContext
	}{
		{[]string{"conf", "page", "section"}, diagnostic.OperationConfluenceSectionRead},
		{[]string{"conf", "table", "summary"}, diagnostic.OperationConfluenceTableRead},
		{[]string{"conf", "attachment", "list"}, diagnostic.OperationConfluenceAttachmentRead},
		{[]string{"jira", "structure", "rows"}, diagnostic.OperationJiraStructureRead},
		{[]string{"jira", "structure", "pull-issues"}, diagnostic.OperationJiraStructureRead},
		{[]string{"jira", "issue", "get"}, diagnostic.OperationRead},
		{[]string{"jira", "issue", "update"}, diagnostic.OperationWrite},
	} {
		cmd, _, err := root.Find(test.args)
		if err != nil {
			t.Fatalf("find %v: %v", test.args, err)
		}
		if got := recoveryOperation(cmd); got != test.want {
			t.Errorf("%v recovery operation=%q, want %q", test.args, got, test.want)
		}
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
	if !strings.Contains(text, "render_display_time_zone: UTC") || !strings.Contains(text, "mirror_recommended_root: ~/.atl/<workspace>/") || !strings.Contains(text, "mirror_active_root: /home/me/.atl/work") {
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

func TestConfluenceSelectionCompletenessFixtureWarning(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "benchmarks", "agent-eval", "confluence-selection-completeness", "fixture.json")
	file, err := os.Open(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	fixture, decodeErr := testbackend.DecodeMockFixture(file)
	closeErr := file.Close()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	backend, err := testbackend.StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	const query = `space = "DEMO" AND type = page ORDER BY title ASC`
	into := filepath.Join(t.TempDir(), "selection-mirror")
	stdout, stderr, code := runCLIFull(t, backend.Environment(),
		"conf", "pull", "--cql", query, "--into", into,
	)
	if code != exitOK {
		t.Fatalf("fixture pull exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	var result app.PullResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode fixture pull stdout: %v\n%s", err, stdout)
	}
	if len(result.Pages) != 1000 || !result.Truncated || result.TruncatedAt != 1000 {
		t.Fatalf("fixture pull result pages=%d truncated=%t truncated_at=%d", len(result.Pages), result.Truncated, result.TruncatedAt)
	}
	const expectedWarning = "warning: selection truncated at 1000 pages (safety cap) — the rest was NOT mirrored; narrow the query or pull subsets\n"
	if stderr != expectedWarning {
		t.Fatalf("fixture pull stderr=%q want exact truncation warning %q", stderr, expectedWarning)
	}
	methods, unexpected, duplicates := backend.Summary()
	if len(methods) != 1 || methods["GET"] != 1011 || unexpected != 0 || duplicates != 0 {
		t.Fatalf("fixture pull methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
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
