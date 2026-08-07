package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

// errWriter fails every write, standing in for stdout that ran out of space or
// otherwise could not accept a complete result.
type errWriter struct{ cause error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.cause }

// runCLIWithFailingStdout runs the root command in-process with the same env
// isolation as runCLIFull but with a stdout writer that always fails, and
// returns the command's error for exit-code and cause assertions.
func runCLIWithFailingStdout(t *testing.T, cause error, args ...string) error {
	return runCLIWithFailingStdoutEnv(t, nil, cause, args...)
}

func runCLIWithFailingStdoutEnv(t *testing.T, env map[string]string, cause error, args ...string) error {
	t.Helper()
	t.Setenv("ATL_NO_UPDATE", "1")
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	for _, k := range []string{
		"ATL_CONFLUENCE_URL", "CONFLUENCE_URL", "ATL_JIRA_URL", "JIRA_URL",
		"ATL_CONFLUENCE_CA_BUNDLE", "ATL_JIRA_CA_BUNDLE",
		"ATL_CONFLUENCE_PAT", "CONFLUENCE_PAT", "ATL_JIRA_PAT", "JIRA_PAT",
		"ATL_MIRROR_ROOT", "ATL_ALLOW_INSECURE", "ATL_READ_ONLY",
	} {
		t.Setenv(k, "")
	}
	for key, value := range env {
		t.Setenv(key, value)
	}
	root := newRoot()
	root.SetArgs(args)
	root.SetOut(errWriter{cause: cause})
	root.SetErr(io.Discard)
	return root.ExecuteContext(context.Background())
}

// withFormat sets the package-level output format for the duration of a test.
// Not safe with t.Parallel (mutates a package var) — intentionally serial.
func withFormat(t *testing.T, f string) {
	t.Helper()
	old := outputFormat
	outputFormat = f
	t.Cleanup(func() { outputFormat = old })
}

func TestEmitID_PrintsIdentifiersOnly(t *testing.T) {
	withFormat(t, "id")
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := emitID(cmd, map[string]any{"ignored": true}, nil, func() []string {
		return []string{"ML-1", "ML-2"}
	})
	if err != nil {
		t.Fatalf("emitID: %v", err)
	}
	if got, want := buf.String(), "ML-1\nML-2\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEmitID_FallsBackToJSONWhenNotIDFormat(t *testing.T) {
	withFormat(t, "json")
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := emitID(cmd, map[string]any{"key": "ML-1"}, nil, func() []string { return []string{"ML-1"} })
	if err != nil {
		t.Fatalf("emitID: %v", err)
	}
	if !strings.Contains(buf.String(), `"key": "ML-1"`) {
		t.Fatalf("expected JSON body, got %q", buf.String())
	}
}

func TestEmitIDPropagatesWriterFailure(t *testing.T) {
	withFormat(t, "id")
	cause := errors.New("id stdout unavailable")
	cmd := &cobra.Command{}
	cmd.SetOut(errWriter{cause: cause})

	err := emitID(cmd, map[string]any{"ignored": true}, nil, func() []string { return []string{"ML-1"} })
	if !errors.Is(err, cause) {
		t.Fatalf("emitID error=%v, want writer cause", err)
	}
}

func TestEmitTextPropagatesWriterFailure(t *testing.T) {
	withFormat(t, "text")
	cause := errors.New("text stdout unavailable")
	cmd := &cobra.Command{}
	cmd.SetOut(errWriter{cause: cause})

	err := emit(cmd, map[string]any{"ignored": true}, func() string { return "result" })
	if !errors.Is(err, cause) {
		t.Fatalf("emit error=%v, want writer cause", err)
	}
}

func TestEmit_RejectsIDFormatWhenUnsupported(t *testing.T) {
	withFormat(t, "id")
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := emit(cmd, map[string]any{"x": 1}, nil)
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("want ErrUsage for unsupported -o id, got %v", err)
	}
}

func TestEmitRejectsTextFormatWhenUnsupported(t *testing.T) {
	withFormat(t, "text")
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := emit(cmd, map[string]any{"x": 1}, nil)
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("want ErrUsage for unsupported -o text, got %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("unsupported text emitted output: %q", buf.String())
	}
}

// A snapshot command emits its qualified aggregate before returning the
// inspection error, so a failed write of that aggregate must survive alongside
// the inspection cause instead of being dropped.
func TestSnapshotResultErrKeepsBothCauses(t *testing.T) {
	if got := snapshotResultErr(nil, nil); got != nil {
		t.Fatalf("no failure: got %v", got)
	}
	emitErr := errors.New("stdout unavailable")
	if got := snapshotResultErr(nil, emitErr); got != emitErr {
		t.Fatalf("emit-only: got %v", got)
	}
	snapshotErr := fmt.Errorf("%w: baseline mismatch", domain.ErrCheckFailed)
	if got := snapshotResultErr(snapshotErr, nil); got != snapshotErr {
		t.Fatalf("snapshot-only: got %v", got)
	}
	both := snapshotResultErr(snapshotErr, emitErr)
	if !errors.Is(both, domain.ErrCheckFailed) || !errors.Is(both, emitErr) {
		t.Fatalf("both: %v", both)
	}
	if code := codeFor(both); code != exitCheckFailed {
		t.Fatalf("both: code=%d err=%v", code, both)
	}
}

