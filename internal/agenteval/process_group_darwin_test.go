//go:build darwin

package agenteval

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestDarwinZombieOnlyProcessGroupSignal(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestDarwinExitingProcessHelper$")
	tree, err := prepareProcessTree(command)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tree.close() }()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := false
	defer func() {
		if !reaped {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	memberCount := 0
	var inspectErr error
	for {
		members, err := inspectDarwinProcessGroup(int32(command.Process.Pid))
		memberCount = len(members)
		inspectErr = err
		if err == nil && len(members) > 0 && processGroupExhausted(int32(command.Process.Pid), members) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("zombie-only process group was not observed: members=%d query error=%v", memberCount, inspectErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("zombie-only group signal error=%v want=%v", err, syscall.EPERM)
	}
	if err := tree.kill(); err != nil {
		t.Fatalf("normalized zombie-only group signal error=%v", err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("reap zombie-only group leader: %v", err)
	}
	reaped = true
}

func TestDarwinExitingProcessHelper(*testing.T) {}
