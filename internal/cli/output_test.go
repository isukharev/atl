package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/domain"
)

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
	fields := jiraFieldsText([]domain.FieldDef{{ID: "summary", Name: "Summary"}, {ID: "customfield_10001", Name: "Delivery\nNotes", Custom: true, Schema: "string"}})
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
