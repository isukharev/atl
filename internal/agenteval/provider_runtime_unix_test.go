//go:build !windows

package agenteval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestProvisionCodexBenchmarkPluginRejectsPackageFIFOBeforeProviderCommand(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"synthetic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	session, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + codexHome})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	capsule, err := newCodexProviderRuntime(scratch, session)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = capsule.Close() }()
	pluginRoot := filepath.Join(t.TempDir(), "plugin")
	writeTestPluginTrees(t, pluginRoot, "0.4.0", "Synthetic plugin.")
	mcpPath := filepath.Join(pluginRoot, "plugins", "atl", ".mcp.json")
	if err := os.Remove(mcpPath); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(mcpPath, 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "provider-called")
	binary := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf called >"+shellQuote(marker)+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err = provisionCodexBenchmarkPlugin(context.Background(), binary, pluginRoot, capsule)
	if err == nil || strings.Contains(err.Error(), mcpPath) {
		t.Fatalf("package FIFO result: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("provider command ran before package validation: %v", err)
	}
}
