//go:build !windows

package brokerserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSelectedAttachmentCLIHostShutdownCancelsSourceAndPreservesDestination(t *testing.T) {
	binary := buildSelectedATLBinary(t, "")
	fixture := newAttachmentCLIProcessFixture(t, []byte("synthetic body"))
	fixture.chain.sourceMode = "blocked"
	fixture.chain.bodyEntered = make(chan struct{})
	fixture.chain.bodyCancelled = make(chan struct{})
	destination := t.TempDir()
	target := filepath.Join(destination, "example.bin")
	prior := []byte("prior-owned-content")
	projectPageProcessWriteFile(t, target, prior)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "jira", "issue", "attachment", "get", "PROJ-1", "--id", "7", "--into", destination)
	command.Env = fixture.environment
	command.WaitDelay = 2 * time.Second
	var stdout, stderr selectedCacheCLIOutput
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-fixture.chain.bodyEntered:
	case err := <-done:
		t.Fatalf("CLI ended before source read: %v", err)
	case <-ctx.Done():
		<-done
		t.Fatal("source read did not start")
	}
	fixture.stop()
	select {
	case err := <-done:
		if err == nil || ctx.Err() != nil || stdout.exceeded || stderr.exceeded {
			t.Fatalf("shutdown result=%v context=%v output bounds=%t/%t", err, ctx.Err(), stdout.exceeded, stderr.exceeded)
		}
	case <-ctx.Done():
		<-done
		t.Fatal("CLI did not terminate after Host shutdown")
	}
	select {
	case <-fixture.chain.bodyCancelled:
	case <-ctx.Done():
		t.Fatal("Host shutdown did not cancel the source body")
	}
	actual, readErr := os.ReadFile(target)
	entries, listErr := os.ReadDir(destination)
	if readErr != nil || listErr != nil || !bytes.Equal(actual, prior) || len(entries) != 1 || entries[0].Name() != "example.bin" {
		t.Fatalf("shutdown changed destination: read=%v list=%v entries=%d", readErr, listErr, len(entries))
	}
	if len(fixture.chain.handler.permits) != 0 || len(fixture.chain.handler.attachmentPermits) != 0 || fixture.directCalls.Load() != 0 {
		t.Fatal("shutdown leaked a permit or caused direct fallback")
	}
	fixture.chain.mu.Lock()
	if fixture.chain.counts["body"] != 1 || fixture.chain.violations != 0 {
		t.Errorf("counts=%v violations=%d", fixture.chain.counts, fixture.chain.violations)
	}
	fixture.chain.mu.Unlock()
	decoder := json.NewDecoder(bytes.NewReader(fixture.audit.Bytes()))
	streamEvents := 0
	for {
		var event AuditEvent
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil || !validAuditEvent(event) {
			t.Fatalf("audit decode=%v", err)
		}
		if event.Route == "data_execute_v3" {
			streamEvents++
			if event.Outcome == "success" {
				t.Error("canceled stream was audited as success")
			}
		}
	}
	if streamEvents != 1 {
		t.Errorf("stream audits=%d", streamEvents)
	}
}
