//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	jsonHelperMode = "ATL_AGENT_EVAL_RACE_JSON_HELPER"
	jsonHelperPID  = "ATL_AGENT_EVAL_RACE_JSON_HELPER_PID"
	jsonHelperCase = "ATL_AGENT_EVAL_RACE_JSON_HELPER_CASE"
)

func TestMain(m *testing.M) {
	if os.Getenv(jsonHelperMode) != "1" {
		os.Exit(m.Run())
	}
	os.Exit(runJSONHelper())
}

func runJSONHelper() int {
	child := exec.Command("/bin/sh", "-c", "sleep 60")
	if err := child.Start(); err != nil {
		return 2
	}
	if err := os.WriteFile(os.Getenv(jsonHelperPID), []byte(fmt.Sprintf("%d\n", child.Process.Pid)), 0o600); err != nil {
		_ = child.Process.Kill()
		return 2
	}
	switch os.Getenv(jsonHelperCase) {
	case "malformed":
		fmt.Fprintln(os.Stdout, "{")
	case "oversized":
		fmt.Fprintln(os.Stdout, strings.Repeat("x", maxJSONLine+1))
	case "consume":
		if err := json.NewEncoder(os.Stdout).Encode(goEvent{Action: "start", Package: "example.test/helper"}); err != nil {
			_ = child.Process.Kill()
			return 2
		}
	default:
		_ = child.Process.Kill()
		return 2
	}
	_ = child.Wait()
	return 0
}

func TestLinuxProcessGroupIsKilledOnTimeout(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	commandDir := t.TempDir()
	go func() {
		done <- runCommand(ctx, commandSpec{
			dir: commandDir, name: "/bin/sh", env: os.Environ(),
			args: []string{"-c", `sleep 60 & child=$!; printf '%s\n' "$child" > "$1"; wait`, "sh", pidPath},
		}, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	pid := waitForChildPID(t, pidPath)
	cancel()
	select {
	case err := <-done:
		if err == nil || ctx.Err() == nil {
			t.Fatalf("err=%v context=%v", err, ctx.Err())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owned process group did not stop after cancellation")
	}
	requireChildGone(t, pid)
}

func TestRunJSONProcessKillsDescendantsOnParserRejection(t *testing.T) {
	for _, scenario := range []string{"malformed", "oversized", "consume"} {
		t.Run(scenario, func(t *testing.T) {
			pidPath := filepath.Join(t.TempDir(), "child.pid")
			env := append(os.Environ(), jsonHelperMode+"=1", jsonHelperPID+"="+pidPath, jsonHelperCase+"="+scenario)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			started := time.Now()
			err := runJSONProcess(ctx, commandSpec{dir: t.TempDir(), name: os.Args[0], env: env}, func(goEvent) error {
				if scenario == "consume" {
					return errors.New("fixture consume rejection")
				}
				return nil
			}, newTailBuffer(maxFailureOutput), newTailBuffer(maxFailureOutput))
			if err == nil {
				t.Fatal("parser rejection succeeded")
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("parser rejection waited for outer timeout instead of canceling its process group")
			}
			requireChildGone(t, waitForChildPID(t, pidPath))
		})
	}
}

func waitForChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		var pid int
		if err == nil {
			if _, err := fmt.Sscanf(string(body), "%d", &pid); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper did not publish its child-process handshake")
	return 0
}

func requireChildGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned child survived process-group cancellation")
}
