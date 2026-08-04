//go:build !windows

package agenteval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBoundedCommandTerminatesDescendantProcessTree(t *testing.T) {
	root := privateSyntheticScratch(t)
	marker := filepath.Join(root, "child.pid")
	binary := filepath.Join(root, "atl-fake")
	writeSyntheticExecutable(t, binary, "#!/bin/sh\n/bin/sleep 30 &\nprintf '%s\\n' \"$!\" > \""+marker+"\"\nwait\n")

	_, err := executeBoundedCommand(
		context.Background(), binary, nil, root, nil,
		100*time.Millisecond, 64, 64,
	)
	if err == nil || !strings.Contains(err.Error(), "did not complete") {
		t.Fatalf("bounded command error=%v", err)
	}
	assertSyntheticProcessGone(t, marker)
}

func TestBoundedMCPCommandTerminatesDescendantProcessTree(t *testing.T) {
	root := privateSyntheticScratch(t)
	marker := filepath.Join(root, "child.pid")
	binary := filepath.Join(root, "atl-fake")
	writeSyntheticExecutable(t, binary, "#!/bin/sh\n/bin/sleep 30 &\nprintf '%s\\n' \"$!\" > \""+marker+"\"\nwait\n")

	process, err := startBoundedMCPCommand(
		context.Background(), binary, []string{"mcp", "serve"}, root, nil,
		100*time.Millisecond, 512, 64,
	)
	if process != nil {
		_ = process.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("bounded MCP start error=%v", err)
	}
	assertSyntheticProcessGone(t, marker)
}

func assertSyntheticProcessGone(t *testing.T, marker string) {
	t.Helper()
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read descendant PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid < 1 {
		t.Fatalf("invalid descendant PID %q: %v", data, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant process %d survived tree cleanup: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