func TestCreatedRegistrationResultErrKeepsBothCauses(t *testing.T) {
	if got := createdRegistrationResultErr(nil, nil); got != nil {
		t.Fatalf("no failure: got %v", got)
	}
	emitErr := errors.New("stdout unavailable")
	if got := createdRegistrationResultErr(nil, emitErr); !errors.Is(got, domain.ErrCheckFailed) || !errors.Is(got, emitErr) || !strings.Contains(got.Error(), "do not replay") {
		t.Fatalf("emit-only: got %v", got)
	} else if codeFor(got) != exitCheckFailed {
		t.Fatalf("emit-only: code=%d err=%v", codeFor(got), got)
	}
	registrationErr := fmt.Errorf("%w: page 42 was created; do not replay", domain.ErrCheckFailed)
	if got := createdRegistrationResultErr(registrationErr, nil); got != registrationErr {
		t.Fatalf("registration-only: got %v", got)
	}
	both := createdRegistrationResultErr(registrationErr, emitErr)
	if !errors.Is(both, domain.ErrCheckFailed) || !errors.Is(both, emitErr) || !strings.Contains(both.Error(), "do not replay") {
		t.Fatalf("both: %v", both)
	}
	if code := codeFor(both); code != exitCheckFailed {
		t.Fatalf("both: code=%d err=%v", code, both)
	}
}

func TestUnsupportedTextIsRejectedBeforeCommandExecution(t *testing.T) {
	stdout, stderr, code := runCLIFull(t, nil, "-o", "text", "auth", "logout", "--service", "jira")
	if code != exitUsage {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout=%q", stdout)
	}
}

func TestHighValueTextProjections(t *testing.T) {
	restricted := true
	meta := confluencePageMetaText(&domain.PageMeta{
		ID: "42", Title: "Plan", Space: "ENG", Version: 7, Ancestors: []string{"Home", "Quarter"},
		Labels: []string{"roadmap", "reviewed"}, Restrictions: &restricted, Updated: "2026-07-12", URL: "https://example.invalid/pages/42",
	})
	versions := confluenceVersionsText([]domain.Version{{Number: 7, When: "2026-07-12", By: "Alex", Message: "reviewed"}})
	comments := commentsText([]domain.Comment{{ID: "99", Author: "Alex", Created: "2026-07-12", Body: "Looks good."}})
	fields := jiraFieldsText(&app.JiraFieldCatalogResult{
		SchemaVersion: 1, Projection: "full", Source: "jira-field-catalog", Complete: true,
		Total: 2, Count: 2, CustomCount: 1, SystemCount: 1,
		Fields: []domain.FieldDef{{ID: "summary", Name: "Summary"}, {ID: "customfield_10001", Name: "Delivery\nNotes", Custom: true, Schema: "string"}},
	})
	transitions := jiraTransitionsText([]domain.TransitionDef{{ID: "31", Name: "Start progress", To: "In Progress"}})
	assertGolden(t, "explicit_text_projections.txt", []byte(strings.Join([]string{
		"[confluence-meta]", meta,
		"[confluence-history]", versions,
		"[confluence-comments]", comments,
		"[jira-fields]", fields,
		"[jira-options]", stringLines([]string{"High\tPriority", "Low"}),
		"[jira-transitions]", transitions,
		"[jira-link-types]", stringLines([]string{"blocks", "relates to"}), "",
	}, "\n")))
}

func TestJiraFieldsTextKeepsPartialQualificationVisible(t *testing.T) {
	text := jiraFieldsText(&app.JiraFieldCatalogResult{
		SchemaVersion: 1, Projection: "full", Source: "legacy", Complete: false,
		PartialReason: "catalog\nnot qualified", Total: 1, Count: 1, SystemCount: 1,
		Fields: []domain.FieldDef{{ID: "summary", Name: "Summary"}},
	})
	if !strings.HasPrefix(text, "complete=false\tsource=legacy\tcount=1\ttotal=1\npartial_reason=catalog not qualified\n") {
		t.Fatalf("text=%q", text)
	}
}

func TestJiraFieldsTextSummaryKeepsDefinitionsHidden(t *testing.T) {
	text := jiraFieldsText(&app.JiraFieldCatalogResult{
		SchemaVersion: 1, Projection: "summary", Source: "jira-field-catalog",
		Complete: true, Total: 3, Count: 2, CustomCount: 1, SystemCount: 1,
		Fields: []domain.FieldDef{},
	})
	if text != "complete=true\tsource=jira-field-catalog\tcount=2\ttotal=3\nprojection=summary\tcustom=1\tsystem=1" {
		t.Fatalf("text=%q", text)
	}
}

// readBounded must reject an over-limit body loudly (exit 2), never truncate:
// a truncated Jira wiki body would be pushed as-is with no validation gate.
func TestReadBoundedRejectsOversizedInput(t *testing.T) {
	small, err := readBounded(strings.NewReader("abc"), 8)
	if err != nil || string(small) != "abc" {
		t.Fatalf("under limit: got %q, %v", small, err)
	}
	exact, err := readBounded(strings.NewReader("12345678"), 8)
	if err != nil || len(exact) != 8 {
		t.Fatalf("at limit: got %d bytes, %v", len(exact), err)
	}
	if _, err := readBounded(strings.NewReader("123456789"), 8); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("over limit: want ErrUsage, got %v", err)
	}
}
