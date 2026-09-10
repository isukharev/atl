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
	checkAttachmentCLISourceCancellation(t, true)
}

func TestSelectedAttachmentCLIOperationDeadlineCancelsSlowSource(t *testing.T) {
	checkAttachmentCLISourceCancellation(t, false)
}

func checkAttachmentCLISourceCancellation(t *testing.T, stopHost bool) {
	t.Helper()
	binary := buildSelectedATLBinary(t, "")
	payload := []byte("synthetic body")
	if !stopHost {
		payload = bytes.Repeat([]byte{'x'}, 2<<20)
	}
	fixture := newAttachmentCLIProcessFixture(t, payload)
	fixture.chain.sourceMode = "blocked"
	if !stopHost {
		fixture.chain.sourceMode = "partial blocked"
	}
	fixture.chain.bodyEntered = make(chan struct{})
	fixture.chain.bodyCancelled = make(chan struct{})
	destination := t.TempDir()
	target := filepath.Join(destination, "example.bin")
	prior := []byte("prior-owned-content")
	projectPageProcessWriteFile(t, target, prior)
	processBound := 20 * time.Second
	if !stopHost {
		processBound = 75 * time.Second
	}
	ctx, cancel := context.WithTimeout(t.Context(), processBound)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "jira", "issue", "attachment", "get", "PROJ-1", "--id", "7", "--into", destination)
	command.Env = fixture.environment
	command.WaitDelay = 2 * time.Second
	var stdout, stderr selectedCacheCLIOutput
	command.Stdout, command.Stderr = &stdout, &stderr
	started := time.Now()
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
	if stopHost {
		fixture.stop()
	}
	select {
	case err := <-done:
		if err == nil || ctx.Err() != nil || stdout.exceeded || stderr.exceeded {
			t.Fatalf("cancellation result=%v context=%v output bounds=%t/%t", err, ctx.Err(), stdout.exceeded, stderr.exceeded)
		}
	case <-ctx.Done():
		<-done
		t.Fatal("CLI did not terminate within the cancellation bound")
	}
	if !stopHost && time.Since(started) < 55*time.Second {
		t.Fatal("slow source failed before the ordinary operation deadline")
	}
	select {
	case <-fixture.chain.bodyCancelled:
	case <-ctx.Done():
		t.Fatal("operation cancellation did not cancel the source body")
	}
	fixture.stop()
	actual, readErr := os.ReadFile(target)
	entries, listErr := os.ReadDir(destination)
	if readErr != nil || listErr != nil || !bytes.Equal(actual, prior) || len(entries) != 1 || entries[0].Name() != "example.bin" {
		t.Fatalf("shutdown changed destination: read=%v list=%v entries=%d", readErr, listErr, len(entries))
	}
	if len(fixture.chain.handler.permits) != 0 || len(fixture.chain.handler.attachmentPermits) != 0 || fixture.directCalls.Load() != 0 {
		t.Fatal("shutdown leaked a permit or caused direct fallback")
	}
	fixture.chain.mu.Lock()
	wantReleases := 0
	if !stopHost {
		wantReleases = 1
	}
	if fixture.chain.counts["body"] != 1 || fixture.chain.counts["operation_release"] != wantReleases || fixture.chain.violations != 0 {
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
