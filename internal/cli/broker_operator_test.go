package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/domain"
)

func TestBrokerOperatorReadOnlyGatePrecedesConfiguration(t *testing.T) {
	previousLoad, previousInitialize := loadBrokerRuntime, initializeBrokerJournal
	defer func() { loadBrokerRuntime, initializeBrokerJournal = previousLoad, previousInitialize }()
	loads, initializes := 0, 0
	loadBrokerRuntime = func(string, string, *cobra.Command) (brokerRunner, error) {
		loads++
		return &brokerRunnerStub{}, nil
	}
	initializeBrokerJournal = func(string) error { initializes++; return nil }
	for _, environment := range []map[string]string{nil, {"ATL_READ_ONLY": "1"}} {
		for _, args := range [][]string{
			{"broker", "serve", "--enable-jira-comments", "--config", "missing.json"},
			{"broker", "journal", "initialize", "--config", "missing.json"},
		} {
			if environment == nil {
				args = append(args, "--read-only")
			}
			stdout, _, err := executeCLIRaw(t, environment, args...)
			var refusal *readOnlyPolicyError
			if stdout != "" || !errors.As(err, &refusal) || loads != 0 || initializes != 0 {
				t.Fatalf("args=%v output=%q err=%v loads=%d initializes=%d", args, stdout, err, loads, initializes)
			}
		}
	}
	if _, code := runCLI(t, nil, "broker", "serve", "--config", "missing.json", "--read-only"); code != exitOK || loads != 1 {
		t.Fatalf("read-only server blocked: code=%d loads=%d", code, loads)
	}
}

func TestBrokerJournalInitializeUsesOnlyExplicitOperatorConfiguration(t *testing.T) {
	previous := initializeBrokerJournal
	defer func() { initializeBrokerJournal = previous }()
	called := ""
	initializeBrokerJournal = func(path string) error { called = path; return nil }
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "config.json"), []byte("}{"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, stderr, code := runCLIFull(t, map[string]string{"ATL_CONFIG_DIR": directory}, "broker", "journal", "initialize", "--config", "private-broker.json")
	if code != exitOK || called != "private-broker.json" || stderr != "" || !strings.Contains(output, `"status": "initialized"`) || strings.Contains(output, called) {
		t.Fatalf("code=%d called=%q output=%q stderr=%q", code, called, output, stderr)
	}
	for _, args := range [][]string{
		{"broker", "journal", "initialize"},
		{"broker", "journal", "initialize", "--config", "one", "--config", "two"},
	} {
		called = ""
		if output, code := runCLI(t, nil, args...); code != exitUsage || output != "" || called != "" {
			t.Fatalf("args=%v code=%d output=%q called=%q", args, code, output, called)
		}
	}
	initializeBrokerJournal = func(string) error { return domain.ErrConfig }
	output, _, err := executeCLIRaw(t, nil, "broker", "journal", "initialize", "--config", "private-broker.json")
	if output != "" || !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("initializer failure reported success: output=%q error=%v", output, err)
	}
}
