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
