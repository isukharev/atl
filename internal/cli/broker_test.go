package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/domain"
)

type brokerRunnerStub struct {
	runs   int
	closed int
	err    error
}

func (r *brokerRunnerStub) Run(context.Context) error { r.runs++; return r.err }
func (r *brokerRunnerStub) Close()                    { r.closed++ }

func TestBrokerServeRequiresOneExplicitConfig(t *testing.T) {
	for _, args := range [][]string{{"broker", "serve"}, {"broker", "serve", "--config", "one", "--config", "two"}, {"broker", "serve", "--config", " spaced "}} {
		if output, code := runCLI(t, nil, args...); code != exitUsage || output != "" {
			t.Fatalf("args=%v code=%d output=%q", args, code, output)
		}
	}
}

func TestBrokerServeSkipsOrdinaryConfigAndClosesRuntime(t *testing.T) {
	previous := loadBrokerRuntime
	defer func() { loadBrokerRuntime = previous }()
	runner := &brokerRunnerStub{}
	var loadedPath string
	loadBrokerRuntime = func(path, _ string, _ *cobra.Command) (brokerRunner, error) {
		loadedPath = path
		return runner, nil
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "config.json"), []byte("}{"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runCLIFull(t, map[string]string{"ATL_CONFIG_DIR": directory}, "broker", "serve", "--config", "broker.json")
	if code != exitOK || loadedPath != "broker.json" || runner.runs != 1 || runner.closed != 1 || stderr != "" || !strings.Contains(stdout, `"status": "stopped"`) {
		t.Fatalf("code=%d path=%q runner=%+v stdout=%q stderr=%q", code, loadedPath, runner, stdout, stderr)
	}
}

func TestBrokerServePreservesClosedRuntimeError(t *testing.T) {
	previous := loadBrokerRuntime
	defer func() { loadBrokerRuntime = previous }()
	runner := &brokerRunnerStub{err: domain.ErrCheckFailed}
	loadBrokerRuntime = func(string, string, *cobra.Command) (brokerRunner, error) { return runner, nil }
	stdout, _, err := executeCLIRaw(t, nil, "broker", "serve", "--config", "broker.json")
	if stdout != "" || !errors.Is(err, domain.ErrCheckFailed) || runner.runs != 1 || runner.closed != 1 {
		t.Fatalf("stdout=%q err=%v runner=%+v", stdout, err, runner)
	}
}
