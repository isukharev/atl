//go:build !windows

package brokerserver

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSelectedAttachmentCLIScopeRefusalPrecedesJiraIO(t *testing.T) {
	binary := buildSelectedATLBinary(t, "")
	for _, test := range []struct {
		name, issue, attachment string
		admissions              int
	}{
		{name: "sibling issue", issue: "PROJ-2", attachment: "7", admissions: 1},
		{name: "sibling attachment", issue: "PROJ-1", attachment: "8", admissions: 1},
		{name: "filename selector", issue: "PROJ-1", attachment: "example.bin"},
		{name: "noncanonical id", issue: "PROJ-1", attachment: "07"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttachmentCLIProcessFixture(t, []byte("synthetic body"))
			destination := t.TempDir()
			target := filepath.Join(destination, "example.bin")
			prior := []byte("prior-owned-content")
			projectPageProcessWriteFile(t, target, prior)
			stdout, _, err := runAttachmentSelectedCLISelector(t, binary, fixture.environment, destination, test.issue, test.attachment)
			fixture.stop()
			if err == nil || stdout != "" {
				t.Fatal("unsupported selector returned success")
			}
			fixture.chain.mu.Lock()
			counts := fixture.chain.counts
			if counts["admission"] != test.admissions || counts["authentication"] != 3*test.admissions || counts["discovery"] != test.admissions || counts["metadata"] != 0 || counts["body"] != 0 || fixture.chain.violations != 0 {
				t.Errorf("scope refusal counts=%v violations=%d", counts, fixture.chain.violations)
			}
			fixture.chain.mu.Unlock()
			actual, readErr := os.ReadFile(target)
			entries, listErr := os.ReadDir(destination)
			if readErr != nil || listErr != nil || !bytes.Equal(actual, prior) || len(entries) != 1 || entries[0].Name() != "example.bin" || fixture.directCalls.Load() != 0 {
				t.Fatalf("scope refusal changed destination or used direct Jira: read=%v list=%v", readErr, listErr)
			}
		})
	}
}
